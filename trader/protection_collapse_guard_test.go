package trader

import "testing"

// When all ladder TP tiers are below current mark (price already moved past the
// targets), the collapse-to-full-TP must NOT re-place a doomed TP (would trigger
// OKX 51279 and loop). Regression for the HYPE 51279 spin (2026-06-09).
func TestValidateProtectionPlanExecutionDoesNotCollapseTPPastMark(t *testing.T) {
	fakeTrader := &fakeOrderProtectionTrader{
		positions: []map[string]interface{}{
			{"symbol": "HYPEUSDT", "side": "LONG", "markPrice": 64.12},
		},
	}
	at := &AutoTrader{trader: fakeTrader, exchange: "okx"}
	plan := &ProtectionPlan{
		NeedsTakeProfit: true,
		TakeProfitOrders: []ProtectionOrder{
			{Price: 60.86, CloseRatioPct: 40},
			{Price: 62.63, CloseRatioPct: 35},
		},
	}
	validated, err := at.validateProtectionPlanExecution("HYPEUSDT", "LONG", 0.2, plan, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if validated != nil && (validated.TakeProfitPrice > 0 || len(validated.TakeProfitOrders) > 0) {
		t.Fatalf("expected no TP re-placed when all tiers are below mark, got %+v", validated)
	}
}

// Sanity: when the ladder TP target is still ahead of mark, collapse still works
// (e.g. a long TP above current price after min-contract filtering).
func TestValidateProtectionPlanExecutionCollapsesTPAheadOfMark(t *testing.T) {
	fakeTrader := &fakeOrderProtectionTrader{
		positions: []map[string]interface{}{
			{"symbol": "HYPEUSDT", "side": "LONG", "markPrice": 58.0},
		},
		validateQtyErrBelow: 1.0, // force ladder tiers (qty<1) below exchange min → drop
	}
	at := &AutoTrader{trader: fakeTrader, exchange: "okx"}
	plan := &ProtectionPlan{
		NeedsTakeProfit: true,
		TakeProfitOrders: []ProtectionOrder{
			{Price: 60.86, CloseRatioPct: 40},
			{Price: 62.63, CloseRatioPct: 35},
		},
	}
	validated, err := at.validateProtectionPlanExecution("HYPEUSDT", "LONG", 0.2, plan, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if validated == nil || validated.TakeProfitPrice != 60.86 {
		t.Fatalf("expected collapse to nearest TP 60.86 (ahead of mark), got %+v", validated)
	}
}
