package backtest

import (
	"strings"

	"nofx/market"
)

// atrLookback is the ATR period used to size ATR-mode distances. Computed once
// at entry from the bars preceding entry (no look-ahead).
const atrLookback = 14

// ReplayEntry replays the protection params over one entry's forward bars and
// returns the trade outcome. `bars` must be ascending by time and include
// enough pre-entry history (>= atrLookback bars before entryIdx) for ATR.
//
// Intrabar convention (conservative): within a bar, the adverse extreme is
// checked before the favorable extreme, so stop-side fills are assumed to
// happen before take-profit when a single bar spans both. This avoids
// optimistic bias in the P&L comparison.
//
// Simplifications vs live (documented for fidelity caveat):
//   - Single market price per touch; no partial-fill slippage/fees modeled.
//   - BE acts as a stop that moves up (long)/down (short) once its profit
//     trigger is met, protecting the cumulative BE ratio.
//   - DD closes its ratio at the bar close when (profit>=min && giveback>=max).
func ReplayEntry(p ProtectionParams, e Entry, bars []market.Kline, entryIdx int) TradeResult {
	res := TradeResult{
		Symbol:     e.Symbol,
		Side:       e.Side,
		EntryPrice: e.EntryPrice,
	}
	if entryIdx < 0 || entryIdx >= len(bars) || e.EntryPrice <= 0 {
		return res
	}
	isLong := strings.EqualFold(e.Side, "long")

	// ATR as of entry, from preceding bars (no look-ahead). An explicit override
	// pins ATR across bar granularities (structural-TF isolation) so only the
	// stop's source timeframe — not the ATR-derived level distances — varies.
	atr := 0.0
	if e.ATROverride > 0 {
		atr = e.ATROverride
	} else if entryIdx >= atrLookback {
		h, l, c := sliceOHLC(bars[:entryIdx], entryIdx-1)
		atr = wilderATR(h, l, c, atrLookback)
	}

	// Resolve protective price levels (percent-of-entry distances).
	// Structural mode reads absolute SL/TP prices from the per-entry plan;
	// percent/ATR modes derive distances uniformly.
	var slPrice float64
	// confirmBoundary>0 activates close-confirm modelling: the tight structural
	// level fires only on a bar CLOSE beyond it (fill at close), while slPrice is
	// the wide backstop resting stop that still fires intrabar.
	var confirmBoundary float64
	if p.Unit == UnitStructural {
		if p.StructUseATRSL && p.StopLossATR > 0 && atr > 0 {
			// Hybrid: ATR-wide SL, structural TP (set below).
			slDist := p.StopLossATR * atr / e.EntryPrice * 100
			slPrice = priceAtDistance(e.EntryPrice, slDist, isLong, false /*adverse*/)
		} else {
			sl, ok := resolveStructuralSLPrice(p, e, atr)
			if !ok {
				// No usable structural stop — fall back to a wide percent stop so the
				// trade still replays (marked via a sentinel distance).
				slPrice = priceAtDistance(e.EntryPrice, 8.0, isLong, false /*adverse*/)
			} else {
				slPrice = sl
			}
		}
	} else if p.RangeSLEnabled && atr > 0 {
		// Range-anchored structural SL (faithful live reconstruction): boundary =
		// lookback range low(long)/high(short) from pre-entry bars, distance
		// clamped to [floor, backstop] ATR. Falls back to the flat StopLossATR
		// when the range has no edge (entered at/through the boundary).
		tight, hasEdge := rangeStructuralSLPrice(p, e, bars, entryIdx, atr, isLong)
		// Isolation override: when the entry carries a precomputed structural
		// boundary (from a specific source timeframe), use it verbatim as the
		// close-confirm level so the ONLY variable vs the reference is the
		// structural stop's timeframe — ATR/backstop stay from these (1h) bars.
		if e.StructBoundaryOverride > 0 && p.RangeSLCloseConfirm {
			confirmBoundary = e.StructBoundaryOverride
			backDist := p.RangeSLBackstopATR
			if backDist <= 0 {
				backDist = 4.5
			}
			slPrice = priceAtDistance(e.EntryPrice, backDist*atr/e.EntryPrice*100, isLong, false /*adverse*/)
		} else if p.RangeSLCloseConfirm && p.ConfirmStopATR > 0 {
			// Proposed: FIXED adverse-excursion close-confirm stop at N×ATR, active
			// even underwater. Backstop resting stop still covers intrabar wicks.
			confirmBoundary = priceAtDistance(e.EntryPrice, p.ConfirmStopATR*atr/e.EntryPrice*100, isLong, false /*adverse*/)
			backDist := p.RangeSLBackstopATR
			if backDist <= 0 {
				backDist = 4.5
			}
			slPrice = priceAtDistance(e.EntryPrice, backDist*atr/e.EntryPrice*100, isLong, false /*adverse*/)
		} else if p.RangeSLCloseConfirm && hasEdge {
			// Live Phase-2: tight structural level enforced on bar CLOSE only; the
			// resting exchange stop sits at the wide backstop for intrabar wicks.
			confirmBoundary = tight
			backDist := p.RangeSLBackstopATR
			if backDist <= 0 {
				backDist = 4.5
			}
			slPrice = priceAtDistance(e.EntryPrice, backDist*atr/e.EntryPrice*100, isLong, false /*adverse*/)
		} else if hasEdge {
			slPrice = tight
		} else {
			slDist := p.slDistancePct(e.EntryPrice, atr)
			slPrice = priceAtDistance(e.EntryPrice, slDist, isLong, false /*adverse*/)
		}
	} else {
		slDist := p.slDistancePct(e.EntryPrice, atr)
		slPrice = priceAtDistance(e.EntryPrice, slDist, isLong, false /*adverse*/)
	}

	tps := make([]tpLevel, 0, len(p.TPLegs))
	if p.Unit == UnitStructural && e.Structural != nil && len(e.Structural.TPLegs) > 0 {
		for _, leg := range e.Structural.TPLegs {
			if leg.Price <= 0 {
				continue
			}
			tps = append(tps, tpLevel{price: leg.Price, frac: leg.CloseRatioPct / 100.0})
		}
	} else {
		for _, leg := range p.TPLegs {
			d := legDistancePct(leg, p.Unit, e.EntryPrice, atr)
			tps = append(tps, tpLevel{
				price: priceAtDistance(e.EntryPrice, d, isLong, true /*favorable*/),
				frac:  leg.CloseRatioPct / 100.0,
			})
		}
	}

	// Min-lot TP collapse: when the position is too small to place the configured
	// ladder, only K tiers survive and the position closes 100% across them. Model the
	// two collapse anchors (nearest vs AI first_target) to compare exit quality.
	if p.CollapseK > 0 && len(tps) > 0 {
		tps = collapseTPs(tps, p, e, atr, isLong)
	}

	remaining := 1.0
	peak := 0.0      // peak PnL% (percent-give-back model)
	peakPrice := 0.0 // peak favorable PRICE (ATR-give-back model)
	beStop := 0.0    // 0 = not armed
	beArmedTier := -1
	ddFired := make([]bool, len(p.DDRules))

	// Trailing structural stop (ratchet). Only active alongside the close-confirm
	// structural model; it recomputes the nearest post-entry swing once per closed
	// bar and moves confirmBoundary tighter, gated by a min-profit excursion.
	trailOn := p.TrailStructEnabled && p.RangeSLCloseConfirm && confirmBoundary > 0 && atr > 0
	ts := &trailState{isLong: isLong, atr: atr, boundary: confirmBoundary}
	// minProfitATR<=0 DISABLES the min-profit gate (mirrors live computeTrailBoundary):
	// the ratchet may arm immediately, gated only by the on-profit/on-loss side checks.
	minProfitATR := p.TrailStructMinProfitATR
	// SPLIT mode: before BE keep the tight current-period stop untouched; only once BE
	// has EVER armed does the (higher-period) runner trail engage, re-seeded from the
	// current boundary so a looser higher structure can take over. beEverArmed latches
	// so the trail stays active after the BE portion fills (beStop resets to 0 then).
	splitAfterBE := trailOn && p.TrailStructOnlyAfterBE
	beEverArmed := false

	var exitNotional, exitFrac float64 // for VWAP exit price
	addExit := func(price, frac float64) {
		if frac <= 0 {
			return
		}
		if frac > remaining {
			frac = remaining
		}
		exitNotional += price * frac
		exitFrac += frac
		remaining -= frac
	}

	endIdx := len(bars) - 1
	for i := entryIdx; i <= endIdx && remaining > 1e-9; i++ {
		if e.ExitTime > 0 && bars[i].OpenTime > e.ExitTime {
			break
		}
		res.BarsHeld++
		bar := bars[i]

		// --- adverse extreme first: SL / BE stop ---
		stopPrice := slPrice
		stopClosesAll := true
		if beStop > 0 {
			// Once BE armed, the BE stop is tighter (above entry for long).
			stopPrice = beStop
			stopClosesAll = false
		}
		adverseHit := false
		if isLong {
			adverseHit = bar.Low <= stopPrice
		} else {
			adverseHit = bar.High >= stopPrice
		}
		if adverseHit {
			if stopClosesAll {
				addExit(stopPrice, remaining)
				res.CloseReasons = append(res.CloseReasons, "stop_loss")
			} else {
				// BE stop: close cumulative BE ratio not yet closed; rest is runner
				cum := cumulativeBERatio(p.BELegs, beArmedTier)
				closeFrac := cum - (1.0 - remaining)
				if closeFrac > remaining {
					closeFrac = remaining
				}
				if closeFrac > 0 {
					addExit(stopPrice, closeFrac)
					res.CloseReasons = append(res.CloseReasons, "break_even")
				}
				// After BE stop fills its portion, the remainder continues with
				// the fixed SL still in force (revert effective stop to SL).
				beStop = 0
				beArmedTier = -1
			}
			continue
		}

		// --- close-confirm structural stop: fires only when a bar CLOSES beyond
		// the tight boundary (fill at close), modelling live runStructuralSLGuard.
		// The wide backstop above already covers intrabar catastrophes. Normally
		// only while the full structural stop is in force (BE not yet armed); with
		// TrailStructAfterBE the ratcheted boundary ALSO stops the runner during the
		// BE phase, but only when it sits TIGHTER than the BE stop (else BE already
		// handles it) so the runner rides the tightening structure.
		runnerTrail := trailOn && (p.TrailStructAfterBE || (splitAfterBE && beEverArmed)) && beStop > 0
		if confirmBoundary > 0 && (beStop == 0 || runnerTrail) {
			tighterThanBE := beStop == 0 ||
				(isLong && confirmBoundary > beStop) || (!isLong && confirmBoundary < beStop)
			confirmed := tighterThanBE &&
				((isLong && bar.Close < confirmBoundary) || (!isLong && bar.Close > confirmBoundary))
			if confirmed {
				addExit(bar.Close, remaining)
				res.CloseReasons = append(res.CloseReasons, "structural_sl")
				if res.TrailRatchets > 0 {
					res.TrailExit = true // a ratcheted boundary (not the entry-frozen level) closed it
				}
				continue
			}
		}

		// --- favorable extreme: take-profit legs ---
		for ti := range tps {
			if tps[ti].fired {
				continue
			}
			tpHit := false
			if isLong {
				tpHit = bar.High >= tps[ti].price
			} else {
				tpHit = bar.Low <= tps[ti].price
			}
			if tpHit {
				tps[ti].fired = true
				addExit(tps[ti].price, tps[ti].frac)
				res.CloseReasons = append(res.CloseReasons, "take_profit")
			}
		}
		if remaining <= 1e-9 {
			break
		}

		// --- end-of-bar state: peak, BE arming, DD ---
		closePnL := pnlPct(e.Side, e.EntryPrice, bar.Close)
		if closePnL > peak {
			peak = closePnL
		}
		// Track peak favorable PRICE for the ATR give-back model.
		if isLong {
			if peakPrice == 0 || bar.Close > peakPrice {
				peakPrice = bar.Close
			}
		} else {
			if peakPrice == 0 || bar.Close < peakPrice {
				peakPrice = bar.Close
			}
		}

		// BE arming: highest tier whose trigger is met.
		for ti := range p.BELegs {
			trig := beTriggerPct(p.BELegs[ti], p.Unit, e.EntryPrice, atr)
			if closePnL >= trig && ti > beArmedTier {
				off := beOffsetPct(p.BELegs[ti], p.Unit, e.EntryPrice, atr)
				beStop = priceAtDistance(e.EntryPrice, off, isLong, true /*favorable: above entry for long*/)
				beArmedTier = ti
				// SPLIT mode: at the FIRST BE arm, hand the runner to the higher-period
				// trail by re-seeding its boundary from the current entry-frozen level.
				// The next recompute then lets a (looser) higher structure take over;
				// the BE stop floors the runner at entry until that structure climbs.
				if splitAfterBE && !beEverArmed {
					ts.boundary = confirmBoundary
				}
				beEverArmed = true
			}
		}

		// DD: close ratio when profit>=min and give-back from peak>=max.
		// Two give-back models:
		//   - ATR (MaxDrawdownATR>0): peak→current PRICE retrace ≥ k×ATR, matching
		//     live drawdown_trailing_convert (callback=(k*atr)/refPrice). Tracks
		//     the peak PRICE, not peak PnL%.
		//   - percent (legacy): give-back ≥ MaxDrawdownPct as a % of peak PnL.
		if peak > 0 {
			givebackPctOfPeak := (peak - closePnL) / peak * 100
			for di := range p.DDRules {
				if ddFired[di] {
					continue
				}
				minP := ddMinProfitPct(p.DDRules[di], p.Unit, e.EntryPrice, atr)
				if closePnL < minP {
					continue
				}
				triggered := false
				if p.DDRules[di].MaxDrawdownATR > 0 && atr > 0 && peakPrice > 0 {
					// price retrace from peak in ATR multiples
					var retrace float64
					if isLong {
						retrace = peakPrice - bar.Close
					} else {
						retrace = bar.Close - peakPrice
					}
					if retrace >= p.DDRules[di].MaxDrawdownATR*atr {
						triggered = true
					}
				} else if givebackPctOfPeak >= p.DDRules[di].MaxDrawdownPct {
					triggered = true
				}
				if triggered {
					ddFired[di] = true
					addExit(bar.Close, p.DDRules[di].CloseRatioPct/100.0)
					res.CloseReasons = append(res.CloseReasons, "drawdown")
				}
			}
		}

		// --- non-price close proxy: time-stop / max-hold / AI-discretionary ---
		// These fire at bar close on the FULL remainder, mirroring the live
		// code-enforced stops the raw replay otherwise ignores.
		if cp := p.CloseProxy; cp.Enabled {
			heldHours := float64(res.BarsHeld) * cp.TimeframeHours
			// time-stop: held long enough AND still losing worse than threshold.
			if cp.TimeStopHours > 0 && heldHours >= cp.TimeStopHours &&
				cp.TimeStopLossPct < 0 && closePnL <= cp.TimeStopLossPct {
				addExit(bar.Close, remaining)
				res.CloseReasons = append(res.CloseReasons, "time_stop")
				continue
			}
			// max-hold: held long enough, not a profitable runner. A per-entry
			// override (placebo) may replace the threshold; the exemption below is
			// deliberately left untouched so only the timing varies.
			maxHoldH := cp.MaxHoldHours
			if e.MaxHoldHoursOverride > 0 {
				maxHoldH = e.MaxHoldHoursOverride
			}
			if maxHoldH > 0 && heldHours >= maxHoldH &&
				closePnL < cp.MaxHoldProfitExemptPct {
				addExit(bar.Close, remaining)
				res.CloseReasons = append(res.CloseReasons, "max_hold")
				continue
			}
			// AI/discretionary proxy: after a runup ≥ arm, close on give-back.
			if cp.AIPeakArmPct > 0 && peak >= cp.AIPeakArmPct && cp.AIGiveBackPct > 0 {
				giveback := (peak - closePnL) / peak * 100
				if giveback >= cp.AIGiveBackPct {
					addExit(bar.Close, remaining)
					res.CloseReasons = append(res.CloseReasons, "ai_proxy")
					continue
				}
			}
		}

		// --- trailing structural stop: once per CLOSED bar, ratchet the confirm
		// boundary tighter. Default: runs before BE arms. TrailStructAfterBE: also
		// keeps ratcheting through the BE phase. SPLIT (TrailStructOnlyAfterBE): runs
		// ONLY once BE has armed (before BE the tight current-period stop is untouched).
		// Gated by minProfitATR.
		trailRecompute := false
		switch {
		case !trailOn:
			trailRecompute = false
		case splitAfterBE:
			trailRecompute = beEverArmed
		case p.TrailStructAfterBE:
			trailRecompute = true
		default:
			trailRecompute = beStop == 0
		}
		if trailRecompute {
			var favATR float64
			if isLong {
				favATR = (bar.Close - e.EntryPrice) / atr
			} else {
				favATR = (e.EntryPrice - bar.Close) / atr
			}
			// side gate: only ratchet in the allowed profit/loss state (mirrors live
			// TrailOnProfit / TrailOnLoss). favATR>0 => favorable (profit) side.
			sideOK := (favATR >= 0 && p.TrailStructOnProfit) || (favATR < 0 && p.TrailStructOnLoss)
			// ratchet-count cap: once TrailStructMaxRatchets tightenings have happened
			// the boundary locks (0 = unlimited).
			capOK := p.TrailStructMaxRatchets <= 0 || res.TrailRatchets < p.TrailStructMaxRatchets
			// min-profit gate only binds when >0 (mirrors live: 0 disables it, leaving
			// sideOK to govern). Positive keeps the "up N ATR before trailing" cushion.
			minProfOK := minProfitATR <= 0 || favATR >= minProfitATR
			if minProfOK && sideOK && capOK {
				prev := ts.boundary
				// Rolling structural lookback ending at the just-closed bar (mirrors
				// live LookbackBars). Includes pre-entry structure so the higher-tf
				// aggregation has enough bars to form swings, not just post-entry.
				lb := p.RangeSLLookback
				if lb <= 0 {
					lb = 24
				}
				start := i + 1 - lb
				if start < 0 {
					start = 0
				}
				window := bars[start : i+1]
				nb := recomputeTrailBoundary(ts, p, window, bar.Close, atr)
				if movedTighter(prev, nb, isLong) {
					confirmBoundary = nb
					res.TrailRatchets++
				}
			}
		}
	}

	// Any unclosed remainder marks-to-last-close (open-ended exit).
	if remaining > 1e-9 {
		last := bars[minInt(endIdx, lastIdxBefore(bars, e.ExitTime, entryIdx))]
		addExit(last.Close, remaining)
		res.CloseReasons = append(res.CloseReasons, "mark_to_market")
		res.FullyClosed = false
	} else {
		res.FullyClosed = true
	}

	if exitFrac > 0 {
		res.ExitPrice = exitNotional / exitFrac
	}
	res.ClosedQty = e.Quantity * exitFrac
	// Return % on notional: weighted PnL% across the closed fractions.
	res.ReturnPct = pnlPct(e.Side, e.EntryPrice, res.ExitPrice)
	res.RealizedPnL = res.ReturnPct / 100.0 * e.EntryPrice * e.Quantity
	return res
}
