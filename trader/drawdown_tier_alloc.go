package trader

import (
	"fmt"
	"math"
	"nofx/logger"
	"nofx/store"
	"sort"
	"strings"
)

// isFullCloseTierRule reports whether a rule closes the whole position. Such tiers are
// independent whole-position exits, not members of the partial ladder.
func isFullCloseTierRule(rule store.DrawdownTakeProfitRule) bool {
	return rule.CloseRatioPct >= 99.999
}

// isFullCloseTierAlloc is the alloc-side counterpart of isFullCloseTierRule.
func isFullCloseTierAlloc(tier store.DrawdownTierAllocation) bool {
	return tier.CloseRatioPct >= 99.999
}

// tierStageName returns the rule's stage name, falling back to a positional label.
func tierStageName(rule store.DrawdownTakeProfitRule, i int) string {
	if rule.StageName != "" {
		return rule.StageName
	}
	return fmt.Sprintf("T%d", i+1)
}

// computeDrawdownTierAllocations computes the fixed position allocations for each drawdown tier
// at position open time. Each tier gets a fixed quantity that never changes.
// The allocation is based on the opening quantity and each tier's close_ratio_pct, which refers
// to the percentage of the ORIGINAL opening position, not the remaining position after prior tiers.
func computeDrawdownTierAllocations(totalQuantity float64, rules []store.DrawdownTakeProfitRule) []store.DrawdownTierAllocation {
	if len(rules) == 0 || totalQuantity <= 0 {
		return nil
	}

	sort.Slice(rules, func(i, j int) bool {
		return rules[i].MinProfitPct < rules[j].MinProfitPct
	})

	allocs := make([]store.DrawdownTierAllocation, 0, len(rules))
	allocatedPct := 0.0

	for i, rule := range rules {
		if rule.MinProfitPct <= 0 || rule.MaxDrawdownPct <= 0 || rule.CloseRatioPct <= 0 {
			continue
		}

		// Full-close tiers (dd1 / outer_exit) are whole-position safety nets, NOT slices of
		// a partial ladder. Under place-at-open they rest on the exchange CONCURRENTLY with
		// the partial tiers, each with its own callback, so they must neither consume the
		// partial budget nor be clamped by it.
		//
		// Live 2026-07-27 (bug class instance #9): claude-ct30 has dd1 (3 ATR peak, close 100)
		// and a partial (4 ATR peak, close 30). Sorted by MinProfitPct, dd1 comes FIRST, filled
		// allocatedPct to 100, and the partial got ratioPct = 100-100 = 0 → `continue` → dropped
		// from the alloc table entirely. Consequences: getCumulativeCloseRatioByRule found no
		// tier (only safe because v1.16.11 stopped inheriting), updateDrawdownTierStates never
		// tracked its high-water mark, the managed fallback path could never execute it, and the
		// dashboard showed one tier for a two-tier strategy.
		if isFullCloseTierRule(rule) {
			allocs = append(allocs, store.DrawdownTierAllocation{
				TierIndex:      i,
				StageName:      tierStageName(rule, i),
				Quantity:       totalQuantity,
				CloseRatioPct:  100,
				MinProfitPct:   rule.MinProfitPct,
				MaxDrawdownPct: rule.MaxDrawdownPct,
				PeakPnLPct:     0,
				Status:         "pending",
			})
			continue
		}

		ratioPct := rule.CloseRatioPct
		if allocatedPct+ratioPct > 100 {
			ratioPct = 100 - allocatedPct
		}
		if ratioPct <= 0 {
			continue
		}

		qty := totalQuantity * ratioPct / 100.0

		allocs = append(allocs, store.DrawdownTierAllocation{
			TierIndex:      i,
			StageName:      tierStageName(rule, i),
			Quantity:       qty,
			CloseRatioPct:  ratioPct,
			MinProfitPct:   rule.MinProfitPct,
			MaxDrawdownPct: rule.MaxDrawdownPct,
			PeakPnLPct:     0,
			Status:         "pending",
		})

		allocatedPct += ratioPct
	}

	return allocs
}

// entryQuantityAnchor 返回该仓位的原始开仓量;取不到时退回 callerQty。
//
// 为什么必须是"原始开仓量"而不是"当前剩余量":档位比例(CloseRatioPct)的分母
// 在仓位整个生命周期里必须固定,否则每次部分止盈之后同一档算出的数量都会变小,
// 而已经挂在交易所上的单不会跟着变 —— 匹配不上就重挂,重挂就是重复单。
// 这与 v1.16.16 把 ATR→百分比 的除数锚定到冻结开仓价是同一条原则:
// 身份/口径里的分母不能是会随时间变的量。
func (at *AutoTrader) entryQuantityAnchor(symbol, side string, callerQty float64) float64 {
	if at.store == nil {
		return callerQty
	}
	dbPos, err := at.store.Position().GetOpenPositionBySymbol(at.id, symbol, strings.ToUpper(side))
	if err != nil || dbPos == nil || dbPos.EntryQuantity <= 0 {
		return callerQty
	}
	return dbPos.EntryQuantity
}

// setDrawdownTierAllocs stores the fixed tier allocations for a position.
func (at *AutoTrader) setDrawdownTierAllocs(symbol, side string, allocs []store.DrawdownTierAllocation) {
	key := positionKey(symbol, side)
	at.drawdownTierAllocMu.Lock()
	defer at.drawdownTierAllocMu.Unlock()
	if at.drawdownTierAllocs == nil {
		at.drawdownTierAllocs = make(map[string][]store.DrawdownTierAllocation)
	}
	at.drawdownTierAllocs[key] = allocs
	logger.Infof("📊 Drawdown tier allocations set for %s %s: %d tiers", symbol, side, len(allocs))
	for _, a := range allocs {
		logger.Infof("  → %s: qty=%.6f (%.1f%%) | peak_trigger=%.2f%% | drawdown=%.2f%%",
			a.StageName, a.Quantity, a.CloseRatioPct, a.MinProfitPct, a.MaxDrawdownPct)
	}
}

// getDrawdownTierAllocs returns the fixed tier allocations for a position.
func (at *AutoTrader) getDrawdownTierAllocs(symbol, side string) []store.DrawdownTierAllocation {
	key := positionKey(symbol, side)
	at.drawdownTierAllocMu.RLock()
	defer at.drawdownTierAllocMu.RUnlock()
	allocs := at.drawdownTierAllocs[key]
	if len(allocs) == 0 {
		return nil
	}
	out := make([]store.DrawdownTierAllocation, len(allocs))
	copy(out, allocs)
	return out
}

// clearDrawdownTierAllocs removes tier allocations when position is fully closed.
func (at *AutoTrader) clearDrawdownTierAllocs(symbol, side string) {
	key := positionKey(symbol, side)
	at.drawdownTierAllocMu.Lock()
	defer at.drawdownTierAllocMu.Unlock()
	delete(at.drawdownTierAllocs, key)
}

// updateTierAlloc updates a single tier allocation's state.
func (at *AutoTrader) updateTierAlloc(symbol, side string, tierIndex int, update func(*store.DrawdownTierAllocation)) {
	key := positionKey(symbol, side)
	at.drawdownTierAllocMu.Lock()
	defer at.drawdownTierAllocMu.Unlock()
	allocs := at.drawdownTierAllocs[key]
	for i := range allocs {
		if allocs[i].TierIndex == tierIndex {
			update(&allocs[i])
			return
		}
	}
}

// evaluateDrawdownTiers checks all pending/tracking tiers against current P&L.
// Returns the tier that should be executed now, or nil if none.
// Single-direction upgrade: when a higher tier activates, all lower tiers are superseded.
// Each tier's CloseRatioPct is relative to the original entry quantity.
// globalPeakPnL is used to initialize a tier's peak when it first enters tracking,
// ensuring continuity for positions where peak was already reached before tier initialization.
func (at *AutoTrader) evaluateDrawdownTiers(symbol, side string, currentPnLPct, globalPeakPnL float64) *store.DrawdownTierAllocation {
	key := positionKey(symbol, side)
	at.drawdownTierAllocMu.Lock()
	defer at.drawdownTierAllocMu.Unlock()

	allocs := at.drawdownTierAllocs[key]
	if len(allocs) == 0 {
		return nil
	}

	var triggered *store.DrawdownTierAllocation

	for i := range allocs {
		tier := &allocs[i]

		if tier.Status == "executed" || tier.Status == "be_covered" || tier.Status == "superseded" {
			continue
		}

		// Tier becomes "tracking" once profit has EVER reached the min_profit threshold.
		//
		// The gate is max(current, peak), not current alone. A drawdown tier is a
		// trailing take-profit: the exchange-native equivalent latches the moment mark
		// crosses activePx and stays armed while price retraces — that retracement is
		// the entire point. Gating on the live PnL instead gave the managed monitor the
		// opposite semantics: it could only arm during the up-move, and a tier that was
		// never armed can neither hold a peak nor compute a retracement from one.
		//
		// The tier alloc table lives only in memory (see initDrawdownTiersForPosition),
		// so every restart re-creates tiers as pending/peak=0 while peakPnLCache is
		// restored from the excursion store. Under a current-only gate the two never
		// reconcile: peak says 8.11%, tier says 0.00%, and the tier stays pending
		// forever if profit has since retraced below the trigger.
		//
		// Live 2026-07-29, GPT SKHYNIXUSDT short: peak 8.11%, trigger 5.82%, retraced to
		// 4.82%. dd1 sat `pending peak=0.00%` through 520 consecutive "managed monitor is
		// co-running as second insurance" log lines while the retracement from peak
		// (2.61%) had already passed the 2.327% threshold. The co-run promise was real
		// but unreachable: the monitor never got past this gate. That is the mechanism
		// behind "陪跑形同虚设".
		if tier.Status == "pending" {
			armPnLPct := currentPnLPct
			if globalPeakPnL > armPnLPct {
				armPnLPct = globalPeakPnL
			}
			if armPnLPct >= tier.MinProfitPct {
				tier.Status = "tracking"
				// Initialize peak from global peak to preserve history for late-initialized tiers
				tier.PeakPnLPct = globalPeakPnL
				if currentPnLPct > tier.PeakPnLPct {
					tier.PeakPnLPct = currentPnLPct
				}
				logger.Infof("📈 Drawdown %s now tracking: %s %s | armPnl=%.2f%% (cur=%.2f%% peak=%.2f%%) >= trigger=%.2f%% | peak=%.2f%%",
					tier.StageName, symbol, side, armPnLPct, currentPnLPct, globalPeakPnL, tier.MinProfitPct, tier.PeakPnLPct)
				// Single-direction upgrade: supersede all lower tiers. A full-close tier is
				// never superseded by a partial — it is a concurrent whole-position safety
				// net (it keeps its own exchange order), so cancelling it in-memory would
				// leave the runner unprotected in the managed fallback path.
				for j := 0; j < i; j++ {
					if isFullCloseTierAlloc(allocs[j]) && !isFullCloseTierAlloc(*tier) {
						continue
					}
					if allocs[j].Status == "tracking" || allocs[j].Status == "pending" {
						allocs[j].Status = "superseded"
						logger.Infof("⏭️ Drawdown %s superseded by %s: %s %s",
							allocs[j].StageName, tier.StageName, symbol, side)
					}
				}
				// Fall through to check drawdown immediately in the same cycle
			} else {
				continue
			}
		}

		// Status == "tracking": update peak and check drawdown
		if currentPnLPct > tier.PeakPnLPct {
			tier.PeakPnLPct = currentPnLPct
		}

		// Price retracement from THIS tier's peak (not global peak). Under the
		// unified trailing semantics MaxDrawdownPct is a PRICE retracement from
		// peak, matching the exchange-native callback ratio. PnL percentages are
		// price-move percentages from entry, so converting to a price fraction:
		//   priceRetracePct = (peakPnL - curPnL) / (100 + peakPnL) * 100
		drawdownFromPeak := 0.0
		if tier.PeakPnLPct > currentPnLPct && (100+tier.PeakPnLPct) > 0 {
			drawdownFromPeak = ((tier.PeakPnLPct - currentPnLPct) / (100 + tier.PeakPnLPct)) * 100
		}

		if drawdownFromPeak >= tier.MaxDrawdownPct {
			// This tier is triggered! Return it for execution.
			// We only trigger one tier per evaluation cycle (the lowest-index active one).
			tierCopy := *tier
			triggered = &tierCopy
			tier.Status = "executed"
			logger.Infof("🚨 Drawdown %s triggered: %s %s | peak=%.2f%% current=%.2f%% priceRetrace=%.2f%% >= threshold=%.2f%%",
				tier.StageName, symbol, side, tier.PeakPnLPct, currentPnLPct, drawdownFromPeak, tier.MaxDrawdownPct)
			break
		}
	}

	return triggered
}

// updateDrawdownTierStates updates tier states (pending→tracking, supersede lower tiers)
// based on current P&L without triggering market closes. Used when native trailing handles
// exchange orders but we still need tier state for high-water-mark tracking.
func (at *AutoTrader) updateDrawdownTierStates(symbol, side string, currentPnLPct, globalPeakPnL float64) {
	key := positionKey(symbol, side)
	at.drawdownTierAllocMu.Lock()
	defer at.drawdownTierAllocMu.Unlock()

	allocs := at.drawdownTierAllocs[key]
	if len(allocs) == 0 {
		return
	}

	for i := range allocs {
		tier := &allocs[i]
		if tier.Status == "executed" || tier.Status == "be_covered" || tier.Status == "superseded" {
			continue
		}
		if tier.Status == "pending" {
			// max(current, peak) — same latching semantics as evaluateDrawdownTiers.
			// These two functions must agree on when a tier is armed: this one runs on
			// the native path and evaluateDrawdownTiers on the managed path, over the
			// SAME alloc table. If only one latched on peak, the tier's state would
			// depend on which path happened to observe it first.
			armPnLPct := currentPnLPct
			if globalPeakPnL > armPnLPct {
				armPnLPct = globalPeakPnL
			}
			if armPnLPct >= tier.MinProfitPct {
				tier.Status = "tracking"
				tier.PeakPnLPct = globalPeakPnL
				if currentPnLPct > tier.PeakPnLPct {
					tier.PeakPnLPct = currentPnLPct
				}
				logger.Infof("📈 Drawdown %s now tracking (native): %s %s | armPnl=%.2f%% (cur=%.2f%% peak=%.2f%%) >= trigger=%.2f%%",
					tier.StageName, symbol, side, armPnLPct, currentPnLPct, globalPeakPnL, tier.MinProfitPct)
				for j := 0; j < i; j++ {
					// Same rule as evaluateDrawdownTiers: a partial never supersedes the
					// concurrent whole-position tier.
					if isFullCloseTierAlloc(allocs[j]) && !isFullCloseTierAlloc(*tier) {
						continue
					}
					if allocs[j].Status == "tracking" || allocs[j].Status == "pending" {
						allocs[j].Status = "superseded"
						logger.Infof("⏭️ Drawdown %s superseded by %s (native): %s %s",
							allocs[j].StageName, tier.StageName, symbol, side)
					}
				}
			}
		} else if tier.Status == "tracking" {
			if currentPnLPct > tier.PeakPnLPct {
				tier.PeakPnLPct = currentPnLPct
			}
		}
	}
}

// getExecutedTierCloseRatio returns the cumulative CloseRatioPct of all executed tiers for a position.
func (at *AutoTrader) getExecutedTierCloseRatio(symbol, side string) float64 {
	key := positionKey(symbol, side)
	at.drawdownTierAllocMu.Lock()
	defer at.drawdownTierAllocMu.Unlock()
	allocs := at.drawdownTierAllocs[key]
	var total float64
	for _, tier := range allocs {
		if tier.Status == "executed" {
			total += tier.CloseRatioPct
		}
	}
	return total
}

// getCumulativeCloseRatio returns the cumulative CloseRatioPct for a tier including
// all lower tiers. When a higher tier activates, it inherits the position responsibility
// of all lower tiers. E.g. T1=65%, T2=25% → when T2 triggers, close 65+25=90% of
// original position using T2's profit/drawdown parameters.
func (at *AutoTrader) getCumulativeCloseRatio(symbol, side string, tierIndex int) float64 {
	key := positionKey(symbol, side)
	at.drawdownTierAllocMu.Lock()
	defer at.drawdownTierAllocMu.Unlock()
	allocs := at.drawdownTierAllocs[key]
	var total float64
	for _, tier := range allocs {
		if tier.TierIndex <= tierIndex {
			total += tier.CloseRatioPct
		}
	}
	if total > 100 {
		total = 100
	}
	return total
}

// getCumulativeCloseRatioByRule finds the tier matching the given rule and returns
// the cumulative close ratio (this tier + all lower tiers). When a higher tier is
// armed on the exchange, it must protect all lower tiers' position as well.
// E.g. T1=65%, T2=25%: when T2 triggers, close 65+25=90% of original position.
func (at *AutoTrader) getCumulativeCloseRatioByRule(symbol, side string, rule store.DrawdownTakeProfitRule) float64 {
	key := positionKey(symbol, side)
	at.drawdownTierAllocMu.Lock()
	defer at.drawdownTierAllocMu.Unlock()
	allocs := at.drawdownTierAllocs[key]
	if len(allocs) == 0 {
		return rule.CloseRatioPct
	}
	// Match on tier IDENTITY, not on numeric proximity. StageName and CloseRatioPct both
	// survive ATR→percent resolution unchanged, so they identify the same logical tier on
	// either side of the resolver; MinProfitPct does not.
	tierIndex := -1
	for _, tier := range allocs {
		if tier.StageName != "" && tier.StageName == rule.StageName &&
			math.Abs(tier.CloseRatioPct-rule.CloseRatioPct) < 0.01 {
			tierIndex = tier.TierIndex
			break
		}
	}
	if tierIndex < 0 {
		// Unnamed tiers (legacy records) fall back to an EXACT threshold match.
		for _, tier := range allocs {
			if math.Abs(tier.MinProfitPct-rule.MinProfitPct) < 0.01 {
				tierIndex = tier.TierIndex
				break
			}
		}
	}
	if tierIndex < 0 {
		// No identifiable tier. Return the rule's OWN ratio and nothing more.
		//
		// The previous fallback here took "the highest tier whose MinProfitPct <= the rule's"
		// and summed the ladder up to it. That is unsafe in exactly the case it fired: when
		// the rule and the allocs disagree on units or the tier was dropped from the ladder,
		// an unrelated tier gets adopted and a PARTIAL tier inherits a cumulative ratio it
		// was never meant to carry — clamped to 100, i.e. a whole-position trailing order.
		// Live 2026-07-27: WLDUSDT partial (close=30) armed for all 380 units, and CLUSDT
		// partial (close=30) for all 1.4. Inheriting nothing is the safe direction: the tier
		// protects its own slice, and the sibling full tier still covers the remainder.
		logger.Infof("⚠️ Drawdown cumulative ratio: %s %s no tier matches rule stage=%q close=%.1f%% min=%.4f — using the rule's own ratio (no ladder inheritance)",
			symbol, side, rule.StageName, rule.CloseRatioPct, rule.MinProfitPct)
		return rule.CloseRatioPct
	}
	// A full-close tier owns the whole position outright and inherits nothing.
	for _, tier := range allocs {
		if tier.TierIndex == tierIndex && isFullCloseTierAlloc(tier) {
			return 100
		}
	}
	// Partial tiers inherit only from LOWER PARTIAL tiers. A concurrent full-close tier is a
	// separate whole-position safety net; summing its 100 in here would clamp every partial to
	// 100 and re-create bug #8 (a close=30 tier arming the entire position) through the match
	// path instead of the fallback.
	var total float64
	for _, tier := range allocs {
		if tier.TierIndex <= tierIndex && !isFullCloseTierAlloc(tier) {
			total += tier.CloseRatioPct
		}
	}
	if total > 100 {
		total = 100
	}
	return total
}

// resolveDrawdownRulesWithModes merges AI-provided rules with strategy-configured rules
// based on per-field mode settings. For each rule, if a field's mode is "ai", the AI value
// is used; if "manual", the strategy config value is used.
func resolveDrawdownRulesWithModes(strategyRules, aiRules []store.DrawdownTakeProfitRule) []store.DrawdownTakeProfitRule {
	if len(strategyRules) == 0 {
		return aiRules
	}
	if len(aiRules) == 0 {
		return strategyRules
	}

	sort.Slice(strategyRules, func(i, j int) bool {
		return strategyRules[i].MinProfitPct < strategyRules[j].MinProfitPct
	})
	sort.Slice(aiRules, func(i, j int) bool {
		return aiRules[i].MinProfitPct < aiRules[j].MinProfitPct
	})

	n := len(strategyRules)
	if len(aiRules) > n {
		n = len(aiRules)
	}

	resolved := make([]store.DrawdownTakeProfitRule, 0, n)

	for i := 0; i < n; i++ {
		var base store.DrawdownTakeProfitRule
		if i < len(strategyRules) {
			base = strategyRules[i]
		}
		var ai store.DrawdownTakeProfitRule
		if i < len(aiRules) {
			ai = aiRules[i]
		}

		result := base

		if base.CloseRatioMode == store.ProtectionValueModeAI && i < len(aiRules) && ai.CloseRatioPct > 0 {
			result.CloseRatioPct = ai.CloseRatioPct
		}
		if base.MinProfitMode == store.ProtectionValueModeAI && i < len(aiRules) && ai.MinProfitPct > 0 {
			result.MinProfitPct = ai.MinProfitPct
		}
		if base.MaxDrawdownMode == store.ProtectionValueModeAI && i < len(aiRules) && ai.MaxDrawdownPct > 0 {
			result.MaxDrawdownPct = ai.MaxDrawdownPct
		}

		if i >= len(strategyRules) && i < len(aiRules) {
			result = ai
		}

		if result.MinProfitPct > 0 && result.MaxDrawdownPct > 0 && result.CloseRatioPct > 0 {
			// Infer SEMANTICALLY, not positionally. The arm path names the same tier via
			// normalizeDrawdownRule → inferDrawdownStageName ("partial_profit_lock",
			// "outer_exit"), and StageName is the tier identity that survives ATR→percent
			// resolution. Stamping "T2" here made the alloc table and the armed DB records
			// disagree on the name for one configured tier, so identity matching in
			// getCumulativeCloseRatioByRule / findRuleForTier could not line them up.
			if result.StageName == "" {
				result.StageName = inferDrawdownStageName(result)
			}
			resolved = append(resolved, result)
		}
	}

	// Ensure strictly increasing MinProfitPct across resolved tiers.
	for i := 1; i < len(resolved); i++ {
		if resolved[i].MinProfitPct <= resolved[i-1].MinProfitPct {
			resolved[i].MinProfitPct = resolved[i-1].MinProfitPct + 0.3
		}
	}

	return resolved
}

// hasAllTiersCompleted returns true if all tier allocations are executed or be_covered.
func hasAllTiersCompleted(allocs []store.DrawdownTierAllocation) bool {
	if len(allocs) == 0 {
		return false
	}
	for _, a := range allocs {
		if a.Status != "executed" && a.Status != "be_covered" {
			return false
		}
	}
	return true
}

// getPendingTierCount returns the number of tiers that haven't been executed yet.
func getPendingTierCount(allocs []store.DrawdownTierAllocation) int {
	count := 0
	for _, a := range allocs {
		if a.Status == "pending" || a.Status == "tracking" {
			count++
		}
	}
	return count
}

// logTierAllocStatus prints a summary of all tier allocations for debugging.
func logTierAllocStatus(symbol, side string, allocs []store.DrawdownTierAllocation) {
	if len(allocs) == 0 {
		return
	}
	var parts []string
	for _, a := range allocs {
		parts = append(parts, fmt.Sprintf("%s[%s peak=%.2f%%]", a.StageName, a.Status, a.PeakPnLPct))
	}
	logger.Infof("📊 Drawdown tiers %s %s: %s", symbol, side, strings.Join(parts, " | "))
}

// initDrawdownTiersFromResolvedRules resolves AI/manual per-field modes against strategy config,
// then computes and stores the fixed tier allocations.
//
// entryPrice is required so ATR-unit thresholds can be re-resolved to percent AFTER the
// mode merge. resolveDrawdownRulesWithModes starts each output from `result := base`, i.e.
// the RAW strategy rule, and only copies a caller value in when that field's mode is "ai".
// A fully-manual strategy (claude-ct30: min_profit_mode/max_drawdown_mode both "manual")
// therefore discards whatever resolution the caller had already applied, and the raw ATR
// multiple (3.0) lands in MinProfitPct — a field every consumer reads as a PERCENT.
// Observed 2026-07-27 on WLDUSDT long: allocs held "peak_trigger=3.00%" while the arm path
// used the resolved 4.2012%, so getCumulativeCloseRatioByRule matched no tier and its
// fallback summed the ladder to 100 — the 30% partial tier armed at the FULL 380 position.
func (at *AutoTrader) initDrawdownTiersFromResolvedRules(symbol, side string, quantity, entryPrice float64, aiRules []store.DrawdownTakeProfitRule) {
	if quantity <= 0 || len(aiRules) == 0 {
		return
	}

	var strategyRules []store.DrawdownTakeProfitRule
	if at.config.StrategyConfig != nil {
		strategyRules = at.config.StrategyConfig.Protection.DrawdownTakeProfit.Rules
	}

	resolved := resolveDrawdownRulesWithModes(strategyRules, aiRules)
	if len(resolved) == 0 {
		resolved = aiRules
	}

	at.initDrawdownTiersForPosition(symbol, side, quantity, entryPrice, resolved)
}

// initDrawdownTiersForPosition computes and stores tier allocations when a position is opened.
//
// Allocations are stored in PERCENT units: resolveDrawdownRulesATR runs here so that
// tier.MinProfitPct is directly comparable to a live PnL percent (updateDrawdownTierStates)
// and to the arm path's resolved rules (getCumulativeCloseRatioByRule). Percent-unit
// strategies are unaffected — the resolver no-ops when no rule carries ATR units.
func (at *AutoTrader) initDrawdownTiersForPosition(symbol, side string, quantity, entryPrice float64, rules []store.DrawdownTakeProfitRule) {
	if len(rules) == 0 || quantity <= 0 {
		return
	}

	// 档位比例是"原始开仓量的百分之几",不是"当前剩余量的百分之几"。
	//
	// 分配表只存在内存里(drawdownTierAllocs),重启后由 auto_trader_risk.go 的
	// lazy-init 兜底重算,而那里传进来的是**当前**持仓量。于是:仓位被某一档
	// 部分止盈砍过 → 重启 → 分配表按缩小后的量重算,而交易所上躺着的单还是
	// 按原始量挂的。2026-07-27 线上实测 CL:分配表 1.1/0.33,交易所 1.4/0.4。
	//
	// 后果不止是"数字不好看":arm 路径的数量匹配(auto_trader_risk.go:2208)
	// 已经按 EntryQuantity 锚定,分配表却按当前量 —— 两边算出的目标数量不一致,
	// 匹配失败就会重挂,这正是重复挂单同族问题的又一个入口。
	//
	// 这里向 DB 取原始开仓量;开仓瞬间 DB 行可能还没落地,取不到就退回调用方
	// 传的量(此时它本来就等于开仓量),与 anchorOrCallerEntry 同一形状。
	quantity = at.entryQuantityAnchor(symbol, side, quantity)

	rules = at.resolveDrawdownRulesATR(rules, symbol, side, entryPrice)

	// Apply runner policy enforcement to each rule before allocation
	var cfg store.DrawdownTakeProfitConfig
	if at.config.StrategyConfig != nil {
		cfg = at.config.StrategyConfig.Protection.DrawdownTakeProfit
	}
	enforced := make([]store.DrawdownTakeProfitRule, len(rules))
	for i, rule := range rules {
		rule = normalizeDrawdownRule(rule)
		rule = enforceDrawdownRunnerPolicy(cfg, rule)
		enforced[i] = rule
	}

	allocs := computeDrawdownTierAllocations(quantity, enforced)
	if len(allocs) == 0 {
		return
	}

	at.setDrawdownTierAllocs(symbol, side, allocs)
}

// markUnreachedTiersForBE marks all pending/tracking tiers as "be_covered" so the
// breakeven system takes over for those portions.
func (at *AutoTrader) markUnreachedTiersForBE(symbol, side string) {
	key := positionKey(symbol, side)
	at.drawdownTierAllocMu.Lock()
	defer at.drawdownTierAllocMu.Unlock()

	allocs := at.drawdownTierAllocs[key]
	for i := range allocs {
		if allocs[i].Status == "pending" || allocs[i].Status == "tracking" {
			allocs[i].Status = "be_covered"
			logger.Infof("🔄 Drawdown %s marked be_covered: %s %s (unreached tier → BE takes over)",
				allocs[i].StageName, symbol, side)
		}
	}
}

// findRuleForTier locates the original DrawdownTakeProfitRule that corresponds to
// a triggered tier allocation, matching by MinProfitPct and MaxDrawdownPct.
func findRuleForTier(rules []store.DrawdownTakeProfitRule, tier *store.DrawdownTierAllocation) *store.DrawdownTakeProfitRule {
	if tier == nil || len(rules) == 0 {
		return nil
	}
	for i := range rules {
		if rules[i].MinProfitPct == tier.MinProfitPct && rules[i].MaxDrawdownPct == tier.MaxDrawdownPct {
			return &rules[i]
		}
	}
	// Threshold equality fails whenever the caller's rules and the alloc table were resolved
	// from different entry prices (or one side is still in ATR units). Match on StageName,
	// which survives resolution — full-close and partial tiers are distinguishable by it.
	// Doing this BEFORE the positional fallback matters: TierIndex refers to the SORTED slice
	// inside computeDrawdownTierAllocations, while `rules` here is the caller's unsorted slice,
	// so rules[tier.TierIndex] can silently name a different tier.
	if tier.StageName != "" {
		var match *store.DrawdownTakeProfitRule
		hits := 0
		for i := range rules {
			if inferDrawdownStageName(rules[i]) == tier.StageName {
				match = &rules[i]
				hits++
			}
		}
		if hits == 1 {
			return match
		}
	}
	if tier.TierIndex < len(rules) {
		return &rules[tier.TierIndex]
	}
	return nil
}

// clampDrawdownRulesToTarget ensures DD tier min_profit_pct values are anchored to
// actual structural targets, not arbitrary AI values that exceed the RR target.
// Strategy:
//  1. Extract resistance/support levels beyond entry from the decision's structural data
//  2. Use those as T1/T2/T3 targets (with 0.2% buffer before each)
//  3. If not enough structural levels, fill with fib extensions (1.618, 2.618) of target distance
//
// Only activates when T1 exceeds the first target distance (AI gave unreasonable values).
func clampDrawdownRulesToTarget(rules []store.DrawdownTakeProfitRule, entryPrice, firstTarget float64, side string, structuralTargets []float64) []store.DrawdownTakeProfitRule {
	if len(rules) == 0 || entryPrice <= 0 || firstTarget <= 0 {
		return rules
	}

	var targetDistPct float64
	if strings.EqualFold(side, "long") {
		targetDistPct = (firstTarget - entryPrice) / entryPrice * 100
	} else {
		targetDistPct = (entryPrice - firstTarget) / entryPrice * 100
	}

	if targetDistPct <= 0 {
		return rules
	}

	sort.Slice(rules, func(i, j int) bool {
		return rules[i].MinProfitPct < rules[j].MinProfitPct
	})

	t1Profit := rules[0].MinProfitPct
	if t1Profit <= 0 {
		return rules
	}

	// Only clamp if T1 exceeds the target distance (T1 should arm before or at target)
	if t1Profit <= targetDistPct {
		return rules
	}

	// Build tier targets from structural levels + fib extensions
	tierTargets, tierSources := buildTierTargets(entryPrice, firstTarget, targetDistPct, side, structuralTargets, len(rules))

	clamped := make([]store.DrawdownTakeProfitRule, len(rules))
	for i, rule := range rules {
		clamped[i] = rule
		if i < len(tierTargets) {
			clamped[i].MinProfitPct = tierTargets[i]
		}
	}

	logger.Infof("📐 DD tiers clamped to structural targets: entry=%.4f target=%.4f dist=%.2f%%",
		entryPrice, firstTarget, targetDistPct)
	for i, r := range clamped {
		source := "fib"
		if i < len(tierSources) {
			source = tierSources[i]
		}
		logger.Infof("  → T%d: min_profit=%.2f%% (was %.2f%%) [%s]", i+1, r.MinProfitPct, rules[i].MinProfitPct, source)
	}

	return clamped
}

// buildTierTargets produces min_profit_pct values for each DD tier.
// Uses structural resistance/support levels beyond entry when available,
// falls back to fibonacci extensions of the target distance.
// Returns (targets, sources) where sources[i] is "target"/"struct"/"fib".
func buildTierTargets(entryPrice, firstTarget, targetDistPct float64, side string, structuralTargets []float64, numTiers int) ([]float64, []string) {
	const buffer = 0.2 // arm slightly before reaching the level

	// Convert structural prices to distance percentages, filter to those beyond entry
	var structDistances []float64
	for _, price := range structuralTargets {
		var dist float64
		if strings.EqualFold(side, "long") {
			dist = (price - entryPrice) / entryPrice * 100
		} else {
			dist = (entryPrice - price) / entryPrice * 100
		}
		// Only use levels meaningfully beyond the first target (at least 0.5% further)
		if dist > targetDistPct+0.5 {
			structDistances = append(structDistances, dist)
		}
	}
	sort.Float64s(structDistances)

	// Deduplicate (levels within 0.3% of each other)
	var deduped []float64
	for _, d := range structDistances {
		if len(deduped) == 0 || d-deduped[len(deduped)-1] > 0.3 {
			deduped = append(deduped, d)
		}
	}

	// T1 is always anchored to the first target
	targets := make([]float64, 0, numTiers)
	sources := make([]string, 0, numTiers)
	t1 := targetDistPct - buffer
	if t1 < 0.3 {
		t1 = 0.3
	}
	targets = append(targets, math.Round(t1*100)/100)
	sources = append(sources, "target")

	// Fill T2+ from structural levels first, then fib extensions
	structIdx := 0
	fibMultipliers := []float64{1.618, 2.618, 3.618, 4.618}
	fibIdx := 0

	for len(targets) < numTiers {
		var nextDist float64
		var src string
		if structIdx < len(deduped) {
			nextDist = deduped[structIdx] - buffer
			src = "struct"
			structIdx++
		} else if fibIdx < len(fibMultipliers) {
			nextDist = targetDistPct*fibMultipliers[fibIdx] - buffer
			src = "fib"
			fibIdx++
		} else {
			last := targets[len(targets)-1]
			nextDist = last * 1.5
			src = "fib"
		}

		if nextDist < 0.3 {
			nextDist = 0.3
		}
		// Ensure monotonically increasing
		if len(targets) > 0 && nextDist <= targets[len(targets)-1] {
			nextDist = targets[len(targets)-1] + 0.5
		}
		targets = append(targets, math.Round(nextDist*100)/100)
		sources = append(sources, src)
	}

	return targets, sources
}

// computeRemainingQuantityForBE calculates the total quantity of tiers that haven't been
// executed by drawdown — this is the quantity that BE should protect.
func (at *AutoTrader) computeRemainingQuantityForBE(symbol, side string) float64 {
	allocs := at.getDrawdownTierAllocs(symbol, side)
	if len(allocs) == 0 {
		return 0
	}
	remaining := 0.0
	for _, a := range allocs {
		if a.Status != "executed" {
			remaining += math.Abs(a.Quantity)
		}
	}
	return remaining
}

// isDrawdownTierExecuted checks if the tier matching the given rule has already been
// executed (trailing order filled). Matches by MinProfitPct and MaxDrawdownPct.
func (at *AutoTrader) isDrawdownTierExecuted(symbol, side string, rule store.DrawdownTakeProfitRule) bool {
	allocs := at.getDrawdownTierAllocs(symbol, side)
	for _, a := range allocs {
		if a.MinProfitPct == rule.MinProfitPct && a.MaxDrawdownPct == rule.MaxDrawdownPct && a.Status == "executed" {
			return true
		}
	}
	return false
}

// drawdownAttributedCloseQty returns how much of THIS position was closed by a
// drawdown mechanism (exchange-native trailing or the code-side managed monitor),
// and how much was closed by anything else (ladder TP, break-even, structural SL,
// AI close, time/max-hold stops, manual...).
//
// Both numbers come from position_close_events, which the sync path fills with exact
// 1:1 attribution (OKX coded client-id / order-detail algoClOrdId, Binance order id).
// It is the authoritative answer to "did a DD tier actually fill", and it exists for
// every close — so no tier state ever has to be *inferred* from position shrinkage.
func (at *AutoTrader) drawdownAttributedCloseQty(symbol, side string) (ddQty, otherQty float64, ok bool) {
	if at.store == nil {
		return 0, 0, false
	}
	pos, err := at.store.Position().GetOpenPositionBySymbol(at.id, symbol, strings.ToUpper(side))
	if err != nil || pos == nil || pos.ID == 0 {
		return 0, 0, false
	}
	events, err := at.store.PositionClose().ListByPositionID(int64(pos.ID))
	if err != nil {
		return 0, 0, false
	}
	for _, ev := range events {
		if ev == nil || ev.CloseQuantity <= 0 {
			continue
		}
		if isDrawdownAttribution(ev.CloseReason) || isDrawdownAttribution(ev.ExecutionSource) {
			ddQty += ev.CloseQuantity
			continue
		}
		otherQty += ev.CloseQuantity
	}
	return ddQty, otherQty, true
}

// isDrawdownAttribution reports whether an attribution string denotes a drawdown
// take-profit execution. Exactly two mechanisms can fill a DD tier:
//   - native_trailing      — the exchange trailing order we placed for the tier
//   - managed_drawdown     — the in-process monitor closing the tier itself
//
// Anything else (ladder_tp / break_even_stop / structural_sl / full_tp / full_sl /
// ai_close* / max_hold / time_stop / giveback_guard_* / manual_close*) is NOT a DD
// fill, no matter how much quantity it removed.
func isDrawdownAttribution(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "native_trailing", "managed_drawdown":
		return true
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "managed_drawdown_")
}

// detectNativeTrailingFills marks a tier executed when its trailing order actually
// filled.
//
// It used to INFER that from position shrinkage alone: expectedRemaining - currentQty
// over a threshold ⇒ "the top tracking tier filled". That inference is wrong whenever
// something else legitimately reduces the position, and for a full-close tier
// (close_ratio=100, expectedRemaining = the WHOLE position) any other mechanism taking
// >50% forges a fill. Live 2026-07-28/29: fired 8 times in two days.
//
// GPT SKHYNIXUSDT short is the worked example. Four ladder_tp fills took 0.035→0.017;
// at 18:40:15 deficit 0.018 ≥ dd1's 0.0175 half-quantity ⇒ dd1 marked executed. dd1 had
// never filled — position_close_events attributes all four closes to ladder_tp. The
// consequence is not cosmetic: isDrawdownTierExecuted then permanently refuses to
// re-arm the tier, so the strategy's configured DD protection stayed off the exchange
// for the rest of the position's life.
//
// So the inference is replaced by attribution: subtract the quantity that verifiably
// closed for non-DD reasons before comparing, and require a DD-attributed close event
// to exist before marking anything executed. When attribution is unavailable (store
// error, pre-attribution legacy position) we mark NOTHING — an un-marked tier stays
// protected on the exchange, a wrongly-marked one is abandoned. Only one of those two
// failure modes is safe.
func (at *AutoTrader) detectNativeTrailingFills(symbol, side string, currentQuantity float64) {
	ddClosed, otherClosed, haveAttribution := at.drawdownAttributedCloseQty(symbol, side)

	key := positionKey(symbol, side)
	at.drawdownTierAllocMu.Lock()
	defer at.drawdownTierAllocMu.Unlock()

	allocs := at.drawdownTierAllocs[key]
	if len(allocs) == 0 {
		return
	}

	// Compute expected remaining quantity (sum of non-executed tiers)
	expectedRemaining := 0.0
	for _, a := range allocs {
		if a.Status != "executed" && a.Status != "be_covered" {
			expectedRemaining += math.Abs(a.Quantity)
		}
	}

	if expectedRemaining <= 0 {
		return
	}

	if !haveAttribution {
		logger.Infof("🟡 Drawdown fill detection skipped (no close attribution available): %s %s position=%.4f expected=%.4f — leaving tiers armed",
			symbol, side, currentQuantity, expectedRemaining)
		return
	}

	// A DD tier can only have filled if a DD-attributed close exists. Without one,
	// every unit that left the position left through some other mechanism.
	if ddClosed <= 0 {
		if otherClosed > 0 {
			logger.Infof("🟡 Drawdown fill detection: %s %s position shrank by %.4f but ALL of it is attributed to non-drawdown closes (dd=0) — no tier marked executed",
				symbol, side, otherClosed)
		}
		return
	}

	// Non-DD closes are not evidence of a DD fill: discount them before comparing.
	deficit := expectedRemaining - currentQuantity - otherClosed
	if deficit <= expectedRemaining*0.05 {
		return
	}
	// Never credit a tier with more than the DD mechanism actually closed.
	if deficit > ddClosed {
		deficit = ddClosed
	}

	// Find the highest-index "tracking" tier and mark it as executed
	for i := len(allocs) - 1; i >= 0; i-- {
		if allocs[i].Status == "tracking" {
			tierQty := math.Abs(allocs[i].Quantity)
			if deficit >= tierQty*0.5 {
				allocs[i].Status = "executed"
				logger.Infof("✅ Drawdown %s detected as filled (dd-attributed): %s %s | position=%.4f expected=%.4f ddClosed=%.4f otherClosed=%.4f deficit=%.4f",
					allocs[i].StageName, symbol, side, currentQuantity, expectedRemaining, ddClosed, otherClosed, deficit)
				return
			}
		}
	}
}
