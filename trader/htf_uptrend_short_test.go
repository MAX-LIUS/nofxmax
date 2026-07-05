package trader

import (
	"nofx/market"
	"nofx/store"
	"testing"
)

// Symmetric test: higherTimeframeUptrendBlocksShort blocks shorts into confirmed 1h+4h uptrend.
func TestHigherTimeframeUptrendBlocksShort(t *testing.T) {
	d := &market.Data{
		TimeframeData: map[string]*market.TimeframeSeriesData{
			"1h": {
				EMA20Values: []float64{98},
				EMA50Values: []float64{95},
				Klines:      []market.KlineBar{{Close: 100}},
			},
			"4h": {
				EMA20Values: []float64{97},
				EMA50Values: []float64{93},
				Klines:      []market.KlineBar{{Close: 99}},
			},
		},
	}
	if !higherTimeframeUptrendBlocksShort("open_short", d) {
		t.Fatal("Both 1h+4h uptrend should block short")
	}
}

// Symmetric test: 1h uptrend but 4h downtrend (bear-bounce) allows short.
func TestBearBounceShortAllowed(t *testing.T) {
	d := &market.Data{
		TimeframeData: map[string]*market.TimeframeSeriesData{
			"1h": {
				EMA20Values: []float64{98},
				EMA50Values: []float64{95},
				Klines:      []market.KlineBar{{Close: 100}},
			},
			"4h": {
				EMA20Values: []float64{92},
				EMA50Values: []float64{95},
				Klines:      []market.KlineBar{{Close: 90}},
			},
		},
	}
	if higherTimeframeUptrendBlocksShort("open_short", d) {
		t.Fatal("1h up + 4h down (bear-bounce) should allow short")
	}
}

// Symmetric test: only affects shorts, not longs.
func TestUptrendBlockOnlyAffectsShorts(t *testing.T) {
	d := &market.Data{
		TimeframeData: map[string]*market.TimeframeSeriesData{
			"1h": {
				EMA20Values: []float64{98},
				EMA50Values: []float64{95},
				Klines:      []market.KlineBar{{Close: 100}},
			},
			"4h": {
				EMA20Values: []float64{97},
				EMA50Values: []float64{93},
				Klines:      []market.KlineBar{{Close: 99}},
			},
		},
	}
	if higherTimeframeUptrendBlocksShort("open_long", d) {
		t.Fatal("Uptrend block must not affect open_long")
	}
}

// End-to-end: short into confirmed 1h+4h uptrend fails alignment.
func TestIsTrendAlignedBlocksShortInHTFUptrend(t *testing.T) {
	d := &market.Data{
		TimeframeData: map[string]*market.TimeframeSeriesData{
			"1h": {
				EMA20Values: []float64{98},
				EMA50Values: []float64{95},
				Klines:      []market.KlineBar{{Close: 100}},
			},
			"4h": {
				EMA20Values: []float64{97},
				EMA50Values: []float64{93},
				Klines:      []market.KlineBar{{Close: 99}},
			},
		},
	}
	if isTrendAlignedWithMode("open_short", "resistance_breakout", d, store.RegimeTrendAlignmentStrict, false, true) {
		t.Fatal("isTrendAlignedWithMode must reject open_short into 1h+4h uptrend")
	}
}
