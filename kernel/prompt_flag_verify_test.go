package kernel

import (
	"strings"
	"testing"

	"nofx/market"
	"nofx/store"
)

func mkEngine(soft, blockBR bool) *StrategyEngine {
	cfg := &store.StrategyConfig{}
	cfg.EntryStructure.EntryGate.SoftRegimeStructureFit = &soft
	cfg.EntryStructure.EntryGate.BlockBreakoutRetest = &blockBR
	return &StrategyEngine{config: cfg}
}

func mkData() *market.Data {
	return &market.Data{
		Symbol: "BTCUSDT", CurrentPrice: 100, PriceChange1h: 1, PriceChange4h: 2,
		TimeframeData: map[string]*market.TimeframeSeriesData{
			"15m": {Timeframe: "15m",
				Klines:           []market.KlineBar{{Time: 1, Open: 99, High: 101, Low: 98, Close: 100}},
				StructuralLevels: []market.StructuralLevel{{Price: 98, Type: "support", Timeframe: "15m", Strength: 3, Source: "swing_point"}}},
		},
		StructuralLevels: []market.StructuralLevel{{Price: 98, Type: "support", Timeframe: "15m", Strength: 3, Source: "swing_point"}},
	}
}

func TestPromptSoftRegime_On(t *testing.T) {
	out := mkEngine(true, true).formatMarketContextV2("BTCUSDT", mkData())
	if strings.Contains(out, "otherwise wait.") {
		t.Errorf("soft mode must NOT use hard 'otherwise wait' rule:\n%s", out)
	}
	if !strings.Contains(out, "reduces its position size") {
		t.Errorf("soft mode must explain size reduction:\n%s", out)
	}
}

func TestPromptSoftRegime_Off(t *testing.T) {
	out := mkEngine(false, true).formatMarketContextV2("BTCUSDT", mkData())
	if !strings.Contains(out, "otherwise wait.") {
		t.Errorf("hard mode must keep 'otherwise wait' rule:\n%s", out)
	}
}
