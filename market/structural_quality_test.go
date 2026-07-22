package market

import "testing"

// mkQualityData builds a Data with a configurable structure richness for tests.
func mkQualityData(price, atr float64) *Data {
	return &Data{
		Symbol:       "TEST-USDT",
		CurrentPrice: price,
		TimeframeData: map[string]*TimeframeSeriesData{
			"1h": {Timeframe: "1h", ATR14: atr},
		},
	}
}

// TestStructuralQuality_HighQuality: clean structure -> A/B grade.
func TestStructuralQuality_HighQuality(t *testing.T) {
	d := mkQualityData(100, 1.0)
	d.StructuralZones = []StructuralZone{
		{Low: 98, High: 98.5, MidPrice: 98.25, Type: "support", Confidence: 85, QualityGrade: "A", MultiTFCount: 3, MaxReactionATR: 3.5, State: "reacted", Role: "reversal"},
		{Low: 102, High: 102.5, MidPrice: 102.25, Type: "resistance", Confidence: 70, QualityGrade: "B", MultiTFCount: 2, MaxReactionATR: 2.0},
	}
	d.StructureBreaks = []StructureBreak{
		{Type: "BOS", Direction: "bullish", BreakLevel: 99, BarsAgo: 5},
		{Type: "BOS", Direction: "bullish", BreakLevel: 97, BarsAgo: 20},
	}
	d.VolumeProfile = &VolumeProfile{POC: 99, VAH: 101, VAL: 98, Timeframe: "1h"}
	d.FairValueGaps = []FairValueGap{{Low: 98.2, High: 98.8, Mid: 98.5, Direction: "bullish", Filled: false}}
	d.LiquidityPools = []LiquidityPool{{Price: 98.3, Type: "equal_lows"}}

	sq := CalculateStructuralQuality(d)
	if sq == nil {
		t.Fatal("expected a quality score, got nil")
	}
	if sq.Grade != "A" && sq.Grade != "B" {
		t.Errorf("clean structure should grade A/B, got %s (score=%.0f, reasons=%v)", sq.Grade, sq.Score, sq.Reasons)
	}
	if sq.Score < 55 {
		t.Errorf("expected score >= 55, got %.0f", sq.Score)
	}
}

// TestStructuralQuality_LowQuality: sparse/messy structure -> C/D grade.
func TestStructuralQuality_LowQuality(t *testing.T) {
	d := mkQualityData(100, 1.0)
	// only a far, low-grade zone; conflicting breaks; no profile
	d.StructuralZones = []StructuralZone{
		{Low: 130, High: 131, MidPrice: 130.5, Type: "resistance", Confidence: 30, QualityGrade: "C"},
	}
	d.StructureBreaks = []StructureBreak{
		{Type: "CHOCH", Direction: "bullish", BreakLevel: 99},
		{Type: "BOS", Direction: "bearish", BreakLevel: 101},
	}
	sq := CalculateStructuralQuality(d)
	if sq == nil {
		t.Fatal("expected a quality score, got nil")
	}
	if sq.Grade == "A" {
		t.Errorf("messy structure should not grade A, got %s (score=%.0f)", sq.Grade, sq.Score)
	}
}

// TestStructuralQuality_Adversary: nil / no ATR / empty.
func TestStructuralQuality_Adversary(t *testing.T) {
	if CalculateStructuralQuality(nil) != nil {
		t.Error("nil data should yield nil")
	}
	// no ATR
	d := &Data{Symbol: "X", CurrentPrice: 100, TimeframeData: map[string]*TimeframeSeriesData{"1h": {ATR14: 0}}}
	if CalculateStructuralQuality(d) != nil {
		t.Error("zero ATR should yield nil")
	}
	// zero price
	d2 := mkQualityData(0, 1.0)
	if CalculateStructuralQuality(d2) != nil {
		t.Error("zero price should yield nil")
	}
	// valid data, empty structure -> still returns a (low) score, not nil
	d3 := mkQualityData(100, 1.0)
	sq := CalculateStructuralQuality(d3)
	if sq == nil {
		t.Fatal("empty-structure valid data should still score (low), got nil")
	}
	if sq.Grade == "A" {
		t.Errorf("empty structure should not grade A, got %s", sq.Grade)
	}
}

// TestQualityGrade boundaries.
func TestQualityGrade(t *testing.T) {
	cases := []struct {
		score float64
		want  string
	}{{80, "A"}, {75, "A"}, {60, "B"}, {55, "B"}, {40, "C"}, {35, "C"}, {20, "D"}, {0, "D"}}
	for _, c := range cases {
		if got := qualityGrade(c.score); got != c.want {
			t.Errorf("qualityGrade(%.0f)=%s want %s", c.score, got, c.want)
		}
	}
}
