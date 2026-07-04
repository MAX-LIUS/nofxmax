package backtest

import (
	"fmt"
	"sort"
	"strings"
)

// MechBucket holds the fidelity breakdown for one live close mechanism.
type MechBucket struct {
	Reason     string  // live close_reason (full_sl, ai_close, managed_drawdown, ...)
	N          int     // number of trades that closed via this mechanism live
	ActualPnL  float64 // sum of live realized PnL for these trades
	BtPnL      float64 // sum of backtest-replayed PnL for the same trades
	AbsErr     float64 // |BtPnL - ActualPnL|
	Modeled    bool    // whether the replay engine can represent this mechanism
}

// modeledReasons maps live close_reason → whether ReplayEntry can reproduce it.
// The replay only knows SL/TP/BE/DD; every AI/time/breadth/manual close is
// currently replayed as "hold to ExitTime then mark-to-market", which is the
// dominant source of fidelity error. This table makes that explicit.
func reasonModeled(reason string) bool {
	r := strings.ToLower(reason)
	switch {
	case strings.Contains(r, "full_sl"),
		strings.Contains(r, "ladder_sl"),
		strings.Contains(r, "fallback"),
		strings.Contains(r, "full_tp"),
		strings.Contains(r, "ladder_tp"),
		strings.Contains(r, "break_even"),
		strings.Contains(r, "managed_drawdown"),
		strings.Contains(r, "native_trailing"),
		strings.Contains(r, "trailing_take_profit"):
		return true
	default:
		// ai_close, time_stop, max_hold, giveback_guard_breadth, trend_reversal,
		// manual_close, sync_*, close_long/short, market_close, liquidation …
		return false
	}
}

// MechanismFidelity replays the baseline over the loaded entries and buckets the
// per-trade error by the live close mechanism. Returns buckets sorted by |error|
// descending so the biggest fidelity gaps surface first, plus modeled vs
// unmodeled PnL totals.
func MechanismFidelity(loaded []loadedEntry, baseline ProtectionParams) (buckets []MechBucket, modeledActual, unmodeledActual float64) {
	agg := map[string]*MechBucket{}
	for _, le := range loaded {
		r := le.entry.CloseReason
		if r == "" {
			r = "(none)"
		}
		b := agg[r]
		if b == nil {
			b = &MechBucket{Reason: r, Modeled: reasonModeled(r)}
			agg[r] = b
		}
		res := ReplayEntry(baseline, le.entry, le.bars, le.entryIdx)
		b.N++
		b.ActualPnL += le.entry.RealizedPnL
		b.BtPnL += res.RealizedPnL
		if b.Modeled {
			modeledActual += le.entry.RealizedPnL
		} else {
			unmodeledActual += le.entry.RealizedPnL
		}
	}
	buckets = make([]MechBucket, 0, len(agg))
	for _, b := range agg {
		b.AbsErr = absf(b.BtPnL - b.ActualPnL)
		buckets = append(buckets, *b)
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].AbsErr > buckets[j].AbsErr })
	return buckets, modeledActual, unmodeledActual
}

// FormatMechanismFidelity renders the per-mechanism breakdown as a table.
func FormatMechanismFidelity(loaded []loadedEntry, baseline ProtectionParams) string {
	buckets, modeledActual, unmodeledActual := MechanismFidelity(loaded, baseline)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%-26s %-4s %-6s %-11s %-11s %-10s\n",
		"close_reason", "mdl", "N", "actual_pnl", "bt_pnl", "abs_err"))
	var totAct, totBt, totErr float64
	for _, b := range buckets {
		mdl := "no"
		if b.Modeled {
			mdl = "YES"
		}
		sb.WriteString(fmt.Sprintf("%-26s %-4s %-6d %-11.2f %-11.2f %-10.2f\n",
			trunc(b.Reason, 26), mdl, b.N, b.ActualPnL, b.BtPnL, b.AbsErr))
		totAct += b.ActualPnL
		totBt += b.BtPnL
		totErr += b.AbsErr
	}
	sb.WriteString(fmt.Sprintf("%-26s %-4s %-6s %-11.2f %-11.2f %-10.2f\n",
		"TOTAL", "", "", totAct, totBt, totErr))
	pctUnmodeled := 0.0
	if totAct != 0 {
		pctUnmodeled = unmodeledActual / totAct * 100
	}
	sb.WriteString(fmt.Sprintf(
		"\nmodeled_actual=%.2f  unmodeled_actual=%.2f  (unmodeled = %.0f%% of actual PnL)\n",
		modeledActual, unmodeledActual, pctUnmodeled))
	sb.WriteString("→ 'no' rows are live closes the replay cannot reproduce yet (AI/time/breadth/manual);\n")
	sb.WriteString("  their abs_err is the fidelity gap an AI-close proxy must close.\n")
	return sb.String()
}

// trunc shortens s to n runes for table alignment.
func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
