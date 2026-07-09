package store

import "testing"

func TestReasonCodecRoundTrip(t *testing.T) {
	const prefix = "x-TESTpfx9"
	for reason := range mechanismToCode {
		id := EncodeReasonClientID(prefix, reason)
		if id == "" {
			t.Fatalf("encode empty for known reason %q", reason)
		}
		if len(id) > 32 {
			t.Fatalf("id %q exceeds 32 chars for reason %q", id, reason)
		}
		if got := DecodeReasonFromClientID(prefix, id); got != reason {
			t.Fatalf("round-trip mismatch reason=%q decoded=%q", reason, got)
		}
	}
}

func TestReasonCodecUniqueCodes(t *testing.T) {
	// codeToMechanism is built with a panic on duplicate; assert sizes match so a
	// silent overwrite (two reasons same code) cannot pass unnoticed.
	if len(codeToMechanism) != len(mechanismToCode) {
		t.Fatalf("code collision: %d reasons but %d unique codes", len(mechanismToCode), len(codeToMechanism))
	}
}

func TestReasonCodecDrawdownNormalization(t *testing.T) {
	const prefix = "x-TESTpfx9"
	id := EncodeReasonClientID(prefix, "managed_drawdown_partial_profit_lock")
	if got := DecodeReasonFromClientID(prefix, id); got != MechManagedDrawdown {
		t.Fatalf("expected %q, got %q", MechManagedDrawdown, got)
	}
}

func TestReasonCodecPrefixIsolation(t *testing.T) {
	// An id encoded under one broker prefix must not decode under another.
	id := EncodeReasonClientID("x-AAAAAAAA9", "break_even_stop")
	if got := DecodeReasonFromClientID("x-BBBBBBBB9", id); got != "" {
		t.Fatalf("cross-prefix decode must be empty, got %q", got)
	}
}

func TestReasonCodecCoversRealReasons(t *testing.T) {
	for _, reason := range []string{
		MechBreakEven, MechLadderSL, MechLadderTP, MechFullSL, MechFullTP,
		MechFallbackSL, MechStructuralSL, MechManagedDrawdown, MechTimeStop,
		MechMaxHold, MechTrailingTP, MechBreadthBreaker, MechTrendReversal,
		MechAIClose, MechEmergency,
	} {
		if CodeForReason(reason) == "" {
			t.Errorf("real mechanism %q has no code", reason)
		}
	}
}
