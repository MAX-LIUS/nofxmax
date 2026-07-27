package trader

import (
	"math"
	"sort"
	"testing"

	"nofx/store"
)

func TestDrawdownTierExecutionPrice(t *testing.T) {
	const eps = 1e-6
	cases := []struct {
		name       string
		side       string
		activation float64
		peak       float64
		callback   float64
		want       float64
	}{
		// Before activation the earliest possible fill anchors on the activation
		// price itself: 100 activation, 2% giveback -> fills at 98.
		{"long_pre_activation", "long", 100, 0, 0.02, 98},
		{"short_pre_activation", "short", 100, 0, 0.02, 102},
		// After activation the anchor ratchets with the realized peak.
		{"long_peak_above", "long", 100, 110, 0.02, 107.8},
		{"short_peak_below", "short", 100, 90, 0.02, 91.8},
		// A peak on the wrong side of activation must never loosen the stop:
		// the anchor stays at activation.
		{"long_peak_below_ignored", "long", 100, 95, 0.02, 98},
		{"short_peak_above_ignored", "short", 100, 105, 0.02, 102},
		// Degenerate inputs return 0 so callers can skip the row.
		{"no_activation", "long", 0, 110, 0.02, 0},
		{"no_callback", "long", 100, 110, 0, 0},
		// callback >= 1 is clamped to 1 (full giveback) rather than going negative.
		{"callback_clamped", "long", 100, 100, 1.5, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := drawdownTierExecutionPrice(tc.side, tc.activation, tc.peak, tc.callback)
			if math.Abs(got-tc.want) > eps {
				t.Fatalf("execution price = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPeakPriceFromPnLPct(t *testing.T) {
	const eps = 1e-6
	if got := peakPriceFromPnLPct("long", 100, 10); math.Abs(got-110) > eps {
		t.Fatalf("long peak = %v, want 110", got)
	}
	if got := peakPriceFromPnLPct("short", 100, 10); math.Abs(got-90) > eps {
		t.Fatalf("short peak = %v, want 90", got)
	}
	// No peak profit yet -> no peak price (caller falls back to activation).
	if got := peakPriceFromPnLPct("long", 100, 0); got != 0 {
		t.Fatalf("zero peak pnl = %v, want 0", got)
	}
	if got := peakPriceFromPnLPct("long", 0, 10); got != 0 {
		t.Fatalf("zero entry = %v, want 0", got)
	}
}

// callback 的单位换算必须只发生在交易所边界上,且往返无损。早先的 ">1 就当百分数"
// 猜法在 dd% < 1 时静默错 100 倍 —— 这里钉住那个坑。
func TestCallbackUnitRoundTrip(t *testing.T) {
	const eps = 1e-12
	cases := []struct {
		exchange string
		ratio    float64
		wantWire float64
	}{
		{"binance", 0.018, 1.8},
		{"BINANCE", 0.018, 1.8},
		{"bitget", 0.018, 1.8},
		{"okx", 0.018, 0.018},
		{"hyperliquid", 0.018, 0.018},
		{"", 0.018, 0.018},
		// 这一行是老猜法翻车的地方:dd 0.54% 的比率是 0.0054,发到 binance 是 0.54。
		// 前端看到 0.54 <= 1 就当成比率 => 54% 回撤,成交价直接算飞。
		{"binance", 0.0054, 0.54},
	}
	for _, tc := range cases {
		wire := callbackRatioToExchangeUnit(tc.exchange, tc.ratio)
		if math.Abs(wire-tc.wantWire) > eps {
			t.Fatalf("%s: wire = %v, want %v", tc.exchange, wire, tc.wantWire)
		}
		back := callbackExchangeUnitToRatio(tc.exchange, wire)
		if math.Abs(back-tc.ratio) > eps {
			t.Fatalf("%s: round trip = %v, want %v", tc.exchange, back, tc.ratio)
		}
	}
}

// This is the ordering the user reported as wrong: a drawdown tier that
// activates at 3ATR and gives back 1.8ATR executes at 1.2ATR, so it must land
// BETWEEN the 1.1ATR and 1.7ATR ladder rungs — not out at the 3ATR slot where
// sorting by activation price would put it.
func TestDrawdownTierSortsByExecutionNotActivation(t *testing.T) {
	const (
		entry = 100.0
		atr   = 2.0 // 1 ATR = 2 price units = 2% of entry
	)
	ladder11 := entry + 1.1*atr // 102.2
	ladder17 := entry + 1.7*atr // 103.4
	activation := entry + 3.0*atr
	// giveback 1.8 ATR expressed against the activation anchor
	callback := (1.8 * atr) / activation
	exec := drawdownTierExecutionPrice("long", activation, 0, callback)

	wantExec := entry + 1.2*atr // 102.4
	if math.Abs(exec-wantExec) > 1e-6 {
		t.Fatalf("execution price = %v, want %v (entry+1.2ATR)", exec, wantExec)
	}
	// LONG: nearest level first == ascending price for upside targets.
	levels := []float64{activation, ladder11, exec, ladder17}
	sort.Float64s(levels)
	want := []float64{ladder11, exec, ladder17, activation}
	for i := range want {
		if math.Abs(levels[i]-want[i]) > 1e-6 {
			t.Fatalf("order[%d] = %v, want %v (full order %v)", i, levels[i], want[i], levels)
		}
	}
}

func TestSortPlanSnapshotTiers(t *testing.T) {
	p := func(v float64) *float64 { return &v }
	// Deliberately shuffled, and mixing "execution price only" (drawdown) with
	// "trigger price only" (static) rows to prove tierSortPrice falls back.
	tiers := []store.ProtectionPlanTier{
		{Label: "TP2", TriggerPrice: p(103.4)},
		{Label: "DD1", TriggerPrice: p(106.0), ExecutionPrice: p(102.4)},
		{Label: "TP1", TriggerPrice: p(102.2)},
		{Label: "SL", TriggerPrice: p(97.0)},
		{Label: "NoPrice"},
	}
	sortPlanSnapshotTiers(tiers, true)
	gotLong := []string{}
	for _, tr := range tiers {
		gotLong = append(gotLong, tr.Label)
	}
	// LONG: descending price (furthest upside first), unpriced rows last.
	// DD1 sorts on its 102.4 EXECUTION price, so it lands between TP2 (103.4) and
	// TP1 (102.2). Sorting on its 106.0 activation price would have led the list.
	wantLong := []string{"TP2", "DD1", "TP1", "SL", "NoPrice"}
	for i := range wantLong {
		if gotLong[i] != wantLong[i] {
			t.Fatalf("long order = %v, want %v", gotLong, wantLong)
		}
	}

	shortTiers := []store.ProtectionPlanTier{
		{Label: "TP2", TriggerPrice: p(96.6)},
		{Label: "DD1", TriggerPrice: p(94.0), ExecutionPrice: p(97.6)},
		{Label: "TP1", TriggerPrice: p(97.8)},
		{Label: "SL", TriggerPrice: p(103.0)},
	}
	sortPlanSnapshotTiers(shortTiers, false)
	gotShort := []string{}
	for _, tr := range shortTiers {
		gotShort = append(gotShort, tr.Label)
	}
	// SHORT: ascending price (furthest downside first).
	wantShort := []string{"TP2", "DD1", "TP1", "SL"}
	for i := range wantShort {
		if gotShort[i] != wantShort[i] {
			t.Fatalf("short order = %v, want %v", gotShort, wantShort)
		}
	}
}
