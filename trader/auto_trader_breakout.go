package trader

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"nofx/kernel"
	"nofx/logger"
	"nofx/market"
	"nofx/store"
)

// RunBreakoutCycle is the standalone breakout strategy cycle (StrategyType
// "breakout_trading"). It is a pure data-validated rule engine: scan candidates,
// detect a closed-bar breakout, optionally let AI soft-veto (size-down) suspicious
// breakouts, then open via executeDecisionWithRecord — inheriting every existing
// risk control and the configured ladder/runner/time-stop protection. AI is an
// ENHANCEMENT layer only: it can shrink size but never block, never add entries,
// never close; if AI is unavailable it fails open (full size).
func (at *AutoTrader) RunBreakoutCycle() error {
	at.isRunningMutex.RLock()
	running := at.isRunning
	at.isRunningMutex.RUnlock()
	if !running {
		return nil
	}

	record := &store.DecisionRecord{
		ExecutionLog:   []string{},
		Success:        true,
		AIDecisionMode: "breakout",
		ReviewContext:  map[string]interface{}{"strategy_type": store.StrategyTypeBreakout},
	}

	ctx, err := at.buildTradingContext()
	if err != nil {
		record.Success = false
		record.ErrorMessage = fmt.Sprintf("build context: %v", err)
		at.saveDecision(record)
		return err
	}
	at.saveEquitySnapshot(ctx)

	at.runBreakoutEntries(ctx, record)

	if err := at.saveDecision(record); err != nil {
		logger.Infof("⚠ breakout: failed to save decision record: %v", err)
	}
	return nil
}

// runBreakoutEntries scans candidates for closed-bar breakouts and opens positions,
// with optional AI soft-veto sizing. Reuses executeDecisionWithRecord so all risk
// controls + configured protection apply automatically.
func (at *AutoTrader) runBreakoutEntries(ctx *kernel.Context, record *store.DecisionRecord) {
	if at == nil || at.config.StrategyConfig == nil || ctx == nil {
		return
	}
	cfg := at.config.StrategyConfig.BreakoutEntry
	lookback := cfg.Lookback
	if lookback <= 0 {
		lookback = 20
	}
	tf := cfg.Timeframe
	if tf == "" {
		tf = at.config.StrategyConfig.Indicators.Klines.PrimaryTimeframe
	}
	if tf == "" {
		tf = "1h"
	}
	leverage := cfg.Leverage
	if leverage <= 0 {
		leverage = at.config.StrategyConfig.RiskControl.AltcoinMaxLeverage
	}
	if leverage <= 0 {
		leverage = 5
	}
	equity := at.fetchEquityForSizing()
	if equity <= 0 {
		return
	}
	frac := cfg.SizeEquityFrac
	if frac <= 0 {
		frac = 1.0
	}
	baseSize := equity * frac

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
		if sym == "" || held[strings.ToUpper(sym)] {
			continue
		}
		md := ctx.MarketDataMap[sym]
		if md == nil || md.TimeframeData == nil {
			continue
		}
		series := md.TimeframeData[tf]
		if series == nil {
			continue
		}
		// Drop the live unclosed bar; evaluate on closed bars only.
		bars := series.Klines
		if len(bars) >= 1 {
			bars = bars[:len(bars)-1]
		}
		if len(bars) < lookback+1 {
			continue
		}
		sig := market.BreakoutSignalBars(bars, lookback)
		if sig == 0 {
			continue
		}
		action := "open_long"
		if sig < 0 {
			action = "open_short"
		}

		// AI soft-veto: shrink size on suspicious breakouts. Fail-open (full size) on
		// any AI error so a flaky AI endpoint never blocks the validated edge.
		mult, qScore, aiNote := at.breakoutAISizeMultiplier(sym, action, md, tf)
		size := baseSize * mult

		d := kernel.Decision{
			Symbol:          sym,
			Action:          action,
			Leverage:        leverage,
			PositionSizeUSD: size,
			SetupType:       "breakout",
			TriggerType:     "breakout",
			Confidence:      qScore,
			Reasoning:       fmt.Sprintf("Breakout(%s,lb%d) %s; AI qscore=%d mult=%.2f %s", tf, lookback, map[bool]string{true: "↑high", false: "↓low"}[sig > 0], qScore, mult, aiNote),
		}
		actionRecord := store.DecisionAction{
			Action: action, Symbol: sym, Leverage: leverage,
			Confidence: qScore, Reasoning: d.Reasoning,
		}
		if err := at.executeDecisionWithRecord(&d, &actionRecord); err != nil {
			actionRecord.Error = err.Error()
			record.ExecutionLog = append(record.ExecutionLog, fmt.Sprintf("🟦 breakout %s %s skipped: %v", sym, action, err))
		} else {
			actionRecord.Success = true
			record.ExecutionLog = append(record.ExecutionLog, fmt.Sprintf("🟦 breakout %s %s opened (mult=%.2f)", sym, action, mult))
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
	for _, k := range []string{"totalEquity", "totalWalletBalance", "availableBalance"} {
		if v, ok := balance[k].(float64); ok && v > 0 {
			return v
		}
	}
	return 0
}

// breakoutAISizeMultiplier asks the AI to judge whether a breakout looks genuine or
// a likely fakeout/exhaustion, returning a size multiplier (soft-veto only):
//
//	qscore >=70 → 1.0 (genuine), 40-69 → 0.6, <40 → 0.3.
//
// AI can ONLY shrink size, never block/add/close. Any failure → (1.0, fail-open).
func (at *AutoTrader) breakoutAISizeMultiplier(symbol, action string, md *market.Data, tf string) (float64, int, string) {
	if at.mcpClient == nil || md == nil {
		return 1.0, 75, "ai_skip"
	}
	series := md.TimeframeData[tf]
	if series == nil {
		return 1.0, 75, "ai_skip"
	}
	atr := series.ATR14
	dir := "LONG"
	if strings.Contains(action, "short") {
		dir = "SHORT"
	}
	sys := "You are a strict crypto breakout-quality filter. A breakout just triggered. " +
		"Judge if it is a GENUINE breakout likely to follow through, or a FAKEOUT/EXHAUSTION likely to revert. " +
		"Reply with ONLY an integer 0-100 (breakout quality score, higher=more genuine). No words."
	usr := fmt.Sprintf("Symbol %s, direction %s, timeframe %s. Current price %.6f, ATR14 %.4f (%.2f%% of price). "+
		"Recent closes trend and whether volume/structure supports continuation. Output only the integer score.",
		symbol, dir, tf, md.CurrentPrice, atr, pctOf(atr, md.CurrentPrice))

	at.mcpClient.SetTimeout(20 * time.Second)
	resp, err := at.mcpClient.CallWithMessages(sys, usr)
	if err != nil {
		return 1.0, 75, "ai_failopen_err"
	}
	score := parseFirstInt(resp)
	if score < 0 {
		return 1.0, 75, "ai_failopen_parse"
	}
	mult, label := breakoutSizeMultiplierForScore(score)
	return mult, score, label
}

// breakoutSizeMultiplierForScore maps an AI breakout-quality score (0-100) to a
// size multiplier. Soft-veto only: AI can shrink size but never below 0.3.
//
//	>=70 → 1.0 (genuine), 40-69 → 0.6 (suspicious), <40 → 0.3 (weak).
func breakoutSizeMultiplierForScore(score int) (float64, string) {
	switch {
	case score >= 70:
		return 1.0, "genuine"
	case score >= 40:
		return 0.6, "suspicious"
	default:
		return 0.3, "weak"
	}
}

func pctOf(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b * 100
}

// parseFirstInt extracts the first integer 0-100 from a string, or -1 if none.
func parseFirstInt(s string) int {
	var cur strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			cur.WriteRune(r)
		} else if cur.Len() > 0 {
			break
		}
	}
	if cur.Len() == 0 {
		return -1
	}
	n, err := strconv.Atoi(cur.String())
	if err != nil || n < 0 || n > 100 {
		return -1
	}
	return n
}
