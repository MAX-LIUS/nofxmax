package backtest

import "nofx/market"

// GenerateBreakoutEntries produces mechanical entries from Donchian-channel
// breakouts: long when close breaks above the highest high of the prior
// `lookback` bars, short when it breaks below the lowest low. A cooldown (in
// bars) prevents clustering. Equal-notional sizing (Quantity = notionalUSD/
// entryPrice) keeps PnL comparable across symbols. This is a SECOND, structurally
// different signal from EMA-cross — used to confirm guard params are not overfit
// to one entry style (breakout = momentum-continuation; EMA-cross = trend-follow).
func GenerateBreakoutEntries(symbol string, bars []market.Kline, lookback, cooldownBars int, notionalUSD float64) []Entry {
	if len(bars) < lookback+2 {
		return nil
	}
	if notionalUSD <= 0 {
		notionalUSD = 1000
	}
	var entries []Entry
	lastSignalIdx := -cooldownBars - 1
	for i := lookback; i < len(bars); i++ {
		// Highest high / lowest low of the prior `lookback` bars (exclude i).
		hh := bars[i-1].High
		ll := bars[i-1].Low
		for j := i - lookback; j < i; j++ {
			if bars[j].High > hh {
				hh = bars[j].High
			}
			if bars[j].Low < ll {
				ll = bars[j].Low
			}
		}
		var side string
		if bars[i].Close > hh {
			side = "LONG"
		} else if bars[i].Close < ll {
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
			ExitTime:   0,
			Quantity:   notionalUSD / entryPrice,
		})
	}
	return entries
}
