package backtest

import (
	"database/sql"
	"fmt"
	"time"

	"nofx/market"
)

// LoadClaudeEntries reads CLOSED positions for the given trader from the DB and
// returns them as backtest entries (ascending by entry time). Positions with
// zero/invalid entry price or quantity are skipped.
func LoadClaudeEntries(db *sql.DB, traderIDLike string) ([]Entry, error) {
	rows, err := db.Query(`
		-- entry_quantity is the size the position was OPENED with; quantity is the
		-- live REMAINING size and is 0 for many closed rows (79/557 for claude), so
		-- reading quantity both mis-sizes positions and silently drops them via the
		-- qty<=0 filter below. Prefer entry_quantity, fall back to quantity.
		SELECT symbol, side, entry_price, entry_time, exit_time,
		       CASE WHEN COALESCE(entry_quantity,0) > 0 THEN entry_quantity ELSE quantity END,
		       realized_pnl, COALESCE(close_reason,''), COALESCE(exit_price,0)
		FROM trader_positions
		WHERE trader_id LIKE ? AND status='CLOSED'
		ORDER BY entry_time ASC`, traderIDLike)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		var (
			symbol, side              string
			entryPrice, qty, realized float64
			exitPrice                 float64
			entryTime, exitTime       sql.NullInt64
			closeReason               string
		)
		if err := rows.Scan(&symbol, &side, &entryPrice, &entryTime, &exitTime, &qty, &realized, &closeReason, &exitPrice); err != nil {
			return nil, err
		}
		if entryPrice <= 0 || qty <= 0 {
			continue
		}
		out = append(out, Entry{
			Symbol:      symbol,
			Side:        side,
			EntryPrice:  entryPrice,
			EntryTime:   entryTime.Int64,
			ExitTime:    exitTime.Int64,
			Quantity:    qty,
			RealizedPnL: realized,
			ExitPrice:   exitPrice,
			CloseReason: closeReason,
		})
	}
	return out, rows.Err()
}

// BarsProvider fetches ascending OHLC bars for a symbol/timeframe over a range.
// Abstracted so tests can inject fakes and prod uses OKX.
type BarsProvider func(symbol, timeframe string, start, end time.Time) ([]market.Kline, error)

// OKXBars is the production provider backed by OKX history-candles.
func OKXBars(symbol, timeframe string, start, end time.Time) ([]market.Kline, error) {
	return market.GetKlinesRangeOKX(symbol, timeframe, start, end)
}

// BinanceBars is the provider backed by Binance futures klines (proxy-aware via
// BINANCE_PROXY_URL). Used for binance-type traders (e.g. BN) so their replay reads
// the SAME exchange the trades executed on.
func BinanceBars(symbol, timeframe string, start, end time.Time) ([]market.Kline, error) {
	return market.GetKlinesRange(symbol, timeframe, start, end)
}

// tfDuration returns the wall-clock duration of one bar for a timeframe token.
// Defaults to 1h for unknown tokens so callers never get a zero window.
func tfDuration(tf string) time.Duration {
	switch tf {
	case "1m":
		return time.Minute
	case "3m":
		return 3 * time.Minute
	case "5m":
		return 5 * time.Minute
	case "15m":
		return 15 * time.Minute
	case "30m":
		return 30 * time.Minute
	case "1h":
		return time.Hour
	case "2h":
		return 2 * time.Hour
	case "4h":
		return 4 * time.Hour
	case "6h":
		return 6 * time.Hour
	case "12h":
		return 12 * time.Hour
	case "1d":
		return 24 * time.Hour
	default:
		return time.Hour
	}
}

// fetchEntryBars returns bars for one entry with `preBars` of pre-entry history
// (for ATR) and forward bars until exit (or +maxHoldHours if open-ended), plus
// the index of the entry bar within the returned slice.
func fetchEntryBars(e Entry, tf string, preBars, maxHoldHours int, provider BarsProvider) ([]market.Kline, int, error) {
	tfDur := tfDuration(tf) // bar duration must match the replay timeframe
	start := time.UnixMilli(e.EntryTime).Add(-time.Duration(preBars+2) * tfDur)
	endMs := e.ExitTime
	if endMs <= 0 {
		endMs = e.EntryTime + int64(maxHoldHours)*int64(time.Hour/time.Millisecond)
	}
	// pad the window so the exit bar is included
	end := time.UnixMilli(endMs).Add(2 * tfDur)

	bars, err := provider(e.Symbol, tf, start, end)
	if err != nil {
		return nil, -1, err
	}
	if len(bars) == 0 {
		return nil, -1, fmt.Errorf("no bars for %s", e.Symbol)
	}
	// entry index = first bar whose OpenTime >= entryTime
	entryIdx := -1
	for i, b := range bars {
		if b.OpenTime >= e.EntryTime {
			entryIdx = i
			break
		}
	}
	if entryIdx < 0 {
		entryIdx = len(bars) - 1
	}
	return bars, entryIdx, nil
}
