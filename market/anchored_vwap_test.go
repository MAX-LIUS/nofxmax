package market

import "testing"

func TestCalculateAnchoredVWAPs_Basic(t *testing.T) {
	// Zigzag series so findSwingHighs/Lows find interior pivots.
	// Pattern: down to a trough, up to a peak, repeated — with enough bars
	// on both sides of each pivot to satisfy the lookback window.
	// Single clean V then inverted-V: down 10 bars to a trough, up 20 bars to
	// a peak, down 10 bars. Long legs (>lookback=5) with strict monotonicity
	// guarantee one interior swing low and one interior swing high.
	var ks []Kline
	// down to trough at index 9
	for i := 0; i < 10; i++ {
		p := 120.0 - float64(i)*2
		ks = append(ks, mkBar(p, p+0.3, p-0.3, p, 500))
	}
	// up to peak
	for i := 1; i <= 20; i++ {
		p := 102.0 + float64(i)*2
		ks = append(ks, mkBar(p, p+0.3, p-0.3, p, 500))
	}
	// down again so the peak is an interior pivot
	for i := 1; i <= 10; i++ {
		p := 142.0 - float64(i)*2
		ks = append(ks, mkBar(p, p+0.3, p-0.3, p, 500))
	}
	avs := CalculateAnchoredVWAPs(ks, "1h")
	if len(avs) == 0 {
		t.Fatal("expected at least one anchored VWAP")
	}
	for _, v := range avs {
		if v.VWAP <= 0 {
			t.Errorf("%s vwap non-positive: %.2f", v.Anchor, v.VWAP)
		}
		// Bands must bracket VWAP.
		if !(v.LowerBand <= v.VWAP && v.VWAP <= v.UpperBand) {
			t.Errorf("%s bands do not bracket vwap: %.2f/%.2f/%.2f", v.Anchor, v.LowerBand, v.VWAP, v.UpperBand)
		}
		// Anchor bars within series.
		if v.AnchorBars < 0 || v.AnchorBars >= len(ks) {
			t.Errorf("%s anchor bars out of range: %d", v.Anchor, v.AnchorBars)
		}
	}
}

func TestCalculateAnchoredVWAPs_Adversary(t *testing.T) {
	cases := map[string][]Kline{
		"nil":     nil,
		"too_few": {mkBar(1, 1, 1, 1, 1)},
		"zero_vol": func() []Kline {
			var k []Kline
			for i := 0; i < 40; i++ {
				k = append(k, mkBar(100, 101, 99, 100, 0))
			}
			return k
		}(),
	}
	for name, ks := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic on %s: %v", name, r)
				}
			}()
			_ = CalculateAnchoredVWAPs(ks, "1h")
		})
	}
}

func TestAnchoredVWAPFrom_BadIndex(t *testing.T) {
	var ks []Kline
	for i := 0; i < 10; i++ {
		ks = append(ks, mkBar(100, 101, 99, 100, 10))
	}
	if v := anchoredVWAPFrom(ks, -1, "x", "1h"); v != nil {
		t.Error("expected nil for negative index")
	}
	if v := anchoredVWAPFrom(ks, 100, "x", "1h"); v != nil {
		t.Error("expected nil for out-of-range index")
	}
	if v := anchoredVWAPFrom(ks, 9, "x", "1h"); v != nil {
		t.Error("expected nil when too few bars after anchor")
	}
}
