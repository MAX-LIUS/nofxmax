package backtest

import (
	"fmt"
	"strings"
)

// anchor_proximity.go quantifies the "entry floating in no-man's-land" problem: for
// each entry that carries a structural SL anchor (the bare confirmed support/resistance
// level the AI cited), it measures how far the ENTRY sits from that anchor in ATR
// multiples, then buckets realized PnL. A long entered far ABOVE its support anchor (or
// a short far BELOW its resistance) has no nearby structure to defend the position — the
// exact case a tightened stop gets whipsawed on. This gives the empirical threshold for
// a hard "entry must sit within N×ATR of a confirmed direction-aligned anchor" gate.
func FormatAnchorProximity(trader string, loaded []loadedEntry) string {
	var sb strings.Builder
	type item struct {
		distATR float64
		pnl     float64
		win     bool
	}
	items := make([]item, 0, len(loaded))
	var noAnchor int
	for _, le := range loaded {
		atr := entryATR(le.bars, le.entryIdx)
		if atr <= 0 || le.entry.EntryPrice <= 0 || le.entry.Structural == nil {
			continue
		}
		anchor := le.entry.Structural.SLAnchor
		if anchor <= 0 {
			noAnchor++
			continue
		}
		dist := le.entry.EntryPrice - anchor
		if dist < 0 {
			dist = -dist
		}
		items = append(items, item{
			distATR: dist / atr,
			pnl:     le.entry.RealizedPnL,
			win:     le.entry.RealizedPnL >= 0,
		})
	}
	sb.WriteString(fmt.Sprintf("==== ANCHOR PROXIMITY: %s (n=%d w/ SL anchor; %d had none) ====\n", trader, len(items), noAnchor))
	if len(items) == 0 {
		sb.WriteString("  (no entries carry a structural SL anchor)\n")
		return sb.String()
	}

	edges := []float64{0, 0.5, 1.0, 1.5, 2.0, 3.0, 1e9}
	names := []string{"<0.5", "0.5-1.0", "1.0-1.5", "1.5-2.0", "2.0-3.0", ">=3.0"}
	type row struct {
		n    int
		pnl  float64
		wins int
	}
	rows := make([]row, len(names))
	for _, it := range items {
		for b := 0; b < len(names); b++ {
			if it.distATR >= edges[b] && it.distATR < edges[b+1] {
				rows[b].n++
				rows[b].pnl += it.pnl
				if it.win {
					rows[b].wins++
				}
				break
			}
		}
	}
	sb.WriteString("  -- entry→SL-anchor distance in ATR (how far entry floats from confirmed structure) --\n")
	sb.WriteString(fmt.Sprintf("  %-10s %-5s %-10s %-6s\n", "anchorATR", "n", "realPnL", "win%"))
	for b := range rows {
		if rows[b].n == 0 {
			sb.WriteString(fmt.Sprintf("  %-10s %-5d %-10s %-6s\n", names[b], 0, "-", "-"))
			continue
		}
		sb.WriteString(fmt.Sprintf("  %-10s %-5d %-+10.2f %-6.1f\n",
			names[b], rows[b].n, rows[b].pnl, 100*float64(rows[b].wins)/float64(rows[b].n)))
	}

	// Gate simulation: block entries whose anchor sits farther than N×ATR.
	sb.WriteString("\n  -- gate: block if entry→anchor > N×ATR (kept vs removed realized PnL) --\n")
	sb.WriteString(fmt.Sprintf("  %-8s %-6s %-11s %-6s | %-8s %-11s\n", "maxATR", "nKept", "keptPnL", "kW%", "nBlock", "blockPnL"))
	for _, thr := range []float64{0.5, 0.8, 1.0, 1.5, 2.0} {
		var keptPnL, blockPnL float64
		var nKept, nBlock, keptWins int
		for _, it := range items {
			if it.distATR > thr {
				blockPnL += it.pnl
				nBlock++
			} else {
				keptPnL += it.pnl
				nKept++
				if it.win {
					keptWins++
				}
			}
		}
		kw := 0.0
		if nKept > 0 {
			kw = 100 * float64(keptWins) / float64(nKept)
		}
		sb.WriteString(fmt.Sprintf("  %-8.1f %-6d %-+11.2f %-6.1f | %-8d %-+11.2f\n",
			thr, nKept, keptPnL, kw, nBlock, blockPnL))
	}
	return sb.String()
}
