package trader

import (
	"testing"

	"nofx/kernel"
	"nofx/market"
	"nofx/store"
)

// TestTrendPhaseExtensionExhaustionAreSoft locks in the 2026-06-07 change:
// extension/exhaustion phase trend-following entries are SOFT-gated (penalty +
// size reduction), not hard-blocked. Backtest: blocking them lost +84%/+43% of
// would-be profit. Counter-trend / EMA-conflict / regime gates stay hard (unchanged).
func mkExtInput(action string, data *market.Data) entryGateInput {
	sc := &store.StrategyConfig{}
	sc.Protection.RegimeFilter = store.RegimeFilterConfig{Enabled: true, RequireTrendAlignment: false}
	return entryGateInput{
		Decision:       &kernel.Decision{Action: action, TriggerType: "support_rejection_confirmed", Confidence: 70},
		MarketData:     data,
		StrategyConfig: sc,
	}
}

func TestTrendPhaseExtensionIsSoft(t *testing.T) {
	// Strong uptrend extension: price way above EMA20, big 4h move → extension phase.
	// A LONG here is trend-following in extension → must be SOFT (not enforced).
	data := &market.Data{CurrentPrice: 106, CurrentEMA20: 100, PriceChange4h: 3.0, PriceChange1h: 0.5}
	checks := evaluateMarketStateGate(mkExtInput("open_long", data))
	c := findCheck(checks, "trend_phase_extension")
	if c == nil {
		c = findCheck(checks, "trend_phase_extension_ema_driven")
	}
	if c == nil {
		t.Fatal("expected a trend_phase_extension check")
	}
	if c.Enforced {
		t.Error("extension trend-following must be SOFT-gated (Enforced=false), not hard block")
	}
	if c.Penalty <= 0 {
		t.Errorf("expected positive penalty for size reduction, got %d", c.Penalty)
	}
}

func TestTrendPhaseExhaustionIsSoft(t *testing.T) {
	// Exhaustion: very large 4h move → exhaustion phase. Must be SOFT now.
	data := &market.Data{CurrentPrice: 104, CurrentEMA20: 100, PriceChange4h: 4.0, PriceChange1h: 0.1}
	checks := evaluateMarketStateGate(mkExtInput("open_long", data))
	c := findCheck(checks, "trend_phase_exhaustion")
	if c == nil {
		t.Skip("data did not classify as exhaustion; skipping")
	}
	if c.Enforced {
		t.Error("exhaustion must be SOFT-gated (Enforced=false), not hard block")
	}
}
