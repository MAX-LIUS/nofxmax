package trader

import (
	"math"
	"strings"
	"time"

	"nofx/logger"
	"nofx/trader/types"
)

// makerEntryCapable is the subset of exchange methods needed for post-only maker entries.
// OKX implements all of these; exchanges that don't are transparently skipped (caller falls
// back to the market-order path).
type makerEntryCapable interface {
	PlaceLimitOrder(req *types.LimitOrderRequest) (*types.LimitOrderResult, error)
	CancelOrder(symbol, orderID string) error
	GetOrderBook(symbol string, depth int) (bids, asks [][]float64, err error)
	GetOrderStatus(symbol string, orderID string) (map[string]interface{}, error)
}

// makerEntryConfig is the resolved, validated maker-entry settings.
type makerEntryConfig struct {
	enabled        bool
	timeout        time.Duration
	offsetTicks    int
	fallbackMarket bool
}

func (at *AutoTrader) resolveMakerEntryConfig() makerEntryConfig {
	cfg := makerEntryConfig{}
	if at == nil || at.config.StrategyConfig == nil {
		return cfg
	}
	rc := at.config.StrategyConfig.RiskControl
	if !rc.MakerEntryEnabled {
		return cfg
	}
	cfg.enabled = true
	timeoutSec := rc.MakerEntryTimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 15
	}
	cfg.timeout = time.Duration(timeoutSec) * time.Second
	cfg.offsetTicks = rc.MakerEntryOffsetTicks
	if cfg.offsetTicks < 0 {
		cfg.offsetTicks = 0
	}
	cfg.fallbackMarket = rc.MakerEntryFallbackMarket
	return cfg
}

// deriveTickFromBook estimates the price tick from the smallest positive gap between
// adjacent order-book levels. Returns 0 if it cannot be derived.
func deriveTickFromBook(bids, asks [][]float64) float64 {
	best := 0.0
	consider := func(levels [][]float64) {
		for i := 1; i < len(levels); i++ {
			if len(levels[i]) == 0 || len(levels[i-1]) == 0 {
				continue
			}
			gap := math.Abs(levels[i][0] - levels[i-1][0])
			if gap > 0 && (best == 0 || gap < best) {
				best = gap
			}
		}
	}
	consider(bids)
	consider(asks)
	return best
}

// makerEntryPrice computes the post-only limit price at (or just inside) the near touch.
// For a long we sit on the best bid (optionally offsetTicks below it to be safer maker);
// for a short we sit on the best ask (optionally offsetTicks above it). Returns 0 on bad book.
func makerEntryPrice(side string, bids, asks [][]float64, offsetTicks int) float64 {
	tick := deriveTickFromBook(bids, asks)
	switch strings.ToLower(side) {
	case "long", "buy":
		if len(bids) == 0 || len(bids[0]) == 0 {
			return 0
		}
		px := bids[0][0]
		if offsetTicks > 0 && tick > 0 {
			px -= float64(offsetTicks) * tick
		}
		return px
	case "short", "sell":
		if len(asks) == 0 || len(asks[0]) == 0 {
			return 0
		}
		px := asks[0][0]
		if offsetTicks > 0 && tick > 0 {
			px += float64(offsetTicks) * tick
		}
		return px
	}
	return 0
}

// tryMakerEntry attempts a post-only limit entry. Returns (orderResult, filled, attempted).
//   - attempted=false  => maker path not used (disabled / unsupported); caller uses market.
//   - attempted=true, filled=true  => orderResult holds the filled order (caller skips market).
//   - attempted=true, filled=false => caller decides: fallback to market or abort.
//
// On unfilled timeout the resting order is cancelled before returning so no stale order leaks.
func (at *AutoTrader) tryMakerEntry(symbol, side string, quantity float64, leverage int) (map[string]interface{}, bool, bool) {
	mc := at.resolveMakerEntryConfig()
	if !mc.enabled {
		return nil, false, false
	}
	ex, ok := at.trader.(makerEntryCapable)
	if !ok {
		logger.Infof("  ℹ️ Maker entry enabled but exchange lacks limit/orderbook support; using market for %s", symbol)
		return nil, false, false
	}

	bids, asks, err := ex.GetOrderBook(symbol, 5)
	if err != nil || len(bids) == 0 || len(asks) == 0 {
		logger.Warnf("  ⚠️ Maker entry: order book unavailable for %s (%v); using market", symbol, err)
		return nil, false, false
	}
	px := makerEntryPrice(side, bids, asks, mc.offsetTicks)
	if px <= 0 {
		logger.Warnf("  ⚠️ Maker entry: bad maker price for %s; using market", symbol)
		return nil, false, false
	}

	reqSide := "BUY"
	posSide := "LONG"
	if s := strings.ToLower(side); s == "short" || s == "sell" {
		reqSide = "SELL"
		posSide = "SHORT"
	}

	placed, err := ex.PlaceLimitOrder(&types.LimitOrderRequest{
		Symbol:       symbol,
		Side:         reqSide,
		PositionSide: posSide,
		Price:        px,
		Quantity:     quantity,
		Leverage:     leverage,
		PostOnly:     true,
	})
	if err != nil {
		// post_only rejection (would cross) or other placement error: treat as unfilled-attempted.
		logger.Warnf("  ⚠️ Maker entry: post-only placement failed for %s @ %.8f: %v", symbol, px, err)
		return nil, false, true
	}
	logger.Infof("  📬 Maker entry: post-only %s %s %.6f @ %.8f (orderID=%s), polling up to %s",
		reqSide, symbol, quantity, px, placed.OrderID, mc.timeout)

	deadline := time.Now().Add(mc.timeout)
	pollEvery := 1 * time.Second
	for time.Now().Before(deadline) {
		time.Sleep(pollEvery)
		st, sErr := ex.GetOrderStatus(symbol, placed.OrderID)
		if sErr != nil {
			logger.Debugf("  maker poll status err %s: %v", placed.OrderID, sErr)
			continue
		}
		status, _ := st["status"].(string)
		switch strings.ToUpper(status) {
		case "FILLED":
			logger.Infof("  ✅ Maker entry filled: %s %s orderID=%s", reqSide, symbol, placed.OrderID)
			result := map[string]interface{}{
				"orderId": placed.OrderID,
				"symbol":  symbol,
				"status":  "FILLED",
			}
			if avg, ok := st["avgPrice"].(float64); ok && avg > 0 {
				result["avgPrice"] = avg
			}
			return result, true, true
		case "CANCELED", "CANCELLED", "REJECTED", "EXPIRED":
			// Order ended early. If it partially filled before ending, treat the
			// partial as terminal (no market top-up) to avoid an oversized position —
			// under-filling is safe, over-filling is not. Exchange position sync
			// reconciles the actual recorded quantity afterward.
			if res, filled := makerPartialResult(placed.OrderID, symbol, st); filled {
				logger.Warnf("  ⚠️ Maker entry order %s ended (%s) with partial fill %.8f; accepting partial, skipping market top-up", placed.OrderID, status, res["executedQty"])
				return res, true, true
			}
			logger.Warnf("  ⚠️ Maker entry order %s ended early with status=%s (no fill)", placed.OrderID, status)
			return nil, false, true
		}
	}

	// Timeout: cancel the resting order so it cannot fill later unexpectedly.
	cancelErr := ex.CancelOrder(symbol, placed.OrderID)
	if cancelErr != nil {
		logger.Warnf("  ⚠️ Maker entry: failed to cancel unfilled order %s: %v", placed.OrderID, cancelErr)
	}
	// Re-check final state after the cancel attempt. This covers two cases:
	//   1) it fully filled in the race between timeout and cancel, or
	//   2) it partially filled before being cancelled.
	// In both cases we treat what filled as terminal and never add a market
	// top-up — under-filling is safe, over-filling risks an oversized position.
	if st, sErr := ex.GetOrderStatus(symbol, placed.OrderID); sErr == nil {
		status, _ := st["status"].(string)
		if strings.ToUpper(status) == "FILLED" {
			logger.Infof("  ✅ Maker entry filled during cancel race: %s", placed.OrderID)
			result := map[string]interface{}{"orderId": placed.OrderID, "symbol": symbol, "status": "FILLED"}
			if avg, ok := st["avgPrice"].(float64); ok && avg > 0 {
				result["avgPrice"] = avg
			}
			return result, true, true
		}
		if res, filled := makerPartialResult(placed.OrderID, symbol, st); filled {
			logger.Warnf("  ⚠️ Maker entry %s timed out with partial fill %.8f; accepting partial, skipping market top-up", placed.OrderID, res["executedQty"])
			return res, true, true
		}
	}
	logger.Infof("  ⏱ Maker entry unfilled within %s for %s; cancelled.", mc.timeout, symbol)
	return nil, false, true
}

// makerPartialResult inspects an order-status map and, if a positive executed
// quantity is present, returns a terminal order result with filled=true.
// Returning filled=true on a partial fill is intentional: the caller must NOT
// place a market order for the full quantity on top of an already-partially-filled
// maker order, which would create an oversized position. The recorded quantity is
// later reconciled by exchange position sync.
func makerPartialResult(orderID, symbol string, st map[string]interface{}) (map[string]interface{}, bool) {
	executed := 0.0
	switch v := st["executedQty"].(type) {
	case float64:
		executed = v
	case int64:
		executed = float64(v)
	}
	if executed <= 0 {
		return nil, false
	}
	result := map[string]interface{}{
		"orderId":     orderID,
		"symbol":      symbol,
		"status":      "FILLED",
		"executedQty": executed,
	}
	if avg, ok := st["avgPrice"].(float64); ok && avg > 0 {
		result["avgPrice"] = avg
	}
	return result, true
}

// makerEntryShouldFallback reports whether the caller should cross with a market order
// after an attempted-but-unfilled maker entry.
func (at *AutoTrader) makerEntryShouldFallback() bool {
	return at.resolveMakerEntryConfig().fallbackMarket
}
