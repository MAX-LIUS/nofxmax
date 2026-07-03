package trader

import (
	"nofx/logger"
	"nofx/market"
	"nofx/store"
)

// runStructuralSLGuard is Phase 2 of the structural stop-loss: the close-confirm
// enforcement layer. It runs on the drawdown poll cadence and, for positions whose
// strategy uses a structural SL with CloseConfirm enabled, market-closes a position
// when the most recent CLOSED bar on the ATR timeframe closed BEYOND the frozen
// pre-entry range boundary (swing low for long / swing high for short).
//
// Why close-confirm + a separate layer: the resting exchange stop for these
// positions is parked at the wide backstop (see resolveATRProtection) so it never
// fires on an intrabar wick (stop-hunt / false break). This guard supplies the tight
// structural exit, but only on a confirmed bar close — which the extended-replay
// backtest showed cuts SL whipsaw from ~34% to ~27% and turns the ranging-regime
// net positive. The backstop resting order remains as a bot-downtime safety net.
func (at *AutoTrader) runStructuralSLGuard() {
	if at.config.StrategyConfig == nil {
		return
	}
	ladder := at.config.StrategyConfig.Protection.LadderTPSL
	if !ladder.StructuralSL.Enabled || !ladder.StructuralSL.WithDefaults().CloseConfirm {
		return
	}
	// Only meaningful if some SL rule actually opts into the structural unit.
	usesStructural := false
	for _, r := range ladder.Rules {
		if r.StopLossUnit == store.ProtectionUnitStructural {
			usesStructural = true
			break
		}
	}
	if !usesStructural {
		return
	}

	acfg := at.config.StrategyConfig.ATRProtection
	if !acfg.Enabled {
		return
	}

	positions, err := at.trader.GetPositions()
	if err != nil || len(positions) == 0 {
		return
	}

	for _, pos := range positions {
		symbol, _ := pos["symbol"].(string)
		side, _ := pos["side"].(string)
		entry, _ := pos["entryPrice"].(float64)
		qty, _ := pos["positionAmt"].(float64)
		if qty < 0 {
			qty = -qty
		}
		if symbol == "" || side == "" || entry <= 0 || qty <= 0 {
			continue
		}
		at.evaluateStructuralSLClose(symbol, side, entry, qty, ladder.StructuralSL, acfg)
	}
}

// evaluateStructuralSLClose closes one position if the last CLOSED bar breached its
// frozen structural boundary. Dedups per closed-bar so a single breach fires once.
func (at *AutoTrader) evaluateStructuralSLClose(symbol, side string, entry, qty float64, sscfg store.StructuralSLConfig, acfg store.ATRProtectionConfig) {
	isLong := actionFromPositionSide(side) == "open_long"
	// allowCompute=false: the guard only enforces a boundary that was frozen AT ENTRY.
	// A position with no frozen boundary (opened before enablement) rides the backstop.
	boundary, ok := at.frozenStructBoundaryForPosition(symbol, entry, isLong, false, sscfg, acfg)
	if !ok || boundary <= 0 {
		return // no structural edge for this position; resting backstop covers it
	}

	c := acfg.WithDefaults()
	// Need at least 2 bars: the last element is the still-forming bar, the one before
	// it is the most recently CLOSED bar we confirm against.
	bars, err := market.GetKlines(symbol, c.Timeframe, at.exchange, 3)
	if err != nil || len(bars) < 2 {
		return
	}
	closedBar := bars[len(bars)-2]
	if closedBar.Close <= 0 {
		return
	}

	breached := (isLong && closedBar.Close < boundary) || (!isLong && closedBar.Close > boundary)
	if !breached {
		return
	}

	// Dedup: only fire once per distinct closed bar.
	key := symbol + "_" + side
	at.structSLMutex.Lock()
	if at.structSLFiredBar == nil {
		at.structSLFiredBar = map[string]int64{}
	}
	if at.structSLFiredBar[key] == closedBar.OpenTime {
		at.structSLMutex.Unlock()
		return
	}
	at.structSLFiredBar[key] = closedBar.OpenTime
	at.structSLMutex.Unlock()

	logger.Infof("🛑 [StructuralSL] %s %s closed %.6f beyond boundary %.6f (close-confirm) → market close %.6f",
		symbol, side, closedBar.Close, boundary, qty)
	if err := at.closePositionByReason(symbol, side, qty, "structural_sl"); err != nil {
		logger.Infof("❌ [StructuralSL] close failed %s %s: %v", symbol, side, err)
	}
}

