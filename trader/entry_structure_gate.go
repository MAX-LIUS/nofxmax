package trader

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"nofx/logger"
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

// structuralMinBars is the minimum number of CLOSED primary-timeframe bars the
// structural check needs before it is allowed to block anything.
//
// This is NOT the same as the arithmetic minimum for pivots to exist (2*lb+2 = 8
// at lb=3). It is a VALIDATION-COVERAGE floor, and it exists because of a real
// production defect found on 2026-07-31: the gate reads the decision-context
// market data, which is trimmed to Indicators.Klines.PrimaryCount for the AI
// prompt. GPT-ct50 had primary_count=22 -> 21 closed bars, and re-running the
// backtest truncated to that window showed the gate degrade to noise:
//
//	window                  pass%   increment   p
//	full history (backtest)  33.6%   +0.263      0.0004
//	29 bars (claude-ct30)    23.7%   +0.309      0.0002
//	21 bars (GPT-ct50)        7.7%   +0.160      0.1522   <-- ineffective
//	40 bars                  32.6%   +0.276      0.0004
//
// At 21 bars, 92.3% of entries were blocked for having too few pivots to form
// n=2 highs AND n=2 lows — i.e. blocked for "structure not VISIBLE", not for
// "structure not PRESENT". That is a data-starvation artifact, not a signal.
const structuralMinBars = 30

// structuralFetchBars is how many bars the self-fetch asks for. Generous on
// purpose: the whole point is to decouple the gate's window from the prompt's
// window, and the validated calibration was run on full history.
const structuralFetchBars = 120

type structuralBarsCacheEntry struct {
	bars      []market.KlineBar
	updatedAt time.Time
}

// structuralBarsCache collapses the self-fetch across traders and across retries
// within a cycle. Keyed by symbol|tf|exchange. TTL is deliberately shorter than
// the shortest primary timeframe in use (15m), so a bar close is never masked.
var structuralBarsCache sync.Map

const structuralBarsCacheTTL = 60 * time.Second

// structuralBars returns the closed primary-timeframe bars the structural check
// should run on, preferring the in-context series and self-fetching a longer one
// when the context is too thin to be the window the rule was validated on.
//
// Returns nil when a sufficient series cannot be obtained. Callers MUST treat
// nil as "abstain" (allow the entry), never as "block": this runs on the
// execution path, so a failed fetch must not turn into a trade decision.
func structuralBars(cfg *store.StrategyConfig, data *market.Data, symbol, exchange string) ([]market.KlineBar, string) {
	bars, tf := primaryTFClosedBars(cfg, data)
	if len(bars) >= structuralMinBars {
		return bars, tf
	}
	// Resolve the timeframe even when the context had no usable series at all,
	// otherwise a thin context would also lose the ability to self-fetch.
	if tf == "" {
		if cfg == nil {
			return nil, ""
		}
		tf = strings.TrimSpace(cfg.Indicators.Klines.PrimaryTimeframe)
	}
	if tf == "" || strings.TrimSpace(symbol) == "" {
		return nil, tf
	}

	ex := strings.TrimSpace(exchange)
	if ex == "" {
		ex = "okx"
	}
	key := fmt.Sprintf("%s|%s|%s", strings.ToUpper(strings.TrimSpace(symbol)), tf, strings.ToLower(ex))
	if cached, ok := structuralBarsCache.Load(key); ok {
		entry := cached.(*structuralBarsCacheEntry)
		if time.Since(entry.updatedAt) < structuralBarsCacheTTL {
			if len(entry.bars) >= structuralMinBars {
				return append([]market.KlineBar(nil), entry.bars...), tf
			}
			return nil, tf
		}
	}

	kl, err := market.GetKlines(symbol, tf, ex, structuralFetchBars)
	if err != nil || len(kl) < structuralMinBars+1 {
		// Abstain. Logged at debug level because a thin/failed fetch is a
		// non-event for execution: the entry proceeds as if the check were off.
		logger.Debugf("structural gate abstains on %s %s: self-fetch got %d bars (err=%v)", symbol, tf, len(kl), err)
		structuralBarsCache.Store(key, &structuralBarsCacheEntry{updatedAt: time.Now().UTC()})
		return nil, tf
	}
	// Drop the last bar: GetKlines includes the still-forming candle, and the
	// whole rule is defined on COMPLETED structure.
	out := make([]market.KlineBar, 0, len(kl)-1)
	for _, k := range kl[:len(kl)-1] {
		out = append(out, market.KlineBar{
			Time: k.OpenTime, Open: k.Open, High: k.High,
			Low: k.Low, Close: k.Close, Volume: k.Volume,
		})
	}
	structuralBarsCache.Store(key, &structuralBarsCacheEntry{bars: out, updatedAt: time.Now().UTC()})
	return append([]market.KlineBar(nil), out...), tf
}
