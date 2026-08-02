package backtest

import (
	"math"
	"testing"
)

// TestUlcerPenalisesDurationNotJustDepth is the property that makes the Ulcer Index
// the right objective for「稳定爬楼梯」. Two paths with the SAME max drawdown must
// score differently when one stays under water far longer. Max drawdown alone
// cannot tell them apart, which is why it is the wrong metric for this goal.
func TestUlcerPenalisesDurationNotJustDepth(t *testing.T) {
	// Both dip to 90 from 100 (same 10% max DD), but one recovers immediately and
	// the other lingers.
	quick := []float64{100, 90, 100, 100, 100, 100, 100, 100}
	slow := []float64{100, 90, 90, 90, 90, 90, 90, 100}
	q, s := AnalyzeCurve(quick), AnalyzeCurve(slow)
	if math.Abs(q.MaxDDPct-s.MaxDDPct) > 1e-9 {
		t.Fatalf("fixture broken: max DD should match (%.4f vs %.4f)", q.MaxDDPct, s.MaxDDPct)
	}
	if !(s.UlcerIndex > q.UlcerIndex) {
		t.Errorf("the lingering path must score a WORSE Ulcer Index: quick=%.4f slow=%.4f",
			q.UlcerIndex, s.UlcerIndex)
	}
}

// TestPerfectStaircaseScoresBetterThanBoomBust encodes the objective itself: a
// steady climb must beat a path that ends at the same place after a large round
// trip. If this fails, the ranking metric is not measuring "staircase".
func TestPerfectStaircaseScoresBetterThanBoomBust(t *testing.T) {
	stair := []float64{100, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110}
	boom := []float64{100, 130, 160, 190, 150, 120, 100, 105, 108, 109, 110}
	a, b := AnalyzeCurve(stair), AnalyzeCurve(boom)
	if math.Abs(a.ReturnPct-b.ReturnPct) > 1e-9 {
		t.Fatalf("fixture broken: both should end at the same return (%.4f vs %.4f)", a.ReturnPct, b.ReturnPct)
	}
	if !(a.MartinRatio > b.MartinRatio) {
		t.Errorf("the staircase must rank above the round trip: stair Martin=%.4f boom=%.4f",
			a.MartinRatio, b.MartinRatio)
	}
	if !(a.TimeAtHighPct > b.TimeAtHighPct) {
		t.Errorf("the staircase must spend more time at its high: %.1f%% vs %.1f%%",
			a.TimeAtHighPct, b.TimeAtHighPct)
	}
}

// TestMonotoneRiseHasZeroUlcer: a curve that never declines has no drawdown at all,
// so the metric must report exactly zero rather than a small positive number.
func TestMonotoneRiseHasZeroUlcer(t *testing.T) {
	q := AnalyzeCurve([]float64{100, 101, 102, 103, 104})
	if q.UlcerIndex != 0 {
		t.Errorf("a monotone rise must have Ulcer 0, got %.9f", q.UlcerIndex)
	}
	if q.TimeAtHighPct != 100 {
		t.Errorf("a monotone rise is always at its high, got %.2f%%", q.TimeAtHighPct)
	}
}

// TestFreezePathArmGateBlocksFiring verifies the「涨到 +n% 才挂上熔断」branch: with an
// unreachable gain requirement the breaker must never fire and the path must equal
// the raw curve.
func TestFreezePathArmGateBlocksFiring(t *testing.T) {
	curve := []float64{100, 99, 97, 95, 92, 90}
	path, freezes := FreezePath(curve, 50 /* +50% unreachable */, 1.0, 10)
	if freezes != 0 {
		t.Fatalf("breaker fired %d times behind an unreachable arm gate", freezes)
	}
	for i := range curve {
		if math.Abs(path[i]-curve[i]) > 1e-9 {
			t.Fatalf("unarmed path must track the raw curve; diverged at %d (%.4f vs %.4f)",
				i, path[i], curve[i])
		}
	}
}

// TestFreezePathHoldsEquityFlatWhileFrozen pins the freeze semantics: while frozen
// the account must not track the curve at all, in either direction.
func TestFreezePathHoldsEquityFlatWhileFrozen(t *testing.T) {
	// Arms immediately (armPct=0), fires on the first 1% dip, then sits out 3 samples
	// during which the raw curve moves violently.
	curve := []float64{100, 98, 50, 200, 150, 160}
	path, freezes := FreezePath(curve, 0, 1.0, 3)
	if freezes == 0 {
		t.Fatal("breaker never fired; test cannot discriminate")
	}
	// After firing at index 1 (98), the next 3 samples must all read 98.
	for i := 2; i <= 4 && i < len(path); i++ {
		if math.Abs(path[i]-98) > 1e-9 {
			t.Errorf("sample %d should be frozen at 98, got %.4f", i, path[i])
		}
	}
}

// TestFreezePathResetsReferenceAfterFiring verifies the new-cycle semantics: after
// re-entry the drawdown reference is the crystallised equity, not the old peak.
// Without the reset the account would be measured against a high it can no longer
// reach and would fire again immediately on resuming.
func TestFreezePathResetsReferenceAfterFiring(t *testing.T) {
	// Fires at 98, sits out 2, resumes at 98 while the curve is far below the old
	// peak of 100. With a correct reset it must NOT instantly re-fire.
	curve := []float64{100, 98, 97, 96, 96.5, 96.6, 96.7}
	_, freezes := FreezePath(curve, 0, 1.0, 2)
	if freezes > 2 {
		t.Errorf("breaker fired %d times; the post-freeze reference was not reset to the "+
			"crystallised equity", freezes)
	}
}

// TestTimeInMarketFallsWithLongerCooldown guards the column that keeps the search
// honest: a longer sit-out must reduce participation. Without this reported, an
// optimiser converges on "stop trading" and presents it as a tuned breaker.
func TestTimeInMarketFallsWithLongerCooldown(t *testing.T) {
	curve := make([]float64, 400)
	v := 100.0
	for i := range curve {
		if i%7 == 0 {
			v *= 0.99
		} else {
			v *= 1.001
		}
		curve[i] = v
	}
	_, _, shortAct := FreezePathActive(curve, 0, 0.5, 5)
	_, _, longAct := FreezePathActive(curve, 0, 0.5, 100)
	if !(longAct < shortAct) {
		t.Errorf("a longer cooldown must reduce time in market: short=%d long=%d", shortAct, longAct)
	}
}

// TestFrozenStretchesInflateSmoothness documents WHY time-in-market must be reported
// next to any smoothness score: a frozen stretch is perfectly flat, and flat samples
// count as "at the high", so sitting out mechanically improves the Ulcer Index
// without any skill. This test asserts the effect exists so nobody later reads a low
// Ulcer Index as evidence of a good breaker.
func TestFrozenStretchesInflateSmoothness(t *testing.T) {
	curve := []float64{100, 99, 98, 97, 96, 95, 94, 93, 92, 91, 90}
	raw := AnalyzeCurve(curve)
	// Fire early and sit out essentially the whole remainder.
	frozen, freezes := FreezePath(curve, 0, 0.5, 1000)
	if freezes == 0 {
		t.Fatal("breaker never fired; test cannot discriminate")
	}
	fq := AnalyzeCurve(frozen)
	if !(fq.UlcerIndex < raw.UlcerIndex) {
		t.Errorf("sitting out should mechanically improve the Ulcer Index "+
			"(raw=%.4f frozen=%.4f); if it does not, the metric is not measuring what "+
			"the time-in-market caveat assumes", raw.UlcerIndex, fq.UlcerIndex)
	}
}
