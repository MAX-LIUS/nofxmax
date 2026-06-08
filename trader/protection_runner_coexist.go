package trader

import "nofx/store"

// ladderRunnerCoexistsWithDrawdown reports whether the strategy is configured for
// the "ladder TP + DD-on-runner" coexistence mode: ladder TP takes staged profit
// (e.g. +3%/40%, +6%/35%) and drawdown trails ONLY the remaining runner afterwards.
//
// When true, BuildConfiguredProtectionPlan must NOT suppress the ladder TP legs even
// though drawdown is enabled, and DD must only arm once the ladder TP has fully filled
// (see ladderRunnerStageReached).
func ladderRunnerCoexistsWithDrawdown(p store.ProtectionConfig) bool {
	ladderTP := p.LadderTPSL.Enabled &&
		protectionFeatureUsesManual(p.LadderTPSL.Mode) &&
		p.LadderTPSL.TakeProfitEnabled &&
		ladderTakeProfitCloseRatioTotal(p.LadderTPSL) > 0
	ddOn := p.DrawdownTakeProfit.Enabled && len(p.DrawdownTakeProfit.Rules) > 0
	return ladderTP && ddOn
}

// ladderTakeProfitCloseRatioTotal returns the cumulative take-profit close ratio (%)
// configured across all ladder rules — i.e. how much of the original position the
// ladder TP tiers will close before the runner remains.
func ladderTakeProfitCloseRatioTotal(ladder store.LadderTPSLConfig) float64 {
	total := 0.0
	for _, r := range ladder.Rules {
		if r.TakeProfitPct > 0 && r.TakeProfitCloseRatioPct > 0 {
			total += r.TakeProfitCloseRatioPct
		}
	}
	if total > 100 {
		total = 100
	}
	return total
}

// ladderRunnerStageReached reports whether the position has been reduced past the
// ladder TP's cumulative close ratio — i.e. the ladder TP tiers have (effectively)
// fully filled and only the runner remains. Used to gate DD arming so drawdown only
// trails the residual runner, never the full position while ladder TP is still working.
//
// A 10% tolerance is applied to the closed ratio so a near-complete ladder fill
// (rounding / min-contract dust) still counts as runner stage.
func ladderRunnerStageReached(entryQuantity, currentQuantity, ladderTPCloseRatioPct float64) bool {
	if entryQuantity <= 0 || currentQuantity <= 0 || ladderTPCloseRatioPct <= 0 {
		return false
	}
	if currentQuantity >= entryQuantity {
		return false
	}
	closedPct := (entryQuantity - currentQuantity) / entryQuantity * 100.0
	// Require closed ratio to reach (ladderTPClose - 10% tolerance) of the original.
	threshold := ladderTPCloseRatioPct * 0.9
	return closedPct >= threshold
}
