package trader

import "testing"

// shortPlanWithTiers builds a 4-tier SHORT ladder TP plan (prices descend away
// from entry, so index 0 is the innermost tier and fills first).
func shortPlanWithTiers() *ProtectionPlan {
	return &ProtectionPlan{
		NeedsTakeProfit: true,
		TakeProfitOrders: []ProtectionOrder{
			{Price: 470.97, CloseRatioPct: 20},
			{Price: 467.23, CloseRatioPct: 18},
			{Price: 462.25, CloseRatioPct: 15},
			{Price: 455.40, CloseRatioPct: 12},
		},
	}
}

// TestAnchorDroppedTierIsToleratedNotCanceled pins the second half of the
// 2026-07-28 ZECUSDT defect. The anchor infers a tier already fired and drops it
// from the plan — correct. But a dropped tier stopped consuming an allowed price,
// so the reconciler reclassified its resting order as a stale bot duplicate and
// canceled it, making the inference true after the fact and destroying a target
// that never triggered. The drop must therefore also register tolerance.
func TestAnchorDroppedTierIsToleratedNotCanceled(t *testing.T) {
	plan := shortPlanWithTiers()
	// Innermost tier (470.97) gone from the exchange while outer tiers rest ⇒
	// Rule 1 infers it fired. Position shrank 0.10 → 0.08, consistent with that.
	live := []float64{467.23, 462.25, 455.40}

	anchorLadderTakeProfitToEntry(plan, "open_short", 0.10, 0.08, live)

	for _, tp := range plan.TakeProfitOrders {
		if approximatelyEqualPrice(tp.Price, 470.97) {
			t.Fatal("470.97 fired; it must be dropped from the plan (not re-placed at a price the market crossed)")
		}
	}
	found := false
	for _, p := range plan.AllowedExtraTakeProfitPrices {
		if approximatelyEqualPrice(p, 470.97) {
			found = true
		}
	}
	if !found {
		t.Fatal("dropped tier 470.97 must be registered as tolerated, else the reconciler cancels it as a stale duplicate")
	}
}

// TestAnchorDroppedTierNotCountedAsUnexpected drives the tolerance through the
// classifier that actually decides cancellation, which is where the damage
// happened. Without the fix this order lands in StaleBotDuplicate and is canceled.
func TestAnchorDroppedTierNotCountedAsUnexpected(t *testing.T) {
	plan := shortPlanWithTiers()
	live := []float64{467.23, 462.25, 455.40}
	anchorLadderTakeProfitToEntry(plan, "open_short", 0.10, 0.08, live)

	// The exchange still holds the dropped tier's order — the cancel has not run yet.
	orders := []OpenOrder{
		{Type: "TAKE_PROFIT_MARKET", PositionSide: "SHORT", StopPrice: 470.97, Quantity: 0.02, OrderID: "dropped", ClientOrderID: "ladder_tp-x"},
		{Type: "TAKE_PROFIT_MARKET", PositionSide: "SHORT", StopPrice: 467.23, Quantity: 0.01, OrderID: "t2", ClientOrderID: "ladder_tp-x"},
		{Type: "TAKE_PROFIT_MARKET", PositionSide: "SHORT", StopPrice: 462.25, Quantity: 0.01, OrderID: "t3", ClientOrderID: "ladder_tp-x"},
		{Type: "TAKE_PROFIT_MARKET", PositionSide: "SHORT", StopPrice: 455.40, Quantity: 0.01, OrderID: "t4", ClientOrderID: "ladder_tp-x"},
	}

	summary := classifyUnexpectedProtectionOrders(orders, "SHORT", plan, breakEvenArmedOnly(false), nativeTrailingOwnership{}, true)
	if summary.StaleBotDuplicate != 0 {
		t.Errorf("no resting ladder TP may be classified stale: got StaleBotDuplicate=%d (the dropped tier would be canceled)", summary.StaleBotDuplicate)
	}
	_, unexpectedTPs := detectUnexpectedProtectionOrders(orders, "SHORT", plan, breakEvenArmedOnly(false), nativeTrailingOwnership{})
	if unexpectedTPs != 0 {
		t.Errorf("unexpectedTP must be 0, got %d — a nonzero count sends the reconciler into the cancel path", unexpectedTPs)
	}

	// Negative control: strip the tolerance the fix adds and the same input must
	// reproduce the production symptom, proving the assertions above are load-bearing.
	legacy := *plan
	legacy.AllowedExtraTakeProfitPrices = nil
	legacySummary := classifyUnexpectedProtectionOrders(orders, "SHORT", &legacy, breakEvenArmedOnly(false), nativeTrailingOwnership{}, true)
	if legacySummary.StaleBotDuplicate == 0 {
		t.Error("expected the pre-fix plan to classify the dropped tier as stale; if it does not, this test no longer pins the bug")
	}
}

// TestAnchorDroppedTierNotRequiredAsMissing pins the other half of the contract:
// tolerated prices must NOT be part of missing-detection, or the reconciler would
// re-place a tier at a price the market already crossed — the exact over-close
// the anchor exists to prevent.
func TestAnchorDroppedTierNotRequiredAsMissing(t *testing.T) {
	plan := shortPlanWithTiers()
	live := []float64{467.23, 462.25, 455.40}
	anchorLadderTakeProfitToEntry(plan, "open_short", 0.10, 0.08, live)

	// Dropped tier's order is now genuinely gone (it filled).
	orders := []OpenOrder{
		{Type: "TAKE_PROFIT_MARKET", PositionSide: "SHORT", StopPrice: 467.23, Quantity: 0.01},
		{Type: "TAKE_PROFIT_MARKET", PositionSide: "SHORT", StopPrice: 462.25, Quantity: 0.01},
		{Type: "TAKE_PROFIT_MARKET", PositionSide: "SHORT", StopPrice: 455.40, Quantity: 0.01},
	}
	_, missingTP := detectMissingProtection(orders, "SHORT", plan, false)
	if missingTP {
		t.Error("a tolerated (dropped) tier must not make the plan look incomplete — that re-places at a crossed price")
	}

	// And a genuinely absent PLANNED tier must still be reported missing.
	partial := orders[:2]
	if _, stillMissing := detectMissingProtection(partial, "SHORT", plan, false); !stillMissing {
		t.Error("a planned tier that is really absent must still be detected as missing")
	}
}
