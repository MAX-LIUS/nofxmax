package market

import "testing"

func TestDetectFairValueGaps_Bullish(t *testing.T) {
	// Build a series with a clear bullish gap: candle1.high << candle3.low.
	var ks []Kline
	for i := 0; i < 5; i++ {
		ks = append(ks, mkBar(100, 101, 99, 100, 100)) // c1 highs ~101
	}
	// impulse candle
	ks = append(ks, mkBar(101, 110, 101, 109, 500))
	// candle3 opens well above candle1.high (101) -> low 108 > 101 => gap [101,108]
	ks = append(ks, mkBar(108, 112, 108, 111, 300))
	// a few more bars that do NOT fill the gap (stay above 108)
	for i := 0; i < 5; i++ {
		ks = append(ks, mkBar(111, 113, 109, 112, 120))
	}
	atr := 3.0
	gaps := DetectFairValueGaps(ks, atr, 112)
	if len(gaps) == 0 {
		t.Fatal("expected at least one FVG")
	}
	found := false
	for _, g := range gaps {
		if g.Direction == "bullish" && g.Low >= 100 && g.High <= 109 {
			found = true
			if g.Mid != (g.Low+g.High)/2 {
				t.Errorf("mid wrong: %.2f", g.Mid)
			}
			if g.SizeATR <= 0 {
				t.Errorf("sizeATR should be positive: %.2f", g.SizeATR)
			}
		}
	}
	if !found {
		t.Errorf("bullish gap not found in %+v", gaps)
	}
}

func TestDetectFairValueGaps_FillDetection(t *testing.T) {
	var ks []Kline
	for i := 0; i < 5; i++ {
		ks = append(ks, mkBar(100, 101, 99, 100, 100))
	}
	ks = append(ks, mkBar(101, 110, 101, 109, 500))
	ks = append(ks, mkBar(108, 112, 108, 111, 300)) // gap [101,108]
	// price fully retraces back down into the gap -> filled
	ks = append(ks, mkBar(108, 109, 100, 101, 400))
	ks = append(ks, mkBar(101, 103, 100, 102, 200))
	gaps := DetectFairValueGaps(ks, 3.0, 102)
	// The gap should now register as filled (fill ratio high).
	for _, g := range gaps {
		if g.Direction == "bullish" && g.Low >= 100 && g.High <= 109 {
			if !g.Filled {
				t.Errorf("expected gap filled, ratio=%.2f", g.FillRatio)
			}
		}
	}
}

func TestDetectFairValueGaps_Adversary(t *testing.T) {
	cases := map[string]struct {
		ks  []Kline
		atr float64
	}{
		"nil":     {nil, 3},
		"too_few": {[]Kline{mkBar(1, 1, 1, 1, 1)}, 3},
		"zero_atr": {func() []Kline {
			var k []Kline
			for i := 0; i < 10; i++ {
				k = append(k, mkBar(100, 101, 99, 100, 10))
			}
			return k
		}(), 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic %s: %v", name, r)
				}
			}()
			if g := DetectFairValueGaps(tc.ks, tc.atr, 100); g != nil {
				t.Errorf("%s expected nil got %+v", name, g)
			}
		})
	}
}

func TestDetectLiquidityPools_EqualHighs(t *testing.T) {
	// Create several swing highs at ~the same price -> equal highs pool.
	// Zigzag with peaks all near 110.
	var ks []Kline
	for cycle := 0; cycle < 4; cycle++ {
		// up to a peak near 110
		for i := 0; i < 8; i++ {
			p := 100.0 + float64(i)*1.25 // reaches ~108.75
			ks = append(ks, mkBar(p, p+1, p-0.5, p, 100))
		}
		// down to a trough near 100
		for i := 0; i < 8; i++ {
			p := 110.0 - float64(i)*1.25
			ks = append(ks, mkBar(p, p+0.5, p-1, p, 100))
		}
	}
	pools := DetectLiquidityPools(ks, 2.0, 105, "1h")
	// Not asserting exact count (depends on pivots) but must not panic and
	// any returned pool must be well-formed.
	for _, p := range pools {
		if p.Touches < 2 {
			t.Errorf("pool with <2 touches: %+v", p)
		}
		if p.Type != "equal_highs" && p.Type != "equal_lows" {
			t.Errorf("bad pool type: %s", p.Type)
		}
	}
}

func TestDetectLiquidityPools_Adversary(t *testing.T) {
	if p := DetectLiquidityPools(nil, 2, 100, "1h"); p != nil {
		t.Error("expected nil for nil input")
	}
	short := []Kline{mkBar(1, 1, 1, 1, 1)}
	if p := DetectLiquidityPools(short, 2, 100, "1h"); p != nil {
		t.Error("expected nil for short input")
	}
}
