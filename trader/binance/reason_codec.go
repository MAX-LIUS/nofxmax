package binance

import "nofx/store"

// Binance close-attribution codec: thin wrappers over the shared store codec so
// the mechanism<->code registry is identical across exchanges (see
// store/reason_codec.go). The broker prefix is brOrderIDPrefix ("x-KzrpZaP9"), so
// an encoded client id is brOrderIDPrefix + code(2) + nonce, <=32 alphanumeric.

// clientIDForReason returns a broker-prefixed client id that encodes the close
// mechanism when the reason has a registered code, else a plain broker id. Used
// for both regular orders (NewClientOrderID) and algo orders (ClientAlgoId).
func clientIDForReason(reason string) string {
	if coded := store.EncodeReasonClientID(brOrderIDPrefix, reason); coded != "" {
		return coded
	}
	return getBrOrderID()
}

// decodeReasonFromClientID recovers the canonical mechanism from a client id we
// placed. Returns "" when the id is not ours or has no recognized code.
func decodeReasonFromClientID(clientID string) string {
	return store.DecodeReasonFromClientID(brOrderIDPrefix, clientID)
}

// tierFromClientID recovers the drawdown tier index (1..9) encoded at placement, or
// 0 when the id is not ours / carries no tier segment. The broker prefix is private
// to this package, so the decode lives here and the result travels on
// OpenOrder.ProtectionTier.
func tierFromClientID(clientID string) int {
	return store.DecodeTierFromClientID(brOrderIDPrefix, clientID)
}
