package backtest

import (
	"testing"

	"nofx/market"
)

// kl is a compact Kline builder for tests.
func kl(o, h, l, c float64) market.Kline {
	return market.Kline{Open: o, High: h, Low: l, Close: c}
}

// TestNearestSwingBeyond_ShortPicksNearestHighAbovePrice verifies we pick the
// CLOSEST swing high above price for a short, not the absolute spike.
func TestNearestSwingBeyond_ShortPicksNearestHighAbovePrice(t *testing.T) {
	// index:      0     1     2      3     4     5     6
	// swing highs at i=2 (110, far) and i=5 (104, near). price=100.
	w := []market.Kline{
		kl(99, 100, 98, 99),
		kl(99, 101, 98, 100),
		kl(100, 110, 99, 101), // pivot high 110 (k=1)
		kl(101, 103, 100, 102),
		kl(102, 103, 101, 102),
		kl(102, 104, 101, 103), // pivot high 104 (k=1)
		kl(103, 103, 102, 103),
	}
	got, ok := nearestSwingBeyond(w, 100, false /*short*/, 1)
	if !ok {
		t.Fatal("expected a swing")
	}
	if got != 104 {
		t.Fatalf("want nearest high 104, got %v", got)
	}
}

// TestNearestSwingBeyond_LongPicksNearestLowBelowPrice mirrors for a long.
func TestNearestSwingBeyond_LongPicksNearestLowBelowPrice(t *testing.T) {
	w := []market.Kline{
		kl(101, 102, 101, 101),
		kl(101, 102, 100, 101),
		kl(100, 101, 90, 99), // pivot low 90 (far)
		kl(99, 100, 98, 99),
		kl(98, 99, 97, 98),
		kl(98, 99, 96, 97), // pivot low 96 (near)
		kl(97, 98, 97, 97),
	}
	got, ok := nearestSwingBeyond(w, 100, true /*long*/, 1)
	if !ok {
		t.Fatal("expected a swing")
	}
	if got != 96 {
		t.Fatalf("want nearest low 96, got %v", got)
	}
}

// TestRecomputeTrail_RatchetsOnlyTighter proves the boundary never loosens.
func TestRecomputeTrail_RatchetsOnlyTighter(t *testing.T) {
	p := ProtectionParams{RangeSLPivotStrength: 1, TrailStructMode: "current", TrailStructTolATR: 0.5}
	atr := 2.0
	// short position, boundary starts wide at 120.
	ts := &trailState{isLong: false, atr: atr, boundary: 120}

	// Window with a near swing high at 108; price now 100. cushion=1.0 → proposed 109.
	w1 := []market.Kline{
		kl(99, 100, 98, 99),
		kl(100, 108, 99, 101), // pivot high 108
		kl(101, 103, 100, 100),
	}
	nb := recomputeTrailBoundary(ts, p, w1, 100, atr)
	if nb >= 120 {
		t.Fatalf("expected tighter than 120, got %v", nb)
	}
	tightened := nb

	// Next: structure moves AWAY (higher swing 115) — trail must NOT loosen.
	w2 := []market.Kline{
		kl(99, 100, 98, 99),
		kl(100, 115, 99, 101), // pivot high 115 (looser)
		kl(101, 103, 100, 100),
	}
	nb2 := recomputeTrailBoundary(ts, p, w2, 100, atr)
	if nb2 != tightened {
		t.Fatalf("ratchet loosened: was %v now %v", tightened, nb2)
	}
}

// TestRecomputeTrail_NeverArmsInsideMoney ensures a short stop is never placed at
// or below current price (which would insta-close).
func TestRecomputeTrail_NeverArmsInsideMoney(t *testing.T) {
	p := ProtectionParams{RangeSLPivotStrength: 1, TrailStructMode: "current", TrailStructTolATR: 0.5}
	atr := 2.0
	ts := &trailState{isLong: false, atr: atr, boundary: 0}
	// All highs sit BELOW price 100 → no structure above → no valid short stop.
	w := []market.Kline{
		kl(96, 98, 90, 97),
		kl(95, 97, 88, 94), // pivot low, everything below price
		kl(94, 96, 92, 95),
	}
	nb := recomputeTrailBoundary(ts, p, w, 100, atr)
	if nb != 0 {
		t.Fatalf("should not arm with no structure above price, got %v", nb)
	}
}

// TestAggregateHigherTF_GroupsAndDropsPartial verifies OHLC aggregation and that a
// trailing partial group is dropped.
func TestAggregateHigherTF_GroupsAndDropsPartial(t *testing.T) {
	base := []market.Kline{
		kl(10, 12, 9, 11),
		kl(11, 15, 10, 14),
		kl(14, 16, 13, 15),
		kl(15, 17, 11, 16),
		kl(16, 18, 15, 17), // partial 5th bar (mult=4) → dropped
	}
	hi := aggregateHigherTF(base, 4)
	if len(hi) != 1 {
		t.Fatalf("want 1 full higher bar, got %d", len(hi))
	}
	g := hi[0]
	if g.Open != 10 || g.Close != 16 || g.High != 17 || g.Low != 9 {
		t.Fatalf("bad aggregation: %+v", g)
	}
}
