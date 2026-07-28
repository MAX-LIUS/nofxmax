package trader

func collectUnexpectedProtectionOrderIDs(openOrders []OpenOrder, positionSide string, plan *ProtectionPlan, beOwnership breakEvenOwnership, trailingOwnership nativeTrailingOwnership) []string {
	summary := classifyUnexpectedProtectionOrders(openOrders, positionSide, plan, beOwnership, trailingOwnership, true)
	ids := make([]string, 0, len(summary.StaleBotDuplicateIDs)+len(summary.OrphanForInactiveIDs))
	ids = append(ids, summary.StaleBotDuplicateIDs...)
	ids = append(ids, summary.OrphanForInactiveIDs...)
	return ids
}
