package trader

import (
	"testing"

	"nofx/store"
)

// snapshotATRContext exists so a closed position can still be reviewed against the
// ATR its protection distances were derived from — the live frozen-ATR record is
// deleted at close. These tests pin the two properties that matter: it reports the
// SAME value the resolver used, and it never freezes one itself.

func newSnapshotATRTrader(t *testing.T, enabled bool) *AutoTrader {
	t.Helper()
	at := &AutoTrader{
		id:     "snap-atr-test",
		config: AutoTraderConfig{StrategyConfig: &store.StrategyConfig{}},
	}
	at.config.StrategyConfig.ATRProtection = store.ATRProtectionConfig{Enabled: enabled}
	return at
}

func seedFrozenATR(t *testing.T, at *AutoTrader, symbol, side string, entry, atr float64) string {
	t.Helper()
	tf := store.ATRProtectionConfig{}.WithDefaults().Timeframe
	key := frozenATRKey(at.id, symbol, tf, side)
	frozenATRMu.Lock()
	frozenATRCache[key] = frozenATREntry{entryPrice: entry, atr: atr}
	frozenATRMu.Unlock()
	t.Cleanup(func() {
		frozenATRMu.Lock()
		delete(frozenATRCache, key)
		frozenATRMu.Unlock()
	})
	return key
}

func TestSnapshotATRContextReportsTheFrozenValue(t *testing.T) {
	symbol, side := "ETHUSDT", "SHORT"
	// The real ETHUSDT SHORT: entry 1862.63 with 15m ATR ≈ 2.1985 price units,
	// i.e. 0.118% per ATR — the number that made 1.1/1.7/2.5 ATR all resolve
	// under the 0.3% floor and collapse onto one price.
	entry, atr := 1862.63, 2.1985
	at := newSnapshotATRTrader(t, true)
	seedFrozenATR(t, at, symbol, side, entry, atr)

	gotATR, gotTF := at.snapshotATRContext(&protectionExecutionRequest{
		Symbol: symbol, PositionSide: side, EntryPrice: entry,
	})
	if gotATR != atr {
		t.Fatalf("expected the frozen ATR %v, got %v", atr, gotATR)
	}
	wantTF := store.ATRProtectionConfig{}.WithDefaults().Timeframe
	if gotTF != wantTF {
		t.Fatalf("timeframe not the effective one: got %q want %q", gotTF, wantTF)
	}
	// Sanity: this ATR really is the sub-floor regime, so the stored number
	// explains the collapse rather than contradicting it.
	if pct := atr / entry * 100; pct >= 0.3 {
		t.Fatalf("expected sub-floor ATR%%, got %.4f%%", pct)
	}
}

func TestSnapshotATRContextDoesNotFreezeOnMiss(t *testing.T) {
	symbol, side := "SOLUSDT", "LONG"
	at := newSnapshotATRTrader(t, true)
	tf := store.ATRProtectionConfig{}.WithDefaults().Timeframe
	key := frozenATRKey(at.id, symbol, tf, side)

	// No cache entry and no store: a get-or-freeze would try a live fetch here
	// and, on success, persist a record — binding the position to an ATR that was
	// never used to size anything. The read-only path must simply report unknown.
	gotATR, gotTF := at.snapshotATRContext(&protectionExecutionRequest{
		Symbol: symbol, PositionSide: side, EntryPrice: 150,
	})
	if gotATR != 0 {
		t.Fatalf("expected unknown (0) on miss, got %v", gotATR)
	}
	// The timeframe is still reported: it is config, not measurement.
	if gotTF != tf {
		t.Fatalf("expected timeframe %q even on miss, got %q", tf, gotTF)
	}
	frozenATRMu.Lock()
	_, created := frozenATRCache[key]
	frozenATRMu.Unlock()
	if created {
		frozenATRMu.Lock()
		delete(frozenATRCache, key)
		frozenATRMu.Unlock()
		t.Fatal("snapshotATRContext froze an ATR entry; the reporting path must never write trading state")
	}
}

func TestSnapshotATRContextSilentWhenATRProtectionOff(t *testing.T) {
	// A percent-unit strategy has no ATR behind its distances, so reporting one
	// would invent a denominator that was never used.
	at := newSnapshotATRTrader(t, false)
	seedFrozenATR(t, at, "BTCUSDT", "LONG", 60000, 500)
	atr, tf := at.snapshotATRContext(&protectionExecutionRequest{
		Symbol: "BTCUSDT", PositionSide: "LONG", EntryPrice: 60000,
	})
	if atr != 0 || tf != "" {
		t.Fatalf("expected no ATR context when ATR protection is off, got %v %q", atr, tf)
	}
}

func TestSnapshotATRContextToleratesMissingStrategyConfig(t *testing.T) {
	// Defensive: some construction paths leave StrategyConfig nil. A snapshot is
	// best-effort and must not panic the open path.
	at := &AutoTrader{id: "snap-atr-nil"}
	atr, tf := at.snapshotATRContext(&protectionExecutionRequest{
		Symbol: "ETHUSDT", PositionSide: "SHORT", EntryPrice: 1862.63,
	})
	if atr != 0 || tf != "" {
		t.Fatalf("expected zero context, got %v %q", atr, tf)
	}
}

func TestFrozenATRReadOnlyRejectsIdentityMismatch(t *testing.T) {
	symbol, side := "ETHUSDT", "SHORT"
	at := newSnapshotATRTrader(t, true)
	// Frozen against a different position (entry far outside the tolerance).
	seedFrozenATR(t, at, symbol, side, 1500, 2.0)

	cfg := store.ATRProtectionConfig{Enabled: true}
	if atr, ok := at.frozenATRForPositionReadOnly(symbol, side, 1862.63, cfg); ok {
		t.Fatalf("expected a miss on identity mismatch, got atr=%v", atr)
	}
	// Same position: hit.
	if atr, ok := at.frozenATRForPositionReadOnly(symbol, side, 1500, cfg); !ok || atr != 2.0 {
		t.Fatalf("expected a hit for the matching entry, got atr=%v ok=%v", atr, ok)
	}
}
