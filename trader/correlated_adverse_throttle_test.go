package trader

import (
	"testing"

	"nofx/kernel"
	"nofx/market"
	"nofx/store"
)

// mkThrottleInput builds a market-state gate input with a trend-aligned open (so
// the ONLY thing that can block is the throttle) plus recent-close stats.
func mkThrottleInput(rc *store.RecentCloseStats, throttle *bool) entryGateInput {
	sc := &store.StrategyConfig{}
	sc.Protection.RegimeFilter = store.RegimeFilterConfig{Enabled: true, RequireTrendAlignment: true}
	sc.EntryStructure.EntryGate.CorrelatedAdverseThrottle = throttle
	// LONG in trending_up is trend-aligned → passes the regime checks.
	return entryGateInput{
		Decision:         &kernel.Decision{Action: "open_long", TriggerType: "support_rejection_confirmed"},
		MarketData:       trendingUpData(),
		StrategyConfig:   sc,
		RecentCloseStats: rc,
	}
}

// optIn is an explicit true — the throttle is OFF by default (opt-in) because the
// multi-year proxy backtest did not confirm it generalises, so tests that exercise
// the firing logic must enable it explicitly.
var optIn = true

func TestThrottleDefaultOff(t *testing.T) {
	// Unset (nil) throttle + toxic regime → must NOT emit the check (default off).
	rc := &store.RecentCloseStats{Count: 5, Losses: 5, LossRate: 1.0}
	checks := evaluateMarketStateGate(mkThrottleInput(rc, nil))
	if c := findCheck(checks, "correlated_adverse_throttle"); c != nil {
		t.Errorf("throttle default is OFF → unset must not emit the check, got %+v", c)
	}
}

func TestThrottleBlocksToxicRegime(t *testing.T) {
	// opt-in + 5 closes, 4 losses (80% >= 60%) with >=3 min closes → block.
	rc := &store.RecentCloseStats{Count: 5, Losses: 4, LossRate: 0.8}
	checks := evaluateMarketStateGate(mkThrottleInput(rc, &optIn))
	c := findCheck(checks, "correlated_adverse_throttle")
	if c == nil {
		t.Fatal("expected correlated_adverse_throttle check to be present")
	}
	if c.Passed {
		t.Errorf("expected throttle to block toxic regime (80%% losses), got Passed=true: %s", c.Detail)
	}
	if !c.Enforced {
		t.Errorf("throttle must be enforced (hard block)")
	}
}

func TestThrottleAllowsHealthyRegime(t *testing.T) {
	// opt-in + 5 closes, 2 losses (40% < 60%) → allow.
	rc := &store.RecentCloseStats{Count: 5, Losses: 2, LossRate: 0.4}
	checks := evaluateMarketStateGate(mkThrottleInput(rc, &optIn))
	c := findCheck(checks, "correlated_adverse_throttle")
	if c == nil {
		t.Fatal("expected throttle check present")
	}
	if !c.Passed {
		t.Errorf("expected throttle to allow healthy regime (40%% losses), got blocked: %s", c.Detail)
	}
}

func TestThrottleRespectsMinCloses(t *testing.T) {
	// opt-in + 2 closes both losses (100%) but below min-closes=3 → must NOT block.
	rc := &store.RecentCloseStats{Count: 2, Losses: 2, LossRate: 1.0}
	checks := evaluateMarketStateGate(mkThrottleInput(rc, &optIn))
	c := findCheck(checks, "correlated_adverse_throttle")
	if c == nil {
		t.Fatal("expected throttle check present")
	}
	if !c.Passed {
		t.Errorf("expected throttle to allow when below min-closes sample, got blocked: %s", c.Detail)
	}
}

func TestThrottleDisabledExplicitly(t *testing.T) {
	// Toxic regime but throttle explicitly disabled → no check emitted.
	no := false
	rc := &store.RecentCloseStats{Count: 5, Losses: 5, LossRate: 1.0}
	checks := evaluateMarketStateGate(mkThrottleInput(rc, &no))
	if c := findCheck(checks, "correlated_adverse_throttle"); c != nil {
		t.Errorf("throttle disabled → check must be absent, got %+v", c)
	}
}

func TestThrottleAbsentWhenNoStats(t *testing.T) {
	// opt-in but no RecentCloseStats plumbed → no throttle check (fail-open).
	checks := evaluateMarketStateGate(mkThrottleInput(nil, &optIn))
	if c := findCheck(checks, "correlated_adverse_throttle"); c != nil {
		t.Errorf("no stats → throttle check must be absent, got %+v", c)
	}
}

func TestThrottleBoundaryAtThreshold(t *testing.T) {
	// opt-in + exactly 60% (3/5) meets the >= threshold → block.
	rc := &store.RecentCloseStats{Count: 5, Losses: 3, LossRate: 0.6}
	checks := evaluateMarketStateGate(mkThrottleInput(rc, &optIn))
	c := findCheck(checks, "correlated_adverse_throttle")
	if c == nil {
		t.Fatal("expected throttle check present")
	}
	if c.Passed {
		t.Errorf("loss_rate exactly at threshold (0.60) must block, got Passed=true: %s", c.Detail)
	}
}

var _ = market.Data{}
