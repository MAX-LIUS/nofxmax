package backtest

import "strings"

// attribution_confidence.go classifies a live close_reason by how much we trust
// it as a REAL protection mechanism, given the historical attribution reality:
// AI/discretionary closing is disabled system-wide, so every real close is a
// PASSIVE protection action. Early trades were mislabeled (ai_close / manual_*
// / close_long|short / market_close) because the deterministic OrderID→origType
// attribution did not exist yet and origType was never persisted — those labels
// are LOST attribution, not genuine mechanisms.
//
// Confidence tiers:
//   TrustedProtection — a recovered/native protection mechanism; usable as a
//                       fidelity ground-truth (replay should approximate it).
//   DataLossUncertain — mislabeled discretionary/market close; the label is not
//                       a real mechanism. Realized PnL is still valid, but the
//                       reason cannot be trusted for fidelity.
//   SyncGap           — position vanished from the exchange side; attribution
//                       unknown by construction.
const (
	AttrTrustedProtection = "trusted_protection"
	AttrDataLossUncertain = "data_loss_uncertain"
	AttrSyncGap           = "sync_gap"
)

// AttributionConfidence maps a close_reason to its confidence tier.
func AttributionConfidence(reason string) string {
	r := strings.ToLower(strings.TrimSpace(reason))
	switch {
	case r == "" :
		return AttrDataLossUncertain
	case strings.HasPrefix(r, "sync"):
		return AttrSyncGap
	case isTrustedProtectionReason(r):
		return AttrTrustedProtection
	case r == "ai_close" ||
		strings.HasPrefix(r, "manual_close") ||
		r == "close_long" || r == "close_short" ||
		r == "market_close" ||
		r == "legacy_unknown" || r == "unknown_close":
		return AttrDataLossUncertain
	default:
		// Unknown/new label: treat conservatively as uncertain rather than
		// silently trusting it.
		return AttrDataLossUncertain
	}
}

// isTrustedProtectionReason reports whether the reason is a genuine, recovered
// protection mechanism (post-fix deterministic attribution or native-side).
func isTrustedProtectionReason(r string) bool {
	switch r {
	case "full_sl", "ladder_sl", "fallback_maxloss_sl",
		"full_tp", "ladder_tp",
		"break_even_stop",
		"managed_drawdown", "native_trailing", "trailing_take_profit",
		"giveback_guard_breadth", "breadth_breaker",
		"max_hold", "time_stop",
		"trend_reversal_flip",
		"liquidation", "emergency_protection_close":
		return true
	default:
		return false
	}
}
