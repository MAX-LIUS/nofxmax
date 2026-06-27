package backtest

import (
	"math"
	"testing"

	"nofx/kernel"
	"nofx/market"
)

func TestAnchorNum(t *testing.T) {
	cases := map[string]float64{
		"1h支撑0.7833+ATR缓冲": 0.7833,
		"4h阻力61.98外侧":      61.98,
		"支撑位 64200 下方":     64200,
		"no number here":   0,
	}
	for in, want := range cases {
		got, ok := anchorNum(in)
		if want == 0 {
			if ok {
				t.Errorf("anchorNum(%q): expected no number, got %v", in, got)
			}
			continue
		}
		if !ok || math.Abs(got-want) > 1e-9 {
			t.Errorf("anchorNum(%q)=%v ok=%v; want %v", in, got, ok, want)
		}
	}
}

func TestExtractStructuralPlan_LongBufferedSL(t *testing.T) {
	pp := &kernel.AIProtectionPlan{
		LadderRules: []kernel.AIProtectionLadderRule{
			{StopLossPrice: 0.7795, StructuralAnchor: "1h支撑0.7833+ATR缓冲", TakeProfitPrice: 0.8055, TakeProfitCloseRatioPct: 50},
			{TakeProfitPrice: 0.83, TakeProfitCloseRatioPct: 30},
		},
	}
	sp := extractStructuralPlan("open_long", pp, 0.7883)
	if sp == nil {
		t.Fatal("expected a structural plan")
	}
	if math.Abs(sp.SLPrice-0.7795) > 1e-9 {
		t.Errorf("SLPrice=%v want 0.7795", sp.SLPrice)
	}
	if math.Abs(sp.SLAnchor-0.7833) > 1e-9 {
		t.Errorf("SLAnchor=%v want 0.7833", sp.SLAnchor)
	}
	if len(sp.TPLegs) != 2 {
		t.Fatalf("want 2 TP legs, got %d", len(sp.TPLegs))
	}
}

func TestExtractStructuralPlan_RejectsWrongSideSL(t *testing.T) {
	// Long with SL above entry is degenerate -> rejected.
	pp := &kernel.AIProtectionPlan{StopLossPrice: 1.05}
	if sp := extractStructuralPlan("open_long", pp, 1.0); sp != nil {
		t.Errorf("expected nil for wrong-side SL, got %+v", sp)
	}
}

// flatBars builds a synthetic ascending bar series for replay tests.
func flatBars(n int, base float64) []market.Kline {
	bars := make([]market.Kline, n)
	for i := 0; i < n; i++ {
		bars[i] = market.Kline{
			OpenTime: int64(i) * 3600000,
			Open:     base, High: base, Low: base, Close: base,
		}
	}
	return bars
}

func TestReplayStructural_StopHit(t *testing.T) {
	// Long entry at 100, structural SL at 99 (buffered). Price dips to 98 -> stop.
	bars := flatBars(80, 100)
	for i := 20; i < 80; i++ {
		bars[i].High = 100
		bars[i].Low = 98 // breaches 99 stop
		bars[i].Close = 99.5
	}
	e := Entry{
		Symbol: "X", Side: "LONG", EntryPrice: 100, Quantity: 1,
		EntryTime: int64(20) * 3600000,
		Structural: &StructuralPlan{
			SLPrice: 99,
			TPLegs:  []StructuralTPLeg{{Price: 110, CloseRatioPct: 100}},
		},
	}
	p := ProtectionParams{Unit: UnitStructural}
	res := ReplayEntry(p, e, bars, 20)
	if res.ExitPrice > 99.0001 {
		t.Errorf("expected stop near 99, exit=%v reasons=%v", res.ExitPrice, res.CloseReasons)
	}
}

func TestReplayStructural_TPHit(t *testing.T) {
	// Long entry at 100, TP at 105. Price rises to 106 -> TP fills.
	bars := flatBars(80, 100)
	for i := 20; i < 80; i++ {
		bars[i].High = 106
		bars[i].Low = 100
		bars[i].Close = 105.5
	}
	e := Entry{
		Symbol: "X", Side: "LONG", EntryPrice: 100, Quantity: 1,
		EntryTime: int64(20) * 3600000,
		Structural: &StructuralPlan{
			SLPrice: 97,
			TPLegs:  []StructuralTPLeg{{Price: 105, CloseRatioPct: 100}},
		},
	}
	p := ProtectionParams{Unit: UnitStructural}
	res := ReplayEntry(p, e, bars, 20)
	if res.ExitPrice < 104.9999 {
		t.Errorf("expected TP near 105, exit=%v reasons=%v", res.ExitPrice, res.CloseReasons)
	}
}

func TestResolveStructuralSL_BufferVariant(t *testing.T) {
	// Variant B: anchor 99, k=0.5, ATR=2 -> SL = 99 - 1 = 98 (long).
	e := Entry{Side: "LONG", EntryPrice: 100, Structural: &StructuralPlan{SLAnchor: 99}}
	p := ProtectionParams{Unit: UnitStructural, StructBufferATR: 0.5}
	sl, ok := resolveStructuralSLPrice(p, e, 2.0)
	if !ok || math.Abs(sl-98) > 1e-9 {
		t.Errorf("buffer SL=%v ok=%v; want 98", sl, ok)
	}
}

func TestResolveStructuralSL_Clamp(t *testing.T) {
	// AI SL too tight (0.1%) -> clamped to StructMinSLPct=0.4%.
	e := Entry{Side: "LONG", EntryPrice: 100, Structural: &StructuralPlan{SLPrice: 99.9}}
	p := ProtectionParams{Unit: UnitStructural, StructMinSLPct: 0.4}
	sl, ok := resolveStructuralSLPrice(p, e, 0)
	if !ok || math.Abs(sl-99.6) > 1e-6 {
		t.Errorf("clamped SL=%v ok=%v; want 99.6", sl, ok)
	}
}
