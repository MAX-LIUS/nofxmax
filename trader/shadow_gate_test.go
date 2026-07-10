package trader

import (
	"testing"

	"nofx/kernel"
	"nofx/market"
)

// buildBars makes a synthetic kline series with a controllable linear drift so we
// can force TREND_UP / TREND_DN and verify counter-trend rules fire correctly.
func buildBars(n int, start, driftPerBar float64) []market.KlineBar {
	bars := make([]market.KlineBar, n)
	p := start
	for i := 0; i < n; i++ {
		o := p
		p += driftPerBar
		hi := o
		lo := p
		if p > o {
			hi, lo = p, o
		}
		bars[i] = market.KlineBar{Open: o, High: hi + 1, Low: lo - 1, Close: p}
	}
	return bars
}

func mdWithBars(tf string, bars []market.KlineBar) *market.Data {
	return &market.Data{
		TimeframeData: map[string]*market.TimeframeSeriesData{
			tf: {Timeframe: tf, Klines: bars},
		},
	}
}


func TestShadowGates_CounterTrendFires(t *testing.T) {
	// Strong uptrend: shorting should be flagged counter-trend.
	up := mdWithBars("1h", buildBars(120, 100, 0.5))
	d := &kernel.Decision{Symbol: "BTCUSDT", Action: "open_short", Confidence: 70}
	vs := evaluateShadowGates("t1", 5, d, up, "1h", "okx", true)
	if len(vs) != len(shadowRules) {
		t.Fatalf("expected %d verdicts, got %d", len(shadowRules), len(vs))
	}
	got := map[string]bool{}
	for _, v := range vs {
		got[v.RuleName] = v.WouldBlock
		if v.Cycle != 5 || v.Symbol != "BTCUSDT" || v.Side != "SHORT" {
			t.Errorf("verdict metadata wrong: %+v", v)
		}
	}
	if !got["countertrend_slope50"] {
		t.Errorf("countertrend_slope50 should block a SHORT in an uptrend")
	}
	if !got["uptrend_short_only_s50"] {
		t.Errorf("uptrend_short_only_s50 should block a SHORT in an uptrend")
	}
	if got["downtrend_long_only_s50"] {
		t.Errorf("downtrend_long_only should NOT block a SHORT in an uptrend")
	}
}

func TestShadowGates_DowntrendLongFires(t *testing.T) {
	dn := mdWithBars("1h", buildBars(120, 200, -0.5))
	d := &kernel.Decision{Symbol: "ETHUSDT", Action: "open_long", Confidence: 60}
	vs := evaluateShadowGates("t1", 9, d, dn, "1h", "okx", true)
	got := map[string]bool{}
	for _, v := range vs {
		got[v.RuleName] = v.WouldBlock
	}
	if !got["downtrend_long_only_s50"] {
		t.Errorf("downtrend_long_only_s50 should block a LONG in a downtrend")
	}
	if !got["countertrend_slope50"] {
		t.Errorf("countertrend_slope50 should block a LONG in a downtrend")
	}
}

func TestShadowGates_InsufficientBars(t *testing.T) {
	short := mdWithBars("1h", buildBars(30, 100, 0.5))
	// empty primaryTF disables the self-fetch fallback, so this stays offline and
	// deterministically exercises the pure <minShadowBars guard.
	d := &kernel.Decision{Symbol: "BTCUSDT", Action: "open_long", Confidence: 70}
	if vs := evaluateShadowGates("t1", 1, d, short, "", "okx", true); vs != nil {
		t.Errorf("expected nil verdicts with <%d bars, got %d", minShadowBars, len(vs))
	}
}

func TestShadowGates_NonOpenActionSkipped(t *testing.T) {
	up := mdWithBars("1h", buildBars(120, 100, 0.5))
	d := &kernel.Decision{Symbol: "BTCUSDT", Action: "hold"}
	// side is empty for hold; evaluateShadowGates still runs but side=="" —
	// the loop-level guard (directionFromAction) prevents the call in prod.
	vs := evaluateShadowGates("t1", 1, d, up, "1h", "okx", true)
	for _, v := range vs {
		if v.Side != "" {
			t.Errorf("hold action should have empty side, got %q", v.Side)
		}
	}
}
