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

// SetLeverage sets the leverage for a symbol.
//
// Gate encodes the margin mode in this very call:
//   - isolated: leverage = "<n>"
//   - cross:    leverage = "0" AND cross_leverage_limit = "<n>"
//
// so the mode recorded by SetMarginMode is applied here.
func (t *GateTrader) SetLeverage(symbol string, leverage int) error {
	symbol = t.convertSymbol(symbol)

	if leverage <= 0 {
		return fmt.Errorf("invalid leverage %d for %s", leverage, symbol)
	}

	t.crossMarginMutex.RLock()
	cross := t.crossMargin
	t.crossMarginMutex.RUnlock()

	leverageArg := fmt.Sprintf("%d", leverage)
	var opts *gateapi.UpdatePositionLeverageOpts
	modeStr := "isolated"
	if cross {
		// leverage=0 selects cross margin; the real multiplier goes in the opt.
		leverageArg = "0"
		opts = &gateapi.UpdatePositionLeverageOpts{
			CrossLeverageLimit: optional.NewString(fmt.Sprintf("%d", leverage)),
		}
		modeStr = "cross"
	}

	_, _, err := t.client.FuturesApi.UpdatePositionLeverage(t.ctx, "usdt", symbol, leverageArg, opts)
	if err != nil {
		// Gate.io may return error if leverage is already set
		if strings.Contains(err.Error(), "RISK_LIMIT_EXCEEDED") {
			logger.Warnf("  [Gate] Leverage %d exceeds limit for %s", leverage, symbol)
			return nil
		}
		return fmt.Errorf("failed to set leverage: %w", err)
	}

	logger.Infof("  [Gate] Leverage set to %dx (%s margin) for %s", leverage, modeStr, symbol)
	return nil
}

// SetMarginMode records the requested margin mode so the next SetLeverage encodes it.
//
// Gate has no standalone margin-mode endpoint. Applying it here would require a
// leverage value we do not have yet, and the generic layer always calls
// SetMarginMode before OpenLong/OpenShort (which call SetLeverage), so recording
// the intent and letting SetLeverage write it is both correct and race-free.
func (t *GateTrader) SetMarginMode(symbol string, isCrossMargin bool) error {
	t.crossMarginMutex.Lock()
	t.crossMargin = isCrossMargin
	t.crossMarginMutex.Unlock()

	mode := "isolated"
	if isCrossMargin {
		mode = "cross"
	}
	logger.Infof("  [Gate] Margin mode recorded as %s (applied with leverage: 0=cross + cross_leverage_limit)", mode)
	return nil
}

// OpenLong opens a long position
func (t *GateTrader) OpenLong(symbol string, quantity float64, leverage int) (map[string]interface{}, error) {
	symbol = t.convertSymbol(symbol)

	// Cancel old orders first
	t.CancelAllOrders(symbol)

	// Set leverage
	if err := t.SetLeverage(symbol, leverage); err != nil {
		logger.Warnf("  [Gate] Failed to set leverage: %v", err)
	}

	// Get contract info for size calculation
	contract, err := t.getContract(symbol)
	if err != nil {
		return nil, err
	}

	// Gate uses contract size units (each contract = quanto_multiplier base currency).
	// Truncation (not rounding) is deliberate on the OPEN path: it can only ever
	// under-size, never exceed the risk-approved notional. The CLOSE path rounds
	// instead, because there under-sizing leaves an unprotected residual position.
	quantoMultiplier, _ := strconv.ParseFloat(contract.QuantoMultiplier, 64)
	if quantoMultiplier <= 0 {
		// Guard the divide. quantity/0 is +Inf, and int64(+Inf) in Go is
		// -9223372036854775808, which is <= 0 — so the `size <= 0 -> size = 1` clamp
		// below would silently turn a metadata failure into a 1-CONTRACT order and
		// report it as success. Failing here is the only safe outcome.
		return nil, fmt.Errorf("invalid quanto_multiplier for %s", symbol)
	}
	size := int64(quantity / quantoMultiplier)
	if size <= 0 {
		size = 1
	}

	order := gateapi.FuturesOrder{
		Contract: symbol,
		Size:     size, // Positive for long
		Price:    "0",  // Market order
		Tif:      "ioc",
		Text:     gateTag,
	}

	logger.Infof("  [Gate] OpenLong: symbol=%s, size=%d, leverage=%d", symbol, size, leverage)

	result, _, err := t.client.FuturesApi.CreateFuturesOrder(t.ctx, "usdt", order, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to open long position: %w", err)
	}

	// Clear cache
	t.clearCache()

	// Parse fill price from result
	fillPrice, _ := strconv.ParseFloat(result.FillPrice, 64)

	logger.Infof("  [Gate] Opened long position: orderId=%d, fillPrice=%.4f", result.Id, fillPrice)

	return map[string]interface{}{
		"orderId":   fmt.Sprintf("%d", result.Id),
		"symbol":    t.revertSymbol(symbol),
		"status":    "FILLED",
		"fillPrice": fillPrice,
		"avgPrice":  fillPrice,
	}, nil
}

// OpenShort opens a short position
func (t *GateTrader) OpenShort(symbol string, quantity float64, leverage int) (map[string]interface{}, error) {
	symbol = t.convertSymbol(symbol)

	// Cancel old orders first
	t.CancelAllOrders(symbol)

	// Set leverage
	if err := t.SetLeverage(symbol, leverage); err != nil {
		logger.Warnf("  [Gate] Failed to set leverage: %v", err)
	}

	// Get contract info for size calculation
	contract, err := t.getContract(symbol)
	if err != nil {
		return nil, err
	}

	// Gate uses contract size units. See OpenLong for why this truncates and why the
	// multiplier must be validated before the divide.
	quantoMultiplier, _ := strconv.ParseFloat(contract.QuantoMultiplier, 64)
	if quantoMultiplier <= 0 {
		return nil, fmt.Errorf("invalid quanto_multiplier for %s", symbol)
	}
	size := int64(quantity / quantoMultiplier)
	if size <= 0 {
		size = 1
	}

	order := gateapi.FuturesOrder{
		Contract: symbol,
		Size:     -size, // Negative for short
		Price:    "0",   // Market order
		Tif:      "ioc",
		Text:     gateTag,
	}

	logger.Infof("  [Gate] OpenShort: symbol=%s, size=%d, leverage=%d", symbol, -size, leverage)

	result, _, err := t.client.FuturesApi.CreateFuturesOrder(t.ctx, "usdt", order, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to open short position: %w", err)
	}

	// Clear cache
	t.clearCache()

	// Parse fill price from result
	fillPrice, _ := strconv.ParseFloat(result.FillPrice, 64)

	logger.Infof("  [Gate] Opened short position: orderId=%d, fillPrice=%.4f", result.Id, fillPrice)

	return map[string]interface{}{
		"orderId":   fmt.Sprintf("%d", result.Id),
		"symbol":    t.revertSymbol(symbol),
		"status":    "FILLED",
		"fillPrice": fillPrice,
		"avgPrice":  fillPrice,
	}, nil
}

// CloseLong closes a long position
func (t *GateTrader) CloseLong(symbol string, quantity float64) (map[string]interface{}, error) {
	return t.closePosition(symbol, "long", quantity, "")
}

// CloseLongTagged closes a long with the close mechanism encoded in the order `text`,
// so the fill attributes 1:1 to the reason that triggered it.
//
// Reached via an anonymous interface assertion in closePositionByReasonWithOutcome
// (auto_trader_risk.go:3890). Without it, gate closes fall back to the untagged path
// and every exit is attributed by heuristics (sync_external), which is exactly the
// ambiguity the reason codec exists to remove.
func (t *GateTrader) CloseLongTagged(symbol string, quantity float64, reasonTag string) (map[string]interface{}, error) {
	return t.closePosition(symbol, "long", quantity, reasonTag)
}

// closePosition is the shared reduce-only market close for both directions.
//
// Two things it deliberately does NOT do the naive way:
//
//  1. Contract sizing uses math.Round, not int64 truncation. `int64(qty/multiplier)`
//     truncates toward zero, so a position of 2.999 contracts closed as 2 leaves a
//     live residual position that the caller believes is flat. For a full close that
//     residual is unprotected — the protection orders were already cancelled.
//
//  2. For a full close (quantity==0) it reads the position's OWN contract count from
//     Gate instead of dividing a float base quantity back by the multiplier. That
//     round trip (contracts -> base qty in GetPositions -> back to contracts here) is
//     lossy; the exchange's integer size is exact by construction.
func (t *GateTrader) closePosition(symbol, side string, quantity float64, reasonTag string) (map[string]interface{}, error) {
	gateSymbol := t.convertSymbol(symbol)

	if quantity < 0 {
		quantity = -quantity
	}

	var size int64
	if quantity == 0 {
		// Full close: take the exchange's exact integer contract count.
		contracts, err := t.rawPositionContracts(gateSymbol, side)
		if err != nil {
			return nil, err
		}
		size = contracts
	} else {
		contract, err := t.getContract(gateSymbol)
		if err != nil {
			return nil, err
		}
		quantoMultiplier, _ := strconv.ParseFloat(contract.QuantoMultiplier, 64)
		if quantoMultiplier <= 0 {
			return nil, fmt.Errorf("invalid quanto_multiplier for %s", gateSymbol)
		}
		size = int64(math.Round(quantity / quantoMultiplier))
		if size <= 0 {
			size = 1
		}
	}

	// Size sign is the close direction: negative sells (closes a long), positive buys
	// (closes a short). ReduceOnly guarantees it can never flip into a new position.
	signedSize := size
	if side == "long" {
		signedSize = -size
	}

	// Mechanism tag travels in `text`. Fall back to the bare broker tag when the reason
	// has no registered code: a malformed text makes Gate reject the whole order, and
	// losing attribution is strictly better than failing to close.
	text := gateTag
	if reasonTag != "" {
		if encoded := encodeReasonClientID(reasonTag); encoded != "" {
			text = encoded
		}
	}

	order := gateapi.FuturesOrder{
		Contract:   gateSymbol,
		Size:       signedSize,
		Price:      "0", // market
		Tif:        "ioc",
		ReduceOnly: true,
		Text:       text,
	}

	logger.Infof("  [Gate] Close %s: symbol=%s, size=%d", strings.ToUpper(side), gateSymbol, signedSize)

	result, _, err := t.client.FuturesApi.CreateFuturesOrder(t.ctx, "usdt", order, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to close %s position: %w", side, err)
	}

	t.clearCache()

	fillPrice, _ := strconv.ParseFloat(result.FillPrice, 64)
	logger.Infof("  [Gate] Closed %s position: orderId=%d, fillPrice=%.4f", side, result.Id, fillPrice)

	return map[string]interface{}{
		"orderId":   fmt.Sprintf("%d", result.Id),
		"symbol":    t.revertSymbol(gateSymbol),
		"status":    "FILLED",
		"fillPrice": fillPrice,
		"avgPrice":  fillPrice,
	}, nil
}

// rawPositionContracts returns the absolute contract count Gate holds for a side.
func (t *GateTrader) rawPositionContracts(gateSymbol, side string) (int64, error) {
	positions, _, err := t.client.FuturesApi.ListPositions(t.ctx, "usdt", nil)
	if err != nil {
		return 0, fmt.Errorf("failed to get positions: %w", err)
	}
	for _, pos := range positions {
		if pos.Contract != gateSymbol || pos.Size == 0 {
			continue
		}
		posSide := "long"
		switch pos.Mode {
		case "dual_long":
			posSide = "long"
		case "dual_short":
			posSide = "short"
		default:
			if pos.Size < 0 {
				posSide = "short"
			}
		}
		if posSide != side {
			continue
		}
		if pos.Size < 0 {
			return -pos.Size, nil
		}
		return pos.Size, nil
	}
	return 0, fmt.Errorf("%s position not found for %s", side, gateSymbol)
}

// CloseShort closes a short position
func (t *GateTrader) CloseShort(symbol string, quantity float64) (map[string]interface{}, error) {
	return t.closePosition(symbol, "short", quantity, "")
}

// CloseShortTagged closes a short with the mechanism encoded in `text`. See
// CloseLongTagged.
func (t *GateTrader) CloseShortTagged(symbol string, quantity float64, reasonTag string) (map[string]interface{}, error) {
	return t.closePosition(symbol, "short", quantity, reasonTag)
}

// GetMarketPrice gets the current market price
func (t *GateTrader) GetMarketPrice(symbol string) (float64, error) {
	symbol = t.convertSymbol(symbol)

	opts := &gateapi.ListFuturesTickersOpts{
		Contract: optional.NewString(symbol),
	}

	tickers, _, err := t.client.FuturesApi.ListFuturesTickers(t.ctx, "usdt", opts)
	if err != nil {
		return 0, fmt.Errorf("failed to get market price: %w", err)
	}

	if len(tickers) == 0 {
		return 0, fmt.Errorf("no ticker data for %s", symbol)
	}

	price, _ := strconv.ParseFloat(tickers[0].Last, 64)
	return price, nil
}

// SetStopLoss sets a stop loss order
func (t *GateTrader) SetStopLoss(symbol string, positionSide string, quantity, stopPrice float64) error {
	_, err := t.placeProtectionTrigger(symbol, positionSide, quantity, stopPrice, protectionKindStopLoss, "")
	return err
}

// SetStopLossTagged places a stop loss whose Gate `text` encodes the mechanism, so
// the eventual close attributes 1:1 instead of falling back to heuristics.
//
// The (string, error) signature is REQUIRED, not stylistic: the generic layer reaches
// the tagged path only through anonymous interface assertions that spell out
// `SetStopLossTagged(...) (string, error)` (protection_execution.go:885,921,1069 and
// auto_trader_risk.go:3786). A variant returning bare `error` fails every one of those
// assertions silently, so the tagged path would never run and every gate protection
// order would land untagged with no protection intent recorded.
func (t *GateTrader) SetStopLossTagged(symbol string, positionSide string, quantity, stopPrice float64, reasonTag string) (string, error) {
	return t.placeProtectionTrigger(symbol, positionSide, quantity, stopPrice, protectionKindStopLoss, reasonTag)
}

// protectionKind distinguishes the two trigger flavours. Gate encodes direction in
// Trigger.Rule, whose meaning depends on BOTH the position side and whether the
// order is a stop or a target, so the kind must be explicit rather than inferred.
type protectionKind int

const (
	protectionKindStopLoss protectionKind = iota
	protectionKindTakeProfit
)

func (k protectionKind) String() string {
	if k == protectionKindTakeProfit {
		return "take profit"
	}
	return "stop loss"
}

// triggerRuleFor returns Gate's Trigger.Rule for a (kind, side) pair.
//
// Gate's official semantics (SDK comment on FuturesPriceTrigger.Rule) are:
//
//	Rule 1 = fire when price >= Trigger.Price  (Gate requires Trigger.Price > last)
//	Rule 2 = fire when price <= Trigger.Price  (Gate requires Trigger.Price < last)
//
// The previous implementation had these INVERTED (it documented "1 = price <=") and
// therefore placed every protection order backwards: a long's stop loss fired on the
// way UP and its take profit on the way DOWN, i.e. the two swapped roles on all four
// (side, kind) combinations. Gate's own price-vs-rule validation would reject many of
// them outright, and any that were accepted would close the position at the wrong
// end of the move. Table below is the corrected mapping:
//
//	LONG  + stop loss    -> price falls to stop   -> <= -> Rule 2
//	LONG  + take profit  -> price rises to target -> >= -> Rule 1
//	SHORT + stop loss    -> price rises to stop   -> >= -> Rule 1
//	SHORT + take profit  -> price falls to target -> <= -> Rule 2
func triggerRuleFor(kind protectionKind, positionSide string) int32 {
	isLong := strings.ToUpper(positionSide) != "SHORT"
	if kind == protectionKindStopLoss {
		if isLong {
			return 2 // long stop: trigger when price <= stop
		}
		return 1 // short stop: trigger when price >= stop
	}
	if isLong {
		return 1 // long target: trigger when price >= target
	}
	return 2 // short target: trigger when price <= target
}

// placeProtectionTrigger is the single entry point for Gate TP/SL placement.
//
// Two Gate contract rules are enforced here, both of which the previous code broke:
//
//  1. Size vs Close are MUTUALLY EXCLUSIVE. Per the SDK comment on
//     FuturesInitialOrder: Size is "full close: size=0; partial close short: size>0;
//     partial close long: size<0", while Close "must be set to true to perform the
//     closing operation" only "when ALL positions are closed", and for a partial
//     close "you can not set close, or close=false". The old code sent a non-zero
//     Size together with Close=true, i.e. it declared a partial and a full close in
//     the same request. Whichever Gate honours, the tiered protection design breaks:
//     if Close wins, every TP/SL tier becomes a FULL-position exit and the whole
//     multi-tier ladder (TP1/TP2/TP3, dd1/dd2, partial locks) collapses into one
//     all-or-nothing order. We now send exactly one of the two.
//
//  2. Size SIGN encodes which side is being closed, independent of Rule. Closing a
//     long is negative, closing a short is positive.
//
// quantity<=0 means "close the whole position" and maps to the Size=0 + Close=true
// form; any positive quantity maps to the signed-Size partial form with Close unset.
// It returns the Gate trigger order ID so callers can record a protection intent
// keyed by the real exchange order, matching the OKX algoID contract.
func (t *GateTrader) placeProtectionTrigger(symbol, positionSide string, quantity, triggerPrice float64, kind protectionKind, reasonTag string) (string, error) {
	gateSymbol := t.convertSymbol(symbol)

	contract, err := t.getContract(gateSymbol)
	if err != nil {
		return "", err
	}
	quantoMultiplier, _ := strconv.ParseFloat(contract.QuantoMultiplier, 64)
	if quantoMultiplier <= 0 {
		return "", fmt.Errorf("invalid quanto_multiplier for %s", gateSymbol)
	}

	isLong := strings.ToUpper(positionSide) != "SHORT"

	initial := gateapi.FuturesInitialOrder{
		Contract:   gateSymbol,
		Price:      "0", // market on trigger
		Tif:        "ioc",
		ReduceOnly: true,
	}

	// Full close vs partial close: exactly one of Close / signed Size, never both.
	if quantity <= 0 {
		initial.Size = 0
		initial.Close = true
	} else {
		size := int64(math.Round(quantity / quantoMultiplier))
		if size < 1 {
			size = 1 // never round a real protection order down to nothing
		}
		if isLong {
			size = -size // closing a long is a negative size
		}
		initial.Size = size
		initial.Close = false
	}

	// Mechanism tag travels in `text` so the close attributes 1:1. Fall back to the
	// bare broker tag when the reason has no registered code — an unrecognized or
	// malformed text makes Gate reject the ENTIRE order, and losing attribution is
	// strictly better than losing the protection order.
	initial.Text = gateTag
	if reasonTag != "" {
		if encoded := encodeReasonClientID(reasonTag); encoded != "" {
			initial.Text = encoded
		}
	}

	trigger := gateapi.FuturesPriceTriggeredOrder{
		Initial: initial,
		Trigger: gateapi.FuturesPriceTrigger{
			StrategyType: 0, // price trigger
			PriceType:    0, // last traded price
			Price:        t.formatPrice(contract, triggerPrice),
			Rule:         triggerRuleFor(kind, positionSide),
		},
	}

	resp, _, err := t.client.FuturesApi.CreatePriceTriggeredOrder(t.ctx, "usdt", trigger)
	if err != nil {
		return "", fmt.Errorf("failed to set %s: %w", kind, err)
	}

	orderID := ""
	if resp.Id != 0 {
		orderID = strconv.FormatInt(resp.Id, 10)
	}

	scope := "partial"
	if initial.Close {
		scope = "full"
	}
	logger.Infof("  [Gate] %s set: %s %s @ %s (rule=%d size=%d %s id=%s)",
		kind, gateSymbol, strings.ToUpper(positionSide), trigger.Trigger.Price,
		trigger.Trigger.Rule, initial.Size, scope, orderID)
	return orderID, nil
}

// formatPrice renders a price at the contract's own tick precision. A hardcoded
// "%.8f" (the previous behaviour) is rejected by Gate for contracts whose tick is
// coarser than 8 decimals, which would fail protection placement outright.
func (t *GateTrader) formatPrice(contract *gateapi.Contract, price float64) string {
	tick, _ := strconv.ParseFloat(contract.OrderPriceRound, 64)
	if tick <= 0 {
		return strconv.FormatFloat(price, 'f', -1, 64)
	}
	rounded := math.Round(price/tick) * tick
	// Derive decimals from the tick itself so we never emit more precision than the
	// contract accepts.
	decimals := 0
	for scaled := tick; scaled < 1 && decimals < 12; scaled *= 10 {
		decimals++
	}
	return strconv.FormatFloat(rounded, 'f', decimals, 64)
}

// SetTakeProfit sets a take profit order
func (t *GateTrader) SetTakeProfit(symbol string, positionSide string, quantity, takeProfitPrice float64) error {
	_, err := t.placeProtectionTrigger(symbol, positionSide, quantity, takeProfitPrice, protectionKindTakeProfit, "")
	return err
}

// SetTakeProfitTagged places a take profit whose Gate `text` encodes the mechanism.
// See SetStopLossTagged for why the (string, error) signature is mandatory.
func (t *GateTrader) SetTakeProfitTagged(symbol string, positionSide string, quantity, takeProfitPrice float64, reasonTag string) (string, error) {
	return t.placeProtectionTrigger(symbol, positionSide, quantity, takeProfitPrice, protectionKindTakeProfit, reasonTag)
}

// CancelStopLossOrders cancels stop loss orders
func (t *GateTrader) CancelStopLossOrders(symbol string) error {
	kind := protectionKindStopLoss
	return t.cancelTriggerOrders(symbol, &kind)
}

// CancelTakeProfitOrders cancels take profit orders
func (t *GateTrader) CancelTakeProfitOrders(symbol string) error {
	kind := protectionKindTakeProfit
	return t.cancelTriggerOrders(symbol, &kind)
}

// triggerOrderSide reports the position side a trigger order closes ("LONG"/"SHORT"),
// and whether it could be determined at all.
//
// Gate's read-only `order_type` field is the authoritative source (SDK comment on
// FuturesPriceTriggeredOrder.OrderType): close-long-order / close-short-order /
// close-long-position / close-short-position / plan-close-long-position /
// plan-close-short-position. We prefer it over the Size sign because a full-close
// order carries Size=0, which has no sign to read.
func triggerOrderSide(order gateapi.FuturesPriceTriggeredOrder) (string, bool) {
	switch {
	case strings.Contains(order.OrderType, "close-long"):
		return "LONG", true
	case strings.Contains(order.OrderType, "close-short"):
		return "SHORT", true
	}
	// Fallback for partial closes: Size<0 closes a long, Size>0 closes a short.
	if order.Initial.Size < 0 {
		return "LONG", true
	}
	if order.Initial.Size > 0 {
		return "SHORT", true
	}
	return "", false
}

// classifyTriggerOrder decides whether a resting trigger order is a stop loss or a
// take profit.
//
// This CANNOT be done from Trigger.Rule alone, which is what the previous code did
// ("if order.Trigger.Rule == 2 { TAKE_PROFIT_MARKET }"). Rule only encodes the
// comparison direction (>= or <=); its protective meaning is a function of the
// position side as well:
//
//	Rule 1 (>=) is a take profit for a LONG  but a stop loss for a SHORT
//	Rule 2 (<=) is a stop loss  for a LONG  but a take profit for a SHORT
//
// So the old classifier reported every SHORT position's stop loss as a take profit
// and vice versa: the reconciler saw missingSL forever (re-arming without bound)
// while treating the real take profit as the stop. Deciding from (side, rule)
// together is what makes CanDistinguishStopTP truthful for Gate.
//
// Returns ok=false when the side cannot be established, so callers can skip rather
// than guess.
func classifyTriggerOrder(order gateapi.FuturesPriceTriggeredOrder) (protectionKind, bool) {
	side, ok := triggerOrderSide(order)
	if !ok {
		return protectionKindStopLoss, false
	}
	isLong := side == "LONG"
	switch order.Trigger.Rule {
	case 1: // fires when price rises to the trigger
		if isLong {
			return protectionKindTakeProfit, true
		}
		return protectionKindStopLoss, true
	case 2: // fires when price falls to the trigger
		if isLong {
			return protectionKindStopLoss, true
		}
		return protectionKindTakeProfit, true
	}
	return protectionKindStopLoss, false
}

// cancelTriggerOrders cancels ONLY the trigger orders matching the requested kind.
//
// The previous implementation took an orderType argument and then ignored it — the
// comment read "For simplicity, cancel all matching symbol orders" — so
// CancelStopLossOrders and CancelTakeProfitOrders behaved identically and wiped every
// trigger order on the symbol. That is exactly the bug the Trader interface calls out
// ("BUG fix: don't delete take-profit when adjusting stop-loss"): moving a break-even
// stop up would cancel all take-profits, and re-tiering a drawdown level would cancel
// the stop loss, leaving the position naked between cancel and re-place.
//
// kindFilter nil means "cancel every trigger order" (used by CancelStopOrders /
// CancelAllOrders, which legitimately clear both).
func (t *GateTrader) cancelTriggerOrders(symbol string, kindFilter *protectionKind) error {
	gateSymbol := t.convertSymbol(symbol)

	opts := &gateapi.ListPriceTriggeredOrdersOpts{
		Contract: optional.NewString(gateSymbol),
	}

	orders, _, err := t.client.FuturesApi.ListPriceTriggeredOrders(t.ctx, "usdt", "open", opts)
	if err != nil {
		return err
	}

	var firstErr error
	canceled, skipped := 0, 0
	for _, order := range orders {
		if kindFilter != nil {
			kind, ok := classifyTriggerOrder(order)
			// Unclassifiable orders are LEFT ALONE on a filtered cancel. Guessing here
			// risks cancelling the very leg we were asked to preserve.
			if !ok || kind != *kindFilter {
				skipped++
				continue
			}
		}
		if _, _, err := t.client.FuturesApi.CancelPriceTriggeredOrder(t.ctx, "usdt", fmt.Sprintf("%d", order.Id)); err != nil {
			logger.Warnf("  [Gate] Failed to cancel trigger order %d: %v", order.Id, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		canceled++
	}

	scope := "all"
	if kindFilter != nil {
		scope = kindFilter.String()
	}
	logger.Infof("  [Gate] Canceled %d %s trigger order(s) on %s (%d preserved)",
		canceled, scope, gateSymbol, skipped)
	return firstErr
}

// CancelAllOrders cancels all pending orders for a symbol
func (t *GateTrader) CancelAllOrders(symbol string) error {
	symbol = t.convertSymbol(symbol)

	// Cancel regular orders
	_, _, err := t.client.FuturesApi.CancelFuturesOrders(t.ctx, "usdt", symbol, nil)
	if err != nil {
		// Ignore if no orders to cancel
		if !strings.Contains(err.Error(), "ORDER_NOT_FOUND") {
			logger.Warnf("  [Gate] Error canceling orders: %v", err)
		}
	}

	// Cancel every trigger order (nil filter = both kinds); CancelAllOrders is
	// explicitly a clear-everything operation.
	if err := t.cancelTriggerOrders(symbol, nil); err != nil {
		logger.Warnf("  [Gate] Error canceling trigger orders: %v", err)
	}

	return nil
}

// CancelStopOrders cancels all stop orders (stop loss and take profit).
// Uses the unfiltered path so an unclassifiable leg is still cleared here — the
// filtered variants deliberately preserve what they cannot identify, but this
// function's contract is to leave no protection order behind.
func (t *GateTrader) CancelStopOrders(symbol string) error {
	return t.cancelTriggerOrders(symbol, nil)
}

// FormatQuantity formats quantity to correct precision
func (t *GateTrader) FormatQuantity(symbol string, quantity float64) (string, error) {
	contract, err := t.getContract(symbol)
	if err != nil {
		return fmt.Sprintf("%.4f", quantity), nil
	}

	// Gate uses quanto_multiplier for contract size
	quantoMultiplier, _ := strconv.ParseFloat(contract.QuantoMultiplier, 64)
	if quantoMultiplier > 0 {
		// Calculate number of contracts
		numContracts := quantity / quantoMultiplier
		return fmt.Sprintf("%.0f", math.Floor(numContracts)), nil
	}

	return fmt.Sprintf("%.4f", quantity), nil
}

// GetOrderStatus gets the status of an order
func (t *GateTrader) GetOrderStatus(symbol string, orderID string) (map[string]interface{}, error) {
	symbol = t.convertSymbol(symbol)

	order, _, err := t.client.FuturesApi.GetFuturesOrder(t.ctx, "usdt", orderID)
	if err != nil {
		return nil, fmt.Errorf("failed to get order status: %w", err)
	}

	fillPrice, _ := strconv.ParseFloat(order.FillPrice, 64)
	tkFee, _ := strconv.ParseFloat(order.Tkfr, 64)
	mkFee, _ := strconv.ParseFloat(order.Mkfr, 64)
	totalFee := tkFee + mkFee

	// Get quanto_multiplier to convert contracts to actual quantity
	quantoMultiplier := 1.0
	contract, contractErr := t.getContract(symbol)
	if contractErr == nil && contract != nil {
		qm, _ := strconv.ParseFloat(contract.QuantoMultiplier, 64)
		if qm > 0 {
			quantoMultiplier = qm
		}
	}

	// Map status
	status := "NEW"
	switch order.Status {
	case "finished":
		if order.FinishAs == "filled" {
			status = "FILLED"
		} else if order.FinishAs == "cancelled" {
			status = "CANCELED"
		} else {
			status = "CLOSED"
		}
	case "open":
		status = "NEW"
	}

	side := "BUY"
	if order.Size < 0 {
		side = "SELL"
	}

	// Convert contract count to actual token quantity
	executedQty := math.Abs(float64(order.Size-order.Left)) * quantoMultiplier

	return map[string]interface{}{
		"orderId":     orderID,
		"symbol":      t.revertSymbol(symbol),
		"status":      status,
		"avgPrice":    fillPrice,
		"executedQty": executedQty,
		"side":        side,
		"type":        order.Tif,
		"time":        int64(order.CreateTime * 1000),
		"updateTime":  int64(order.FinishTime * 1000),
		"commission":  totalFee,
	}, nil
}

// GetOpenOrders gets open/pending orders
func (t *GateTrader) GetOpenOrders(symbol string) ([]types.OpenOrder, error) {
	symbol = t.convertSymbol(symbol)

	opts := &gateapi.ListFuturesOrdersOpts{
		Contract: optional.NewString(symbol),
	}

	orders, _, err := t.client.FuturesApi.ListFuturesOrders(t.ctx, "usdt", "open", opts)
	if err != nil {
		return nil, fmt.Errorf("failed to get open orders: %w", err)
	}

	// Get quanto_multiplier to convert contracts to actual quantity
	quantoMultiplier := 1.0
	contract, err := t.getContract(symbol)
	if err == nil && contract != nil {
		qm, _ := strconv.ParseFloat(contract.QuantoMultiplier, 64)
		if qm > 0 {
			quantoMultiplier = qm
		}
	}

	var result []types.OpenOrder
	for _, order := range orders {
		price, _ := strconv.ParseFloat(order.Price, 64)

		// Size sign is the order's direction: negative sells (closing a long),
		// positive buys (closing a short). ReduceOnly/IsClose tell us it is a
		// closing order, which is what PositionSide must describe.
		side := "BUY"
		positionSide := "SHORT"
		if order.Size < 0 {
			side = "SELL"
			positionSide = "LONG"
		}
		if !order.IsReduceOnly && !order.IsClose {
			// An opening order's PositionSide is the side it establishes, which is
			// the same mapping inverted.
			if order.Size > 0 {
				positionSide = "LONG"
			} else {
				positionSide = "SHORT"
			}
		}

		// Convert contract count to actual token quantity
		quantity := math.Abs(float64(order.Size)) * quantoMultiplier

		result = append(result, types.OpenOrder{
			OrderID:        fmt.Sprintf("%d", order.Id),
			Symbol:         t.revertSymbol(order.Contract),
			Side:           side,
			PositionSide:   positionSide,
			Type:           "LIMIT",
			Price:          price,
			Quantity:       quantity,
			Status:         "NEW",
			ClientOrderID:  order.Text,
			ProtectionRole: decodeReasonFromClientID(order.Text),
			ProtectionTier: decodeTierFromClientID(order.Text),
		})
	}

	// Also get trigger orders
	triggerOpts := &gateapi.ListPriceTriggeredOrdersOpts{
		Contract: optional.NewString(symbol),
	}

	triggerOrders, _, err := t.client.FuturesApi.ListPriceTriggeredOrders(t.ctx, "usdt", "open", triggerOpts)
	if err == nil {
		for _, order := range triggerOrders {
			triggerPrice, _ := strconv.ParseFloat(order.Trigger.Price, 64)

			// PositionSide comes from order_type (authoritative, and the only source
			// that works for full closes where Size==0). Previously left EMPTY, which
			// silently disabled all 18 `order.PositionSide != "" && ...` side filters
			// across the protection stack — every order matched every position.
			positionSide, sideOK := triggerOrderSide(order)

			// Closing a long sells, closing a short buys.
			side := "BUY"
			if positionSide == "LONG" {
				side = "SELL"
			} else if !sideOK && order.Initial.Size < 0 {
				side = "SELL"
			}

			// Stop-vs-target must be decided from (side, rule) together — see
			// classifyTriggerOrder. Rule alone inverts the answer for shorts.
			orderType := "STOP_MARKET"
			coarse := "stop_loss"
			if kind, ok := classifyTriggerOrder(order); ok && kind == protectionKindTakeProfit {
				orderType = "TAKE_PROFIT_MARKET"
				coarse = "take_profit"
			} else if !ok {
				coarse = "unknown"
			}

			// A full-close trigger carries Size==0 and closes the whole position; report
			// 0 rather than a fake quantity so callers do not treat it as a 0-size order.
			quantity := math.Abs(float64(order.Initial.Size)) * quantoMultiplier

			result = append(result, types.OpenOrder{
				OrderID:              fmt.Sprintf("%d", order.Id),
				Symbol:               t.revertSymbol(order.Initial.Contract),
				Side:                 side,
				PositionSide:         positionSide,
				Type:                 orderType,
				StopPrice:            triggerPrice,
				Quantity:             quantity,
				Status:               "NEW",
				ClientOrderID:        order.Initial.Text,
				ProtectionRole:       decodeReasonFromClientID(order.Initial.Text),
				ProtectionRoleCoarse: coarse,
				ProtectionTier:       decodeTierFromClientID(order.Initial.Text),
			})
		}
	}

	return result, nil
}
