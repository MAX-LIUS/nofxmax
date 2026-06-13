package market

// BreakoutSignal is the data-validated breakout entry rule (see backtest 2026-06-13:
// across 10 symbols, 2 independent multi-month windows, 1H+15m, and friction-cost
// stress tests it was the only robust positive-expectancy entry — long if the close
// breaks above the highest high of the prior `lookback` bars, short if it breaks below
// the lowest low). Returns +1 long, -1 short, 0 none, evaluated on the LAST closed bar.
//
// Pure function, no side effects — mirrors the Python backtest exactly so live
// behaviour reproduces the validated edge.
func BreakoutSignal(klines []Kline, lookback int) int {
	if lookback <= 0 || len(klines) < lookback+1 {
		return 0
	}
	last := klines[len(klines)-1]
	// prior `lookback` bars are those BEFORE the last bar.
	hh := klines[len(klines)-1-lookback].High
	ll := klines[len(klines)-1-lookback].Low
	for i := len(klines) - lookback - 1; i < len(klines)-1; i++ {
		if klines[i].High > hh {
			hh = klines[i].High
		}
		if klines[i].Low < ll {
			ll = klines[i].Low
		}
	}
	if last.Close > hh {
		return 1
	}
	if last.Close < ll {
		return -1
	}
	return 0
}

// BreakoutSignalBars is the KlineBar-typed variant used in the live decision cycle,
// where per-timeframe series are already computed as []KlineBar. Identical logic to
// BreakoutSignal: +1 if last close strictly breaks the prior `lookback`-bar high,
// -1 if it strictly breaks the prior low, else 0.
func BreakoutSignalBars(bars []KlineBar, lookback int) int {
	if lookback <= 0 || len(bars) < lookback+1 {
		return 0
	}
	last := bars[len(bars)-1]
	hh := bars[len(bars)-1-lookback].High
	ll := bars[len(bars)-1-lookback].Low
	for i := len(bars) - lookback - 1; i < len(bars)-1; i++ {
		if bars[i].High > hh {
			hh = bars[i].High
		}
		if bars[i].Low < ll {
			ll = bars[i].Low
		}
	}
	if last.Close > hh {
		return 1
	}
	if last.Close < ll {
		return -1
	}
	return 0
}
