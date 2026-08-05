package gate

import "nofx/store"

// Gate close-attribution codec: thin wrappers over the shared store codec so there
// is a single mechanism<->code registry across exchanges (see store/reason_codec.go),
// mirroring trader/okx/reason_codec.go and trader/binance/reason_codec.go.
//
// The broker prefix is gateTag. Gate's `text` field has hard rules (see the SDK
// comment on FuturesOrder.Text):
//  1. must be prefixed with "t-"
//  2. no longer than 28 bytes WITHOUT the "t-" prefix
//  3. only 0-9, A-Z, a-z, underscore, hyphen or dot
//
// gateTag is "t-nofx", so an encoded id is "t-nofx" + code(2) + optional "T<tier>"
// + 12 hex chars = at most 22 bytes, i.e. 20 bytes after the "t-" prefix. That sits
// well inside the 28-byte budget and uses only hex + alphanumerics, so it satisfies
// all three rules. Keep any future segment additions inside that budget: Gate
// rejects the whole order when text is malformed, which would turn an encoding
// change into a protection-placement outage rather than a silent attribution loss.
const gateTag = "t-nofx"

// CodeForReason returns the stable 2-char mechanism code, or "" when unknown.
func CodeForReason(reason string) string {
	return store.CodeForReason(reason)
}

// encodeReasonClientID builds a Gate `text` value that carries the mechanism.
// Returns "" when the reason has no registered code, so callers fall back to the
// plain gateTag instead of sending an unrecognized code.
func encodeReasonClientID(reason string) string {
	return store.EncodeReasonClientID(gateTag, reason)
}

// decodeReasonFromClientID recovers the canonical reason from a text we placed.
// Returns "" when the text is not ours or carries no recognized code (never guesses).
func decodeReasonFromClientID(clientID string) string {
	return store.DecodeReasonFromClientID(gateTag, clientID)
}

// decodeTierFromClientID recovers the drawdown tier index (1..9) encoded at
// placement, or 0 when the text is not ours / carries no tier segment. The broker
// prefix is private to this package, which is why the decode lives here and the
// result travels on OpenOrder.ProtectionTier.
func decodeTierFromClientID(clientID string) int {
	return store.DecodeTierFromClientID(gateTag, clientID)
}
