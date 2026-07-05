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

func TestRangeSL_LongClampsToBackstop(t *testing.T) {
	atr := 100.0
	entry := 10000.0
	// range low 9000 => distance 1000 = 10 ATR, above backstop 4.5 => clamp to 4.5 ATR = 9550.
	bars := rangeBars(30, 9000, 10050, entry)
	p := ProtectionParams{RangeSLEnabled: true, RangeSLFloorATR: 1.5, RangeSLBackstopATR: 4.5, RangeSLLookback: 24}
	e := Entry{Side: "LONG", EntryPrice: entry}
	got, ok := rangeStructuralSLPrice(p, e, bars, 30, atr, true)
	if !ok {
		t.Fatal("expected ok")
	}
	want := 9550.0
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("backstop clamp: got %.2f want %.2f", got, want)
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
