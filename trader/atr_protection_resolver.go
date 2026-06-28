package trader

import (
	"sync"
	"time"

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
// re-freezes.
//
// The frozen value is persisted to the store so a process restart reuses the
// open-time ATR instead of re-freezing at the then-current (drifted) ATR. This
// keeps ATR-mode activation/callback stable for the life of the position; the
// in-memory cache is a fast path in front of the persisted record.
func (at *AutoTrader) frozenATRForPosition(symbol string, entryPrice float64, cfg store.ATRProtectionConfig) (float64, bool) {
	key := at.id + "|" + symbol
	frozenATRMu.Lock()
	ent, ok := frozenATRCache[key]
	frozenATRMu.Unlock()
	if ok && entryPrice > 0 && entrySamePosition(ent.entryPrice, entryPrice) && ent.atr > 0 {
		return ent.atr, true
	}

	// Cache miss (cold start / restart): try the persisted record before
	// recomputing, so a restart does not re-freeze against today's ATR.
	if entryPrice > 0 && at.store != nil {
		if state, err := at.store.LoadFrozenATRState(); err == nil {
			if rec, found := state.Records[key]; found && rec.ATR > 0 && entrySamePosition(rec.EntryPrice, entryPrice) {
				frozenATRMu.Lock()
				frozenATRCache[key] = frozenATREntry{entryPrice: rec.EntryPrice, atr: rec.ATR}
				frozenATRMu.Unlock()
				return rec.ATR, true
			}
		}
	}

	atr, ok := at.atrForProtection(symbol, cfg)
	if !ok {
		return 0, false
	}
	frozenATRMu.Lock()
	frozenATRCache[key] = frozenATREntry{entryPrice: entryPrice, atr: atr}
	frozenATRMu.Unlock()
	// Persist the freshly frozen ATR so it survives a restart.
	if entryPrice > 0 && at.store != nil {
		rec := store.FrozenATRRecord{TraderID: at.id, Symbol: symbol, EntryPrice: entryPrice, ATR: atr, UpdatedAt: time.Now().Unix()}
		if err := at.store.SaveFrozenATRRecord(key, rec); err != nil {
			logger.Warnf("⚠️ Frozen ATR: failed to persist %s: %v", key, err)
		}
	}
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

// resolveATRProtection returns a copy of the strategy's ProtectionConfig with
// each ATR-UNIT field (per-rule TakeProfitUnit/StopLossUnit/TriggerUnit/
// MinProfitUnit == "atr") converted to an effective percent using the frozen
// ATR. Percent-unit fields are left untouched. Returns (adjusted, true) when at
// least one field was ATR-resolved; (original, false) otherwise. This replaces
// the former global per-dimension overlay: the unit now lives on each rule, so
// the UI manages everything in the original TP/SL/BE/DD panels.
func (at *AutoTrader) resolveATRProtection(entryPrice float64, symbol string) (store.ProtectionConfig, bool) {
	if at.config.StrategyConfig == nil {
		return store.ProtectionConfig{}, false
	}
	base := at.config.StrategyConfig.Protection
	acfg := at.config.StrategyConfig.ATRProtection
	if !acfg.Enabled || entryPrice <= 0 {
		return base, false
	}
	// Only fetch ATR if some field actually opts into ATR units.
	if !protectionUsesATR(base) {
		return base, false
	}
	atr, ok := at.frozenATRForPosition(symbol, entryPrice, acfg)
	if !ok {
		logger.Warnf("  ⚠️ ATR-protection: no ATR for %s; falling back to configured percents", symbol)
		return base, false
	}

	adj := base // value copy; nested slices copied below before mutation
	applied := false

	// Ladder TP/SL: resolve each rule's TP and SL distance when its unit is "atr".
	if len(base.LadderTPSL.Rules) > 0 {
		rules := make([]store.LadderTPSLRule, len(base.LadderTPSL.Rules))
		copy(rules, base.LadderTPSL.Rules)
		for i := range rules {
			if rules[i].StopLossUnit == store.ProtectionUnitATR && rules[i].StopLossPct > 0 {
				if pct, ok := acfg.EffectivePercent(rules[i].StopLossPct, atr, entryPrice); ok {
					rules[i].StopLossPct = pct
					applied = true
				}
			}
			if rules[i].TakeProfitUnit == store.ProtectionUnitATR && rules[i].TakeProfitPct > 0 {
				if pct, ok := acfg.EffectivePercent(rules[i].TakeProfitPct, atr, entryPrice); ok {
					rules[i].TakeProfitPct = pct
					applied = true
				}
			}
		}
		adj.LadderTPSL.Rules = rules
	}

	// Break-even tiers: resolve trigger when its unit is "atr" (profit_pct mode).
	if len(base.BreakEvenStop.Rules) > 0 {
		beRules := make([]store.BreakEvenStopRule, len(base.BreakEvenStop.Rules))
		copy(beRules, base.BreakEvenStop.Rules)
		for i := range beRules {
			if beRules[i].TriggerUnit == store.ProtectionUnitATR &&
				beRules[i].TriggerMode == store.BreakEvenTriggerProfitPct && beRules[i].TriggerValue > 0 {
				if pct, ok := acfg.EffectivePercent(beRules[i].TriggerValue, atr, entryPrice); ok {
					beRules[i].TriggerValue = pct
					applied = true
				}
			}
			// Offset shares the rule's TriggerUnit (sign preserved for losing-side BE).
			if beRules[i].TriggerUnit == store.ProtectionUnitATR && beRules[i].OffsetPct != 0 {
				if pct, ok := atrOffsetEffectivePercent(acfg, beRules[i].OffsetPct, atr, entryPrice); ok {
					beRules[i].OffsetPct = pct
					applied = true
				}
			}
		}
		adj.BreakEvenStop.Rules = beRules
	}

	// Drawdown: resolve each rule's min-profit ARM threshold when its unit is
	// "atr". Max-drawdown give-back stays a % of peak (a ratio, not a distance).
	if len(base.DrawdownTakeProfit.Rules) > 0 {
		ddRules := make([]store.DrawdownTakeProfitRule, len(base.DrawdownTakeProfit.Rules))
		copy(ddRules, base.DrawdownTakeProfit.Rules)
		for i := range ddRules {
			if ddRules[i].MinProfitUnit == store.ProtectionUnitATR && ddRules[i].MinProfitPct > 0 {
				if pct, ok := acfg.EffectivePercent(ddRules[i].MinProfitPct, atr, entryPrice); ok {
					ddRules[i].MinProfitPct = pct
					applied = true
				}
			}
		}
		adj.DrawdownTakeProfit.Rules = ddRules
	}

	if applied {
		logger.Infof("  🎯 ATR-units resolved for %s: ATR(%s)=%.6f entry=%.6f → per-field ATR→%% (SL/TP/BE/DD)",
			symbol, acfg.WithDefaults().Timeframe, atr, entryPrice)
	}
	return adj, applied
}

// protectionUsesATR reports whether any TP/SL/BE/DD field opts into ATR units,
// so we skip the ATR fetch entirely for pure-percent strategies.
func protectionUsesATR(p store.ProtectionConfig) bool {
	for _, r := range p.LadderTPSL.Rules {
		if r.StopLossUnit == store.ProtectionUnitATR || r.TakeProfitUnit == store.ProtectionUnitATR {
			return true
		}
	}
	for _, r := range p.BreakEvenStop.Rules {
		if r.TriggerUnit == store.ProtectionUnitATR {
			return true
		}
	}
	for _, r := range p.DrawdownTakeProfit.Rules {
		if r.MinProfitUnit == store.ProtectionUnitATR || r.MaxDrawdownUnit == store.ProtectionUnitATR {
			return true
		}
	}
	return false
}

// resolveDrawdownRulesATR resolves each drawdown rule's MinProfitPct to a
// percent when its MinProfitUnit is "atr", so the runtime DD arm threshold
// matches the open-time distance (fixes the prior bug where DD armed on the raw
// percent while open-time ATR-ized it). Percent-unit rules pass through.
func (at *AutoTrader) resolveDrawdownRulesATR(rules []store.DrawdownTakeProfitRule, symbol string, entryPrice float64) []store.DrawdownTakeProfitRule {
	if len(rules) == 0 || at.config.StrategyConfig == nil {
		return rules
	}
	acfg := at.config.StrategyConfig.ATRProtection
	if !acfg.Enabled || entryPrice <= 0 {
		return rules
	}
	anyATR := false
	for _, r := range rules {
		if r.MinProfitUnit == store.ProtectionUnitATR || r.MaxDrawdownUnit == store.ProtectionUnitATR {
			anyATR = true
			break
		}
	}
	if !anyATR {
		return rules
	}
	atr, ok := at.frozenATRForPosition(symbol, entryPrice, acfg)
	if !ok {
		return rules
	}
	out := make([]store.DrawdownTakeProfitRule, len(rules))
	copy(out, rules)
	for i := range out {
		if out[i].MinProfitUnit == store.ProtectionUnitATR && out[i].MinProfitPct > 0 {
			if pct, ok := acfg.EffectivePercent(out[i].MinProfitPct, atr, entryPrice); ok {
				out[i].MinProfitPct = pct
			}
		}
		if out[i].MaxDrawdownUnit == store.ProtectionUnitATR && out[i].MaxDrawdownPct > 0 {
			if pct, ok := acfg.EffectivePercent(out[i].MaxDrawdownPct, atr, entryPrice); ok {
				out[i].MaxDrawdownPct = pct
			}
		}
	}
	return out
}

// getActiveBreakEvenRulesATR returns the active break-even rules with each
// rule's trigger value resolved to a percent when its TriggerUnit is "atr", so
// the runtime BE monitor arms at the SAME distance the orders were placed with
// at open (no percent/ATR mismatch). Percent-unit rules pass through unchanged.
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
	// Skip the ATR fetch unless at least one rule uses ATR units.
	anyATR := false
	for _, r := range rules {
		if r.TriggerUnit == store.ProtectionUnitATR {
			anyATR = true
			break
		}
	}
	if !anyATR {
		return rules
	}
	atr, ok := at.frozenATRForPosition(symbol, entryPrice, acfg)
	if !ok {
		return rules
	}
	out := make([]store.BreakEvenStopRule, len(rules))
	copy(out, rules)
	for i := range out {
		if out[i].TriggerUnit == store.ProtectionUnitATR &&
			out[i].TriggerMode == store.BreakEvenTriggerProfitPct && out[i].TriggerValue > 0 {
			if pct, ok := acfg.EffectivePercent(out[i].TriggerValue, atr, entryPrice); ok {
				out[i].TriggerValue = pct
			}
		}
		// OffsetPct shares the rule's TriggerUnit: when the trigger is in ATR units
		// so is the offset (an ATR multiple of entry). Sign is preserved so a
		// negative offset (park the stop slightly losing-side) stays negative.
		if out[i].TriggerUnit == store.ProtectionUnitATR && out[i].OffsetPct != 0 {
			if pct, ok := atrOffsetEffectivePercent(acfg, out[i].OffsetPct, atr, entryPrice); ok {
				out[i].OffsetPct = pct
			}
		}
	}
	return out
}

// atrOffsetEffectivePercent resolves a signed ATR-multiple offset to an effective
// percent. EffectivePercent only accepts a positive multiple (and clamps to the
// configured min/max effective percent), so the sign is stripped, the magnitude
// resolved, and the sign reattached — preserving negative offsets (which park a
// break-even stop slightly on the losing side: cover fees + a noise buffer).
func atrOffsetEffectivePercent(acfg store.ATRProtectionConfig, atrMultiple, atrValue, entryPrice float64) (float64, bool) {
	sign := 1.0
	mag := atrMultiple
	if mag < 0 {
		sign = -1.0
		mag = -mag
	}
	pct, ok := acfg.EffectivePercent(mag, atrValue, entryPrice)
	if !ok {
		return 0, false
	}
	return sign * pct, true
}

