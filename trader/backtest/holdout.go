package backtest

import (
	"fmt"
	"sort"
)

// sortLoadedByEntryTime orders prepared entries chronologically so a train/test
// split is a genuine past→future holdout (the multi-symbol merge interleaves
// symbols, so a raw split would leak future bars into train).
func sortLoadedByEntryTime(loaded []loadedEntry) {
	sort.SliceStable(loaded, func(i, j int) bool {
		return loaded[i].entry.EntryTime < loaded[j].entry.EntryTime
	})
}

// HoldoutResult reports out-of-sample validation: params are optimised on the
// train split (chronologically first trainFrac of entries) then scored on the
// untouched test split, alongside the percent baseline and ATR-wide reference
// on the SAME test set. If the optimised params beat baseline OOS, the gain is
// real; if they collapse to baseline-or-worse OOS, the in-sample win was overfit.
type HoldoutResult struct {
	TrainN, TestN int
	Final         ProtectionParams
	// Test-set metrics:
	TestPctPnL, TestPctDD     float64
	TestWidePnL, TestWideDD   float64
	TestFinalPnL, TestFinalDD float64
	TestFinalWin, TestFinalPF float64
}

// OptimizeHoldout splits loaded chronologically, optimises on train, validates
// on test. trainFrac e.g. 0.7 = first 70% train, last 30% test.
func OptimizeHoldout(loaded []loadedEntry, lambda, trainFrac float64) HoldoutResult {
	n := len(loaded)
	cut := int(float64(n) * trainFrac)
	if cut < 1 {
		cut = 1
	}
	if cut >= n {
		cut = n - 1
	}
	train, test := loaded[:cut], loaded[cut:]

	_, final := OptimizeProtectionATR(train, lambda)

	pct := RunParams(ClaudeBaselineParams(), test)
	wide := RunParams(buildATRParams(4.5, 2.0, 7.0, 2.0, 3.5), test)
	fin := RunParams(final, test)

	return HoldoutResult{
		TrainN: cut, TestN: n - cut, Final: final,
		TestPctPnL: pct.TotalPnL, TestPctDD: pct.MaxDrawdown,
		TestWidePnL: wide.TotalPnL, TestWideDD: wide.MaxDrawdown,
		TestFinalPnL: fin.TotalPnL, TestFinalDD: fin.MaxDrawdown,
		TestFinalWin: fin.WinRatePct, TestFinalPF: fin.ProfitFactor,
	}
}

// FormatHoldout renders the OOS validation verdict.
func FormatHoldout(h HoldoutResult) string {
	s := fmt.Sprintf("OUT-OF-SAMPLE VALIDATION (train=%d trades, test=%d trades)\n", h.TrainN, h.TestN)
	s += fmt.Sprintf("optimised-on-train params: %s\n", DescribeParams(h.Final))
	s += fmt.Sprintf("%-32s | %9s %9s\n", "config (scored on TEST set)", "PnL", "MaxDD")
	s += fmt.Sprintf("%-32s | %9.2f %9.2f\n", "percent baseline (live)", h.TestPctPnL, h.TestPctDD)
	s += fmt.Sprintf("%-32s | %9.2f %9.2f\n", "ATR-wide reference", h.TestWidePnL, h.TestWideDD)
	s += fmt.Sprintf("%-32s | %9.2f %9.2f (win%%=%.1f PF=%.2f)\n",
		"OPTIMISED (train→test)", h.TestFinalPnL, h.TestFinalDD, h.TestFinalWin, h.TestFinalPF)
	return s
}
