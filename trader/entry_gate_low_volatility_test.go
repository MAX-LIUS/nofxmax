package trader

import (
	"testing"

	"nofx/kernel"
	"nofx/market"
	"nofx/store"
)

// The volatility FLOOR is a sibling of the existing ceiling and lives in the
// market-access stage on purpose: EntryGateConfig.MinATR14Pct does the same job
// but hangs off EntryStructure, which is disabled on every live strategy, so it
// can be set and silently never run. These tests pin that the floor needs no
// parent switch, that it is off unless asked for, and that missing ATR is not
// treated as "too low".

func lowVolConfig(block bool, minPct float64) *store.StrategyConfig {
	cfg := &store.StrategyConfig{}
	cfg.Protection.RegimeFilter = store.RegimeFilterConfig{
		Enabled:            true,
		BlockLowVolatility: block,
		MinATR14Pct:        minPct,
	}
	// Deliberately left false: this is the switch that short-circuits the OTHER
	// min-ATR gate. The floor under test must fire regardless.
	cfg.EntryStructure.Enabled = false
	return cfg
}

// lowVolConfigParentOff is lowVolConfig with regime_filter.enabled FALSE, the
// state 3 of the 4 live strategies are actually in. Every other check in
// evaluateMarketStateGate sits behind that early-return, so a floor placed below
// it would be settable in the UI and dead in production.
func lowVolConfigParentOff(block bool, minPct float64) *store.StrategyConfig {
	cfg := lowVolConfig(block, minPct)
	cfg.Protection.RegimeFilter.Enabled = false
	return cfg
}

// lowVolMarketData builds market data whose ATR14% equals the requested percent.
func lowVolMarketData(atrPct float64) *market.Data {
	const price = 1000.0
	return &market.Data{
		CurrentPrice:   price,
		IntradaySeries: &market.IntradayData{ATR14: price * atrPct / 100},
	}
}

func lowVolInput(cfg *store.StrategyConfig, data *market.Data) entryGateInput {
	return entryGateInput{
		// A valid trigger keeps the earlier checks from muddying the result.
		Decision: &kernel.Decision{
			Symbol:      "ETHUSDT",
			Action:      "open_long",
			TriggerType: "support_rejection_confirmed",
		},
		MarketData:     data,
		StrategyConfig: cfg,
	}
}

func TestVolatilityFloorBlocksFeeDominatedRegime(t *testing.T) {
	// 0.118% is the real ETHUSDT case: a 0.9-ATR target resolves to 0.106%,
	// against a 0.12% round-trip cost — the target is inside the fee band.
	checks := evaluateMarketStateGate(lowVolInput(lowVolConfig(true, 0.3), lowVolMarketData(0.118)))
	c := findCheck(checks, "volatility_too_low")
	if c == nil {
		t.Fatal("expected a volatility_too_low check to be emitted")
	}
	if c.Passed {
		t.Fatalf("expected ATR14%%=0.118 to fail a 0.3 floor, got passed: %s", c.Detail)
	}
	if !c.Enforced {
		t.Fatal("the floor must be enforced (blocking), not advisory")
	}
	// It must actually block, not merely deduct score.
	blocked, code, _ := firstEnforcedFailure(checks)
	if !blocked || code != "volatility_too_low" {
		t.Fatalf("expected the gate to block on volatility_too_low, got blocked=%v code=%q", blocked, code)
	}
}

func TestVolatilityFloorIndependentOfEntryStructureSwitch(t *testing.T) {
	// The whole point: EntryStructure.Enabled is false here (as on every live
	// strategy) and the floor still fires. If this ever regresses, the setting
	// becomes another one that can be configured but does nothing.
	cfg := lowVolConfig(true, 0.5)
	if cfg.EntryStructure.Enabled {
		t.Fatal("test setup error: EntryStructure must be disabled for this test to mean anything")
	}
	checks := evaluateMarketStateGate(lowVolInput(cfg, lowVolMarketData(0.2)))
	c := findCheck(checks, "volatility_too_low")
	if c == nil || c.Passed {
		t.Fatalf("floor must fire with EntryStructure disabled; got %+v", c)
	}
}

func TestVolatilityFloorIndependentOfRegimeFilterSwitch(t *testing.T) {
	// regime_filter.enabled is false on 3 of the 4 live strategies. The floor is
	// evaluated ahead of that early-return specifically so operators do not have to
	// turn on the whole regime filter (and inherit its regime allow-list, funding
	// and trend-alignment rules) just to reject fee-dominated ranges.
	cfg := lowVolConfigParentOff(true, 0.5)
	if cfg.Protection.RegimeFilter.Enabled {
		t.Fatal("test setup error: regime_filter must be disabled for this test to mean anything")
	}
	checks := evaluateMarketStateGate(lowVolInput(cfg, lowVolMarketData(0.2)))
	c := findCheck(checks, "volatility_too_low")
	if c == nil {
		t.Fatal("floor must be evaluated even when regime_filter.enabled is false")
	}
	if c.Passed {
		t.Fatalf("ATR14%%=0.2 is below the 0.5 floor and must fail; got %+v", c)
	}
	if !c.Enforced {
		t.Error("floor must be enforced, not advisory, or it will not block the entry")
	}
	// Sanity: the parent switch must still suppress its own checks, i.e. moving the
	// floor up must not have leaked the rest of the gate out of the early-return.
	if other := findCheck(checks, "volatility_too_high"); other != nil {
		t.Errorf("ceiling must stay behind regime_filter.enabled; got %+v", other)
	}
}

func TestVolatilityFloorOffByDefaultWithParentDisabled(t *testing.T) {
	// Default-off has to hold in the parent-disabled state too, otherwise moving the
	// check above the early-return would start rejecting entries on the 3 live
	// strategies the moment this ships.
	cfg := lowVolConfigParentOff(false, 0.3)
	checks := evaluateMarketStateGate(lowVolInput(cfg, lowVolMarketData(0.05)))
	if c := findCheck(checks, "volatility_too_low"); c != nil {
		t.Fatalf("floor must stay silent when block_low_volatility is false; got %+v", c)
	}
}

func TestVolatilityFloorPassesAboveThreshold(t *testing.T) {
	checks := evaluateMarketStateGate(lowVolInput(lowVolConfig(true, 0.3), lowVolMarketData(0.9)))
	c := findCheck(checks, "volatility_too_low")
	if c == nil {
		t.Fatal("expected the check to be reported even when it passes")
	}
	if !c.Passed {
		t.Fatalf("ATR14%%=0.9 should clear a 0.3 floor: %s", c.Detail)
	}
}

func TestVolatilityFloorBoundaryIsInclusive(t *testing.T) {
	// Exactly at the floor must pass: the operator asked for "at least this much
	// volatility", and rejecting the boundary would make the number mean
	// something other than what the label says.
	checks := evaluateMarketStateGate(lowVolInput(lowVolConfig(true, 0.3), lowVolMarketData(0.3)))
	c := findCheck(checks, "volatility_too_low")
	if c == nil || !c.Passed {
		t.Fatalf("ATR14%% exactly at the floor must pass; got %+v", c)
	}
}

func TestVolatilityFloorOffByDefault(t *testing.T) {
	// Measured on 519 closed positions: the LOW-volatility half is where this
	// system currently makes money (+20.01 at 63% win, vs -132.69 at 52% for the
	// high half, controlling for symbol). Defaulting this ON would remove the
	// profitable half, so absence of the flag must mean "no check at all".
	checks := evaluateMarketStateGate(lowVolInput(lowVolConfig(false, 0.3), lowVolMarketData(0.05)))
	if c := findCheck(checks, "volatility_too_low"); c != nil {
		t.Fatalf("no floor check should be emitted when block_low_volatility is off, got %+v", c)
	}
}

func TestVolatilityFloorSkippedWhenThresholdUnset(t *testing.T) {
	// Enabled with a 0 threshold is a half-configured state; blocking everything
	// would be the worst reading of it. The API surfaces a warning for this.
	checks := evaluateMarketStateGate(lowVolInput(lowVolConfig(true, 0), lowVolMarketData(0.05)))
	if c := findCheck(checks, "volatility_too_low"); c != nil {
		t.Fatalf("a 0 threshold must disable the check, got %+v", c)
	}
}

func TestVolatilityFloorSkippedWhenATRUnavailable(t *testing.T) {
	// ATR14% == 0 means "could not measure", which is NOT the same as "too low".
	// Blocking here would halt all entries on a data outage.
	data := &market.Data{CurrentPrice: 1000}
	checks := evaluateMarketStateGate(lowVolInput(lowVolConfig(true, 0.3), data))
	if c := findCheck(checks, "volatility_too_low"); c != nil {
		t.Fatalf("missing ATR must skip the floor, not block; got %+v", c)
	}
}

func TestVolatilityCeilingStillWorksAlongsideFloor(t *testing.T) {
	// The two form one window and must not interfere.
	cfg := &store.StrategyConfig{}
	cfg.Protection.RegimeFilter = store.RegimeFilterConfig{
		Enabled:             true,
		BlockLowVolatility:  true,
		MinATR14Pct:         0.3,
		BlockHighVolatility: true,
		MaxATR14Pct:         3.0,
	}
	// Inside the window: both report, both pass.
	checks := evaluateMarketStateGate(lowVolInput(cfg, lowVolMarketData(1.0)))
	for _, code := range []string{"volatility_too_low", "volatility_too_high"} {
		if c := findCheck(checks, code); c == nil || !c.Passed {
			t.Fatalf("%s should pass inside the window; got %+v", code, c)
		}
	}
	// Above the ceiling: only the ceiling fails.
	checks = evaluateMarketStateGate(lowVolInput(cfg, lowVolMarketData(4.0)))
	if c := findCheck(checks, "volatility_too_high"); c == nil || c.Passed {
		t.Fatalf("ceiling should fail at ATR14%%=4.0; got %+v", c)
	}
	if c := findCheck(checks, "volatility_too_low"); c == nil || !c.Passed {
		t.Fatalf("floor should still pass at ATR14%%=4.0; got %+v", c)
	}
}
