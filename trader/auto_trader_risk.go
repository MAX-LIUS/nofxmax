package trader

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"nofx/kernel"
	"nofx/logger"
	"nofx/market"
	"nofx/store"
	"sort"
	"strconv"
	"strings"
	"time"
)

// startDrawdownMonitor 启动运行态回撤监控协程。
// 这条链路属于持仓后的风险保护，和开仓前的 AI 决策风控不同，
// 它的职责是在仓位已存在时继续兜底处理利润回撤与异常退出。
const (
	minNativeDrawdownCallbackRatio = 0.003 // 0.3% price callback; OKX minimum 0.1% is too tight for strategy-level drawdown
)

type nativeDrawdownCallbackAdjustment struct {
	CallbackRatio float64
	Adjusted      bool
	Reason        string
}

type nativeDrawdownRejection struct {
	CallbackRatio float64
	SafetyFloor   float64
	Reason        string
}

func (e nativeDrawdownRejection) Error() string {
	return fmt.Sprintf("drawdown callback %.6f below native safety floor %.6f; use managed drawdown fallback instead of widening callback", e.CallbackRatio, e.SafetyFloor)
}

func adjustNativeDrawdownCallbackRatio(entryPrice float64, side string, rule store.DrawdownTakeProfitRule, callbackRatio float64) (nativeDrawdownCallbackAdjustment, error) {
	adjustment := nativeDrawdownCallbackAdjustment{CallbackRatio: callbackRatio}
	activationPrice := calculateProfitBasedTrailingTriggerPrice(entryPrice, side, rule.MinProfitPct)
	if entryPrice <= 0 || activationPrice <= 0 || callbackRatio <= 0 {
		return adjustment, fmt.Errorf("invalid drawdown callback inputs")
	}
	if callbackRatio >= minNativeDrawdownCallbackRatio {
		return adjustment, nil
	}
	// Clamp up to the minimum native callback rather than rejecting.
	// This ensures an exchange trailing order is always placed (visible to user),
	// with slightly wider drawdown tolerance than the AI plan specified.
	adjustment.CallbackRatio = minNativeDrawdownCallbackRatio
	adjustment.Adjusted = true
	adjustment.Reason = "clamped_to_native_floor"
	return adjustment, nil
}

func calculateImmediateTrailingCallback(entryPrice float64, side string, ladderSLOrders []ProtectionOrder) (float64, bool) {
	if entryPrice <= 0 || len(ladderSLOrders) == 0 {
		return 0, false
	}
	firstSL := ladderSLOrders[0].Price
	if firstSL <= 0 {
		return 0, false
	}
	callback := math.Abs(entryPrice-firstSL) / entryPrice
	if callback < minNativeDrawdownCallbackRatio {
		return 0, false
	}
	return callback, true
}

func (at *AutoTrader) startDrawdownMonitor() {
	at.monitorWg.Add(1)
	go func() {
		defer at.monitorWg.Done()

		interval := at.getDrawdownMonitorInterval()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		logger.Infof("📊 Started position drawdown monitoring (check every %s)", interval)

		for {
			select {
			case <-ticker.C:
				at.checkPositionDrawdown()
			case <-at.stopMonitorCh:
				logger.Info("⏹ Stopped position drawdown monitoring")
				return
			}
		}
	}()
}

func (at *AutoTrader) getDrawdownMonitorInterval() time.Duration {
	if at.config.StrategyConfig == nil {
		return time.Minute
	}

	drawdown := at.config.StrategyConfig.Protection.DrawdownTakeProfit
	if !drawdown.Enabled {
		return time.Minute
	}

	minSeconds := 60
	for _, rule := range drawdown.Rules {
		if rule.PollIntervalSeconds > 0 && rule.PollIntervalSeconds < minSeconds {
			minSeconds = rule.PollIntervalSeconds
		}
	}
	if minSeconds < 5 {
		minSeconds = 5
	}
	return time.Duration(minSeconds) * time.Second
}

// checkPositionDrawdown checks position drawdown situation
func (at *AutoTrader) checkPositionDrawdown() {
	// Portfolio giveback guard (L1 per-symbol + L2 portfolio circuit breaker).
	// Runs first because L2 is portfolio-level. No-op unless explicitly enabled.
	at.runGivebackGuard()

	// Structural stop-loss close-confirm layer (Phase 2). No-op unless the strategy
	// enables structural SL with close-confirm. Closes positions whose last CLOSED bar
	// breached the frozen pre-entry range boundary; the resting backstop stays as the
	// bot-downtime safety net.
	at.runStructuralSLGuard()

	// Get current positions
	positions, err := at.trader.GetPositions()
	if err != nil {
		logger.Infof("❌ Drawdown monitoring: failed to get positions: %v", err)
		return
	}

	drawdownCfg := store.DrawdownTakeProfitConfig{}
	if at.config.StrategyConfig != nil {
		drawdownCfg = at.config.StrategyConfig.Protection.DrawdownTakeProfit
	}

	for _, pos := range positions {
		symbol := pos["symbol"].(string)
		side := pos["side"].(string)
		entryPrice := pos["entryPrice"].(float64)
		markPrice := pos["markPrice"].(float64)
		quantity := pos["positionAmt"].(float64)
		if quantity < 0 {
			quantity = -quantity
		}
		var posCreatedTime int64
		if ct, ok := pos["createdTime"]; ok {
			if ctInt, ok2 := ct.(int64); ok2 {
				posCreatedTime = ctInt
			} else if ctFloat, ok3 := ct.(float64); ok3 {
				posCreatedTime = int64(ctFloat)
			}
		}

		// Time-stop (independent of drawdown rules): force-close a position held too long
		// that is still in loss. Cuts the slow-bleed "wrong direction held 20-37h" pattern
		// (fix 2026-06-12). Runs BEFORE the drawdown-rules gate so it works even when no
		// drawdown rules are configured. Winners are never touched (loss condition required).
		if at.maybeTimeStopClose(symbol, side, entryPrice, markPrice, quantity, posCreatedTime) {
			continue
		}

		// Max-hold stop: clears long-held non-runners (flat grinders, break-even/dust tails)
		// that the time-stop's loss condition never catches. Profitable runners are spared.
		if at.maybeMaxHoldClose(symbol, side, entryPrice, markPrice, quantity, posCreatedTime) {
			continue
		}

		// Config trailing take-profit. Done BEFORE the AI-drawdown-rules early-return so it
		// works even when no drawdown rules are configured. Gated on the feature flag so when
		// disabled this block is a complete no-op (zero behavior change vs. prior versions):
		// the peak cache is only touched here when trailing TP is actually enabled.
		if at.config.StrategyConfig != nil && at.config.StrategyConfig.RiskControl.TrailingTakeProfitEnabled {
			currentPnLPctEarly := calculatePositionPnLPct(side, entryPrice, markPrice)
			at.UpdatePeakPnL(symbol, side, currentPnLPctEarly)
			earlyPosKey := symbol + "_" + side
			at.peakPnLCacheMutex.RLock()
			earlyPeak := at.peakPnLCache[earlyPosKey]
			at.peakPnLCacheMutex.RUnlock()
			if at.maybeTrailingTPClose(symbol, side, entryPrice, markPrice, quantity, earlyPeak) {
				continue
			}
		}

		rules := at.getActiveDrawdownRulesForPosition(symbol, side)
		if len(rules) == 0 {
			continue
		}
		// Resolve ATR-unit min-profit / max-drawdown thresholds to percent so the
		// arm gate uses the same distance as the open-time placement (no mismatch).
		rules = at.resolveDrawdownRulesATR(rules, symbol, side, entryPrice)

		currentPnLPct := calculatePositionPnLPct(side, entryPrice, markPrice)

		// Legacy global peak tracking (kept for backward compat with native trailing)
		posKey := symbol + "_" + side
		at.peakPnLCacheMutex.RLock()
		peakPnLPct, exists := at.peakPnLCache[posKey]
		at.peakPnLCacheMutex.RUnlock()
		if !exists {
			peakPnLPct = currentPnLPct
			at.UpdatePeakPnL(symbol, side, currentPnLPct)
		} else {
			at.UpdatePeakPnL(symbol, side, currentPnLPct)
		}

		if fingerprintChanged := at.refreshDrawdownExecutionFingerprint(symbol, side, posCreatedTime); fingerprintChanged {
			logger.Infof("🟠 Drawdown monitor: %s %s position identity changed (new position), clearing previous execution guard and armed records", symbol, side)
			at.clearArmedDrawdownRecords(symbol, side)
		}

		// Break-even: apply exchange-side BE stops when profit thresholds are met.
		// ATR-aware: when ATR protection is on, BE triggers are ATR-scaled to match
		// the distances used at open (no percent/ATR mismatch).
		matchedBreakEvenRules := at.getActiveBreakEvenRulesATR(symbol, side, entryPrice)
		if len(matchedBreakEvenRules) > 0 {
			if at.isBreakEvenSuppressedByRunner(symbol, side) {
				logger.Infof("🟠 Break-even monitor: %s %s suppressed by runner semantics, skipping mechanical BE apply", symbol, side)
			} else {
				beQuantity := at.computeRemainingQuantityForBE(symbol, side)
				if beQuantity <= 0 {
					beQuantity = quantity
				}
				// Convert r_multiple rules to profit_pct equivalent
				resolvedRules := resolveBreakEvenRulesForPosition(matchedBreakEvenRules, entryPrice, symbol, side, at)
				if err := at.applyBreakEvenStops(symbol, side, beQuantity, entryPrice, currentPnLPct, resolvedRules); err != nil {
					logger.Infof("❌ Break-even stop apply failed (%s %s): %v", symbol, side, err)
				}
			}
		}

		// ===== Tier alloc state update (always runs, needed for high-water-mark tracking) =====
		// Initialize tier allocations if not set yet (e.g. position opened before this code deployed).
		// MUST happen before native trailing arm so getCumulativeCloseRatioByRule can find allocs.
		allocs := at.getDrawdownTierAllocs(symbol, side)
		if len(allocs) == 0 {
			at.initDrawdownTiersFromResolvedRules(symbol, side, quantity, rules)
			allocs = at.getDrawdownTierAllocs(symbol, side)
		}

		if len(allocs) > 0 {
			// Update tier states (tracking/superseded) based on current P&L — needed for
			// cumulative ratio calculation and high-water-mark tracking.
			// MUST run before native trailing arm so lower tiers are properly superseded.
			at.updateDrawdownTierStates(symbol, side, currentPnLPct, peakPnLPct)

			// Detect native trailing order fills: if position quantity decreased below
			// expected remaining, mark the highest tracking tier as executed.
			at.detectNativeTrailingFills(symbol, side, quantity)
		}

		// For exchange-native trailing protections, arm all tiers whose min-profit gate is already met.
		nativeTrailingHandled := false
		if at.supportsNativeTrailingStop() {
			executionMode := at.getDrawdownExecutionMode(symbol, side)
			armRules := at.getDrawdownArmRulesForNativeExposure(currentPnLPct, entryPrice, quantity, symbol, side, rules)
			if len(armRules) == 0 && isNativeTrailingProtectionState(at.getProtectionState(symbol, side)) {
				// Check if existing trailing orders need reactivation (placed with activePx
				// that is now past current price but OKX didn't auto-activate)
				if at.checkAndFixStaleTrailingActivation(symbol, side, entryPrice, markPrice, rules) {
					// Stale trailing order was cancelled, re-fetch arm rules
					armRules = at.getDrawdownArmRulesForNativeExposure(currentPnLPct, entryPrice, quantity, symbol, side, rules)
				} else {
					logger.Infof("🟣 Drawdown monitor: %s %s already has all satisfied native trailing tiers armed (%s), skipping duplicate arm pass", symbol, side, executionMode)
					nativeTrailingHandled = true
				}
			}
			if !nativeTrailingHandled {
				for _, armRule := range armRules {
					if at.applyNativeTrailingDrawdown(symbol, side, entryPrice, markPrice, armRule) {
						// Only mark as native-handled if the state is actually native trailing.
						// If applyNativeTrailingDrawdown fell back to managed mode (callback too small),
						// we must NOT skip the managed execution path below.
						if isNativeTrailingProtectionState(at.getProtectionState(symbol, side)) {
							nativeTrailingHandled = true
						}
					}
				}
			}
		}

		if len(allocs) > 0 {
			if nativeTrailingHandled {
				// Native trailing is handling exchange orders; skip managed market-close execution
				// but tier state has been updated above for high-water-mark tracking.
				continue
			}

			// Log tier status periodically
			if currentPnLPct > 0 {
				logTierAllocStatus(symbol, side, allocs)
			}

			// Evaluate tiers with independent peak tracking per tier
			triggered := at.evaluateDrawdownTiers(symbol, side, currentPnLPct, peakPnLPct)
			if triggered == nil {
				if currentPnLPct > 0 {
					logger.Infof("📊 Drawdown monitoring: %s %s | Profit: %.2f%% | Peak: %.2f%%",
						symbol, side, currentPnLPct, peakPnLPct)
				}
				continue
			}

			// Execute the triggered tier's partial close using its fixed quantity
			closeQty := triggered.Quantity
			if closeQty <= 0 {
				continue
			}

			// Fallback gate: the code side only SUPPLEMENTS the exchange side. If a
			// live trailing order already covers this tier on the exchange, suppress
			// the code-side close so the same tier never executes twice (one on the
			// exchange, one here). The code side fires only when the exchange side is
			// genuinely absent (no matching trailing order). The "armed/pending/
			// activated" status shown to the user reflects the EXCHANGE order, not
			// this monitor — so suppressing here keeps display and execution aligned.
			if matchingRule := findRuleForTier(rules, triggered); matchingRule != nil {
				if at.exchangeSideCoversDrawdownTier(symbol, side, normalizeDrawdownRule(*matchingRule), entryPrice) {
					// Exchange owns this tier; keep tier state as-is (do not mark
					// executed — the exchange fill detector handles that) and move on.
					continue
				}
			}

			logger.Infof("🚨 Drawdown %s triggered: %s %s | qty=%.6f | pnl=%.2f%% | tier_peak=%.2f%%",
				triggered.StageName, symbol, side, closeQty, currentPnLPct, triggered.PeakPnLPct)

			if err := at.closePositionByReason(symbol, side, closeQty, "managed_drawdown_"+triggered.StageName); err != nil {
				logger.Infof("❌ Drawdown %s close failed (%s %s): %v", triggered.StageName, symbol, side, err)
				// Revert tier status on failure
				at.updateTierAlloc(symbol, side, triggered.TierIndex, func(a *store.DrawdownTierAllocation) {
					a.Status = "tracking"
				})
				continue
			}

			logger.Infof("✅ Drawdown %s succeeded: %s %s | closed %.6f", triggered.StageName, symbol, side, closeQty)
			at.setProtectionState(symbol, side, "drawdown_triggered_"+triggered.StageName)

			// Set runner state from the original rule to preserve runner/BE suppression semantics
			matchingRule := findRuleForTier(rules, triggered)
			if matchingRule != nil {
				enforced := enforceDrawdownRunnerPolicy(drawdownCfg, normalizeDrawdownRule(*matchingRule))
				at.setDrawdownRunnerState(symbol, side, buildDrawdownRunnerState(enforced))
			}

			// Check if all tiers are done — if so, remaining position is handled by BE
			updatedAllocs := at.getDrawdownTierAllocs(symbol, side)
			if hasAllTiersCompleted(updatedAllocs) {
				logger.Infof("✅ All drawdown tiers completed for %s %s — remaining position protected by break-even", symbol, side)
			}

			continue
		}

		// Fallback: legacy path for positions without tier allocations.
		// Under unified trailing semantics MaxDrawdownPct is a PRICE retracement
		// from peak; PnL percentages are price-move percentages from entry, so
		// convert peak->current move into a price fraction of the peak price.
		var drawdownPct float64
		if peakPnLPct > currentPnLPct && (100+peakPnLPct) > 0 {
			drawdownPct = ((peakPnLPct - currentPnLPct) / (100 + peakPnLPct)) * 100
		}

		triggeredRules := at.getTriggeredDrawdownRules(currentPnLPct, drawdownPct, rules)
		if len(triggeredRules) > 0 {
			triggeredRules = []store.DrawdownTakeProfitRule{enforceDrawdownRunnerPolicy(drawdownCfg, normalizeDrawdownRule(triggeredRules[0]))}
		}
		if drawdownCfg.Enabled && drawdownCfg.Mode == store.ProtectionModeAI && drawdownCfg.EngineMode == store.DrawdownEngineModeAI {
			structureCtx := at.buildDrawdownStructureContext(symbol, side)
			if eval := evaluateAIDrawdownRule(drawdownCfg, currentPnLPct, peakPnLPct, drawdownPct, rules, structureCtx, side, markPrice); eval != nil {
				triggeredRules = []store.DrawdownTakeProfitRule{eval.Rule}
			} else {
				triggeredRules = nil
			}
		}
		if len(triggeredRules) == 0 {
			if currentPnLPct > 0 {
				logger.Infof("📊 Drawdown monitoring: %s %s | Profit: %.2f%% | Peak: %.2f%% | Drawdown: %.2f%%",
					symbol, side, currentPnLPct, peakPnLPct, drawdownPct)
			}
			continue
		}

		if at.supportsNativeTrailingStop() {
			for _, triggeredRule := range triggeredRules {
				at.applyNativeTrailingDrawdown(symbol, side, entryPrice, markPrice, triggeredRule)
			}
			if at.hasArmedNativeDrawdownForPosition(symbol, side, entryPrice) {
				logger.Infof("🟣 Drawdown monitor: %s %s native trailing drawdown is armed; skipping managed market close fallback", symbol, side)
				continue
			}
		}

		matchedRule := normalizeDrawdownRule(triggeredRules[0])
		ruleFingerprint := stableDrawdownRuleFingerprint(entryPrice, matchedRule)
		if at.getDrawdownExecutionFingerprint(symbol, side) == ruleFingerprint {
			logger.Infof("🟠 Drawdown monitor: %s %s rule already executed (fingerprint=%s), skipping duplicate close", symbol, side, ruleFingerprint)
			continue
		}
		closeQty := quantity * matchedRule.CloseRatioPct / 100.0
		if closeQty <= 0 || matchedRule.CloseRatioPct >= 99.999 {
			closeQty = 0
		}

		logger.Infof("🚨 Drawdown take-profit triggered: %s %s | Current profit: %.2f%% | Peak profit: %.2f%% | Drawdown: %.2f%% | CloseRatio: %.2f%%",
			symbol, side, currentPnLPct, peakPnLPct, drawdownPct, matchedRule.CloseRatioPct)

		if err := at.closePositionByReason(symbol, side, closeQty, "managed_drawdown"); err != nil {
			logger.Infof("❌ Drawdown take-profit failed (%s %s): %v", symbol, side, err)
			continue
		}

		logger.Infof("✅ Drawdown take-profit succeeded: %s %s", symbol, side)
		at.setDrawdownExecutionFingerprint(symbol, side, ruleFingerprint)
		at.setProtectionState(symbol, side, "drawdown_triggered")
		at.setDrawdownRunnerState(symbol, side, buildDrawdownRunnerState(matchedRule))
		if closeQty == 0 {
			at.ClearPeakPnLCache(symbol, side)
			at.clearBreakEvenState(symbol, side)
			at.clearDrawdownExecutionFingerprint(symbol, side)
		} else {
			at.UpdatePeakPnL(symbol, side, currentPnLPct)
		}
	}
}

func (at *AutoTrader) setAIDrawdownRules(symbol, side string, rules []store.DrawdownTakeProfitRule) {
	if len(rules) == 0 {
		return
	}
	key := positionKey(symbol, side)
	cloned := make([]store.DrawdownTakeProfitRule, 0, len(rules))
	for _, rule := range rules {
		rule = normalizeDrawdownRule(rule)
		if rule.MinProfitPct <= 0 || rule.MaxDrawdownPct <= 0 || rule.CloseRatioPct <= 0 {
			continue
		}
		cloned = append(cloned, rule)
	}
	if len(cloned) == 0 {
		return
	}
	// Stage 1 must lock meaningful profit: close_ratio ≥ 50%
	if cloned[0].CloseRatioPct < 50 {
		logger.Infof("  ⚙️ Drawdown tier 1 close_ratio clamped: %.0f%% → 50%% (minimum for stage 1)", cloned[0].CloseRatioPct)
		cloned[0].CloseRatioPct = 50
	}
	at.protectionStateMutex.Lock()
	if at.drawdownAIRules == nil {
		at.drawdownAIRules = make(map[string][]store.DrawdownTakeProfitRule)
	}
	at.drawdownAIRules[key] = cloned
	at.drawdownSource[key] = "ai_decision"
	at.protectionStateMutex.Unlock()
}

func (at *AutoTrader) restoreAIDrawdownRulesForPosition(symbol, side string) []store.DrawdownTakeProfitRule {
	return at.restoreAIDrawdownRulesForPositionWithEntry(symbol, side, 0)
}

func (at *AutoTrader) restoreAIDrawdownRulesForPositionWithEntry(symbol, side string, fallbackEntryPrice float64) []store.DrawdownTakeProfitRule {
	if at == nil || at.store == nil || symbol == "" || side == "" {
		return nil
	}
	pos, err := at.store.Position().GetOpenPositionBySymbol(at.id, symbol, strings.ToUpper(side))
	if err != nil || pos == nil {
		return nil
	}
	if pos.EntryDecisionCycle <= 0 {
		if inferred := at.store.Position().FindEntryDecisionCycleForPosition(at.id, symbol, strings.ToUpper(side), pos.EntryTime); inferred > 0 {
			pos.EntryDecisionCycle = inferred
			_ = at.store.Position().BackfillEntryDecisionCycle(pos.ID, inferred)
		}
	}
	if pos.EntryDecisionCycle <= 0 {
		return nil
	}
	record, err := at.store.Decision().GetRecordByCycle(at.id, pos.EntryDecisionCycle)
	if err != nil || record == nil {
		return nil
	}
	action := sideToOpenAction(side)
	entryPrice := pos.EntryPrice
	if entryPrice <= 0 {
		entryPrice = fallbackEntryPrice
	}
	if entryPrice <= 0 {
		entryPrice = recordActionPrice(record, symbol, action)
	}
	if entryPrice <= 0 {
		return nil
	}
	for _, payload := range []string{record.DecisionJSON, record.RawResponse} {
		if strings.TrimSpace(payload) == "" {
			continue
		}
		var decisions []kernel.Decision
		if err := json.Unmarshal([]byte(payload), &decisions); err != nil {
			continue
		}
		for i := range decisions {
			decision := decisions[i]
			if !strings.EqualFold(decision.Symbol, symbol) || !strings.EqualFold(decision.Action, action) || decision.ProtectionPlan == nil {
				continue
			}
			plan, err := buildAIProtectionPlan(entryPrice, decision.Action, decision.ProtectionPlan, at.config.StrategyConfig)
			if err != nil || plan == nil || len(plan.DrawdownRules) == 0 {
				continue
			}
			// Clamp DD tiers to RR target on restore (same logic as open-time)
			if decision.EntryProtection != nil && decision.EntryProtection.RiskReward.FirstTarget > 0 {
				structTargets := extractStructuralTargets(&decision, entryPrice, side)
				plan.DrawdownRules = clampDrawdownRulesToTarget(plan.DrawdownRules, entryPrice, decision.EntryProtection.RiskReward.FirstTarget, side, structTargets)
			}
			at.setAIDrawdownRules(symbol, side, plan.DrawdownRules)
			return plan.DrawdownRules
		}
	}
	return nil
}

func (at *AutoTrader) restoreAIProtectionPlanForPositionWithEntry(symbol, side string, fallbackEntryPrice float64) *ProtectionPlan {
	if at == nil || at.store == nil || symbol == "" || side == "" {
		return nil
	}
	entryDecisionCycle := 0
	entryPrice := fallbackEntryPrice
	pos, err := at.store.Position().GetOpenPositionBySymbol(at.id, symbol, strings.ToUpper(side))
	if err == nil && pos != nil {
		entryDecisionCycle = pos.EntryDecisionCycle
		if entryPrice <= 0 {
			entryPrice = pos.EntryPrice
		}
		if entryDecisionCycle <= 0 {
			if inferred := at.store.Position().FindEntryDecisionCycleForPosition(at.id, symbol, strings.ToUpper(side), pos.EntryTime); inferred > 0 {
				entryDecisionCycle = inferred
				_ = at.store.Position().BackfillEntryDecisionCycle(pos.ID, inferred)
			}
		}
	}
	// If local position state is stale/missing but exchange still reports an active
	// position, fall back to the latest matching open decision. This keeps mandatory
	// ladder SL repair alive even when position sync marked a live position CLOSED.
	if entryDecisionCycle <= 0 {
		entryDecisionCycle = at.store.Position().FindEntryDecisionCycleForPosition(at.id, symbol, strings.ToUpper(side), 0)
	}
	if entryDecisionCycle <= 0 {
		return nil
	}
	record, err := at.store.Decision().GetRecordByCycle(at.id, entryDecisionCycle)
	if err != nil || record == nil {
		return nil
	}
	action := sideToOpenAction(side)
	if entryPrice <= 0 {
		entryPrice = recordActionPrice(record, symbol, action)
	}
	if entryPrice <= 0 {
		return nil
	}
	for _, payload := range []string{record.DecisionJSON, record.RawResponse} {
		if strings.TrimSpace(payload) == "" {
			continue
		}
		var decisions []kernel.Decision
		if err := json.Unmarshal([]byte(payload), &decisions); err != nil {
			continue
		}
		for i := range decisions {
			decision := decisions[i]
			if !strings.EqualFold(decision.Symbol, symbol) || !strings.EqualFold(decision.Action, action) || decision.ProtectionPlan == nil {
				continue
			}
			decisionEntryPrice := 0.0
			if decision.EntryProtection != nil && decision.EntryProtection.RiskReward.Entry > 0 {
				decisionEntryPrice = decision.EntryProtection.RiskReward.Entry
			}
			if decisionEntryPrice > 0 && entryPrice > 0 {
				deviation := math.Abs(decisionEntryPrice-entryPrice) / entryPrice
				if deviation > 0.005 {
					logger.Warnf("⚠️ restoreAIProtectionPlan: cycle %d decision entry %.4f deviates %.2f%% from position entry %.4f; skipping stale plan",
						entryDecisionCycle, decisionEntryPrice, deviation*100, entryPrice)
					continue
				}
			}
			plan, err := buildAIProtectionPlan(entryPrice, decision.Action, decision.ProtectionPlan, at.config.StrategyConfig)
			if err != nil || plan == nil {
				continue
			}
			if len(plan.DrawdownRules) > 0 {
				// Clamp DD tiers to RR target on restore
				if decision.EntryProtection != nil && decision.EntryProtection.RiskReward.FirstTarget > 0 {
					structTargets := extractStructuralTargets(&decision, entryPrice, side)
					plan.DrawdownRules = clampDrawdownRulesToTarget(plan.DrawdownRules, entryPrice, decision.EntryProtection.RiskReward.FirstTarget, side, structTargets)
				}
				at.setAIDrawdownRules(symbol, side, plan.DrawdownRules)
			}
			return plan
		}
	}
	return nil
}

func recordActionPrice(record *store.DecisionRecord, symbol, action string) float64 {
	candidate := findMatchedDecisionAction(record, symbol, action)
	if candidate == nil {
		return 0
	}
	return candidate.Price
}

func (at *AutoTrader) getActiveDrawdownRules() []store.DrawdownTakeProfitRule {
	return at.getActiveDrawdownRulesForPosition("", "")
}

func (at *AutoTrader) getActiveDrawdownRulesForPosition(symbol, side string) []store.DrawdownTakeProfitRule {
	if at.config.StrategyConfig == nil {
		return nil
	}

	cfg := at.config.StrategyConfig.Protection.DrawdownTakeProfit
	if !cfg.Enabled {
		return nil
	}

	// Ladder-TP + DD-on-runner coexistence (2026-06-09, Plan B1): DD trails the runner
	// for EVERY position, not only after the ladder TP has fully filled. No runner-stage
	// gate — the runner DD arms whenever profit reaches the activation tier (+6%). Oversell
	// is prevented structurally by the DD close_ratio being the runner share (25% =
	// 100% - ladder TP cumulative 75%), sized off the ORIGINAL entry quantity in
	// applyNativeTrailingDrawdown (partialQty = originalQty * cumulativeRatio), so ladder
	// (75%) + DD runner (25%) = 100% with no overlap regardless of fill timing.

	if symbol != "" || side != "" {
		key := positionKey(symbol, side)
		at.protectionStateMutex.RLock()
		if rules := at.drawdownAIRules[key]; len(rules) > 0 {
			out := make([]store.DrawdownTakeProfitRule, 0, len(rules))
			for _, rule := range rules {
				out = append(out, normalizeDrawdownRule(rule))
			}
			at.protectionStateMutex.RUnlock()
			return out
		}
		at.protectionStateMutex.RUnlock()

		if cfg.Mode == store.ProtectionModeAI && at.store != nil {
			if restored := at.restoreAIDrawdownRulesForPositionWithEntry(symbol, side, 0); len(restored) > 0 {
				return restored
			}
			// AI restore failed — fall through to configured fallback rules
		}
	}

	if len(cfg.Rules) == 0 {
		return nil
	}

	rules := make([]store.DrawdownTakeProfitRule, 0, len(cfg.Rules))
	for _, rule := range cfg.Rules {
		if rule.MinProfitPct <= 0 || rule.MaxDrawdownPct <= 0 || rule.CloseRatioPct <= 0 {
			continue
		}
		if rule.CloseRatioPct > 100 {
			rule.CloseRatioPct = 100
		}
		rules = append(rules, normalizeDrawdownRule(rule))
	}
	return rules
}

func isNativeTrailingProtectionState(state string) bool {
	return state == "native_trailing_arming" || state == "native_trailing_armed" || state == "native_partial_trailing_arming" || state == "native_partial_trailing_armed"
}

func (at *AutoTrader) isManagedDrawdownRecord(symbol, side, fingerprint string) bool {
	if at.store == nil {
		return false
	}
	state, err := at.store.LoadDynamicProtectionState()
	if err != nil || state == nil {
		return false
	}
	for _, record := range state.Records {
		if record.TraderID != "" && record.TraderID != at.id {
			continue
		}
		if !strings.EqualFold(record.Symbol, symbol) || !strings.EqualFold(record.Side, side) {
			continue
		}
		if record.Status != "armed" {
			continue
		}
		if record.RuleFingerprint == fingerprint && record.ProtectionType == "managed_drawdown" {
			return true
		}
	}
	return false
}

func isDrawdownRuleSatisfied(currentPnLPct float64, rule store.DrawdownTakeProfitRule) bool {
	return currentPnLPct >= rule.MinProfitPct
}

// clearArmedDrawdownRecords removes all armed drawdown records for a symbol/side
// when the position entry fingerprint changes (new position opened after old one closed).
func (at *AutoTrader) clearArmedDrawdownRecords(symbol, side string) {
	if at.store == nil {
		return
	}
	state, err := at.store.LoadDynamicProtectionState()
	if err != nil || state == nil {
		return
	}
	changed := false
	for key, record := range state.Records {
		if record.TraderID != "" && record.TraderID != at.id {
			continue
		}
		if !strings.EqualFold(record.Symbol, symbol) || !strings.EqualFold(record.Side, side) {
			continue
		}
		if record.Status == "armed" && isDynamicNativeProtectionType(record.ProtectionType) {
			record.Status = "cleared_new_position"
			record.UpdatedAt = time.Now().UTC().UnixMilli()
			state.Records[key] = record
			changed = true
			logger.Infof("🟠 Cleared stale armed record for %s %s: type=%s fp=%s", symbol, side, record.ProtectionType, record.RuleFingerprint)
		}
	}
	if changed {
		data, _ := json.Marshal(state)
		_ = at.store.SetSystemConfig(store.DynamicProtectionStateConfigKey, string(data))
	}
}

// checkAndFixStaleTrailingActivation checks if existing trailing orders have an activePx
// that the current price has already passed, but OKX hasn't activated them (phantom
// protection). Plan A (2026-06-09): instead of re-placing without activePx (which would
// drop the required +6% activation anchor and loop forever), it cancels the phantom order
// and converts protection to a MANAGED drawdown monitor (fixed retained-profit close at
// the 40%-retracement price). Returns false so the caller does NOT re-arm native trailing.
func (at *AutoTrader) checkAndFixStaleTrailingActivation(symbol, side string, entryPrice, markPrice float64, rules []store.DrawdownTakeProfitRule) bool {
	if markPrice <= 0 {
		return false
	}
	openOrders, err := at.trader.GetOpenOrders(symbol)
	if err != nil {
		return false
	}
	positionSide := strings.ToUpper(side)
	for _, order := range openOrders {
		if !strings.Contains(strings.ToUpper(order.Type), "TRAILING") {
			continue
		}
		if order.PositionSide != "" && !strings.EqualFold(order.PositionSide, positionSide) {
			continue
		}
		if order.ActivationStatus == "activated" {
			continue
		}
		// Check if activation price has been passed
		activePx := order.ActivationPrice
		if activePx <= 0 {
			activePx = order.StopPrice
		}
		if activePx <= 0 {
			continue
		}
		pricePast := false
		if strings.EqualFold(side, "long") && markPrice >= activePx {
			pricePast = true
		} else if strings.EqualFold(side, "short") && markPrice <= activePx {
			pricePast = true
		}
		if !pricePast {
			continue
		}
		// Phantom trailing: activePx passed but OKX did not activate. Cancel it and
		// convert to a managed drawdown monitor (keeps the +6% activation semantics via
		// a fixed retained-profit close) instead of re-placing without activePx.
		logger.Infof("🔄 DD trailing phantom activation: %s %s | mark=%.4f past activePx=%.4f — converting to managed drawdown (preserving +6%% anchor)", symbol, side, markPrice, activePx)
		if cancelTrader, ok := at.trader.(interface {
			CancelAlgoOrderByID(symbol string, algoID string) error
		}); ok {
			if err := cancelTrader.CancelAlgoOrderByID(symbol, order.OrderID); err != nil {
				logger.Warnf("⚠️ Failed to cancel phantom trailing order %s: %v", order.OrderID, err)
				continue
			}
		}
		at.clearProtectionState(symbol, side)
		at.clearDrawdownTierAllocs(symbol, side)
		// Arm managed drawdown for the matching rule so the runner still has a
		// retracement exit without a native trailing order.
		if managedRule, ok := matchDrawdownRuleByActivation(rules, entryPrice, side, activePx); ok {
			activationPrice := calculateProfitBasedTrailingTriggerPrice(entryPrice, side, managedRule.MinProfitPct)
			callbackRatio := calculateDrawdownRuleCallbackRatio(entryPrice, side, managedRule)
			if at.applyManagedDrawdownFallback(symbol, side, entryPrice, managedRule, activationPrice, callbackRatio) {
				logger.Infof("🟣 DD converted to managed drawdown after phantom activation: %s %s", symbol, side)
			}
		}
		// Managed now owns the runner exit — do NOT re-arm native trailing.
		return false
	}
	return false
}

// matchDrawdownRuleByActivation finds the drawdown rule whose +minProfit activation
// price is CLOSEST to the given activePx (within tolerance). Falls back to the
// highest-minProfit rule when no rule's activation is within tolerance.
//
// Closest-match (not first-within-tolerance) matters because adjacent drawdown tiers
// can sit only ~1% apart, so a loose "first hit" would misattribute e.g. a 30%
// partial-lock tier to a neighbouring 100% tier and force a full close instead of the
// strategy's partial reduction. Minimising the relative distance always resolves to
// the exact tier the trailing order actually belonged to.
func matchDrawdownRuleByActivation(rules []store.DrawdownTakeProfitRule, entryPrice float64, side string, activePx float64) (store.DrawdownTakeProfitRule, bool) {
	var best store.DrawdownTakeProfitRule
	hasBest := false
	var matched store.DrawdownTakeProfitRule
	hasMatched := false
	bestRelDist := math.MaxFloat64
	for _, r := range rules {
		r = normalizeDrawdownRule(r)
		if r.MinProfitPct <= 0 || r.MaxDrawdownPct <= 0 || r.CloseRatioPct <= 0 {
			continue
		}
		ap := calculateProfitBasedTrailingTriggerPrice(entryPrice, side, r.MinProfitPct)
		if ap > 0 && activePx > 0 {
			relDist := math.Abs(ap-activePx) / activePx
			if relDist <= 0.01 && relDist < bestRelDist {
				bestRelDist = relDist
				matched = r
				hasMatched = true
			}
		}
		if !hasBest || r.MinProfitPct > best.MinProfitPct {
			best = r
			hasBest = true
		}
	}
	if hasMatched {
		return matched, true
	}
	return best, hasBest
}

func (at *AutoTrader) clearArmedDrawdownRecordsAboveMinProfit(symbol, side string, maxMinProfit float64) {
	if at.store == nil {
		return
	}
	state, err := at.store.LoadDynamicProtectionState()
	if err != nil || state == nil {
		return
	}
	changed := false
	for key, record := range state.Records {
		if record.TraderID != "" && record.TraderID != at.id {
			continue
		}
		if !strings.EqualFold(record.Symbol, symbol) || !strings.EqualFold(record.Side, side) {
			continue
		}
		if record.Status != "armed" || !isDynamicNativeProtectionType(record.ProtectionType) {
			continue
		}
		parts := strings.Split(record.RuleFingerprint, "|")
		if len(parts) >= 3 {
			if minProfit, err := strconv.ParseFloat(parts[2], 64); err == nil && minProfit > maxMinProfit {
				record.Status = "cleared_floor_downgrade"
				record.UpdatedAt = time.Now().UTC().UnixMilli()
				state.Records[key] = record
				changed = true
				logger.Infof("🔄 Cleared unreachable armed record: %s %s minProfit=%.2f%% (floor downgraded to %.2f%%)", symbol, side, minProfit, maxMinProfit)
			}
		}
	}
	if changed {
		data, _ := json.Marshal(state)
		_ = at.store.SetSystemConfig(store.DynamicProtectionStateConfigKey, string(data))
	}
}

func (at *AutoTrader) getArmedDrawdownRecords(symbol, side string) []store.DynamicProtectionRecord {
	return at.getArmedDrawdownRecordsForPosition(symbol, side, 0, 0, 0)
}

func (at *AutoTrader) getArmedDrawdownRecordsForPosition(symbol, side string, entryPrice, quantity float64, posCreatedTime int64) []store.DynamicProtectionRecord {
	if at.store == nil {
		return nil
	}
	state, err := at.store.LoadDynamicProtectionState()
	if err != nil || state == nil {
		return nil
	}
	currentFingerprint := positionFingerprint(entryPrice, quantity)
	currentEntryFingerprint := entryPositionFingerprint(entryPrice)
	records := make([]store.DynamicProtectionRecord, 0)
	var staleKeys []string
	for key, record := range state.Records {
		if record.TraderID != "" && record.TraderID != at.id {
			continue
		}
		if !strings.EqualFold(record.Symbol, symbol) || !strings.EqualFold(record.Side, side) {
			continue
		}
		if record.Status != "armed" || !isDynamicNativeProtectionType(record.ProtectionType) {
			continue
		}
		// Position identity check: prefer cTime (exact, never changes during partial close)
		if posCreatedTime > 0 && record.PositionCreatedTime > 0 {
			if record.PositionCreatedTime != posCreatedTime {
				logger.Infof("🟣 Drawdown record ignored (cTime mismatch): %s %s record_ctime=%d current_ctime=%d type=%s", symbol, side, record.PositionCreatedTime, posCreatedTime, record.ProtectionType)
				continue
			}
		} else if currentEntryFingerprint != "" && record.PositionFingerprint != "" {
			// Fallback for old records without cTime: use entry price tolerance
			if !entryPriceWithinTolerance(recordEntryFingerprint(record.PositionFingerprint), currentEntryFingerprint, 0.005) {
				logger.Infof("🟣 Drawdown record ignored (entry price mismatch): %s %s record_fp=%s current_fp=%s type=%s", symbol, side, record.PositionFingerprint, currentFingerprint, record.ProtectionType)
				continue
			}
		}
		// Ignore and mark stale records from a closed position (qty=0) when current position is open
		if quantity > 0 && record.PositionFingerprint != "" {
			parts := strings.Split(record.PositionFingerprint, "|")
			if len(parts) >= 2 {
				if recordQty, err := strconv.ParseFloat(parts[1], 64); err == nil && recordQty == 0 {
					logger.Infof("🟠 Drawdown record stale (closed position qty=0): %s %s record_fp=%s current_fp=%s — marking cleared", symbol, side, record.PositionFingerprint, currentFingerprint)
					staleKeys = append(staleKeys, key)
					continue
				}
			}
		}
		records = append(records, record)
	}
	// Proactively clear stale records so they don't accumulate
	if len(staleKeys) > 0 {
		for _, key := range staleKeys {
			r := state.Records[key]
			r.Status = "cleared_stale_qty0"
			r.UpdatedAt = time.Now().UTC().UnixMilli()
			state.Records[key] = r
		}
		if data, err := json.Marshal(state); err == nil {
			_ = at.store.SetSystemConfig(store.DynamicProtectionStateConfigKey, string(data))
		}
	}
	return records
}

func positionFingerprint(entryPrice, quantity float64) string {
	if entryPrice <= 0 || quantity <= 0 {
		return ""
	}
	return fmt.Sprintf("%.8f|%.8f", entryPrice, quantity)
}

func entryPositionFingerprint(entryPrice float64) string {
	if entryPrice <= 0 {
		return ""
	}
	return fmt.Sprintf("%.8f", entryPrice)
}

func recordEntryFingerprint(positionFingerprint string) string {
	parts := strings.Split(positionFingerprint, "|")
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func entryPriceWithinTolerance(recordFP, currentFP string, tolerance float64) bool {
	recordPrice, err1 := strconv.ParseFloat(recordFP, 64)
	currentPrice, err2 := strconv.ParseFloat(currentFP, 64)
	if err1 != nil || err2 != nil || recordPrice <= 0 || currentPrice <= 0 {
		return recordFP == currentFP
	}
	return math.Abs(recordPrice-currentPrice)/recordPrice <= tolerance
}

func (at *AutoTrader) hasArmedNativeDrawdownForPosition(symbol, side string, entryPrice float64) bool {
	return len(at.getArmedDrawdownRecordsForPosition(symbol, side, entryPrice, 0, 0)) > 0
}

func (at *AutoTrader) getArmedDrawdownRuleFingerprints(symbol, side string) map[string]struct{} {
	return at.getArmedDrawdownRuleFingerprintsForPosition(symbol, side, 0, 0)
}

func (at *AutoTrader) getArmedDrawdownRuleFingerprintsForPosition(symbol, side string, entryPrice, quantity float64) map[string]struct{} {
	armed := make(map[string]struct{})
	for _, record := range at.getArmedDrawdownRecordsForPosition(symbol, side, entryPrice, quantity, 0) {
		if record.RuleFingerprint != "" {
			armed[record.RuleFingerprint] = struct{}{}
		}
	}
	return armed
}

func (at *AutoTrader) hasMatchingNativeTrailingOrderForRule(symbol, side string, entryPrice float64, rule store.DrawdownTakeProfitRule, openOrders []OpenOrder) bool {
	if len(openOrders) == 0 {
		return false
	}
	plannedActivationPrice := calculateProfitBasedTrailingTriggerPrice(entryPrice, side, rule.MinProfitPct)
	plannedCallbackRate := calculateDrawdownRuleCallbackRatio(entryPrice, side, rule)
	for _, order := range openOrders {
		if order.PositionSide != "" && !strings.EqualFold(order.PositionSide, strings.ToUpper(side)) {
			continue
		}
		if !strings.Contains(strings.ToUpper(order.Type), "TRAILING") {
			continue
		}
		callback := order.CallbackRate
		if callback <= 0 && order.CallbackRatePct > 0 {
			callback = order.CallbackRatePct / 100.0
		}
		if callback > 1 {
			callback = callback / 100.0
		}
		callbackTolerance := 0.00025
		activationOK := true
		if order.StopPrice > 0 && plannedActivationPrice > 0 {
			activationDrift := math.Abs(order.StopPrice-plannedActivationPrice) / math.Max(math.Abs(order.StopPrice), math.Abs(plannedActivationPrice))
			activationOK = activationDrift <= 0.01
		}
		if activationOK && math.Abs(callback-plannedCallbackRate) <= callbackTolerance {
			return true
		}
	}
	return false
}

func (at *AutoTrader) getDrawdownArmRulesForNativeExposure(currentPnLPct, entryPrice, quantity float64, symbol, side string, rules []store.DrawdownTakeProfitRule) []store.DrawdownTakeProfitRule {
	// Exchange-native trailing coverage is intentionally single-tier on OKX-style
	// venues. Live OKX behaviour showed multiple simultaneous trailing tiers on the
	// same symbol/side can churn (place/cancel/place). Keep one exchange tier stable
	// and let local managed drawdown + reconciler migrate it as profit advances.
	highestArmedMinProfit := at.getHighestArmedTierMinProfit(symbol, side, entryPrice, quantity)

	// If current profit is below the floor tier's activation threshold, the floor
	// tier's trailing order is unreachable (activation price not met). In this case,
	// drop the floor so we can arm the highest currently-satisfied tier instead.
	// This prevents "phantom protection" where an unreachable trailing order exists
	// but provides no actual protection.
	// HOWEVER: if the exchange already has a trailing order with activePx set,
	// it will auto-activate when price reaches it — don't clear in that case.
	if highestArmedMinProfit > 0 && currentPnLPct < highestArmedMinProfit {
		if openOrders, err := at.trader.GetOpenOrders(symbol); err == nil {
			positionSide := strings.ToUpper(side)
			hasLiveTrailing := false
			for _, o := range openOrders {
				if !strings.EqualFold(o.PositionSide, positionSide) && o.PositionSide != "" && o.PositionSide != "BOTH" {
					continue
				}
				if strings.Contains(strings.ToUpper(o.Type), "TRAILING") {
					hasLiveTrailing = true
					break
				}
			}
			if hasLiveTrailing {
				// Exchange has a trailing order waiting for activation — don't clear
				return nil
			}
		}
		logger.Infof("🔄 Drawdown floor downgrade: %s %s profit %.2f%% < floor %.2f%% — allowing lower tier", symbol, side, currentPnLPct, highestArmedMinProfit)
		at.clearArmedDrawdownRecordsAboveMinProfit(symbol, side, currentPnLPct)
		highestArmedMinProfit = 0
	}

	rule, ok := selectNativeDrawdownExposureRuleWithFloor(currentPnLPct, rules, highestArmedMinProfit)
	if !ok {
		return nil
	}
	return at.getDrawdownArmRulesForSelectedRule(entryPrice, quantity, symbol, side, rule)
}

// getHighestArmedTierMinProfit returns the MinProfitPct of the highest tier that has been
// armed (tracking or superseded or executed). Also checks DB records to survive restarts.
func (at *AutoTrader) getHighestArmedTierMinProfit(symbol, side string, entryPrice, quantity float64) float64 {
	allocs := at.getDrawdownTierAllocs(symbol, side)
	var highest float64
	for _, a := range allocs {
		if a.Status == "tracking" || a.Status == "superseded" || a.Status == "executed" {
			if a.MinProfitPct > highest {
				highest = a.MinProfitPct
			}
		}
	}
	// Also check persisted DB records to survive container restarts.
	// Fingerprint format: entryPrice|quantity|MinProfitPct|MaxDrawdownPct|...
	for _, record := range at.getArmedDrawdownRecordsForPosition(symbol, side, entryPrice, quantity, 0) {
		parts := strings.Split(record.RuleFingerprint, "|")
		if len(parts) >= 3 {
			if minProfit, err := strconv.ParseFloat(parts[2], 64); err == nil && minProfit > highest {
				highest = minProfit
			}
		}
	}
	return highest
}

func selectNativeDrawdownExposureRule(currentPnLPct float64, rules []store.DrawdownTakeProfitRule) (store.DrawdownTakeProfitRule, bool) {
	return selectNativeDrawdownExposureRuleWithFloor(currentPnLPct, rules, 0)
}

// selectNativeDrawdownExposureRuleWithFloor selects the best native trailing tier to arm.
// floorMinProfit prevents downgrade: never select a tier with MinProfitPct < floorMinProfit.
// When currentPnL drops below the floor tier's threshold, the floor tier is still returned
// (maintaining the highest armed tier rather than downgrading).
func selectNativeDrawdownExposureRuleWithFloor(currentPnLPct float64, rules []store.DrawdownTakeProfitRule, floorMinProfit float64) (store.DrawdownTakeProfitRule, bool) {
	bestSatisfied := store.DrawdownTakeProfitRule{}
	hasSatisfied := false
	next := store.DrawdownTakeProfitRule{}
	hasNext := false
	var floorRule store.DrawdownTakeProfitRule
	hasFloor := false

	for _, raw := range rules {
		rule := normalizeDrawdownRule(raw)
		if rule.MinProfitPct <= 0 || rule.MaxDrawdownPct <= 0 || rule.CloseRatioPct <= 0 {
			continue
		}
		// Track the floor tier (highest previously armed)
		if floorMinProfit > 0 && math.Abs(rule.MinProfitPct-floorMinProfit) < 0.001 {
			floorRule = rule
			hasFloor = true
		}
		// Skip tiers below the floor (prevent downgrade)
		if floorMinProfit > 0 && rule.MinProfitPct < floorMinProfit {
			continue
		}
		if currentPnLPct >= rule.MinProfitPct {
			if !hasSatisfied || rule.MinProfitPct > bestSatisfied.MinProfitPct {
				bestSatisfied = rule
				hasSatisfied = true
			}
			continue
		}
		if !hasNext || rule.MinProfitPct < next.MinProfitPct {
			next = rule
			hasNext = true
		}
	}
	if hasSatisfied {
		return bestSatisfied, true
	}
	// If no tier is currently satisfied but we have a floor (previously armed tier),
	// return the floor tier to maintain it (profit dropped but we don't downgrade)
	if hasFloor && !hasNext {
		return floorRule, true
	}
	if hasFloor && floorRule.MinProfitPct >= next.MinProfitPct {
		return floorRule, true
	}
	// If profit dropped below floor but there's a higher next tier, return floor
	if hasFloor {
		return floorRule, true
	}
	return next, hasNext
}

func (at *AutoTrader) getDrawdownArmRulesForSelectedRule(entryPrice, quantity float64, symbol, side string, rule store.DrawdownTakeProfitRule) []store.DrawdownTakeProfitRule {
	rule = normalizeDrawdownRule(rule)
	armedFingerprints := at.getArmedDrawdownRuleFingerprintsForPosition(symbol, side, entryPrice, quantity)
	openOrders, _ := at.trader.GetOpenOrders(symbol)
	fingerprint := stableDrawdownRuleFingerprint(entryPrice, rule)
	if _, ok := armedFingerprints[fingerprint]; ok {
		if at.hasMatchingNativeTrailingOrderForRule(symbol, side, entryPrice, rule, openOrders) {
			logger.Infof("🟣 Drawdown native exposure skipped: %s %s already armed fingerprint=%s", symbol, side, fingerprint)
			return nil
		}
		// Check if this is a managed drawdown record — managed mode doesn't place
		// exchange trailing orders, so absence of a trailing order is expected.
		if at.isManagedDrawdownRecord(symbol, side, fingerprint) {
			logger.Infof("🟣 Drawdown managed mode already armed: %s %s fingerprint=%s (no exchange trailing expected)", symbol, side, fingerprint)
			return nil
		}
		// Check if this tier was already executed (order filled, position reduced).
		// If the tier alloc shows executed/triggered, don't re-arm.
		if at.isDrawdownTierExecuted(symbol, side, rule) {
			logger.Infof("🟣 Drawdown tier already executed: %s %s fingerprint=%s (trailing order filled, not re-arming)", symbol, side, fingerprint)
			return nil
		}
		// Prevent re-arm loop: if we recently armed this fingerprint (within 300s) and
		// the trailing order is not visible, OKX likely activated and filled it immediately
		// (activation price already breached). Don't re-arm to avoid spamming orders.
		if lastArm, ok := at.nativeTrailingArmTime[fingerprint]; ok && time.Since(lastArm) < 300*time.Second {
			logger.Infof("🟠 Drawdown native trailing arm cooldown: %s %s fingerprint=%s (armed %.0fs ago, not re-arming)", symbol, side, fingerprint, time.Since(lastArm).Seconds())
			return nil
		}
		logger.Infof("⚠️ Drawdown native exposure record stale: %s %s fingerprint=%s has no matching exchange trailing order, re-arming", symbol, side, fingerprint)
	}
	logger.Infof("🟣 Drawdown native exposure selected: %s %s min=%.4f close=%.1f%% fingerprint=%s", symbol, side, rule.MinProfitPct, rule.CloseRatioPct, fingerprint)
	return []store.DrawdownTakeProfitRule{rule}
}

func (at *AutoTrader) getDrawdownArmRules(currentPnLPct, entryPrice, quantity float64, symbol, side string, rules []store.DrawdownTakeProfitRule) []store.DrawdownTakeProfitRule {
	armedFingerprints := at.getArmedDrawdownRuleFingerprintsForPosition(symbol, side, entryPrice, quantity)
	openOrders, _ := at.trader.GetOpenOrders(symbol)

	// Find the highest satisfied tier — tiers only upgrade, never downgrade.
	var bestRule store.DrawdownTakeProfitRule
	hasBest := false
	for _, rule := range rules {
		rule = normalizeDrawdownRule(rule)
		if !isDrawdownRuleSatisfied(currentPnLPct, rule) {
			logger.Infof("🟣 Drawdown arm pending: %s %s profit %.4f below min %.4f (close=%.1f%%)", symbol, side, currentPnLPct, rule.MinProfitPct, rule.CloseRatioPct)
			continue
		}
		if !hasBest || rule.MinProfitPct > bestRule.MinProfitPct {
			bestRule = rule
			hasBest = true
		}
	}
	if !hasBest {
		return nil
	}

	fingerprint := stableDrawdownRuleFingerprint(entryPrice, bestRule)
	if _, ok := armedFingerprints[fingerprint]; ok {
		if at.hasMatchingNativeTrailingOrderForRule(symbol, side, entryPrice, bestRule, openOrders) {
			logger.Infof("🟣 Drawdown arm skipped: %s %s highest tier already armed fingerprint=%s (min=%.4f close=%.1f%%)", symbol, side, fingerprint, bestRule.MinProfitPct, bestRule.CloseRatioPct)
			return nil
		}
		if at.isDrawdownTierExecuted(symbol, side, bestRule) {
			logger.Infof("🟣 Drawdown tier already executed: %s %s fingerprint=%s (not re-arming)", symbol, side, fingerprint)
			return nil
		}
		// Prevent re-arm loop when OKX immediately fills trailing orders (activation already breached)
		if lastArm, ok := at.nativeTrailingArmTime[fingerprint]; ok && time.Since(lastArm) < 300*time.Second {
			logger.Infof("🟠 Drawdown arm cooldown: %s %s fingerprint=%s (armed %.0fs ago, not re-arming)", symbol, side, fingerprint, time.Since(lastArm).Seconds())
			return nil
		}
		logger.Infof("⚠️ Drawdown arm record stale: %s %s fingerprint=%s has no matching exchange trailing order, re-arming highest tier (min=%.4f close=%.1f%%)", symbol, side, fingerprint, bestRule.MinProfitPct, bestRule.CloseRatioPct)
	}
	logger.Infof("🟣 Drawdown arm eligible: %s %s profit %.4f >= min %.4f close=%.1f%% fingerprint=%s (highest satisfied tier)", symbol, side, currentPnLPct, bestRule.MinProfitPct, bestRule.CloseRatioPct, fingerprint)
	return []store.DrawdownTakeProfitRule{bestRule}
}

func sideToOpenAction(side string) string {
	switch strings.ToUpper(strings.TrimSpace(side)) {
	case "LONG":
		return "open_long"
	case "SHORT":
		return "open_short"
	default:
		return ""
	}
}

func findMatchedDecisionAction(record *store.DecisionRecord, symbol, action string) *store.DecisionAction {
	if record == nil {
		return nil
	}
	for i := range record.Decisions {
		candidate := &record.Decisions[i]
		if symbol != "" && !strings.EqualFold(candidate.Symbol, symbol) {
			continue
		}
		if action != "" && !strings.EqualFold(candidate.Action, action) {
			continue
		}
		return candidate
	}
	return nil
}

func extractDecisionReviewMap(actionReview *store.DecisionActionReviewContext) map[string]interface{} {
	if actionReview == nil {
		return nil
	}
	payload, err := json.Marshal(actionReview)
	if err != nil {
		return nil
	}
	decoded := map[string]interface{}{}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil
	}
	return decoded
}

func buildEntryReviewSummaryFromDecisionReview(review map[string]interface{}) map[string]interface{} {
	if review == nil {
		return nil
	}
	summary := map[string]interface{}{}
	for _, key := range []string{"timeframe_context", "risk_reward", "key_levels", "anchors", "alignment_notes", "protection", "control", "execution_constraints"} {
		if value, ok := review[key]; ok {
			summary[key] = value
		}
	}
	if len(summary) == 0 {
		return nil
	}
	return summary
}

func (at *AutoTrader) buildDrawdownStructureContext(symbol, side string) *drawdownStructureContext {
	if at.store == nil {
		return nil
	}
	positionStore := at.store.Position()
	decisionStore := at.store.Decision()
	if positionStore == nil || decisionStore == nil {
		return nil
	}
	openPos, err := positionStore.GetOpenPositionBySymbol(at.id, symbol, strings.ToUpper(side))
	if err != nil || openPos == nil {
		return nil
	}
	entryDecisionCycle := openPos.EntryDecisionCycle
	if entryDecisionCycle <= 0 {
		if inferred := positionStore.FindEntryDecisionCycleForPosition(at.id, symbol, strings.ToUpper(side), openPos.EntryTime); inferred > 0 {
			entryDecisionCycle = inferred
			if err := positionStore.BackfillEntryDecisionCycle(openPos.ID, inferred); err != nil {
				logger.Infof("⚠️ Failed to backfill entry decision cycle for %s %s position %d: %v", symbol, side, openPos.ID, err)
			} else {
				logger.Infof("🧷 Backfilled entry decision cycle for %s %s position %d -> cycle %d", symbol, side, openPos.ID, inferred)
			}
		}
	}
	if entryDecisionCycle <= 0 {
		return nil
	}
	record, err := decisionStore.GetRecordByCycle(at.id, entryDecisionCycle)
	if err != nil || record == nil {
		return nil
	}

	ctx := &drawdownStructureContext{}
	candidate := findMatchedDecisionAction(record, symbol, sideToOpenAction(strings.ToUpper(side)))
	decoded := extractDecisionReviewMap(func() *store.DecisionActionReviewContext {
		if candidate != nil {
			return candidate.ReviewContext
		}
		return nil
	}())
	if decoded == nil {
		return nil
	}
	if review, ok := decoded["timeframe_context"].(map[string]interface{}); ok {
		ctx.PrimaryTimeframe, _ = review["primary"].(string)
		ctx.LowerTimeframes = readStringSlice(review["lower"])
		ctx.HigherTimeframes = readStringSlice(review["higher"])
	}
	if rr, ok := decoded["risk_reward"].(map[string]interface{}); ok {
		ctx.Entry = readFloat(rr["entry"])
		ctx.Invalidation = readFloat(rr["invalidation"])
		ctx.FirstTarget = readFloat(rr["first_target"])
	}
	if levels, ok := decoded["key_levels"].(map[string]interface{}); ok {
		ctx.Support = readFloatSlice(levels["support"])
		ctx.Resistance = readFloatSlice(levels["resistance"])
		if fib, ok := levels["fibonacci"].(map[string]interface{}); ok {
			ctx.FibLevels = readFloatSlice(fib["levels"])
		}
	}
	ctx.Anchors = append(ctx.Anchors, readDecisionAnchors(decoded["anchors"])...)
	ctx.Anchors = append(ctx.Anchors, readDecisionAnchors(decoded["higher_timeframe_anchors"])...)
	if structuresRaw, ok := decoded["timeframe_structures"].([]interface{}); ok {
		for _, raw := range structuresRaw {
			item, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			tf := readString(item["timeframe"])
			for _, v := range readFloatSlice(item["support"]) {
				ctx.Support = append(ctx.Support, v)
			}
			for _, v := range readFloatSlice(item["resistance"]) {
				ctx.Resistance = append(ctx.Resistance, v)
			}
			if fib, ok := item["fibonacci"].(map[string]interface{}); ok {
				ctx.FibLevels = append(ctx.FibLevels, readFloatSlice(fib["levels"])...)
			}
			anchors := readDecisionAnchors(item["anchors"])
			for i := range anchors {
				if anchors[i].Timeframe == "" {
					anchors[i].Timeframe = tf
				}
			}
			ctx.Anchors = append(ctx.Anchors, anchors...)
		}
	}
	if ctx.PrimaryTimeframe == "" && len(ctx.Support) == 0 && len(ctx.Resistance) == 0 && len(ctx.FibLevels) == 0 && len(ctx.Anchors) == 0 && ctx.Entry == 0 && ctx.FirstTarget == 0 {
		return nil
	}
	return ctx
}

func readDecisionAnchors(value interface{}) []store.DecisionActionReasonAnchor {
	anchorsRaw, ok := value.([]interface{})
	if !ok {
		return nil
	}
	anchors := make([]store.DecisionActionReasonAnchor, 0, len(anchorsRaw))
	for _, raw := range anchorsRaw {
		item, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		anchors = append(anchors, store.DecisionActionReasonAnchor{
			Type:      readString(item["type"]),
			Timeframe: readString(item["timeframe"]),
			Price:     readFloat(item["price"]),
			Reason:    readString(item["reason"]),
		})
	}
	return anchors
}

func readString(value interface{}) string {
	str, _ := value.(string)
	return strings.TrimSpace(str)
}

func readStringSlice(value interface{}) []string {
	items, ok := value.([]interface{})
	if !ok {
		if existing, ok := value.([]string); ok {
			return existing
		}
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			result = append(result, strings.TrimSpace(s))
		}
	}
	return result
}

func readFloat(value interface{}) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	default:
		return 0
	}
}

func readFloatSlice(value interface{}) []float64 {
	items, ok := value.([]interface{})
	if !ok {
		if existing, ok := value.([]float64); ok {
			return existing
		}
		return nil
	}
	result := make([]float64, 0, len(items))
	for _, item := range items {
		if f := readFloat(item); f > 0 {
			result = append(result, f)
		}
	}
	return result
}

func (at *AutoTrader) supportsNativeTrailingStop() bool {
	caps := at.GetProtectionCapabilities()
	exchange := strings.ToLower(at.exchange)
	switch exchange {
	case "binance":
		return caps.SupportsAlgoOrders && caps.CanAmendProtection
	case "bitget":
		return caps.NativeStopLoss && caps.NativeTakeProfit && caps.NativePartialClose
	case "okx":
		return caps.SupportsAlgoOrders && caps.CanAmendProtection
	default:
		return false
	}
}

type nativeTrailingOrder struct {
	PositionSide     string
	StopPrice        float64
	CallbackRate     float64
	Quantity         float64
	OrderID          string
	ActivationStatus string
}

func (at *AutoTrader) findExistingFullTrailingOrder(side string, openOrders []OpenOrder) *nativeTrailingOrder {
	for _, order := range openOrders {
		if order.PositionSide != "" && !strings.EqualFold(order.PositionSide, strings.ToUpper(side)) {
			continue
		}
		if !strings.Contains(strings.ToUpper(order.Type), "TRAILING") {
			continue
		}
		return &nativeTrailingOrder{
			PositionSide: order.PositionSide,
			StopPrice:    order.StopPrice,
			CallbackRate: order.CallbackRate,
			Quantity:     order.Quantity,
			OrderID:      order.OrderID,
		}
	}
	return nil
}

// exchangeSideCoversDrawdownTier reports whether the exchange already carries a
// live trailing order that protects this drawdown tier — i.e. the exchange side
// is the active protection and the code side must NOT also fire (avoid double
// execution). It is the gate for "code side only supplements when the exchange
// side did not actually execute".
//
// Returns true (exchange covers it, suppress code-side close) when a trailing
// order matching this tier's planned activation + callback + quantity exists on
// the exchange in any non-terminal state. Returns false only when no such order
// is found (exchange side genuinely absent), in which case the code-side managed
// close is allowed to supplement.
func (at *AutoTrader) exchangeSideCoversDrawdownTier(symbol, side string, rule store.DrawdownTakeProfitRule, entryPrice float64) bool {
	if !at.supportsNativeTrailingStop() {
		// Exchange cannot carry native trailing at all → code side is the sole
		// protection and must execute (no double-fire risk).
		return false
	}
	openOrders, err := at.GetOpenOrders(symbol)
	if err != nil {
		// Cannot confirm exchange state. Be conservative: do NOT suppress the code
		// side, so protection still fires (a possible duplicate is safer than an
		// unprotected position). Duplicate close is reduce-only and bounded by the
		// remaining position size, so the worst case is a no-op second close.
		logger.Warnf("⚠️ Drawdown fallback gate: cannot fetch open orders (%s %s): %v — allowing code-side close", symbol, side, err)
		return false
	}
	existing, _, _, _ := at.findEquivalentPartialTrailingOrder(symbol, side, rule, entryPrice, openOrders)
	if existing != nil {
		logger.Infof("🛡 Exchange side covers drawdown tier (%s %s close=%.1f%% status=%s) — suppressing code-side close to avoid double execution",
			symbol, side, rule.CloseRatioPct, existing.ActivationStatus)
		return true
	}
	return false
}

func (at *AutoTrader) findEquivalentPartialTrailingOrder(symbol, side string, rule store.DrawdownTakeProfitRule, entryPrice float64, openOrders []OpenOrder) (*nativeTrailingOrder, float64, float64, float64) {
	plannedActivationPrice := calculateProfitBasedTrailingTriggerPrice(entryPrice, side, rule.MinProfitPct)
	plannedCallbackRate := calculateDrawdownRuleCallbackRatio(entryPrice, side, rule)
	currentQty := 0.0
	positions, err := at.trader.GetPositions()
	if err == nil {
		for _, pos := range positions {
			ps, _ := pos["symbol"].(string)
			pd, _ := pos["side"].(string)
			if ps != symbol || !strings.EqualFold(pd, side) {
				continue
			}
			currentQty, _ = pos["positionAmt"].(float64)
			if currentQty < 0 {
				currentQty = -currentQty
			}
			break
		}
	}
	if currentQty > 0 {
		// Use cumulative ratio (this tier + all lower tiers) to match the actual
		// trailing order quantity on exchange. E.g. T2 armed = T1(65%)+T2(25%)=90%.
		cumulativeRatio := at.getCumulativeCloseRatioByRule(symbol, side, rule)
		originalQty := currentQty
		if at.store != nil {
			if dbPos, err := at.store.Position().GetOpenPositionBySymbol(at.id, symbol, strings.ToUpper(side)); err == nil && dbPos != nil && dbPos.EntryQuantity > 0 {
				originalQty = dbPos.EntryQuantity
			}
		}
		var qtyTarget float64
		if cumulativeRatio >= 100 {
			qtyTarget = currentQty
		} else {
			qtyTarget = originalQty * cumulativeRatio / 100.0
			if qtyTarget > currentQty {
				qtyTarget = currentQty
			}
		}
		callbackTolerance := 0.0002
		qtyTolerance := math.Max(0.0001, qtyTarget*0.1)
		for _, order := range openOrders {
			if order.PositionSide != "" && !strings.EqualFold(order.PositionSide, strings.ToUpper(side)) {
				continue
			}
			if !strings.Contains(strings.ToUpper(order.Type), "TRAILING") {
				continue
			}
			if math.Abs(order.Quantity-qtyTarget) <= qtyTolerance && math.Abs(order.CallbackRate-plannedCallbackRate) <= callbackTolerance && activationMatches(order.StopPrice, plannedActivationPrice) {
				return &nativeTrailingOrder{
					PositionSide:     order.PositionSide,
					StopPrice:        order.StopPrice,
					CallbackRate:     order.CallbackRate,
					Quantity:         order.Quantity,
					OrderID:          order.OrderID,
					ActivationStatus: order.ActivationStatus,
				}, qtyTarget, plannedActivationPrice, plannedCallbackRate
			}
		}
		return nil, qtyTarget, plannedActivationPrice, plannedCallbackRate
	}
	return nil, 0, plannedActivationPrice, plannedCallbackRate
}

func activationMatches(actual, planned float64) bool {
	if actual <= 0 || planned <= 0 {
		return false
	}
	return math.Abs(actual-planned)/math.Max(math.Abs(actual), math.Abs(planned)) <= 0.003
}

func (at *AutoTrader) findPartialTrailingReplacementCandidate(side string, openOrders []OpenOrder, qtyTarget, plannedActivationPrice, plannedCallbackRate float64) *nativeTrailingOrder {
	var best *nativeTrailingOrder
	bestScore := math.MaxFloat64
	for _, order := range openOrders {
		if order.PositionSide != "" && !strings.EqualFold(order.PositionSide, strings.ToUpper(side)) {
			continue
		}
		if !strings.Contains(strings.ToUpper(order.Type), "TRAILING") {
			continue
		}
		qtyScore := math.Abs(order.Quantity - qtyTarget)
		callbackScore := math.Abs(order.CallbackRate-plannedCallbackRate) * 100
		activationScore := 0.0
		if order.StopPrice > 0 && plannedActivationPrice > 0 {
			activationScore = math.Abs(order.StopPrice-plannedActivationPrice) / math.Max(math.Abs(order.StopPrice), math.Abs(plannedActivationPrice))
		}
		score := qtyScore + callbackScore + activationScore
		if best == nil || score < bestScore {
			bestScore = score
			best = &nativeTrailingOrder{
				PositionSide: order.PositionSide,
				StopPrice:    order.StopPrice,
				CallbackRate: order.CallbackRate,
				Quantity:     order.Quantity,
				OrderID:      order.OrderID,
			}
		}
	}
	return best
}

func (at *AutoTrader) shouldReplacePartialTrailingTier(existing *nativeTrailingOrder, plannedActivationPrice, plannedCallbackRate float64) bool {
	if existing == nil {
		return false
	}
	if existing.StopPrice <= 0 || plannedActivationPrice <= 0 {
		return false
	}
	activationDrift := math.Abs(existing.StopPrice-plannedActivationPrice) / math.Max(math.Abs(existing.StopPrice), math.Abs(plannedActivationPrice))
	callbackDrift := math.Abs(existing.CallbackRate - plannedCallbackRate)
	return activationDrift > 0.003 || callbackDrift > 0.0002
}

func findNewestMatchingTrailingOrderID(openOrders []OpenOrder, positionSide string, existingTier *nativeTrailingOrder, qtyTarget, plannedCallbackRate float64) string {
	for i := len(openOrders) - 1; i >= 0; i-- {
		order := openOrders[i]
		if order.PositionSide != "" && !strings.EqualFold(order.PositionSide, positionSide) {
			continue
		}
		if !strings.Contains(strings.ToUpper(order.Type), "TRAILING") {
			continue
		}
		if existingTier != nil && order.OrderID == existingTier.OrderID {
			continue
		}
		qtyTolerance := math.Max(0.0001, qtyTarget*0.1)
		if math.Abs(order.Quantity-qtyTarget) <= qtyTolerance && math.Abs(order.CallbackRate-plannedCallbackRate) <= 0.0002 {
			return order.OrderID
		}
	}
	return ""
}

func (at *AutoTrader) applyManagedDrawdownFallback(symbol, side string, entryPrice float64, rule store.DrawdownTakeProfitRule, activationPrice float64, callbackRatio float64) bool {
	if !at.verifyLivePositionForProtection(symbol, side, "managed drawdown fallback") {
		return false
	}
	if rule.CloseRatioPct <= 0 || entryPrice <= 0 {
		return false
	}
	positionSide := strings.ToUpper(side)
	positionAction := "open_" + strings.ToLower(side)
	if rule.CloseRatioPct < 99.999 {
		positions, err := at.trader.GetPositions()
		if err != nil {
			logger.Infof("❌ Managed partial drawdown fallback failed to fetch positions (%s %s): %v", symbol, side, err)
			return false
		}
		quantity := 0.0
		for _, pos := range positions {
			ps, _ := pos["symbol"].(string)
			pd, _ := pos["side"].(string)
			if ps != symbol || !strings.EqualFold(pd, side) {
				continue
			}
			quantity, _ = pos["positionAmt"].(float64)
			if quantity < 0 {
				quantity = -quantity
			}
			break
		}
		if quantity <= 0 {
			logger.Infof("❌ Managed partial drawdown fallback missing quantity (%s %s)", symbol, side)
			return false
		}
		candidate := buildManagedPartialDrawdownPlanCandidate(entryPrice, positionAction, rule)
		if candidate == nil || !at.canApplyManagedPartialDrawdownPlan(candidate) {
			return false
		}
		logger.Infof("🟣 Managed partial drawdown fallback armed after native rejection: %s %s | activation=%.6f callbackRatio=%.6f close=%.1f%%", symbol, side, activationPrice, callbackRatio, rule.CloseRatioPct)
		if err := at.placeAndVerifyProtectionPlanWithRetry(symbol, positionSide, quantity, candidate); err != nil {
			logger.Infof("❌ Managed partial drawdown fallback apply failed (%s %s): %v", symbol, side, err)
			return false
		}
		at.setProtectionState(symbol, side, "managed_partial_drawdown_armed")
		at.persistDynamicProtectionRecordWithDetails(symbol, side, "managed_drawdown", stableDrawdownRuleFingerprint(entryPrice, rule), rule.CloseRatioPct, "armed", "", activationPrice, callbackRatio, 0)
		return true
	}
	at.setProtectionState(symbol, side, "managed_drawdown_armed")
	at.persistDynamicProtectionRecordWithDetails(symbol, side, "managed_drawdown", stableDrawdownRuleFingerprint(entryPrice, rule), rule.CloseRatioPct, "armed", "", activationPrice, callbackRatio, 0)
	logger.Infof("🟣 Managed full drawdown fallback armed after native rejection: %s %s | activation=%.6f callbackRatio=%.6f close=100%%", symbol, side, activationPrice, callbackRatio)
	return true
}

func (at *AutoTrader) applyNativeTrailingDrawdown(symbol, side string, entryPrice, markPrice float64, rule store.DrawdownTakeProfitRule) bool {
	if !at.supportsNativeTrailingStop() {
		return false
	}
	if !at.verifyLivePositionForProtection(symbol, side, "native trailing drawdown") {
		return false
	}
	currentState := at.getProtectionState(symbol, side)
	if currentState == "native_trailing_arming" || currentState == "native_partial_trailing_arming" {
		logger.Infof("🟣 Native trailing drawdown already arming, skipping duplicate apply (%s %s state=%s)", symbol, side, currentState)
		return true
	}
	isPartial := rule.CloseRatioPct < 99.999
	var staleFullTrailing *nativeTrailingOrder
	if currentState == "native_trailing_armed" || currentState == "native_partial_trailing_armed" {
		if openOrders, err := at.trader.GetOpenOrders(symbol); err == nil {
			if !isPartial {
				plannedActivationPrice := calculateProfitBasedTrailingTriggerPrice(entryPrice, side, rule.MinProfitPct)
				plannedCallbackRate := calculateDrawdownRuleCallbackRatio(entryPrice, side, rule)
				existing := at.findExistingFullTrailingOrder(side, openOrders)
				if existing != nil {
					if !at.shouldReplacePartialTrailingTier(existing, plannedActivationPrice, plannedCallbackRate) {
						return true
					}
					logger.Infof("⚠️ Native trailing state exists but full trailing order drifted from plan (%s %s), re-arming", symbol, side)
					staleFullTrailing = existing
				} else {
					logger.Infof("⚠️ Native trailing state exists but no trailing order found on exchange (%s %s), re-arming", symbol, side)
				}
			} else {
				existingTier, _, plannedActivationPrice, plannedCallbackRate := at.findEquivalentPartialTrailingOrder(symbol, side, rule, entryPrice, openOrders)
				if existingTier != nil && !at.shouldReplacePartialTrailingTier(existingTier, plannedActivationPrice, plannedCallbackRate) {
					// Plan A (2026-06-09): an existing partial trailing tier that matches the
					// plan (activePx anchored at +6%, correct callback) is KEPT as-is — even if
					// price has passed activePx and OKX hasn't activated it. We no longer
					// re-place it without activePx (that would drop the +6% anchor and loop).
					// The phantom-not-activated case is converted to a managed drawdown monitor
					// by checkAndFixStaleTrailingActivation, which preserves the +6% semantics.
					if existingTier.StopPrice > 0 && plannedActivationPrice > 0 {
						logger.Infof("ℹ️ Native partial trailing tier already exists on exchange (%s %s close=%.1f%% activation=%.6f callback=%.6f status=%s)", symbol, side, rule.CloseRatioPct, existingTier.StopPrice, existingTier.CallbackRate, existingTier.ActivationStatus)
					}
					return true
				}
			}
		}
	}
	// For partial close rules, check if exchange supports native partial close
	if isPartial {
		caps := at.GetProtectionCapabilities()
		if !caps.NativePartialClose {
			return false
		}
	}
	if entryPrice <= 0 || rule.MinProfitPct <= 0 || rule.MaxDrawdownPct <= 0 {
		return false
	}

	plannedActivationPrice := calculateProfitBasedTrailingTriggerPrice(entryPrice, side, rule.MinProfitPct)
	// Plan B1/A: the runner DD trailing MUST always carry an activation price anchored
	// at the outer ladder TP (+6%). We do NOT drop activePx to "activate immediately"
	// when price has already moved past it — that would leave the runner with no fixed
	// activation anchor. If OKX then fails to auto-activate a passed-activePx order
	// (phantom protection), checkAndFixStaleTrailingActivation converts it to a managed
	// drawdown monitor instead of re-placing without activePx (fix 2026-06-09).
	activationPrice := plannedActivationPrice
	priceBasedCallbackRatio := calculateDrawdownRuleCallbackRatio(entryPrice, side, rule)
	if plannedActivationPrice <= 0 || priceBasedCallbackRatio <= 0 {
		return false
	}

	logger.Infof("🎯 Trailing activation resolved: %s %s | activation=%.6f planned=%.6f callbackRatio=%.6f rule=minProfit=%.4f maxDrawdown=%.4f close=%.2f%% stage=%s",
		symbol, side, activationPrice, plannedActivationPrice, priceBasedCallbackRatio, rule.MinProfitPct, rule.MaxDrawdownPct, rule.CloseRatioPct, rule.StageName)
	callbackAdjustment, err := adjustNativeDrawdownCallbackRatio(entryPrice, side, rule, priceBasedCallbackRatio)
	if err != nil {
		logger.Warnf("❌ Native trailing drawdown rejected by safety policy (%s %s): %v", symbol, side, err)
		var nativeReject nativeDrawdownRejection
		if errors.As(err, &nativeReject) {
			return at.applyManagedDrawdownFallback(symbol, side, entryPrice, rule, activationPrice, priceBasedCallbackRatio)
		}
		return false
	}
	if callbackAdjustment.Adjusted {
		logger.Warnf("🛠 Native trailing drawdown callback adjusted (%s %s): %.6f -> %.6f reason=%s",
			symbol, side, priceBasedCallbackRatio, callbackAdjustment.CallbackRatio, callbackAdjustment.Reason)
		priceBasedCallbackRatio = callbackAdjustment.CallbackRatio
	}

	positionSide := strings.ToUpper(side)
	positionAction := "open_" + strings.ToLower(side)
	exchange := strings.ToLower(at.exchange)
	armingState := "native_trailing_arming"
	if isPartial {
		armingState = "native_partial_trailing_arming"
	}
	claimed, previousProtectionState, actualProtectionState := at.claimProtectionArmingState(symbol, side, currentState, armingState)
	if !claimed {
		if isNativeTrailingArmingState(actualProtectionState) {
			logger.Infof("🟣 Native trailing drawdown already arming, skipping duplicate apply (%s %s state=%s)", symbol, side, actualProtectionState)
		} else {
			logger.Infof("🟣 Native trailing drawdown state changed during prepare, skipping duplicate apply (%s %s expected=%s actual=%s)", symbol, side, currentState, actualProtectionState)
		}
		return true
	}
	defer func() {
		if at.getProtectionState(symbol, side) != armingState {
			return
		}
		if previousProtectionState == "" {
			at.clearProtectionState(symbol, side)
			return
		}
		at.setProtectionState(symbol, side, previousProtectionState)
	}()

	if isPartial {
		positions, err := at.trader.GetPositions()
		if err != nil {
			logger.Infof("❌ Partial drawdown failed to fetch positions (%s %s): %v", symbol, side, err)
			return false
		}
		var quantity float64
		for _, pos := range positions {
			ps, _ := pos["symbol"].(string)
			pd, _ := pos["side"].(string)
			if ps != symbol || !strings.EqualFold(pd, side) {
				continue
			}
			quantity, _ = pos["positionAmt"].(float64)
			if quantity < 0 {
				quantity = -quantity
			}
			break
		}
		if quantity <= 0 {
			logger.Infof("❌ Partial drawdown missing quantity (%s %s)", symbol, side)
			return false
		}

		// Use original entry quantity for CloseRatioPct calculation.
		// When a tier triggers, it closes its own ratio PLUS all superseded lower tiers' ratios.
		// E.g. T1=65%, T2=25%: if T2 triggers (T1 was superseded), close 65+25=90% of original.
		// This implements "higher tier inherits lower tier's position responsibility".
		originalQty := quantity
		if at.store != nil {
			if dbPos, err := at.store.Position().GetOpenPositionBySymbol(at.id, symbol, strings.ToUpper(side)); err == nil && dbPos != nil && dbPos.EntryQuantity > 0 {
				originalQty = dbPos.EntryQuantity
			}
		}
		cumulativeRatio := at.getCumulativeCloseRatioByRule(symbol, side, rule)
		var partialQty float64
		if cumulativeRatio >= 100 {
			partialQty = quantity
		} else {
			partialQty = originalQty * cumulativeRatio / 100.0
			if partialQty > quantity {
				partialQty = quantity
			}
		}
		if partialQty <= 0 {
			return false
		}

		caps := at.GetProtectionCapabilities()
		if caps.SupportsNativePartialTrailing {
			switch exchange {
			case "binance":
				binanceTrader, ok := at.trader.(interface {
					SetTrailingStopLoss(symbol string, positionSide string, activationPrice float64, callbackRate float64, quantity float64) error
					CancelTrailingStopOrders(symbol string) error
				})
				if ok {
					binanceCallbackPercent := priceBasedCallbackRatio * 100.0
					if binanceCallbackPercent < 0.1 {
						binanceCallbackPercent = 0.1
					}
					if binanceCallbackPercent > 10 {
						binanceCallbackPercent = 10
					}
					if err := binanceTrader.SetTrailingStopLoss(symbol, positionSide, activationPrice, binanceCallbackPercent, partialQty); err == nil {
						at.setProtectionState(symbol, side, "native_partial_trailing_armed")
						logger.Infof("🟣 Native partial trailing drawdown armed: %s %s | activation=%.6f callback=%.4f close=%.1f%%(cumul) qty=%.4f stage=%s", symbol, side, activationPrice, binanceCallbackPercent, cumulativeRatio, partialQty, rule.StageName)
						at.cancelImmediateTrailing(symbol, side)
						return true
					} else {
						logger.Infof("❌ Native partial trailing drawdown apply failed (%s %s, binance): %v", symbol, side, err)
					}
				}
			case "bitget":
				bitgetTrader, ok := at.trader.(interface {
					SetTrailingStopLoss(symbol string, positionSide string, activationPrice float64, callbackRate float64, quantity float64) error
					CancelTrailingStopOrders(symbol string) error
				})
				if ok {
					bitgetCallbackPercent := priceBasedCallbackRatio * 100.0
					if bitgetCallbackPercent < 0.1 {
						bitgetCallbackPercent = 0.1
					}
					if bitgetCallbackPercent > 10 {
						bitgetCallbackPercent = 10
					}
					if err := bitgetTrader.SetTrailingStopLoss(symbol, positionSide, activationPrice, bitgetCallbackPercent, partialQty); err == nil {
						at.setProtectionState(symbol, side, "native_partial_trailing_armed")
						logger.Infof("🟣 Native partial trailing drawdown armed: %s %s | activation=%.6f callback=%.4f close=%.1f%%(cumul) qty=%.4f stage=%s", symbol, side, activationPrice, bitgetCallbackPercent, cumulativeRatio, partialQty, rule.StageName)
						at.cancelImmediateTrailing(symbol, side)
						return true
					} else {
						logger.Infof("❌ Native partial trailing drawdown apply failed (%s %s, bitget): %v", symbol, side, err)
					}
				}
			case "okx":
				okxTrader, ok := at.trader.(interface {
					SetTrailingStopLoss(symbol string, positionSide string, activationPrice float64, callbackRate float64, quantity float64) error
					CancelTrailingStopOrders(symbol string) error
				})
				if ok {
					okxCallbackRatio := priceBasedCallbackRatio
					if okxCallbackRatio < 0.001 {
						okxCallbackRatio = 0.001
					}
					if okxCallbackRatio > 1 {
						okxCallbackRatio = 1
					}
					var existingTier *nativeTrailingOrder
					oldTrailingCount := 0
					qtyTarget := partialQty
					plannedCallbackRate := okxCallbackRatio
					if openOrders, err := at.trader.GetOpenOrders(symbol); err == nil {
						var plannedActivationPrice float64
						existingTier, qtyTarget, plannedActivationPrice, plannedCallbackRate = at.findEquivalentPartialTrailingOrder(symbol, side, rule, entryPrice, openOrders)
						if existingTier == nil {
							existingTier = at.findPartialTrailingReplacementCandidate(side, openOrders, qtyTarget, plannedActivationPrice, plannedCallbackRate)
						}
						for _, order := range openOrders {
							if order.PositionSide != "" && !strings.EqualFold(order.PositionSide, strings.ToUpper(side)) {
								continue
							}
							if strings.Contains(strings.ToUpper(order.Type), "TRAILING") {
								oldTrailingCount++
							}
						}
					}
					if tagged, ok := at.trader.(interface {
						SetTrailingStopLossTaggedWithID(symbol string, positionSide string, activationPrice float64, callbackRate float64, quantity float64, reasonTag string) (string, error)
						CancelTrailingStopOrdersByIDs(symbol string, orderIDs []string) error
					}); ok {
						placedOrderID, err := tagged.SetTrailingStopLossTaggedWithID(symbol, positionSide, activationPrice, okxCallbackRatio, partialQty, "native_trailing")
						if err == nil {
							verified := false
							for attempt := 1; !verified && attempt <= protectionVerifyMaxAttempts; attempt++ {
								at.sleepForVerification(protectionVerifyDelay)
								openOrders, err := at.trader.GetOpenOrders(symbol)
								if err == nil {
									trailingCount := 0
									_, qtyTarget, _, plannedCallbackRate := at.findEquivalentPartialTrailingOrder(symbol, side, rule, entryPrice, openOrders)
									for _, order := range openOrders {
										if order.PositionSide != "" && !strings.EqualFold(order.PositionSide, strings.ToUpper(side)) {
											continue
										}
										if !strings.Contains(strings.ToUpper(order.Type), "TRAILING") {
											continue
										}
										trailingCount++
										if existingTier != nil && order.OrderID == existingTier.OrderID {
											continue
										}
										qtyTolerance := math.Max(0.0001, qtyTarget*0.1)
										if math.Abs(order.Quantity-qtyTarget) <= qtyTolerance && math.Abs(order.CallbackRate-plannedCallbackRate) <= 0.0002 {
											verified = true
											break
										}
									}
									if !verified && trailingCount > oldTrailingCount {
										verified = true
									}
									if verified {
										break
									}
								}
							}
							if verified {
								newOrderID := placedOrderID
								if newOrderID == "" {
									if openOrders, err := at.trader.GetOpenOrders(symbol); err == nil {
										newOrderID = findNewestMatchingTrailingOrderID(openOrders, strings.ToUpper(side), existingTier, qtyTarget, plannedCallbackRate)
									}
								}
								if existingTier != nil && existingTier.OrderID != "" {
									if err := tagged.CancelTrailingStopOrdersByIDs(symbol, []string{existingTier.OrderID}); err != nil {
										logger.Infof("⚠️ Failed to cancel replaced native partial trailing tier (%s %s, okx): %v", symbol, side, err)
									}
								}
								at.setProtectionState(symbol, side, "native_partial_trailing_armed")
								at.persistDynamicProtectionRecordWithDetails(symbol, side, "native_partial_trailing", stableDrawdownRuleFingerprint(entryPrice, rule), cumulativeRatio, "armed", newOrderID, activationPrice, okxCallbackRatio, partialQty)
								if at.nativeTrailingArmTime != nil {
									at.nativeTrailingArmTime[stableDrawdownRuleFingerprint(entryPrice, rule)] = time.Now()
								}
								logger.Infof("🟣 Native partial trailing drawdown armed: %s %s | activation=%.6f callback=%.6f close=%.1f%%(cumul) qty=%.4f stage=%s", symbol, side, activationPrice, okxCallbackRatio, cumulativeRatio, partialQty, rule.StageName)
								at.cancelImmediateTrailing(symbol, side)
								return true
							}
							logger.Infof("❌ Native partial trailing drawdown verify failed (%s %s, okx): new tier not visible after placement", symbol, side)
						} else {
							logger.Infof("❌ Native partial trailing drawdown apply failed (%s %s, okx): %v", symbol, side, err)
						}
					} else if err := okxTrader.SetTrailingStopLoss(symbol, positionSide, activationPrice, okxCallbackRatio, partialQty); err == nil {
						at.setProtectionState(symbol, side, "native_partial_trailing_armed")
						if at.nativeTrailingArmTime != nil {
							at.nativeTrailingArmTime[stableDrawdownRuleFingerprint(entryPrice, rule)] = time.Now()
						}
						logger.Infof("🟣 Native partial trailing drawdown armed: %s %s | activation=%.6f callback=%.6f close=%.1f%%(cumul) qty=%.4f stage=%s", symbol, side, activationPrice, okxCallbackRatio, cumulativeRatio, partialQty, rule.StageName)
						at.cancelImmediateTrailing(symbol, side)
						return true
					} else {
						logger.Infof("❌ Native partial trailing drawdown apply failed (%s %s, okx): %v", symbol, side, err)
					}
				}
			}
			// Native-partial-capable exchanges should not silently fall back to managed TP here.
			return false
		}

		candidate := buildManagedPartialDrawdownPlanCandidate(entryPrice, positionAction, rule)
		if candidate == nil || !at.canApplyManagedPartialDrawdownPlan(candidate) {
			return false
		}
		logger.Infof("🟣 Managed partial drawdown armed: %s %s | activation=%.6f callbackRatio=%.6f close=%.1f%%",
			symbol, side, activationPrice, priceBasedCallbackRatio, rule.CloseRatioPct)
		if err := at.placeAndVerifyProtectionPlanWithRetry(symbol, positionSide, quantity, candidate); err != nil {
			logger.Infof("❌ Managed partial drawdown apply failed (%s %s): %v", symbol, side, err)
			return false
		}
		at.setProtectionState(symbol, side, "managed_partial_drawdown_armed")
		return true
	}

	if !isPartial && currentState == "native_partial_trailing_armed" {
		if openOrders, err := at.trader.GetOpenOrders(symbol); err == nil {
			ids := make([]string, 0)
			for _, order := range openOrders {
				if order.PositionSide != "" && !strings.EqualFold(order.PositionSide, strings.ToUpper(side)) {
					continue
				}
				if strings.Contains(strings.ToUpper(order.Type), "TRAILING") {
					ids = append(ids, order.OrderID)
				}
			}
			if len(ids) > 0 {
				if tagged, ok := at.trader.(interface {
					CancelTrailingStopOrdersByIDs(symbol string, orderIDs []string) error
				}); ok {
					if err := tagged.CancelTrailingStopOrdersByIDs(symbol, ids); err != nil {
						logger.Infof("⚠️ Failed to collapse old native partial trailing tiers before full-tier migration (%s %s): %v", symbol, side, err)
					} else {
						logger.Infof("🧹 Collapsed %d old native partial trailing tier(s) before full-tier migration: %s %s", len(ids), symbol, side)
					}
				}
			}
		}
	}

	placedOrderID := ""
	switch exchange {
	case "binance":
		binanceTrader, ok := at.trader.(interface {
			SetTrailingStopLoss(symbol string, positionSide string, activationPrice float64, callbackRate float64, quantity float64) error
			CancelTrailingStopOrders(symbol string) error
		})
		if !ok {
			return false
		}
		binanceCallbackPercent := priceBasedCallbackRatio * 100.0
		if binanceCallbackPercent < 0.1 {
			binanceCallbackPercent = 0.1
		}
		if binanceCallbackPercent > 10 {
			binanceCallbackPercent = 10
		}
		if err := binanceTrader.SetTrailingStopLoss(symbol, positionSide, activationPrice, binanceCallbackPercent, 0); err != nil {
			logger.Infof("❌ Native trailing drawdown apply failed (%s %s): %v", symbol, side, err)
			return false
		}
	case "bitget":
		bitgetTrader, ok := at.trader.(interface {
			SetTrailingStopLoss(symbol string, positionSide string, activationPrice float64, callbackRate float64, quantity float64) error
			CancelTrailingStopOrders(symbol string) error
		})
		if !ok {
			return false
		}
		bitgetCallbackPercent := priceBasedCallbackRatio * 100.0
		if bitgetCallbackPercent < 0.1 {
			bitgetCallbackPercent = 0.1
		}
		if bitgetCallbackPercent > 10 {
			bitgetCallbackPercent = 10
		}
		if err := bitgetTrader.SetTrailingStopLoss(symbol, positionSide, activationPrice, bitgetCallbackPercent, 0); err != nil {
			logger.Infof("❌ Native trailing drawdown apply failed (%s %s): %v", symbol, side, err)
			return false
		}
	case "okx":
		okxTrader, ok := at.trader.(interface {
			SetTrailingStopLoss(symbol string, positionSide string, activationPrice float64, callbackRate float64, quantity float64) error
			CancelTrailingStopOrders(symbol string) error
		})
		if !ok {
			return false
		}
		okxCallbackRatio := priceBasedCallbackRatio
		if okxCallbackRatio < 0.001 {
			okxCallbackRatio = 0.001
		}
		if okxCallbackRatio > 1 {
			okxCallbackRatio = 1
		}
		if tagged, ok := at.trader.(interface {
			SetTrailingStopLossTaggedWithID(symbol string, positionSide string, activationPrice float64, callbackRate float64, quantity float64, reasonTag string) (string, error)
			CancelTrailingStopOrdersByIDs(symbol string, orderIDs []string) error
		}); ok {
			var err error
			placedOrderID, err = tagged.SetTrailingStopLossTaggedWithID(symbol, positionSide, activationPrice, okxCallbackRatio, 0, "native_trailing")
			if err != nil {
				logger.Infof("❌ Native trailing drawdown apply failed (%s %s): %v", symbol, side, err)
				return false
			}
			if staleFullTrailing != nil && staleFullTrailing.OrderID != "" {
				if err := tagged.CancelTrailingStopOrdersByIDs(symbol, []string{staleFullTrailing.OrderID}); err != nil {
					logger.Infof("⚠️ Failed to cancel replaced native full trailing order (%s %s, okx): %v", symbol, side, err)
				}
			}
		} else if err := okxTrader.SetTrailingStopLoss(symbol, positionSide, activationPrice, okxCallbackRatio, 0); err != nil {
			logger.Infof("❌ Native trailing drawdown apply failed (%s %s): %v", symbol, side, err)
			return false
		}
	default:
		return false
	}

	if isPartial {
		at.setProtectionState(symbol, side, "managed_partial_drawdown_armed")
		logger.Infof("🟣 Managed partial drawdown armed: %s %s | activation=%.6f callbackRatio=%.6f close=%.1f%%", symbol, side, activationPrice, priceBasedCallbackRatio, rule.CloseRatioPct)
	} else {
		at.setProtectionState(symbol, side, "native_trailing_armed")
		at.persistDynamicProtectionRecordWithDetails(symbol, side, "native_trailing", stableDrawdownRuleFingerprint(entryPrice, rule), rule.CloseRatioPct, "armed", placedOrderID, activationPrice, priceBasedCallbackRatio, 0)
		logger.Infof("🟣 Native trailing drawdown armed: %s %s | activation=%.6f callbackRatio=%.6f", symbol, side, activationPrice, priceBasedCallbackRatio)
	}
	at.cancelImmediateTrailing(symbol, side)
	return true
}

func (at *AutoTrader) matchDrawdownArmRule(currentPnLPct float64, rules []store.DrawdownTakeProfitRule) *store.DrawdownTakeProfitRule {
	var matched *store.DrawdownTakeProfitRule
	for i := range rules {
		rule := normalizeDrawdownRule(rules[i])
		if currentPnLPct < rule.MinProfitPct {
			continue
		}
		if matched == nil || rule.MinProfitPct > matched.MinProfitPct {
			matched = &rule
		}
	}
	return matched
}

func (at *AutoTrader) matchDrawdownRule(currentPnLPct, drawdownPct float64, rules []store.DrawdownTakeProfitRule) *store.DrawdownTakeProfitRule {
	var matched *store.DrawdownTakeProfitRule
	for i := range rules {
		rule := rules[i]
		if currentPnLPct < rule.MinProfitPct || !isDrawdownThresholdMet(currentPnLPct, drawdownPct, rule) {
			continue
		}
		if matched == nil || rule.MinProfitPct > matched.MinProfitPct ||
			(rule.MinProfitPct == matched.MinProfitPct && rule.MaxDrawdownPct > matched.MaxDrawdownPct) {
			matched = &rule
		}
	}
	return matched
}

func (at *AutoTrader) getTriggeredDrawdownRules(currentPnLPct, drawdownPct float64, rules []store.DrawdownTakeProfitRule) []store.DrawdownTakeProfitRule {
	matched := make([]store.DrawdownTakeProfitRule, 0, len(rules))
	for _, rule := range rules {
		if currentPnLPct < rule.MinProfitPct || !isDrawdownThresholdMet(currentPnLPct, drawdownPct, rule) {
			continue
		}
		matched = append(matched, rule)
	}
	return matched
}

func (at *AutoTrader) getDrawdownConfigSource(symbol, side string) string {
	at.protectionStateMutex.RLock()
	defer at.protectionStateMutex.RUnlock()
	if symbol != "" || side != "" {
		key := positionKey(symbol, side)
		if src, ok := at.drawdownSource[key]; ok && src != "" {
			return src
		}
		if len(at.drawdownAIRules[key]) > 0 {
			return "ai_decision"
		}
	}
	for _, src := range at.drawdownSource {
		if src == "ai_decision" {
			return src
		}
	}
	if len(at.getActiveDrawdownRules()) == 0 {
		return "none"
	}
	return "strategy"
}

func (at *AutoTrader) getBreakEvenConfigSource(symbol, side string) string {
	at.breakEvenStateMutex.RLock()
	defer at.breakEvenStateMutex.RUnlock()
	if symbol != "" || side != "" {
		if src, ok := at.breakEvenSource[positionKey(symbol, side)]; ok && src != "" {
			return src
		}
	}
	for _, src := range at.breakEvenSource {
		if src == "ai_decision" {
			return src
		}
	}
	return "strategy"
}

func (at *AutoTrader) getActiveBreakEvenConfigForPlan(plan *ProtectionPlan) *store.BreakEvenStopConfig {
	if at != nil && at.config.StrategyConfig != nil && at.config.StrategyConfig.Protection.BreakEvenStop.Mode == store.ProtectionModeAI {
		if plan != nil && plan.BreakEvenConfig != nil {
			cfg := *plan.BreakEvenConfig
			if cfg.Enabled && cfg.TriggerValue > 0 {
				// Negative OffsetPct is allowed (park the stop slightly losing-side).
				return &cfg
			}
		}
		return at.getActiveBreakEvenConfig()
	}
	return at.getActiveBreakEvenConfig()
}

func (at *AutoTrader) getActiveBreakEvenConfig() *store.BreakEvenStopConfig {
	rules := at.getActiveBreakEvenRules()
	if len(rules) == 0 || at == nil || at.config.StrategyConfig == nil {
		return nil
	}
	cfg := at.config.StrategyConfig.Protection.BreakEvenStop
	cfg.TriggerMode = rules[0].TriggerMode
	cfg.TriggerValue = rules[0].TriggerValue
	cfg.OffsetPct = rules[0].OffsetPct
	return &cfg
}

func (at *AutoTrader) getActiveBreakEvenRules() []store.BreakEvenStopRule {
	if at == nil || at.config.StrategyConfig == nil {
		return nil
	}
	cfg := at.config.StrategyConfig.Protection.BreakEvenStop
	if !cfg.Enabled {
		return nil
	}
	rules := cfg.Rules
	if len(rules) == 0 && cfg.TriggerValue > 0 {
		rules = []store.BreakEvenStopRule{{TriggerMode: cfg.TriggerMode, TriggerValue: cfg.TriggerValue, OffsetPct: cfg.OffsetPct, CloseRatioPct: 100, StageName: "BE1"}}
	}
	out := make([]store.BreakEvenStopRule, 0, len(rules))
	for _, rule := range rules {
		if rule.TriggerMode == "" {
			rule.TriggerMode = cfg.TriggerMode
		}
		if rule.TriggerValue <= 0 {
			continue
		}
		if rule.TriggerMode != store.BreakEvenTriggerProfitPct && rule.TriggerMode != store.BreakEvenTriggerRMultiple {
			continue
		}
		// Negative OffsetPct is allowed: it parks the break-even stop slightly on
		// the losing side (e.g. cover fees + a noise buffer) instead of at exact
		// break-even, so the stop isn't repeatedly swept at the entry price.
		if rule.CloseRatioPct <= 0 {
			rule.CloseRatioPct = 100
		}
		out = append(out, rule)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TriggerValue < out[j].TriggerValue })
	return out
}

// resolveBreakEvenRulesForPosition converts r_multiple rules to profit_pct equivalent
// by computing the SL distance from open stop-loss orders.
func resolveBreakEvenRulesForPosition(rules []store.BreakEvenStopRule, entryPrice float64, symbol, side string, at *AutoTrader) []store.BreakEvenStopRule {
	hasRMultiple := false
	for _, r := range rules {
		if r.TriggerMode == store.BreakEvenTriggerRMultiple {
			hasRMultiple = true
			break
		}
	}
	if !hasRMultiple {
		return rules
	}

	// Find SL distance from open orders
	slDistancePct := 0.0
	if openOrders, err := at.trader.GetOpenOrders(symbol); err == nil {
		positionSide := strings.ToUpper(side)
		for _, order := range openOrders {
			orderSide := strings.ToUpper(order.PositionSide)
			if orderSide != positionSide {
				continue
			}
			if !looksLikeStopLoss(order) {
				continue
			}
			stopPrice := order.StopPrice
			if stopPrice <= 0 {
				continue
			}
			dist := math.Abs(stopPrice-entryPrice) / entryPrice * 100
			if dist > slDistancePct {
				slDistancePct = dist
			}
		}
	}

	if slDistancePct <= 0 {
		// Fallback: filter out r_multiple rules if we can't determine SL distance
		out := make([]store.BreakEvenStopRule, 0, len(rules))
		for _, r := range rules {
			if r.TriggerMode == store.BreakEvenTriggerProfitPct {
				out = append(out, r)
			}
		}
		return out
	}

	// Convert r_multiple to profit_pct: 1R profit = slDistancePct
	out := make([]store.BreakEvenStopRule, 0, len(rules))
	for _, r := range rules {
		if r.TriggerMode == store.BreakEvenTriggerRMultiple {
			r.TriggerValue = r.TriggerValue * slDistancePct
			r.TriggerMode = store.BreakEvenTriggerProfitPct
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TriggerValue < out[j].TriggerValue })
	return out
}

func (at *AutoTrader) applyBreakEvenStops(symbol, side string, quantity, entryPrice, currentPnLPct float64, rules []store.BreakEvenStopRule) error {
	if len(rules) == 0 {
		return nil
	}

	positionSide := strings.ToUpper(side)
	var openOrders []OpenOrder
	var openOrdersErr error
	openOrdersFetched := false

	for idx, rule := range rules {
		if currentPnLPct < rule.TriggerValue {
			continue
		}
		if rule.CloseRatioPct <= 0 {
			rule.CloseRatioPct = 100
		}

		// Cumulative quantity: sum of this tier and all lower tiers, capped at 100%
		cumulativeRatio := 0.0
		for i := 0; i <= idx; i++ {
			r := rules[i]
			if r.CloseRatioPct <= 0 {
				r.CloseRatioPct = 100
			}
			cumulativeRatio += r.CloseRatioPct
		}
		if cumulativeRatio > 100 {
			cumulativeRatio = 100
		}
		ruleQty := quantity * cumulativeRatio / 100.0
		if ruleQty <= 0 {
			continue
		}

		stage := rule.StageName
		if stage == "" {
			stage = fmt.Sprintf("BE%d", idx+1)
		}

		newBEPrice := calculateBreakEvenStopPrice(side, entryPrice, rule.OffsetPct)
		if newBEPrice <= 0 {
			continue
		}

		// Lazy-fetch open orders (once per call)
		if !openOrdersFetched {
			openOrders, openOrdersErr = at.trader.GetOpenOrders(symbol)
			openOrdersFetched = true
		}
		if openOrdersErr != nil {
			continue
		}

		// Check if this tier already has a matching exchange order
		if hasMatchingBreakEvenOrder(openOrders, positionSide, newBEPrice) {
			// Ensure armed state is persisted
			at.persistDynamicProtectionRecordWithDetails(symbol, side, "break_even_stop", fmt.Sprintf("%.8f|%.8f|%.4f|%.4f|%s", entryPrice, quantity, rule.TriggerValue, rule.OffsetPct, stage), 0, "armed", "", newBEPrice, 0, ruleQty)
			continue
		}

		// Place this tier's stop order
		cfg := store.BreakEvenStopConfig{Enabled: true, Mode: store.ProtectionModeManual, TriggerMode: rule.TriggerMode, TriggerValue: rule.TriggerValue, OffsetPct: rule.OffsetPct}
		if err := at.applyBreakEvenStop(symbol, side, ruleQty, entryPrice, currentPnLPct, cfg, stage); err != nil {
			logger.Warnf("❌ BE tier %s apply failed (%s %s): %v", stage, symbol, side, err)
			continue
		}
	}

	// Set overall armed state if any tier was placed
	at.setBreakEvenState(symbol, side, "armed")
	return nil
}

func (at *AutoTrader) applyBreakEvenStop(symbol, side string, quantity, entryPrice, currentPnLPct float64, cfg store.BreakEvenStopConfig, stageName ...string) error {
	if currentPnLPct < cfg.TriggerValue || entryPrice <= 0 || quantity <= 0 {
		return nil
	}
	if !at.verifyLivePositionForProtection(symbol, side, "break-even stop") {
		return nil
	}

	caps := at.GetProtectionCapabilities()
	if !caps.NativeStopLoss {
		return fmt.Errorf("exchange %s does not support native stop loss for break-even", at.exchange)
	}

	stage := "BE"
	if len(stageName) > 0 && stageName[0] != "" {
		stage = stageName[0]
	}
	breakEvenPrice := calculateBreakEvenStopPrice(side, entryPrice, cfg.OffsetPct)
	if breakEvenPrice <= 0 {
		return fmt.Errorf("invalid break-even stop price calculated for %s %s", symbol, side)
	}

	positionSide := strings.ToUpper(side)
	// If a matching break-even stop is already live (for example after a restart
	// before local BE state has been restored), treat it as armed instead of
	// placing another native stop. Only match orders tagged as BE, not ladder SL
	// that happens to be at the same price.
	if openOrders, err := at.trader.GetOpenOrders(symbol); err == nil {
		if hasMatchingBreakEvenOrder(openOrders, positionSide, breakEvenPrice) {
			logger.Infof("🟠 Break-even stop already live: %s %s | stop=%.6f", symbol, side, breakEvenPrice)
			at.persistDynamicProtectionRecordWithDetails(symbol, side, "break_even_stop", fmt.Sprintf("%.8f|%.8f|%.4f|%.4f|%s", entryPrice, quantity, cfg.TriggerValue, cfg.OffsetPct, stage), 0, "armed", "", breakEvenPrice, 0, quantity)
			return nil
		}
	} else {
		logger.Warnf("⚠️ Break-even live-order precheck failed (%s %s): %v", symbol, side, err)
	}

	// Break-even stop is managed independently. Do not cancel existing ladder/full stop-loss
	// orders here, otherwise we destroy the long-term stop-loss protection stack.
	// If exchanges later support per-order tags / amend-by-id, we can target only prior
	// break-even stops. For now, preserve existing SL orders and add break-even separately.
	if okxTrader, ok := at.trader.(interface {
		SetStopLoss(symbol string, positionSide string, quantity, stopPrice float64) error
		SetStopLossTagged(symbol string, positionSide string, quantity, stopPrice float64, reasonTag string) error
	}); ok {
		if err := okxTrader.SetStopLossTagged(symbol, positionSide, quantity, breakEvenPrice, "break_even_stop"); err != nil {
			return fmt.Errorf("failed to set break-even stop loss: %w", err)
		}
		at.recordProtectionIntent(symbol, positionSide, "break_even_stop", quantity, breakEvenPrice)
	} else if err := at.trader.SetStopLoss(symbol, positionSide, quantity, breakEvenPrice); err != nil {
		return fmt.Errorf("failed to set break-even stop loss: %w", err)
	}

	var verified bool
	for attempt := 1; attempt <= protectionVerifyMaxAttempts; attempt++ {
		at.sleepForVerification(protectionVerifyDelay)
		openOrders, err := at.trader.GetOpenOrders(symbol)
		if err != nil {
			return fmt.Errorf("failed to verify break-even stop loss: %w", err)
		}
		if hasMatchingProtectionOrder(openOrders, positionSide, false, breakEvenPrice) {
			verified = true
			logger.Infof("✅ Break-even stop verified: %s %s | stop=%.6f (attempt %d/%d)",
				symbol, side, breakEvenPrice, attempt, protectionVerifyMaxAttempts)
			break
		}
		if attempt < protectionVerifyMaxAttempts {
			logger.Infof("⏳ Break-even verification pending (attempt %d/%d), retrying...", attempt, protectionVerifyMaxAttempts)
		}
	}
	if !verified {
		return fmt.Errorf("break-even stop verification failed for %s %s at %.6f after %d attempts", symbol, side, breakEvenPrice, protectionVerifyMaxAttempts)
	}

	logger.Infof("🟠 Break-even stop applied: %s %s | stage=%s trigger=%.2f%% current=%.2f%% qty=%.6f stop=%.6f",
		symbol, side, stage, cfg.TriggerValue, currentPnLPct, quantity, breakEvenPrice)
	at.persistDynamicProtectionRecordWithDetails(symbol, side, "break_even_stop", fmt.Sprintf("%.8f|%.8f|%.4f|%.4f|%s", entryPrice, quantity, cfg.TriggerValue, cfg.OffsetPct, stage), 0, "armed", "", breakEvenPrice, 0, quantity)
	return nil
}

func calculateBreakEvenStopPrice(side string, entryPrice, offsetPct float64) float64 {
	if entryPrice <= 0 {
		return 0
	}
	move := offsetPct / 100.0
	switch strings.ToLower(side) {
	case "long":
		return entryPrice * (1 + move)
	case "short":
		return entryPrice * (1 - move)
	default:
		return 0
	}
}

func (at *AutoTrader) closePositionBySide(symbol, side string, quantity float64) error {
	return at.closePositionByReason(symbol, side, quantity, "close_by_side")
}

// closeOrderSkipped reports whether a close-order result map represents a
// non-execution (no order was actually sent) rather than a filled order. This
// covers NO_POSITION, SKIPPED and POSITION_DUST statuses returned by the
// exchange close paths, so callers can avoid logging a false "succeeded" line
// and avoid pointless retries.
func closeOrderSkipped(order map[string]interface{}) (bool, string) {
	if order == nil {
		return false, ""
	}
	status, _ := order["status"].(string)
	switch status {
	case "NO_POSITION", "SKIPPED", "POSITION_DUST":
		return true, status
	}
	return false, ""
}

func (at *AutoTrader) closePositionByReason(symbol, side string, quantity float64, closeReason string) error {
	type taggedCloser interface {
		CloseLongTagged(symbol string, quantity float64, reasonTag string) (map[string]interface{}, error)
		CloseShortTagged(symbol string, quantity float64, reasonTag string) (map[string]interface{}, error)
	}

	switch strings.ToLower(side) {
	case "long":
		var (
			order map[string]interface{}
			err   error
		)
		if tagged, ok := at.trader.(taggedCloser); ok && closeReason != "" {
			order, err = tagged.CloseLongTagged(symbol, quantity, closeReason)
		} else {
			order, err = at.trader.CloseLong(symbol, quantity)
		}
		if err != nil {
			return err
		}
		if skipped, reason := closeOrderSkipped(order); skipped {
			logger.Warnf("⚠️ Close long not executed (%s): %s — %v", symbol, reason, order["message"])
			return nil
		}
		logger.Infof("✅ Close long position succeeded, order ID: %v", order["orderId"])
		at.persistCloseReasonFromOrderResult(order, closeReason)
		at.recordCloseIntentFromOrderResult(order, symbol, "LONG", closeReason, quantity)
	case "short":
		var (
			order map[string]interface{}
			err   error
		)
		if tagged, ok := at.trader.(taggedCloser); ok && closeReason != "" {
			order, err = tagged.CloseShortTagged(symbol, quantity, closeReason)
		} else {
			order, err = at.trader.CloseShort(symbol, quantity)
		}
		if err != nil {
			return err
		}
		if skipped, reason := closeOrderSkipped(order); skipped {
			logger.Warnf("⚠️ Close short not executed (%s): %s — %v", symbol, reason, order["message"])
			return nil
		}
		logger.Infof("✅ Close short position succeeded, order ID: %v", order["orderId"])
		at.persistCloseReasonFromOrderResult(order, closeReason)
		at.recordCloseIntentFromOrderResult(order, symbol, "SHORT", closeReason, quantity)
	default:
		return fmt.Errorf("unknown position direction: %s", side)
	}

	return nil
}

func (at *AutoTrader) persistCloseReasonFromOrderResult(order map[string]interface{}, closeReason string) {
	if at.store == nil || closeReason == "" || order == nil {
		return
	}
	var orderID string
	switch v := order["orderId"].(type) {
	case int64:
		orderID = fmt.Sprintf("%d", v)
	case float64:
		orderID = fmt.Sprintf("%.0f", v)
	case string:
		orderID = v
	default:
		orderID = fmt.Sprintf("%v", v)
	}
	if orderID == "" || orderID == "<nil>" {
		return
	}
	_ = at.store.Position().UpdateCloseReasonByExitOrderID(at.id, orderID, closeReason)
	_ = at.store.PositionClose().UpdateReasonByOrderID(at.id, orderID, closeReason, closeReason)
}

// recordCloseIntentFromOrderResult writes a durable close-intent ledger row so the
// asynchronous OKX fill-sync can attribute the resulting close fill to the real
// mechanism (the reason) instead of the bare close_long/close_short action. The
// exchange order id from the order result is the deterministic match key; the
// sync path also falls back to a trader+symbol+side time-window match.
func (at *AutoTrader) recordCloseIntentFromOrderResult(order map[string]interface{}, symbol, side, closeReason string, quantity float64) {
	if at.store == nil || closeReason == "" {
		return
	}
	orderID := ""
	if order != nil {
		switch v := order["orderId"].(type) {
		case int64:
			orderID = fmt.Sprintf("%d", v)
		case float64:
			orderID = fmt.Sprintf("%.0f", v)
		case string:
			orderID = v
		default:
			if v != nil {
				orderID = fmt.Sprintf("%v", v)
			}
		}
	}
	if orderID == "<nil>" {
		orderID = ""
	}
	if err := at.store.CloseIntent().Record(at.id, at.exchangeID, market.Normalize(symbol), side, closeReason, quantity, at.cycleNumber, orderID); err != nil {
		logger.Warnf("⚠️ Failed to record close intent for %s %s (%s): %v", symbol, side, closeReason, err)
	}
}

// recordProtectionIntent writes a placement-time close-intent for an
// exchange-native protection order (Binance STOP/TP), keyed by its trigger price.
// This is what brings Binance protection attribution to OKX parity: unlike OKX
// (bot MARKET close, resolved by order id), Binance protection fires exchange-side
// and its triggered fill carries a NEW order id the bot never saw — but the fill
// price equals the trigger, so a trigger-price intent recorded here lets the sync
// path attribute the mechanism deterministically, even after the conditional
// order has aged out of the exchange (origType lookup fails). positionSide is
// LONG/SHORT. Safe no-op when triggerPrice<=0 or store is nil.
func (at *AutoTrader) recordProtectionIntent(symbol, positionSide, mechanism string, quantity, triggerPrice float64) {
	if at.store == nil || mechanism == "" || triggerPrice <= 0 {
		return
	}
	if err := at.store.CloseIntent().RecordProtection(
		at.id, at.exchangeID, market.Normalize(symbol), positionSide, mechanism, quantity, triggerPrice, at.cycleNumber, "",
	); err != nil {
		logger.Warnf("⚠️ Failed to record protection intent for %s %s (%s @ %.6f): %v", symbol, positionSide, mechanism, triggerPrice, err)
	}
}

// emergencyClosePosition emergency close position function
func (at *AutoTrader) emergencyClosePosition(symbol, side string) error {
	return at.closePositionByReason(symbol, side, 0, "emergency_protection_close")
}

// GetPeakPnLCache gets peak profit cache
func (at *AutoTrader) GetPeakPnLCache() map[string]float64 {
	at.peakPnLCacheMutex.RLock()
	defer at.peakPnLCacheMutex.RUnlock()

	// Return a copy of the cache
	cache := make(map[string]float64)
	for k, v := range at.peakPnLCache {
		cache[k] = v
	}
	return cache
}

// UpdatePeakPnL updates peak profit cache
func (at *AutoTrader) UpdatePeakPnL(symbol, side string, currentPnLPct float64) {
	at.peakPnLCacheMutex.Lock()
	posKey := symbol + "_" + side
	changed := false
	if peak, exists := at.peakPnLCache[posKey]; exists {
		// Update peak (if long, take larger value; if short, currentPnLPct is negative, also compare)
		if currentPnLPct > peak {
			at.peakPnLCache[posKey] = currentPnLPct
			changed = true
		}
	} else {
		// First time recording
		at.peakPnLCache[posKey] = currentPnLPct
		changed = true
	}
	at.peakPnLCacheMutex.Unlock()

	// Persist only when the high-water mark actually moved, so the value survives
	// a restart. Writes are infrequent (peak only ever rises) so DB load is light.
	if changed && at.store != nil {
		if err := at.store.SavePeakPnL(at.id, posKey, currentPnLPct); err != nil {
			logger.Warnf("⚠️ Failed to persist peak PnL for %s: %v", posKey, err)
		}
	}
}

// ClearPeakPnLCache clears peak cache for specified position
func (at *AutoTrader) ClearPeakPnLCache(symbol, side string) {
	at.peakPnLCacheMutex.Lock()
	posKey := symbol + "_" + side
	delete(at.peakPnLCache, posKey)
	at.peakPnLCacheMutex.Unlock()

	if at.store != nil {
		if err := at.store.DeletePeakPnL(at.id, posKey); err != nil {
			logger.Warnf("⚠️ Failed to delete persisted peak PnL for %s: %v", posKey, err)
		}
	}
}

// ============================================================================
// Risk Control Helpers
// ============================================================================

// isBTCETH checks if a symbol is BTC or ETH
func isBTCETH(symbol string) bool {
	symbol = strings.ToUpper(symbol)
	return strings.HasPrefix(symbol, "BTC") || strings.HasPrefix(symbol, "ETH")
}

// enforcePositionValueRatio checks and enforces position value ratio limits (CODE ENFORCED)
// Returns the adjusted position size (capped if necessary) and whether the position was capped
// positionSizeUSD: the original position size in USD
// equity: the account equity
// symbol: the trading symbol
func (at *AutoTrader) enforcePositionValueRatio(positionSizeUSD float64, equity float64, symbol string) (float64, bool) {
	if at.config.StrategyConfig == nil {
		return positionSizeUSD, false
	}

	riskControl := at.config.StrategyConfig.RiskControl

	// Get the appropriate position value ratio limit
	var maxPositionValueRatio float64
	if isBTCETH(symbol) {
		maxPositionValueRatio = riskControl.BTCETHMaxPositionValueRatio
		if maxPositionValueRatio <= 0 {
			maxPositionValueRatio = 5.0 // Default: 5x for BTC/ETH
		}
	} else {
		maxPositionValueRatio = riskControl.AltcoinMaxPositionValueRatio
		if maxPositionValueRatio <= 0 {
			maxPositionValueRatio = 1.0 // Default: 1x for altcoins
		}
	}

	// Calculate max allowed position value = equity × ratio
	maxPositionValue := equity * maxPositionValueRatio

	// Check if position size exceeds limit
	if positionSizeUSD > maxPositionValue {
		logger.Infof("  ⚠️ [RISK CONTROL] Position %.2f USDT exceeds limit (equity %.2f × %.1fx = %.2f USDT max for %s), capping",
			positionSizeUSD, equity, maxPositionValueRatio, maxPositionValue, symbol)
		return maxPositionValue, true
	}

	return positionSizeUSD, false
}

// enforceMinPositionSize checks minimum position size (CODE ENFORCED)
func (at *AutoTrader) enforceMinPositionSize(positionSizeUSD float64, symbol ...string) error {
	if at.config.StrategyConfig == nil {
		return nil
	}

	minSize := at.config.StrategyConfig.RiskControl.MinPositionSize
	if minSize <= 0 {
		minSize = 12 // Default: 12 USDT
	}
	if len(symbol) > 0 && strings.TrimSpace(symbol[0]) != "" {
		if snap := at.collectExecutionConstraintsSnapshot(symbol[0]); snap != nil {
			minSize = snap.ExecutableMinPositionUSD(minSize)
		}
	}

	if positionSizeUSD < minSize {
		return fmt.Errorf("❌ [RISK CONTROL] Position %.2f USDT below minimum executable size (%.2f USDT)", positionSizeUSD, minSize)
	}
	return nil
}

// enforceMaxPositions checks maximum positions count (CODE ENFORCED)
func (at *AutoTrader) enforceMaxPositions(currentPositionCount int) error {
	if at.config.StrategyConfig == nil {
		return nil
	}

	maxPositions := at.config.StrategyConfig.RiskControl.MaxPositions
	if maxPositions <= 0 {
		maxPositions = 3 // Default: 3 positions
	}

	if currentPositionCount >= maxPositions {
		return fmt.Errorf("❌ [RISK CONTROL] Already at max positions (%d/%d)", currentPositionCount, maxPositions)
	}
	return nil
}

// getSideFromAction converts order action to side (BUY/SELL)
func getSideFromAction(action string) string {
	switch action {
	case "open_long", "close_short":
		return "BUY"
	case "open_short", "close_long":
		return "SELL"
	default:
		return "BUY"
	}
}

func isDrawdownThresholdMet(currentPnLPct, drawdownPct float64, rule store.DrawdownTakeProfitRule) bool {
	rule = normalizeDrawdownRule(rule)
	if rule.MaxDrawdownAbsPct > 0 {
		return currentPnLPct <= rule.MinProfitPct-rule.MaxDrawdownAbsPct
	}
	return drawdownPct >= rule.MaxDrawdownPct
}

func calculateProfitBasedTrailingTriggerPrice(entryPrice float64, side string, minProfitPct float64) float64 {
	if entryPrice <= 0 || minProfitPct <= 0 {
		return 0
	}
	move := minProfitPct / 100.0
	switch strings.ToLower(side) {
	case "long":
		return entryPrice * (1 + move)
	case "short":
		return entryPrice * (1 - move)
	default:
		return 0
	}
}

// calculateProfitBasedTrailingCallbackRatio converts a drawdown rule's
// price-retracement distance into the exchange-native trailing callback ratio
// (decimal price fraction). Under the unified trailing semantics maxDrawdownPct
// IS the percentage the price may retrace from the running peak before the stop
// fires, so the callback ratio equals maxDrawdownPct/100 directly. ATR-unit
// rules are pre-resolved to an effective percent by resolveDrawdownRulesATR
// before reaching here, so this single formula covers both units.
//
// Example LONG (percent mode):
// - entry = 10.0, minProfitPct = 3.0 => activation at 10.3
// - maxDrawdownPct = 4.0 => price may retrace 4% from peak
// - callback ratio = 0.04
//
// Returns ratio in decimal form for OKX (0.001..1); adapters convert to percent
// for Binance/Bitget at the boundary.
func calculateProfitBasedTrailingCallbackRatio(entryPrice float64, side string, minProfitPct float64, maxDrawdownPct float64) float64 {
	if entryPrice <= 0 || maxDrawdownPct <= 0 {
		return 0
	}
	callback := maxDrawdownPct / 100.0
	if callback > 1 {
		callback = 1
	}
	return callback
}

func calculateAbsoluteProfitDrawdownCallbackRatio(entryPrice float64, side string, minProfitPct float64, absDrawdownPct float64) float64 {
	activationPrice := calculateProfitBasedTrailingTriggerPrice(entryPrice, side, minProfitPct)
	if entryPrice <= 0 || activationPrice <= 0 || absDrawdownPct <= 0 {
		return 0
	}
	allowedGivebackAbs := entryPrice * absDrawdownPct / 100.0
	if allowedGivebackAbs <= 0 {
		return 0
	}
	return allowedGivebackAbs / activationPrice
}

func calculateDrawdownRuleCallbackRatio(entryPrice float64, side string, rule store.DrawdownTakeProfitRule) float64 {
	rule = normalizeDrawdownRule(rule)
	if rule.MaxDrawdownAbsPct > 0 {
		return calculateAbsoluteProfitDrawdownCallbackRatio(entryPrice, side, rule.MinProfitPct, rule.MaxDrawdownAbsPct)
	}
	return calculateProfitBasedTrailingCallbackRatio(entryPrice, side, rule.MinProfitPct, rule.MaxDrawdownPct)
}

// enforceLeverageCap clamps AI-requested leverage to the strategy maximum.
// Returns the clamped leverage value.
func (at *AutoTrader) enforceLeverageCap(leverage int, symbol string) int {
	if at.config.StrategyConfig == nil {
		return leverage
	}
	rc := at.config.StrategyConfig.RiskControl
	var maxLev int
	if isBTCETH(symbol) {
		maxLev = rc.BTCETHMaxLeverage
	} else {
		maxLev = rc.AltcoinMaxLeverage
	}
	if maxLev <= 0 {
		maxLev = 5
	}
	if leverage > maxLev {
		logger.Infof("  ⚙️ Leverage clamped: AI requested %dx → capped to %dx (strategy max for %s)", leverage, maxLev, symbol)
		return maxLev
	}
	return leverage
}

// enforceMaxMarginUsage checks whether opening a new position would exceed
// the configured max margin usage ratio. Returns an error if the new margin
// would push total usage above the limit.
func (at *AutoTrader) enforceMaxMarginUsage(positionSizeUSD float64, leverage int, equity float64) error {
	if at.config.StrategyConfig == nil {
		return nil
	}
	maxUsage := at.config.StrategyConfig.RiskControl.MaxMarginUsage
	if maxUsage <= 0 {
		return nil
	}

	positions, err := at.trader.GetPositions()
	if err != nil {
		return nil
	}

	var currentMargin float64
	for _, pos := range positions {
		qty, _ := pos["quantity"].(float64)
		price, _ := pos["markPrice"].(float64)
		lev := 10.0
		if l, ok := pos["leverage"].(float64); ok && l > 0 {
			lev = l
		}
		currentMargin += (qty * price) / lev
	}

	newMargin := positionSizeUSD / float64(leverage)
	totalMargin := currentMargin + newMargin
	usageRatio := totalMargin / equity

	if usageRatio > maxUsage {
		return fmt.Errorf("margin usage would be %.1f%% (limit %.0f%%): current=%.2f + new=%.2f, equity=%.2f",
			usageRatio*100, maxUsage*100, currentMargin, newMargin, equity)
	}
	return nil
}
