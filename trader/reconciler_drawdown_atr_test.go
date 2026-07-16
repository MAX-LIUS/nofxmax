package trader

import (
	"math"
	"testing"

	"nofx/store"
)

// TestReconcilerDrawdownATRResolution guards against the SPCXUSDT churn bug
// (2026-07-16): the protection reconciler fetched active drawdown rules but did
// NOT run resolveDrawdownRulesATR, while the main drawdown monitor and decision
// path both did. An ATR-unit rule therefore armed on its RAW multiple in the
// reconciler (e.g. 1.5 treated as 1.5%) but on the ATR-derived percent in the
// main loop (e.g. 3.0%), placing two native trailing orders at different
// activation prices that each treated the other as a stale duplicate — an
// endless place/cancel churn.
//
// This test pins the transform: an ATR-unit drawdown rule MUST resolve to the
// ATR-derived percent, so both paths (which now share resolveDrawdownRulesATR)
// produce identical activation prices.
func TestReconcilerDrawdownATRResolution(t *testing.T) {
	const traderID, symbol = "t-dd-atr", "BTCUSDT"
	entry, atr := 100.0, 2.0 // 1 ATR = 2% of entry

	tf := store.ATRProtectionConfig{}.WithDefaults().Timeframe
	key := frozenATRKey(traderID, symbol, tf, "SHORT")
	frozenATRMu.Lock()
	frozenATRCache[key] = frozenATREntry{entryPrice: entry, atr: atr}
	frozenATRMu.Unlock()
	t.Cleanup(func() {
		frozenATRMu.Lock()
		delete(frozenATRCache, key)
		frozenATRMu.Unlock()
	})

	at := &AutoTrader{
		id: traderID,
		config: AutoTraderConfig{StrategyConfig: &store.StrategyConfig{
			ATRProtection: store.ATRProtectionConfig{Enabled: true},
		}},
	}

	// Rule expressed in ATR units: MinProfit 1.5 ATR, MaxDrawdown 0.9 ATR.
	rules := []store.DrawdownTakeProfitRule{{
		MinProfitPct:    1.5,
		MinProfitUnit:   store.ProtectionUnitATR,
		MaxDrawdownPct:  0.9,
		MaxDrawdownUnit: store.ProtectionUnitATR,
		CloseRatioPct:   100,
	}}

	resolved := at.resolveDrawdownRulesATR(rules, symbol, "short", entry)
	if len(resolved) != 1 {
		t.Fatalf("expected 1 resolved rule, got %d", len(resolved))
	}

	// 1.5 ATR * 2.0 / 100 * 100 = 3.0%; 0.9 ATR → 1.8%.
	if got := resolved[0].MinProfitPct; math.Abs(got-3.0) > 1e-9 {
		t.Fatalf("MinProfitPct: want 3.0%% (1.5 ATR), got %.4f — reconciler would churn against main loop", got)
	}
	if got := resolved[0].MaxDrawdownPct; math.Abs(got-1.8) > 1e-9 {
		t.Fatalf("MaxDrawdownPct: want 1.8%% (0.9 ATR), got %.4f", got)
	}

	// The raw input must NOT survive unconverted (that was the bug).
	if math.Abs(resolved[0].MinProfitPct-1.5) < 1e-9 {
		t.Fatalf("MinProfitPct stayed at raw ATR multiple 1.5 — ATR resolution did not run")
	}
}
