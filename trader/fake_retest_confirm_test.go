package trader

import (
	"testing"

	"nofx/market"
	"nofx/store"
)

// bar is a tiny OHLC helper (volume/time irrelevant to confirmation logic).
func bar(o, h, l, c float64) market.KlineBar {
	return market.KlineBar{Open: o, High: h, Low: l, Close: c}
}

func TestFakeRetestConfirmed(t *testing.T) {
	const anchor = 100.0
	tests := []struct {
		name   string
		bars   []market.KlineBar
		isLong bool
		want   bool
	}{
		{
			name: "long touch then hold-close above support → confirmed",
			bars: []market.KlineBar{
				bar(102, 103, 101, 102),
				bar(102, 102, 99.9, 100.5), // touches 100 (Low 99.9), closes above
				bar(100.5, 101.5, 100.4, 101.2),
			},
			isLong: true,
			want:   true,
		},
		{
			name: "long wick-rejection on touch bar → confirmed",
			bars: []market.KlineBar{
				bar(102, 103, 101, 102),
				bar(101, 101.2, 99.0, 101.0), // long lower wick (2.0) vs body 0.0→~0, closes above
			},
			isLong: true,
			want:   true,
		},
		{
			name: "long touch but every later candle closes below anchor → not confirmed",
			bars: []market.KlineBar{
				bar(101, 101, 99.9, 99.5), // touch + close below
				bar(99.5, 99.8, 99.0, 99.2),
				bar(99.2, 99.6, 98.8, 99.1),
			},
			isLong: true,
			want:   false,
		},
		{
			name: "long no touch within lookback → not confirmed",
			bars: []market.KlineBar{
				bar(105, 106, 104, 105),
				bar(105, 106, 104.5, 105.5),
				bar(105.5, 106, 105, 105.8),
			},
			isLong: true,
			want:   false,
		},
		{
			name: "short touch then hold-close below resistance → confirmed",
			bars: []market.KlineBar{
				bar(98, 99, 97, 98),
				bar(98, 100.1, 98, 99.5), // touches 100 (High 100.1), closes below
				bar(99.5, 99.6, 98.5, 99.0),
			},
			isLong: false,
			want:   true,
		},
		{
			name: "short touch but closes back above resistance → not confirmed",
			bars: []market.KlineBar{
				bar(99, 100.1, 98.5, 100.5), // touch + close above
				bar(100.5, 101, 100.2, 100.8),
			},
			isLong: false,
			want:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := fakeRetestConfirmed(tc.bars, anchor, tc.isLong)
			if got != tc.want {
				t.Fatalf("fakeRetestConfirmed=%v want %v", got, tc.want)
			}
		})
	}
}

func TestConfirmationTFBars_PicksOneBelowPrimaryAndDropsForming(t *testing.T) {
	cfg := &store.StrategyConfig{}
	cfg.Indicators.Klines.PrimaryTimeframe = "1h"
	cfg.Indicators.Klines.SelectedTimeframes = []string{"1h", "15m", "4h"}

	data := &market.Data{TimeframeData: map[string]*market.TimeframeSeriesData{
		"1h":  {Timeframe: "1h", Klines: []market.KlineBar{bar(1, 1, 1, 1), bar(2, 2, 2, 2), bar(3, 3, 3, 3), bar(4, 4, 4, 4)}},
		"15m": {Timeframe: "15m", Klines: []market.KlineBar{bar(1, 1, 1, 1), bar(2, 2, 2, 2), bar(3, 3, 3, 3), bar(4, 4, 4, 4), bar(5, 5, 5, 5)}},
		"4h":  {Timeframe: "4h", Klines: []market.KlineBar{bar(1, 1, 1, 1), bar(2, 2, 2, 2), bar(3, 3, 3, 3), bar(4, 4, 4, 4)}},
	}}

	bars, tf := confirmationTFBars(cfg, data)
	if tf != "15m" {
		t.Fatalf("confTF=%q want 15m (one step below 1h primary)", tf)
	}
	// 15m had 5 klines; forming bar dropped → 4.
	if len(bars) != 4 {
		t.Fatalf("len(bars)=%d want 4 (forming bar dropped)", len(bars))
	}
	if bars[len(bars)-1].Close != 4 {
		t.Fatalf("last closed bar close=%v want 4 (bar 5 is forming)", bars[len(bars)-1].Close)
	}
}

func TestConfirmationTFBars_FallbackToPrimaryWhenNoLower(t *testing.T) {
	cfg := &store.StrategyConfig{}
	cfg.Indicators.Klines.PrimaryTimeframe = "15m"
	cfg.Indicators.Klines.SelectedTimeframes = []string{"15m", "1h", "4h"}

	data := &market.Data{TimeframeData: map[string]*market.TimeframeSeriesData{
		"15m": {Timeframe: "15m", Klines: []market.KlineBar{bar(1, 1, 1, 1), bar(2, 2, 2, 2), bar(3, 3, 3, 3), bar(4, 4, 4, 4)}},
	}}

	_, tf := confirmationTFBars(cfg, data)
	if tf != "15m" {
		t.Fatalf("confTF=%q want 15m (no lower TF available → fall back to primary)", tf)
	}
}
