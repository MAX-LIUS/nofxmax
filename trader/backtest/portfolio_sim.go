package backtest

import (
	"sort"
	"strings"

	"nofx/market"
)

// GuardParams configures the portfolio giveback guard overlay tested by the
// time-synchronized PortfolioSim. All distances are in quote currency unless a
// field name says Pct. Zero value = fully disabled (no-op overlay), so a guard
// run with GuardParams{} reproduces the pure-baseline run exactly.
type GuardParams struct {
	Enabled bool

	// L1: per-symbol giveback-velocity guard. Each position tracks its peak
	// unrealized profit (quote ccy). When, within L1WindowBars, the position
	// gives back >= L1GivebackPct of its peak AND the peak reached
	// >= L1MinPeakPct (position profit %), trim L1ClosePct of the position.
	L1Enabled     bool
	L1WindowBars  int
	L1GivebackPct float64
	L1MinPeakPct  float64
	L1ClosePct    float64

	// L2: portfolio circuit breaker. Tracks portfolio total unrealized profit
	// high-water mark. When portfolio gives back >= L2GivebackPct of its peak
	// (within L2WindowBars) AND peak reached >= L2MinPeakQuote, trim L2ClosePct
	// of EACH currently-winning position (de-risk the whole book).
	L2Enabled      bool
	L2WindowBars   int
	L2GivebackPct  float64
	L2MinPeakQuote float64
	L2ClosePct     float64

	// Directional concentration: when net same-side notional exposure ratio
	// >= ConcentrationPct (e.g. 0.7 = 70% one-sided) the L2 giveback threshold
	// is multiplied by ConcTightenMult (<1 = trigger earlier). 0 disables.
	ConcentrationPct float64
	ConcTightenMult  float64

	// Trend-adaptive close ratio: when enabled, the L2 close ratio is chosen by
	// the portfolio's trend strength (notional-weighted avg entry-ADX of open
	// winners). Strong trend (avg ADX >= TrendADXThreshold) => reversals tend to
	// be real => trim L2ClosePctTrend (lock more). Chop (below threshold) =>
	// pullbacks tend to recover => trim L2ClosePctChop (trim less). When
	// AdaptiveClose is false, the flat L2ClosePct is used.
	AdaptiveClose     bool
	TrendADXThreshold float64
	L2ClosePctTrend   float64
	L2ClosePctChop    float64
}

// SimResult is the outcome of a synchronized portfolio simulation.
type SimResult struct {
	TotalPnL       float64 // realized PnL summed across all positions at sim end
	MaxPortfolioDD float64 // max drawdown of realized+unrealized equity curve (quote)
	MaxGiveback    float64 // largest peak->trough drop of total UNREALIZED profit (quote)
	GuardTrims     int     // count of guard-triggered partial closes
	GuardClosedQty float64 // total fraction-equivalent closed by guard
	WinRatePct     float64
	Trades         int
}

// tpLvl mirrors replay.go's tpLevel (kept local to avoid touching the tested engine).
type tpLvl struct {
	price float64
	frac  float64
	fired bool
}

// simPos is the per-position stateful protection machine, a bar-stepping mirror
// of ReplayEntry. It exposes step() for one bar and tracks guard state.
type simPos struct {
	e      Entry
	isLong bool

	slPrice     float64
	tps         []tpLvl
	beStop      float64
	beArmedTier int
	ddFired     []bool

	remaining    float64 // fraction of original position still open (0..1)
	peakPnlPct   float64 // peak position profit % (for BE/DD/L1 arming)
	exitNotional float64 // VWAP exit accounting
	exitFrac     float64
	closeReasons []string

	// guard state
	peakUnrealQuote float64 // peak unrealized profit in quote ccy (L1 high-water)
	l1FiredAtPeak   float64 // peakPnlPct value when L1 last fired (ratchet re-arm)
	entryADX        float64 // Wilder ADX at entry (trend strength of entry regime)
	armed           bool    // entry bar reached
	done            bool    // fully closed
}

// newSimPos resolves protective levels at entry (mirrors ReplayEntry setup).
func newSimPos(p ProtectionParams, e Entry, atr float64) *simPos {
	isLong := strings.EqualFold(e.Side, "long")
	sp := &simPos{
		e:           e,
		isLong:      isLong,
		beArmedTier: -1,
		remaining:   1.0,
		ddFired:     make([]bool, len(p.DDRules)),
	}
	slDist := p.slDistancePct(e.EntryPrice, atr)
	sp.slPrice = priceAtDistance(e.EntryPrice, slDist, isLong, false)
	for _, leg := range p.TPLegs {
		d := legDistancePct(leg, p.Unit, e.EntryPrice, atr)
		sp.tps = append(sp.tps, tpLvl{
			price: priceAtDistance(e.EntryPrice, d, isLong, true),
			frac:  leg.CloseRatioPct / 100.0,
		})
	}
	return sp
}

// addExit records a partial/full close at price for fraction frac of original.
func (sp *simPos) addExit(price, frac float64, reason string) {
	if frac <= 0 {
		return
	}
	if frac > sp.remaining {
		frac = sp.remaining
	}
	sp.exitNotional += price * frac
	sp.exitFrac += frac
	sp.remaining -= frac
	sp.closeReasons = append(sp.closeReasons, reason)
	if sp.remaining <= 1e-9 {
		sp.done = true
	}
}

// unrealizedQuote returns current open-fraction unrealized PnL in quote ccy.
func (sp *simPos) unrealizedQuote(price float64) float64 {
	if sp.remaining <= 1e-9 {
		return 0
	}
	return pnlPct(sp.e.Side, sp.e.EntryPrice, price) / 100.0 * sp.e.EntryPrice * sp.e.Quantity * sp.remaining
}

// notionalRemaining returns the open notional (entry-priced) for exposure calc.
func (sp *simPos) notionalRemaining() float64 {
	if sp.remaining <= 1e-9 {
		return 0
	}
	return sp.e.EntryPrice * sp.e.Quantity * sp.remaining
}

// stepBaseline applies one bar of baseline protection (SL/BE/TP/DD), mirroring
// ReplayEntry's intrabar convention (adverse extreme before favorable). Returns
// true if the position closed this bar. Guard logic is applied separately by
// the portfolio loop after all baseline steps, so baseline and guard never
// double-count within a bar.
func (sp *simPos) stepBaseline(p ProtectionParams, atr float64, bar market.Kline) {
	if sp.done {
		return
	}
	// --- adverse extreme first: SL / BE stop ---
	stopPrice := sp.slPrice
	stopClosesAll := true
	if sp.beStop > 0 {
		stopPrice = sp.beStop
		stopClosesAll = false
	}
	adverseHit := false
	if sp.isLong {
		adverseHit = bar.Low <= stopPrice
	} else {
		adverseHit = bar.High >= stopPrice
	}
	if adverseHit {
		if stopClosesAll {
			sp.addExit(stopPrice, sp.remaining, "stop_loss")
		} else {
			cum := cumulativeBERatio(p.BELegs, sp.beArmedTier)
			closeFrac := cum - (1.0 - sp.remaining)
			if closeFrac > sp.remaining {
				closeFrac = sp.remaining
			}
			if closeFrac > 0 {
				sp.addExit(stopPrice, closeFrac, "break_even")
			}
			sp.beStop = 0
			sp.beArmedTier = -1
		}
		return
	}

	// --- favorable extreme: take-profit legs ---
	for ti := range sp.tps {
		if sp.tps[ti].fired {
			continue
		}
		tpHit := false
		if sp.isLong {
			tpHit = bar.High >= sp.tps[ti].price
		} else {
			tpHit = bar.Low <= sp.tps[ti].price
		}
		if tpHit {
			sp.tps[ti].fired = true
			sp.addExit(sp.tps[ti].price, sp.tps[ti].frac, "take_profit")
		}
	}
	if sp.done {
		return
	}

	// --- end-of-bar: peak, BE arming, DD ---
	closePnL := pnlPct(sp.e.Side, sp.e.EntryPrice, bar.Close)
	if closePnL > sp.peakPnlPct {
		sp.peakPnlPct = closePnL
	}
	for ti := range p.BELegs {
		trig := beTriggerPct(p.BELegs[ti], p.Unit, sp.e.EntryPrice, atr)
		if closePnL >= trig && ti > sp.beArmedTier {
			off := beOffsetPct(p.BELegs[ti], p.Unit, sp.e.EntryPrice, atr)
			sp.beStop = priceAtDistance(sp.e.EntryPrice, off, sp.isLong, true)
			sp.beArmedTier = ti
		}
	}
	if sp.peakPnlPct > 0 {
		giveback := (sp.peakPnlPct - closePnL) / sp.peakPnlPct * 100
		for di := range p.DDRules {
			if sp.ddFired[di] {
				continue
			}
			minP := ddMinProfitPct(p.DDRules[di], p.Unit, sp.e.EntryPrice, atr)
			if closePnL >= minP && giveback >= p.DDRules[di].MaxDrawdownPct {
				sp.ddFired[di] = true
				sp.addExit(bar.Close, p.DDRules[di].CloseRatioPct/100.0, "drawdown")
			}
		}
	}
}

// finalize marks any unclosed remainder to the last seen close (open-ended exit).
func (sp *simPos) finalize(lastClose float64) {
	if sp.remaining > 1e-9 {
		sp.addExit(lastClose, sp.remaining, "mark_to_market")
	}
}

// realizedPnL returns the position's realized PnL given its VWAP exit.
func (sp *simPos) realizedPnL() float64 {
	if sp.exitFrac <= 0 {
		return 0
	}
	exitPrice := sp.exitNotional / sp.exitFrac
	return pnlPct(sp.e.Side, sp.e.EntryPrice, exitPrice) / 100.0 * sp.e.EntryPrice * sp.e.Quantity
}

// simEntry bundles a prepared entry with its bars, entry index, and entry ATR.
type simEntry struct {
	loaded loadedEntry
	atr    float64
}

// GuardSweepRow is one evaluated guard config plus its DD-cut/PnL-cost score,
// exported so cmd tools can rank and print without naming the unexported
// loadedEntry type.
type GuardSweepRow struct {
	Guard   GuardParams
	Result  SimResult
	DDCut   float64 // baseline.MaxPortfolioDD - result.MaxPortfolioDD
	PnLCost float64 // baseline.TotalPnL - result.TotalPnL
	Score   float64 // DDCut - PnLCost
}

// SweepGuards runs the baseline (guard off) then every guard in grid over the
// prepared entries, returning the baseline result and rows sorted by Score desc.
func SweepGuards(grid []GuardParams, loaded []loadedEntry) (SimResult, []GuardSweepRow) {
	p := ClaudeBaselineParams()
	base := RunPortfolioSim(p, GuardParams{}, loaded)
	rows := make([]GuardSweepRow, 0, len(grid))
	for _, g := range grid {
		r := RunPortfolioSim(p, g, loaded)
		ddCut := base.MaxPortfolioDD - r.MaxPortfolioDD
		pnlCost := base.TotalPnL - r.TotalPnL
		rows = append(rows, GuardSweepRow{
			Guard: g, Result: r, DDCut: ddCut, PnLCost: pnlCost, Score: ddCut - pnlCost,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Score > rows[j].Score })
	return base, rows
}

// RunPortfolioSim runs a time-synchronized multi-position simulation over the
// prepared entries, applying baseline protection plus the giveback guard
// overlay. It steps a master clock (union of all bar open-times at 1h grid) and,
// at each tick, advances every active position one bar then applies guards.
//
// With GuardParams{} (disabled) the realized PnL equals the pure baseline
// (sum of ReplayEntry) up to intrabar-ordering identity, validated by tests.
func RunPortfolioSim(p ProtectionParams, g GuardParams, loaded []loadedEntry) SimResult {
	// indicatorWindow bounds how many pre-entry bars feed ATR/ADX. Period-14
	// Wilder indicators converge well within this, and bounding it keeps the
	// per-entry cost O(window) instead of O(entryIdx) — critical for sweeps over
	// long histories (late entries would otherwise scan thousands of bars each).
	const indicatorWindow = 60

	// Precompute entry ATR per loaded entry (mirrors ReplayEntry).
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

	// Build master clock = sorted union of all bar OpenTimes across entries.
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

	// Per-entry: map OpenTime -> bar index for O(1) lookup, and the position machine.
	type live struct {
		se        simEntry
		barIdx    map[int64]int
		pos       *simPos
		started   bool
		lastClose float64
	}
	lives := make([]*live, 0, len(sims))
	for _, s := range sims {
		bi := make(map[int64]int, len(s.loaded.bars))
		for i, b := range s.loaded.bars {
			bi[b.OpenTime] = i
		}
		pos := newSimPos(p, s.loaded.entry, s.atr)
		// Entry-ADX: trend strength of the regime the position opened into.
		// Computed once from a bounded pre-entry window (no look-ahead). Used by
		// the trend-adaptive close ratio. atrLookback also serves as ADX period.
		// ADX needs 2*period+1 bars to seed; use a window of 4*period to be safe.
		if s.loaded.entryIdx >= 2*atrLookback+1 {
			lo := s.loaded.entryIdx - 4*atrLookback
			if lo < 0 {
				lo = 0
			}
			h, l, c := sliceOHLCRange(s.loaded.bars, lo, s.loaded.entryIdx-1)
			pos.entryADX = wilderADX(h, l, c, atrLookback)
		}
		lives = append(lives, &live{
			se:     s,
			barIdx: bi,
			pos:    pos,
		})
	}

	// Portfolio-level guard state.
	var portfolioPeakUnreal float64
	gstate := &guardState{}
	var maxGiveback float64
	var realizedEquity, peakEquity, maxDD float64
	var guardTrims int
	var guardClosedQty float64

	res := SimResult{Trades: len(lives)}

	for _, t := range clock {
		// 1) advance each active position one baseline bar at this tick.
		for _, lv := range lives {
			if lv.pos.done {
				continue
			}
			bi, ok := lv.barIdx[t]
			if !ok {
				continue // this symbol has no bar at this tick
			}
			e := lv.se.loaded.entry
			// respect entry window: only start at/after entry bar, stop after exit.
			if bi < lv.se.loaded.entryIdx {
				continue
			}
			if e.ExitTime > 0 && lv.se.loaded.bars[bi].OpenTime > e.ExitTime {
				if !lv.pos.done {
					lv.pos.finalize(lv.lastClose)
				}
				continue
			}
			lv.started = true
			bar := lv.se.loaded.bars[bi]
			lv.lastClose = bar.Close
			lv.pos.stepBaseline(p, lv.se.atr, bar)
			// update per-position peak unrealized (quote) for L1.
			u := lv.pos.unrealizedQuote(bar.Close)
			if u > lv.pos.peakUnrealQuote {
				lv.pos.peakUnrealQuote = u
			}
		}

		// 2) apply guards (after all baseline steps this tick).
		if g.Enabled {
			gps := make([]guardPos, 0, len(lives))
			for _, lv := range lives {
				if lv.started && !lv.pos.done && lv.lastClose > 0 {
					gps = append(gps, guardPos{pos: lv.pos, price: lv.lastClose})
				}
			}
			nt, ncq := applyGuards(g, gps, portfolioPeakUnreal, gstate)
			guardTrims += nt
			guardClosedQty += ncq
		}

		// 3) measure portfolio unrealized + equity for giveback / DD.
		var totalUnreal, realizedSoFar float64
		for _, lv := range lives {
			price := lv.lastClose
			if lv.started && !lv.pos.done && price > 0 {
				totalUnreal += lv.pos.unrealizedQuote(price)
			}
			realizedSoFar += lv.pos.realizedPnL()
		}
		if totalUnreal > portfolioPeakUnreal {
			portfolioPeakUnreal = totalUnreal
		}
		if gb := portfolioPeakUnreal - totalUnreal; gb > maxGiveback {
			maxGiveback = gb
		}
		equity := realizedSoFar + totalUnreal
		if equity > peakEquity {
			peakEquity = equity
		}
		if dd := peakEquity - equity; dd > maxDD {
			maxDD = dd
		}
		_ = realizedEquity
	}

	// finalize any still-open positions to their last close.
	for _, lv := range lives {
		if !lv.pos.done {
			lv.pos.finalize(lv.lastClose)
		}
	}

	var wins int
	for _, lv := range lives {
		pnl := lv.pos.realizedPnL()
		res.TotalPnL += pnl
		if pnl >= 0 {
			wins++
		}
	}
	if len(lives) > 0 {
		res.WinRatePct = float64(wins) / float64(len(lives)) * 100
	}
	res.MaxPortfolioDD = maxDD
	res.MaxGiveback = maxGiveback
	res.GuardTrims = guardTrims
	res.GuardClosedQty = guardClosedQty
	return res
}

// guardPos pairs a position machine with its current mark price for the guard.
type guardPos struct {
	pos   *simPos
	price float64
}

// guardState carries cross-tick ratchet state for the portfolio (L2) guard so
// it fires once per reversal episode and re-arms only on a new portfolio peak.
type guardState struct {
	l2FiredAtPeak float64 // portfolioPeak value when L2 last fired (0 = armed)
}

// applyGuards runs L1 (per-symbol) then L2 (portfolio) guard trims on the
// currently-open positions. portfolioPeak is the running high-water mark of
// total unrealized profit (quote). Returns (#trims, summed closed fraction).
//
// Ratchet semantics (prevents per-tick churn): each guard fires at most once
// per reversal episode. L1 re-arms when its position makes a NEW profit-% peak
// above the peak at which it last fired. L2 re-arms when the portfolio unreal
// high-water makes a new high above the peak at which it last fired. This
// mirrors how a live trailing/give-back rule would act on discrete events
// rather than every monitoring tick.
//
// L1 uses profit-% (peakPnlPct) giveback, which is size-invariant so trimming
// the position does not distort the next measurement. L2 uses quote-unrealized
// giveback against the running portfolio high-water.
func applyGuards(g GuardParams, gps []guardPos, portfolioPeak float64, st *guardState) (int, float64) {
	trims := 0
	var closed float64

	// L1: per-symbol giveback-velocity trim (profit-% based, ratcheted).
	if g.L1Enabled {
		for _, gp := range gps {
			sp := gp.pos
			if sp.done || sp.remaining <= 1e-9 {
				continue
			}
			peakPct := sp.peakPnlPct
			if peakPct < g.L1MinPeakPct {
				continue
			}
			// re-arm: only consider firing if a new profit peak was set since
			// the last L1 fire on this position.
			if sp.l1FiredAtPeak > 0 && peakPct <= sp.l1FiredAtPeak {
				continue
			}
			curPct := pnlPct(sp.e.Side, sp.e.EntryPrice, gp.price)
			giveback := (peakPct - curPct) / peakPct * 100
			if giveback >= g.L1GivebackPct {
				frac := g.L1ClosePct / 100.0
				before := sp.remaining
				sp.addExit(gp.price, frac, "guard_l1")
				closed += before - sp.remaining
				sp.l1FiredAtPeak = peakPct
				trims++
			}
		}
	}

	// L2: portfolio circuit breaker — trim every currently-winning position,
	// once per episode (re-arm on new portfolio high-water).
	if g.L2Enabled && portfolioPeak >= g.L2MinPeakQuote && portfolioPeak > 0 {
		// re-arm: if portfolio set a new high-water above last fire, clear the latch.
		if st.l2FiredAtPeak > 0 && portfolioPeak > st.l2FiredAtPeak {
			st.l2FiredAtPeak = 0
		}
		armed := st.l2FiredAtPeak == 0
		var totalUnreal, longNotional, shortNotional float64
		for _, gp := range gps {
			if gp.pos.done {
				continue
			}
			totalUnreal += gp.pos.unrealizedQuote(gp.price)
			n := gp.pos.notionalRemaining()
			if gp.pos.isLong {
				longNotional += n
			} else {
				shortNotional += n
			}
		}
		threshold := g.L2GivebackPct
		// directional concentration tightening.
		if g.ConcentrationPct > 0 && g.ConcTightenMult > 0 {
			total := longNotional + shortNotional
			if total > 0 {
				netSame := longNotional
				if shortNotional > longNotional {
					netSame = shortNotional
				}
				if netSame/total >= g.ConcentrationPct {
					threshold *= g.ConcTightenMult
				}
			}
		}
		giveback := (portfolioPeak - totalUnreal) / portfolioPeak * 100
		if armed && giveback >= threshold {
			frac := g.L2ClosePct / 100.0
			// Trend-adaptive close ratio: pick based on the notional-weighted
			// avg entry-ADX of currently-open winners. Strong trend -> trim more.
			if g.AdaptiveClose {
				var wADX, wNotional float64
				for _, gp := range gps {
					if gp.pos.done || gp.pos.unrealizedQuote(gp.price) <= 0 {
						continue
					}
					n := gp.pos.notionalRemaining()
					wADX += gp.pos.entryADX * n
					wNotional += n
				}
				avgADX := 0.0
				if wNotional > 0 {
					avgADX = wADX / wNotional
				}
				if avgADX >= g.TrendADXThreshold {
					frac = g.L2ClosePctTrend / 100.0
				} else {
					frac = g.L2ClosePctChop / 100.0
				}
			}
			for _, gp := range gps {
				sp := gp.pos
				if sp.done || sp.remaining <= 1e-9 {
					continue
				}
				// only trim winning positions (de-risk profit, don't realize losers).
				if sp.unrealizedQuote(gp.price) <= 0 {
					continue
				}
				before := sp.remaining
				sp.addExit(gp.price, frac, "guard_l2")
				closed += before - sp.remaining
				trims++
			}
			st.l2FiredAtPeak = portfolioPeak // latch until a new high-water
		}
	}

	return trims, closed
}
