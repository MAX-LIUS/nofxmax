package trader

import (
	"time"

	"nofx/logger"
	"nofx/market"
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
	executed, err := at.closePositionByReasonWithOutcome(symbol, side, quantity, "time_stop")
	if err != nil {
		logger.Warnf("⚠️ Time-stop close failed for %s %s: %v", symbol, side, err)
		return false
	}
	if !executed {
		// 交易所侧报 NO_POSITION/SKIPPED/POSITION_DUST:仓位早已不在,什么都没平。
		// 打成 ✅ 会让事后按这行计数的平仓次数翻倍(线上 CLUSDT/ETHUSDT 各一次)。
		// 仍回 true —— 对调用方而言"这个仓位不需要再处理"是成立的。
		logger.Warnf("⏱ Time-stop skipped for %s %s: position already gone on exchange (held %.1fh, pnl %.2f%%) — nothing closed",
			symbol, side, heldHours, pnlPct)
		return true
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
	executed, err := at.closePositionByReasonWithOutcome(symbol, side, quantity, "max_hold")
	if err != nil {
		logger.Warnf("⚠️ Max-hold close failed for %s %s: %v", symbol, side, err)
		return false
	}
	if !executed {
		// 同 time-stop:仓位已不在场,没有下过单,不能打成功。
		logger.Warnf("⏳ Max-hold skipped for %s %s: position already gone on exchange (held %.1fh, pnl %.2f%%) — nothing closed",
			symbol, side, heldHours, pnlPct)
		return true
	}
	logger.Infof("✅ Max-hold closed %s %s (held %.1fh, pnl %.2f%%)", symbol, side, heldHours, pnlPct)
	return true
}

// shouldTrailingTPClose is the pure decision for the config trailing take-profit:
// the position must have reached the activation threshold at some point (peakPnLPct >=
// activatePct), then close if the current pnl has given back >= givebackPct of the peak
// AND the locked pnl is still >= minLockPct (so we don't fire on a still-tiny profit).
//
//	givebackPct is a percentage of the PEAK (e.g. 35 => close when pnl falls to 65% of peak).
//	minLockPct is an absolute pnl floor (e.g. 0.5 => only fire if current pnl still >= +0.5%).
//
// Returns (trigger, armed, drawdownFromPeakPct).
func shouldTrailingTPClose(currentPnLPct, peakPnLPct, activatePct, givebackPct, minLockPct float64) (bool, bool, float64) {
	if activatePct <= 0 || givebackPct <= 0 {
		return false, false, 0
	}
	// Never armed: peak never reached the activation threshold.
	if peakPnLPct < activatePct {
		return false, false, 0
	}
	if peakPnLPct <= 0 {
		return false, true, 0
	}
	// Drawdown from peak as a fraction of the peak profit.
	drawdownFromPeak := ((peakPnLPct - currentPnLPct) / peakPnLPct) * 100
	if drawdownFromPeak < givebackPct {
		return false, true, drawdownFromPeak
	}
	// Don't fire if what we'd lock in is below the min-lock floor (e.g. profit already gone).
	if currentPnLPct < minLockPct {
		return false, true, drawdownFromPeak
	}
	return true, true, drawdownFromPeak
}

// maybeTrailingTPClose closes the remaining position when the configured trailing
// take-profit triggers. It reads the peak from peakPnLCache (shared with the drawdown
// monitor) so it tracks the true high-water mark. Runs before the AI drawdown rules so it
// works even when no drawdown rules are configured. Returns true if it closed the position.
func (at *AutoTrader) maybeTrailingTPClose(symbol, side string, entryPrice, markPrice, quantity, peakPnLPct float64) bool {
	if at == nil || at.config.StrategyConfig == nil || quantity <= 0 {
		return false
	}
	rc := at.config.StrategyConfig.RiskControl
	if !rc.TrailingTakeProfitEnabled {
		return false
	}
	currentPnLPct := calculatePositionPnLPct(side, entryPrice, markPrice)
	trigger, armed, ddFromPeak := shouldTrailingTPClose(currentPnLPct, peakPnLPct, rc.TrailingActivatePct, rc.TrailingGivebackPct, rc.TrailingMinLockPct)
	if !trigger {
		if armed {
			logger.Debugf("🎯 Trailing-TP armed for %s %s: pnl=%.2f%% peak=%.2f%% ddFromPeak=%.1f%% (need %.1f%%)",
				symbol, side, currentPnLPct, peakPnLPct, ddFromPeak, rc.TrailingGivebackPct)
		}
		return false
	}

	logger.Infof("🎯 Trailing-TP: %s %s peak=%.2f%% pnl=%.2f%% gave back %.1f%% of peak (>= %.1f%%) — locking profit",
		symbol, side, peakPnLPct, currentPnLPct, ddFromPeak, rc.TrailingGivebackPct)
	if err := at.closePositionByReason(symbol, side, quantity, "trailing_take_profit"); err != nil {
		logger.Warnf("⚠️ Trailing-TP close failed for %s %s: %v", symbol, side, err)
		return false
	}
	at.ClearPeakPnLCache(symbol, side)
	logger.Infof("✅ Trailing-TP closed %s %s (locked %.2f%%, peak was %.2f%%)", symbol, side, currentPnLPct, peakPnLPct)
	return true
}

// volatilitySizeMultiplier returns the position-size multiplier for volatility targeting.
// It scales inversely to the symbol's current ATR14% (atr14Pct = ATR14/price*100): a coin
// more volatile than the target gets a smaller multiplier, a calmer coin gets a larger one,
// clamped to [minMult, maxMult]. Returns 1.0 (no change) when disabled or inputs are invalid.
func volatilitySizeMultiplier(atr14Pct, targetPct, minMult, maxMult float64) float64 {
	if targetPct <= 0 || atr14Pct <= 0 {
		return 1.0
	}
	if minMult <= 0 {
		minMult = 0.4
	}
	if maxMult <= 0 {
		maxMult = 1.5
	}
	if minMult > maxMult {
		minMult, maxMult = maxMult, minMult
	}
	mult := targetPct / atr14Pct
	if mult < minMult {
		mult = minMult
	}
	if mult > maxMult {
		mult = maxMult
	}
	return mult
}

// extractPrimaryATR14 pulls the ATR14 absolute value from market data, preferring the
// primary timeframe series, then any timeframe, then the legacy IntradaySeries. Returns 0
// if unavailable (vol-sizing then becomes a no-op).
func extractPrimaryATR14(data *market.Data, primaryTimeframe string) float64 {
	if data == nil {
		return 0
	}
	if primaryTimeframe != "" && data.TimeframeData != nil {
		if tf, ok := data.TimeframeData[primaryTimeframe]; ok && tf != nil && tf.ATR14 > 0 {
			return tf.ATR14
		}
	}
	if data.TimeframeData != nil {
		for _, tf := range data.TimeframeData {
			if tf != nil && tf.ATR14 > 0 {
				return tf.ATR14
			}
		}
	}
	if data.IntradaySeries != nil && data.IntradaySeries.ATR14 > 0 {
		return data.IntradaySeries.ATR14
	}
	return 0
}

// extractExecutionATR14 resolves the ATR14 for vol-sizing using the strategy's primary
// timeframe when available, falling back to any timeframe / IntradaySeries.
func (at *AutoTrader) extractExecutionATR14(data *market.Data) float64 {
	primaryTF := ""
	if at != nil && at.strategyEngine != nil {
		cfg := at.strategyEngine.GetConfig()
		primaryTF = cfg.Indicators.Klines.PrimaryTimeframe
		if primaryTF == "" && len(cfg.Indicators.Klines.SelectedTimeframes) > 0 {
			primaryTF = cfg.Indicators.Klines.SelectedTimeframes[0]
		}
	}
	return extractPrimaryATR14(data, primaryTF)
}

// applyVolatilitySizing scales positionSizeUSD by the volatility-target multiplier when
// VolSizingEnabled. Returns the (possibly reduced) size and the multiplier applied.
func (at *AutoTrader) applyVolatilitySizing(symbol string, positionSizeUSD, atr14, currentPrice float64) (float64, float64) {
	if at == nil || at.config.StrategyConfig == nil {
		return positionSizeUSD, 1.0
	}
	rc := at.config.StrategyConfig.RiskControl
	if !rc.VolSizingEnabled || rc.VolTargetPct <= 0 {
		return positionSizeUSD, 1.0
	}
	if atr14 <= 0 || currentPrice <= 0 {
		return positionSizeUSD, 1.0
	}
	atr14Pct := atr14 / currentPrice * 100
	mult := volatilitySizeMultiplier(atr14Pct, rc.VolTargetPct, rc.VolSizeMinMult, rc.VolSizeMaxMult)
	if mult == 1.0 {
		return positionSizeUSD, 1.0
	}
	adjusted := positionSizeUSD * mult
	logger.Infof("  📐 Vol-sizing %s: atr14=%.2f%% target=%.2f%% mult=%.2fx | size %.2f -> %.2f",
		symbol, atr14Pct, rc.VolTargetPct, mult, positionSizeUSD, adjusted)
	return adjusted, mult
}
