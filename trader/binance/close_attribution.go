package binance

import (
	"context"
	"strconv"
	"strings"

	"nofx/logger"
	"nofx/store"
	"nofx/trader/types"
)

// resolvedCloseOrder is the deterministic attribution for a single Binance close
// fill: the real originating order type (which the fill itself does not carry —
// a triggered STOP/TP/TRAILING spawns a MARKET fill) plus the resolved mechanism
// reason when a bot-issued close-intent matched. Every field is best-effort; the
// caller degrades gracefully to the bare close action (today's sync_external)
// when nothing resolves.
type resolvedCloseOrder struct {
	reason        string  // resolved mechanism (e.g. native_trailing, ai_close_long); "" when only the type is known
	realType      string  // origType from the exchange (STOP_MARKET, TAKE_PROFIT_MARKET, TRAILING_STOP_MARKET, LIMIT, MARKET)
	stopPrice     float64 // trigger price of the originating order (0 when unknown)
	clientOrderID string  // exchange clientOrderId of the originating order (broker-tagged when ours)
}

// lookupOrderType queries a single order by its exchange order id and returns the
// authoritative origType, trigger price and clientOrderId. Binance preserves
// origType across a conditional-order trigger: a fired STOP_MARKET/TAKE_PROFIT/
// TRAILING order spawns a MARKET-executed fill whose Type becomes MARKET but
// whose OrigType stays the conditional type. Returns ok=false on any API error
// (e.g. the order was purged after full fill) so the caller can fall back.
func (t *FuturesTrader) lookupOrderType(symbol, orderID string) (origType, clientID string, stopPrice float64, ok bool) {
	execSymbol := t.toExecSymbol(symbol) // internal USDT -> exec (USDC when applicable)
	oid, err := strconv.ParseInt(strings.TrimSpace(orderID), 10, 64)
	if err != nil || oid <= 0 {
		return "", "", 0, false
	}
	order, err := t.client.NewGetOrderService().
		Symbol(execSymbol).
		OrderID(oid).
		Do(context.Background())
	if err != nil || order == nil {
		return "", "", 0, false
	}
	origType = strings.ToUpper(strings.TrimSpace(string(order.OrigType)))
	if origType == "" {
		origType = strings.ToUpper(strings.TrimSpace(string(order.Type)))
	}
	sp, _ := strconv.ParseFloat(order.StopPrice, 64)
	return origType, order.ClientOrderID, sp, true
}

// resolveBinanceClose attributes a Binance close fill to its real mechanism.
// Binance protection lives as native exchange orders (STOP_MARKET /
// TAKE_PROFIT_MARKET / TRAILING_STOP_MARKET / maker-TP LIMIT) that trigger
// exchange-side, so — unlike OKX — the bot issues no close order and records no
// close-intent for them. The resulting fill returns as a bare close_long/short
// and was previously dumped into sync_external. This resolver restores parity
// with OKX's attribution chain using deterministic signals only:
//
//	L1  close-intent by exact order id — a bot-issued MARKET close (ai_close,
//	    managed_drawdown, breadth_breaker, time_stop, manual, ...). Fully
//	    deterministic: the close order records its id, the fill carries it.
//	L2  GetOrder(orderId).OrigType — the authoritative native order type for
//	    exchange-triggered protection. Maps type -> mechanism family; the caller's
//	    deriveCloseReason then refines partial-vs-full and break-even-vs-SL with
//	    position context.
//	L3  close-intent by trader+symbol+side time window — last-resort for a bot
//	    close whose order id was not recorded on the intent.
//
// perRunCache dedups the GetOrder call across the many fills a single order can
// produce within one sync run. Never returns an error: on total miss it yields a
// zero resolvedCloseOrder and the caller keeps today's behaviour.
func (t *FuturesTrader) resolveBinanceClose(
	st *store.Store,
	traderID, symbol, positionSide string,
	trade types.TradeRecord,
	perRunCache map[string]resolvedCloseOrder,
) resolvedCloseOrder {
	if perRunCache != nil {
		if cached, ok := perRunCache[trade.OrderID]; ok {
			return cached
		}
	}
	var out resolvedCloseOrder

	// L1: bot-issued MARKET close — exact order-id match against the close-intent
	// ledger. This is the same ledger OKX consumes; it is exchange-agnostic.
	if st != nil && trade.OrderID != "" {
		if ci := st.CloseIntent(); ci != nil {
			if intent, err := ci.MatchByOrderIDAndConsume(traderID, trade.OrderID); err == nil && intent != nil && intent.Reason != "" {
				out.reason = intent.Reason
				out.realType = "MARKET"
				logger.Infof("  🎯 BN close %s %s attributed reason=%s via close-intent (order-id exact)", symbol, positionSide, out.reason)
			}
		}
	}

	// L2: native exchange protection — resolve the real origType. Only when L1
	// did not already resolve a bot mechanism.
	if out.reason == "" && trade.OrderID != "" {
		if origType, clientID, stopPrice, ok := t.lookupOrderType(symbol, trade.OrderID); ok {
			out.realType = origType
			out.clientOrderID = clientID
			out.stopPrice = stopPrice
			// A post-only maker take-profit is a plain LIMIT on the exchange; only
			// treat it as a TP when it is our own broker-tagged order, otherwise a
			// manual/foreign LIMIT close would be mislabeled.
			if origType == "LIMIT" && strings.HasPrefix(clientID, brOrderIDPrefix) {
				out.realType = "TAKE_PROFIT"
			}
			if origType != "" && origType != "MARKET" {
				logger.Infof("  🔗 BN close %s %s real origType=%s stop=%.6f (client=%s)", symbol, positionSide, origType, stopPrice, clientID)
			}
		}
	}

	// L2.5: exchange-native protection resolved by trigger-price match. When L1/L2
	// did not resolve a mechanism (no bot order-id intent; origType lookup failed
	// or returned bare MARKET because the conditional order aged out of the
	// exchange's queryable window), a protection intent recorded AT PLACEMENT time
	// still pins the mechanism: a real STOP/TP fills AT its trigger, so the fill
	// price ≈ the intent's trigger_price. This is the aged-order-survivable path
	// that brings Binance native protection attribution to OKX parity, since a
	// triggered algo order spawns a NEW fill order id that the placement never saw.
	if out.reason == "" && (out.realType == "" || out.realType == "MARKET") && st != nil && trade.Price > 0 {
		if ci := st.CloseIntent(); ci != nil {
			if intent, err := ci.MatchByTriggerPriceAndConsume(traderID, symbol, positionSide, trade.Price, 0.15); err == nil && intent != nil && intent.Reason != "" {
				out.reason = intent.Reason
				if out.realType == "" || out.realType == "MARKET" {
					out.realType = "MARKET"
				}
				logger.Infof("  🎯 BN close %s %s attributed reason=%s via protection-intent (trigger-price %.6f≈fill %.6f)",
					symbol, positionSide, out.reason, intent.TriggerPrice, trade.Price)
			}
		}
	}

	// L3: bot close whose order id was not recorded on the intent — newest
	// unconsumed intent for trader+symbol+side within a tight window.
	if out.reason == "" && (out.realType == "" || out.realType == "MARKET") && st != nil {
		if ci := st.CloseIntent(); ci != nil {
			fillMs := trade.Time.UTC().UnixMilli()
			if intent, err := ci.MatchByWindowAndConsume(traderID, symbol, positionSide, fillMs, 5*60*1000); err == nil && intent != nil && intent.Reason != "" {
				out.reason = intent.Reason
				if out.realType == "" {
					out.realType = "MARKET"
				}
				logger.Infof("  🎯 BN close %s %s attributed reason=%s via close-intent (time-window)", symbol, positionSide, out.reason)
			}
		}
	}

	if perRunCache != nil {
		perRunCache[trade.OrderID] = out
	}
	return out
}
