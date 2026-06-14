package trader

import (
	"time"

	"nofx/logger"
)

// shouldTimeStop is the pure decision: held past `hours` AND still in loss worse
// than `lossPct` (negative). Returns (trigger, heldHours, pnlPct).
func shouldTimeStop(side string, entryPrice, markPrice float64, posCreatedTime, nowMs int64, hours, lossPct float64) (bool, float64, float64) {
	if hours <= 0 || lossPct >= 0 {
		return false, 0, 0
	}
	if posCreatedTime <= 0 || entryPrice <= 0 || markPrice <= 0 {
		return false, 0, 0
	}
	heldHours := float64(nowMs-posCreatedTime) / float64(3600*1000)
	pnlPct := calculatePositionPnLPct(side, entryPrice, markPrice)
	if heldHours < hours {
		return false, heldHours, pnlPct
	}
	if pnlPct > lossPct {
		return false, heldHours, pnlPct
	}
	return true, heldHours, pnlPct
}

// maybeTimeStopClose force-closes a position that has been held longer than the
// configured TimeStopHours AND is still in loss worse than TimeStopLossPct.
//
// Rationale (2026-06-12, from claude's data): some of the largest losses were
// directionally-wrong trades held 20-37h that bled slowly toward the stop. A
// time-stop cuts that slow-bleed pattern deterministically. The loss condition is
// mandatory so winners (e.g. a +9% position held 34h) are never touched.
//
// Returns true if it closed the position (caller should skip further handling).
func (at *AutoTrader) maybeTimeStopClose(symbol, side string, entryPrice, markPrice, quantity float64, posCreatedTime int64) bool {
	if at == nil || at.config.StrategyConfig == nil || quantity <= 0 {
		return false
	}
	rc := at.config.StrategyConfig.RiskControl
	trigger, heldHours, pnlPct := shouldTimeStop(side, entryPrice, markPrice, posCreatedTime, time.Now().UnixMilli(), rc.TimeStopHours, rc.TimeStopLossPct)
	if !trigger {
		return false
	}

	logger.Infof("⏱ Time-stop: %s %s held %.1fh (>= %.1fh) and still in loss %.2f%% (<= %.2f%%) — force closing",
		symbol, side, heldHours, rc.TimeStopHours, pnlPct, rc.TimeStopLossPct)
	if err := at.closePositionByReason(symbol, side, quantity, "time_stop"); err != nil {
		logger.Warnf("⚠️ Time-stop close failed for %s %s: %v", symbol, side, err)
		return false
	}
	logger.Infof("✅ Time-stop closed %s %s (held %.1fh, pnl %.2f%%)", symbol, side, heldHours, pnlPct)
	return true
}

// shouldMaxHoldClose is the pure decision for the max-hold stop: held past `hours`
// AND not a profitable runner (pnlPct < exemptPct). Unlike shouldTimeStop this fires
// on flat / break-even / dust tails too — anything that is not a genuine winner.
// Returns (trigger, heldHours, pnlPct).
func shouldMaxHoldClose(side string, entryPrice, markPrice float64, posCreatedTime, nowMs int64, hours, exemptPct float64) (bool, float64, float64) {
	if hours <= 0 {
		return false, 0, 0
	}
	if posCreatedTime <= 0 || entryPrice <= 0 || markPrice <= 0 {
		return false, 0, 0
	}
	heldHours := float64(nowMs-posCreatedTime) / float64(3600*1000)
	pnlPct := calculatePositionPnLPct(side, entryPrice, markPrice)
	if heldHours < hours {
		return false, heldHours, pnlPct
	}
	if pnlPct >= exemptPct {
		// Profitable runner — spare it so trends are not cut short.
		return false, heldHours, pnlPct
	}
	return true, heldHours, pnlPct
}

// maybeMaxHoldClose force-closes a position held longer than MaxHoldHours that is not
// a profitable runner (pnl < MaxHoldProfitExemptPct). This clears slow-bleed wrong-way
// trades, flat sideways grinders, and break-even / dust tails that would otherwise
// occupy a position slot indefinitely. Runs alongside the time-stop; either may fire.
//
// Returns true if it closed the position (caller should skip further handling).
func (at *AutoTrader) maybeMaxHoldClose(symbol, side string, entryPrice, markPrice, quantity float64, posCreatedTime int64) bool {
	if at == nil || at.config.StrategyConfig == nil || quantity <= 0 {
		return false
	}
	rc := at.config.StrategyConfig.RiskControl
	trigger, heldHours, pnlPct := shouldMaxHoldClose(side, entryPrice, markPrice, posCreatedTime, time.Now().UnixMilli(), rc.MaxHoldHours, rc.MaxHoldProfitExemptPct)
	if !trigger {
		return false
	}

	logger.Infof("⏳ Max-hold stop: %s %s held %.1fh (>= %.1fh) and pnl %.2f%% (< exempt %.2f%%) — force closing",
		symbol, side, heldHours, rc.MaxHoldHours, pnlPct, rc.MaxHoldProfitExemptPct)
	if err := at.closePositionByReason(symbol, side, quantity, "max_hold"); err != nil {
		logger.Warnf("⚠️ Max-hold close failed for %s %s: %v", symbol, side, err)
		return false
	}
	logger.Infof("✅ Max-hold closed %s %s (held %.1fh, pnl %.2f%%)", symbol, side, heldHours, pnlPct)
	return true
}
