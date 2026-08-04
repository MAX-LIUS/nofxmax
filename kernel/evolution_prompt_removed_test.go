package kernel

import (
	"strings"
	"testing"

	"nofx/market"
	"nofx/store"
)

// TestPromptCarriesNoHistoricalProfile pins the removal of the coin evolution
// profile engine (2026-08-04) at the level that actually mattered: the prompt.
//
// The engine used to append one line per symbol in TWO places —
//
//	candidate block: "Historical Performance Profile: LONG: 适配度 76/100 ..."
//	position  block: "Historical Performance: SHORT: 适配度 77/100 ..."
//
// so the model was told, every cycle, how this coin had treated this trader
// before. That is self-reinforcing: the profile is computed FROM the trader's
// own past fills, so a run of luck hardens into a "technical tendency" the model
// then defers to instead of reading the current structure.
//
// Context.EvolutionContexts is gone, so re-adding the old injection cannot
// compile — but a reimplementation could add a new field. This test fails on the
// rendered STRING, which any such attempt trips.
//
// IMPORTANT for whoever edits this: both injection sites sit INSIDE loops that
// `continue` unless the symbol has a MarketDataMap entry, and the candidate loop
// additionally skips any symbol that is already a position. A fixture missing
// either condition renders neither site and the test passes vacuously — that
// happened on the first draft and was caught only by re-adding the injection and
// watching the test stay green. Keep the negative control in mind.
func TestPromptCarriesNoHistoricalProfile(t *testing.T) {
	for _, lang := range []string{"zh", "en"} {
		t.Run(lang, func(t *testing.T) { assertNoProfile(t, lang) })
	}
}

func evoTestData(symbol string, price float64) *market.Data {
	return &market.Data{
		Symbol: symbol, CurrentPrice: price,
		PriceChange1h: 1, PriceChange4h: 2,
		TimeframeData: map[string]*market.TimeframeSeriesData{
			"1h": {Timeframe: "1h", Klines: []market.KlineBar{
				{Time: 1, Open: price, High: price * 1.01, Low: price * 0.99, Close: price},
			}},
		},
	}
}

func assertNoProfile(t *testing.T, lang string) {
	t.Helper()
	cfg := &store.StrategyConfig{}
	cfg.Language = lang
	e := &StrategyEngine{config: cfg}

	// Candidate symbol MUST differ from the position symbol, else the candidate
	// loop skips it and that injection site is never reached.
	ctx := &Context{
		CurrentTime: "2026-08-04 09:00:00",
		CallCount:   1,
		Account:     AccountInfo{TotalEquity: 1000, AvailableBalance: 900},
		CandidateCoins: []CandidateCoin{
			{Symbol: "SOXLUSDT", Score: 0.7},
		},
		Positions: []PositionInfo{
			{
				Symbol: "SKHYNIXUSDT", Side: "short",
				EntryPrice: 1064.91, MarkPrice: 1070.00,
				Quantity: 0.03, Leverage: 5,
			},
		},
		MarketDataMap: map[string]*market.Data{
			"SOXLUSDT":    evoTestData("SOXLUSDT", 42.5),
			"SKHYNIXUSDT": evoTestData("SKHYNIXUSDT", 1070.0),
		},
	}

	prompt := e.BuildUserPrompt(ctx)
	// Guard against the vacuous-pass failure mode described above: assert both
	// loop bodies actually ran before asserting what they did NOT emit.
	for _, want := range []string{"SOXLUSDT", "SKHYNIXUSDT"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("fixture did not render %s — the injection sites were never reached, "+
				"so this test would pass vacuously", want)
		}
	}
	for _, banned := range []string{
		"Historical Performance",
		"适配度",
		"EMA20一致胜率",
	} {
		if strings.Contains(prompt, banned) {
			t.Errorf("prompt must not contain evolution-profile text %q", banned)
		}
	}
}
