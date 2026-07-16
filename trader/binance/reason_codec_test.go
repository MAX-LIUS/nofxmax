package binance

import (
	"strings"
	"testing"
)

func TestBinanceReasonRoundTrip(t *testing.T) {
	for _, reason := range []string{
		"break_even_stop", "ladder_sl", "ladder_tp", "full_sl", "full_tp",
		"fallback_maxloss_sl", "structural_sl", "managed_drawdown",
		"managed_drawdown_partial_profit_lock", "time_stop", "max_hold",
		"trailing_take_profit", "breadth_breaker", "trend_reversal_flip",
		"ai_close", "emergency_protection_close",
	} {
		id := clientIDForReason(reason)
		if !strings.HasPrefix(id, brOrderIDPrefix) {
			t.Fatalf("id %q must carry broker prefix", id)
		}
		if len(id) > 32 {
			t.Fatalf("id %q exceeds 32 chars for reason %q", id, reason)
		}
		got := decodeReasonFromClientID(id)
		if got == "" {
			t.Fatalf("coded reason %q did not decode (got empty) id=%q", reason, id)
		}
	}
}

func TestBinanceUnknownReasonFallsBackToPlainBrokerID(t *testing.T) {
	// Unknown reason: still a valid broker id, but decodes to "" (no false label).
	id := clientIDForReason("no_such_mechanism")
	if !strings.HasPrefix(id, brOrderIDPrefix) {
		t.Fatalf("fallback id %q must still carry broker prefix", id)
	}
	if got := decodeReasonFromClientID(id); got != "" {
		t.Fatalf("unknown-reason id must not decode to a mechanism, got %q", got)
	}
}

func TestBinanceForeignIDNotAttributed(t *testing.T) {
	if got := decodeReasonFromClientID("someExchangeGeneratedId"); got != "" {
		t.Fatalf("foreign id must decode empty, got %q", got)
	}
}
