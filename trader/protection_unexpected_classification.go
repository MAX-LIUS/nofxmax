package trader

import "strings"

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

func classifyUnexpectedProtectionOrders(openOrders []OpenOrder, positionSide string, plan *ProtectionPlan, breakEvenArmed bool, trailingOwnership nativeTrailingOwnership, positionActive bool) unexpectedProtectionSummary {
	allowedStops, allowedTPs := allowedProtectionPricesForPlan(plan)
	summary := unexpectedProtectionSummary{}
	for _, order := range openOrders {
		if positionSide != "" && order.PositionSide != "" && !strings.EqualFold(order.PositionSide, positionSide) {
			continue
		}
		classification := classifyProtectionOrder(order, &allowedStops, &allowedTPs, breakEvenArmed, trailingOwnership, positionActive)
		// "保本止损额度只有一张"的消耗判定必须看**分类结果**,不能看 looksLikeStopLoss(order):
		// 后者只做字符串匹配,而 TRAILING_STOP_MARKET 里含 "STOP" —— 于是第一张 trailing
		// 就把保本额度吃掉了,真正的保本止损排在 trailing 之后时会掉到 manual_or_foreign,
		// 被当成"外来单"污染 ownership(交易所返回挂单的顺序不做保证,所以这是概率性误判)。
		if classification.Category == unexpectedCategoryExpectedDynamicOwner && classification.Kind == "stop_loss" && breakEvenArmed {
			breakEvenArmed = false
		}
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

func allowedProtectionPricesForPlan(plan *ProtectionPlan) ([]float64, []float64) {
	allowedStops := make([]float64, 0)
	allowedTPs := make([]float64, 0)
	if plan == nil {
		return allowedStops, allowedTPs
	}
	for _, target := range plan.StopLossOrders {
		allowedStops = append(allowedStops, target.Price)
	}
	if len(plan.StopLossOrders) == 0 && plan.NeedsStopLoss && plan.StopLossPrice > 0 {
		allowedStops = append(allowedStops, plan.StopLossPrice)
	}
	if plan.FallbackMaxLossPrice > 0 {
		allowedStops = append(allowedStops, plan.FallbackMaxLossPrice)
	}
	// Guard-owned rolling backup stop(s): tolerated so the reconciler won't churn them,
	// but intentionally NOT added to missing-detection (the guard owns placement/roll).
	for _, p := range plan.AllowedExtraStopPrices {
		if p > 0 {
			allowedStops = append(allowedStops, p)
		}
	}
	for _, target := range plan.TakeProfitOrders {
		allowedTPs = append(allowedTPs, target.Price)
	}
	// Anchor-dropped ladder tiers: tolerated so a tier the anchor merely INFERRED as
	// executed is not canceled (which would make the inference true and oscillate the
	// plan every cycle). Like AllowedExtraStopPrices, intentionally NOT part of
	// missing-detection, so the reconciler never re-places them either.
	for _, p := range plan.AllowedExtraTakeProfitPrices {
		if p > 0 {
			allowedTPs = append(allowedTPs, p)
		}
	}
	if len(plan.TakeProfitOrders) == 0 && plan.NeedsTakeProfit && plan.TakeProfitPrice > 0 {
		allowedTPs = append(allowedTPs, plan.TakeProfitPrice)
	}
	return allowedStops, allowedTPs
}

func classifyProtectionOrder(order OpenOrder, allowedStops, allowedTPs *[]float64, breakEvenArmed bool, trailingOwnership nativeTrailingOwnership, positionActive bool) unexpectedProtectionOrderClassification {
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
		if consumeAllowedProtectionPrice(allowedTPs, price) {
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
		if consumeAllowedProtectionPrice(allowedStops, price) {
			classification.Category = unexpectedCategoryExpectedStaticOwner
		} else if !positionActive {
			classification.Category = unexpectedCategoryOrphanForInactive
		} else if breakEvenArmed {
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
		"x-kzrpzap9",        // Binance broker tag prefix (see binance.getBrOrderID);
		// Binance client IDs carry ONLY this broker prefix + timestamp/random — the
		// semantic reasonTag is NOT applied there (see binance SetStopLossTagged), so
		// without this marker every Binance protection order was misread as
		// manual/foreign and preserved forever, accumulating stale stop orders across
		// re-entries. Matching the broker prefix lets the reconciler recognize and
		// clean its own stale Binance stops.
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
