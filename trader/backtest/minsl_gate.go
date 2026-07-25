package backtest

import (
	"fmt"
	"strings"
)

// minsl_gate.go simulates the entry gate's sl_distance_below_atr_min check across a
// grid of thresholds. The live gate blocks an entry whose AI-declared invalidation sits
// closer than MinSLDistanceATRMul × ATR from entry (a too-tight stop that normal noise
// will sweep). We proxy the AI invalidation with Structural.SLPrice (buffer included),
// falling back to SLAnchor. For each threshold we report realized PnL kept vs blocked so
// the frequency/PnL tradeoff of raising the floor (0.2 → 0.8/1.0/1.2/1.5) is explicit.
func FormatMinSLGate(trader string, loaded []loadedEntry, thresholds []float64) string {
	var sb strings.Builder
	type item struct {
		slATR float64
		pnl   float64
		win   bool
	}
	items := make([]item, 0, len(loaded))
	var noSL int
	for _, le := range loaded {
		atr := entryATR(le.bars, le.entryIdx)
		if atr <= 0 || le.entry.EntryPrice <= 0 || le.entry.Structural == nil {
			continue
		}
		sl := le.entry.Structural.SLPrice
		if sl <= 0 {
			sl = le.entry.Structural.SLAnchor
		}
		if sl <= 0 {
			noSL++
			continue
		}
		d := le.entry.EntryPrice - sl
		if d < 0 {
			d = -d
		}
		items = append(items, item{slATR: d / atr, pnl: le.entry.RealizedPnL, win: le.entry.RealizedPnL >= 0})
	}
	sb.WriteString(fmt.Sprintf("==== MIN-SL-DISTANCE GATE: %s (n=%d w/ AI SL; %d none) ====\n", trader, len(items), noSL))
	if len(items) == 0 {
		sb.WriteString("  (no entries carry an AI structural stop)\n")
		return sb.String()
	}
	var total float64
	for _, it := range items {
		total += it.pnl
	}
	sb.WriteString(fmt.Sprintf("  (total realized PnL over %d entries = %+.2f)\n", len(items), total))
	sb.WriteString(fmt.Sprintf("  %-8s %-6s %-11s %-6s | %-8s %-11s %-6s\n", "minSLatr", "nKept", "keptPnL", "kW%", "nBlock", "blockPnL", "bW%"))
	for _, thr := range thresholds {
		var keptPnL, blockPnL float64
		var nKept, nBlock, kw, bw int
		for _, it := range items {
			if it.slATR < thr {
				blockPnL += it.pnl
				nBlock++
				if it.win {
					bw++
				}
			} else {
				keptPnL += it.pnl
				nKept++
				if it.win {
					kw++
				}
			}
		}
		kwp := 0.0
		if nKept > 0 {
			kwp = 100 * float64(kw) / float64(nKept)
		}
		bwp := 0.0
		if nBlock > 0 {
			bwp = 100 * float64(bw) / float64(nBlock)
		}
		sb.WriteString(fmt.Sprintf("  %-8.1f %-6d %-+11.2f %-6.1f | %-8d %-+11.2f %-6.1f\n",
			thr, nKept, keptPnL, kwp, nBlock, blockPnL, bwp))
	}
	return sb.String()
}
