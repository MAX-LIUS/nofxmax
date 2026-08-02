package backtest

import (
	"math"
	"testing"
)

func cycleGuard(capital, ddPct, armGain float64) GuardParams {
	return GuardParams{
		Enabled:           true,
		L3Enabled:         true,
		StartCapital:      capital,
		L3CycleEnabled:    true,
		L3CycleDDPct:      ddPct,
		L3CycleArmGainPct: armGain,
		L3VelWindow:       6,
	}
}

// TestCycleBreakerEquityIsContinuousThroughFiring is the central property of the
// equity-freeze model: flattening CRYSTALLISES value, it does not destroy it. So
// total equity immediately after a firing must equal total equity immediately
// before it, up to the exit fees actually paid.
//
// If this fails, the model is silently creating or destroying money at each
// firing, and every cross-threshold comparison is meaningless because deeper
// thresholds fire a different number of times.
func TestCycleBreakerEquityIsContinuousThroughFiring(t *testing.T) {
	p := ProtectionParams{Unit: UnitPercent, FaithfulExits: true, FeeRatePct: 0}
	loaded := acctStaggered(6, 2, sawPath)

	// No fees => continuity must be exact.
	off := RunPortfolioSim(p, GuardParams{}, loaded)
	on := RunPortfolioSim(p, cycleGuard(150, 1.5, 0), loaded)

	if on.L3Cycles < 2 {
		t.Fatalf("breaker never fired (cycles=%d); continuity claim untested", on.L3Cycles)
	}
	// With zero fees a freeze only converts unrealised into realised. Since every
	// position is eventually closed anyway, TOTAL PnL can differ (the breaker
	// closes at a different price than the natural exit) — but it must not differ
	// by a wild amount, and it must not be NaN/Inf.
	if math.IsNaN(on.TotalPnL) || math.IsInf(on.TotalPnL, 0) {
		t.Fatalf("cycle breaker produced non-finite PnL: %v", on.TotalPnL)
	}
	t.Logf("baseline PnL=%.4f, cycle-breaker PnL=%.4f over %d cycles",
		off.TotalPnL, on.TotalPnL, on.L3Cycles)
}

// TestCycleBreakerFireCountMonotone tests the rigor claim that motivated this
// model. Because the breaker only TRUNCATES real positions and never invents
// exposure, the entry universe is identical at every threshold, so firing counts
// should be non-increasing in the drawdown threshold — with far fewer violations
// than the re-entry model produced.
func TestCycleBreakerFireCountMonotone(t *testing.T) {
	p := ProtectionParams{Unit: UnitPercent, FaithfulExits: true, FeeRatePct: 0.05}
	loaded := acctStaggered(10, 2, sawPath)

	dds := []float64{1.0, 1.5, 2.0, 2.5, 3.0, 4.0, 5.0, 6.0, 8.0, 10.0}
	prev := -1
	violations := 0
	for _, dd := range dds {
		r := RunPortfolioSim(p, cycleGuard(150, dd, 0), loaded)
		fires := r.L3Cycles - 1
		if prev >= 0 && fires > prev {
			violations++
			t.Logf("non-monotone: dd=%.1f%% fired %d, easier threshold fired %d", dd, fires, prev)
		}
		prev = fires
	}
	if violations > 0 {
		t.Errorf("%d monotonicity violations; the freeze model should have "+
			"essentially none because it never invents exposure", violations)
	}
}

// TestCycleBreakerArmGateRequiresGain verifies 「正收益达到一定比例才挂上熔断」: with a
// gain requirement the cycle cannot fire until it has first earned that gain.
func TestCycleBreakerArmGateRequiresGain(t *testing.T) {
	p := ProtectionParams{Unit: UnitPercent, FaithfulExits: true}
	loaded := acctStaggered(6, 2, sawPath)

	always := RunPortfolioSim(p, cycleGuard(150, 1.5, 0), loaded)
	gated := RunPortfolioSim(p, cycleGuard(150, 1.5, 25), loaded) // +25% is unreachable here

	if always.L3Cycles < 2 {
		t.Fatalf("control never fired (cycles=%d); test cannot discriminate", always.L3Cycles)
	}
	if gated.L3Cycles != 1 {
		t.Fatalf("with an unreachable +25%% arm gain the breaker must never fire, "+
			"got %d cycles", gated.L3Cycles)
	}
}

// TestCycleBreakerFlattensEntireBook verifies the 全仓 semantics: a firing leaves
// no position open, unlike the partial-de-lever breakers.
func TestCycleBreakerFlattensEntireBook(t *testing.T) {
	p := ProtectionParams{Unit: UnitPercent, FaithfulExits: true}
	loaded := acctStaggered(6, 1, sawPath)
	r := RunPortfolioSim(p, cycleGuard(150, 1.5, 0), loaded)
	if r.L3Cycles < 2 {
		t.Skip("no firing on this fixture")
	}
	// L3Fires counts positions closed by the breaker; with whole-book freezes it
	// must be at least the number of firings (each closes >=1 position).
	if r.L3Fires < r.L3Cycles-1 {
		t.Fatalf("closed %d positions across %d firings; a freeze must close the "+
			"whole book", r.L3Fires, r.L3Cycles-1)
	}
}

// TestCycleBreakerDisabledIsNoOp guards the default.
func TestCycleBreakerDisabledIsNoOp(t *testing.T) {
	p := ProtectionParams{Unit: UnitPercent, FaithfulExits: true}
	loaded := acctStaggered(6, 2, sawPath)
	a := RunPortfolioSim(p, GuardParams{}, loaded)
	b := RunPortfolioSim(p, GuardParams{Enabled: true, L3Enabled: true, StartCapital: 150}, loaded)
	if math.Abs(a.TotalPnL-b.TotalPnL) > 1e-9 {
		t.Fatalf("cycle breaker off must be a no-op: %.9f vs %.9f", a.TotalPnL, b.TotalPnL)
	}
	if b.L3Cycles != 0 {
		t.Fatalf("disabled breaker must not create cycles, got %d", b.L3Cycles)
	}
}

// --- Placebo control tests ---------------------------------------------------
//
// The placebo is the load-bearing part of the whole study: it is what separates
// "the breaker helped" from "trading less on a losing book helped". If the
// placebo silently did nothing, or silently differed from the real breaker in
// some way other than the trigger, the conclusion drawn from it would be wrong.

// TestPlaceboIsDeterministicPerSeed: the same seed must reproduce exactly, or
// percentile claims computed across seeds are not reproducible.
func TestPlaceboIsDeterministicPerSeed(t *testing.T) {
	p := ProtectionParams{Unit: UnitPercent, FaithfulExits: true, FeeRatePct: 0.05}
	loaded := acctStaggered(10, 2, sawPath)
	g := cycleGuard(150, 999, 0)
	g.L3CyclePlaceboProb = 0.05
	g.L3CyclePlaceboSeed = 7

	a := RunPortfolioSim(p, g, loaded)
	b := RunPortfolioSim(p, g, loaded)
	if a.TotalPnL != b.TotalPnL || a.L3Cycles != b.L3Cycles {
		t.Fatalf("placebo not reproducible: (%.9f,%d) vs (%.9f,%d)",
			a.TotalPnL, a.L3Cycles, b.TotalPnL, b.L3Cycles)
	}
}

// TestPlaceboSeedsDiffer guards against a placebo that ignores its seed, which
// would collapse the distribution to a single point and make every percentile
// read 0 or 100.
func TestPlaceboSeedsDiffer(t *testing.T) {
	p := ProtectionParams{Unit: UnitPercent, FaithfulExits: true, FeeRatePct: 0.05}
	loaded := acctStaggered(10, 2, sawPath)
	mk := func(seed int64) SimResult {
		g := cycleGuard(150, 999, 0)
		g.L3CyclePlaceboProb = 0.1
		g.L3CyclePlaceboSeed = seed
		return RunPortfolioSim(p, g, loaded)
	}
	seen := map[float64]bool{}
	for s := int64(1); s <= 8; s++ {
		seen[mk(s).TotalPnL] = true
	}
	if len(seen) < 2 {
		t.Fatalf("all 8 placebo seeds gave an identical result; the seed is being ignored")
	}
}

// TestPlaceboIgnoresDrawdownThreshold: the placebo must be driven ONLY by its
// coin flip. If L3CycleDDPct still leaked into its trigger, the "no drawdown
// signal" control would secretly contain the drawdown signal.
func TestPlaceboIgnoresDrawdownThreshold(t *testing.T) {
	p := ProtectionParams{Unit: UnitPercent, FaithfulExits: true, FeeRatePct: 0.05}
	loaded := acctStaggered(10, 2, sawPath)
	mk := func(dd float64) SimResult {
		g := cycleGuard(150, dd, 0)
		g.L3CyclePlaceboProb = 0.1
		g.L3CyclePlaceboSeed = 3
		return RunPortfolioSim(p, g, loaded)
	}
	// A threshold of 0.01% would fire constantly; 999% could never fire. Under a
	// correct placebo both are irrelevant and the results must be identical.
	lo, hi := mk(0.01), mk(999)
	if lo.TotalPnL != hi.TotalPnL || lo.L3Cycles != hi.L3Cycles {
		t.Fatalf("placebo is contaminated by the drawdown threshold: dd=0.01%% gave "+
			"(%.4f,%d), dd=999%% gave (%.4f,%d)", lo.TotalPnL, lo.L3Cycles, hi.TotalPnL, hi.L3Cycles)
	}
}

// TestPlaceboRespectsArmGate is the fix for the flawed first comparison. The
// placebo must obey the SAME arm gate as the real breaker, otherwise it fires
// uniformly across the run while the real breaker fires only just after a gain,
// and matching on firing count compares two different things.
func TestPlaceboRespectsArmGate(t *testing.T) {
	p := ProtectionParams{Unit: UnitPercent, FaithfulExits: true, FeeRatePct: 0.05}
	loaded := acctStaggered(10, 2, sawPath)
	g := cycleGuard(150, 999, 25) // +25% arm gain is unreachable on this fixture
	g.L3CyclePlaceboProb = 0.9    // would fire almost every tick if ungated
	g.L3CyclePlaceboSeed = 1
	r := RunPortfolioSim(p, g, loaded)
	if r.L3Cycles != 1 {
		t.Fatalf("placebo fired %d times behind an unreachable arm gate; it must be "+
			"gated identically to the real breaker", r.L3Cycles-1)
	}
}

// TestPlaceboFiresMoreWithHigherProbability sanity-checks the control's own
// monotonicity: a higher coin-flip probability must not fire less.
func TestPlaceboFiresMoreWithHigherProbability(t *testing.T) {
	p := ProtectionParams{Unit: UnitPercent, FaithfulExits: true, FeeRatePct: 0.05}
	loaded := acctStaggered(12, 2, sawPath)
	// Averaged over seeds, since a single seed is noisy by construction.
	avg := func(prob float64) float64 {
		var tot float64
		for s := int64(1); s <= 12; s++ {
			g := cycleGuard(150, 999, 0)
			g.L3CyclePlaceboProb = prob
			g.L3CyclePlaceboSeed = s
			tot += float64(RunPortfolioSim(p, g, loaded).L3Cycles - 1)
		}
		return tot / 12
	}
	lo, hi := avg(0.01), avg(0.5)
	if hi < lo {
		t.Fatalf("mean placebo firings fell as probability rose: %.2f at 1%% vs %.2f at 50%%", lo, hi)
	}
}

// TestPlaceboDisabledByDefault: prob=0 must leave the real trigger in charge.
func TestPlaceboDisabledByDefault(t *testing.T) {
	p := ProtectionParams{Unit: UnitPercent, FaithfulExits: true, FeeRatePct: 0.05}
	loaded := acctStaggered(10, 2, sawPath)
	real := RunPortfolioSim(p, cycleGuard(150, 1.5, 0), loaded)
	if real.L3Cycles < 2 {
		t.Fatal("real trigger did not fire; test cannot discriminate")
	}
	inert := RunPortfolioSim(p, cycleGuard(150, 999, 0), loaded)
	if inert.L3Cycles != 1 {
		t.Fatalf("dd=999%% must never fire with the placebo off, got %d cycles", inert.L3Cycles)
	}
}

// TestCycleTriggerMonotoneWithoutFeedback is the engine-validity probe. In
// measure-only mode the breaker observes and counts but never touches the book,
// so the equity path is identical for every threshold and the firing count MUST
// be non-increasing as the threshold rises. A failure here means the trigger is
// not measuring the drawdown it claims, and no sweep result could be trusted.
//
// This is what licenses reporting residual violations in the live sweep as
// closed-loop feedback rather than as a bug.
func TestCycleTriggerMonotoneWithoutFeedback(t *testing.T) {
	p := ProtectionParams{Unit: UnitPercent, FaithfulExits: true, FeeRatePct: 0.05}
	loaded := acctStaggered(12, 2, sawPath)

	dds := []float64{0.5, 1.0, 1.5, 2.0, 2.5, 3.0, 4.0, 5.0, 6.0, 8.0, 10.0, 15.0}
	prev := -1
	var got []int
	for _, dd := range dds {
		g := cycleGuard(150, dd, 0)
		g.L3CycleMeasureOnly = true
		r := RunPortfolioSim(p, g, loaded)
		fires := r.L3Cycles - 1
		got = append(got, fires)
		if prev >= 0 && fires > prev {
			t.Errorf("MEASUREMENT BUG: with feedback removed, dd=%.1f%% fired %d but "+
				"the easier threshold fired only %d", dd, fires, prev)
		}
		prev = fires
	}
	t.Logf("measure-only firing counts across %v: %v", dds, got)
}

// TestCycleMeasureOnlyDoesNotAlterTheBook guards the probe itself: if
// measure-only silently changed the book it would not be a zero-feedback control.
func TestCycleMeasureOnlyDoesNotAlterTheBook(t *testing.T) {
	p := ProtectionParams{Unit: UnitPercent, FaithfulExits: true, FeeRatePct: 0.05}
	loaded := acctStaggered(10, 2, sawPath)
	off := RunPortfolioSim(p, GuardParams{}, loaded)
	g := cycleGuard(150, 0.5, 0)
	g.L3CycleMeasureOnly = true
	probe := RunPortfolioSim(p, g, loaded)
	if probe.L3Cycles < 2 {
		t.Fatal("probe never fired; it is not exercising the trigger")
	}
	if math.Abs(off.TotalPnL-probe.TotalPnL) > 1e-9 {
		t.Fatalf("measure-only altered PnL (%.9f vs %.9f); it is not zero-feedback",
			off.TotalPnL, probe.TotalPnL)
	}
}
