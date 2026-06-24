package trader

import (
	"sync"

	"nofx/logger"
	"nofx/market"
	"nofx/store"
)

// frozenATREntry caches a position's ATR frozen at open so the reconciler reuses
// a stable value instead of recomputing ATR every cycle (which drifts bar-to-bar
// and made resting protection orders perpetually look "unexpected" → order churn).
type frozenATREntry struct {
	entryPrice float64
	atr        float64
}

var (
	frozenATRMu    sync.Mutex
	frozenATRCache = map[string]frozenATREntry{}
)

// frozenATRForPosition returns a stable ATR for a position keyed by symbol.
// First call (at open) computes fresh ATR and freezes it against the entry
// price; subsequent calls (reconcile cycles) reuse the frozen value. When the
// entry price changes (a new position on the same symbol) it recomputes and
// re-freezes. After a restart the cache is empty, so the first reconcile
// re-freezes at the then-current ATR (a one-time small shift, then stable).
func (at *AutoTrader) frozenATRForPosition(symbol string, entryPrice float64, cfg store.ATRProtectionConfig) (float64, bool) {
	key := at.id + "|" + symbol
	frozenATRMu.Lock()
	ent, ok := frozenATRCache[key]
	frozenATRMu.Unlock()
	if ok && entryPrice > 0 && entrySamePosition(ent.entryPrice, entryPrice) && ent.atr > 0 {
		return ent.atr, true
	}
	atr, ok := at.atrForProtection(symbol, cfg)
	if !ok {
		return 0, false
	}
	frozenATRMu.Lock()
	frozenATRCache[key] = frozenATREntry{entryPrice: entryPrice, atr: atr}
	frozenATRMu.Unlock()
	return atr, true
}

// entrySamePosition reports whether two entry prices refer to the same position
// (within 0.05% — covers minor fill-price rounding between open and reconcile).
func entrySamePosition(a, b float64) bool {
	if a <= 0 || b <= 0 {
		return false
	}
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff/b <= 0.0005
}

// atrFor1h returns the Wilder ATR(period) for the symbol on the configured
// timeframe, fetched fresh from market data. Returns (atr, ok).
func (at *AutoTrader) atrForProtection(symbol string, cfg store.ATRProtectionConfig) (float64, bool) {
	c := cfg.WithDefaults()
	bars, err := market.GetKlines(symbol, c.Timeframe, at.exchange, c.ATRPeriod*4+10)
	if err != nil || len(bars) < c.ATRPeriod+1 {
		return 0, false
	}
	highs := make([]float64, len(bars))
	lows := make([]float64, len(bars))
	closes := make([]float64, len(bars))
	for i, b := range bars {
		highs[i] = b.High
		lows[i] = b.Low
		closes[i] = b.Close
	}
	atr := wilderATRLast(highs, lows, closes, c.ATRPeriod)
	if atr <= 0 {
		return 0, false
	}
	return atr, true
}

// wilderATRLast computes Wilder ATR(period) and returns the last value.
func wilderATRLast(highs, lows, closes []float64, period int) float64 {
	n := len(closes)
	if period <= 0 || n < period+1 {
		return 0
	}
	trs := make([]float64, 0, n-1)
	for i := 1; i < n; i++ {
		hl := highs[i] - lows[i]
		hc := absFloat(highs[i] - closes[i-1])
		lc := absFloat(lows[i] - closes[i-1])
		tr := hl
		if hc > tr {
			tr = hc
		}
		if lc > tr {
			tr = lc
		}
		trs = append(trs, tr)
	}
	if len(trs) < period {
		return 0
	}
	var sum float64
	for i := 0; i < period; i++ {
		sum += trs[i]
	}
	atr := sum / float64(period)
	for i := period; i < len(trs); i++ {
		atr = (atr*float64(period-1) + trs[i]) / float64(period)
	}
	return atr
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// effectiveATRMultiples returns the ATR config with per-dimension multiples
// resolved. If ANY dimension is in "ai" mode it queries (cached) AI multiples
// for the symbol and overrides ONLY the AI-mode dimensions' multiples; fixed
// and percent dimensions keep their configured values. On AI failure all
// dimensions fall back to their configured fixed multiples.
func (at *AutoTrader) effectiveATRMultiples(symbol string, acfg store.ATRProtectionConfig, atr, entryPrice float64) store.ATRProtectionConfig {
	c := acfg.WithDefaults()
	if entryPrice <= 0 || atr <= 0 {
		return c
	}
	// Determine which dimensions want AI multiples.
	aiSL := c.DimMode(store.ATRDimSL) == "ai"
	aiTP1 := c.DimMode(store.ATRDimTP1) == "ai"
	aiTP2 := c.DimMode(store.ATRDimTP2) == "ai"
	aiBE1 := c.DimMode(store.ATRDimBE1) == "ai"
	aiBE2 := c.DimMode(store.ATRDimBE2) == "ai"
	aiDD := c.DimMode(store.ATRDimDD) == "ai"
	if !(aiSL || aiTP1 || aiTP2 || aiBE1 || aiBE2 || aiDD) {
		return c // no AI dimensions; use fixed multiples as-is
	}
	atrPct := atr / entryPrice * 100.0
	m, ok := at.resolveAIMultiples(symbol, c, atr, atrPct, entryPrice)
	if !ok {
		return c // fall back to fixed multiples
	}
	if aiSL {
		c.StopLossATR = m.StopLossATR
	}
	if aiTP1 {
		c.TakeProfit1ATR = m.TakeProfit1ATR
	}
	if aiTP2 {
		c.TakeProfit2ATR = m.TakeProfit2ATR
	}
	if aiBE1 {
		c.BreakEven1ATR = m.BreakEven1ATR
	}
	if aiBE2 {
		c.BreakEven2ATR = m.BreakEven2ATR
	}
	if aiDD {
		// Reuse the TP2-scale AI multiple for DD min-profit if not separately modeled.
		c.DrawdownMinProfitATR = m.TakeProfit1ATR
	}
	return c
}

// resolveATRProtection returns a copy of the strategy's ProtectionConfig with
// percent distances overwritten by ATR-derived percents, when ATRProtection is
// enabled for this trader. Returns (adjustedProtection, true) when ATR sizing
// was applied; (original, false) otherwise (no-op for non-ATR traders).
//
// Only dimensions with a positive ATR multiple are overwritten; others keep
// their configured percent. Mirrors the resolve-at-placement pattern.
func (at *AutoTrader) resolveATRProtection(entryPrice float64, symbol string) (store.ProtectionConfig, bool) {
	if at.config.StrategyConfig == nil {
		return store.ProtectionConfig{}, false
	}
	base := at.config.StrategyConfig.Protection
	acfg := at.config.StrategyConfig.ATRProtection
	if !acfg.Enabled || entryPrice <= 0 {
		return base, false
	}
	atr, ok := at.frozenATRForPosition(symbol, entryPrice, acfg)
	if !ok {
		logger.Warnf("  ⚠️ ATR-protection: no ATR for %s; falling back to configured percents", symbol)
		return base, false
	}
	// Resolve multiples (fixed config or per-coin AI) before applying.
	acfg = at.effectiveATRMultiples(symbol, acfg, atr, entryPrice)

	adj := base // value copy; nested slices are copied below before mutation
	applied := false

	// Ladder TP/SL: overwrite TP1/TP2 distance and SL distance per rule index,
	// only for dimensions whose mode is fixed/ai (percent = leave untouched).
	if len(base.LadderTPSL.Rules) > 0 {
		rules := make([]store.LadderTPSLRule, len(base.LadderTPSL.Rules))
		copy(rules, base.LadderTPSL.Rules)
		slActive := acfg.DimMode(store.ATRDimSL) != "percent"
		for i := range rules {
			// SL distance (applies to whichever rule carries the SL).
			if slActive && acfg.StopLossATR > 0 && rules[i].StopLossPct > 0 {
				if pct, ok := acfg.EffectivePercent(acfg.StopLossATR, atr, entryPrice); ok {
					rules[i].StopLossPct = pct
					applied = true
				}
			}
			// TP distances by ladder tier.
			var tpMult float64
			var tpActive bool
			switch i {
			case 0:
				tpMult = acfg.TakeProfit1ATR
				tpActive = acfg.DimMode(store.ATRDimTP1) != "percent"
			case 1:
				tpMult = acfg.TakeProfit2ATR
				tpActive = acfg.DimMode(store.ATRDimTP2) != "percent"
			}
			if tpActive && tpMult > 0 && rules[i].TakeProfitPct > 0 {
				if pct, ok := acfg.EffectivePercent(tpMult, atr, entryPrice); ok {
					rules[i].TakeProfitPct = pct
					applied = true
				}
			}
		}
		adj.LadderTPSL.Rules = rules
	}

	// Break-even tiers: overwrite trigger values by tier, per-dimension mode.
	if len(base.BreakEvenStop.Rules) > 0 {
		beRules := make([]store.BreakEvenStopRule, len(base.BreakEvenStop.Rules))
		copy(beRules, base.BreakEvenStop.Rules)
		for i := range beRules {
			var beMult float64
			var beActive bool
			switch i {
			case 0:
				beMult = acfg.BreakEven1ATR
				beActive = acfg.DimMode(store.ATRDimBE1) != "percent"
			case 1:
				beMult = acfg.BreakEven2ATR
				beActive = acfg.DimMode(store.ATRDimBE2) != "percent"
			}
			if beActive && beMult > 0 && beRules[i].TriggerMode == store.BreakEvenTriggerProfitPct && beRules[i].TriggerValue > 0 {
				if pct, ok := acfg.EffectivePercent(beMult, atr, entryPrice); ok {
					beRules[i].TriggerValue = pct
					applied = true
				}
			}
		}
		adj.BreakEvenStop.Rules = beRules
	}

	// Drawdown: ATR-ize the min-profit ARM threshold (price distance) when the
	// DD dimension is in fixed/ai mode. Max-drawdown give-back stays % of peak.
	if acfg.DimMode(store.ATRDimDD) != "percent" && acfg.DrawdownMinProfitATR > 0 && len(base.DrawdownTakeProfit.Rules) > 0 {
		ddRules := make([]store.DrawdownTakeProfitRule, len(base.DrawdownTakeProfit.Rules))
		copy(ddRules, base.DrawdownTakeProfit.Rules)
		if pct, ok := acfg.EffectivePercent(acfg.DrawdownMinProfitATR, atr, entryPrice); ok {
			// Apply to the primary (runner_exit) rule = highest min-profit tier.
			best := -1
			for i := range ddRules {
				if ddRules[i].MinProfitPct > 0 && (best < 0 || ddRules[i].MinProfitPct > ddRules[best].MinProfitPct) {
					best = i
				}
			}
			if best >= 0 {
				ddRules[best].MinProfitPct = pct
				applied = true
			}
		}
		adj.DrawdownTakeProfit.Rules = ddRules
	}

	if applied {
		logger.Infof("  🎯 ATR-protection applied for %s: ATR(%s)=%.6f entry=%.6f → per-dimension ATR-scaled (SL/TP/BE/DD)",
			symbol, acfg.WithDefaults().Timeframe, atr, entryPrice)
	}
	return adj, applied
}

// getActiveBreakEvenRulesATR returns the active break-even rules with trigger
// values ATR-adjusted when ATR protection is enabled for this trader/symbol.
// Falls back to the raw configured rules when ATR is off or unavailable, so the
// runtime BE monitor arms at the SAME trigger distance the orders were placed
// with at open (no percent/ATR mismatch).
func (at *AutoTrader) getActiveBreakEvenRulesATR(symbol string, entryPrice float64) []store.BreakEvenStopRule {
	rules := at.getActiveBreakEvenRules()
	if len(rules) == 0 {
		return rules
	}
	if at.config.StrategyConfig == nil {
		return rules
	}
	acfg := at.config.StrategyConfig.ATRProtection
	if !acfg.Enabled || entryPrice <= 0 {
		return rules
	}
	atr, ok := at.frozenATRForPosition(symbol, entryPrice, acfg)
	if !ok {
		return rules
	}
	// Resolve multiples (fixed or per-coin AI) so BE arms at the same triggers
	// the orders were placed with at open.
	acfg = at.effectiveATRMultiples(symbol, acfg, atr, entryPrice)
	out := make([]store.BreakEvenStopRule, len(rules))
	copy(out, rules)
	for i := range out {
		var beMult float64
		var active bool
		switch i {
		case 0:
			beMult = acfg.BreakEven1ATR
			active = acfg.DimMode(store.ATRDimBE1) != "percent"
		case 1:
			beMult = acfg.BreakEven2ATR
			active = acfg.DimMode(store.ATRDimBE2) != "percent"
		}
		if active && beMult > 0 && out[i].TriggerMode == store.BreakEvenTriggerProfitPct && out[i].TriggerValue > 0 {
			if pct, ok := acfg.EffectivePercent(beMult, atr, entryPrice); ok {
				out[i].TriggerValue = pct
			}
		}
	}
	return out
}

