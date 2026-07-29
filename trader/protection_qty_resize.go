package trader

import (
	"fmt"
	"strings"

	"nofx/logger"
	tradertypes "nofx/trader/types"
)

// 加仓后保护单数量不跟涨,是这套系统里一条独立于价格的漏血路径。
//
// 生产实况 (2026-07-29, GPT/SKHYNIXUSDT SHORT): 仓位经 0.013→0.017→0.022→0.028→0.035
// 逐次加仓,而交易所侧止损单还停在下单那一刻的量(0.017/0.013)。
// detectMissingProtection 判"在场"只比**价格**(hasMatchingProtectionOrder /
// countMatchingProtectionOrders 全是价格谓词),所以一张 0.017 的止损单在 0.035 的仓位上
// 照样报 missingSL=false —— 档位在账面上"有",止血能力却只有一半。
//
// 为什么不能靠"每次加仓重挂一遍"解决:那正是 2026-07-27 单子摞叠事故的成因
// (BN CLUSDT 一个仓位上摞出 2.43+0.73+1.22 三张 trailing)。仓库为此定的规矩是
// "按单号解析自己的单、不churn",代价就是数量陈旧。
//
// 所以这里走第三条路:**只撤,不挂**。撤掉量不足的那张之后,该档位对既有的
// detectMissingProtection 自然变成"缺失",既有修复路径(placeAndVerifyProtectionPlan-
// WithRetry)就会按**当前**仓位量重挂一张。一行下单代码都不新增,检测器语义不动,
// 修复路径也不用知道 resize 这回事。
//
// 三条自我约束,每条都是为了不把已经修掉的 churn 请回来:
//  1. **只管不足,不管超出。** 超量止损单在 reduce-only 下无害(平仓量被剩余仓位夹住),
//     而 TP 部分成交缩仓后留下的大单恰恰就是超量 —— 去动它就会和
//     protection_reconciler.go:318 那条"多一张止损只是多一层保险"的容忍策略对打。
//  2. **量化后再比。** 复用 protectionQuantitiesEquivalent + 场内 FormatQuantity。
//     ZECUSDT 那次 25 轮 place/cancel 的教训:拿量化前的意图去比量化后的事实,
//     没有任何固定容差是对的(窄档位量化误差 30%,宽档位 0.5%)。
//  3. **量未知一律放过。** 沿用 fullTrailingAdoptionMinCoverage 那条注释立的规矩:
//     "unknown must not mean inadequate" —— 有些场内不报单量。
//
// 刻意不覆盖 trailing/DD 档:它们有自己的归属与熔断机制(deliberateImmediateTrail /
// reArmBreaker),从这里去撤会绕过那套账本。BE 档另有 replaceStaleBreakEvenTierOrders
// 按档撤单,数量不足并进那条判据(见 break_even_tier_replace.go)。

// undercoveredProtectionOrder 是一张价格对得上、但量不足以兑现该档意图的保护单。
type undercoveredProtectionOrder struct {
	OrderID  string
	Price    float64
	LiveQty  float64
	WantQty  float64
	Kind     string // ladder_sl / ladder_tp / full_sl / full_tp / fallback_sl
	IsProfit bool
}

func (u undercoveredProtectionOrder) String() string {
	return fmt.Sprintf("%s@%.6f live=%.8f want=%.8f id=%s", u.Kind, u.Price, u.LiveQty, u.WantQty, u.OrderID)
}

// protectionQtyTarget 是"某个价位上应该有多少量"的规范化意图。
type protectionQtyTarget struct {
	Price    float64
	WantQty  float64
	Kind     string
	IsProfit bool
}

// protectionQtyTargetsForPlan 把 plan 摊平成 (价位, 应有量) 列表。
// 量的算法必须和下单路径逐字一致(placeAndVerifyLadderProtection 里
// `quantity * CloseRatioPct / 100`),否则检测和下单会各算一套,永远收敛不了。
func protectionQtyTargetsForPlan(plan *ProtectionPlan, positionQty float64) []protectionQtyTarget {
	if plan == nil || positionQty <= 0 {
		return nil
	}
	var targets []protectionQtyTarget

	hasLadderSL := len(plan.StopLossOrders) > 0
	hasLadderTP := len(plan.TakeProfitOrders) > 0

	for _, order := range plan.StopLossOrders {
		qty := positionQty * order.CloseRatioPct / 100.0
		if order.Price > 0 && qty > 0 {
			targets = append(targets, protectionQtyTarget{Price: order.Price, WantQty: qty, Kind: "ladder_sl"})
		}
	}
	for _, order := range plan.TakeProfitOrders {
		qty := positionQty * order.CloseRatioPct / 100.0
		if order.Price > 0 && qty > 0 {
			targets = append(targets, protectionQtyTarget{Price: order.Price, WantQty: qty, Kind: "ladder_tp", IsProfit: true})
		}
	}
	// 全量 SL/TP 只在没有对应阶梯时才由 plan 实际下单(见 placeAndVerifyProtectionPlan
	// 的 fullStop/fullTP 判据),这里的条件必须与之一致,否则会去撤一张 plan 根本不打算
	// 维护的单。
	if plan.NeedsStopLoss && plan.StopLossPrice > 0 && !hasLadderSL {
		targets = append(targets, protectionQtyTarget{Price: plan.StopLossPrice, WantQty: positionQty, Kind: "full_sl"})
	}
	if plan.NeedsTakeProfit && plan.TakeProfitPrice > 0 && !hasLadderTP {
		targets = append(targets, protectionQtyTarget{Price: plan.TakeProfitPrice, WantQty: positionQty, Kind: "full_tp", IsProfit: true})
	}
	if plan.FallbackMaxLossPrice > 0 {
		targets = append(targets, protectionQtyTarget{Price: plan.FallbackMaxLossPrice, WantQty: positionQty, Kind: "fallback_sl"})
	}
	return targets
}

// findUndercoveredProtectionOrders 找出价格在场但量不足的静态保护单。
//
// 判据刻意保守:同价位所有同向单的**合计**量达标就算达标(阶梯档被拆成两张各半的单
// 也算覆盖),只有合计仍不足才报。这样"部分成交后补挂了一张小单"这种正常中间态
// 不会被误判。
func findUndercoveredProtectionOrders(
	plan *ProtectionPlan,
	openOrders []tradertypes.OpenOrder,
	positionSide string,
	positionQty float64,
	quantize protectionQtyQuantizer,
	symbol string,
	beOwnedOrderIDs map[string]struct{},
) []undercoveredProtectionOrder {
	targets := protectionQtyTargetsForPlan(plan, positionQty)
	if len(targets) == 0 {
		return nil
	}

	var out []undercoveredProtectionOrder
	claimed := map[string]struct{}{}

	for _, target := range targets {
		var (
			liveQty   float64
			candidate *tradertypes.OpenOrder
			known     = true
		)
		for i := range openOrders {
			order := openOrders[i]
			if !protectionOrderMatchesTargetPrice(order, positionSide, target.IsProfit, target.Price) {
				continue
			}
			if _, owned := beOwnedOrderIDs[order.OrderID]; owned && order.OrderID != "" {
				// 保本止损单归 BE 逐档路径(replaceStaleBreakEvenTierOrders)按档管量,
				// 那条路径有 stage/额度/认领账本。BE 单也是 STOP_MARKET,价位一旦和某个
				// plan SL/fallback 价撞进容差,这里就会把它当欠量的静态止损给撤了 ——
				// 那正是两条路径抢同一张单、一撤一挂的 churn。认领给 BE 的一律让开。
				continue
			}
			if _, taken := claimed[order.OrderID]; taken && order.OrderID != "" {
				// 一张单只能兑现一个档位的意图。两个档位价格撞在一起时(收敛到同价),
				// 第二个档位不能把同一张单再数一遍,否则会把"其实只有一张单"读成足额。
				continue
			}
			if order.Quantity <= 0 {
				// 场内不报量 → 不可判,整个档位放过(unknown ≠ inadequate)。
				known = false
				break
			}
			liveQty += order.Quantity
			if candidate == nil || order.Quantity < candidate.Quantity {
				candidate = &openOrders[i]
			}
		}
		if !known || candidate == nil || liveQty <= 0 {
			continue
		}
		if candidate.OrderID != "" {
			claimed[candidate.OrderID] = struct{}{}
		}
		// 达标 → 不动。超量 → 也不动(见文件头第 1 条)。
		if protectionQuantitiesEquivalent(quantize, symbol, target.WantQty, liveQty) || liveQty >= target.WantQty {
			continue
		}
		if candidate.OrderID == "" {
			// 撤不了(没单号)就别报:报了也只能降级成按 tag 撤,那会连坐兄弟档。
			continue
		}
		out = append(out, undercoveredProtectionOrder{
			OrderID:  candidate.OrderID,
			Price:    target.Price,
			LiveQty:  liveQty,
			WantQty:  target.WantQty,
			Kind:     target.Kind,
			IsProfit: target.IsProfit,
		})
	}
	return out
}

// protectionOrderMatchesTargetPrice 是价格+方向匹配,判据与 hasMatchingProtectionOrder
// 逐条相同(同边、同类型、StopPrice 优先回落 Price、approximatelyEqualPrice)。
// 抽出来是为了让"合计量"这条路径和"是否在场"那条路径不可能各判一套。
func protectionOrderMatchesTargetPrice(order tradertypes.OpenOrder, positionSide string, wantTakeProfit bool, targetPrice float64) bool {
	if positionSide != "" && !strings.EqualFold(order.PositionSide, positionSide) && order.PositionSide != "" {
		return false
	}
	if wantTakeProfit {
		if !looksLikeTakeProfit(order) {
			return false
		}
	} else {
		if !looksLikeStopLoss(order) {
			return false
		}
	}
	// trailing 单有自己的归属账本,不归这里管(见文件头)。
	if strings.Contains(strings.ToUpper(order.Type), "TRAILING") {
		return false
	}
	price := order.StopPrice
	if price <= 0 {
		price = order.Price
	}
	if price <= 0 {
		return false
	}
	return approximatelyEqualPrice(price, targetPrice)
}

// resizeUndercoveredProtectionOrders 撤掉量不足的静态保护单,返回撤成功的张数。
// 调用方在返回 >0 时必须重取 openOrders —— 撤掉之后该档位才会被
// detectMissingProtection 判成缺失并由既有修复路径按当前仓位量重挂。
func (at *AutoTrader) resizeUndercoveredProtectionOrders(
	symbol, side, positionSide string,
	positionQty float64,
	plan *ProtectionPlan,
	openOrders []tradertypes.OpenOrder,
) int {
	if at == nil || plan == nil || positionQty <= 0 || len(openOrders) == 0 {
		return 0
	}
	// BE 逐档路径按档管自己那些止损单的量,这里别插手,否则两条路径会抢同一张单。
	beOwned, _ := at.claimedBreakEvenOrderIDsForPosition(symbol, side)
	stale := findUndercoveredProtectionOrders(plan, openOrders, positionSide, positionQty, at.protectionQtyQuantizerFor(), symbol, beOwned)
	if len(stale) == 0 {
		return 0
	}
	cancelled := 0
	for _, order := range stale {
		logger.Warnf("📐 Protection qty resize: %s %s %s order %s covers %.8f of %.8f (%.1f%%) after position grew — canceling so the plan re-places it at full size",
			symbol, positionSide, order.Kind, order.OrderID, order.LiveQty, order.WantQty, order.LiveQty/order.WantQty*100)
		if err := at.cancelProtectionOrderByID(symbol, order.OrderID); err != nil {
			// 撤不掉就保持现状:那张小单仍在挂,该档位仍报"在场",不会被重挂。
			// 宁可继续欠量,也不能在撤单失败后又挂一张 —— 那就是摞叠。
			logger.Warnf("⚠️ Protection qty resize: cancel %s failed (%s %s %s): %v — keeping undersized order, no re-place",
				order.OrderID, symbol, positionSide, order.Kind, err)
			continue
		}
		cancelled++
	}
	if cancelled > 0 {
		logger.Infof("📐 Protection qty resize: %s %s canceled %d undersized protection order(s); plan re-apply will restore them at position qty %.8f",
			symbol, positionSide, cancelled, positionQty)
	}
	return cancelled
}
