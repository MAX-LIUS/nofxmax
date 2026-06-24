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

	// ATR as of entry, from preceding bars (no look-ahead).
	atr := 0.0
	if entryIdx >= atrLookback {
		h, l, c := sliceOHLC(bars[:entryIdx], entryIdx-1)
		atr = wilderATR(h, l, c, atrLookback)
	}

	// Resolve protective price levels (percent-of-entry distances).
	slDist := p.slDistancePct(e.EntryPrice, atr)
	slPrice := priceAtDistance(e.EntryPrice, slDist, isLong, false /*adverse*/)

	type tpLevel struct {
		price float64
		frac  float64
		fired bool
	}
	tps := make([]tpLevel, 0, len(p.TPLegs))
	for _, leg := range p.TPLegs {
		d := legDistancePct(leg, p.Unit, e.EntryPrice, atr)
		tps = append(tps, tpLevel{
			price: priceAtDistance(e.EntryPrice, d, isLong, true /*favorable*/),
			frac:  leg.CloseRatioPct / 100.0,
		})
	}

	remaining := 1.0
	peak := 0.0
	beStop := 0.0 // 0 = not armed
	beArmedTier := -1
	ddFired := make([]bool, len(p.DDRules))

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

		// BE arming: highest tier whose trigger is met.
		for ti := range p.BELegs {
			trig := beTriggerPct(p.BELegs[ti], p.Unit, e.EntryPrice, atr)
			if closePnL >= trig && ti > beArmedTier {
				off := beOffsetPct(p.BELegs[ti], p.Unit, e.EntryPrice, atr)
				beStop = priceAtDistance(e.EntryPrice, off, isLong, true /*favorable: above entry for long*/)
				beArmedTier = ti
			}
		}

		// DD: close ratio when profit>=min and giveback from peak>=max.
		if peak > 0 {
			giveback := (peak - closePnL) / peak * 100
			for di := range p.DDRules {
				if ddFired[di] {
					continue
				}
				minP := ddMinProfitPct(p.DDRules[di], p.Unit, e.EntryPrice, atr)
				if closePnL >= minP && giveback >= p.DDRules[di].MaxDrawdownPct {
					ddFired[di] = true
					addExit(bar.Close, p.DDRules[di].CloseRatioPct/100.0)
					res.CloseReasons = append(res.CloseReasons, "drawdown")
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
