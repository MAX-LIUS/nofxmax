package store

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
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

// tierSuffixSep separates a caller-supplied tier marker from the mechanism in a
// reason string: "native_trailing#2" = mechanism native_trailing, tier 2.
//
// Why the tier travels in the reason string instead of a new parameter: the reason
// already flows through every adapter's Tagged* placement signature (10 exchanges),
// and normalizeReason is the single choke point every mechanism comparison passes
// through. Folding the suffix off here means all existing comparisons keep working
// untouched while the encoder can still read the tier — no signature churn, no
// per-adapter patching.
const tierSuffixSep = "#"

// normalizeReason folds dynamic variants onto their canonical base mechanism.
// Managed-drawdown tiers arrive as "managed_drawdown_<stage>"; all map to
// managed_drawdown for attribution. A "#<tier>" suffix is stripped here too, so a
// tier-tagged reason compares equal to its bare mechanism everywhere.
func normalizeReason(reason string) string {
	reason = strings.TrimSpace(strings.ToLower(reason))
	if idx := strings.Index(reason, tierSuffixSep); idx >= 0 {
		reason = reason[:idx]
	}
	if strings.HasPrefix(reason, MechManagedDrawdown) {
		return MechManagedDrawdown
	}
	return reason
}

// ReasonWithTier builds the reason string a placement site passes down when it wants
// the tier recorded on the exchange order itself. tier must be 1..9 (a single digit
// keeps the encoded id inside the 32-char budget on every venue); out-of-range tiers
// return the bare reason so the caller degrades to mechanism-only attribution rather
// than emitting an unparseable id.
func ReasonWithTier(reason string, tier int) string {
	if tier < 1 || tier > 9 {
		return reason
	}
	return reason + tierSuffixSep + strconv.Itoa(tier)
}

// tierFromReason extracts the 1..9 tier marker from a reason string, or 0 when the
// reason carries none.
func tierFromReason(reason string) int {
	idx := strings.Index(reason, tierSuffixSep)
	if idx < 0 || idx+2 != len(reason) {
		return 0
	}
	tier, err := strconv.Atoi(reason[idx+1:])
	if err != nil || tier < 1 || tier > 9 {
		return 0
	}
	return tier
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
//
//	brokerPrefix + code(2) [+ "T" + tierDigit] + nonce      (clamped to 32 chars)
//
// The optional tier segment appears only when the reason carries a "#<1..9>" suffix
// (see ReasonWithTier). It is the SECOND line of defence for multi-tier drawdown:
// the primary per-tier identity is the orderID we persist in the protection record,
// but a record can be lost (write failure, restart before persist, venue that does
// not echo an id). With the tier on the order itself, the exchange side alone still
// says which tier an order belongs to, so a rebuild never has to guess by
// qty/activation/callback — the heuristic that collides once tiers rest together.
//
// "T" is the separator because the nonce is hex: T can never appear in it, so the
// segment is unambiguous to decode and old ids (no T at that offset) still parse.
// Returns "" when the reason has no registered code so callers can skip encoding.
func EncodeReasonClientID(brokerPrefix, reason string) string {
	code := CodeForReason(reason)
	if code == "" {
		return ""
	}
	tierSeg := ""
	if tier := tierFromReason(reason); tier > 0 {
		tierSeg = "T" + strconv.Itoa(tier)
	}
	nonceBytes := make([]byte, 6)
	_, _ = rand.Read(nonceBytes)
	id := brokerPrefix + code + tierSeg + hex.EncodeToString(nonceBytes)
	if len(id) > 32 {
		id = id[:32]
	}
	return id
}

// DecodeTierFromClientID recovers the 1..9 tier marker from a client order id we
// placed, or 0 when the id is not ours / carries no tier segment (legacy ids and
// non-tiered mechanisms). Never guesses: absence reads as "unknown tier", which
// callers must fall back on the stored orderID or fuzzy matching for.
func DecodeTierFromClientID(brokerPrefix, clientID string) int {
	clientID = strings.TrimSpace(clientID)
	if brokerPrefix == "" || !strings.HasPrefix(clientID, brokerPrefix) {
		return 0
	}
	// Need brokerPrefix + code(2) + "T" + digit.
	tierOffset := len(brokerPrefix) + 2
	if len(clientID) < tierOffset+2 || clientID[tierOffset] != 'T' {
		return 0
	}
	tier, err := strconv.Atoi(string(clientID[tierOffset+1]))
	if err != nil || tier < 1 || tier > 9 {
		return 0
	}
	return tier
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
