package trader

import (
	"math"
	"strings"

	"nofx/market"
	"nofx/store"
)

// swingSeqDir reports the HH/HL structural direction of a bar series:
// +1 when the last n swing highs AND the last n swing lows are both strictly
// rising (HH+HL uptrend), -1 when both are strictly falling (LH+LL downtrend),
// 0 otherwise (mixed / insufficient pivots = "no clean structure").
//
// This is deliberately STRICTER than chartSwingAlign in chart_trend_gate.go:
// that one scores the *fraction* of agreeing pivot pairs, which admits a mixed
// sequence at any threshold below 1.0. The backtested rule here requires a clean
// monotonic sequence, because that is the criterion the user reads off a chart
// ("是否有已完成的 HH/HL") and the one the validation was run on.
//
// lb is the fractal half-width (pivot must be the extreme of lb bars either
// side). n is how many of the most recent pivots of each kind to test.
// Pivot detection uses >=/<= so flat-topped double tops still register, matching
// chartSwingAlign and the Python research harness.
func swingSeqDir(bars []market.KlineBar, lb, n int) int {
	if lb < 1 {
		lb = 3
	}
	if n < 2 {
		n = 2
	}
	if len(bars) < 2*lb+2 {
		return 0
	}
	var highs, lows []float64
	for i := lb; i < len(bars)-lb; i++ {
		isHigh, isLow := true, true
		for j := 1; j <= lb; j++ {
			if !(bars[i].High >= bars[i-j].High && bars[i].High >= bars[i+j].High) {
				isHigh = false
			}
			if !(bars[i].Low <= bars[i-j].Low && bars[i].Low <= bars[i+j].Low) {
				isLow = false
			}
		}
		if isHigh {
			highs = append(highs, bars[i].High)
		}
		if isLow {
			lows = append(lows, bars[i].Low)
		}
	}
	trend := func(seq []float64) int {
		if len(seq) > n {
			seq = seq[len(seq)-n:]
		}
		if len(seq) < 2 {
			return 0
		}
		up, dn := 0, 0
		for i := 1; i < len(seq); i++ {
			if seq[i] > seq[i-1] {
				up++
			} else if seq[i] < seq[i-1] {
				dn++
			}
		}
		switch {
		case up > 0 && dn == 0:
			return 1
		case dn > 0 && up == 0:
			return -1
		default:
			return 0
		}
	}
	th, tl := trend(highs), trend(lows)
	if th == 1 && tl == 1 {
		return 1
	}
	if th == -1 && tl == -1 {
		return -1
	}
	return 0
}

// nearestBlockingLevelPct returns the distance (in percent of entry) to the
// closest REAL swing pivot standing in the trade's path: a swing high above a
// LONG entry, or a swing low below a SHORT entry. ok=false means there is no
// such pivot at all, i.e. the path is clear.
//
// This reads computed pivots, NOT the AI's self-reported rationale.KeyLevels.
// That distinction is the whole point: on the研究 sample the AI omitted blocking
// levels in 47% of trades (reported mean 1.12 levels vs 3.34 computed), so the
// existing validateTargetPathClear — which counts self-reported levels and only
// rejects at >=6 — cannot see the structure that actually stops the trade.
func nearestBlockingLevelPct(bars []market.KlineBar, entry float64, isLong bool, lb int) (float64, bool) {
	if entry <= 0 || lb < 1 {
		return 0, false
	}
	if len(bars) < 2*lb+2 {
		return 0, false
	}
	best := math.MaxFloat64
	found := false
	for i := lb; i < len(bars)-lb; i++ {
		isHigh, isLow := true, true
		for j := 1; j <= lb; j++ {
			if !(bars[i].High >= bars[i-j].High && bars[i].High >= bars[i+j].High) {
				isHigh = false
			}
			if !(bars[i].Low <= bars[i-j].Low && bars[i].Low <= bars[i+j].Low) {
				isLow = false
			}
		}
		if isLong && isHigh && bars[i].High > entry {
			if d := bars[i].High - entry; d < best {
				best, found = d, true
			}
		}
		if !isLong && isLow && bars[i].Low < entry {
			if d := entry - bars[i].Low; d < best {
				best, found = d, true
			}
		}
	}
	if !found {
		return 0, false
	}
	return best / entry * 100, true
}

// primaryTFClosedBars returns the primary-timeframe closed candles (the still
// forming last bar is dropped) plus the timeframe token. Mirrors
// confirmationTFBars but stays on the PRIMARY timeframe: the structural effect
// was timeframe-specific in validation — it held on 1h (the primary of 4 of the
// 5 live strategies) and vanished or inverted on 15m and 4h.
func primaryTFClosedBars(cfg *store.StrategyConfig, data *market.Data) ([]market.KlineBar, string) {
	if cfg == nil || data == nil || len(data.TimeframeData) == 0 {
		return nil, ""
	}
	primary := strings.TrimSpace(cfg.Indicators.Klines.PrimaryTimeframe)
	if primary == "" {
		return nil, ""
	}
	series, ok := data.TimeframeData[primary]
	if !ok || series == nil || len(series.Klines) < 4 {
		return nil, ""
	}
	return series.Klines[:len(series.Klines)-1], primary
}
