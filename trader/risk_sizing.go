package trader

import (
	"fmt"
	"math"

	"nofx/kernel"
	"nofx/store"
)

// riskBasedPositionSizeGuarded is the nil-safe entry point used by the order path,
// where StrategyConfig may be nil. Delegates to riskBasedPositionSize.
func riskBasedPositionSizeGuarded(cfg *store.StrategyConfig, decision *kernel.Decision, entryPrice, atr, equity float64) (float64, bool, string) {
	if cfg == nil {
		return 0, false, "no strategy config"
	}
	return riskBasedPositionSize(cfg, decision, entryPrice, atr, equity)
}

// effectiveStopDistancePct returns the WORST-CASE (widest) stop distance as a
// fraction of entry, considering BOTH the AI-declared stop AND the strategy's
// configured protection stop. Using the wider of the two guarantees the risk
// budget is never exceeded even when a manual/structural stop is placed farther
// than the AI's nominal invalidation (the SPCX/SKHYNIX over-loss pattern).
//
//   - AI stop distance    = |entry - decision.StopLoss| / entry
//   - config stop distance = the protection stack's stop distance:
//     · when structural close-confirm is on → BackstopATRMul × ATR (the resting net)
//     · else per ladder SL rule: ATR/structural unit → pct×ATR; percent unit → pct
//     (take the widest SL rule)
//
// The config side is computed from ATR multiples only (not the live structural
// boundary), which is intentionally conservative: it never under-sizes.
func effectiveStopDistancePct(cfg *store.StrategyConfig, decision *kernel.Decision, entryPrice, atr float64) (float64, string) {
	aiPct := 0.0
	if decision.StopLoss > 0 && entryPrice > 0 {
		aiPct = math.Abs(entryPrice-decision.StopLoss) / entryPrice * 100.0
	}

	cfgPct := 0.0
	src := "ai"
	prot := cfg.Protection
	acfg := cfg.ATRProtection
	if atr > 0 && entryPrice > 0 && prot.LadderTPSL.Enabled {
		ss := prot.LadderTPSL.StructuralSL.WithDefaults()
		if prot.LadderTPSL.StructuralSL.Enabled && ss.CloseConfirm {
			// Resting safety net sits at the backstop; that is the true worst-case exit.
			if pct, ok := acfg.EffectivePercent(ss.BackstopATRMul, atr, entryPrice); ok {
				cfgPct = pct
			}
		} else {
			for _, r := range prot.LadderTPSL.Rules {
				if r.StopLossPct <= 0 {
					continue
				}
				var p float64
				var ok bool
				switch r.StopLossUnit {
				case store.ProtectionUnitATR, store.ProtectionUnitStructural:
					p, ok = acfg.EffectivePercent(r.StopLossPct, atr, entryPrice)
				default: // percent
					p, ok = r.StopLossPct, true
				}
				if ok && p > cfgPct {
					cfgPct = p
				}
			}
		}
	}

	eff := aiPct
	if cfgPct > eff {
		eff = cfgPct
		src = "config-stop"
	}
	return eff, src
}

// riskBasedPositionSize reverse-computes a position size from the EFFECTIVE
// (worst-case) stop distance so a single trade can lose at most
// RiskPerTradePctOfEquity % of equity.
//
//	riskSizeUSD = (equity × pct/100) / (effStopDistPct/100)
//
// A FAR stop → SMALL size; a TIGHT stop → LARGER size. The returned size is still
// subject to all downstream caps (position-value ratio, margin, min-size). When
// the reverse-computed size is below the executable floor, the downstream
// enforceMinPositionSize check rejects the open (skip, not force).
func riskBasedPositionSize(cfg *store.StrategyConfig, decision *kernel.Decision, entryPrice, atr, equity float64) (float64, bool, string) {
	rc := cfg.RiskControl
	if !rc.RiskSizingEnabled || rc.RiskPerTradePctOfEquity <= 0 {
		return 0, false, "risk-sizing disabled"
	}
	if equity <= 0 {
		return 0, false, "equity<=0"
	}
	if entryPrice <= 0 {
		return 0, false, "entry price<=0"
	}
	if decision.StopLoss <= 0 {
		return 0, false, "no stop_loss on decision — cannot risk-size"
	}
	effPct, src := effectiveStopDistancePct(cfg, decision, entryPrice, atr)
	// Guard against a degenerate (near-zero) stop distance that would explode size.
	if effPct < 0.05 { // 0.05% floor
		return 0, false, fmt.Sprintf("effective stop distance %.4f%% too small to risk-size", effPct)
	}
	riskBudget := equity * rc.RiskPerTradePctOfEquity / 100.0
	riskSize := riskBudget / (effPct / 100.0)
	return riskSize, true, fmt.Sprintf("risk %.2f%% of equity %.2f = %.2f budget / effStop %.3f%%(%s) → size %.2f USDT",
		rc.RiskPerTradePctOfEquity, equity, riskBudget, effPct, src, riskSize)
}
