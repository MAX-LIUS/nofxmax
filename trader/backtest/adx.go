package backtest

import "math"

// wilderADX computes the Wilder-smoothed ADX over the given OHLC bars and
// returns the ADX value as of the LAST bar. Returns 0 if insufficient data.
// ADX measures trend strength (not direction): high ADX (>~25) = strong trend,
// low ADX (<~20) = choppy/range. Used by the adaptive guard to pick how
// aggressively to trim (strong trend -> reversals are real, lock more;
// chop -> pullbacks recover, trim less).
func wilderADX(highs, lows, closes []float64, period int) float64 {
	n := len(closes)
	if period <= 0 || n < 2*period+1 {
		return 0
	}
	// Directional movement and true range series (from index 1).
	plusDM := make([]float64, n)
	minusDM := make([]float64, n)
	tr := make([]float64, n)
	for i := 1; i < n; i++ {
		up := highs[i] - highs[i-1]
		down := lows[i-1] - lows[i]
		if up > down && up > 0 {
			plusDM[i] = up
		}
		if down > up && down > 0 {
			minusDM[i] = down
		}
		hl := highs[i] - lows[i]
		hc := math.Abs(highs[i] - closes[i-1])
		lc := math.Abs(lows[i] - closes[i-1])
		t := hl
		if hc > t {
			t = hc
		}
		if lc > t {
			t = lc
		}
		tr[i] = t
	}

	// Wilder smoothing of TR, +DM, -DM over `period`, seeded by simple sums.
	var sTR, sPlus, sMinus float64
	for i := 1; i <= period; i++ {
		sTR += tr[i]
		sPlus += plusDM[i]
		sMinus += minusDM[i]
	}
	dx := make([]float64, 0, n)
	calcDX := func(plusDI, minusDI float64) float64 {
		denom := plusDI + minusDI
		if denom == 0 {
			return 0
		}
		return math.Abs(plusDI-minusDI) / denom * 100
	}
	if sTR > 0 {
		dx = append(dx, calcDX(sPlus/sTR*100, sMinus/sTR*100))
	}
	for i := period + 1; i < n; i++ {
		sTR = sTR - sTR/float64(period) + tr[i]
		sPlus = sPlus - sPlus/float64(period) + plusDM[i]
		sMinus = sMinus - sMinus/float64(period) + minusDM[i]
		if sTR > 0 {
			dx = append(dx, calcDX(sPlus/sTR*100, sMinus/sTR*100))
		}
	}
	if len(dx) < period {
		return 0
	}
	// ADX = Wilder-smoothed DX. Seed with average of first `period` DX values.
	var adx float64
	for i := 0; i < period; i++ {
		adx += dx[i]
	}
	adx /= float64(period)
	for i := period; i < len(dx); i++ {
		adx = (adx*float64(period-1) + dx[i]) / float64(period)
	}
	return adx
}
