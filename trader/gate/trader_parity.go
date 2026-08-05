package gate

import (
	"fmt"
	"math"
	"nofx/logger"
	"nofx/trader/types"
	"strconv"
	"strings"

	"github.com/antihax/optional"
	"github.com/gateio/gateapi-go/v6"
)

// This file closes the optional-capability gap between gate and the two exchanges the
// system was tuned on (okx, binance). None of these methods are in the base Trader
// interface: the generic layer reaches them through anonymous interface assertions and
// silently degrades when they are absent. That degradation is safe but it is NOT parity:
//
//   - CancelOrder: cancelProtectionOrderByID (break_even_tier_replace.go:172) needs
//     cancel-by-id. Without it, it refuses to cancel at all — deliberately, because the
//     only alternative is cancel-by-tag which would take out sibling BE tiers. The
//     result was that gate could never replace a break-even tier, only stack new ones.
//   - GetOrderBook + PlaceLimitOrder + CancelOrder + GetOrderStatus together form
//     makerEntryCapable (auto_trader_maker_entry.go:15). Missing any one of them made
//     every gate entry cross the spread and pay taker fees even with maker entry on.

// CancelOrder cancels a single non-trigger order by its exchange order id.
//
// Note the asymmetry with the trigger-order path: Gate has two distinct cancel
// endpoints, and an id from one namespace is meaningless in the other. This handles
// regular orders; trigger (TP/SL) orders go through cancelTriggerOrders. We try the
// regular endpoint first and fall back to the trigger endpoint, because callers hold an
// id without knowing which namespace produced it.
func (t *GateTrader) CancelOrder(symbol, orderID string) error {
	orderID = strings.TrimSpace(orderID)
	if orderID == "" {
		return fmt.Errorf("cancel order: empty order id")
	}

	_, _, err := t.client.FuturesApi.CancelFuturesOrder(t.ctx, "usdt", orderID, nil)
	if err == nil {
		logger.Infof("  [Gate] Canceled order %s on %s", orderID, t.convertSymbol(symbol))
		return nil
	}

	// Already gone is success: the caller's goal is "this order must not be resting".
	if isGateOrderGone(err) {
		logger.Infof("  [Gate] Order %s already gone on %s", orderID, t.convertSymbol(symbol))
		return nil
	}

	// Fall back to the trigger namespace before reporting failure.
	if _, _, triggerErr := t.client.FuturesApi.CancelPriceTriggeredOrder(t.ctx, "usdt", orderID); triggerErr == nil {
		logger.Infof("  [Gate] Canceled trigger order %s on %s", orderID, t.convertSymbol(symbol))
		return nil
	} else if isGateOrderGone(triggerErr) {
		return nil
	}

	return fmt.Errorf("failed to cancel order %s: %w", orderID, err)
}

// isGateOrderGone reports whether an error means the order no longer exists.
func isGateOrderGone(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range []string{"ORDER_NOT_FOUND", "ORDER_FINISHED", "ORDER_CLOSED", "NOT_FOUND"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// GetOrderBook returns top-of-book levels as [][]float64{{price, qty}, ...}, matching
// okx's shape so the shared maker-entry logic needs no per-venue branch.
//
// Gate reports depth quantities in CONTRACTS; they are converted to base units with
// quanto_multiplier so the caller compares like with like against order quantities.
func (t *GateTrader) GetOrderBook(symbol string, depth int) (bids, asks [][]float64, err error) {
	gateSymbol := t.convertSymbol(symbol)
	if depth <= 0 {
		depth = 5
	}

	opts := &gateapi.ListFuturesOrderBookOpts{
		Limit: optional.NewInt32(int32(depth)),
	}
	book, _, err := t.client.FuturesApi.ListFuturesOrderBook(t.ctx, "usdt", gateSymbol, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get order book: %w", err)
	}

	quantoMultiplier := 1.0
	if contract, cerr := t.getContract(gateSymbol); cerr == nil && contract != nil {
		if qm, _ := strconv.ParseFloat(contract.QuantoMultiplier, 64); qm > 0 {
			quantoMultiplier = qm
		}
	}

	convert := func(items []gateapi.FuturesOrderBookItem) [][]float64 {
		out := make([][]float64, 0, len(items))
		for _, item := range items {
			price, perr := strconv.ParseFloat(item.P, 64)
			if perr != nil || price <= 0 {
				continue
			}
			out = append(out, []float64{price, math.Abs(float64(item.S)) * quantoMultiplier})
		}
		return out
	}

	return convert(book.Bids), convert(book.Asks), nil
}

// PlaceLimitOrder places a limit order, optionally post-only.
//
// Gate's tif "poc" (PendingOrCancelled) is the post-only mode: it is cancelled rather
// than filled if it would take. Requesting PostOnly and silently sending "gtc" would
// pay taker fees on the very path whose purpose is to earn the maker fee, so PostOnly
// maps to poc and nothing else.
func (t *GateTrader) PlaceLimitOrder(req *types.LimitOrderRequest) (*types.LimitOrderResult, error) {
	if req == nil {
		return nil, fmt.Errorf("place limit order: nil request")
	}
	if req.Price <= 0 {
		return nil, fmt.Errorf("place limit order: invalid price %v", req.Price)
	}
	if req.Quantity <= 0 {
		return nil, fmt.Errorf("place limit order: invalid quantity %v", req.Quantity)
	}

	gateSymbol := t.convertSymbol(req.Symbol)
	contract, err := t.getContract(gateSymbol)
	if err != nil {
		return nil, err
	}
	quantoMultiplier, _ := strconv.ParseFloat(contract.QuantoMultiplier, 64)
	if quantoMultiplier <= 0 {
		return nil, fmt.Errorf("invalid quanto_multiplier for %s", gateSymbol)
	}

	size := int64(req.Quantity / quantoMultiplier)
	if size <= 0 {
		return nil, fmt.Errorf("quantity %v is below one contract (%v) for %s", req.Quantity, quantoMultiplier, gateSymbol)
	}
	// Size sign is the order direction, exactly as on the market paths.
	if strings.EqualFold(req.Side, "SELL") {
		size = -size
	}

	if req.Leverage > 0 && !req.ReduceOnly {
		if err := t.SetLeverage(req.Symbol, req.Leverage); err != nil {
			logger.Warnf("  [Gate] Failed to set leverage for limit order: %v", err)
		}
	}

	tif := "gtc"
	if req.PostOnly {
		tif = "poc" // PendingOrCancelled: maker-only, cancelled instead of taking
	}

	text := gateTag
	if req.ClientID != "" {
		if candidate := sanitizeGateText(req.ClientID); candidate != "" {
			text = candidate
		}
	}

	order := gateapi.FuturesOrder{
		Contract:   gateSymbol,
		Size:       size,
		Price:      t.formatPrice(contract, req.Price),
		Tif:        tif,
		ReduceOnly: req.ReduceOnly,
		Text:       text,
	}

	result, _, err := t.client.FuturesApi.CreateFuturesOrder(t.ctx, "usdt", order, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to place limit order: %w", err)
	}
	t.clearCache()

	price, _ := strconv.ParseFloat(result.Price, 64)
	if price <= 0 {
		price = req.Price
	}

	logger.Infof("  [Gate] Limit order placed: %s %s size=%d price=%s tif=%s id=%d",
		gateSymbol, strings.ToUpper(req.Side), size, order.Price, tif, result.Id)

	return &types.LimitOrderResult{
		OrderID:      fmt.Sprintf("%d", result.Id),
		ClientID:     result.Text,
		Symbol:       t.revertSymbol(gateSymbol),
		Side:         strings.ToUpper(req.Side),
		PositionSide: strings.ToUpper(req.PositionSide),
		Price:        price,
		Quantity:     math.Abs(float64(result.Size)) * quantoMultiplier,
		Status:       gateOrderStatus(result),
	}, nil
}

// gateOrderStatus maps Gate's (status, finish_as) pair onto the status vocabulary the
// generic layer switches on (NEW / FILLED / PARTIALLY_FILLED / CANCELED).
func gateOrderStatus(order gateapi.FuturesOrder) string {
	if !strings.EqualFold(order.Status, "finished") {
		if order.Left != 0 && order.Left != order.Size {
			return "PARTIALLY_FILLED"
		}
		return "NEW"
	}
	switch strings.ToLower(order.FinishAs) {
	case "filled":
		return "FILLED"
	case "cancelled", "canceled", "ioc", "poc", "reduce_only", "stp", "liquidated", "position_closed":
		// poc here means the post-only order would have taken and was rejected, which
		// the maker-entry caller must read as "unfilled", not as an error.
		if order.Left != 0 && order.Left != order.Size {
			return "PARTIALLY_FILLED"
		}
		return "CANCELED"
	default:
		return "CANCELED"
	}
}

// sanitizeGateText coerces a caller-supplied client id into a Gate-legal `text`.
//
// Gate rejects the ENTIRE order when text is malformed, so an unusable client id must
// degrade to a valid tag rather than propagate. Rules: prefixed "t-", at most 28 bytes
// after the prefix, characters limited to [0-9A-Za-z_-.].
func sanitizeGateText(clientID string) string {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return ""
	}
	payload := strings.TrimPrefix(clientID, "t-")
	var b strings.Builder
	for _, r := range payload {
		switch {
		case r >= '0' && r <= '9', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z',
			r == '_', r == '-', r == '.':
			b.WriteRune(r)
		}
		if b.Len() >= 28 {
			break
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "t-" + b.String()
}
