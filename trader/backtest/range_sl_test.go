package backtest

import (
	"math"
	"testing"

	"nofx/market"
)

// build lb bars before entry with a known range low/high, then an entry bar.
func rangeBars(entryIdx int, lo, hi, entry float64) []market.Kline {
	bars := make([]market.Kline, entryIdx+1)
	for i := 0; i < entryIdx; i++ {
		// alternate touching lo and hi so the window spans [lo,hi].
		l, h := lo, hi
		bars[i] = market.Kline{OpenTime: int64(i) * 60000, Open: (lo + hi) / 2, High: h, Low: l, Close: (lo + hi) / 2}
	}
	bars[entryIdx] = market.Kline{OpenTime: int64(entryIdx) * 60000, Open: entry, High: entry, Low: entry, Close: entry}
	return bars
}

func TestRangeSL_LongUsesRangeLowWithinClamp(t *testing.T) {
	atr := 100.0
	entry := 10000.0
	// range low 9800 => distance 200 = 2.0 ATR, within [1.5,4.5].
	bars := rangeBars(30, 9800, 10050, entry)
	p := ProtectionParams{RangeSLEnabled: true, RangeSLFloorATR: 1.5, RangeSLBackstopATR: 4.5, RangeSLLookback: 24}
	e := Entry{Side: "LONG", EntryPrice: entry}
	got, ok := rangeStructuralSLPrice(p, e, bars, 30, atr, true)
	if !ok {
		t.Fatal("expected ok")
	}
	want := 9800.0 // 2 ATR below, unclamped
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("got %.2f want %.2f", got, want)
	}
}

func TestRangeSL_LongClampsToFloor(t *testing.T) {
	atr := 100.0
	entry := 10000.0
	// range low 9950 => distance 50 = 0.5 ATR, below floor 1.5 => clamp to 1.5 ATR = 9850.
	bars := rangeBars(30, 9950, 10050, entry)
	p := ProtectionParams{RangeSLEnabled: true, RangeSLFloorATR: 1.5, RangeSLBackstopATR: 4.5, RangeSLLookback: 24}
	e := Entry{Side: "LONG", EntryPrice: entry}
	got, ok := rangeStructuralSLPrice(p, e, bars, 30, atr, true)
	if !ok {
		t.Fatal("expected ok")
	}
	want := 9850.0
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("floor clamp: got %.2f want %.2f", got, want)
	}
}

// buildLowBars builds len(lows)+1 bars: one per low (constant high `hi`), then an
// entry bar. Used to exercise nearest-swing pivot selection with controlled troughs.
func buildLowBars(lows []float64, hi, entry float64) []market.Kline {
	bars := make([]market.Kline, len(lows)+1)
	for i, l := range lows {
		bars[i] = market.Kline{OpenTime: int64(i) * 60000, Open: (l + hi) / 2, High: hi, Low: l, Close: (l + hi) / 2}
	}
	n := len(lows)
	bars[n] = market.Kline{OpenTime: int64(n) * 60000, Open: entry, High: entry, Low: entry, Close: entry}
	return bars
}

func TestRangeSL_LongBeyondBackstopUsesFallback(t *testing.T) {
	atr := 100.0
	entry := 10000.0
	// range low 9000 => distance 1000 = 10 ATR, above backstop 4.5. With the
	// nearest-structure fix, beyond-backstop now falls back to FallbackATR (3.0),
	// not the wide backstop: 3.0 ATR = 9700 (was 9550 before the fix).
	bars := rangeBars(30, 9000, 10050, entry)
	p := ProtectionParams{RangeSLEnabled: true, RangeSLFloorATR: 1.5, RangeSLBackstopATR: 4.5, RangeSLFallbackATR: 3.0, RangeSLLookback: 24}
	e := Entry{Side: "LONG", EntryPrice: entry}
	got, ok := rangeStructuralSLPrice(p, e, bars, 30, atr, true)
	if !ok {
		t.Fatal("expected ok")
	}
	want := 9700.0
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("fallback: got %.2f want %.2f", got, want)
	}
}

// TestRangeSL_NearestSwingNotAbsoluteExtreme pins the 2026-07 fix: the boundary
// anchors to the NEAREST swing beyond entry, not the window's deepest spike.
func TestRangeSL_NearestSwingNotAbsoluteExtreme(t *testing.T) {
	atr := 100.0
	entry := 10000.0
	// A deep spike low (9000) far from entry and a nearer swing low (9800, 2 ATR).
	// Non-trough bars are shoulders (a lower neighbor disqualifies them as pivots), so
	// only 9800 and 9000 are swing lows. Nearest-swing must pick 9800 (unclamped,
	// within band), NOT 9000 (which the old absolute-extreme logic would clamp).
	lows := []float64{9990, 9980, 9800, 9980, 9990, 9500, 9000, 9500, 9990, 9985}
	bars := buildLowBars(lows, 10050, entry)
	p := ProtectionParams{RangeSLEnabled: true, RangeSLFloorATR: 1.5, RangeSLBackstopATR: 4.5, RangeSLFallbackATR: 3.0, RangeSLLookback: 24, RangeSLPivotStrength: 1}
	e := Entry{Side: "LONG", EntryPrice: entry}
	got, ok := rangeStructuralSLPrice(p, e, bars, len(lows), atr, true)
	if !ok {
		t.Fatal("expected ok")
	}
	if math.Abs(got-9800.0) > 1e-6 {
		t.Fatalf("nearest-swing: got %.2f want 9800 (not the 9000 spike)", got)
	}
}

// TestRangeSL_FallbackRRCap pins the RR cap on the no-near-structure path: the
// fallback stop must stay below ratio × TP target.
func TestRangeSL_FallbackRRCap(t *testing.T) {
	atr := 100.0 // 1 ATR = 1% of entry
	entry := 10000.0
	// range low 9000 => 10 ATR, beyond backstop => fallback 3.0 ATR, but TP target 3%
	// with ratio 0.8 => cap = 2.4% < 3.0 => stop at 2.4 ATR = 9760.
	bars := rangeBars(30, 9000, 10050, entry)
	p := ProtectionParams{
		RangeSLEnabled: true, RangeSLFloorATR: 1.5, RangeSLBackstopATR: 4.5,
		RangeSLFallbackATR: 3.0, RangeSLFallbackRRCapRatio: 0.8,
		RangeSLLookback: 24,
		TPLegs:          []LadderLeg{{DistPct: 3.0, CloseRatioPct: 100}},
	}
	e := Entry{Side: "LONG", EntryPrice: entry}
	got, ok := rangeStructuralSLPrice(p, e, bars, 30, atr, true)
	if !ok {
		t.Fatal("expected ok")
	}
	if math.Abs(got-9760.0) > 1e-6 {
		t.Fatalf("RR-capped fallback: got %.2f want 9760", got)
	}
}

func TestRangeSL_ShortUsesRangeHigh(t *testing.T) {
	atr := 100.0
	entry := 10000.0
	// range high 10200 => distance 200 = 2 ATR within clamp; short stop ABOVE entry.
	bars := rangeBars(30, 9950, 10200, entry)
	p := ProtectionParams{RangeSLEnabled: true, RangeSLFloorATR: 1.5, RangeSLBackstopATR: 4.5, RangeSLLookback: 24}
	e := Entry{Side: "SHORT", EntryPrice: entry}
	got, ok := rangeStructuralSLPrice(p, e, bars, 30, atr, false)
	if !ok {
		t.Fatal("expected ok")
	}
	want := 10200.0
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("short: got %.2f want %.2f", got, want)
	}
}

func TestRangeSL_NoEdgeFallsBack(t *testing.T) {
	atr := 100.0
	entry := 10000.0
	// long entered AT/below range floor (lo=entry) => no edge => ok=false.
	bars := rangeBars(30, 10000, 10200, entry)
	p := ProtectionParams{RangeSLEnabled: true, RangeSLFloorATR: 1.5, RangeSLBackstopATR: 4.5, RangeSLLookback: 24}
	e := Entry{Side: "LONG", EntryPrice: entry}
	if _, ok := rangeStructuralSLPrice(p, e, bars, 30, atr, true); ok {
		t.Fatal("expected ok=false when entered at range floor")
	}
}
