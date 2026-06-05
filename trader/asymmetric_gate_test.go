package trader

import (
	"testing"

	"nofx/kernel"
	"nofx/market"
	"nofx/store"
)

// findCheck returns the first check with the given code, or nil.
func findCheck(checks []EntryGateCheck, code string) *EntryGateCheck {
	for i := range checks {
		if checks[i].Code == code {
			return &checks[i]
		}
	}
	return nil
}

func baseRegimeCfg() store.RegimeFilterConfig {
	return store.RegimeFilterConfig{
		Enabled:               true,
		RequireTrendAlignment: true,
	}
}

func mkInput(action string, data *market.Data, cfg store.RegimeFilterConfig) entryGateInput {
	sc := &store.StrategyConfig{}
	sc.Protection.RegimeFilter = cfg
	return entryGateInput{
		Decision:       &kernel.Decision{Action: action, TriggerType: "support_rejection_confirmed"},
		MarketData:     data,
		StrategyConfig: sc,
	}
}

// trending_up: price >2% above EMA20 forces classifyProtectionRegime → trending_up.
func trendingUpData() *market.Data {
	return &market.Data{
		CurrentPrice:  103.0,
		CurrentEMA20:  100.0, // +3% deviation → forced trending_up
		PriceChange4h: 2.5,
		PriceChange1h: 0.5,
		CurrentMACD:   1.0,
	}
}

// trending_down: price >2% below EMA20 → forced trending_down.
func trendingDownData() *market.Data {
	return &market.Data{
		CurrentPrice:  97.0,
		CurrentEMA20:  100.0, // -3% deviation → forced trending_down
		PriceChange4h: -2.5,
		PriceChange1h: -0.5,
		CurrentMACD:   -1.0,
	}
}

// After reverting the sweet-spot soft-gate (2026-06-04), ALL counter-trend
// (opposes-regime) entries must be HARD-blocked (Enforced=true), regardless of
// dir_mom. Rigorous backtests proved counter-trend trades lose in both directions.

func TestCounterTrendShortIsHardBlocked(t *testing.T) {
	// SHORT in trending_up = counter-trend → hard block.
	checks := evaluateMarketStateGate(mkInput("open_short", trendingUpData(), baseRegimeCfg()))
	c := findCheck(checks, "trend_misaligned")
	if c == nil {
		t.Fatal("expected trend_misaligned check")
	}
	if c.Passed {
		t.Fatal("counter-trend short in trending_up should be misaligned (not passed)")
	}
	if !c.Enforced {
		t.Error("counter-trend short must be HARD-blocked (Enforced=true) after sweet-spot revert")
	}
}

func TestCounterTrendLongIsHardBlocked(t *testing.T) {
	// LONG in trending_down = counter-trend → hard block.
	checks := evaluateMarketStateGate(mkInput("open_long", trendingDownData(), baseRegimeCfg()))
	c := findCheck(checks, "trend_misaligned")
	if c == nil {
		t.Fatal("expected trend_misaligned check")
	}
	if c.Passed {
		t.Fatal("counter-trend long in trending_down should be misaligned")
	}
	if !c.Enforced {
		t.Error("counter-trend long must be HARD-blocked (Enforced=true)")
	}
}

func TestTrendFollowingLongPasses(t *testing.T) {
	// LONG in trending_up = trend-following → should pass alignment.
	checks := evaluateMarketStateGate(mkInput("open_long", trendingUpData(), baseRegimeCfg()))
	c := findCheck(checks, "trend_misaligned")
	if c == nil {
		t.Fatal("expected trend_misaligned check")
	}
	if !c.Passed {
		t.Error("trend-following long in trending_up should pass alignment")
	}
}

func TestTrendFollowingShortPasses(t *testing.T) {
	// SHORT in trending_down = trend-following → should pass alignment.
	checks := evaluateMarketStateGate(mkInput("open_short", trendingDownData(), baseRegimeCfg()))
	c := findCheck(checks, "trend_misaligned")
	if c == nil {
		t.Fatal("expected trend_misaligned check")
	}
	if !c.Passed {
		t.Error("trend-following short in trending_down should pass alignment")
	}
}
