package trader

import (
	"strings"

	"nofx/logger"
)

// Plan-tier cancel guard.
//
// 需求(用户 2026-07-29):「不应该重挂,应该始终保持存在」—— 一张仍然属于当前
// 保护计划的委托,任何情况下都不能被撤。此前的行为不是这样:
// protection_reconciler.go 的 coverage-complete 分支会 `canceling N stale duplicate
// protection orders directly (… no re-place)`,而"stale"的判定来自分类器。分类器一旦
// 误判(旧实现按价格匹配,0.2% 容差下 342 份线上快照里 75.7% 至少有一对档位互撞),
// 一张活的计划档位就会被当成重复单撤掉,并且这条分支明确不补挂 —— ZECUSDT 的 TP1
// 就是这样在挂上 5 分钟后消失,之后再也没有出现,而价格随后穿过了它的价位。
//
// 角色感知匹配(consumeAllowedProtectionSlot)修的是误判的**成因**;这里修的是
// **后果**:撤单前再按价格核对一次当前计划,凡是还能对上任一计划档位价位的委托
// 一律从撤单集合里剔除。两层是有意冗余的 —— 分类是一整条推理链(角色解码、额度、
// 认领集合、动态归属),而"这张单还在计划里"是个不依赖那条链的单点事实。任何未来
// 改动只要让分类再次出错,这道闸门仍然拦得住。
//
// 注意这道闸门不判断委托的**数量**是否正确。数量不符要走 protection_qty_resize 的
// 缩放路径,而不是撤掉再挂 —— 撤掉会留下真空窗口,这正是本次要消除的东西。
func planTierPrices(plan *ProtectionPlan) []float64 {
	if plan == nil {
		return nil
	}
	prices := make([]float64, 0, len(plan.StopLossOrders)+len(plan.TakeProfitOrders)+4)
	add := func(p float64) {
		if p > 0 {
			prices = append(prices, p)
		}
	}
	for _, t := range plan.StopLossOrders {
		add(t.Price)
	}
	for _, t := range plan.TakeProfitOrders {
		add(t.Price)
	}
	if plan.NeedsStopLoss {
		add(plan.StopLossPrice)
	}
	if plan.NeedsTakeProfit {
		add(plan.TakeProfitPrice)
	}
	add(plan.FallbackMaxLossPrice)
	// 容忍名单同样算"计划内":AllowedExtra* 表示"这个价位是预期的,只是不由
	// reconciler 挂",它们被撤掉的后果和撤掉正式档位一样(锚点掉档的 TP 一撤,
	// 推断就变成了事实,计划每轮震荡)。
	for _, p := range plan.AllowedExtraStopPrices {
		add(p)
	}
	for _, p := range plan.AllowedExtraTakeProfitPrices {
		add(p)
	}
	return prices
}

// bareOrderID 去掉撤单集合里的 _sl/_tp 后缀(见 cancelUnexpectedProtectionOrdersByID),
// OpenOrder.OrderID 不带后缀,两侧要在同一命名空间下比对。
func bareOrderID(id string) string {
	return strings.TrimSuffix(strings.TrimSuffix(id, "_sl"), "_tp")
}

func orderTriggerPrice(o OpenOrder) float64 {
	if o.StopPrice > 0 {
		return o.StopPrice
	}
	return o.Price
}

// filterCancelIDsAgainstPlan 从待撤集合中剔除「撤掉就会让某个计划档位失去覆盖」的委托。
// 返回(保留待撤的 id, 被闸门救下的 id)。
//
// 判据不是"价位在计划里",而是"这张单是该档位**唯一**的覆盖"。区别很重要:
//
//	同价位重复单是真实存在的一类垃圾 —— TP 成交把仓位缩小后,旧的整仓数量止损会和
//	新挂的余量止损停在同一个价位上(这条 coverage-complete 分支的原注释就是在讲它)。
//	如果只看"价位在计划里",两张都会被豁免,垃圾单永久驻留、告警每轮重复,而档位其实
//	早就有人覆盖了。
//
// 所以先看**不在撤单集合里的存活单**能覆盖哪些档位:能覆盖的档位不需要保护,撤掉候选
// 不产生真空;剩下没人覆盖的档位,才从候选里救一张回来。这样"档位始终有单在场"这个
// 保证是精确的,而不是顺带把所有同价位的单都变成不可撤。
func filterCancelIDsAgainstPlan(orderIDs []string, openOrders []OpenOrder, plan *ProtectionPlan) (keepCancel []string, spared []string) {
	if len(orderIDs) == 0 {
		return orderIDs, nil
	}
	prices := planTierPrices(plan)
	if len(prices) == 0 {
		return orderIDs, nil
	}

	cancelSet := make(map[string]bool, len(orderIDs))
	for _, id := range orderIDs {
		cancelSet[bareOrderID(id)] = true
	}

	priceByID := make(map[string]float64, len(openOrders))
	// survivors 是不在撤单集合里的存活委托价位,用来先行覆盖计划档位。
	survivors := make([]float64, 0, len(openOrders))
	for _, o := range openOrders {
		p := orderTriggerPrice(o)
		if o.OrderID == "" || p <= 0 {
			continue
		}
		id := bareOrderID(o.OrderID)
		priceByID[id] = p
		if !cancelSet[id] {
			survivors = append(survivors, p)
		}
	}

	// 每个计划档位最多被一张存活单认领(一对一,避免一张单声称覆盖了两档)。
	survivorUsed := make([]bool, len(survivors))
	needsCover := make([]bool, len(prices))
	for i, want := range prices {
		covered := false
		for j, sp := range survivors {
			if survivorUsed[j] {
				continue
			}
			if approximatelyEqualPrice(sp, want) {
				survivorUsed[j] = true
				covered = true
				break
			}
		}
		needsCover[i] = !covered
	}

	keepCancel = make([]string, 0, len(orderIDs))
	for _, id := range orderIDs {
		p, found := priceByID[bareOrderID(id)]
		if !found {
			// 查不到价位就不豁免 —— 闸门只在有确证时拦,不靠猜。否则一份失效的
			// openOrders 快照会让所有清理停摆,真正的垃圾单无限堆积。
			keepCancel = append(keepCancel, id)
			continue
		}
		rescued := false
		for i, want := range prices {
			if !needsCover[i] {
				continue
			}
			if approximatelyEqualPrice(p, want) {
				needsCover[i] = false // 这一档已由本张单救回,不再救第二张
				rescued = true
				break
			}
		}
		if rescued {
			spared = append(spared, id)
			continue
		}
		keepCancel = append(keepCancel, id)
	}
	return keepCancel, spared
}

// subtractOrderIDs 返回 ids 中不在 exclude 里的部分,按裸 id(去掉 _sl/_tp 后缀)比对。
// 用于撤单后的收尾核验:被闸门救下的单必然还在,不能算"没清理干净"。
func subtractOrderIDs(ids, exclude []string) []string {
	if len(ids) == 0 || len(exclude) == 0 {
		return ids
	}
	skip := make(map[string]bool, len(exclude))
	for _, id := range exclude {
		skip[bareOrderID(id)] = true
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !skip[bareOrderID(id)] {
			out = append(out, id)
		}
	}
	return out
}

// registerDroppedTiersAsTolerated 把「原计划里有、收窄后没了、但交易所上还挂着」的
// 档位价位登记进 narrowed 的容忍名单。
//
// 三个条件必须同时满足才登记:
//   - 原计划有这个档位(不是凭空造一个容忍价位);
//   - 收窄后的计划没有它(确实被门禁滤掉了);
//   - 交易所上**存在**一张该价位的在场单(只保护真实存在的单,不给幽灵价位开后门)。
//
// 不满足第三条时不登记:没有在场单意味着没有什么可保护,把价位塞进容忍名单只会让
// 未来某张恰好落在这个价位的陌生单被无条件放过。
func registerDroppedTiersAsTolerated(original, narrowed *ProtectionPlan, openOrders []OpenOrder, positionSide string) {
	if original == nil || narrowed == nil {
		return
	}
	livePrice := func(wantTP bool, price float64) bool {
		for _, o := range openOrders {
			if positionSide != "" && o.PositionSide != "" && !strings.EqualFold(o.PositionSide, positionSide) {
				continue
			}
			if strings.Contains(strings.ToUpper(o.Type), "TRAILING") {
				continue
			}
			if wantTP != looksLikeTakeProfit(o) {
				continue
			}
			if approximatelyEqualPrice(orderTriggerPrice(o), price) {
				return true
			}
		}
		return false
	}
	stillPlanned := func(tiers []ProtectionOrder, price float64) bool {
		for _, t := range tiers {
			if approximatelyEqualPrice(t.Price, price) {
				return true
			}
		}
		return false
	}
	alreadyTolerated := func(list []float64, price float64) bool {
		for _, p := range list {
			if approximatelyEqualPrice(p, price) {
				return true
			}
		}
		return false
	}

	for _, tier := range original.TakeProfitOrders {
		if tier.Price <= 0 || stillPlanned(narrowed.TakeProfitOrders, tier.Price) {
			continue
		}
		if !livePrice(true, tier.Price) || alreadyTolerated(narrowed.AllowedExtraTakeProfitPrices, tier.Price) {
			continue
		}
		narrowed.AllowedExtraTakeProfitPrices = append(narrowed.AllowedExtraTakeProfitPrices, tier.Price)
		logger.Warnf("🧷 Protection plan narrowing: take-profit tier @%.6f was dropped as non-executable but is LIVE on the exchange — tolerating it instead of cancelling (kept, not re-placed)", tier.Price)
	}
	for _, tier := range original.StopLossOrders {
		if tier.Price <= 0 || stillPlanned(narrowed.StopLossOrders, tier.Price) {
			continue
		}
		if !livePrice(false, tier.Price) || alreadyTolerated(narrowed.AllowedExtraStopPrices, tier.Price) {
			continue
		}
		narrowed.AllowedExtraStopPrices = append(narrowed.AllowedExtraStopPrices, tier.Price)
		logger.Warnf("🧷 Protection plan narrowing: stop tier @%.6f was dropped as non-executable but is LIVE on the exchange — tolerating it instead of cancelling (kept, not re-placed)", tier.Price)
	}
}
