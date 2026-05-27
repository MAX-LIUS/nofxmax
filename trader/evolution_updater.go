package trader

import (
	"encoding/json"
	"nofx/logger"
	"nofx/store"
	"strings"
	"time"
)

// updateEvolutionProfile recalculates the evolution profile for a coin+direction
// after a trade closes. Runs asynchronously to not block the main loop.
func (at *AutoTrader) updateEvolutionProfile(symbol, side string) {
	if at.store == nil {
		return
	}

	normalizedSide := strings.ToLower(side)
	if normalizedSide != "long" && normalizedSide != "short" {
		return
	}

	// Fetch recent closed trades for this symbol+side (last 30)
	trades, err := at.store.Position().GetClosedTradesForEvolution(at.id, symbol, normalizedSide, 30)
	if err != nil {
		logger.Infof("⚠️ [%s] Evolution: failed to fetch trades for %s %s: %v", at.name, symbol, normalizedSide, err)
		return
	}
	if len(trades) < 3 {
		return // not enough data
	}

	// Convert to TradeOutcome format
	outcomes := make([]store.TradeOutcome, 0, len(trades))
	for _, t := range trades {
		outcome := store.TradeOutcome{
			Symbol:      t.Symbol,
			Side:        normalizedSide,
			PnLPct:      0,
			IsWin:       t.RealizedPnL > 0,
			CloseTime:   time.UnixMilli(t.ExitTime),
			EntryTime:   time.UnixMilli(t.EntryTime),
			CloseReason: t.CloseReason,
		}

		// Calculate PnL percentage
		if t.EntryPrice > 0 && t.ExitPrice > 0 {
			if normalizedSide == "long" {
				outcome.PnLPct = (t.ExitPrice - t.EntryPrice) / t.EntryPrice * 100
			} else {
				outcome.PnLPct = (t.EntryPrice - t.ExitPrice) / t.EntryPrice * 100
			}
		}

		// Parse scene tags
		if t.EntrySceneTags != "" {
			var tags store.SceneTagsData
			if err := json.Unmarshal([]byte(t.EntrySceneTags), &tags); err == nil {
				outcome.SceneTags = tags
			}
		}

		outcomes = append(outcomes, outcome)
	}

	// Compute factors
	factors := store.ComputeFactors(outcomes)
	if len(factors) == 0 {
		return
	}

	// Generate adaptations
	adaptations := store.GenerateAdaptations(factors)

	// Get or create profile
	profile, err := at.store.Evolution().GetProfile(at.id, symbol, normalizedSide)
	if err != nil {
		logger.Infof("⚠️ [%s] Evolution: failed to get profile for %s %s: %v", at.name, symbol, normalizedSide, err)
		return
	}
	if profile == nil {
		profile = &store.CoinEvolutionProfile{
			TraderID: at.id,
			Symbol:   symbol,
			Side:     normalizedSide,
		}
	}

	// Merge adaptations: keep existing ones that are still valid, add new ones
	existingAdapts := profile.GetAdaptations()
	existingAdapts = store.PruneExpiredAdaptations(existingAdapts)

	// Replace adaptations with same condition, keep others
	mergedAdapts := mergeAdaptations(existingAdapts, adaptations)

	profile.SetFactors(factors)
	profile.SetAdaptations(mergedAdapts)
	profile.SampleSize = len(trades)

	if err := at.store.Evolution().SaveProfile(profile); err != nil {
		logger.Infof("⚠️ [%s] Evolution: failed to save profile for %s %s: %v", at.name, symbol, normalizedSide, err)
	} else {
		logger.Infof("🧬 [%s] Evolution profile updated: %s %s (v%d, %d trades, %d adaptations)",
			at.name, symbol, normalizedSide, profile.Version, profile.SampleSize, len(mergedAdapts))
	}
}

// mergeAdaptations combines existing and new adaptations, replacing by condition.
func mergeAdaptations(existing, new []store.Adaptation) []store.Adaptation {
	condMap := make(map[string]store.Adaptation)
	for _, a := range existing {
		condMap[a.Condition] = a
	}
	for _, a := range new {
		condMap[a.Condition] = a
	}
	result := make([]store.Adaptation, 0, len(condMap))
	for _, a := range condMap {
		result = append(result, a)
	}
	return result
}
