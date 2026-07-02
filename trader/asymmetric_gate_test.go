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

// --- Momentum-divergence guard (data-driven, 2026-07-02) --------------------
// A 627-trade audit showed MACD-divergent entries lose -0.56/trade while
// non-divergent trend-following makes +0.15/trade. Even when the regime label
// agrees with the direction, an entry that fights MACD momentum must be blocked.

// divergentShort: forced trending_down (price 3% below EMA20) but MACD is
// POSITIVE (bullish momentum) — the ZEC 2026-07-01 stale-label case.
func divergentShortData() *market.Data {
	return &market.Data{
		CurrentPrice:  97.0,
		CurrentEMA20:  100.0, // -3% → forced trending_down
		PriceChange4h: -2.5,
		PriceChange1h: -0.1,
		CurrentMACD:   0.8, // bullish momentum → divergent with open_short
	}
}

// divergentLong: forced trending_up (price 3% above EMA20) but MACD is NEGATIVE
// (bearish momentum) — the mirror case.
func divergentLongData() *market.Data {
	return &market.Data{
		CurrentPrice:  103.0,
		CurrentEMA20:  100.0, // +3% → forced trending_up
		PriceChange4h: 2.5,
		PriceChange1h: 0.1,
		CurrentMACD:   -0.8, // bearish momentum → divergent with open_long
	}
}

func TestDivergentShortIsBlocked(t *testing.T) {
	// SHORT in trending_down but MACD>0 → momentum divergence, must be blocked
	// even though the regime label "agrees" with a short.
	checks := evaluateMarketStateGate(mkInput("open_short", divergentShortData(), baseRegimeCfg()))
	c := findCheck(checks, "trend_misaligned")
	if c == nil {
		t.Fatal("expected trend_misaligned check")
	}
	if c.Passed {
		t.Error("divergent short (MACD>0) in trending_down must be blocked (ZEC case)")
	}
	if !c.Enforced {
		t.Error("momentum-divergence block must be hard-enforced")
	}
}

func TestDivergentLongIsBlocked(t *testing.T) {
	// LONG in trending_up but MACD<0 → momentum divergence, must be blocked.
	checks := evaluateMarketStateGate(mkInput("open_long", divergentLongData(), baseRegimeCfg()))
	c := findCheck(checks, "trend_misaligned")
	if c == nil {
		t.Fatal("expected trend_misaligned check")
	}
	if c.Passed {
		t.Error("divergent long (MACD<0) in trending_up must be blocked")
	}
}

func TestNonDivergentTrendFollowingStillPasses(t *testing.T) {
	// Control: trending_down + short with MACD<0 (aligned momentum) must STILL pass.
	// Guards against the divergence check over-blocking genuine trend-following.
	checks := evaluateMarketStateGate(mkInput("open_short", trendingDownData(), baseRegimeCfg()))
	c := findCheck(checks, "trend_misaligned")
	if c == nil {
		t.Fatal("expected trend_misaligned check")
	}
	if !c.Passed {
		t.Error("non-divergent trend-following short must still pass after divergence guard")
	}
}
