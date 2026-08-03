package trader

import (
	"strings"

	"nofx/store"
)

type unexpectedProtectionOrderCategory string

const (
	unexpectedCategoryStaleBotDuplicate    unexpectedProtectionOrderCategory = "stale_bot_duplicate"
	unexpectedCategoryOrphanForInactive    unexpectedProtectionOrderCategory = "orphan_for_inactive_position"
	unexpectedCategoryManualOrForeign      unexpectedProtectionOrderCategory = "manual_or_foreign"
	unexpectedCategoryExpectedDynamicOwner unexpectedProtectionOrderCategory = "expected_dynamic_owner"
	unexpectedCategoryExpectedStaticOwner  unexpectedProtectionOrderCategory = "expected_static_owner"
)

type unexpectedProtectionOrderClassification struct {
	OrderID  string
	Kind     string
	Category unexpectedProtectionOrderCategory
}

type unexpectedProtectionSummary struct {
	StaleBotDuplicate    int
	OrphanForInactive    int
	ManualOrForeign      int
	ExpectedDynamicOwner int
	ExpectedStaticOwner  int
	StaleBotDuplicateIDs []string
	OrphanForInactiveIDs []string
	ManualOrForeignIDs   []string
	// StaleTrailingDuplicate 是 StaleBotDuplicate 中属于 trailing 的部分。单独计数
	// 是因为 reconciler 有一条"止损覆盖已满足就容忍多余止损单"的分支,而 trailing
	// 单会被 detectUnexpectedProtectionOrders 归到 unexpectedStops 里 —— 多一张止损
	// 只是多一层保险,多一张部分平仓 trailing 则会真的多平仓,两者不能同等容忍。
	StaleTrailingDuplicate int
	// ExpectedDynamicOwner 的**成分拆分**。这个总数把两类完全不同的单混在一个数字里:
	// trailing(回撤止盈,归 drawdown 管)和已推到保本的止损(归 break-even 管)。
	// 混计的后果是日志读不出信息 —— 同一个 symbol 上 dynamicOwner=2 和 =3 交替出现时,
	// 无法判断是"多了一张 trailing"(真问题:会多平仓)还是"多了一张保本止损"
	// (正常:BE 推过就有)。两者都要靠翻上下文猜。这里按 Kind 拆开,让日志自解释。
	ExpectedDynamicTrailing int
	ExpectedDynamicStop     int
}

func classifyUnexpectedProtectionOrders(openOrders []OpenOrder, positionSide string, plan *ProtectionPlan, beOwnership breakEvenOwnership, trailingOwnership nativeTrailingOwnership, positionActive bool) unexpectedProtectionSummary {
	allowedStops, allowedTPs := allowedProtectionPricesForPlan(plan)
	summary := unexpectedProtectionSummary{}
	// 保本止损额度:认领集合可用时为 0(按 order id 判,不需要额度),否则等于该仓位
	// 的档位数(拿不到就是 1,即改造前行为)。额度在整个循环内共享 —— 消耗判定必须看
	// **分类结果**,不能看 looksLikeStopLoss(order):后者只做字符串匹配,而
	// TRAILING_STOP_MARKET 里含 "STOP" —— 于是第一张 trailing 就把保本额度吃掉了,
	// 真正的保本止损排在 trailing 之后时会掉到 manual_or_foreign,被当成"外来单"污染
	// ownership(交易所返回挂单的顺序不做保证,所以这是概率性误判)。
	beQuota := beOwnership.breakEvenQuota()
	for _, order := range openOrders {
		if positionSide != "" && order.PositionSide != "" && !strings.EqualFold(order.PositionSide, positionSide) {
			continue
		}
		classification := classifyProtectionOrder(order, &allowedStops, &allowedTPs, beOwnership, &beQuota, trailingOwnership, positionActive)
		switch classification.Category {
		case unexpectedCategoryStaleBotDuplicate:
			summary.StaleBotDuplicate++
			if classification.Kind == "trailing" {
				summary.StaleTrailingDuplicate++
			}
			if classification.OrderID != "" {
				summary.StaleBotDuplicateIDs = append(summary.StaleBotDuplicateIDs, classification.OrderID)
			}
		case unexpectedCategoryOrphanForInactive:
			summary.OrphanForInactive++
			if classification.OrderID != "" {
				summary.OrphanForInactiveIDs = append(summary.OrphanForInactiveIDs, classification.OrderID)
			}
		case unexpectedCategoryManualOrForeign:
			summary.ManualOrForeign++
			if classification.OrderID != "" {
				summary.ManualOrForeignIDs = append(summary.ManualOrForeignIDs, classification.OrderID)
			}
		case unexpectedCategoryExpectedDynamicOwner:
			summary.ExpectedDynamicOwner++
			switch classification.Kind {
			case "trailing":
				summary.ExpectedDynamicTrailing++
			case "stop_loss":
				summary.ExpectedDynamicStop++
			}
		case unexpectedCategoryExpectedStaticOwner:
			summary.ExpectedStaticOwner++
		}
	}
	return summary
}

// allowedProtectionSlot is one price the plan expects, together with the mechanism
// that owns it.
//
// Why the role travels with the price: matching used to be price-only with a 0.2%
// tolerance, and every tier is derived from the same ATR multiples, so tiers collide
// by construction. Measured on 342 production snapshots, 75.7% contained at least one
// pair inside that tolerance and two common pairs were byte-identical (ladder_tp vs
// managed_drawdown, ladder_sl vs structural_sl). When a live ladder_tp got consumed by
// the break-even slot it sitting next to, the TP itself no longer matched anything,
// was classified stale_bot_duplicate, and was cancelled. Carrying the role makes a
// slot refuse an order from a different mechanism, so a collision can no longer
// cross-consume.
type allowedProtectionSlot struct {
	Price float64
	// Role is the canonical mechanism (store.Mech*). Empty means "any" — used for plan
	// entries whose owning mechanism is not recorded, so behaviour degrades to the old
	// price-only match rather than rejecting a legitimate order.
	Role string
}

func allowedProtectionPricesForPlan(plan *ProtectionPlan) ([]allowedProtectionSlot, []allowedProtectionSlot) {
	allowedStops := make([]allowedProtectionSlot, 0)
	allowedTPs := make([]allowedProtectionSlot, 0)
	if plan == nil {
		return allowedStops, allowedTPs
	}
	for _, target := range plan.StopLossOrders {
		allowedStops = append(allowedStops, allowedProtectionSlot{Price: target.Price, Role: store.MechLadderSL})
	}
	if len(plan.StopLossOrders) == 0 && plan.NeedsStopLoss && plan.StopLossPrice > 0 {
		allowedStops = append(allowedStops, allowedProtectionSlot{Price: plan.StopLossPrice, Role: store.MechFullSL})
	}
	if plan.FallbackMaxLossPrice > 0 {
		allowedStops = append(allowedStops, allowedProtectionSlot{Price: plan.FallbackMaxLossPrice, Role: store.MechFallbackSL})
	}
	// Guard-owned rolling backup stop(s): tolerated so the reconciler won't churn them,
	// but intentionally NOT added to missing-detection (the guard owns placement/roll).
	// Role is structural_sl because the structural guard is what places them.
	for _, p := range plan.AllowedExtraStopPrices {
		if p > 0 {
			allowedStops = append(allowedStops, allowedProtectionSlot{Price: p, Role: store.MechStructuralSL})
		}
	}
	for _, target := range plan.TakeProfitOrders {
		allowedTPs = append(allowedTPs, allowedProtectionSlot{Price: target.Price, Role: store.MechLadderTP})
	}
	// Anchor-dropped ladder tiers: tolerated so a tier the anchor merely INFERRED as
	// executed is not canceled (which would make the inference true and oscillate the
	// plan every cycle). Like AllowedExtraStopPrices, intentionally NOT part of
	// missing-detection, so the reconciler never re-places them either.
	for _, p := range plan.AllowedExtraTakeProfitPrices {
		if p > 0 {
			allowedTPs = append(allowedTPs, allowedProtectionSlot{Price: p, Role: store.MechLadderTP})
		}
	}
	if len(plan.TakeProfitOrders) == 0 && plan.NeedsTakeProfit && plan.TakeProfitPrice > 0 {
		allowedTPs = append(allowedTPs, allowedProtectionSlot{Price: plan.TakeProfitPrice, Role: store.MechFullTP})
	}
	return allowedStops, allowedTPs
}

func classifyProtectionOrder(order OpenOrder, allowedStops, allowedTPs *[]allowedProtectionSlot, beOwnership breakEvenOwnership, beQuota *int, trailingOwnership nativeTrailingOwnership, positionActive bool) unexpectedProtectionOrderClassification {
	classification := unexpectedProtectionOrderClassification{OrderID: order.OrderID}
	upperType := strings.ToUpper(order.Type)
	if strings.Contains(upperType, "TRAILING") {
		classification.Kind = "trailing"
		if !positionActive {
			classification.Category = unexpectedCategoryOrphanForInactive
		} else if trailingOwnership.classifyTrailing(order.OrderID) {
			classification.Category = unexpectedCategoryExpectedDynamicOwner
		} else if isLikelyBotProtectionOrder(order) {
			classification.Category = unexpectedCategoryStaleBotDuplicate
		} else {
			classification.Category = unexpectedCategoryManualOrForeign
		}
		return classification
	}

	price := order.StopPrice
	if price <= 0 {
		price = order.Price
	}
	if looksLikeTakeProfit(order) {
		classification.Kind = "take_profit"
		if consumeAllowedProtectionSlot(allowedTPs, price, protectionRoleOfOrder(order)) {
			classification.Category = unexpectedCategoryExpectedStaticOwner
		} else if !positionActive {
			classification.Category = unexpectedCategoryOrphanForInactive
		} else if isLikelyBotProtectionOrder(order) {
			classification.Category = unexpectedCategoryStaleBotDuplicate
		} else {
			classification.Category = unexpectedCategoryManualOrForeign
		}
		return classification
	}
	if looksLikeStopLoss(order) {
		classification.Kind = "stop_loss"
		if consumeAllowedProtectionSlot(allowedStops, price, protectionRoleOfOrder(order)) {
			classification.Category = unexpectedCategoryExpectedStaticOwner
		} else if !positionActive {
			classification.Category = unexpectedCategoryOrphanForInactive
		} else if beOwnership.classifyBreakEvenStop(order.OrderID, beQuota) {
			classification.Category = unexpectedCategoryExpectedDynamicOwner
		} else if isLikelyBotProtectionOrder(order) {
			classification.Category = unexpectedCategoryStaleBotDuplicate
		} else {
			classification.Category = unexpectedCategoryManualOrForeign
		}
		return classification
	}
	classification.Kind = "unknown"
	classification.Category = unexpectedCategoryManualOrForeign
	return classification
}

func isLikelyBotProtectionOrder(order OpenOrder) bool {
	id := strings.ToLower(order.OrderID)
	clientID := strings.ToLower(order.ClientOrderID)
	combined := id + " " + clientID
	botMarkers := []string{
		"native_trailing",
		"managed_drawdown",
		"break_even",
		"ladder_",
		"full_",
		"fallback",
		"4c363c81edc5bcde", // OKX broker tag prefix
		"x-kzrpzap9",       // Binance broker tag prefix (see binance.getBrOrderID).
		// Kept as a fallback marker for orders placed before the reason moved into the
		// client id. The comment here used to claim Binance client IDs carry ONLY the
		// broker prefix + timestamp/random and that the semantic reasonTag is not
		// applied — that is no longer true: SetStopLossTagged, SetTakeProfitTagged and
		// placeMakerTakeProfit all pass clientIDForReason(reasonTag), so current orders
		// DO carry a decodable mechanism. The Binance adapter now decodes it into
		// OpenOrder.ProtectionRole. This marker still matters for legacy resting orders
		// and for orders whose id we cannot decode, which would otherwise be misread as
		// manual/foreign and preserved forever.
		"be-stop",
		"new-tier",
		"stale-",
	}
	for _, marker := range botMarkers {
		if strings.Contains(combined, marker) {
			return true
		}
	}
	return false
}
