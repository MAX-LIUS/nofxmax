package trader

import "testing"

// okxLotFloorPasses mimics OKX ValidateProtectionQuantity's lot-size floor for
// ZEC-USDT-SWAP (ctVal=0.01, lotSz=1): a tier is placeable only when it spans at
// least one whole contract. This is the gate that actually decides whether the
// executability filter keeps a tier in the plan, and therefore whether the
// reconciler treats the tier's resting order as expected or as a stale duplicate.
func okxLotFloorPasses(currentQuantity, closeRatioPct float64) bool {
	const ctVal, lotSz = 0.01, 1.0
	return currentQuantity*closeRatioPct/100.0/ctVal >= lotSz
}

// TestLiveTierRatioIsAnchoredToCurrentQuantity pins the ZECUSDT ping-pong root
// cause (2026-07-28). Rule 2 used to keep a LIVE tier at its original
// entry-quantity ratio, on the reasoning that the resting order was already
// sized against the entry quantity and a price match would keep it from being
// re-placed. That reasoning stopped holding once validateProtectionPlanExecution
// began filtering the plan through `currentQuantity × ratio` BEFORE
// missing/unexpected detection:
//
//	tier live   → ratio 12% → 0.08×12% = 0.0096 → 0.96 lots → BELOW floor
//	              ⇒ tier filtered out of the plan ⇒ resting 455.40 has no allowed
//	              price ⇒ classified stale duplicate ⇒ CANCELED
//	tier absent → reduction path → 0.012/0.08 = 15% → 1.2 lots → passes floor
//	              ⇒ tier in plan ⇒ detected missing ⇒ PLACED
//
// Being alive made the tier unplaceable; being dead made it placeable. Hence one
// cancel + one place every ~2.5 minutes, forever. The two paths must agree.
func TestLiveTierRatioIsAnchoredToCurrentQuantity(t *testing.T) {
	const entryQty, currentQty = 0.10, 0.08

	// Cycle A — the 12% tier's order is NOT resting (it was canceled last cycle).
	absent := shortPlanWithTiers()
	anchorLadderTakeProfitToEntry(absent, "open_short", entryQty, currentQty, []float64{467.23, 462.25})
	ratioWhenAbsent := ratioForPrice(t, absent, 455.40)

	// Cycle B — the same tier's order IS resting (it was placed last cycle).
	live := shortPlanWithTiers()
	anchorLadderTakeProfitToEntry(live, "open_short", entryQty, currentQty, []float64{467.23, 462.25, 455.40})
	ratioWhenLive := ratioForPrice(t, live, 455.40)

	if !approximatelyEqualPrice(ratioWhenAbsent, ratioWhenLive) {
		t.Fatalf("tier ratio must not depend on whether its own order is resting: absent=%.4f%% live=%.4f%% — this asymmetry IS the ping-pong",
			ratioWhenAbsent, ratioWhenLive)
	}

	// The point of agreeing is to agree on a PLACEABLE size. Both cycles must
	// clear the exchange floor, otherwise they would merely churn in lockstep.
	if !okxLotFloorPasses(currentQty, ratioWhenLive) {
		t.Fatalf("live-tier ratio %.4f%% yields %.6f (%.2f lots) — below the exchange floor, so the executability filter drops the tier and its resting order gets canceled as a stale duplicate",
			ratioWhenLive, currentQty*ratioWhenLive/100, currentQty*ratioWhenLive/100/0.01)
	}
	if !okxLotFloorPasses(currentQty, ratioWhenAbsent) {
		t.Fatalf("absent-tier ratio %.4f%% is not placeable either", ratioWhenAbsent)
	}
}

// TestLiveTierRatioPreservesIntendedQuantity guards the fix from becoming a
// silent size change: re-expressing the ratio must reproduce the SAME base-asset
// quantity the tier always intended (entryQuantity × original ratio), just
// measured against the current position instead of the entry position.
func TestLiveTierRatioPreservesIntendedQuantity(t *testing.T) {
	const entryQty, currentQty = 0.10, 0.08

	plan := shortPlanWithTiers()
	anchorLadderTakeProfitToEntry(plan, "open_short", entryQty, currentQty, []float64{467.23, 462.25, 455.40})

	for _, original := range shortPlanWithTiers().TakeProfitOrders {
		if approximatelyEqualPrice(original.Price, 470.97) {
			continue // Rule 1 drop, covered by the tolerance test
		}
		intended := entryQty * original.CloseRatioPct / 100.0
		got := currentQty * ratioForPrice(t, plan, original.Price) / 100.0
		if !approximatelyEqualPrice(intended, got) {
			t.Fatalf("tier @%.2f: intended qty %.6f but plan now yields %.6f — the re-anchor must change the DENOMINATOR, not the size",
				original.Price, intended, got)
		}
	}
}

// TestNoReductionKeepsOriginalRatio is the negative control: when the position
// has not shrunk there is nothing to re-anchor, and the original ratios must
// survive untouched. Without this, "always re-express" would pass the test above
// while quietly rewriting ratios on every healthy position.
func TestNoReductionKeepsOriginalRatio(t *testing.T) {
	plan := shortPlanWithTiers()
	anchorLadderTakeProfitToEntry(plan, "open_short", 0.08, 0.08, []float64{470.97, 467.23, 462.25, 455.40})

	for _, original := range shortPlanWithTiers().TakeProfitOrders {
		if got := ratioForPrice(t, plan, original.Price); !approximatelyEqualPrice(got, original.CloseRatioPct) {
			t.Fatalf("tier @%.2f: no reduction happened, ratio must stay %.4f%% but became %.4f%%",
				original.Price, original.CloseRatioPct, got)
		}
	}
}

// TestLiveTierRatioReproducesChurnWithoutFix is the reverse control that pins the
// production symptom itself: at the ORIGINAL entry-quantity ratio the ZEC 12%
// tier is not placeable against the shrunken position, which is precisely why its
// resting order was canceled every other cycle.
func TestLiveTierRatioReproducesChurnWithoutFix(t *testing.T) {
	const currentQty = 0.08
	if okxLotFloorPasses(currentQty, 12) {
		t.Fatal("premise broken: 0.08×12% must fall below the OKX lot floor, else this bug could not have happened")
	}
	if !okxLotFloorPasses(currentQty, 15) {
		t.Fatal("premise broken: the re-anchored 15% must clear the floor, else the fix cannot work")
	}
}

func ratioForPrice(t *testing.T, plan *ProtectionPlan, price float64) float64 {
	t.Helper()
	for _, tp := range plan.TakeProfitOrders {
		if approximatelyEqualPrice(tp.Price, price) {
			return tp.CloseRatioPct
		}
	}
	t.Fatalf("tier @%.2f missing from plan (%d tiers kept)", price, len(plan.TakeProfitOrders))
	return 0
}
