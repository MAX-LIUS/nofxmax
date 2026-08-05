package gate

import (
	"fmt"
	"nofx/trader/types"
	"strconv"
	"time"

	"github.com/antihax/optional"
	"github.com/gateio/gateapi-go/v6"
)

// GetBalance retrieves account balance
func (t *GateTrader) GetBalance() (map[string]interface{}, error) {
	// Check cache
	t.balanceCacheMutex.RLock()
	if t.cachedBalance != nil && time.Since(t.balanceCacheTime) < t.cacheDuration {
		cached := t.cachedBalance
		t.balanceCacheMutex.RUnlock()
		return cached, nil
	}
	t.balanceCacheMutex.RUnlock()

	// Fetch from API
	accounts, _, err := t.client.FuturesApi.ListFuturesAccounts(t.ctx, "usdt")
	if err != nil {
		return nil, fmt.Errorf("failed to get balance: %w", err)
	}

	total, _ := strconv.ParseFloat(accounts.Total, 64)
	available, _ := strconv.ParseFloat(accounts.Available, 64)
	unrealizedPnl, _ := strconv.ParseFloat(accounts.UnrealisedPnl, 64)

	// Gate.io 'Total' is total equity (including unrealized PnL)
	// Calculate wallet balance to maintain consistency with other exchanges
	walletBalance := total - unrealizedPnl

	result := map[string]interface{}{
		"totalEquity":           total,         // Total equity INCLUDING unrealized PnL
		"totalWalletBalance":    walletBalance, // Wallet balance EXCLUDING unrealized PnL
		"availableBalance":      available,
		"totalUnrealizedProfit": unrealizedPnl,
	}

	// Update cache
	t.balanceCacheMutex.Lock()
	t.cachedBalance = result
	t.balanceCacheTime = time.Now()
	t.balanceCacheMutex.Unlock()

	return result, nil
}

// GetPositions retrieves all open positions
func (t *GateTrader) GetPositions() ([]map[string]interface{}, error) {
	// Check cache
	t.positionsCacheMutex.RLock()
	if t.cachedPositions != nil && time.Since(t.positionsCacheTime) < t.cacheDuration {
		cached := t.cachedPositions
		t.positionsCacheMutex.RUnlock()
		return cached, nil
	}
	t.positionsCacheMutex.RUnlock()

	// Fetch from API
	positions, _, err := t.client.FuturesApi.ListPositions(t.ctx, "usdt", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get positions: %w", err)
	}

	var result []map[string]interface{}
	for _, pos := range positions {
		if pos.Size == 0 {
			continue // Skip empty positions
		}

		entryPrice, _ := strconv.ParseFloat(pos.EntryPrice, 64)
		markPrice, _ := strconv.ParseFloat(pos.MarkPrice, 64)
		liqPrice, _ := strconv.ParseFloat(pos.LiqPrice, 64)
		unrealizedPnl, _ := strconv.ParseFloat(pos.UnrealisedPnl, 64)

		// Gate leverage semantics differ from okx/binance: `leverage == "0"` means
		// CROSS margin and the effective multiplier lives in `cross_leverage_limit`.
		// A positive `leverage` means ISOLATED margin. Reporting the raw 0 upstream
		// made every cross position look like 0x, and the generic layer guards on
		// `lev > 0` (auto_trader_replace.go:93, auto_trader_risk.go:4575), so a 0
		// silently disabled leverage-aware sizing and risk math.
		leverage, _ := strconv.ParseFloat(pos.Leverage, 64)
		mgnMode := "isolated"
		if leverage == 0 {
			mgnMode = "cross"
			if crossLev, err := strconv.ParseFloat(pos.CrossLeverageLimit, 64); err == nil && crossLev > 0 {
				leverage = crossLev
			}
		}

		// Gate returns position size in contracts, need to convert to base currency
		// Each contract = quanto_multiplier base currency
		contractSize := float64(pos.Size)
		if pos.Size < 0 {
			contractSize = float64(-pos.Size)
		}

		// Get quanto_multiplier from contract info to convert contracts to actual quantity
		quantoMultiplier := 1.0
		contract, err := t.getContract(pos.Contract)
		if err == nil && contract != nil {
			qm, _ := strconv.ParseFloat(contract.QuantoMultiplier, 64)
			if qm > 0 {
				quantoMultiplier = qm
			}
		}

		// Convert contract count to actual token quantity
		positionAmt := contractSize * quantoMultiplier

		// Determine side. In dual-position (hedge) mode Gate reports the direction in
		// `mode` (dual_long / dual_short) and `size` is always positive for the long
		// leg, so the size sign alone is not authoritative there. Prefer `mode`, fall
		// back to the size sign for single-position mode.
		side := "long"
		switch pos.Mode {
		case "dual_long":
			side = "long"
		case "dual_short":
			side = "short"
		default:
			if pos.Size < 0 {
				side = "short"
			}
		}

		result = append(result, map[string]interface{}{
			// MUST be the internal symbol (BTCUSDT), not Gate's native BTC_USDT.
			// The whole protection stack keys positions/orders by this string; an
			// unconverted contract name never matches the orders returned by
			// GetOpenOrders (which does revert), leaving gate positions invisible
			// to the reconciler.
			"symbol":           t.revertSymbol(pos.Contract),
			"positionAmt":      positionAmt,
			"entryPrice":       entryPrice,
			"markPrice":        markPrice,
			"unRealizedProfit": unrealizedPnl,
			// float64, matching okx/binance. The generic layer type-asserts
			// pos["leverage"].(float64) in 6 places; an int would fail every one.
			"leverage":         leverage,
			"liquidationPrice": liqPrice,
			"side":             side,
			"mgnMode":          mgnMode,
			"createdTime":      pos.OpenTime * 1000, // Gate returns seconds; generic layer expects ms
			"updatedTime":      pos.UpdateTime * 1000,
		})
	}

	// Update cache
	t.positionsCacheMutex.Lock()
	t.cachedPositions = result
	t.positionsCacheTime = time.Now()
	t.positionsCacheMutex.Unlock()

	return result, nil
}

// GetClosedPnL retrieves closed position PnL records
func (t *GateTrader) GetClosedPnL(startTime time.Time, limit int) ([]types.ClosedPnLRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 100 {
		limit = 100
	}

	opts := &gateapi.ListPositionCloseOpts{
		Limit: optional.NewInt32(int32(limit)),
		From:  optional.NewInt64(startTime.Unix()),
	}

	closedPositions, _, err := t.client.FuturesApi.ListPositionClose(t.ctx, "usdt", opts)
	if err != nil {
		return nil, fmt.Errorf("failed to get closed positions: %w", err)
	}

	records := make([]types.ClosedPnLRecord, 0, len(closedPositions))
	for _, pos := range closedPositions {
		pnl, _ := strconv.ParseFloat(pos.Pnl, 64)

		record := types.ClosedPnLRecord{
			Symbol:      t.revertSymbol(pos.Contract),
			Side:        pos.Side,
			RealizedPnL: pnl,
			ExitTime:    time.Unix(int64(pos.Time), 0).UTC(),
			CloseType:   "unknown",
		}

		records = append(records, record)
	}

	return records, nil
}
