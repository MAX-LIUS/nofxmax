package okx

import (
	"encoding/json"
	"fmt"
	"nofx/logger"
	"nofx/market"
	"nofx/store"
	"sort"
	"strconv"
	"strings"
	"time"
)

func protectionReasonFromTag(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	switch {
	case strings.Contains(tag, "break_even"):
		return "break_even_stop"
	case strings.Contains(tag, "native_trailing"):
		return "native_trailing"
	case strings.Contains(tag, "managed_drawdown"):
		return "managed_drawdown"
	case strings.Contains(tag, "ladder_tp"):
		return "ladder_tp"
	case strings.Contains(tag, "ladder_sl"):
		return "ladder_sl"
	case strings.Contains(tag, "full_tp"):
		return "full_tp"
	case strings.Contains(tag, "full_sl"):
		return "full_sl"
	case strings.Contains(tag, "fallback_maxloss"):
		return "fallback_maxloss_sl"
	}
	return ""
}

// NOTE: matchProtectionReasonByPrice / protectionCandidate were REMOVED (2026-07).
// They guessed a close fill's mechanism from the nearest unlabeled live order by
// price proximity, which produced false attributions (e.g. a loss-side market
// close mislabeled break_even_stop when BE was never armed). Attribution is now
// exact-only: coded client id, order-detail algoClOrdId, order-id close-intent, or
// ledger trigger-price intent (which carries the REAL reason recorded at
// placement, with direction gating). Unresolved closes stay honestly
// unattributed rather than guessed.

// OKXTrade represents a trade record from OKX fills history
type OKXTrade struct {
	InstID      string
	Symbol      string
	TradeID     string
	OrderID     string
	Side        string // buy or sell
	PosSide     string // long or short
	FillPrice   float64
	FillQty     float64 // In contracts
	FillQtyBase float64 // In base asset (BTC, ETH, etc)
	Fee         float64
	FeeAsset    string
	ExecTime    time.Time
	IsMaker     bool
	OrderType   string
	OrderAction string // open_long, open_short, close_long, close_short
	Tag         string
	ClientID    string // clOrdId or algoClOrdId we set at placement (may be "")
	CodedReason string // mechanism decoded from ClientID; "" when not one of ours
}

// GetTrades retrieves trade/fill records from OKX
func (t *OKXTrader) GetTrades(startTime time.Time, limit int) ([]OKXTrade, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 100 {
		limit = 100 // OKX max limit is 100
	}

	// Build query path
	// OKX fills-history endpoint for historical fills
	path := fmt.Sprintf("/api/v5/trade/fills-history?instType=SWAP&limit=%d", limit)
	if !startTime.IsZero() {
		path += fmt.Sprintf("&begin=%d", startTime.UnixMilli())
	}

	data, err := t.doRequest("GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get fills history: %w", err)
	}

	var fills []struct {
		InstID      string `json:"instId"`      // e.g., "BTC-USDT-SWAP"
		TradeID     string `json:"tradeId"`     // Trade ID
		OrdID       string `json:"ordId"`       // Order ID
		ClOrdID     string `json:"clOrdId"`     // client order id (market closes we placed)
		AlgoClOrdID string `json:"algoClOrdId"` // client algo id (protection orders we placed)
		BillID      string `json:"billId"`      // Bill ID
		Side        string `json:"side"`        // buy or sell
		PosSide     string `json:"posSide"`     // long, short, or net
		FillPx      string `json:"fillPx"`      // Fill price
		FillSz      string `json:"fillSz"`      // Fill size (contracts)
		Fee         string `json:"fee"`         // Fee (negative for cost)
		FeeCcy      string `json:"feeCcy"`      // Fee currency
		Ts          string `json:"ts"`          // Trade timestamp (ms)
		ExecType    string `json:"execType"`    // T: taker, M: maker
		Tag         string `json:"tag"`         // Order tag
	}

	if err := json.Unmarshal(data, &fills); err != nil {
		return nil, fmt.Errorf("failed to parse fills: %w", err)
	}

	trades := make([]OKXTrade, 0, len(fills))

	for _, fill := range fills {
		fillPrice, _ := strconv.ParseFloat(fill.FillPx, 64)
		fillSz, _ := strconv.ParseFloat(fill.FillSz, 64)
		fee, _ := strconv.ParseFloat(fill.Fee, 64)
		ts, _ := strconv.ParseInt(fill.Ts, 10, 64)

		// Convert symbol: BTC-USDT-SWAP -> BTCUSDT
		symbol := t.convertSymbolBack(fill.InstID)

		// Convert contract count to base asset quantity
		fillQtyBase := fillSz
		inst, err := t.getInstrument(symbol)
		if err == nil && inst.CtVal > 0 {
			fillQtyBase = fillSz * inst.CtVal
		}

		// Determine order action based on side and posSide
		// OKX uses dual position mode:
		// - buy + long = open long
		// - sell + long = close long
		// - sell + short = open short
		// - buy + short = close short
		orderAction := "open_long"
		posSide := strings.ToLower(fill.PosSide)
		side := strings.ToLower(fill.Side)

		if posSide == "long" {
			if side == "buy" {
				orderAction = "open_long"
			} else {
				orderAction = "close_long"
			}
		} else if posSide == "short" {
			if side == "sell" {
				orderAction = "open_short"
			} else {
				orderAction = "close_short"
			}
		} else {
			// One-way mode (net position)
			if side == "buy" {
				orderAction = "open_long"
			} else {
				orderAction = "open_short"
			}
		}

		// Client id we set at placement: algoClOrdId for protection algos, clOrdId
		// for market closes. Decode the mechanism directly from it (exact, no guess).
		clientID := firstNonEmpty(fill.AlgoClOrdID, fill.ClOrdID)
		codedReason := decodeReasonFromClientID(clientID)

		trade := OKXTrade{
			InstID:      fill.InstID,
			Symbol:      symbol,
			TradeID:     fill.TradeID,
			OrderID:     fill.OrdID,
			Side:        fill.Side,
			PosSide:     fill.PosSide,
			FillPrice:   fillPrice,
			FillQty:     fillSz,
			FillQtyBase: fillQtyBase,
			Fee:         -fee, // OKX returns negative fee
			FeeAsset:    fill.FeeCcy,
			ExecTime:    time.UnixMilli(ts).UTC(),
			IsMaker:     fill.ExecType == "M",
			OrderType:   "MARKET",
			OrderAction: orderAction,
			Tag:         fill.Tag,
			ClientID:    clientID,
			CodedReason: codedReason,
		}

		trades = append(trades, trade)
	}

	return trades, nil
}

func (t *OKXTrader) SyncOpenProtectionOrdersToStore(traderID string, exchangeID string, exchangeType string, st *store.Store) error {
	if st == nil || st.Order() == nil {
		return nil
	}
	state, _ := st.LoadDynamicProtectionState()
	byAlgoID := map[string]store.DynamicProtectionRecord{}
	if state != nil {
		for _, record := range state.Records {
			if record.TraderID != traderID || record.ExchangeID != exchangeID || record.ExchangeOrderID == "" {
				continue
			}
			byAlgoID[record.ExchangeOrderID] = record
		}
	}
	positions, _ := t.GetPositions()
	activeQty := func(symbol, side string) float64 {
		for _, pos := range positions {
			if fmt.Sprint(pos["symbol"]) != symbol || !strings.EqualFold(fmt.Sprint(pos["side"]), side) {
				continue
			}
			if q, ok := pos["positionAmt"].(float64); ok {
				if q < 0 {
					return -q
				}
				return q
			}
		}
		return 0
	}
	for _, query := range []struct{ ordType, reason string }{{"move_order_stop", "native_trailing"}, {"conditional", ""}} {
		// OKX pending algo API requires instId. Use currently active symbols plus symbols from dynamic records.
		symbolSet := map[string]struct{}{}
		for _, pos := range positions {
			if sym := fmt.Sprint(pos["symbol"]); sym != "" {
				symbolSet[sym] = struct{}{}
			}
		}
		for _, record := range byAlgoID {
			if record.Symbol != "" {
				symbolSet[record.Symbol] = struct{}{}
			}
		}
		for symbol := range symbolSet {
			instId := t.convertSymbol(symbol)
			path := fmt.Sprintf("%s?instType=SWAP&instId=%s&ordType=%s", okxAlgoPendingPath, instId, query.ordType)
			data, err := t.doRequest("GET", path, nil)
			if err != nil {
				return err
			}
			var orders []struct {
				AlgoID        string `json:"algoId"`
				InstID        string `json:"instId"`
				Side          string `json:"side"`
				PosSide       string `json:"posSide"`
				OrdType       string `json:"ordType"`
				Sz            string `json:"sz"`
				TriggerPx     string `json:"triggerPx"`
				SlTriggerPx   string `json:"slTriggerPx"`
				TpTriggerPx   string `json:"tpTriggerPx"`
				ActivePx      string `json:"activePx"`
				CallbackRatio string `json:"callbackRatio"`
				Tag           string `json:"tag"`
				// AlgoClOrdID is the client id we set at placement — the ONLY field that
				// can carry the mechanism (tag is 16 chars, fully consumed by okxTag).
				AlgoClOrdID string `json:"algoClOrdId"`
			}
			if err := json.Unmarshal(data, &orders); err != nil {
				return err
			}
			for _, order := range orders {
				if order.AlgoID == "" {
					continue
				}
				reason := query.reason
				// Exact-first: the coded algoClOrdId we set at placement decodes to the
				// real mechanism with zero guessing. protectionReasonFromTag is only a
				// fallback for legacy/foreign orders — it can never match our own orders,
				// because okxTag consumes all 16 tag chars (see trader.go).
				if coded := decodeReasonFromClientID(order.AlgoClOrdID); coded != "" {
					reason = coded
				} else if tagged := protectionReasonFromTag(order.Tag); tagged != "" {
					reason = tagged
				}
				if reason == "" {
					if strings.EqualFold(order.OrdType, "move_order_stop") {
						reason = "native_trailing"
					} else if order.TpTriggerPx != "" {
						reason = "full_tp"
					} else {
						reason = "full_sl"
					}
				}
				posSide := strings.ToUpper(order.PosSide)
				if posSide == "" {
					if strings.EqualFold(order.Side, "buy") {
						posSide = "SHORT"
					} else {
						posSide = "LONG"
					}
				}
				// Disambiguate a stop sitting on the PROFIT side of entry: that is a
				// break-even stop, not the -X% full stop. The 16-char broker tag leaves
				// no room for a reason, so an untagged conditional SL would otherwise be
				// mislabeled full_sl. Use the live position entry price to classify by
				// where the stop trigger sits relative to entry.
				if reason == "full_sl" && order.SlTriggerPx != "" && st != nil && st.Position() != nil {
					if slPx, perr := strconv.ParseFloat(order.SlTriggerPx, 64); perr == nil && slPx > 0 {
						if pos, gerr := st.Position().GetOpenPositionBySymbol(traderID, symbol, posSide); gerr == nil && pos != nil && pos.EntryPrice > 0 {
							onProfitSide := (posSide == "LONG" && slPx > pos.EntryPrice) ||
								(posSide == "SHORT" && slPx < pos.EntryPrice)
							if onProfitSide {
								reason = "break_even_stop"
							}
						}
					}
				}
				qty, _ := strconv.ParseFloat(order.Sz, 64)
				if inst, err := t.getInstrument(symbol); err == nil && inst.CtVal > 0 {
					qty *= inst.CtVal
				}
				if qty <= 0 {
					qty = activeQty(symbol, posSide)
				}
				activation, _ := strconv.ParseFloat(firstNonEmpty(order.ActivePx, order.TriggerPx, order.SlTriggerPx, order.TpTriggerPx), 64)
				callback, _ := strconv.ParseFloat(order.CallbackRatio, 64)
				// OKX callbackRatio is reported in percentage units; store/order runtime
				// comparisons use decimal ratios. Keep persisted metadata consistent with
				// GetOpenOrders so synced native trailing orders can be matched reliably.
				callbackRate := normalizeOKXCallbackRatio(callback)
				t.recordProtectionOrder(st, traderID, exchangeID, exchangeType, symbol, order.Side, posSide, order.AlgoID, order.AlgoClOrdID, reason, activation, callbackRate, qty)
			}
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// recordProtectionOrder mirrors a live protection algo into the DB. clientOrderID must
// be the REAL algoClOrdId echoed by the exchange: it previously stored the collapsed
// broker tag, so every recorded protection order carried an identical client id and the
// column was useless for identifying a specific order. When the exchange returns no
// client id, the column stays empty rather than being back-filled with a synthesized
// coded id — a fabricated nonce would not match anything on the exchange while looking
// authoritative. OrderAction still carries the mechanism.
func (t *OKXTrader) recordProtectionOrder(st *store.Store, traderID string, exchangeID string, exchangeType string, symbol string, side string, positionSide string, algoID string, clientOrderID string, reason string, activationPrice float64, callbackRatio float64, quantity float64) {
	if st == nil || st.Order() == nil || algoID == "" {
		return
	}
	orderSide := "SELL"
	if strings.EqualFold(positionSide, "SHORT") {
		orderSide = "BUY"
	}
	orderType := "ALGO"
	if reason == "native_trailing" || strings.Contains(reason, "trailing") {
		orderType = "TRAILING_STOP_MARKET"
	}
	ord := &store.TraderOrder{
		TraderID:        traderID,
		ExchangeID:      exchangeID,
		ExchangeType:    exchangeType,
		ExchangeOrderID: algoID,
		ClientOrderID:   strings.TrimSpace(clientOrderID),
		Symbol:          market.Normalize(symbol),
		Side:            orderSide,
		PositionSide:    strings.ToUpper(positionSide),
		Type:            orderType,
		OrderAction:     reason,
		Quantity:        quantity,
		StopPrice:       activationPrice,
		Status:          "NEW",
		ReduceOnly:      true,
		CreatedAt:       time.Now().UTC().UnixMilli(),
		UpdatedAt:       time.Now().UTC().UnixMilli(),
	}
	if err := st.Order().CreateOrder(ord); err != nil {
		logger.Infof("  ⚠️ Failed to record protection algo order %s %s: %v", symbol, algoID, err)
	}
}

// SyncOrdersFromOKX syncs OKX exchange order history to local database
// Also creates/updates position records to ensure orders/fills/positions data consistency
// exchangeID: Exchange account UUID (from exchanges.id)
// exchangeType: Exchange type ("okx")
func (t *OKXTrader) SyncOrdersFromOKX(traderID string, exchangeID string, exchangeType string, st *store.Store) error {
	return t.SyncOrdersFromOKXWithFullCloseHandler(traderID, exchangeID, exchangeType, st, nil)
}

func (t *OKXTrader) SyncOrdersFromOKXWithFullCloseHandler(traderID string, exchangeID string, exchangeType string, st *store.Store, onFullClose func(symbol, side string)) error {
	if st == nil {
		return fmt.Errorf("store is nil")
	}
	if err := t.SyncOpenProtectionOrdersToStore(traderID, exchangeID, exchangeType, st); err != nil {
		logger.Infof("  ⚠️ Failed to sync OKX open protection orders before fill attribution: %v", err)
	}

	// Get recent trades (last 24 hours)
	startTime := time.Now().Add(-24 * time.Hour)

	logger.Infof("🔄 Syncing OKX trades from: %s", startTime.Format(time.RFC3339))

	// Use GetTrades method to fetch trade records
	trades, err := t.GetTrades(startTime, 100)
	if err != nil {
		return fmt.Errorf("failed to get trades: %w", err)
	}

	logger.Infof("📥 Received %d trades from OKX", len(trades))

	// Sort trades by time ASC (oldest first) for proper position building
	sort.Slice(trades, func(i, j int) bool {
		return trades[i].ExecTime.UnixMilli() < trades[j].ExecTime.UnixMilli()
	})

	// Process trades one by one (no transaction to avoid deadlock)
	orderStore := st.Order()
	positionStore := st.Position()
	posBuilder := store.NewPositionBuilder(positionStore)
	syncedCount := 0

	for _, trade := range trades {
		// Check if trade already exists (use exchangeID which is UUID, not exchange type)
		existing, err := orderStore.GetOrderByExchangeID(exchangeID, trade.TradeID)
		if err == nil && existing != nil {
			continue // Order already exists, skip
		}

		// Normalize symbol
		symbol := market.Normalize(trade.Symbol)

		// Determine position side from order action
		positionSide := "LONG"
		if strings.Contains(trade.OrderAction, "short") {
			positionSide = "SHORT"
		}

		// Normalize side for storage
		side := strings.ToUpper(trade.Side)

		// Create order record - use UTC time in milliseconds to avoid timezone issues
		execTimeMs := trade.ExecTime.UTC().UnixMilli()
		canonicalAction := trade.OrderAction
		requestedReason := canonicalAction
		parentOrderID := strings.TrimSpace(trade.OrderID)
		ownerTraderID := traderID
		if parentOrderID != "" {
			if parentOrder, err := orderStore.GetOrderByExchangeID(exchangeID, parentOrderID); err == nil && parentOrder != nil {
				if parentOrder.TraderID != "" {
					ownerTraderID = parentOrder.TraderID
				}
				parentReason := strings.ToLower(strings.TrimSpace(parentOrder.OrderAction + " " + parentOrder.ClientOrderID))
				if requestedReason == canonicalAction {
					switch {
					case strings.Contains(parentReason, "native_trailing") || strings.Contains(parentReason, "trailing"):
						requestedReason = "native_trailing"
					case strings.Contains(parentReason, "break_even"):
						requestedReason = "break_even_stop"
					case strings.Contains(parentReason, "managed_drawdown"):
						requestedReason = "managed_drawdown"
					case strings.Contains(parentReason, "ladder_tp"):
						requestedReason = "ladder_tp"
					case strings.Contains(parentReason, "ladder_sl"):
						requestedReason = "ladder_sl"
					case strings.Contains(parentReason, "full_tp"):
						requestedReason = "full_tp"
					case strings.Contains(parentReason, "full_sl") || strings.Contains(parentReason, "fallback_maxloss"):
						requestedReason = "full_sl"
					}
				}
			}
		}
		if canonicalAction == "close_long" || canonicalAction == "close_short" {
			// Highest-priority EXACT source: the mechanism code we embedded in the
			// client-controlled order id at placement (algoClOrdId / clOrdId). The
			// fill echoes it back verbatim, so this is a true 1:1 correspondence with
			// zero guessing and zero race. When present it wins over every other path.
			if coded := trade.CodedReason; coded != "" {
				requestedReason = coded
				logger.Infof("  ✅ Close fill %s %s attributed to reason=%s via coded client-id=%s (exact 1:1)", symbol, canonicalAction, coded, trade.ClientID)
			}
			if requestedReason == canonicalAction {
				if reason := protectionReasonFromTag(trade.Tag); reason != "" {
					requestedReason = reason
				}
			}
			// EXACT via order-detail: a triggered algo spawns a fresh fill ordId, but
			// the order-detail endpoint still returns the algoClOrdId we set at
			// placement, which encodes the mechanism. This is the VERIFIED-available
			// exact path (does not depend on the fills feed echoing the client id).
			if requestedReason == canonicalAction && parentOrderID != "" {
				if reason, rerr := t.GetOrderLinkedReason(symbol, parentOrderID); rerr == nil && reason != "" {
					requestedReason = reason
					logger.Infof("  ✅ Close fill %s %s attributed to reason=%s via order-detail algoClOrdId (exact 1:1)", symbol, canonicalAction, reason)
				}
			}
			// Deterministic attribution: when an OKX TP/SL/trailing algo triggers,
			// it spawns a regular order whose detail carries the originating algoId.
			// The fill's ordId differs from the stored algoId, so resolve the link
			// via the order-detail API, then map algoId -> the protection order's
			// recorded reason. This is exact (not price-proximity). Only runs when
			// the tag/coded-id could not resolve the mechanism.
			if requestedReason == canonicalAction && orderStore != nil && parentOrderID != "" {
				if algoID, aerr := t.GetOrderLinkedAlgoID(symbol, parentOrderID); aerr == nil && algoID != "" {
					if protOrd, perr := orderStore.GetOrderByExchangeID(exchangeID, algoID); perr == nil && protOrd != nil && protOrd.OrderAction != "" {
						requestedReason = protOrd.OrderAction
						logger.Infof("  🔗 Close fill %s %s attributed to reason=%s via algoId=%s (deterministic)", symbol, canonicalAction, requestedReason, algoID)
					}
				}
			}
			// Deterministic system-close attribution: a system-initiated market close
			// (AI exit, breadth breaker, managed drawdown, time stop, trailing TP,
			// manual, replacement) records a CloseIntent keyed by the close order id.
			// The resulting fill carries that same order id, so this match is exact.
			// Runs before price-match because it is system intent, not an exchange
			// resting order. Only applies while still unresolved.
			if requestedReason == canonicalAction && parentOrderID != "" {
				if ci := st.CloseIntent(); ci != nil {
					if intent, ierr := ci.MatchByOrderIDAndConsume(ownerTraderID, parentOrderID); ierr == nil && intent != nil && intent.Reason != "" {
						requestedReason = intent.Reason
						logger.Infof("  🎯 Close fill %s %s attributed to reason=%s via close-intent (order-id exact, intentID=%d)", symbol, canonicalAction, requestedReason, intent.ID)
					} else if ierr != nil {
						logger.Infof("  ⚠️ close-intent order-id match failed for %s: %v", symbol, ierr)
					}
				}
			}
			// Ledger-backed trigger-price match: a native SL/TP/BE algo records a
			// placement-time intent carrying its REAL reason + trigger price (see
			// recordProtectionIntent). A triggered protection fills AT its trigger, so
			// a fill whose price ≈ an intent's trigger_price resolves to that intent's
			// recorded mechanism — with DIRECTION GATING (a fill on the physically
			// impossible side of the trigger is rejected). This is NOT the old
			// nearest-live-order guess: the reason comes from what we recorded at
			// placement, not inferred from an unlabeled resting order. Survives the
			// algo aging out of the exchange's queryable window.
			if requestedReason == canonicalAction && trade.FillPrice > 0 {
				if ci := st.CloseIntent(); ci != nil {
					// Scope to the current position's lifetime so an untriggered tier
					// from an earlier same-symbol position cannot be borrowed.
					var notBeforeMs int64
					if pos, perr := st.Position().GetOpenPositionBySymbol(ownerTraderID, symbol, positionSide); perr == nil && pos != nil {
						notBeforeMs = pos.EntryTime
					}
					if intent, ierr := ci.MatchByTriggerPriceAndConsume(ownerTraderID, symbol, positionSide, trade.FillPrice, 3.5, notBeforeMs); ierr == nil && intent != nil && intent.Reason != "" {
						requestedReason = intent.Reason
						logger.Infof("  🎯 Close fill %s %s attributed to reason=%s via protection-intent (trigger %.6f≈fill %.6f, intentID=%d)", symbol, canonicalAction, requestedReason, intent.TriggerPrice, trade.FillPrice, intent.ID)
					}
				}
			}
			// Price-proximity guessing (matchProtectionReasonByPrice) is REMOVED as an
			// attribution source: it inferred a reason from the nearest unlabeled live
			// order, producing false labels (e.g. a loss-side market close tagged
			// break_even_stop when BE was never armed). Every order we place now carries
			// an exact reason via coded id / order-detail / order-id intent / trigger
			// intent above. If all of those miss, we DO NOT guess — the close stays
			// honestly unattributed (bare close_long/short) rather than mislabeled.
			// Last-resort fallback: a system close whose order id was not recorded on
			// the intent (e.g. order result lacked orderId). Match the newest
			// unconsumed intent for trader+symbol+side within a tight time window.
			if requestedReason == canonicalAction {
				if ci := st.CloseIntent(); ci != nil {
					if intent, ierr := ci.MatchByWindowAndConsume(ownerTraderID, symbol, positionSide, execTimeMs, 5*60*1000); ierr == nil && intent != nil && intent.Reason != "" {
						requestedReason = intent.Reason
						logger.Infof("  🎯 Close fill %s %s attributed to reason=%s via close-intent (time-window, intentID=%d)", symbol, canonicalAction, requestedReason, intent.ID)
					}
				}
			}

			// When all attribution layers fail (still bare close_long/short), mark
			// explicitly as unresolved rather than silent sync_external. Emit the raw
			// fingerprint so future forensics can match the fill to its origin.
			if requestedReason == canonicalAction {
				requestedReason = "unresolved_exchange_close"
				logger.Warnf("⚠️ OKX close %s %s unresolved (all attribution layers failed) — fingerprint: orderId=%s clientID=%s tag=%s type=%s price=%.6f qty=%.6f tradeID=%s",
					symbol, positionSide, parentOrderID, trade.ClientID, trade.Tag, trade.OrderType, trade.FillPrice, trade.FillQtyBase, trade.TradeID)
			}
		}
		orderRecord := &store.TraderOrder{
			TraderID:        ownerTraderID,
			ExchangeID:      exchangeID,   // UUID
			ExchangeType:    exchangeType, // Exchange type
			ExchangeOrderID: trade.TradeID,
			ClientOrderID:   trade.Tag,
			ParentOrderID:   parentOrderID,
			Symbol:          symbol,
			Side:            side,
			PositionSide:    positionSide,
			Type:            trade.OrderType,
			OrderAction:     requestedReason,
			Quantity:        trade.FillQtyBase,
			Price:           trade.FillPrice,
			Status:          "FILLED",
			FilledQuantity:  trade.FillQtyBase,
			AvgFillPrice:    trade.FillPrice,
			Commission:      trade.Fee,
			FilledAt:        execTimeMs,
			CreatedAt:       execTimeMs,
			UpdatedAt:       execTimeMs,
		}

		if err := orderStore.UpsertSyncedOrder(orderRecord); err != nil {
			logger.Infof("  ⚠️ Failed to sync order ownership for trade %s: %v", trade.TradeID, err)
			continue
		}

		// Create fill record - use UTC time in milliseconds
		fillRecord := &store.TraderFill{
			TraderID:        orderRecord.TraderID,
			ExchangeID:      exchangeID,   // UUID
			ExchangeType:    exchangeType, // Exchange type
			OrderID:         orderRecord.ID,
			ExchangeOrderID: trade.OrderID,
			ParentOrderID:   parentOrderID,
			ExchangeTradeID: trade.TradeID,
			Symbol:          symbol,
			Side:            side,
			Price:           trade.FillPrice,
			Quantity:        trade.FillQtyBase,
			QuoteQuantity:   trade.FillPrice * trade.FillQtyBase,
			Commission:      trade.Fee,
			CommissionAsset: trade.FeeAsset,
			RealizedPnL:     0, // OKX fills don't include PnL per trade
			IsMaker:         trade.IsMaker,
			CreatedAt:       execTimeMs,
		}

		if err := orderStore.CreateFill(fillRecord); err != nil {
			logger.Infof("  ⚠️ Failed to sync fill for trade %s: %v", trade.TradeID, err)
		}

		// Create/update position record using PositionBuilder
		preClosePosition, _ := positionStore.GetOpenPositionBySymbol(orderRecord.TraderID, symbol, positionSide)
		preCloseQty := 0.0
		if preClosePosition != nil {
			preCloseQty = preClosePosition.Quantity
		}
		if err := posBuilder.ProcessTrade(
			orderRecord.TraderID, exchangeID, exchangeType,
			symbol, positionSide, canonicalAction,
			trade.FillQtyBase, trade.FillPrice, trade.Fee, 0, // No per-trade PnL from OKX
			execTimeMs, trade.TradeID,
		); err != nil {
			logger.Infof("  ⚠️ Failed to sync position for trade %s: %v", trade.TradeID, err)
		} else {
			store.AttachSyncedOrderToPosition(st, orderStore, positionStore, orderRecord, orderRecord.TraderID, symbol, positionSide, canonicalAction, trade.TradeID)
			logger.Infof("  📍 Position updated for trade: %s (action: %s, qty: %.6f)", trade.TradeID, canonicalAction, trade.FillQtyBase)
			if onFullClose != nil && strings.HasPrefix(canonicalAction, "close_") && preClosePosition != nil && trade.FillQtyBase >= preCloseQty-0.0001 {
				onFullClose(symbol, positionSide)
			}
		}

		syncedCount++
		logger.Infof("  ✅ Synced trade: %s %s %s qty=%.6f price=%.6f fee=%.6f action=%s source=%s",
			trade.TradeID, trade.Symbol, side, trade.FillQtyBase, trade.FillPrice, trade.Fee, trade.OrderAction, requestedReason)
	}

	logger.Infof("✅ OKX order sync completed: %d new trades synced", syncedCount)
	return nil
}

// StartOrderSync starts background order sync task for OKX
func (t *OKXTrader) StartOrderSync(traderID string, exchangeID string, exchangeType string, st *store.Store, interval time.Duration) {
	t.StartOrderSyncWithFullCloseHandler(traderID, exchangeID, exchangeType, st, interval, nil)
}

func (t *OKXTrader) StartOrderSyncWithFullCloseHandler(traderID string, exchangeID string, exchangeType string, st *store.Store, interval time.Duration, onFullClose func(symbol, side string)) {
	ticker := time.NewTicker(interval)
	go func() {
		for range ticker.C {
			if err := t.SyncOrdersFromOKXWithFullCloseHandler(traderID, exchangeID, exchangeType, st, onFullClose); err != nil {
				logger.Infof("⚠️  OKX order sync failed: %v", err)
			}
		}
	}()
	logger.Infof("🔄 OKX order sync started (interval: %v)", interval)
}
