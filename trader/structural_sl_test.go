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
	if pct, ok := structuralSLPercent(entry, 94.0, atr, 0, ss, acfg); !ok || math.Abs(pct-6.0) > 1e-9 {
		t.Fatalf("mid want 6%%, got %.4f ok=%v", pct, ok)
	}
	// Boundary 1 below entry (99) → 0.5 ATR → floored to 1.5 ATR → 3%.
	if pct, ok := structuralSLPercent(entry, 99.0, atr, 0, ss, acfg); !ok || math.Abs(pct-3.0) > 1e-9 {
		t.Fatalf("floor want 3%%, got %.4f ok=%v", pct, ok)
	}
	// Boundary 20 below entry (80) → 10 ATR → beyond backstop 4.5 → falls back to
	// FallbackATRMul (default 2.5) → 5% (was 9% before the nearest-structure fix).
	if pct, ok := structuralSLPercent(entry, 80.0, atr, 0, ss, acfg); !ok || math.Abs(pct-5.0) > 1e-9 {
		t.Fatalf("beyond-backstop fallback want 5%%, got %.4f ok=%v", pct, ok)
	}
}

func TestStructuralSLPercent_InvalidInputs(t *testing.T) {
	acfg := store.ATRProtectionConfig{Enabled: true}
	ss := store.StructuralSLConfig{Enabled: true}
	if _, ok := structuralSLPercent(0, 94, 2, 0, ss, acfg); ok {
		t.Fatalf("zero entry must be not-ok")
	}
	if _, ok := structuralSLPercent(100, 0, 2, 0, ss, acfg); ok {
		t.Fatalf("zero boundary must be not-ok")
	}
	if _, ok := structuralSLPercent(100, 94, 0, 0, ss, acfg); ok {
		t.Fatalf("zero atr must be not-ok")
	}
}

// A SHORT position's boundary is a swing HIGH above entry; distance is symmetric.
func TestStructuralSLPercent_ShortSideSymmetric(t *testing.T) {
	acfg := store.ATRProtectionConfig{Enabled: true}
	ss := store.StructuralSLConfig{Enabled: true, FloorATRMul: 1.5, BackstopATRMul: 4.5}
	entry, atr := 100.0, 2.0
	// Boundary 106 above entry → 3 ATR → 6%.
	if pct, ok := structuralSLPercent(entry, 106.0, atr, 0, ss, acfg); !ok || math.Abs(pct-6.0) > 1e-9 {
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
	key := frozenATRKey(traderID, symbol, tf, "LONG")
	frozenATRMu.Lock()
	frozenATRCache[key] = frozenATREntry{entryPrice: entry, atr: atr}
	frozenATRMu.Unlock()
	frozenStructMu.Lock()
	frozenStructCache[key] = frozenStructEntry{entryPrice: entry, boundary: boundary}
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

	resolved, applied := at.resolveATRProtection(entry, symbol, "open_long", true)
	if !applied {
		t.Fatalf("expected structural resolution to apply")
	}
	got := resolved.LadderTPSL.Rules[0].StopLossPct
	if math.Abs(got-6.0) > 1e-9 {
		t.Fatalf("structural SL want 6%% (3 ATR), got %.4f", got)
	}
}

// Safety property: with an EMPTY cache and atEntry=false (reconcile/guard path), the
// structural boundary is NOT computed fresh — so a pre-existing position that never
// had a boundary frozen at entry is never assigned a fabricated (post-entry-window)
// level. frozenStructBoundaryForPosition must return ok=false without touching klines.
func TestFrozenStructBoundary_NoComputeOffEntry(t *testing.T) {
	const traderID, symbol = "t-noentry", "SOLUSDT"
	tf := store.ATRProtectionConfig{}.WithDefaults().Timeframe
	key := frozenATRKey(traderID, symbol, tf, "LONG")
	frozenStructMu.Lock()
	delete(frozenStructCache, key)
	frozenStructMu.Unlock()

	at := &AutoTrader{id: traderID} // no store, no exchange — compute would need klines
	ss := store.StructuralSLConfig{Enabled: true}
	acfg := store.ATRProtectionConfig{Enabled: true}

	if _, ok := at.frozenStructBoundaryForPosition(symbol, 100, true, false, ss, acfg); ok {
		t.Fatalf("off-entry path must not fabricate a boundary")
	}
}

// With close-confirm on, the RESTING stop resolves to the wide backstop (safety net);
// the tight structural level is enforced by the poll guard instead.
func TestResolveATRProtection_CloseConfirmParksBackstop(t *testing.T) {
	const traderID, symbol = "t-struct-cc", "ETHUSDT"
	entry, atr := 100.0, 2.0
	boundary := 99.0 // 0.5 ATR → would floor to 1.5 ATR, but close-confirm parks backstop

	tf := store.ATRProtectionConfig{}.WithDefaults().Timeframe
	key := frozenATRKey(traderID, symbol, tf, "LONG")
	frozenATRMu.Lock()
	frozenATRCache[key] = frozenATREntry{entryPrice: entry, atr: atr}
	frozenATRMu.Unlock()
	frozenStructMu.Lock()
	frozenStructCache[key] = frozenStructEntry{entryPrice: entry, boundary: boundary}
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

	resolved, applied := at.resolveATRProtection(entry, symbol, "open_long", true)
	if !applied {
		t.Fatalf("expected resolution to apply")
	}
	// Backstop 4.5 ATR × 2% = 9%, not the floored structural 3%.
	got := resolved.LadderTPSL.Rules[0].StopLossPct
	if math.Abs(got-9.0) > 1e-9 {
		t.Fatalf("close-confirm resting stop want backstop 9%%, got %.4f", got)
	}
}

// Regression (hedge isolation): a symbol held simultaneously LONG and SHORT must
// freeze an independent ATR and structural boundary per side. Before the side was
// added to the frozen key, the two legs shared one slot and — because their entry
// prices differ — each reconcile pass evicted the other's frozen value and
// recomputed against a drifted ATR, wobbling the close-confirm backstop price and
// churning duplicate stop orders. Here we prime BOTH sides with distinct values
// and assert neither reads the other's.
func TestFrozenKey_HedgePositionsDoNotCollide(t *testing.T) {
	const traderID, symbol = "t-hedge", "ZECUSDT"
	tf := store.ATRProtectionConfig{}.WithDefaults().Timeframe
	longEntry, shortEntry := 436.95, 427.17
	longATR, shortATR := 6.30, 6.58
	longBoundary, shortBoundary := 420.0, 455.0

	longKey := frozenATRKey(traderID, symbol, tf, "LONG")
	shortKey := frozenATRKey(traderID, symbol, tf, "SHORT")
	if longKey == shortKey {
		t.Fatalf("LONG and SHORT must produce distinct frozen keys, both = %q", longKey)
	}

	frozenATRMu.Lock()
	frozenATRCache[longKey] = frozenATREntry{entryPrice: longEntry, atr: longATR}
	frozenATRCache[shortKey] = frozenATREntry{entryPrice: shortEntry, atr: shortATR}
	frozenATRMu.Unlock()
	frozenStructMu.Lock()
	frozenStructCache[longKey] = frozenStructEntry{entryPrice: longEntry, boundary: longBoundary}
	frozenStructCache[shortKey] = frozenStructEntry{entryPrice: shortEntry, boundary: shortBoundary}
	frozenStructMu.Unlock()
	t.Cleanup(func() {
		frozenATRMu.Lock()
		delete(frozenATRCache, longKey)
		delete(frozenATRCache, shortKey)
		frozenATRMu.Unlock()
		frozenStructMu.Lock()
		delete(frozenStructCache, longKey)
		delete(frozenStructCache, shortKey)
		frozenStructMu.Unlock()
	})

	acfg := store.ATRProtectionConfig{Enabled: true}
	ss := store.StructuralSLConfig{Enabled: true}
	at := &AutoTrader{id: traderID}

	// ATR: each side reads its own frozen value, not the other's.
	if atr, ok := at.frozenATRForPosition(symbol, "LONG", longEntry, acfg); !ok || math.Abs(atr-longATR) > 1e-9 {
		t.Fatalf("LONG frozen ATR want %.2f, got %.4f (ok=%v)", longATR, atr, ok)
	}
	if atr, ok := at.frozenATRForPosition(symbol, "SHORT", shortEntry, acfg); !ok || math.Abs(atr-shortATR) > 1e-9 {
		t.Fatalf("SHORT frozen ATR want %.2f, got %.4f (ok=%v)", shortATR, atr, ok)
	}

	// Structural boundary: each side reads its own, and the entry-price guard rejects
	// a hit that belongs to a different position on the same key.
	if b, ok := at.frozenStructBoundaryForPosition(symbol, longEntry, true, false, ss, acfg); !ok || math.Abs(b-longBoundary) > 1e-9 {
		t.Fatalf("LONG boundary want %.2f, got %.4f (ok=%v)", longBoundary, b, ok)
	}
	if b, ok := at.frozenStructBoundaryForPosition(symbol, shortEntry, false, false, ss, acfg); !ok || math.Abs(b-shortBoundary) > 1e-9 {
		t.Fatalf("SHORT boundary want %.2f, got %.4f (ok=%v)", shortBoundary, b, ok)
	}
}

// A cache slot left over from a prior position on the same key must NOT be handed
// to a new position with a different entry price (Bug 2: the in-memory struct hit
// previously returned without validating entry price).
func TestFrozenStructBoundary_RejectsStaleEntryPrice(t *testing.T) {
	const traderID, symbol = "t-stale", "BTCUSDT"
	tf := store.ATRProtectionConfig{}.WithDefaults().Timeframe
	key := frozenATRKey(traderID, symbol, tf, "LONG")

	frozenStructMu.Lock()
	frozenStructCache[key] = frozenStructEntry{entryPrice: 100.0, boundary: 94.0}
	frozenStructMu.Unlock()
	t.Cleanup(func() {
		frozenStructMu.Lock()
		delete(frozenStructCache, key)
		frozenStructMu.Unlock()
	})

	ss := store.StructuralSLConfig{Enabled: true}
	acfg := store.ATRProtectionConfig{Enabled: true}
	at := &AutoTrader{id: traderID} // no store/exchange: a fresh compute would need klines

	// New position at a very different entry (200) on the same key: the stale 100-entry
	// boundary must be rejected, and with allowCompute=false no fabricated value returns.
	if _, ok := at.frozenStructBoundaryForPosition(symbol, 200.0, true, false, ss, acfg); ok {
		t.Fatalf("stale-entry boundary must be rejected for a different position")
	}
}
