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
