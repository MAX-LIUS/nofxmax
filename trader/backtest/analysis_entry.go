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

// OptimizeProtectionFromEntries fetches OKX bars per entry then runs the optimiser.
func OptimizeProtectionFromEntries(entries []Entry, tf string, lambda float64) ([]OptResult, ProtectionParams, int, int) {
	loaded, skipped := PrepareEntries(entries, tf, OKXBars)
	if len(loaded) == 0 {
		return nil, ProtectionParams{}, 0, skipped
	}
	stages, final := OptimizeProtectionATR(loaded, lambda)
	return stages, final, len(loaded), skipped
}

// OptimizeHoldoutFromEntries fetches bars then runs OOS train/test validation.
func OptimizeHoldoutFromEntries(entries []Entry, tf string, lambda, trainFrac float64) (HoldoutResult, int, int) {
	loaded, skipped := PrepareEntries(entries, tf, OKXBars)
	if len(loaded) == 0 {
		return HoldoutResult{}, 0, skipped
	}
	return OptimizeHoldout(loaded, lambda, trainFrac), len(loaded), skipped
}
