package okx

import "testing"

// realCloseReasons are the mechanism strings the system actually passes to
// close/protection placement. All must encode, or attribution for that mechanism
// silently falls back to a non-exact path.
var realCloseReasons = []string{
	"break_even_stop", "ladder_sl", "ladder_tp", "full_sl", "full_tp",
	"fallback_maxloss_sl", "structural_sl", "managed_drawdown",
	"managed_drawdown_partial_profit_lock", "time_stop", "max_hold",
	"trailing_take_profit", "breadth_breaker", "trend_reversal_flip",
	"ai_close", "emergency_protection_close",
}

func TestOKXReasonRoundTrip(t *testing.T) {
	for _, reason := range realCloseReasons {
		id := encodeReasonClientID(reason)
		if id == "" {
			t.Fatalf("encode returned empty for real reason %q", reason)
		}
		if len(id) > 32 {
			t.Fatalf("id %q exceeds 32 chars (%d) for reason %q", id, len(id), reason)
		}
		got := decodeReasonFromClientID(id)
		// managed_drawdown stage variants normalize to the base mechanism.
		want := reason
		if got != want && !(reason == "managed_drawdown_partial_profit_lock" && got == "managed_drawdown") {
			t.Fatalf("round-trip mismatch: reason=%q id=%q decoded=%q", reason, id, got)
		}
	}
}

func TestOKXReasonUnknownAndForeign(t *testing.T) {
	if id := encodeReasonClientID("no_such_mechanism"); id != "" {
		t.Fatalf("expected empty id for unknown reason, got %q", id)
	}
	if got := decodeReasonFromClientID("someForeignExchangeId123"); got != "" {
		t.Fatalf("expected empty decode for foreign id, got %q", got)
	}
	if got := decodeReasonFromClientID(okxTag + "ZZdeadbeef"); got != "" {
		t.Fatalf("expected empty decode for unknown code, got %q", got)
	}
}

func TestOKXReasonPrefixIsBrokerTag(t *testing.T) {
	id := encodeReasonClientID("break_even_stop")
	if len(id) < len(okxTag) || id[:len(okxTag)] != okxTag {
		t.Fatalf("encoded id %q must start with broker tag %q", id, okxTag)
	}
}
