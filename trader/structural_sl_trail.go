package trader

import (
	"nofx/market"
	"nofx/store"
)

// structural_sl_trail.go ports the backtest ratcheting-structural-stop logic
// (trader/backtest/struct_trail.go) into the live guard. Once per CLOSED bar the
// guard recomputes the nearest swing beyond the CURRENT price and moves the
// close-confirm boundary TIGHTER toward locking profit — never looser — gated by
// a per-side (profit/loss) switch, a min-profit activation, a volatility cushion,
// an anti-jitter step, and an optional max-ratchet cap.

// aggregateHigherTF groups base bars into higher-timeframe bars, `mult` base bars
// per higher bar (e.g. 4 → 4h from 1h). Only fully-formed groups are emitted so we
// never confirm against an unfinished higher bar.
func aggregateHigherTFLive(base []market.Kline, mult int) []market.Kline {
	if mult <= 1 || len(base) == 0 {
		return base
	}
	var out []market.Kline
	for start := 0; start+mult <= len(base); start += mult {
		grp := base[start : start+mult]
		hi := market.Kline{
			OpenTime:  grp[0].OpenTime,
			Open:      grp[0].Open,
			High:      grp[0].High,
			Low:       grp[0].Low,
			Close:     grp[len(grp)-1].Close,
			CloseTime: grp[len(grp)-1].CloseTime,
		}
		for _, b := range grp {
			if b.High > hi.High {
				hi.High = b.High
			}
			if b.Low < hi.Low {
				hi.Low = b.Low
			}
		}
		out = append(out, hi)
	}
	return out
}

// nearestSwingBeyondLive returns the fractal swing (strength k) CLOSEST to price on
// the protective side: nearest swing HIGH above price for a short, nearest swing LOW
// below price for a long. Anchors to the CURRENT price so it tracks structure as
// price moves. Falls back to the nearest plain bar extreme when no pivot qualifies.
func nearestSwingBeyondLive(window []market.Kline, price float64, isLong bool, k int) (float64, bool) {
	if len(window) < 2*k+1 || price <= 0 {
		return 0, false
	}
	best := 0.0
	found := false
	for i := k; i < len(window)-k; i++ {
		if !isLong {
			h := window[i].High
			if h <= price {
				continue
			}
			isPivot := true
			for j := i - k; j <= i+k; j++ {
				if j != i && window[j].High > h {
					isPivot = false
					break
				}
			}
			if isPivot && (!found || h < best) {
				best, found = h, true
			}
		} else {
			l := window[i].Low
			if l >= price {
				continue
			}
			isPivot := true
			for j := i - k; j <= i+k; j++ {
				if j != i && window[j].Low < l {
					isPivot = false
					break
				}
			}
			if isPivot && (!found || l > best) {
				best, found = l, true
			}
		}
	}
	if found {
		return best, true
	}
	for i := 0; i < len(window); i++ {
		if !isLong {
			h := window[i].High
			if h > price && (!found || h < best) {
				best, found = h, true
			}
		} else {
			l := window[i].Low
			if l < price && (!found || l > best) {
				best, found = l, true
			}
		}
	}
	return best, found
}

// trailRecomputeInput carries the per-bar inputs for a ratchet step.
type trailRecomputeInput struct {
	window   []market.Kline // base (ATR-timeframe) closed bars up to and incl. just-closed bar
	curClose float64        // just-closed bar close
	atr      float64        // frozen open-time ATR (price units)
	entry    float64        // entry price (for profit/loss side gate)
	isLong   bool
	curBound float64 // current boundary (frozen entry boundary or prior trail boundary)
	ratchets int     // ratchets already taken
}

// computeTrailBoundary runs the ratchet for one closed bar and returns the new
// boundary and updated ratchet count. It only tightens; returns the input boundary
// unchanged when no tighten is warranted (gated, capped, or structure not beyond).
func computeTrailBoundary(ss store.StructuralSLConfig, in trailRecomputeInput) (float64, int) {
	if in.atr <= 0 || in.curClose <= 0 || len(in.window) < 3 {
		return in.curBound, in.ratchets
	}
	// Max-ratchet cap.
	if ss.TrailMaxRatchets > 0 && in.ratchets >= ss.TrailMaxRatchets {
		return in.curBound, in.ratchets
	}
	// Per-side gate: is the position currently in profit or loss?
	inProfit := (in.isLong && in.curClose >= in.entry) || (!in.isLong && in.curClose <= in.entry)
	if inProfit && !ss.TrailRatchetOnProfit() {
		return in.curBound, in.ratchets
	}
	if !inProfit && !ss.TrailRatchetOnLoss() {
		return in.curBound, in.ratchets
	}
	// Min-profit activation: favorable excursion must exceed TrailMinProfitATR from
	// entry. Unset → 1.0, so the cushion is ON by default; an explicit 0 DISABLES the
	// gate (ratchet may arm immediately, subject only to the per-side on-profit/on-loss
	// gates above). Read through the accessor — the field is a pointer precisely so
	// "unset" and "explicit 0" stay distinguishable.
	minProf := ss.TrailMinProfitATRValue()
	if minProf > 0 {
		favMove := 0.0
		if in.isLong {
			favMove = in.curClose - in.entry
		} else {
			favMove = in.entry - in.curClose
		}
		if favMove < minProf*in.atr {
			return in.curBound, in.ratchets
		}
	}

	k := ss.PivotStrength
	if k < 1 {
		k = 2
	}
	curSwing, curOK := nearestSwingBeyondLive(in.window, in.curClose, in.isLong, k)

	var candidate float64
	var haveCand bool
	switch ss.TrailMode {
	case "higher":
		mult := ss.TrailHigherMult
		if mult < 2 {
			mult = 4
		}
		hi := aggregateHigherTFLive(in.window, mult)
		if s, ok := nearestSwingBeyondLive(hi, in.curClose, in.isLong, k); ok {
			candidate, haveCand = s, true
		} else if curOK {
			candidate, haveCand = curSwing, true
		}
	case "both":
		mult := ss.TrailHigherMult
		if mult < 2 {
			mult = 4
		}
		hi := aggregateHigherTFLive(in.window, mult)
		hiSwing, hiOK := nearestSwingBeyondLive(hi, in.curClose, in.isLong, k)
		switch {
		case curOK && hiOK:
			if !in.isLong {
				candidate = maxFloat(curSwing, hiSwing) // short: higher = looser
			} else {
				candidate = minFloat(curSwing, hiSwing) // long: lower = looser
			}
			haveCand = true
		case curOK:
			candidate, haveCand = curSwing, true
		case hiOK:
			candidate, haveCand = hiSwing, true
		}
	default: // "current" / ""
		if curOK {
			candidate, haveCand = curSwing, true
		}
	}
	if !haveCand || candidate <= 0 {
		return in.curBound, in.ratchets
	}

	tol := ss.TrailTolATR
	if tol < 0 {
		tol = 0
	}
	cushion := tol * in.atr
	var proposed float64
	if !in.isLong {
		proposed = candidate + cushion // short stop sits ABOVE structure
		if proposed <= in.curClose {
			return in.curBound, in.ratchets // structure not beyond price; don't arm in the money
		}
	} else {
		proposed = candidate - cushion // long stop sits BELOW structure
		if proposed >= in.curClose {
			return in.curBound, in.ratchets
		}
	}

	step := cushion
	if step <= 0 {
		step = 0.0001 * in.curClose
	}
	if in.curBound <= 0 {
		return proposed, in.ratchets + 1 // first arming
	}
	if !in.isLong {
		if proposed < in.curBound-step { // tighter = lower for a short
			return proposed, in.ratchets + 1
		}
	} else {
		if proposed > in.curBound+step { // tighter = higher for a long
			return proposed, in.ratchets + 1
		}
	}
	return in.curBound, in.ratchets
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
