package market

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"
)

// okxHistoryMaxLimit is the max candles OKX returns per history-candles request.
const okxHistoryMaxLimit = 100

// GetKlinesRangeOKX fetches a K-line series within [start, end] (closed interval)
// from OKX history-candles, paginated backward via the `after` cursor. Returns
// candles sorted ascending by open time. This is the long-period historical
// source used by the backtester.
func GetKlinesRangeOKX(symbol, timeframe string, start, end time.Time) ([]Kline, error) {
	if !end.After(start) {
		return nil, fmt.Errorf("end time must be after start time")
	}
	okxSymbol := binanceToOKXSymbol(symbol)
	okxBar := convertInterval(timeframe)

	startMs := start.UnixMilli()
	endMs := end.UnixMilli()

	client := NewOKXAPIClient()

	// Dedup by open time across pages; OKX `after` is exclusive-ish but overlaps can occur.
	seen := make(map[int64]struct{})
	var all []Kline

	// Page backward from end → start. `after` = return records older than this ts.
	cursor := endMs
	for cursor > startMs {
		params := map[string]string{
			"instId": okxSymbol,
			"bar":    okxBar,
			"after":  strconv.FormatInt(cursor, 10),
			"limit":  strconv.Itoa(okxHistoryMaxLimit),
		}
		data, err := client.doGet("/api/v5/market/history-candles", params)
		if err != nil {
			return nil, err
		}

		var raw [][]string
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			break
		}

		oldestInPage := cursor
		added := 0
		for _, k := range raw {
			if len(k) < 7 {
				continue
			}
			openTime, _ := strconv.ParseInt(k[0], 10, 64)
			if openTime < oldestInPage {
				oldestInPage = openTime
			}
			if openTime < startMs || openTime > endMs {
				continue
			}
			if _, dup := seen[openTime]; dup {
				continue
			}
			seen[openTime] = struct{}{}
			open, _ := strconv.ParseFloat(k[1], 64)
			high, _ := strconv.ParseFloat(k[2], 64)
			low, _ := strconv.ParseFloat(k[3], 64)
			close_, _ := strconv.ParseFloat(k[4], 64)
			vol, _ := strconv.ParseFloat(k[5], 64)
			qvol, _ := strconv.ParseFloat(k[6], 64)
			all = append(all, Kline{
				OpenTime:    openTime,
				Open:        open,
				High:        high,
				Low:         low,
				Close:       close_,
				Volume:      vol,
				QuoteVolume: qvol,
			})
			added++
		}

		// Advance cursor to the oldest open time in this page. If it didn't move,
		// we've exhausted available history — stop to avoid an infinite loop.
		if oldestInPage >= cursor {
			break
		}
		cursor = oldestInPage
		if len(raw) < okxHistoryMaxLimit && added == 0 {
			break
		}
		// Gentle pacing to respect OKX public rate limits.
		time.Sleep(120 * time.Millisecond)
	}

	sort.Slice(all, func(i, j int) bool { return all[i].OpenTime < all[j].OpenTime })
	return all, nil
}
