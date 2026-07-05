package trader

import (
	"math"
	"testing"

	"nofx/store"
)

func TestClampStructuralBoundary(t *testing.T) {
	ss := store.StructuralSLConfig{Enabled: true, FloorATRMul: 1.5, BackstopATRMul: 4.5, LookbackBars: 24}
	atr := 100.0
	entry := 10000.0

	// LONG, raw swing 6.2 ATR away (beyond backstop 4.5) -> clamp to 4.5 ATR below entry.
	if got, ok := clampStructuralBoundary(entry, entry-620, atr, true, ss); !ok || math.Abs(got-(entry-450)) > 1e-6 {
		t.Fatalf("long beyond-backstop: got %.2f ok=%v want %.2f", got, ok, entry-450)
	}
	// LONG, raw swing 2.0 ATR (within band) -> unchanged.
	if got, ok := clampStructuralBoundary(entry, entry-200, atr, true, ss); !ok || math.Abs(got-(entry-200)) > 1e-6 {
		t.Fatalf("long in-band: got %.2f ok=%v want %.2f", got, ok, entry-200)
	}
	// LONG, raw swing 0.5 ATR (below floor 1.5) -> clamp out to 1.5 ATR.
	if got, ok := clampStructuralBoundary(entry, entry-50, atr, true, ss); !ok || math.Abs(got-(entry-150)) > 1e-6 {
		t.Fatalf("long below-floor: got %.2f ok=%v want %.2f", got, ok, entry-150)
	}
	// SHORT, raw swing 6.2 ATR above -> clamp to 4.5 ATR above entry.
	if got, ok := clampStructuralBoundary(entry, entry+620, atr, false, ss); !ok || math.Abs(got-(entry+450)) > 1e-6 {
		t.Fatalf("short beyond-backstop: got %.2f ok=%v want %.2f", got, ok, entry+450)
	}
	// invalid inputs -> (raw,false)
	if got, ok := clampStructuralBoundary(0, 100, atr, true, ss); ok || got != 100 {
		t.Fatalf("invalid entry: got %.2f ok=%v", got, ok)
	}
}
