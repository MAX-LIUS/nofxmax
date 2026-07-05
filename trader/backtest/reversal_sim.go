package backtest

import (
	"sort"
	"strconv"
)

// ftoa formats a float compactly (3 significant digits) for sweep labels.
func ftoa(f float64) string {
	return strconv.FormatFloat(f, 'g', 3, 64)
}

// ReversalParams configures the trend-reversal / position-flip overlay. It
// implements the four-rule methodology synthesized from trend-following
// literature (Turtle exit, Wilder ADX/exhaustion, Moskowitz cross-sectional
// momentum, Parabolic-SAR age sensitivity):
//
//	Rule 1 (conviction): a reverse signal must clear a strength bar. In the
//	  mechanical backtest we have no AI confidence, so conviction is proxied by
//	  an EMA-cross flip in the opposite direction (the same signal the robust
//	  harness uses to open) — i.e. the trend model itself flipped.
//	Rule 2 (breadth): >= BreadthFrac of the open book must be retracing in the
//	  same direction as the challenged position (correlated reversal, not noise).
//	  Reuses isRetracing() so the live breaker and the flip share one definition.
//	Rule 3 (age): the position must be at least MinAgeBars old (no flips inside
//	  the first trend leg).
//	Rule 4 (exhaustion): the held trend must show exhaustion — EMA20 deviation
//	  reverting to the mean (|dev| < EMAExhaustionPct) OR an adverse move from
//	  peak exceeding ATRFromPeakMult ATRs.
//
// When all enabled rules pass and ReverseOpen is true, the original position is
// closed at the current bar and a synthetic reverse position is opened and
// tracked forward under the same ProtectionParams. When ReverseOpen is false
// (exhaustion-only mode) the original is closed but no reverse is opened.
type ReversalParams struct {
	Enabled bool

	// Rule gates (each <=0 / false disables that specific gate).
	RequireOppositeCross bool    // Rule 1: require an opposite EMA-cross at/near the bar
	BreadthFrac          float64 // Rule 2: fraction of book retracing same-direction (0 disables)
	BreadthMinPos        int     // Rule 2: quorum
	MinAgeBars           int     // Rule 3: minimum bars held before a flip is allowed
	EMAExhaustionPct     float64 // Rule 4a: |EMA20 deviation%| below this = exhausted (0 disables)
	ATRFromPeakMult      float64 // Rule 4b: adverse-from-peak in ATR units = exhausted (0 disables)

	// Retracement definition shared with the breaker (ATR-normalized velocity).
	UseATR        bool
	ATRMult       float64 // ATR-from-peak that counts as retracing (breadth leg)
	VelEps        float64 // ATR-normalized velocity threshold (breadth leg)
	VelWindow     int     // velocity look-back bars (0 => 6)

	// EMA cross parameters for Rule 1 (must match the entry signal generator).
	EMAFast int
	EMASlow int

	// Action.
	ReverseOpen bool // true: close + open reverse; false: exhaustion-only close

	// Safeguards.
	FlipCooldownBars int // after a flip, suppress re-flip on the same symbol for N bars
	MaxFlipsPerEntry int // cap flips originating from one original entry (0 => 1)
}

// ReversalResult extends a baseline run with flip-specific diagnostics so we can
// judge whether flipping adds value over holding the original to its exit.
type ReversalResult struct {
	SimResult

	Flips            int     // number of flip events executed
	ExhaustionCloses int     // exhaustion-only closes (no reverse opened)
	FlipWins         int     // reverse positions that closed profitable
	FlipPnL          float64 // total PnL attributable to reverse positions
	BaselineHeldPnL  float64 // counterfactual: PnL if originals were held to exit (no flip)
	OriginalCutPnL   float64 // realized PnL of originals at the flip point (early close)
}

// flipLive tracks one original entry plus any synthetic reverse spawned from it.
type flipLive struct {
	se          simEntry
	barIdx      map[int64]int
	pos         *simPos
	started     bool
	lastClose   float64
	flips       int
	lastFlipBar int
	// reverse position machine (nil until a flip occurs); tracked forward.
	rev *simPos
	// counterfactual baseline: a parallel clone of the original that is NEVER
	// flipped, so we can measure held-to-exit PnL for the same entry.
	baseline *simPos
}

// emaCrossDirAt returns the EMA-cross direction signalled at bar index i:
// +1 = bullish cross (fast crosses above slow), -1 = bearish cross, 0 = none.
func emaCrossDirAt(closes []float64, fast, slow, i int) int {
	if i < slow+1 || i >= len(closes) {
		return 0
	}
	ef := ema(closes, fast)
	es := ema(closes, slow)
	prevDiff := ef[i-1] - es[i-1]
	curDiff := ef[i] - es[i]
	if prevDiff <= 0 && curDiff > 0 {
		return 1
	}
	if prevDiff >= 0 && curDiff < 0 {
		return -1
	}
	return 0
}

// RunReversalSim replays the loaded entries and, at each bar, evaluates the
// four-rule flip methodology. When a flip fires it closes the original early
// and (if ReverseOpen) opens a synthetic reverse tracked forward under the same
// ProtectionParams. A parallel never-flipped baseline clone is kept per entry so
// the result reports the held-to-exit counterfactual alongside the flipped PnL.
func RunReversalSim(p ProtectionParams, rp ReversalParams, loaded []loadedEntry) ReversalResult {
	const indicatorWindow = 60
	velWindow := rp.VelWindow
	if velWindow <= 0 {
		velWindow = 6
	}

	// Precompute entry ATR per loaded entry (mirrors runPortfolioSim).
	sims := make([]simEntry, 0, len(loaded))
	for _, le := range loaded {
		atr := 0.0
		if le.entryIdx >= atrLookback {
			lo := le.entryIdx - indicatorWindow
			if lo < 0 {
				lo = 0
			}
			h, l, c := sliceOHLCRange(le.bars, lo, le.entryIdx-1)
			atr = wilderATR(h, l, c, atrLookback)
		}
		sims = append(sims, simEntry{loaded: le, atr: atr})
	}

	// Master clock = sorted union of all bar OpenTimes.
	tset := map[int64]struct{}{}
	for _, s := range sims {
		for _, b := range s.loaded.bars {
			tset[b.OpenTime] = struct{}{}
		}
	}
	clock := make([]int64, 0, len(tset))
	for t := range tset {
		clock = append(clock, t)
	}
	sort.Slice(clock, func(i, j int) bool { return clock[i] < clock[j] })

	lives := make([]*flipLive, 0, len(sims))
	for _, s := range sims {
		bi := make(map[int64]int, len(s.loaded.bars))
		for i, b := range s.loaded.bars {
			bi[b.OpenTime] = i
		}
		lives = append(lives, &flipLive{
			se:          s,
			barIdx:      bi,
			pos:         newSimPos(p, s.loaded.entry, s.atr),
			baseline:    newSimPos(p, s.loaded.entry, s.atr),
			lastFlipBar: -1 << 30,
		})
	}

	var res ReversalResult
	res.Trades = len(lives)

	for _, t := range clock {
		for _, lv := range lives {
			bi, ok := lv.barIdx[t]
			if !ok {
				continue
			}
			if bi < lv.se.loaded.entryIdx {
				continue
			}
			bar := lv.se.loaded.bars[bi]
			lv.started = true
			lv.lastClose = bar.Close

			// Advance the never-flipped baseline clone (held to data end / its SL/TP).
			if !lv.baseline.done {
				lv.baseline.stepBaseline(p, lv.se.atr, bar)
			}

			// Advance the active machine (original until flipped, then the reverse).
			active := lv.pos
			if lv.rev != nil {
				active = lv.rev
			}
			if !active.done {
				active.stepBaseline(p, lv.se.atr, bar)
				u := active.unrealizedQuote(bar.Close)
				if u > active.peakUnrealQuote {
					active.peakUnrealQuote = u
				}
				active.recordPnl(bar.Close)
			}

			// Flip evaluation only applies while the ORIGINAL is still open.
			if lv.rev == nil && !lv.pos.done {
				ageBars := bi - lv.se.loaded.entryIdx
				if rp.Enabled && ageBars >= rp.MinAgeBars && lv.evalFlip(rp, lives, t, bi, velWindow) {
					lv.pos.addExit(bar.Close, lv.pos.remaining, "trend_reversal_flip")
					res.OriginalCutPnL += lv.pos.realizedPnL()
					lv.flips++
					lv.lastFlipBar = bi
					if rp.ReverseOpen {
						revEntry := Entry{
							Symbol:     lv.se.loaded.entry.Symbol,
							Side:       oppositeSide(lv.se.loaded.entry.Side),
							EntryPrice: bar.Close,
							EntryTime:  bar.OpenTime,
							Quantity:   lv.se.loaded.entry.Quantity,
						}
						lv.rev = newSimPos(p, revEntry, lv.se.atr)
						res.Flips++
					} else {
						res.ExhaustionCloses++
					}
				}
			}
		}
	}

	// Finalize everything to last close.
	for _, lv := range lives {
		if !lv.pos.done {
			lv.pos.finalize(lv.lastClose)
		}
		if lv.rev != nil && !lv.rev.done {
			lv.rev.finalize(lv.lastClose)
		}
		if !lv.baseline.done {
			lv.baseline.finalize(lv.lastClose)
		}
	}

	// Aggregate.
	var wins int
	for _, lv := range lives {
		pnl := lv.pos.realizedPnL()
		res.BaselineHeldPnL += lv.baseline.realizedPnL()
		if lv.rev != nil {
			rpnl := lv.rev.realizedPnL()
			res.FlipPnL += rpnl
			pnl += rpnl
			if rpnl >= 0 {
				res.FlipWins++
			}
		}
		res.TotalPnL += pnl
		if pnl >= 0 {
			wins++
		}
	}
	if len(lives) > 0 {
		res.WinRatePct = float64(wins) / float64(len(lives)) * 100
	}
	return res
}

// oppositeSide returns the reverse trading side.
func oppositeSide(side string) string {
	if side == "LONG" || side == "long" {
		return "SHORT"
	}
	return "LONG"
}

// evalFlip evaluates the four reversal rules for one entry at bar index bi.
// Returns true only when ALL enabled gates pass.
func (lv *flipLive) evalFlip(rp ReversalParams, all []*flipLive, t int64, bi, velWindow int) bool {
	// Safeguard: flip cooldown on the same entry.
	if rp.FlipCooldownBars > 0 && bi-lv.lastFlipBar < rp.FlipCooldownBars {
		return false
	}
	maxFlips := rp.MaxFlipsPerEntry
	if maxFlips <= 0 {
		maxFlips = 1
	}
	if lv.flips >= maxFlips {
		return false
	}

	bars := lv.se.loaded.bars
	bar := bars[bi]
	origSide := lv.se.loaded.entry.Side
	isLong := origSide == "LONG" || origSide == "long"

	// --- Rule 1: opposite conviction (EMA-cross flip against the held side) ---
	if rp.RequireOppositeCross {
		closes := make([]float64, len(bars))
		for i, b := range bars {
			closes[i] = b.Close
		}
		fast, slow := rp.EMAFast, rp.EMASlow
		if fast <= 0 {
			fast = 9
		}
		if slow <= 0 {
			slow = 21
		}
		dir := emaCrossDirAt(closes, fast, slow, bi)
		// long position needs a bearish cross (-1); short needs bullish (+1).
		wantDir := -1
		if !isLong {
			wantDir = 1
		}
		if dir != wantDir {
			return false
		}
	}

	// --- Rule 4: trend exhaustion (EMA20 mean-reversion OR ATR-from-peak) ---
	if rp.EMAExhaustionPct > 0 || rp.ATRFromPeakMult > 0 {
		exhausted := false
		// 4a: EMA20 deviation reverting to the mean.
		if rp.EMAExhaustionPct > 0 {
			closes := make([]float64, len(bars))
			for i, b := range bars {
				closes[i] = b.Close
			}
			e20 := ema(closes, 20)
			if bi < len(e20) && e20[bi] > 0 {
				devPct := (bar.Close - e20[bi]) / e20[bi] * 100
				if devPct < 0 {
					devPct = -devPct
				}
				if devPct < rp.EMAExhaustionPct {
					exhausted = true
				}
			}
		}
		// 4b: adverse move from peak exceeds ATRFromPeakMult ATRs.
		if !exhausted && rp.ATRFromPeakMult > 0 && lv.pos.atrPct > 0 {
			cur := pnlPct(origSide, lv.pos.e.EntryPrice, bar.Close)
			adverseFromPeak := (lv.pos.peakPnlPct - cur) / lv.pos.atrPct
			if adverseFromPeak >= rp.ATRFromPeakMult {
				exhausted = true
			}
		}
		if !exhausted {
			return false
		}
	}

	// --- Rule 2: portfolio breadth — majority of the book retracing same-side ---
	if rp.BreadthFrac > 0 && rp.BreadthMinPos > 0 {
		// Build a transient GuardParams so we reuse the canonical isRetracing().
		g := GuardParams{
			BreadthUseATR: rp.UseATR,
			BreadthATRMult: rp.ATRMult,
			BreadthVelEps:  rp.VelEps,
			L3VelWindow:    velWindow,
		}
		sameSideTotal := 0
		sameSideRetracing := 0
		for _, other := range all {
			if !other.started || other.pos.done {
				continue
			}
			// Only count positions on the SAME side as the one being challenged
			// (correlated same-direction reversal = regime shift, per Moskowitz).
			oSide := other.se.loaded.entry.Side
			oIsLong := oSide == "LONG" || oSide == "long"
			if oIsLong != isLong {
				continue
			}
			// Use each position's own latest close at this tick.
			price := other.lastClose
			if price <= 0 {
				continue
			}
			sameSideTotal++
			if isRetracing(g, other.pos, price, velWindow) {
				sameSideRetracing++
			}
		}
		if sameSideTotal < rp.BreadthMinPos {
			return false
		}
		if float64(sameSideRetracing)/float64(sameSideTotal) < rp.BreadthFrac {
			return false
		}
	}

	return true
}

// ReversalSweepRow is one reversal config's result row.
type ReversalSweepRow struct {
	Label            string
	TotalPnL         float64
	WinRatePct       float64
	Flips            int
	ExhaustionCloses int
	FlipWins         int
	FlipPnL          float64
	OriginalCutPnL   float64
	BaselineHeldPnL  float64
	// Edge = TotalPnL - BaselineHeldPnL: net value added by flipping vs holding.
	Edge float64
}

// ReversalGrid builds the candidate reversal configs to sweep. Row 0 is the
// no-flip baseline (Enabled=false) for reference. Subsequent rows progressively
// enable rules so we can attribute each rule's marginal contribution.
func ReversalGrid(emaFast, emaSlow int) []ReversalParams {
	base := func() ReversalParams {
		return ReversalParams{
			Enabled:     true,
			UseATR:      true,
			ATRMult:     1.5,
			VelEps:      0.25,
			VelWindow:   6,
			EMAFast:     emaFast,
			EMASlow:     emaSlow,
			ReverseOpen: true,
			MinAgeBars:  4,
		}
	}
	var grid []ReversalParams
	// Row 0: disabled baseline.
	grid = append(grid, ReversalParams{Enabled: false})
	// Row 1: Rule 1 only (opposite EMA cross), no other gates.
	{
		r := base()
		r.RequireOppositeCross = true
		grid = append(grid, r)
	}
	// Row 2: Rule 1 + Rule 4 (exhaustion: EMA20 mean-reversion).
	{
		r := base()
		r.RequireOppositeCross = true
		r.EMAExhaustionPct = 1.0
		grid = append(grid, r)
	}
	// Row 3: Rule 1 + Rule 4 (exhaustion: ATR-from-peak).
	{
		r := base()
		r.RequireOppositeCross = true
		r.ATRFromPeakMult = 1.5
		grid = append(grid, r)
	}
	// Row 4: Rule 1 + Rule 2 (breadth) + Rule 4 (ATR exhaustion) — full stack.
	{
		r := base()
		r.RequireOppositeCross = true
		r.ATRFromPeakMult = 1.5
		r.BreadthFrac = 0.6
		r.BreadthMinPos = 3
		grid = append(grid, r)
	}
	// Row 5: full stack, stricter breadth (0.7) + older age (8 bars).
	{
		r := base()
		r.RequireOppositeCross = true
		r.ATRFromPeakMult = 1.5
		r.BreadthFrac = 0.7
		r.BreadthMinPos = 4
		r.MinAgeBars = 8
		grid = append(grid, r)
	}
	// Row 6: exhaustion-only mode (close, no reverse) — Rule 1 + Rule 4.
	{
		r := base()
		r.RequireOppositeCross = true
		r.ATRFromPeakMult = 1.5
		r.ReverseOpen = false
		grid = append(grid, r)
	}
	// Row 7: Rule 1 alone but reverse-open with cooldown safeguard.
	{
		r := base()
		r.RequireOppositeCross = true
		r.FlipCooldownBars = 12
		grid = append(grid, r)
	}
	return grid
}

// ReversalAgeGrid isolates the MinAgeBars lever on the winning config from the
// rule sweep (pure opposite-cross flip + reverse-open, no exhaustion/breadth
// gates — those proved harmful or inert on real entries). Row 0 is the no-flip
// baseline; subsequent rows sweep the minimum hold age before a flip is allowed.
// This answers "how old must a position be before we trust a reversal?".
func ReversalAgeGrid(emaFast, emaSlow int) []ReversalParams {
	base := func(age int) ReversalParams {
		return ReversalParams{
			Enabled:              true,
			RequireOppositeCross: true,
			UseATR:               true,
			VelEps:               0.25,
			VelWindow:            6,
			EMAFast:              emaFast,
			EMASlow:              emaSlow,
			ReverseOpen:          true,
			MinAgeBars:           age,
		}
	}
	var grid []ReversalParams
	grid = append(grid, ReversalParams{Enabled: false})
	for _, age := range []int{2, 4, 6, 8, 12, 24, 48} {
		grid = append(grid, base(age))
	}
	return grid
}

// ReversalSafetyGrid tests anti-whipsaw safeguards on the winning config: flip
// cooldown (bars before the same entry may flip again) and max flips per entry.
// These guard against oscillation (flip→flip-back→flip) in choppy regimes. Row 0
// is the no-flip baseline. The reference age is fixed at the winning value.
func ReversalSafetyGrid(emaFast, emaSlow, refAge int) []ReversalParams {
	if refAge <= 0 {
		refAge = 4
	}
	base := func() ReversalParams {
		return ReversalParams{
			Enabled:              true,
			RequireOppositeCross: true,
			UseATR:               true,
			VelEps:               0.25,
			VelWindow:            6,
			EMAFast:              emaFast,
			EMASlow:              emaSlow,
			ReverseOpen:          true,
			MinAgeBars:           refAge,
		}
	}
	var grid []ReversalParams
	grid = append(grid, ReversalParams{Enabled: false})
	// No safeguards (reference).
	grid = append(grid, base())
	// Cooldown sweep.
	for _, cd := range []int{6, 12, 24, 48} {
		r := base()
		r.FlipCooldownBars = cd
		grid = append(grid, r)
	}
	// Max-flips-per-entry sweep (allow re-flip up to N).
	for _, mf := range []int{2, 3} {
		r := base()
		r.MaxFlipsPerEntry = mf
		grid = append(grid, r)
	}
	// Combined: cd12 + max2 (allow one re-flip but with cooldown).
	{
		r := base()
		r.FlipCooldownBars = 12
		r.MaxFlipsPerEntry = 2
		grid = append(grid, r)
	}
	return grid
}

// SweepReversalRobust runs the reversal grid over long-period mechanical entries.
func SweepReversalRobust(cfg RobustConfig) (ReversalResult, []ReversalSweepRow, map[string]int, error) {
	merged, per, err := PrepareRobustPortfolioEntries(cfg)
	if err != nil {
		return ReversalResult{}, nil, per, err
	}
	p := ClaudeBaselineParams()
	fast, slow := cfg.EMAFast, cfg.EMASlow
	if fast <= 0 {
		fast = 20
	}
	if slow <= 0 {
		slow = 50
	}
	grid := ReversalGrid(fast, slow)
	rows := make([]ReversalSweepRow, 0, len(grid))
	var baseline ReversalResult
	for i, rp := range grid {
		r := RunReversalSim(p, rp, merged)
		if i == 0 {
			baseline = r
		}
		label := reversalLabel(rp)
		rows = append(rows, ReversalSweepRow{
			Label:            label,
			TotalPnL:         r.TotalPnL,
			WinRatePct:       r.WinRatePct,
			Flips:            r.Flips,
			ExhaustionCloses: r.ExhaustionCloses,
			FlipWins:         r.FlipWins,
			FlipPnL:          r.FlipPnL,
			OriginalCutPnL:   r.OriginalCutPnL,
			BaselineHeldPnL:  r.BaselineHeldPnL,
			Edge:             r.TotalPnL - baseline.TotalPnL,
		})
	}
	return baseline, rows, per, nil
}

// reversalGridFor returns the grid for a named mode: "rules" (default rule
// attribution), "age" (MinAgeBars sweep), "safety" (whipsaw safeguards).
func reversalGridFor(mode string, emaFast, emaSlow int) []ReversalParams {
	switch mode {
	case "age":
		return ReversalAgeGrid(emaFast, emaSlow)
	case "safety":
		return ReversalSafetyGrid(emaFast, emaSlow, 6)
	default:
		return ReversalGrid(emaFast, emaSlow)
	}
}

// SweepReversalFromEntries runs a named reversal grid over real DB entries.
func SweepReversalFromEntries(entries []Entry, tf, mode string, emaFast, emaSlow int) (ReversalResult, []ReversalSweepRow, int, int) {
	loaded, skipped := PrepareEntries(entries, tf, OKXBars)
	if len(loaded) == 0 {
		return ReversalResult{}, nil, 0, skipped
	}
	p := ClaudeBaselineParams()
	if emaFast <= 0 {
		emaFast = 9
	}
	if emaSlow <= 0 {
		emaSlow = 21
	}
	grid := reversalGridFor(mode, emaFast, emaSlow)
	rows := make([]ReversalSweepRow, 0, len(grid))
	var baseline ReversalResult
	for i, rp := range grid {
		r := RunReversalSim(p, rp, loaded)
		if i == 0 {
			baseline = r
		}
		rows = append(rows, ReversalSweepRow{
			Label:            reversalLabel(rp),
			TotalPnL:         r.TotalPnL,
			WinRatePct:       r.WinRatePct,
			Flips:            r.Flips,
			ExhaustionCloses: r.ExhaustionCloses,
			FlipWins:         r.FlipWins,
			FlipPnL:          r.FlipPnL,
			OriginalCutPnL:   r.OriginalCutPnL,
			BaselineHeldPnL:  r.BaselineHeldPnL,
			Edge:             r.TotalPnL - baseline.TotalPnL,
		})
	}
	return baseline, rows, len(loaded), skipped
}

func reversalLabel(rp ReversalParams) string {
	if !rp.Enabled {
		return "BASELINE (no flip)"
	}
	s := "flip"
	if rp.RequireOppositeCross {
		s += " +cross"
	}
	if rp.EMAExhaustionPct > 0 {
		s += " +emaexh" + ftoa(rp.EMAExhaustionPct)
	}
	if rp.ATRFromPeakMult > 0 {
		s += " +atrexh" + ftoa(rp.ATRFromPeakMult)
	}
	if rp.BreadthFrac > 0 {
		s += " +brd" + ftoa(rp.BreadthFrac) + "/min" + itoa(rp.BreadthMinPos)
	}
	if rp.MinAgeBars > 0 {
		s += " age" + itoa(rp.MinAgeBars)
	}
	if !rp.ReverseOpen {
		s += " EXHONLY"
	}
	if rp.FlipCooldownBars > 0 {
		s += " cd" + itoa(rp.FlipCooldownBars)
	}
	return s
}
