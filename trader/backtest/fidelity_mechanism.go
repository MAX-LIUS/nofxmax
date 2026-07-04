package backtest

import (
	"fmt"
	"sort"
	"strings"
)

// MechBucket holds the replay-vs-actual breakdown for one live close_reason,
// tagged with the attribution-confidence tier of that reason.
type MechBucket struct {
	Reason     string  // live close_reason (full_sl, ai_close, managed_drawdown, ...)
	Confidence string  // AttrTrustedProtection | AttrDataLossUncertain | AttrSyncGap
	N          int     // number of trades closed under this reason
	ActualPnL  float64 // sum of live realized PnL for these trades
	BtPnL      float64 // sum of backtest-replayed PnL for the same trades
	AbsErr     float64 // |BtPnL - ActualPnL|
}

// MechanismFidelity replays the baseline over the loaded entries and buckets the
// per-trade result by live close_reason, tagged with attribution confidence.
//
// Because AI/discretionary closing is disabled, every REAL close is a passive
// protection action; early "ai_close/manual/close_*/market_close" labels are
// LOST attribution, not genuine mechanisms. So fidelity (replay≈actual) is only
// meaningful on the trusted-protection subset — that is where the recorded
// reason is a real mechanism the replay is expected to reproduce. Data-loss and
// sync-gap trades still carry valid realized PnL (used for total evaluation) but
// their reason is untrustworthy, so they are excluded from the fidelity metric.
func MechanismFidelity(loaded []loadedEntry, baseline ProtectionParams) (buckets []MechBucket, trustedActual, trustedBt, dataLossActual float64) {
	agg := map[string]*MechBucket{}
	for _, le := range loaded {
		r := le.entry.CloseReason
		if r == "" {
			r = "(none)"
		}
		b := agg[r]
		if b == nil {
			b = &MechBucket{Reason: r, Confidence: AttributionConfidence(le.entry.CloseReason)}
			agg[r] = b
		}
		res := ReplayEntry(baseline, le.entry, le.bars, le.entryIdx)
		b.N++
		b.ActualPnL += le.entry.RealizedPnL
		b.BtPnL += res.RealizedPnL
		switch b.Confidence {
		case AttrTrustedProtection:
			trustedActual += le.entry.RealizedPnL
			trustedBt += res.RealizedPnL
		default:
			dataLossActual += le.entry.RealizedPnL
		}
	}
	buckets = make([]MechBucket, 0, len(agg))
	for _, b := range agg {
		b.AbsErr = absf(b.BtPnL - b.ActualPnL)
		buckets = append(buckets, *b)
	}
	// Sort: trusted first, then by |error| within tier so the biggest
	// trusted-subset fidelity gaps (the ones that matter) surface at the top.
	sort.Slice(buckets, func(i, j int) bool {
		if buckets[i].Confidence != buckets[j].Confidence {
			return confRank(buckets[i].Confidence) < confRank(buckets[j].Confidence)
		}
		return buckets[i].AbsErr > buckets[j].AbsErr
	})
	return buckets, trustedActual, trustedBt, dataLossActual
}

// confRank orders confidence tiers for display (trusted first).
func confRank(c string) int {
	switch c {
	case AttrTrustedProtection:
		return 0
	case AttrDataLossUncertain:
		return 1
	default:
		return 2
	}
}

// confShort renders a tier as a short table tag.
func confShort(c string) string {
	switch c {
	case AttrTrustedProtection:
		return "TRUST"
	case AttrDataLossUncertain:
		return "LOSS"
	case AttrSyncGap:
		return "SYNC"
	default:
		return "?"
	}
}

// FormatMechanismFidelity renders the per-reason breakdown, then reports the
// fidelity metric on the trusted subset only.
func FormatMechanismFidelity(loaded []loadedEntry, baseline ProtectionParams) string {
	buckets, trustedActual, trustedBt, dataLossActual := MechanismFidelity(loaded, baseline)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%-24s %-6s %-5s %-11s %-11s %-10s\n",
		"close_reason", "conf", "N", "actual_pnl", "bt_pnl", "abs_err"))
	var totAct, totBt float64
	var trustedN, lossN int
	for _, b := range buckets {
		sb.WriteString(fmt.Sprintf("%-24s %-6s %-5d %-11.2f %-11.2f %-10.2f\n",
			trunc(b.Reason, 24), confShort(b.Confidence), b.N, b.ActualPnL, b.BtPnL, b.AbsErr))
		totAct += b.ActualPnL
		totBt += b.BtPnL
		if b.Confidence == AttrTrustedProtection {
			trustedN += b.N
		} else {
			lossN += b.N
		}
	}
	sb.WriteString(fmt.Sprintf("%-24s %-6s %-5s %-11.2f %-11.2f\n",
		"TOTAL", "", "", totAct, totBt))

	// Fidelity is computed on the trusted subset only.
	trustedRelErr := 0.0
	if trustedActual != 0 {
		trustedRelErr = (trustedBt - trustedActual) / absf(trustedActual) * 100
	}
	sb.WriteString(fmt.Sprintf(
		"\n[trusted-subset fidelity]  N=%d  actual=%.2f  replay=%.2f  rel_err=%.1f%%\n",
		trustedN, trustedActual, trustedBt, trustedRelErr))
	sb.WriteString(fmt.Sprintf(
		"[data-loss/sync]           N=%d  actual=%.2f  (valid PnL, reason UNTRUSTED — excluded from fidelity)\n",
		lossN, dataLossActual))
	sb.WriteString("→ AI/manual/close_*/market_close are disabled-feature mislabels = lost attribution,\n")
	sb.WriteString("  not real mechanisms. Their entries still drive protection-param optimization;\n")
	sb.WriteString("  only the reason label is untrusted. Fidelity is judged on TRUST rows only.\n")
	return sb.String()
}

// trunc shortens s to n runes for table alignment.
func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
