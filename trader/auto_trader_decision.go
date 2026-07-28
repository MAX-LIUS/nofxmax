package trader

import (
	"fmt"
	"math"
	"nofx/kernel"
	"nofx/logger"
	"nofx/market"
	"nofx/store"
	"nofx/telemetry"
	"strings"
	"time"
)

// saveEquitySnapshot saves equity snapshot independently (for drawing profit curve, decoupled from AI decision)
func (at *AutoTrader) saveEquitySnapshot(ctx *kernel.Context) {
	if at.store == nil || ctx == nil {
		return
	}

	snapshot := &store.EquitySnapshot{
		TraderID:      at.id,
		Timestamp:     time.Now().UTC(),
		TotalEquity:   ctx.Account.TotalEquity,
		Balance:       ctx.Account.TotalEquity - ctx.Account.UnrealizedPnL,
		UnrealizedPnL: ctx.Account.UnrealizedPnL,
		PositionCount: ctx.Account.PositionCount,
		MarginUsedPct: ctx.Account.MarginUsedPct,
	}

	if err := at.store.Equity().Save(snapshot); err != nil {
		logger.Infof("⚠️ Failed to save equity snapshot: %v", err)
	}
}

// saveDecision saves AI decision log to database (only records AI input/output, for debugging)
func (at *AutoTrader) saveDecision(record *store.DecisionRecord) error {
	if at.store == nil {
		return nil
	}

	at.cycleNumber++
	record.CycleNumber = at.cycleNumber
	record.TraderID = at.id

	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now().UTC()
	}

	if err := at.store.Decision().LogDecision(record); err != nil {
		logger.Infof("⚠️ Failed to save decision record: %v", err)
		return err
	}

	logger.Infof("📝 Decision record saved: trader=%s, cycle=%d", at.id, at.cycleNumber)
	return nil
}

// GetStatus gets system status (for API)
func (at *AutoTrader) GetStatus() map[string]interface{} {
	aiProvider := "DeepSeek"
	if at.config.UseQwen {
		aiProvider = "Qwen"
	}

	at.isRunningMutex.RLock()
	isRunning := at.isRunning
	at.isRunningMutex.RUnlock()

	result := map[string]interface{}{
		"trader_id":        at.id,
		"trader_name":      at.name,
		"ai_model":         at.aiModel,
		"exchange":         at.exchange,
		"is_running":       isRunning,
		"start_time":       at.startTime.Format(time.RFC3339),
		"runtime_minutes":  int(time.Since(at.startTime).Minutes()),
		"call_count":       at.callCount,
		"initial_balance":  at.initialBalance,
		"scan_interval":    at.config.ScanInterval.String(),
		"stop_until":       at.stopUntil.Format(time.RFC3339),
		"last_reset_time":  at.lastResetTime.Format(time.RFC3339),
		"ai_provider":      aiProvider,
		"safe_mode":        at.safeMode,
		"safe_mode_reason": at.safeModeReason,
		"protect_only":     at.safeMode && strings.HasPrefix(at.safeModeReason, "protect-only"),
		"allow_ai_open":    at.GetAllowAIOpen(),
		"allow_ai_close":   at.GetAllowAIClose(),
		"ai_decision_mode": at.GetAIDecisionMode(),
	}

	// Add strategy info
	if at.config.StrategyConfig != nil {
		result["strategy_type"] = at.config.StrategyConfig.StrategyType
		if at.config.StrategyConfig.GridConfig != nil {
			result["grid_symbol"] = at.config.StrategyConfig.GridConfig.Symbol
		}
	}

	// Breadth circuit-breaker pressure indices (0–100) for the two close conditions
	// (velocity path + from-peak path). 0 = no risk; 100 = that path has reached the
	// fraction-of-positions that fires the breaker. Last computed at gbBreadthIndexAt.
	at.gbGuardMutex.Lock()
	result["breadth_vel_index"] = at.gbBreadthVelIndex
	result["breadth_peak_index"] = at.gbBreadthPeakIndex
	result["breadth_index_at"] = at.gbBreadthIndexAt
	at.gbGuardMutex.Unlock()

	return result
}

// GetAccountInfo gets account information (for API)
// GetAccountInfo (API) returns account equity/margin for the dashboard using the
// same stale-while-revalidate + singleflight pattern as GetPositions, so opening a
// dashboard never blocks on the exchange balance+positions round-trips. The trading
// loop does not use this path.
func (at *AutoTrader) GetAccountInfo() (map[string]interface{}, error) {
	at.apiReadMu.RLock()
	snap, at0 := at.apiAccountSnap, at.apiAccountAt
	at.apiReadMu.RUnlock()

	age := time.Since(at0)
	if snap != nil && age < apiReadStaleWindow {
		if age >= apiReadFreshWindow {
			go func() {
				_, _, _ = at.apiReadGroup.Do("account", func() (interface{}, error) {
					return at.fetchAccountInfo()
				})
			}()
		}
		return snap, nil
	}
	v, err, _ := at.apiReadGroup.Do("account", func() (interface{}, error) {
		return at.fetchAccountInfo()
	})
	if err != nil {
		if snap != nil {
			return snap, nil
		}
		return nil, err
	}
	return v.(map[string]interface{}), nil
}

// fetchAccountInfo computes account info from a fresh exchange balance+positions read
// and updates the API read cache. Shared by the blocking and async paths.
func (at *AutoTrader) fetchAccountInfo() (map[string]interface{}, error) {
	balance, err := at.trader.GetBalance()
	if err != nil {
		return nil, fmt.Errorf("failed to get balance: %w", err)
	}

	// Get account fields
	totalWalletBalance := 0.0
	totalUnrealizedProfit := 0.0
	availableBalance := 0.0
	totalEquity := 0.0

	if wallet, ok := balance["totalWalletBalance"].(float64); ok {
		totalWalletBalance = wallet
	}
	if unrealized, ok := balance["totalUnrealizedProfit"].(float64); ok {
		totalUnrealizedProfit = unrealized
	}
	if avail, ok := balance["availableBalance"].(float64); ok {
		availableBalance = avail
	}

	// Use totalEquity directly if provided by trader (more accurate)
	// This is already the total account value (wallet + unrealized PnL)
	if eq, ok := balance["totalEquity"].(float64); ok && eq > 0 {
		totalEquity = eq
	} else {
		// Fallback: Total Equity = Wallet balance + Unrealized profit
		// This works for exchanges like Binance where totalWalletBalance excludes unrealized PnL
		totalEquity = totalWalletBalance + totalUnrealizedProfit
	}

	// Get positions to calculate total margin
	positions, err := at.trader.GetPositions()
	if err != nil {
		return nil, fmt.Errorf("failed to get positions: %w", err)
	}

	totalMarginUsed := 0.0
	totalUnrealizedPnLCalculated := 0.0
	for _, pos := range positions {
		markPrice := pos["markPrice"].(float64)
		quantity := pos["positionAmt"].(float64)
		if quantity < 0 {
			quantity = -quantity
		}
		unrealizedPnl := pos["unRealizedProfit"].(float64)
		totalUnrealizedPnLCalculated += unrealizedPnl

		leverage := 10
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}
		marginUsed := (quantity * markPrice) / float64(leverage)
		totalMarginUsed += marginUsed
	}

	// Verify unrealized P&L consistency (API value vs calculated from positions)
	// Note: Lighter API may return 0 for unrealized PnL, this is a known limitation
	diff := math.Abs(totalUnrealizedProfit - totalUnrealizedPnLCalculated)
	if diff > 5.0 { // Only warn if difference is significant (> 5 USDT)
		logger.Infof("⚠️ Unrealized P&L inconsistency (Lighter API limitation): API=%.4f, Calculated=%.4f, Diff=%.4f",
			totalUnrealizedProfit, totalUnrealizedPnLCalculated, diff)
	}

	// Calculate total PnL excluding fund transfers (deposits/withdrawals)
	// Total PnL = current equity - (initial balance + total adjustments)
	totalAdjustments := 0.0
	if at.store != nil {
		adjustments, err := at.store.EquityAdjustment().GetTotalAdjustments(at.id)
		if err != nil {
			logger.Infof("⚠️ Failed to get equity adjustments, assuming 0: %v", err)
		} else {
			totalAdjustments = adjustments
		}
	}

	adjustedInitialBalance := at.initialBalance + totalAdjustments
	totalPnL := totalEquity - adjustedInitialBalance
	totalPnLPct := 0.0
	if adjustedInitialBalance > 0 {
		totalPnLPct = (totalPnL / adjustedInitialBalance) * 100
	} else {
		logger.Infof("⚠️ Adjusted initial balance abnormal: %.2f (initial=%.2f, adjustments=%.2f), cannot calculate P&L percentage",
			adjustedInitialBalance, at.initialBalance, totalAdjustments)
	}

	marginUsedPct := 0.0
	if totalEquity > 0 {
		marginUsedPct = (totalMarginUsed / totalEquity) * 100
	}

	acct := map[string]interface{}{
		// Core fields
		"total_equity":      totalEquity,           // Account equity = wallet + unrealized
		"wallet_balance":    totalWalletBalance,    // Wallet balance (excluding unrealized P&L)
		"unrealized_profit": totalUnrealizedProfit, // Unrealized P&L (official value from exchange API)
		"available_balance": availableBalance,      // Available balance

		// P&L statistics
		"total_pnl":       totalPnL,          // Total P&L = equity - initial
		"total_pnl_pct":   totalPnLPct,       // Total P&L percentage
		"initial_balance": at.initialBalance, // Initial balance
		"daily_pnl":       at.dailyPnL,       // Daily P&L

		// Position information
		"position_count":  len(positions),  // Position count
		"margin_used":     totalMarginUsed, // Margin used
		"margin_used_pct": marginUsedPct,   // Margin usage rate
	}

	at.apiReadMu.Lock()
	at.apiAccountSnap = acct
	at.apiAccountAt = time.Now()
	at.apiReadMu.Unlock()

	return acct, nil
}

// API-facing read-cache tuning. The dashboard tolerates a slightly stale view; the
// trading loop does NOT use this path (it calls at.trader.GetPositions directly).
const (
	apiReadFreshWindow = 8 * time.Second // within this age, serve cache with no refresh
	apiReadStaleWindow = 5 * time.Minute // older than this, block for a fresh fetch
)

// PositionsSnapshotFresh reports whether the last API positions snapshot is recent
// enough (< apiReadFreshWindow) to be safe for the write-side reconcile in the
// positions handler. Acting on a stale snapshot could wrongly close a freshly-opened
// row, so the handler skips reconciliation when this returns false.
func (at *AutoTrader) PositionsSnapshotFresh() bool {
	at.apiReadMu.RLock()
	defer at.apiReadMu.RUnlock()
	return !at.apiPositionsAt.IsZero() && time.Since(at.apiPositionsAt) < apiReadFreshWindow
}

// GetPositions (API) returns the projected position list for the dashboard using
// stale-while-revalidate + singleflight: a present snapshot is returned immediately
// (async-refreshed when older than the fresh window), so opening a dashboard never
// blocks on the exchange. Only a cold/too-stale cache blocks for one fetch.
func (at *AutoTrader) GetPositions() ([]map[string]interface{}, error) {
	at.apiReadMu.RLock()
	snap, at0 := at.apiPositionsSnap, at.apiPositionsAt
	at.apiReadMu.RUnlock()

	age := time.Since(at0)
	if snap != nil && age < apiReadStaleWindow {
		if age >= apiReadFreshWindow {
			at.refreshPositionsAsync() // stale-but-usable: refresh in background
		}
		return snap, nil
	}
	// Cold or too stale: block on a single deduped fetch.
	v, err, _ := at.apiReadGroup.Do("positions", func() (interface{}, error) {
		return at.fetchAndProjectPositions()
	})
	if err != nil {
		if snap != nil {
			return snap, nil // fall back to whatever we last had rather than erroring the UI
		}
		return nil, err
	}
	return v.([]map[string]interface{}), nil
}

// refreshPositionsAsync triggers a background refresh of the API position cache,
// deduped by singleflight so concurrent dashboards cause exactly one exchange call.
func (at *AutoTrader) refreshPositionsAsync() {
	go func() {
		_, _, _ = at.apiReadGroup.Do("positions", func() (interface{}, error) {
			return at.fetchAndProjectPositions()
		})
	}()
}

// fetchAndProjectPositions pulls fresh positions from the exchange, projects them to
// the API shape, and updates the API read cache. Shared by the blocking and async paths.
func (at *AutoTrader) fetchAndProjectPositions() ([]map[string]interface{}, error) {
	positions, err := at.trader.GetPositions()
	if err != nil {
		return nil, fmt.Errorf("failed to get positions: %w", err)
	}

	var result []map[string]interface{}
	for _, pos := range positions {
		symbol := pos["symbol"].(string)
		side := pos["side"].(string)
		entryPrice := pos["entryPrice"].(float64)
		markPrice := pos["markPrice"].(float64)
		quantity := pos["positionAmt"].(float64)
		if quantity < 0 {
			quantity = -quantity
		}
		unrealizedPnl := pos["unRealizedProfit"].(float64)
		liquidationPrice := pos["liquidationPrice"].(float64)

		leverage := 10
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}

		// Calculate margin used
		marginUsed := (quantity * markPrice) / float64(leverage)

		// Calculate P&L percentage (based on margin)
		pnlPct := calculatePnLPercentage(unrealizedPnl, marginUsed)

		openOrders, _ := at.trader.GetOpenOrders(symbol)
		positionSideUpper := strings.ToUpper(side)
		openOrders = at.enrichProtectionOrders(openOrders)
		protectionRuntime := at.buildPositionProtectionRuntime(symbol, side, quantity, entryPrice, openOrders)
		entryDecisionCycle := 0
		entryQuantity := quantity
		var entryReviewSummary map[string]interface{}
		var entryStructureAudit map[string]interface{}
		var entryTimeMs int64
		var realizedPnl float64
		var accumulatedFee float64
		if at.store != nil {
			if openPos, err := at.store.Position().GetOpenPositionBySymbol(at.id, symbol, positionSideUpper); err == nil && openPos != nil {
				entryDecisionCycle = openPos.EntryDecisionCycle
				entryTimeMs = openPos.EntryTime
				realizedPnl = openPos.RealizedPnL
				accumulatedFee = openPos.Fee
				if openPos.EntryQuantity > 0 {
					entryQuantity = openPos.EntryQuantity
				}
				if decisionStore := at.store.Decision(); decisionStore != nil {
					if record, err := decisionStore.GetRecordByCycle(at.id, openPos.EntryDecisionCycle); err == nil && record != nil {
						candidate := findMatchedDecisionAction(record, symbol, sideToOpenAction(positionSideUpper))
						decoded := extractDecisionReviewMap(func() *store.DecisionActionReviewContext {
							if candidate != nil {
								return candidate.ReviewContext
							}
							return nil
						}())
						entryReviewSummary = buildEntryReviewSummaryFromDecisionReview(decoded)
					}
				}
				if traderRecord, err := at.store.Trader().GetByID(at.id); err == nil && traderRecord != nil {
					if fullCfg, err := at.store.Trader().GetFullConfig(traderRecord.UserID, at.id); err == nil && fullCfg != nil && fullCfg.Strategy != nil {
						if parsed, err := fullCfg.Strategy.ParseConfig(); err == nil && parsed != nil {
							es := parsed.EntryStructure
							entryStructureAudit = map[string]interface{}{
								"audit_primary_timeframe":             es.AuditPrimaryTimeframe,
								"audit_adjacent_timeframes":           es.AuditAdjacentTimeframes,
								"audit_support_resistance":            es.AuditSupportResistance,
								"audit_structural_anchors":            es.AuditStructuralAnchors,
								"audit_fibonacci":                     es.AuditFibonacci,
								"require_invalidation_target_linkage": es.RequireInvalidationTargetLinkage,
							}
						}
					}
				}
			}
		}

		// Fallback: when the local DB has no matching OPEN row (e.g. exchange/local
		// sync drift, manual position, or a row not yet persisted), use the open
		// time reported by the exchange adapter so the UI still shows hold time.
		// OKX/Bybit expose "createdTime" (ms); Binance omits it (stays 0).
		if entryTimeMs == 0 {
			if ct, ok := pos["createdTime"].(int64); ok && ct > 0 {
				entryTimeMs = ct
			} else if ctf, ok := pos["createdTime"].(float64); ok && ctf > 0 {
				entryTimeMs = int64(ctf)
			}
		}

		result = append(result, map[string]interface{}{
			"symbol":                    symbol,
			"side":                      side,
			"entry_price":               entryPrice,
			"mark_price":                markPrice,
			"quantity":                  quantity,
			"entry_quantity":            entryQuantity,
			"leverage":                  leverage,
			"unrealized_pnl":            unrealizedPnl,
			"unrealized_pnl_pct":        pnlPct,
			"liquidation_price":         liquidationPrice,
			"margin_used":               marginUsed,
			"protection_state":          at.getProtectionState(symbol, side),
			"break_even_state":          at.getBreakEvenState(symbol, side),
			"drawdown_execution_mode":   at.getDrawdownExecutionMode(symbol, side),
			"break_even_execution_mode": at.getBreakEvenExecutionMode(symbol, side),
			"protection_runtime":        protectionRuntime,
			"position_side":             positionSideUpper,
			"entry_decision_cycle":      entryDecisionCycle,
			"entry_review_summary":      entryReviewSummary,
			"entry_structure_audit":     entryStructureAudit,
			"entry_time":                entryTimeMs,
			"realized_pnl":              realizedPnl,
			"fee":                       accumulatedFee,
			// net_pnl = accumulated realized (gross) + current unrealized (gross) - total fees.
			// This is the true overall result of the position including partially-closed
			// portions and all fees, which the exchange's unrealized_pnl alone omits.
			"net_pnl": realizedPnl + unrealizedPnl - accumulatedFee,
		})

		// Excursion (MFE/MAE): surface the live favorable-peak / adverse-trough profit%
		// and their open-time ATR multiples so the dashboard and reverse-lookup have the
		// running envelope, not just the current PnL. Zero-value until the first poll.
		ex := at.GetExcursion(symbol, side)
		posMap := result[len(result)-1]
		posMap["peak_pnl_pct"] = ex.PeakPnlPct
		posMap["trough_pnl_pct"] = ex.TroughPnlPct
		posMap["peak_atr_mult"] = ex.PeakAtrMult
		posMap["trough_atr_mult"] = ex.TroughAtrMult
	}

	// Update the API read cache for stale-while-revalidate serving.
	at.apiReadMu.Lock()
	at.apiPositionsSnap = result
	at.apiPositionsAt = time.Now()
	at.apiReadMu.Unlock()

	return result, nil
}

// recordAndConfirmOrder polls order status for actual fill data and records position
// action: open_long, open_short, close_long, close_short
// entryPrice: entry price when closing (0 when opening)
func (at *AutoTrader) recordAndConfirmOrder(orderResult map[string]interface{}, symbol, action string, quantity float64, price float64, leverage int, entryPrice float64) {
	if at.store == nil {
		return
	}

	// Get order ID (supports multiple types)
	var orderID string
	switch v := orderResult["orderId"].(type) {
	case int64:
		orderID = fmt.Sprintf("%d", v)
	case float64:
		orderID = fmt.Sprintf("%.0f", v)
	case string:
		orderID = v
	default:
		orderID = fmt.Sprintf("%v", v)
	}

	if orderID == "" || orderID == "0" {
		logger.Infof("  ⚠️ Order ID is empty, skipping record")
		return
	}

	// Determine positionSide
	var positionSide string
	switch action {
	case "open_long", "close_long":
		positionSide = "LONG"
	case "open_short", "close_short":
		positionSide = "SHORT"
	}

	var actualPrice = price
	var actualQty = quantity
	var fee float64

	// Exchanges with OrderSync still need a durable ownership anchor so later
	// fill sync can attribute the trade to the trader that actually placed it.
	// We store a lightweight placeholder keyed by the exchange order id.
	switch at.exchange {
	case "binance", "lighter", "hyperliquid", "bybit", "okx", "bitget", "aster", "kucoin", "gate":
		orderRecord := at.createOrderRecord(orderID, symbol, action, positionSide, quantity, price, leverage)
		if err := at.store.Order().CreateOrder(orderRecord); err != nil {
			logger.Infof("  ⚠️ Failed to anchor order owner for sync: %v", err)
		} else {
			logger.Infof("  📝 Order submitted (id: %s), owner anchored for OrderSync", orderID)
		}
		return
	}

	// For exchanges without OrderSync (e.g., Binance): record immediately and poll for fill data
	orderRecord := at.createOrderRecord(orderID, symbol, action, positionSide, quantity, price, leverage)
	if err := at.store.Order().CreateOrder(orderRecord); err != nil {
		logger.Infof("  ⚠️ Failed to record order: %v", err)
	} else {
		logger.Infof("  📝 Order recorded: %s [%s] %s", orderID, action, symbol)
	}

	// Wait for order to be filled and get actual fill data
	time.Sleep(500 * time.Millisecond)
	for i := 0; i < 5; i++ {
		status, err := at.trader.GetOrderStatus(symbol, orderID)
		if err == nil {
			statusStr, _ := status["status"].(string)
			if statusStr == "FILLED" {
				// Get actual fill price
				if avgPrice, ok := status["avgPrice"].(float64); ok && avgPrice > 0 {
					actualPrice = avgPrice
				}
				// Get actual executed quantity
				if execQty, ok := status["executedQty"].(float64); ok && execQty > 0 {
					actualQty = execQty
				}
				// Get commission/fee
				if commission, ok := status["commission"].(float64); ok {
					fee = commission
				}
				logger.Infof("  ✅ Order filled: avgPrice=%.6f, qty=%.6f, fee=%.6f", actualPrice, actualQty, fee)

				// Update order status to FILLED
				if err := at.store.Order().UpdateOrderStatus(orderRecord.ID, "FILLED", actualQty, actualPrice, fee); err != nil {
					logger.Infof("  ⚠️ Failed to update order status: %v", err)
				}

				// Record fill details
				at.recordOrderFill(orderRecord.ID, orderID, symbol, action, actualPrice, actualQty, fee)
				break
			} else if statusStr == "CANCELED" || statusStr == "EXPIRED" || statusStr == "REJECTED" {
				logger.Infof("  ⚠️ Order %s, skipping position record", statusStr)
				// Update order status
				if err := at.store.Order().UpdateOrderStatus(orderRecord.ID, statusStr, 0, 0, 0); err != nil {
					logger.Infof("  ⚠️ Failed to update order status: %v", err)
				}
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Normalize symbol for position record consistency
	normalizedSymbolForPosition := market.Normalize(symbol)

	logger.Infof("  📝 Recording position (ID: %s, action: %s, price: %.6f, qty: %.6f, fee: %.4f)",
		orderID, action, actualPrice, actualQty, fee)

	// Record position change with actual fill data (use normalized symbol)
	at.recordPositionChange(orderID, normalizedSymbolForPosition, positionSide, action, actualQty, actualPrice, leverage, entryPrice, fee)

	// Send anonymous trade statistics for experience improvement (async, non-blocking)
	// This helps us understand overall product usage across all deployments
	telemetry.TrackTrade(telemetry.TradeEvent{
		Exchange:  at.exchange,
		TradeType: action,
		Symbol:    symbol,
		AmountUSD: actualPrice * actualQty,
		Leverage:  leverage,
		UserID:    at.userID,
		TraderID:  at.id,
	})
}

// recordPositionChange records position change (create record on open, update record on close)
func (at *AutoTrader) recordPositionChange(orderID, symbol, side, action string, quantity, price float64, leverage int, entryPrice float64, fee float64) {
	if at.store == nil {
		return
	}

	switch action {
	case "open_long", "open_short":
		// Open position: create new position record
		nowMs := time.Now().UTC().UnixMilli()
		// Entry source attribution: breakout strategy vs normal AI decision. Both
		// are model/strategy initiated; the sync path uses "sync" for exchange-side
		// discoveries. ClassifyOpen maps these to canonical category/mechanism.
		entrySource := "ai_open"
		if at.lastTriggerTypes != nil && strings.Contains(strings.ToLower(at.lastTriggerTypes[symbol]), "breakout") {
			entrySource = "breakout"
		}
		pos := &store.TraderPosition{
			TraderID:           at.id,
			ExchangeID:         at.exchangeID, // Exchange account UUID
			ExchangeType:       at.exchange,   // Exchange type: binance/bybit/okx/etc
			Symbol:             symbol,
			Side:               side, // LONG or SHORT
			Quantity:           quantity,
			EntryPrice:         price,
			EntryOrderID:       orderID,
			EntryDecisionCycle: at.cycleNumber,
			EntryTime:          nowMs,
			Leverage:           leverage,
			Status:             "OPEN",
			Source:             entrySource,
			EntrySceneTags:     at.buildEntrySceneTags(symbol),
			CreatedAt:          nowMs,
			UpdatedAt:          nowMs,
		}
		if err := at.store.Position().Create(pos); err != nil {
			logger.Infof("  ⚠️ Failed to record position: %v", err)
		} else {
			logger.Infof("  📊 Position recorded [%s] %s %s @ %.4f", at.id[:8], symbol, side, price)
		}

	case "close_long", "close_short":
		// Close position using PositionBuilder for consistent handling
		// PositionBuilder will handle both cases:
		// 1. If open position exists: close it properly
		// 2. If no open position (e.g., table cleared): create a closed position record
		posBuilder := store.NewPositionBuilder(at.store.Position())
		if err := posBuilder.ProcessTrade(
			at.id, at.exchangeID, at.exchange,
			symbol, side, action,
			quantity, price, fee, 0, // realizedPnL will be calculated
			time.Now().UTC().UnixMilli(), orderID,
		); err != nil {
			logger.Infof("  ⚠️ Failed to process close position: %v", err)
		} else {
			closeReason := action
			if action == "close_long" {
				closeReason = "ai_close_long"
			} else if action == "close_short" {
				closeReason = "ai_close_short"
			}
			_ = at.store.Position().UpdateCloseReasonByExitOrderID(at.id, orderID, closeReason)
			_ = at.store.PositionClose().UpdateReasonByOrderID(at.id, orderID, closeReason, closeReason)
			logger.Infof("  ✅ Position closed [%s] %s %s @ %.4f", at.id[:8], symbol, side, price)
		}
	}
}

// buildEntrySceneTags creates a JSON string capturing the market state at entry time.
// This data feeds the evolution engine for post-trade analysis.
func (at *AutoTrader) buildEntrySceneTags(symbol string) string {
	if at.lastMarketDataMap == nil {
		return ""
	}
	data := at.lastMarketDataMap[symbol]
	if data == nil {
		return ""
	}

	phase := market.ClassifyTrendPhase(data)
	regime := market.InferExecutionRegimePublic(data)

	var ema20Dev float64
	if data.CurrentEMA20 > 0 && data.CurrentPrice > 0 {
		ema20Dev = (data.CurrentPrice - data.CurrentEMA20) / data.CurrentEMA20 * 100
	}

	triggerType := ""
	if at.lastTriggerTypes != nil {
		triggerType = at.lastTriggerTypes[symbol]
	}

	tags := fmt.Sprintf(`{"trend_phase":"%s","regime":"%s","chg4h":%.2f,"chg1h":%.2f,"ema20_dev":%.2f,"direction":"%s","trigger_type":"%s"}`,
		phase.Phase, regime, data.PriceChange4h, data.PriceChange1h, ema20Dev, phase.Direction, triggerType)
	return tags
}

// createOrderRecord creates an order record struct from order details
func (at *AutoTrader) createOrderRecord(orderID, symbol, action, positionSide string, quantity, price float64, leverage int) *store.TraderOrder {
	// Determine order type (market for auto trader)
	orderType := "MARKET"

	// Determine side (BUY/SELL)
	var side string
	switch action {
	case "open_long", "close_short":
		side = "BUY"
	case "open_short", "close_long":
		side = "SELL"
	}

	// Use action as orderAction directly (keep lowercase format)
	orderAction := action

	// Determine if it's a reduce only order
	reduceOnly := (action == "close_long" || action == "close_short")

	// Normalize symbol for consistency
	normalizedSymbol := market.Normalize(symbol)

	return &store.TraderOrder{
		TraderID:        at.id,
		ExchangeID:      at.exchangeID,
		ExchangeType:    at.exchange,
		ExchangeOrderID: orderID,
		Symbol:          normalizedSymbol,
		Side:            side,
		PositionSide:    positionSide,
		Type:            orderType,
		TimeInForce:     "GTC",
		Quantity:        quantity,
		Price:           price,
		Status:          "NEW",
		FilledQuantity:  0,
		AvgFillPrice:    0,
		Commission:      0,
		CommissionAsset: "USDT",
		Leverage:        leverage,
		ReduceOnly:      reduceOnly,
		ClosePosition:   reduceOnly,
		OrderAction:     orderAction,
		CreatedAt:       time.Now().UTC().UnixMilli(),
		UpdatedAt:       time.Now().UTC().UnixMilli(),
	}
}

// recordOrderFill records order fill/trade details
func (at *AutoTrader) recordOrderFill(orderRecordID int64, exchangeOrderID, symbol, action string, price, quantity, fee float64) {
	if at.store == nil {
		return
	}

	// Determine side (BUY/SELL)
	var side string
	switch action {
	case "open_long", "close_short":
		side = "BUY"
	case "open_short", "close_long":
		side = "SELL"
	}

	// Generate a simple trade ID (exchange doesn't always provide one)
	tradeID := fmt.Sprintf("%s-%d", exchangeOrderID, time.Now().UnixNano())

	// Normalize symbol for consistency
	normalizedSymbol := market.Normalize(symbol)

	fill := &store.TraderFill{
		TraderID:        at.id,
		ExchangeID:      at.exchangeID,
		ExchangeType:    at.exchange,
		OrderID:         orderRecordID,
		ExchangeOrderID: exchangeOrderID,
		ExchangeTradeID: tradeID,
		Symbol:          normalizedSymbol,
		Side:            side,
		Price:           price,
		Quantity:        quantity,
		QuoteQuantity:   price * quantity,
		Commission:      fee,
		CommissionAsset: "USDT",
		RealizedPnL:     0,     // Will be calculated for close orders
		IsMaker:         false, // Market orders are usually taker
		CreatedAt:       time.Now().UTC().UnixMilli(),
	}

	// Calculate realized PnL for close orders
	if action == "close_long" || action == "close_short" {
		// Try to get the entry price from the open position
		var positionSide string
		if action == "close_long" {
			positionSide = "LONG"
		} else {
			positionSide = "SHORT"
		}

		if openPos, err := at.store.Position().GetOpenPositionBySymbol(at.id, symbol, positionSide); err == nil && openPos != nil {
			if positionSide == "LONG" {
				fill.RealizedPnL = (price - openPos.EntryPrice) * quantity
			} else {
				fill.RealizedPnL = (openPos.EntryPrice - price) * quantity
			}
		}
	}

	if err := at.store.Order().CreateFill(fill); err != nil {
		logger.Infof("  ⚠️ Failed to record fill: %v", err)
	} else {
		logger.Infof("  📋 Fill recorded: %.4f @ %.6f, fee: %.4f", quantity, price, fee)
	}
}

func classifyProtectionOrderRole(order OpenOrder) string {
	kind := strings.ToUpper(order.Type)
	if strings.Contains(kind, "TRAILING") {
		return "trailing"
	}
	if looksLikeTakeProfit(order) {
		return "take_profit"
	}
	if looksLikeStopLoss(order) {
		return "stop_loss"
	}
	return "unknown"
}

func classifyProtectionOrderStatus(order OpenOrder) string {
	kind := strings.ToUpper(order.Type)
	status := strings.ToUpper(order.Status)
	if strings.Contains(kind, "TRAILING") && (order.StopPrice <= 0 && order.Price <= 0) {
		return "pending_activation"
	}
	if status == "" || status == "NEW" || status == "LIVE" || status == "OPEN" || status == "PENDING" {
		return "delegated"
	}
	return "delegated"
}

func (at *AutoTrader) enrichProtectionOrders(openOrders []OpenOrder) []OpenOrder {
	if len(openOrders) == 0 {
		return openOrders
	}
	enriched := make([]OpenOrder, 0, len(openOrders))
	for _, order := range openOrders {
		// 适配器可能已经从 clientOrderID/algoClOrdId 解出**细粒度**归因
		// (break_even / ladder_tp / ladder_sl / fallback_maxloss / structural_sl,
		// 见 okx/trader_orders.go:1519 的 reasonFromAlgoIDs)。这里原先无条件覆写成
		// classifyProtectionOrderRole 的四类粗粒度(trailing/take_profit/stop_loss/
		// unknown),把交易所侧唯一可靠的归因来源冲掉了 —— 落库和面板都只剩"这是个
		// 止损",分不清是保本、阶梯还是兜底。只在适配器没给出归因时才回退推断。
		if strings.TrimSpace(order.ProtectionRole) == "" {
			order.ProtectionRole = classifyProtectionOrderRole(order)
		}
		// ProtectionRoleCoarse 恒为四类粗粒度,给只关心"止损还是止盈"的消费者用,
		// 这样细粒度归因和粗分类可以共存,不必二选一。
		order.ProtectionRoleCoarse = classifyProtectionOrderRole(order)
		order.ProtectionStatus = classifyProtectionOrderStatus(order)
		enriched = append(enriched, order)
	}
	return enriched
}

func (at *AutoTrader) enrichProtectionOrdersWithPlan(symbol string, openOrders []OpenOrder) []OpenOrder {
	openOrders = at.enrichProtectionOrders(openOrders)
	if at == nil || at.config.StrategyConfig == nil || len(openOrders) == 0 {
		return openOrders
	}
	positions, err := at.trader.GetPositions()
	if err != nil {
		return openOrders
	}
	for _, pos := range positions {
		posSymbol, _ := pos["symbol"].(string)
		if !strings.EqualFold(posSymbol, symbol) {
			continue
		}
		side, _ := pos["side"].(string)
		entryPrice, _ := pos["entryPrice"].(float64)
		quantity, _ := pos["positionAmt"].(float64)
		if side == "" || entryPrice <= 0 || quantity == 0 {
			continue
		}
		plan, err := at.BuildConfiguredProtectionPlan(entryPrice, actionFromPositionSide(side))
		if err != nil || plan == nil {
			continue
		}
		positionSide := strings.ToUpper(side)
		for i := range openOrders {
			if openOrders[i].PositionSide != "" && !strings.EqualFold(openOrders[i].PositionSide, positionSide) {
				continue
			}
			if openOrders[i].ClientOrderID == "" {
				openOrders[i].ClientOrderID = inferProtectionClientOrderID(openOrders[i], positionSide, plan)
			}
		}
	}
	return openOrders
}

func inferProtectionClientOrderID(order OpenOrder, positionSide string, plan *ProtectionPlan) string {
	price := order.StopPrice
	if price <= 0 {
		price = order.Price
	}
	if looksLikeStopLoss(order) {
		if plan.FallbackMaxLossPrice > 0 && hasMatchingProtectionOrder([]OpenOrder{order}, positionSide, false, plan.FallbackMaxLossPrice) {
			return "fallback_maxloss_sl"
		}
		if plan.NeedsStopLoss && plan.StopLossPrice > 0 && approximatelyEqualPrice(price, plan.StopLossPrice) {
			return "full_sl"
		}
		for _, target := range plan.StopLossOrders {
			if approximatelyEqualPrice(price, target.Price) {
				return "ladder_sl"
			}
		}
	}
	if looksLikeTakeProfit(order) {
		if plan.NeedsTakeProfit && plan.TakeProfitPrice > 0 && approximatelyEqualPrice(price, plan.TakeProfitPrice) {
			return "full_tp"
		}
		for _, target := range plan.TakeProfitOrders {
			if approximatelyEqualPrice(price, target.Price) {
				return "ladder_tp"
			}
		}
	}
	return ""
}

// GetOpenOrders returns open orders (pending SL/TP) from exchange
func (at *AutoTrader) GetOpenOrders(symbol string) ([]OpenOrder, error) {
	orders, err := at.trader.GetOpenOrders(symbol)
	if err != nil {
		return nil, err
	}
	return at.enrichProtectionOrdersWithPlan(symbol, orders), nil
}

// isTierSatisfied checks if a DD tier has been satisfied (reached min_profit at some point).
// Uses persistent tier alloc state + peak PnL + live trailing presence to determine status.
// computeExchangeLight derives the true 4-colour protection status for a drawdown
// tier from the REAL exchange order state (not the peak-derived is_activated
// heuristic that the old panel used). Colours:
//
//	"blue"   — triggered / filled (drawdown threshold met while covered).
//	"green"  — healthy protection: an activated trailing order, or a resting one
//	           whose activation price the mark has NOT yet passed (will auto-fire),
//	           OR a managed in-process monitor is armed (no exchange order expected).
//	"yellow" — order present but FLAWED: no activation price, or phantom (activePx
//	           already passed but the venue never activated it → dead order).
//	"red"    — this tier SHOULD be armed (profit reached its activation) but no
//	           effective exchange order exists (missing coverage).
//
// It is deliberately conservative: anything it cannot positively confirm as
// healthy renders yellow/red so the panel surfaces the gap rather than a false
// green.
func computeExchangeLight(
	side string,
	markPrice, currentPnLPct, drawdownPct float64,
	rule store.DrawdownTakeProfitRule,
	matchedLive bool,
	matchedActivationStatus string,
	matchedActivationPrice float64,
	plannedActivationPrice float64,
	supportsNative bool,
	executionMode string,
) string {
	// Triggered/filled dominates every other state.
	if currentPnLPct >= rule.MinProfitPct && isDrawdownThresholdMet(currentPnLPct, drawdownPct, rule) {
		return "blue"
	}

	// Managed / local-monitor modes carry protection in-process — there is no
	// exchange order to inspect, so a healthy managed arm is green (the exchange-
	// failed variant is surfaced separately via exchange_order_failed → the panel
	// already renders that reverse-colour warning).
	modeLower := strings.ToLower(executionMode)
	if strings.Contains(modeLower, "managed") {
		// The in-process managed monitor IS actively protecting (peak-tracked
		// giveback close every poll). That is healthy protection → green. The
		// exchange_failed variant differs only in that no exchange order backs it;
		// the panel surfaces that distinction as a GREEN FLASHING tile (via the
		// exchange_order_failed flag), not as an alarming yellow. Both are green
		// here because both close promptly.
		return "green"
	}

	// Venues without native trailing rely purely on the code-side monitor; treat an
	// armed tier as green (managed) and an un-reached tier as green-waiting too.
	if !supportsNative {
		return "green"
	}

	if !matchedLive {
		// No matching exchange order. Under place-at-open, EVERY tier should carry a
		// resting order from the moment the position opens (each at its own activePx,
		// safest when price is furthest away). So a missing order is a genuine gap —
		// the monitor re-fills it on the next poll. Surface it as red (missing) rather
		// than a soft "waiting": there is no legitimate steady state where a configured
		// tier has no exchange order. (Brief red between detection and re-fill is
		// honest and self-heals.)
		return "red"
	}

	// A live order matched this tier. Judge its effectiveness.
	if strings.EqualFold(matchedActivationStatus, "activated") {
		// "activated" WITH an activation price is genuine protection (green).
		// "activated" WITHOUT one is profit-dependent: at/above this tier's profit
		// floor it is a DELIBERATE immediate-trail we place on purpose for a tier
		// whose activePx the mark already passed (peak anchored in profit → green,
		// safe). BELOW the floor it is the dangerous near-entry immediate-active order
		// that mis-closes on any retrace (yellow; the danger reconciler cancels it).
		if matchedActivationPrice > 0 {
			return "green"
		}
		if currentPnLPct >= rule.MinProfitPct {
			return "green"
		}
		return "yellow"
	}
	// Use the ORDER's own activation price — do NOT fall back to the planned value
	// here: a present order whose own activation price is missing is flawed (the
	// user-facing "no activation price" yellow), and masking it with the plan would
	// hide exactly the defect this light exists to surface.
	activePx := matchedActivationPrice
	if activePx <= 0 {
		// Order present but no activation anchor → flawed.
		return "yellow"
	}
	if markPrice <= 0 {
		// Cannot evaluate reachability; don't claim green.
		return "yellow"
	}
	// Resting order: green while activePx not yet passed (will auto-activate);
	// phantom (activePx already passed, not activated) → yellow.
	passed := false
	if strings.EqualFold(side, "long") {
		passed = markPrice >= activePx
	} else {
		passed = markPrice <= activePx
	}
	if passed {
		return "yellow" // phantom: passed activation but venue never activated
	}
	return "green" // resting, waiting for activation — healthy
}

// computeExchangeLightReason sub-classifies a YELLOW exchange light so the panel
// can show a precise warning. It mirrors the decision points inside computeExchange-
// Light but only for the yellow cases; it returns "" for non-yellow lights. The
// three yellow reasons carry very different urgency:
//
//   - "no_activation": a live order is present but its activation price is <=0. On
//     OKX this is the immediately-active order that trails from the current price
//     and WILL mis-close on any retrace — the most urgent flaw. The danger
//     reconciler cancels and re-places it with the correct anchor.
//   - "exchange_failed": the re-arm breaker tripped or placement failed; the local
//     managed monitor is active with NO exchange order (cannot mis-close).
//   - "phantom": activation price set but mark already passed it, so the venue never
//     activated the order — it is dead (no protection) but cannot mis-close.
func computeExchangeLightReason(
	side string,
	markPrice float64,
	exchangeLight, executionMode string,
	matchedLive bool,
	matchedActivationStatus string,
	matchedActivationPrice float64,
) string {
	if exchangeLight != "yellow" {
		return ""
	}
	if strings.Contains(strings.ToLower(executionMode), "exchange_failed") {
		return "exchange_failed"
	}
	// A live order that is "activated" with no activation price is the dangerous
	// immediate-activation kind (mis-closes). Also covers a matched resting order
	// whose own activation price is missing.
	if matchedLive && matchedActivationPrice <= 0 {
		return "no_activation"
	}
	if !matchedLive {
		// Unreachable via computeExchangeLight: every one of its yellow branches sits
		// behind matchedLive==true, and !matchedLive returns RED (a missing order is a
		// genuine gap under place-at-open, never a soft yellow). Kept as a defensive
		// classification rather than a "not_placed" reason string the panel would have
		// to render: an absent order should surface as red/missing, and inventing a
		// yellow sub-reason for it would contradict the light itself.
		return "absent_order_unexpected_yellow"
	}
	if markPrice <= 0 {
		// computeExchangeLight also yellows when the mark is unusable ("cannot evaluate
		// reachability; don't claim green"). That is NOT a phantom — a phantom is a
		// positive finding that the venue skipped activation. Falling through to
		// "phantom" here would tell the user an order is dead when the truth is only
		// that we could not judge it.
		return "unknown_mark"
	}
	// Matched order with a positive activation price that mark has passed → phantom.
	return "phantom"
}

func isTierSatisfied(ruleIdx int, currentPnLPct, minProfitPct float64, allocs []store.DrawdownTierAllocation, hasLiveTrailing bool) bool {
	// Check persistent tier alloc state first (doesn't flip back once tracking)
	for _, a := range allocs {
		if a.TierIndex == ruleIdx || a.MinProfitPct == minProfitPct {
			if a.Status == "tracking" || a.Status == "executed" || a.Status == "superseded" {
				return true
			}
			break
		}
	}
	// If there's a live trailing order on exchange, the lowest tier (idx 0) is definitely satisfied.
	// Trailing orders are only placed after profit reaches the tier's activation threshold.
	if hasLiveTrailing && ruleIdx == 0 {
		return true
	}
	// Fallback to real-time check
	return currentPnLPct >= minProfitPct
}

func (at *AutoTrader) buildPositionProtectionRuntime(symbol, side string, quantity, entryPrice float64, openOrders []OpenOrder) map[string]interface{} {
	positionSide := strings.ToUpper(side)
	currentPnLPct := 0.0
	peakPnLPct := 0.0
	drawdownPct := 0.0
	markPrice, _ := at.getPositionMarkPrice(symbol, side)

	// ATR context for the UI: frozen value at entry (used to convert ATR-multiple
	// thresholds to prices) and the current live ATR (shows volatility drift).
	atrAtEntry := 0.0
	currentATR := 0.0
	atrTimeframe := ""
	if at.config.StrategyConfig != nil {
		acfg := at.config.StrategyConfig.ATRProtection
		if acfg.Enabled {
			atrTimeframe = acfg.WithDefaults().Timeframe
			if v, ok := at.frozenATRForPosition(symbol, side, entryPrice, acfg); ok {
				atrAtEntry = v
			}
			if v, ok := at.atrForProtection(symbol, acfg); ok {
				currentATR = v
			}
		}
	}

	if entryPrice > 0 && markPrice > 0 {
		currentPnLPct = calculatePositionPnLPct(side, entryPrice, markPrice)
		peakPnLPct = currentPnLPct
		at.peakPnLCacheMutex.RLock()
		if peak, ok := at.peakPnLCache[positionKey(symbol, side)]; ok && peak > peakPnLPct {
			peakPnLPct = peak
		}
		at.peakPnLCacheMutex.RUnlock()
		if peakPnLPct > 0 && currentPnLPct < peakPnLPct {
			drawdownPct = ((peakPnLPct - currentPnLPct) / peakPnLPct) * 100
		}
	}

	// Structural stop-loss (range-anchored) surface for the UI. Two price levels:
	//   Phase 2 boundary  — the frozen pre-entry swing edge; a bar CLOSE beyond it
	//                       triggers the tight structural close (the real "structural
	//                       stop"). Read-only here (allowCompute=false) so the panel
	//                       never fabricates a boundary — it shows exactly what the
	//                       guard enforces, or nothing when none was frozen.
	//   Phase 1 backstop  — the wide resting exchange stop (entry ∓ BackstopATRMul×ATR)
	//                       that covers bot downtime / catastrophic gaps.
	// 结构位两级统一走 resolveStructuralSLLevels(structural_sl_levels.go)—— 开仓
	// 快照也要落这两个价位,两处各算一遍必然漂移。
	structLevels := at.resolveStructuralSLLevels(symbol, side, entryPrice, atrAtEntry)
	structuralSLEnabled := structLevels.Enabled
	structuralCloseConfirm := structLevels.CloseConfirm
	structuralBoundaryPrice := structLevels.BoundaryPrice
	structuralBackstopPrice := structLevels.BackstopPrice
	structuralFloorATRMul := structLevels.FloorATRMul
	structuralBackstopATRMul := structLevels.BackstopMul

	// Time / max-hold forced-close conditions (no fixed price — condition-based).
	timeStopHours := 0.0
	timeStopLossPct := 0.0
	maxHoldHours := 0.0
	maxHoldProfitExemptPct := 0.0
	if at.config.StrategyConfig != nil {
		rc := at.config.StrategyConfig.RiskControl
		timeStopHours = rc.TimeStopHours
		timeStopLossPct = rc.TimeStopLossPct
		maxHoldHours = rc.MaxHoldHours
		maxHoldProfitExemptPct = rc.MaxHoldProfitExemptPct
	}

	be := at.getActiveBreakEvenConfigForPlan(nil)
	breakEvenTrigger := 0.0
	breakEvenOffset := 0.0
	breakEvenSuppressedByRunner := at.isBreakEvenSuppressedByRunner(symbol, side)
	nextBreakEvenGap := 0.0
	breakEvenSource := at.getBreakEvenConfigSource(symbol, side)
	if be == nil {
		breakEvenSource = "none"
	}
	if be != nil {
		breakEvenTrigger = be.TriggerValue
		breakEvenOffset = be.OffsetPct
		nextBreakEvenGap = be.TriggerValue - currentPnLPct
		if nextBreakEvenGap < 0 {
			nextBreakEvenGap = 0
		}
	}
	if breakEvenSuppressedByRunner {
		nextBreakEvenGap = 0
	}

	drawdownRules := at.getActiveDrawdownRulesForPosition(symbol, side)
	if len(drawdownRules) == 0 {
		// Only restore AI-baked drawdown rules from the entry decision when drawdown
		// take-profit is actually enabled. Otherwise the disabled feature leaks into
		// telemetry ("Drawdown arm pending …") and is misleading (fix 2026-06-07).
		if at.config.StrategyConfig != nil && at.config.StrategyConfig.Protection.DrawdownTakeProfit.Enabled {
			drawdownRules = at.restoreAIDrawdownRulesForPositionWithEntry(symbol, side, entryPrice)
		}
	}
	// Resolve ATR-unit min-profit / max-drawdown distances to effective percent
	// using the position's frozen open-time ATR, exactly as the runtime arming
	// path does (auto_trader_risk.go). Without this the displayed activation /
	// callback would treat ATR multiples as raw percents (e.g. 3 ATR shown as
	// +3% activation, 1.2 ATR shown as 1.2% callback), diverging from the live
	// exchange order which is placed with ATR-resolved values.
	drawdownRules = at.resolveDrawdownRulesATR(drawdownRules, symbol, side, entryPrice)
	drawdownSource := at.getDrawdownConfigSource(symbol, side)
	runnerState := at.getDrawdownRunnerState(symbol, side)
	drawdownCfg := store.DrawdownTakeProfitConfig{}
	if at.config.StrategyConfig != nil {
		drawdownCfg = at.config.StrategyConfig.Protection.DrawdownTakeProfit
	}
	armRules := at.getDrawdownArmRules(currentPnLPct, entryPrice, quantity, symbol, side, drawdownRules)
	currentStageMinProfit := 0.0
	currentStageRuleCount := 0
	if len(armRules) > 0 {
		currentStageMinProfit = armRules[0].MinProfitPct
		currentStageRuleCount = len(armRules)
	}

	plannedLadderStopCount := 0
	plannedLadderTakeProfitCount := 0
	fullStopPlanned := false
	fullTakeProfitPlanned := false
	fallbackPlanned := false
	unexpectedSummary := unexpectedProtectionSummary{}
	plannedLadderOrders := map[string]interface{}{}
	var configuredPlan *ProtectionPlan
	if plan, err := at.BuildConfiguredProtectionPlan(entryPrice, "open_"+strings.ToLower(side)); err == nil && plan != nil {
		configuredPlan = plan
		plannedLadderStopCount = len(plan.StopLossOrders)
		plannedLadderTakeProfitCount = len(plan.TakeProfitOrders)
		plannedLadderOrders = map[string]interface{}{
			"stop_loss":   plan.StopLossOrders,
			"take_profit": plan.TakeProfitOrders,
		}
		fullStopPlanned = plan.NeedsStopLoss && plan.StopLossPrice > 0
		fullTakeProfitPlanned = plan.NeedsTakeProfit && plan.TakeProfitPrice > 0
		fallbackPlanned = plan.FallbackMaxLossPrice > 0
		breakEvenArmed := at.getBreakEvenState(symbol, side) == "armed"
		// 必须含 *_arming 两个状态,与 reconciler(protection_reconciler.go:175)保持
		// 一致:nativeTrailingOwnership.Armed 的语义就是"armed 或 arming"(见该类型的
		// 注释)。这里少两个状态会让面板在武装窗口内把在场的 trailing 单归成
		// "非我认领",与 reconciler 同一时刻的判断相反。
		nativeTrailingArmed := isNativeTrailingProtectionState(at.getProtectionState(symbol, side))
		unexpectedSummary = classifyUnexpectedProtectionOrders(openOrders, positionSide, plan,
			at.breakEvenOwnershipForPosition(symbol, side, breakEvenArmed),
			at.nativeTrailingOwnershipForPosition(symbol, side, entryPrice, nativeTrailingArmed), true)
	}

	activeOrders := make([]map[string]interface{}, 0)
	trailingOrders := make([]map[string]interface{}, 0)
	liveTrailingTriggerPrice := 0.0
	liveTrailingCallbackRate := 0.0
	// 在场 trailing 单张数。多档 trailing 常驻多张,而 runner migration 只想比较
	// "runner 那一档"的在场单;张数 >1 时 (trigger, callback) 这对标量无法确定
	// 属于哪一档,migration 必须拒绝动作而不是猜。
	liveTrailingCandidateCount := 0
	liveBreakEvenStopPrice := 0.0
	breakEvenOrderDetected := false
	ladderStopCount := 0
	ladderTakeProfitCount := 0
	fullStopCount := 0
	fullTakeProfitCount := 0
	fallbackStopCount := 0
	maxProtectionOrderQuantity := 0.0
	maxProtectionOrderQuantityOrderID := ""
	protectionQuantityDrift := false
	protectionQuantityDriftReason := ""
	protectionQuantityDriftOrders := make([]map[string]interface{}, 0)
	quantityTolerance := math.Max(0.00000001, quantity*0.02)
	for _, order := range openOrders {
		if order.PositionSide != "" && !strings.EqualFold(order.PositionSide, positionSide) {
			continue
		}
		triggerPrice := order.StopPrice
		if triggerPrice <= 0 {
			triggerPrice = order.Price
		}
		clientOrderID := strings.ToLower(strings.TrimSpace(order.ClientOrderID))
		if clientOrderID == "" && configuredPlan != nil {
			if inferred := inferProtectionClientOrderID(order, positionSide, configuredPlan); inferred != "" {
				order.ClientOrderID = inferred
				clientOrderID = strings.ToLower(inferred)
			}
		}
		if !breakEvenOrderDetected && looksLikeStopLoss(order) {
			if strings.Contains(clientOrderID, "break_even") || strings.Contains(clientOrderID, "breakeven") {
				breakEvenOrderDetected = true
				if triggerPrice > 0 {
					liveBreakEvenStopPrice = triggerPrice
				}
			}
		}
		if strings.Contains(strings.ToUpper(order.Type), "TRAILING") {
			// 激活价与 callback 必须**成对**取自同一张单。原来两个 if 各自独立赋值,
			// 在多档 trailing(一个仓位现在常驻 2~4 张)下会拼出一对根本不属于同一张
			// 单的 (trigger, callback):trigger 来自最后一张有 trigger 的单,callback
			// 来自最后一张有 callback 的单。这对幻影参数随后驱动 runner migration 的
			// 漂移计算和 would_loosen_protection 安全判定 —— 用不存在的单去判断
			// "换单会不会放松保护",结论无意义。
			// 仍是 last-wins(交易所返回顺序无保证),但至少是一张真实存在的单;
			// 下面 runnerMigrationLiveTrailingCount 记录候选张数,供歧义时拒绝动作。
			if triggerPrice > 0 && order.CallbackRate > 0 {
				liveTrailingTriggerPrice = triggerPrice
				liveTrailingCallbackRate = order.CallbackRate
			}
			liveTrailingCandidateCount++
			trailingOrders = append(trailingOrders, map[string]interface{}{
				"order_id":          order.OrderID,
				"type":              order.Type,
				"side":              order.Side,
				"position_side":     order.PositionSide,
				"trigger_price":     triggerPrice,
				"callback_rate":     order.CallbackRate,
				"quantity":          order.Quantity,
				"status":            order.Status,
				"client_order_id":   order.ClientOrderID,
				"activation_status": order.ActivationStatus,
				"activation_price":  order.ActivationPrice,
			})
		}
		// 这里的计数只关心"止损还是止盈"，所以必须读**粗粒度**字段:
		// ProtectionRole 现在保留适配器解出的细粒度归因(break_even / ladder_sl /
		// fallback_maxloss / structural_sl …)，直接 switch "stop_loss" 会全部落空。
		role := strings.ToLower(strings.TrimSpace(order.ProtectionRoleCoarse))
		if role == "" {
			role = strings.ToLower(strings.TrimSpace(order.ProtectionRole))
		}
		clientOrderIDLower := strings.ToLower(strings.TrimSpace(order.ClientOrderID))
		switch role {
		case "stop_loss":
			switch {
			case strings.Contains(clientOrderIDLower, "fallback_maxloss"):
				fallbackStopCount++
			case strings.Contains(clientOrderIDLower, "ladder"):
				ladderStopCount++
			case strings.Contains(clientOrderIDLower, "full"):
				fullStopCount++
			}
		case "take_profit":
			switch {
			case strings.Contains(clientOrderIDLower, "ladder"):
				ladderTakeProfitCount++
			case strings.Contains(clientOrderIDLower, "full"):
				fullTakeProfitCount++
			}
		}
		if (looksLikeStopLoss(order) || strings.Contains(strings.ToUpper(order.Type), "TRAILING")) && order.Quantity > 0 {
			if order.Quantity > maxProtectionOrderQuantity {
				maxProtectionOrderQuantity = order.Quantity
				maxProtectionOrderQuantityOrderID = order.OrderID
			}
			if quantity > 0 && order.Quantity > quantity+quantityTolerance {
				protectionQuantityDrift = true
				if protectionQuantityDriftReason == "" {
					protectionQuantityDriftReason = "protection_order_quantity_exceeds_position"
				}
				protectionQuantityDriftOrders = append(protectionQuantityDriftOrders, map[string]interface{}{
					"order_id":          order.OrderID,
					"client_order_id":   order.ClientOrderID,
					"type":              order.Type,
					"quantity":          order.Quantity,
					"position_quantity": quantity,
					"excess_quantity":   order.Quantity - quantity,
				})
			}
		}
		activeOrders = append(activeOrders, map[string]interface{}{
			"order_id":          order.OrderID,
			"type":              order.Type,
			"side":              order.Side,
			"position_side":     order.PositionSide,
			"trigger_price":     triggerPrice,
			"callback_rate":     order.CallbackRate,
			"quantity":          order.Quantity,
			"status":            order.Status,
			"client_order_id":   order.ClientOrderID,
			// 两个归因都发:protection_role 是细粒度(保本/阶梯/兜底/结构位),
			// protection_role_coarse 是四类粗粒度,面板按前者展示、按后者归类。
			"protection_role":        order.ProtectionRole,
			"protection_role_coarse": order.ProtectionRoleCoarse,
			"protection_status":      order.ProtectionStatus,
		})
	}

	tiers := make([]map[string]interface{}, 0)
	tierAllocs := at.getDrawdownTierAllocs(symbol, side)
	structureCtx := at.buildDrawdownStructureContext(symbol, side)
	currentStructureStage := ""
	currentStructureStopSource := ""
	currentStructureTargetSource := ""
	currentStructureTargetProgress := 0.0
	currentStructurePrimaryTf := ""
	currentStructureEvidence := []string{}
	currentStructureTrace := []string{}
	currentStructureHealth := "unstructured"
	currentStructureDriftReason := ""
	currentStructureDetached := false
	if drawdownCfg.Enabled && drawdownCfg.Mode == store.ProtectionModeAI && drawdownCfg.EngineMode == store.DrawdownEngineModeAI {
		stage, stopSource, targetSource := classifyAIDrawdownStage(currentPnLPct, peakPnLPct, structureCtx, side, markPrice)
		currentStructureStage = stage
		currentStructureStopSource = stopSource
		currentStructureTargetSource = targetSource
		if structureCtx != nil {
			currentStructureTargetProgress = structuralTargetProgress(side, structureCtx.Entry, structureCtx.FirstTarget, markPrice)
			currentStructurePrimaryTf = structureCtx.PrimaryTimeframe
			currentStructureEvidence = summarizeDrawdownStructureEvidence(structureCtx, side)
			currentStructureTrace = append(currentStructureTrace,
				fmt.Sprintf("tf=%s", currentStructurePrimaryTf),
				fmt.Sprintf("stage=%s", currentStructureStage),
				fmt.Sprintf("progress=%.2f", currentStructureTargetProgress),
				fmt.Sprintf("stop_source=%s", currentStructureStopSource),
				fmt.Sprintf("target_source=%s", currentStructureTargetSource),
			)
			currentStructureHealth = "aligned"
		}
	}
	if len(drawdownRules) > 0 {
		for idx, rule := range drawdownRules {
			rule = normalizeDrawdownRule(rule)
			if rule.MinProfitPct <= 0 || rule.MaxDrawdownPct <= 0 || rule.CloseRatioPct <= 0 {
				continue
			}
			executionMode := at.getDrawdownExecutionMode(symbol, side)
			source := executionMode
			// 归属判断统一走谓词,不再用字面量白名单 —— 详见 protection_reconciler.go
			// 里 drawdownExecutionModeIsNative 的注释(白名单漏掉 *_tiers 曾让双档仓位
			// 的 DD1/DD2 恒红灯)。
			if drawdownExecutionModeIsNative(executionMode) {
				source = "native"
			} else if drawdownExecutionModeIsManaged(executionMode) {
				source = "managed"
			}
			plannedActivationPrice := 0.0
			if entryPrice > 0 {
				move := rule.MinProfitPct / 100.0
				if strings.EqualFold(side, "long") {
					plannedActivationPrice = entryPrice * (1 + move)
				} else if strings.EqualFold(side, "short") {
					plannedActivationPrice = entryPrice * (1 - move)
				}
			}
			activationPrice := plannedActivationPrice
			callbackRate := calculateDrawdownRuleCallbackRatio(entryPrice, side, rule)
			activationSource := "planned"
			callbackSource := "planned"
			plannedQty := quantity * rule.CloseRatioPct / 100.0
			matchedLive := false
			matchedActivationStatus := ""
			matchedActivationPrice := 0.0
			// 同上:任何 native_* 归属都必须走这段"找回交易所上那张单"的匹配。
			// 漏掉任何一个 native 值的后果不是少一点信息,而是 matchedLive 恒 false →
			// exchange_light 直接红灯,把健康的挂单报成缺单。
			if drawdownExecutionModeIsNative(executionMode) {
				activationSource = "request"
				callbackSource = "request"
				// matchCallback 是**给模糊匹配用的**交易所单位形态:binance/bitget 的
				// API 收发 callbackRate 用百分数(1.8 = 1.8%),okx 等用比率(0.018)。
				// 注意它只能留在匹配逻辑里 —— 早先这里直接把 callbackRate 本身乘了
				// 100,于是 tier JSON 里 "callback_rate" 的单位随交易所而变,前端只能
				// 靠 ">1 就当百分数" 猜。这个猜法在 dd% < 1 时静默错 100 倍
				// (dd=0.54% → 传 0.54 → 前端当成 54% 回撤),trigger 价直接算飞。
				// 现在 callbackRate 全程保持比率,单位歧义从根上去掉。
				matchCallback := callbackRatioToExchangeUnit(at.exchange, callbackRate)
				applyMatch := func(order map[string]interface{}) {
					trVal, _ := order["trigger_price"].(float64)
					cbVal, _ := order["callback_rate"].(float64)
					if trVal > 0 {
						activationPrice = trVal
						activationSource = "exchange"
					}
					if cbVal > 0 {
						// 交易所回读值也要归一到比率。binance/bitget 的 OpenOrder.CallbackRate
						// 是百分数形态(与下单时同单位);okx 适配器已在 trader_orders.go:1514
						// 归一成比率。不归一就会把 1.8% 当成 180% 存进 tier。
						callbackRate = callbackExchangeUnitToRatio(at.exchange, cbVal)
						callbackSource = "exchange"
					}
					matchedActivationStatus, _ = order["activation_status"].(string)
					matchedActivationPrice, _ = order["activation_price"].(float64)
					matchedLive = true
				}
				// ID-first matching: every tier persists its placement's exchange
				// orderID (OKX algoId / Binance algoId) keyed by RuleFingerprint. Match
				// on that first — it is collision-free across concurrent multi-tier
				// orders (dd1 + partial resting together), unlike qty+callback fuzzy
				// matching which cannot tell sibling tiers apart. Only fall back to
				// fuzzy matching when no stored ID exists (legacy / pre-restart orders).
				if wantID := at.storedTrailingOrderIDForRule(symbol, side, entryPrice, rule); wantID != "" {
					for _, order := range trailingOrders {
						if oid, _ := order["order_id"].(string); oid != "" && oid == wantID {
							applyMatch(order)
							break
						}
					}
					// Stored ID present but order gone → this tier is genuinely
					// missing. Do NOT fuzzy-match onto a sibling tier's order.
				} else {
					for _, order := range trailingOrders {
						qtyVal, _ := order["quantity"].(float64)
						cbVal, _ := order["callback_rate"].(float64)
						qtyTolerance := math.Max(0.0001, plannedQty*0.1)
						callbackTolerance := 0.0002
						if strings.ToLower(at.exchange) == "binance" || strings.ToLower(at.exchange) == "bitget" {
							callbackTolerance = 0.05
						}
						// 比的是交易所单位形态(matchCallback),不是归一后的 callbackRate。
						if plannedQty > 0 && math.Abs(qtyVal-plannedQty) <= qtyTolerance && math.Abs(cbVal-matchCallback) <= callbackTolerance {
							applyMatch(order)
							break
						}
					}
				}
				if !matchedLive {
					// Keep per-tier planned values — don't override with first live order's global values
					activationSource = "planned"
					callbackSource = "planned"
				}
			}
			// exchange_light: the TRUE 4-colour status driven by the real exchange
			// order state (not the peak-derived is_activated heuristic). Semantics:
			//   red    = no order for this tier on the exchange (should be armed, isn't)
			//   yellow = order present but flawed: no activation price, or phantom
			//            (activePx already passed but venue never activated → dead)
			//   green  = healthy: activated, or resting with activePx not yet reached
			//   blue   = triggered / filled
			exchangeLight := computeExchangeLight(
				side, markPrice, currentPnLPct, drawdownPct,
				rule, matchedLive, matchedActivationStatus, matchedActivationPrice,
				activationPrice, at.supportsNativeTrailingStop(), executionMode)
			anchor := (*drawdownTierAnchor)(nil)
			if structureCtx != nil {
				anchor = structureCtx.selectTierAnchor(side, rule, entryPrice)
			}
			legacyWarning := ""
			nativeRejectedReason := ""
			if rule.MaxDrawdownPct > 0 && rule.MaxDrawdownPct < 5 && rule.MaxDrawdownAbsPct <= 0 {
				legacyWarning = "max_drawdown_pct is below 5 under peak-profit giveback semantics; likely legacy absolute-profit value"
			}
			if entryPrice > 0 && rule.MinProfitPct > 0 && strings.Contains(strings.ToLower(executionMode), "managed") {
				cb := calculateDrawdownRuleCallbackRatio(entryPrice, side, rule)
				if cb > 0 && cb < minNativeDrawdownCallbackRatio {
					nativeRejectedReason = fmt.Sprintf("native callback %.6f below safety floor %.6f; managed fallback preserves rule math", cb, minNativeDrawdownCallbackRatio)
				}
			}
			tier := map[string]interface{}{
				"index":                       idx + 1,
				"stage_name":                  rule.StageName,
				"timeframe":                   rule.Timeframe,
				"reason_anchor":               rule.ReasonAnchor,
				"min_profit_pct":              rule.MinProfitPct,
				"max_drawdown_pct":            rule.MaxDrawdownPct,
				"max_drawdown_abs_profit_pct": rule.MaxDrawdownAbsPct,
				"close_ratio_pct":             rule.CloseRatioPct,
				"runner_keep_pct":             rule.RunnerKeepPct,
				"runner_stop_mode":            rule.RunnerStopMode,
				"runner_stop_source":          rule.RunnerStopSource,
				"runner_target_mode":          rule.RunnerTargetMode,
				"runner_target_source":        rule.RunnerTargetSource,
				"activation_price":            activationPrice,
				"planned_activation_price":    plannedActivationPrice,
				"activation_source":           activationSource,
				// callback_rate 恒为**比率**(0.018 = 1.8%),不随交易所变单位。
				"callback_rate":   callbackRate,
				"callback_source": callbackSource,
				// execution_price:这一档真正成交的价位 = 峰值 ×(1 ∓ callback)。
				// 激活价只是"开始跟踪"的门槛,到激活价不成交任何东西 —— 面板按价格
				// 排序必须用这个成交价,否则一个"3ATR 启动/1.8ATR 回吐"的档会被排到
				// 3ATR 的位置,而它实际成交在 +1.2ATR(该排在 1.1/1.7 两个阶梯之间)。
				// 详见 drawdown_execution_price.go。
				"execution_price": drawdownTierExecutionPrice(side, activationPrice,
					peakPriceFromPnLPct(side, entryPrice, peakPnLPct), callbackRate),
				"planned_quantity":            quantity * rule.CloseRatioPct / 100.0,
				"source":                      source,
				"execution_mode":              executionMode,
				// exchange_order_failed: the native trailing order could not be placed
				// (or was cancelled after read-back divergence) and protection dropped
				// to the LOCAL managed-drawdown monitor. Drives the panel's
				// reverse-colour warning so profit is never silently left on a
				// mis-registered (immediate-triggering) exchange order.
				"exchange_order_failed": executionMode == "managed_drawdown_exchange_failed",
				// exchange_light: real 4-colour status from actual exchange order state.
				"exchange_light": exchangeLight,
				// exchange_light_reason: sub-classifies a yellow light so the panel can
				// warn precisely. "no_activation" = a live order is present but has NO
				// activation price (activated immediately → WILL mis-close on any retrace;
				// the danger reconciler cancels + re-places it). "phantom" = activePx set
				// but mark already passed it so the venue never activated (dead, provides
				// no protection, but cannot mis-close). "exchange_failed" = re-arm breaker
				// tripped / placement failed, local managed monitor active. Empty for
				// non-yellow lights.
				"exchange_light_reason": computeExchangeLightReason(
					side, markPrice, exchangeLight, executionMode,
					matchedLive, matchedActivationStatus, matchedActivationPrice),
				// is_armed: a trailing order for this tier exists on the exchange
				// (or a managed tier is tracking) — i.e. protection is in place but
				// not necessarily activated. is_activated: the peak profit actually
				// reached the activation threshold so the trailing stop is live and
				// tracking the peak. The two are distinct: an order can rest on the
				// exchange (armed) long before price reaches activation (activated).
				"is_armed":     matchedLive || isTierSatisfied(idx, currentPnLPct, rule.MinProfitPct, tierAllocs, len(trailingOrders) > 0),
				"is_activated": peakPnLPct >= rule.MinProfitPct,
				// is_satisfied now means genuinely activated (drives the green
				// "已激活" dot); placement-only state shows as "已布单/待满足".
				"is_satisfied":                      peakPnLPct >= rule.MinProfitPct,
				"is_triggered":                      currentPnLPct >= rule.MinProfitPct && isDrawdownThresholdMet(currentPnLPct, drawdownPct, rule),
				"legacy_drawdown_semantics_warning": legacyWarning,
				"native_trailing_rejected_reason":   nativeRejectedReason,
			}
			if anchor != nil {
				tier["structure_anchor"] = anchor
				tier["anchor_timeframe"] = anchor.Timeframe
				tier["anchor_price"] = anchor.Price
				tier["anchor_source"] = anchor.Source
			}
			tiers = append(tiers, tier)
		}
	}

	// 面板红灯必须在日志里留痕。
	//
	// DD1/DD2 恒红灯那个 bug(见 protection_reconciler.go 里 drawdownExecutionModeIsNative
	// 的注释)在整个存活期内**日志一片干净** —— reconciler 报 state=protected
	// verified=true dynamicOwner=2 claimedTrail=2,红灯只存在于 HTTP 响应里,而
	// protection_runtime 从不落库。于是它只能靠人盯着面板发现,发现了也无从回溯。
	//
	// 红灯的语义是"这一档在交易所上没有单",这本身就是需要动作的事件,频率天然很低
	// (健康时恒为 0 条)。把它打出来:既让"面板红 vs reconciler 正常"这种自相矛盾的
	// 状态在同一份日志里对齐,也让事后能查"什么时候开始红的"。
	if len(tiers) > 0 {
		redTiers := make([]string, 0, len(tiers))
		for _, tier := range tiers {
			if light, _ := tier["exchange_light"].(string); light != "red" {
				continue
			}
			stage, _ := tier["stage_name"].(string)
			idx, _ := tier["index"].(int)
			redTiers = append(redTiers, fmt.Sprintf("dd%d/%s", idx, stage))
		}
		if len(redTiers) > 0 {
			logger.Warnf("🔴 Protection panel: %s %s drawdown tiers report RED (no exchange order matched): %s | mode=%s state=%s trailingOrdersOnExchange=%d",
				symbol, positionSide, strings.Join(redTiers, ","),
				at.getDrawdownExecutionMode(symbol, side), at.getProtectionState(symbol, side), len(trailingOrders))
		}
	}

	ladderDegradedStop := plannedLadderStopCount > 0 && ladderStopCount < plannedLadderStopCount
	ladderDegradedTakeProfit := plannedLadderTakeProfitCount > 0 && ladderTakeProfitCount < plannedLadderTakeProfitCount
	ladderDegradedToFullStop := ladderDegradedStop && fullStopCount > 0
	ladderDegradedToFullTakeProfit := ladderDegradedTakeProfit && fullTakeProfitCount > 0
	fallbackActive := fallbackStopCount > 0
	runnerMigrationNeeded := false
	runnerMigrationReason := ""
	runnerMigrationAnchor := (*drawdownTierAnchor)(nil)
	runnerMigrationDesiredActivation := 0.0
	runnerMigrationDesiredCallback := 0.0
	runnerMigrationLiveActivation := 0.0
	runnerMigrationLiveCallback := 0.0
	runnerMigrationSafe := false
	runnerMigrationSafetyReason := ""
	runnerMigrationWouldLoosenProtection := false
	runnerMigrationWouldTightenProtection := false
	runnerMigrationActionable := false
	runnerMigrationActionableReason := ""
	runnerMigrationPlan := map[string]interface{}{}
	if currentStructureStage == "higher_timeframe_runner" && structureCtx != nil && len(drawdownRules) > 0 {
		var runnerRule *store.DrawdownTakeProfitRule
		for i := range drawdownRules {
			rule := normalizeDrawdownRule(drawdownRules[i])
			if rule.RunnerKeepPct > 0 || strings.Contains(strings.ToLower(rule.StageName), "runner") {
				runnerRule = &rule
				break
			}
		}
		if runnerRule == nil {
			rule := normalizeDrawdownRule(drawdownRules[len(drawdownRules)-1])
			runnerRule = &rule
		}
		if runnerRule != nil {
			runnerMigrationAnchor = structureCtx.selectTierAnchor(side, *runnerRule, entryPrice)
			runnerMigrationDesiredActivation = calculateProfitBasedTrailingTriggerPrice(entryPrice, side, runnerRule.MinProfitPct)
			runnerMigrationDesiredCallback = calculateProfitBasedTrailingCallbackRatio(entryPrice, side, runnerRule.MinProfitPct, runnerRule.MaxDrawdownPct)
			runnerMigrationLiveActivation = liveTrailingTriggerPrice
			runnerMigrationLiveCallback = liveTrailingCallbackRate
			if runnerMigrationAnchor == nil || runnerMigrationAnchor.Timeframe == "" {
				runnerMigrationNeeded = true
				runnerMigrationReason = "missing_higher_runner_anchor"
			} else if liveTrailingTriggerPrice <= 0 {
				runnerMigrationNeeded = true
				runnerMigrationReason = "missing_live_trailing"
			} else {
				activationDrift := math.Abs(liveTrailingTriggerPrice-runnerMigrationDesiredActivation) / math.Max(runnerMigrationDesiredActivation, 1)
				callbackDrift := 0.0
				if runnerMigrationDesiredCallback > 0 {
					callbackDrift = math.Abs(liveTrailingCallbackRate-runnerMigrationDesiredCallback) / runnerMigrationDesiredCallback
				}
				if activationDrift > 0.003 || callbackDrift > 0.20 {
					runnerMigrationNeeded = true
					runnerMigrationReason = "live_trailing_differs_from_higher_runner_plan"
				}
			}
			if runnerMigrationNeeded && liveTrailingTriggerPrice > 0 && runnerMigrationDesiredActivation > 0 && runnerMigrationDesiredCallback > 0 {
				liveTrailDistance := liveTrailingTriggerPrice * liveTrailingCallbackRate
				desiredTrailDistance := runnerMigrationDesiredActivation * runnerMigrationDesiredCallback
				runnerMigrationWouldLoosenProtection = desiredTrailDistance > liveTrailDistance
				runnerMigrationWouldTightenProtection = desiredTrailDistance < liveTrailDistance
				if runnerMigrationAnchor != nil && runnerMigrationAnchor.Price > 0 && runnerMigrationDesiredCallback >= minNativeDrawdownCallbackRatio {
					if runnerMigrationWouldLoosenProtection {
						runnerMigrationSafe = false
						runnerMigrationSafetyReason = "would_loosen_live_trailing"
					} else {
						runnerMigrationSafe = true
						runnerMigrationSafetyReason = "tightens_or_preserves_live_trailing"
					}
				}
			}
		}
	}
	if runnerMigrationNeeded {
		switch {
		case !runnerMigrationSafe:
			runnerMigrationActionableReason = "migration_not_safe"
		case runnerMigrationAnchor == nil || runnerMigrationAnchor.Price <= 0:
			runnerMigrationActionableReason = "missing_higher_runner_anchor"
		case runnerMigrationLiveActivation <= 0 || runnerMigrationLiveCallback <= 0:
			runnerMigrationActionableReason = "missing_live_trailing"
		case runnerMigrationDesiredActivation <= 0 || runnerMigrationDesiredCallback < minNativeDrawdownCallbackRatio:
			runnerMigrationActionableReason = "invalid_desired_trailing_plan"
		default:
			runnerMigrationActionable = true
			runnerMigrationActionableReason = "manual_replace_ready"
		}
		if runnerMigrationActionable {
			// 判定"撤哪张"抽到 resolveRunnerMigrationTarget(见 runner_migration_target.go):
			// 多张在场 trailing ⇒ 歧义拒绝;精确匹配不上 ⇒ 拒绝而不是随便挑一张。
			target := resolveRunnerMigrationTarget(
				trailingOrders, runnerMigrationLiveActivation, runnerMigrationLiveCallback,
				liveTrailingCandidateCount,
			)
			if !target.Resolved {
				runnerMigrationActionable = false
				runnerMigrationActionableReason = target.Reason
			} else {
				cancelQuantity := target.Quantity
				if cancelQuantity <= 0 {
					cancelQuantity = quantity
				}
				// 只有确实认出了要撤的那张单才发计划:带空/错 cancel_order_id 的
				// replace_native_trailing 计划比没有计划更危险 —— 它看起来是可执行的。
				runnerMigrationPlan = map[string]interface{}{
					"action":                 "replace_native_trailing",
					"cancel_order_id":        target.OrderID,
					"cancel_client_order_id": target.ClientOrderID,
					"new_activation":         runnerMigrationDesiredActivation,
					"new_callback":           runnerMigrationDesiredCallback,
					"quantity":               math.Min(cancelQuantity, quantity),
					"requires_confirmation":  true,
				}
			}
		}
	}
	if currentStructureHealth == "aligned" && runnerMigrationNeeded {
		currentStructureHealth = "runner_migration_needed"
		currentStructureDriftReason = runnerMigrationReason
	}
	if currentStructureHealth == "aligned" {
		switch {
		case ladderDegradedStop || ladderDegradedTakeProfit:
			currentStructureHealth = "partially_degraded"
			currentStructureDriftReason = "ladder_degraded"
		case ladderDegradedToFullStop || ladderDegradedToFullTakeProfit || fallbackActive:
			currentStructureHealth = "degraded_to_full_fallback"
			currentStructureDriftReason = "degraded_to_full_fallback"
		}
	}
	if len(currentStructureEvidence) == 0 {
		currentStructureDetached = true
		if currentStructureHealth == "aligned" || currentStructureHealth == "unstructured" {
			currentStructureHealth = "structure_detached"
			if currentStructureDriftReason == "" {
				currentStructureDriftReason = "missing_structure_context"
			}
		}
	}

	orphanProtectionCleanupNeeded := false
	orphanProtectionOrderCount := 0
	if quantity <= 0 && len(activeOrders) > 0 {
		orphanProtectionCleanupNeeded = true
		orphanProtectionOrderCount = len(activeOrders)
	}

	return map[string]interface{}{
		"protection_state":                at.getProtectionState(symbol, side),
		"break_even_state":                at.getBreakEvenState(symbol, side),
		"break_even_suppressed_by_runner": breakEvenSuppressedByRunner,
		"drawdown_runner_mode_active":     runnerState != nil,
		"drawdown_runner_stage_name": func() string {
			if runnerState != nil {
				return runnerState.StageName
			}
			return ""
		}(),
		"drawdown_runner_keep_pct": func() float64 {
			if runnerState != nil {
				return runnerState.RunnerKeepPct
			}
			return 0
		}(),
		"drawdown_runner_stop_mode": func() string {
			if runnerState != nil {
				return runnerState.RunnerStopMode
			}
			return ""
		}(),
		"drawdown_runner_stop_source": func() string {
			if runnerState != nil {
				return runnerState.RunnerStopSource
			}
			return ""
		}(),
		"drawdown_runner_target_mode": func() string {
			if runnerState != nil {
				return runnerState.RunnerTargetMode
			}
			return ""
		}(),
		"drawdown_runner_target_source": func() string {
			if runnerState != nil {
				return runnerState.RunnerTargetSource
			}
			return ""
		}(),
		"drawdown_structure_stage":             currentStructureStage,
		"drawdown_structure_stop_source":       currentStructureStopSource,
		"drawdown_structure_target_source":     currentStructureTargetSource,
		"drawdown_structure_target_progress":   currentStructureTargetProgress,
		"drawdown_structure_primary_timeframe": currentStructurePrimaryTf,
		"drawdown_structure_higher_timeframes": func() []string {
			if structureCtx != nil {
				return structureCtx.HigherTimeframes
			}
			return nil
		}(),
		"drawdown_structure_anchors": func() []store.DecisionActionReasonAnchor {
			if structureCtx != nil {
				return structureCtx.Anchors
			}
			return nil
		}(),
		"drawdown_structure_evidence":         currentStructureEvidence,
		"drawdown_structure_trace":            currentStructureTrace,
		"structure_protection_health":         currentStructureHealth,
		"structure_protection_drift_reason":   currentStructureDriftReason,
		"structure_protection_detached":       currentStructureDetached,
		"protection_quantity_drift":           protectionQuantityDrift,
		"protection_quantity_drift_reason":    protectionQuantityDriftReason,
		"protection_position_quantity":        quantity,
		"protection_max_order_quantity":       maxProtectionOrderQuantity,
		"protection_max_order_id":             maxProtectionOrderQuantityOrderID,
		"protection_quantity_drift_orders":    protectionQuantityDriftOrders,
		"orphan_protection_cleanup_needed":    orphanProtectionCleanupNeeded,
		"orphan_protection_order_count":       orphanProtectionOrderCount,
		"runner_migration_needed":             runnerMigrationNeeded,
		"runner_migration_reason":             runnerMigrationReason,
		"runner_migration_anchor":             runnerMigrationAnchor,
		"runner_migration_desired_activation": runnerMigrationDesiredActivation,
		"runner_migration_desired_callback":   runnerMigrationDesiredCallback,
		"runner_migration_live_activation":    runnerMigrationLiveActivation,
		"runner_migration_live_callback":      runnerMigrationLiveCallback,
		"runner_migration_safe":               runnerMigrationSafe,
		"runner_migration_safety_reason":      runnerMigrationSafetyReason,
		"runner_migration_would_loosen":       runnerMigrationWouldLoosenProtection,
		"runner_migration_would_tighten":      runnerMigrationWouldTightenProtection,
		"runner_migration_actionable":         runnerMigrationActionable,
		"runner_migration_actionable_reason":  runnerMigrationActionableReason,
		"runner_migration_plan":               runnerMigrationPlan,
		"drawdown_execution_mode":             at.getDrawdownExecutionMode(symbol, side),
		"drawdown_config_source":              drawdownSource,
		"break_even_execution_mode":           at.getBreakEvenExecutionMode(symbol, side),
		"current_pnl_pct":                     currentPnLPct,
		"drawdown_peak_pnl_pct":               peakPnLPct,
		"current_drawdown_pct":                drawdownPct,
		"atr_at_entry":                        atrAtEntry,
		"current_atr":                         currentATR,
		"atr_timeframe":                       atrTimeframe,
		"current_break_even_trigger_pct":      breakEvenTrigger,
		"break_even_offset_pct":               breakEvenOffset,
		"next_break_even_gap_pct":             nextBreakEvenGap,
		"break_even_config_source":            breakEvenSource,
		"live_break_even_stop_price":          liveBreakEvenStopPrice,
		"break_even_order_detected":           breakEvenOrderDetected,
		"planned_ladder_stop_count":           plannedLadderStopCount,
		"planned_ladder_take_profit_count":    plannedLadderTakeProfitCount,
		"planned_ladder_orders":               plannedLadderOrders,
		"live_ladder_stop_count":              ladderStopCount,
		"live_ladder_take_profit_count":       ladderTakeProfitCount,
		"live_full_stop_count":                fullStopCount,
		"live_full_take_profit_count":         fullTakeProfitCount,
		"fallback_order_detected":             fallbackActive,
		"live_fallback_stop_count":            fallbackStopCount,
		"full_stop_planned":                   fullStopPlanned,
		"full_take_profit_planned":            fullTakeProfitPlanned,
		"fallback_planned":                    fallbackPlanned,
		"unexpected_protection": map[string]interface{}{
			"stale_bot_duplicate_count":     unexpectedSummary.StaleBotDuplicate,
			"orphan_inactive_count":         unexpectedSummary.OrphanForInactive,
			"manual_or_foreign_count":       unexpectedSummary.ManualOrForeign,
			"expected_dynamic_owner_count":  unexpectedSummary.ExpectedDynamicOwner,
			// 成分拆分:trailing(回撤止盈)vs 已推保本的止损。总数混计两者,单看总数
			// 读不出是哪种多了 —— 多一张 trailing 会真的多平仓,多一张保本止损是正常的。
			"expected_dynamic_trailing_count": unexpectedSummary.ExpectedDynamicTrailing,
			"expected_dynamic_stop_count":     unexpectedSummary.ExpectedDynamicStop,
			"expected_static_owner_count":     unexpectedSummary.ExpectedStaticOwner,
			"stale_bot_duplicate_order_ids": unexpectedSummary.StaleBotDuplicateIDs,
			"orphan_inactive_order_ids":     unexpectedSummary.OrphanForInactiveIDs,
			"manual_or_foreign_order_ids":   unexpectedSummary.ManualOrForeignIDs,
		},
		"ladder_stop_degraded":                  ladderDegradedStop,
		"ladder_take_profit_degraded":           ladderDegradedTakeProfit,
		"ladder_stop_degraded_to_full":          ladderDegradedToFullStop,
		"ladder_take_profit_degraded_to_full":   ladderDegradedToFullTakeProfit,
		"current_drawdown_stage_min_profit_pct": currentStageMinProfit,
		"current_drawdown_stage_rule_count":     currentStageRuleCount,
		"active_orders":                         activeOrders,
		"active_trailing_orders":                trailingOrders,
		"scheduled_tiers":                       tiers,
		// Structural stop-loss (range-anchored) surface — auto-populated when enabled.
		"structural_sl_enabled":       structuralSLEnabled,
		"structural_close_confirm":    structuralCloseConfirm,
		"structural_boundary_price":   structuralBoundaryPrice,
		"structural_backstop_price":   structuralBackstopPrice,
		"structural_floor_atr_mul":    structuralFloorATRMul,
		"structural_backstop_atr_mul": structuralBackstopATRMul,
		// Time / max-hold forced-close conditions (condition-based, no fixed price).
		"time_stop_hours":            timeStopHours,
		"time_stop_loss_pct":         timeStopLossPct,
		"max_hold_hours":             maxHoldHours,
		"max_hold_profit_exempt_pct": maxHoldProfitExemptPct,
	}
}
