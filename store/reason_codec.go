package store

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// Deterministic close-attribution codec (exchange-agnostic).
//
// Problem: exchange broker tags are short and a triggered protection algo spawns
// a fill whose order id differs from the placed order id, so a close fill could
// historically only be attributed by price proximity — a guess that produced
// false labels (e.g. a loss-side market close tagged break_even_stop when BE was
// never armed).
//
// Fix: every protection/close order the bot places carries a client-controlled id
// of the form
//
//	<brokerPrefix><code(2)><nonce>
//
// where <code> is a stable 2-char mechanism code bound to the canonical Mech*
// constants below. The exchange echoes this id back on the fill (directly or via
// an order-detail lookup), so the fill decodes its own mechanism with zero
// guessing and zero race. The id stays <=32 alphanumeric chars and preserves the
// broker prefix for broker attribution. Both the OKX and Binance adapters call
// these helpers with their own broker prefix, so there is a single registry.

// mechanismToCode binds each canonical close mechanism to a stable, unique 2-char
// code. Codes are permanent: never reuse a code for a different meaning.
var mechanismToCode = map[string]string{
	MechBreakEven:       "BE",
	MechLadderSL:        "LS",
	MechLadderTP:        "LT",
	MechFullSL:          "FS",
	MechFullTP:          "FT",
	MechFallbackSL:      "FM",
	MechNativeTrailing:  "NT",
	MechTrailingTP:      "TT",
	MechManagedDrawdown: "DD",
	MechStructuralSL:    "ST",
	MechBreadthBreaker:  "CB", // portfolio-level circuit breaker (熔断)
	MechTrendReversal:   "TR",
	MechAIClose:         "AC",
	MechManualClose:     "MC",
	MechTimeStop:        "TS",
	MechMaxHold:         "MH",
	MechEmergency:       "EM",
	MechLiquidation:     "LQ",
}

// codeToMechanism is the reverse registry; a duplicate code panics at startup so
// an ambiguous mapping can never silently reintroduce mis-attribution.
var codeToMechanism = func() map[string]string {
	m := make(map[string]string, len(mechanismToCode))
	for reason, code := range mechanismToCode {
		if existing, dup := m[code]; dup {
			panic("store reason_codec: duplicate mechanism code " + code + " for " + reason + " and " + existing)
		}
		m[code] = reason
	}
	return m
}()

// normalizeReason folds dynamic variants onto their canonical base mechanism.
// Managed-drawdown tiers arrive as "managed_drawdown_<stage>"; all map to
// managed_drawdown for attribution.
func normalizeReason(reason string) string {
	reason = strings.TrimSpace(strings.ToLower(reason))
	if strings.HasPrefix(reason, MechManagedDrawdown) {
		return MechManagedDrawdown
	}
	return reason
}

// CodeForReason returns the stable 2-char code for a canonical reason, or "" when
// the reason has no registered code (caller falls back to the plain tag path).
func CodeForReason(reason string) string {
	return mechanismToCode[normalizeReason(reason)]
}

// NormalizeMechanism is the exported form of normalizeReason, for adapters that must
// compare a caller-supplied reason against a decoded mechanism on equal footing
// (e.g. OKX targeted algo cleanup). Kept here so the dynamic-variant folding rule
// lives in exactly one place.
func NormalizeMechanism(reason string) string {
	return normalizeReason(reason)
}

// EncodeReasonClientID builds a client order id carrying the mechanism code:
// brokerPrefix + code(2) + nonce, clamped to 32 chars. Returns "" when the reason
// has no registered code so callers can skip encoding.
func EncodeReasonClientID(brokerPrefix, reason string) string {
	code := CodeForReason(reason)
	if code == "" {
		return ""
	}
	nonceBytes := make([]byte, 6)
	_, _ = rand.Read(nonceBytes)
	id := brokerPrefix + code + hex.EncodeToString(nonceBytes)
	if len(id) > 32 {
		id = id[:32]
	}
	return id
}

// DecodeReasonFromClientID recovers the canonical reason from a client order id
// produced by EncodeReasonClientID with the same brokerPrefix. Returns "" when the
// id is not one of ours or carries no recognized code (never guesses).
func DecodeReasonFromClientID(brokerPrefix, clientID string) string {
	clientID = strings.TrimSpace(clientID)
	if brokerPrefix == "" || len(clientID) < len(brokerPrefix)+2 {
		return ""
	}
	if !strings.HasPrefix(clientID, brokerPrefix) {
		return ""
	}
	code := clientID[len(brokerPrefix) : len(brokerPrefix)+2]
	return codeToMechanism[code]
}
