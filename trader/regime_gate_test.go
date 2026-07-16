package trader

import (
	"testing"

	"nofx/store"
)

// synthetic uptrend: strictly rising closes (slope>0), matching highs/lows.
func upCtx(side string) shadowGateCtx {
	n := 80
	h := make([]float64, n)
	l := make([]float64, n)
	c := make([]float64, n)
	for i := 0; i < n; i++ {
		base := 100.0 + float64(i) // steady rise
		c[i] = base
		h[i] = base + 0.5
		l[i] = base - 0.5
	}
	return shadowGateCtx{highs: h, lows: l, closes: c, side: side, conf: 75}
}

func downCtx(side string) shadowGateCtx {
	c := upCtx(side)
	// reverse into a downtrend
	n := len(c.closes)
	for i := 0; i < n; i++ {
		base := 100.0 + float64(n-i)
		c.closes[i] = base
		c.highs[i] = base + 0.5
		c.lows[i] = base - 0.5
	}
	return c
}

func TestEvalRegimeGate_CounterTrend(t *testing.T) {
	g := store.RegimeGateConfig{Category: "counter_trend"}
	g.Params.SlopeWindow = 30

	// LONG into a downtrend => should block
	if block, _ := evalRegimeGate(g, downCtx("LONG")); !block {
		t.Errorf("counter_trend should block LONG in downtrend")
	}
	// LONG into an uptrend => should NOT block
	if block, _ := evalRegimeGate(g, upCtx("LONG")); block {
		t.Errorf("counter_trend should NOT block LONG in uptrend")
	}
	// SHORT into an uptrend => should block
	if block, _ := evalRegimeGate(g, upCtx("SHORT")); !block {
		t.Errorf("counter_trend should block SHORT in uptrend")
	}
}

func TestEvalRegimeGate_TrendDirectionOnly(t *testing.T) {
	g := store.RegimeGateConfig{Category: "trend_direction_only"}
	g.Params.SlopeWindow = 50
	g.Params.BlockSide = "LONG"
	// blocks LONG in downtrend, but leaves SHORT alone
	if block, _ := evalRegimeGate(g, downCtx("LONG")); !block {
		t.Errorf("trend_direction_only(LONG) should block LONG in downtrend")
	}
	if block, _ := evalRegimeGate(g, downCtx("SHORT")); block {
		t.Errorf("trend_direction_only(LONG) should NOT touch SHORT")
	}
}

func TestEvalRegimeGate_UnknownNeverBlocks(t *testing.T) {
	if block, _ := evalRegimeGate(store.RegimeGateConfig{Category: "nope"}, upCtx("LONG")); block {
		t.Errorf("unknown category must never block")
	}
}

func TestEvaluateEnforceRegimeGates_ShadowModeIgnored(t *testing.T) {
	cfg := &store.StrategyConfig{
		RegimeGates: []store.RegimeGateConfig{
			{Category: "counter_trend", Mode: "shadow", Enabled: true}, // shadow => not enforced
		},
	}
	// No enforce-mode gates => returns nil without needing market data.
	if hits := evaluateEnforceRegimeGates(cfg, nil, nil, "15m", "binance"); hits != nil {
		t.Errorf("shadow-mode gates must not enforce, got %v", hits)
	}
}
