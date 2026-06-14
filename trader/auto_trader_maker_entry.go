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
			logger.Warnf("  ⚠️ Maker entry order %s ended early with status=%s", placed.OrderID, status)
			return nil, false, true
		}
	}

	// Timeout: cancel the resting order so it cannot fill later unexpectedly.
	if cErr := ex.CancelOrder(symbol, placed.OrderID); cErr != nil {
		logger.Warnf("  ⚠️ Maker entry: failed to cancel unfilled order %s: %v", placed.OrderID, cErr)
		// Re-check: it may have filled in the race between timeout and cancel.
		if st, sErr := ex.GetOrderStatus(symbol, placed.OrderID); sErr == nil {
			if status, _ := st["status"].(string); strings.ToUpper(status) == "FILLED" {
				logger.Infof("  ✅ Maker entry filled during cancel race: %s", placed.OrderID)
				return map[string]interface{}{"orderId": placed.OrderID, "symbol": symbol, "status": "FILLED"}, true, true
			}
		}
	}
	logger.Infof("  ⏱ Maker entry unfilled within %s for %s; cancelled.", mc.timeout, symbol)
	return nil, false, true
}

// makerEntryShouldFallback reports whether the caller should cross with a market order
// after an attempted-but-unfilled maker entry.
func (at *AutoTrader) makerEntryShouldFallback() bool {
	return at.resolveMakerEntryConfig().fallbackMarket
}
