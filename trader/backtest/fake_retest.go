package backtest

import (
	"fmt"
	"math"
	"strings"

	"nofx/market"
)

// fake_retest.go replays the LIVE structural_fit "fake_retest_trap" gate over each
// historical entry, then compares FOUR ways of deciding the same block:
//
//	(A) LIVE:    fixed |entry-anchor|/entry < 0.15%           (current production)
//	(B) FIXED%:  sweep the same percent threshold
//	(C) ATR:     |entry-anchor| < k*ATR   (volatility-scaled)
//	(D) CONFIRM: replace the distance proxy with a REAL closed-candle confirmation
//	             check on the pre-entry bars (was there a genuine rejection+hold?)
//
// The live gate (trader/entry_gate.go:709-742) is a pure distance proxy: it blocks
// long-at-support / short-at-resistance whenever entry sits within 0.15% of the
// anchor, on the theory that "price just arrived, so it cannot have confirmed."
// SLAnchor is exactly that invalidation-side level, so |entry-SLAnchor|/entry
// reproduces the gate faithfully.
//
// For every variant we split PASS vs BLOCK and report each group's realized PnL,
// win-rate and entry quality (MFE/MAE). A gate is USEFUL only if the BLOCK group is
// genuinely worse than the PASS group; if BLOCK trades were profitable, the gate is
// killing good entries.

// fakeRetestDistPct returns the entry-to-anchor distance in percent, and whether the
// entry carries a usable adverse-side structural anchor (the fake_retest precondition).
func fakeRetestDistPct(le loadedEntry) (distPct float64, ok bool) {
	if le.entry.Structural == nil {
		return 0, false
	}
	anchor := le.entry.Structural.SLAnchor
	entry := le.entry.EntryPrice
	if anchor <= 0 || entry <= 0 {
		return 0, false
	}
	return math.Abs(entry-anchor) / entry * 100, true
}

// realCandleConfirmed inspects the pre-entry bars to decide whether a genuine
// closed-candle confirmation existed before entry — the thing the live proxy only
// GUESSES at via distance. Definition (symmetric):
//   - long (anchor=support): within the last `look` closed pre-entry bars, some bar
//     TOUCHED the anchor zone (Low <= anchor*(1+touchTol)), and the bar AFTER the
//     touch CLOSED back above the anchor, AND the next bar also closed above it
//     (2-candle hold — the "candle after the touch must also close in your
//     direction" anti-fake-retest rule).
//   - short (anchor=resistance): mirror (High >= anchor*(1-touchTol), then two
//     closes back below).
//
// touchTol lets "near the level" count as a touch (default 0.1%). Returns whether a
// confirmation was found within the window.
func realCandleConfirmed(le loadedEntry, look int, touchTol float64) bool {
	if le.entry.Structural == nil {
		return false
	}
	anchor := le.entry.Structural.SLAnchor
	if anchor <= 0 || le.entryIdx < 3 {
		return false
	}
	isLong := strings.EqualFold(le.entry.Side, "LONG")
	start := le.entryIdx - look
	if start < 1 {
		start = 1
	}
	// scan touch candles, then require 2 closes back on the correct side after it
	for i := start; i < le.entryIdx-1; i++ {
		b := le.bars[i]
		touched := false
		if isLong {
			touched = b.Low <= anchor*(1+touchTol)
		} else {
			touched = b.High >= anchor*(1-touchTol)
		}
		if !touched {
			continue
		}
		// need the next TWO closed bars on the correct side of the anchor
		c1 := le.bars[i+1]
		if i+2 > le.entryIdx {
			continue
		}
		c2 := le.bars[i+2]
		var hold1, hold2 bool
		if isLong {
			hold1 = c1.Close > anchor
			hold2 = c2.Close > anchor
		} else {
			hold1 = c1.Close < anchor
			hold2 = c2.Close < anchor
		}
		if hold1 && hold2 {
			return true
		}
	}
	return false
}

var _ = market.Kline{}

// frAgg accumulates a PASS or BLOCK group's realized PnL and entry-quality profile.
type frAgg struct {
	n              int
	wins           int
	sumRealized    float64
	sumMAE, sumMFE float64
	immediate      int
}

func (a *frAgg) add(le loadedEntry, atr float64) {
	a.n++
	if le.entry.RealizedPnL > 0 {
		a.wins++
	}
	a.sumRealized += le.entry.RealizedPnL
	q := oneEntryQuality(le, atr)
	a.sumMAE += q.MAEatr
	a.sumMFE += q.MFEatr
	if q.Immediate {
		a.immediate++
	}
}

func frRow(sb *strings.Builder, label string, a frAgg) {
	if a.n == 0 {
		sb.WriteString(fmt.Sprintf("    %-6s n=0\n", label))
		return
	}
	n := float64(a.n)
	sb.WriteString(fmt.Sprintf("    %-6s n=%-3d win%%=%4.0f realizedPnL=%+8.2f avgPnL=%+6.2f MAE=%.2f MFE=%.2f MFE/MAE=%.2f immAdv=%.0f%%\n",
		label, a.n, 100*float64(a.wins)/n, a.sumRealized, a.sumRealized/n,
		a.sumMAE/n, a.sumMFE/n, (a.sumMFE/n)/((a.sumMAE/n)+1e-9), 100*float64(a.immediate)/n))
}

// splitByBlock partitions loaded entries into (pass, block) via a per-entry blocked
// predicate, accumulating each group. Entries without a usable ATR/anchor are skipped.
func splitByBlock(loaded []loadedEntry, blocked func(le loadedEntry, atr float64) bool) (pass, block frAgg, evaluated int) {
	for _, le := range loaded {
		atr := entryATR(le.bars, le.entryIdx)
		if atr <= 0 || le.entry.EntryPrice <= 0 {
			continue
		}
		if _, ok := fakeRetestDistPct(le); !ok {
			continue // no adverse-side anchor → gate can't apply
		}
		evaluated++
		if blocked(le, atr) {
			block.add(le, atr)
		} else {
			pass.add(le, atr)
		}
	}
	return
}

// FormatFakeRetestSweep runs all four variants and prints the PASS/BLOCK comparison.
func FormatFakeRetestSweep(trader string, loaded []loadedEntry) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("==== FAKE-RETEST GATE SWEEP: %s ====\n", trader))
	sb.WriteString("(SLAnchor = invalidation-side structural level; gate blocks long@support / short@resistance too close to it)\n")

	// how many entries even carry an anchor?
	var withAnchor int
	var dists []float64
	for _, le := range loaded {
		if d, ok := fakeRetestDistPct(le); ok {
			withAnchor++
			dists = append(dists, d)
		}
	}
	sb.WriteString(fmt.Sprintf("entries with usable adverse anchor: %d / %d\n", withAnchor, len(loaded)))
	if withAnchor > 0 {
		sb.WriteString(fmt.Sprintf("entry→anchor distance distribution: min=%.3f%% median=%.3f%% max=%.3f%%\n",
			minf(dists), medianf(dists), maxf(dists)))
	}

	// (A) LIVE fixed 0.15%
	sb.WriteString("\n-- (A) LIVE gate: fixed |dist| < 0.15% --\n")
	pass, block, ev := splitByBlock(loaded, func(le loadedEntry, _ float64) bool {
		d, _ := fakeRetestDistPct(le)
		return d < 0.15
	})
	sb.WriteString(fmt.Sprintf("   evaluated=%d  blocked=%d (%.0f%%)\n", ev, block.n, pct(block.n, ev)))
	frRow(&sb, "PASS", pass)
	frRow(&sb, "BLOCK", block)

	// (B) FIXED% sweep
	sb.WriteString("\n-- (B) FIXED% threshold sweep --\n")
	for _, thr := range []float64{0.05, 0.10, 0.15, 0.20, 0.30, 0.50} {
		p, b, e := splitByBlock(loaded, func(le loadedEntry, _ float64) bool {
			d, _ := fakeRetestDistPct(le)
			return d < thr
		})
		sb.WriteString(fmt.Sprintf("   thr=%.2f%%: blocked=%d/%d (%.0f%%) | BLOCK realizedPnL=%+.2f avg=%+.2f | PASS realizedPnL=%+.2f avg=%+.2f\n",
			thr, b.n, e, pct(b.n, e), b.sumRealized, avg(b.sumRealized, b.n), p.sumRealized, avg(p.sumRealized, p.n)))
	}

	// (C) ATR-scaled
	sb.WriteString("\n-- (C) ATR-scaled threshold: |entry-anchor| < k*ATR --\n")
	for _, k := range []float64{0.10, 0.15, 0.20, 0.30, 0.50} {
		p, b, e := splitByBlock(loaded, func(le loadedEntry, atr float64) bool {
			return math.Abs(le.entry.EntryPrice-le.entry.Structural.SLAnchor) < k*atr
		})
		sb.WriteString(fmt.Sprintf("   k=%.2f ATR: blocked=%d/%d (%.0f%%) | BLOCK realizedPnL=%+.2f avg=%+.2f | PASS realizedPnL=%+.2f avg=%+.2f\n",
			k, b.n, e, pct(b.n, e), b.sumRealized, avg(b.sumRealized, b.n), p.sumRealized, avg(p.sumRealized, p.n)))
	}

	// (D) REAL candle confirmation (replay-TF bars)
	sb.WriteString("\n-- (D) REAL closed-candle confirmation (block when NO genuine confirm found) --\n")
	for _, look := range []int{4, 6, 8} {
		p, b, e := splitByBlock(loaded, func(le loadedEntry, _ float64) bool {
			return !realCandleConfirmed(le, look, 0.001) // 0.1% touch tolerance
		})
		sb.WriteString(fmt.Sprintf("   look=%d bars: blocked(no-confirm)=%d/%d (%.0f%%) | BLOCK realizedPnL=%+.2f avg=%+.2f | PASS realizedPnL=%+.2f avg=%+.2f\n",
			look, b.n, e, pct(b.n, e), b.sumRealized, avg(b.sumRealized, b.n), p.sumRealized, avg(p.sumRealized, p.n)))
	}
	sb.WriteString("\nRead: a gate helps only if BLOCK avg PnL < PASS avg PnL (it removes the worse trades).\n")
	return sb.String()
}

// ── small numeric helpers ──
func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}
func avg(sum float64, n int) float64 {
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}
func minf(xs []float64) float64 {
	m := xs[0]
	for _, x := range xs {
		if x < m {
			m = x
		}
	}
	return m
}
func maxf(xs []float64) float64 {
	m := xs[0]
	for _, x := range xs {
		if x > m {
			m = x
		}
	}
	return m
}
func medianf(xs []float64) float64 {
	c := append([]float64(nil), xs...)
	for i := 1; i < len(c); i++ {
		for j := i; j > 0 && c[j-1] > c[j]; j-- {
			c[j-1], c[j] = c[j], c[j-1]
		}
	}
	return c[len(c)/2]
}
