package okx

import (
	"encoding/json"
	"fmt"
	"math"
	"nofx/logger"
	"nofx/store"
	"nofx/trader/types"
	"strconv"
	"strings"
	"time"
)

// OpenLong opens long position
func (t *OKXTrader) OpenLong(symbol string, quantity float64, leverage int) (map[string]interface{}, error) {
	// Cancel old orders
	t.CancelAllOrders(symbol)

	// Set leverage
	if err := t.SetLeverage(symbol, leverage); err != nil {
		logger.Infof("  ⚠️ Failed to set leverage: %v", err)
	}

	instId := t.convertSymbol(symbol)

	// Get instrument info and calculate contract size
	inst, err := t.getInstrument(symbol)
	if err != nil {
		return nil, fmt.Errorf("failed to get instrument info: %w", err)
	}

	// OKX uses contract count, need to convert quantity (in base asset) to contract count
	// sz = quantity / ctVal (number of contracts = asset amount / asset per contract)
	sz := quantity / inst.CtVal
	szStr := t.formatSize(sz, inst)

	logger.Infof("  📊 OKX OpenLong: quantity=%.6f, ctVal=%.6f, contracts=%.2f", quantity, inst.CtVal, sz)

	// Check max market order size limit
	if inst.MaxMktSz > 0 && sz > inst.MaxMktSz {
		logger.Infof("  ⚠️ OKX market order size %.2f exceeds max %.2f, reducing to max", sz, inst.MaxMktSz)
		sz = inst.MaxMktSz
		szStr = t.formatSize(sz, inst)
	}

	body := map[string]interface{}{
		"instId":  instId,
		"tdMode":  "cross",
		"side":    "buy",
		"posSide": "long",
		"ordType": "market",
		"sz":      szStr,
		"clOrdId": genOkxClOrdID(),
		"tag":     okxTag,
	}

	data, err := t.doRequest("POST", okxOrderPath, body)
	if err != nil {
		return nil, fmt.Errorf("failed to open long position: %w", err)
	}

	var orders []struct {
		OrdId   string `json:"ordId"`
		ClOrdId string `json:"clOrdId"`
		SCode   string `json:"sCode"`
		SMsg    string `json:"sMsg"`
	}

	if err := json.Unmarshal(data, &orders); err != nil {
		return nil, fmt.Errorf("failed to parse order response: %w", err)
	}

	if len(orders) == 0 || orders[0].SCode != "0" {
		msg := "unknown error"
		if len(orders) > 0 {
			msg = orders[0].SMsg
		}
		return nil, fmt.Errorf("failed to open long position: %s", msg)
	}

	logger.Infof("✓ OKX opened long position successfully: %s size: %s", symbol, szStr)
	logger.Infof("  Order ID: %s", orders[0].OrdId)

	return map[string]interface{}{
		"orderId": orders[0].OrdId,
		"symbol":  symbol,
		"status":  "FILLED",
	}, nil
}

// OpenShort opens short position
func (t *OKXTrader) OpenShort(symbol string, quantity float64, leverage int) (map[string]interface{}, error) {
	// Cancel old orders
	t.CancelAllOrders(symbol)

	// Set leverage
	if err := t.SetLeverage(symbol, leverage); err != nil {
		logger.Infof("  ⚠️ Failed to set leverage: %v", err)
	}

	instId := t.convertSymbol(symbol)

	// Get instrument info and calculate contract size
	inst, err := t.getInstrument(symbol)
	if err != nil {
		return nil, fmt.Errorf("failed to get instrument info: %w", err)
	}

	// OKX uses contract count, need to convert quantity (in base asset) to contract count
	// sz = quantity / ctVal (number of contracts = asset amount / asset per contract)
	sz := quantity / inst.CtVal
	szStr := t.formatSize(sz, inst)

	logger.Infof("  📊 OKX OpenShort: quantity=%.6f, ctVal=%.6f, contracts=%.2f", quantity, inst.CtVal, sz)

	// Check max market order size limit
	if inst.MaxMktSz > 0 && sz > inst.MaxMktSz {
		logger.Infof("  ⚠️ OKX market order size %.2f exceeds max %.2f, reducing to max", sz, inst.MaxMktSz)
		sz = inst.MaxMktSz
		szStr = t.formatSize(sz, inst)
	}

	body := map[string]interface{}{
		"instId":  instId,
		"tdMode":  "cross",
		"side":    "sell",
		"posSide": "short",
		"ordType": "market",
		"sz":      szStr,
		"clOrdId": genOkxClOrdID(),
		"tag":     okxTag,
	}

	data, err := t.doRequest("POST", okxOrderPath, body)
	if err != nil {
		return nil, fmt.Errorf("failed to open short position: %w", err)
	}

	var orders []struct {
		OrdId   string `json:"ordId"`
		ClOrdId string `json:"clOrdId"`
		SCode   string `json:"sCode"`
		SMsg    string `json:"sMsg"`
	}

	if err := json.Unmarshal(data, &orders); err != nil {
		return nil, fmt.Errorf("failed to parse order response: %w", err)
	}

	if len(orders) == 0 || orders[0].SCode != "0" {
		msg := "unknown error"
		if len(orders) > 0 {
			msg = orders[0].SMsg
		}
		return nil, fmt.Errorf("failed to open short position: %s", msg)
	}

	logger.Infof("✓ OKX opened short position successfully: %s size: %s", symbol, szStr)
	logger.Infof("  Order ID: %s", orders[0].OrdId)

	return map[string]interface{}{
		"orderId": orders[0].OrdId,
		"symbol":  symbol,
		"status":  "FILLED",
	}, nil
}

// CloseLong closes long position
func (t *OKXTrader) CloseLong(symbol string, quantity float64) (map[string]interface{}, error) {
	return t.closeLongWithTag(symbol, quantity, "")
}

func (t *OKXTrader) CloseLongTagged(symbol string, quantity float64, reasonTag string) (map[string]interface{}, error) {
	return t.closeLongWithTag(symbol, quantity, reasonTag)
}

func (t *OKXTrader) closeLongWithTag(symbol string, quantity float64, reasonTag string) (map[string]interface{}, error) {
	instId := t.convertSymbol(symbol)

	// Get instrument info for contract conversion
	inst, err := t.getInstrument(symbol)
	if err != nil {
		return nil, fmt.Errorf("failed to get instrument info: %w", err)
	}

	// Invalidate position cache and get fresh positions
	t.InvalidatePositionCache()
	positions, err := t.GetPositions()
	if err != nil {
		return nil, fmt.Errorf("failed to get positions: %w", err)
	}

	// Find actual position from exchange
	var actualQty float64
	var posFound bool
	var posMgnMode string = "cross" // Default to cross margin
	logger.Infof("🔍 OKX CloseLong: searching for symbol=%s in %d positions", symbol, len(positions))
	for _, pos := range positions {
		logger.Infof("🔍 OKX position: symbol=%v, side=%v, positionAmt=%v, mgnMode=%v", pos["symbol"], pos["side"], pos["positionAmt"], pos["mgnMode"])
		if pos["symbol"] == symbol {
			side := pos["side"].(string)
			// In net_mode, "long" means positive position
			// In dual mode, check explicit "long" side
			if side == "long" || (t.positionMode == "net_mode" && side == "long") {
				actualQty = pos["positionAmt"].(float64)
				posFound = true
				if mgnMode, ok := pos["mgnMode"].(string); ok && mgnMode != "" {
					posMgnMode = mgnMode
				}
				logger.Infof("🔍 OKX CloseLong: found matching position! qty=%.6f, mgnMode=%s", actualQty, posMgnMode)
				break
			}
		}
	}

	if !posFound || actualQty == 0 {
		logger.Infof("🔍 OKX CloseLong: NO position found for %s LONG", symbol)
		return map[string]interface{}{
			"status":  "NO_POSITION",
			"message": fmt.Sprintf("No long position found for %s on OKX", symbol),
		}, nil
	}

	// Use actual quantity from exchange (more accurate than passed quantity)
	if quantity == 0 || quantity > actualQty {
		quantity = actualQty
	}

	// Convert quantity (base asset) to contract count
	// contracts = quantity / ctVal
	contracts := quantity / inst.CtVal
	fullContracts := actualQty / inst.CtVal

	// Resolve a lot-aligned, non-zero close size. Handles the sz=0 rejection
	// (sCode 51000) by bumping sub-lot wants up to one lot, and surfaces true
	// sub-lot "dust" positions as a terminal POSITION_DUST status instead of
	// retrying forever.
	dec := t.resolveCloseSize(contracts, fullContracts, inst)
	if dec.Skip {
		logger.Warnf("⚠️ OKX close long skipped: symbol=%s, wantContracts=%.4f, fullContracts=%.4f, lotSz=%.4f, reason=%s",
			symbol, contracts, fullContracts, inst.LotSz, dec.Reason)
		status := "SKIPPED"
		if dec.Dust {
			status = "POSITION_DUST"
		}
		return map[string]interface{}{
			"status":  status,
			"message": fmt.Sprintf("close long skipped for %s: %s", symbol, dec.Reason),
		}, nil
	}
	szStr := dec.SzStr

	logger.Infof("🔻 OKX close long: symbol=%s, instId=%s, quantity=%.6f, ctVal=%.6f, contracts=%.2f, fullContracts=%.2f, szStr=%s, bumped=%v, posMode=%s, mgnMode=%s",
		symbol, instId, quantity, inst.CtVal, contracts, fullContracts, szStr, dec.Bumped, t.positionMode, posMgnMode)

	body := map[string]interface{}{
		"instId":  instId,
		"tdMode":  posMgnMode, // Use position's actual margin mode (cross or isolated)
		"side":    "sell",
		"ordType": "market",
		"sz":      szStr,
		"clOrdId": clOrdIDForReason(reasonTag),
		"tag":     okxTag,
	}

	// Only add posSide in dual mode (long_short_mode)
	if t.positionMode == "long_short_mode" {
		body["posSide"] = "long"
	}

	data, err := t.doRequest("POST", okxOrderPath, body)
	if err != nil {
		return nil, fmt.Errorf("failed to close long position: %w", err)
	}

	var orders []struct {
		OrdId string `json:"ordId"`
		SCode string `json:"sCode"`
		SMsg  string `json:"sMsg"`
	}

	if err := json.Unmarshal(data, &orders); err != nil {
		return nil, err
	}

	if len(orders) == 0 || orders[0].SCode != "0" {
		msg := "unknown error"
		if len(orders) > 0 {
			msg = orders[0].SMsg
		}
		return nil, fmt.Errorf("failed to close long position: %s", msg)
	}

	logger.Infof("✓ OKX closed long position successfully: %s", symbol)

	// Cancel pending orders after closing position
	t.CancelAllOrders(symbol)

	return map[string]interface{}{
		"orderId": orders[0].OrdId,
		"symbol":  symbol,
		"status":  "FILLED",
	}, nil
}

// CloseShort closes short position
func (t *OKXTrader) CloseShort(symbol string, quantity float64) (map[string]interface{}, error) {
	return t.closeShortWithTag(symbol, quantity, "")
}

func (t *OKXTrader) CloseShortTagged(symbol string, quantity float64, reasonTag string) (map[string]interface{}, error) {
	return t.closeShortWithTag(symbol, quantity, reasonTag)
}

func (t *OKXTrader) closeShortWithTag(symbol string, quantity float64, reasonTag string) (map[string]interface{}, error) {
	instId := t.convertSymbol(symbol)

	// Get instrument info for contract conversion
	inst, err := t.getInstrument(symbol)
	if err != nil {
		return nil, fmt.Errorf("failed to get instrument info: %w", err)
	}

	// Invalidate position cache and get fresh positions
	t.InvalidatePositionCache()
	positions, err := t.GetPositions()
	if err != nil {
		return nil, fmt.Errorf("failed to get positions: %w", err)
	}

	// Find actual position from exchange
	var actualQty float64
	var posFound bool
	var posMgnMode string = "cross" // Default to cross margin
	logger.Infof("🔍 OKX CloseShort searching positions: symbol=%s, current position count=%d", symbol, len(positions))
	for _, pos := range positions {
		logger.Infof("🔍 OKX position: symbol=%v, side=%v, positionAmt=%v, mgnMode=%v",
			pos["symbol"], pos["side"], pos["positionAmt"], pos["mgnMode"])
		if pos["symbol"] == symbol && pos["side"] == "short" {
			actualQty = pos["positionAmt"].(float64)
			posFound = true
			if mgnMode, ok := pos["mgnMode"].(string); ok && mgnMode != "" {
				posMgnMode = mgnMode
			}
			logger.Infof("🔍 OKX found short position: quantity=%f (base asset), mgnMode=%s", actualQty, posMgnMode)
			break
		}
	}

	if !posFound || actualQty == 0 {
		return map[string]interface{}{
			"status":  "NO_POSITION",
			"message": fmt.Sprintf("No short position found for %s on OKX", symbol),
		}, nil
	}

	// Use actual quantity from exchange (more accurate than passed quantity)
	if quantity == 0 || quantity > actualQty {
		quantity = actualQty
	}

	// Ensure quantity is positive (OKX sz parameter must be positive)
	if quantity < 0 {
		quantity = -quantity
	}

	// Convert quantity (base asset) to contract count
	// contracts = quantity / ctVal
	contracts := quantity / inst.CtVal
	fullContracts := actualQty / inst.CtVal

	// Resolve a lot-aligned, non-zero close size. Handles the sz=0 rejection
	// (sCode 51000) by bumping sub-lot wants up to one lot, and surfaces true
	// sub-lot "dust" positions as a terminal POSITION_DUST status instead of
	// retrying forever.
	dec := t.resolveCloseSize(contracts, fullContracts, inst)
	if dec.Skip {
		logger.Warnf("⚠️ OKX close short skipped: symbol=%s, wantContracts=%.4f, fullContracts=%.4f, lotSz=%.4f, reason=%s",
			symbol, contracts, fullContracts, inst.LotSz, dec.Reason)
		status := "SKIPPED"
		if dec.Dust {
			status = "POSITION_DUST"
		}
		return map[string]interface{}{
			"status":  status,
			"message": fmt.Sprintf("close short skipped for %s: %s", symbol, dec.Reason),
		}, nil
	}
	szStr := dec.SzStr

	logger.Infof("🔻 OKX close short: symbol=%s, quantity=%.6f, ctVal=%.6f, contracts=%.2f, fullContracts=%.2f, szStr=%s, bumped=%v, posMode=%s, mgnMode=%s",
		symbol, quantity, inst.CtVal, contracts, fullContracts, szStr, dec.Bumped, t.positionMode, posMgnMode)

	body := map[string]interface{}{
		"instId":  instId,
		"tdMode":  posMgnMode, // Use position's actual margin mode (cross or isolated)
		"side":    "buy",
		"ordType": "market",
		"sz":      szStr,
		"clOrdId": clOrdIDForReason(reasonTag),
		"tag":     okxTag,
	}

	// Only add posSide in dual mode (long_short_mode)
	if t.positionMode == "long_short_mode" {
		body["posSide"] = "short"
	}

	logger.Infof("🔻 OKX close short request body: %+v", body)

	data, err := t.doRequest("POST", okxOrderPath, body)
	if err != nil {
		return nil, fmt.Errorf("failed to close short position: %w", err)
	}

	var orders []struct {
		OrdId string `json:"ordId"`
		SCode string `json:"sCode"`
		SMsg  string `json:"sMsg"`
	}

	if err := json.Unmarshal(data, &orders); err != nil {
		return nil, err
	}

	if len(orders) == 0 || orders[0].SCode != "0" {
		msg := "unknown error"
		if len(orders) > 0 {
			msg = fmt.Sprintf("sCode=%s, sMsg=%s", orders[0].SCode, orders[0].SMsg)
		}
		logger.Infof("❌ OKX failed to close short position: %s, response: %s", msg, string(data))
		return nil, fmt.Errorf("failed to close short position: %s", msg)
	}

	logger.Infof("✓ OKX closed short position successfully: %s, ordId=%s", symbol, orders[0].OrdId)

	// Cancel pending orders after closing position
	t.CancelAllOrders(symbol)

	return map[string]interface{}{
		"orderId": orders[0].OrdId,
		"symbol":  symbol,
		"status":  "FILLED",
	}, nil
}

// ValidateProtectionQuantity checks whether a base-asset quantity can produce a valid
// OKX contract size after lot-size rounding. It is intentionally stricter than
// FormatQuantity so protection planning can degrade before sending impossible orders.
func (t *OKXTrader) ValidateProtectionQuantity(symbol string, quantity float64) error {
	inst, err := t.getInstrument(symbol)
	if err != nil {
		return fmt.Errorf("failed to get instrument info: %w", err)
	}
	if inst.CtVal <= 0 {
		return fmt.Errorf("invalid instrument contract value")
	}
	contracts := quantity / inst.CtVal
	if inst.MinSz > 0 && contracts < inst.MinSz {
		return fmt.Errorf("quantity %.8f below min contracts %.8f", contracts, inst.MinSz)
	}
	if inst.LotSz > 0 && contracts < inst.LotSz {
		return fmt.Errorf("quantity %.8f below lot size %.8f", contracts, inst.LotSz)
	}
	formatted := t.formatSize(contracts, inst)
	formattedContracts, err := strconv.ParseFloat(formatted, 64)
	if err != nil || formattedContracts <= 0 {
		return fmt.Errorf("quantity %.8f rounds to invalid contract size %q", contracts, formatted)
	}
	if inst.MinSz > 0 && formattedContracts < inst.MinSz {
		return fmt.Errorf("quantity %.8f rounds below min contracts %.8f", formattedContracts, inst.MinSz)
	}
	return nil
}

func normalizeOKXCallbackRatio(callbackRatio float64) float64 {
	if callbackRatio <= 0 {
		return 0
	}
	if callbackRatio > 1 {
		return callbackRatio / 100.0
	}
	if callbackRatio >= 0.1 {
		return callbackRatio / 100.0
	}
	return callbackRatio
}

// SetTrailingStopLoss sets a native trailing stop on OKX advance algo orders
func (t *OKXTrader) SetTrailingStopLoss(symbol string, positionSide string, activationPrice float64, callbackRate float64, quantity float64) error {
	return t.setTrailingStopLossWithTag(symbol, positionSide, activationPrice, callbackRate, quantity, "")
}

func (t *OKXTrader) SetTrailingStopLossTagged(symbol string, positionSide string, activationPrice float64, callbackRate float64, quantity float64, reasonTag string) error {
	return t.setTrailingStopLossWithTag(symbol, positionSide, activationPrice, callbackRate, quantity, reasonTag)
}

func (t *OKXTrader) SetTrailingStopLossTaggedWithID(symbol string, positionSide string, activationPrice float64, callbackRate float64, quantity float64, reasonTag string) (string, error) {
	return t.setTrailingStopLossWithTagReturningID(symbol, positionSide, activationPrice, callbackRate, quantity, reasonTag)
}

func (t *OKXTrader) setTrailingStopLossWithTag(symbol string, positionSide string, activationPrice float64, callbackRate float64, quantity float64, reasonTag string) error {
	_, err := t.setTrailingStopLossWithTagReturningID(symbol, positionSide, activationPrice, callbackRate, quantity, reasonTag)
	return err
}

func (t *OKXTrader) setTrailingStopLossWithTagReturningID(symbol string, positionSide string, activationPrice float64, callbackRate float64, quantity float64, reasonTag string) (string, error) {
	defer t.invalidateOpenOrdersCache(symbol)
	instId := t.convertSymbol(symbol)

	inst, err := t.getInstrument(symbol)
	if err != nil {
		return "", fmt.Errorf("failed to get instrument info: %w", err)
	}

	if quantity <= 0 {
		// Trailing stops are often armed immediately after an entry fill. Avoid using
		// a stale pre-entry position cache, otherwise OKX drawdown arming can miss the
		// just-opened position and fail with "no active position found".
		t.InvalidatePositionCache()
		positions, err := t.GetPositions()
		if err != nil {
			return "", fmt.Errorf("failed to get positions for trailing stop: %w", err)
		}

		for _, pos := range positions {
			if pos["symbol"] == symbol && strings.EqualFold(fmt.Sprint(pos["side"]), strings.ToLower(positionSide)) {
				if q, ok := pos["positionAmt"].(float64); ok {
					quantity = q
					if quantity < 0 {
						quantity = -quantity
					}
					break
				}
			}
		}
	}
	if quantity <= 0 {
		return "", fmt.Errorf("no active position found for trailing stop: %s %s", symbol, positionSide)
	}

	sz := quantity / inst.CtVal
	if inst.MinSz > 0 && sz < inst.MinSz {
		sz = inst.MinSz
	}
	szStr := t.formatSize(sz, inst)

	side := "sell"
	posSide := "long"
	if strings.ToUpper(positionSide) == "SHORT" {
		side = "buy"
		posSide = "short"
	}

	body := map[string]interface{}{
		"instId":        instId,
		"tdMode":        "cross",
		"side":          side,
		"posSide":       posSide,
		"ordType":       "move_order_stop",
		"sz":            szStr,
		"callbackRatio": strconv.FormatFloat(callbackRate, 'f', -1, 64),
		"tag":           okxTag,
	}
	// Carry the mechanism in the client-controlled algo id. Without this the trailing
	// order's reason is unrecoverable (tag is fully consumed by okxTag) and targeted
	// cleanup cannot distinguish it from any other algo on the symbol.
	if algoClOrdID := encodeReasonClientID(reasonTag); algoClOrdID != "" {
		body["algoClOrdId"] = algoClOrdID
	}
	if activationPrice > 0 {
		body["activePx"] = t.formatPrice(activationPrice, inst)
	}

	resp, err := t.doRequest("POST", okxAdvanceAlgoPath, body)
	if err != nil {
		return "", fmt.Errorf("failed to set trailing stop loss: %w", err)
	}

	var orders []struct {
		AlgoId string `json:"algoId"`
		SCode  string `json:"sCode"`
		SMsg   string `json:"sMsg"`
	}
	if err := json.Unmarshal(resp, &orders); err == nil && len(orders) > 0 {
		if orders[0].SCode != "0" {
			return "", fmt.Errorf("OKX trailing stop rejected: code=%s msg=%s", orders[0].SCode, orders[0].SMsg)
		}
		logger.Infof("  ✓ [OKX] Trailing stop set: %s activation=%.4f callback=%.4f qty=%.4f sz=%s algoId=%s", symbol, activationPrice, callbackRate, quantity, szStr, orders[0].AlgoId)
		// Safety: for full trailing, keep a single live trailing order per symbol.
		// For partial trailing drawdown, multiple tiers must be allowed to coexist.
		if orders[0].AlgoId != "" && quantity <= 0 {
			if err := t.cancelOtherTrailingStopOrders(symbol, orders[0].AlgoId); err != nil {
				logger.Infof("  ⚠️ Failed to prune older OKX trailing stop orders for %s: %v", symbol, err)
			}
		}
		return orders[0].AlgoId, nil
	}

	logger.Infof("  ✓ [OKX] Trailing stop set: %s activation=%.4f callback=%.4f qty=%.4f sz=%s resp=%s", symbol, activationPrice, callbackRate, quantity, szStr, string(resp))
	return "", nil
}

func (t *OKXTrader) cancelOtherTrailingStopOrders(symbol string, keepAlgoID string) error {
	instId := t.convertSymbol(symbol)
	path := fmt.Sprintf("%s?instType=SWAP&instId=%s&ordType=move_order_stop", okxAlgoPendingPath, instId)
	data, err := t.doRequest("GET", path, nil)
	if err != nil {
		return fmt.Errorf("failed to get trailing stop algo orders: %w", err)
	}

	var orders []struct {
		AlgoId string `json:"algoId"`
		InstId string `json:"instId"`
	}
	if err := json.Unmarshal(data, &orders); err != nil {
		return fmt.Errorf("failed to parse trailing stop algo orders: %w", err)
	}

	canceled := 0
	for _, order := range orders {
		if order.AlgoId == "" || order.AlgoId == keepAlgoID {
			continue
		}
		body := []map[string]interface{}{{
			"algoId": order.AlgoId,
			"instId": order.InstId,
		}}
		if _, err := t.doRequest("POST", okxCancelAdvanceAlgoPath, body); err != nil {
			logger.Infof("  ⚠️ Failed to cancel stale OKX trailing stop algo %s: %v", order.AlgoId, err)
			continue
		}
		canceled++
	}
	if canceled > 0 {
		logger.Infof("  ✓ Canceled %d stale OKX trailing stop orders for %s", canceled, symbol)
	}
	return nil
}

func (t *OKXTrader) CancelTrailingStopOrders(symbol string) error {
	defer t.invalidateOpenOrdersCache(symbol)
	instId := t.convertSymbol(symbol)
	path := fmt.Sprintf("%s?instType=SWAP&instId=%s&ordType=move_order_stop", okxAlgoPendingPath, instId)
	data, err := t.doRequest("GET", path, nil)
	if err != nil {
		return fmt.Errorf("failed to get trailing stop algo orders: %w", err)
	}

	var orders []struct {
		AlgoId string `json:"algoId"`
		InstId string `json:"instId"`
	}
	if err := json.Unmarshal(data, &orders); err != nil {
		return fmt.Errorf("failed to parse trailing stop algo orders: %w", err)
	}
	logger.Infof("  🔍 [OKX] CancelTrailingStopOrders query for %s returned %d move_order_stop orders", symbol, len(orders))

	for _, order := range orders {
		body := []map[string]interface{}{{
			"algoId": order.AlgoId,
			"instId": order.InstId,
		}}
		if _, err := t.doRequest("POST", okxCancelAdvanceAlgoPath, body); err != nil {
			logger.Infof("  ⚠️ Failed to cancel OKX trailing stop algo %s: %v", order.AlgoId, err)
		}
	}
	return nil
}

func (t *OKXTrader) CancelTrailingStopOrdersByIDs(symbol string, orderIDs []string) error {
	defer t.invalidateOpenOrdersCache(symbol)
	if len(orderIDs) == 0 {
		return nil
	}
	instId := t.convertSymbol(symbol)
	set := make(map[string]struct{}, len(orderIDs))
	for _, id := range orderIDs {
		if id != "" {
			set[id] = struct{}{}
		}
	}
	path := fmt.Sprintf("%s?instType=SWAP&instId=%s&ordType=move_order_stop", okxAlgoPendingPath, instId)
	data, err := t.doRequest("GET", path, nil)
	if err != nil {
		return fmt.Errorf("failed to get trailing stop algo orders: %w", err)
	}
	var orders []struct {
		AlgoId string `json:"algoId"`
		InstId string `json:"instId"`
	}
	if err := json.Unmarshal(data, &orders); err != nil {
		return fmt.Errorf("failed to parse trailing stop algo orders: %w", err)
	}
	for _, order := range orders {
		if _, ok := set[order.AlgoId]; !ok {
			continue
		}
		body := []map[string]interface{}{{"algoId": order.AlgoId, "instId": order.InstId}}
		if _, err := t.doRequest("POST", okxCancelAdvanceAlgoPath, body); err != nil {
			return fmt.Errorf("failed to cancel targeted OKX trailing stop algo %s: %w", order.AlgoId, err)
		}
	}
	return nil
}

// cancelAlgoOrdersByReason cancels ONLY the live algo orders whose coded client id
// decodes to the requested mechanism.
//
// It used to filter on `tag == okxReasonTag(reason)`. Because okxTag already fills
// the 16-char tag budget, that comparison reduced to "tag == okxTag" and matched
// every bot conditional algo on the symbol — so a call asking for ladder_sl also
// cancelled every other SL tier and every TP tier (conditional covers both). The
// caller's contract forbids broad-cancelling protection while a position is active,
// and this silently violated it.
//
// Fail-safe direction: an order whose client id cannot be decoded is SKIPPED, never
// cancelled. Leaving one extra protection order alive is recoverable; cancelling a
// live stop that is actually protecting a position is not.
//
// Consequence to be aware of: orders placed before coded client ids existed carry
// nothing to decode, so this degrades to a no-op for them. That is deliberate and
// leaves no real gap — actual surplus-order cleanup runs through
// cancelUnexpectedProtectionOrdersByID (explicit algoId, driven by the reconciler's
// ownership diff) and, for fully inactive symbols, the broad orphan sweep. This
// function is only the targeted mechanism-scoped variant.
func (t *OKXTrader) cancelAlgoOrdersByReason(symbol string, ordType string, reasonTag string) error {
	defer t.invalidateOpenOrdersCache(symbol)
	wantReason := store.NormalizeMechanism(reasonTag)
	if wantReason == "" || store.CodeForReason(wantReason) == "" {
		// No registered code means we cannot prove which orders belong to this
		// mechanism. Refuse rather than fall back to cancelling everything.
		logger.Infof("  ⏭️ [OKX] Skip tagged cleanup for %s: reason %q has no registered mechanism code", symbol, reasonTag)
		return nil
	}
	instId := t.convertSymbol(symbol)
	path := fmt.Sprintf("%s?instType=SWAP&instId=%s&ordType=%s", okxAlgoPendingPath, instId, ordType)
	data, err := t.doRequest("GET", path, nil)
	if err != nil {
		return fmt.Errorf("failed to get algo orders for cleanup: %w", err)
	}

	var orders []struct {
		AlgoId      string `json:"algoId"`
		InstId      string `json:"instId"`
		Tag         string `json:"tag"`
		AlgoClOrdID string `json:"algoClOrdId"`
	}
	if err := json.Unmarshal(data, &orders); err != nil {
		return fmt.Errorf("failed to parse algo orders for cleanup: %w", err)
	}

	matched, skipped := 0, 0
	for _, order := range orders {
		if decodeReasonFromClientID(order.AlgoClOrdID) != wantReason {
			skipped++
			continue
		}
		matched++
		body := []map[string]interface{}{{"algoId": order.AlgoId, "instId": order.InstId}}
		if _, err := t.doRequest("POST", okxCancelAlgoPath, body); err != nil {
			return fmt.Errorf("failed to cancel tagged algo order %s: %w", order.AlgoId, err)
		}
	}
	if matched > 0 || skipped > 0 {
		logger.Infof("  🎯 [OKX] Targeted cleanup %s %s: cancelled=%d preserved=%d (preserved = other mechanisms or no decodable client id)",
			symbol, wantReason, matched, skipped)
	}
	return nil
}

func (t *OKXTrader) CancelStopLossOrdersTagged(symbol string, reasonTag string) error {
	return t.cancelAlgoOrdersByReason(symbol, "conditional", reasonTag)
}

func (t *OKXTrader) CancelTakeProfitOrdersTagged(symbol string, reasonTag string) error {
	return t.cancelAlgoOrdersByReason(symbol, "conditional", reasonTag)
}

// SetStopLoss sets stop loss order
func (t *OKXTrader) SetStopLoss(symbol string, positionSide string, quantity, stopPrice float64) error {
	_, err := t.setStopLossWithTag(symbol, positionSide, quantity, stopPrice, "")
	return err
}

func (t *OKXTrader) SetStopLossTagged(symbol string, positionSide string, quantity, stopPrice float64, reasonTag string) (string, error) {
	return t.setStopLossWithTag(symbol, positionSide, quantity, stopPrice, reasonTag)
}

func (t *OKXTrader) setStopLossWithTag(symbol string, positionSide string, quantity, stopPrice float64, reasonTag string) (string, error) {
	defer t.invalidateOpenOrdersCache(symbol)
	instId := t.convertSymbol(symbol)

	// Get instrument info
	inst, err := t.getInstrument(symbol)
	if err != nil {
		return "", fmt.Errorf("failed to get instrument info: %w", err)
	}

	// Calculate contract size: quantity (in base asset) / ctVal (asset per contract)
	sz := quantity / inst.CtVal
	if inst.MinSz > 0 && sz < inst.MinSz {
		sz = inst.MinSz
	}
	szStr := t.formatSize(sz, inst)

	// Determine direction
	side := "sell"
	posSide := "long"
	if strings.ToUpper(positionSide) == "SHORT" {
		side = "buy"
		posSide = "short"
	}

	body := map[string]interface{}{
		"instId":      instId,
		"tdMode":      "cross",
		"side":        side,
		"posSide":     posSide,
		"ordType":     "conditional",
		"sz":          szStr,
		"slTriggerPx": t.formatPrice(stopPrice, inst),
		"slOrdPx":     "-1", // Market price
		"tag":         okxTag,
	}
	// Deterministic attribution: carry the mechanism inside a client-controlled
	// algo id so the eventual close fill decodes its own reason (no price guessing).
	// Only set when the reason has a registered code; unknown reasons fall through
	// to the plain tag path unchanged.
	algoClOrdID := encodeReasonClientID(reasonTag)
	if algoClOrdID != "" {
		body["algoClOrdId"] = algoClOrdID
	}

	resp, err := t.doRequest("POST", okxAlgoOrderPath, body)
	if err != nil {
		return "", fmt.Errorf("failed to set stop loss: %w", err)
	}
	algoID, err := parseOKXAlgoOrderResponse(resp, "stop loss")
	if err != nil {
		return "", err
	}

	logger.Infof("  Stop loss price set: %.4f algoId=%s algoClOrdId=%s reason=%s", stopPrice, algoID, algoClOrdID, reasonTag)
	return algoID, nil
}

// SetTakeProfit sets take profit order
func (t *OKXTrader) SetTakeProfit(symbol string, positionSide string, quantity, takeProfitPrice float64) error {
	_, err := t.setTakeProfitWithTag(symbol, positionSide, quantity, takeProfitPrice, "")
	return err
}

func (t *OKXTrader) SetTakeProfitTagged(symbol string, positionSide string, quantity, takeProfitPrice float64, reasonTag string) (string, error) {
	return t.setTakeProfitWithTag(symbol, positionSide, quantity, takeProfitPrice, reasonTag)
}

func (t *OKXTrader) setTakeProfitWithTag(symbol string, positionSide string, quantity, takeProfitPrice float64, reasonTag string) (string, error) {
	defer t.invalidateOpenOrdersCache(symbol)
	instId := t.convertSymbol(symbol)

	// Get instrument info
	inst, err := t.getInstrument(symbol)
	if err != nil {
		return "", fmt.Errorf("failed to get instrument info: %w", err)
	}

	// Calculate contract size: quantity (in base asset) / ctVal (asset per contract)
	sz := quantity / inst.CtVal
	if inst.MinSz > 0 && sz < inst.MinSz {
		sz = inst.MinSz
	}
	szStr := t.formatSize(sz, inst)

	// Determine direction
	side := "sell"
	posSide := "long"
	if strings.ToUpper(positionSide) == "SHORT" {
		side = "buy"
		posSide = "short"
	}

	body := map[string]interface{}{
		"instId":      instId,
		"tdMode":      "cross",
		"side":        side,
		"posSide":     posSide,
		"ordType":     "conditional",
		"sz":          szStr,
		"tpTriggerPx": t.formatPrice(takeProfitPrice, inst),
		"tpOrdPx":     "-1", // Market price
		"tag":         okxTag,
	}
	// Deterministic attribution: carry the mechanism inside a client-controlled
	// algo id (see reason_codec.go). Only set for reasons with a registered code.
	algoClOrdID := encodeReasonClientID(reasonTag)
	if algoClOrdID != "" {
		body["algoClOrdId"] = algoClOrdID
	}

	resp, err := t.doRequest("POST", okxAlgoOrderPath, body)
	if err != nil {
		return "", fmt.Errorf("failed to set take profit: %w", err)
	}
	algoID, err := parseOKXAlgoOrderResponse(resp, "take profit")
	if err != nil {
		return "", err
	}

	logger.Infof("  Take profit price set: %.4f algoId=%s algoClOrdId=%s reason=%s", takeProfitPrice, algoID, algoClOrdID, reasonTag)
	return algoID, nil
}

func (t *OKXTrader) CancelAlgoOrderByID(symbol string, algoID string) error {
	defer t.invalidateOpenOrdersCache(symbol)
	algoID = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(algoID, "_sl"), "_tp"))
	if algoID == "" {
		return nil
	}
	instId := t.convertSymbol(symbol)
	body := []map[string]interface{}{{"algoId": algoID, "instId": instId}}
	data, err := t.doRequest("POST", okxCancelAlgoPath, body)
	if err != nil {
		return fmt.Errorf("failed to cancel algo order %s: %w", algoID, err)
	}
	// doRequest treats top-level code=1 (partial success) as non-error, so a per-order
	// failure (e.g. wrong endpoint, already gone) surfaces ONLY inside data[].sCode. Without
	// this check a failed cancel would log a fake success — the exact bug that left structural
	// backup algos resting on the exchange after a "successful" cancel. sCode "0" = canceled;
	// "51400"/"51401" = order already canceled/does-not-exist (idempotent, treat as success).
	if err := okxAlgoCancelResultErr(data, algoID); err != nil {
		return err
	}
	logger.Infof("  ✓ Canceled algo order by id for %s: %s", symbol, algoID)
	return nil
}

// okxAlgoCancelResultErr inspects the per-order result array from cancel-algos and returns
// an error when the order was NOT actually canceled. "Already gone" sCodes are idempotent
// successes (the desired end-state — no resting order — is achieved).
func okxAlgoCancelResultErr(data []byte, algoID string) error {
	if len(data) == 0 {
		return nil
	}
	var results []struct {
		AlgoID string `json:"algoId"`
		SCode  string `json:"sCode"`
		SMsg   string `json:"sMsg"`
	}
	if err := json.Unmarshal(data, &results); err != nil {
		return nil // unparseable body: don't fail the caller on a shape mismatch
	}
	for _, r := range results {
		switch r.SCode {
		case "", "0", "51400", "51401", "51402": // canceled / already-canceled / not-found
			return nil
		default:
			return fmt.Errorf("OKX cancel-algo rejected algoId=%s: sCode=%s sMsg=%s", algoID, r.SCode, r.SMsg)
		}
	}
	return nil
}

// CancelStopLossOrders cancels stop loss orders
func (t *OKXTrader) CancelStopLossOrders(symbol string) error {
	return t.cancelAlgoOrders(symbol, "sl")
}

// CancelTakeProfitOrders cancels take profit orders
func (t *OKXTrader) CancelTakeProfitOrders(symbol string) error {
	return t.cancelAlgoOrders(symbol, "tp")
}

// cancelAlgoOrders cancels algo orders
func (t *OKXTrader) cancelAlgoOrders(symbol string, orderType string) error {
	defer t.invalidateOpenOrdersCache(symbol)
	instId := t.convertSymbol(symbol)

	// Get pending algo orders
	path := fmt.Sprintf("%s?instType=SWAP&instId=%s&ordType=conditional", okxAlgoPendingPath, instId)
	data, err := t.doRequest("GET", path, nil)
	if err != nil {
		return err
	}

	var orders []struct {
		AlgoId string `json:"algoId"`
		InstId string `json:"instId"`
	}

	if err := json.Unmarshal(data, &orders); err != nil {
		return err
	}

	canceledCount := 0
	for _, order := range orders {
		body := []map[string]interface{}{
			{
				"algoId": order.AlgoId,
				"instId": order.InstId,
			},
		}

		_, err := t.doRequest("POST", okxCancelAlgoPath, body)
		if err != nil {
			logger.Infof("  ⚠️ Failed to cancel algo order: %v", err)
			continue
		}
		canceledCount++
	}

	if canceledCount > 0 {
		logger.Infof("  ✓ Canceled %d algo orders for %s", canceledCount, symbol)
	}

	return nil
}

// CancelTakeProfitOrdersByPrices cancels only TP algo orders whose trigger prices match
// the provided targets. This avoids wiping ladder/full TP orders when cleaning up
// a failed drawdown plan.
func (t *OKXTrader) CancelTakeProfitOrdersByPrices(symbol string, prices []float64) error {
	defer t.invalidateOpenOrdersCache(symbol)
	instId := t.convertSymbol(symbol)
	path := fmt.Sprintf("%s?instType=SWAP&instId=%s&ordType=conditional", okxAlgoPendingPath, instId)
	data, err := t.doRequest("GET", path, nil)
	if err != nil {
		return err
	}

	var orders []struct {
		AlgoId      string `json:"algoId"`
		InstId      string `json:"instId"`
		TpTriggerPx string `json:"tpTriggerPx"`
	}
	if err := json.Unmarshal(data, &orders); err != nil {
		return err
	}

	canceledCount := 0
	for _, order := range orders {
		if order.TpTriggerPx == "" {
			continue
		}
		tpPrice, _ := strconv.ParseFloat(order.TpTriggerPx, 64)
		matched := false
		for _, target := range prices {
			if target > 0 && math.Abs(tpPrice-target)/math.Max(math.Abs(tpPrice), math.Abs(target)) <= 0.005 {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}

		body := []map[string]interface{}{{
			"algoId": order.AlgoId,
			"instId": order.InstId,
		}}
		if _, err := t.doRequest("POST", okxCancelAlgoPath, body); err != nil {
			logger.Infof("  ⚠️ Failed to cancel targeted TP algo order: %v", err)
			continue
		}
		canceledCount++
	}

	if canceledCount > 0 {
		logger.Infof("  ✓ Canceled %d targeted TP algo orders for %s", canceledCount, symbol)
	}
	return nil
}

// CancelAllOrders cancels all pending orders
func (t *OKXTrader) CancelAllOrders(symbol string) error {
	defer t.invalidateOpenOrdersCache(symbol)
	instId := t.convertSymbol(symbol)

	// Get pending orders
	path := fmt.Sprintf("%s?instType=SWAP&instId=%s", okxPendingOrdersPath, instId)
	data, err := t.doRequest("GET", path, nil)
	if err != nil {
		return err
	}

	var orders []struct {
		OrdId  string `json:"ordId"`
		InstId string `json:"instId"`
	}

	if err := json.Unmarshal(data, &orders); err != nil {
		return err
	}

	// Batch cancel
	for _, order := range orders {
		body := map[string]interface{}{
			"instId": order.InstId,
			"ordId":  order.OrdId,
		}
		t.doRequest("POST", okxCancelOrderPath, body)
	}

	// Also cancel algo orders
	t.cancelAlgoOrders(symbol, "")

	if len(orders) > 0 {
		logger.Infof("  ✓ Canceled all pending orders for %s", symbol)
	}

	return nil
}

// CancelStopOrders cancels stop loss and take profit orders
func (t *OKXTrader) CancelStopOrders(symbol string) error {
	defer t.invalidateOpenOrdersCache(symbol)
	return t.cancelAlgoOrders(symbol, "")
}

// GetOrderStatus gets order status
func (t *OKXTrader) GetOrderStatus(symbol string, orderID string) (map[string]interface{}, error) {
	instId := t.convertSymbol(symbol)
	path := fmt.Sprintf("/api/v5/trade/order?instId=%s&ordId=%s", instId, orderID)

	data, err := t.doRequest("GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get order status: %w", err)
	}

	var orders []struct {
		OrdId       string `json:"ordId"`
		State       string `json:"state"`
		AvgPx       string `json:"avgPx"`
		AccFillSz   string `json:"accFillSz"`
		Fee         string `json:"fee"`
		Side        string `json:"side"`
		OrdType     string `json:"ordType"`
		CTime       string `json:"cTime"`
		UTime       string `json:"uTime"`
		AlgoId      string `json:"algoId"`      // set when this order was spawned by an algo (TP/SL/trailing)
		AlgoClOrdId string `json:"algoClOrdId"` // client algo id, if provided at algo placement
	}

	if err := json.Unmarshal(data, &orders); err != nil {
		return nil, err
	}

	if len(orders) == 0 {
		return nil, fmt.Errorf("order not found")
	}

	order := orders[0]
	avgPrice, _ := strconv.ParseFloat(order.AvgPx, 64)
	fillSz, _ := strconv.ParseFloat(order.AccFillSz, 64) // This is in contracts
	fee, _ := strconv.ParseFloat(order.Fee, 64)
	cTime, _ := strconv.ParseInt(order.CTime, 10, 64)
	uTime, _ := strconv.ParseInt(order.UTime, 10, 64)

	// Convert contract count to base asset quantity
	// executedQty = contracts * ctVal
	executedQty := fillSz
	inst, err := t.getInstrument(symbol)
	if err == nil && inst.CtVal > 0 {
		executedQty = fillSz * inst.CtVal
		logger.Debugf("  📊 OKX order %s: fillSz(contracts)=%.4f, ctVal=%.6f, executedQty=%.6f", orderID, fillSz, inst.CtVal, executedQty)
	}

	// Status mapping
	statusMap := map[string]string{
		"filled":           "FILLED",
		"live":             "NEW",
		"partially_filled": "PARTIALLY_FILLED",
		"canceled":         "CANCELED",
	}

	status := statusMap[order.State]
	if status == "" {
		status = order.State
	}

	return map[string]interface{}{
		"orderId":     order.OrdId,
		"symbol":      symbol,
		"status":      status,
		"avgPrice":    avgPrice,
		"executedQty": executedQty,
		"side":        order.Side,
		"type":        order.OrdType,
		"time":        cTime,
		"updateTime":  uTime,
		"commission":  -fee, // OKX returns negative value
		"algoId":      order.AlgoId,
		"algoClOrdId": order.AlgoClOrdId,
	}, nil
}

// GetOrderLinkedAlgoID returns the algoId that spawned the given order, or "".
// When an OKX TP/SL/trailing algo triggers, it creates a regular order whose
// detail carries the originating algoId. This is the deterministic link from a
// close fill back to the protection order that caused it (the fill's ordId is a
// fresh id, not the stored algoId). Returns "" when the order was not algo-spawned.
func (t *OKXTrader) GetOrderLinkedAlgoID(symbol, orderID string) (string, error) {
	if orderID == "" {
		return "", nil
	}
	st, err := t.GetOrderStatus(symbol, orderID)
	if err != nil {
		return "", err
	}
	if algoID, ok := st["algoId"].(string); ok && algoID != "" {
		return algoID, nil
	}
	return "", nil
}

// GetOrderLinkedReason resolves the canonical close mechanism for a fill's order
// id by reading the order detail's algoClOrdId (the client-controlled id we set at
// placement) and decoding it. This is the VERIFIED-available exact path: the
// order-detail endpoint returns algoClOrdId even when the triggered algo spawned a
// fresh fill ordId, so attribution does not depend on the fills-history feed
// echoing the client id. Returns "" (never guesses) when the order carries no
// recognizable coded id.
func (t *OKXTrader) GetOrderLinkedReason(symbol, orderID string) (string, error) {
	if orderID == "" {
		return "", nil
	}
	st, err := t.GetOrderStatus(symbol, orderID)
	if err != nil {
		return "", err
	}
	if aco, ok := st["algoClOrdId"].(string); ok {
		if reason := decodeReasonFromClientID(aco); reason != "" {
			return reason, nil
		}
	}
	return "", nil
}

// GetOpenOrders gets all open/pending orders for a symbol
func (t *OKXTrader) GetOpenOrders(symbol string) ([]types.OpenOrder, error) {
	// Short-lived per-symbol cache: each call below does 3 serial OKX GETs, and the
	// dashboard loads protection orders per position. Serve a fresh-enough cached
	// copy to collapse N*3 round-trips during a dashboard load (fix 2026-06-10).
	// TTL bumped 4s->8s (2026-06-23): accounts holding ~10 symbols issue 3-4
	// algo-pending sub-queries each per monitor pass, saturating the per-key OKX
	// rate limit (orders-algo-pending) and dragging /api/positions to ~4.5s. Any
	// order mutation busts this cache via invalidateOpenOrdersCache, so a longer
	// read TTL cannot serve stale state after a place/cancel/amend.
	const openOrdersCacheTTL = 8 * time.Second
	if cached, ok := t.readOpenOrdersCache(symbol, openOrdersCacheTTL); ok {
		return cached, nil
	}

	// singleflight collapses concurrent misses for the same symbol into one fetch;
	// the waiters share the leader's result, so we hand each caller its own copy.
	v, err, _ := t.openOrdersSF.Do(symbol, func() (interface{}, error) {
		// Re-check the cache: a concurrent leader may have populated it while we
		// queued behind singleflight.
		if cached, ok := t.readOpenOrdersCache(symbol, openOrdersCacheTTL); ok {
			return cached, nil
		}
		return t.fetchOpenOrders(symbol)
	})
	if err != nil {
		return nil, err
	}
	orders, _ := v.([]types.OpenOrder)
	out := make([]types.OpenOrder, len(orders))
	copy(out, orders)
	return out, nil
}

// readOpenOrdersCache returns a copy of the cached open orders for symbol when a
// fresh entry exists.
func (t *OKXTrader) readOpenOrdersCache(symbol string, ttl time.Duration) ([]types.OpenOrder, bool) {
	t.openOrdersCacheMutex.RLock()
	defer t.openOrdersCacheMutex.RUnlock()
	if ent, ok := t.cachedOpenOrders[symbol]; ok && time.Since(ent.at) < ttl {
		cached := make([]types.OpenOrder, len(ent.orders))
		copy(cached, ent.orders)
		return cached, true
	}
	return nil, false
}

// fetchOpenOrders performs the actual OKX round-trips (limit + conditional algo +
// trailing) and populates the per-symbol cache. Callers reach it through
// GetOpenOrders, which guards it with the cache and singleflight.
func (t *OKXTrader) fetchOpenOrders(symbol string) ([]types.OpenOrder, error) {
	instId := t.convertSymbol(symbol)
	var result []types.OpenOrder

	inst, instErr := t.getInstrument(symbol)
	if instErr != nil {
		logger.Warnf("[OKX] Failed to get instrument for open-order quantity conversion (%s): %v", symbol, instErr)
	}
	ctVal := 1.0
	if instErr == nil && inst.CtVal > 0 {
		ctVal = inst.CtVal
	}

	// 1. Get pending limit orders
	path := fmt.Sprintf("%s?instId=%s&instType=SWAP", okxPendingOrdersPath, instId)
	data, err := t.doRequest("GET", path, nil)
	if err != nil {
		logger.Warnf("[OKX] Failed to get pending orders: %v", err)
	}
	if err == nil && data != nil {
		var orders []struct {
			OrdId   string `json:"ordId"`
			InstId  string `json:"instId"`
			Side    string `json:"side"`    // buy/sell
			PosSide string `json:"posSide"` // long/short/net
			OrdType string `json:"ordType"` // limit/market/post_only
			Px      string `json:"px"`      // price
			Sz      string `json:"sz"`      // size
			State   string `json:"state"`   // live/partially_filled
		}
		if err := json.Unmarshal(data, &orders); err == nil {
			for _, order := range orders {
				price, _ := strconv.ParseFloat(order.Px, 64)
				quantityContracts, _ := strconv.ParseFloat(order.Sz, 64)
				quantity := quantityContracts * ctVal

				// Convert OKX side to standard format
				side := strings.ToUpper(order.Side)
				positionSide := strings.ToUpper(order.PosSide)
				if positionSide == "NET" {
					positionSide = "BOTH"
				}

				result = append(result, types.OpenOrder{
					OrderID:      order.OrdId,
					Symbol:       symbol,
					Side:         side,
					PositionSide: positionSide,
					Type:         strings.ToUpper(order.OrdType),
					Price:        price,
					StopPrice:    0,
					Quantity:     quantity,
					Status:       "NEW",
				})
			}
		}
	}

	// 2. Get pending algo orders (stop-loss/take-profit)
	// OKX requires ordType parameter for algo orders API
	algoPath := fmt.Sprintf("%s?instId=%s&instType=SWAP&ordType=conditional", okxAlgoPendingPath, instId)
	algoData, err := t.doRequest("GET", algoPath, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get OKX conditional algo orders for %s: %w", symbol, err)
	}
	if algoData != nil {
		var algoOrders []struct {
			AlgoId      string `json:"algoId"`
			InstId      string `json:"instId"`
			Side        string `json:"side"`
			PosSide     string `json:"posSide"`
			OrdType     string `json:"ordType"` // conditional/oco/trigger
			TriggerPx   string `json:"triggerPx"`
			SlTriggerPx string `json:"slTriggerPx"` // Stop loss trigger price
			TpTriggerPx string `json:"tpTriggerPx"` // Take profit trigger price
			Sz          string `json:"sz"`
			State       string `json:"state"`
			Tag         string `json:"tag"`
			AlgoClOrdID string `json:"algoClOrdId"`
		}
		if err := json.Unmarshal(algoData, &algoOrders); err == nil {
			for _, order := range algoOrders {
				quantityContracts, _ := strconv.ParseFloat(order.Sz, 64)
				quantity := quantityContracts * ctVal

				side := strings.ToUpper(order.Side)
				positionSide := strings.ToUpper(order.PosSide)
				if positionSide == "NET" {
					positionSide = "BOTH"
				}

				// Check for stop loss order (slTriggerPx is set)
				if order.SlTriggerPx != "" {
					slPrice, _ := strconv.ParseFloat(order.SlTriggerPx, 64)
					if slPrice > 0 {
						result = append(result, types.OpenOrder{
							OrderID:       order.AlgoId + "_sl",
							Symbol:        symbol,
							Side:          side,
							PositionSide:  positionSide,
							Type:          "STOP_MARKET",
							Price:         0,
							StopPrice:     slPrice,
							Quantity:      quantity,
							Status:        "NEW",
							ClientOrderID: order.AlgoClOrdID,
						})
					}
				}

				// Check for take profit order (tpTriggerPx is set)
				if order.TpTriggerPx != "" {
					tpPrice, _ := strconv.ParseFloat(order.TpTriggerPx, 64)
					if tpPrice > 0 {
						result = append(result, types.OpenOrder{
							OrderID:       order.AlgoId + "_tp",
							Symbol:        symbol,
							Side:          side,
							PositionSide:  positionSide,
							Type:          "TAKE_PROFIT_MARKET",
							Price:         0,
							StopPrice:     tpPrice,
							Quantity:      quantity,
							Status:        "NEW",
							ClientOrderID: order.AlgoClOrdID,
						})
					}
				}

				// Fallback for trigger orders (triggerPx is set)
				if order.TriggerPx != "" && order.SlTriggerPx == "" && order.TpTriggerPx == "" {
					triggerPrice, _ := strconv.ParseFloat(order.TriggerPx, 64)
					if triggerPrice > 0 {
						result = append(result, types.OpenOrder{
							OrderID:       order.AlgoId,
							Symbol:        symbol,
							Side:          side,
							PositionSide:  positionSide,
							Type:          "STOP_MARKET",
							Price:         0,
							StopPrice:     triggerPrice,
							Quantity:      quantity,
							Status:        "NEW",
							ClientOrderID: order.AlgoClOrdID,
						})
					}
				}
			}
		}
	}

	// 3. Get pending trailing stop algo orders (native move_order_stop)
	// Query both default (live) and effective states — OKX moves trailing orders
	// to "effective" once activation price is reached, and they no longer appear
	// in the default pending query.
	seenTrailingIDs := make(map[string]bool)
	for _, trailingState := range []string{"", "effective"} {
		trailingPath := fmt.Sprintf("%s?instId=%s&instType=SWAP&ordType=move_order_stop", okxAlgoPendingPath, instId)
		if trailingState != "" {
			trailingPath += "&state=" + trailingState
		}
		trailingData, err := t.doRequest("GET", trailingPath, nil)
		if err != nil {
			logger.Warnf("[OKX] Failed to get trailing algo orders (state=%s): %v", trailingState, err)
			continue
		}
		if trailingData == nil {
			continue
		}
		var trailingOrders []struct {
			AlgoId        string `json:"algoId"`
			InstId        string `json:"instId"`
			Side          string `json:"side"`
			PosSide       string `json:"posSide"`
			ActivePx      string `json:"activePx"`
			CallbackRatio string `json:"callbackRatio"`
			MoveTriggerPx string `json:"moveTriggerPx"`
			Sz            string `json:"sz"`
			Tag           string `json:"tag"`
			AlgoClOrdID   string `json:"algoClOrdId"`
		}
		if err := json.Unmarshal(trailingData, &trailingOrders); err == nil {
			for _, order := range trailingOrders {
				if seenTrailingIDs[order.AlgoId] {
					continue
				}
				seenTrailingIDs[order.AlgoId] = true
				quantityContracts, _ := strconv.ParseFloat(order.Sz, 64)
				quantity := quantityContracts * ctVal
				activePx, _ := strconv.ParseFloat(order.ActivePx, 64)
				callbackRatio, _ := strconv.ParseFloat(order.CallbackRatio, 64)
				moveTriggerPx, _ := strconv.ParseFloat(order.MoveTriggerPx, 64)
				// OKX returns callbackRatio in percentage units (for example "0.55" means
				// 0.55%). Internally OpenOrder.CallbackRate is a decimal ratio, matching
				// the value we pass when placing trailing orders (0.0055). Normalizing here
				// prevents equivalent-order detection from missing live native trailing
				// orders and re-placing the same tier every monitor/reconcile pass.
				callbackRate := normalizeOKXCallbackRatio(callbackRatio)
				side := strings.ToUpper(order.Side)
				positionSide := strings.ToUpper(order.PosSide)
				if positionSide == "NET" {
					positionSide = "BOTH"
				}
				activationStatus := "pending_activation"
				stopPrice := activePx
				if trailingState == "effective" {
					activationStatus = "activated"
					if moveTriggerPx > 0 {
						stopPrice = moveTriggerPx
					}
				} else if activePx == 0 {
					// No activePx means immediate activation — treat as activated
					activationStatus = "activated"
				}
				result = append(result, types.OpenOrder{
					OrderID:          order.AlgoId,
					Symbol:           symbol,
					Side:             side,
					PositionSide:     positionSide,
					Type:             "TRAILING_STOP_MARKET",
					Price:            0,
					StopPrice:        stopPrice,
					ActivationPrice:  activePx,
					ActivationStatus: activationStatus,
					CallbackRate:     callbackRate,
					CallbackRatePct:  callbackRatio,
					Quantity:         quantity,
					Status:           "NEW",
					ClientOrderID:    order.AlgoClOrdID,
					ProtectionRole:   reasonFromAlgoIDs(order.AlgoClOrdID, order.Tag),
					ProtectionTier:   decodeTierFromClientID(order.AlgoClOrdID),
					ParentOrderID:    order.AlgoId,
				})
			}
		}
	}

	logger.Infof("✓ OKX GetOpenOrders: found %d open orders for %s", len(result), symbol)
	t.openOrdersCacheMutex.Lock()
	t.cachedOpenOrders[symbol] = cachedOpenOrderEntry{orders: result, at: time.Now()}
	t.openOrdersCacheMutex.Unlock()
	return result, nil
}

// PlaceLimitOrder places a limit order for grid trading
// Implements GridTrader interface
func (t *OKXTrader) PlaceLimitOrder(req *types.LimitOrderRequest) (*types.LimitOrderResult, error) {
	instId := t.convertSymbol(req.Symbol)

	// Get instrument info
	inst, err := t.getInstrument(req.Symbol)
	if err != nil {
		return nil, fmt.Errorf("failed to get instrument info: %w", err)
	}

	// Set leverage if specified
	if req.Leverage > 0 {
		if err := t.SetLeverage(req.Symbol, req.Leverage); err != nil {
			logger.Warnf("[OKX] Failed to set leverage: %v", err)
		}
	}

	// Convert quantity to contract size
	sz := req.Quantity / inst.CtVal
	szStr := t.formatSize(sz, inst)

	// Determine side and position side
	side := "buy"
	posSide := "long"
	if req.Side == "SELL" {
		side = "sell"
		posSide = "short"
	}

	ordType := "limit"
	if req.PostOnly {
		ordType = "post_only"
	}

	body := map[string]interface{}{
		"instId":  instId,
		"tdMode":  "cross",
		"side":    side,
		"posSide": posSide,
		"ordType": ordType,
		"sz":      szStr,
		"px":      fmt.Sprintf("%.8f", req.Price),
		"clOrdId": genOkxClOrdID(),
		"tag":     okxTag,
	}

	// Add reduce only if specified
	if req.ReduceOnly {
		body["reduceOnly"] = true
	}

	logger.Infof("[OKX] PlaceLimitOrder: %s %s @ %.4f, sz=%s", instId, side, req.Price, szStr)

	data, err := t.doRequest("POST", okxOrderPath, body)
	if err != nil {
		return nil, fmt.Errorf("failed to place limit order: %w", err)
	}

	var orders []struct {
		OrdId   string `json:"ordId"`
		ClOrdId string `json:"clOrdId"`
		SCode   string `json:"sCode"`
		SMsg    string `json:"sMsg"`
	}

	if err := json.Unmarshal(data, &orders); err != nil {
		return nil, fmt.Errorf("failed to parse order response: %w", err)
	}

	if len(orders) == 0 {
		return nil, fmt.Errorf("empty order response")
	}

	if orders[0].SCode != "0" {
		return nil, fmt.Errorf("OKX order failed: %s", orders[0].SMsg)
	}

	logger.Infof("✓ [OKX] Limit order placed: %s %s @ %.4f, orderID=%s",
		instId, side, req.Price, orders[0].OrdId)

	return &types.LimitOrderResult{
		OrderID:      orders[0].OrdId,
		ClientID:     orders[0].ClOrdId,
		Symbol:       req.Symbol,
		Side:         req.Side,
		PositionSide: req.PositionSide,
		Price:        req.Price,
		Quantity:     req.Quantity,
		Status:       "NEW",
	}, nil
}

// CancelOrder cancels a specific order by ID
// Implements GridTrader interface
func (t *OKXTrader) CancelOrder(symbol, orderID string) error {
	instId := t.convertSymbol(symbol)

	body := map[string]interface{}{
		"instId": instId,
		"ordId":  orderID,
	}

	data, err := t.doRequest("POST", "/api/v5/trade/cancel-order", body)
	if err != nil {
		return fmt.Errorf("failed to cancel order: %w", err)
	}
	// Guard against the code=1 partial-success trap: a per-order sCode failure (e.g. this is
	// actually an ALGO order that cancel-order can't touch) must NOT log a fake success.
	if err := okxOrderCancelResultErr(data, orderID); err != nil {
		return err
	}

	logger.Infof("✓ [OKX] Order cancelled: %s %s", symbol, orderID)
	return nil
}

// okxOrderCancelResultErr mirrors okxAlgoCancelResultErr for the regular order-cancel endpoint.
func okxOrderCancelResultErr(data []byte, ordID string) error {
	if len(data) == 0 {
		return nil
	}
	var results []struct {
		OrdID string `json:"ordId"`
		SCode string `json:"sCode"`
		SMsg  string `json:"sMsg"`
	}
	if err := json.Unmarshal(data, &results); err != nil {
		return nil
	}
	for _, r := range results {
		switch r.SCode {
		case "", "0", "51400", "51401", "51402":
			return nil
		default:
			return fmt.Errorf("OKX cancel-order rejected ordId=%s: sCode=%s sMsg=%s", ordID, r.SCode, r.SMsg)
		}
	}
	return nil
}

// GetOrderBook gets the order book for a symbol
// Implements GridTrader interface
func (t *OKXTrader) GetOrderBook(symbol string, depth int) (bids, asks [][]float64, err error) {
	instId := t.convertSymbol(symbol)
	path := fmt.Sprintf("/api/v5/market/books?instId=%s&sz=%d", instId, depth)

	data, err := t.doRequest("GET", path, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get order book: %w", err)
	}

	var result []struct {
		Bids [][]string `json:"bids"`
		Asks [][]string `json:"asks"`
	}

	if err := json.Unmarshal(data, &result); err != nil {
		return nil, nil, fmt.Errorf("failed to parse order book: %w", err)
	}

	if len(result) == 0 {
		return nil, nil, nil
	}

	// Parse bids
	for _, b := range result[0].Bids {
		if len(b) >= 2 {
			price, _ := strconv.ParseFloat(b[0], 64)
			qty, _ := strconv.ParseFloat(b[1], 64)
			bids = append(bids, []float64{price, qty})
		}
	}

	// Parse asks
	for _, a := range result[0].Asks {
		if len(a) >= 2 {
			price, _ := strconv.ParseFloat(a[0], 64)
			qty, _ := strconv.ParseFloat(a[1], 64)
			asks = append(asks, []float64{price, qty})
		}
	}

	return bids, asks, nil
}
