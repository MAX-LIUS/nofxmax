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
	"nofx/trader/binance"
	"nofx/trader/bitget"
	"nofx/trader/okx"
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

	// 顺手把面板用的仓位快照保持温热。监控循环走的是适配器缓存,不会更新 API 侧的
	// 投影快照,所以切到一个久未查看的交易员时 /api/positions 必然走阻塞分支 ——
	// 在 1 核机器上要和 4 个监控循环抢 CPU,实测出现过 16s。详见
	// WarmPositionsSnapshotIfStale 的注释。异步、按年龄节流,不阻塞本轮监控。
	at.WarmPositionsSnapshotIfStale(len(positions) > 0)

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

		// Excursion tracking (MFE/MAE): record the favorable peak + adverse trough
		// profit% and their open-time ATR multiples EVERY poll for EVERY position,
		// independent of any protection config. This is the durable process-data trail
		// used for reverse-lookup/backtest, so it must run before any early-continue
		// (time-stop / max-hold / drawdown gates) that would skip a position.
		at.UpdateExcursion(symbol, side, symbol+"_"+side, entryPrice, markPrice)

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
			at.initDrawdownTiersFromResolvedRules(symbol, side, quantity, entryPrice, rules)
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
		//
		// Anti-churn design (2026-07): we NO LONGER cancel+convert phantom trailing
		// orders here (that place→phantom→cancel→managed→re-arm loop caused the WLD
		// 208-set/106-cancel/212-phantom churn). Instead: arm any genuinely-missing
		// tier once, then LEAVE existing orders alone. Whether the code-side managed
		// close must supplement is decided by real EFFECTIVENESS (activated, or
		// resting with activePx not yet passed) — a phantom/flawed order no longer
		// suppresses the managed backup. So nativeTrailingHandled reflects effective
		// coverage, not mere placement.
		nativeTrailingHandled := false
		if at.supportsNativeTrailingStop() {
			executionMode := at.getDrawdownExecutionMode(symbol, side)
			// Safety FIRST: cancel any DANGEROUS no-activation trailing order that would
			// mis-close at a loss — i.e. activePx<=0 activated-immediately AND the position
			// is below the tier profit floor (trailing from near-entry). At/above the floor
			// a no-activePx order is a DELIBERATE profit-locked immediate-trail we place on
			// purpose (unified reached/unreached protection) and must be kept. Runs before
			// arming so the arm loop can re-place a genuinely-missing tier this pass.
			// Phantom orders (activePx>0, mark passed) are NOT touched here.
			safeProfitFloor := lowestTierMinProfit(rules)
			at.reconcileDangerousTrailingOrders(symbol, side, entryPrice, currentPnLPct, safeProfitFloor)
			armRules := at.getDrawdownArmRulesForNativeExposure(currentPnLPct, entryPrice, quantity, symbol, side, rules)
			for _, armRule := range armRules {
				// If this tier's re-arm breaker has tripped (repeated place/verify
				// failures), STOP re-placing on the exchange — the in-process managed
				// monitor owns it now. This is what cuts the cancel→re-place churn once
				// the exchange side is proven unreliable for the tier.
				if at.reArmBreakerTripped(symbol, side, armRule, entryPrice) {
					continue
				}
				// applyNativeTrailingDrawdown is idempotent: it re-places only when the
				// tier is genuinely missing/drifted, and leaves a present (incl. phantom)
				// order untouched. This "arm only when missing" is the whole point.
				_ = at.applyNativeTrailingDrawdown(symbol, side, entryPrice, markPrice, armRule)
			}
			// Run the breaker accounting for its side effects (per-tier failure counting
			// and the local-monitor upgrade), but do NOT use its verdict to skip the
			// managed pass. See the co-run note below.
			if at.accountReArmBreaker(symbol, side, entryPrice, markPrice, currentPnLPct, rules) {
				nativeTrailingHandled = true
			} else {
				logger.Warnf("🟡 Drawdown monitor: %s %s (%s) has satisfied tier(s) NOT effectively covered on exchange — allowing managed backup to supplement", symbol, side, executionMode)
			}
		}

		if len(allocs) > 0 {
			// CO-RUN, NEVER HAND OVER (2026-07-27, product decision).
			//
			// This used to be `if nativeTrailingHandled { continue }` — an ACCOUNT-level
			// hand-over: one aggregate "everything looks covered" verdict switched the
			// managed monitor off for the whole position. Two ways that lost protection
			// outright:
			//
			//  1. accountReArmBreaker returns true for a tier whose re-arm breaker has
			//     TRIPPED (it `continue`s without clearing allCovered, on the assumption
			//     that applyExchangeFailedLocalMonitor now owns the tier). For a
			//     close>=100 tier that helper places NOTHING and registers NO executor —
			//     it only sets protection state and persists a record. Meanwhile the arm
			//     loop skips tripped tiers, so no exchange order is placed either. Net
			//     result: no exchange order, no managed close, panel says "armed", and
			//     positionHasArmedProtection() reports the position as protected so the
			//     giveback breadth breaker leaves the retracing winner alone. The breaker
			//     never resets either (resetReArmFail only runs on verified coverage,
			//     which needs an order that is never placed), so the gap is permanent for
			//     the life of the position.
			//  2. Any aggregate verdict is only as good as its inputs. v1.16.10 showed a
			//     path where logs and the reconciler both claimed coverage while nothing
			//     was on the exchange — an account-level switch turns one bad read into
			//     zero protection.
			//
			// So the managed monitor now ALWAYS evaluates. Double execution is prevented
			// where it belongs: the per-tier gate below (exchangeSideCoversDrawdownTier),
			// which checks EFFECTIVENESS of that specific tier's live order and fails open
			// (allows the managed close) when exchange state cannot be read. nativeTrailingHandled
			// is kept only for logging/telemetry.
			if nativeTrailingHandled && currentPnLPct > 0 {
				logger.Infof("🤝 Drawdown co-run: %s %s exchange side looks covered — managed monitor keeps tracking as second insurance (per-tier gate decides execution)", symbol, side)
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
				if at.exchangeSideCoversDrawdownTier(symbol, side, normalizeDrawdownRule(*matchingRule), entryPrice, markPrice) {
					// Exchange owns this tier. evaluateDrawdownTiers ALREADY flipped the
					// tier to "executed" before returning it, so we must put it back —
					// otherwise the tier is skipped by every later evaluation (executed
					// tiers are filtered out) and the managed side silently stops watching
					// a tier it never actually closed. hasAllTiersCompleted would also
					// report the position fully exited. The exchange fill detector is what
					// legitimately marks this tier executed, when the order really fills.
					at.updateTierAlloc(symbol, side, triggered.TierIndex, func(a *store.DrawdownTierAllocation) {
						a.Status = "tracking"
						if triggered.PeakPnLPct > a.PeakPnLPct {
							a.PeakPnLPct = triggered.PeakPnLPct
						}
					})
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
			// Same co-run rule as the tier-alloc path: suppress the managed close only when
			// the exchange order for THIS triggered tier is genuinely EFFECTIVE. The old
			// check here was hasArmedNativeDrawdownForPosition — an armed-RECORD check, so a
			// stale record pointing at a dead order (or a phantom order) silently disabled
			// the only remaining executor on this path.
			if at.exchangeSideCoversDrawdownTier(symbol, side, normalizeDrawdownRule(triggeredRules[0]), entryPrice, markPrice) {
				logger.Infof("🟣 Drawdown monitor: %s %s exchange trailing is effective for the triggered tier; managed market close stands down this poll", symbol, side)
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

// isManagedDrawdownProtectionState 是 isNativeTrailingProtectionState 的 managed 对偶:
// 进程内 managed 回撤监控已武装的全部状态(含"交易所挂单失败、只剩本地监控"两种)。
//
// 之所以要有这个函数:setProtectionState 写入的 managed 状态一共 4 个,而调用方历史上
// 都是手写字面量并集,**managed_drawdown_armed 在多处被漏掉**:
//   - getDrawdownExecutionMode 漏它 → 归属被算成 native_trailing_pending(已修);
//   - protection_reconciler.go:98 的"保留动态状态"白名单漏它 → 交易所校验通过那一轮
//     把 managed_drawdown_armed 覆盖成 exchange_protection_verified,managed 武装记忆
//     丢失,下一轮又重新武装一次。
//
// 同一份并集只在这里写一次,新增 managed 状态时只改这一处。
func isManagedDrawdownProtectionState(state string) bool {
	return state == "managed_drawdown_armed" || state == "managed_partial_drawdown_armed" ||
		state == "managed_drawdown_exchange_failed_armed" || state == "managed_partial_drawdown_exchange_failed_armed"
}

// isDynamicDrawdownArmState 表示"这个仓位的动态保护(trailing / managed 回撤)已经武装
// 或正在武装中",即 protectionState 这一格里存的是**动态保护的进度**,不能被别的
// 语义(例如 exchange_protection_verified)覆盖掉。
func isDynamicDrawdownArmState(state string) bool {
	return isNativeTrailingProtectionState(state) || isManagedDrawdownProtectionState(state)
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
		// 身份比较,理由同 storedTrailingOrderIDForRule:开仓均价被修正后,
		// managed 记录会认不出自己,于是这一档被当成"没武装过"再走一遍。
		if drawdownRuleIdentity(record.RuleFingerprint) == drawdownRuleIdentity(fingerprint) && record.ProtectionType == "managed_drawdown" {
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

// getArmedDrawdownRuleFingerprintsForPosition 返回本仓位已武装梯度的**规则身份**集合。
//
// 存的是 drawdownRuleIdentity(record.RuleFingerprint) 而不是原始 RuleFingerprint:
// 记录里那个串带着武装当时的开仓均价,而开仓均价会被修正(见 drawdown_rule_identity.go)。
// 用原串比较时,开仓价一漂,同一档梯度就查不到自己的武装记录 → 重复挂单。
// 调用方必须同样用身份去查(见 getDrawdownArmRules* 里的 armedIdentity)。
func (at *AutoTrader) getArmedDrawdownRuleFingerprintsForPosition(symbol, side string, entryPrice, quantity float64) map[string]struct{} {
	armed := make(map[string]struct{})
	for _, record := range at.getArmedDrawdownRecordsForPosition(symbol, side, entryPrice, quantity, 0) {
		if record.RuleFingerprint != "" {
			armed[drawdownRuleIdentity(record.RuleFingerprint)] = struct{}{}
		}
	}
	return armed
}

// storedTrailingOrderIDForRule returns the exchange orderID (OKX algoId / Binance
// algoId) we persisted for THIS specific drawdown tier when we last armed it, or ""
// if none is on record. It is the authoritative per-tier identity: every arm path
// persists the placement's orderID via persistDynamicProtectionRecordWithDetails,
// keyed by the tier's stable rule fingerprint, so a later poll can find exactly the
// order it placed for this tier instead of guessing by qty/activation/callback (which
// collide once multiple tiers rest concurrently under place-at-open). Uniform across
// all exchanges: both OKX and Binance set OpenOrder.OrderID = the placement algoId.
func (at *AutoTrader) storedTrailingOrderIDForRule(symbol, side string, entryPrice float64, rule store.DrawdownTakeProfitRule) string {
	wantFP := drawdownRuleIdentity(stableDrawdownRuleFingerprint(entryPrice, rule))
	// A position can carry SEVERAL armed records with the SAME RuleFingerprint: the
	// position-identity filter matches on entry price only (quantity legitimately
	// changes on partial close, and the native-trailing rule fingerprint is
	// quantity-independent), so a pre-partial-close record survives alongside the
	// current one with a DIFFERENT ExchangeOrderID. Returning the first map hit made
	// the answer depend on Go's randomized map iteration order — half the polls
	// returned the stale order ID, whose order no longer exists on the exchange, so
	// the tier was judged missing (2026-07-27: CL/HYPE/ETH re-armed every cooldown
	// window and the panel showed dd1/dd2 red on the unlucky polls). Always pick the
	// most recently armed record: that is the order actually resting on the exchange.
	best := ""
	var bestUpdatedAt int64 = -1
	for _, record := range at.getArmedDrawdownRecordsForPosition(symbol, side, entryPrice, 0, 0) {
		// 按规则身份比,不按原始 fingerprint 比:记录里的串带着武装当时的开仓均价,
		// 而开仓均价会被修正(place-at-open 用计划/成交价,运行时用交易所同步均价)。
		// 用原串比时开仓价一漂就查不到自己挂的单,于是这一档被判"缺单"并重复挂出。
		if record.ExchangeOrderID == "" || drawdownRuleIdentity(record.RuleFingerprint) != wantFP {
			continue
		}
		if record.UpdatedAt > bestUpdatedAt {
			bestUpdatedAt = record.UpdatedAt
			best = record.ExchangeOrderID
		}
	}
	return best
}

// claimedTrailingOrderIDsForPosition returns the set of exchange orderIDs that armed
// protection records currently claim for this position, optionally excluding one rule
// fingerprint (the tier being (re-)armed right now).
//
// Under the place-at-open multi-tier model several trailing orders rest concurrently
// (dd1 full + partial), each owned by its own armed record. Any bulk cancel must skip
// these — canceling a sibling tier's live order leaves that tier's record pointing at
// a dead orderID forever, so its matcher reports "missing" on every poll (panel red +
// re-arm once per cooldown window). Orders NOT in this set are genuine orphans.
func (at *AutoTrader) claimedTrailingOrderIDsForPosition(symbol, side string, entryPrice float64, excludeRuleFP string) map[string]struct{} {
	claimed := make(map[string]struct{})
	for _, record := range at.getArmedDrawdownRecordsForPosition(symbol, side, entryPrice, 0, 0) {
		if record.ExchangeOrderID == "" {
			continue
		}
		// 身份比较:excludeRuleFP 由调用方现算(带当前开仓均价),记录里那串带的是
		// 武装当时的开仓均价。两者可能因均价修正而不同 —— 用原串比时"排除自己这一档"
		// 会失效,于是本档把自己上一次挂的单当成兄弟档的单保护起来,永远撤不掉。
		if excludeRuleFP != "" && drawdownRuleIdentity(record.RuleFingerprint) == drawdownRuleIdentity(excludeRuleFP) {
			continue
		}
		claimed[record.ExchangeOrderID] = struct{}{}
	}
	// The immediate trailing order placed at open is ALSO an owned trailing order, but
	// it is not a drawdown tier so getArmedDrawdownRecordsForPosition (filtered by
	// isDynamicNativeProtectionType) never returns it. Leaving it out let the tiers'
	// positional fallback adopt a 50%-quantity order and report full coverage —
	// v1.16.8/v1.16.10 all over again with one more unowned order.
	//
	// Adding it here fixes every consumer of the claimed set at once
	// (findExistingFullTrailingOrder, hasMatchingNativeTrailingOrderForRule,
	// findPartialTrailingReplacementCandidate, and the collapse/cancel claim logic)
	// instead of patching each matcher separately.
	for _, id := range at.immediateTrailingClaimedIDs(symbol, side) {
		claimed[id] = struct{}{}
	}
	return claimed
}

// deliberateImmediateTrailIDsForPosition returns the exchange orderIDs of trailing
// orders that WE placed on purpose as no-activePx immediate-trails (the "⚡ Trailing
// activation passed → immediate (no-activePx) order" branch in
// armNativeTrailingDrawdownTier). Such a record is written with ActivationPrice==0,
// which is the durable fingerprint of that decision: every other arm path persists a
// positive planned activation price.
//
// 为什么需要它(CLUSDT 门槛抖动的根因):挂这种单的前提是"当时 pnl>=档位 floor",
// 挂上之后 OKX 立即激活、锚点就固定在那一刻的盈利价上,之后只会朝有利方向棘轮。
// 但 trailingOrderIsDangerousNoActivation 用的是**之后某一刻**的 pnl 复判同一张单,
// 两边同一个阈值、不同时刻、中间没有滞回带 —— 于是 pnl 在 floor 上下几个基点晃动时,
// 同一张单反复被判"故意的安全立即跟踪"(挂)和"危险的立即成交单"(撤):
// 2026-07-28 生产实况 CLUSDT short floor=3.1309%,挂单时 pnl=3.29%,之后漂到
// 3.12/3.13% → 4 次 🔴 撤单 + 重挂。代价是真的:撤掉一张已在盈利处生效的跟踪单会把
// 交易所侧的跟踪峰值**重置到更差的价格**,且撤到重挂之间有一段裸奔窗口。
//
// 正确的判别不是"现在在不在门上",而是"下单那刻在不在门上" —— 后者已经被持久化记录
// 钉住了,所以这里读记录而不是重算 pnl。用持久化记录而不是内存注册表,是为了让判别
// 在重启后依然成立(重启后本进程没挂过任何单,内存表必然为空,自己的单就会被当成
// 遗留单撤掉 —— 正是这个缺陷最坏的形态)。
//
// 遗留/手工/交易所异常产生的无激活价单没有这样的记录,仍然走 pnl 门 —— 对它们我们
// 确实不知道锚点在哪,保守当危险处理是对的。
func (at *AutoTrader) deliberateImmediateTrailIDsForPosition(symbol, side string, entryPrice float64) map[string]struct{} {
	deliberate := make(map[string]struct{})
	for _, record := range at.getArmedDrawdownRecordsForPosition(symbol, side, entryPrice, 0, 0) {
		if record.ExchangeOrderID == "" || record.ActivationPrice != 0 {
			continue
		}
		deliberate[record.ExchangeOrderID] = struct{}{}
	}
	return deliberate
}

// findTrailingOrderByID returns the live trailing order whose exchange OrderID equals
// wantID (and matches side), or nil. This is the precise, collision-free identity
// match that replaces fuzzy qty/activation/callback matching whenever we have a stored
// orderID for the tier.
func findTrailingOrderByID(side, wantID string, openOrders []OpenOrder) *nativeTrailingOrder {
	if wantID == "" {
		return nil
	}
	for _, order := range openOrders {
		if order.OrderID != wantID {
			continue
		}
		if order.PositionSide != "" && !strings.EqualFold(order.PositionSide, strings.ToUpper(side)) {
			continue
		}
		return &nativeTrailingOrder{
			PositionSide:     order.PositionSide,
			StopPrice:        order.StopPrice,
			CallbackRate:     order.CallbackRate,
			Quantity:         order.Quantity,
			OrderID:          order.OrderID,
			ActivationStatus: order.ActivationStatus,
			ActivationPrice:  order.ActivationPrice,
		}
	}
	return nil
}

func (at *AutoTrader) hasMatchingNativeTrailingOrderForRule(symbol, side string, entryPrice float64, rule store.DrawdownTakeProfitRule, openOrders []OpenOrder) bool {
	if len(openOrders) == 0 {
		return false
	}
	// Authoritative identity match first: if we recorded an orderID for this exact
	// tier, its presence in the live order book IS the match (and its absence is a
	// genuine gap → re-arm). This is collision-free across concurrently-resting tiers,
	// unlike the qty/activation/callback heuristics below. Fuzzy matching remains only
	// as a fallback for tiers armed before we had a stored ID (legacy / pre-restart).
	if wantID := at.storedTrailingOrderIDForRule(symbol, side, entryPrice, rule); wantID != "" {
		return findTrailingOrderByID(side, wantID, openOrders) != nil
	}
	plannedActivationPrice := calculateProfitBasedTrailingTriggerPrice(entryPrice, side, rule.MinProfitPct)
	plannedCallbackRate := calculateDrawdownRuleCallbackRatio(entryPrice, side, rule)
	// Orders owned by a SIBLING tier's armed record are not candidates for this tier.
	// Without this, the fuzzy fallback below matches the first trailing order on the
	// side and — because an activated order short-circuits to true at any parameters —
	// reports this tier covered by another tier's order. See the identical guard in
	// findExistingFullTrailingOrder for the 2026-07-27 BN SOLUSDT case.
	claimed := at.claimedTrailingOrderIDsForPosition(symbol, side, entryPrice, stableDrawdownRuleFingerprint(entryPrice, rule))
	for _, order := range openOrders {
		if order.PositionSide != "" && !strings.EqualFold(order.PositionSide, strings.ToUpper(side)) {
			continue
		}
		if !strings.Contains(strings.ToUpper(order.Type), "TRAILING") {
			continue
		}
		if _, owned := claimed[order.OrderID]; owned {
			continue
		}
		// Once a trailing order has ACTIVATED, its exchange-reported trigger
		// (StopPrice) is the MOVING trail level and, on Binance, its callback rate is
		// not reported at all (CallbackRate=0). Both the activation-drift and
		// callback-match checks below therefore FAIL for a perfectly healthy activated
		// order, so this dedup gate returned false and the drawdown monitor re-armed —
		// placing a fresh trailing order EVERY cycle (~11/min across Binance shorts,
		// occasionally tripping the -4045 max-stop limit). An activated trailing order
		// of the right position/side IS the live protection for this tier; treat it as
		// a match so we do not re-arm. Pre-activation orders still go through the
		// activation+callback comparison below.
		if order.ActivationStatus == "activated" {
			return true
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
		// When the venue does not report callback (<=0 after the pct fallback), trust
		// the activation-price match alone rather than treating unreported as drift.
		callbackOK := callback <= 0 || math.Abs(callback-plannedCallbackRate) <= callbackTolerance
		if activationOK && callbackOK {
			return true
		}
	}
	return false
}

func (at *AutoTrader) getDrawdownArmRulesForNativeExposure(currentPnLPct, entryPrice, quantity float64, symbol, side string, rules []store.DrawdownTakeProfitRule) []store.DrawdownTakeProfitRule {
	// Unified place-at-open protection (2026-07-26): ALL configured tiers are armed at
	// open, each with its own activation price (safest moment — price is furthest from
	// every activePx). At runtime we only MONITOR and re-fill genuinely-missing tiers.
	// We no longer migrate a single exchange tier as profit advances — that migration
	// tried to arm a higher tier only once price approached/passed its activePx, which
	// on OKX became an immediate-active or phantom order and looped (WLD churn). Instead:
	// query every tier, return those without a matching live exchange order so each gets
	// re-placed (a passed+profitable tier is re-placed with an immediate anchor by
	// applyNativeTrailingDrawdown; an unreached tier is re-placed resting at its activePx).
	//
	// Churn-safety: per-rule getDrawdownArmRulesForSelectedRule keeps the fingerprint
	// dedup (skip when a matching order already exists), the executed-tier guard, and
	// the 300s re-arm cooldown — so "return all missing" cannot spam duplicate orders.
	// The two historical multi-tier churns were both fixed independently: the activated-
	// order drift false-positive (leave activated orders alone) and the ATR-resolution
	// mismatch (resolveDrawdownRulesATR). Holding N resting tiers at distinct activePx is
	// now stable.
	var out []store.DrawdownTakeProfitRule
	for _, raw := range rules {
		rule := normalizeDrawdownRule(raw)
		if rule.MinProfitPct <= 0 || rule.MaxDrawdownPct <= 0 || rule.CloseRatioPct <= 0 {
			continue
		}
		out = append(out, at.getDrawdownArmRulesForSelectedRule(entryPrice, quantity, symbol, side, rule)...)
	}
	return out
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
	// 规则身份(不含开仓均价):armedFingerprints 里存的也是身份,两边必须同源。
	// 见 drawdown_rule_identity.go —— 开仓均价被修正时用原串比会判定"没武装过"从而重复挂单。
	fingerprint := drawdownRuleIdentity(stableDrawdownRuleFingerprint(entryPrice, rule))
	// Structural backstop, checked BEFORE the armed-record branch on purpose.
	// nativeTrailingArmTime is written by every successful arm, so it is the one
	// piece of state that survives a failed/missing persist. Historically this
	// cooldown lived only INSIDE the `armedFingerprints[fingerprint]` branch, and
	// armedFingerprints is built from records whose ProtectionType passes
	// isDynamicNativeProtectionType (native_trailing / native_partial_trailing only).
	// So any arm path that placed an order but did not persist a native record
	// could never reach the cooldown: the gate re-selected the tier every ~20s poll
	// with NO upper bound (2026-07-27: 174 identical HYPEUSDT partial trailing
	// orders on Binance). Hoisting it here bounds every such bug to one re-arm per
	// 300s instead of unbounded, whatever the record state. Cost: a genuinely
	// missing order waits up to 300s before re-arming — the same delay that already
	// applied when a record did exist, and the reason this cooldown is a required
	// safety fallback rather than an optimisation.
	// It is an unconditional rate limit, not a fallback: we armed this exact tier
	// moments ago, so placing another order now cannot be correct regardless of what
	// the records or the matchers say.
	if lastArm, ok := at.nativeTrailingArmTime[nativeTrailingArmKey(symbol, side, fingerprint)]; ok && time.Since(lastArm) < 300*time.Second {
		logger.Infof("🟠 Drawdown native trailing arm cooldown: %s %s fingerprint=%s (armed %.0fs ago, not re-arming)", symbol, side, fingerprint, time.Since(lastArm).Seconds())
		return nil
	}
	if _, ok := armedFingerprints[fingerprint]; ok {
		if at.hasMatchingNativeTrailingOrderForRule(symbol, side, entryPrice, rule, openOrders) {
			logger.Infof("🟣 Drawdown native exposure skipped: %s %s already armed fingerprint=%s", symbol, side, fingerprint)
			return nil
		}
		// A managed_drawdown record does NOT mean "no exchange order expected".
		//
		// This branch used to `return nil` here, which made the code-side monitor a
		// SUBSTITUTE for the exchange order: once any managed record existed for the
		// tier, the exchange was never tried again for the life of the position. That
		// inverts the product rule — managed co-runs as second insurance, it never takes
		// over (see the CO-RUN, NEVER HAND OVER block at the top of this file).
		//
		// Live 2026-07-28/29, GPT SKHYNIXUSDT short: the breaker wrote a managed record
		// at 19:13:46 and from then on this line logged "no exchange trailing expected"
		// every poll while the strategy's only DD tier rested nowhere. Falling through
		// instead re-places it. Runaway placement is not a risk here: the unconditional
		// 300s arm cooldown above already bounds this to one attempt per tier per 300s.
		if at.isManagedDrawdownRecord(symbol, side, fingerprint) {
			logger.Infof("🟣 Drawdown managed record present but exchange order missing: %s %s fingerprint=%s — re-arming exchange side (managed keeps co-running, never substitutes)", symbol, side, fingerprint)
		}
		// Check if this tier was already executed (order filled, position reduced).
		// If the tier alloc shows executed/triggered, don't re-arm.
		if at.isDrawdownTierExecuted(symbol, side, rule) {
			logger.Infof("🟣 Drawdown tier already executed: %s %s fingerprint=%s (trailing order filled, not re-arming)", symbol, side, fingerprint)
			return nil
		}
		// (The 300s re-arm cooldown that used to sit here is now checked above, before
		// this branch — it applies whether or not a record exists. See the comment there.)
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

	// 规则身份,同 getDrawdownArmRulesForSelectedRule。
	fingerprint := drawdownRuleIdentity(stableDrawdownRuleFingerprint(entryPrice, bestRule))
	// Same structural backstop as getDrawdownArmRulesForSelectedRule — see the
	// rationale there. Bounds an arm path with a missing persist to one re-arm per
	// 300s rather than one per poll.
	if lastArm, ok := at.nativeTrailingArmTime[nativeTrailingArmKey(symbol, side, fingerprint)]; ok && time.Since(lastArm) < 300*time.Second {
		logger.Infof("🟠 Drawdown arm cooldown: %s %s fingerprint=%s (armed %.0fs ago, not re-arming)", symbol, side, fingerprint, time.Since(lastArm).Seconds())
		return nil
	}
	if _, ok := armedFingerprints[fingerprint]; ok {
		if at.hasMatchingNativeTrailingOrderForRule(symbol, side, entryPrice, bestRule, openOrders) {
			logger.Infof("🟣 Drawdown arm skipped: %s %s highest tier already armed fingerprint=%s (min=%.4f close=%.1f%%)", symbol, side, fingerprint, bestRule.MinProfitPct, bestRule.CloseRatioPct)
			return nil
		}
		if at.isDrawdownTierExecuted(symbol, side, bestRule) {
			logger.Infof("🟣 Drawdown tier already executed: %s %s fingerprint=%s (not re-arming)", symbol, side, fingerprint)
			return nil
		}
		// (The 300s re-arm cooldown that used to sit here is now checked above, before
		// this branch, so it applies whether or not a record exists.)
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
	// ActivationPrice is the fixed activePx the order was placed with. On OKX a
	// resting order reports StopPrice==activePx, but once ACTIVATED StopPrice
	// becomes the moving trail level — so we carry activePx separately to judge
	// phantom activation (activePx passed but exchange never activated) reliably.
	ActivationPrice float64
}

// findExistingFullTrailingOrder locates the full-close (100%) trailing order for this
// position. It matches by the tier's stored orderID first (collision-free — critical
// now that a partial tier can rest concurrently; the old "return the first TRAILING
// order" logic would hand back the partial tier's order and make the full tier look
// drifted, re-arming every cooldown). Falls back to first-trailing-order only when no
// orderID is on record (legacy / pre-restart positions).
func (at *AutoTrader) findExistingFullTrailingOrder(symbol, side string, entryPrice float64, rule store.DrawdownTakeProfitRule, openOrders []OpenOrder) *nativeTrailingOrder {
	if wantID := at.storedTrailingOrderIDForRule(symbol, side, entryPrice, rule); wantID != "" {
		// A stored ID exists: only that exact order is this tier. If it is gone, the
		// tier is genuinely missing (return nil → re-arm) rather than mis-binding to a
		// sibling tier's order.
		return findTrailingOrderByID(side, wantID, openOrders)
	}
	// No stored ID for this tier: fall back to "any trailing order on this side".
	//
	// That fallback predates place-at-open (a56f741). When only one trailing order per
	// side could exist, "the first trailing order" WAS this tier. Now dd1 and one or more
	// partial tiers rest concurrently, so the first trailing order is very often a
	// SIBLING tier's order — and claiming it makes this tier look present forever:
	// applyNativeTrailingDrawdown sees ActivationStatus=="activated" (which Binance
	// hard-codes for every trailing order) and returns true without arming anything.
	//
	// Observed 2026-07-27 on BN SOLUSDT: after removing the wrongly-parameterised raw dd1
	// order, the only trailing order left was the 30% partial (qty 1.32 of 4.41). The dd1
	// tier matched it, reported itself satisfied on every 10s poll ("native exposure
	// selected" with no "armed" line), and the position ran with NO full-close trailing
	// protection at all. Excluding sibling-claimed orders makes the tier correctly report
	// missing so it gets armed. Same remedy as v1.16.8 applied to the partial matchers —
	// no new threshold, just "an order another record owns is not mine".
	claimed := at.claimedTrailingOrderIDsForPosition(symbol, side, entryPrice, stableDrawdownRuleFingerprint(entryPrice, rule))
	for _, order := range openOrders {
		if order.PositionSide != "" && !strings.EqualFold(order.PositionSide, strings.ToUpper(side)) {
			continue
		}
		if !strings.Contains(strings.ToUpper(order.Type), "TRAILING") {
			continue
		}
		if _, owned := claimed[order.OrderID]; owned {
			continue
		}
		// A FULL-CLOSE tier may only adopt an order that can actually close the full
		// position. Ownership alone is not enough here: this branch adopts an order we
		// cannot prove is ours, so an unowned partial-sized order (older binary, manual
		// order, or an arm whose persist failed) would otherwise be reported as full
		// coverage while resting on a fraction of the position.
		//
		// Deliberately narrow: only the fallback is guarded, never the stored-ID path —
		// so an add-on entry that grows the position keeps resolving its own order by ID
		// and does not churn. Skipped entirely when either quantity is unknown (some
		// venues report qty=0 on trailing orders), because "unknown" must not mean
		// "inadequate".
		if posQty, _ := at.getPositionDetailsForFingerprint(symbol, side); posQty > 0 && order.Quantity > 0 {
			if order.Quantity/posQty < fullTrailingAdoptionMinCoverage {
				logger.Warnf("🟠 Full-close tier refuses to adopt unowned trailing order %s (%s %s qty=%.6f vs position %.6f = %.1f%% coverage) — treating tier as missing so it arms its own order",
					order.OrderID, symbol, side, order.Quantity, posQty, order.Quantity/posQty*100)
				continue
			}
		}
		return &nativeTrailingOrder{
			PositionSide:     order.PositionSide,
			StopPrice:        order.StopPrice,
			CallbackRate:     order.CallbackRate,
			Quantity:         order.Quantity,
			OrderID:          order.OrderID,
			ActivationStatus: order.ActivationStatus,
			ActivationPrice:  order.ActivationPrice,
		}
	}
	return nil
}

// trailingOrderIsDangerousNoActivation reports whether a native trailing order is
// the DANGEROUS "no activation price" kind: OKX places a move_order_stop without
// activePx as immediately-active, and the adapter reports it as ActivationStatus
// "activated" with ActivationPrice<=0 (see trader/okx/trader_orders.go:1457). Such
// an order trails from the CURRENT price the instant it is placed, so any small
// retrace closes the position early — even before the tier's profit anchor. It is
// never something WE place (applyNativeTrailingDrawdown requires activePx>0), so it
// only arises from legacy/manual/exchange-anomaly orders. It must be cancelled and
// re-placed with the correct anchor, NOT left resting. This is distinct from a
// PHANTOM order (activePx>0 but mark already passed it) which will never fire and
// so cannot mis-close — phantoms are left alone and covered by the managed backup.
// exchange is required because the danger pattern is VENUE-SPECIFIC: only OKX
// reports an immediately-active (no-activePx) order as ActivationStatus "activated"
// with ActivationPrice<=0. On Binance an "activated" trailing order legitimately
// carries ActivationPrice = triggerPrice (>0), and even a synthetic 0 there means a
// genuinely armed order (Binance reliably activates), NOT a mis-closing one — so the
// rule must never fire off-OKX or it would cancel healthy Binance protection.
//
// PROFIT-AWARE (2026-07-26): a no-activePx immediate-trail order is DANGEROUS only
// when placed while the position is NOT yet profitable enough — it then trails from
// a near-entry/loss level and mis-closes at a loss on any small retrace. Once the
// position's profit has reached the lowest tier's activation floor (safeProfitFloor
// = min MinProfitPct across rules), an immediate-trail order is a DELIBERATE,
// SAFE profit-lock: its peak is anchored in profit, so the worst-case close is
// (peak × (1 − callback)) ≥ around the tier's designed retained profit — never a
// fresh-position loss. We place exactly such orders on purpose for tiers whose
// activePx the mark has already passed (unifying reached/unreached protection), so
// the reconciler must NOT cancel them or it recreates the place→cancel churn. Below
// the floor the old always-dangerous rule still holds.
// lowestTierMinProfit returns the smallest positive MinProfitPct across the given
// drawdown rules — the profit level at/above which a no-activePx immediate-trail is
// a deliberate, safe profit-lock rather than a mis-closing hazard. Returns 0 when no
// valid rule exists (callers then fall back to the always-dangerous rule).
func lowestTierMinProfit(rules []store.DrawdownTakeProfitRule) float64 {
	floor := 0.0
	for _, raw := range rules {
		rule := normalizeDrawdownRule(raw)
		if rule.MinProfitPct <= 0 || rule.MaxDrawdownPct <= 0 || rule.CloseRatioPct <= 0 {
			continue
		}
		if floor == 0 || rule.MinProfitPct < floor {
			floor = rule.MinProfitPct
		}
	}
	return floor
}

func trailingOrderIsDangerousNoActivation(exchange string, order *nativeTrailingOrder, currentPnLPct, safeProfitFloor float64) bool {
	if order == nil {
		return false
	}
	if !strings.EqualFold(exchange, "okx") {
		return false
	}
	if !(strings.EqualFold(order.ActivationStatus, "activated") && order.ActivationPrice <= 0) {
		return false
	}
	// Profit gate: at/above the floor this is a deliberate, safe immediate-trail
	// (profit locked in). Only below the floor is it the mis-closing kind.
	if safeProfitFloor > 0 && currentPnLPct >= safeProfitFloor {
		return false
	}
	return true
}

// nativeTrailingEffective reports whether a resting/native trailing order is
// actually EFFECTIVE protection right now — i.e. it will (or already does) close
// the position on giveback. This is the distinction that the old "order merely
// exists" check missed and that let phantom orders silently disable the managed
// backup:
//
//   - activated WITH activePx>0 → EFFECTIVE (genuinely trailing the peak).
//   - activated WITHOUT activePx → NOT effective (dangerous immediate-activation
//     order; it mis-closes rather than protects — the managed backup must cover
//     the giveback while the danger reconciler cancels+re-places it).
//   - activated (legacy)         → EFFECTIVE (already trailing the peak).
//   - resting, activePx NOT yet
//     passed by mark            → EFFECTIVE (OKX will auto-activate when reached).
//   - phantom (resting, activePx
//     already passed, never
//     activated)                → NOT effective (dead order; OKX won't activate).
//   - no activePx recorded      → NOT effective (cannot judge; treat as flawed so
//     the managed backup covers it).
//
// markPrice<=0 means we cannot evaluate reachability; be conservative and treat
// the order as NOT effective so the managed backup still fires (a duplicate
// reduce-only close is bounded and safe; an unprotected giveback is not).
//
// PROFIT-AWARE (2026-07-26): an OKX activated order WITHOUT activePx is effective
// protection when the position is at/above the tier's profit floor (safeProfitFloor)
// — it is then a deliberate immediate-trail whose peak is anchored in profit and
// WILL close on giveback. Below the floor it is the dangerous mis-closing kind and
// NOT effective (managed backup covers while the reconciler cancels it).
func nativeTrailingEffective(exchange string, order *nativeTrailingOrder, side string, markPrice, currentPnLPct, safeProfitFloor float64) bool {
	if order == nil {
		return false
	}
	if strings.EqualFold(order.ActivationStatus, "activated") {
		// A genuinely activated order carries the activation price it was placed
		// with (adapter sets ActivationPrice=activePx). On OKX, if activePx<=0 the
		// order was placed with NO activation anchor and OKX activated it immediately.
		// Whether that is protection or a mis-close hazard depends on PROFIT: at/above
		// the tier floor it is a deliberate profit-locked immediate-trail (effective);
		// below the floor it mis-closes (not effective → managed backup covers while
		// the danger reconciler cancels+re-places). This gate is OKX-ONLY: on Binance
		// an activated order always carries ActivationPrice=triggerPrice and activates
		// reliably, so it stays effective regardless of the recorded value.
		if strings.EqualFold(exchange, "okx") {
			if order.ActivationPrice > 0 {
				return true
			}
			return safeProfitFloor > 0 && currentPnLPct >= safeProfitFloor
		}
		return true
	}
	activePx := order.ActivationPrice
	if activePx <= 0 {
		// OKX resting orders report activePx via StopPrice; fall back to it.
		activePx = order.StopPrice
	}
	if activePx <= 0 {
		// No activation anchor at all — flawed order, cannot rely on it.
		return false
	}
	if markPrice <= 0 {
		return false
	}
	// Resting order is effective ONLY while the activation price has NOT yet been
	// passed (it will auto-activate when reached). Once mark passed activePx and
	// the venue still reports it non-activated, it is phantom → NOT effective.
	//
	// 但"越过"必须带容差带。我们拿**自己的 mark** 去判**OKX 会不会触发**,而 OKX 用的是
	// 它自己的触发价源(last/index),两者在边界上必然有几个基点的分歧。原先是严格不等号,
	// 于是 mark 只要漂过 activePx 一跳,一张健康的挂单就被判幻影;连续 3 轮就把 re-arm
	// 熔断跳掉(2026-07-28 16:47 生产实况:CLUSDT short close=30% 档 activePx=80.19
	// mark=80.15,只越过 0.05%,交易所侧仍报 pending_activation —— 它根本没触发,是我们
	// 判错了)。带上既有的 protectionPriceTolerancePct(0.2%)后,这类边界分歧不再误判,
	// 真正的死单会漂出容差带照样被抓住。
	//
	// 容差为什么不会漏掉真幻影:要用到这一档的跟踪保护,价格得先回吐该档的
	// maxDrawdown 量级(CLUSDT 这档 callback≈1.25%),比 0.2% 大一个数量级 ——
	// 也就是说价格不可能"在容差带内"就需要这张单生效。等真需要它时,偏离早已超出容差带,
	// 幻影判定照常触发,managed 侧照常补位。
	tolerance := activePx * protectionPriceTolerancePct
	if strings.EqualFold(side, "long") {
		return markPrice < activePx+tolerance
	}
	return markPrice > activePx-tolerance
}

// exchangeSideCoversDrawdownTier reports whether the exchange already carries an
// EFFECTIVE trailing order that protects this drawdown tier — i.e. the exchange
// side is genuinely active protection and the code side must NOT also fire (avoid
// double execution). It is the gate for "code side only supplements when the
// exchange side is not actually protecting".
//
// Returns true (suppress code-side close) ONLY when a matching trailing order
// exists AND is effective (activated, or resting with its activation price not
// yet passed). Returns false — allowing the managed code-side close to fire —
// when the order is absent, phantom (activePx already passed but never
// activated), or flawed (no activation price). This is the decisive fix: a
// phantom order no longer masquerades as coverage and silently disables the
// managed backup.
func (at *AutoTrader) exchangeSideCoversDrawdownTier(symbol, side string, rule store.DrawdownTakeProfitRule, entryPrice, markPrice float64) bool {
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
	return at.exchangeSideCoversDrawdownTierWithOrders(symbol, side, rule, entryPrice, markPrice, openOrders)
}

// exchangeSideCoversDrawdownTierWithOrders is the pure form of the coverage gate
// operating on a pre-fetched open-orders snapshot, so a caller iterating many tiers
// (breaker accounting) can fetch once instead of once per tier. exchangeSideCovers-
// DrawdownTier is the fetch-then-delegate wrapper used everywhere else.
func (at *AutoTrader) exchangeSideCoversDrawdownTierWithOrders(symbol, side string, rule store.DrawdownTakeProfitRule, entryPrice, markPrice float64, openOrders []OpenOrder) bool {
	// A tier whose re-arm breaker has TRIPPED is deliberately no longer placed on the
	// exchange (the arm loop skips it to stop the place→fail→re-place churn). The managed
	// monitor is therefore its ONLY executor, so this gate must never suppress the managed
	// close for it — regardless of what stale order might still match. Before this check,
	// a tripped tier could be reported as covered and end up with no executor at all.
	if at.getReArmFail(reArmFailKey(symbol, side, rule, entryPrice)) >= reArmBreakerLimit {
		logger.Warnf("🟠 Drawdown tier %s %s close=%.1f%% has a TRIPPED re-arm breaker — exchange side is not being maintained, managed monitor owns execution",
			symbol, side, rule.CloseRatioPct)
		return false
	}
	existing, _, _, _ := at.findEquivalentPartialTrailingOrder(symbol, side, rule, entryPrice, openOrders)
	if existing == nil {
		return false
	}
	// Profit-aware effectiveness: a no-activePx immediate-trail counts as effective
	// coverage only when the position is at/above this tier's profit floor (its own
	// MinProfitPct) — then it is a deliberate profit-lock. Below the floor it mis-
	// closes and the managed backup must supplement.
	//
	// 例外与 reconcileDangerousTrailingOrders 同源:这一张单如果是我们自己按
	// "下单那刻 pnl>=floor" 故意挂的(持久化记录 activationPrice==0 认得出),它的锚点
	// 就固定在盈利处,当前 pnl 掉到 floor 之下并不会让它变成误平单 —— 它照样会在
	// 回吐时按设计的保留利润平掉。用当前 pnl 复判会让这一档在 floor 上下抖动时被判
	// "present but NOT effective",连续 3 轮就把 re-arm 熔断**误跳闸**;熔断一跳,
	// 交易所侧这一档在本仓位余生都不再维护。两处必须同源判别,只修一处等于把撤单
	// churn 换成误跳闸(2026-07-28 CLUSDT short 的实际链路)。
	currentPnLPct := calculatePositionPnLPct(side, entryPrice, markPrice)
	if _, ours := at.deliberateImmediateTrailIDsForPosition(symbol, side, entryPrice)[existing.OrderID]; ours && existing.OrderID != "" {
		logger.Infof("🛡 Exchange side covers drawdown tier via deliberate immediate-trail (%s %s close=%.1f%% pnl=%.2f%% floor=%.2f%%) — anchored in profit at placement time",
			symbol, side, rule.CloseRatioPct, currentPnLPct, rule.MinProfitPct)
		return true
	}
	if !nativeTrailingEffective(at.exchange, existing, side, markPrice, currentPnLPct, rule.MinProfitPct) {
		logger.Warnf("🟡 Exchange trailing tier present but NOT effective (%s %s close=%.1f%% status=%s activePx=%.6f mark=%.6f) — allowing managed code-side close to supplement",
			symbol, side, rule.CloseRatioPct, existing.ActivationStatus, existing.ActivationPrice, markPrice)
		return false
	}
	logger.Infof("🛡 Exchange side covers drawdown tier (%s %s close=%.1f%% status=%s) — suppressing code-side close to avoid double execution",
		symbol, side, rule.CloseRatioPct, existing.ActivationStatus)
	return true
}

// allSatisfiedNativeTiersEffective reports whether EVERY drawdown tier whose
// min-profit gate is currently met has an EFFECTIVE native trailing order on the
// exchange. "Satisfied" = currentPnLPct >= rule.MinProfitPct (the tier's
// activation price has been reached, so a resting-but-unactivated order for it is
// phantom). Used to decide whether the managed code-side close pass can be
// skipped: only when the exchange genuinely owns every reachable tier.
//
// Returns true when there are no satisfied tiers (nothing to protect yet — the
// resting at-open orders wait for activation) OR all satisfied tiers are
// effectively covered. Returns false the moment any satisfied tier is phantom,
// flawed, or missing — forcing the managed backup to supplement.
func (at *AutoTrader) allSatisfiedNativeTiersEffective(symbol, side string, entryPrice, markPrice, currentPnLPct float64, rules []store.DrawdownTakeProfitRule) bool {
	for _, raw := range rules {
		rule := normalizeDrawdownRule(raw)
		if rule.MinProfitPct <= 0 || rule.MaxDrawdownPct <= 0 || rule.CloseRatioPct <= 0 {
			continue
		}
		if currentPnLPct < rule.MinProfitPct {
			// Not yet reached this tier's activation — its resting order is legitimately
			// waiting, not a gap. Don't require effective coverage yet.
			continue
		}
		if !at.exchangeSideCoversDrawdownTier(symbol, side, rule, entryPrice, markPrice) {
			return false
		}
	}
	return true
}

// reconcileDangerousTrailingOrders finds and cancels DANGEROUS no-activation-price
// native trailing orders (ActivationStatus "activated" with activePx<=0). Such an
// order was placed without an activation anchor, so OKX activated it immediately
// and it trails from the current price — a small retrace mis-closes the position
// before the tier's profit anchor.
//
// 注意:这里**不能**假设"这绝不会是我们挂的"。这条注释原先写的是 place path 一定带
// activePx>0,但 armNativeTrailingDrawdownTier 的 `⚡ activation passed → immediate`
// 分支正是故意挂 activePx=0 的(见那里的长注释)。我们自己挂的那些由
// deliberateImmediateTrailIDsForPosition 按持久化记录识别并跳过,只有认不出归属的
// (遗留/手工/交易所异常)才走下面的 pnl 门。
//
// We cancel it here; the normal arm loop then re-places the tier with the
// correct anchor (when mark hasn't passed the planned activation) and the managed
// backup covers the giveback in the meantime. PHANTOM orders (activePx>0, mark
// already passed) are deliberately NOT touched here — they can't mis-close, and
// re-placing them would just recreate a phantom (churn). Returns the number of
// dangerous orders cancelled. Runs before the arm loop each drawdown poll.
func (at *AutoTrader) reconcileDangerousTrailingOrders(symbol, side string, entryPrice, currentPnLPct, safeProfitFloor float64) int {
	if !at.supportsNativeTrailingStop() {
		return 0
	}
	openOrders, err := at.GetOpenOrders(symbol)
	if err != nil {
		logger.Warnf("⚠️ Dangerous-trailing reconcile: cannot fetch open orders (%s %s): %v — skipping this pass", symbol, side, err)
		return 0
	}
	// 我们自己故意挂的无激活价单不参与危险判定 —— 它们的锚点在下单那刻就固定在
	// 盈利处,用当前 pnl 复判会在 floor 上下抖动时把它们反复撤掉再重挂
	// (见 deliberateImmediateTrailIDsForPosition 的说明)。
	deliberate := at.deliberateImmediateTrailIDsForPosition(symbol, side, entryPrice)
	var dangerousIDs []string
	keptDeliberate := 0
	for _, order := range openOrders {
		if order.PositionSide != "" && !strings.EqualFold(order.PositionSide, strings.ToUpper(side)) {
			continue
		}
		if !strings.Contains(strings.ToUpper(order.Type), "TRAILING") {
			continue
		}
		candidate := &nativeTrailingOrder{
			PositionSide:     order.PositionSide,
			StopPrice:        order.StopPrice,
			CallbackRate:     order.CallbackRate,
			Quantity:         order.Quantity,
			OrderID:          order.OrderID,
			ActivationStatus: order.ActivationStatus,
			ActivationPrice:  order.ActivationPrice,
		}
		if trailingOrderIsDangerousNoActivation(at.exchange, candidate, currentPnLPct, safeProfitFloor) && order.OrderID != "" {
			if _, ours := deliberate[order.OrderID]; ours {
				keptDeliberate++
				continue
			}
			dangerousIDs = append(dangerousIDs, order.OrderID)
		}
	}
	if keptDeliberate > 0 {
		logger.Infof("🛡 Keeping %d deliberate no-activePx immediate-trail order(s) (%s %s pnl=%.2f%% floor=%.2f%%) — anchored in profit at placement time, current pnl does not re-judge them",
			keptDeliberate, symbol, side, currentPnLPct, safeProfitFloor)
	}
	if len(dangerousIDs) == 0 {
		return 0
	}
	tagged, ok := at.trader.(interface {
		CancelTrailingStopOrdersByIDs(symbol string, orderIDs []string) error
	})
	if !ok {
		logger.Warnf("🔴 Dangerous no-activation trailing detected (%s %s) but venue lacks CancelTrailingStopOrdersByIDs — cannot cancel; managed backup covers giveback", symbol, side)
		return 0
	}
	if err := tagged.CancelTrailingStopOrdersByIDs(symbol, dangerousIDs); err != nil {
		logger.Warnf("🔴 Failed to cancel %d dangerous no-activation trailing order(s) (%s %s): %v", len(dangerousIDs), symbol, side, err)
		return 0
	}
	logger.Warnf("🔴 Cancelled %d DANGEROUS no-activation trailing order(s) (%s %s) — would mis-close on any retrace; arm loop will re-place with correct anchor, managed backup covers meanwhile", len(dangerousIDs), symbol, side)
	return len(dangerousIDs)
}

// accountReArmBreaker runs AFTER the arm loop each poll and returns whether every
// currently-satisfied native tier is EFFECTIVELY covered on the exchange (the same
// answer as allSatisfiedNativeTiersEffective) while maintaining the per-tier re-arm
// breaker. For each satisfied tier: if effectively covered → reset its breaker; if
// NOT covered → bump its breaker, and once it reaches reArmBreakerLimit, trip it —
// stop re-placing on the exchange and arm the in-process managed monitor (panel
// shows the exchange-failed reverse-colour warning). A tripped tier is reported as
// "covered" for suppression purposes because the managed monitor now owns it (via
// checkPositionDrawdown), so we don't ALSO fire the tier-alloc managed close in the
// same pass — the two must never double-execute. Uses one open-orders fetch.
func (at *AutoTrader) accountReArmBreaker(symbol, side string, entryPrice, markPrice, currentPnLPct float64, rules []store.DrawdownTakeProfitRule) bool {
	openOrders, err := at.GetOpenOrders(symbol)
	if err != nil {
		// Cannot confirm exchange state — do NOT trip the breaker on a read failure
		// and do NOT claim coverage; let the managed backup supplement this pass.
		logger.Warnf("⚠️ Re-arm breaker accounting: cannot fetch open orders (%s %s): %v — allowing managed backup", symbol, side, err)
		return false
	}
	allCovered := true
	for _, raw := range rules {
		rule := normalizeDrawdownRule(raw)
		if rule.MinProfitPct <= 0 || rule.MaxDrawdownPct <= 0 || rule.CloseRatioPct <= 0 {
			continue
		}
		if currentPnLPct < rule.MinProfitPct {
			// Tier not yet reached — its resting order is legitimately waiting; do not
			// require coverage and do not touch its breaker.
			continue
		}
		key := reArmFailKey(symbol, side, rule, entryPrice)
		// 跳闸不是永久判决:expireReArmBreaker 在 reArmBreakerCooldown 之后自动放行一次
		// 重挂尝试,所以"交易所侧永远不再补挂"这个洞由冷却衰减堵住,而每轮 churn 仍被压制。
		// managed monitor 全程陪跑,不因跳闸而停。
		if at.expireReArmBreaker(key) {
			// Already tripped on an earlier poll. Do NOT query coverage (the gate now
			// reports a tripped tier as uncovered by design) and do NOT re-arm the local
			// monitor — for a partial tier that helper places exchange orders, so calling
			// it every poll would be exactly the churn the breaker exists to stop. Report
			// the tier as uncovered so this verdict stays honest: the exchange side is not
			// being maintained for it, and the managed monitor is its only executor.
			allCovered = false
			continue
		}
		if at.exchangeSideCoversDrawdownTierWithOrders(symbol, side, rule, entryPrice, markPrice, openOrders) {
			at.resetReArmFail(key)
			continue
		}
		// Satisfied tier not effectively covered despite the arm attempt this poll.
		fails := at.bumpReArmFail(key)
		if fails >= reArmBreakerLimit {
			logger.Warnf("🔴 Re-arm breaker TRIPPED (%s %s close=%.1f%% fails=%d) — stop re-placing exchange trailing, managed monitor owns this tier", symbol, side, rule.CloseRatioPct, fails)
			// Best-effort: for a partial tier this stages conditional TP-style orders and
			// upgrades the panel state. For a close>=100 tier it only records state — it
			// places nothing and registers no executor, which is precisely why the tier
			// must NOT be reported as covered here.
			at.applyExchangeFailedLocalMonitor(symbol, side, entryPrice, rule, calculateProfitBasedTrailingTriggerPrice(entryPrice, side, rule.MinProfitPct), calculateDrawdownRuleCallbackRatio(entryPrice, side, rule))
			allCovered = false
			continue
		}
		logger.Warnf("🟡 Re-arm attempt %d/%d not yet effective (%s %s close=%.1f%%) — managed backup supplements this poll", fails, reArmBreakerLimit, symbol, side, rule.CloseRatioPct)
		allCovered = false
	}
	return allCovered
}

// reArmBreakerTripped reports whether a tier's re-arm breaker has already tripped,
// so the arm loop can SKIP calling applyNativeTrailingDrawdown for it (no more
// place attempts — the managed monitor owns it). Prevents the cancel→re-place churn
// once we've decided the exchange side is unreliable for this tier.
func (at *AutoTrader) reArmBreakerTripped(symbol, side string, rule store.DrawdownTakeProfitRule, entryPrice float64) bool {
	// Time-decaying, not a permanent latch — see expireReArmBreaker for why the latch
	// was a protection-losing deadlock.
	return at.expireReArmBreaker(reArmFailKey(symbol, side, normalizeDrawdownRule(rule), entryPrice))
}

func (at *AutoTrader) findEquivalentPartialTrailingOrder(symbol, side string, rule store.DrawdownTakeProfitRule, entryPrice float64, openOrders []OpenOrder) (*nativeTrailingOrder, float64, float64, float64) {
	plannedActivationPrice := calculateProfitBasedTrailingTriggerPrice(entryPrice, side, rule.MinProfitPct)
	plannedCallbackRate := calculateDrawdownRuleCallbackRatio(entryPrice, side, rule)
	// Authoritative identity match first: the orderID we persisted for this exact tier
	// pins its live order regardless of how qty/activation/callback have drifted (an
	// activated trail reports a moving StopPrice; siblings share the same cumulative
	// qty). This is what stops the multi-tier churn where a partial tier could not be
	// re-found and was re-placed every 300s cooldown. Fuzzy matching below is the
	// fallback for tiers with no stored ID (legacy / pre-restart).
	if wantID := at.storedTrailingOrderIDForRule(symbol, side, entryPrice, rule); wantID != "" {
		if found := findTrailingOrderByID(side, wantID, openOrders); found != nil {
			return found, found.Quantity, plannedActivationPrice, plannedCallbackRate
		}
		// Stored ID exists but the order is gone → genuinely missing; report nil so the
		// caller re-arms this tier (do NOT fuzzy-match onto a sibling tier's order).
		return nil, 0, plannedActivationPrice, plannedCallbackRate
	}
	currentQty := 0.0
	markPrice := 0.0
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
			markPrice, _ = pos["markPrice"].(float64)
			break
		}
	}
	// Once the mark has passed the planned activation, this tier's order is no longer
	// a resting order sitting at the planned anchor — it is either the now-activated
	// original OR an immediate-anchor we deliberately re-placed for the passed tier
	// (activation ≈ mark, deep in profit). Its activation price therefore no longer
	// equals the planned value, so activation-price matching would spuriously miss it
	// and the tier would churn (re-place) / double-fire (managed supplements). In that
	// zone we identify the tier by qty+callback alone (both are per-tier discriminators:
	// cumulative ratio and MaxDrawdown-derived callback differ across tiers).
	markPassedPlanned := markPrice > 0 && plannedActivationPrice > 0 &&
		((strings.EqualFold(side, "long") && markPrice >= plannedActivationPrice) ||
			(strings.EqualFold(side, "short") && markPrice <= plannedActivationPrice))
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
		// Orders another armed tier already claims by orderID are off-limits. We only
		// reach this fuzzy loop because THIS tier has no stored ID, and the
		// discriminators below are weak under place-at-open: dd1 (full) and the partial
		// tiers rest concurrently, a 100%-cumulative partial shares qty with the full
		// tier, and on Binance both callbackOK and activationOK degenerate to "always
		// true" (the venue reports neither callbackRate nor a real activation status).
		// Without this guard an unidentified tier fuzzy-matches onto a SIBLING's live
		// order and the caller then cancels it as its own replacement.
		claimed := at.claimedTrailingOrderIDsForPosition(symbol, side, entryPrice, stableDrawdownRuleFingerprint(entryPrice, rule))
		for _, order := range openOrders {
			if order.PositionSide != "" && !strings.EqualFold(order.PositionSide, strings.ToUpper(side)) {
				continue
			}
			if !strings.Contains(strings.ToUpper(order.Type), "TRAILING") {
				continue
			}
			if _, owned := claimed[order.OrderID]; owned {
				continue
			}
			qtyOK := math.Abs(order.Quantity-qtyTarget) <= qtyTolerance
			// Callback comparison is only meaningful when the venue reports the live
			// order's callback rate. Binance's algo-order list endpoint (SDK
			// GetAlgoOrderResp) carries NO callbackRate field, so CallbackRate reads
			// back as 0 for every Binance trailing order while plannedCallbackRate is
			// e.g. 0.0074 — the comparison could NEVER be satisfied, making this whole
			// fuzzy fallback structurally dead on Binance. That left the stored-orderID
			// path as the only working matcher there, so any tier whose arm failed to
			// persist an ID was judged missing on every poll and re-armed forever
			// (2026-07-27 HYPEUSDT: 174 identical partial trailing orders). The same
			// venue-capability guard already exists in shouldReplacePartialTrailingTier
			// (2026-07-16, commit 5bd81b9) — this mirrors it. OKX populates
			// CallbackRate and keeps the full comparison.
			//
			// Note the trade-off: without callback, qty+activation are weaker
			// discriminators between sibling tiers (a 100%-cumulative partial tier and
			// a full tier can share both). That is acceptable ONLY as a fallback —
			// the stored-orderID path above is authoritative and is tried first.
			callbackOK := order.CallbackRate <= 0 ||
				math.Abs(order.CallbackRate-plannedCallbackRate) <= callbackTolerance
			// Match the tier's activation anchor OR, once mark has passed the planned
			// activation, accept the immediate-anchor/activated order by qty+callback so a
			// deliberately re-placed passed tier isn't seen as missing (would churn/double-fire).
			//
			// An order the venue reports as ACTIVATED is trailing the peak: its StopPrice
			// is the moving stop, which has no relation to the planned activation anchor,
			// so comparing them is meaningless (shouldReplacePartialTrailingTier
			// short-circuits activated orders for exactly this reason). This also covers
			// Binance, whose algo list omits activatePrice — the adapter substitutes
			// triggerPrice for ActivationPrice/StopPrice and always reports
			// ActivationStatus="activated" (futures_orders.go), so before this guard the
			// anchor comparison was 58.5 (moving stop) vs 60.6 (planned) and never matched.
			activationOK := activationMatches(order.StopPrice, plannedActivationPrice) ||
				markPassedPlanned ||
				strings.EqualFold(order.ActivationStatus, "activated")
			if qtyOK && callbackOK && activationOK {
				return &nativeTrailingOrder{
					PositionSide:     order.PositionSide,
					StopPrice:        order.StopPrice,
					CallbackRate:     order.CallbackRate,
					Quantity:         order.Quantity,
					OrderID:          order.OrderID,
					ActivationStatus: order.ActivationStatus,
					ActivationPrice:  order.ActivationPrice,
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

// findPartialTrailingReplacementCandidate picks the live trailing order most likely to
// BE this partial tier when neither the stored orderID nor the fuzzy matcher identified
// it. There is deliberately no score threshold — the caller treats the winner as "my
// tier, drifted" and cancels it after placing the replacement.
//
// That greedy contract was safe when it was written (commit b5a7aca): at most ONE
// trailing order rested per position side, so the best match could only be this tier.
// place-at-open (v1.16.1, a56f741) broke the premise — dd1 (full) and the partial tiers
// now rest CONCURRENTLY — and with no threshold the "best" match is simply whatever
// single order happens to be open, i.e. dd1. Arming a partial tier then cancelled the
// full-close protection outright (reproduced by TestFullAndPartialTiersCoexistPerVenue,
// OKX: full tier armed as algo-1, gone one partial arm later).
//
// claimedExclusions is the set of orderIDs other armed tiers own; they can never be
// this tier and must survive. Residual gap: a sibling whose arm failed to persist an
// orderID is unclaimed and can still be consumed here — the reason every arm path must
// persist its placement ID (see the v1.16.7 Binance/Bitget/OKX persist fixes).
func (at *AutoTrader) findPartialTrailingReplacementCandidate(side string, openOrders []OpenOrder, qtyTarget, plannedActivationPrice, plannedCallbackRate float64, claimedExclusions map[string]struct{}) *nativeTrailingOrder {
	var best *nativeTrailingOrder
	bestScore := math.MaxFloat64
	for _, order := range openOrders {
		if order.PositionSide != "" && !strings.EqualFold(order.PositionSide, strings.ToUpper(side)) {
			continue
		}
		if !strings.Contains(strings.ToUpper(order.Type), "TRAILING") {
			continue
		}
		if _, owned := claimedExclusions[order.OrderID]; owned {
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
	// An ACTIVATED order is actively trailing the peak: its reported StopPrice is the
	// moving stop, NOT the planned activation anchor, so activation-drift is meaningless
	// and re-placing it would cancel live protection and reset the tracked peak. This
	// includes the immediate-anchor order we deliberately place for a passed tier
	// (activated at ≈mark, deep in profit). Never replace an activated tier — matches the
	// full-trailing path which short-circuits activated orders before this check.
	if strings.EqualFold(existing.ActivationStatus, "activated") {
		return false
	}
	if existing.StopPrice <= 0 || plannedActivationPrice <= 0 {
		return false
	}
	activationDrift := math.Abs(existing.StopPrice-plannedActivationPrice) / math.Max(math.Abs(existing.StopPrice), math.Abs(plannedActivationPrice))
	if activationDrift > 0.003 {
		return true
	}
	// Callback-drift check is only meaningful when the venue reports the live
	// order's callback rate. Binance's algo-order list endpoint (and the SDK's
	// GetAlgoOrderResp) carries NO callback/priceRate field, so OpenOrder.CallbackRate
	// is left at 0 for every Binance trailing order. Comparing 0 against the planned
	// ratio (e.g. 0.017) always exceeded the 0.0002 tolerance, so the tier was judged
	// "drifted from plan" and re-armed EVERY monitor cycle (~7/min on SPCX/XAG/XAU),
	// which the reconciler then chased as stale duplicates — an endless place/cancel
	// churn. When CallbackRate is unavailable (<=0) we trust the activation-price match
	// alone. OKX populates CallbackRate, so it still gets the full comparison.
	if existing.CallbackRate <= 0 {
		return false
	}
	callbackDrift := math.Abs(existing.CallbackRate - plannedCallbackRate)
	return callbackDrift > 0.0002
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

// trailingFailureIsPositionGone 判断一次 native trailing 挂单失败是否其实是
// "交易所侧仓位已经没了",而不是"挂单挂不上去"。
//
// 为什么必须区分:applyExchangeFailedLocalMonitor 的语义是"交易所保护失败,本地
// 兜底顶上,面板点红告警"。对一个已经平掉的仓位走这条路是纯假警报 —— 它会
//  1. 给不存在的仓位 arm 一个 managed-drawdown 监控;
//  2. 把状态升级成 *_exchange_failed_armed,面板反色告警;
//  3. 最坏情况下打出"position may be UNPROTECTED"。
//
// 平仓侧本来就存在同步空窗:交易所先成交,本地 order_sync 后落账(实盘实测 19s)。
// 开仓侧已经有 protectionFillGraceWindow 对称保护("还不知道"≠"已经平了"),平仓侧
// 此前没有对应闸门,这里补上。
//
// 判据只认 sentinel,不做字符串匹配 —— 交易所文案会变,sentinel 不会。
// 各交易所适配器各有自己的 sentinel(值相同但类型独立),这里逐个认。
func trailingFailureIsPositionGone(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, binance.ErrPositionGone) ||
		errors.Is(err, okx.ErrPositionGone) ||
		errors.Is(err, bitget.ErrPositionGone)
}

// logNativeTrailingPlacementFailure 统一记录 native trailing 挂单失败。
//
// 补的是一个**观测漏洞**:此前只有 Binance 分支用 Warnf,OKX/Bitget 全部用 Infof。
// 健康巡检按 `[WARN]` 取样,于是 3 个 OKX 交易员的挂单失败在巡检里**完全不可见** ——
// 我自己这一轮巡检就漏过去了,是先按交易所逐个读代码才发现的。
//
// 为什么必须先有 okx.ErrPositionGone 才能提级:平仓同步空窗里"仓位已消失"会稳定
// 触发这条路径(Binance 侧实测 19s 空窗内两轮),不区分就等于把假警报批量塞进 WARN,
// 把刚修好的噪音换个交易所再犯一次。position-gone 走 Infof 静默,其余一律 Warnf。
func (at *AutoTrader) logNativeTrailingPlacementFailure(tierKind, exchange, symbol, side string, err error) {
	if trailingFailureIsPositionGone(err) {
		logger.Infof("🧯 Native %s trailing skipped (%s %s, %s): position no longer open on exchange (local view still stale) — not a placement failure",
			tierKind, symbol, side, exchange)
		return
	}
	logger.Warnf("❌ Native %s trailing drawdown apply failed (%s %s, %s): %v — exchange side NOT covered this poll, managed monitor supplements",
		tierKind, symbol, side, exchange, err)
}

// applyExchangeFailedLocalMonitor is the bottom-line fallback when a native
// trailing order could NOT be placed correctly on the exchange — either the
// placement call failed, or the read-back showed the exchange dropped/misplaced
// the activation price and the adapter cancelled the mis-registered order.
//
// Product decision (per user): rather than leaving an exchange order that would
// trigger at entry instead of the peak (eroding profit), the adapter places
// NOTHING on the exchange. This helper then arms the in-process managed-drawdown
// monitor, which enforces the exact same peak+drawdown rule locally with no
// exchange order (ExchangeOrderID=""). It also upgrades the protection state to
// the "exchange_failed" variant so the protection panel can surface a
// reverse-colour warning: local monitor is active, but the exchange order failed.
func (at *AutoTrader) applyExchangeFailedLocalMonitor(symbol, side string, entryPrice float64, rule store.DrawdownTakeProfitRule, activationPrice, callbackRatio float64) bool {
	if !at.applyManagedDrawdownFallback(symbol, side, entryPrice, rule, activationPrice, callbackRatio) {
		logger.Warnf("❌ Exchange trailing placement failed AND local managed-drawdown monitor could not arm: %s %s — position may be UNPROTECTED", symbol, side)
		return false
	}
	// Upgrade the state from the normal managed-drawdown-armed value so the panel
	// can distinguish a deliberate managed-drawdown arm from an exchange-arm
	// failure that dropped to local monitoring.
	switch at.getProtectionState(symbol, side) {
	case "managed_drawdown_armed":
		at.setProtectionState(symbol, side, "managed_drawdown_exchange_failed_armed")
	case "managed_partial_drawdown_armed":
		at.setProtectionState(symbol, side, "managed_partial_drawdown_exchange_failed_armed")
	}
	logger.Warnf("⚠️ Exchange trailing placement FAILED — LOCAL managed-drawdown monitor ACTIVE (panel warning set): %s %s | activation=%.6f callbackRatio=%.6f close=%.1f%%",
		symbol, side, activationPrice, callbackRatio, rule.CloseRatioPct)
	return true
}

// applyNativeTrailingDrawdown 返回 true 表示"这一档此刻在交易所侧确有有效保护"
// —— 无论是本次刚挂上的,还是本来就已覆盖。
//
// 这里是"档位已覆盖 ⇒ 撤掉开仓时那张 50% immediate trailing"这条规则的唯一落点。
// 之前这条规则被写在六个地方(内部四条刚挂成功的分支 + 全平档末尾 + place-at-open
// 循环外),而"已覆盖"的提前 return true 分支一个都没覆盖到:加仓时每档都已覆盖,
// 于是每次加仓都新挂一张 50% 单又不撤旧的,一个仓位上摞出三张 trailing
// (2026-07-27 生产实况:Binance BN CLUSDT 2.43 + 0.73 + 1.22;OKX ETHUSDT 0.239 中的 0.120)。
//
// 按返回值收口而不是按"走了哪条内部分支"收口,才能同时覆盖刚挂上和已覆盖两种情形;
// 放在这一层而不是调用方,才能同时覆盖开仓路径和运行时 arm 轮询路径
// (auto_trader_risk.go 的两处 + protection_reconciler.go 的两处),后者是重启后
// 内存 hint 丢失、只剩持久化归属记录的那张漏单唯一还能被回收的地方。
//
// cancelImmediateTrailing 本身幂等:撤成功后内存 hint 与归属记录都清掉,再调用即空转。
// 熔断门控放在这一层,和上面"按返回值收口"同一个理由:它必须是不变量,而不是各调用方
// 自觉。原先只有 auto_trader_risk.go:293 那一处 arm loop 查 reArmBreakerTripped,
// 另外四个调用点(同文件 triggered 分支、protection_reconciler 的 arm/triggered 两处、
// protection_execution 的开仓路径)全都绕过它 —— 于是"跳闸后停止在交易所侧重挂、交给
// managed 独占"这个承诺根本兑现不了:2026-07-28 生产实况 CLUSDT short close=100%
// 14:44:31 跳闸,之后 14:46/14:49/15:04/15:10 仍由 reconciler 一路重挂。
//
// 返回 false 而不是 true 是对的:跳闸档位的交易所侧确实没在维护(与
// exchangeSideCoversDrawdownTierWithOrders 对跳闸档的判定一致),managed monitor 是它
// 唯一执行者。顺带也不会去撤开仓那张 immediate trailing —— 不挂新的就不该撤旧的。
func (at *AutoTrader) applyNativeTrailingDrawdown(symbol, side string, entryPrice, markPrice float64, rule store.DrawdownTakeProfitRule) bool {
	if at.reArmBreakerTripped(symbol, side, rule, entryPrice) {
		logger.Infof("🟠 Native trailing arm suppressed by TRIPPED re-arm breaker (%s %s close=%.1f%%) — managed monitor owns this tier", symbol, side, rule.CloseRatioPct)
		return false
	}
	covered := at.armNativeTrailingDrawdownTier(symbol, side, entryPrice, markPrice, rule)
	if covered {
		at.cancelImmediateTrailing(symbol, side)
	}
	return covered
}

func (at *AutoTrader) armNativeTrailingDrawdownTier(symbol, side string, entryPrice, markPrice float64, rule store.DrawdownTakeProfitRule) bool {
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
				existing := at.findExistingFullTrailingOrder(symbol, side, entryPrice, rule, openOrders)
				if existing != nil {
					// Once a native trailing order has ACTIVATED, its exchange-reported
					// trigger is the MOVING trail level (it trails the market), not the
					// fixed activation price planned at open. Comparing that moving level
					// against plannedActivationPrice drifts every cycle as price moves, so
					// the drift check re-armed forever (Binance SPCX/XAG/XAU churn: 4788
					// false "drifted" re-arms, 0 genuine missing events). An armed, present
					// trailing order IS the protection — leave it alone; re-place only when
					// genuinely missing (else branch below). Pre-activation drift comparison
					// still applies to venues reporting a resting, not-yet-activated order.
					if existing.ActivationStatus == "activated" {
						return true
					}
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
					// A matching partial trailing tier exists. Normally keep it as-is. The
					// ONE exception is a PHANTOM tier: resting (not activated) with its
					// activePx already passed by the mark, while profit is at/above the tier
					// floor. OKX will never activate it (dead protection). Previously we left
					// it and handed the runner to a managed monitor; now we RE-PLACE it as an
					// immediate-anchor order (see activation-anchor block below) so real
					// protection sits on the exchange. Detect that case and fall through to
					// re-placement; otherwise keep the existing order.
					phantomProfitable := false
					if existingTier.StopPrice > 0 && !strings.EqualFold(existingTier.ActivationStatus, "activated") && markPrice > 0 {
						activePx := existingTier.ActivationPrice
						if activePx <= 0 {
							activePx = existingTier.StopPrice
						}
						passed := false
						if strings.EqualFold(side, "long") {
							passed = markPrice >= activePx
						} else {
							passed = markPrice <= activePx
						}
						if passed && calculatePositionPnLPct(side, entryPrice, markPrice) >= rule.MinProfitPct {
							phantomProfitable = true
						}
					}
					if !phantomProfitable {
						if existingTier.StopPrice > 0 && plannedActivationPrice > 0 {
							logger.Infof("ℹ️ Native partial trailing tier already exists on exchange (%s %s close=%.1f%% activation=%.6f callback=%.6f status=%s)", symbol, side, rule.CloseRatioPct, existingTier.StopPrice, existingTier.CallbackRate, existingTier.ActivationStatus)
						}
						return true
					}
					logger.Infof("⚡ Phantom partial trailing tier passed activePx while profitable (%s %s close=%.1f%% activePx=%.6f mark=%.6f) — re-placing as immediate-anchor order",
						symbol, side, rule.CloseRatioPct, existingTier.StopPrice, markPrice)
					// Cancel the phantom first so the re-place doesn't stack a duplicate tier.
					if existingTier.OrderID != "" {
						if cancelTrader, ok := at.trader.(interface {
							CancelAlgoOrderByID(symbol string, algoID string) error
						}); ok {
							if err := cancelTrader.CancelAlgoOrderByID(symbol, existingTier.OrderID); err != nil {
								logger.Warnf("⚠️ Failed to cancel phantom trailing tier %s before immediate re-place (%s %s): %v — keeping existing", existingTier.OrderID, symbol, side, err)
								return true
							}
						}
					}
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
	priceBasedCallbackRatio := calculateDrawdownRuleCallbackRatio(entryPrice, side, rule)
	if plannedActivationPrice <= 0 || priceBasedCallbackRatio <= 0 {
		return false
	}
	// Activation anchor (unified reached/unreached protection, 2026-07-26):
	//   - Normal case (mark has NOT yet reached the planned activePx, or mark unknown
	//     at open): keep the planned +minProfit anchor. The order rests on the
	//     exchange and OKX auto-activates when price reaches it.
	//   - Passed case (mark already at/through the planned activePx AND profit is at/
	//     above this tier's floor): the planned anchor is unreachable — placing it
	//     would create a phantom (dead order) or, if dropped, loop. Instead place a
	//     NO-ACTIVATION-PRICE order (activePx=0): the OKX adapter omits activePx so the
	//     venue ACTIVATES IT IMMEDIATELY and trails from the current (profit-locked)
	//     price. This is exactly the user's "无激活价" fallback and is safe precisely
	//     because profit>=floor: the peak is anchored in profit, worst-case close is the
	//     tier's designed retained profit, never a fresh loss.
	//
	//     Why NOT a near-mark anchor (mark*0.9999 etc.): OKX tick-rounding pushes such a
	//     thin (0.01%) offset to the WRONG side of the mark, so the order never activates
	//     (stays pending_activation) → judged phantom → re-placed every poll (observed
	//     live: ~5 replacements/min on WLD short, activePx rounded 0.333867→0.334 above a
	//     0.3339 mark). activePx=0 sidesteps rounding entirely — there is no price to
	//     round. The profit-aware guards (trailingOrderIsDangerousNoActivation /
	//     nativeTrailingEffective) already treat an activated no-activePx order at
	//     profit>=floor as SAFE + EFFECTIVE, and the matcher's markPassedPlanned branch
	//     recognizes it by qty+callback, so it neither churns nor is mistaken for danger.
	//     Un-reached tiers are unaffected: their planned anchor sits >=minProfit% away
	//     from mark, dwarfing tick noise (~0.03%), well inside the matcher's 0.3%
	//     activation tolerance — so they rest cleanly and OKX auto-activates on reach.
	activationPrice := plannedActivationPrice
	if markPrice > 0 && rule.MinProfitPct > 0 {
		currentPnLPct := calculatePositionPnLPct(side, entryPrice, markPrice)
		passed := false
		if strings.EqualFold(side, "long") {
			passed = markPrice >= plannedActivationPrice
		} else {
			passed = markPrice <= plannedActivationPrice
		}
		if passed && currentPnLPct >= rule.MinProfitPct {
			activationPrice = 0 // no activation price → OKX activates immediately from current price
			logger.Infof("⚡ Trailing activation passed → immediate (no-activePx) order: %s %s | mark=%.6f planned=%.6f pnl=%.2f%% floor=%.2f%%",
				symbol, side, markPrice, plannedActivationPrice, currentPnLPct, rule.MinProfitPct)
		}
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
				// Phase 1b: use the tagged variant so the trailing fill's client id
				// encodes the mechanism (native_partial_trailing) and the eventual
				// close attributes 1:1 instead of falling to sync_external/heuristics.
				if tagged, ok := at.trader.(interface {
					SetTrailingStopLossTaggedWithID(symbol string, positionSide string, activationPrice float64, callbackRate float64, quantity float64, reasonTag string) (string, error)
					CancelTrailingStopOrders(symbol string) error
				}); ok {
					// Phase 1a: pass decimal ratio directly (adapter converts internally)
					if placedOrderID, err := tagged.SetTrailingStopLossTaggedWithID(symbol, positionSide, activationPrice, priceBasedCallbackRatio, partialQty, at.drawdownTrailingReasonTag(symbol, side, entryPrice, rule)); err == nil {
						// Caller-side invariant (2026-07-28, defense-in-depth): a successful
						// placement that yields an EMPTY order ID is unrecoverable on any venue
						// that cannot fuzzy-match tiers (ReportsTrailingCallbackRate=false, e.g.
						// Binance) — the tier is judged missing every poll and re-armed without
						// bound. The Binance adapter now returns an error in this case, but this
						// guard is venue-agnostic: never persist an ID-less armed record where it
						// can't be re-found. Drop to the LOCAL managed monitor instead.
						if at.trailingRecordIsUnrecoverable(placedOrderID) {
							logger.Warnf("❌ Native partial trailing placed but returned empty order ID on non-fuzzy-matchable venue (%s %s, %s) — falling back to LOCAL monitor to avoid unbounded re-arm", symbol, side, exchange)
							return at.applyExchangeFailedLocalMonitor(symbol, side, entryPrice, rule, activationPrice, priceBasedCallbackRatio)
						}
						at.setProtectionState(symbol, side, "native_partial_trailing_armed")
						// Persist the tier record + arm timestamp, symmetric with the OKX
						// partial path. Without these two lines this branch armed the tier on
						// the exchange but left NO trace: getArmedDrawdownRuleFingerprintsForPosition
						// never saw the tier, so getDrawdownArmRulesForSelectedRule re-selected it
						// every poll, and with no nativeTrailingArmTime entry the 300s cooldown
						// could not engage either. Result (observed 2026-07-27 on the Binance
						// trader, HYPEUSDT long): a fresh partial trailing order placed every
						// ~10s, dynamicOwner climbing +2 per cycle with no upper bound — the same
						// leak class that hit the OKX 55-order cap.
						at.persistDynamicProtectionRecordWithDetails(symbol, side, "native_partial_trailing", stableDrawdownRuleFingerprint(entryPrice, rule), cumulativeRatio, "armed", placedOrderID, activationPrice, priceBasedCallbackRatio, partialQty)
						if at.nativeTrailingArmTime != nil {
							at.nativeTrailingArmTime[nativeTrailingArmKey(symbol, side, stableDrawdownRuleFingerprint(entryPrice, rule))] = time.Now()
						}
						logger.Infof("🟣 Native partial trailing drawdown armed: %s %s | activation=%.6f callback=%.4f(ratio) close=%.1f%%(cumul) qty=%.4f stage=%s", symbol, side, activationPrice, priceBasedCallbackRatio, cumulativeRatio, partialQty, rule.StageName)
						return true
					} else {
						// Bottom-line fallback: the adapter refuses to leave a
						// mis-registered (immediate-triggering) exchange order, so a
						// failure here means NO exchange trailing order exists. Drop to
						// the LOCAL managed-drawdown monitor and flag the panel that the
						// exchange order failed, rather than leaving profit unprotected.
						// 同上:仓位已消失走"无单可挂"分支,不产生假警报。
						if trailingFailureIsPositionGone(err) {
							logger.Infof("🧯 Native partial trailing skipped: %s %s no longer open on exchange (local view still stale) — no local monitor, no panel warning", symbol, side)
							return false
						}
						logger.Warnf("❌ Native partial trailing drawdown apply failed (%s %s, binance): %v — falling back to LOCAL monitor", symbol, side, err)
						return at.applyExchangeFailedLocalMonitor(symbol, side, entryPrice, rule, activationPrice, priceBasedCallbackRatio)
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
						// Same asymmetry as the Binance branch above: without a persisted
						// record and an arm timestamp this tier is invisible to the arm gate
						// and re-arms every poll forever. Bitget's SetTrailingStopLoss returns
						// no order ID, so the record carries an empty ExchangeOrderID and the
						// matcher falls back to fuzzy matching — acceptable, and far better
						// than no record at all.
						at.persistDynamicProtectionRecordWithDetails(symbol, side, "native_partial_trailing", stableDrawdownRuleFingerprint(entryPrice, rule), cumulativeRatio, "armed", "", activationPrice, priceBasedCallbackRatio, partialQty)
						if at.nativeTrailingArmTime != nil {
							at.nativeTrailingArmTime[nativeTrailingArmKey(symbol, side, stableDrawdownRuleFingerprint(entryPrice, rule))] = time.Now()
						}
						logger.Infof("🟣 Native partial trailing drawdown armed: %s %s | activation=%.6f callback=%.4f close=%.1f%%(cumul) qty=%.4f stage=%s", symbol, side, activationPrice, bitgetCallbackPercent, cumulativeRatio, partialQty, rule.StageName)
						return true
					} else {
						at.logNativeTrailingPlacementFailure("partial", "bitget", symbol, side, err)
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
							existingTier = at.findPartialTrailingReplacementCandidate(side, openOrders, qtyTarget, plannedActivationPrice, plannedCallbackRate,
								at.claimedTrailingOrderIDsForPosition(symbol, side, entryPrice, stableDrawdownRuleFingerprint(entryPrice, rule)))
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
						placedOrderID, err := tagged.SetTrailingStopLossTaggedWithID(symbol, positionSide, activationPrice, okxCallbackRatio, partialQty, at.drawdownTrailingReasonTag(symbol, side, entryPrice, rule))
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
									at.nativeTrailingArmTime[nativeTrailingArmKey(symbol, side, stableDrawdownRuleFingerprint(entryPrice, rule))] = time.Now()
								}
								logger.Infof("🟣 Native partial trailing drawdown armed: %s %s | activation=%.6f callback=%.6f close=%.1f%%(cumul) qty=%.4f stage=%s", symbol, side, activationPrice, okxCallbackRatio, cumulativeRatio, partialQty, rule.StageName)
								return true
							}
							// 读回验证失败:单可能挂上了但看不见,不是"无单可挂",一律 WARN。
							at.logNativeTrailingPlacementFailure("partial", "okx", symbol, side,
								errors.New("new tier not visible after placement"))
						} else {
							at.logNativeTrailingPlacementFailure("partial", "okx", symbol, side, err)
						}
					} else if err := okxTrader.SetTrailingStopLoss(symbol, positionSide, activationPrice, okxCallbackRatio, partialQty); err == nil {
						at.setProtectionState(symbol, side, "native_partial_trailing_armed")
						// The arm timestamp alone is not enough: getDrawdownArmRulesForSelectedRule
						// only consults the cooldown INSIDE the armedFingerprints branch, so a tier
						// with a timestamp but no persisted record skips straight to "selected" and
						// re-arms every poll. Persist the record too (no order ID available on this
						// untagged fallback — the matcher falls back to fuzzy matching).
						at.persistDynamicProtectionRecordWithDetails(symbol, side, "native_partial_trailing", stableDrawdownRuleFingerprint(entryPrice, rule), cumulativeRatio, "armed", "", activationPrice, okxCallbackRatio, partialQty)
						if at.nativeTrailingArmTime != nil {
							at.nativeTrailingArmTime[nativeTrailingArmKey(symbol, side, stableDrawdownRuleFingerprint(entryPrice, rule))] = time.Now()
						}
						logger.Infof("🟣 Native partial trailing drawdown armed: %s %s | activation=%.6f callback=%.6f close=%.1f%%(cumul) qty=%.4f stage=%s", symbol, side, activationPrice, okxCallbackRatio, cumulativeRatio, partialQty, rule.StageName)
						return true
					} else {
						at.logNativeTrailingPlacementFailure("partial", "okx", symbol, side, err)
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
			// Only collapse ORPHAN trailing orders. Predating place-at-open this block
			// canceled EVERY trailing order on the side, assuming a full tier replaces
			// the partial one. Under place-at-open dd1 and partial legitimately rest
			// together, and protectionState is a single string per position that cannot
			// express "both tiers armed" — so arming the full tier while state still
			// reads native_partial_trailing_armed nuked the partial's live order
			// (2026-07-27 SPCX: partial armed 01:56:41, collapsed 01:56:45, then its
			// record kept pointing at the dead algoId → judged missing every poll →
			// panel red + re-arm each cooldown window). Skip any order another armed
			// tier claims; cancel only the genuinely unowned leftovers.
			claimed := at.claimedTrailingOrderIDsForPosition(symbol, side, entryPrice, stableDrawdownRuleFingerprint(entryPrice, rule))
			ids := make([]string, 0)
			skipped := 0
			for _, order := range openOrders {
				if order.PositionSide != "" && !strings.EqualFold(order.PositionSide, strings.ToUpper(side)) {
					continue
				}
				if strings.Contains(strings.ToUpper(order.Type), "TRAILING") {
					if _, owned := claimed[order.OrderID]; owned {
						skipped++
						continue
					}
					ids = append(ids, order.OrderID)
				}
			}
			if skipped > 0 {
				logger.Infof("🛡 Preserving %d live sibling trailing tier(s) claimed by armed records during full-tier migration: %s %s", skipped, symbol, side)
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
		// Phase 1b: use the tagged variant so the trailing fill's client id
		// encodes the mechanism (native_trailing) for 1:1 attribution.
		if tagged, ok := at.trader.(interface {
			SetTrailingStopLossTaggedWithID(symbol string, positionSide string, activationPrice float64, callbackRate float64, quantity float64, reasonTag string) (string, error)
			CancelTrailingStopOrders(symbol string) error
		}); ok {
			// Phase 1a: pass decimal ratio directly (adapter converts internally)
			placedOrderID, err = tagged.SetTrailingStopLossTaggedWithID(symbol, positionSide, activationPrice, priceBasedCallbackRatio, 0, at.drawdownTrailingReasonTag(symbol, side, entryPrice, rule))
			if err != nil {
				// Product decision: the adapter refuses to leave a mis-registered
				// (immediate-triggering) exchange order — it returns an error and
				// places NOTHING rather than an order without the peak activation.
				// So a failure here means there is NO exchange trailing order. Drop
				// to the LOCAL managed-drawdown monitor (which enforces the same
				// peak+drawdown rule in-process) and flag the panel that the exchange
				// order failed, so profit is never left unprotected.
				// 平仓侧同步空窗闸门:交易所已经没有这个仓位了 —— "无单可挂",
				// 不是"挂单失败"。不 arm 本地兜底、不点亮 exchange_failed 面板告警,
				// 让常规的 inactive-symbol 清理去收尾。
				if trailingFailureIsPositionGone(err) {
					logger.Infof("🧯 Native trailing skipped: %s %s no longer open on exchange (local view still stale) — no local monitor, no panel warning", symbol, side)
					return false
				}
				logger.Warnf("❌ Native trailing exchange placement failed (%s %s): %v — falling back to LOCAL monitor", symbol, side, err)
				return at.applyExchangeFailedLocalMonitor(symbol, side, entryPrice, rule, activationPrice, priceBasedCallbackRatio)
			}
		} else {
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
			at.logNativeTrailingPlacementFailure("full", exchange, symbol, side, err)
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
			placedOrderID, err = tagged.SetTrailingStopLossTaggedWithID(symbol, positionSide, activationPrice, okxCallbackRatio, 0, at.drawdownTrailingReasonTag(symbol, side, entryPrice, rule))
			if err != nil {
				at.logNativeTrailingPlacementFailure("full", exchange, symbol, side, err)
				return false
			}
			if staleFullTrailing != nil && staleFullTrailing.OrderID != "" {
				if err := tagged.CancelTrailingStopOrdersByIDs(symbol, []string{staleFullTrailing.OrderID}); err != nil {
					logger.Infof("⚠️ Failed to cancel replaced native full trailing order (%s %s, okx): %v", symbol, side, err)
				}
			}
		} else if err := okxTrader.SetTrailingStopLoss(symbol, positionSide, activationPrice, okxCallbackRatio, 0); err != nil {
			at.logNativeTrailingPlacementFailure("full", exchange, symbol, side, err)
			return false
		}
	default:
		return false
	}

	if isPartial {
		at.setProtectionState(symbol, side, "managed_partial_drawdown_armed")
		logger.Infof("🟣 Managed partial drawdown armed: %s %s | activation=%.6f callbackRatio=%.6f close=%.1f%%", symbol, side, activationPrice, priceBasedCallbackRatio, rule.CloseRatioPct)
	} else {
		// Dormant-risk invariant (2026-07-28, global): a full-tier native trailing
		// record with an empty ExchangeOrderID is only recoverable on venues that echo
		// back callbackRate (ReportsTrailingCallbackRate) — the fuzzy matcher needs it.
		// Where it does not (Binance), an ID-less armed record can never be re-found, so
		// the tier is judged missing every poll and re-armed without bound. Binance's
		// adapter now returns an error on AlgoId==0 (handled above → local fallback), so
		// this should be unreachable for Binance; the guard stays as the caller-side
		// safety net for any current/future venue that returns an empty ID yet cannot
		// fuzzy-match. Drop to the LOCAL managed monitor (double cover, never takeover).
		if at.trailingRecordIsUnrecoverable(placedOrderID) {
			logger.Warnf("❌ Native trailing armed with empty exchange order ID on a non-fuzzy-matchable venue (%s %s, %s) — treating as placement failure, falling back to LOCAL monitor", symbol, side, exchange)
			return at.applyExchangeFailedLocalMonitor(symbol, side, entryPrice, rule, activationPrice, priceBasedCallbackRatio)
		}
		at.setProtectionState(symbol, side, "native_trailing_armed")
		at.persistDynamicProtectionRecordWithDetails(symbol, side, "native_trailing", stableDrawdownRuleFingerprint(entryPrice, rule), rule.CloseRatioPct, "armed", placedOrderID, activationPrice, priceBasedCallbackRatio, 0)
		// Record the arm time so the 300s cooldown (getDrawdownArmRulesForSelectedRule) suppresses
		// re-arm loops for the full tier, symmetric with the partial-tier paths above. Without this,
		// a full-tier arm never populates nativeTrailingArmTime[fingerprint], so if the stored orderID
		// falls out of the exchange snapshot (e.g. after restart collapse + snapshot lag) the full tier
		// re-arms every cycle and stacks orders until the OKX 55-order cap rejects everything.
		if at.nativeTrailingArmTime != nil {
			at.nativeTrailingArmTime[nativeTrailingArmKey(symbol, side, stableDrawdownRuleFingerprint(entryPrice, rule))] = time.Now()
		}
		logger.Infof("🟣 Native trailing drawdown armed: %s %s | activation=%.6f callbackRatio=%.6f", symbol, side, activationPrice, priceBasedCallbackRatio)
	}
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

	// 本轮已被某一档认领的 BE 挂单价 → 该档信息。用来守住"一张活着的保护单只能有
	// 一个主人"这个不变量的**上游**:不是等两档都写进账本后再由 reconciler 去收敛,
	// 而是根本不让第二档去认领同一张单。
	//
	// 为什么会有两档指向同一张单(2026-07-31 线上 ETHUSDT short 实况):
	// matchingBreakEvenOrderID 只按价格匹配,容差 protectionPriceTolerancePct=0.2%。
	// ATR 解析后 BE1 offset=0.3000、BE2 offset=0.4498,两档挂单价 1862.1467/1859.3489
	// 相对间距只有 0.1503% < 0.2%,于是 BE2 匹配到了 BE1 那张单:
	//   - BE2 认为"本档已在场",从不挂自己的单(这一点修复前后一致,见下);
	//   - 但两档都把同一个 orderID 写成 armed,supersedeConflictingOrderClaim 每轮
	//     把对方标 superseded,双方轮流成为"新写入的那条",规则不构成收敛 ——
	//     22:06:49→22:11:09 翻转 53 次,只因仓位平掉才停。
	//
	// 危害不是"多一条记录":档位→单的解析会因此依赖谁先被读到,而 100% 全平档一旦
	// 被误判成已执行,该档的保护就静默消失。
	//
	// 收敛规则:**先到者赢**。rules 已按 TriggerValue 升序排好,先到的就是低档位,
	// 也正是交易所上那张单的真实挂单价所属档位 —— 账本因此与实物一致,而不是反过来
	// 让账本记一个不存在的价格。被抑制的那一档不写记录、不挂单、不撤单,所以修复
	// **不改变任何实物挂单行为**,只把账本从自相矛盾变成自洽。
	//
	// 只在"先到那档的数量确实覆盖得住本档"时才抑制。覆盖不住就不抑制 —— 那是真实的
	// 覆盖不足,必须留在原路径上并打出来,而不是被这里悄悄吞掉。
	claimedThisCycle := make([]claimedBETier, 0, len(rules))
	// 返回 (先到档位, 是否应抑制本档)。判定本身是纯函数(findConflictingBETierClaim),
	// 这里只负责把"撞价但覆盖不住"这一例外打出来 —— 它是真实的覆盖不足,不能静默。
	findConflictingClaim := func(stage string, price, qty float64) (claimedBETier, bool) {
		winner, decision := findConflictingBETierClaim(claimedThisCycle, price, qty)
		if decision == beTierClaimUndercovered {
			logger.Warnf("⚠️ BE tier %s (%s %s): price %.6f collides with tier %s @ %.6f (within %.2f%% tolerance) but that tier only covers %.8f of %.8f — NOT suppressing, this tier keeps its own path",
				stage, symbol, side, price, winner.stage, winner.price, protectionPriceTolerancePct*100, winner.qty, qty)
			return claimedBETier{}, false
		}
		return winner, decision == beTierClaimSuppress
	}

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

		// 本档与已认领档位撞进价格容差带 → 不认领、不挂单、不撤单,直接跳过。
		// 跳过的这一档在交易所上**本来也挂不出单**:matchingBreakEvenOrderID 会把兄弟档
		// 那张单匹配成"本档已在场"。所以抑制不损失任何实物保护,只是不再往账本里写
		// 第二个主人。价格拉开到容差带外后,本档下一轮会自然走回正常挂单路径(自愈)。
		if winner, suppress := findConflictingClaim(stage, newBEPrice, ruleQty); suppress {
			logger.Infof("🟠 BE tier %s suppressed (%s %s): its price %.6f is within %.2f%% of tier %s @ %.6f which already owns the live order — one live order must have exactly one owner; widen the tiers' offset_pct to give this tier its own order",
				stage, symbol, side, newBEPrice, protectionPriceTolerancePct*100, winner.stage, winner.price)
			at.retireSuppressedBreakEvenTierRecord(symbol, side, stage, winner.stage)
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

		// Retire this tier's own stale order before deciding whether a live order
		// already covers it. "Stale" means either the price drifted (cost line moved)
		// OR the quantity no longer covers the position (加仓 grew the position but the
		// BE order stayed at its old size — the exact "止损止不了血" case). Running this
		// BEFORE the price-match check is what lets an add-on-grown position get a
		// correctly-sized BE order: an order at the right price but too-small qty is
		// stale too, and must be replaced rather than accepted as adequate coverage.
		//
		// Break-even price tracks the live position average on purpose (that is what
		// "retreat to cost" means), but tracking it while only ever placing and never
		// cancelling is what stacked 4 BE orders on WLDUSDT for a 2-tier config. Cancel
		// is by persisted order ID, per tier — never by reason tag, since every tier's
		// mechanism code is "BE" and a tag cancel would take the sibling tier down with
		// it. Only undersized/mispriced orders are cancelled; oversized reduce-only BE
		// orders are harmless and left alone.
		if cancelled := at.replaceStaleBreakEvenTierOrders(symbol, side, stage, newBEPrice, ruleQty, openOrders); cancelled > 0 {
			openOrders, openOrdersErr = at.trader.GetOpenOrders(symbol)
			if openOrdersErr != nil {
				// Lost visibility after cancelling. Placing blind could double up, so
				// stop here; the next monitor poll re-runs this tier from a clean read.
				logger.Warnf("⚠️ BE tier %s: re-fetch open orders after replace failed (%s %s): %v", stage, symbol, side, openOrdersErr)
				continue
			}
		}

		// Check if this tier already has a matching exchange order (any stale undersized
		// or mispriced order for this tier was just retired above, so a match here is a
		// live order at the right price AND adequate size).
		// Persist the live exchange order ID so ownership classification can claim
		// this tier by handle. Writing "" here would erase the ID recorded when the
		// order was first placed, which is what let sibling BE tiers be mistaken for
		// stale duplicates and cancelled on every poll.
		if liveID, found := matchingBreakEvenOrderID(openOrders, positionSide, newBEPrice); found {
			// Ensure armed state is persisted
			at.persistDynamicProtectionRecordWithDetails(symbol, side, "break_even_stop", fmt.Sprintf("%.8f|%.8f|%.4f|%.4f|%s", entryPrice, quantity, rule.TriggerValue, rule.OffsetPct, stage), 0, "armed", liveID, newBEPrice, 0, ruleQty)
			claimedThisCycle = append(claimedThisCycle, claimedBETier{stage: stage, price: newBEPrice, qty: ruleQty})
			continue
		}

		// Place this tier's stop order
		cfg := store.BreakEvenStopConfig{Enabled: true, Mode: store.ProtectionModeManual, TriggerMode: rule.TriggerMode, TriggerValue: rule.TriggerValue, OffsetPct: rule.OffsetPct}
		if err := at.applyBreakEvenStop(symbol, side, ruleQty, entryPrice, currentPnLPct, cfg, stage); err != nil {
			logger.Warnf("❌ BE tier %s apply failed (%s %s): %v", stage, symbol, side, err)
			continue
		}
		// 挂单成功(或 applyBreakEvenStop 内部认到了在场同价单)后本档已是这张单的主人,
		// 登记进本轮认领表,后续更高档位撞进容差带时才知道该抑制谁。
		claimedThisCycle = append(claimedThisCycle, claimedBETier{stage: stage, price: newBEPrice, qty: ruleQty})
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
		if liveID, found := matchingBreakEvenOrderID(openOrders, positionSide, breakEvenPrice); found {
			logger.Infof("🟠 Break-even stop already live: %s %s | stop=%.6f id=%s", symbol, side, breakEvenPrice, liveID)
			at.persistDynamicProtectionRecordWithDetails(symbol, side, "break_even_stop", fmt.Sprintf("%.8f|%.8f|%.4f|%.4f|%s", entryPrice, quantity, cfg.TriggerValue, cfg.OffsetPct, stage), 0, "armed", liveID, breakEvenPrice, 0, quantity)
			return nil
		}
	} else {
		logger.Warnf("⚠️ Break-even live-order precheck failed (%s %s): %v", symbol, side, err)
	}

	// Break-even stop is managed independently. Do not cancel existing ladder/full stop-loss
	// orders here, otherwise we destroy the long-term stop-loss protection stack.
	// If exchanges later support per-order tags / amend-by-id, we can target only prior
	// break-even stops. For now, preserve existing SL orders and add break-even separately.
	// placedOrderID:交易所回的这一档 BE 单句柄。必须持久化 —— 没有它,"按档替换旧 BE 单"
	// 只能靠 reason tag,而所有档位共用同一个 mechanism code "BE"(store/reason_codec.go),
	// 按 tag 撤单会连兄弟档一起撤掉(v1.16.5 那一族事故)。
	var placedOrderID string
	if okxTrader, ok := at.trader.(interface {
		SetStopLoss(symbol string, positionSide string, quantity, stopPrice float64) error
		SetStopLossTagged(symbol string, positionSide string, quantity, stopPrice float64, reasonTag string) (string, error)
	}); ok {
		algoID, err := okxTrader.SetStopLossTagged(symbol, positionSide, quantity, breakEvenPrice, "break_even_stop")
		if err != nil {
			return fmt.Errorf("failed to set break-even stop loss: %w", err)
		}
		placedOrderID = algoID
		at.recordProtectionIntent(symbol, positionSide, "break_even_stop", quantity, breakEvenPrice, algoID)
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
			// Venues whose SetStopLoss returns no handle (the non-OKX branch above): recover
			// the id from the verification snapshot we already fetched, so this tier is still
			// individually cancellable later.
			if placedOrderID == "" {
				if id, found := matchingBreakEvenOrderID(openOrders, positionSide, breakEvenPrice); found {
					placedOrderID = id
				}
			}
			logger.Infof("✅ Break-even stop verified: %s %s | stop=%.6f id=%s (attempt %d/%d)",
				symbol, side, breakEvenPrice, placedOrderID, attempt, protectionVerifyMaxAttempts)
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
	at.persistDynamicProtectionRecordWithDetails(symbol, side, "break_even_stop", fmt.Sprintf("%.8f|%.8f|%.4f|%.4f|%s", entryPrice, quantity, cfg.TriggerValue, cfg.OffsetPct, stage), 0, "armed", placedOrderID, breakEvenPrice, 0, quantity)
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

// closePositionByReason 关闭仓位。返回 nil 表示"没有出错",**不表示一定平掉了** ——
// 交易所侧的 NO_POSITION/SKIPPED/POSITION_DUST 都被折叠成 nil(见 closeOrderSkipped)。
// 需要区分"平掉了"和"跳过了"的调用方必须用 closePositionByReasonWithOutcome。
func (at *AutoTrader) closePositionByReason(symbol, side string, quantity float64, closeReason string) error {
	_, err := at.closePositionByReasonWithOutcome(symbol, side, quantity, closeReason)
	return err
}

// closePositionByReasonWithOutcome 是 closePositionByReason 的可观测形式:除了 error,
// 还回一个 executed 布尔 —— 交易所确实收下了平仓单才为 true。
//
// 为什么需要它(2026-07-30/31 线上):closeOrderSkipped 把 NO_POSITION 折叠成
// (nil) 之后,调用方无法区分"平掉了"和"仓位早就没了、什么都没做"。于是 time-stop
// 在 10 秒内触发两次,第二次撞上 NO_POSITION 却照样打出 `✅ Time-stop closed`
// —— CLUSDT 与 ETHUSDT 各有一次。幂等性本身是好的(没有重复下单,2 条
// NO_POSITION 警告与 2 次重复触发一一对应),坏的是日志在说一件没发生的事:
// 事后按 `✅ Time-stop closed` 计数会把平仓次数数成 4 次而实际只有 2 次。
//
// 这个函数不改变任何下单行为,只把已有的 skip 事实回传给调用方。
func (at *AutoTrader) closePositionByReasonWithOutcome(symbol, side string, quantity float64, closeReason string) (bool, error) {
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
			return false, err
		}
		if skipped, reason := closeOrderSkipped(order); skipped {
			logger.Warnf("⚠️ Close long not executed (%s): %s — %v", symbol, reason, order["message"])
			return false, nil
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
			return false, err
		}
		if skipped, reason := closeOrderSkipped(order); skipped {
			logger.Warnf("⚠️ Close short not executed (%s): %s — %v", symbol, reason, order["message"])
			return false, nil
		}
		logger.Infof("✅ Close short position succeeded, order ID: %v", order["orderId"])
		at.persistCloseReasonFromOrderResult(order, closeReason)
		at.recordCloseIntentFromOrderResult(order, symbol, "SHORT", closeReason, quantity)
	default:
		return false, fmt.Errorf("unknown position direction: %s", side)
	}

	return true, nil
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
func (at *AutoTrader) recordProtectionIntent(symbol, positionSide, mechanism string, quantity, triggerPrice float64, exchangeOrderID string) {
	if at.store == nil || mechanism == "" || triggerPrice <= 0 {
		return
	}
	if err := at.store.CloseIntent().RecordProtection(
		at.id, at.exchangeID, market.Normalize(symbol), positionSide, mechanism, quantity, triggerPrice, at.cycleNumber, exchangeOrderID,
	); err != nil {
		logger.Warnf("⚠️ Failed to record protection intent for %s %s (%s @ %.6f, orderID=%s): %v", symbol, positionSide, mechanism, triggerPrice, exchangeOrderID, err)
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

// ClearPeakPnLCache clears the peak/trough/ATR-mult caches for a position and
// removes the persisted excursion row (called on close).
func (at *AutoTrader) ClearPeakPnLCache(symbol, side string) {
	at.peakPnLCacheMutex.Lock()
	posKey := symbol + "_" + side
	delete(at.peakPnLCache, posKey)
	delete(at.troughPnLCache, posKey)
	delete(at.peakAtrMultCache, posKey)
	delete(at.troughAtrMultCache, posKey)
	at.peakPnLCacheMutex.Unlock()

	if at.store != nil {
		if err := at.store.DeletePeakPnL(at.id, posKey); err != nil {
			logger.Warnf("⚠️ Failed to delete persisted excursion for %s: %v", posKey, err)
		}
	}
}

// resetPerPositionStateOnOpen wipes ALL in-memory per-position state keyed by
// symbol|side at the moment a brand-new position is opened. This is the definitive
// guard against cross-position state carryover (fix 2026-07-17): the reconcile
// sweep evicts state when a position closes, but a new position can open on the same
// symbol|side BEFORE the next reconcile pass runs — and sync/exchange-side closes
// may never have cleared some maps at all. Resetting unconditionally at open ensures
// a new position never inherits the prior position's drawdown tiers, peak/trough
// excursion, break-even/protection status, runner semantics, AI rules, or the
// structural-SL fire dedup. Idempotent and safe: a freshly opened position legitimately
// has no prior state to preserve.
func (at *AutoTrader) resetPerPositionStateOnOpen(symbol, side string) {
	sideLower := strings.ToLower(side)
	key := positionKey(symbol, sideLower)

	// Excursion caches (peak/trough/ATR-mult) + persisted excursion row.
	at.ClearPeakPnLCache(symbol, sideLower)

	// Drawdown tier allocations — the SPCX false-DD root cause.
	at.clearDrawdownTierAllocs(symbol, sideLower)

	// Drawdown runner + executed-fingerprint + armed records.
	at.clearDrawdownRunnerState(symbol, sideLower)
	at.clearDrawdownExecutionFingerprint(symbol, sideLower)

	// Break-even lifecycle state.
	at.clearBreakEvenState(symbol, sideLower)

	// Protection status + immediate trailing order id.
	at.clearProtectionState(symbol, sideLower)
	at.clearImmediateTrailingOrderID(symbol, sideLower)

	// AI rule/source markers + structural-SL dedup that lack dedicated clearers.
	at.protectionStateMutex.Lock()
	delete(at.drawdownAIRules, key)
	delete(at.drawdownSource, key)
	at.protectionStateMutex.Unlock()

	at.breakEvenStateMutex.Lock()
	delete(at.breakEvenSource, key)
	at.breakEvenStateMutex.Unlock()

	at.structSLMutex.Lock()
	delete(at.structSLFiredBar, key)
	at.structSLMutex.Unlock()

	at.gbGuardMutex.Lock()
	delete(at.gbPnlHist, key)
	at.gbGuardMutex.Unlock()

	// Re-arm breaker counters for this position (keyed symbol_side_tierFP).
	at.clearReArmFailForPosition(symbol, sideLower)

	// 300s 重复武装冷却(keyed symbol|side|ruleIdentity)。
	// 这一项以前不需要清:key 里含开仓均价,换仓自然换 key。改成"与开仓价无关的
	// 规则身份"之后 key 变稳定了,不清就会让新仓位继承上一个仓位的冷却窗口 ——
	// 平仓后 300s 内在同一 symbol|side 上重开,保护单会被冷却挡住挂不上去。
	// 与本包既有约定一致:该 map 由交易循环单线程访问,不额外加锁。
	armKeyPrefix := strings.ToLower(symbol) + "|" + sideLower + "|"
	for k := range at.nativeTrailingArmTime {
		if strings.HasPrefix(k, armKeyPrefix) {
			delete(at.nativeTrailingArmTime, k)
		}
	}

	// Candidate-ATR scaffold cache (only populated when StructuralSL.DynamicATR on).
	// Keyed by frozenATRKey (id|symbol|SIDE[|@tf]) — clear both the default 1h and any
	// suffixed-timeframe entries for this symbol|side so a new position recomputes.
	atrPrefix := at.id + "|" + symbol
	at.candidateATRMutex.Lock()
	for k := range at.candidateATRCache {
		if strings.HasPrefix(k, atrPrefix) && strings.Contains(strings.ToUpper(k), strings.ToUpper(sideLower)) {
			delete(at.candidateATRCache, k)
			delete(at.candidateATRAtMs, k)
		}
	}
	at.candidateATRMutex.Unlock()
}

// reArmFailKey builds the per-tier breaker key. The tier fingerprint isolates the
// counter per drawdown rule so one flaky tier tripping its breaker never disables
// arming for the other tiers on the same position.
// 用规则身份而不是带开仓价的原串:否则开仓均价一被修正,同一档梯度的失败计数就换了
// 一个新桶从 0 开始,连续失败永远攒不到 3 次,熔断器(降级到 managed)形同不存在。
func reArmFailKey(symbol, side string, rule store.DrawdownTakeProfitRule, entryPrice float64) string {
	return strings.ToLower(symbol) + "_" + strings.ToLower(side) + "_" + drawdownRuleIdentity(stableDrawdownRuleFingerprint(entryPrice, rule))
}

// bumpReArmFail increments and returns the consecutive-failure count for a tier.
func (at *AutoTrader) bumpReArmFail(key string) int {
	at.reArmFailMutex.Lock()
	defer at.reArmFailMutex.Unlock()
	if at.reArmFailCache == nil {
		at.reArmFailCache = make(map[string]int)
	}
	at.reArmFailCache[key]++
	if at.reArmTripTime == nil {
		at.reArmTripTime = make(map[string]time.Time)
	}
	if at.reArmFailCache[key] >= reArmBreakerLimit {
		// Stamp (or re-stamp) the trip so the cooldown below is measured from the most
		// recent failure, not the first one.
		at.reArmTripTime[key] = time.Now()
	}
	return at.reArmFailCache[key]
}

// resetReArmFail clears a tier's consecutive-failure count after a verified success.
func (at *AutoTrader) resetReArmFail(key string) {
	at.reArmFailMutex.Lock()
	delete(at.reArmFailCache, key)
	delete(at.reArmTripTime, key)
	at.reArmFailMutex.Unlock()
}

// reArmBreakerCooldown is how long a tripped tier stays off the exchange before the
// breaker allows one more attempt. It bounds the RATE of re-placement; it must never
// bound the TOTAL. See expireReArmBreaker.
const reArmBreakerCooldown = 30 * time.Minute

// expireReArmBreaker lets a tripped breaker re-arm after reArmBreakerCooldown, and
// reports whether the tier is still tripped.
//
// The breaker used to be a permanent latch, and that is a protection-losing bug rather
// than a conservative choice. resetReArmFail has exactly one caller — the verified-
// coverage branch of accountReArmBreaker — and verified coverage requires a live
// exchange order. Once tripped, the arm loop stops placing that order, so coverage can
// never be verified, so the counter can never reset: no arm → no order → no coverage →
// no reset → no arm. The tier's exchange side is abandoned for the whole life of the
// position, which is exactly what happened to GPT SKHYNIXUSDT dd1 after the 19:13:46
// trip on 2026-07-28 (the deadlock was even described in the CO-RUN comment at the top
// of this file, but only the managed-evaluation side was ever fixed).
//
// A trip means "the exchange is misbehaving for this tier right now" — a transient
// condition (the trigger here was OKX reporting a phantom pending_activation). The
// correct response is to back off and retry, not to give up permanently. After the
// cooldown we drop the counter to reArmBreakerLimit-1 rather than 0: one attempt is
// allowed, and a single further failure re-trips immediately for another cooldown. So
// a genuinely broken exchange path costs one order per 30 minutes instead of the churn
// the breaker was built to stop, while a recovered path heals on its own.
func (at *AutoTrader) expireReArmBreaker(key string) bool {
	at.reArmFailMutex.Lock()
	defer at.reArmFailMutex.Unlock()
	if at.reArmFailCache[key] < reArmBreakerLimit {
		return false
	}
	trippedAt, ok := at.reArmTripTime[key]
	if !ok {
		// Counter at/over the limit with no timestamp: pre-upgrade state or a lost
		// stamp. Stamp it now so the cooldown starts ticking instead of latching forever.
		if at.reArmTripTime == nil {
			at.reArmTripTime = make(map[string]time.Time)
		}
		at.reArmTripTime[key] = time.Now()
		return true
	}
	if time.Since(trippedAt) < reArmBreakerCooldown {
		return true
	}
	at.reArmFailCache[key] = reArmBreakerLimit - 1
	delete(at.reArmTripTime, key)
	logger.Warnf("🟢 Re-arm breaker cooldown elapsed (%s) after %.0fmin — allowing one more exchange arm attempt (managed monitor kept co-running throughout)", key, time.Since(trippedAt).Minutes())
	return false
}

// 曾经这里有一个 clearReArmBreakerIfNoLiveOrder:"交易所无活单 → 归零计数"。它被删掉了,
// 因为它每轮都归零,等于把断路器整体废掉 —— 而断路器存在的唯一理由就是止住
// place→fail→place 的每轮重试(对交易所 API 的无限 churn)。"永久闩死"这个真问题
// 已经由 expireReArmBreaker 的 reArmBreakerCooldown 冷却衰减解决:跳闸只压制
// reArmBreakerCooldown 这么久,之后自动放行一次重挂尝试,managed 全程陪跑。
// 两者叠加会互相抵消,只能留冷却这一条。

// getReArmFail reads a tier's current consecutive-failure count.
func (at *AutoTrader) getReArmFail(key string) int {
	at.reArmFailMutex.RLock()
	defer at.reArmFailMutex.RUnlock()
	return at.reArmFailCache[key]
}

// clearReArmFailForPosition wipes all breaker counters belonging to a position
// (any tier). Called on new-position identity so counters never carry across
// positions on the same symbol|side.
func (at *AutoTrader) clearReArmFailForPosition(symbol, side string) {
	prefix := strings.ToLower(symbol) + "_" + strings.ToLower(side) + "_"
	at.reArmFailMutex.Lock()
	for k := range at.reArmFailCache {
		if strings.HasPrefix(k, prefix) {
			delete(at.reArmFailCache, k)
		}
	}
	at.reArmFailMutex.Unlock()
}

// reArmBreakerLimit is the consecutive re-arm/verify failure count at which the
// breaker trips: we stop trying to place the exchange trailing order for this tier,
// cancel any residual, and fall back to the in-process managed monitor. Chosen (3)
// to give transient network/exchange hiccups a couple of retries while still cutting
// a churn loop quickly.
const reArmBreakerLimit = 3

// UpdateExcursion tracks the full favorable/adverse excursion (MFE/MAE) of a
// position: the high-water peak profit% and low-water trough profit%, plus each
// extreme expressed in open-time ATR multiples (signed: +favorable / -adverse).
// It also keeps peakPnLCache consistent so existing peak readers are unaffected.
// Persists only when an extreme actually moves, so DB load stays light. ATR% comes
// from the frozen open-time ATR (stable, restart-safe); when ATR protection is off
// or unavailable the ATR multiples stay 0 while the profit% extremes are still kept.
func (at *AutoTrader) UpdateExcursion(symbol, side, posKey string, entryPrice, markPrice float64) {
	if posKey == "" {
		posKey = symbol + "_" + side
	}
	pnlPct := calculatePositionPnLPct(side, entryPrice, markPrice)

	// Resolve open-time ATR% once (best-effort). Only when ATR protection is enabled,
	// so strategies without ATR never trigger a live ATR freeze as a side effect.
	atrPct := 0.0
	if at.config.StrategyConfig != nil && at.config.StrategyConfig.ATRProtection.Enabled && entryPrice > 0 {
		if atr, ok := at.frozenATRForPosition(symbol, side, entryPrice, at.config.StrategyConfig.ATRProtection); ok && atr > 0 {
			atrPct = atr / entryPrice * 100
		}
	}

	at.peakPnLCacheMutex.Lock()
	// Lazy-init: tests construct AutoTrader structs directly (not via the
	// constructor), so the excursion maps may be nil here.
	if at.peakPnLCache == nil {
		at.peakPnLCache = make(map[string]float64)
	}
	if at.troughPnLCache == nil {
		at.troughPnLCache = make(map[string]float64)
	}
	if at.peakAtrMultCache == nil {
		at.peakAtrMultCache = make(map[string]float64)
	}
	if at.troughAtrMultCache == nil {
		at.troughAtrMultCache = make(map[string]float64)
	}
	changed := false
	// Peak (favorable high-water).
	if peak, ok := at.peakPnLCache[posKey]; !ok || pnlPct > peak {
		at.peakPnLCache[posKey] = pnlPct
		if atrPct > 0 {
			at.peakAtrMultCache[posKey] = pnlPct / atrPct
		}
		changed = true
	}
	// Trough (adverse low-water).
	if trough, ok := at.troughPnLCache[posKey]; !ok || pnlPct < trough {
		at.troughPnLCache[posKey] = pnlPct
		if atrPct > 0 {
			at.troughAtrMultCache[posKey] = pnlPct / atrPct
		}
		changed = true
	}
	ex := store.ExcursionState{
		PeakPnlPct:    at.peakPnLCache[posKey],
		TroughPnlPct:  at.troughPnLCache[posKey],
		PeakAtrMult:   at.peakAtrMultCache[posKey],
		TroughAtrMult: at.troughAtrMultCache[posKey],
	}
	at.peakPnLCacheMutex.Unlock()

	if changed && at.store != nil {
		if err := at.store.SaveExcursion(at.id, posKey, ex); err != nil {
			logger.Warnf("⚠️ Failed to persist excursion for %s: %v", posKey, err)
		}
	}
}

// GetExcursion returns the current excursion snapshot for a position (zero-value
// when none tracked yet).
func (at *AutoTrader) GetExcursion(symbol, side string) store.ExcursionState {
	posKey := symbol + "_" + side
	at.peakPnLCacheMutex.RLock()
	defer at.peakPnLCacheMutex.RUnlock()
	return store.ExcursionState{
		PeakPnlPct:    at.peakPnLCache[posKey],
		TroughPnlPct:  at.troughPnLCache[posKey],
		PeakAtrMult:   at.peakAtrMultCache[posKey],
		TroughAtrMult: at.troughAtrMultCache[posKey],
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
