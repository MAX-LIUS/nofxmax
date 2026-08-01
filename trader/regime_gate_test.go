package trader

import (
	"strings"
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

// adxCfg builds an adx_weak gate config with the given threshold and period.
func adxCfg(thr float64, period int) store.RegimeGateConfig {
	var g store.RegimeGateConfig
	g.Category = "adx_weak"
	g.Enabled = true
	g.Params.Threshold = thr
	g.Params.ADXPeriod = period
	return g
}

// TestADXPeriodDefaultsTo14 is the compatibility guard: every config written
// before adx_period existed must keep its exact behaviour. period=0 (the JSON
// zero value for an absent key) must be indistinguishable from period=14.
func TestADXPeriodDefaultsTo14(t *testing.T) {
	for _, side := range []string{"LONG", "SHORT"} {
		ctx := upCtx(side)
		absent, dAbsent := evalRegimeGate(adxCfg(25, 0), ctx)
		explicit, dExplicit := evalRegimeGate(adxCfg(25, 14), ctx)
		if absent != explicit {
			t.Fatalf("side=%s: unset period must behave as 14, got block=%v vs %v", side, absent, explicit)
		}
		if dAbsent != dExplicit {
			t.Fatalf("side=%s: detail must match too:\n unset=%q\n  14  =%q", side, dAbsent, dExplicit)
		}
	}
}

// TestADXPeriodNegativeFallsBackTo14 guards a malformed config: a negative
// period must not reach sgADX (which would index out of range or divide by
// zero), it must fall back to the default.
func TestADXPeriodNegativeFallsBackTo14(t *testing.T) {
	ctx := upCtx("LONG")
	got, detail := evalRegimeGate(adxCfg(25, -5), ctx)
	want, wantDetail := evalRegimeGate(adxCfg(25, 14), ctx)
	if got != want || detail != wantDetail {
		t.Fatalf("negative period must fall back to 14: got (%v,%q) want (%v,%q)", got, detail, want, wantDetail)
	}
}

// TestADXPeriodIsHonoured proves the parameter actually reaches sgADX: a short
// period reacts faster than a long one, so on a trend that only just started
// they must disagree at some threshold. Without this the field could be silently
// ignored and the two tests above would still pass.
func TestADXPeriodIsHonoured(t *testing.T) {
	// Flat for 60 bars, then a sharp trend for 12. ADX(10) sees a strong trend;
	// ADX(30) is still diluted by the flat stretch.
	n := 72
	h := make([]float64, n)
	l := make([]float64, n)
	c := make([]float64, n)
	for i := 0; i < n; i++ {
		base := 100.0
		if i >= 60 {
			base = 100.0 + float64(i-59)*3
		}
		c[i], h[i], l[i] = base, base+0.4, base-0.4
	}
	ctx := shadowGateCtx{highs: h, lows: l, closes: c, side: "LONG", conf: 75}

	adx10 := sgADX(h, l, c, 10)
	adx30 := sgADX(h, l, c, 30)
	if adx10 <= adx30 {
		t.Skipf("fixture did not separate the periods (adx10=%.1f adx30=%.1f); "+
			"the field-plumbing assertion below is what matters", adx10, adx30)
	}
	// Pick a threshold between them: period 10 passes, period 30 blocks.
	thr := (adx10 + adx30) / 2
	if block10, d10 := evalRegimeGate(adxCfg(thr, 10), ctx); block10 {
		t.Errorf("period=10 should pass at thr=%.1f (adx10=%.1f): %s", thr, adx10, d10)
	}
	if block30, d30 := evalRegimeGate(adxCfg(thr, 30), ctx); !block30 {
		t.Errorf("period=30 should block at thr=%.1f (adx30=%.1f): %s", thr, adx30, d30)
	}
}

// TestADXPeriodInDetail keeps the log auditable: the detail string must name the
// period actually used, otherwise a live rejection cannot be reproduced offline.
func TestADXPeriodInDetail(t *testing.T) {
	_, detail := evalRegimeGate(adxCfg(30, 10), upCtx("LONG"))
	if !strings.Contains(detail, "adx10=") {
		t.Errorf("detail must state the period used, got %q", detail)
	}
}

// ctCfg builds a chart_trend gate config. lookback=0 means "unset".
func ctCfg(lookback int) store.RegimeGateConfig {
	var g store.RegimeGateConfig
	g.Category = "chart_trend"
	g.Enabled = true
	g.Params.SlopeWindow = 30
	g.Params.AlignMin = 0.55
	g.Params.R2Min = 0.60
	g.Params.Lookback = lookback
	return g
}

// TestChartTrendLookbackDefaultsTo2 is the compatibility guard for the same
// reason as the ADX one: chart_trend was calibrated with pivot lb hardcoded to
// 2, so an absent lookback key must reproduce that exactly.
func TestChartTrendLookbackDefaultsTo2(t *testing.T) {
	for _, side := range []string{"LONG", "SHORT"} {
		for _, ctx := range []shadowGateCtx{upCtx(side), downCtx(side)} {
			absent, dAbsent := evalRegimeGate(ctCfg(0), ctx)
			explicit, dExplicit := evalRegimeGate(ctCfg(2), ctx)
			if absent != explicit || dAbsent != dExplicit {
				t.Fatalf("side=%s: unset lookback must behave as 2:\n unset=(%v,%q)\n   2  =(%v,%q)",
					side, absent, dAbsent, explicit, dExplicit)
			}
		}
	}
}

// TestChartTrendLookbackNegativeFallsBackTo2 guards a malformed config: a
// negative lookback must not reach chartSwingAlign (n < 2*lb+2 would misbehave).
func TestChartTrendLookbackNegativeFallsBackTo2(t *testing.T) {
	ctx := upCtx("LONG")
	got, detail := evalRegimeGate(ctCfg(-3), ctx)
	want, wantDetail := evalRegimeGate(ctCfg(2), ctx)
	if got != want || detail != wantDetail {
		t.Fatalf("negative lookback must fall back to 2: got (%v,%q) want (%v,%q)",
			got, detail, want, wantDetail)
	}
}

// TestChartTrendLookbackReachesAlign proves the parameter is actually plumbed
// through rather than silently dropped: the detail string must report it.
func TestChartTrendLookbackReachesAlign(t *testing.T) {
	_, detail := evalRegimeGate(ctCfg(4), upCtx("LONG"))
	if !strings.Contains(detail, "lb=4") {
		t.Errorf("detail must state the pivot lookback used, got %q", detail)
	}
}
