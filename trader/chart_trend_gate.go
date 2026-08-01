package trader

import (
	"fmt"
	"math"
	"strings"
)

// chart_trend_gate.go implements the "chart_trend" regime-gate category: an
// ALLOW-list gate that only lets an open through when the pre-entry price action
// is a clean visual trend in the trade direction. It mirrors the backtested
// Python chart gate (validated on 1h: BN book -53 -> +18..+35):
//
//	ALLOW iff  swingAlign >= alignMin  AND  regChannelR2 >= r2Min  AND  dirSlope > 0
//
// where dirSlope is the log-price regression slope signed by trade direction
// (positive = channel slopes with the position). Everything else is blocked.
// Uses the last `window` closed bars of the causal shadow context (no look-ahead).

// chartRegChannel returns the log-price linear-regression slope as percent-per-bar
// and the R^2 (trend cleanliness) over the last `window` closes.
func chartRegChannel(closes []float64, window int) (slopePct, r2 float64) {
	n := len(closes)
	if window <= 2 || n < window {
		return 0, 0
	}
	seg := closes[n-window:]
	xm := float64(window-1) / 2
	ym := 0.0
	for _, v := range seg {
		ym += v
	}
	ym /= float64(window)
	var sxy, sxx, syy float64
	for i, v := range seg {
		dx := float64(i) - xm
		dy := v - ym
		sxy += dx * dy
		sxx += dx * dx
		syy += dy * dy
	}
	if sxx <= 0 || ym == 0 {
		return 0, 0
	}
	slope := sxy / sxx
	slopePct = slope / ym * 100
	if syy > 0 {
		r2 = (sxy * sxy) / (sxx * syy)
	}
	return slopePct, r2
}

// chartSwingAlign returns the fraction of consecutive swing points that agree with
// the trade direction (higher-highs+higher-lows for LONG, lower for SHORT) over the
// given highs/lows. lb is the fractal pivot half-width (2, matching the backtest).
func chartSwingAlign(highs, lows []float64, side string, lb int) float64 {
	if lb < 1 {
		lb = 2
	}
	n := len(highs)
	if n != len(lows) || n < 2*lb+2 {
		return 0.5
	}
	var sh, sl []float64
	for i := lb; i < n-lb; i++ {
		isHigh, isLow := true, true
		for j := 1; j <= lb; j++ {
			if !(highs[i] >= highs[i-j] && highs[i] >= highs[i+j]) {
				isHigh = false
			}
			if !(lows[i] <= lows[i-j] && lows[i] <= lows[i+j]) {
				isLow = false
			}
		}
		if isHigh {
			sh = append(sh, highs[i])
		}
		if isLow {
			sl = append(sl, lows[i])
		}
	}
	long := strings.EqualFold(side, "LONG")
	agree := 0
	for i := 1; i < len(sh); i++ {
		if (long && sh[i] > sh[i-1]) || (!long && sh[i] < sh[i-1]) {
			agree++
		}
	}
	for i := 1; i < len(sl); i++ {
		if (long && sl[i] > sl[i-1]) || (!long && sl[i] < sl[i-1]) {
			agree++
		}
	}
	tot := 0
	if len(sh) > 1 {
		tot += len(sh) - 1
	}
	if len(sl) > 1 {
		tot += len(sl) - 1
	}
	if tot == 0 {
		return 0.5
	}
	return float64(agree) / float64(tot)
}

// chartTrendGate evaluates the chart_trend ALLOW-list gate. Returns (block, detail).
// block=true means the entry is NOT a clean chart trend and should be rejected.
//
// pivotLB is the swing-pivot lookback for the alignment term. Callers pass 2 to
// reproduce the original calibration; it used to be hardcoded, which made the
// gate unable to express the lb=3 setting the structural gate treats as the
// decisive one.
func chartTrendGate(c shadowGateCtx, window int, alignMin, r2Min float64, pivotLB int) (bool, string) {
	if pivotLB <= 0 {
		pivotLB = 2
	}
	slopePct, r2 := chartRegChannel(c.closes, window)
	dirSlope := slopePct
	if strings.EqualFold(c.side, "SHORT") {
		dirSlope = -slopePct
	}
	align := chartSwingAlign(c.highs, c.lows, c.side, pivotLB)
	allow := align >= alignMin && r2 >= r2Min && dirSlope > 0
	detail := fmt.Sprintf("align=%.2f(>=%.2f,lb=%d) r2=%.2f(>=%.2f) dirSlope=%+.3f side=%s allow=%v",
		align, alignMin, pivotLB, r2, r2Min, dirSlope, c.side, allow)
	return !allow, detail
}

var _ = math.Abs
