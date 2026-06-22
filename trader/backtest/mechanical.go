package backtest

import (
	"strings"

	"nofx/market"
)

// ema computes an exponential moving average series over closes.
func ema(closes []float64, period int) []float64 {
	out := make([]float64, len(closes))
	if len(closes) == 0 || period <= 0 {
		return out
	}
	k := 2.0 / float64(period+1)
	out[0] = closes[0]
	for i := 1; i < len(closes); i++ {
		out[i] = closes[i]*k + out[i-1]*(1-k)
	}
	return out
}

// GenerateEMACrossEntries produces mechanical entries from EMA(fast)/EMA(slow)
// crossovers over the given bars: long on bullish cross, short on bearish.
// A cooldown (in bars) prevents clustering. `notionalUSD` sizes every entry to
// an equal dollar notional (Quantity = notionalUSD/entryPrice) so PnL is
// comparable across symbols of very different price (BTC vs SOL). Used for
// long-period robustness testing — a direction-neutral signal so we stress
// protection params across many regimes rather than a curated entry set.
func GenerateEMACrossEntries(symbol string, bars []market.Kline, fast, slow, cooldownBars int, notionalUSD float64) []Entry {
	if len(bars) < slow+2 {
		return nil
	}
	if notionalUSD <= 0 {
		notionalUSD = 1000
	}
	closes := make([]float64, len(bars))
	for i, b := range bars {
		closes[i] = b.Close
	}
	ef := ema(closes, fast)
	es := ema(closes, slow)

	var entries []Entry
	lastSignalIdx := -cooldownBars - 1
	for i := slow + 1; i < len(bars); i++ {
		prevDiff := ef[i-1] - es[i-1]
		curDiff := ef[i] - es[i]
		var side string
		if prevDiff <= 0 && curDiff > 0 {
			side = "LONG"
		} else if prevDiff >= 0 && curDiff < 0 {
			side = "SHORT"
		} else {
			continue
		}
		if i-lastSignalIdx <= cooldownBars {
			continue
		}
		lastSignalIdx = i
		entryPrice := bars[i].Close
		if entryPrice <= 0 {
			continue
		}
		entries = append(entries, Entry{
			Symbol:     symbol,
			Side:       side,
			EntryPrice: entryPrice,
			EntryTime:  bars[i].OpenTime,
			ExitTime:   0, // open-ended; replay caps at maxHold
			Quantity:   notionalUSD / entryPrice, // equal-notional sizing
		})
	}
	return entries
}

// PrepareEntriesFromBars wraps a single fetched bar series into loadedEntry
// items for each mechanical entry, locating each entry's index in the shared
// bar slice (no extra network calls — all entries reuse the same series).
func PrepareEntriesFromBars(entries []Entry, bars []market.Kline) []loadedEntry {
	loaded := make([]loadedEntry, 0, len(entries))
	for _, e := range entries {
		idx := -1
		for i, b := range bars {
			if b.OpenTime >= e.EntryTime {
				idx = i
				break
			}
		}
		if idx < atrLookback {
			continue
		}
		loaded = append(loaded, loadedEntry{entry: e, bars: bars, entryIdx: idx})
	}
	return loaded
}

var _ = strings.ToUpper
