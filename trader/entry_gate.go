package trader

import (
	"fmt"
	"math"
	"nofx/kernel"
	"nofx/market"
	"nofx/store"
	"strings"
)

// EntryGateStage identifies which stage rejected the entry.
type EntryGateStage string

const (
	EntryGateStageMarketState    EntryGateStage = "market_state"
	EntryGateStageStructuralFit  EntryGateStage = "structural_fit"
	EntryGateStageConfidenceRisk EntryGateStage = "confidence_risk"
)

// EntryGateCheck is one specific check within a stage.
type EntryGateCheck struct {
	Code     string `json:"code"`
	Stage    string `json:"stage"`
	Passed   bool   `json:"passed"`
	Detail   string `json:"detail"`
	Values   string `json:"values,omitempty"`
	Enforced bool   `json:"enforced"`
}

// EntryGateResult is the consolidated outcome of the 3-stage gate pipeline.
type EntryGateResult struct {
	Allowed       bool             `json:"allowed"`
	Stage         EntryGateStage   `json:"rejected_stage,omitempty"`
	BlockedBy     string           `json:"blocked_by,omitempty"`
	BlockReason   string           `json:"block_reason,omitempty"`
	Checks        []EntryGateCheck `json:"checks"`
	FailedCodes   []string         `json:"failed_codes,omitempty"`
	EnforcedCodes []string         `json:"enforced_codes,omitempty"`
	Regime        string           `json:"regime,omitempty"`
	SystemRegime  string           `json:"system_regime,omitempty"`
	ATR14Pct      float64          `json:"atr14_pct,omitempty"`
	FundingRate   float64          `json:"funding_rate,omitempty"`
	EffectiveRR   float64          `json:"effective_rr,omitempty"`
}

// entryGateInput collects all inputs needed for gate evaluation.
type entryGateInput struct {
	Decision        *kernel.Decision
	MarketData      *market.Data
	StrategyConfig  *store.StrategyConfig
	PolicyMode      store.StrategyControlPolicyMode
	MinRR           float64
	MinConfidence   int
	ConstraintSnap  *ExecutionConstraintsSnapshot
	ProtectionAlign *store.DecisionActionProtectionAlignment
}

// evaluateEntryGate runs the 3-stage entry gate pipeline.
// Stages are mutually exclusive: if Stage 1 blocks, Stages 2-3 are not evaluated.
// All checks within a stage DO run so the rejection shows the full picture.
func evaluateEntryGate(input entryGateInput) EntryGateResult {
	result := EntryGateResult{Allowed: true}
	if input.Decision == nil || !isOpenAction(input.Decision.Action) {
		return result
	}

	// ── Stage 1: Market State ──
	stage1Checks := evaluateMarketStateGate(input)
	result.Checks = append(result.Checks, stage1Checks...)
	if blocked, code, reason := firstEnforcedFailure(stage1Checks); blocked {
		result.Allowed = false
		result.Stage = EntryGateStageMarketState
		result.BlockedBy = code
		result.BlockReason = reason
		result.FailedCodes = failedCodes(stage1Checks)
		result.EnforcedCodes = enforcedFailedCodes(stage1Checks)
		return result
	}

	// ── Stage 2: Structural Fit ──
	stage2Checks := evaluateStructuralFitGate(input)
	result.Checks = append(result.Checks, stage2Checks...)
	if blocked, code, reason := firstEnforcedFailure(stage2Checks); blocked {
		result.Allowed = false
		result.Stage = EntryGateStageStructuralFit
		result.BlockedBy = code
		result.BlockReason = reason
		result.FailedCodes = failedCodes(result.Checks)
		result.EnforcedCodes = enforcedFailedCodes(result.Checks)
		return result
	}

	// ── Stage 3: Confidence & Risk ──
	stage3Checks := evaluateConfidenceRiskGate(input)
	result.Checks = append(result.Checks, stage3Checks...)
	if blocked, code, reason := firstEnforcedFailure(stage3Checks); blocked {
		result.Allowed = false
		result.Stage = EntryGateStageConfidenceRisk
		result.BlockedBy = code
		result.BlockReason = reason
		result.FailedCodes = failedCodes(result.Checks)
		result.EnforcedCodes = enforcedFailedCodes(result.Checks)
		return result
	}

	result.FailedCodes = failedCodes(result.Checks)
	result.EnforcedCodes = enforcedFailedCodes(result.Checks)
	return result
}

// ════════════════════════════════════════════════════════════════════════
// Stage 1: Market State — regime, funding, volatility, trend alignment
// ════════════════════════════════════════════════════════════════════════

func evaluateMarketStateGate(input entryGateInput) []EntryGateCheck {
	var checks []EntryGateCheck
	d := input.Decision
	data := input.MarketData

	// 1.0 Trigger type validation — must have a valid confirmed trigger
	triggerType := d.TriggerType
	validTriggers := map[string]bool{
		"support_rejection_confirmed":           true,
		"resistance_breakout_retest_successful": true,
		"higher_low_breakout_confirmed":         true,
		"resistance_rejection_confirmed":        true,
		"support_breakdown_retest_failed":       true,
		"lower_high_breakdown_confirmed":        true,
	}
	if triggerType == "" {
		checks = append(checks, EntryGateCheck{
			Code:     "trigger_type_missing",
			Stage:    string(EntryGateStageMarketState),
			Passed:   false,
			Enforced: true,
			Detail:   "trigger_type is required — must confirm candle-close at structural zone before entry",
		})
	} else if !validTriggers[triggerType] {
		// AI provided an invalid trigger_type — block
		checks = append(checks, EntryGateCheck{
			Code:     "trigger_type_invalid",
			Stage:    string(EntryGateStageMarketState),
			Passed:   false,
			Enforced: true,
			Detail:   fmt.Sprintf("trigger_type=%q is not a valid confirmed trigger", triggerType),
			Values:   fmt.Sprintf("trigger_type=%s", triggerType),
		})
	} else {
		checks = append(checks, EntryGateCheck{
			Code:     "trigger_type_valid",
			Stage:    string(EntryGateStageMarketState),
			Passed:   true,
			Enforced: true,
			Detail:   fmt.Sprintf("trigger_type=%q confirmed", triggerType),
			Values:   fmt.Sprintf("trigger_type=%s", triggerType),
		})
	}

	regimeCfg := getRegimeFilterConfig(input.StrategyConfig)
	if !regimeCfg.Enabled {
		return checks
	}

	regime := classifyProtectionRegime(data)

	// 1a. Regime allowed list
	if len(regimeCfg.AllowedRegimes) > 0 {
		allowed := isRegimeInAllowedList(regime, regimeCfg.AllowedRegimes)
		check := EntryGateCheck{
			Code:     "regime_not_allowed",
			Stage:    string(EntryGateStageMarketState),
			Passed:   allowed,
			Enforced: true,
		}
		if allowed {
			check.Detail = fmt.Sprintf("regime %s is in allowed list [%s]", regime, strings.Join(regimeCfg.AllowedRegimes, ","))
		} else {
			check.Detail = fmt.Sprintf("regime %s is NOT in allowed list [%s]", regime, strings.Join(regimeCfg.AllowedRegimes, ","))
			check.Values = fmt.Sprintf("current=%s allowed=[%s]", regime, strings.Join(regimeCfg.AllowedRegimes, ","))
		}
		checks = append(checks, check)
	}

	// 1b. Funding rate
	if regimeCfg.BlockHighFunding && regimeCfg.MaxFundingRateAbs > 0 && data != nil {
		absRate := math.Abs(data.FundingRate)
		passed := absRate <= regimeCfg.MaxFundingRateAbs
		check := EntryGateCheck{
			Code:     "funding_rate_too_high",
			Stage:    string(EntryGateStageMarketState),
			Passed:   passed,
			Enforced: true,
			Values:   fmt.Sprintf("funding=%.6f max=%.6f abs=%.6f", data.FundingRate, regimeCfg.MaxFundingRateAbs, absRate),
		}
		if passed {
			check.Detail = fmt.Sprintf("funding rate %.6f within limit %.6f", absRate, regimeCfg.MaxFundingRateAbs)
		} else {
			check.Detail = fmt.Sprintf("funding rate |%.6f| = %.6f exceeds max %.6f", data.FundingRate, absRate, regimeCfg.MaxFundingRateAbs)
		}
		checks = append(checks, check)
	}

	// 1c. ATR volatility
	atr14Pct := computeATR14Pct(data)
	if regimeCfg.BlockHighVolatility && regimeCfg.MaxATR14Pct > 0 {
		passed := atr14Pct <= regimeCfg.MaxATR14Pct
		check := EntryGateCheck{
			Code:     "volatility_too_high",
			Stage:    string(EntryGateStageMarketState),
			Passed:   passed,
			Enforced: true,
			Values:   fmt.Sprintf("atr14_pct=%.2f max=%.2f", atr14Pct, regimeCfg.MaxATR14Pct),
		}
		if passed {
			check.Detail = fmt.Sprintf("ATR14%%=%.2f within limit %.2f", atr14Pct, regimeCfg.MaxATR14Pct)
		} else {
			check.Detail = fmt.Sprintf("ATR14%%=%.2f exceeds max %.2f", atr14Pct, regimeCfg.MaxATR14Pct)
		}
		checks = append(checks, check)
	}

	// 1d. Trend alignment (direction vs regime)
	if regimeCfg.RequireTrendAlignment {
		aligned := isTrendAlignedWithMode(d.Action, d.SetupType, data, regimeCfg.TrendAlignmentMode)
		check := EntryGateCheck{
			Code:     "trend_misaligned",
			Stage:    string(EntryGateStageMarketState),
			Passed:   aligned,
			Enforced: true,
		}
		trendFactors := describeTrendFactors(d.Action, data)
		if aligned {
			check.Detail = fmt.Sprintf("action %s aligned with regime %s (%s)", d.Action, regime, trendFactors)
		} else {
			check.Detail = fmt.Sprintf("action %s opposes regime %s (%s)", d.Action, regime, trendFactors)
			if regimeCfg.TrendAlignmentMode == store.RegimeTrendAlignmentAllowRangeEdgeReversal {
				check.Detail += " — range_edge reversal exception not satisfied"
			}
		}
		check.Values = fmt.Sprintf("action=%s regime=%s setup=%s mode=%s", d.Action, regime, d.SetupType, regimeCfg.TrendAlignmentMode)
		checks = append(checks, check)
	}

	// 1d2. EMA20 direction consistency — price must be on the correct side of EMA20
	// for the trade direction. This is the single most important mid-term trend filter.
	if data != nil && data.CurrentEMA20 > 0 && data.CurrentPrice > 0 {
		deviation := (data.CurrentPrice - data.CurrentEMA20) / data.CurrentEMA20 * 100
		isLong := strings.Contains(strings.ToLower(d.Action), "long")
		isShort := strings.Contains(strings.ToLower(d.Action), "short")

		ema20Conflict := false
		// Long requires price >= EMA20 * 0.995 (allow 0.5% tolerance)
		if isLong && deviation < -0.5 {
			ema20Conflict = true
		}
		// Short requires price <= EMA20 * 1.005
		if isShort && deviation > 0.5 {
			ema20Conflict = true
		}

		// Exception: range_edge with small deviation allows mean-reversion
		if ema20Conflict && regime == string(market.RegimeLevelTrendingUp) || regime == string(market.RegimeLevelTrendingDown) {
			// In trending regimes, EMA20 conflict is always enforced
		} else if ema20Conflict {
			absDeviation := deviation
			if absDeviation < 0 {
				absDeviation = -absDeviation
			}
			// Allow mean-reversion in range_edge when deviation is small (<1%)
			regimeStr := market.InferExecutionRegimePublic(data)
			if regimeStr == "range_edge" && absDeviation < 1.0 {
				ema20Conflict = false
			}
		}

		if ema20Conflict {
			checks = append(checks, EntryGateCheck{
				Code:     "ema20_direction_conflict",
				Stage:    string(EntryGateStageMarketState),
				Passed:   false,
				Enforced: true,
				Detail:   fmt.Sprintf("EMA20方向冲突: %s但price偏离EMA20 %.2f%% (price=%.2f, EMA20=%.2f)", d.Action, deviation, data.CurrentPrice, data.CurrentEMA20),
				Values:   fmt.Sprintf("action=%s deviation=%.4f price=%.4f ema20=%.4f", d.Action, deviation, data.CurrentPrice, data.CurrentEMA20),
			})
		} else {
			checks = append(checks, EntryGateCheck{
				Code:     "ema20_direction_ok",
				Stage:    string(EntryGateStageMarketState),
				Passed:   true,
				Enforced: true,
				Detail:   fmt.Sprintf("EMA20方向一致: %s, price偏离EMA20 %.2f%%", d.Action, deviation),
				Values:   fmt.Sprintf("deviation=%.4f", deviation),
			})
		}
	}

	// 1d3. Trend phase extension/exhaustion gate — block entries in overextended trends
	if data != nil {
		trendPhase := market.ClassifyTrendPhase(data)
		isLong := strings.Contains(strings.ToLower(d.Action), "long")
		isShort := strings.Contains(strings.ToLower(d.Action), "short")

		switch trendPhase.Phase {
		case market.TrendPhaseExhaustion:
			checks = append(checks, EntryGateCheck{
				Code:     "trend_phase_exhaustion",
				Stage:    string(EntryGateStageMarketState),
				Passed:   false,
				Enforced: true,
				Detail:   fmt.Sprintf("趋势耗竭阶段禁止开仓 (4h=%.2f%%, EMA20偏离=%.2f%%, 动量衰减=%.2f)", data.PriceChange4h, trendPhase.ExtensionPct, trendPhase.MomentumDecay),
				Values:   fmt.Sprintf("phase=%s chg4h=%.4f extension=%.4f decay=%.4f", trendPhase.Phase, data.PriceChange4h, trendPhase.ExtensionPct, trendPhase.MomentumDecay),
			})
		case market.TrendPhaseExtension:
			// Block trend-following entries in extension phase
			isTrendFollowing := (isLong && data.PriceChange4h > 0) || (isShort && data.PriceChange4h < 0)
			if isTrendFollowing {
				checks = append(checks, EntryGateCheck{
					Code:     "trend_phase_extension",
					Stage:    string(EntryGateStageMarketState),
					Passed:   false,
					Enforced: true,
					Detail:   fmt.Sprintf("趋势延伸阶段禁止顺势开仓 (4h=%.2f%%, EMA20偏离=%.2f%%) — 等回调到EMA20附近", data.PriceChange4h, trendPhase.ExtensionPct),
					Values:   fmt.Sprintf("phase=%s chg4h=%.4f extension=%.4f action=%s", trendPhase.Phase, data.PriceChange4h, trendPhase.ExtensionPct, d.Action),
				})
			}
		default:
			checks = append(checks, EntryGateCheck{
				Code:     "trend_phase_ok",
				Stage:    string(EntryGateStageMarketState),
				Passed:   true,
				Enforced: true,
				Detail:   fmt.Sprintf("趋势阶段=%s, 可正常交易", trendPhase.Phase),
				Values:   fmt.Sprintf("phase=%s chg4h=%.4f extension=%.4f", trendPhase.Phase, data.PriceChange4h, trendPhase.ExtensionPct),
			})
		}
	}

	// 1d4. Coin-specific momentum gate — reject entries where the coin itself
	// shows no directional momentum (stale) or excessive momentum (late trend).
	if data != nil && regimeCfg.MomentumGateEnabled {
		primaryTF := ""
		if input.StrategyConfig != nil {
			primaryTF = input.StrategyConfig.Indicators.Klines.PrimaryTimeframe
		}
		checks = append(checks, evaluateCoinMomentumGate(d.Action, data, regimeCfg, primaryTF)...)
	}

	// 1e. Blocked regime (AI-reported chop/news_risk/no_trade)
	if d.Regime == "chop" || d.Regime == "news_risk" || d.Regime == "no_trade" {
		check := EntryGateCheck{
			Code:     "blocked_regime",
			Stage:    string(EntryGateStageMarketState),
			Passed:   false,
			Enforced: true,
			Detail:   fmt.Sprintf("AI reported regime=%s which is a blocked regime", d.Regime),
			Values:   fmt.Sprintf("ai_regime=%s", d.Regime),
		}
		checks = append(checks, check)
	}

	return checks
}

// ════════════════════════════════════════════════════════════════════════
// Stage 2: Structural Fit — setup type vs regime, cross-validation,
//          protection alignment, strong counter-trend
// ════════════════════════════════════════════════════════════════════════

func evaluateStructuralFitGate(input entryGateInput) []EntryGateCheck {
	var checks []EntryGateCheck
	d := input.Decision
	data := input.MarketData

	// 2a. Regime-structure gate: is the setup type valid for this regime?
	if data != nil {
		guidance := market.BuildRegimeEntryGuidance(
			data,
			market.BuildMarketStructureBrief(data),
			market.BuildDerivativesContext(data),
			data.QuantContext,
			market.BuildExchangeFlowContext(data),
		)
		if guidance != nil && guidance.Regime != "" {
			setup := strings.ToLower(strings.TrimSpace(d.SetupType))
			if setup == "" {
				setup = "none"
			}
			allowed := false
			for _, item := range guidance.AllowedSetups {
				if strings.EqualFold(item, setup) {
					allowed = true
					break
				}
			}
			check := EntryGateCheck{
				Code:     "regime_structure_mismatch",
				Stage:    string(EntryGateStageStructuralFit),
				Passed:   allowed,
				Enforced: true,
				Values:   fmt.Sprintf("setup=%s regime=%s allowed=[%s]", setup, guidance.Regime, strings.Join(guidance.AllowedSetups, ",")),
			}
			if allowed {
				check.Detail = fmt.Sprintf("setup %s compatible with regime %s (allowed: %s)", setup, guidance.Regime, strings.Join(guidance.AllowedSetups, ","))
			} else {
				check.Detail = fmt.Sprintf("setup %s incompatible with regime %s — only [%s] are allowed", setup, guidance.Regime, strings.Join(guidance.AllowedSetups, ","))
			}
			checks = append(checks, check)

			// 2b. Squeeze/crowded regime: extra confidence and RR requirements
			if guidance.Regime == "squeeze_risk" || guidance.Regime == "crowded" {
				gate := store.EntryGateConfig{}
				if input.StrategyConfig != nil {
					gate = input.StrategyConfig.EntryStructure.EntryGate.WithDefaults()
				}
				if d.Confidence > 0 {
					squeezeMinConf := gate.SqueezeMinConfidence
					confPassed := d.Confidence >= squeezeMinConf
					confCheck := EntryGateCheck{
						Code:     "squeeze_regime_low_confidence",
						Stage:    string(EntryGateStageStructuralFit),
						Passed:   confPassed,
						Enforced: true,
						Values:   fmt.Sprintf("confidence=%d min_required=%d regime=%s", d.Confidence, squeezeMinConf, guidance.Regime),
					}
					if confPassed {
						confCheck.Detail = fmt.Sprintf("confidence %d >= %d required for %s regime", d.Confidence, squeezeMinConf, guidance.Regime)
					} else {
						confCheck.Detail = fmt.Sprintf("confidence %d < %d required for %s regime", d.Confidence, squeezeMinConf, guidance.Regime)
					}
					checks = append(checks, confCheck)
				}
				if d.EntryProtection != nil && d.EntryProtection.RiskReward.NetEstimatedRR > 0 {
					netRR := d.EntryProtection.RiskReward.NetEstimatedRR
					squeezeMinRR := gate.SqueezeMinRR
					rrPassed := netRR >= squeezeMinRR
					rrCheck := EntryGateCheck{
						Code:     "squeeze_regime_low_rr",
						Stage:    string(EntryGateStageStructuralFit),
						Passed:   rrPassed,
						Enforced: true,
						Values:   fmt.Sprintf("net_rr=%.2f min_required=%.2f regime=%s", netRR, squeezeMinRR, guidance.Regime),
					}
					if rrPassed {
						rrCheck.Detail = fmt.Sprintf("net RR %.2f >= %.2f required for %s regime", netRR, squeezeMinRR, guidance.Regime)
					} else {
						rrCheck.Detail = fmt.Sprintf("net RR %.2f < %.2f required for %s regime", netRR, squeezeMinRR, guidance.Regime)
					}
					checks = append(checks, rrCheck)
				}
			}
		}
	}

	// 2c. AI regime cross-validation
	if d.Regime != "" && data != nil {
		systemRegime := market.InferExecutionRegimePublic(data)
		crossFailed := isRegimeCrossValidationFailed(d.Regime, systemRegime)
		exempt := false
		exemptReason := ""
		if crossFailed {
			// Exempt 1: trade direction aligns with the system-detected trend.
			// AI might mis-label regime as "range" but if it's opening long in a
			// trend_up market, the direction is correct — don't block.
			if isTrendAlignedWithRegime(d.Action, systemRegime) {
				exempt = true
				exemptReason = fmt.Sprintf("trade %s aligns with system trend %s", d.Action, systemRegime)
			}
		}
		passed := !crossFailed || exempt
		check := EntryGateCheck{
			Code:     "regime_cross_validation_failed",
			Stage:    string(EntryGateStageStructuralFit),
			Passed:   passed,
			Enforced: true,
			Values:   fmt.Sprintf("ai_regime=%s system_regime=%s action=%s", d.Regime, systemRegime, d.Action),
		}
		if passed {
			if exempt {
				check.Detail = fmt.Sprintf("AI says %s, system sees %s — exempted: %s", d.Regime, systemRegime, exemptReason)
			} else {
				check.Detail = fmt.Sprintf("AI regime %s consistent with system regime %s", d.Regime, systemRegime)
			}
		} else {
			check.Detail = fmt.Sprintf("AI claims regime=%s but system detects %s, and trade %s opposes trend — cross-validation failed", d.Regime, systemRegime, d.Action)
		}
		checks = append(checks, check)
	}

	// 2d. Protection target alignment
	if input.ProtectionAlign != nil {
		if input.ProtectionAlign.PolicyRejected {
			reasons := strings.Join(input.ProtectionAlign.PolicyReasons, ", ")
			check := EntryGateCheck{
				Code:     "protection_policy_rejected",
				Stage:    string(EntryGateStageStructuralFit),
				Passed:   false,
				Enforced: true,
				Detail:   fmt.Sprintf("protection plan rejected by structural alignment policy: %s", reasons),
				Values:   fmt.Sprintf("reasons=[%s]", reasons),
			}
			checks = append(checks, check)
		} else if !input.ProtectionAlign.TargetAligned {
			check := EntryGateCheck{
				Code:     "protection_target_before_first_target",
				Stage:    string(EntryGateStageStructuralFit),
				Passed:   false,
				Enforced: false,
				Detail:   fmt.Sprintf("configured TP target is before AI's first target — informational only, does not block"),
			}
			if d.EntryProtection != nil {
				rr := d.EntryProtection.RiskReward
				check.Values = fmt.Sprintf("entry=%.6f first_target=%.6f invalidation=%.6f", rr.Entry, rr.FirstTarget, rr.Invalidation)
			}
			checks = append(checks, check)
		}
	}

	// 2e. Range middle without edge setup
	if data != nil {
		regime := classifyProtectionRegime(data)
		if isRangeMiddleRegime(regime) && !isRangeEdgeSetup(d.SetupType) {
			check := EntryGateCheck{
				Code:     "range_middle_without_edge_setup",
				Stage:    string(EntryGateStageStructuralFit),
				Passed:   false,
				Enforced: false,
				Detail:   fmt.Sprintf("regime is %s (range) but setup=%s is not a range-edge type — prefer range_edge or breakout_retest setups", regime, d.SetupType),
				Values:   fmt.Sprintf("regime=%s setup=%s", regime, d.SetupType),
			}
			checks = append(checks, check)
		}
	}

	return checks
}

// ════════════════════════════════════════════════════════════════════════
// Stage 3: Confidence & Risk — min confidence, RR, short-specific gates
// ════════════════════════════════════════════════════════════════════════

func evaluateConfidenceRiskGate(input entryGateInput) []EntryGateCheck {
	var checks []EntryGateCheck
	d := input.Decision

	// 3a. Minimum confidence
	minConf := input.MinConfidence
	if minConf > 0 && d.Confidence > 0 {
		passed := d.Confidence >= minConf
		check := EntryGateCheck{
			Code:     "confidence_below_min",
			Stage:    string(EntryGateStageConfidenceRisk),
			Passed:   passed,
			Enforced: true,
			Values:   fmt.Sprintf("confidence=%d min=%d", d.Confidence, minConf),
		}
		if passed {
			check.Detail = fmt.Sprintf("confidence %d >= min %d", d.Confidence, minConf)
		} else {
			check.Detail = fmt.Sprintf("confidence %d < min %d", d.Confidence, minConf)
		}
		checks = append(checks, check)
	}

	// 3b. Runtime RR check
	minRR := input.MinRR
	if minRR <= 0 {
		if input.StrategyConfig != nil {
			minRR = input.StrategyConfig.EntryStructure.EntryGate.WithDefaults().FallbackMinRR
		} else {
			minRR = 1.5
		}
	}
	if d.EntryProtection != nil {
		rr := d.EntryProtection.RiskReward
		effectiveRR := rr.GrossEstimatedRR
		rrSource := "gross"
		if rr.NetEstimatedRR > 0 {
			effectiveRR = rr.NetEstimatedRR
			rrSource = "net"
		}
		if hasRuntimeRiskRewardExecutionConstraints(d.EntryProtection.ExecutionConstraints) {
			if recomputedGross, recomputedNet, ok := recomputeRuntimeRiskRewardWithExecutionConstraints(d.Action, rr, d.EntryProtection.ExecutionConstraints); ok {
				d.EntryProtection.RiskReward.GrossEstimatedRR = recomputedGross
				d.EntryProtection.RiskReward.NetEstimatedRR = recomputedNet
				effectiveRR = recomputedNet
				rrSource = "runtime_net"
				d.EntryProtection.RiskReward.Passed = effectiveRR+0.02 >= minRR
			}
		}

		if effectiveRR > 0 {
			passed := effectiveRR+0.02 >= minRR
			check := EntryGateCheck{
				Code:     "rr_below_min",
				Stage:    string(EntryGateStageConfidenceRisk),
				Passed:   passed,
				Enforced: true,
				Values:   fmt.Sprintf("effective_rr=%.2f min_rr=%.2f source=%s entry=%.6f sl=%.6f tp=%.6f", effectiveRR, minRR, rrSource, rr.Entry, rr.Invalidation, rr.FirstTarget),
			}
			if passed {
				check.Detail = fmt.Sprintf("effective RR %.2f (%s) >= min %.2f — entry=%.6f sl=%.6f tp=%.6f", effectiveRR, rrSource, minRR, rr.Entry, rr.Invalidation, rr.FirstTarget)
			} else {
				check.Detail = fmt.Sprintf("effective RR %.2f (%s) < min %.2f — entry=%.6f sl=%.6f tp=%.6f", effectiveRR, rrSource, minRR, rr.Entry, rr.Invalidation, rr.FirstTarget)
			}
			checks = append(checks, check)
		}
	}

	// 3c. Net RR minimum (shadow check)
	if d.EntryProtection != nil && d.EntryProtection.RiskReward.NetEstimatedRR > 0 {
		netRR := d.EntryProtection.RiskReward.NetEstimatedRR
		passed := netRR+0.02 >= minRR
		if !passed {
			check := EntryGateCheck{
				Code:     "net_rr_below_min",
				Stage:    string(EntryGateStageConfidenceRisk),
				Passed:   false,
				Enforced: false,
				Detail:   fmt.Sprintf("net RR %.2f < min %.2f after fees", netRR, minRR),
				Values:   fmt.Sprintf("net_rr=%.2f min_rr=%.2f", netRR, minRR),
			}
			checks = append(checks, check)
		}
	}

	// 3d. ATR-relative SL and reward distance (with volatility buffer)
	if d.EntryProtection != nil && input.StrategyConfig != nil {
		gate := input.StrategyConfig.EntryStructure.EntryGate
		gateDefaults := gate.WithDefaults()
		rr := d.EntryProtection.RiskReward
		atrPct := d.EntryProtection.VolatilityAdjustment.ATR14Pct
		if atrPct <= 0 && input.MarketData != nil {
			atrPct = computeATR14Pct(input.MarketData)
		}
		if atrPct > 0 && rr.Entry > 0 && rr.Invalidation > 0 && rr.FirstTarget > 0 {
			atrAbs := rr.Entry * (atrPct / 100)
			volBuffer := gateDefaults.VolatilityBufferATRMul

			slDist := math.Abs(rr.Entry - rr.Invalidation)
			slATRMul := slDist / atrAbs
			minSLMul := gate.MinSLDistanceATRMul
			if minSLMul <= 0 {
				minSLMul = 1.2
			}
			// Hard gate: only base threshold (reject garbage SL)
			slPassed := slATRMul >= minSLMul
			slCheck := EntryGateCheck{
				Code:     "sl_distance_below_atr_min",
				Stage:    string(EntryGateStageConfidenceRisk),
				Passed:   slPassed,
				Enforced: true,
				Values:   fmt.Sprintf("sl_atr_mul=%.2f min=%.1f sl_dist=%.4f atr14=%.4f atr_pct=%.2f%%", slATRMul, minSLMul, slDist, atrAbs, atrPct),
			}
			if slPassed {
				slCheck.Detail = fmt.Sprintf("SL distance %.2fx ATR14 >= min %.1fx — passes base gate (auto-widen to %.1fx at execution if needed)", slATRMul, minSLMul, minSLMul+volBuffer)
			} else {
				slCheck.Detail = fmt.Sprintf("SL distance %.2fx ATR14 < min %.1fx — structure too shallow, will be swept by normal noise", slATRMul, minSLMul)
			}
			checks = append(checks, slCheck)

			// Informational: vol buffer check (not enforced, auto-widen handles it)
			if volBuffer > 0 && slPassed {
				effectiveMinSL := minSLMul + volBuffer
				volPassed := slATRMul >= effectiveMinSL
				volCheck := EntryGateCheck{
					Code:     "sl_distance_below_vol_buffer",
					Stage:    string(EntryGateStageConfidenceRisk),
					Passed:   volPassed,
					Enforced: false,
					Values:   fmt.Sprintf("sl_atr_mul=%.2f effective_min=%.1f (base+buf)", slATRMul, effectiveMinSL),
				}
				if volPassed {
					volCheck.Detail = fmt.Sprintf("SL distance %.2fx ATR14 >= %.1fx (incl vol buffer) — no auto-widening needed", slATRMul, effectiveMinSL)
				} else {
					volCheck.Detail = fmt.Sprintf("SL distance %.2fx ATR14 < %.1fx — ladder SL will be auto-widened at execution", slATRMul, effectiveMinSL)
				}
				checks = append(checks, volCheck)
			}

			// SL distance percentage cap — reject entries where SL is too far from entry
			// A wide SL means the structure is too loose for the position size, or
			// the coin has already moved too far and "support" is unreliable.
			if rr.Entry > 0 {
				slPctDist := slDist / rr.Entry * 100
				maxSLPct := 2.0
				if input.StrategyConfig != nil {
					rfCfg := getRegimeFilterConfig(input.StrategyConfig)
					if rfCfg.MaxSLDistancePct > 0 {
						maxSLPct = rfCfg.MaxSLDistancePct
					}
				}
				slPctPassed := slPctDist <= maxSLPct
				slPctCheck := EntryGateCheck{
					Code:     "sl_distance_pct_too_wide",
					Stage:    string(EntryGateStageConfidenceRisk),
					Passed:   slPctPassed,
					Enforced: true,
					Values:   fmt.Sprintf("sl_pct=%.2f%% max=%.1f%% entry=%.4f invalidation=%.4f", slPctDist, maxSLPct, rr.Entry, rr.Invalidation),
				}
				if slPctPassed {
					slPctCheck.Detail = fmt.Sprintf("SL distance %.2f%% within %.1f%% cap — risk per trade acceptable", slPctDist, maxSLPct)
				} else {
					slPctCheck.Detail = fmt.Sprintf("SL distance %.2f%% exceeds %.1f%% cap — structure too loose, invalidation too far from entry", slPctDist, maxSLPct)
				}
				checks = append(checks, slPctCheck)
			}

			rewardDist := math.Abs(rr.FirstTarget - rr.Entry)
			rewardATRMul := rewardDist / atrAbs
			minRewardMul := gate.MinRewardATRMul
			if minRewardMul <= 0 {
				minRewardMul = 1.8
			}
			rewardPassed := rewardATRMul >= minRewardMul
			rewardCheck := EntryGateCheck{
				Code:     "reward_distance_below_atr_min",
				Stage:    string(EntryGateStageConfidenceRisk),
				Passed:   rewardPassed,
				Enforced: true,
				Values:   fmt.Sprintf("reward_atr_mul=%.2f min=%.1f reward_dist=%.4f atr14=%.4f", rewardATRMul, minRewardMul, rewardDist, atrAbs),
			}
			if rewardPassed {
				rewardCheck.Detail = fmt.Sprintf("target distance %.2fx ATR14 >= min %.1fx — target beyond noise range", rewardATRMul, minRewardMul)
			} else {
				rewardCheck.Detail = fmt.Sprintf("target distance %.2fx ATR14 < min %.1fx — target within noise, find higher-TF structural target", rewardATRMul, minRewardMul)
			}
			checks = append(checks, rewardCheck)
		}
	}

	// 3d-fallback. Minimum SL percentage (hard floor when ATR is unavailable or as belt-and-suspenders)
	if d.EntryProtection != nil {
		rr := d.EntryProtection.RiskReward
		if rr.Entry > 0 && rr.Invalidation > 0 {
			slDist := math.Abs(rr.Entry - rr.Invalidation)
			slPct := slDist / rr.Entry * 100
			minSLPct := 0.15 // absolute floor: 0.15% minimum SL distance
			passed := slPct >= minSLPct
			check := EntryGateCheck{
				Code:     "sl_pct_floor",
				Stage:    string(EntryGateStageConfidenceRisk),
				Passed:   passed,
				Enforced: true,
				Values:   fmt.Sprintf("sl_pct=%.3f%% min=%.2f%% entry=%.6f invalidation=%.6f", slPct, minSLPct, rr.Entry, rr.Invalidation),
			}
			if passed {
				check.Detail = fmt.Sprintf("SL distance %.3f%% >= %.2f%% floor", slPct, minSLPct)
			} else {
				check.Detail = fmt.Sprintf("⚠️ SL distance %.3f%% < %.2f%% floor — garbage SL, will be swept by any tick noise", slPct, minSLPct)
			}
			checks = append(checks, check)
		}
	}

	// 3d-diag. Data diagnostics — surface key data availability and anomalies
	if d.EntryProtection != nil {
		rr := d.EntryProtection.RiskReward
		atrPct := d.EntryProtection.VolatilityAdjustment.ATR14Pct
		if atrPct <= 0 && input.MarketData != nil {
			atrPct = computeATR14Pct(input.MarketData)
		}
		var diagParts []string
		if atrPct <= 0 {
			diagParts = append(diagParts, "⚠️ATR=MISSING")
		} else {
			diagParts = append(diagParts, fmt.Sprintf("ATR=%.2f%%", atrPct))
		}
		if rr.Entry > 0 && rr.Invalidation > 0 {
			slPct := math.Abs(rr.Entry-rr.Invalidation) / rr.Entry * 100
			diagParts = append(diagParts, fmt.Sprintf("SL=%.3f%%", slPct))
			if slPct < 0.3 {
				diagParts = append(diagParts, "⚠️SL_TIGHT")
			}
		}
		if rr.NetEstimatedRR > 10 {
			diagParts = append(diagParts, fmt.Sprintf("⚠️RR_SUSPICIOUS=%.1f", rr.NetEstimatedRR))
		}
		if input.MarketData != nil {
			if input.MarketData.CurrentPrice <= 0 {
				diagParts = append(diagParts, "⚠️PRICE=0")
			}
			if input.MarketData.TimeframeData == nil || len(input.MarketData.TimeframeData) == 0 {
				diagParts = append(diagParts, "⚠️NO_TF_DATA")
			}
		} else {
			diagParts = append(diagParts, "⚠️MARKET_DATA=NIL")
		}
		hasAnomaly := false
		for _, p := range diagParts {
			if strings.Contains(p, "⚠️") {
				hasAnomaly = true
				break
			}
		}
		diagCheck := EntryGateCheck{
			Code:     "data_diagnostics",
			Stage:    string(EntryGateStageConfidenceRisk),
			Passed:   !hasAnomaly,
			Enforced: false,
			Values:   strings.Join(diagParts, " | "),
		}
		if hasAnomaly {
			diagCheck.Detail = "⚠️ Data quality issues detected — review values"
		} else {
			diagCheck.Detail = "data inputs OK"
		}
		checks = append(checks, diagCheck)
	}

	// 3e. Short-specific: non-trending regime requires higher confidence
	if data := input.MarketData; data != nil {
		regime := classifyProtectionRegime(data)
		if isShortAction(d.Action) && !isTrendDownRegime(regime) {
			shortMinConf := 85
			if input.StrategyConfig != nil {
				cfg := input.StrategyConfig.EntryStructure.EntryGate.WithDefaults()
				shortMinConf = cfg.ShortNonDowntrendMinConfidence
			}
			if d.Confidence > 0 {
				passed := d.Confidence >= shortMinConf
				check := EntryGateCheck{
					Code:     "short_confidence_below_regime_min",
					Stage:    string(EntryGateStageConfidenceRisk),
					Passed:   passed,
					Enforced: true,
					Values:   fmt.Sprintf("confidence=%d min_for_short_non_trending=%d regime=%s", d.Confidence, shortMinConf, regime),
				}
				if passed {
					check.Detail = fmt.Sprintf("SHORT confidence %d >= %d (required when regime=%s is not trend_down)", d.Confidence, shortMinConf, regime)
				} else {
					check.Detail = fmt.Sprintf("SHORT confidence %d < %d (required when regime=%s is not trend_down)", d.Confidence, shortMinConf, regime)
				}
				checks = append(checks, check)
			}
		}
	}

	// 3f. Unsupported setup type (shadow)
	if d.SetupType != "" && d.SetupType != "trend_pullback" && d.SetupType != "range_edge" && d.SetupType != "breakout_retest" {
		check := EntryGateCheck{
			Code:     "unsupported_setup_type",
			Stage:    string(EntryGateStageConfidenceRisk),
			Passed:   false,
			Enforced: false,
			Detail:   fmt.Sprintf("setup_type=%s is not in recognized list [trend_pullback, range_edge, breakout_retest]", d.SetupType),
			Values:   fmt.Sprintf("setup=%s", d.SetupType),
		}
		checks = append(checks, check)
	}

	return checks
}

// ════════════════════════════════════════════════════════════════════════
// Helpers
// ════════════════════════════════════════════════════════════════════════

func firstEnforcedFailure(checks []EntryGateCheck) (blocked bool, code string, reason string) {
	for _, c := range checks {
		if !c.Passed && c.Enforced {
			return true, c.Code, c.Detail
		}
	}
	return false, "", ""
}

func failedCodes(checks []EntryGateCheck) []string {
	var codes []string
	for _, c := range checks {
		if !c.Passed {
			codes = append(codes, c.Code)
		}
	}
	return codes
}

func enforcedFailedCodes(checks []EntryGateCheck) []string {
	var codes []string
	for _, c := range checks {
		if !c.Passed && c.Enforced {
			codes = append(codes, c.Code)
		}
	}
	return codes
}

func getRegimeFilterConfig(cfg *store.StrategyConfig) store.RegimeFilterConfig {
	if cfg == nil {
		return store.RegimeFilterConfig{}
	}
	return cfg.Protection.RegimeFilter
}

// evaluateCoinMomentumGate checks whether the specific coin has sufficient
// directional momentum to justify entry. Prevents opening positions on coins
// that are flat (stale momentum) or have already moved too far (exhausted).
func evaluateCoinMomentumGate(action string, data *market.Data, cfg store.RegimeFilterConfig, primaryTimeframe string) []EntryGateCheck {
	if data == nil {
		return nil
	}

	// Thresholds from config, with sensible defaults
	staleChg1h := cfg.MomentumStaleChg1h
	if staleChg1h == 0 {
		staleChg1h = 0.15
	}
	staleChg4h := cfg.MomentumStaleChg4h
	if staleChg4h == 0 {
		staleChg4h = 0.4
	}
	exhaustedChg4h := cfg.MomentumExhaustedChg4h
	if exhaustedChg4h == 0 {
		exhaustedChg4h = 3.5
	}
	// Altcoins use a tighter exhausted threshold — they reverse faster after big moves
	sym := ""
	if data != nil {
		sym = strings.ToUpper(data.Symbol)
	}
	isMajor := strings.HasPrefix(sym, "BTC") || strings.HasPrefix(sym, "ETH")
	if !isMajor && exhaustedChg4h > 2.8 {
		exhaustedChg4h = 2.8
	}
	counterChg1h := cfg.MomentumCounterChg1h
	if counterChg1h == 0 {
		counterChg1h = 0.3
	}

	// When primary timeframe >= 1h, PriceChange1h covers too few bars for
	// reliable momentum assessment. Recompute from lower-TF klines and widen
	// thresholds proportionally. This makes the gate work correctly regardless
	// of which timeframe is configured as primary.
	chg1h := data.PriceChange1h
	chg4h := data.PriceChange4h
	tfMinutes := parseTimeframeMinutes(primaryTimeframe)
	if tfMinutes >= 60 {
		if betterChg1h := computeChg1hFromLowerTF(data); betterChg1h != 0 || chg1h == 0 {
			chg1h = betterChg1h
		}
		// Scale thresholds: the coarser the primary TF, the noisier the raw
		// PriceChange values. Factor = 15 / tfMinutes (baseline is 15m).
		scale := 15.0 / float64(tfMinutes) // 1h→0.25, 4h→0.0625
		if scale > 1 {
			scale = 1
		}
		staleChg1h *= scale + 0.25  // 1h: ×0.5, 4h: ×0.31
		staleChg4h *= scale + 0.25  // same
		counterChg1h *= 2 - scale   // 1h: ×1.75, 4h: ×1.94
		// Exhausted: larger TF means 4h change covers fewer bars, normal
		// trending moves look bigger. Widen proportionally.
		if isMajor {
			exhaustedChg4h = 4.5 + float64(tfMinutes)/30.0 // 1h→6.5, 4h→12.5
		} else {
			exhaustedChg4h = 3.5 + float64(tfMinutes)/40.0 // 1h→5.0, 4h→9.5
		}
	}
	absChg1h := chg1h
	if absChg1h < 0 {
		absChg1h = -absChg1h
	}
	absChg4h := chg4h
	if absChg4h < 0 {
		absChg4h = -absChg4h
	}

	isLong := strings.Contains(strings.ToLower(action), "long")
	isShort := strings.Contains(strings.ToLower(action), "short")

	var checks []EntryGateCheck

	// Check 1: Stale momentum
	// Both 1h AND 4h must be flat, OR 1h must be extremely flat (but only if 4h
	// also lacks meaningful direction).
	staleMomentum := absChg1h < staleChg1h && absChg4h < staleChg4h
	if !staleMomentum && absChg1h < staleChg1h*0.67 && absChg4h < staleChg4h*2.5 {
		staleMomentum = true
	}
	if staleMomentum {
		checks = append(checks, EntryGateCheck{
			Code:     "coin_momentum_stale",
			Stage:    string(EntryGateStageMarketState),
			Passed:   false,
			Enforced: true,
			Detail:   fmt.Sprintf("coin has no directional momentum (chg1h=%.2f%% chg4h=%.2f%%) — high stop-hunt risk in flat market", chg1h, chg4h),
			Values:   fmt.Sprintf("chg1h=%.4f chg4h=%.4f threshold_1h=%.2f threshold_4h=%.2f", chg1h, chg4h, staleChg1h, staleChg4h),
		})
		return checks
	}

	// Check 2: Exhausted momentum
	if absChg4h > exhaustedChg4h {
		checks = append(checks, EntryGateCheck{
			Code:     "coin_momentum_exhausted",
			Stage:    string(EntryGateStageMarketState),
			Passed:   false,
			Enforced: true,
			Detail:   fmt.Sprintf("coin momentum exhausted (chg4h=%.2f%%) — late trend entry, mean-reversion risk", chg4h),
			Values:   fmt.Sprintf("chg4h=%.4f threshold=%.2f", chg4h, exhaustedChg4h),
		})
		return checks
	}

	// Check 2b: Momentum fading — 4h move is large but 1h momentum is weak or reversing
	fadingThreshold := cfg.MomentumFadingChg4h
	if fadingThreshold == 0 {
		fadingThreshold = 2.0
	}
	if absChg4h > fadingThreshold {
		momentumRatio := absChg1h / absChg4h
		fading := false
		if momentumRatio < 0.2 {
			fading = true // 1h is <20% of 4h move — momentum has stalled
		}
		if isLong && chg4h > 2.0 && chg1h < 0 {
			fading = true // 4h up big but 1h turning negative — reversal starting
		}
		if isShort && chg4h < -2.0 && chg1h > 0 {
			fading = true // 4h down big but 1h turning positive — bounce starting
		}
		if fading {
			checks = append(checks, EntryGateCheck{
				Code:     "coin_momentum_fading",
				Stage:    string(EntryGateStageMarketState),
				Passed:   false,
				Enforced: true,
				Detail:   fmt.Sprintf("coin momentum fading (chg4h=%.2f%% but chg1h=%.2f%%) — big move already happened, chasing risk", chg4h, chg1h),
				Values:   fmt.Sprintf("chg1h=%.4f chg4h=%.4f ratio=%.2f", chg1h, chg4h, momentumRatio),
			})
			return checks
		}
	}

	// Check 3: Counter-momentum
	counterMomentum := false
	if isLong && chg1h < -counterChg1h {
		counterMomentum = true
	} else if isShort && chg1h > counterChg1h {
		counterMomentum = true
	}
	if counterMomentum {
		checks = append(checks, EntryGateCheck{
			Code:     "coin_momentum_counter",
			Stage:    string(EntryGateStageMarketState),
			Passed:   false,
			Enforced: true,
			Detail:   fmt.Sprintf("coin short-term momentum opposes %s (chg1h=%.2f%%) — fighting the tape", action, chg1h),
			Values:   fmt.Sprintf("action=%s chg1h=%.4f threshold=%.2f", action, chg1h, counterChg1h),
		})
		return checks
	}

	// Passed — momentum is in a healthy range
	phase := "early"
	if absChg4h > 2.0 {
		phase = "mid"
	}
	_ = isLong
	_ = isShort
	checks = append(checks, EntryGateCheck{
		Code:     "coin_momentum_ok",
		Stage:    string(EntryGateStageMarketState),
		Passed:   true,
		Enforced: true,
		Detail:   fmt.Sprintf("coin momentum healthy (%s phase, chg1h=%.2f%% chg4h=%.2f%%)", phase, chg1h, chg4h),
		Values:   fmt.Sprintf("chg1h=%.4f chg4h=%.4f phase=%s", chg1h, chg4h, phase),
	})
	return checks
}

// computeChg1hFromLowerTF calculates 1-hour price change from the best
// available lower-timeframe kline data in TimeframeData. This gives a more
// accurate momentum reading when the primary timeframe is >= 1h.
func computeChg1hFromLowerTF(data *market.Data) float64 {
	if data == nil || data.TimeframeData == nil {
		return 0
	}
	// Try progressively coarser timeframes; pick the finest available
	// that has enough bars to cover 1 hour.
	type candidate struct {
		tf       string
		barsFor1h int
	}
	candidates := []candidate{
		{"5m", 12},
		{"15m", 4},
		{"30m", 2},
		{"1h", 1},
	}
	for _, c := range candidates {
		sd, ok := data.TimeframeData[c.tf]
		if !ok || sd == nil || len(sd.Klines) < c.barsFor1h+1 {
			continue
		}
		klines := sd.Klines
		current := klines[len(klines)-1].Close
		idx := len(klines) - 1 - c.barsFor1h
		if idx < 0 {
			idx = 0
		}
		old := klines[idx].Close
		if old <= 0 || current <= 0 {
			continue
		}
		return (current - old) / old * 100
	}
	return 0
}

func parseTimeframeMinutes(tf string) int {
	switch tf {
	case "1m":
		return 1
	case "3m":
		return 3
	case "5m":
		return 5
	case "15m":
		return 15
	case "30m":
		return 30
	case "1h":
		return 60
	case "2h":
		return 120
	case "4h":
		return 240
	case "6h":
		return 360
	case "8h":
		return 480
	case "12h":
		return 720
	case "1d":
		return 1440
	default:
		return 15
	}
}

func computeATR14Pct(data *market.Data) float64 {
	if data == nil || data.CurrentPrice <= 0 {
		return 0
	}
	if data.IntradaySeries != nil && data.IntradaySeries.ATR14 > 0 {
		return data.IntradaySeries.ATR14 / data.CurrentPrice * 100
	}
	if data.LongerTermContext != nil && data.LongerTermContext.ATR14 > 0 {
		return data.LongerTermContext.ATR14 / data.CurrentPrice * 100
	}
	// Fallback: extract ATR14 from multi-timeframe data (GetWithTimeframesExchange path)
	if data.TimeframeData != nil {
		// Prefer smaller timeframes for tighter SL validation
		for _, tf := range []string{"1h", "15m", "4h", "30m", "5m", "1d"} {
			if sd, ok := data.TimeframeData[tf]; ok && sd != nil && sd.ATR14 > 0 {
				return sd.ATR14 / data.CurrentPrice * 100
			}
		}
		// If none of the preferred TFs matched, use any available
		for _, sd := range data.TimeframeData {
			if sd != nil && sd.ATR14 > 0 {
				return sd.ATR14 / data.CurrentPrice * 100
			}
		}
	}
	return 0
}

func isRegimeInAllowedList(regime string, allowed []string) bool {
	for _, item := range allowed {
		if strings.EqualFold(item, regime) {
			return true
		}
		if strings.EqualFold(item, "trending") &&
			(strings.EqualFold(regime, "trending_up") || strings.EqualFold(regime, "trending_down")) {
			return true
		}
	}
	return false
}

func isStrictMode(mode store.StrategyControlPolicyMode) bool {
	return effectiveRuntimePolicyMode(mode) == store.StrategyControlPolicyModeStrict
}

func describeTrendFactors(action string, data *market.Data) string {
	if data == nil {
		return "no data"
	}
	parts := []string{
		fmt.Sprintf("price_vs_ema20=%.6f/%.6f", data.CurrentPrice, data.CurrentEMA20),
		fmt.Sprintf("chg1h=%.2f%%", data.PriceChange1h),
		fmt.Sprintf("chg4h=%.2f%%", data.PriceChange4h),
		fmt.Sprintf("macd=%.6f", data.CurrentMACD),
	}
	score := classifyTrendDirection(data)
	parts = append(parts, fmt.Sprintf("trend_score=%d/4", score))
	return strings.Join(parts, " ")
}

// entryGateResultToBlockReason builds a human-readable block reason from the result.
func entryGateResultToBlockReason(result EntryGateResult) string {
	if result.Allowed {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[%s] %s", result.Stage, result.BlockReason))

	failedDetails := []string{}
	for _, c := range result.Checks {
		if !c.Passed && c.Enforced {
			entry := c.Code
			if c.Values != "" {
				entry += " {" + c.Values + "}"
			}
			failedDetails = append(failedDetails, entry)
		}
	}
	if len(failedDetails) > 1 {
		sb.WriteString(" | also failed: ")
		sb.WriteString(strings.Join(failedDetails[1:], ", "))
	}
	return sb.String()
}

// entryGateResultToQualityGate converts the new gate result to the legacy
// DecisionActionQualityGate struct for backward-compatible storage.
func entryGateResultToQualityGate(result EntryGateResult, d *kernel.Decision) *store.DecisionActionQualityGate {
	gate := &store.DecisionActionQualityGate{
		ShadowMode: false,
		Passed:     result.Allowed,
		Regime:     result.Regime,
	}
	if d != nil {
		gate.SetupType = d.SetupType
		gate.Confidence = d.Confidence
		if d.QualityScore != nil {
			gate.QualityTotal = d.QualityScore.Total
		}
		if d.EntryProtection != nil {
			gate.NetRR = d.EntryProtection.RiskReward.NetEstimatedRR
		}
	}
	gate.FailedChecks = result.FailedCodes
	if result.Allowed {
		gate.Decision = "passed"
	} else {
		gate.Decision = fmt.Sprintf("blocked_by_%s", result.Stage)
		gate.BlockedStage = string(result.Stage)
	}
	for _, c := range result.Checks {
		gate.GateChecks = append(gate.GateChecks, store.EntryGateCheckRecord{
			Code:     c.Code,
			Stage:    c.Stage,
			Passed:   c.Passed,
			Detail:   c.Detail,
			Values:   c.Values,
			Enforced: c.Enforced,
		})
	}
	return gate
}

// entryGateResultToControlOutcome converts the gate result into the legacy
// DecisionActionControlOutcome for backward-compatible UI display.
func entryGateResultToControlOutcome(result EntryGateResult, d *kernel.Decision) *store.DecisionActionControlOutcome {
	control := &store.DecisionActionControlOutcome{}
	if d == nil {
		return control
	}
	control.OriginalAction = d.Action
	control.FinalAction = d.Action

	if result.Allowed {
		control.Decision = "accepted"
	} else {
		control.Decision = "rejected"
		control.NoOrderPlaced = true
	}

	for _, c := range result.Checks {
		if !c.Passed {
			control.FailedChecks = append(control.FailedChecks, c.Code)
			control.Reasons = append(control.Reasons, c.Detail)
		}
	}

	control.RegimeCurrent = result.Regime
	control.RegimeStructureCurrent = result.SystemRegime
	control.RegimeATR14Pct = result.ATR14Pct
	control.RegimeFundingRate = result.FundingRate
	control.EffectiveRR = result.EffectiveRR

	if d.EntryProtection != nil {
		control.AIGrossRR = d.EntryProtection.RiskReward.GrossEstimatedRR
		control.AINetRR = d.EntryProtection.RiskReward.NetEstimatedRR
	}

	return control
}

// entryGateChecksLog builds a compact log summary of all checks for the execution log.
func entryGateChecksLog(result EntryGateResult) string {
	var parts []string
	for _, c := range result.Checks {
		status := "✓"
		if !c.Passed {
			if c.Enforced {
				status = "✗"
			} else {
				status = "⚠"
			}
		}
		parts = append(parts, fmt.Sprintf("%s %s", status, c.Code))
	}
	return strings.Join(parts, " | ")
}
