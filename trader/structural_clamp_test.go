package trader

import (
	"math"
	"testing"

	"nofx/store"
)

func TestClampStructuralBoundary(t *testing.T) {
	// FallbackATRMul defaults to 3.0 via WithDefaults; beyond-backstop now falls back
	// to the tighter fallback (3.0 ATR), not the wide backstop (4.5 ATR).
	ss := store.StructuralSLConfig{Enabled: true, FloorATRMul: 1.5, BackstopATRMul: 4.5, LookbackBars: 24}
	atr := 100.0
	entry := 10000.0

	// LONG, raw swing 6.2 ATR away (beyond backstop 4.5) -> fall back to 3.0 ATR below entry.
	if got, ok := clampStructuralBoundary(entry, entry-620, atr, 0, true, ss); !ok || math.Abs(got-(entry-300)) > 1e-6 {
		t.Fatalf("long beyond-backstop: got %.2f ok=%v want %.2f", got, ok, entry-300)
	}
	// LONG, raw swing 2.0 ATR (within band) -> unchanged.
	if got, ok := clampStructuralBoundary(entry, entry-200, atr, 0, true, ss); !ok || math.Abs(got-(entry-200)) > 1e-6 {
		t.Fatalf("long in-band: got %.2f ok=%v want %.2f", got, ok, entry-200)
	}
	// LONG, raw swing 0.5 ATR (below floor 1.5) -> clamp out to 1.5 ATR.
	if got, ok := clampStructuralBoundary(entry, entry-50, atr, 0, true, ss); !ok || math.Abs(got-(entry-150)) > 1e-6 {
		t.Fatalf("long below-floor: got %.2f ok=%v want %.2f", got, ok, entry-150)
	}
	// SHORT, raw swing 6.2 ATR above -> fall back to 3.0 ATR above entry.
	if got, ok := clampStructuralBoundary(entry, entry+620, atr, 0, false, ss); !ok || math.Abs(got-(entry+300)) > 1e-6 {
		t.Fatalf("short beyond-backstop: got %.2f ok=%v want %.2f", got, ok, entry+300)
	}
	// invalid inputs -> (raw,false)
	if got, ok := clampStructuralBoundary(0, 100, atr, 0, true, ss); ok || got != 100 {
		t.Fatalf("invalid entry: got %.2f ok=%v", got, ok)
	}
}

// TestStructuralFallbackTighterThanBackstop pins the 2026-07 fix: when the nearest
// structure is beyond the backstop, the stop uses FallbackATRMul (tighter), and an
// explicit fallback smaller than the default is honored.
func TestStructuralFallbackTighterThanBackstop(t *testing.T) {
	atr := 100.0
	entry := 10000.0
	// Explicit tight fallback of 2.5 ATR.
	ss := store.StructuralSLConfig{Enabled: true, FloorATRMul: 1.5, BackstopATRMul: 4.5, FallbackATRMul: 2.5}
	// SHORT, raw swing 6.29 ATR above (the LIT case) -> 2.5 ATR above entry, not 4.5.
	if got, ok := clampStructuralBoundary(entry, entry+629, atr, 0, false, ss); !ok || math.Abs(got-(entry+250)) > 1e-6 {
		t.Fatalf("short beyond-backstop fallback: got %.2f ok=%v want %.2f", got, ok, entry+250)
	}
	// structuralSLPercent must agree: distance = 2.5 ATR = 250, entry 10000 -> 2.5%.
	acfg := store.ATRProtectionConfig{Enabled: true, MinEffPct: 0.1, MaxEffPct: 50}
	if pct, ok := structuralSLPercent(entry, entry+629, atr, 0, ss, acfg); !ok || math.Abs(pct-2.5) > 1e-6 {
		t.Fatalf("structuralSLPercent fallback: got %.4f ok=%v want 2.5", pct, ok)
	}
}

// TestStructuralFallbackRRCap pins the RR cap: on the no-near-structure path the
// fallback stop must stay below FallbackRRCapRatio × TP target, and FloorATRMul stays
// a hard minimum below the cap.
func TestStructuralFallbackRRCap(t *testing.T) {
	atr := 100.0        // 1 ATR = 1% of entry
	entry := 10000.0
	acfg := store.ATRProtectionConfig{Enabled: true, MinEffPct: 0.1, MaxEffPct: 50}

	// FallbackATRMul 3.0 (= 3%), but TP target 3% with ratio 0.8 => cap = 0.8*3 = 2.4%.
	// Cap (2.4) < fallback (3.0) and > floor (1.5) => stop uses 2.4 ATR.
	ss := store.StructuralSLConfig{Enabled: true, FloorATRMul: 1.5, BackstopATRMul: 4.5, FallbackATRMul: 3.0, FallbackRRCapRatio: 0.8}
	if pct, ok := structuralSLPercent(entry, 80.0, atr, 3.0, ss, acfg); !ok || math.Abs(pct-2.4) > 1e-9 {
		t.Fatalf("RR-capped fallback want 2.4%%, got %.4f ok=%v", pct, ok)
	}
	// clamp mirrors it: SHORT beyond backstop -> entry + 2.4 ATR = entry+240.
	if got, ok := clampStructuralBoundary(entry, entry+620, atr, 3.0, false, ss); !ok || math.Abs(got-(entry+240)) > 1e-6 {
		t.Fatalf("RR-capped clamp: got %.2f ok=%v want %.2f", got, ok, entry+240)
	}

	// TP target 1.0% with ratio 0.8 => cap 0.8% < floor 1.5% => floor wins.
	if pct, ok := structuralSLPercent(entry, 80.0, atr, 1.0, ss, acfg); !ok || math.Abs(pct-1.5) > 1e-9 {
		t.Fatalf("floor must beat RR cap: want 1.5%%, got %.4f ok=%v", pct, ok)
	}

	// TP target 0 disables the cap: fallback stays at 3.0%.
	if pct, ok := structuralSLPercent(entry, 80.0, atr, 0, ss, acfg); !ok || math.Abs(pct-3.0) > 1e-9 {
		t.Fatalf("no TP target => no cap, want 3.0%%, got %.4f ok=%v", pct, ok)
	}
}
