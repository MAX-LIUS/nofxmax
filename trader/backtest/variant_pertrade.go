package backtest

import (
	"fmt"
	"sort"
	"strings"
)

// variant_pertrade.go answers "does tightening BE (or any variant) cut winning
// trades early?" by comparing a variant against the baseline TRADE-BY-TRADE, not
// just on the aggregate. For each entry it replays both configs and classifies
// the outcome, so the net PnL delta is decomposed into winners-cut vs losers-saved.

// PerTradeDelta is one entry's baseline-vs-variant outcome.
type PerTradeDelta struct {
	Symbol      string
	BasePnL     float64
	VarPnL      float64
	Delta       float64
	BaseReasons string
	VarReasons  string
	Class       string // winner_cut / winner_grown / loser_saved / loser_worse / flat / flip_to_loss / flip_to_win
}

// ComparePerTrade replays baseline and variant over the same loaded entries and
// classifies each trade's change. Returns the per-trade rows plus category sums.
func ComparePerTrade(baseline, variant ProtectionParams, loaded []loadedEntry) ([]PerTradeDelta, map[string]catSum) {
	rows := make([]PerTradeDelta, 0, len(loaded))
	cats := map[string]catSum{}
	for _, le := range loaded {
		b := ReplayEntry(baseline, le.entry, le.bars, le.entryIdx)
		v := ReplayEntry(variant, le.entry, le.bars, le.entryIdx)
		d := v.RealizedPnL - b.RealizedPnL
		row := PerTradeDelta{
			Symbol:      le.entry.Symbol,
			BasePnL:     b.RealizedPnL,
			VarPnL:      v.RealizedPnL,
			Delta:       d,
			BaseReasons: strings.Join(b.CloseReasons, ","),
			VarReasons:  strings.Join(v.CloseReasons, ","),
		}
		row.Class = classifyDelta(b.RealizedPnL, v.RealizedPnL)
		rows = append(rows, row)
		c := cats[row.Class]
		c.n++
		c.baseSum += b.RealizedPnL
		c.varSum += v.RealizedPnL
		c.deltaSum += d
		cats[row.Class] = c
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Delta < rows[j].Delta })
	return rows, cats
}

type catSum struct {
	n                        int
	baseSum, varSum, deltaSum float64
}

// classifyDelta buckets a trade by how the variant changed its outcome. The key
// question — winners cut early — maps to winner_cut (was positive, variant
// reduced it but still >=0) and flip_to_loss (was positive, variant made it <0).
func classifyDelta(base, var_ float64) string {
	const eps = 1e-9
	switch {
	case base > eps && var_ >= -eps && var_ < base-eps:
		return "winner_cut"
	case base > eps && var_ < -eps:
		return "flip_to_loss"
	case base > eps && var_ > base+eps:
		return "winner_grown"
	case base < -eps && var_ > base+eps && var_ <= eps:
		return "loser_saved"
	case base < -eps && var_ > eps:
		return "flip_to_win"
	case base < -eps && var_ < base-eps:
		return "loser_worse"
	default:
		return "flat"
	}
}

// FormatPerTradeCompare renders the category summary + the largest movers so the
// winners-cut-early question is answered with concrete PnL, not just aggregates.
func FormatPerTradeCompare(name string, baseline, variant ProtectionParams, loaded []loadedEntry, topMovers int) string {
	rows, cats := ComparePerTrade(baseline, variant, loaded)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("==== PER-TRADE COMPARE: %s vs live-baseline ====\n", name))
	sb.WriteString(fmt.Sprintf("%-14s %-5s %-11s %-11s %-11s\n", "class", "N", "base_pnl", "var_pnl", "delta"))
	// Fixed category order for readability.
	order := []string{"winner_cut", "flip_to_loss", "winner_grown", "flip_to_win", "loser_saved", "loser_worse", "flat"}
	var totDelta float64
	for _, k := range order {
		c, ok := cats[k]
		if !ok {
			continue
		}
		sb.WriteString(fmt.Sprintf("%-14s %-5d %-11.2f %-11.2f %-+11.2f\n", k, c.n, c.baseSum, c.varSum, c.deltaSum))
		totDelta += c.deltaSum
	}
	sb.WriteString(fmt.Sprintf("%-14s %-5s %-11s %-11s %-+11.2f\n", "NET", "", "", "", totDelta))

	// The direct answer: PnL given up on winners vs PnL rescued on losers.
	var givenUp, rescued float64
	var cutN, savedN int
	for k, c := range cats {
		if k == "winner_cut" || k == "flip_to_loss" {
			givenUp += c.deltaSum // negative
			cutN += c.n
		}
		if k == "loser_saved" || k == "flip_to_win" {
			rescued += c.deltaSum // positive
			savedN += c.n
		}
	}
	sb.WriteString(fmt.Sprintf("\n→ winners cut/flipped: %d trades, PnL given up %.2f\n", cutN, givenUp))
	sb.WriteString(fmt.Sprintf("→ losers saved/flipped: %d trades, PnL rescued %.2f\n", savedN, rescued))
	sb.WriteString(fmt.Sprintf("→ net effect on winners+losers: %+.2f (positive = tightening helps net)\n", givenUp+rescued))

	if topMovers > 0 {
		sb.WriteString(fmt.Sprintf("\n-- %d biggest adverse movers (winners cut most) --\n", topMovers))
		shown := 0
		for _, r := range rows { // rows sorted ascending by delta (most negative first)
			if r.Delta >= -1e-9 {
				break
			}
			sb.WriteString(fmt.Sprintf("  %-12s base=%-8.2f var=%-8.2f d=%-8.2f [%s]->[%s]\n",
				r.Symbol, r.BasePnL, r.VarPnL, r.Delta, r.BaseReasons, r.VarReasons))
			shown++
			if shown >= topMovers {
				break
			}
		}
		if shown == 0 {
			sb.WriteString("  (none — no winner was cut by this variant)\n")
		}
	}
	return sb.String()
}
