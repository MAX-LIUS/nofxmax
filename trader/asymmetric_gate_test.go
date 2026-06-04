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

// hasCodePrefix reports whether any check's code starts with prefix.
func baseRegimeCfg() store.RegimeFilterConfig {
	return store.RegimeFilterConfig{
		Enabled:                  true,
		RequireTrendAlignment:    true,
		AsymmetricTrendAlignment: true,
		CounterTrendShortPenalty: 25,
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

// trending_up with mild pullback: price >2% above EMA20 forces trending_up,
// but 4h change is mildly negative (-0.3%) → a SHORT here has dir_mom=+0.3... no.
// dir_mom for short = -chg4h = +0.3 (>0, chasing) — not sweet spot.
// For the sweet spot we need dir_mom ∈ (-0.5,0], i.e. for a short: -chg4h ∈ (-0.5,0]
// → chg4h ∈ [0, 0.5). So a small POSITIVE 4h with price extended above EMA20.
func trendingUpSweetShortData() *market.Data {
	return &market.Data{
		CurrentPrice:  103.0,
		CurrentEMA20:  100.0, // +3% → forced trending_up
		PriceChange4h: 0.3,   // short dir_mom = -0.3 ∈ (-0.5,0] → sweet spot
		PriceChange1h: 0.1,
		CurrentMACD:   1.0,
	}
}

// trending_up with a hard counter-trend short (4h still rising strongly):
// short dir_mom = -2.5 (≤ -0.5) → falling-knife, must stay HARD.
func trendingUpHardData() *market.Data {
	return &market.Data{
		CurrentPrice:  103.0,
		CurrentEMA20:  100.0,
		PriceChange4h: 2.5, // short dir_mom = -2.5 → not sweet spot
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

func TestSweetSpotCounterTrendIsSoft(t *testing.T) {
	// Counter-trend SHORT in trending_up, dir_mom in sweet spot → SOFT (not enforced).
	checks := evaluateMarketStateGate(mkInput("open_short", trendingUpSweetShortData(), baseRegimeCfg()))
	c := findCheck(checks, "trend_misaligned")
	if c == nil {
		t.Fatal("expected trend_misaligned check")
	}
	if c.Passed {
		t.Fatal("counter-trend short in trending_up should be misaligned (not passed)")
	}
	if c.Enforced {
		t.Error("sweet-spot counter-trend (dir_mom∈(-0.5,0]) should be SOFT (Enforced=false)")
	}
	if c.Penalty <= 0 {
		t.Errorf("expected positive penalty, got %d", c.Penalty)
	}
}

func TestHardCounterTrendStaysHard(t *testing.T) {
	// Counter-trend SHORT against a strong rising 4h (falling knife) → stays HARD.
	checks := evaluateMarketStateGate(mkInput("open_short", trendingUpHardData(), baseRegimeCfg()))
	c := findCheck(checks, "trend_misaligned")
	if c == nil {
		t.Fatal("expected trend_misaligned check")
	}
	if c.Passed {
		t.Fatal("hard counter-trend short should be misaligned")
	}
	if !c.Enforced {
		t.Error("counter-trend short outside sweet spot (dir_mom=-2.5) must stay HARD")
	}
}

func TestAsymmetricCounterTrendLongStaysHard(t *testing.T) {
	// Counter-trend LONG in trending_down with strong move → should stay HARD.
	checks := evaluateMarketStateGate(mkInput("open_long", trendingDownData(), baseRegimeCfg()))
	c := findCheck(checks, "trend_misaligned")
	if c == nil {
		t.Fatal("expected trend_misaligned check")
	}
	if c.Passed {
		t.Fatal("counter-trend long in trending_down should be misaligned")
	}
	if !c.Enforced {
		t.Error("counter-trend long in strong trending_down must stay HARD")
	}
}

func TestAsymmetricDisabledKeepsLegacyHardBlock(t *testing.T) {
	// With AsymmetricTrendAlignment=false, even sweet-spot counter-trend stays hard.
	cfg := baseRegimeCfg()
	cfg.AsymmetricTrendAlignment = false
	checks := evaluateMarketStateGate(mkInput("open_short", trendingUpSweetShortData(), cfg))
	c := findCheck(checks, "trend_misaligned")
	if c == nil {
		t.Fatal("expected trend_misaligned check")
	}
	if !c.Enforced {
		t.Error("with asymmetric disabled, counter-trend short must stay HARD (legacy behavior)")
	}
}
