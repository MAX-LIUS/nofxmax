package trader

import (
	"fmt"
	"strings"

	"nofx/kernel"
	"nofx/market"
	"nofx/store"
)

// runBreakoutEntries scans candidate coins for the data-validated breakout signal
// and opens positions independently of the AI. It reuses executeDecisionWithRecord,
// so every existing risk control applies automatically: post-loss cooldown, max
// positions, position-value cap, and the configured ladder/runner/time-stop
// protection. Symbols already holding a position, or already acted on by the AI this
// cycle, are skipped.
func (at *AutoTrader) runBreakoutEntries(ctx *kernel.Context, record *store.DecisionRecord) {
	if at == nil || at.config.StrategyConfig == nil || ctx == nil {
		return
	}
	cfg := at.config.StrategyConfig.BreakoutEntry
	if !cfg.Enabled {
		return
	}
	lookback := cfg.Lookback
	if lookback <= 0 {
		lookback = 20
	}
	tf := cfg.Timeframe
	if tf == "" {
		tf = at.config.StrategyConfig.Indicators.Klines.PrimaryTimeframe
	}
	leverage := cfg.Leverage
	if leverage <= 0 {
		leverage = at.config.StrategyConfig.RiskControl.AltcoinMaxLeverage
	}
	if leverage <= 0 {
		leverage = 5
	}

	// Equity for sizing.
	equity := at.fetchEquityForSizing()
	if equity <= 0 {
		return
	}
	frac := cfg.SizeEquityFrac
	if frac <= 0 {
		frac = 0.5
	}
	sizeUSD := equity * frac

	// Symbols already opened by the AI this cycle (avoid double entry).
	aiOpened := map[string]bool{}
	for _, a := range record.Decisions {
		if isOpenAction(a.Action) {
			aiOpened[strings.ToUpper(a.Symbol)] = true
		}
	}
	// Current live positions.
	held := map[string]bool{}
	if positions, err := at.trader.GetPositions(); err == nil {
		for _, p := range positions {
			if s, ok := p["symbol"].(string); ok {
				held[strings.ToUpper(s)] = true
			}
		}
	}

	for _, coin := range ctx.CandidateCoins {
		sym := coin.Symbol
		if sym == "" {
			continue
		}
		up := strings.ToUpper(sym)
		if aiOpened[up] || held[up] {
			continue
		}
		md := ctx.MarketDataMap[sym]
		if md == nil || md.TimeframeData == nil {
			continue
		}
		series := md.TimeframeData[tf]
		if series == nil || len(series.Klines) < lookback+1 {
			continue
		}
		sig := market.BreakoutSignalBars(series.Klines, lookback)
		if sig == 0 {
			continue
		}
		action := "open_long"
		if sig < 0 {
			action = "open_short"
		}
		d := kernel.Decision{
			Symbol:          sym,
			Action:          action,
			Leverage:        leverage,
			PositionSizeUSD: sizeUSD,
			SetupType:       "breakout",
			TriggerType:     "breakout",
			Confidence:      75,
			Reasoning:       fmt.Sprintf("Code breakout: close broke prior-%d-bar %s on %s", lookback, map[bool]string{true: "high", false: "low"}[sig > 0], tf),
		}
		actionRecord := store.DecisionAction{
			Action: action, Symbol: sym, Leverage: leverage,
			Confidence: 75, Reasoning: d.Reasoning,
		}
		if err := at.executeDecisionWithRecord(&d, &actionRecord); err != nil {
			actionRecord.Error = err.Error()
			record.ExecutionLog = append(record.ExecutionLog, fmt.Sprintf("🟦 breakout %s %s skipped: %v", sym, action, err))
		} else {
			actionRecord.Success = true
			record.ExecutionLog = append(record.ExecutionLog, fmt.Sprintf("🟦 breakout %s %s opened", sym, action))
		}
		record.Decisions = append(record.Decisions, actionRecord)
	}
}

// fetchEquityForSizing returns total account equity (falls back to available balance).
func (at *AutoTrader) fetchEquityForSizing() float64 {
	balance, err := at.trader.GetBalance()
	if err != nil || balance == nil {
		return 0
	}
	if eq, ok := balance["totalEquity"].(float64); ok && eq > 0 {
		return eq
	}
	if eq, ok := balance["totalWalletBalance"].(float64); ok && eq > 0 {
		return eq
	}
	if av, ok := balance["availableBalance"].(float64); ok && av > 0 {
		return av
	}
	return 0
}
