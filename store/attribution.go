package store

import "strings"

// Attribution is the canonical, single-source classification of how a position
// entry or exit was initiated. Every open and close flows through Classify* so
// that the taxonomy is defined in exactly one place and is fully testable.
//
// Category is the coarse origin; Mechanism is the specific trigger. Both are
// stable string enums safe to persist and query.
type Attribution struct {
	Category  string // ai | protection | manual | exchange | system
	Mechanism string // e.g. ladder_tp, full_sl, native_trailing, ai_close, manual_close, liquidation, breakout, ai_open
}

// Attribution categories.
const (
	CategoryAI         = "ai"         // model-initiated decision
	CategoryProtection = "protection" // code/exchange protection order (TP/SL/trailing/drawdown/break-even/time-stop)
	CategoryManual     = "manual"     // operator action
	CategoryExchange   = "exchange"   // exchange-forced (liquidation/ADL) or unattributed exchange-side close
	CategorySystem     = "system"     // internal bookkeeping (snapshots, emergency, unknown)
)

// Close mechanisms (stable).
const (
	MechLadderTP        = "ladder_tp"
	MechLadderSL        = "ladder_sl"
	MechFullTP          = "full_tp"
	MechFullSL          = "full_sl"
	MechFallbackSL      = "fallback_maxloss_sl"
	MechNativeTrailing  = "native_trailing"
	MechManagedDrawdown = "managed_drawdown"
	MechBreakEven       = "break_even_stop"
	MechTimeStop        = "time_stop"
	MechTrailingTP      = "trailing_take_profit"
	MechAIClose         = "ai_close"
	MechManualClose     = "manual_close"
	MechLiquidation     = "liquidation"
	MechEmergency       = "emergency_protection_close"
	MechSyncExternal    = "sync_external" // closed on exchange, mechanism unknown
	MechUnknownClose    = "unknown_close"
)

// Open mechanisms (stable).
const (
	MechAIOpen       = "ai_open"
	MechBreakout     = "breakout"
	MechManualOpen   = "manual_open"
	MechSyncExtOpen  = "sync_external_open"
	MechUnknownOpen  = "unknown_open"
)

// ClassifyClose maps a raw close reason/action string into a canonical
// Attribution. The input may be any reason produced anywhere in the system
// (AI decisions, protection executors, order-sync attribution, manual API).
// It is intentionally tolerant: prefix/substring matches keep it stable as
// stage-name suffixes (e.g. "managed_drawdown_runner_exit") evolve.
func ClassifyClose(rawReason string) Attribution {
	r := strings.ToLower(strings.TrimSpace(rawReason))
	switch {
	case r == "":
		return Attribution{CategorySystem, MechUnknownClose}

	// Manual first: explicit operator actions.
	case strings.HasPrefix(r, "manual_close") || r == "manual":
		return Attribution{CategoryManual, MechManualClose}

	// Exchange-forced.
	case strings.Contains(r, "liquidat") || r == "adl":
		return Attribution{CategoryExchange, MechLiquidation}

	// Protection mechanisms (code/exchange protection orders).
	case strings.Contains(r, "managed_drawdown"):
		return Attribution{CategoryProtection, MechManagedDrawdown}
	case strings.Contains(r, "native_trailing") || r == "trailing":
		return Attribution{CategoryProtection, MechNativeTrailing}
	case strings.Contains(r, "trailing_take_profit"):
		return Attribution{CategoryProtection, MechTrailingTP}
	case strings.Contains(r, "break_even"):
		return Attribution{CategoryProtection, MechBreakEven}
	case strings.Contains(r, "ladder_tp"):
		return Attribution{CategoryProtection, MechLadderTP}
	case strings.Contains(r, "ladder_sl"):
		return Attribution{CategoryProtection, MechLadderSL}
	case strings.Contains(r, "fallback_maxloss"):
		return Attribution{CategoryProtection, MechFallbackSL}
	case strings.Contains(r, "full_tp"):
		return Attribution{CategoryProtection, MechFullTP}
	case strings.Contains(r, "full_sl"):
		return Attribution{CategoryProtection, MechFullSL}
	case strings.Contains(r, "time_stop"):
		return Attribution{CategoryProtection, MechTimeStop}
	case strings.Contains(r, "emergency"):
		return Attribution{CategoryProtection, MechEmergency}

	// AI proactive close.
	case strings.HasPrefix(r, "ai_close"):
		return Attribution{CategoryAI, MechAIClose}

	// Closed on the exchange but mechanism could not be resolved.
	case strings.Contains(r, "sync_absent") || strings.Contains(r, "sync_external"):
		return Attribution{CategoryExchange, MechSyncExternal}

	// Bare close actions from the sync path with no protection attribution:
	// these are exchange-side fills whose specific mechanism is unknown.
	case r == "close_long" || r == "close_short":
		return Attribution{CategoryExchange, MechSyncExternal}
	}
	return Attribution{CategorySystem, MechUnknownClose}
}

// ClassifyOpen maps an open reason/source into a canonical Attribution.
func ClassifyOpen(rawReason string) Attribution {
	r := strings.ToLower(strings.TrimSpace(rawReason))
	switch {
	case r == "":
		return Attribution{CategorySystem, MechUnknownOpen}
	case strings.HasPrefix(r, "manual_open") || r == "manual":
		return Attribution{CategoryManual, MechManualOpen}
	case strings.Contains(r, "breakout"):
		return Attribution{CategoryAI, MechBreakout}
	case strings.HasPrefix(r, "ai_open") || r == "ai" || r == "system":
		return Attribution{CategoryAI, MechAIOpen}
	case strings.Contains(r, "sync") || strings.Contains(r, "snapshot") || strings.Contains(r, "external"):
		return Attribution{CategoryExchange, MechSyncExtOpen}
	}
	return Attribution{CategorySystem, MechUnknownOpen}
}
