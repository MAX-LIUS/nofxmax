package binance

import (
	"fmt"
	"nofx/logger"
	"nofx/market"
	"nofx/store"
	"nofx/trader/types"
	"sort"
	"strings"
	"sync"
	"time"
)

// syncState stores the last sync time (Unix ms) for incremental sync
var (
	binanceSyncState      = make(map[string]int64) // exchangeID -> lastSyncTimeMs (Unix ms)
	binanceSyncStateMutex sync.RWMutex
)

// SyncOrdersFromBinance syncs Binance Futures trade history to local database
// Uses COMMISSION detection + fromId for efficient incremental sync
// Also creates/updates position records to ensure orders/fills/positions data consistency
func (t *FuturesTrader) SyncOrdersFromBinance(traderID string, exchangeID string, exchangeType string, st *store.Store) error {
	if st == nil {
		return fmt.Errorf("store is nil")
	}

	orderStore := st.Order()

	// Get last sync time (Unix ms) - first try memory, then database, then default
	binanceSyncStateMutex.RLock()
	lastSyncTimeMs, exists := binanceSyncState[exchangeID]
	binanceSyncStateMutex.RUnlock()

	nowMs := time.Now().UTC().UnixMilli()
	if !exists {
		// Try to get last fill time from database (persist across restarts)
		lastFillTimeMs, err := orderStore.GetLastFillTimeByExchange(exchangeID)
		if err == nil && lastFillTimeMs > 0 {
			// If recovered time is in the future, it's clearly wrong - use default
			if lastFillTimeMs > nowMs {
				logger.Infof("⚠️ DB sync time %d is in the future (now: %d), using default",
					lastFillTimeMs, nowMs)
				lastSyncTimeMs = nowMs - 24*60*60*1000 // 24 hours ago
			} else {
				// Add 1 second buffer to avoid re-fetching the same fill
				lastSyncTimeMs = lastFillTimeMs + 1000
				logger.Infof("📅 Recovered last sync time from DB: %s (UTC)",
					time.UnixMilli(lastSyncTimeMs).UTC().Format("2006-01-02 15:04:05"))
			}
		} else {
			// First sync: go back 24 hours
			lastSyncTimeMs = nowMs - 24*60*60*1000
			logger.Infof("📅 First sync, starting from 24 hours ago: %s (UTC)",
				time.UnixMilli(lastSyncTimeMs).UTC().Format("2006-01-02 15:04:05"))
		}
	}

	logger.Infof("🔄 Syncing Binance trades from: %s (UTC) [ms: %d, now: %d]",
		time.UnixMilli(lastSyncTimeMs).UTC().Format("2006-01-02 15:04:05"), lastSyncTimeMs, nowMs)

	// Step 1: Get max trade IDs from local DB for incremental sync
	maxTradeIDs, err := orderStore.GetMaxTradeIDsByExchange(exchangeID)
	if err != nil {
		logger.Infof("  ⚠️ Failed to get max trade IDs: %v, will use time-based query", err)
		maxTradeIDs = make(map[string]int64)
	}

	// Step 2: Detect symbols to sync using multiple methods
	// COMMISSION detection may miss trades (VIP users, BNB discount, 0-fee trades)
	symbolMap := make(map[string]bool)
	lastSyncTime := time.UnixMilli(lastSyncTimeMs) // Convert to time.Time for API calls

	// Method 1: COMMISSION income detection
	commissionSymbols, err := t.GetCommissionSymbols(lastSyncTime)
	if err != nil {
		logger.Infof("  ⚠️ Failed to get commission symbols: %v", err)
	} else {
		logger.Infof("  📋 COMMISSION symbols found: %d - %v", len(commissionSymbols), commissionSymbols)
		for _, s := range commissionSymbols {
			symbolMap[s] = true
		}
	}

	// Method 2: Always include active positions (catches trades that COMMISSION missed)
	positionSymbols := t.getPositionSymbols()
	logger.Infof("  📋 Position symbols found: %d - %v", len(positionSymbols), positionSymbols)
	for _, s := range positionSymbols {
		symbolMap[s] = true
	}

	// Method 3: Include symbols from recent fills in DB (in case some were partially synced)
	recentSymbols, _ := orderStore.GetRecentFillSymbolsByExchange(exchangeID, lastSyncTimeMs)
	logger.Infof("  📋 Recent fill symbols found: %d - %v", len(recentSymbols), recentSymbols)
	for _, s := range recentSymbols {
		symbolMap[s] = true
	}

	// Method 4: ALWAYS query REALIZED_PNL income to find symbols with closed trades
	// This catches trades that COMMISSION missed (VIP users, BNB fee discount)
	// IMPORTANT: Must run always, not just when symbolMap is empty,
	// because a position might be fully closed (no active position) but have PnL
	pnlSymbols, err := t.GetPnLSymbols(lastSyncTime)
	if err != nil {
		logger.Infof("  ⚠️ Failed to get PnL symbols: %v", err)
	} else {
		logger.Infof("  📋 REALIZED_PNL symbols found: %d - %v", len(pnlSymbols), pnlSymbols)
		for _, s := range pnlSymbols {
			symbolMap[s] = true
		}
	}

	var changedSymbols []string
	for s := range symbolMap {
		changedSymbols = append(changedSymbols, s)
	}

	if len(changedSymbols) == 0 {
		logger.Infof("📭 No symbols with new trades to sync")
		// DON'T update lastSyncTime to current time here!
		// Keep using the last actual trade time from DB to avoid creating gaps
		// The lastSyncTimeMs from DB already has +1000ms buffer added
		return nil
	}

	logger.Infof("📊 Found %d symbols with new trades: %v", len(changedSymbols), changedSymbols)

	// Step 3: Query trades for changed symbols using fromId (incremental) or time-based (new symbols)
	var allTrades []types.TradeRecord
	var failedSymbols []string
	apiCalls := 0
	for _, symbol := range changedSymbols {
		var trades []types.TradeRecord
		var queryErr error

		if lastID, ok := maxTradeIDs[symbol]; ok && lastID > 0 {
			// Incremental sync: query from last known trade ID
			trades, queryErr = t.GetTradesForSymbolFromID(symbol, lastID+1, 500)
		} else {
			// New symbol or first sync: query by time
			trades, queryErr = t.GetTradesForSymbol(symbol, lastSyncTime, 500)
		}
		apiCalls++

		if queryErr != nil {
			logger.Infof("  ⚠️ Failed to get trades for %s: %v", symbol, queryErr)
			failedSymbols = append(failedSymbols, symbol)
			continue
		}
		allTrades = append(allTrades, trades...)
	}

	logger.Infof("📥 Received %d trades from Binance (%d API calls)", len(allTrades), apiCalls)

	if len(allTrades) == 0 {
		// No trades returned, but symbols were detected - might be false positive from COMMISSION/PnL detection
		// Don't update lastSyncTime, keep using DB value
		if len(failedSymbols) > 0 {
			logger.Infof("  ⚠️ %d symbols failed: %v", len(failedSymbols), failedSymbols)
		}
		return nil
	}

	// Sort trades by time ASC (oldest first) for proper position building
	sort.Slice(allTrades, func(i, j int) bool {
		return allTrades[i].Time.UnixMilli() < allTrades[j].Time.UnixMilli()
	})

	// Process trades one by one
	positionStore := st.Position()
	posBuilder := store.NewPositionBuilder(positionStore)
	syncedCount := 0

	// Per-run cache for close attribution: a single triggered order can produce
	// many fills, so dedup the GetOrder / close-intent resolution by order id.
	closeAttrCache := make(map[string]resolvedCloseOrder)

	skippedCount := 0
	for _, trade := range allTrades {
		// Check if trade already exists
		existing, err := orderStore.GetOrderByExchangeID(exchangeID, trade.TradeID)
		if err == nil && existing != nil {
			skippedCount++
			continue // Trade already exists, skip
		}

		// Normalize symbol (reverse-map exec USDC -> internal USDT first, so a
		// BTCUSDC fill is stored/keyed as BTCUSDT like the rest of the system)
		symbol := market.Normalize(toInternalSymbol(trade.Symbol))

		// Determine order action based on side and position side
		orderAction := t.determineOrderAction(trade.Side, trade.PositionSide, trade.RealizedPnL)

		// Determine position side for position builder
		positionSide := trade.PositionSide
		if positionSide == "" || positionSide == "BOTH" {
			// Infer from order action
			if strings.Contains(orderAction, "long") {
				positionSide = "LONG"
			} else {
				positionSide = "SHORT"
			}
		}

		// Normalize side
		side := strings.ToUpper(trade.Side)

		// Deterministic close attribution. Binance protection (STOP/TP/TRAILING/
		// maker-TP) fires exchange-side and returns a bare close fill; resolve the
		// real originating order type + any bot close-intent so the close event is
		// attributed to a mechanism instead of sync_external. Only for closes;
		// opens keep their bare open_long/open_short. The synthetic "MARKET" type
		// stays only when nothing resolved (today's behaviour).
		recordType := "MARKET"
		recordAction := orderAction
		recordStopPrice := 0.0
		recordClientID := ""
		recordParentID := ""
		if strings.HasPrefix(orderAction, "close_") {
			resolved := t.resolveBinanceClose(st, traderID, symbol, positionSide, trade, closeAttrCache)
			if resolved.realType != "" {
				recordType = resolved.realType
			}
			// A resolved bot mechanism (ai_close/managed_drawdown/...) is written
			// as the order action so deriveCloseReason adopts it directly. Native
			// protection (STOP/TP/TRAILING) keeps the bare close action and is
			// classified from recordType with full position context downstream.
			if resolved.reason != "" {
				recordAction = resolved.reason
			}
			recordStopPrice = resolved.stopPrice
			recordClientID = resolved.clientOrderID
			if trade.OrderID != "" {
				recordParentID = trade.OrderID
			}
		}

		// Create order record - use Unix milliseconds UTC
		tradeTimeMs := trade.Time.UTC().UnixMilli()
		orderRecord := &store.TraderOrder{
			TraderID:        traderID,
			ExchangeID:      exchangeID,
			ExchangeType:    exchangeType,
			ExchangeOrderID: trade.TradeID,
			ClientOrderID:   recordClientID,
			ParentOrderID:   recordParentID,
			Symbol:          symbol,
			Side:            side,
			PositionSide:    positionSide,
			Type:            recordType,
			StopPrice:       recordStopPrice,
			OrderAction:     recordAction,
			Quantity:        trade.Quantity,
			Price:           trade.Price,
			Status:          "FILLED",
			FilledQuantity:  trade.Quantity,
			AvgFillPrice:    trade.Price,
			Commission:      trade.Fee,
			FilledAt:        tradeTimeMs,
			CreatedAt:       tradeTimeMs,
			UpdatedAt:       tradeTimeMs,
		}

		// Insert order record
		if err := orderStore.CreateOrder(orderRecord); err != nil {
			logger.Infof("  ⚠️ Failed to sync trade %s: %v", trade.TradeID, err)
			continue
		}

		// Create fill record - use Unix milliseconds UTC
		fillRecord := &store.TraderFill{
			TraderID:        traderID,
			ExchangeID:      exchangeID,
			ExchangeType:    exchangeType,
			OrderID:         orderRecord.ID,
			ExchangeOrderID: trade.TradeID,
			ExchangeTradeID: trade.TradeID,
			Symbol:          symbol,
			Side:            side,
			Price:           trade.Price,
			Quantity:        trade.Quantity,
			QuoteQuantity:   trade.Price * trade.Quantity,
			Commission:      trade.Fee,
			CommissionAsset: "USDT",
			RealizedPnL:     trade.RealizedPnL,
			IsMaker:         false,
			CreatedAt:       tradeTimeMs,
		}

		if err := orderStore.CreateFill(fillRecord); err != nil {
			logger.Infof("  ⚠️ Failed to sync fill for trade %s: %v", trade.TradeID, err)
		}

		// Create/update position record using PositionBuilder
		if err := posBuilder.ProcessTrade(
			traderID, exchangeID, exchangeType,
			symbol, positionSide, orderAction,
			trade.Quantity, trade.Price, trade.Fee, trade.RealizedPnL,
			tradeTimeMs, trade.TradeID,
		); err != nil {
			logger.Infof("  ⚠️ Failed to sync position for trade %s: %v", trade.TradeID, err)
		} else {
			store.AttachSyncedOrderToPosition(st, orderStore, positionStore, orderRecord, traderID, symbol, positionSide, orderAction, trade.TradeID)
			logger.Infof("  📍 Position updated for trade: %s (action: %s, qty: %.6f)", trade.TradeID, orderAction, trade.Quantity)
		}

		syncedCount++
		logger.Infof("  ✅ Synced trade: %s %s %s qty=%.6f price=%.6f pnl=%.2f fee=%.6f action=%s time=%s(UTC)",
			trade.TradeID, symbol, side, trade.Quantity, trade.Price, trade.RealizedPnL, trade.Fee, orderAction,
			trade.Time.UTC().Format("01-02 15:04:05"))
	}

	// Update lastSyncTime to the LATEST trade time (not current time!)
	// This ensures next sync starts from where we left off, not from "now"
	// allTrades is already sorted by time ASC, so last element is the latest
	if len(allTrades) > 0 && len(failedSymbols) == 0 {
		latestTradeTimeMs := allTrades[len(allTrades)-1].Time.UTC().UnixMilli()
		binanceSyncStateMutex.Lock()
		binanceSyncState[exchangeID] = latestTradeTimeMs
		binanceSyncStateMutex.Unlock()
		logger.Infof("📅 Updated lastSyncTime to latest trade: %s (UTC)",
			time.UnixMilli(latestTradeTimeMs).UTC().Format("2006-01-02 15:04:05"))
	} else if len(failedSymbols) > 0 {
		logger.Infof("  ⚠️ %d symbols failed, not updating lastSyncTime to retry next time: %v", len(failedSymbols), failedSymbols)
	}

	logger.Infof("✅ Binance order sync completed: %d new trades synced, %d skipped (already exist)", syncedCount, skippedCount)
	return nil
}

// getPositionSymbols returns list of symbols that have active positions
// Used as fallback when COMMISSION detection fails
func (t *FuturesTrader) getPositionSymbols() []string {
	positions, err := t.GetPositions()
	if err != nil {
		return nil
	}

	var symbols []string
	for _, pos := range positions {
		if symbol, ok := pos["symbol"].(string); ok && symbol != "" {
			symbols = append(symbols, symbol)
		}
	}
	return symbols
}

// determineOrderAction determines the order action based on trade data
func (t *FuturesTrader) determineOrderAction(side, positionSide string, realizedPnL float64) string {
	side = strings.ToUpper(side)
	positionSide = strings.ToUpper(positionSide)

	// If there's realized PnL, it's likely a close trade
	isClose := realizedPnL != 0

	if positionSide == "LONG" || positionSide == "" {
		if side == "BUY" {
			if isClose {
				return "close_short" // Buying to close short
			}
			return "open_long"
		} else {
			if isClose {
				return "close_long" // Selling to close long
			}
			return "open_short"
		}
	} else if positionSide == "SHORT" {
		if side == "SELL" {
			if isClose {
				return "close_long"
			}
			return "open_short"
		} else {
			if isClose {
				return "close_short"
			}
			return "open_long"
		}
	}

	// Default fallback
	if side == "BUY" {
		return "open_long"
	}
	return "open_short"
}

// StartOrderSync starts background order sync task for Binance
func (t *FuturesTrader) StartOrderSync(traderID string, exchangeID string, exchangeType string, st *store.Store, interval time.Duration) {
	// Run first sync immediately
	go func() {
		logger.Infof("🔄 Running initial Binance order sync...")
		if err := t.SyncOrdersFromBinance(traderID, exchangeID, exchangeType, st); err != nil {
			logger.Infof("⚠️  Initial Binance order sync failed: %v", err)
		}
	}()

	// Then run periodically
	ticker := time.NewTicker(interval)
	go func() {
		pruneEvery := 0
		for range ticker.C {
			if err := t.SyncOrdersFromBinance(traderID, exchangeID, exchangeType, st); err != nil {
				logger.Infof("⚠️  Binance order sync failed: %v", err)
			}
			// Bound the close_intents table. Protection intents are recorded per
			// tier at open; only the fired tier is consumed, so unfired tiers must
			// be reclaimed once the position is long gone. Prune stale intents
			// older than 72h (well beyond any hold horizon) periodically.
			pruneEvery++
			if pruneEvery >= 20 && st != nil {
				pruneEvery = 0
				if ci := st.CloseIntent(); ci != nil {
					cutoff := time.Now().Add(-72 * time.Hour).UnixMilli()
					if n, err := ci.PruneStale(cutoff); err != nil {
						logger.Infof("⚠️  close_intents prune failed: %v", err)
					} else if n > 0 {
						logger.Infof("🧹 Pruned %d stale close intents (older than 72h)", n)
					}
				}
			}
		}
	}()
	logger.Infof("🔄 Binance order sync started (interval: %v)", interval)
}
