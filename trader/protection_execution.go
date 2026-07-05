package trader

import (
	"fmt"
	"math"
	"strings"
	"time"

	"nofx/kernel"
	"nofx/logger"
	"nofx/store"
	tradertypes "nofx/trader/types"
)

const (
	protectionPriceTolerancePct = 0.002 // 0.2% — tightened to avoid BE/DD price confusion
	protectionSetupMaxAttempts  = 2
	protectionVerifyMaxAttempts = 6 // retry GetOpenOrders verification up to 6 times for OKX TP visibility lag
)

var protectionVerifyDelay = 700 * time.Millisecond // delay between verification attempts

type protectionExecutionRequest struct {
	Symbol       string
	Action       string
	PositionSide string
	Quantity     float64
	EntryPrice   float64
	Decision     *kernel.Decision
}

func (at *AutoTrader) applyPostOpenProtection(req *protectionExecutionRequest) error {
	if req == nil || req.Decision == nil {
		return nil
	}

	configuredPlan, err := at.BuildConfiguredProtectionPlanForSymbol(req.EntryPrice, req.Action, req.Symbol)
	if err != nil {
		return err
	}
	if configuredPlan == nil && at.config.StrategyConfig != nil {
		configuredPlan = buildFallbackMaxLossPlan(req.EntryPrice, req.Action, at.config.StrategyConfig.Protection)
	}
	plan := configuredPlan

	// When the strategy's protection is configured in MANUAL mode, the operator has
	// explicitly fixed the protection scheme (e.g. ladder TP-ladder + fixed SL). The AI
	// may still emit a protection_plan out of habit, but it must NOT override the manual
	// config. Only defer to the AI plan when the relevant legs are in AI mode.
	// (Fixes: 2026-06-05 A1 deploy — AI protection_plan was silently overriding the
	// manual ladder 8% SL + laddered take-profit config.)
	manualProtection := at.usesManualProtection()

	if !manualProtection && req.Decision.ProtectionPlan != nil {
		decisionPlan, err := buildAIProtectionPlan(req.EntryPrice, req.Action, req.Decision.ProtectionPlan, at.config.StrategyConfig)
		if err != nil {
			return err
		}
		if decisionPlan != nil {
			plan = preferDecisionProtectionPlan(configuredPlan, decisionPlan)
			if len(decisionPlan.DrawdownRules) > 0 {
				ddRules := decisionPlan.DrawdownRules
				// Clamp DD tiers to RR target distance if AI gave unreasonable values
				if req.Decision.EntryProtection != nil && req.Decision.EntryProtection.RiskReward.FirstTarget > 0 {
					side := strings.ToLower(req.PositionSide)
					structTargets := extractStructuralTargets(req.Decision, req.EntryPrice, side)
					ddRules = clampDrawdownRulesToTarget(ddRules, req.EntryPrice, req.Decision.EntryProtection.RiskReward.FirstTarget, side, structTargets)
					decisionPlan.DrawdownRules = ddRules
				}
				at.setAIDrawdownRules(req.Symbol, req.PositionSide, ddRules)
				at.initDrawdownTiersFromResolvedRules(req.Symbol, req.PositionSide, req.Quantity, ddRules)
			}
		}
	}

	if at.config.StrategyConfig != nil && protectionRouteRequiresDecisionPlan(at.config.StrategyConfig.Protection, req.Decision.ProtectionPlan, plan) {
		return fmt.Errorf("AI protection route is enabled but the opening decision did not materialize required protection_plan legs; refusing configured/default protection fallback")
	}

	// Structural fallback: if plan has no TP/SL orders, try generating from structural levels
	if plan == nil || (len(plan.TakeProfitOrders) == 0 && !plan.NeedsTakeProfit && len(plan.StopLossOrders) == 0 && !plan.NeedsStopLoss) {
		if mdata, err := at.getExecutionMarketData(req.Symbol); err == nil && mdata != nil {
			isLong := req.Action == "open_long"
			tpOrders, slOrders := generateStructuralLadderRules(req.EntryPrice, isLong, mdata)
			if len(tpOrders) > 0 || len(slOrders) > 0 {
				structPlan := &ProtectionPlan{
					Mode:                 "structural_fallback",
					RequiresNativeOrders: true,
					RequiresPartialClose: len(tpOrders) > 1,
					TakeProfitOrders:     tpOrders,
					StopLossOrders:       slOrders,
					NeedsTakeProfit:      len(tpOrders) > 0,
					NeedsStopLoss:        len(slOrders) > 0,
				}
				if len(slOrders) == 1 {
					structPlan.StopLossPrice = slOrders[0].Price
				}
				logger.Infof("  🏗 Using structural fallback protection: %d TP tiers, %d SL levels", len(tpOrders), len(slOrders))
				plan = mergeProtectionPlans(plan, structPlan)
			}
		}
	}

	var planErr error
	if plan != nil {
		caps := at.GetProtectionCapabilities()
		if plan.RequiresNativeOrders && (!caps.NativeStopLoss && plan.NeedsStopLoss || !caps.NativeTakeProfit && plan.NeedsTakeProfit) {
			return fmt.Errorf("exchange %s cannot safely support required native protection orders", at.exchange)
		}
		if plan.RequiresPartialClose && !caps.NativePartialClose {
			return fmt.Errorf("exchange %s cannot safely support ladder partial-close protection", at.exchange)
		}

		logger.Infof("  🛡 Applying %s protection plan: stop=%v tp=%v ladderSL=%d ladderTP=%d",
			plan.Mode, plan.NeedsStopLoss, plan.NeedsTakeProfit, len(plan.StopLossOrders), len(plan.TakeProfitOrders))
		if err := at.placeAndVerifyProtectionPlanWithRetry(req.Symbol, req.PositionSide, req.Quantity, plan); err != nil {
			planErr = err
			logger.Warnf("  ⚠️ Primary protection plan failed for %s %s: %v", req.Symbol, req.PositionSide, err)
		}
	}

	if err := at.applyNativeProtectionTargetsAfterOpen(req, plan); err != nil {
		if planErr != nil {
			return fmt.Errorf("primary protection failed: %v; native targets failed: %w", planErr, err)
		}
		return err
	}

	if plan != nil && planErr == nil {
		return nil
	}

	needsStopLoss := req.Decision.StopLoss > 0
	needsTakeProfit := req.Decision.TakeProfit > 0
	if !needsStopLoss && !needsTakeProfit {
		if planErr != nil {
			return planErr
		}
		return nil
	}

	logger.Infof("  🛡 Applying AI decision protection fallback: stop=%v@%.6f tp=%v@%.6f",
		needsStopLoss, req.Decision.StopLoss, needsTakeProfit, req.Decision.TakeProfit)
	if err := at.placeAndVerifyProtectionWithRetry(req.Symbol, req.PositionSide, req.Quantity, needsStopLoss, req.Decision.StopLoss, needsTakeProfit, req.Decision.TakeProfit); err != nil {
		if planErr != nil {
			return fmt.Errorf("primary protection failed: %v; fallback failed: %w", planErr, err)
		}
		return err
	}
	return nil
}

func buildFallbackMaxLossPlan(entryPrice float64, action string, protection store.ProtectionConfig) *ProtectionPlan {
	if entryPrice <= 0 {
		return nil
	}
	fallbackPct := 0.0
	if pct, ok := resolveLadderFallbackMaxLoss(protection.LadderTPSL); ok {
		fallbackPct = pct
	}
	if pct, ok := resolveFallbackMaxLoss(protection.FullTPSL); ok && (fallbackPct == 0 || pct > fallbackPct) {
		fallbackPct = pct
	}
	if fallbackPct <= 0 {
		return nil
	}
	move := fallbackPct / 100.0
	price := 0.0
	switch action {
	case "open_long":
		price = entryPrice * (1 - move)
	case "open_short":
		price = entryPrice * (1 + move)
	default:
		return nil
	}
	return &ProtectionPlan{Mode: "fallback_max_loss", RequiresNativeOrders: true, FallbackMaxLossPrice: roundProtectionPrice(price)}
}

func protectionRouteRequiresDecisionPlan(protection store.ProtectionConfig, decisionPlan *kernel.AIProtectionPlan, materialized *ProtectionPlan) bool {
	ladderAI := protection.LadderTPSL.Enabled && protection.LadderTPSL.Mode == store.ProtectionModeAI
	drawdownAI := protection.DrawdownTakeProfit.Enabled && protection.DrawdownTakeProfit.Mode == store.ProtectionModeAI
	fullAI := protection.FullTPSL.Enabled && protection.FullTPSL.Mode == store.ProtectionModeAI
	if !ladderAI && !drawdownAI && !fullAI {
		return false
	}
	if decisionPlan == nil || materialized == nil {
		return true
	}
	mode := strings.ToLower(strings.TrimSpace(decisionPlan.Mode))
	if ladderAI && drawdownAI {
		return mode != "combined" || len(materialized.StopLossOrders) == 0 || len(materialized.DrawdownRules) < 2
	}
	if ladderAI {
		return mode != "ladder" || len(materialized.StopLossOrders) == 0
	}
	if drawdownAI {
		return mode != "drawdown" || len(materialized.DrawdownRules) < 2
	}
	if fullAI {
		return mode != "full" || (!materialized.NeedsStopLoss && !materialized.NeedsTakeProfit)
	}
	return false
}

// usesManualProtection reports whether the strategy's protection is configured in
// MANUAL mode (ladder/full manual own the legs, DD not in AI mode). In this regime
// the operator has fixed the protection scheme, and any AI protection_plan must be
// ignored — both at open time (applyPostOpenProtection) and on every reconcile
// (protection_reconciler). Fixes 2026-06-06: AI plan was overriding manual A1 config
// via the reconciler's preferDecisionProtectionPlan call every 20s.
func (at *AutoTrader) usesManualProtection() bool {
	if at.config.StrategyConfig == nil {
		return false
	}
	prot := at.config.StrategyConfig.Protection
	ladderManual := prot.LadderTPSL.Enabled && prot.LadderTPSL.Mode == store.ProtectionModeManual
	fullManual := prot.FullTPSL.Enabled && prot.FullTPSL.Mode == store.ProtectionModeManual
	ddAI := prot.DrawdownTakeProfit.Enabled && prot.DrawdownTakeProfit.Mode == store.ProtectionModeAI
	return (ladderManual || fullManual) && !ddAI
}

func preferDecisionProtectionPlan(configuredPlan, decisionPlan *ProtectionPlan) *ProtectionPlan {
	if decisionPlan == nil {
		return configuredPlan
	}
	if configuredPlan == nil {
		return decisionPlan
	}

	preferred := *decisionPlan
	preferred.StopLossOrders = append([]ProtectionOrder(nil), decisionPlan.StopLossOrders...)
	preferred.TakeProfitOrders = append([]ProtectionOrder(nil), decisionPlan.TakeProfitOrders...)
	preferred.DrawdownRules = append([]store.DrawdownTakeProfitRule(nil), decisionPlan.DrawdownRules...)
	if decisionPlan.BreakEvenConfig != nil {
		cfg := *decisionPlan.BreakEvenConfig
		preferred.BreakEvenConfig = &cfg
	}
	if decisionPlan.DrawdownRunnerState != nil {
		state := *decisionPlan.DrawdownRunnerState
		preferred.DrawdownRunnerState = &state
	}

	if preferred.FallbackMaxLossPrice == 0 && configuredPlan.FallbackMaxLossPrice > 0 {
		preferred.FallbackMaxLossPrice = configuredPlan.FallbackMaxLossPrice
	}
	if preferred.BreakEvenConfig == nil && configuredPlan.BreakEvenConfig != nil {
		cfg := *configuredPlan.BreakEvenConfig
		preferred.BreakEvenConfig = &cfg
	}
	preferred.RequiresNativeOrders = preferred.RequiresNativeOrders || configuredPlan.RequiresNativeOrders
	preferred.RequiresPartialClose = preferred.RequiresPartialClose || configuredPlan.RequiresPartialClose
	if preferred.Mode == "" {
		preferred.Mode = configuredPlan.Mode
	}
	return &preferred
}

func (at *AutoTrader) applyNativeProtectionTargetsAfterOpen(req *protectionExecutionRequest, plan *ProtectionPlan) error {
	if req == nil || req.Decision == nil || at.config.StrategyConfig == nil {
		return nil
	}

	prot := at.config.StrategyConfig.Protection
	drawdownRules := prot.DrawdownTakeProfit.Rules
	if plan != nil && len(plan.DrawdownRules) > 0 {
		drawdownRules = plan.DrawdownRules
		at.protectionStateMutex.Lock()
		at.drawdownSource[positionKey(req.Symbol, strings.ToLower(req.PositionSide))] = "ai_decision"
		at.protectionStateMutex.Unlock()
	} else if prot.DrawdownTakeProfit.Enabled && prot.DrawdownTakeProfit.Mode == store.ProtectionModeAI {
		// AI mode but AI did not provide drawdown_rules — use strategy defaults as an explicit
		// safety fallback only. The prompt/validator should make this rare, but we still
		// preserve otherwise-good entries instead of leaving positions unprotected.
		if len(prot.DrawdownTakeProfit.Rules) > 0 {
			logger.Warnf("[%s] drawdown AI mode but AI did not provide drawdown_rules; falling back to strategy default rules (%d rules)",
				req.Symbol, len(prot.DrawdownTakeProfit.Rules))
			drawdownRules = prot.DrawdownTakeProfit.Rules
			at.protectionStateMutex.Lock()
			at.drawdownSource[positionKey(req.Symbol, strings.ToLower(req.PositionSide))] = "strategy_fallback"
			at.protectionStateMutex.Unlock()
		} else {
			return fmt.Errorf("drawdown protection is in AI mode but decision did not provide drawdown_rules and no strategy default rules configured")
		}
	} else if !prot.DrawdownTakeProfit.Enabled || prot.DrawdownTakeProfit.Mode == store.ProtectionModeDisabled {
		drawdownRules = nil
	}

	side := strings.ToLower(req.PositionSide)

	// 0. Immediate trailing: 50% partial trailing at entry for immediate drawdown protection.
	// Acts as first line of defense against bad entries or fast reversals. Canceled when
	// the formal drawdown tier trailing arms successfully.
	if plan != nil && len(plan.StopLossOrders) > 0 && len(drawdownRules) > 0 {
		if immediateCallback, ok := calculateImmediateTrailingCallback(req.EntryPrice, side, plan.StopLossOrders); ok {
			at.placeImmediateTrailing(req.Symbol, side, req.EntryPrice, immediateCallback)
		}
	}

	// 1. Native drawdown/trailing should be armed as early as safely possible.
	// Apply only exchange-native trailing drawdown here. Managed drawdown fallback uses
	// conditional TP-style orders and must not be staged immediately after opening,
	// because OKX conditional algo orders can cancel/replace same-symbol protection
	// and wipe the mandatory ladder SL stack. If native trailing is unavailable or
	// below safety floor, runtime drawdown monitoring will handle the managed close
	// path when drawdown is actually triggered.
	for _, rule := range drawdownRules {
		if rule.MinProfitPct <= 0 || rule.MaxDrawdownPct <= 0 || rule.CloseRatioPct <= 0 {
			continue
		}
		_ = at.applyNativeTrailingDrawdown(req.Symbol, side, req.EntryPrice, 0, rule)
	}

	// 2. Break-even: runtime polling will detect when profit reaches trigger level
	// and place a stop-loss at the break-even price (entry + offset).
	// No pre-placed TP orders — stops are placed only after profit threshold is met.

	if be := at.getActiveBreakEvenConfigForPlan(plan); be != nil && be.TriggerValue > 0 {
		if plan != nil && plan.BreakEvenConfig != nil {
			at.breakEvenStateMutex.Lock()
			at.breakEvenSource[positionKey(req.Symbol, side)] = "ai_decision"
			at.breakEvenStateMutex.Unlock()
		}
		at.setBreakEvenState(req.Symbol, side, "armed")
	}

	return nil
}

func (at *AutoTrader) placeImmediateTrailing(symbol, side string, entryPrice, callbackRatio float64) {
	if !at.supportsNativeTrailingStop() {
		return
	}
	positionSide := strings.ToUpper(side)
	activationPrice := entryPrice
	if strings.ToLower(side) == "long" {
		activationPrice = entryPrice * 1.0001
	} else {
		activationPrice = entryPrice * 0.9999
	}

	positions, err := at.trader.GetPositions()
	if err != nil {
		logger.Warnf("⚠️ Immediate trailing: failed to get positions for %s %s: %v", symbol, side, err)
		return
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
		logger.Warnf("⚠️ Immediate trailing: no position found for %s %s", symbol, side)
		return
	}
	partialQty := quantity * 0.5

	tagged, ok := at.trader.(interface {
		SetTrailingStopLossTaggedWithID(symbol string, positionSide string, activationPrice float64, callbackRate float64, quantity float64, reasonTag string) (string, error)
	})
	if !ok {
		return
	}

	orderID, err := tagged.SetTrailingStopLossTaggedWithID(symbol, positionSide, activationPrice, callbackRatio, partialQty, "immediate_trailing")
	if err != nil {
		logger.Warnf("⚠️ Immediate trailing placement failed for %s %s: %v", symbol, side, err)
		return
	}
	at.setImmediateTrailingOrderID(symbol, side, orderID)
	logger.Infof("🛡 Immediate trailing armed: %s %s | activation=%.6f callback=%.6f qty=%.4f orderID=%s",
		symbol, side, activationPrice, callbackRatio, partialQty, orderID)
}

func (at *AutoTrader) cancelImmediateTrailing(symbol, side string) {
	orderID := at.getImmediateTrailingOrderID(symbol, side)
	if orderID == "" {
		return
	}
	canceler, ok := at.trader.(interface {
		CancelTrailingStopOrdersByIDs(symbol string, orderIDs []string) error
	})
	if !ok {
		at.clearImmediateTrailingOrderID(symbol, side)
		return
	}
	if err := canceler.CancelTrailingStopOrdersByIDs(symbol, []string{orderID}); err != nil {
		logger.Warnf("⚠️ Failed to cancel immediate trailing for %s %s (orderID=%s): %v", symbol, side, orderID, err)
	} else {
		logger.Infof("🛡 Immediate trailing canceled (replaced by tier trailing): %s %s orderID=%s", symbol, side, orderID)
	}
	at.clearImmediateTrailingOrderID(symbol, side)
}

func (at *AutoTrader) canApplyManagedPartialDrawdownPlan(plan *ProtectionPlan) bool {
	if plan == nil || !plan.RequiresPartialClose {
		return false
	}
	caps := at.GetProtectionCapabilities()
	return caps.NativePartialClose && caps.NativeTakeProfit
}

func (at *AutoTrader) placeAndVerifyProtectionPlanWithRetry(symbol, positionSide string, quantity float64, plan *ProtectionPlan) error {
	var lastErr error
	for attempt := 1; attempt <= protectionSetupMaxAttempts; attempt++ {
		if err := at.placeAndVerifyProtectionPlan(symbol, positionSide, quantity, plan); err != nil {
			lastErr = err
			logger.Warnf("  ⚠️ Protection plan attempt %d/%d failed for %s %s: %v", attempt, protectionSetupMaxAttempts, symbol, positionSide, err)
			if isNonRetryableProtectionReject(err) {
				logger.Warnf("  🛑 Protection plan failure is non-retryable without price/plan refresh for %s %s; skipping immediate retry", symbol, positionSide)
				break
			}
			continue
		}
		if attempt > 1 {
			logger.Infof("  ✅ Protection plan recovered on retry %d/%d for %s %s", attempt, protectionSetupMaxAttempts, symbol, positionSide)
		}
		return nil
	}
	return fmt.Errorf("protection plan setup failed after %d attempts: %w", protectionSetupMaxAttempts, lastErr)
}

func (at *AutoTrader) placeAndVerifyProtectionWithRetry(symbol, positionSide string, quantity float64, needsStopLoss bool, stopLossPrice float64, needsTakeProfit bool, takeProfitPrice float64) error {
	var lastErr error
	for attempt := 1; attempt <= protectionSetupMaxAttempts; attempt++ {
		if err := at.placeAndVerifyProtection(symbol, positionSide, quantity, needsStopLoss, stopLossPrice, needsTakeProfit, takeProfitPrice); err != nil {
			lastErr = err
			logger.Warnf("  ⚠️ Protection setup attempt %d/%d failed for %s %s: %v", attempt, protectionSetupMaxAttempts, symbol, positionSide, err)
			if isNonRetryableProtectionReject(err) {
				logger.Warnf("  🛑 Protection setup failure is non-retryable without price/plan refresh for %s %s; skipping immediate retry", symbol, positionSide)
				break
			}
			continue
		}
		if attempt > 1 {
			logger.Infof("  ✅ Protection setup recovered on retry %d/%d for %s %s", attempt, protectionSetupMaxAttempts, symbol, positionSide)
		}
		return nil
	}
	return fmt.Errorf("protection setup failed after %d attempts: %w", protectionSetupMaxAttempts, lastErr)
}

func (at *AutoTrader) validateProtectionPlanExecution(symbol, positionSide string, quantity float64, plan *ProtectionPlan) (*ProtectionPlan, error) {
	if plan == nil {
		return nil, nil
	}

	adjusted := *plan
	if len(plan.StopLossOrders) > 0 {
		adjusted.StopLossOrders = append([]ProtectionOrder(nil), plan.StopLossOrders...)
	}
	if len(plan.TakeProfitOrders) > 0 {
		adjusted.TakeProfitOrders = append([]ProtectionOrder(nil), plan.TakeProfitOrders...)
	}

	markPrice, hasMarkPrice := at.getProtectionReferencePrice(symbol, strings.ToLower(positionSide))
	if hasMarkPrice {
		isExecutableStop := func(order ProtectionOrder) bool {
			if isExecutableHeldStopPrice(strings.ToLower(positionSide), order.Price, markPrice) {
				return true
			}
			logger.Warnf("  ⚠️ Protection ladder stop dropped as non-executable against mark: symbol=%s side=%s stop=%.6f mark=%.6f", symbol, positionSide, order.Price, markPrice)
			return false
		}
		isExecutableTP := func(order ProtectionOrder) bool {
			if isExecutableHeldTakeProfitPrice(strings.ToLower(positionSide), order.Price, markPrice) {
				return true
			}
			logger.Warnf("  ⚠️ Protection ladder take-profit dropped as non-executable against mark: symbol=%s side=%s tp=%.6f mark=%.6f", symbol, positionSide, order.Price, markPrice)
			return false
		}
		adjusted.StopLossOrders = filterProtectionOrders(adjusted.StopLossOrders, isExecutableStop)
		adjusted.TakeProfitOrders = filterProtectionOrders(adjusted.TakeProfitOrders, isExecutableTP)
		if len(plan.StopLossOrders) > 0 && len(adjusted.StopLossOrders) == 0 {
			adjusted.StopLossPrice = 0
		}
		if len(plan.TakeProfitOrders) > 0 && len(adjusted.TakeProfitOrders) == 0 {
			adjusted.TakeProfitPrice = 0
		}
		if adjusted.StopLossPrice > 0 && !isExecutableHeldStopPrice(strings.ToLower(positionSide), adjusted.StopLossPrice, markPrice) {
			logger.Warnf("  ⚠️ Protection full stop dropped as non-executable against mark: symbol=%s side=%s stop=%.6f mark=%.6f", symbol, positionSide, adjusted.StopLossPrice, markPrice)
			adjusted.StopLossPrice = 0
			adjusted.NeedsStopLoss = len(adjusted.StopLossOrders) > 0 || adjusted.FallbackMaxLossPrice > 0
		}
		if adjusted.TakeProfitPrice > 0 && !isExecutableHeldTakeProfitPrice(strings.ToLower(positionSide), adjusted.TakeProfitPrice, markPrice) {
			logger.Warnf("  ⚠️ Protection full take-profit dropped as non-executable against mark: symbol=%s side=%s tp=%.6f mark=%.6f", symbol, positionSide, adjusted.TakeProfitPrice, markPrice)
			adjusted.TakeProfitPrice = 0
			adjusted.NeedsTakeProfit = len(adjusted.TakeProfitOrders) > 0
		}
	}

	if okxTrader, ok := at.trader.(interface {
		ValidateProtectionQuantity(symbol string, quantity float64) error
	}); ok {
		filterExecutable := func(orders []ProtectionOrder) []ProtectionOrder {
			if len(orders) == 0 {
				return orders
			}
			filtered := make([]ProtectionOrder, 0, len(orders))
			for _, order := range orders {
				orderQty := quantity * order.CloseRatioPct / 100.0
				if orderQty <= 0 {
					continue
				}
				if err := okxTrader.ValidateProtectionQuantity(symbol, orderQty); err != nil {
					logger.Warnf("  ⚠️ Protection tier dropped as non-executable: symbol=%s side=%s price=%.6f qty=%.6f err=%v", symbol, positionSide, order.Price, orderQty, err)
					continue
				}
				filtered = append(filtered, order)
			}
			return filtered
		}

		adjusted.StopLossOrders = filterExecutable(adjusted.StopLossOrders)
		adjusted.TakeProfitOrders = filterExecutable(adjusted.TakeProfitOrders)

		if len(plan.StopLossOrders) > 0 && len(adjusted.StopLossOrders) == 0 && plan.NeedsStopLoss {
			// All ladder stop tiers fell below the exchange minimum (dust remainder).
			// Honor a pre-set full stop price if present; otherwise collapse to the
			// tightest tier so the remaining position keeps stop coverage instead of
			// losing it entirely (fix 2026-06-07).
			if adjusted.StopLossPrice > 0 {
				logger.Warnf("  ⚠️ Ladder stop-loss tiers all below exchange minimum; using full stop @%.6f for %s %s", adjusted.StopLossPrice, symbol, positionSide)
			} else if collapsePrice := tightestLadderStopPrice(plan.StopLossOrders, strings.ToLower(positionSide)); collapsePrice > 0 {
				// Only collapse to a stop that is still executable against current mark.
				// A stop already breached by price would be rejected by the exchange and
				// loop forever (fix 2026-06-09).
				if hasMarkPrice && !isExecutableHeldStopPrice(strings.ToLower(positionSide), collapsePrice, markPrice) {
					logger.Warnf("  ⚠️ Ladder stop collapse price %.6f already breached vs mark %.6f for %s %s; leaving stop to fallback/existing protection", collapsePrice, markPrice, symbol, positionSide)
				} else {
					logger.Warnf("  ⚠️ Ladder stop-loss tiers all below exchange minimum; collapsing to full stop @%.6f for %s %s", collapsePrice, symbol, positionSide)
					adjusted.StopLossPrice = collapsePrice
					adjusted.StopLossOrders = nil
				}
			}
		}
		if len(plan.TakeProfitOrders) > 0 && len(adjusted.TakeProfitOrders) == 0 && plan.NeedsTakeProfit {
			// All ladder TP tiers fell below the exchange minimum (dust remainder).
			// Honor a pre-set full TP price if present; otherwise collapse to the
			// nearest (first-to-fill) tier so the dust is still taken at the earliest
			// configured target instead of leaving the position with no TP and looping
			// forever (fix 2026-06-07).
			if adjusted.TakeProfitPrice > 0 {
				logger.Warnf("  ⚠️ Ladder take-profit tiers all below exchange minimum; using full TP @%.6f for %s %s", adjusted.TakeProfitPrice, symbol, positionSide)
			} else if collapsePrice := nearestLadderTakeProfitPrice(plan.TakeProfitOrders, strings.ToLower(positionSide)); collapsePrice > 0 {
				// Only collapse to a TP that is still executable against current mark.
				// When price has already moved past the TP target (e.g. long TP below
				// mark), the exchange rejects it (OKX 51279) and the reconciler loops
				// forever re-placing a doomed order. In that case leave TP empty and let
				// the stop / break-even own the exit (fix 2026-06-09).
				if hasMarkPrice && !isExecutableHeldTakeProfitPrice(strings.ToLower(positionSide), collapsePrice, markPrice) {
					logger.Warnf("  ⚠️ Ladder TP collapse price %.6f already passed vs mark %.6f for %s %s; not re-placing TP (price moved past target)", collapsePrice, markPrice, symbol, positionSide)
				} else {
					logger.Warnf("  ⚠️ Ladder take-profit tiers all below exchange minimum; collapsing to full TP @%.6f for %s %s", collapsePrice, symbol, positionSide)
					adjusted.TakeProfitPrice = collapsePrice
					adjusted.TakeProfitOrders = nil
				}
			}
		}
	}

	adjusted.NeedsStopLoss = plan.NeedsStopLoss && (adjusted.StopLossPrice > 0 || len(adjusted.StopLossOrders) > 0 || adjusted.FallbackMaxLossPrice > 0)
	adjusted.NeedsTakeProfit = plan.NeedsTakeProfit && (adjusted.TakeProfitPrice > 0 || len(adjusted.TakeProfitOrders) > 0)

	if len(adjusted.StopLossOrders) > 0 {
		adjusted.StopLossPrice = 0
	}
	if len(adjusted.TakeProfitOrders) > 0 {
		adjusted.TakeProfitPrice = 0
	}

	if !adjusted.NeedsStopLoss && !adjusted.NeedsTakeProfit && adjusted.FallbackMaxLossPrice <= 0 {
		return nil, nil
	}
	return &adjusted, nil
}

func (at *AutoTrader) getProtectionReferencePrice(symbol, side string) (float64, bool) {
	if at == nil || at.trader == nil {
		return 0, false
	}
	if price, ok := at.getPositionMarkPrice(symbol, side); ok && price > 0 {
		return price, true
	}
	price, err := at.trader.GetMarketPrice(symbol)
	if err != nil || price <= 0 {
		return 0, false
	}
	return price, true
}

func filterProtectionOrders(orders []ProtectionOrder, keep func(ProtectionOrder) bool) []ProtectionOrder {
	if len(orders) == 0 || keep == nil {
		return orders
	}
	filtered := make([]ProtectionOrder, 0, len(orders))
	for _, order := range orders {
		if keep(order) {
			filtered = append(filtered, order)
		}
	}
	return filtered
}

func isNonRetryableProtectionReject(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "code=51280") ||
		strings.Contains(msg, "code=51279") ||
		strings.Contains(msg, "cannot be lower than the last price") ||
		strings.Contains(msg, "cannot be higher than the last price") ||
		strings.Contains(msg, "SL trigger price must be less than the last price") ||
		strings.Contains(msg, "SL trigger price must be greater than the last price") ||
		strings.Contains(msg, "TP trigger price must be less than the last price") ||
		strings.Contains(msg, "TP trigger price must be greater than the last price")
}

func (at *AutoTrader) placeAndVerifyProtectionPlan(symbol, positionSide string, quantity float64, plan *ProtectionPlan) error {
	if plan == nil {
		return nil
	}

	validatedPlan, err := at.validateProtectionPlanExecution(symbol, positionSide, quantity, plan)
	if err != nil {
		return err
	}
	plan = validatedPlan

	if plan == nil {
		return nil
	}

	hasLadderSL := len(plan.StopLossOrders) > 0
	hasLadderTP := len(plan.TakeProfitOrders) > 0

	logger.Infof("  🧾 Protection plan materialized: symbol=%s side=%s mode=%s ladderSL=%d ladderTP=%d fullSL=%v@%.6f fullTP=%v@%.6f fallback=%.6f",
		symbol, positionSide, plan.Mode, len(plan.StopLossOrders), len(plan.TakeProfitOrders), plan.NeedsStopLoss, plan.StopLossPrice, plan.NeedsTakeProfit, plan.TakeProfitPrice, plan.FallbackMaxLossPrice)

	// Apply ladder legs first when present.
	if hasLadderSL || hasLadderTP {
		if err := at.placeAndVerifyLadderProtection(symbol, positionSide, quantity, plan); err != nil {
			return err
		}
	}

	// Full-position TP/SL should still be applied for directions NOT already covered by ladder orders.
	fullStop := plan.NeedsStopLoss && plan.StopLossPrice > 0 && !hasLadderSL
	fullTP := plan.NeedsTakeProfit && plan.TakeProfitPrice > 0 && !hasLadderTP
	if fullStop || fullTP {
		if err := at.placeAndVerifyProtection(symbol, positionSide, quantity, fullStop, plan.StopLossPrice, fullTP, plan.TakeProfitPrice); err != nil {
			return err
		}
	}

	if plan.FallbackMaxLossPrice > 0 {
		fallbackNeeded := !fullStop || plan.StopLossPrice == 0 || !approximatelyEqualPrice(plan.StopLossPrice, plan.FallbackMaxLossPrice)
		logger.Infof("  🧾 Fallback evaluation: symbol=%s side=%s fallback=%.6f fullStop=%v stopPrice=%.6f needed=%v",
			symbol, positionSide, plan.FallbackMaxLossPrice, fullStop, plan.StopLossPrice, fallbackNeeded)
		if fallbackNeeded {
			if err := at.placeFallbackMaxLossProtection(symbol, positionSide, quantity, plan.FallbackMaxLossPrice); err != nil {
				return err
			}
		}
	}

	if plan.FallbackMaxLossPrice > 0 {
		openOrders, err := at.trader.GetOpenOrders(symbol)
		if err != nil {
			return fmt.Errorf("failed to verify fallback max-loss stop loss: %w", err)
		}
		if !hasMatchingProtectionOrder(openOrders, positionSide, false, plan.FallbackMaxLossPrice) {
			return fmt.Errorf("fallback max-loss stop verification failed for %s %s at %.6f", symbol, positionSide, plan.FallbackMaxLossPrice)
		}
		logger.Infof("  ✅ Fallback max-loss stop verified: symbol=%s side=%s stop=%.6f", symbol, positionSide, plan.FallbackMaxLossPrice)
	}

	return nil
}

func (at *AutoTrader) placeFallbackMaxLossProtection(symbol, positionSide string, quantity float64, stopLossPrice float64) error {
	if stopLossPrice <= 0 {
		return nil
	}
	logger.Infof("  🛡 Placing fallback max-loss stop: symbol=%s side=%s qty=%.6f stop=%.6f", symbol, positionSide, quantity, stopLossPrice)
	if setter, ok := at.trader.(interface {
		SetStopLoss(symbol string, positionSide string, quantity, stopPrice float64) error
		SetStopLossTagged(symbol string, positionSide string, quantity, stopPrice float64, reasonTag string) error
	}); ok {
		if err := setter.SetStopLossTagged(symbol, positionSide, quantity, stopLossPrice, "fallback_maxloss_sl"); err != nil {
			return fmt.Errorf("failed to set fallback max-loss stop loss: %w", err)
		}
		at.recordProtectionIntent(symbol, positionSide, "fallback_maxloss_sl", quantity, stopLossPrice)
		return nil
	}
	if err := at.trader.SetStopLoss(symbol, positionSide, quantity, stopLossPrice); err != nil {
		return fmt.Errorf("failed to set fallback max-loss stop loss: %w", err)
	}
	return nil
}

func (at *AutoTrader) placeAndVerifyLadderProtection(symbol, positionSide string, quantity float64, plan *ProtectionPlan) error {
	if plan == nil {
		return nil
	}

	existingOrders, err := at.trader.GetOpenOrders(symbol)
	if err != nil {
		return fmt.Errorf("failed to inspect existing ladder protection orders: %w", err)
	}

	for _, order := range plan.StopLossOrders {
		orderQty := quantity * order.CloseRatioPct / 100.0
		if orderQty <= 0 {
			continue
		}
		if hasExistingEquivalentProtection(existingOrders, positionSide, false, order.Price, orderQty) {
			continue
		}
		if setter, ok := at.trader.(interface {
			SetStopLoss(symbol string, positionSide string, quantity, stopPrice float64) error
			SetStopLossTagged(symbol string, positionSide string, quantity, stopPrice float64, reasonTag string) error
		}); ok {
			if err := setter.SetStopLossTagged(symbol, positionSide, orderQty, order.Price, "ladder_sl"); err != nil {
				return fmt.Errorf("failed to set ladder stop loss %.6f (ratio %.2f%%): %w", order.Price, order.CloseRatioPct, err)
			}
			at.recordProtectionIntent(symbol, positionSide, "ladder_sl", orderQty, order.Price)
		} else if err := at.trader.SetStopLoss(symbol, positionSide, orderQty, order.Price); err != nil {
			return fmt.Errorf("failed to set ladder stop loss %.6f (ratio %.2f%%): %w", order.Price, order.CloseRatioPct, err)
		}
	}
	for _, order := range plan.TakeProfitOrders {
		orderQty := quantity * order.CloseRatioPct / 100.0
		if orderQty <= 0 {
			continue
		}
		if hasExistingEquivalentProtection(existingOrders, positionSide, true, order.Price, orderQty) {
			continue
		}
		if setter, ok := at.trader.(interface {
			SetTakeProfit(symbol string, positionSide string, quantity, takeProfitPrice float64) error
			SetTakeProfitTagged(symbol string, positionSide string, quantity, takeProfitPrice float64, reasonTag string) error
		}); ok {
			if err := setter.SetTakeProfitTagged(symbol, positionSide, orderQty, order.Price, "ladder_tp"); err != nil {
				return fmt.Errorf("failed to set ladder take profit %.6f (ratio %.2f%%): %w", order.Price, order.CloseRatioPct, err)
			}
			at.recordProtectionIntent(symbol, positionSide, "ladder_tp", orderQty, order.Price)
		} else if err := at.trader.SetTakeProfit(symbol, positionSide, orderQty, order.Price); err != nil {
			return fmt.Errorf("failed to set ladder take profit %.6f (ratio %.2f%%): %w", order.Price, order.CloseRatioPct, err)
		}
	}

	// Retry verification with delay to handle exchange propagation latency.
	for attempt := 1; attempt <= protectionVerifyMaxAttempts; attempt++ {
		at.sleepForVerification(protectionVerifyDelay)

		openOrders, err := at.trader.GetOpenOrders(symbol)
		if err != nil {
			return fmt.Errorf("failed to verify ladder protection orders: %w", err)
		}

		slErr := verifyProtectionOrders(openOrders, positionSide, plan.StopLossOrders, false)
		tpErr := verifyProtectionOrders(openOrders, positionSide, plan.TakeProfitOrders, true)
		if slErr == nil && tpErr == nil {
			logger.Infof("  ✅ Ladder protection orders verified: symbol=%s side=%s ladderSL=%d ladderTP=%d (attempt %d/%d)",
				symbol, positionSide, len(plan.StopLossOrders), len(plan.TakeProfitOrders), attempt, protectionVerifyMaxAttempts)
			return nil
		}

		if attempt < protectionVerifyMaxAttempts {
			logger.Infof("  ⏳ Ladder verification pending (attempt %d/%d), retrying...", attempt, protectionVerifyMaxAttempts)
		} else {
			if slErr != nil {
				return slErr
			}
			return tpErr
		}
	}

	return nil
}

func verifyProtectionOrders(orders []tradertypes.OpenOrder, positionSide string, targets []ProtectionOrder, wantTakeProfit bool) error {
	for _, target := range targets {
		if !hasMatchingProtectionOrder(orders, positionSide, wantTakeProfit, target.Price) {
			kind := "stop loss"
			if wantTakeProfit {
				kind = "take profit"
			}
			// Debug: log what orders we actually see so we can diagnose verification mismatches
			var candidates []string
			for _, o := range orders {
				if positionSide != "" && !strings.EqualFold(o.PositionSide, positionSide) && o.PositionSide != "" {
					continue
				}
				price := o.StopPrice
				if price <= 0 {
					price = o.Price
				}
				matches := ""
				if wantTakeProfit && looksLikeTakeProfit(o) {
					matches = "match-type"
				} else if !wantTakeProfit && looksLikeStopLoss(o) {
					matches = "match-type"
				}
				candidates = append(candidates, fmt.Sprintf("%s|side=%s|price=%.6f|qty=%.4f|%s", o.Type, o.PositionSide, price, o.Quantity, matches))
			}
			logger.Infof("  🔍 %s verify failed: target=%.6f side=%s | candidates=%v", kind, target.Price, positionSide, candidates)
			return fmt.Errorf("%s ladder verification failed for %s at %.6f", kind, positionSide, target.Price)
		}
	}
	return nil
}

func hasExistingEquivalentProtection(orders []tradertypes.OpenOrder, positionSide string, wantTakeProfit bool, targetPrice, targetQty float64) bool {
	for _, order := range orders {
		if hasEquivalentProtectionOrder(order, positionSide, wantTakeProfit, targetPrice, targetQty) {
			return true
		}
	}
	return false
}

func (at *AutoTrader) placeAndVerifyProtection(symbol, positionSide string, quantity float64, needsStopLoss bool, stopLossPrice float64, needsTakeProfit bool, takeProfitPrice float64) error {
	if needsStopLoss {
		if setter, ok := at.trader.(interface {
			SetStopLoss(symbol string, positionSide string, quantity, stopPrice float64) error
			SetStopLossTagged(symbol string, positionSide string, quantity, stopPrice float64, reasonTag string) error
		}); ok {
			if err := setter.SetStopLossTagged(symbol, positionSide, quantity, stopLossPrice, "full_sl"); err != nil {
				return fmt.Errorf("failed to set stop loss: %w", err)
			}
			at.recordProtectionIntent(symbol, positionSide, "full_sl", quantity, stopLossPrice)
		} else if err := at.trader.SetStopLoss(symbol, positionSide, quantity, stopLossPrice); err != nil {
			return fmt.Errorf("failed to set stop loss: %w", err)
		}
	}
	if needsTakeProfit {
		if setter, ok := at.trader.(interface {
			SetTakeProfit(symbol string, positionSide string, quantity, takeProfitPrice float64) error
			SetTakeProfitTagged(symbol string, positionSide string, quantity, takeProfitPrice float64, reasonTag string) error
		}); ok {
			if err := setter.SetTakeProfitTagged(symbol, positionSide, quantity, takeProfitPrice, "full_tp"); err != nil {
				return fmt.Errorf("failed to set take profit: %w", err)
			}
			at.recordProtectionIntent(symbol, positionSide, "full_tp", quantity, takeProfitPrice)
		} else if err := at.trader.SetTakeProfit(symbol, positionSide, quantity, takeProfitPrice); err != nil {
			return fmt.Errorf("failed to set take profit: %w", err)
		}
	}

	if !needsStopLoss && !needsTakeProfit {
		return nil
	}

	// Retry verification with delay to handle exchange propagation latency.
	for attempt := 1; attempt <= protectionVerifyMaxAttempts; attempt++ {
		at.sleepForVerification(protectionVerifyDelay)

		openOrders, err := at.trader.GetOpenOrders(symbol)
		if err != nil {
			return fmt.Errorf("failed to verify protection orders: %w", err)
		}

		slOK := !needsStopLoss || hasMatchingProtectionOrder(openOrders, positionSide, false, stopLossPrice)
		tpOK := !needsTakeProfit || hasMatchingProtectionOrder(openOrders, positionSide, true, takeProfitPrice)
		if slOK && tpOK {
			logger.Infof("  ✅ Protection orders verified: symbol=%s side=%s stop=%v tp=%v (attempt %d/%d)",
				symbol, positionSide, needsStopLoss, needsTakeProfit, attempt, protectionVerifyMaxAttempts)
			return nil
		}

		if attempt < protectionVerifyMaxAttempts {
			logger.Infof("  ⏳ Protection verification pending (attempt %d/%d), retrying...", attempt, protectionVerifyMaxAttempts)
		}
	}

	// Final check failed — return specific error.
	if needsStopLoss {
		return fmt.Errorf("stop loss verification failed for %s %s at %.6f after %d attempts", symbol, positionSide, stopLossPrice, protectionVerifyMaxAttempts)
	}
	return fmt.Errorf("take profit verification failed for %s %s at %.6f after %d attempts", symbol, positionSide, takeProfitPrice, protectionVerifyMaxAttempts)
}

// sleepForVerification waits before re-checking exchange orders. Extracted for test override.
func (at *AutoTrader) sleepForVerification(d time.Duration) {
	time.Sleep(d)
}

func hasMatchingProtectionOrder(orders []tradertypes.OpenOrder, positionSide string, wantTakeProfit bool, targetPrice float64) bool {
	for _, order := range orders {
		if positionSide != "" && !strings.EqualFold(order.PositionSide, positionSide) && order.PositionSide != "" {
			continue
		}

		if wantTakeProfit {
			if !looksLikeTakeProfit(order) {
				continue
			}
		} else {
			if !looksLikeStopLoss(order) {
				continue
			}
		}

		price := order.StopPrice
		if price <= 0 {
			price = order.Price
		}
		if approximatelyEqualPrice(price, targetPrice) {
			return true
		}
	}
	return false
}

func hasMatchingBreakEvenOrder(orders []tradertypes.OpenOrder, positionSide string, targetPrice float64) bool {
	for _, order := range orders {
		if positionSide != "" && !strings.EqualFold(order.PositionSide, positionSide) && order.PositionSide != "" {
			continue
		}
		if !looksLikeStopLoss(order) {
			continue
		}
		price := order.StopPrice
		if price <= 0 {
			price = order.Price
		}
		if price <= 0 {
			continue
		}
		// Match by tag if available
		tag := strings.ToLower(order.ClientOrderID)
		if strings.Contains(tag, "break_even") || strings.Contains(tag, "be-stop") {
			if approximatelyEqualPrice(price, targetPrice) {
				return true
			}
			continue
		}
		// For OKX where tag is truncated: match any conditional stop at the target price
		// that is not a trailing stop and not identified as ladder/fallback
		if !strings.Contains(strings.ToUpper(order.Type), "TRAILING") {
			if !strings.Contains(tag, "ladder") && !strings.Contains(tag, "fallback") && !strings.Contains(tag, "full_sl") {
				if approximatelyEqualPrice(price, targetPrice) {
					return true
				}
			}
		}
	}
	return false
}

func hasAnyBreakEvenOrderOnExchange(orders []tradertypes.OpenOrder, positionSide string) bool {
	for _, order := range orders {
		if positionSide != "" && !strings.EqualFold(order.PositionSide, positionSide) && order.PositionSide != "" {
			continue
		}
		if !looksLikeStopLoss(order) {
			continue
		}
		// Check by tag first (works for exchanges with longer tag support)
		if strings.Contains(strings.ToLower(order.ClientOrderID), "break_even") || strings.Contains(strings.ToLower(order.ClientOrderID), "be-stop") {
			return true
		}
		// For OKX where tag is truncated: any conditional stop-loss that is NOT a
		// trailing stop and NOT identified as ladder/fallback is likely a BE order.
		// This is a heuristic — the reconciler uses it as a safety check, not for
		// precise matching (applyBreakEvenStops does exact price matching).
		if order.StopPrice > 0 && !strings.Contains(strings.ToUpper(order.Type), "TRAILING") {
			tag := strings.ToLower(order.ClientOrderID)
			if !strings.Contains(tag, "ladder") && !strings.Contains(tag, "fallback") && !strings.Contains(tag, "full_sl") && !strings.Contains(tag, "full_tp") {
				return true
			}
		}
	}
	return false
}

func countMatchingProtectionOrders(orders []tradertypes.OpenOrder, positionSide string, wantTakeProfit bool, targetPrice float64) int {
	count := 0
	for _, order := range orders {
		if positionSide != "" && !strings.EqualFold(order.PositionSide, positionSide) && order.PositionSide != "" {
			continue
		}
		if wantTakeProfit {
			if !looksLikeTakeProfit(order) {
				continue
			}
		} else {
			if !looksLikeStopLoss(order) {
				continue
			}
		}
		price := order.StopPrice
		if price <= 0 {
			price = order.Price
		}
		if approximatelyEqualPrice(price, targetPrice) {
			count++
		}
	}
	return count
}

func hasEquivalentProtectionOrder(order tradertypes.OpenOrder, positionSide string, wantTakeProfit bool, targetPrice, targetQty float64) bool {
	if positionSide != "" && !strings.EqualFold(order.PositionSide, positionSide) && order.PositionSide != "" {
		return false
	}
	if wantTakeProfit {
		if !looksLikeTakeProfit(order) {
			return false
		}
	} else {
		if !looksLikeStopLoss(order) {
			return false
		}
	}
	price := order.StopPrice
	if price <= 0 {
		price = order.Price
	}
	if !approximatelyEqualPrice(price, targetPrice) {
		return false
	}
	if targetQty > 0 && order.Quantity > 0 && math.Abs(order.Quantity-targetQty)/math.Max(order.Quantity, targetQty) > 0.05 {
		return false
	}
	return true
}

func looksLikeStopLoss(order tradertypes.OpenOrder) bool {
	kind := strings.ToUpper(order.Type)
	return (strings.Contains(kind, "STOP") || strings.Contains(kind, "SL")) && !strings.Contains(kind, "TAKE_PROFIT") && !strings.Contains(kind, "TP")
}

func looksLikeTakeProfit(order tradertypes.OpenOrder) bool {
	kind := strings.ToUpper(order.Type)
	return strings.Contains(kind, "TAKE_PROFIT") || strings.Contains(kind, "TP")
}

func approximatelyEqualPrice(a, b float64) bool {
	if a <= 0 || b <= 0 {
		return false
	}
	base := math.Max(math.Abs(a), math.Abs(b))
	if base == 0 {
		return false
	}
	return math.Abs(a-b)/base <= protectionPriceTolerancePct
}

// cancelOrphanedDrawdownOrders cancels only the TP orders created by the failed
// drawdown plan. It must not wipe ladder/full TP orders for the same symbol.
func (at *AutoTrader) cancelOrphanedDrawdownOrders(symbol string, plan *ProtectionPlan) {
	if plan == nil || len(plan.TakeProfitOrders) == 0 {
		return
	}

	targetPrices := make([]float64, 0, len(plan.TakeProfitOrders))
	for _, order := range plan.TakeProfitOrders {
		if order.Price > 0 {
			targetPrices = append(targetPrices, order.Price)
		}
	}

	if targetedCanceller, ok := at.trader.(interface {
		CancelTakeProfitOrdersByPrices(symbol string, prices []float64) error
	}); ok && len(targetPrices) > 0 {
		if err := targetedCanceller.CancelTakeProfitOrdersByPrices(symbol, targetPrices); err != nil {
			logger.Warnf("  ⚠️ Failed to cancel targeted orphaned drawdown TP orders for %s: %v", symbol, err)
		} else {
			logger.Infof("  🧹 Cancelled targeted orphaned drawdown TP orders for %s at prices=%v", symbol, targetPrices)
		}
		return
	}

	if canceller, ok := at.trader.(interface {
		CancelTakeProfitOrders(symbol string) error
	}); ok {
		if err := canceller.CancelTakeProfitOrders(symbol); err != nil {
			logger.Warnf("  ⚠️ Failed to cancel orphaned drawdown TP orders for %s: %v", symbol, err)
		} else {
			logger.Infof("  🧹 Cancelled orphaned drawdown TP orders for %s", symbol)
		}
	}
}

// extractStructuralTargets extracts resistance (for long) or support (for short) price levels
// from the AI decision's entry_protection_rationale. These are used as structural anchors
// for DD tier targets when the AI's min_profit_pct values are unreasonable.
func extractStructuralTargets(decision *kernel.Decision, entryPrice float64, side string) []float64 {
	if decision == nil || decision.EntryProtection == nil || entryPrice <= 0 {
		return nil
	}
	ep := decision.EntryProtection

	isLong := strings.EqualFold(side, "long")
	seen := make(map[float64]bool)
	var targets []float64

	addTarget := func(price float64) {
		if price <= 0 || seen[price] {
			return
		}
		if isLong && price > entryPrice {
			seen[price] = true
			targets = append(targets, price)
		} else if !isLong && price < entryPrice {
			seen[price] = true
			targets = append(targets, price)
		}
	}

	if isLong {
		for _, r := range ep.KeyLevels.Resistance {
			addTarget(r)
		}
	} else {
		for _, s := range ep.KeyLevels.Support {
			addTarget(s)
		}
	}

	for _, skl := range ep.StructuralKeyLevels {
		if isLong && strings.EqualFold(skl.Type, "resistance") {
			addTarget(skl.Price)
		} else if !isLong && strings.EqualFold(skl.Type, "support") {
			addTarget(skl.Price)
		}
	}

	for _, a := range ep.Anchors {
		if isLong && (strings.EqualFold(a.Type, "resistance") || strings.EqualFold(a.Type, "first_target") || strings.EqualFold(a.Type, "target")) {
			addTarget(a.Price)
		} else if !isLong && (strings.EqualFold(a.Type, "support") || strings.EqualFold(a.Type, "first_target") || strings.EqualFold(a.Type, "target")) {
			addTarget(a.Price)
		}
	}

	for _, a := range ep.HigherAnchors {
		if isLong && (strings.EqualFold(a.Type, "resistance") || strings.EqualFold(a.Type, "target")) {
			addTarget(a.Price)
		} else if !isLong && (strings.EqualFold(a.Type, "support") || strings.EqualFold(a.Type, "target")) {
			addTarget(a.Price)
		}
	}

	return targets
}
