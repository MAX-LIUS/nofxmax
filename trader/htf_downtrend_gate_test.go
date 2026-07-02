package trader

import (
	"testing"

	"nofx/market"
	"nofx/store"
)

// build market.Data with a 1h EMA structure. down=true => EMA20<EMA50 & price<EMA50.
func dataWith1hStructure(down bool) *market.Data {
	var ema20, ema50, price float64
	if down {
		ema20, ema50, price = 96.0, 100.0, 95.0 // established 1h downtrend
	} else {
		ema20, ema50, price = 104.0, 100.0, 105.0 // established 1h uptrend
	}
	return &market.Data{
		CurrentPrice: price,
		CurrentEMA20: ema20,
		TimeframeData: map[string]*market.TimeframeSeriesData{
			"1h": {
				EMA20Values: []float64{ema20 - 1, ema20},
				EMA50Values: []float64{ema50, ema50},
				Klines:      []market.KlineBar{{Close: price - 1}, {Close: price}},
			},
		},
	}
}

func TestHTFDowntrendBlocksLong(t *testing.T) {
	if !higherTimeframeDowntrendBlocksLong("open_long", dataWith1hStructure(true)) {
		t.Fatal("open_long into established 1h downtrend must be blocked")
	}
}

func TestHTFDowntrendDoesNotBlockLongInUptrend(t *testing.T) {
	if higherTimeframeDowntrendBlocksLong("open_long", dataWith1hStructure(false)) {
		t.Fatal("open_long in 1h uptrend must not be blocked")
	}
}

// The critical asymmetry: shorts are NEVER blocked by this gate, even into a
// downtrend, and especially not into an uptrend (up×SHORT is +8.9% in backtest).
func TestHTFDowntrendNeverBlocksShort(t *testing.T) {
	if higherTimeframeDowntrendBlocksLong("open_short", dataWith1hStructure(true)) {
		t.Fatal("open_short must never be blocked by the downtrend-long gate")
	}
	if higherTimeframeDowntrendBlocksLong("open_short", dataWith1hStructure(false)) {
		t.Fatal("open_short into uptrend must not be blocked (up×SHORT is profitable)")
	}
}

func TestHTFDowntrendNoDataDoesNotBlock(t *testing.T) {
	// missing 1h context → do not block (avoid false positives)
	if higherTimeframeDowntrendBlocksLong("open_long", &market.Data{CurrentPrice: 100}) {
		t.Fatal("missing 1h timeframe data must not block")
	}
	if higherTimeframeDowntrendBlocksLong("open_long", nil) {
		t.Fatal("nil data must not block")
	}
}

// Partial downtrend (only one condition true) must not trigger — needs BOTH
// EMA20<EMA50 AND price<EMA50.
func TestHTFDowntrendRequiresBothConditions(t *testing.T) {
	// EMA20<EMA50 but price ABOVE EMA50 (early recovery) → not an established downtrend
	d := &market.Data{
		CurrentPrice: 101,
		TimeframeData: map[string]*market.TimeframeSeriesData{
			"1h": {
				EMA20Values: []float64{96},
				EMA50Values: []float64{100},
				Klines:      []market.KlineBar{{Close: 101}}, // price above EMA50
			},
		},
	}
	if higherTimeframeDowntrendBlocksLong("open_long", d) {
		t.Fatal("price above EMA50 should not be treated as established downtrend")
	}
}

// End-to-end through the alignment path: a long into a 1h downtrend fails alignment.
func TestIsTrendAlignedBlocksLongInHTFDowntrend(t *testing.T) {
	if isTrendAlignedWithMode("open_long", "support_rejection", dataWith1hStructure(true), store.RegimeTrendAlignmentStrict, true) {
		t.Fatal("isTrendAlignedWithMode must reject open_long into 1h downtrend")
	}
}
