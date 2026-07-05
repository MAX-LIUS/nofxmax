package backtest

// AblateProtectionRobust prepares mechanical entries then runs the layer
// ablation (guard off). Returns rows plus per-symbol entry counts.
func AblateProtectionRobust(cfg RobustConfig) ([]AblationRow, map[string]int, error) {
	merged, per, err := PrepareRobustPortfolioEntries(cfg)
	if err != nil {
		return nil, per, err
	}
	return AblateProtection(merged), per, nil
}

// AblateProtectionFromEntries fetches OKX bars per entry then runs the ablation.
func AblateProtectionFromEntries(entries []Entry, tf string) ([]AblationRow, int, int) {
	loaded, skipped := PrepareEntries(entries, tf, OKXBars)
	if len(loaded) == 0 {
		return nil, 0, skipped
	}
	return AblateProtection(loaded), len(loaded), skipped
}

// CompareUnitsRobust prepares mechanical entries then runs the %-vs-ATR compare.
func CompareUnitsRobust(cfg RobustConfig) ([]UnitCompareRow, map[string]int, error) {
	merged, per, err := PrepareRobustPortfolioEntries(cfg)
	if err != nil {
		return nil, per, err
	}
	return CompareUnits(merged, nil), per, nil
}

// CompareUnitsFromEntries fetches OKX bars per entry then runs the %-vs-ATR compare.
func CompareUnitsFromEntries(entries []Entry, tf string) ([]UnitCompareRow, int, int) {
	loaded, skipped := PrepareEntries(entries, tf, OKXBars)
	if len(loaded) == 0 {
		return nil, 0, skipped
	}
	return CompareUnits(loaded, nil), len(loaded), skipped
}

// OptimizeProtectionRobust prepares mechanical entries then runs the staged ATR
// optimiser. Returns stage rows, the final params, and per-symbol entry counts.
func OptimizeProtectionRobust(cfg RobustConfig, lambda float64) ([]OptResult, ProtectionParams, map[string]int, error) {
	merged, per, err := PrepareRobustPortfolioEntries(cfg)
	if err != nil {
		return nil, ProtectionParams{}, per, err
	}
	stages, final := OptimizeProtectionATR(merged, lambda)
	return stages, final, per, nil
}

// OptimizeHoldoutRobust prepares the broad mechanical sample, time-orders it,
// then optimises on the train split and validates on the untouched test split.
// This is the universality/overfitting guard for the multi-coin run.
func OptimizeHoldoutRobust(cfg RobustConfig, lambda, trainFrac float64) (HoldoutResult, map[string]int, error) {
	merged, per, err := PrepareRobustPortfolioEntries(cfg)
	if err != nil {
		return HoldoutResult{}, per, err
	}
	sortLoadedByEntryTime(merged)
	return OptimizeHoldout(merged, lambda, trainFrac), per, nil
}

// OptimizeProtectionFromEntries fetches OKX bars per entry then runs the optimiser.
func OptimizeProtectionFromEntries(entries []Entry, tf string, lambda float64) ([]OptResult, ProtectionParams, int, int) {
	loaded, skipped := PrepareEntries(entries, tf, OKXBars)
	if len(loaded) == 0 {
		return nil, ProtectionParams{}, 0, skipped
	}
	stages, final := OptimizeProtectionATR(loaded, lambda)
	return stages, final, len(loaded), skipped
}

// RankCandidatesRobust prepares the broad sample and ranks curated candidates.
func RankCandidatesRobust(cfg RobustConfig, lambda, trainFrac float64) ([]CandRankRow, map[string]int, error) {
	merged, per, err := PrepareRobustPortfolioEntries(cfg)
	if err != nil {
		return nil, per, err
	}
	return RankCandidates(merged, lambda, trainFrac), per, nil
}

// RankCandidatesFromEntries fetches bars per entry then ranks curated candidates.
func RankCandidatesFromEntries(entries []Entry, tf string, lambda, trainFrac float64) ([]CandRankRow, int, int) {
	loaded, skipped := PrepareEntries(entries, tf, OKXBars)
	if len(loaded) == 0 {
		return nil, 0, skipped
	}
	return RankCandidates(loaded, lambda, trainFrac), len(loaded), skipped
}

// OptimizeHoldoutFromEntries fetches bars then runs OOS train/test validation.
func OptimizeHoldoutFromEntries(entries []Entry, tf string, lambda, trainFrac float64) (HoldoutResult, int, int) {
	loaded, skipped := PrepareEntries(entries, tf, OKXBars)
	if len(loaded) == 0 {
		return HoldoutResult{}, 0, skipped
	}
	return OptimizeHoldout(loaded, lambda, trainFrac), len(loaded), skipped
}

// CompareStructuralFromEntries fetches bars per entry, then compares percent/ATR
// vs AI structural protection (Variant A), a config-buffer sweep (Variant B), and
// structural-TP+ATR-SL hybrids on the structurally-matched subset. The BE/DD base
// is held constant (Claude baseline) so only SL/TP placement differs. testTailFrac
// in (0,1) restricts scoring to the most recent fraction (out-of-sample tail).
// Returns (rows, usedMatched, skipped).
func CompareStructuralFromEntries(entries []Entry, tf string, bufferSweep []float64, testTailFrac float64) ([]StructCompareRow, int, int) {
	loaded, skipped := PrepareEntries(entries, tf, OKXBars)
	if len(loaded) == 0 {
		return nil, 0, skipped
	}
	base := ClaudeBaselineParams() // supplies BE/DD tiers, held constant
	rows, used := CompareStructural(loaded, base, bufferSweep, testTailFrac)
	return rows, used, skipped
}

