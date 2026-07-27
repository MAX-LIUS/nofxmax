package trader

import "testing"

func tp(price, ratio float64) ProtectionOrder {
	return ProtectionOrder{Price: price, CloseRatioPct: ratio}
}

// TestAnchor_NoReduction keeps the open-time ladder untouched when nothing closed.
func TestAnchor_NoReduction(t *testing.T) {
	plan := &ProtectionPlan{
		NeedsTakeProfit:  true,
		TakeProfitOrders: []ProtectionOrder{tp(0.422, 40), tp(0.409, 35)},
	}
	// nil live prices = every tier is gone from the exchange, so the decision falls
	// through to the quantity arithmetic these three tests were written to pin.
	anchorLadderTakeProfitToEntry(plan, "open_short", 51, 51, nil)
	if len(plan.TakeProfitOrders) != 2 {
		t.Fatalf("expected 2 tiers preserved, got %d", len(plan.TakeProfitOrders))
	}
}

// TestAnchor_TP1FilledDropped drops TP1 once the position shrank past its size,
// and re-expresses TP2 against the current remaining quantity.
func TestAnchor_TP1FilledDropped(t *testing.T) {
	// entry 51, TP1=40% (20.4), TP2=35% (17.85). Position now 31 (closed 20 ≈ TP1).
	plan := &ProtectionPlan{
		NeedsTakeProfit:  true,
		TakeProfitOrders: []ProtectionOrder{tp(0.422, 40), tp(0.409, 35)},
	}
	anchorLadderTakeProfitToEntry(plan, "open_short", 51, 31, nil)
	if len(plan.TakeProfitOrders) != 1 {
		t.Fatalf("expected TP1 dropped, 1 tier left, got %d", len(plan.TakeProfitOrders))
	}
	got := plan.TakeProfitOrders[0]
	if got.Price != 0.409 {
		t.Fatalf("expected TP2 (0.409) kept, got %.3f", got.Price)
	}
	// TP2 unfilled size = 17.85, current = 31 → ratio ≈ 57.58%
	wantQty := 17.85
	gotQty := 31 * got.CloseRatioPct / 100.0
	if diff := gotQty - wantQty; diff > 0.2 || diff < -0.2 {
		t.Fatalf("expected anchored qty ~%.2f, got %.2f (ratio %.2f%%)", wantQty, gotQty, got.CloseRatioPct)
	}
}

// TestAnchor_AllFilledDustTail clears TP entirely on a dust remainder so the
// reconciler stops re-placing (WLD 51→2 scenario).
func TestAnchor_AllFilledDustTail(t *testing.T) {
	plan := &ProtectionPlan{
		NeedsTakeProfit:  true,
		TakeProfitOrders: []ProtectionOrder{tp(0.422, 40), tp(0.409, 35)},
	}
	anchorLadderTakeProfitToEntry(plan, "open_short", 51, 2, nil)
	if len(plan.TakeProfitOrders) != 0 {
		t.Fatalf("expected all tiers dropped on dust tail, got %d", len(plan.TakeProfitOrders))
	}
	if plan.NeedsTakeProfit {
		t.Fatalf("expected NeedsTakeProfit=false on dust tail")
	}
}

// TestNearestLadderTakeProfitPrice picks the first-to-fill tier per side.
func TestNearestLadderTakeProfitPrice(t *testing.T) {
	short := []ProtectionOrder{tp(0.422, 40), tp(0.409, 35)}
	if got := nearestLadderTakeProfitPrice(short, "short"); got != 0.422 {
		t.Fatalf("short nearest expected 0.422, got %.3f", got)
	}
	long := []ProtectionOrder{tp(105, 40), tp(110, 35)}
	if got := nearestLadderTakeProfitPrice(long, "long"); got != 105 {
		t.Fatalf("long nearest expected 105, got %.3f", got)
	}
}

// TestTightestLadderStopPrice picks the tightest stop per side.
func TestTightestLadderStopPrice(t *testing.T) {
	short := []ProtectionOrder{tp(0.457, 100), tp(0.470, 50)}
	if got := tightestLadderStopPrice(short, "short"); got != 0.457 {
		t.Fatalf("short tightest expected 0.457, got %.3f", got)
	}
	long := []ProtectionOrder{tp(95, 100), tp(90, 50)}
	if got := tightestLadderStopPrice(long, "long"); got != 95 {
		t.Fatalf("long tightest expected 95, got %.3f", got)
	}
}
