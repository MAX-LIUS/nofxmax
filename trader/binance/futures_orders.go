package binance

import (
	"context"
	"fmt"
	"math"
	"nofx/logger"
	"nofx/trader/types"
	"strconv"
	"strings"

	"github.com/adshao/go-binance/v2/futures"
)

func (t *FuturesTrader) SetTrailingStopLoss(symbol string, positionSide string, activationPrice float64, callbackRate float64, quantity float64) error {
	_, err := t.setTrailingStopLossCore(symbol, positionSide, activationPrice, callbackRate, quantity, "")
	return err
}

// SetTrailingStopLossTaggedWithID places a native trailing stop whose ClientAlgoId
// encodes the close mechanism (via clientIDForReason), so when Binance triggers the
// trailing stop the resulting fill decodes its own mechanism 1:1 through the coded
// client id — bringing Binance native-trailing attribution to OKX parity. Without
// this, trailing fills carry a plain broker id, decode to "", and fall through to
// heuristic guessing (which mislabeled them ladder_tp / dumped them to
// sync_external). Returns the exchange algoId so the caller can cancel it precisely.
func (t *FuturesTrader) SetTrailingStopLossTaggedWithID(symbol string, positionSide string, activationPrice float64, callbackRate float64, quantity float64, reasonTag string) (string, error) {
	return t.setTrailingStopLossCore(symbol, positionSide, activationPrice, callbackRate, quantity, reasonTag)
}

func (t *FuturesTrader) setTrailingStopLossCore(symbol string, positionSide string, activationPrice float64, callbackRate float64, quantity float64, reasonTag string) (string, error) {
	symbol = t.toExecSymbol(symbol) // internal USDT -> exec (USDC when applicable)
	var side futures.SideType
	var posSide futures.PositionSideType

	if positionSide == "LONG" {
		side = futures.SideTypeSell
		posSide = futures.PositionSideTypeLong
	} else {
		side = futures.SideTypeBuy
		posSide = futures.PositionSideTypeShort
	}

	// Encode the mechanism when a reason is given (falls back to a plain broker id
	// when the reason has no registered code), so the trailing fill is attributable.
	clientOrderID := getBrOrderID()
	if reasonTag != "" {
		clientOrderID = clientIDForReason(reasonTag)
	}

	// Resolve quantity early: both endpoints need explicit quantity (no closePosition
	// support for trailing). When the caller wants a full-position trail (quantity<=0),
	// resolve the live position size and trail the whole amount.
	if quantity <= 0 {
		amt, err := t.execPositionAmt(symbol, positionSide)
		if err != nil {
			return "", fmt.Errorf("failed to resolve position size for trailing stop: %w", err)
		}
		if amt <= 0 {
			return "", fmt.Errorf("no open %s position on %s to attach trailing stop", positionSide, symbol)
		}
		quantity = amt
	}
	qtyStr, err := t.FormatQuantity(symbol, quantity)
	if err != nil {
		return "", fmt.Errorf("failed to format trailing stop quantity: %w", err)
	}

	// Format activation price and callback rate (shared by both endpoints).
	var actStr string
	if activationPrice > 0 {
		actStr, err = t.FormatPrice(symbol, activationPrice)
		if err != nil {
			return "", fmt.Errorf("failed to format trailing activation price: %w", err)
		}
	}

	// Phase 1a fix: unified contract — callers always pass decimal ratio
	// (e.g. 0.012 = 1.2%), adapter converts to Binance percentage internally.
	// Binance callbackRate is a percentage constrained to [0.1, 5] with a
	// 0.1 step; sending 4-decimal percentages is rejected with -2007 (Invalid
	// callBack rate). Convert ratio→percent, clamp to [0.1, 5], and round to
	// 0.1 step so the exchange trailing always registers. This is the single
	// boundary every trailing callback passes through (Binance-only; OKX uses
	// decimal ratios throughout). Fixes: immediate_trailing was passing ratio
	// which got clamped to 0.1% (should be 3.74%); native trailing was
	// pre-multiplying at call site (inconsistent contract).
	cbPercent := callbackRate * 100.0 // ratio → percent
	if cbPercent < 0.1 {
		cbPercent = 0.1
	}
	if cbPercent > 5 {
		cbPercent = 5
	}
	cbPercent = math.Round(cbPercent*10) / 10
	cbStr := fmt.Sprintf("%.1f", cbPercent)

	// Place on the ALGO endpoint (/fapi/v1/algoOrder). Binance migrated
	// TRAILING_STOP_MARKET off the classic /fapi/v1/order endpoint — the classic
	// endpoint now rejects it with -4120 ("Order type not supported for this
	// endpoint. Please use the Algo Order API endpoints instead."), confirmed on a
	// live account 2026-07-22.
	//
	// CRITICAL (root cause of the immediate-trigger / unresolved_exchange_close bug):
	// go-binance <= v2.8.9 sent the activation param under the WRONG key
	// ("activationPrice"), which Binance silently ignored, defaulting the trail to
	// the order-time market price → it armed at entry and triggered on the first
	// adverse tick. v2.8.10+ renamed it to the correct "activatePrice", so the
	// exchange now honors the requested peak. Verified live: sending activatePrice
	// = entry*1.02 registered exactly at +2.000% (0.000% divergence). We call
	// .ActivatePrice() explicitly to bind to the corrected key.
	activationRequired := activationPrice > 0

	algoService := t.client.NewCreateAlgoOrderService().
		Symbol(symbol).
		Side(side).
		PositionSide(posSide).
		Type(futures.AlgoOrderTypeTrailingStopMarket).
		WorkingType(futures.WorkingTypeContractPrice).
		ClientAlgoId(clientOrderID).
		Quantity(qtyStr)

	if actStr != "" {
		algoService = algoService.ActivatePrice(actStr)
	}
	algoService = algoService.CallbackRate(cbStr)

	resp, err := algoService.Do(context.Background())
	if err != nil {
		return "", fmt.Errorf("failed to set trailing stop-loss (algo endpoint): %w", err)
	}

	orderID := ""
	if resp != nil && resp.AlgoId != 0 {
		orderID = strconv.FormatInt(resp.AlgoId, 10)
	}

	// Read-back verification: confirm the exchange registered the activation/callback
	// we sent. This is the safety gate — if the exchange came back WITHOUT the peak
	// activation (or with a materially different one), the order would trigger too
	// early, so we CANCEL it and return an error rather than leave a wrong order live.
	// With the v2.8.10 param-name fix this passes cleanly; it stays as a genuine
	// guard against future regressions (wrong param, API change) so a mis-registered
	// order never rests silently.
	exchangeActivation, _ := strconv.ParseFloat(resp.ActivatePrice, 64)
	exchangeCallback, _ := strconv.ParseFloat(resp.CallbackRate, 64)

	activationDivergence := 0.0
	if activationRequired && exchangeActivation > 0 {
		activationDivergence = math.Abs(exchangeActivation-activationPrice) / activationPrice * 100.0
	}
	callbackDivergence := math.Abs(exchangeCallback - cbPercent)

	// Fatal: we asked for a peak activation but the exchange registered none, or one
	// that diverges materially (>0.3%). Either way the trail would arm at the wrong
	// level — cancel and bail so the caller uses the local monitor.
	activationMissing := activationRequired && exchangeActivation <= 0
	activationWrong := activationRequired && activationDivergence > 0.3
	if activationMissing || activationWrong {
		reason := "exchange registered NO activation price (would trigger immediately)"
		if activationWrong {
			reason = fmt.Sprintf("exchange activation %.4f diverges %.2f%% from requested %.4f", exchangeActivation, activationDivergence, activationPrice)
		}
		logger.Warnf("⚠️  Trailing order rejected on read-back: %s — cancelling algoId=%s and falling back to local monitor (reason=%q)", reason, orderID, reasonTag)
		if resp.AlgoId != 0 {
			if _, cErr := t.client.NewCancelAlgoOrderService().AlgoID(resp.AlgoId).Do(context.Background()); cErr != nil {
				if !contains(cErr.Error(), "Unknown order") && !contains(cErr.Error(), "UNKNOWN_ORDER") {
					logger.Warnf("⚠️  Failed to cancel mis-registered trailing order %s: %v", orderID, cErr)
				}
			}
		}
		return "", fmt.Errorf("trailing activation not honored by exchange (%s)", reason)
	}

	if callbackDivergence > 0.05 {
		// Callback rounding to the 0.1 step is expected and harmless; only warn.
		logger.Warnf("⚠️  Trailing callback registered with divergence: sent %.1f%%, exchange %.1f%% (Δ=%.2f%%) algoId=%s reason=%q",
			cbPercent, exchangeCallback, callbackDivergence, orderID, reasonTag)
	}

	// Dormant-risk guard (2026-07-28): an empty orderID here means the exchange
	// accepted the order (no API error, activation read-back passed) but returned
	// AlgoId==0, so we have NO handle to it. Binance trailing orders carry no
	// callbackRate in the algo-order list (ReportsTrailingCallbackRate=false), so the
	// fuzzy tier matcher cannot re-find an ID-less order either — it would be judged
	// "missing" on every poll and re-armed without bound (the HYPEUSDT 174-order leak
	// class). An unmanageable order is worse than none: cancel any just-placed
	// trailing order for this symbol/side to avoid an orphan, then return an error so
	// the caller drops to the LOCAL managed monitor (double cover, never takeover).
	if orderID == "" {
		logger.Warnf("⚠️  Trailing order placed but exchange returned AlgoId=0 (no handle) for %s %s reason=%q — cancelling to avoid an unmanageable orphan and falling back to local monitor", symbol, posSide, reasonTag)
		if cErr := t.CancelTrailingStopOrders(symbol); cErr != nil {
			logger.Warnf("⚠️  Failed to cancel ID-less trailing order for %s: %v", symbol, cErr)
		}
		return "", fmt.Errorf("exchange returned no algo id for trailing order (unmanageable); falling back to local monitor")
	}

	logger.Infof("  Trailing stop-loss set (Algo Order): activation=%.4f callback=%.1f%% (exchange confirmed) reason=%q algoId=%s",
		activationPrice, cbPercent, reasonTag, orderID)
	return orderID, nil
}

// CancelTrailingStopOrdersByIDs cancels specific trailing/algo orders by their
// exchange algoId or orderId, leaving other algo orders intact. Mirrors the OKX
// method the trailing re-arm path expects (so a replaced full-trail can drop only
// the stale order). Unknown/already-gone ids are treated as success. Phase 1c:
// supports both classic OrderIDs and AlgoIds — tries algo cancellation first, then
// regular order cancellation if algo fails with "unknown".
func (t *FuturesTrader) CancelTrailingStopOrdersByIDs(symbol string, orderIDs []string) error {
	symbol = t.toExecSymbol(symbol) // internal USDT -> exec (USDC when applicable)
	for _, id := range orderIDs {
		if strings.TrimSpace(id) == "" {
			continue
		}
		numericID, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64)
		if err != nil {
			// Not a numeric id (e.g. a client id) — skip rather than fail the batch.
			logger.Infof("  ⚠ Skipping non-numeric trailing order id %q: %v", id, err)
			continue
		}

		// Try algo cancellation first (most trailing orders are algo).
		_, algoErr := t.client.NewCancelAlgoOrderService().AlgoID(numericID).Do(context.Background())
		if algoErr == nil {
			continue // Successfully cancelled as algo order
		}

		// Algo cancellation failed — check if it's "unknown algo order" (means it's
		// a classic order ID instead), or a real error.
		if !contains(algoErr.Error(), "no algo") && !contains(algoErr.Error(), "No algo") && !contains(algoErr.Error(), "Unknown order") {
			return fmt.Errorf("failed to cancel trailing algo order %s: %w", id, algoErr)
		}

		// Unknown algo — try classic order cancellation (Phase 1c: classic trailing
		// orders return regular OrderIDs that need standard cancellation).
		_, orderErr := t.client.NewCancelOrderService().
			Symbol(symbol).
			OrderID(numericID).
			Do(context.Background())
		if orderErr != nil {
			if !contains(orderErr.Error(), "Unknown order") && !contains(orderErr.Error(), "UNKNOWN_ORDER") {
				return fmt.Errorf("failed to cancel trailing order %s (tried both algo and classic): %w", id, orderErr)
			}
			// Unknown in both — already cancelled or never existed, treat as success
		}
	}
	return nil
}

func (t *FuturesTrader) CancelTrailingStopOrders(symbol string) error {
	symbol = t.toExecSymbol(symbol) // internal USDT -> exec (USDC when applicable)

	// 1. Cancel algo trailing orders (fallback-path orders live here).
	err := t.client.NewCancelAllAlgoOpenOrdersService().
		Symbol(symbol).
		Do(context.Background())
	if err != nil {
		if !contains(err.Error(), "no algo") && !contains(err.Error(), "No algo") {
			return fmt.Errorf("failed to cancel trailing/algo orders: %w", err)
		}
	}

	// 2. Phase 1c: cancel classic TRAILING_STOP_MARKET orders (primary path). These
	// live in the regular open-orders list, not the algo list, so the algo cancel
	// above misses them. Enumerate and cancel individually by order id.
	orders, listErr := t.client.NewListOpenOrdersService().
		Symbol(symbol).
		Do(context.Background())
	if listErr == nil {
		for _, order := range orders {
			if string(order.Type) != "TRAILING_STOP_MARKET" {
				continue
			}
			if _, cErr := t.client.NewCancelOrderService().
				Symbol(symbol).
				OrderID(order.OrderID).
				Do(context.Background()); cErr != nil {
				if !contains(cErr.Error(), "Unknown order") && !contains(cErr.Error(), "UNKNOWN_ORDER") {
					return fmt.Errorf("failed to cancel classic trailing order %d: %w", order.OrderID, cErr)
				}
			}
		}
	}

	return nil
}

// OpenLong opens a long position
func (t *FuturesTrader) OpenLong(symbol string, quantity float64, leverage int) (map[string]interface{}, error) {
	symbol = t.toExecSymbol(symbol) // internal USDT -> exec (USDC when applicable)
	// First cancel all pending orders for this symbol (clean up old stop-loss and take-profit orders)
	if err := t.CancelAllOrders(symbol); err != nil {
		logger.Infof("  ⚠ Failed to cancel old pending orders (may not have any): %v", err)
	}

	// Set leverage
	if err := t.SetLeverage(symbol, leverage); err != nil {
		return nil, err
	}

	// Note: Margin mode should be set by the caller (AutoTrader) before opening position via SetMarginMode

	// Format quantity to correct precision
	quantityStr, err := t.FormatQuantity(symbol, quantity)
	if err != nil {
		return nil, err
	}

	// Check if formatted quantity is 0 (prevent rounding errors)
	quantityFloat, parseErr := strconv.ParseFloat(quantityStr, 64)
	if parseErr != nil || quantityFloat <= 0 {
		return nil, fmt.Errorf("position size too small, rounded to 0 (original: %.8f → formatted: %s). Suggest increasing position amount or selecting a lower-priced coin", quantity, quantityStr)
	}

	// Check minimum notional value (Binance requires at least 10 USDT)
	if err := t.CheckMinNotional(symbol, quantityFloat); err != nil {
		return nil, err
	}

	// Create market buy order (using br ID)
	order, err := t.client.NewCreateOrderService().
		Symbol(symbol).
		Side(futures.SideTypeBuy).
		PositionSide(futures.PositionSideTypeLong).
		Type(futures.OrderTypeMarket).
		Quantity(quantityStr).
		NewClientOrderID(getBrOrderID()).
		Do(context.Background())

	if err != nil {
		return nil, fmt.Errorf("failed to open long position: %w", err)
	}

	logger.Infof("✓ Opened long position successfully: %s quantity: %s", symbol, quantityStr)
	logger.Infof("  Order ID: %d", order.OrderID)

	result := make(map[string]interface{})
	result["orderId"] = order.OrderID
	result["symbol"] = order.Symbol
	result["status"] = order.Status
	return result, nil
}

// OpenShort opens a short position
func (t *FuturesTrader) OpenShort(symbol string, quantity float64, leverage int) (map[string]interface{}, error) {
	symbol = t.toExecSymbol(symbol) // internal USDT -> exec (USDC when applicable)
	// First cancel all pending orders for this symbol (clean up old stop-loss and take-profit orders)
	if err := t.CancelAllOrders(symbol); err != nil {
		logger.Infof("  ⚠ Failed to cancel old pending orders (may not have any): %v", err)
	}

	// Set leverage
	if err := t.SetLeverage(symbol, leverage); err != nil {
		return nil, err
	}

	// Note: Margin mode should be set by the caller (AutoTrader) before opening position via SetMarginMode

	// Format quantity to correct precision
	quantityStr, err := t.FormatQuantity(symbol, quantity)
	if err != nil {
		return nil, err
	}

	// Check if formatted quantity is 0 (prevent rounding errors)
	quantityFloat, parseErr := strconv.ParseFloat(quantityStr, 64)
	if parseErr != nil || quantityFloat <= 0 {
		return nil, fmt.Errorf("position size too small, rounded to 0 (original: %.8f → formatted: %s). Suggest increasing position amount or selecting a lower-priced coin", quantity, quantityStr)
	}

	// Check minimum notional value (Binance requires at least 10 USDT)
	if err := t.CheckMinNotional(symbol, quantityFloat); err != nil {
		return nil, err
	}

	// Create market sell order (using br ID)
	order, err := t.client.NewCreateOrderService().
		Symbol(symbol).
		Side(futures.SideTypeSell).
		PositionSide(futures.PositionSideTypeShort).
		Type(futures.OrderTypeMarket).
		Quantity(quantityStr).
		NewClientOrderID(getBrOrderID()).
		Do(context.Background())

	if err != nil {
		return nil, fmt.Errorf("failed to open short position: %w", err)
	}

	logger.Infof("✓ Opened short position successfully: %s quantity: %s", symbol, quantityStr)
	logger.Infof("  Order ID: %d", order.OrderID)

	result := make(map[string]interface{})
	result["orderId"] = order.OrderID
	result["symbol"] = order.Symbol
	result["status"] = order.Status
	return result, nil
}

// CloseLong closes a long position
func (t *FuturesTrader) CloseLong(symbol string, quantity float64) (map[string]interface{}, error) {
	symbol = t.toExecSymbol(symbol) // internal USDT -> exec (USDC when applicable)
	// If quantity is 0, resolve the LIVE position size. This MUST read fresh from
	// the exchange (execPositionAmt calls GetPositionRisk directly, symbol-scoped),
	// NOT the 15s GetPositions() cache: a close issued right after an open/resize
	// would otherwise read a stale (possibly zero) amount and silently skip the
	// close, leaving a NAKED position. Observed live 2026-07-22: a cached read
	// reported "no long position" ~10s after a fill while the position was live.
	if quantity == 0 {
		amt, err := t.execPositionAmt(symbol, "LONG")
		if err != nil {
			return nil, err
		}
		quantity = amt
		if quantity == 0 {
			return nil, fmt.Errorf("no long position found for %s", symbol)
		}
	}

	// Format quantity
	quantityStr, err := t.FormatQuantity(symbol, quantity)
	if err != nil {
		return nil, err
	}

	// Create market sell order (close long, using br ID)
	order, err := t.client.NewCreateOrderService().
		Symbol(symbol).
		Side(futures.SideTypeSell).
		PositionSide(futures.PositionSideTypeLong).
		Type(futures.OrderTypeMarket).
		Quantity(quantityStr).
		NewClientOrderID(getBrOrderID()).
		Do(context.Background())

	if err != nil {
		return nil, fmt.Errorf("failed to close long position: %w", err)
	}

	logger.Infof("✓ Closed long position successfully: %s quantity: %s", symbol, quantityStr)

	// After closing position, cancel all pending orders for this symbol (stop-loss and take-profit orders)
	if err := t.CancelAllOrders(symbol); err != nil {
		logger.Infof("  ⚠ Failed to cancel pending orders: %v", err)
	}

	result := make(map[string]interface{})
	result["orderId"] = order.OrderID
	result["symbol"] = order.Symbol
	result["status"] = order.Status
	return result, nil
}

// CloseShort closes a short position
func (t *FuturesTrader) CloseShort(symbol string, quantity float64) (map[string]interface{}, error) {
	symbol = t.toExecSymbol(symbol) // internal USDT -> exec (USDC when applicable)
	// If quantity is 0, resolve the LIVE position size fresh from the exchange
	// (execPositionAmt returns the absolute amount). Must NOT use the 15s
	// GetPositions() cache — a close right after an open/resize could read a stale
	// amount and skip the close, leaving a naked position (see CloseLong note).
	if quantity == 0 {
		amt, err := t.execPositionAmt(symbol, "SHORT")
		if err != nil {
			return nil, err
		}
		quantity = amt // already absolute value
		if quantity == 0 {
			return nil, fmt.Errorf("no short position found for %s", symbol)
		}
	}

	// Format quantity
	quantityStr, err := t.FormatQuantity(symbol, quantity)
	if err != nil {
		return nil, err
	}

	// Create market buy order (close short, using br ID)
	order, err := t.client.NewCreateOrderService().
		Symbol(symbol).
		Side(futures.SideTypeBuy).
		PositionSide(futures.PositionSideTypeShort).
		Type(futures.OrderTypeMarket).
		Quantity(quantityStr).
		NewClientOrderID(getBrOrderID()).
		Do(context.Background())

	if err != nil {
		return nil, fmt.Errorf("failed to close short position: %w", err)
	}

	logger.Infof("✓ Closed short position successfully: %s quantity: %s", symbol, quantityStr)

	// After closing position, cancel all pending orders for this symbol (stop-loss and take-profit orders)
	if err := t.CancelAllOrders(symbol); err != nil {
		logger.Infof("  ⚠ Failed to cancel pending orders: %v", err)
	}

	result := make(map[string]interface{})
	result["orderId"] = order.OrderID
	result["symbol"] = order.Symbol
	result["status"] = order.Status
	return result, nil
}

// CancelStopLossOrders cancels only stop-loss orders (doesn't affect take-profit orders)
// Now uses both legacy API and new Algo Order API
func (t *FuturesTrader) CancelStopLossOrders(symbol string) error {
	symbol = t.toExecSymbol(symbol) // internal USDT -> exec (USDC when applicable)
	canceledCount := 0
	var cancelErrors []error

	// 1. Cancel legacy stop-loss orders
	orders, err := t.client.NewListOpenOrdersService().
		Symbol(symbol).
		Do(context.Background())

	if err == nil {
		for _, order := range orders {
			orderType := string(order.Type)

			// Only cancel stop-loss orders (don't cancel take-profit orders)
			// Use string comparison since OrderType constants were removed in v2.8.9
			if orderType == "STOP_MARKET" || orderType == "STOP" {
				_, err := t.client.NewCancelOrderService().
					Symbol(symbol).
					OrderID(order.OrderID).
					Do(context.Background())

				if err != nil {
					errMsg := fmt.Sprintf("Order ID %d: %v", order.OrderID, err)
					cancelErrors = append(cancelErrors, fmt.Errorf("%s", errMsg))
					logger.Infof("  ⚠ Failed to cancel legacy stop-loss order: %s", errMsg)
					continue
				}

				canceledCount++
				logger.Infof("  ✓ Canceled legacy stop-loss order (Order ID: %d, Type: %s, Side: %s)", order.OrderID, orderType, order.PositionSide)
			}
		}
	}

	// 2. Cancel Algo stop-loss orders
	algoOrders, err := t.client.NewListOpenAlgoOrdersService().
		Symbol(symbol).
		Do(context.Background())

	if err == nil {
		for _, algoOrder := range algoOrders {
			// Only cancel stop-loss orders
			if algoOrder.OrderType == futures.AlgoOrderTypeStopMarket || algoOrder.OrderType == futures.AlgoOrderTypeStop {
				_, err := t.client.NewCancelAlgoOrderService().
					AlgoID(algoOrder.AlgoId).
					Do(context.Background())

				if err != nil {
					errMsg := fmt.Sprintf("Algo ID %d: %v", algoOrder.AlgoId, err)
					cancelErrors = append(cancelErrors, fmt.Errorf("%s", errMsg))
					logger.Infof("  ⚠ Failed to cancel Algo stop-loss order: %s", errMsg)
					continue
				}

				canceledCount++
				logger.Infof("  ✓ Canceled Algo stop-loss order (Algo ID: %d, Type: %s)", algoOrder.AlgoId, algoOrder.OrderType)
			}
		}
	}

	if canceledCount == 0 && len(cancelErrors) == 0 {
		logger.Infof("  ℹ %s has no stop-loss orders to cancel", symbol)
	} else if canceledCount > 0 {
		logger.Infof("  ✓ Canceled %d stop-loss order(s) for %s", canceledCount, symbol)
	}

	// If all cancellations failed, return error
	if len(cancelErrors) > 0 && canceledCount == 0 {
		return fmt.Errorf("failed to cancel stop-loss orders: %v", cancelErrors)
	}

	return nil
}

// CancelTakeProfitOrders cancels only take-profit orders (doesn't affect stop-loss orders)
// Now uses both legacy API and new Algo Order API
func (t *FuturesTrader) CancelTakeProfitOrders(symbol string) error {
	symbol = t.toExecSymbol(symbol) // internal USDT -> exec (USDC when applicable)
	canceledCount := 0
	var cancelErrors []error

	// 1. Cancel legacy take-profit orders
	orders, err := t.client.NewListOpenOrdersService().
		Symbol(symbol).
		Do(context.Background())

	if err == nil {
		for _, order := range orders {
			orderType := string(order.Type)

			// Only cancel take-profit orders (don't cancel stop-loss orders)
			// Use string comparison since OrderType constants were removed in v2.8.9
			if orderType == "TAKE_PROFIT_MARKET" || orderType == "TAKE_PROFIT" {
				_, err := t.client.NewCancelOrderService().
					Symbol(symbol).
					OrderID(order.OrderID).
					Do(context.Background())

				if err != nil {
					errMsg := fmt.Sprintf("Order ID %d: %v", order.OrderID, err)
					cancelErrors = append(cancelErrors, fmt.Errorf("%s", errMsg))
					logger.Infof("  ⚠ Failed to cancel legacy take-profit order: %s", errMsg)
					continue
				}

				canceledCount++
				logger.Infof("  ✓ Canceled legacy take-profit order (Order ID: %d, Type: %s, Side: %s)", order.OrderID, orderType, order.PositionSide)
			}
		}
	}

	// 2. Cancel Algo take-profit orders
	algoOrders, err := t.client.NewListOpenAlgoOrdersService().
		Symbol(symbol).
		Do(context.Background())

	if err == nil {
		for _, algoOrder := range algoOrders {
			// Only cancel take-profit orders
			if algoOrder.OrderType == futures.AlgoOrderTypeTakeProfitMarket || algoOrder.OrderType == futures.AlgoOrderTypeTakeProfit {
				_, err := t.client.NewCancelAlgoOrderService().
					AlgoID(algoOrder.AlgoId).
					Do(context.Background())

				if err != nil {
					errMsg := fmt.Sprintf("Algo ID %d: %v", algoOrder.AlgoId, err)
					cancelErrors = append(cancelErrors, fmt.Errorf("%s", errMsg))
					logger.Infof("  ⚠ Failed to cancel Algo take-profit order: %s", errMsg)
					continue
				}

				canceledCount++
				logger.Infof("  ✓ Canceled Algo take-profit order (Algo ID: %d, Type: %s)", algoOrder.AlgoId, algoOrder.OrderType)
			}
		}
	}

	if canceledCount == 0 && len(cancelErrors) == 0 {
		logger.Infof("  ℹ %s has no take-profit orders to cancel", symbol)
	} else if canceledCount > 0 {
		logger.Infof("  ✓ Canceled %d take-profit order(s) for %s", canceledCount, symbol)
	}

	// If all cancellations failed, return error
	if len(cancelErrors) > 0 && canceledCount == 0 {
		return fmt.Errorf("failed to cancel take-profit orders: %v", cancelErrors)
	}

	return nil
}

// CancelAllOrders cancels all pending orders for this symbol
// Now uses both legacy API and new Algo Order API
func (t *FuturesTrader) CancelAllOrders(symbol string) error {
	symbol = t.toExecSymbol(symbol) // internal USDT -> exec (USDC when applicable)
	// 1. Cancel all legacy orders
	err := t.client.NewCancelAllOpenOrdersService().
		Symbol(symbol).
		Do(context.Background())

	if err != nil {
		logger.Infof("  ⚠ Failed to cancel legacy orders: %v", err)
	} else {
		logger.Infof("  ✓ Canceled all legacy pending orders for %s", symbol)
	}

	// 2. Cancel all Algo orders
	err = t.client.NewCancelAllAlgoOpenOrdersService().
		Symbol(symbol).
		Do(context.Background())

	if err != nil {
		// Ignore "no algo orders" error
		if !contains(err.Error(), "no algo") && !contains(err.Error(), "No algo") {
			logger.Infof("  ⚠ Failed to cancel Algo orders: %v", err)
		}
	} else {
		logger.Infof("  ✓ Canceled all Algo orders for %s", symbol)
	}

	return nil
}

// PlaceLimitOrder places a limit order for grid trading
// This implements the GridTrader interface for FuturesTrader
func (t *FuturesTrader) PlaceLimitOrder(req *types.LimitOrderRequest) (*types.LimitOrderResult, error) {
	// Convert internal USDT symbol to exec (USDC when applicable) for all API calls
	// below. FormatQuantity/FormatPrice are idempotent on already-exec symbols.
	if req != nil {
		req.Symbol = t.toExecSymbol(req.Symbol)
	}
	// Format quantity to correct precision
	quantityStr, err := t.FormatQuantity(req.Symbol, req.Quantity)
	if err != nil {
		return nil, fmt.Errorf("failed to format quantity: %w", err)
	}

	// Format price to correct precision
	priceStr, err := t.FormatPrice(req.Symbol, req.Price)
	if err != nil {
		return nil, fmt.Errorf("failed to format price: %w", err)
	}

	// Set leverage if specified
	if req.Leverage > 0 {
		if err := t.SetLeverage(req.Symbol, req.Leverage); err != nil {
			logger.Warnf("Failed to set leverage: %v", err)
		}
	}

	// Determine side and position side
	var side futures.SideType
	var positionSide futures.PositionSideType

	if req.Side == "BUY" {
		side = futures.SideTypeBuy
		positionSide = futures.PositionSideTypeLong
	} else {
		side = futures.SideTypeSell
		positionSide = futures.PositionSideTypeShort
	}

	// Build order service with broker ID
	orderService := t.client.NewCreateOrderService().
		Symbol(req.Symbol).
		Side(side).
		PositionSide(positionSide).
		Type(futures.OrderTypeLimit).
		TimeInForce(futures.TimeInForceTypeGTC).
		Quantity(quantityStr).
		Price(priceStr).
		NewClientOrderID(getBrOrderID())

	// Execute order
	order, err := orderService.Do(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to place limit order: %w", err)
	}

	logger.Infof("✓ [Grid] Placed limit order: %s %s %s @ %s, qty=%s, orderID=%d",
		req.Symbol, req.Side, positionSide, priceStr, quantityStr, order.OrderID)

	return &types.LimitOrderResult{
		OrderID:      fmt.Sprintf("%d", order.OrderID),
		ClientID:     order.ClientOrderID,
		Symbol:       toInternalSymbol(order.Symbol),
		Side:         string(order.Side),
		PositionSide: string(order.PositionSide),
		Price:        req.Price,
		Quantity:     req.Quantity,
		Status:       string(order.Status),
	}, nil
}

// CancelOrder cancels a specific order by ID
// This implements the GridTrader interface for FuturesTrader
func (t *FuturesTrader) CancelOrder(symbol, orderID string) error {
	// Parse order ID to int64
	orderIDInt, err := strconv.ParseInt(orderID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid order ID: %w", err)
	}

	_, err = t.client.NewCancelOrderService().
		Symbol(symbol).
		OrderID(orderIDInt).
		Do(context.Background())

	if err != nil {
		return fmt.Errorf("failed to cancel order: %w", err)
	}

	logger.Infof("✓ [Grid] Cancelled order: %s/%s", symbol, orderID)
	return nil
}

// CancelAlgoOrderByID cancels a single algo (stop/take-profit) order by its algo
// ID. Binance migrated stop orders to the Algo Order system, so they must be
// cancelled via the algo endpoint — the regular CancelOrder path (fapi/v1/order)
// does not know about them.
//
// The shared protection reconciler cancels stale duplicate protection orders
// through the okxProtectionOrderIDCanceller interface (cancelUnexpectedProtection
// OrdersByID). Only OKX implemented it, so on Binance the type assertion failed
// silently: the fast-path logged "canceling N stale duplicates" but nothing was
// actually cancelled, leaving the reconciler to report "stale duplicate cleanup
// incomplete" every cycle while the stops accumulated. Implementing it here lets
// Binance's own stale stops actually be removed.
//
// The id string is what GetOpenOrders reported as OpenOrder.OrderID. That is the
// AlgoId for algo stops (fmt.Sprintf("%d", algoOrder.AlgoId)) BUT the regular
// exchange OrderID for maker take-profit orders — those are placed as post-only
// reduce-direction LIMIT orders (placeMakerTakeProfit), not algo orders. The
// shared reconciler cannot tell the two apart from the ID alone, so this method
// tries the algo endpoint first and, when the exchange reports the order does not
// exist there (-2011 Unknown order sent), falls back to the regular order-cancel
// endpoint. Without the fallback, stale maker-TP LIMIT orders could never be
// cleaned (the reconciler churned "stale duplicate cleanup incomplete" forever on
// e.g. XAGUSDT's leftover TP orders after re-entries).
func (t *FuturesTrader) CancelAlgoOrderByID(symbol string, id string) error {
	symbol = t.toExecSymbol(symbol) // internal USDT -> exec (USDC when applicable)
	idInt, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid order ID %q: %w", id, err)
	}

	_, algoErr := t.client.NewCancelAlgoOrderService().
		AlgoID(idInt).
		Do(context.Background())
	if algoErr == nil {
		logger.Infof("✓ [Binance] Cancelled algo order: %s/%d", symbol, idInt)
		return nil
	}

	// Not in the algo system — likely a regular LIMIT protection order (maker TP).
	// Retry via the regular order-cancel endpoint before giving up.
	if isUnknownAlgoOrder(algoErr) {
		_, regErr := t.client.NewCancelOrderService().
			Symbol(symbol).
			OrderID(idInt).
			Do(context.Background())
		if regErr == nil {
			logger.Infof("✓ [Binance] Cancelled order (algo fallback): %s/%d", symbol, idInt)
			return nil
		}
		return fmt.Errorf("failed to cancel order %d via algo and regular endpoints: algo=%w; regular=%v", idInt, algoErr, regErr)
	}

	return fmt.Errorf("failed to cancel algo order %d: %w", idInt, algoErr)
}

// GetOrderBook gets the order book for a symbol
// This implements the GridTrader interface for FuturesTrader
func (t *FuturesTrader) GetOrderBook(symbol string, depth int) (bids, asks [][]float64, err error) {
	symbol = t.toExecSymbol(symbol) // internal USDT -> exec (USDC when applicable)
	book, err := t.client.NewDepthService().
		Symbol(symbol).
		Limit(depth).
		Do(context.Background())

	if err != nil {
		return nil, nil, fmt.Errorf("failed to get order book: %w", err)
	}

	// Convert bids
	bids = make([][]float64, len(book.Bids))
	for i, bid := range book.Bids {
		price, _ := strconv.ParseFloat(bid.Price, 64)
		qty, _ := strconv.ParseFloat(bid.Quantity, 64)
		bids[i] = []float64{price, qty}
	}

	// Convert asks
	asks = make([][]float64, len(book.Asks))
	for i, ask := range book.Asks {
		price, _ := strconv.ParseFloat(ask.Price, 64)
		qty, _ := strconv.ParseFloat(ask.Quantity, 64)
		asks[i] = []float64{price, qty}
	}

	return bids, asks, nil
}

// CancelStopOrders cancels take-profit/stop-loss orders for this symbol (used to adjust TP/SL positions)
// Now uses both legacy API and new Algo Order API (Binance migrated stop orders to Algo system)
func (t *FuturesTrader) CancelStopOrders(symbol string) error {
	symbol = t.toExecSymbol(symbol) // internal USDT -> exec (USDC when applicable)
	canceledCount := 0

	// 1. Cancel legacy stop orders (for backward compatibility)
	orders, err := t.client.NewListOpenOrdersService().
		Symbol(symbol).
		Do(context.Background())

	if err == nil {
		for _, order := range orders {
			orderType := string(order.Type)

			// Only cancel stop-loss and take-profit orders
			// Use string comparison since OrderType constants were removed in v2.8.9
			// Phase 1c: TRAILING_STOP_MARKET (classic endpoint) also surfaces here.
			if orderType == "STOP_MARKET" ||
				orderType == "TAKE_PROFIT_MARKET" ||
				orderType == "STOP" ||
				orderType == "TAKE_PROFIT" ||
				orderType == "TRAILING_STOP_MARKET" {

				_, err := t.client.NewCancelOrderService().
					Symbol(symbol).
					OrderID(order.OrderID).
					Do(context.Background())

				if err != nil {
					logger.Infof("  ⚠ Failed to cancel legacy order %d: %v", order.OrderID, err)
					continue
				}

				canceledCount++
				logger.Infof("  ✓ Canceled legacy stop order for %s (Order ID: %d, Type: %s)",
					symbol, order.OrderID, orderType)
			}
		}
	}

	// 2. Cancel Algo orders (new API)
	err = t.client.NewCancelAllAlgoOpenOrdersService().
		Symbol(symbol).
		Do(context.Background())

	if err != nil {
		// Ignore "no algo orders" error
		if !contains(err.Error(), "no algo") && !contains(err.Error(), "No algo") {
			logger.Infof("  ⚠ Failed to cancel Algo orders: %v", err)
		}
	} else {
		logger.Infof("  ✓ Canceled all Algo orders for %s", symbol)
		canceledCount++
	}

	if canceledCount == 0 {
		logger.Infof("  ℹ %s has no take-profit/stop-loss orders to cancel", symbol)
	}

	return nil
}

// GetOpenOrders gets all open/pending orders for a symbol
func (t *FuturesTrader) GetOpenOrders(symbol string) ([]types.OpenOrder, error) {
	symbol = t.toExecSymbol(symbol) // internal USDT -> exec (USDC when applicable)
	var result []types.OpenOrder

	// 1. Get legacy open orders
	orders, err := t.client.NewListOpenOrdersService().
		Symbol(symbol).
		Do(context.Background())

	if err != nil {
		return nil, fmt.Errorf("failed to get open orders: %w", err)
	}

	for _, order := range orders {
		price, _ := strconv.ParseFloat(order.Price, 64)
		stopPrice, _ := strconv.ParseFloat(order.StopPrice, 64)
		quantity, _ := strconv.ParseFloat(order.OrigQuantity, 64)

		// Maker take-profit is placed as a post-only reduce-direction LIMIT
		// (see placeMakerTakeProfit) rather than an algo TAKE_PROFIT_MARKET.
		// The shared protection reconciler only recognises orders whose Type
		// says TAKE_PROFIT, so a bare LIMIT would look like a missing TP and be
		// re-placed every cycle (unbounded churn). Report our own maker TP with
		// Type=TAKE_PROFIT so the shared code dedups/attributes it correctly.
		// This is confined to the Binance trader — OKX and others never run this
		// path, and no cancel path consumes GetOpenOrders (they query the
		// exchange directly), so classification here cannot disturb them.
		reportType := string(order.Type)
		if isMakerTakeProfitLimit(order) {
			reportType = "TAKE_PROFIT"
		}

		oo := types.OpenOrder{
			OrderID:       fmt.Sprintf("%d", order.OrderID),
			Symbol:        toInternalSymbol(order.Symbol),
			Side:          string(order.Side),
			PositionSide:  string(order.PositionSide),
			Type:          reportType,
			Price:         price,
			StopPrice:     stopPrice,
			Quantity:      quantity,
			Status:        string(order.Status),
			ClientOrderID: order.ClientOrderID,
		}

		// Phase 1c: classic TRAILING_STOP_MARKET orders (from the /fapi/v1/order
		// endpoint) surface here in the regular list, carrying the REAL activation
		// price (ActivatePrice) and callback rate (PriceRate) the exchange registered
		// — unlike the algo-list endpoint which omits them. Read the real values so
		// the panel/reconciler see truth, and mark activated so the phantom-activation
		// guard (checkAndFixStaleTrailingActivation) doesn't force-close a live trail.
		if strings.Contains(strings.ToUpper(string(order.Type)), "TRAILING") {
			if activatePrice, e := strconv.ParseFloat(order.ActivatePrice, 64); e == nil && activatePrice > 0 {
				oo.ActivationPrice = activatePrice
			} else {
				oo.ActivationPrice = stopPrice
			}
			oo.ActivationStatus = "activated"
		}

		result = append(result, oo)
	}

	// 2. Get Algo orders (new API for stop-loss/take-profit)
	algoOrders, err := t.client.NewListOpenAlgoOrdersService().
		Symbol(symbol).
		Do(context.Background())

	if err == nil {
		for _, algoOrder := range algoOrders {
			triggerPrice, _ := strconv.ParseFloat(algoOrder.TriggerPrice, 64)
			quantity, _ := strconv.ParseFloat(algoOrder.Quantity, 64)
			orderType := string(algoOrder.OrderType)
			stopPrice := triggerPrice

			oo := types.OpenOrder{
				OrderID:      fmt.Sprintf("%d", algoOrder.AlgoId),
				Symbol:       toInternalSymbol(algoOrder.Symbol),
				Side:         string(algoOrder.Side),
				PositionSide: string(algoOrder.PositionSide),
				Type:         orderType,
				Price:        0, // Algo orders use stop/trigger price
				StopPrice:    stopPrice,
				Quantity:     quantity,
				Status:       "NEW",
				// Carry the algo client ID so the shared protection reconciler can
				// recognise our own stops via the broker-tag prefix (x-KzrpZaP9).
				// Binance stop-loss/take-profit are ALGO orders placed with
				// ClientAlgoId(getBrOrderID()); the regular-orders branch copies
				// ClientOrderID but this algo branch previously dropped it, so every
				// stop looked manual/foreign to isLikelyBotProtectionOrder and was
				// preserved forever — stale stops then accumulated across re-entries
				// and eventually hit Binance's max stop-order limit (-4045).
				ClientOrderID: algoOrder.ClientAlgoId,
			}

			// Report native trailing orders as already activated so the shared
			// drawdown reconciler skips its OKX-oriented "phantom activation"
			// conversion.
			//
			// Background: checkAndFixStaleTrailingActivation (auto_trader_risk.go)
			// treats a trailing order whose activation price the mark has already
			// crossed as a *phantom* (OKX sometimes fails to actually activate) and
			// force-converts it to a MANAGED full-close. That guard keys off
			// ActivationStatus=="activated", a field ONLY OKX populates. On Binance
			// it was always "" here, so every in-profit position looked phantom and
			// got force-closed then re-armed every cycle (the "无端平仓" reports).
			// Binance's engine, unlike OKX, reliably activates a native
			// TRAILING_STOP_MARKET once price passes the activation price, so an order
			// still present in the open list is genuinely armed — reporting it as
			// "activated" is correct and stops the misfire. The phantom check falls
			// back to StopPrice (=triggerPrice) for the activation price, which we
			// already set above, so no ActivationPrice field is needed. Binance's
			// open-algo list endpoint does not return callbackRate, so that stays 0.
			// Confined to the Binance trader; OKX keeps its own detection untouched.
			if strings.Contains(strings.ToUpper(orderType), "TRAILING") {
				oo.ActivationPrice = triggerPrice
				oo.ActivationStatus = "activated"
			}

			result = append(result, oo)
		}
	}

	return result, nil
}

// SetStopLoss sets stop-loss order using new Algo Order API
// Binance has migrated stop orders to Algo Order system (error -4120 STOP_ORDER_SWITCH_ALGO)
func (t *FuturesTrader) SetStopLoss(symbol string, positionSide string, quantity, stopPrice float64) error {
	_, err := t.SetStopLossTagged(symbol, positionSide, quantity, stopPrice, "")
	return err
}

// SetStopLossTagged sets a stop-loss order. Stop-loss is protective and MUST fill
// on trigger, so it always uses the trigger-market algo order (taker) — never a
// resting post-only limit, which could fail to fill in a fast adverse move. The
// reasonTag is accepted for interface parity with OKX/attribution but does not
// change execution semantics. Returns the exchange algoId so the caller can record
// it in the close-intent ledger for exact order-id attribution.
func (t *FuturesTrader) SetStopLossTagged(symbol string, positionSide string, quantity, stopPrice float64, reasonTag string) (string, error) {
	symbol = t.toExecSymbol(symbol) // internal USDT -> exec (USDC when applicable)
	var side futures.SideType
	var posSide futures.PositionSideType

	if positionSide == "LONG" {
		side = futures.SideTypeSell
		posSide = futures.PositionSideTypeLong
	} else {
		side = futures.SideTypeBuy
		posSide = futures.PositionSideTypeShort
	}

	// Trigger price MUST be tick-aligned to the exec symbol's precision. Raw
	// "%.8f" over-specifies decimals and Binance rejects it with -1111
	// (Precision is over the maximum defined for this asset) — fatal for a
	// protective order. symbol is already exec-converted above; FormatPrice is
	// idempotent on exec symbols.
	triggerStr, err := t.FormatPrice(symbol, stopPrice)
	if err != nil {
		return "", fmt.Errorf("failed to format stop-loss trigger price: %w", err)
	}

	// Use new Algo Order API. In hedge mode Binance permits only ONE
	// closePosition=true stop per side, so laddered partial stops (quantity>0)
	// must be placed as explicit-quantity orders (PositionSide + Quantity, no
	// reduceOnly — that returns -1106; see CloseLong/CloseShort). Only a
	// full-position stop (quantity<=0: full_sl / break_even / fallback) uses
	// closePosition=true, which auto-tracks the whole position size.
	svc := t.client.NewCreateAlgoOrderService().
		Symbol(symbol).
		Side(side).
		PositionSide(posSide).
		Type(futures.AlgoOrderTypeStopMarket).
		TriggerPrice(triggerStr).
		WorkingType(futures.WorkingTypeContractPrice).
		// Encode the mechanism into the client algo id so the eventual close fill
		// decodes its own reason exactly (BE/ladder_sl/structural_sl/full_sl all
		// become STOP_MARKET on the exchange; only the coded id disambiguates them).
		ClientAlgoId(clientIDForReason(reasonTag))

	if quantity > 0 {
		qtyStr, ferr := t.FormatQuantity(symbol, quantity)
		if ferr != nil {
			return "", fmt.Errorf("failed to format stop-loss quantity: %w", ferr)
		}
		svc = svc.Quantity(qtyStr)
	} else {
		svc = svc.ClosePosition(true)
	}

	resp, err := svc.Do(context.Background())
	if err != nil {
		return "", fmt.Errorf("failed to set stop-loss: %w", err)
	}

	algoID := ""
	if resp != nil && resp.AlgoId != 0 {
		algoID = strconv.FormatInt(resp.AlgoId, 10)
	}
	logger.Infof("  Stop-loss price set (Algo Order): %.4f (qty=%.6f) reason=%q algoId=%s", stopPrice, quantity, reasonTag, algoID)
	return algoID, nil
}

// SetTakeProfit sets take-profit. Delegates to the tagged implementation.
func (t *FuturesTrader) SetTakeProfit(symbol string, positionSide string, quantity, takeProfitPrice float64) error {
	_, err := t.SetTakeProfitTagged(symbol, positionSide, quantity, takeProfitPrice, "")
	return err
}

// SetTakeProfitTagged sets a take-profit order.
//
// When makerTakeProfit is enabled and a concrete quantity is given, it first tries
// a POST-ONLY reduce-only LIMIT order at the TP price. Such an order rests on the
// book and, when hit, fills as a MAKER (0 fee on USDC pairs) instead of crossing
// the book as a taker. If the exchange would execute it immediately (price already
// through the market) it rejects the post-only order; we detect that and fall back
// to the trigger-market algo TP so the target is never silently dropped.
//
// SL / break-even / trailing intentionally do NOT use this path: those are
// protective and must fill on trigger, so they stay trigger-market (taker).
//
// Returns the exchange algoId when the algo TP path is used (empty string when
// maker TP succeeds, since regular limit orders don't have algoId).
func (t *FuturesTrader) SetTakeProfitTagged(symbol string, positionSide string, quantity, takeProfitPrice float64, reasonTag string) (string, error) {
	symbol = t.toExecSymbol(symbol) // internal USDT -> exec (USDC when applicable)

	if t.makerTakeProfit && quantity > 0 {
		if err := t.placeMakerTakeProfit(symbol, positionSide, quantity, takeProfitPrice, reasonTag); err == nil {
			return "", nil // maker TP succeeded, no algoId
		} else if isPostOnlyCrossRejection(err) {
			logger.Infof("  ↩️ Maker TP would cross for %s @ %.8f; falling back to algo TP", symbol, takeProfitPrice)
		} else {
			logger.Warnf("  ⚠️ Maker TP failed for %s (%v); falling back to algo TP", symbol, err)
		}
	}
	return t.setAlgoTakeProfit(symbol, positionSide, quantity, takeProfitPrice, reasonTag)
}

// placeMakerTakeProfit places a post-only LIMIT order that earns the maker fee
// when filled. In hedge mode PositionSide already constrains it to a closing
// order, so reduceOnly must NOT be sent (Binance returns -1106); the opposite
// Side + PositionSide combination is inherently reduce-only. Returns the raw
// exchange error so callers can classify a post-only cross rejection.
func (t *FuturesTrader) placeMakerTakeProfit(symbol, positionSide string, quantity, price float64, reasonTag string) error {
	var side futures.SideType
	var posSide futures.PositionSideType
	if positionSide == "LONG" {
		side = futures.SideTypeSell
		posSide = futures.PositionSideTypeLong
	} else {
		side = futures.SideTypeBuy
		posSide = futures.PositionSideTypeShort
	}
	qtyStr, err := t.FormatQuantity(symbol, quantity)
	if err != nil {
		return fmt.Errorf("format maker TP quantity: %w", err)
	}
	priceStr, err := t.FormatPrice(symbol, price)
	if err != nil {
		return fmt.Errorf("format maker TP price: %w", err)
	}
	_, err = t.client.NewCreateOrderService().
		Symbol(symbol).
		Side(side).
		PositionSide(posSide).
		Type(futures.OrderTypeLimit).
		TimeInForce(futures.TimeInForceTypeGTX). // GTX = post-only (maker or reject)
		Quantity(qtyStr).
		Price(priceStr).
		NewClientOrderID(clientIDForReason(reasonTag)).
		Do(context.Background())
	if err != nil {
		return err
	}
	logger.Infof("  ✓ Maker TP (post-only limit) set: %s %s %s @ %s", symbol, posSide, qtyStr, priceStr)
	return nil
}

// setAlgoTakeProfit is the original trigger-market algo TP (taker on fill).
// Laddered partial TPs (quantity>0) use explicit quantity (hedge mode allows
// only one closePosition=true TP per side; see SetStopLossTagged). A
// full-position TP (quantity<=0) uses closePosition=true.
func (t *FuturesTrader) setAlgoTakeProfit(symbol, positionSide string, quantity, takeProfitPrice float64, reasonTag string) (string, error) {
	var side futures.SideType
	var posSide futures.PositionSideType
	if positionSide == "LONG" {
		side = futures.SideTypeSell
		posSide = futures.PositionSideTypeLong
	} else {
		side = futures.SideTypeBuy
		posSide = futures.PositionSideTypeShort
	}

	// Tick-align the trigger price (see SetStopLossTagged) to avoid -1111.
	triggerStr, err := t.FormatPrice(symbol, takeProfitPrice)
	if err != nil {
		return "", fmt.Errorf("failed to format take-profit trigger price: %w", err)
	}

	svc := t.client.NewCreateAlgoOrderService().
		Symbol(symbol).
		Side(side).
		PositionSide(posSide).
		Type(futures.AlgoOrderTypeTakeProfitMarket).
		TriggerPrice(triggerStr).
		WorkingType(futures.WorkingTypeContractPrice).
		ClientAlgoId(clientIDForReason(reasonTag))

	if quantity > 0 {
		qtyStr, ferr := t.FormatQuantity(symbol, quantity)
		if ferr != nil {
			return "", fmt.Errorf("failed to format take-profit quantity: %w", ferr)
		}
		svc = svc.Quantity(qtyStr)
	} else {
		svc = svc.ClosePosition(true)
	}

	resp, err := svc.Do(context.Background())
	if err != nil {
		return "", fmt.Errorf("failed to set take-profit: %w", err)
	}

	algoID := ""
	if resp != nil && resp.AlgoId != 0 {
		algoID = strconv.FormatInt(resp.AlgoId, 10)
	}
	logger.Infof("  Take-profit price set (Algo Order): %.4f (qty=%.6f) reason=%q algoId=%s", takeProfitPrice, quantity, reasonTag, algoID)
	return algoID, nil
}

// isPostOnlyCrossRejection reports whether err is Binance's rejection of a
// post-only order that would have executed immediately (would-be taker).
// -5022: "Due to the order could not be executed as maker" ; -2021: order would
// immediately trigger.
func isPostOnlyCrossRejection(err error) bool {
	if err == nil {
		return false
	}
	m := err.Error()
	return contains(m, "-5022") || contains(m, "-2021") ||
		contains(m, "GTX") || contains(m, "immediately") || contains(m, "post only") || contains(m, "post-only")
}

// isUnknownAlgoOrder reports whether a cancel error means the ID is not a live
// algo order (-2011 Unknown order sent). Used to decide whether to fall back to
// the regular order-cancel endpoint — a maker take-profit is a regular LIMIT
// order, so the algo endpoint rejects its ID with -2011.
func isUnknownAlgoOrder(err error) bool {
	if err == nil {
		return false
	}
	m := err.Error()
	return contains(m, "-2011") || contains(m, "Unknown order")
}

// GetOrderStatus gets order status
func (t *FuturesTrader) GetOrderStatus(symbol string, orderID string) (map[string]interface{}, error) {
	symbol = t.toExecSymbol(symbol) // internal USDT -> exec (USDC when applicable)
	// Convert orderID to int64
	orderIDInt, err := strconv.ParseInt(orderID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid order ID: %s", orderID)
	}

	order, err := t.client.NewGetOrderService().
		Symbol(symbol).
		OrderID(orderIDInt).
		Do(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get order status: %w", err)
	}

	// Parse execution price
	avgPrice, _ := strconv.ParseFloat(order.AvgPrice, 64)
	executedQty, _ := strconv.ParseFloat(order.ExecutedQuantity, 64)

	result := map[string]interface{}{
		"orderId":     order.OrderID,
		"symbol":      order.Symbol,
		"status":      string(order.Status),
		"avgPrice":    avgPrice,
		"executedQty": executedQty,
		"side":        string(order.Side),
		"type":        string(order.Type),
		"time":        order.Time,
		"updateTime":  order.UpdateTime,
	}

	// Binance futures commission fee needs to be obtained through GetUserTrades, not retrieved here for now
	// Can be obtained later through WebSocket or separate query
	result["commission"] = 0.0

	return result, nil
}
