package backtest

import "nofx/market"

// struct_trail.go implements the ratcheting (trailing) structural stop for the
// backtest harness. Once per CLOSED bar it recomputes the nearest post-entry swing
// beyond current price and moves the close-confirm boundary tighter toward locking
// profit — never looser — with a volatility tolerance cushion and an anti-jitter step.
// This models the proposed live feature so parameters can be swept before deployment.

// trailState holds the per-entry trailing boundary as it ratchets across bars.
type trailState struct {
	active   bool    // gate: only trails after min-profit reached
	isLong   bool    // position side
	atr      float64 // frozen open-time ATR (price units)
	boundary float64 // current trailing close-confirm boundary (0 = not yet set)
}

// aggregateHigherTF groups base bars into higher-timeframe bars, `mult` base bars per
// higher bar (e.g. 4 → 4h from 1h). Only fully-formed groups are emitted (a trailing
// partial group is dropped so we never confirm against an unfinished higher bar). The
// grouping is anchored to base index 0, which is stable within a single entry replay.
func aggregateHigherTF(base []market.Kline, mult int) []market.Kline {
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

// nearestSwingBeyond returns the fractal swing (strength k) CLOSEST to price on the
// protective side: for a short, the nearest swing HIGH above price; for a long, the
// nearest swing LOW below price. Mirrors rangeStructuralBoundary's pivot rule but
// anchors to the CURRENT price instead of entry, so it tracks structure as price moves.
// Falls back to the nearest plain bar extreme on the correct side when no fractal pivot
// qualifies. Returns ok=false when nothing sits beyond price on the protective side.
func nearestSwingBeyond(window []market.Kline, price float64, isLong bool, k int) (float64, bool) {
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
	// Fallback: nearest bar extreme on the correct side of price.
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

// recomputeTrailBoundary runs ONCE PER CLOSED BAR. It finds the nearest structural
// swing beyond current price (per the configured mode/timeframe), adds the volatility
// tolerance cushion, then RATCHETS the trailing boundary tighter — never looser — with
// an anti-jitter step so noise-sized structure shifts don't nudge the stop every bar.
// `window` is the base (cycle-timeframe) bars up to and INCLUDING the just-closed bar.
// Returns the (possibly unchanged) boundary; ts.boundary is updated in place.
func recomputeTrailBoundary(ts *trailState, p ProtectionParams, window []market.Kline, curClose, atr float64) float64 {
	if atr <= 0 || curClose <= 0 || len(window) < 3 {
		return ts.boundary
	}
	k := p.RangeSLPivotStrength
	if k < 1 {
		k = 2
	}

	// Candidate swing per mode. "current"/"" = this-period tightest structure;
	// "higher" = higher-timeframe structure only; "both" = the LOOSER of the two
	// (higher-tf floor) to preserve anti-volatility while still locking gains.
	curSwing, curOK := nearestSwingBeyond(window, curClose, ts.isLong, k)

	var candidate float64
	var haveCand bool
	switch p.TrailStructMode {
	case "higher":
		mult := p.TrailStructHigherMult
		if mult < 2 {
			mult = 4
		}
		hi := aggregateHigherTF(window, mult)
		if s, ok := nearestSwingBeyond(hi, curClose, ts.isLong, k); ok {
			candidate, haveCand = s, true
		} else if curOK {
			candidate, haveCand = curSwing, true // fall back to current when higher-tf has no structure yet
		}
	case "both":
		mult := p.TrailStructHigherMult
		if mult < 2 {
			mult = 4
		}
		hi := aggregateHigherTF(window, mult)
		hiSwing, hiOK := nearestSwingBeyond(hi, curClose, ts.isLong, k)
		switch {
		case curOK && hiOK:
			// Looser (wider) of the two = farther from price on the protective side.
			if !ts.isLong {
				candidate = maxF(curSwing, hiSwing) // short: higher = looser
			} else {
				candidate = minF(curSwing, hiSwing) // long: lower = looser
			}
			haveCand = true
		case curOK:
			candidate, haveCand = curSwing, true
		case hiOK:
			candidate, haveCand = hiSwing, true
		}
	default: // "current" or unset
		if curOK {
			candidate, haveCand = curSwing, true
		}
	}
	if !haveCand || candidate <= 0 {
		return ts.boundary
	}

	// Volatility tolerance cushion beyond the swing so intrabar noise up to tol×ATR
	// past the structure does not trip the close-confirm stop.
	tol := p.TrailStructTolATR
	if tol < 0 {
		tol = 0
	}
	cushion := tol * atr
	var proposed float64
	if !ts.isLong {
		proposed = candidate + cushion // short stop sits ABOVE structure
		if proposed <= curClose {
			return ts.boundary // structure not beyond price yet; don't arm inside the money
		}
	} else {
		proposed = candidate - cushion // long stop sits BELOW structure
		if proposed >= curClose {
			return ts.boundary
		}
	}

	// Ratchet: only tighten. Anti-jitter step = tol×ATR (min one tick of tolerance).
	step := cushion
	if step <= 0 {
		step = 0.0001 * curClose
	}
	if ts.boundary <= 0 {
		ts.boundary = proposed // first arming
		return ts.boundary
	}
	if !ts.isLong {
		// tighter = lower boundary; require improvement >= step
		if proposed < ts.boundary-step {
			ts.boundary = proposed
		}
	} else {
		// tighter = higher boundary
		if proposed > ts.boundary+step {
			ts.boundary = proposed
		}
	}
	return ts.boundary
}

// movedTighter reports whether nb is a strictly tighter boundary than prev
// (higher for a short, lower for a long). A fresh arming from 0 also counts.
func movedTighter(prev, nb float64, isLong bool) bool {
	if nb <= 0 {
		return false
	}
	if prev <= 0 {
		return true
	}
	if !isLong {
		return nb < prev
	}
	return nb > prev
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
