package okx

import "nofx/store"

// OKX close-attribution codec: thin wrappers over the shared store codec so there
// is a single mechanism<->code registry across exchanges (see store/reason_codec.go).
// The broker prefix is okxTag (the 16-char broker/affiliate tag), so an encoded id
// is okxTag + code(2) + nonce, <=32 alphanumeric chars.

// CodeForReason returns the stable 2-char mechanism code, or "" when unknown.
func CodeForReason(reason string) string {
	return store.CodeForReason(reason)
}

// encodeReasonClientID builds an algoClOrdId/clOrdId that carries the mechanism.
// Returns "" when the reason has no registered code.
func encodeReasonClientID(reason string) string {
	return store.EncodeReasonClientID(okxTag, reason)
}

// decodeReasonFromClientID recovers the canonical reason from an id we placed.
// Returns "" when the id is not ours or carries no recognized code (never guesses).
func decodeReasonFromClientID(clientID string) string {
	return store.DecodeReasonFromClientID(okxTag, clientID)
}
