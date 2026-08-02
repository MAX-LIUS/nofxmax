package backtest

import (
	"fmt"
	"sort"
)

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
		mkRoll(capital, 4, 20, false, 0, 6), // every -4% -> cut 20% of remaining (whole book)
		mkRoll(capital, 4, 30, false, 0, 6), // every -4% -> cut 30%
		mkRoll(capital, 6, 30, false, 0, 6), // every -6% -> cut 30%
		mkRoll(capital, 3, 25, false, 0, 6), // every -3% -> cut 25% (tighter)
		mkRoll(capital, 6, 50, false, 0, 6), // every -6% -> cut 50% (aggressive)
		// counter-only: cut counter-trend in full, keep trend (TrendKeepMult)
		mkRoll(capital, 4, 50, true, 0, 6),    // every -4% -> counter cut 50%, keep trend
		mkRoll(capital, 4, 100, true, 0, 6),   // every -4% -> counter cut 100%, keep trend
		mkRoll(capital, 6, 100, true, 0, 6),   // every -6% -> counter cut 100%, keep trend
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

// SweepAccountBreakerFromEntries runs the user-specified account-breaker study
// (L3AccountBreakerGrid) with FEES ENABLED at feeRatePct per side.
//
// Fees are non-optional here. The breaker produces no alpha of its own; it only
// re-times exposure, and every firing pays a full round trip on the whole book.
// Scored fee-free, "fire more often" is a free option and the sweep's ranking is
// meaningless. Both the baseline and every grid row are charged identically, so
// the comparison stays apples-to-apples.
// AccountBreakerMonotonicityViolation records one place where raising the
// drawdown threshold produced MORE breaker firings on identical data.
type AccountBreakerMonotonicityViolation struct {
	Policy      string  // policy signature the two rows share
	LowerDrop   float64 // easier threshold
	LowerFires  int
	HigherDrop  float64 // stricter threshold
	HigherFires int
}

// CheckAccountBreakerMonotonicity groups sweep rows by policy (everything except
// the drop threshold) and reports where firing counts RISE as the threshold
// rises. Interpreting the output requires care, and the distinction below is the
// whole reason this function exists:
//
//   - A closed-loop breaker is legitimately path-dependent. Firing changes the
//     book, which changes later equity, which changes later triggers. A stricter
//     threshold delays the first cut, so the two runs diverge and the stricter one
//     can encounter MORE later opportunities. Small violations (±1-3 firings) are
//     this effect, not a defect. Verified in TestMonotoneUnderZeroFeedback:
//     with cuts disabled the same sweep is perfectly monotone, which proves the
//     trigger itself measures correctly and the residual is feedback.
//
//   - A measurement bug looks completely different: order-of-magnitude jumps
//     (the earlier equity-series study produced 4 firings at 1.9% and 39 at 2.0%)
//     because the drawdown was measured against the SIMULATED path's own peak
//     while real returns were credited to it, so adjacent thresholds were scored
//     in incompatible universes.
//
// So violations are reported as a diagnostic with their magnitude, NOT as an
// automatic invalidation. Large or clustered ones mean stop and investigate.
//
// Comparison uses L3Events (firings), never L3Fires (per-position trims) — trims
// legitimately vary with book size and would produce spurious violations.
// acctDropOf returns the row's drawdown threshold regardless of which breaker
// model produced it: the cycle (equity-freeze) model carries it in L3CycleDDPct,
// the rolling model in L3RollDropPct. Monotonicity and ordering must be checked
// on the same axis for both, or the cycle sweep would silently be checked against
// an all-zero threshold and always report "OK".
func acctDropOf(g GuardParams) float64 {
	if g.L3CycleEnabled {
		return g.L3CycleDDPct
	}
	return g.L3RollDropPct
}

// acctArmOf returns the arm-gain requirement for either model.
func acctArmOf(g GuardParams) float64 {
	if g.L3CycleEnabled {
		return g.L3CycleArmGainPct
	}
	return g.L3ArmProfitPct
}

// AcctDropOf / AcctArmOf expose the model-agnostic axes for CLI rendering.
func AcctDropOf(g GuardParams) float64 { return acctDropOf(g) }
func AcctArmOf(g GuardParams) float64  { return acctArmOf(g) }

func CheckAccountBreakerMonotonicity(rows []EquityBreakerRow) []AccountBreakerMonotonicityViolation {
	type key struct {
		cut, keep, arm, margin, rollRearm, scale float64
		counterOnly                              bool
		minPos, cooldown, velWin                 int
	}
	groups := map[key][]EquityBreakerRow{}
	for _, r := range rows {
		g := r.Guard
		// Placebo rows are excluded: their trigger is a coin flip, so they have no
		// drawdown-threshold axis to be monotone along. Including them produced a
		// spurious "worst gap +76 firings" — the checker was lining up rows that all
		// carry the same inert dd=999% sentinel and differ only by random seed, then
		// reporting the seed-to-seed spread as a monotonicity violation.
		if g.L3CyclePlaceboProb > 0 {
			continue
		}
		k := key{
			cut: g.L3RollCutPct, keep: g.L3TrendKeepMult, arm: acctArmOf(g),
			margin: g.L3GateMarginPct, rollRearm: g.L3RollReArmPct,
			scale: g.L3RollScaleCap, counterOnly: g.L3RollCounterOnly,
			minPos: g.L3GateMinPos, cooldown: g.L3FireCooldownBars, velWin: g.L3VelWindow,
		}
		groups[k] = append(groups[k], r)
	}
	var out []AccountBreakerMonotonicityViolation
	for k, grp := range groups {
		sort.Slice(grp, func(i, j int) bool {
			return acctDropOf(grp[i].Guard) < acctDropOf(grp[j].Guard)
		})
		for i := 1; i < len(grp); i++ {
			prev, cur := grp[i-1], grp[i]
			if cur.Result.L3Events > prev.Result.L3Events {
				pol := "whole"
				if k.counterOnly {
					pol = "counter-first"
				}
				if cur.Guard.L3CycleEnabled {
					pol = "freeze"
				}
				out = append(out, AccountBreakerMonotonicityViolation{
					Policy: fmt.Sprintf("%s cut%.0f%% arm%.0f%% gate%d/%.0f%% cd%d",
						pol, k.cut, k.arm, k.minPos, k.margin, k.cooldown),
					LowerDrop: acctDropOf(prev.Guard), LowerFires: prev.Result.L3Events,
					HigherDrop: acctDropOf(cur.Guard), HigherFires: cur.Result.L3Events,
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Policy < out[j].Policy })
	return out
}

func SweepAccountBreakerFromEntries(entries []Entry, tf string, capital, nominalLeverage, feeRatePct float64, reentryBars int) (SimResult, []EquityBreakerRow, int, int) {
	return SweepAccountBreakerWithProvider(entries, tf, capital, nominalLeverage, feeRatePct, reentryBars, OKXBars)
}

// SweepAccountBreakerWithProvider is SweepAccountBreakerFromEntries with an
// explicit bar provider, so a Binance-executed trader replays on Binance bars
// rather than OKX ones. Replaying a book against a different exchange's prices
// introduces basis and different wick extremes, which is precisely the kind of
// silent data mismatch that invalidates a stop/breaker study.
func SweepAccountBreakerWithProvider(entries []Entry, tf string, capital, nominalLeverage, feeRatePct float64, reentryBars int, provider BarsProvider) (SimResult, []EquityBreakerRow, int, int) {
	return SweepAccountBreakerFull(entries, tf, capital, nominalLeverage, feeRatePct, reentryBars, false, provider)
}

// SweepAccountBreakerFull adds the faithful-exit switch. With faithful=true the
// baseline reproduces the trader's realized PnL (positions run to their real exit
// price, modelled SL/TP/BE/DD off), so the breaker is the only overlay and its
// measured effect is attributable to it rather than to a protection ladder the
// trader never used. See ProtectionParams.FaithfulExits.
func SweepAccountBreakerFull(entries []Entry, tf string, capital, nominalLeverage, feeRatePct float64, reentryBars int, faithful bool, provider BarsProvider) (SimResult, []EquityBreakerRow, int, int) {
	loaded, skipped := PrepareEntries(entries, tf, provider)
	if len(loaded) == 0 {
		return SimResult{}, nil, 0, skipped
	}
	p := ClaudeBaselineParams()
	if faithful {
		p = ProtectionParams{Unit: UnitPercent, FaithfulExits: true}
	}
	p.FeeRatePct = feeRatePct
	base := RunPortfolioSim(p, GuardParams{}, loaded)
	var grid []GuardParams
	if reentryBars == -2 {
		// Engine probe: cycle grid with feedback removed.
		grid = L3CycleGrid(capital, nominalLeverage)
		for i := range grid {
			grid[i].L3CycleMeasureOnly = true
		}
	} else if reentryBars < 0 {
		// reentryBars<0 selects the equity-freeze (cycle) model, which has no
		// re-entry axis by construction: it never invents exposure, so the flag
		// that configures fabricated re-entries is meaningless there.
		grid = L3CycleGrid(capital, nominalLeverage)
	} else {
		grid = L3AccountBreakerGrid(capital, nominalLeverage, reentryBars)
	}
	rows := make([]EquityBreakerRow, 0, len(grid))
	for _, g := range grid {
		r := RunPortfolioSim(p, g, loaded)
		rows = append(rows, EquityBreakerRow{
			Guard:   g,
			Result:  r,
			DDCut:   base.MaxPortfolioDD - r.MaxPortfolioDD,
			PnLCost: base.TotalPnL - r.TotalPnL,
			Score:   (base.MaxPortfolioDD - r.MaxPortfolioDD) - (base.TotalPnL - r.TotalPnL),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Score > rows[j].Score })
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

// L3AccountBreakerGrid is the study of the user-specified account-equity circuit
// breaker: 「仓位>2 或 保证金使用率达标 时，账户权益回撤 X% 全仓熔断；熔断后要正收益
// 达到一定比例才重新挂上熔断，否则各自仓位控制」.
//
// Differences from L3RollingGrid, all required by that spec:
//
//   - Drop band is 1.5%..4.0% in 0.25% steps. The existing grid starts at 3% and
//     never enters the band at all, so it could not answer the question.
//   - Whole-book flatten (CounterOnly=false, cut=100) is the PRIMARY policy,
//     because 「全仓熔断」 means flatten everything, not cut-losers-only. Partial
//     cuts (50%) and cut-losers-only are included as controls, so the sweep can
//     say whether full flattening is actually better than trimming.
//   - Exposure gate: MinPos=3 (「仓位大于2」) OR margin>=70%, leverage-aware at
//     the trader's nominal leverage.
//   - Two-state arm: L3ArmProfitPct over 0/1/2/3%, where 0 is the always-armed
//     control. This is the parameter the user's spec hinges on and the one most
//     likely to make the breaker inert.
//
// A caution carried over from the equity-series study: at ~3.7x effective
// leverage, a 1.5% equity drawdown is a ~0.4% price move — inside noise and
// tighter than any stop. The band is swept because the user asked for it, but
// the ATR-normalized variants in L3RollingGrid exist precisely because raw
// equity% is leverage-contaminated, and that caveat applies to every row here.
func L3AccountBreakerGrid(capital, nominalLeverage float64, reentryBars int) []GuardParams {
	var grid []GuardParams
	drops := []float64{1.5, 1.75, 2.0, 2.25, 2.5, 2.75, 3.0, 3.25, 3.5, 3.75, 4.0}
	arms := []float64{0, 1, 2, 3}

	withGate := func(g GuardParams, arm float64) GuardParams {
		g.L3GateMinPos = 3 // 「仓位大于2」
		g.L3GateMarginPct = 70
		g.L3NominalLeverage = nominalLeverage
		g.L3ArmProfitPct = arm
		// Re-entry ON by default for this study (「熔断将会降低损失并重新开仓」).
		// Without it a flatten is a permanent early exit: the breaker sheds all
		// later adverse moves and never pays to restore exposure, which flatters
		// every drawdown-reduction number. Re-entry is what makes the trade-off
		// honest — it pays a full round trip and re-enters at the post-flatten
		// price, so a breaker that fires into a V-bounce is correctly penalised.
		g.L3ReentryBars = reentryBars
		g.L3ReentryMaxCycles = 3
		return g
	}

	// Primary: whole-book flatten at each drop%, across arm thresholds.
	for _, d := range drops {
		for _, arm := range arms {
			grid = append(grid, withGate(mkRoll(capital, d, 100, false, 0, 6), arm))
		}
	}
	// Control A: half-book de-lever instead of a full flatten. Isolates "is
	// flattening everything necessary, or is trimming enough?"
	for _, d := range drops {
		grid = append(grid, withGate(mkRoll(capital, d, 50, false, 0, 6), 0))
	}
	// Control B: cut-losers-first policy (counter-trend full, trend kept 30%).
	// Isolates 全仓 vs 砍逆势留顺势 at the same trigger.
	for _, d := range drops {
		grid = append(grid, withGate(mkRoll(capital, d, 100, true, 0.3, 6), 0))
	}
	// Control C: no exposure gate, whole-book. Isolates the gate's contribution;
	// if these match the gated rows, the gate is not doing anything (expected,
	// since the book holds >=3 positions ~76% of the time).
	for _, d := range drops {
		g := mkRoll(capital, d, 100, false, 0, 6)
		g.L3ArmProfitPct = 0
		g.L3ReentryBars = reentryBars // keep re-entry identical; isolate the gate
		g.L3ReentryMaxCycles = 3
		grid = append(grid, g)
	}
	// Control D: cooldown on the leading band values, to test whipsaw sensitivity
	// at these very tight thresholds where re-firing is most likely.
	for _, d := range []float64{1.5, 2.0, 2.5, 3.0} {
		for _, cd := range []int{3, 6} {
			g := withGate(mkRoll(capital, d, 100, false, 0, 6), 0)
			g.L3FireCooldownBars = cd
			grid = append(grid, g)
		}
	}
	return grid
}

// ScaleEntrySizes returns a copy of the prepared entries with every position
// quantity multiplied by frac.
//
// This is the control that tests the user's own argument rather than a strawman of
// it. The argument is: "cutting exposure on a losing book must be positive
// expectancy." That is CORRECT, and this function is the cheapest possible way to
// act on it — it holds less risk with no threshold to choose, no parameter to fit,
// no extra round trips, and no extra fees.
//
// If simply trading smaller captures as much as the breaker does, then the breaker
// is an expensive, overfittable way to buy something a one-line size change buys
// outright, and the drawdown trigger is not the source of the benefit.
func ScaleEntrySizes(loaded []LoadedEntry, frac float64) []LoadedEntry {
	out := make([]LoadedEntry, len(loaded))
	for i, le := range loaded {
		cp := le
		cp.entry.Quantity = le.entry.Quantity * frac
		out[i] = cp
	}
	return out
}

// SizeScaleControl evaluates plain size reduction on the same train/test split the
// breaker was judged on, so the two are directly comparable.
type SizeScaleRow struct {
	Frac      float64
	TrainPnL  float64
	TestPnL   float64
	TrainBase float64
	TestBase  float64
}

func SweepSizeScaleSplit(p ProtectionParams, loaded []LoadedEntry, fracs []float64) []SizeScaleRow {
	train, test := SplitEntriesByTime(loaded)
	trainBase := RunPortfolioSim(p, GuardParams{}, train).TotalPnL
	testBase := RunPortfolioSim(p, GuardParams{}, test).TotalPnL
	var out []SizeScaleRow
	for _, f := range fracs {
		rt := RunPortfolioSim(p, GuardParams{}, ScaleEntrySizes(train, f))
		rv := RunPortfolioSim(p, GuardParams{}, ScaleEntrySizes(test, f))
		out = append(out, SizeScaleRow{
			Frac: f, TrainPnL: rt.TotalPnL, TestPnL: rv.TotalPnL,
			TrainBase: trainBase, TestBase: testBase,
		})
	}
	return out
}

// L3CycleFineGrid is the 0.1%-step sweep of the equity-freeze breaker, on RAW
// account equity with no leverage adjustment (equity 148 -> 146.52 is a 1% drop).
//
// It exists because the coarse grid never actually tested the configuration under
// discussion: arm was only {0,1,2,3,5,8,12}, so "+4% gain then 2% drawdown" was
// never run. Coarse grids that skip the point of interest are worthless for
// settling a disagreement about that point.
//
// The drawdown axis deliberately starts at 0.1% and steps by 0.1%. That is not
// padding: the limit dd->0 IS a pure take-profit at +arm%, with no retrace given
// back. If the goal is "don't let earned profit slip away", the take-profit limit
// should dominate any positive dd, because every basis point of dd is profit
// handed back by construction. Including the limit turns an opinion into a
// measurement.
//
// WARNING on interpretation: this grid has ~1200 cells, so the best cell is
// guaranteed to look good by selection alone. It must only ever be read together
// with the train/test split (SweepCycleFineSplit), never on its own.
func L3CycleFineGrid(capital, nominalLeverage float64) []GuardParams {
	var grid []GuardParams
	for ddI := 1; ddI <= 60; ddI++ { // 0.1% .. 6.0% by 0.1%
		for armI := 0; armI <= 100; armI += 5 { // 0% .. 10% by 0.5%
			grid = append(grid, GuardParams{
				Enabled: true, L3Enabled: true, StartCapital: capital,
				L3CycleEnabled:    true,
				L3CycleDDPct:      float64(ddI) / 10,
				L3CycleArmGainPct: float64(armI) / 10,
				L3GateMinPos:      3,
				L3GateMarginPct:   70,
				L3NominalLeverage: nominalLeverage,
				L3VelWindow:       6,
			})
		}
	}
	return grid
}

// SplitEntriesByTime splits prepared entries into two consecutive halves by entry
// time, for out-of-sample validation. Returns (train, test).
//
// Chronological, never random: a random split would leak, because positions open
// at the same time share the same market path, so a random test set would contain
// near-copies of train rows and report a fake pass.
func SplitEntriesByTime(loaded []LoadedEntry) ([]LoadedEntry, []LoadedEntry) {
	if len(loaded) < 4 {
		return loaded, nil
	}
	byTime := append([]LoadedEntry(nil), loaded...)
	sort.Slice(byTime, func(i, j int) bool {
		return byTime[i].entry.EntryTime < byTime[j].entry.EntryTime
	})
	mid := len(byTime) / 2
	return byTime[:mid], byTime[mid:]
}

// CycleSplitResult reports one config's train and test outcome.
type CycleSplitResult struct {
	Guard      GuardParams
	TrainPnL   float64
	TrainBase  float64
	TestPnL    float64
	TestBase   float64
	TrainFires int
	TestFires  int
}

// TrainEdge / TestEdge are improvements over the no-breaker baseline on each half.
func (c CycleSplitResult) TrainEdge() float64 { return c.TrainPnL - c.TrainBase }
func (c CycleSplitResult) TestEdge() float64  { return c.TestPnL - c.TestBase }

// SweepCycleFineSplit answers the only question that matters for deployment: does
// a config chosen on past data still help on data it was not chosen on?
//
// Every config is evaluated on BOTH halves. The caller can then compare the
// config that won the training half against how it did out-of-sample, and against
// what actually won out-of-sample. A rule with a real edge keeps most of it; a
// rule that was merely fitted loses it, and the size of the loss is the honest
// estimate of how much of the in-sample gain was selection.
func SweepCycleFineSplit(p ProtectionParams, loaded []LoadedEntry, capital, nominalLeverage float64) ([]CycleSplitResult, SimResult, SimResult) {
	train, test := SplitEntriesByTime(loaded)
	trainBase := RunPortfolioSim(p, GuardParams{}, train)
	testBase := RunPortfolioSim(p, GuardParams{}, test)

	grid := L3CycleFineGrid(capital, nominalLeverage)
	out := make([]CycleSplitResult, 0, len(grid))
	for _, g := range grid {
		// StartCapital must match the capital actually at risk in each half, or the
		// arm/drawdown percentages mean different things across the two halves.
		gt := g
		gt.StartCapital = capital
		rTrain := RunPortfolioSim(p, gt, train)
		rTest := RunPortfolioSim(p, gt, test)
		out = append(out, CycleSplitResult{
			Guard:      g,
			TrainPnL:   rTrain.TotalPnL,
			TrainBase:  trainBase.TotalPnL,
			TestPnL:    rTest.TotalPnL,
			TestBase:   testBase.TotalPnL,
			TrainFires: rTrain.L3Cycles - 1,
			TestFires:  rTest.L3Cycles - 1,
		})
	}
	return out, trainBase, testBase
}

// L3CycleGrid sweeps the equity-freeze (cycle) breaker: 熔断=权益价值冻结，之后开仓算新周期.
//
// Range width is deliberate. The 1.5-4% band was only an example, and it is far
// too narrow to contain an answer: raw account-equity drawdown% is roughly
// leverage x price-move%, so at claude's ~3.7x effective leverage a 1.5% equity
// drawdown is a ~0.4% price move — inside tick noise, tighter than any stop the
// book runs. To find out whether an optimum exists at all, the sweep has to reach
// up to where the threshold corresponds to a real adverse move (10-20% equity is
// a 3-5% price move), and it has to include a NO-BREAKER row so every result is
// read against the honest counterfactual rather than against its neighbours.
//
// The arm-gain axis is widened for the same reason: 「正收益达到一定比例」 gates the
// breaker on the cycle first earning a profit, and if that requirement is small
// the breaker is effectively always on, so the axis must extend far enough to
// show where it starts to bind.
func L3CycleGrid(capital, nominalLeverage float64) []GuardParams {
	drops := []float64{1.0, 1.5, 2.0, 2.5, 3.0, 3.5, 4.0, 5.0, 6.0, 8.0, 10.0, 12.0, 15.0, 20.0}
	arms := []float64{0, 1, 2, 3, 5, 8, 12}

	mk := func(dd, arm float64, minPos int, marginPct float64) GuardParams {
		return GuardParams{
			Enabled: true, L3Enabled: true, StartCapital: capital,
			L3CycleEnabled:    true,
			L3CycleDDPct:      dd,
			L3CycleArmGainPct: arm,
			L3GateMinPos:      minPos,
			L3GateMarginPct:   marginPct,
			L3NominalLeverage: nominalLeverage,
			L3VelWindow:       6,
		}
	}

	var grid []GuardParams
	// Primary: gated per the user spec (仓位>2 或 保证金使用率>=70%).
	for _, d := range drops {
		for _, a := range arms {
			grid = append(grid, mk(d, a, 3, 70))
		}
	}
	// Placebo control: identical machinery, drawdown trigger replaced by a coin
	// flip, swept over probabilities so the firing counts span the real rows' range
	// and 3 seeds each so the placebo's own dispersion is visible. This is the row
	// set that decides whether the drawdown trigger carries information: if a
	// random breaker at the same firing count does as well, the gain attributed to
	// "熔断" is really just the gain from holding less risk on a losing book.
	// 30 seeds per probability, not 3: the placebo's own spread across seeds turned
	// out to be as wide as the entire effect being measured (three seeds at ~38
	// firings spanned -22 to +47 PnL). With a spread that large, any single-seed
	// comparison is meaningless; only a distribution supports a percentile claim.
	// The placebo MUST sweep the same arm axis as the real rows. A first pass ran
	// every placebo at arm=0 and produced a badly misleading "54 of 90 configs beat
	// the coin flip": an arm=0 placebo is armed on 100% of ticks and so fires
	// uniformly across the run, whereas an arm=12% real row is armed on only ~3% of
	// ticks — the ones right after a gain. Matched on firing COUNT but not on firing
	// OPPORTUNITY, that comparison credits the drawdown trigger with what is really
	// the arm gate's profit-taking effect. Pairing at equal arm% isolates the only
	// question that matters: given the same armed state, does drawdown MAGNITUDE
	// pick better moments than a coin flip?
	for _, arm := range arms {
		for _, prob := range []float64{0.002, 0.005, 0.01, 0.02, 0.05, 0.1, 0.25} {
			for seed := int64(1); seed <= 12; seed++ {
				g := mk(0, arm, 3, 70)
				g.L3CycleDDPct = 999 // inert; the placebo trigger takes over
				g.L3CyclePlaceboProb = prob
				g.L3CyclePlaceboSeed = seed
				grid = append(grid, g)
			}
		}
	}
	// Control: no exposure gate. If these match the gated rows the gate is inert,
	// which is the expected outcome since the book holds >=3 positions most of the
	// time — worth showing rather than assuming.
	for _, d := range drops {
		grid = append(grid, mk(d, 0, 0, 0))
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
	grid = append(grid, mk(5, 0.70, 0.9)) // higher quorum
	grid = append(grid, mk(4, 0.80, 0.9)) // higher frac (need 80% retracing)
	grid = append(grid, mk(4, 0.70, 1.3)) // deeper ATR-from-peak
	grid = append(grid, mk(4, 0.70, 1.5)) // deeper still
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
