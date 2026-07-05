package backtest

import (
	"math"
	"strings"

	"nofx/market"
)

// wilderATR computes Wilder-smoothed ATR(period) over the given OHLC bars and
// returns the ATR value as of the LAST bar. Returns 0 if insufficient data.
func wilderATR(highs, lows, closes []float64, period int) float64 {
	n := len(closes)
	if period <= 0 || n < period+1 {
		return 0
	}
	// True range series (from index 1).
	trs := make([]float64, 0, n-1)
	for i := 1; i < n; i++ {
		hl := highs[i] - lows[i]
		hc := math.Abs(highs[i] - closes[i-1])
		lc := math.Abs(lows[i] - closes[i-1])
		tr := hl
		if hc > tr {
			tr = hc
		}
		if lc > tr {
			tr = lc
		}
		trs = append(trs, tr)
	}
	if len(trs) < period {
		return 0
	}
	// Initial ATR = average of first `period` TRs, then Wilder smoothing.
	var sum float64
	for i := 0; i < period; i++ {
		sum += trs[i]
	}
	atr := sum / float64(period)
	for i := period; i < len(trs); i++ {
		atr = (atr*float64(period-1) + trs[i]) / float64(period)
	}
	return atr
}

// slDistancePct resolves the stop-loss distance as a percent of entry price,
// regardless of unit. In ATR mode: distancePct = StopLossATR*atr/entry*100.
func (p ProtectionParams) slDistancePct(entry, atr float64) float64 {
	if p.Unit == UnitATRMult {
		if entry <= 0 {
			return 0
		}
		return p.StopLossATR * atr / entry * 100
	}
	return p.StopLossPct
}

// legDistancePct resolves a ladder TP leg distance as percent of entry.
func legDistancePct(leg LadderLeg, unit ValueUnit, entry, atr float64) float64 {
	if unit == UnitATRMult {
		if entry <= 0 {
			return 0
		}
		return leg.ATRMult * atr / entry * 100
	}
	return leg.DistPct
}

// beTriggerPct / beOffsetPct resolve BE thresholds as percent of entry.
func beTriggerPct(leg BELeg, unit ValueUnit, entry, atr float64) float64 {
	if unit == UnitATRMult {
		if entry <= 0 {
			return 0
		}
		return leg.TriggerATR * atr / entry * 100
	}
	return leg.TriggerPct
}

func beOffsetPct(leg BELeg, unit ValueUnit, entry, atr float64) float64 {
	if unit == UnitATRMult {
		if entry <= 0 {
			return 0
		}
		return leg.OffsetATR * atr / entry * 100
	}
	return leg.OffsetPct
}

// ddMinProfitPct resolves the DD arm threshold as percent of entry.
func ddMinProfitPct(rule DDRule, unit ValueUnit, entry, atr float64) float64 {
	if unit == UnitATRMult {
		if entry <= 0 {
			return 0
		}
		return rule.MinProfitATR * atr / entry * 100
	}
	return rule.MinProfitPct
}

// pnlPct mirrors the live calculatePositionPnLPct (price-move %, no leverage).
func pnlPct(side string, entry, price float64) float64 {
	if entry <= 0 || price <= 0 {
		return 0
	}
	if strings.EqualFold(side, "long") {
		return (price - entry) / entry * 100
	}
	return (entry - price) / entry * 100
}

// resolveStructuralSLPrice returns the absolute stop price for an entry under
// structural mode. Variant A (StructBufferATR<=0): use the AI's buffered SLPrice
// as-is. Variant B (StructBufferATR>0): re-derive from the bare anchor as
// anchor ± k×ATR. The result is clamped to [StructMinSLPct, StructMaxSLPct] of
// entry when those are set. Returns (price, ok). ok=false means no usable
// structural stop (caller should skip or fall back).
func resolveStructuralSLPrice(p ProtectionParams, e Entry, atr float64) (float64, bool) {
	if e.Structural == nil {
		return 0, false
	}
	isLong := strings.EqualFold(e.Side, "long")
	var sl float64
	if p.StructBufferATR > 0 && e.Structural.SLAnchor > 0 && atr > 0 {
		// Buffer the bare anchor away from price in the adverse direction.
		if isLong {
			sl = e.Structural.SLAnchor - p.StructBufferATR*atr
		} else {
			sl = e.Structural.SLAnchor + p.StructBufferATR*atr
		}
	} else {
		sl = e.Structural.SLPrice
	}
	if sl <= 0 {
		return 0, false
	}
	// Clamp the stop distance to configured guardrails.
	dist := pnlPct(e.Side, e.EntryPrice, sl) // negative = stop is adverse
	adverseDistPct := -dist                   // positive number = how far adverse
	if adverseDistPct <= 0 {
		// Degenerate: stop on the wrong side of entry. Reject.
		return 0, false
	}
	clamped := adverseDistPct
	if p.StructMinSLPct > 0 && clamped < p.StructMinSLPct {
		clamped = p.StructMinSLPct
	}
	if p.StructMaxSLPct > 0 && clamped > p.StructMaxSLPct {
		clamped = p.StructMaxSLPct
	}
	if clamped != adverseDistPct {
		sl = priceAtDistance(e.EntryPrice, clamped, isLong, false /*adverse*/)
	}
	return sl, true
}

// klineHighsLowsCloses extracts OHLC slices up to (and including) index i.
func sliceOHLC(bars []market.Kline, upto int) (highs, lows, closes []float64) {
	highs = make([]float64, upto+1)
	lows = make([]float64, upto+1)
	closes = make([]float64, upto+1)
	for i := 0; i <= upto; i++ {
		highs[i] = bars[i].High
		lows[i] = bars[i].Low
		closes[i] = bars[i].Close
	}
	return
}

// sliceOHLCRange extracts OHLC slices over the inclusive bar index range
// [lo, hi]. Used to bound indicator (ATR/ADX) lookback to a fixed pre-entry
// window so per-entry cost stays O(window) regardless of how late the entry is.
func sliceOHLCRange(bars []market.Kline, lo, hi int) (highs, lows, closes []float64) {
	if lo < 0 {
		lo = 0
	}
	if hi >= len(bars) {
		hi = len(bars) - 1
	}
	if hi < lo {
		return nil, nil, nil
	}
	n := hi - lo + 1
	highs = make([]float64, n)
	lows = make([]float64, n)
	closes = make([]float64, n)
	for i := 0; i < n; i++ {
		highs[i] = bars[lo+i].High
		lows[i] = bars[lo+i].Low
		closes[i] = bars[lo+i].Close
	}
	return
}
