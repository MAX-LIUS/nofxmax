package backtest

import (
	"fmt"
	"sort"
	"strings"
)

// drawdown_source.go localizes where big drawdown comes from. It buckets every
// entry by its post-entry MAE (in ATR) and by a "no-progress" signal (never went
// favorable by k ATR within w bars), then reports realized PnL and the dominant
// close_reason per bucket. This pinpoints whether large losses are a few deep-MAE
// trades riding the wide backstop, and whether a no-progress / not-green early exit
// would cut them without touching normal-volatility trades.

type ddBucket struct {
	label    string
	n        int
	pnl      float64
	wins     int
	reasons  map[string]int
}

func (b *ddBucket) addReason(r string) {
	if b.reasons == nil {
		b.reasons = map[string]int{}
	}
	b.reasons[r]++
}

func (b *ddBucket) topReason() string {
	best, bn := "", 0
	for r, c := range b.reasons {
		if c > bn {
			best, bn = r, c
		}
	}
	if best == "" {
		return "-"
	}
	return fmt.Sprintf("%s(%d)", best, bn)
}

// FormatDrawdownSource renders MAE buckets vs realized PnL + close_reason, and a
// no-progress early-exit simulation.
func FormatDrawdownSource(trader string, loaded []loadedEntry) string {
	var sb strings.Builder
	type item struct {
		mae, mfe, firstMove float64
		barsToMFE           int
		everGreen           bool
		firstBarGreen       bool
		pnl                 float64
		reason              string
	}
	items := make([]item, 0, len(loaded))
	for _, le := range loaded {
		atr := entryATR(le.bars, le.entryIdx)
		if atr <= 0 || le.entry.EntryPrice <= 0 {
			continue
		}
		eq := oneEntryQuality(le, atr)
		// first-bar green: did the entry bar CLOSE favorable?
		fbg := eq.FirstMove > 0
		items = append(items, item{
			mae: eq.MAEatr, mfe: eq.MFEatr, firstMove: eq.FirstMove,
			barsToMFE: eq.BarsToMFE, everGreen: eq.EverGreen, firstBarGreen: fbg,
			pnl: le.entry.RealizedPnL, reason: le.entry.CloseReason,
		})
	}
	sb.WriteString(fmt.Sprintf("==== DRAWDOWN SOURCE: %s (n=%d) ====\n", trader, len(items)))
	if len(items) == 0 {
		sb.WriteString("  (no analyzable entries)\n")
		return sb.String()
	}

	// MAE buckets.
	edges := []float64{0, 1.0, 2.0, 3.0, 4.5, 1e9}
	names := []string{"<1.0", "1.0-2.0", "2.0-3.0", "3.0-4.5", ">=4.5"}
	rows := make([]ddBucket, len(names))
	for i := range rows {
		rows[i].label = names[i]
	}
	for _, it := range items {
		for b := 0; b < len(names); b++ {
			if it.mae >= edges[b] && it.mae < edges[b+1] {
				rows[b].n++
				rows[b].pnl += it.pnl
				if it.pnl >= 0 {
					rows[b].wins++
				}
				rows[b].addReason(it.reason)
				break
			}
		}
	}
	sb.WriteString("  -- by post-entry MAE (ATR) --\n")
	sb.WriteString(fmt.Sprintf("  %-9s %-5s %-10s %-6s %-18s\n", "MAE-ATR", "n", "realPnL", "win%", "topCloseReason"))
	for b := range rows {
		r := rows[b]
		if r.n == 0 {
			sb.WriteString(fmt.Sprintf("  %-9s %-5d %-10s %-6s %-18s\n", r.label, 0, "-", "-", "-"))
			continue
		}
		sb.WriteString(fmt.Sprintf("  %-9s %-5d %-+10.2f %-6.1f %-18s\n",
			r.label, r.n, r.pnl, 100*float64(r.wins)/float64(r.n), r.topReason()))
	}

	// Not-green-at-entry-bar split (the filter-entrybar-green signal).
	var gN, ngN int
	var gP, ngP float64
	for _, it := range items {
		if it.firstBarGreen {
			gN++
			gP += it.pnl
		} else {
			ngN++
			ngP += it.pnl
		}
	}
	sb.WriteString("\n  -- entry-bar close direction --\n")
	sb.WriteString(fmt.Sprintf("  green-at-entry : n=%d realPnL=%+.2f\n", gN, gP))
	sb.WriteString(fmt.Sprintf("  NOT-green      : n=%d realPnL=%+.2f  <- candidate early-exit\n", ngN, ngP))

	// No-progress early-exit simulation: among entries whose MFE never reached k*ATR
	// (proxy: everGreen based on 0.1ATR; use MFE threshold grid), what realized PnL
	// would be removed. This is directional (realized PnL is the FULL-trade result, so
	// removing = "never opened"), showing the loss carried by no-progress trades.
	sb.WriteString("\n  -- no-progress trades (MFE never reached k*ATR): realized PnL carried --\n")
	sb.WriteString(fmt.Sprintf("  %-8s %-6s %-11s %-6s | %-8s %-11s\n", "k(ATR)", "nProg", "progPnL", "pW%", "nNoProg", "noProgPnL"))
	for _, k := range []float64{0.25, 0.5, 0.75, 1.0} {
		var pN, npN, pW int
		var pP, npP float64
		for _, it := range items {
			if it.mfe >= k {
				pN++
				pP += it.pnl
				if it.pnl >= 0 {
					pW++
				}
			} else {
				npN++
				npP += it.pnl
			}
		}
		pw := 0.0
		if pN > 0 {
			pw = 100 * float64(pW) / float64(pN)
		}
		sb.WriteString(fmt.Sprintf("  %-8.2f %-6d %-+11.2f %-6.1f | %-8d %-+11.2f\n",
			k, pN, pP, pw, npN, npP))
	}
	_ = sort.Float64s
	return sb.String()
}
