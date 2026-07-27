package trader

import "testing"

// The CLUSDT SHORT incident of 2026-07-28, replayed from the production log and
// the trader_orders rows it wrote. Entry 2.8 @ 83.68, ladder TP tiers
// 20/18/15/12% at 82.7193 / 82.1954 / 81.4967 / 80.5360.
//
// 00:33:00 tier 1 filled on the exchange (0.6 @ 82.71).
// 00:33:08 the reconciler read open orders FRESH (tier 1 gone) but its position
//          quantity came from the loop's cached GetPositions call, still 2.8, and
//          the DB position row was mid-write (the "2.800000 → 2.200000" line lands
//          between plan materialization and order placement in the same second).
//
// Both regressions below are about that one instant.
func clusdtTiers() []ProtectionOrder {
	return []ProtectionOrder{
		tp(82.7193, 20),
		tp(82.1954, 18),
		tp(81.4967, 15),
		tp(80.5360, 12),
	}
}

func tierPrices(orders []ProtectionOrder) []float64 {
	out := make([]float64, 0, len(orders))
	for _, o := range orders {
		out = append(out, o.Price)
	}
	return out
}

func hasTierPrice(orders []ProtectionOrder, price float64) bool {
	for _, o := range orders {
		if approximatelyEqualPrice(o.Price, price) {
			return true
		}
	}
	return false
}

// TestAnchor_StalePositionQtyDoesNotRePlaceFilledTier is the money half. With the
// position quantity still reading its pre-fill size, the old code early-returned on
// entryQuantity <= currentQuantity, kept all four tiers, and the caller re-placed
// tier 1 at 82.7193 — a price the market had already crossed (fill was 82.71), so
// it filled again ~10s later and closed 0.6 that should still have been running.
//
// The live snapshot alone settles it: tier 1 is gone while tiers 2-4 are still
// live, and a short ladder cannot reach tier 2's price without passing tier 1's.
func TestAnchor_StalePositionQtyDoesNotRePlaceFilledTier(t *testing.T) {
	all := clusdtTiers()
	plan := &ProtectionPlan{NeedsTakeProfit: true, TakeProfitOrders: all}

	// Exchange truth at 00:33:08: tier 1 gone, tiers 2/3/4 live.
	live := tierPrices(all[1:])

	// Position quantity as the reconciler saw it: stale, no reduction visible.
	anchorLadderTakeProfitToEntry(plan, "open_short", 2.8, 2.8, live)

	if hasTierPrice(plan.TakeProfitOrders, 82.7193) {
		t.Fatalf("tier 1 (82.7193) had already filled and must not be re-placed; "+
			"re-placing it is what closed an extra 0.6 in production. got %v",
			tierPrices(plan.TakeProfitOrders))
	}
	if len(plan.TakeProfitOrders) != 3 {
		t.Fatalf("expected the 3 live tiers preserved, got %d: %v",
			len(plan.TakeProfitOrders), tierPrices(plan.TakeProfitOrders))
	}
	for _, want := range []float64{82.1954, 81.4967, 80.5360} {
		if !hasTierPrice(plan.TakeProfitOrders, want) {
			t.Fatalf("live tier %.4f must be preserved, got %v", want, tierPrices(plan.TakeProfitOrders))
		}
	}
	// Live tiers keep their open-time ratios: the order on the exchange is already
	// sized, and the caller treats a price match as satisfied, so nothing re-places.
	for _, o := range plan.TakeProfitOrders {
		if approximatelyEqualPrice(o.Price, 82.1954) && o.CloseRatioPct != 18 {
			t.Fatalf("live tier 2 ratio must stay 18%%, got %.2f%%", o.CloseRatioPct)
		}
	}
}

// TestAnchor_LiveTierIsNeverDeclaredExecuted is the amplifier half. Once the
// duplicate tier 1 also filled, closedQty was 1.2 — double tier 1's size. The
// greedy walk gave the surplus to tier 2 and declared it executed, so the caller
// canceled tier 2's order as a stale duplicate. Tier 2's order was live on the
// exchange the entire time and had never triggered: 82.1954 needs +1.77% and the
// position only ever reached +1.18%.
//
// A tier with a live order has provably not fired, whatever the arithmetic says.
func TestAnchor_LiveTierIsNeverDeclaredExecuted(t *testing.T) {
	all := clusdtTiers()
	plan := &ProtectionPlan{NeedsTakeProfit: true, TakeProfitOrders: all}

	// 00:34:28: tiers 1 and its duplicate both filled (1.2 closed, position 1.6).
	// Tiers 2/3/4 all still live on the exchange.
	live := tierPrices(all[1:])

	anchorLadderTakeProfitToEntry(plan, "open_short", 2.8, 1.6, live)

	if !hasTierPrice(plan.TakeProfitOrders, 82.1954) {
		t.Fatalf("tier 2 (82.1954) was LIVE on the exchange and never triggered; "+
			"declaring it executed is what canceled a working TP target. got %v",
			tierPrices(plan.TakeProfitOrders))
	}
	for _, want := range []float64{81.4967, 80.5360} {
		if !hasTierPrice(plan.TakeProfitOrders, want) {
			t.Fatalf("live tier %.4f must survive an over-close, got %v", want, tierPrices(plan.TakeProfitOrders))
		}
	}
	if hasTierPrice(plan.TakeProfitOrders, 82.7193) {
		t.Fatalf("tier 1 did fill and must stay dropped, got %v", tierPrices(plan.TakeProfitOrders))
	}
}

// TestAnchor_SurplusCloseIsNotAttributedToAnyTier pins the rule that makes the
// above safe in general: quantity closed by something this function cannot see
// (manual close, drawdown trailing, an over-close) must not be charged to a tier.
// Here the whole ladder is live and the position was halved by an outside close.
func TestAnchor_SurplusCloseIsNotAttributedToAnyTier(t *testing.T) {
	all := clusdtTiers()
	plan := &ProtectionPlan{NeedsTakeProfit: true, TakeProfitOrders: all}
	live := tierPrices(all)

	anchorLadderTakeProfitToEntry(plan, "open_short", 2.8, 1.4, live)

	if len(plan.TakeProfitOrders) != 4 {
		t.Fatalf("every tier is live and none fired; all 4 must survive a 50%% outside close, got %d: %v",
			len(plan.TakeProfitOrders), tierPrices(plan.TakeProfitOrders))
	}
}

// TestAnchor_FilledInnerTierWithNoOuterLiveFallsBackToQty covers the last tier of
// a ladder, where Rule 1 has no outer tier to lean on. Nothing is live, so only
// the quantity arithmetic can answer — and it must still answer correctly.
func TestAnchor_FilledInnerTierWithNoOuterLiveFallsBackToQty(t *testing.T) {
	plan := &ProtectionPlan{
		NeedsTakeProfit:  true,
		TakeProfitOrders: []ProtectionOrder{tp(82.7193, 20), tp(82.1954, 18)},
	}
	// Both gone from the exchange, position reduced past both (0.56+0.504=1.064).
	anchorLadderTakeProfitToEntry(plan, "open_short", 2.8, 1.7, nil)

	if len(plan.TakeProfitOrders) != 0 {
		t.Fatalf("both tiers filled and gone; expected none kept, got %v", tierPrices(plan.TakeProfitOrders))
	}
	if plan.NeedsTakeProfit {
		t.Fatalf("expected NeedsTakeProfit=false once every tier executed")
	}
}

// TestLiveLadderTakeProfitPrices_ExcludesTrailing keeps the drawdown owner's
// trailing orders out of the ladder view: they are priced by callback, not by a
// tier, so matching one to a tier price would be meaningless.
func TestLiveLadderTakeProfitPrices_ExcludesTrailing(t *testing.T) {
	orders := []OpenOrder{
		{OrderID: "tp2", Type: "TAKE_PROFIT", PositionSide: "SHORT", StopPrice: 82.1954},
		{OrderID: "trail", Type: "TRAILING_STOP_MARKET", PositionSide: "SHORT", StopPrice: 81.06},
		{OrderID: "sl", Type: "STOP", PositionSide: "SHORT", StopPrice: 86.3873},
		{OrderID: "other", Type: "TAKE_PROFIT", PositionSide: "LONG", StopPrice: 99},
	}
	got := liveLadderTakeProfitPrices(orders, "SHORT")
	if len(got) != 1 || !approximatelyEqualPrice(got[0], 82.1954) {
		t.Fatalf("expected only the SHORT ladder TP price 82.1954, got %v", got)
	}
}
