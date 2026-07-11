package trader

import (
	"fmt"
	"strings"

	"nofx/kernel"
	"nofx/market"
	"nofx/store"
)

// regime_gate.go bridges the configurable RegimeGateConfig (promoted from the
// shadow dry-run candidates) into the live entry path. It reuses the EXACT rule
// primitives the shadow sweep uses (counterTrend, chopLowConf, sgADX, ...), so a
// gate's enforce behavior is identical to what the dry-run measured. The full
// observation-only shadow sweep still runs separately, so enforcing one gate never
// stops us from recording every candidate's counterfactual verdict.

// RegimeGateHit is one enforcing gate that would block the given open.
type RegimeGateHit struct {
	Category string
	Detail   string
}

// evalRegimeGate runs a single configured gate over the causal context and reports
// whether it blocks, plus a human detail. Unknown categories never block.
func evalRegimeGate(g store.RegimeGateConfig, ctx shadowGateCtx) (bool, string) {
	switch g.Category {
	case "counter_trend":
		w := g.Params.SlopeWindow
		if w == 0 {
			w = 30
		}
		block, _, detail := counterTrend(ctx, w)
		return block, detail
	case "trend_direction_only":
		w := g.Params.SlopeWindow
		if w == 0 {
			w = 50
		}
		reg := trendLabel(ctx, w)
		side := strings.ToUpper(g.Params.BlockSide)
		block := (side == "LONG" && reg == "TREND_DN" && ctx.side == "LONG") ||
			(side == "SHORT" && reg == "TREND_UP" && ctx.side == "SHORT")
		return block, fmt.Sprintf("slope%d=%s side=%s blockSide=%s", w, reg, ctx.side, side)
	case "chop_reject":
		chop := sgConsensusChop(ctx.highs, ctx.lows, ctx.closes)
		return chop, fmt.Sprintf("consensusChop=%v", chop)
	case "chop_lowconf":
		mc := g.Params.MinConf
		if mc == 0 {
			mc = 70
		}
		block, _, detail := chopLowConf(ctx, mc)
		return block, detail
	case "adx_weak":
		thr := g.Params.Threshold
		if thr == 0 {
			thr = 20
		}
		adx := sgADX(ctx.highs, ctx.lows, ctx.closes, 14)
		return adx < thr, fmt.Sprintf("adx=%.1f<%.1f", adx, thr)
	case "donchian_counter":
		n := g.Params.Lookback
		if n == 0 {
			n = 48
		}
		br := sgDonchianBreak(ctx.highs, ctx.lows, ctx.closes, n)
		block := (br > 0 && ctx.side == "SHORT") || (br < 0 && ctx.side == "LONG")
		return block, fmt.Sprintf("donchian%d=%d side=%s", n, br, ctx.side)
	}
	return false, "unknown_category:" + g.Category
}

// evaluateEnforceRegimeGates checks all ENFORCE-mode enabled gates for one open
// decision and returns every hit. Empty result = nothing blocks. Best-effort: if
// the causal context can't be built, it returns nil (fail-open, never blocks on a
// data gap — the standard shadow-gate posture).
func evaluateEnforceRegimeGates(cfg *store.StrategyConfig, d *kernel.Decision, data *market.Data, primaryTF, exchange string) []RegimeGateHit {
	if cfg == nil || len(cfg.RegimeGates) == 0 {
		return nil
	}
	hasEnforce := false
	for _, g := range cfg.RegimeGates {
		if g.Enabled && g.Mode == "enforce" {
			hasEnforce = true
			break
		}
	}
	if !hasEnforce {
		return nil
	}
	ctx, ok := buildShadowGateCtx(d, data, primaryTF, exchange)
	if !ok {
		return nil
	}
	var hits []RegimeGateHit
	for _, g := range cfg.RegimeGates {
		if !g.Enabled || g.Mode != "enforce" {
			continue
		}
		if block, detail := evalRegimeGate(g, ctx); block {
			hits = append(hits, RegimeGateHit{Category: g.Category, Detail: detail})
		}
	}
	return hits
}
