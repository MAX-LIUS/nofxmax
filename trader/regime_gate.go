package trader

import (
	"fmt"
	"strings"
	"time"

	"nofx/kernel"
	"nofx/logger"
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
		// Period is configurable because the useful period is timeframe-dependent.
		// Default stays 14 so existing configs keep their exact behaviour.
		// Measured on Claude-R 15m (369 entries, neutral 6h hold): ADX(14) is flat
		// to harmful at every threshold (>=20 → -0.072, >=25 → -0.064, >=30 →
		// -0.120 vs baseline -0.071), while ADX(10)>=30 is the only variant that
		// helps. Hardcoding 14 made that combination unexpressible in config.
		period := g.Params.ADXPeriod
		if period <= 0 {
			period = 14
		}
		adx := sgADX(ctx.highs, ctx.lows, ctx.closes, period)
		return adx < thr, fmt.Sprintf("adx%d=%.1f<%.1f", period, adx, thr)
	case "donchian_counter":
		n := g.Params.Lookback
		if n == 0 {
			n = 48
		}
		br := sgDonchianBreak(ctx.highs, ctx.lows, ctx.closes, n)
		block := (br > 0 && ctx.side == "SHORT") || (br < 0 && ctx.side == "LONG")
		return block, fmt.Sprintf("donchian%d=%d side=%s", n, br, ctx.side)
	case "chart_trend":
		w := g.Params.SlopeWindow
		if w == 0 {
			w = 30
		}
		alignMin := g.Params.AlignMin
		if alignMin == 0 {
			alignMin = 0.55
		}
		r2Min := g.Params.R2Min
		if r2Min == 0 {
			r2Min = 0.60
		}
		// Pivot lookback for the swing-alignment term. Default 2 preserves the
		// behaviour this gate was calibrated with; it is settable because the
		// rest of the codebase treats lb=2 and lb=3 as materially different
		// (see store.EntryGateConfig.StructuralPivotLookback: lb=2 is noise-level,
		// lb=3 is decisive) and this gate had no way to express that.
		lb := g.Params.Lookback
		if lb <= 0 {
			lb = 2
		}
		return chartTrendGate(ctx, w, alignMin, r2Min, lb)
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

// captureBlockedIntent records an enforce-blocked open so it can be paper-traded
// through the real protection ladder later (blocksim), restoring the
// counterfactual. Best-effort: never blocks or errors into the live path.
func (at *AutoTrader) captureBlockedIntent(d *kernel.Decision, data *market.Data, blockedBy string) {
	if at.store == nil || d == nil || data == nil {
		return
	}
	entry := data.CurrentPrice
	if entry <= 0 || d.StopLoss <= 0 {
		return // no usable entry/stop => can't simulate; skip
	}
	qty := 0.0
	if d.PositionSizeUSD > 0 {
		qty = d.PositionSizeUSD / entry
	}
	if qty <= 0 {
		qty = 1.0 // nominal; R = pnl/risk normalizes size out
	}
	now := time.Now().UTC().UnixMilli()
	o := &store.BlockedSimOutcome{
		TraderID:   at.id,
		Cycle:      int64(at.cycleNumber),
		Symbol:     d.Symbol,
		Side:       strings.ToUpper(directionFromAction(d.Action)),
		EntryPrice: entry,
		StopLoss:   d.StopLoss,
		TakeProfit: d.TakeProfit,
		Quantity:   qty,
		Confidence: float64(d.Confidence),
		Leverage:   d.Leverage,
		BlockedBy:  blockedBy,
		SimStatus:  "pending",
		ObservedAt: now,
		CreatedAt:  now,
	}
	if err := at.store.BlockedSim().Record(o); err != nil {
		logger.Infof("⚠ blocksim capture failed (non-blocking): %v", err)
	}
}
