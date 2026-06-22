package main

import (
	"database/sql"
	"math"
	"strings"
)

// deriveReasonForPosition implements the rebuild taxonomy for one closed
// position using canonical order data.
func deriveReasonForPosition(db *sql.DB, posID int64, exchangeID, side string, entryPrice float64, exitCycle int64, traderID string) string {
	// 1. Inspect the dominant FILLED closing order's tags/type.
	closeSide := "SELL"
	if strings.EqualFold(side, "SHORT") {
		closeSide = "BUY"
	}
	rows, err := db.Query(`
		SELECT client_order_id, order_action, type, filled_quantity, avg_fill_price, price
		FROM trader_orders
		WHERE related_position_id=? AND side=? AND status='FILLED' AND filled_quantity>0
		ORDER BY filled_quantity DESC`, posID, closeSide)
	if err == nil {
		defer rows.Close()
		bestQty := 0.0
		bestReason := ""
		for rows.Next() {
			var clientID, action, otype string
			var fq, avg, price float64
			if err := rows.Scan(&clientID, &action, &otype, &fq, &avg, &price); err != nil {
				continue
			}
			r := classifyOrderReason(clientID, action, otype, entryPrice, pickPrice(avg, price), side)
			if r != "" && fq > bestQty {
				bestQty = fq
				bestReason = r
			}
		}
		if bestReason != "" {
			return bestReason
		}
	}

	// 2. AI close: exit decision cycle links to a real decision record.
	if exitCycle > 0 {
		var n int
		_ = db.QueryRow(`SELECT count(*) FROM decision_records WHERE trader_id=? AND cycle_number=?`,
			traderID, exitCycle).Scan(&n)
		if n > 0 {
			return "ai_close"
		}
	}

	// 3. Origin not recorded.
	return "market_close"
}

func pickPrice(avg, price float64) float64 {
	if avg > 0 {
		return avg
	}
	return price
}

// classifyOrderReason maps an order's tags/type to a canonical close reason.
// Returns "" when the order is generic (close_long/close_short/open_*) and
// carries no protection signal.
func classifyOrderReason(clientID, action, otype string, entryPrice, execPrice float64, side string) string {
	tag := strings.ToLower(clientID + " " + action)
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
	case strings.Contains(tag, "full_sl") || strings.Contains(tag, "fallback_maxloss"):
		return "full_sl"
	case strings.Contains(tag, "trailing_take_profit"):
		return "trailing_take_profit"
	case strings.Contains(tag, "time_stop") || strings.Contains(tag, "max_hold"):
		return "time_stop"
	}
	// Type-based fallback for exchange-native protection fills.
	kind := strings.ToUpper(otype)
	switch {
	case strings.Contains(kind, "TRAILING"):
		return "native_trailing"
	case strings.Contains(kind, "TAKE_PROFIT") || strings.Contains(kind, "TP"):
		return "full_tp"
	case strings.Contains(kind, "STOP") || strings.Contains(kind, "SL"):
		if entryPrice > 0 && execPrice > 0 && math.Abs(execPrice-entryPrice)/entryPrice <= 0.003 {
			return "break_even_stop"
		}
		return "full_sl"
	}
	return ""
}
