package binance

import (
	"testing"

	"nofx/store"
)

// TestFuturesTraderImplementsTaggedTrailing locks the fix for the native-trailing
// mis-attribution bug: the drawdown/protection arm path type-asserts the trader to
// this interface and only encodes the mechanism (native_trailing) into the algo
// client id when it matches. Binance previously lacked these methods, so it fell
// through to the plain SetTrailingStopLoss (ClientAlgoId=getBrOrderID(), no code),
// and every trailing fill decoded to "" and was mislabeled ladder_tp / dumped to
// sync_external. If this assertion ever regresses, trailing attribution silently
// breaks again.
func TestFuturesTraderImplementsTaggedTrailing(t *testing.T) {
	var tr interface{} = &FuturesTrader{}
	if _, ok := tr.(interface {
		SetTrailingStopLossTaggedWithID(symbol string, positionSide string, activationPrice float64, callbackRate float64, quantity float64, reasonTag string) (string, error)
		CancelTrailingStopOrdersByIDs(symbol string, orderIDs []string) error
	}); !ok {
		t.Fatal("FuturesTrader must implement SetTrailingStopLossTaggedWithID + CancelTrailingStopOrdersByIDs so the drawdown arm path encodes native_trailing into the algo client id")
	}
}

// TestNativeTrailingClientIDRoundTrips guarantees the encoded trailing client id
// decodes back to native_trailing (the code the arm path passes), so a triggered
// trailing fill is attributed 1:1 via the coded client id at L2 instead of falling
// through to the L3 time-window guess.
func TestNativeTrailingClientIDRoundTrips(t *testing.T) {
	id := clientIDForReason(store.MechNativeTrailing)
	if id == "" {
		t.Fatal("clientIDForReason(native_trailing) returned empty")
	}
	if got := decodeReasonFromClientID(id); got != store.MechNativeTrailing {
		t.Fatalf("decode mismatch: id=%q decoded=%q want %q", id, got, store.MechNativeTrailing)
	}
}
