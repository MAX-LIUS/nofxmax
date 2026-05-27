package trader

import (
	"encoding/json"
	"nofx/kernel"
	"nofx/logger"
	"nofx/market"
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

// applyEvolutionAdaptations modifies a decision based on the coin's evolution profile.
// This is NOT a hard block — it adjusts position size and logs the adjustment.
func (at *AutoTrader) applyEvolutionAdaptations(d *kernel.Decision, data *market.Data) {
	if at.store == nil || d == nil {
		return
	}

	side := "long"
	if strings.Contains(strings.ToLower(d.Action), "short") {
		side = "short"
	}

	profile, err := at.store.Evolution().GetProfile(at.id, d.Symbol, side)
	if err != nil || profile == nil || profile.SampleSize < 3 {
		return
	}

	adaptations := store.PruneExpiredAdaptations(profile.GetAdaptations())
	if len(adaptations) == 0 {
		return
	}

	// Determine current market conditions for matching
	var phase string
	var chg4hAbs float64
	if data != nil {
		tp := market.ClassifyTrendPhase(data)
		phase = tp.Phase
		chg4hAbs = tp.Chg4hAbs
	}

	var ema20Contradicted bool
	if data != nil && data.CurrentEMA20 > 0 {
		dev := (data.CurrentPrice - data.CurrentEMA20) / data.CurrentEMA20 * 100
		if (side == "long" && dev < -0.5) || (side == "short" && dev > 0.5) {
			ema20Contradicted = true
		}
	}

	originalSize := d.PositionSizeUSD
	applied := []string{}

	for _, adapt := range adaptations {
		matched := false
		switch adapt.Condition {
		case "phase=extension":
			matched = phase == "extension" || phase == "exhaustion"
		case "ema20_contradicted":
			matched = ema20Contradicted
		case "chg4h_gt_2.5":
			matched = chg4hAbs > 2.5
		case "trigger_low_quality":
			// Matches when the trigger type is in the "worst" category for this coin
			// For now, always apply when condition exists (the adaptation itself is only
			// generated when trigger quality score is very low)
			matched = true
		case "trigger_tf=15m":
			continue
		}

		if !matched {
			continue
		}

		// Parse and apply actions
		actions := strings.Split(adapt.Action, ",")
		for _, action := range actions {
			action = strings.TrimSpace(action)
			switch {
			case action == "reduce_size_50%":
				d.PositionSizeUSD *= 0.5
				applied = append(applied, "size×0.5")
			case action == "reduce_size_30%":
				d.PositionSizeUSD *= 0.3
				applied = append(applied, "size×0.3")
			case action == "require_confidence_85":
				if d.Confidence < 85 {
					d.PositionSizeUSD *= 0.6 // Don't block, but reduce size significantly
					applied = append(applied, "conf<85→size×0.6")
				}
			case action == "require_confidence_90":
				if d.Confidence < 90 {
					d.PositionSizeUSD *= 0.5
					applied = append(applied, "conf<90→size×0.5")
				}
			}
		}
	}

	if len(applied) > 0 && d.PositionSizeUSD != originalSize {
		logger.Infof("🧬 [%s] Evolution adaptation applied for %s %s: %s (size %.0f→%.0f)",
			at.name, d.Symbol, d.Action, strings.Join(applied, "; "), originalSize, d.PositionSizeUSD)
	}
}
