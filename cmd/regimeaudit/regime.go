package main

import "math"

var slopeWindow = 50

func setSlopeWindow(w int) {
	if w > 0 {
		slopeWindow = w
	}
}

// regimeAt classifies market state at bar i using ONLY bars[0..i] (no look-ahead).
// Returns "CHOP" / "TREND_UP" / "TREND_DN" / "FLAT".
// ADX<20 + Chop>61.8 + ER<0.25 consensus (>=2) => CHOP.
// Else if trending (ADX>=20 or Chop<61.8), use 50-bar slope sign for direction.
func regimeAt(h, l, c []float64, i int) string {
	adxVal := adx14(h, l, c, i)
	chopVal := choppiness14(h, l, c, i)
	erVal := efficiencyRatio(c, i, 14)
	votes := 0
	if adxVal < 20 {
		votes++
	}
	if chopVal > 61.8 {
		votes++
	}
	if erVal < 0.25 {
		votes++
	}
	if votes >= 2 {
		return "CHOP"
	}
	// trending: use slope of last W bars for direction
	W := slopeWindow
	if i < W {
		return "FLAT"
	}
	logc := make([]float64, W)
	for k := 0; k < W; k++ {
		logc[k] = math.Log(c[i-W+1+k])
	}
	n := float64(W)
	mx := (n - 1) / 2
	my := 0.0
	for _, y := range logc {
		my += y
	}
	my /= n
	sxy := 0.0
	for k := 0; k < W; k++ {
		sxy += (float64(k) - mx) * (logc[k] - my)
	}
	if sxy > 0 {
		return "TREND_UP"
	} else if sxy < 0 {
		return "TREND_DN"
	}
	return "FLAT"
}

// entryAligns returns true if the position's side matches the regime direction.
// CHOP: no trend-following entries allowed (reject all? or define a chop-specific logic?)
// For now: CHOP always rejects (user can refine later).
// TREND_UP: allow LONG, reject SHORT.
// TREND_DN: allow SHORT, reject LONG.
// FLAT: allow all.
func entryAligns(side, regime string) bool {
	switch regime {
	case "CHOP":
		return true // keep chop entries (isolate pure counter-trend gate)
	case "TREND_UP":
		return side == "LONG"
	case "TREND_DN":
		return side == "SHORT"
	case "FLAT":
		return true
	default:
		return true
	}
}

func adx14(h, l, c []float64, end int) float64 {
	p := 14
	if end < 2*p {
		return 50
	}
	start := end - 4*p
	if start < 1 {
		start = 1
	}
	var atr, pdi, ndi float64
	first := true
	var dxSum float64
	var dxN int
	for i := start; i <= end; i++ {
		tr := math.Max(h[i]-l[i], math.Max(math.Abs(h[i]-c[i-1]), math.Abs(l[i]-c[i-1])))
		up := h[i] - h[i-1]
		dn := l[i-1] - l[i]
		pdm, ndm := 0.0, 0.0
		if up > dn && up > 0 {
			pdm = up
		}
		if dn > up && dn > 0 {
			ndm = dn
		}
		if first {
			atr, pdi, ndi = tr, pdm, ndm
			first = false
			continue
		}
		atr = (atr*float64(p-1) + tr) / float64(p)
		pdi = (pdi*float64(p-1) + pdm) / float64(p)
		ndi = (ndi*float64(p-1) + ndm) / float64(p)
		if atr <= 0 {
			continue
		}
		pd := 100 * pdi / atr
		nd := 100 * ndi / atr
		s := pd + nd
		dx := 0.0
		if s > 0 {
			dx = 100 * math.Abs(pd-nd) / s
		}
		dxSum += dx
		dxN++
	}
	if dxN == 0 {
		return 50
	}
	return dxSum / float64(dxN)
}

func choppiness14(h, l, c []float64, end int) float64 {
	n := 14
	if end < n {
		return 50
	}
	sumTR := 0.0
	hh, ll := h[end-n+1], l[end-n+1]
	for i := end - n + 1; i <= end; i++ {
		tr := h[i] - l[i]
		if i > 0 {
			tr = math.Max(h[i]-l[i], math.Max(math.Abs(h[i]-c[i-1]), math.Abs(l[i]-c[i-1])))
		}
		sumTR += tr
		if h[i] > hh {
			hh = h[i]
		}
		if l[i] < ll {
			ll = l[i]
		}
	}
	rng := hh - ll
	if rng <= 0 || sumTR <= 0 {
		return 50
	}
	return 100 * math.Log10(sumTR/rng) / math.Log10(float64(n))
}

func efficiencyRatio(c []float64, end, n int) float64 {
	if end < n {
		return 1
	}
	chg := math.Abs(c[end] - c[end-n])
	vol := 0.0
	for i := end - n + 1; i <= end; i++ {
		vol += math.Abs(c[i] - c[i-1])
	}
	if vol <= 0 {
		return 1
	}
	return chg / vol
}
