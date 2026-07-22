package market

import "testing"

// obBar builds a kline with explicit OHLC and a default volume.
func obBar(o, h, l, c float64) Kline {
	return Kline{Open: o, High: h, Low: l, Close: c, Volume: 100}
}

// buildBOSSeries (4h, lookback=4) forms a valid swing high at 112, an order
// block (down candle), an impulsive bullish break above 112, then a retest.
func buildBOSSeries() []Kline {
	var ks []Kline
	// 0-5: flat base
	for i := 0; i < 6; i++ {
		ks = append(ks, obBar(100, 101, 99, 100))
	}
	// rise to swing high at idx 9 (high 112)
	ks = append(ks, obBar(101, 103, 100, 102)) // 6
	ks = append(ks, obBar(102, 106, 101, 105)) // 7
	ks = append(ks, obBar(105, 109, 104, 108)) // 8
	ks = append(ks, obBar(108, 112, 107, 110)) // 9  <- swing high peak 112
	// 4 lower bars after (pullback) so idx9 is a valid pivot
	ks = append(ks, obBar(110, 108, 106, 107)) // 10
	ks = append(ks, obBar(107, 106, 103, 104)) // 11
	ks = append(ks, obBar(104, 105, 101, 102)) // 12
	ks = append(ks, obBar(102, 103, 100, 101)) // 13
	// order block: last down candle before the impulse
	ks = append(ks, obBar(103, 104, 99, 100)) // 14 down candle (demand block)
	// impulsive bullish break above 112
	ks = append(ks, obBar(100, 120, 100, 118)) // 15 close 118 >> 112
	ks = append(ks, obBar(118, 122, 117, 121)) // 16
	// retest back to ~112
	ks = append(ks, obBar(121, 122, 112, 114)) // 17 dips into retest band
	ks = append(ks, obBar(114, 118, 113, 117)) // 18
	// tail
	for i := 0; i < 8; i++ {
		ks = append(ks, obBar(117, 119, 116, 118))
	}
	return ks
}

func TestDetectStructureBreaks_BullishBOS(t *testing.T) {
	ks := buildBOSSeries()
	atr := 2.0
	breaks, blocks := DetectStructureBreaks(ks, atr, ks[len(ks)-1].Close, "4h")
	if len(breaks) == 0 {
		t.Fatalf("expected at least one structure break, got 0")
	}
	foundBull := false
	for _, b := range breaks {
		if b.Direction == "bullish" {
			foundBull = true
			if b.BreakLevel < 108 || b.BreakLevel > 114 {
				t.Errorf("bullish break level %.2f not near swing high 112", b.BreakLevel)
			}
			if b.RetestHigh <= b.RetestLow {
				t.Errorf("retest band inverted: [%.2f,%.2f]", b.RetestLow, b.RetestHigh)
			}
			if !b.Retested {
				t.Errorf("expected retest to be detected for %+v", b)
			}
		}
	}
	if !foundBull {
		t.Errorf("expected a bullish break in %+v", breaks)
	}
	foundDemand := false
	for _, ob := range blocks {
		if ob.Direction == "demand" {
			foundDemand = true
			if ob.High <= ob.Low {
				t.Errorf("order block inverted: [%.2f,%.2f]", ob.Low, ob.High)
			}
		}
	}
	if !foundDemand {
		t.Errorf("expected a demand order block in %+v", blocks)
	}
}

func TestDetectStructureBreaks_Adversary(t *testing.T) {
	atr := 2.0
	if b, o := DetectStructureBreaks(nil, atr, 100, "4h"); b != nil || o != nil {
		t.Errorf("nil klines should yield nil,nil")
	}
	few := make([]Kline, 5)
	if b, o := DetectStructureBreaks(few, atr, 100, "4h"); b != nil || o != nil {
		t.Errorf("too-few klines should yield nil,nil")
	}
	ks := buildBOSSeries()
	if b, o := DetectStructureBreaks(ks, 0, 100, "4h"); b != nil || o != nil {
		t.Errorf("zero atr should yield nil,nil")
	}
	flat := make([]Kline, 40)
	for i := range flat {
		flat[i] = obBar(100, 100.1, 99.9, 100)
	}
	b, _ := DetectStructureBreaks(flat, atr, 100, "4h")
	if len(b) != 0 {
		t.Errorf("flat series should yield no breaks, got %d", len(b))
	}
}

// buildCHOCHSeries: bullish break first (sets bull trend), then a valid swing
// low (kept well clear of the impulse candle's low) that is broken downward ->
// CHOCH.
func buildCHOCHSeries() []Kline {
	var ks []Kline
	for i := 0; i < 6; i++ {
		ks = append(ks, obBar(100, 101, 99, 100))
	}
	// swing high at idx 9 (112)
	ks = append(ks, obBar(101, 103, 100, 102)) // 6
	ks = append(ks, obBar(102, 106, 101, 105)) // 7
	ks = append(ks, obBar(105, 109, 104, 108)) // 8
	ks = append(ks, obBar(108, 112, 107, 110)) // 9 swing high
	ks = append(ks, obBar(110, 108, 106, 107)) // 10
	ks = append(ks, obBar(107, 106, 103, 104)) // 11
	ks = append(ks, obBar(104, 105, 101, 102)) // 12
	ks = append(ks, obBar(102, 103, 100, 101)) // 13
	// bullish BOS above 112 (trend -> bull)
	ks = append(ks, obBar(101, 120, 100, 118)) // 14
	// elevated bars with high lows so the coming swing low is a valid pivot
	ks = append(ks, obBar(118, 121, 117, 120)) // 15 low117
	ks = append(ks, obBar(120, 122, 118, 121)) // 16 low118
	ks = append(ks, obBar(121, 123, 119, 122)) // 17 low119
	ks = append(ks, obBar(122, 124, 120, 123)) // 18 low120
	ks = append(ks, obBar(123, 124, 118, 120)) // 19 low118
	// swing low at idx 20 (low 114); bars 16-19 lows all >114
	ks = append(ks, obBar(120, 121, 114, 116)) // 20 swing low 114
	ks = append(ks, obBar(116, 119, 115, 118)) // 21 low115
	ks = append(ks, obBar(118, 121, 116, 120)) // 22 low116
	ks = append(ks, obBar(120, 122, 117, 121)) // 23 low117
	ks = append(ks, obBar(121, 122, 118, 119)) // 24 low118
	// bearish break below swing low 114 -> CHOCH (against bull trend)
	ks = append(ks, obBar(119, 120, 108, 109)) // 25 close 109 << 114
	for i := 0; i < 8; i++ {
		ks = append(ks, obBar(109, 110, 107, 108))
	}
	return ks
}

func TestDetectStructureBreaks_CHOCH(t *testing.T) {
	ks := buildCHOCHSeries()
	atr := 1.5
	breaks, _ := DetectStructureBreaks(ks, atr, ks[len(ks)-1].Close, "4h")
	hasChoch := false
	for _, b := range breaks {
		if b.Type == "CHOCH" {
			hasChoch = true
		}
	}
	if !hasChoch {
		t.Logf("breaks: %+v", breaks)
		t.Errorf("expected a CHOCH after trend reversal")
	}
}
