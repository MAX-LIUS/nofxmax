package trader

import (
	"strings"

	"nofx/logger"
	"nofx/store"
)

// ── 账本核心不变量:一张活着的保护单,只能有一条 armed 记录认领它 ─────────────────
//
// 2026-07-29 线上 (BN / WLDUSDT short, entry 0.3036) 的实况:
//
//	order 2000001319445192  ← native_trailing      close=100 act=0.292060 cb=0.022805  (对)
//	                        ← native_partial_trailing close=30 act=0.291456 cb=0.0120  (错,且更新)
//	order 2000001319445212  ← native_partial_trailing close=30 act=0.285137 cb=0.018244 (对)
//	                        ← native_trailing      close=100 act=0.296010 cb=0.0150     (错,且更新)
//
// 成因:reclaim 路径用**未 ATR 解析**的规则算 activation/callback/fingerprint(已在
// drawdown_order_reclaim.go 修掉),写出的记录 min=2.5/4.0 与 arm 侧的 3.6791/5.8865
// 在 drawdownRuleIdentity 看来"不是同一档",supersedeOlderArmedRecords 的窄匹配
// (同档 + 不同 orderID + 更旧)永远碰不到它们。于是 4 条记录抢 2 张单,每 ~20s 重写一次。
//
// 危害:档位→单的解析(storedTrailingOrderIDForRule)按 UpdatedAt 取最新 —— 而错的那条
// 恰恰更新。于是 30% 那一档会把全平档的单当成自己的;它一成交,全平档可能把自己标成
// "已执行",**全平回撤保护静默消失**。
//
// 为什么收敛必须放在 reconcile 而不是启动加载:
//   - 启动时手里没有交易所数据,唯一可用的判据是 UpdatedAt,而线上恰恰是**错的更新** ——
//     按"取最新"清扫会留下错的那条,比不清扫更糟;
//   - reconcile 每轮都拿着 openOrders,交易所是唯一的事实来源。判据用"这条记录描述的
//     形状是否匹配它认领的那张真实活单",与猜测无关。
//
// 判据顺序(全部只用手里已有的数据,不发一次额外请求):
//  1. 形状匹配:记录的 ActivationPrice / CallbackRatio 是否对得上那张活单。对得上的胜。
//  2. 档位在册:记录的档位身份是否还在该仓位当前生效的规则集里。在册的胜。
//  3. 都分不出来:保留 UpdatedAt 最新的,并打 WARN —— 明确记下"这次是按时序猜的"。

// resolveConflictingTrailingClaims 收敛"一张单多条 armed 记录"的冲突。
//
// openOrders 是本轮从交易所读到的活单(reconcile 已经在手),ordered 是该仓位当前生效的
// 规范档位集(canonicalDrawdownTierRules)。返回被退役的记录条数。
func (at *AutoTrader) resolveConflictingTrailingClaims(symbol, side string, entryPrice float64, openOrders []OpenOrder, ordered []store.DrawdownTakeProfitRule) int {
	if at == nil || at.store == nil {
		return 0
	}
	records := at.getArmedDrawdownRecordsForPosition(symbol, side, entryPrice, 0, 0)
	if len(records) < 2 {
		return 0
	}

	byOrder := make(map[string][]store.DynamicProtectionRecord)
	for _, r := range records {
		if r.ExchangeOrderID == "" {
			continue
		}
		byOrder[r.ExchangeOrderID] = append(byOrder[r.ExchangeOrderID], r)
	}

	liveByID := make(map[string]OpenOrder, len(openOrders))
	for _, o := range openOrders {
		if o.OrderID != "" {
			liveByID[o.OrderID] = o
		}
	}

	inTierSet := make(map[string]struct{}, len(ordered))
	for _, rule := range ordered {
		inTierSet[drawdownRuleIdentity(stableDrawdownRuleFingerprint(entryPrice, rule))] = struct{}{}
	}

	retired := 0
	for orderID, claims := range byOrder {
		if len(claims) < 2 {
			continue
		}
		// 只处理**跨档**冲突。同档的陈旧分叉是 supersedeOlderArmedRecords 的时序职责,
		// 在这里插手会改变既有的自愈语义。
		identities := make(map[string]struct{}, len(claims))
		for _, c := range claims {
			identities[c.ProtectionType+"|"+drawdownRuleIdentity(c.RuleFingerprint)] = struct{}{}
		}
		if len(identities) < 2 {
			continue
		}

		keep, basis := pickAuthoritativeClaim(claims, liveByID[orderID], inTierSet)
		for _, c := range claims {
			if c.Key == keep.Key {
				continue
			}
			superseded := c
			superseded.Status = "superseded"
			if err := at.store.SaveDynamicProtectionRecord(superseded); err != nil {
				logger.Warnf("⚠️ Claim conflict: failed to supersede %s claim on order %s (%s %s): %v",
					c.ProtectionType, orderID, symbol, side, err)
				continue
			}
			retired++
			logger.Warnf("🧾 Claim conflict resolved on order %s (%s %s): retired %s close=%.1f%% act=%.6f cb=%.6f — kept %s close=%.1f%% act=%.6f cb=%.6f (basis: %s). One live order must have exactly one owner, or the partial tier's fill can mark the full-close tier executed and silently drop whole-position drawdown protection.",
				orderID, symbol, side,
				c.ProtectionType, c.CloseRatioPct, c.ActivationPrice, c.CallbackRatio,
				keep.ProtectionType, keep.CloseRatioPct, keep.ActivationPrice, keep.CallbackRatio, basis)
		}
	}
	return retired
}

// pickAuthoritativeClaim 在争抢同一张单的多条记录里挑出该被保留的那条,并返回判据名。
//
// live 为零值(那张单已经不在活单列表里)时退回后两级判据 —— 单子都没了,形状无从对比。
func pickAuthoritativeClaim(claims []store.DynamicProtectionRecord, live OpenOrder, inTierSet map[string]struct{}) (store.DynamicProtectionRecord, string) {
	// 1) 形状匹配交易所上那张真单。
	if live.OrderID != "" {
		shapeHits := make([]store.DynamicProtectionRecord, 0, len(claims))
		for _, c := range claims {
			if claimMatchesLiveOrderShape(c, live) {
				shapeHits = append(shapeHits, c)
			}
		}
		if len(shapeHits) == 1 {
			return shapeHits[0], "exchange order shape"
		}
		if len(shapeHits) > 1 {
			return newestClaim(shapeHits), "exchange order shape (tie broken by recency)"
		}
	}
	// 2) 档位是否还在当前生效的规则集里。
	if len(inTierSet) > 0 {
		tierHits := make([]store.DynamicProtectionRecord, 0, len(claims))
		for _, c := range claims {
			if _, ok := inTierSet[drawdownRuleIdentity(c.RuleFingerprint)]; ok {
				tierHits = append(tierHits, c)
			}
		}
		if len(tierHits) == 1 {
			return tierHits[0], "membership in the position's current tier set"
		}
		if len(tierHits) > 1 {
			return newestClaim(tierHits), "tier-set membership (tie broken by recency)"
		}
	}
	// 3) 分不出来:按时序,并如实说明这是猜的。
	return newestClaim(claims), "recency only — NO shape or tier-set evidence available"
}

// claimMatchesLiveOrderShape 判断一条记录描述的形状是否就是这张活单。
//
// 容差与 trailingOrderMatchesDrawdownRule 同源(activation 1%,callback 0.00025):
// 同一个"是不是这一张"的问题不能在两处得出不同答案。
//
// 已激活的单不参与形状判定:激活后 StopPrice 是移动中的跟踪价,Binance 干脆不报
// callback —— 拿它去比对武装时的计划值必然失配,那会把正确的记录判成错的。返回 false
// 让调用方退到下一级判据,而不是给出一个错误的"匹配"。
func claimMatchesLiveOrderShape(c store.DynamicProtectionRecord, live OpenOrder) bool {
	if strings.EqualFold(live.ActivationStatus, "activated") {
		return false
	}
	if c.ActivationPrice <= 0 || live.StopPrice <= 0 {
		return false
	}
	denom := absFloat(live.StopPrice)
	if absFloat(c.ActivationPrice) > denom {
		denom = absFloat(c.ActivationPrice)
	}
	if denom <= 0 {
		return false
	}
	if absFloat(live.StopPrice-c.ActivationPrice)/denom > 0.01 {
		return false
	}
	// 交易所不报 callback 时只凭 activation 判定,而不是把"没报"当成漂移。
	liveCB := live.CallbackRate
	if liveCB <= 0 && live.CallbackRatePct > 0 {
		liveCB = live.CallbackRatePct / 100.0
	}
	if liveCB > 1 {
		liveCB = liveCB / 100.0
	}
	if liveCB > 0 && c.CallbackRatio > 0 && absFloat(liveCB-c.CallbackRatio) > 0.00025 {
		return false
	}
	return true
}

func newestClaim(claims []store.DynamicProtectionRecord) store.DynamicProtectionRecord {
	best := claims[0]
	for _, c := range claims[1:] {
		if c.UpdatedAt > best.UpdatedAt {
			best = c
		}
	}
	return best
}
