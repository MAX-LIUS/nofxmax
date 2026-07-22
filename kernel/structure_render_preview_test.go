package kernel

import (
	"strings"
	"testing"

	"nofx/market"
)

// buildFullStructureData returns a Data populated with every Phase-1 structure
// detector output, used to regression-test the AI-facing render.
func buildFullStructureData() *market.Data {
	return &market.Data{
		Symbol:       "BTC-USDT-SWAP",
		CurrentPrice: 100000,
		StructuralZones: []market.StructuralZone{
			{Low: 98000, High: 98500, MidPrice: 98250, Type: "support", Timeframes: []string{"1h", "4h"}, Sources: []string{"swing_point", "volume_cluster"}, TouchCount: 3, Confidence: 78, QualityGrade: "A", State: "reacted", Role: "reversal", MaxReactionATR: 2.3, TestCount: 2},
			{Low: 102000, High: 102600, MidPrice: 102300, Type: "resistance", Timeframes: []string{"1h"}, Sources: []string{"swing_point"}, TouchCount: 2, Confidence: 60, QualityGrade: "B", Flipped: true, State: "flipped", Role: "continuation"},
		},
		VolumeProfile: &market.VolumeProfile{
			POC: 99200, VAH: 101000, VAL: 97500,
			HVNs: []float64{97800, 99200, 100800}, LVNs: []float64{96500, 101800},
			RangeLow: 94000, RangeHigh: 103000, TotalVol: 1e6, Timeframe: "1h", BinCount: 50,
		},
		AnchoredVWAPs: []market.AnchoredVWAP{
			{Anchor: "swing_low", AnchorPrice: 94000, AnchorBars: 60, VWAP: 98800, UpperBand: 100500, LowerBand: 97100, Timeframe: "1h"},
		},
		PeriodLevels: &market.PeriodLevels{
			PrevDayHigh: 101500, PrevDayLow: 97800, PrevWeekHigh: 103000, PrevWeekLow: 92000,
			CurrDayHigh: 100800, CurrDayLow: 99100,
		},
		FairValueGaps: []market.FairValueGap{
			{Low: 99000, High: 99600, Mid: 99300, Direction: "bullish", BarsAgo: 8, Filled: false, FillRatio: 0.2, SizeATR: 0.6},
			{Low: 90000, High: 90500, Mid: 90250, Direction: "bullish", BarsAgo: 100, Filled: true, FillRatio: 1.0, SizeATR: 0.5},
		},
		LiquidityPools: []market.LiquidityPool{
			{Price: 102500, Type: "equal_highs", Touches: 3, BarsAgo: 12, StrengthATR: 0.15},
		},
		StructureBreaks: []market.StructureBreak{
			{Type: "CHOCH", Direction: "bearish", BreakLevel: 99500, BarsAgo: 4, RetestLow: 99275, RetestHigh: 99725, Retested: true, SizeATR: 1.2},
			{Type: "BOS", Direction: "bullish", BreakLevel: 100200, BarsAgo: 20, RetestLow: 99975, RetestHigh: 100425, Retested: false, SizeATR: 2.1},
		},
		OrderBlocks: []market.OrderBlock{
			{Low: 98600, High: 99100, Mid: 98850, Direction: "demand", BarsAgo: 22, Mitigated: false, SizeATR: 2.5},
			{Low: 101200, High: 101700, Mid: 101450, Direction: "supply", BarsAgo: 6, Mitigated: true, SizeATR: 1.8},
		},
		StructuralQuality: &market.StructuralQuality{
			Score: 78, Grade: "A",
			Reasons: []string{"clean nearby S/R zone (high grade/confidence)", "clear bullish structure (aligned BOS)"},
		},
		TimeframeData: map[string]*market.TimeframeSeriesData{
			"1h": {Timeframe: "1h", ATR14: 900},
		},
	}
}

func TestStructureRender_AllSectionsPresent(t *testing.T) {
	out := formatStructuralZones(buildFullStructureData(), true)
	must := []string{
		"关键结构区间",
		"成交量分布", "POC", "HVN", "LVN",
		"锚定 VWAP", "摆动低点",
		"周期关键位", "前日高", "前周高",
		"未回补缺口", "看涨缺口",
		"流动性池", "等高",
		"结构突破", "CHOCH", "BOS", "回踩区",
		"供需区", "需求", "供给",
		"强反应", "反转位", "已翻转", "延续位",
		"结构质量", "选币证据",
	}
	for _, s := range must {
		if !strings.Contains(out, s) {
			t.Errorf("render missing section marker %q", s)
		}
	}
}

func TestStructureRender_FiltersNoise(t *testing.T) {
	out := formatStructuralZones(buildFullStructureData(), true)
	// Prev-week low 92000 is 8.9x ATR away -> must be filtered from period levels.
	if strings.Contains(out, "92000") {
		t.Error("far period level 92000 (8.9x ATR) should be filtered as noise")
	}
	// Filled FVG at 90000 must not be surfaced (only unfilled gaps shown).
	if strings.Contains(out, "90000") || strings.Contains(out, "90500") {
		t.Error("filled FVG should not be rendered")
	}
	// Prev-week high 103000 (3.3x ATR) is within range -> should remain.
	if !strings.Contains(out, "103000") {
		t.Error("prev-week high 103000 (within 8x ATR) should be kept")
	}
}
