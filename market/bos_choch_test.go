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

// buildOrderVsFormationSeries 构造一个"甩尾/spring"形态,用于验证 BOS/CHOCH 的
// 判定按"突破实际发生时间(breakIdx)"而非"pivot 形成顺序"进行:
//   - 摆动高点在 idx 9 先形成(pivot idx 更小)
//   - 摆动低点在 idx 18 后形成
//   - 但低点的向下跌破(breakIdx=23)先于高点的向上涨破(breakIdx=26)发生
//
// 正确行为:先发生的 bearish 破位 = BOS(trend 0->-1),后发生的 bullish 破位 = CHOCH。
// 旧的按形成序遍历会判反(bullish=BOS、bearish=CHOCH)。
func buildOrderVsFormationSeries() []Kline {
	var ks []Kline
	// 0-5: flat base (low 99, high 101; 非严格极值,不产生 pivot)
	for i := 0; i < 6; i++ {
		ks = append(ks, obBar(100, 101, 99, 100))
	}
	// rise to swing high at idx 9 (high 112)
	ks = append(ks, obBar(101, 103, 100, 102)) // 6
	ks = append(ks, obBar(102, 106, 101, 105)) // 7
	ks = append(ks, obBar(105, 109, 104, 108)) // 8
	ks = append(ks, obBar(108, 112, 107, 110)) // 9  <- swing high 112
	// pullback so idx 9 is a valid pivot (4 lower highs after)
	ks = append(ks, obBar(110, 108, 106, 107)) // 10
	ks = append(ks, obBar(107, 106, 103, 104)) // 11
	ks = append(ks, obBar(104, 105, 101, 102)) // 12
	ks = append(ks, obBar(102, 103, 100, 101)) // 13
	// descend to swing low at idx 18 (low 90)
	ks = append(ks, obBar(101, 102, 98, 99)) // 14 low98
	ks = append(ks, obBar(99, 100, 96, 97))  // 15 low96
	ks = append(ks, obBar(97, 98, 94, 95))   // 16 low94
	ks = append(ks, obBar(95, 96, 92, 93))   // 17 low92
	ks = append(ks, obBar(93, 94, 90, 91))   // 18 <- swing low 90
	// bounce so idx 18 is a valid pivot (4 higher lows after)
	ks = append(ks, obBar(91, 94, 91, 93)) // 19 low91
	ks = append(ks, obBar(93, 95, 92, 94)) // 20 low92
	ks = append(ks, obBar(94, 96, 93, 95)) // 21 low93
	ks = append(ks, obBar(95, 97, 94, 96)) // 22 low94
	// bearish break below 90 first (breakIdx 23, close 87 << 89)
	ks = append(ks, obBar(96, 96, 86, 87)) // 23
	// reverse up and break above 112 later (breakIdx 26, close 114 >> 113)
	ks = append(ks, obBar(87, 100, 86, 99))    // 24
	ks = append(ks, obBar(99, 110, 98, 108))   // 25
	ks = append(ks, obBar(108, 116, 107, 114)) // 26 bullish break
	// flat plateau tail (equal highs/lows -> no new pivots)
	for i := 0; i < 8; i++ {
		ks = append(ks, obBar(114, 116, 113, 115))
	}
	return ks
}

func TestDetectStructureBreaks_ClassifyByBreakTime(t *testing.T) {
	ks := buildOrderVsFormationSeries()
	atr := 2.0
	breaks, _ := DetectStructureBreaks(ks, atr, ks[len(ks)-1].Close, "4h")

	var bull, bear *StructureBreak
	for i := range breaks {
		switch breaks[i].Direction {
		case "bullish":
			bull = &breaks[i]
		case "bearish":
			bear = &breaks[i]
		}
	}
	if bull == nil || bear == nil {
		t.Fatalf("expected both a bullish and a bearish break, got %+v", breaks)
	}
	// 跌破先发生 -> BOS;涨破后发生且逆转 -> CHOCH。
	if bear.Type != "BOS" {
		t.Errorf("earlier (bearish) break should be BOS, got %s in %+v", bear.Type, bear)
	}
	if bull.Type != "CHOCH" {
		t.Errorf("later (bullish) break should be CHOCH, got %s in %+v", bull.Type, bull)
	}
}

func TestDedupBreaks_ATRScaleMerge(t *testing.T) {
	atr := 15.0 // tol = 0.15 * 15 = 2.25
	in := []StructureBreak{
		{Direction: "bullish", BreakLevel: 1911, BarsAgo: 5},
		{Direction: "bullish", BreakLevel: 1909, BarsAgo: 2}, // 距 1911 为 2 < 2.25 -> 重复
		{Direction: "bullish", BreakLevel: 1920, BarsAgo: 8}, // 距 1909 为 11 -> 保留
	}
	out := dedupBreaks(in, atr)
	if len(out) != 2 {
		t.Fatalf("expected 2 breaks after ATR-scale dedup, got %d: %+v", len(out), out)
	}
	// 合并后保留 BarsAgo 最小(最近)的那条;近距离两条里 1909(BarsAgo 2)应胜出。
	kept1909 := false
	kept1920 := false
	for _, b := range out {
		if b.BreakLevel == 1909 {
			kept1909 = true
		}
		if b.BreakLevel == 1920 {
			kept1920 = true
		}
		if b.BreakLevel == 1911 {
			t.Errorf("1911 should have been merged away (BarsAgo not minimal): %+v", out)
		}
	}
	if !kept1909 || !kept1920 {
		t.Errorf("expected 1909 (nearest) and 1920 (far) to remain, got %+v", out)
	}
}

func TestRoundSig(t *testing.T) {
	cases := []struct {
		in, want float64
		n        int
	}{
		{1277.14046987, 1277.14, 6},
		{65696.5851736, 65696.6, 6},
		{0.34158391780, 0.341584, 6},
		{0, 0, 6},
		{123.456, 123.456, 0}, // n<=0 -> unchanged
	}
	for _, c := range cases {
		if got := roundSig(c.in, c.n); got != c.want {
			t.Errorf("roundSig(%v,%d)=%v want %v", c.in, c.n, got, c.want)
		}
	}
}
