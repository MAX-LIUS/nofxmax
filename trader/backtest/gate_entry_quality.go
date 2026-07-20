package backtest

import (
	"fmt"
	"math"
	"strings"
)

// gate_entry_quality.go replays the LIVE chart_trend ALLOW-list gate over each
// historical entry's PRE-ENTRY bars, then splits the entry-quality profile into
// PASS vs BLOCK groups. This answers: does the gate actually select better
// entries (higher MFE/MAE, lower immediate-adverse), i.e. is it doing the
// root-cause fix rather than just tightening stops?
//
// The gate math mirrors trader/chart_trend_gate.go exactly:
//   ALLOW iff swingAlign >= alignMin AND regChannelR2 >= r2Min AND dirSlope > 0

func gateRegChannel(closes []float64, window int) (slopePct, r2 float64) {
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

func gateSwingAlign(highs, lows []float64, side string, lb int) float64 {
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

// chartTrendAllow returns true if the entry PASSES the chart_trend gate given its
// pre-entry window (bars strictly before entryIdx).
func chartTrendAllow(le loadedEntry, window int, alignMin, r2Min float64) (bool, float64, float64, float64) {
	if le.entryIdx < window {
		return false, 0, 0, 0 // insufficient history -> treat as block (gate can't confirm)
	}
	pre := le.bars[:le.entryIdx]
	closes := make([]float64, len(pre))
	highs := make([]float64, len(pre))
	lows := make([]float64, len(pre))
	for i, b := range pre {
		closes[i] = b.Close
		highs[i] = b.High
		lows[i] = b.Low
	}
	slopePct, r2 := gateRegChannel(closes, window)
	dirSlope := slopePct
	if strings.EqualFold(le.entry.Side, "SHORT") {
		dirSlope = -slopePct
	}
	align := gateSwingAlign(highs, lows, le.entry.Side, 2)
	allow := align >= alignMin && r2 >= r2Min && dirSlope > 0
	return allow, align, r2, dirSlope
}

// FormatGateEntryQuality splits entry quality by the chart_trend gate verdict.
func FormatGateEntryQuality(trader string, loaded []loadedEntry, window int, alignMin, r2Min float64) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("==== GATE-SPLIT ENTRY QUALITY: %s (chart_trend win=%d align>=%.2f r2>=%.2f) ====\n",
		trader, window, alignMin, r2Min))

	type agg struct {
		n                   int
		sumMAE, sumMFE      float64
		sumFirst            float64
		immediate, neverGrn int
		sumRealized         float64
	}
	var pass, block agg
	eqs := AnalyzeEntryQuality(loaded)
	// index eqs by pointer position: AnalyzeEntryQuality skips zero-ATR entries, so
	// recompute inline to keep alignment simple.
	_ = eqs
	for _, le := range loaded {
		atr := entryATR(le.bars, le.entryIdx)
		if atr <= 0 || le.entry.EntryPrice <= 0 {
			continue
		}
		allow, _, _, _ := chartTrendAllow(le, window, alignMin, r2Min)
		q := oneEntryQuality(le, atr)
		tgt := &block
		if allow {
			tgt = &pass
		}
		tgt.n++
		tgt.sumMAE += q.MAEatr
		tgt.sumMFE += q.MFEatr
		tgt.sumFirst += q.FirstMove
		if q.Immediate {
			tgt.immediate++
		}
		if !q.EverGreen {
			tgt.neverGrn++
		}
		tgt.sumRealized += le.entry.RealizedPnL
	}

	row := func(label string, a agg) {
		if a.n == 0 {
			sb.WriteString(fmt.Sprintf("  %-7s n=0\n", label))
			return
		}
		n := float64(a.n)
		sb.WriteString(fmt.Sprintf("  %-7s n=%-4d MAE=%.2f MFE=%.2f MFE/MAE=%.2f first=%+.2f immAdv=%.0f%% neverGrn=%.0f%% liveRealizedPnL=%.2f\n",
			label, a.n, a.sumMAE/n, a.sumMFE/n, (a.sumMFE/n)/((a.sumMAE/n)+1e-9),
			a.sumFirst/n, 100*float64(a.immediate)/n, 100*float64(a.neverGrn)/n, a.sumRealized))
	}
	row("PASS", pass)
	row("BLOCK", block)
	tot := pass.n + block.n
	if tot > 0 {
		sb.WriteString(fmt.Sprintf("  -> gate passes %d/%d = %.0f%% of entries\n",
			pass.n, tot, 100*float64(pass.n)/float64(tot)))
	}
	return sb.String()
}

// oneEntryQuality computes the excursion profile for a single loaded entry.
func oneEntryQuality(le loadedEntry, atr float64) EntryQuality {
	isLong := strings.EqualFold(le.entry.Side, "LONG")
	entry := le.entry.EntryPrice
	eq := EntryQuality{Symbol: le.entry.Symbol, Side: le.entry.Side}
	end := le.entryIdx + eqWindowBars
	if end >= len(le.bars) {
		end = len(le.bars) - 1
	}
	firstFav, firstAdv := -1, -1
	for i := le.entryIdx; i <= end; i++ {
		b := le.bars[i]
		var favExc, advExc float64
		if isLong {
			favExc = (b.High - entry) / atr
			advExc = (entry - b.Low) / atr
		} else {
			favExc = (entry - b.Low) / atr
			advExc = (b.High - entry) / atr
		}
		if favExc > eq.MFEatr {
			eq.MFEatr = favExc
			eq.BarsToMFE = i - le.entryIdx
		}
		if advExc > eq.MAEatr {
			eq.MAEatr = advExc
		}
		if favExc > 0.5 && firstFav < 0 {
			firstFav = i
		}
		if advExc > 0.5 && firstAdv < 0 {
			firstAdv = i
		}
		if i == le.entryIdx {
			if isLong {
				eq.FirstMove = (b.Close - entry) / atr
			} else {
				eq.FirstMove = (entry - b.Close) / atr
			}
		}
	}
	eq.EverGreen = eq.MFEatr > 0.1
	eq.Immediate = firstAdv >= 0 && (firstFav < 0 || firstAdv < firstFav)
	return eq
}

var _ = math.Abs
