package backtest

import "testing"

// rollGuard builds a rolling-mode GuardParams with the given knobs.
func rollGuard(drop, cut float64, counterOnly bool, keep float64) GuardParams {
	return GuardParams{
		Enabled: true, L3Enabled: true, StartCapital: 250,
		L3Mode: "rolling", L3RollDropPct: drop, L3RollCutPct: cut,
		L3RollCounterOnly: counterOnly, L3TrendKeepMult: keep, L3VelWindow: 6,
	}
}

// Below the drop threshold -> no cut.
func TestRolling_NoTriggerAboveThreshold(t *testing.T) {
	g := rollGuard(4, 30, false, 0)
	st := &guardState{}
	p := l3pos(100, 1, 1.0)
	// seed ref at 250, then equity only down 2% (< 4%)
	applyRollingBreaker(g, []guardPos{{pos: p, price: 100}}, 250, st)
	n, _ := applyRollingBreaker(g, []guardPos{{pos: p, price: 100}}, 245, st)
	if n != 0 {
		t.Fatalf("expected no cut at 2%% drop, got %d", n)
	}
	if p.remaining != 1.0 {
		t.Fatalf("position should be untouched, remaining=%.3f", p.remaining)
	}
}

// Crossing the drop threshold cuts cut% of CURRENT remaining (exponential).
func TestRolling_CutsFractionOfRemaining(t *testing.T) {
	g := rollGuard(4, 30, false, 0)
	st := &guardState{}
	p := l3pos(100, 1, 1.0)
	applyRollingBreaker(g, []guardPos{{pos: p, price: 100}}, 250, st) // seed ref=250
	n, _ := applyRollingBreaker(g, []guardPos{{pos: p, price: 100}}, 235, st) // -6% -> fire
	if n != 1 {
		t.Fatalf("expected 1 trim, got %d", n)
	}
	// cut 30% of remaining 1.0 -> 0.70 left
	if d := p.remaining - 0.70; d > 1e-9 || d < -1e-9 {
		t.Fatalf("expected remaining 0.70 after 30%% cut, got %.4f", p.remaining)
	}
	// reference re-based to 235; another -6% from there (to ~221) fires again,
	// cutting 30% of the CURRENT 0.70 -> 0.49 (exponential, not linear-to-0.40).
	applyRollingBreaker(g, []guardPos{{pos: p, price: 100}}, 235, st) // raise? no, equals ref
	n2, _ := applyRollingBreaker(g, []guardPos{{pos: p, price: 100}}, 220, st)
	if n2 != 1 {
		t.Fatalf("expected 2nd trim, got %d", n2)
	}
	if d := p.remaining - 0.49; d > 1e-3 || d < -1e-3 {
		t.Fatalf("expected exponential remaining ~0.49, got %.4f", p.remaining)
	}
}

// Gap scaling ②: a drop far past the threshold cuts proportionally more (capped).
func TestRolling_GapScaling(t *testing.T) {
	g := rollGuard(4, 20, false, 0)
	g.L3RollScaleCap = 3
	st := &guardState{}
	p := l3pos(100, 1, 1.0)
	applyRollingBreaker(g, []guardPos{{pos: p, price: 100}}, 250, st) // seed
	// -12% = 3x threshold -> scale 3 -> effective cut 60% of remaining.
	applyRollingBreaker(g, []guardPos{{pos: p, price: 100}}, 220, st)
	if d := p.remaining - 0.40; d > 1e-3 || d < -1e-3 {
		t.Fatalf("expected 60%% cut (scaled) -> remaining 0.40, got %.4f", p.remaining)
	}
}

// Whipsaw re-arm ①: after a cut, a small bounce must NOT re-arm; rollRef stays
// pinned until equity recovers L3RollReArmPct, so the next dip doesn't re-cut.
func TestRolling_ReArmBlocksWhipsaw(t *testing.T) {
	g := rollGuard(4, 30, false, 0)
	g.L3RollReArmPct = 4 // need +4% recovery to re-arm
	st := &guardState{}
	p := l3pos(100, 1, 1.0)
	applyRollingBreaker(g, []guardPos{{pos: p, price: 100}}, 250, st)       // seed ref=250
	n1, _ := applyRollingBreaker(g, []guardPos{{pos: p, price: 100}}, 235, st) // -6% -> fire, ref->235, floor=235*1.04
	if n1 != 1 {
		t.Fatalf("expected first cut, got %d", n1)
	}
	rem := p.remaining
	// small bounce to 240 (< floor 244.4) then dip to 228: ref must NOT have risen
	// to 240, so drop is measured from 235 -> only -3% -> no fire.
	applyRollingBreaker(g, []guardPos{{pos: p, price: 100}}, 240, st)
	n2, _ := applyRollingBreaker(g, []guardPos{{pos: p, price: 100}}, 228, st)
	if n2 != 0 {
		t.Fatalf("re-arm guard should block whipsaw cut, got %d (remaining %.3f->%.3f)", n2, rem, p.remaining)
	}
	if p.remaining != rem {
		t.Fatalf("position should be untouched during whipsaw, remaining=%.3f", p.remaining)
	}
}

// New-position fallback ④: a losing position with no velocity history is treated
// as counter-trend (cut in full), not given a trend-aligned free pass.
func TestRolling_NewLosingPositionClassifiedCounter(t *testing.T) {
	g := rollGuard(4, 100, true, 0) // counter-only, full cut, preserve trend
	st := &guardState{}
	// fresh LONG, entry 100, now 95 (losing), NO pnl history.
	fresh := l3pos(100, 1, 1.0)
	applyRollingBreaker(g, []guardPos{{pos: fresh, price: 95}}, 250, st) // seed ref=250
	applyRollingBreaker(g, []guardPos{{pos: fresh, price: 95}}, 235, st) // -6% -> fire
	if fresh.remaining > 1e-9 {
		t.Fatalf("fresh losing position should be cut as counter-trend, remaining=%.3f", fresh.remaining)
	}
}

// Counter-only with keep: trend-aligned position trimmed lightly, counter in full.
func TestRolling_CounterFullTrendKeep(t *testing.T) {
	g := rollGuard(4, 100, true, 0.3) // counter 100%, trend 30%
	st := &guardState{}
	counter := l3posVel(100, 1, 1.0, -1.5, 8) // deteriorating
	trend := l3posVel(100, 1, 1.0, +1.5, 8)   // improving
	gps := []guardPos{{pos: counter, price: 100}, {pos: trend, price: 100}}
	applyRollingBreaker(g, gps, 250, st)
	applyRollingBreaker(g, gps, 235, st) // -6% fire
	if counter.remaining > 1e-9 {
		t.Fatalf("counter-trend should be fully cut, remaining=%.3f", counter.remaining)
	}
	// trend: cut 100%*0.3 = 30% of remaining -> 0.70 left
	if d := trend.remaining - 0.70; d > 1e-3 || d < -1e-3 {
		t.Fatalf("trend-aligned should keep 70%%, remaining=%.3f", trend.remaining)
	}
}

// Cascade protection: re-arm (a RECOVERY gate) must NOT block consecutive cuts
// during a continuing crash — if equity keeps falling it never reaches the arm
// floor, so each fresh -drop% from the pinned reference fires immediately. This
// is the key distinction from a cooldown (a TIME gate), which would lock out
// cuts for N bars even as a kept runner reverses post-spike. Verifies the live
// blind-spot fix: cooldown=0 + re-arm only.
func TestRolling_ReArmDoesNotBlockCascade(t *testing.T) {
	g := rollGuard(5, 30, false, 0) // whole-book, -5% per cut, 30% each (leaves remainder)
	g.L3RollReArmPct = 3             // recovery gate
	g.L3FireCooldownBars = 0        // no time gate
	st := &guardState{}
	p := l3pos(100, 1, 1.0)
	applyRollingBreaker(g, []guardPos{{pos: p, price: 100}}, 250, st) // seed ref=250
	// Continuous crash: -6% -> fire, ref->235, armFloor=235*1.03=242.05.
	n1, _ := applyRollingBreaker(g, []guardPos{{pos: p, price: 100}}, 235, st)
	if n1 != 1 {
		t.Fatalf("first cascade cut expected, got %d", n1)
	}
	rem1 := p.remaining
	// Still crashing (no recovery to armFloor): -6% from 235 -> 221 must fire AGAIN
	// immediately (cascade), cutting more of the remaining.
	n2, _ := applyRollingBreaker(g, []guardPos{{pos: p, price: 100}}, 221, st)
	if n2 != 1 {
		t.Fatalf("cascade cut #2 must fire during continuing crash (re-arm must not block), got %d", n2)
	}
	if p.remaining >= rem1 {
		t.Fatalf("cascade cut #2 should have reduced remaining further: %.3f -> %.3f", rem1, p.remaining)
	}
}

// rollATRGuard builds an ATR-normalized rolling guard.
func rollATRGuard(atrMult, cut float64, counterOnly bool, keep float64) GuardParams {
	return GuardParams{
		Enabled: true, L3Enabled: true, StartCapital: 250,
		L3Mode: "rolling", L3RollATRMult: atrMult, L3RollCutPct: cut,
		L3RollCounterOnly: counterOnly, L3TrendKeepMult: keep, L3VelWindow: 6,
	}
}

// l3posATR builds a position with a known ATR% (at entry) for ATR-trigger tests.
func l3posATR(entry, qty, remaining, atrPct float64) *simPos {
	sp := l3pos(entry, qty, remaining)
	sp.atrPct = atrPct
	return sp
}

// ATR trigger: a book under 1 ATR adverse must NOT fire at atrMult=1.5; only when
// the adverse move crosses 1.5 ATR does it cut. This is the leverage-free trigger:
// the same 1%/ATR move fires identically regardless of equity leverage.
func TestRollingATR_FiresOnATRUnitsNotEquity(t *testing.T) {
	g := rollATRGuard(1.5, 100, false, 0)
	st := &guardState{}
	// entry 100, atrPct 2% => 1 ATR = 2 price%. price 99 = -1% = 0.5 ATR adverse.
	p := l3posATR(100, 1, 1.0, 2.0)
	n, _ := applyRollingBreaker(g, []guardPos{{pos: p, price: 99}}, 200, st)
	if n != 0 {
		t.Fatalf("0.5 ATR adverse < 1.5 mult must not fire, got %d", n)
	}
	// price 97 = -3% = 1.5 ATR adverse => fire.
	n2, _ := applyRollingBreaker(g, []guardPos{{pos: p, price: 97}}, 200, st)
	if n2 != 1 {
		t.Fatalf("1.5 ATR adverse must fire, got %d", n2)
	}
	if p.remaining > 1e-9 {
		t.Fatalf("whole-book cut100 should fully close, remaining=%.3f", p.remaining)
	}
}

// Leverage invariance: the SAME 2-ATR adverse move fires identically whether the
// account equity barely moved or collapsed — the trigger ignores equity entirely.
func TestRollingATR_LeverageInvariant(t *testing.T) {
	g := rollATRGuard(1.5, 50, false, 0)
	// low-leverage account: equity almost unchanged. high-leverage: equity halved.
	for _, eq := range []float64{199.0, 100.0} {
		st := &guardState{}
		p := l3posATR(100, 1, 1.0, 2.0)
		n, _ := applyRollingBreaker(g, []guardPos{{pos: p, price: 96}}, eq, st) // -4% = 2 ATR
		if n != 1 {
			t.Fatalf("2 ATR adverse must fire regardless of equity %.0f, got %d", eq, n)
		}
	}
}

// Notional weighting: a tiny deeply-underwater position can't drag the whole book
// over the threshold; the weighted-ATR is dominated by the large position.
func TestRollingATR_NotionalWeighted(t *testing.T) {
	g := rollATRGuard(1.5, 100, false, 0)
	st := &guardState{}
	// big position (qty 100) only 0.5 ATR under; tiny position (qty 1) 5 ATR under.
	big := l3posATR(100, 100, 1.0, 2.0)  // price 99 => -1% => 0.5 ATR
	tiny := l3posATR(100, 1, 1.0, 2.0)   // price 90 => -10% => 5 ATR
	gps := []guardPos{{pos: big, price: 99}, {pos: tiny, price: 90}}
	n, _ := applyRollingBreaker(g, gps, 200, st)
	// weighted = (0.5*10000 + 5*100) / (10000+100) ~= 0.54 ATR < 1.5 => no fire.
	if n != 0 {
		t.Fatalf("notional-weighted ATR ~0.54 < 1.5 must not fire, got %d", n)
	}
}

// Overshoot scaling: a book deep past the threshold cuts proportionally more,
// capped at L3RollScaleCap.
func TestRollingATR_OvershootScaling(t *testing.T) {
	g := rollATRGuard(1.5, 20, false, 0)
	g.L3RollScaleCap = 3
	st := &guardState{}
	// price 91 = -9% = 4.5 ATR = 3x the 1.5 mult => scale capped at 3 => cut 60%.
	p := l3posATR(100, 1, 1.0, 2.0)
	applyRollingBreaker(g, []guardPos{{pos: p, price: 91}}, 200, st)
	if d := p.remaining - 0.40; d > 1e-3 || d < -1e-3 {
		t.Fatalf("expected 60%% scaled cut -> remaining 0.40, got %.4f", p.remaining)
	}
}

// ATR-unit whipsaw guard: after a fire, a slight further deepening that is below
// the +ReArmPct%% ATR floor must NOT re-fire; only a deeper crash re-arms.
func TestRollingATR_ReArmBlocksWhipsaw(t *testing.T) {
	g := rollATRGuard(1.5, 30, false, 0)
	g.L3RollReArmPct = 30 // need +30% deepening in ATR to re-fire
	st := &guardState{}
	p := l3posATR(100, 1, 1.0, 2.0)
	// price 96 = -4% = 2.0 ATR => fire, lastFireATR=2.0, floor=2.6 ATR.
	n1, _ := applyRollingBreaker(g, []guardPos{{pos: p, price: 96}}, 200, st)
	if n1 != 1 {
		t.Fatalf("first fire expected, got %d", n1)
	}
	rem := p.remaining
	// price 95 = -5% = 2.5 ATR < 2.6 floor => blocked.
	n2, _ := applyRollingBreaker(g, []guardPos{{pos: p, price: 95}}, 195, st)
	if n2 != 0 {
		t.Fatalf("2.5 ATR < 2.6 re-arm floor must block, got %d", n2)
	}
	if p.remaining != rem {
		t.Fatalf("position should be untouched during ATR whipsaw, remaining=%.3f", p.remaining)
	}
	// price 92 = -8% = 4.0 ATR > 2.6 floor => re-fires (continuing crash).
	n3, _ := applyRollingBreaker(g, []guardPos{{pos: p, price: 92}}, 184, st)
	if n3 != 1 {
		t.Fatalf("4.0 ATR deepening past floor must re-fire, got %d", n3)
	}
}

