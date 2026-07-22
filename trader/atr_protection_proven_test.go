package trader

import (
	"testing"

	"nofx/market"
)

// pBar builds a kline with explicit OHLC (mirrors market.obBar test helper).
func pBar(o, h, l, c float64) market.Kline {
	return market.Kline{Open: o, High: h, Low: l, Close: c, Volume: 100}
}

// buildProvenBOSSeries mirrors the validated market.buildBOSSeries: a swing high
// at idx 9 (112), a demand order block down-candle at idx 14 (Low=99, High=104),
// an impulsive bullish break above 112, then a retest and tail. This is a known-
// good series for DetectStructureBreaks (demand block present).
func buildProvenBOSSeries() []market.Kline {
	var ks []market.Kline
	for i := 0; i < 6; i++ {
		ks = append(ks, pBar(100, 101, 99, 100))
	}
	ks = append(ks, pBar(101, 103, 100, 102)) // 6
	ks = append(ks, pBar(102, 106, 101, 105)) // 7
	ks = append(ks, pBar(105, 109, 104, 108)) // 8
	ks = append(ks, pBar(108, 112, 107, 110)) // 9  swing high 112
	ks = append(ks, pBar(110, 108, 106, 107)) // 10
	ks = append(ks, pBar(107, 106, 103, 104)) // 11
	ks = append(ks, pBar(104, 105, 101, 102)) // 12
	ks = append(ks, pBar(102, 103, 100, 101)) // 13
	ks = append(ks, pBar(103, 104, 99, 100))  // 14 demand block (Low=99, High=104)
	ks = append(ks, pBar(100, 120, 100, 118)) // 15 impulse close 118 >> 112
	ks = append(ks, pBar(118, 122, 117, 121)) // 16
	ks = append(ks, pBar(121, 122, 112, 114)) // 17 retest
	ks = append(ks, pBar(114, 118, 113, 117)) // 18
	for i := 0; i < 8; i++ {
		ks = append(ks, pBar(117, 119, 116, 118))
	}
	return ks
}

// TestNearestProvenBoundary_Long: LONG entry above the demand block returns the
// block High (the tested protective edge below entry).
func TestNearestProvenBoundary_Long(t *testing.T) {
	at := &AutoTrader{exchange: "test"}
	bars := buildProvenBOSSeries()

	entryPrice := 118.0 // above the demand block High (104)
	boundary, ok := at.nearestProvenBoundary(bars, entryPrice, true, "4h")
	if !ok {
		t.Fatal("expected proven boundary from demand order block, got none")
	}
	// Demand block idx 14: High=104. Protective boundary for a LONG is the High.
	if boundary != 104.0 {
		t.Errorf("proven boundary = %.2f, want 104.00 (demand OB High)", boundary)
	}
	if boundary >= entryPrice {
		t.Errorf("proven boundary %.2f must be below LONG entry %.2f", boundary, entryPrice)
	}
}

// TestNearestProvenBoundary_NoBlockOnSide: LONG entry BELOW the demand block
// means the block is not on the protective (below-entry) side, so no proven
// boundary is returned.
func TestNearestProvenBoundary_NoBlockOnSide(t *testing.T) {
	at := &AutoTrader{exchange: "test"}
	bars := buildProvenBOSSeries()

	// Entry at 100 — the demand block High (104) is ABOVE entry, not protective.
	_, ok := at.nearestProvenBoundary(bars, 100.0, true, "4h")
	if ok {
		t.Error("expected no proven boundary when block is above LONG entry")
	}
}

// TestNearestProvenBoundary_TooFewBars: guards the minimum-bar precondition.
func TestNearestProvenBoundary_TooFewBars(t *testing.T) {
	at := &AutoTrader{exchange: "test"}
	few := make([]market.Kline, 10)
	for i := range few {
		few[i] = pBar(100, 101, 99, 100)
	}
	if _, ok := at.nearestProvenBoundary(few, 100, true, "4h"); ok {
		t.Error("expected no boundary with <20 bars")
	}
}
