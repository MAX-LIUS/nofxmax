package trader

import (
	"math"
	"nofx/logger"
	"nofx/store"
	"strings"
)

// 这个文件只做一件事:在撤单之前,拦下那些"仍然是本策略某一档 DD 保护"的活单。
//
// 背景(2026-07-28 生产实况, GPT SKHYNIXUSDT short):
// 09:03:14 开仓时 dd1 正确挂上 algoId=3781523903370133504(activation=1048.5615
// callback=0.0233 qty=0.0350)。19:13:46 熔断跳闸,归属从 native_trailing 翻成
// managed_drawdown;19:14:00 —— 跳闸后 14 秒 —— reconciler 的"覆盖已完整,直接撤掉
// 多余重复单"快速路径把这张**仍然生效**的 dd1 一起撤了,理由只是它已经不在
// trailingOwnership.Claimed 里(认领记录跟着归属一起翻走了)。
//
// 用户的要求是明确的:"这个是开仓就挂好的,怎么能随便动?为什么要动?只要是没有丢失,
// 就应该一直在,如果检测丢失了,就应该补挂。"归属记录翻转是**我们自己的账本变动**,
// 不是"这张单失效了"。账本可以重写,交易所上那张正在保护仓位的单不能因此被撤。
//
// 所以这里的判据不看账本,只看单子本身的形状:activation / callback / 方向是否仍然
// 匹配某一档**当前配置生效**的 DD 规则。匹配就意味着"它正是策略要求的那张保护单",
// 于是从撤单名单里剔除,并把认领记录补回去(补挂账本,而不是撤掉实物)。
//
// 只作用于 TRAILING 类型:静态 SL/TP 的重复单清理有自己的等价性逻辑,不在此列。

// reclaimableTrailingMatch 描述一张被拦下的活单,以及它匹配到哪一档规则。
type reclaimableTrailingMatch struct {
	OrderID         string
	Rule            store.DrawdownTakeProfitRule
	RuleFingerprint string
	ActivationPrice float64
	CallbackRatio   float64
	Quantity        float64
}

// trailingOrderMatchesDrawdownRule 判断一张活的 trailing 单是否仍然是这一档 DD 的形状。
//
// 与 hasMatchingNativeTrailingOrderForRule 的模糊回退用同一套容差(activation 1%,
// callback 0.00025),故意保持一致:同一个"是不是这一档"的问题,不能在挂单侧和撤单侧
// 得出不同答案 —— 那正是"一边判缺失去补挂、一边判多余去撤掉"的对撞churn根源。
//
// 已激活(activated)的单直接算匹配:激活后交易所报的 StopPrice 是移动中的跟踪价,
// Binance 更是完全不报 callback,拿它去比对开仓时的计划值必然失配。一张已激活、
// 方向正确的 trailing 单就是这一档的活保护,绝不能因为"参数对不上"被撤。
func trailingOrderMatchesDrawdownRule(order OpenOrder, side string, entryPrice float64, rule store.DrawdownTakeProfitRule) bool {
	if !strings.Contains(strings.ToUpper(order.Type), "TRAILING") {
		return false
	}
	if order.PositionSide != "" && !strings.EqualFold(order.PositionSide, strings.ToUpper(side)) {
		return false
	}
	if rule.MinProfitPct <= 0 || rule.MaxDrawdownPct <= 0 || rule.CloseRatioPct <= 0 {
		return false
	}
	if order.ActivationStatus == "activated" {
		return true
	}

	plannedActivation := calculateProfitBasedTrailingTriggerPrice(entryPrice, side, rule.MinProfitPct)
	plannedCallback := calculateDrawdownRuleCallbackRatio(entryPrice, side, rule)

	callback := order.CallbackRate
	if callback <= 0 && order.CallbackRatePct > 0 {
		callback = order.CallbackRatePct / 100.0
	}
	if callback > 1 {
		callback = callback / 100.0
	}

	activationOK := true
	if order.StopPrice > 0 && plannedActivation > 0 {
		drift := math.Abs(order.StopPrice-plannedActivation) / math.Max(math.Abs(order.StopPrice), math.Abs(plannedActivation))
		activationOK = drift <= 0.01
	}
	// 交易所不报 callback 时(<=0)只凭 activation 匹配,而不是把"没报"当成漂移。
	callbackOK := callback <= 0 || math.Abs(callback-plannedCallback) <= 0.00025
	return activationOK && callbackOK
}

// tierTaggedRuleIndex 在已排序的规则表里定位这张单**自报**的档位。
//
// order.ProtectionTier 是适配器从 clientOrderID 解出的档位序号(见
// drawdown_tier_tag.go / store.EncodeReasonClientID),即挂单当时我们编进交易所侧
// 的档位标识。它比形状匹配精确得多:形状匹配对"已激活"的单无能为力(激活后
// StopPrice 变成移动跟踪价、Binance 干脆不报 callback),只能退回"按 MinProfitPct
// 取最低未用档",于是两档并存时可能认错档 —— 认错的后果是另一档被判缺单、多挂一张
// (过度保护,方向安全,但不是真相)。
//
// 序号语义必须与挂单侧同源:两边都用 drawdownTierTagIndex 的排序(MinProfitPct 升序,
// 次级键 MaxDrawdownPct / CloseRatioPct)。这里的 ordered 只按 MinProfitPct 排,所以
// 不能直接用下标 —— 改为按 tag 序号先还原出那一档的规则身份,再在 ordered 里找它。
//
// 返回 -1 表示这张单没带标识(旧单/非本策略单)或标识指向的档已不在当前配置里。
func (at *AutoTrader) tierTaggedRuleIndex(order OpenOrder, ordered []store.DrawdownTakeProfitRule) int {
	if order.ProtectionTier <= 0 {
		return -1
	}
	for idx := range ordered {
		if at.drawdownTierTagIndex(ordered[idx]) == order.ProtectionTier {
			return idx
		}
	}
	return -1
}

// reclaimLiveDrawdownTrailingOrders 从待撤名单里剔除仍然匹配某档 DD 规则的活 trailing 单,
// 返回(过滤后的待撤名单, 被拦下并已补回认领记录的匹配)。
//
// 认领档位的优先级:
//  1. 单子自报的档位标识(clientOrderID 里的 T<N>)—— 读出来的,不是猜的;
//  2. 形状匹配(activation / callback / 方向)+ "最低未用档"兜底 —— 旧单没有标识时。
//
// 一张单最多认领给一档:多档同时匹配时按 MinProfitPct 从低到高取第一个未被占用的档,
// 与 computeDrawdownTierAllocations 的排序一致。这样两档并存(place-at-open)时不会把
// 同一张单同时算作两档的覆盖 —— 那会让另一档看起来"已覆盖"而永远不被补挂。
func (at *AutoTrader) reclaimLiveDrawdownTrailingOrders(symbol, side string, entryPrice float64, candidateIDs []string, openOrders []OpenOrder) ([]string, []reclaimableTrailingMatch) {
	if len(candidateIDs) == 0 {
		return candidateIDs, nil
	}
	rules := at.getActiveDrawdownRulesForPosition(symbol, side)
	if len(rules) == 0 {
		return candidateIDs, nil
	}

	byID := make(map[string]OpenOrder, len(openOrders))
	for _, o := range openOrders {
		if o.OrderID != "" {
			byID[o.OrderID] = o
		}
	}

	// 规则按 MinProfitPct 升序,保证认领顺序稳定可预期。
	ordered := make([]store.DrawdownTakeProfitRule, 0, len(rules))
	for _, raw := range rules {
		r := normalizeDrawdownRule(raw)
		if r.MinProfitPct > 0 && r.MaxDrawdownPct > 0 && r.CloseRatioPct > 0 {
			ordered = append(ordered, r)
		}
	}
	for i := 0; i < len(ordered); i++ {
		for j := i + 1; j < len(ordered); j++ {
			if ordered[j].MinProfitPct < ordered[i].MinProfitPct {
				ordered[i], ordered[j] = ordered[j], ordered[i]
			}
		}
	}

	usedRule := make(map[int]bool)
	kept := make([]string, 0, len(candidateIDs))
	var matches []reclaimableTrailingMatch

	for _, id := range candidateIDs {
		order, ok := byID[id]
		if !ok {
			// 不在活单列表里(可能已成交/已撤):没有实物需要保护,交回原名单。
			kept = append(kept, id)
			continue
		}
		if !strings.Contains(strings.ToUpper(order.Type), "TRAILING") {
			kept = append(kept, id)
			continue
		}
		matchedIdx := -1
		// 第一优先:单子自报的档位标识。带标识就直接认这一档,不再用形状去猜。
		// 只在该档尚未被别的单占用时成立 —— 两张单自报同一档说明有重复挂单,第二张
		// 仍然走形状匹配/被交回撤单名单,不能让它顶掉第一张的认领。
		if taggedIdx := at.tierTaggedRuleIndex(order, ordered); taggedIdx >= 0 && !usedRule[taggedIdx] {
			matchedIdx = taggedIdx
		}
		if matchedIdx < 0 {
			for idx, rule := range ordered {
				if usedRule[idx] {
					continue
				}
				if trailingOrderMatchesDrawdownRule(order, side, entryPrice, rule) {
					matchedIdx = idx
					break
				}
			}
		}
		if matchedIdx < 0 {
			kept = append(kept, id)
			continue
		}
		usedRule[matchedIdx] = true
		rule := ordered[matchedIdx]
		fp := stableDrawdownRuleFingerprint(entryPrice, rule)
		matches = append(matches, reclaimableTrailingMatch{
			OrderID:         id,
			Rule:            rule,
			RuleFingerprint: fp,
			ActivationPrice: calculateProfitBasedTrailingTriggerPrice(entryPrice, side, rule.MinProfitPct),
			CallbackRatio:   calculateDrawdownRuleCallbackRatio(entryPrice, side, rule),
			Quantity:        order.Quantity,
		})
	}

	for _, m := range matches {
		protectionType := "native_trailing"
		if m.Rule.CloseRatioPct < 99.999 {
			protectionType = "native_partial_trailing"
		}
		logger.Warnf("🛟 Reclaimed live drawdown trailing order instead of canceling it: %s %s orderID=%s stage=%s close=%.1f%% — a live protective order is never cancelled because our ownership ledger moved; the ledger is rewritten to match the exchange",
			symbol, side, m.OrderID, m.Rule.StageName, m.Rule.CloseRatioPct)
		at.persistDynamicProtectionRecordWithDetails(symbol, side, protectionType, m.RuleFingerprint, m.Rule.CloseRatioPct, "armed", m.OrderID, m.ActivationPrice, m.CallbackRatio, m.Quantity)
		// 认领回来了就把这一档的熔断计数清零:交易所侧确实有活单在保护它,
		// 继续压着重挂只会让下一次真正丢单时无人补挂。
		at.resetReArmFail(reArmFailKey(symbol, side, m.Rule, entryPrice))
	}

	return kept, matches
}
