package trader

import (
	"fmt"
	"strings"
	"time"

	"nofx/kernel"
	"nofx/market"
	"nofx/store"
)

// shadowGateCtx is the causal snapshot a shadow rule sees: OHLC up to the decision
// bar (last element = most recent closed bar), plus the decision's side/confidence.
type shadowGateCtx struct {
	highs []float64
	lows  []float64
	closes []float64
	side  string // LONG / SHORT
	conf  float64
	// builtinRegime is the label the LIVE built-in regime filter would assign to this
	// bar (classifyProtectionRegime): trending_up / trending_down / ranging / "".
	// Captured so the built-in regime filter can be scored as a parallel shadow rule
	// using its REAL live logic (EMA20/MACD/trend-score) rather than a slope proxy.
	builtinRegime string
}

// shadowRule is one candidate gate method. Returns (wouldBlock, regimeLabel, detail).
type shadowRule struct {
	name string
	fn   func(c shadowGateCtx) (bool, string, string)
}

// shadowRules is the FULL set of regime/trend candidate gates evaluated in
// parallel, observation-only. Add a rule here and it is automatically logged for
// every open decision — no other wiring needed. Forward live data then ranks them.
var shadowRules = []shadowRule{
	// ── Counter-trend family (reject entry opposing regression-slope trend) ──
	{"countertrend_slope30", func(c shadowGateCtx) (bool, string, string) {
		return counterTrend(c, 30)
	}},
	{"countertrend_slope50", func(c shadowGateCtx) (bool, string, string) {
		return counterTrend(c, 50)
	}},
	{"countertrend_slope72", func(c shadowGateCtx) (bool, string, string) {
		return counterTrend(c, 72)
	}},
	// ── Downtrend-long-only (the single bleeding cell, isolated) ──
	{"downtrend_long_only_s50", func(c shadowGateCtx) (bool, string, string) {
		reg := trendLabel(c, 50)
		block := reg == "TREND_DN" && c.side == "LONG"
		return block, reg, fmt.Sprintf("slope50=%s side=%s", reg, c.side)
	}},
	// ── Uptrend-short-only ──
	{"uptrend_short_only_s50", func(c shadowGateCtx) (bool, string, string) {
		reg := trendLabel(c, 50)
		block := reg == "TREND_UP" && c.side == "SHORT"
		return block, reg, fmt.Sprintf("slope50=%s side=%s", reg, c.side)
	}},
	// ── Chop family (reject ALL entries during consensus chop) ──
	{"chop_reject_all", func(c shadowGateCtx) (bool, string, string) {
		chop := sgConsensusChop(c.highs, c.lows, c.closes)
		reg := "TREND"
		if chop {
			reg = "CHOP"
		}
		return chop, reg, fmt.Sprintf("consensusChop=%v", chop)
	}},
	// ── Chop + low-confidence (reject only low-conf entries in chop) ──
	{"chop_lowconf_lt70", func(c shadowGateCtx) (bool, string, string) {
		return chopLowConf(c, 70)
	}},
	{"chop_lowconf_lt80", func(c shadowGateCtx) (bool, string, string) {
		return chopLowConf(c, 80)
	}},
	// ── ADX-weak (reject entries when trend strength is weak, ADX<20) ──
	{"adx_weak_lt20", func(c shadowGateCtx) (bool, string, string) {
		adx := sgADX(c.highs, c.lows, c.closes, 14)
		return adx < 20, fmt.Sprintf("ADX=%.1f", adx), fmt.Sprintf("adx=%.1f<20", adx)
	}},
	// ── Donchian counter-breakout (reject entry opposing 48-bar breakout) ──
	{"donchian48_counter", func(c shadowGateCtx) (bool, string, string) {
		br := sgDonchianBreak(c.highs, c.lows, c.closes, 48)
		reg := "none"
		block := false
		if br > 0 {
			reg = "BREAK_UP"
			block = c.side == "SHORT"
		} else if br < 0 {
			reg = "BREAK_DN"
			block = c.side == "LONG"
		}
		return block, reg, fmt.Sprintf("donchian48=%s side=%s", reg, c.side)
	}},
	// ── Built-in regime filter (the LIVE production directional gate) ──
	// Scored as a parallel candidate using its REAL classifyProtectionRegime label
	// (EMA20/MACD/trend-score), not a slope proxy. Blocks a long in trending_down
	// and a short in trending_up — the same trend-alignment rule the live gate
	// enforces. Lets the operator compare the built-in gate head-to-head with the
	// configurable slope/direction gates on one scorecard.
	{"builtin_regime_filter", func(c shadowGateCtx) (bool, string, string) {
		reg := c.builtinRegime
		block := (reg == "trending_down" && c.side == "LONG") ||
			(reg == "trending_up" && c.side == "SHORT")
		return block, reg, fmt.Sprintf("regime=%s side=%s", reg, c.side)
	}},
}

func trendLabel(c shadowGateCtx, window int) string {
	if sgConsensusChop(c.highs, c.lows, c.closes) {
		return "CHOP"
	}
	switch sgSlopeSign(c.closes, window) {
	case 1:
		return "TREND_UP"
	case -1:
		return "TREND_DN"
	default:
		return "FLAT"
	}
}

func counterTrend(c shadowGateCtx, window int) (bool, string, string) {
	reg := trendLabel(c, window)
	block := (reg == "TREND_DN" && c.side == "LONG") || (reg == "TREND_UP" && c.side == "SHORT")
	return block, reg, fmt.Sprintf("slope%d=%s side=%s", window, reg, c.side)
}

func chopLowConf(c shadowGateCtx, minConf float64) (bool, string, string) {
	chop := sgConsensusChop(c.highs, c.lows, c.closes)
	reg := "TREND"
	if chop {
		reg = "CHOP"
	}
	block := chop && c.conf > 0 && c.conf < minConf
	return block, reg, fmt.Sprintf("chop=%v conf=%.0f<%.0f", chop, c.conf, minConf)
}

// buildShadowGateCtx extracts a causal OHLC snapshot from MarketData for the
// primary timeframe. Returns ok=false if insufficient klines.
// minShadowBars is the longest lookback any candidate rule needs
// (countertrend_slope72 = 72). The decision-context market data is trimmed to
// PrimaryCount (typically 30) for the AI prompt, which is far too short, so the
// shadow gate self-fetches a longer causal series when the context is thin.
const minShadowBars = 72

func buildShadowGateCtx(d *kernel.Decision, data *market.Data, primaryTF, exchange string) (shadowGateCtx, bool) {
	side := strings.ToUpper(directionFromAction(d.Action))

	// 1) Prefer the in-memory decision context if it already carries enough bars.
	var highs, lows, closes []float64
	if data != nil && data.TimeframeData != nil {
		var bars []market.KlineBar
		if tf, ok := data.TimeframeData[primaryTF]; ok && tf != nil && len(tf.Klines) >= minShadowBars {
			bars = tf.Klines
		} else {
			for _, tf := range data.TimeframeData { // longest available series
				if tf != nil && len(tf.Klines) > len(bars) {
					bars = tf.Klines
				}
			}
		}
		if len(bars) >= minShadowBars {
			highs = make([]float64, len(bars))
			lows = make([]float64, len(bars))
			closes = make([]float64, len(bars))
			for i, b := range bars {
				highs[i], lows[i], closes[i] = b.High, b.Low, b.Close
			}
		}
	}

	// 2) Fall back to a best-effort self-fetch of a longer series. Observation-only:
	// any failure just skips this decision (never touches execution).
	if len(closes) < minShadowBars && d.Symbol != "" && primaryTF != "" {
		ex := exchange
		if ex == "" {
			ex = "okx"
		}
		if kl, err := market.GetKlines(d.Symbol, primaryTF, ex, 120); err == nil && len(kl) >= minShadowBars {
			highs = make([]float64, len(kl))
			lows = make([]float64, len(kl))
			closes = make([]float64, len(kl))
			for i, k := range kl {
				highs[i], lows[i], closes[i] = k.High, k.Low, k.Close
			}
		}
	}

	if len(closes) < minShadowBars {
		return shadowGateCtx{}, false
	}
	// Capture the live built-in regime label from the SAME market.Data the live gate
	// sees, so the built-in regime filter is scored via its real logic, not a proxy.
	builtinRegime := ""
	if data != nil {
		builtinRegime = classifyProtectionRegime(data)
	}
	return shadowGateCtx{
		highs:  highs,
		lows:   lows,
		closes: closes,
		side:   side,
		conf:   float64(d.Confidence),
		builtinRegime: builtinRegime,
	}, true
}

// evaluateShadowGates runs ALL candidate rules for one open decision and returns
// the verdict rows. Observation-only: never mutates the decision or blocks it.
func evaluateShadowGates(traderID string, cycle int64, d *kernel.Decision, data *market.Data, primaryTF, exchange string, liveAllowed bool) []*store.ShadowGateVerdict {
	ctx, ok := buildShadowGateCtx(d, data, primaryTF, exchange)
	if !ok {
		return nil
	}
	now := time.Now().UTC().UnixMilli()
	out := make([]*store.ShadowGateVerdict, 0, len(shadowRules))
	for _, r := range shadowRules {
		block, regime, detail := r.fn(ctx)
		out = append(out, &store.ShadowGateVerdict{
			TraderID:    traderID,
			Cycle:       cycle,
			Symbol:      d.Symbol,
			Action:      d.Action,
			Side:        ctx.side,
			RuleName:    r.name,
			WouldBlock:  block,
			Regime:      regime,
			Confidence:  ctx.conf,
			Detail:      detail,
			LiveAllowed: liveAllowed,
			ObservedAt:  now,
			CreatedAt:   now,
		})
	}
	return out
}

// ── Backfill exports ──
// These let the offline shadowbackfill tool run the EXACT same rule registry over
// historical klines, so backfilled verdicts are identical in logic to live ones.

// ShadowBackfillVerdict is the minimal verdict shape for the backfill tool.
type ShadowBackfillVerdict struct {
	Rule   string
	Block  bool
	Regime string
	Detail string
}

// ShadowRuleNames returns the names of all registered candidate rules.
func ShadowRuleNames() []string {
	out := make([]string, 0, len(shadowRules))
	for _, r := range shadowRules {
		out = append(out, r.name)
	}
	return out
}

// EvaluateShadowGatesForBackfill runs every rule over a causal kline slice (last
// bar = entry bar) for a given side, returning one verdict per rule. Confidence
// is unavailable in pure-price backfill, so conf-gated rules see conf=0 (their
// chop branch still evaluates; the conf<threshold test is simply not satisfied).
func EvaluateShadowGatesForBackfill(bars []market.Kline, side string) []ShadowBackfillVerdict {
	if len(bars) < 60 {
		return nil
	}
	h := make([]float64, len(bars))
	l := make([]float64, len(bars))
	c := make([]float64, len(bars))
	for i, b := range bars {
		h[i], l[i], c[i] = b.High, b.Low, b.Close
	}
	ctx := shadowGateCtx{highs: h, lows: l, closes: c, side: side, conf: 0}
	out := make([]ShadowBackfillVerdict, 0, len(shadowRules))
	for _, r := range shadowRules {
		block, regime, detail := r.fn(ctx)
		out = append(out, ShadowBackfillVerdict{Rule: r.name, Block: block, Regime: regime, Detail: detail})
	}
	return out
}
