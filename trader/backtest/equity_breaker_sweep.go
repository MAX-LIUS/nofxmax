package backtest

import "sort"

// EquityBreakerRow is one evaluated L3 equity-circuit-breaker config plus its
// drawdown-cut / PnL-cost score against the guard-off baseline.
type EquityBreakerRow struct {
	Guard   GuardParams
	Result  SimResult
	DDCut   float64 // baseline.MaxPortfolioDD - result.MaxPortfolioDD
	PnLCost float64 // baseline.TotalPnL - result.TotalPnL
	Score   float64 // DDCut - PnLCost (USD-equivalent net benefit)
}

// L3Grid enumerates equity-drawdown circuit-breaker configs to sweep. The base
// ladder is the cross-validated 6/12/20 -> 30/50/75 staged cut; on top of it the
// grid varies the counter-trend-first refinement (cut deteriorating positions,
// preserve trend-aligned ones at L3TrendKeepMult) and the early rate-based
// trigger (fire when equity plunges faster than L3EquityVelTrigger %/bar past a
// small drawdown floor). This isolates how much the velocity refinements add
// over the flat "cut everything" baseline on real entries.
func L3Grid(capital float64) []GuardParams {
	baseTiers := []L3Tier{{6, 30}, {12, 50}, {20, 75}}
	mk := func(keep, velTrig, floor float64, win int) GuardParams {
		return GuardParams{
			Enabled:            true,
			L3Enabled:          true,
			StartCapital:       capital,
			L3Tiers:            baseTiers,
			L3TrendKeepMult:    keep,
			L3VelWindow:        win,
			L3CounterVelEps:    0.0,
			L3EquityVelTrigger: velTrig,
			L3EarlyFloorPct:    floor,
		}
	}
	return []GuardParams{
		mk(1.0, 0, 0, 6),  // flat: cut everything equally, no early trigger
		mk(0.0, 0, 0, 6),  // counter-trend-first, fully preserve trend, no early
		mk(0.3, 0, 0, 6),  // counter-trend-first, lightly trim trend (keep .3)
		mk(0.5, 0, 0, 6),  // counter-trend-first, trim trend (keep .5)
		mk(0.0, 2, 2, 6),  // ct-first + early trigger 2%/bar, floor 2%
		mk(0.0, 3, 3, 6),  // ct-first + early trigger 3%/bar, floor 3%
		mk(0.0, 4, 2, 6),  // ct-first + early trigger 4%/bar, floor 2%
		mk(0.3, 3, 2, 6),  // ct-first + early + light trend trim
		mk(0.0, 3, 2, 4),  // shorter velocity window (4 bars)
		mk(0.0, 3, 2, 10), // longer velocity window (10 bars)
		// --- Rolling-baseline variants (no tiers/latch/re-arm) ---
		// whole-book: trim X% of EVERY remaining position each -drop%
		mkRoll(capital, 4, 20, false, 0, 6),  // every -4% -> cut 20% of remaining (whole book)
		mkRoll(capital, 4, 30, false, 0, 6),  // every -4% -> cut 30%
		mkRoll(capital, 6, 30, false, 0, 6),  // every -6% -> cut 30%
		mkRoll(capital, 3, 25, false, 0, 6),  // every -3% -> cut 25% (tighter)
		mkRoll(capital, 6, 50, false, 0, 6),  // every -6% -> cut 50% (aggressive)
		// counter-only: cut counter-trend in full, keep trend (TrendKeepMult)
		mkRoll(capital, 4, 50, true, 0, 6),   // every -4% -> counter cut 50%, keep trend
		mkRoll(capital, 4, 100, true, 0, 6),  // every -4% -> counter cut 100%, keep trend
		mkRoll(capital, 6, 100, true, 0, 6),  // every -6% -> counter cut 100%, keep trend
		mkRoll(capital, 4, 100, true, 0.3, 6), // counter 100%, trend trimmed .3
	}
}

// mkRoll builds a rolling-baseline L3 config: cut cutPct of (counter or all)
// positions each time equity drops dropPct below the rolling reference.
func mkRoll(capital, dropPct, cutPct float64, counterOnly bool, keep float64, win int) GuardParams {
	return GuardParams{
		Enabled:           true,
		L3Enabled:         true,
		StartCapital:      capital,
		L3Mode:            "rolling",
		L3RollDropPct:     dropPct,
		L3RollCutPct:      cutPct,
		L3RollCounterOnly: counterOnly,
		L3TrendKeepMult:   keep,
		L3VelWindow:       win,
		L3CounterVelEps:   0.0,
	}
}

// mkRollATR builds an ATR-normalized rolling breaker: fire on the portfolio
// weighted adverse move in ATR units (atrMult) instead of equity drop%. The cut
// policy (counter-only / keep / cut size) is identical to mkRoll.
func mkRollATR(capital, atrMult, cutPct float64, counterOnly bool, keep float64, win int) GuardParams {
	return GuardParams{
		Enabled:           true,
		L3Enabled:         true,
		StartCapital:      capital,
		L3Mode:            "rolling",
		L3RollATRMult:     atrMult,
		L3RollCutPct:      cutPct,
		L3RollCounterOnly: counterOnly,
		L3TrendKeepMult:   keep,
		L3VelWindow:       win,
		L3CounterVelEps:   0.0,
	}
}

// l3VariantName renders a swept L3 config's refinement settings for the report.
func l3VariantName(g GuardParams) string {
	if !g.L3Enabled || len(g.L3Tiers) == 0 && g.L3Mode != "rolling" {
		return "off"
	}
	if g.L3Mode == "rolling" {
		pol := "all"
		if g.L3RollCounterOnly {
			pol = "ct"
			if g.L3TrendKeepMult > 0 {
				pol = "ct+k" + itoa(int(g.L3TrendKeepMult*100))
			}
		}
		if g.L3RollATRMult > 0 {
			// ATR-normalized trigger: render the ATR multiple (×10 for one decimal).
			return "ATR " + itoa(int(g.L3RollATRMult*10)) + "/10 /cut" + itoa(int(g.L3RollCutPct)) + "% " + pol +
				" sc" + itoa(int(g.L3RollScaleCap)) + " ra" + itoa(int(g.L3RollReArmPct))
		}
		return "ROLL -" + itoa(int(g.L3RollDropPct)) + "%/cut" + itoa(int(g.L3RollCutPct)) + "% " + pol +
			" cd" + itoa(g.L3FireCooldownBars) + " ra" + itoa(int(g.L3RollReArmPct))
	}
	keep := "ct-first"
	if g.L3TrendKeepMult >= 1.0 {
		keep = "flat"
	} else if g.L3TrendKeepMult > 0 {
		keep = "keep" + itoa(int(g.L3TrendKeepMult*100)) + "%"
	}
	early := "noearly"
	if g.L3EquityVelTrigger > 0 {
		early = "vel" + itoa(int(g.L3EquityVelTrigger)) + "/fl" + itoa(int(g.L3EarlyFloorPct))
	}
	return keep + " " + early + " w" + itoa(g.L3VelWindow)
}

// l3Name returns the readable ladder name for a swept L3 config (matches L3Grid).
func l3Name(g GuardParams) string {
	if !g.L3Enabled || len(g.L3Tiers) == 0 {
		return "off"
	}
	s := ""
	for i, t := range g.L3Tiers {
		if i > 0 {
			s += " "
		}
		s += sprintTier(t)
	}
	return s
}

func sprintTier(t L3Tier) string {
	return itoa(int(t.DrawdownPct)) + "%->" + itoa(int(t.ClosePct)) + "%"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// SweepEquityBreaker runs the guard-off baseline then every L3 config in grid
// over the prepared entries, returning the baseline and rows sorted by Score.
func SweepEquityBreaker(grid []GuardParams, loaded []loadedEntry) (SimResult, []EquityBreakerRow) {
	p := ClaudeBaselineParams()
	base := RunPortfolioSim(p, GuardParams{}, loaded)
	rows := make([]EquityBreakerRow, 0, len(grid))
	for _, g := range grid {
		r := RunPortfolioSim(p, g, loaded)
		ddCut := base.MaxPortfolioDD - r.MaxPortfolioDD
		pnlCost := base.TotalPnL - r.TotalPnL
		rows = append(rows, EquityBreakerRow{
			Guard: g, Result: r, DDCut: ddCut, PnLCost: pnlCost, Score: ddCut - pnlCost,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Score > rows[j].Score })
	return base, rows
}

// SweepEquityBreakerFromEntries prepares entries (fetching OKX history) then
// sweeps the L3 grid. Mirrors SweepGuardsFromEntries.
func SweepEquityBreakerFromEntries(entries []Entry, tf string, capital float64) (SimResult, []EquityBreakerRow, int, int) {
	loaded, skipped := PrepareEntries(entries, tf, OKXBars)
	if len(loaded) == 0 {
		return SimResult{}, nil, 0, skipped
	}
	base, rows := SweepEquityBreaker(L3RollingGrid(capital), loaded)
	return base, rows, len(loaded), skipped
}

// L3RollingGrid is the FOCUSED large sweep of the rolling-baseline breaker. It
// systematically varies drop% (trigger spacing), the counter-trend cut, and the
// trend-aligned keep multiplier (the "cut counter in full, trim trend lightly"
// idea the user wants validated), plus whole-book variants as a control. This is
// the grid run on long-period robust data to pick a config that generalizes
// rather than overfitting one trader's 30-day window.
func L3RollingGrid(capital float64) []GuardParams {
	var grid []GuardParams
	// Counter-full + trend-trim sweep: every drop% cut counter-trend at 100%,
	// trim trend-aligned at keepMult (0=fully preserve .. 0.5=half). This is the
	// V-bounce-defending policy: a counter-trend position (deteriorating) is
	// fully removed, a trend-aligned one is only lightly trimmed so a V-snap-back
	// still leaves most of the winner on.
	for _, drop := range []float64{3, 4, 5, 6, 8} {
		for _, keep := range []float64{0, 0.15, 0.3, 0.5} {
			grid = append(grid, mkRoll(capital, drop, 100, true, keep, 6))
		}
	}
	// Whole-book control: trim cut% of EVERY remaining position each drop%.
	for _, drop := range []float64{3, 4, 6} {
		for _, cut := range []float64{20, 30, 50} {
			grid = append(grid, mkRoll(capital, drop, cut, false, 0, 6))
		}
	}
	// Velocity-window sensitivity on the leading counter-full/keep30 config.
	for _, win := range []int{4, 10} {
		grid = append(grid, mkRoll(capital, 4, 100, true, 0.3, win))
	}
	// Governed variants: whipsaw cooldown + re-arm + gap scaling on the two
	// leading policies (whole-book cut30 and counter-full/keep30).
	for _, cd := range []int{0, 3, 6} {
		for _, rearm := range []float64{0, 2, 4} {
			g := mkRoll(capital, 4, 30, false, 0, 6)
			g.L3FireCooldownBars = cd
			g.L3RollReArmPct = rearm
			grid = append(grid, g)
		}
	}
	// Gap scaling on whole-book: deeper drops cut proportionally more.
	for _, cap := range []float64{2, 3} {
		g := mkRoll(capital, 4, 25, false, 0, 6)
		g.L3RollScaleCap = cap
		g.L3FireCooldownBars = 3
		grid = append(grid, g)
	}
	// Governed counter-full/keep30 (the user's preferred policy): compare cooldown
	// 0 vs 3 vs 6 WITH re-arm, on both -4% and the production -5% drop, to test
	// whether the re-arm floor alone (cd=0) handles whipsaw while preserving
	// cascade-crash protection (cd>0 blocks consecutive cuts when a kept runner
	// reverses post-spike — the live-flagged blind spot).
	for _, drop := range []float64{4, 5} {
		for _, cd := range []int{0, 3, 6} {
			g := mkRoll(capital, drop, 100, true, 0.3, 6)
			g.L3FireCooldownBars = cd
			g.L3RollReArmPct = 3
			grid = append(grid, g)
		}
	}
	// ATR-normalized trigger (leverage-free): fire on the portfolio weighted
	// adverse-move-in-ATR crossing atrMult, decoupling "should act" from leverage.
	// Sweep the trigger threshold (in ATR units) on the user's preferred policy
	// (counter-full / trend-keep30), plus a whole-book control, with overshoot
	// scaling and an ATR-unit re-arm floor for whipsaw.
	for _, mult := range []float64{1.0, 1.5, 2.0, 2.5} {
		for _, keep := range []float64{0, 0.3} {
			g := mkRollATR(capital, mult, 100, true, keep, 6)
			g.L3RollScaleCap = 2
			g.L3RollReArmPct = 30 // re-fire only when the book deepens +30% in ATR
			grid = append(grid, g)
		}
	}
	// Whole-book ATR control: trim cut% of every position when the book crosses mult ATR.
	for _, mult := range []float64{1.5, 2.0} {
		for _, cut := range []float64{25, 40} {
			g := mkRollATR(capital, mult, cut, false, 0, 6)
			g.L3RollScaleCap = 2
			g.L3RollReArmPct = 30
			grid = append(grid, g)
		}
	}
	return grid
}

// mkBreadth builds a breadth-breaker GuardParams in pnl%-giveback retrace mode.
func mkBreadth(capital float64, minPos int, frac, giveback, cut float64) GuardParams {
	return GuardParams{
		Enabled: true, BreadthEnabled: true, StartCapital: capital,
		BreadthMinPos: minPos, BreadthFrac: frac,
		BreadthGivebackPct: giveback, BreadthLoserCutPct: cut,
		L3VelWindow: 6,
	}
}

// mkBreadthATR builds a breadth-breaker in ATR-from-peak retrace mode.
func mkBreadthATR(capital float64, minPos int, frac, atrMult, cut float64) GuardParams {
	g := mkBreadth(capital, minPos, frac, 0, cut)
	g.BreadthUseATR = true
	g.BreadthATRMult = atrMult
	return g
}

// BreadthGrid sweeps the breadth-breaker design space: the quorum (min open
// positions for a "majority" to mean anything), the retrace fraction that fires
// the gate, and the retracement definition (peak-to-current giveback% vs adverse
// move in ATR units). Loser cut is fixed at 100% (full cut of the bleeding side)
// per the agreed design. A light cooldown variant guards against re-firing every
// bar inside one continuing reversal.
func BreadthGrid(capital float64) []GuardParams {
	var grid []GuardParams
	// pnl%-giveback retrace mode: a position counts as retracing when it has given
	// back >= giveback% from its own peak profit. Sweep quorum × frac × giveback.
	for _, minPos := range []int{3, 4} {
		for _, frac := range []float64{0.5, 0.6, 0.7} {
			for _, gb := range []float64{2, 3, 5} {
				grid = append(grid, mkBreadth(capital, minPos, frac, gb, 100))
			}
		}
	}
	// ATR-from-peak retrace mode (volatility-normalized): retracing when the
	// adverse move from peak exceeds atrMult ATRs. Sweep quorum × frac × atrMult.
	for _, minPos := range []int{3, 4} {
		for _, frac := range []float64{0.5, 0.6, 0.7} {
			for _, mult := range []float64{1.0, 1.5, 2.0} {
				grid = append(grid, mkBreadthATR(capital, minPos, frac, mult, 100))
			}
		}
	}
	// Cooldown variants on the central config to test whipsaw control.
	for _, cd := range []int{3, 6} {
		g := mkBreadth(capital, 3, 0.6, 3, 100)
		g.L3FireCooldownBars = cd
		grid = append(grid, g)
	}
	return grid
}

// BreadthGridFine is the narrow refinement sweep around the cross-validated
// leading region (min4 / f60% / ATR≈1.0). The coarse grid stepped frac by 0.1
// and ATR by 0.5, so the true optimum may sit between grid points; this steps
// frac by 0.05 and ATR by 0.1 to pin it down. ATR-from-peak retrace mode only
// (it led both the crash and trending boards); loser cut fixed at 100%.
func BreadthGridFine(capital float64) []GuardParams {
	var grid []GuardParams
	for _, minPos := range []int{3, 4} {
		for _, frac := range []float64{0.55, 0.60, 0.65, 0.70} {
			for _, mult := range []float64{0.8, 0.9, 1.0, 1.1, 1.2} {
				grid = append(grid, mkBreadthATR(capital, minPos, frac, mult, 100))
			}
		}
	}
	// Light cooldown probes on the leading region (whipsaw control without losing
	// cascade protection in a continuing reversal).
	for _, cd := range []int{3, 6} {
		g := mkBreadthATR(capital, 4, 0.60, 1.0, 100)
		g.L3FireCooldownBars = cd
		grid = append(grid, g)
	}
	return grid
}

// BreadthWhipsawGrid isolates the whipsaw levers behind the live deployed config
// (min4 / f70% / ATR0.9). The breaker fired 5× inside one V-shaped dip on
// 2026-06-26, cutting longs near a local bottom (velocity false-positive) then
// cutting the flipped shorts as price recovered. This grid holds the breadth gate
// fixed and sweeps ONLY the levers that govern that failure mode:
//   - BreadthVelEps: the ATR-normalized velocity threshold. Higher = less
//     sensitive to a single fast bar (the V-bottom insert). 1e9 disables the
//     velocity leg entirely, leaving ATR-from-peak as the sole retrace signal
//     (the "AND-relationship" / velocity-off candidate).
//   - L3FireCooldownBars: bars to suppress re-firing after a fire (stops the
//     cut-longs-then-cut-shorts double hit inside one reversal).
//
// The first row reproduces the LIVE config exactly (vel_eps=0.25, cd=0) so the
// table reads as live-vs-candidates.
func BreadthWhipsawGrid(capital float64) []GuardParams {
	var grid []GuardParams
	// Two anchors matching the ACTUAL deployed live configs (verified 2026-06-26):
	//   claude  : min3 / f70% / ATR1.5 / vel0.25 / cd0
	//   GPT & R : min4 / f70% / ATR2.0 / vel0.25 / cd0
	// We sweep the whipsaw levers (vel_eps, cooldown) behind EACH real anchor so
	// the table reads as live-vs-candidates for the actual fleet.
	anchors := []struct {
		name    string
		minPos  int
		frac    float64
		atrMult float64
	}{
		{"claude", 3, 0.70, 1.5},
		{"gpt_r", 4, 0.70, 2.0},
	}
	for _, a := range anchors {
		base := func() GuardParams { return mkBreadthATR(capital, a.minPos, a.frac, a.atrMult, 100) }
		// Live config first (vel_eps=0.25, no cooldown).
		{
			g := base()
			g.BreadthVelEps = 0.25
			g.L3FireCooldownBars = 0
			grid = append(grid, g)
		}
		// cooldown sweep at the live vel_eps (whipsaw suppression alone).
		for _, cd := range []int{2, 3, 6} {
			g := base()
			g.BreadthVelEps = 0.25
			g.L3FireCooldownBars = cd
			grid = append(grid, g)
		}
		// vel_eps sweep (cd=0): how much does desensitizing the velocity leg help?
		for _, eps := range []float64{0.45, 1e9} {
			g := base()
			g.BreadthVelEps = eps
			g.L3FireCooldownBars = 0
			grid = append(grid, g)
		}
		// combined: desensitized velocity + cd6.
		for _, eps := range []float64{0.45, 1e9} {
			g := base()
			g.BreadthVelEps = eps
			g.L3FireCooldownBars = 6
			grid = append(grid, g)
		}
	}
	return grid
}

// BreadthStrictGateGrid tests whether a STRICTER gate (higher quorum/frac/ATR
// mult) plus the cd6 whipsaw cooldown can deliver the same tail protection with
// far fewer firings — turning the breaker back into a rare true circuit breaker
// (fires in a genuine crash) instead of a daily risk-trimmer. The whipsaw study
// showed cd6 makes firing nearly cost-free; the open question is whether we can
// also fire LESS often without losing crash protection.
//
// Row 1 reproduces the recommended near-term config (live gate + cd6) as the
// reference; subsequent rows tighten quorum (min5), frac (0.8), and ATR-from-peak
// (1.3 / 1.5 = require a deeper correlated drawdown before firing). Velocity leg
// kept at the live 0.25 (it is the early-warning signal; the whipsaw study showed
// disabling it guts protection). All at cd6.
func BreadthStrictGateGrid(capital float64) []GuardParams {
	var grid []GuardParams
	mk := func(minPos int, frac, atrMult float64) GuardParams {
		g := mkBreadthATR(capital, minPos, frac, atrMult, 100)
		g.BreadthVelEps = 0.25
		g.L3FireCooldownBars = 6
		return g
	}
	// Reference: live gate + cd6.
	grid = append(grid, mk(4, 0.70, 0.9))
	// Tighten one lever at a time.
	grid = append(grid, mk(5, 0.70, 0.9))  // higher quorum
	grid = append(grid, mk(4, 0.80, 0.9))  // higher frac (need 80% retracing)
	grid = append(grid, mk(4, 0.70, 1.3))  // deeper ATR-from-peak
	grid = append(grid, mk(4, 0.70, 1.5))  // deeper still
	// Combined strictness.
	grid = append(grid, mk(5, 0.80, 1.3))
	grid = append(grid, mk(5, 0.80, 1.5))
	grid = append(grid, mk(4, 0.80, 1.5))
	grid = append(grid, mk(5, 0.70, 1.3))
	return grid
}

// BreadthAtrGrid isolates the atr_mult lever ALONE behind each real live anchor.
// Everything else is held at the deployed live value (vel_eps=0.25, cd=0); only
// the ATR-from-peak retrace depth moves. This answers "should atr_mult change?"
// without confounding it with velocity/cooldown.
//
//	claude  : min3 / f70% / vel0.25 / cd0 ; atr_mult swept around live 1.5
//	gpt_r   : min4 / f70% / vel0.25 / cd0 ; atr_mult swept around live 2.0
//
// The live point is included in each band so the table reads as live-vs-candidate.
func BreadthAtrGrid(capital float64) []GuardParams {
	var grid []GuardParams
	anchors := []struct {
		name   string
		minPos int
		frac   float64
		live   float64
		band   []float64
	}{
		{"claude", 3, 0.70, 1.5, []float64{1.0, 1.3, 1.5, 1.8, 2.0, 2.5}},
		{"gpt_r", 4, 0.70, 2.0, []float64{1.3, 1.5, 2.0, 2.5, 3.0, 3.5}},
	}
	for _, a := range anchors {
		for _, mult := range a.band {
			g := mkBreadthATR(capital, a.minPos, a.frac, mult, 100)
			g.BreadthVelEps = 0.25
			g.L3FireCooldownBars = 0
			grid = append(grid, g)
		}
	}
	return grid
}

// SweepBreadthAtrFromEntries sweeps ONLY atr_mult on real DB entries.
func SweepBreadthAtrFromEntries(entries []Entry, tf string, capital float64) (SimResult, []EquityBreakerRow, int, int) {
	loaded, skipped := PrepareEntries(entries, tf, OKXBars)
	if len(loaded) == 0 {
		return SimResult{}, nil, 0, skipped
	}
	base, rows := SweepEquityBreaker(BreadthAtrGrid(capital), loaded)
	return base, rows, len(loaded), skipped
}

// SweepBreadthAtrRobust runs the atr_mult-only sweep over long-period robust data.
func SweepBreadthAtrRobust(cfg RobustConfig, capital float64) (SimResult, []EquityBreakerRow, map[string]int, error) {
	merged, per, err := PrepareRobustPortfolioEntries(cfg)
	if err != nil {
		return SimResult{}, nil, per, err
	}
	base, rows := SweepEquityBreaker(BreadthAtrGrid(capital), merged)
	return base, rows, per, nil
}

// SweepBreadthRobust runs the breadth-breaker sweep over long-period robust data.
func SweepBreadthRobust(cfg RobustConfig, capital float64, fine bool) (SimResult, []EquityBreakerRow, map[string]int, error) {
	merged, per, err := PrepareRobustPortfolioEntries(cfg)
	if err != nil {
		return SimResult{}, nil, per, err
	}
	grid := BreadthGrid(capital)
	if fine {
		grid = BreadthGridFine(capital)
	}
	base, rows := SweepEquityBreaker(grid, merged)
	return base, rows, per, nil
}

// SweepBreadthFromEntries prepares real DB entries then sweeps the breadth grid.
func SweepBreadthFromEntries(entries []Entry, tf string, capital float64, fine bool) (SimResult, []EquityBreakerRow, int, int) {
	loaded, skipped := PrepareEntries(entries, tf, OKXBars)
	if len(loaded) == 0 {
		return SimResult{}, nil, 0, skipped
	}
	grid := BreadthGrid(capital)
	if fine {
		grid = BreadthGridFine(capital)
	}
	base, rows := SweepEquityBreaker(grid, loaded)
	return base, rows, len(loaded), skipped
}

// SweepBreadthWhipsawFromEntries sweeps ONLY the whipsaw-control levers (vel_eps,
// cooldown) behind the live breadth gate, on real DB entries. Row 1 is the live
// config so the table reads as live-vs-candidates.
func SweepBreadthWhipsawFromEntries(entries []Entry, tf string, capital float64) (SimResult, []EquityBreakerRow, int, int) {
	loaded, skipped := PrepareEntries(entries, tf, OKXBars)
	if len(loaded) == 0 {
		return SimResult{}, nil, 0, skipped
	}
	base, rows := SweepEquityBreaker(BreadthWhipsawGrid(capital), loaded)
	return base, rows, len(loaded), skipped
}

// SweepBreadthWhipsawRobust runs the whipsaw-lever sweep over long-period robust data.
func SweepBreadthWhipsawRobust(cfg RobustConfig, capital float64) (SimResult, []EquityBreakerRow, map[string]int, error) {
	merged, per, err := PrepareRobustPortfolioEntries(cfg)
	if err != nil {
		return SimResult{}, nil, per, err
	}
	base, rows := SweepEquityBreaker(BreadthWhipsawGrid(capital), merged)
	return base, rows, per, nil
}

// SweepBreadthStrictGateFromEntries sweeps the strict-gate+cd6 grid on real DB entries.
func SweepBreadthStrictGateFromEntries(entries []Entry, tf string, capital float64) (SimResult, []EquityBreakerRow, int, int) {
	loaded, skipped := PrepareEntries(entries, tf, OKXBars)
	if len(loaded) == 0 {
		return SimResult{}, nil, 0, skipped
	}
	base, rows := SweepEquityBreaker(BreadthStrictGateGrid(capital), loaded)
	return base, rows, len(loaded), skipped
}

// SweepBreadthStrictGateRobust runs the strict-gate+cd6 sweep over long-period robust data.
func SweepBreadthStrictGateRobust(cfg RobustConfig, capital float64) (SimResult, []EquityBreakerRow, map[string]int, error) {
	merged, per, err := PrepareRobustPortfolioEntries(cfg)
	if err != nil {
		return SimResult{}, nil, per, err
	}
	base, rows := SweepEquityBreaker(BreadthStrictGateGrid(capital), merged)
	return base, rows, per, nil
}

// SweepEquityBreakerRobust runs the rolling-baseline L3 sweep over long-period
func SweepEquityBreakerRobust(cfg RobustConfig, capital float64) (SimResult, []EquityBreakerRow, map[string]int, error) {
	merged, per, err := PrepareRobustPortfolioEntries(cfg)
	if err != nil {
		return SimResult{}, nil, per, err
	}
	base, rows := SweepEquityBreaker(L3RollingGrid(capital), merged)
	return base, rows, per, nil
}

// L3WinnerConfig returns the cross-validated production preset:
// tiers 6/12/20 -> 30/50/75, counter-trend-first with trend-aligned positions
// trimmed at 30% of the tier ratio (k30), early trigger at 3%/bar equity drop
// past a 2% drawdown floor (v3/f2), velocity window 6 bars (w6), with V-bounce
// protection: the early trigger needs 2 consecutive plunge bars (c2) and a
// 3-bar cooldown (cd3) between firings so a single down-wick can't cut at a low.
func L3WinnerConfig(capital float64) GuardParams {
	return GuardParams{
		Enabled:            true,
		L3Enabled:          true,
		StartCapital:       capital,
		L3Tiers:            []L3Tier{{6, 30}, {12, 50}, {20, 75}},
		L3TrendKeepMult:    0.3,
		L3VelWindow:        6,
		L3CounterVelEps:    0.0,
		L3EquityVelTrigger: 3,
		L3EarlyFloorPct:    2,
		L3EarlyConfirmBars: 2,
		L3FireCooldownBars: 3,
	}
}

// TraceEquityBreakerFromEntries prepares entries then runs the production L3
// preset with tracing on, returning the baseline result, the L3 result, and the
// per-firing trace. Used by the trace CLI.
func TraceEquityBreakerFromEntries(entries []Entry, tf string, capital float64) (base SimResult, l3 SimResult, trace []L3FireEvent, prepared, skipped int) {
	loaded, sk := PrepareEntries(entries, tf, OKXBars)
	if len(loaded) == 0 {
		return SimResult{}, SimResult{}, nil, 0, sk
	}
	p := ClaudeBaselineParams()
	base = RunPortfolioSim(p, GuardParams{}, loaded)
	l3, trace = RunPortfolioSimTrace(p, L3WinnerConfig(capital), loaded)
	return base, l3, trace, len(loaded), sk
}
