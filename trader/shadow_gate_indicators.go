package trader

import "math"

// Indicator helpers for shadow gates. All operate on a slice of closes/highs/lows
// where the LAST element is the most recent CLOSED bar. No look-ahead: callers
// pass only bars up to and including the decision bar.

func sgADX(h, l, c []float64, period int) float64 {
	n := len(c)
	if n < 2*period+1 {
		return 50
	}
	start := n - 1 - 4*period
	if start < 1 {
		start = 1
	}
	var atr, pdi, ndi float64
	first := true
	var dxSum float64
	var dxN int
	for i := start; i < n; i++ {
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
		atr = (atr*float64(period-1) + tr) / float64(period)
		pdi = (pdi*float64(period-1) + pdm) / float64(period)
		ndi = (ndi*float64(period-1) + ndm) / float64(period)
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

func sgChoppiness(h, l, c []float64, period int) float64 {
	n := len(c)
	if n < period+1 {
		return 50
	}
	sumTR := 0.0
	hh, ll := h[n-period], l[n-period]
	for i := n - period; i < n; i++ {
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
	return 100 * math.Log10(sumTR/rng) / math.Log10(float64(period))
}

func sgEfficiencyRatio(c []float64, period int) float64 {
	n := len(c)
	if n < period+1 {
		return 1
	}
	chg := math.Abs(c[n-1] - c[n-1-period])
	vol := 0.0
	for i := n - period; i < n; i++ {
		vol += math.Abs(c[i] - c[i-1])
	}
	if vol <= 0 {
		return 1
	}
	return chg / vol
}

// sgSlopeSign returns +1 (up) / -1 (down) / 0 for the log-price regression slope
// over the last window bars.
func sgSlopeSign(c []float64, window int) int {
	n := len(c)
	if n < window {
		return 0
	}
	seg := c[n-window:]
	mx := float64(window-1) / 2
	my := 0.0
	logs := make([]float64, window)
	for i, v := range seg {
		if v <= 0 {
			return 0
		}
		logs[i] = math.Log(v)
		my += logs[i]
	}
	my /= float64(window)
	sxy := 0.0
	for i := 0; i < window; i++ {
		sxy += (float64(i) - mx) * (logs[i] - my)
	}
	if sxy > 0 {
		return 1
	} else if sxy < 0 {
		return -1
	}
	return 0
}

// sgDonchianBreak returns +1 if last close breaks above prior-N high, -1 if below
// prior-N low, 0 otherwise.
func sgDonchianBreak(h, l, c []float64, n int) int {
	m := len(c)
	if m < n+1 {
		return 0
	}
	hh := h[m-1-n]
	ll := l[m-1-n]
	for i := m - 1 - n; i < m-1; i++ {
		if h[i] > hh {
			hh = h[i]
		}
		if l[i] < ll {
			ll = l[i]
		}
	}
	if c[m-1] > hh {
		return 1
	}
	if c[m-1] < ll {
		return -1
	}
	return 0
}

// sgConsensusChop reports whether >=2 of {ADX<20, Chop>61.8, ER<0.25} hold.
func sgConsensusChop(h, l, c []float64) bool {
	votes := 0
	if sgADX(h, l, c, 14) < 20 {
		votes++
	}
	if sgChoppiness(h, l, c, 14) > 61.8 {
		votes++
	}
	if sgEfficiencyRatio(c, 14) < 0.25 {
		votes++
	}
	return votes >= 2
}
