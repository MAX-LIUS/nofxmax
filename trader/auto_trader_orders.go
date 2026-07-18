package trader

import (
	"fmt"
	"nofx/kernel"
	"nofx/logger"
	"nofx/market"
	"nofx/store"
	"time"
)

func (at *AutoTrader) getExecutionMarketData(symbol string) (*market.Data, error) {
	// Prefer strategy-configured timeframes so execution aligns with AI analysis.
	if at.strategyEngine != nil {
		cfg := at.strategyEngine.GetConfig()
		timeframes := append([]string{}, cfg.Indicators.Klines.SelectedTimeframes...)
		primaryTimeframe := cfg.Indicators.Klines.PrimaryTimeframe
		klineCount := cfg.Indicators.Klines.PrimaryCount

		// Backward-compatible fallback for older configs.
		if len(timeframes) == 0 {
			if primaryTimeframe != "" {
				timeframes = append(timeframes, primaryTimeframe)
			} else {
				timeframes = append(timeframes, "3m")
			}
			if cfg.Indicators.Klines.LongerTimeframe != "" {
				timeframes = append(timeframes, cfg.Indicators.Klines.LongerTimeframe)
			}
		}
		if primaryTimeframe == "" && len(timeframes) > 0 {
			primaryTimeframe = timeframes[0]
		}
		if klineCount <= 0 {
			klineCount = 30
		}

		exchangeSrc := cfg.CoinSource.ExchangeSource
		if exchangeSrc == "" {
			exchangeSrc = at.exchange
		}
		logger.Infof("  📊 Execution market data uses strategy timeframes: %v (primary=%s, count=%d, exchange=%s)", timeframes, primaryTimeframe, klineCount, exchangeSrc)
		return market.GetWithTimeframesExchange(symbol, timeframes, primaryTimeframe, klineCount, exchangeSrc)
	}

	// Legacy fallback when no strategy engine is available.
	logger.Infof("  ⚠️ Strategy engine unavailable, falling back to legacy execution market data path")
	return market.GetWithExchange(symbol, at.exchange)
}

func (at *AutoTrader) applyRegimeGateToActionRecord(decision *kernel.Decision, actionRecord *store.DecisionAction, gate regimeGateResult) {
	if actionRecord == nil || gate.Allowed {
		return
	}
	if actionRecord.ReviewContext == nil {
		actionRecord.ReviewContext = buildDecisionActionReviewContext(decision, at.getMinRiskRewardRatio(), nil)
		if actionRecord.ReviewContext == nil {
			actionRecord.ReviewContext = &store.DecisionActionReviewContext{}
		}
	}
	control := actionRecord.ReviewContext.Control
	if control == nil {
		control = &store.DecisionActionControlOutcome{}
		actionRecord.ReviewContext.Control = control
	}
	control.Decision = "rejected"
	control.OriginalAction = decision.Action
	control.FinalAction = decision.Action
	control.NoOrderPlaced = true
	if gate.Reason != "" {
		control.Reasons = append(control.Reasons, gate.Reason)
	}
	if gate.ReasonCode != "" {
		control.FailedChecks = append(control.FailedChecks, gate.ReasonCode)
	}
	control.RegimeCurrent = gate.CurrentRegime
	control.RegimeAllowed = append([]string{}, gate.AllowedRegimes...)
	control.RegimePrimaryTimeframe = gate.PrimaryTimeframe
	control.RegimeATR14Pct = gate.ATR14Pct
	control.RegimeFundingRate = gate.FundingRate
	if gate.TrendAligned != nil {
		aligned := *gate.TrendAligned
		control.RegimeTrendAligned = &aligned
	}
}

// executeDecisionWithRecord executes AI decision and records detailed information
func (at *AutoTrader) executeDecisionWithRecord(decision *kernel.Decision, actionRecord *store.DecisionAction) error {
	switch decision.Action {
	case "open_long":
		return at.executeOpenLongWithRecord(decision, actionRecord)
	case "open_short":
		return at.executeOpenShortWithRecord(decision, actionRecord)
	case "close_long":
		return at.executeCloseLongWithRecord(decision, actionRecord)
	case "close_short":
		return at.executeCloseShortWithRecord(decision, actionRecord)
	case "hold", "wait":
		// No execution needed, just record
		return nil
	default:
		return fmt.Errorf("unknown action: %s", decision.Action)
	}
}

// executeOpenLongWithRecord executes open long position and records detailed information
func (at *AutoTrader) executeOpenLongWithRecord(decision *kernel.Decision, actionRecord *store.DecisionAction) error {
	logger.Infof("  📈 Open long: %s", decision.Symbol)

	// [CODE ENFORCED] Post-loss cooldown check
	if cooling, remaining := at.cooldownManager.IsCoolingDown(decision.Symbol); cooling {
		return fmt.Errorf("⏳ entry cooldown active for %s (%v remaining after stop-loss)", decision.Symbol, remaining.Round(time.Minute))
	}

	// ⚠️ Get current positions for multiple checks
	positions, err := at.trader.GetPositions()
	if err != nil {
		return fmt.Errorf("failed to get positions: %w", err)
	}

	// Check if there's already a position in the same symbol and direction
	for _, pos := range positions {
		if pos["symbol"] == decision.Symbol && pos["side"] == "long" {
			return fmt.Errorf("❌ %s already has long position, close it first", decision.Symbol)
		}
		// Opposite (short) position on the same symbol: a high-conviction long
		// signal may FLIP it (close short + open long) when trend-reversal is
		// enabled and the position is aged enough. Otherwise fall through to the
		// normal capacity path (which still rejects/handles as before).
		if pos["symbol"] == decision.Symbol && pos["side"] == "short" {
			fd := at.evaluateFlip(decision, "long", pos)
			if fd.ShouldFlip {
				executed, err := at.executeFlipClose(decision, "long", fd)
				if err != nil {
					return fmt.Errorf("trend-reversal flip (close short) failed for %s: %w", decision.Symbol, err)
				}
				if !executed {
					// DryRun: observed only, keep the original short intact.
					return fmt.Errorf("🔄 %s flip observed (dry_run): would close short + open long", decision.Symbol)
				}
				// Live close done; re-fetch positions before opening the reverse.
				if refreshed, rerr := at.trader.GetPositions(); rerr == nil {
					positions = refreshed
				}
			} else if fd.IsCandidate {
				// Genuine opposite-on-held reversal that did NOT flip (conf/age/
				// disabled). Record the near-miss so it is reviewable, then let the
				// normal capacity path reject the same-symbol entry as before.
				at.recordFlipObservation(decision, "long", fd, false)
			}
		}
	}

	// Get current price
	marketData, err := at.getExecutionMarketData(decision.Symbol)
	if err != nil {
		return err
	}

	// [CODE ENFORCED] Capacity check with optional strong-signal replacement. When at capacity and
	// replacement is enabled, this cuts the weakest eligible position to free a slot (after the
	// entry-price deviation precheck passes), then re-fetches positions and re-enforces the limit.
	// The cut runs BEFORE the balance fetch below so freed margin is reflected downstream.
	positions, err = at.enforceCapacityWithReplacement(decision, "long", positions, marketData.CurrentPrice)
	if err != nil {
		return err
	}

	// Get balance (needed for multiple checks)
	balance, err := at.trader.GetBalance()
	if err != nil {
		return fmt.Errorf("failed to get account balance: %w", err)
	}
	availableBalance := 0.0
	if avail, ok := balance["availableBalance"].(float64); ok {
		availableBalance = avail
	}

	// Get equity for position value ratio check
	equity := 0.0
	if eq, ok := balance["totalEquity"].(float64); ok && eq > 0 {
		equity = eq
	} else if eq, ok := balance["totalWalletBalance"].(float64); ok && eq > 0 {
		equity = eq
	} else {
		equity = availableBalance
	}

	// [CODE ENFORCED] Volatility-targeted sizing: shrink size on high-ATR symbols before
	// any downstream caps so all checks see the volatility-adjusted size. No-op when disabled.
	if adj, mult := at.applyVolatilitySizing(decision.Symbol, decision.PositionSizeUSD, at.extractExecutionATR14(marketData), marketData.CurrentPrice); mult != 1.0 {
		decision.PositionSizeUSD = adj
	}

	// [CODE ENFORCED] Position Value Ratio Check: position_value <= equity × ratio
	adjustedPositionSize, wasCapped := at.enforcePositionValueRatio(decision.PositionSizeUSD, equity, decision.Symbol)
	if wasCapped {
		decision.PositionSizeUSD = adjustedPositionSize
	}

	// Auto-adjust position size if insufficient margin
	marginFactor := 1.01/float64(decision.Leverage) + 0.001
	maxAffordablePositionSize := availableBalance / marginFactor

	actualPositionSize := decision.PositionSizeUSD
	if actualPositionSize > maxAffordablePositionSize {
		adjustedSize := maxAffordablePositionSize * 0.98
		logger.Infof("  ⚠️ Position size %.2f exceeds max affordable %.2f, auto-reducing to %.2f",
			actualPositionSize, maxAffordablePositionSize, adjustedSize)
		actualPositionSize = adjustedSize
		decision.PositionSizeUSD = actualPositionSize
	}

	// [CODE ENFORCED] Minimum position size check
	if err := at.enforceMinPositionSize(decision.PositionSizeUSD, decision.Symbol); err != nil {
		return err
	}

	// [CODE ENFORCED] Entry price deviation check
	if err := enforceEntryPriceDeviationWithMax(decision, marketData.CurrentPrice, "long", at.getMaxEntryDeviationPct()); err != nil {
		return err
	}

	// [CODE ENFORCED] Leverage cap
	decision.Leverage = at.enforceLeverageCap(decision.Leverage, decision.Symbol)

	// [CODE ENFORCED] Max margin usage
	if err := at.enforceMaxMarginUsage(decision.PositionSizeUSD, decision.Leverage, equity); err != nil {
		return err
	}

	// Calculate quantity with adjusted position size
	quantity := actualPositionSize / marketData.CurrentPrice
	actionRecord.Quantity = quantity
	actionRecord.Price = marketData.CurrentPrice

	// Set margin mode
	if err := at.trader.SetMarginMode(decision.Symbol, at.config.IsCrossMargin); err != nil {
		logger.Infof("  ⚠️ Failed to set margin mode: %v", err)
		// Continue execution, doesn't affect trading
	}

	// Open position. Try a post-only maker entry first (earns maker fee); fall back to
	// a market order when unfilled (if configured) or when maker is disabled/unsupported.
	var order map[string]interface{}
	if makerResult, filled, attempted := at.tryMakerEntry(decision.Symbol, "long", quantity, decision.Leverage); attempted {
		if filled {
			order = makerResult
		} else if at.makerEntryShouldFallback() {
			logger.Infof("  ↩️ Maker entry unfilled for %s LONG, crossing with market order", decision.Symbol)
			order, err = at.trader.OpenLong(decision.Symbol, quantity, decision.Leverage)
		} else {
			return fmt.Errorf("maker entry unfilled for %s long and market fallback disabled", decision.Symbol)
		}
	} else {
		order, err = at.trader.OpenLong(decision.Symbol, quantity, decision.Leverage)
	}
	if err != nil {
		return err
	}

	// Record order ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	logger.Infof("  ✓ Position opened successfully, order ID: %v, quantity: %.4f", order["orderId"], quantity)

	// Record order to database and poll for confirmation
	at.recordAndConfirmOrder(order, decision.Symbol, "open_long", quantity, marketData.CurrentPrice, decision.Leverage, 0)

	// Record position opening time
	posKey := decision.Symbol + "_long"
	at.positionFirstSeenTime[posKey] = time.Now().UnixMilli()

	// Wipe any stale per-position state from a prior same-symbol position before the
	// new position's protection is materialized (fix 2026-07-17 cross-position carryover).
	at.resetPerPositionStateOnOpen(decision.Symbol, "long")

	if err := at.applyPostOpenProtection(&protectionExecutionRequest{
		Symbol:       decision.Symbol,
		Action:       decision.Action,
		PositionSide: "LONG",
		Quantity:     quantity,
		EntryPrice:   marketData.CurrentPrice,
		Decision:     decision,
	}); err != nil {
		logger.Warnf("  ❌ Protection setup failed for %s LONG: %v", decision.Symbol, err)
		logger.Warnf("  🚨 Protection setup failed, closing position immediately: %s", decision.Symbol)
		if _, closeErr := at.trader.CloseLong(decision.Symbol, 0); closeErr != nil {
			logger.Errorf("  ❌ Failed to emergency close unprotected long %s: %v", decision.Symbol, closeErr)
			return fmt.Errorf("protection setup failed: %w; emergency close failed: %v", err, closeErr)
		}
		return fmt.Errorf("protection setup failed and position was closed: %w", err)
	}

	return nil
}

// executeOpenShortWithRecord executes open short position and records detailed information
func (at *AutoTrader) executeOpenShortWithRecord(decision *kernel.Decision, actionRecord *store.DecisionAction) error {
	logger.Infof("  📉 Open short: %s", decision.Symbol)

	// [CODE ENFORCED] Post-loss cooldown check
	if cooling, remaining := at.cooldownManager.IsCoolingDown(decision.Symbol); cooling {
		return fmt.Errorf("⏳ entry cooldown active for %s (%v remaining after stop-loss)", decision.Symbol, remaining.Round(time.Minute))
	}

	// ⚠️ Get current positions for multiple checks
	positions, err := at.trader.GetPositions()
	if err != nil {
		return fmt.Errorf("failed to get positions: %w", err)
	}

	// Check if there's already a position in the same symbol and direction
	for _, pos := range positions {
		if pos["symbol"] == decision.Symbol && pos["side"] == "short" {
			return fmt.Errorf("❌ %s already has short position, close it first", decision.Symbol)
		}
		// Opposite (long) position on the same symbol: a high-conviction short
		// signal may FLIP it (close long + open short) when trend-reversal is
		// enabled and the position is aged enough.
		if pos["symbol"] == decision.Symbol && pos["side"] == "long" {
			fd := at.evaluateFlip(decision, "short", pos)
			if fd.ShouldFlip {
				executed, err := at.executeFlipClose(decision, "short", fd)
				if err != nil {
					return fmt.Errorf("trend-reversal flip (close long) failed for %s: %w", decision.Symbol, err)
				}
				if !executed {
					return fmt.Errorf("🔄 %s flip observed (dry_run): would close long + open short", decision.Symbol)
				}
				if refreshed, rerr := at.trader.GetPositions(); rerr == nil {
					positions = refreshed
				}
			} else if fd.IsCandidate {
				// Genuine opposite-on-held reversal that did NOT flip — record the
				// near-miss before falling through to the normal capacity reject.
				at.recordFlipObservation(decision, "short", fd, false)
			}
		}
	}

	// Get current price
	marketData, err := at.getExecutionMarketData(decision.Symbol)
	if err != nil {
		return err
	}

	// [CODE ENFORCED] Capacity check with optional strong-signal replacement. When at capacity and
	// replacement is enabled, this cuts the weakest eligible position to free a slot (after the
	// entry-price deviation precheck passes), then re-fetches positions and re-enforces the limit.
	// The cut runs BEFORE the balance fetch below so freed margin is reflected downstream.
	positions, err = at.enforceCapacityWithReplacement(decision, "short", positions, marketData.CurrentPrice)
	if err != nil {
		return err
	}

	// Get balance (needed for multiple checks)
	balance, err := at.trader.GetBalance()
	if err != nil {
		return fmt.Errorf("failed to get account balance: %w", err)
	}
	availableBalance := 0.0
	if avail, ok := balance["availableBalance"].(float64); ok {
		availableBalance = avail
	}

	// Get equity for position value ratio check
	equity := 0.0
	if eq, ok := balance["totalEquity"].(float64); ok && eq > 0 {
		equity = eq
	} else if eq, ok := balance["totalWalletBalance"].(float64); ok && eq > 0 {
		equity = eq
	} else {
		equity = availableBalance
	}

	// [CODE ENFORCED] Volatility-targeted sizing: shrink size on high-ATR symbols before
	// any downstream caps so all checks see the volatility-adjusted size. No-op when disabled.
	if adj, mult := at.applyVolatilitySizing(decision.Symbol, decision.PositionSizeUSD, at.extractExecutionATR14(marketData), marketData.CurrentPrice); mult != 1.0 {
		decision.PositionSizeUSD = adj
	}

	// [CODE ENFORCED] Position Value Ratio Check: position_value <= equity × ratio
	adjustedPositionSize, wasCapped := at.enforcePositionValueRatio(decision.PositionSizeUSD, equity, decision.Symbol)
	if wasCapped {
		decision.PositionSizeUSD = adjustedPositionSize
	}

	// Auto-adjust position size if insufficient margin
	marginFactor := 1.01/float64(decision.Leverage) + 0.001
	maxAffordablePositionSize := availableBalance / marginFactor

	actualPositionSize := decision.PositionSizeUSD
	if actualPositionSize > maxAffordablePositionSize {
		adjustedSize := maxAffordablePositionSize * 0.98
		logger.Infof("  ⚠️ Position size %.2f exceeds max affordable %.2f, auto-reducing to %.2f",
			actualPositionSize, maxAffordablePositionSize, adjustedSize)
		actualPositionSize = adjustedSize
		decision.PositionSizeUSD = actualPositionSize
	}

	// [CODE ENFORCED] Minimum position size check
	if err := at.enforceMinPositionSize(decision.PositionSizeUSD, decision.Symbol); err != nil {
		return err
	}

	// [CODE ENFORCED] Entry price deviation check
	if err := enforceEntryPriceDeviationWithMax(decision, marketData.CurrentPrice, "short", at.getMaxEntryDeviationPct()); err != nil {
		return err
	}

	// [CODE ENFORCED] Leverage cap
	decision.Leverage = at.enforceLeverageCap(decision.Leverage, decision.Symbol)

	// [CODE ENFORCED] Max margin usage
	if err := at.enforceMaxMarginUsage(decision.PositionSizeUSD, decision.Leverage, equity); err != nil {
		return err
	}

	// Calculate quantity with adjusted position size
	quantity := actualPositionSize / marketData.CurrentPrice
	actionRecord.Quantity = quantity
	actionRecord.Price = marketData.CurrentPrice

	// Set margin mode
	if err := at.trader.SetMarginMode(decision.Symbol, at.config.IsCrossMargin); err != nil {
		logger.Infof("  ⚠️ Failed to set margin mode: %v", err)
		// Continue execution, doesn't affect trading
	}

	// Open position. Try a post-only maker entry first (earns maker fee); fall back to
	// a market order when unfilled (if configured) or when maker is disabled/unsupported.
	var order map[string]interface{}
	if makerResult, filled, attempted := at.tryMakerEntry(decision.Symbol, "short", quantity, decision.Leverage); attempted {
		if filled {
			order = makerResult
		} else if at.makerEntryShouldFallback() {
			logger.Infof("  ↩️ Maker entry unfilled for %s SHORT, crossing with market order", decision.Symbol)
			order, err = at.trader.OpenShort(decision.Symbol, quantity, decision.Leverage)
		} else {
			return fmt.Errorf("maker entry unfilled for %s short and market fallback disabled", decision.Symbol)
		}
	} else {
		order, err = at.trader.OpenShort(decision.Symbol, quantity, decision.Leverage)
	}
	if err != nil {
		return err
	}

	// Record order ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	logger.Infof("  ✓ Position opened successfully, order ID: %v, quantity: %.4f", order["orderId"], quantity)

	// Record order to database and poll for confirmation
	at.recordAndConfirmOrder(order, decision.Symbol, "open_short", quantity, marketData.CurrentPrice, decision.Leverage, 0)

	// Record position opening time
	posKey := decision.Symbol + "_short"
	at.positionFirstSeenTime[posKey] = time.Now().UnixMilli()

	// Wipe any stale per-position state from a prior same-symbol position before the
	// new position's protection is materialized (fix 2026-07-17 cross-position carryover).
	at.resetPerPositionStateOnOpen(decision.Symbol, "short")

	if err := at.applyPostOpenProtection(&protectionExecutionRequest{
		Symbol:       decision.Symbol,
		Action:       decision.Action,
		PositionSide: "SHORT",
		Quantity:     quantity,
		EntryPrice:   marketData.CurrentPrice,
		Decision:     decision,
	}); err != nil {
		logger.Warnf("  ❌ Protection setup failed for %s SHORT: %v", decision.Symbol, err)
		logger.Warnf("  🚨 Protection setup failed, closing position immediately: %s", decision.Symbol)
		if _, closeErr := at.trader.CloseShort(decision.Symbol, 0); closeErr != nil {
			logger.Errorf("  ❌ Failed to emergency close unprotected short %s: %v", decision.Symbol, closeErr)
			return fmt.Errorf("protection setup failed: %w; emergency close failed: %v", err, closeErr)
		}
		return fmt.Errorf("protection setup failed and position was closed: %w", err)
	}

	return nil
}

// executeCloseLongWithRecord executes close long position and records detailed information
func (at *AutoTrader) executeCloseLongWithRecord(decision *kernel.Decision, actionRecord *store.DecisionAction) error {
	logger.Infof("  🔄 Close long: %s", decision.Symbol)

	// Get current price
	marketData, err := at.getExecutionMarketData(decision.Symbol)
	if err != nil {
		return err
	}
	actionRecord.Price = marketData.CurrentPrice

	// Normalize symbol for database lookup
	normalizedSymbol := market.Normalize(decision.Symbol)

	// Get entry price and quantity - prioritize local database for accurate quantity
	var entryPrice float64
	var quantity float64

	// First try to get from local database (more accurate for quantity)
	if at.store != nil {
		if openPos, err := at.store.Position().GetOpenPositionBySymbol(at.id, normalizedSymbol, "LONG"); err == nil && openPos != nil {
			quantity = openPos.Quantity
			entryPrice = openPos.EntryPrice
			logger.Infof("  📊 Using local position data: qty=%.8f, entry=%.2f", quantity, entryPrice)
		}
	}

	// Fallback to exchange API if local data not found
	if quantity == 0 {
		positions, err := at.trader.GetPositions()
		if err == nil {
			for _, pos := range positions {
				if pos["symbol"] == decision.Symbol && pos["side"] == "long" {
					if ep, ok := pos["entryPrice"].(float64); ok {
						entryPrice = ep
					}
					if amt, ok := pos["positionAmt"].(float64); ok && amt > 0 {
						quantity = amt
					}
					break
				}
			}
		}
		logger.Infof("  📊 Using exchange position data: qty=%.8f, entry=%.2f", quantity, entryPrice)
	}

	// Close position
	order, err := at.trader.CloseLong(decision.Symbol, 0) // 0 = close all
	if err != nil {
		return err
	}

	// Record order ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	// Durable intent FIRST: the async OKX fill-sync can detect the fill during the
	// recordAndConfirmOrder polling window below. Recording the intent before that
	// poll closes the race that would otherwise mis-attribute the close as
	// sync_external instead of ai_close_long.
	at.recordCloseIntentFromOrderResult(order, decision.Symbol, "LONG", "ai_close_long", quantity)

	// Record order to database and poll for confirmation
	at.recordAndConfirmOrder(order, decision.Symbol, "close_long", quantity, marketData.CurrentPrice, 0, entryPrice)

	logger.Infof("  ✓ Position closed successfully")
	return nil
}

// executeCloseShortWithRecord executes close short position and records detailed information
func (at *AutoTrader) executeCloseShortWithRecord(decision *kernel.Decision, actionRecord *store.DecisionAction) error {
	logger.Infof("  🔄 Close short: %s", decision.Symbol)

	// Get current price
	marketData, err := at.getExecutionMarketData(decision.Symbol)
	if err != nil {
		return err
	}
	actionRecord.Price = marketData.CurrentPrice

	// Normalize symbol for database lookup
	normalizedSymbol := market.Normalize(decision.Symbol)

	// Get entry price and quantity - prioritize local database for accurate quantity
	var entryPrice float64
	var quantity float64

	// First try to get from local database (more accurate for quantity)
	if at.store != nil {
		if openPos, err := at.store.Position().GetOpenPositionBySymbol(at.id, normalizedSymbol, "SHORT"); err == nil && openPos != nil {
			quantity = openPos.Quantity
			entryPrice = openPos.EntryPrice
			logger.Infof("  📊 Using local position data: qty=%.8f, entry=%.2f", quantity, entryPrice)
		}
	}

	// Fallback to exchange API if local data not found
	if quantity == 0 {
		positions, err := at.trader.GetPositions()
		if err == nil {
			for _, pos := range positions {
				if pos["symbol"] == decision.Symbol && pos["side"] == "short" {
					if ep, ok := pos["entryPrice"].(float64); ok {
						entryPrice = ep
					}
					if amt, ok := pos["positionAmt"].(float64); ok {
						quantity = -amt // positionAmt is negative for short
					}
					break
				}
			}
		}
		logger.Infof("  📊 Using exchange position data: qty=%.8f, entry=%.2f", quantity, entryPrice)
	}

	// Close position
	order, err := at.trader.CloseShort(decision.Symbol, 0) // 0 = close all
	if err != nil {
		return err
	}

	// Record order ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	// Durable intent FIRST: see executeCloseLongWithRecord — recording before the
	// recordAndConfirmOrder poll closes the fill-sync race that would otherwise
	// mis-attribute this close as sync_external instead of ai_close_short.
	at.recordCloseIntentFromOrderResult(order, decision.Symbol, "SHORT", "ai_close_short", quantity)

	// Record order to database and poll for confirmation
	at.recordAndConfirmOrder(order, decision.Symbol, "close_short", quantity, marketData.CurrentPrice, 0, entryPrice)

	logger.Infof("  ✓ Position closed successfully")
	return nil
}
