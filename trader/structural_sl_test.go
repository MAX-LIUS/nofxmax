package trader

import (
	"math"
	"testing"

	"nofx/store"
)

// structuralSLPercent: distance = |entry - boundary| in ATR units, clamped to
// [floor, backstop], then converted to percent-of-entry.
func TestStructuralSLPercent_ClampAndConvert(t *testing.T) {
	acfg := store.ATRProtectionConfig{Enabled: true}
	ss := store.StructuralSLConfig{Enabled: true, FloorATRMul: 1.5, BackstopATRMul: 4.5}
	entry, atr := 100.0, 2.0 // 1 ATR = 2% of entry

	// Boundary 6 below entry (94) → 3 ATR → within [1.5,4.5] → 6%.
	if pct, ok := structuralSLPercent(entry, 94.0, atr, ss, acfg); !ok || math.Abs(pct-6.0) > 1e-9 {
		t.Fatalf("mid want 6%%, got %.4f ok=%v", pct, ok)
	}
	// Boundary 1 below entry (99) → 0.5 ATR → floored to 1.5 ATR → 3%.
	if pct, ok := structuralSLPercent(entry, 99.0, atr, ss, acfg); !ok || math.Abs(pct-3.0) > 1e-9 {
		t.Fatalf("floor want 3%%, got %.4f ok=%v", pct, ok)
	}
	// Boundary 20 below entry (80) → 10 ATR → capped to 4.5 ATR → 9%.
	if pct, ok := structuralSLPercent(entry, 80.0, atr, ss, acfg); !ok || math.Abs(pct-9.0) > 1e-9 {
		t.Fatalf("cap want 9%%, got %.4f ok=%v", pct, ok)
	}
}

func TestStructuralSLPercent_InvalidInputs(t *testing.T) {
	acfg := store.ATRProtectionConfig{Enabled: true}
	ss := store.StructuralSLConfig{Enabled: true}
	if _, ok := structuralSLPercent(0, 94, 2, ss, acfg); ok {
		t.Fatalf("zero entry must be not-ok")
	}
	if _, ok := structuralSLPercent(100, 0, 2, ss, acfg); ok {
		t.Fatalf("zero boundary must be not-ok")
	}
	if _, ok := structuralSLPercent(100, 94, 0, ss, acfg); ok {
		t.Fatalf("zero atr must be not-ok")
	}
}

// A SHORT position's boundary is a swing HIGH above entry; distance is symmetric.
func TestStructuralSLPercent_ShortSideSymmetric(t *testing.T) {
	acfg := store.ATRProtectionConfig{Enabled: true}
	ss := store.StructuralSLConfig{Enabled: true, FloorATRMul: 1.5, BackstopATRMul: 4.5}
	entry, atr := 100.0, 2.0
	// Boundary 106 above entry → 3 ATR → 6%.
	if pct, ok := structuralSLPercent(entry, 106.0, atr, ss, acfg); !ok || math.Abs(pct-6.0) > 1e-9 {
		t.Fatalf("short mid want 6%%, got %.4f ok=%v", pct, ok)
	}
}

// End-to-end resolver wiring: a ladder SL rule with unit="structural" resolves to the
// structural percent derived from the frozen boundary. Seeds the frozen caches to
// bypass the network GetKlines fetch (that path is exercised at runtime).
func TestResolveATRProtection_StructuralUnitWiring(t *testing.T) {
	const traderID, symbol = "t-struct", "BTCUSDT"
	entry, atr := 100.0, 2.0 // 1 ATR = 2%
	boundary := 94.0         // 3 ATR below entry → within [1.5,4.5] → 6%

	tf := store.ATRProtectionConfig{}.WithDefaults().Timeframe
	key := frozenATRKey(traderID, symbol, tf)
	frozenATRMu.Lock()
	frozenATRCache[key] = frozenATREntry{entryPrice: entry, atr: atr}
	frozenATRMu.Unlock()
	frozenStructMu.Lock()
	frozenStructCache[key] = boundary
	frozenStructMu.Unlock()
	t.Cleanup(func() {
		frozenATRMu.Lock()
		delete(frozenATRCache, key)
		frozenATRMu.Unlock()
		frozenStructMu.Lock()
		delete(frozenStructCache, key)
		frozenStructMu.Unlock()
	})

	at := &AutoTrader{
		id: traderID,
		config: AutoTraderConfig{StrategyConfig: &store.StrategyConfig{
			ATRProtection: store.ATRProtectionConfig{Enabled: true},
			Protection: store.ProtectionConfig{
				LadderTPSL: store.LadderTPSLConfig{
					Enabled:         true,
					StopLossEnabled: true,
					StructuralSL:    store.StructuralSLConfig{Enabled: true, FloorATRMul: 1.5, BackstopATRMul: 4.5},
					Rules: []store.LadderTPSLRule{{
						StopLossPct: 4.5, StopLossUnit: store.ProtectionUnitStructural, StopLossCloseRatioPct: 100,
					}},
				},
			},
		}},
	}

	resolved, applied := at.resolveATRProtection(entry, symbol, "open_long")
	if !applied {
		t.Fatalf("expected structural resolution to apply")
	}
	got := resolved.LadderTPSL.Rules[0].StopLossPct
	if math.Abs(got-6.0) > 1e-9 {
		t.Fatalf("structural SL want 6%% (3 ATR), got %.4f", got)
	}
}

// With close-confirm on, the RESTING stop resolves to the wide backstop (safety net);
// the tight structural level is enforced by the poll guard instead.
func TestResolveATRProtection_CloseConfirmParksBackstop(t *testing.T) {
	const traderID, symbol = "t-struct-cc", "ETHUSDT"
	entry, atr := 100.0, 2.0
	boundary := 99.0 // 0.5 ATR → would floor to 1.5 ATR, but close-confirm parks backstop

	tf := store.ATRProtectionConfig{}.WithDefaults().Timeframe
	key := frozenATRKey(traderID, symbol, tf)
	frozenATRMu.Lock()
	frozenATRCache[key] = frozenATREntry{entryPrice: entry, atr: atr}
	frozenATRMu.Unlock()
	frozenStructMu.Lock()
	frozenStructCache[key] = boundary
	frozenStructMu.Unlock()
	t.Cleanup(func() {
		frozenATRMu.Lock()
		delete(frozenATRCache, key)
		frozenATRMu.Unlock()
		frozenStructMu.Lock()
		delete(frozenStructCache, key)
		frozenStructMu.Unlock()
	})

	at := &AutoTrader{
		id: traderID,
		config: AutoTraderConfig{StrategyConfig: &store.StrategyConfig{
			ATRProtection: store.ATRProtectionConfig{Enabled: true},
			Protection: store.ProtectionConfig{
				LadderTPSL: store.LadderTPSLConfig{
					Enabled:         true,
					StopLossEnabled: true,
					StructuralSL:    store.StructuralSLConfig{Enabled: true, FloorATRMul: 1.5, BackstopATRMul: 4.5, CloseConfirm: true},
					Rules: []store.LadderTPSLRule{{
						StopLossPct: 4.5, StopLossUnit: store.ProtectionUnitStructural, StopLossCloseRatioPct: 100,
					}},
				},
			},
		}},
	}

	resolved, applied := at.resolveATRProtection(entry, symbol, "open_long")
	if !applied {
		t.Fatalf("expected resolution to apply")
	}
	// Backstop 4.5 ATR × 2% = 9%, not the floored structural 3%.
	got := resolved.LadderTPSL.Rules[0].StopLossPct
	if math.Abs(got-9.0) > 1e-9 {
		t.Fatalf("close-confirm resting stop want backstop 9%%, got %.4f", got)
	}
}
