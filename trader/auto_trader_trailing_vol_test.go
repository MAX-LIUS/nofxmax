package trader

import (
	"math"
	"testing"

	"nofx/market"
)

func TestShouldTrailingTPClose(t *testing.T) {
	cases := []struct {
		name                                   string
		cur, peak, activate, giveback, minLock float64
		wantTrigger, wantArmed                 bool
	}{
		{"never armed below activate", 1.5, 1.8, 3.0, 35, 0.5, false, false},
		{"armed but small giveback", 4.5, 5.0, 3.0, 35, 0.5, false, true},      // 10% giveback < 35
		{"armed and gives back enough", 3.0, 5.0, 3.0, 35, 0.5, true, true},    // 40% giveback >= 35
		{"giveback exactly at threshold", 3.25, 5.0, 3.0, 35, 0.5, true, true}, // 35% exactly
		{"locked profit below min lock", 0.3, 5.0, 3.0, 90, 0.5, false, true},  // 94% giveback but locked 0.3 < 0.5
		{"disabled activate", 3.0, 5.0, 0, 35, 0.5, false, false},
		{"disabled giveback", 3.0, 5.0, 3.0, 0, 0.5, false, false},
		{"peak at activate, giveback below thresh", 0.5, 3.0, 3.0, 90, 0.4, false, true}, // 83% < 90
		{"peak at activate, giveback meets thresh", 0.2, 3.0, 3.0, 90, 0.1, true, true},  // 93% >= 90
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			trigger, armed, _ := shouldTrailingTPClose(c.cur, c.peak, c.activate, c.giveback, c.minLock)
			if trigger != c.wantTrigger {
				t.Errorf("trigger=%v want %v", trigger, c.wantTrigger)
			}
			if armed != c.wantArmed {
				t.Errorf("armed=%v want %v", armed, c.wantArmed)
			}
		})
	}
}

func TestVolatilitySizeMultiplier(t *testing.T) {
	cases := []struct {
		name                             string
		atrPct, target, minM, maxM, want float64
	}{
		{"at target = 1x", 1.5, 1.5, 0.4, 1.5, 1.0},
		{"2x volatile halves", 3.0, 1.5, 0.4, 1.5, 0.5},
		{"very volatile clamps to floor", 10.0, 1.5, 0.4, 1.5, 0.4},
		{"calm clamps to cap", 0.5, 1.5, 0.4, 1.5, 1.5},
		{"disabled target", 3.0, 0, 0.4, 1.5, 1.0},
		{"bad atr", 0, 1.5, 0.4, 1.5, 1.0},
		{"default clamps when zero", 3.0, 1.5, 0, 0, 0.5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := volatilitySizeMultiplier(c.atrPct, c.target, c.minM, c.maxM)
			if math.Abs(got-c.want) > 1e-9 {
				t.Errorf("mult=%.4f want %.4f", got, c.want)
			}
		})
	}
}

func TestExtractPrimaryATR14(t *testing.T) {
	d := &market.Data{
		TimeframeData: map[string]*market.TimeframeSeriesData{
			"15m": {ATR14: 12.5},
			"1h":  {ATR14: 30.0},
		},
		IntradaySeries: &market.IntradayData{ATR14: 5.0},
	}
	if got := extractPrimaryATR14(d, "1h"); got != 30.0 {
		t.Errorf("primary tf atr=%.2f want 30.0", got)
	}
	// missing primary -> any tf (nonzero)
	got := extractPrimaryATR14(d, "4h")
	if got != 12.5 && got != 30.0 {
		t.Errorf("fallback tf atr=%.2f want 12.5 or 30.0", got)
	}
	// no timeframe data -> intraday
	d2 := &market.Data{IntradaySeries: &market.IntradayData{ATR14: 7.0}}
	if got := extractPrimaryATR14(d2, "1h"); got != 7.0 {
		t.Errorf("intraday atr=%.2f want 7.0", got)
	}
	if got := extractPrimaryATR14(nil, "1h"); got != 0 {
		t.Errorf("nil data atr=%.2f want 0", got)
	}
}

func TestMakerEntryPrice(t *testing.T) {
	bids := [][]float64{{100.0, 5}, {99.9, 3}, {99.8, 2}}
	asks := [][]float64{{100.2, 5}, {100.3, 3}, {100.4, 2}}
	// long sits at best bid
	if px := makerEntryPrice("long", bids, asks, 0); px != 100.0 {
		t.Errorf("long touch px=%.4f want 100.0", px)
	}
	// short sits at best ask
	if px := makerEntryPrice("short", bids, asks, 0); px != 100.2 {
		t.Errorf("short touch px=%.4f want 100.2", px)
	}
	// long with 1 tick offset below (tick derived ~0.1)
	px := makerEntryPrice("long", bids, asks, 1)
	if math.Abs(px-99.9) > 1e-6 {
		t.Errorf("long offset px=%.4f want ~99.9", px)
	}
	// short with 1 tick offset above
	px = makerEntryPrice("short", bids, asks, 1)
	if math.Abs(px-100.3) > 1e-6 {
		t.Errorf("short offset px=%.4f want ~100.3", px)
	}
	// empty book -> 0
	if px := makerEntryPrice("long", nil, asks, 0); px != 0 {
		t.Errorf("empty bids px=%.4f want 0", px)
	}
}

func TestDeriveTickFromBook(t *testing.T) {
	bids := [][]float64{{100.0, 5}, {99.95, 3}}
	asks := [][]float64{{100.05, 5}, {100.10, 3}}
	tick := deriveTickFromBook(bids, asks)
	if math.Abs(tick-0.05) > 1e-6 {
		t.Errorf("tick=%.4f want 0.05", tick)
	}
}

func TestMakerPartialResult(t *testing.T) {
	// No fill: must report not-filled so caller knows nothing executed.
	if _, filled := makerPartialResult("o1", "BTCUSDT", map[string]interface{}{
		"status": "CANCELED", "executedQty": 0.0,
	}); filled {
		t.Errorf("zero executedQty should return filled=false")
	}

	// Partial fill: must report filled=true (terminal) so the caller does NOT
	// place a full market top-up that would oversize the position.
	res, filled := makerPartialResult("o2", "BTCUSDT", map[string]interface{}{
		"status": "CANCELED", "executedQty": 0.003, "avgPrice": 65000.0,
	})
	if !filled {
		t.Fatalf("partial executedQty should return filled=true")
	}
	if res["executedQty"].(float64) != 0.003 {
		t.Errorf("executedQty=%v want 0.003", res["executedQty"])
	}
	if res["status"].(string) != "FILLED" {
		t.Errorf("status=%v want FILLED", res["status"])
	}
	if res["avgPrice"].(float64) != 65000.0 {
		t.Errorf("avgPrice=%v want 65000", res["avgPrice"])
	}

	// int64 executedQty (defensive: some paths may type it differently).
	if _, filled := makerPartialResult("o3", "ETHUSDT", map[string]interface{}{
		"executedQty": int64(2),
	}); !filled {
		t.Errorf("int64 positive executedQty should return filled=true")
	}
}
