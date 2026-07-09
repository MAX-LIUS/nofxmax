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
func buildShadowGateCtx(d *kernel.Decision, data *market.Data, primaryTF string) (shadowGateCtx, bool) {
	if data == nil {
		return shadowGateCtx{}, false
	}
	var bars []market.KlineBar
	if data.TimeframeData != nil {
		if tf, ok := data.TimeframeData[primaryTF]; ok && tf != nil && len(tf.Klines) >= 60 {
			bars = tf.Klines
		} else {
			// fall back to the longest available timeframe series
			for _, tf := range data.TimeframeData {
				if tf != nil && len(tf.Klines) > len(bars) {
					bars = tf.Klines
				}
			}
		}
	}
	if len(bars) < 60 {
		return shadowGateCtx{}, false
	}
	h := make([]float64, len(bars))
	l := make([]float64, len(bars))
	cl := make([]float64, len(bars))
	for i, b := range bars {
		h[i], l[i], cl[i] = b.High, b.Low, b.Close
	}
	return shadowGateCtx{
		highs:  h,
		lows:   l,
		closes: cl,
		side:   strings.ToUpper(directionFromAction(d.Action)),
		conf:   float64(d.Confidence),
	}, true
}

// evaluateShadowGates runs ALL candidate rules for one open decision and returns
// the verdict rows. Observation-only: never mutates the decision or blocks it.
func evaluateShadowGates(traderID string, cycle int64, d *kernel.Decision, data *market.Data, primaryTF string, liveAllowed bool) []*store.ShadowGateVerdict {
	ctx, ok := buildShadowGateCtx(d, data, primaryTF)
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
