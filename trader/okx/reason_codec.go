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

// decodeTierFromClientID recovers the drawdown tier index (1..9) we encoded at
// placement, or 0 when the id is not ours / carries no tier segment. The broker
// prefix is private to this package, which is why the decode lives here and the
// result travels on OpenOrder.ProtectionTier.
func decodeTierFromClientID(clientID string) int {
	return store.DecodeTierFromClientID(okxTag, clientID)
}

// reasonFromAlgoIDs resolves an order's mechanism from the two identity fields OKX
// gives back, in order of information content: the client-controlled algo id (32
// chars, carries a mechanism code we set at placement) first, then the tag.
//
// The tag path only ever matches legacy orders placed before the reason moved into
// the client id — okxTag consumes all 16 chars a tag can hold, so a current order's
// tag is the bare broker tag and carries no mechanism. Returns "" rather than
// guessing, which callers must read as "unknown", never as "not ours".
func reasonFromAlgoIDs(algoClOrdID string, tag string) string {
	if reason := decodeReasonFromClientID(algoClOrdID); reason != "" {
		return reason
	}
	return protectionReasonFromTag(tag)
}
