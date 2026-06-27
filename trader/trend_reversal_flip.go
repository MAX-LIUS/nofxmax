package trader

import (
	"strings"
	"time"

	"nofx/kernel"
	"nofx/logger"
	"nofx/market"
	"nofx/store"
)

// flipDecision is the outcome of evaluating whether an opposite-direction AI
// signal on an already-held symbol should flip the position (close + reverse)
// rather than be rejected as a same-symbol entry.
type flipDecision struct {
	ShouldFlip   bool
	Reason       string  // human-readable why (for logs / attribution)
	ExistingSide string  // "long" or "short" — the side being closed
	Quantity     float64 // absolute size of the existing position
	AgeHours     float64 // current hold age
}

// resolvedFlipConfig is the effective per-trader flip configuration after merging
// the fleet default with any strategy override. Distinct from the JSON
// store.TrendReversalConfig (which carries only override markers).
type resolvedFlipConfig struct {
	Enabled             bool
	DryRun              bool
	MinConfidence       int
	MinPositionAgeHours float64
}

// fleet-wide defaults applied to EVERY trader (existing and newly created).
const (
	flipDefaultEnabled = true
	// LIVE: fleet-wide real flip execution. A strategy may still hard-disable per
	// trader via protection.trend_reversal.disabled=true. The single-flip-per-entry
	// architecture is inherently anti-whipsaw (a flipped position is not re-flipped).
	flipDefaultDryRun     = false
	flipDefaultMinConf    = 75
	flipDefaultMinAgeHour = 6.0
)

// trendReversalConfig returns the effective config. The fleet default applies to
// all traders unconditionally (enabled + LIVE); a strategy may override per
// trader: Disabled turns the feature off; ForceDryRun pins it to observe-only;
// MinConfidence / MinPositionAgeHours override thresholds.
func (at *AutoTrader) trendReversalConfig() resolvedFlipConfig {
	cfg := resolvedFlipConfig{
		Enabled:             flipDefaultEnabled,
		DryRun:              flipDefaultDryRun,
		MinConfidence:       flipDefaultMinConf,
		MinPositionAgeHours: flipDefaultMinAgeHour,
	}
	if at.config.StrategyConfig == nil {
		return cfg
	}
	o := at.config.StrategyConfig.Protection.TrendReversal
	if o.MinConfidence > 0 {
		cfg.MinConfidence = o.MinConfidence
	}
	if o.MinPositionAgeHours > 0 {
		cfg.MinPositionAgeHours = o.MinPositionAgeHours
	}
	if o.LiveExecution {
		cfg.DryRun = false
	}
	if o.ForceDryRun {
		cfg.DryRun = true // quarantine this trader to observe-only
	}
	if o.Disabled {
		cfg.Enabled = false
	}
	return cfg
}

// evaluateFlip decides whether an incoming opposite-direction decision should
// flip the existing same-symbol position. It implements the minimal, backtested
// rule set: feature enabled + AI confidence >= MinConfidence + position aged >=
// MinPositionAgeHours. The breadth/exhaustion gates from the original design were
// dropped (backtest showed them harmful or inert on real entries).
//
// newSide is the direction the AI wants to open ("long" or "short"); existingPos
// is the opposing open position on the same symbol from GetPositions().
func (at *AutoTrader) evaluateFlip(decision *kernel.Decision, newSide string, existingPos map[string]interface{}) flipDecision {
	cfg := at.trendReversalConfig()
	res := flipDecision{}
	if !cfg.Enabled {
		return res
	}

	existingSide, _ := existingPos["side"].(string)
	existingSide = strings.ToLower(existingSide)
	res.ExistingSide = existingSide

	// Must be a genuine reversal: incoming side opposite the held side.
	if existingSide == strings.ToLower(newSide) {
		return res
	}

	// Rule 1: AI conviction floor.
	minConf := cfg.MinConfidence
	if minConf <= 0 {
		minConf = 75
	}
	if decision.Confidence < minConf {
		res.Reason = "confidence below flip floor"
		return res
	}

	// Rule 3: minimum hold age.
	minAgeHours := cfg.MinPositionAgeHours
	if minAgeHours <= 0 {
		minAgeHours = 6
	}
	ageMinutes := at.positionHoldMinutes(decision.Symbol, existingSide)
	res.AgeHours = ageMinutes / 60.0
	if res.AgeHours < minAgeHours {
		res.Reason = "position too young to flip"
		return res
	}

	// Resolve the absolute quantity to close.
	qty, _ := existingPos["positionAmt"].(float64)
	if qty < 0 {
		qty = -qty
	}
	if qty <= 0 {
		res.Reason = "no resolvable quantity"
		return res
	}
	res.Quantity = qty

	res.ShouldFlip = true
	res.Reason = "AI high-conviction reversal on aged position"
	return res
}

// FLIP_EXECUTION_PLACEHOLDER

// executeFlipClose closes the existing opposing position so the caller can then
// open the reverse direction. Returns (executed, error): executed=false in
// DryRun mode (intent logged, no order placed) so the caller leaves the original
// position intact and skips the reverse open.
//
// The close is tagged with the canonical trend_reversal_flip mechanism so the
// attribution system records it distinctly from AI/protection/manual closes.
func (at *AutoTrader) executeFlipClose(decision *kernel.Decision, newSide string, fd flipDecision) (bool, error) {
	cfg := at.trendReversalConfig()
	closeReason := store.MechTrendReversal

	logger.Infof("🔄 Trend-reversal flip [%s] %s: close %s (age %.1fh, conf %d) → open %s — %s",
		at.id[:min8(len(at.id))], decision.Symbol, fd.ExistingSide, fd.AgeHours,
		decision.Confidence, newSide, fd.Reason)

	if cfg.DryRun {
		logger.Infof("  🧪 dry_run: would close %s %.6f then open %s (no order placed)",
			fd.ExistingSide, fd.Quantity, newSide)
		at.recordFlipObservation(decision, newSide, fd, false)
		return false, nil
	}

	if err := at.closePositionByReason(decision.Symbol, fd.ExistingSide, fd.Quantity, closeReason); err != nil {
		return false, err
	}
	at.recordFlipObservation(decision, newSide, fd, true)
	logger.Infof("  ✅ flip close done; proceeding to open %s %s", newSide, decision.Symbol)
	return true, nil
}

// recordFlipObservation persists the flip decision (dry_run or live) to the
// flip_observations table so the dashboard can review live AI reversal-signal
// quality. Best-effort: logs on failure, never blocks the trade path.
func (at *AutoTrader) recordFlipObservation(decision *kernel.Decision, newSide string, fd flipDecision, executed bool) {
	mode := "live"
	if !executed {
		mode = "dry_run"
	}
	logger.Infof("  📋 flip-audit symbol=%s from=%s to=%s conf=%d age=%.1fh qty=%.6f mode=%s",
		market.Normalize(decision.Symbol), fd.ExistingSide, newSide,
		decision.Confidence, fd.AgeHours, fd.Quantity, mode)

	if at.store == nil {
		return
	}
	nowMs := time.Now().UnixMilli()
	obs := &store.FlipObservation{
		TraderID:      at.id,
		ExchangeID:    at.exchangeID,
		Symbol:        market.Normalize(decision.Symbol),
		FromSide:      fd.ExistingSide,
		ToSide:        strings.ToLower(newSide),
		Confidence:    decision.Confidence,
		AgeHours:      fd.AgeHours,
		Quantity:      fd.Quantity,
		DecisionCycle: at.cycleNumber,
		Executed:      executed,
		Reasoning:     decision.Reasoning,
		ObservedAt:    nowMs,
		CreatedAt:     nowMs,
	}
	if err := at.store.FlipObservation().Record(obs); err != nil {
		logger.Warnf("  ⚠️ failed to persist flip observation: %v", err)
	}
}

// min8 caps a slice length at 8 for short id logging.
func min8(n int) int {
	if n < 8 {
		return n
	}
	return 8
}
