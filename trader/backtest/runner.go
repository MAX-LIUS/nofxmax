package backtest

import (
	"fmt"

	"nofx/market"
)

// preBarsForATR is how many bars of pre-entry history to fetch for ATR.
const preBarsForATR = 60

// maxHoldHoursDefault caps open-ended replays.
const maxHoldHoursDefault = 24 * 14 // 14 days

// loadedEntry pairs an entry with its pre-fetched bars and entry index, so a
// parameter sweep can reuse the same bars across many ProtectionParams.
type loadedEntry struct {
	entry    Entry
	bars     []market.Kline
	entryIdx int
}

// PrepareEntries fetches bars once for every entry (the slow, network-bound
// step). Entries whose bars can't be fetched are skipped and counted.
func PrepareEntries(entries []Entry, tf string, provider BarsProvider) ([]loadedEntry, int) {
	var loaded []loadedEntry
	skipped := 0
	for _, e := range entries {
		bars, idx, err := fetchEntryBars(e, tf, preBarsForATR, maxHoldHoursDefault, provider)
		if err != nil || idx < 0 {
			skipped++
			continue
		}
		loaded = append(loaded, loadedEntry{entry: e, bars: bars, entryIdx: idx})
	}
	return loaded, skipped
}

// RunParams replays one ProtectionParams over pre-loaded entries and aggregates.
func RunParams(p ProtectionParams, loaded []loadedEntry) PortfolioResult {
	results := make([]TradeResult, 0, len(loaded))
	for _, le := range loaded {
		results = append(results, ReplayEntry(p, le.entry, le.bars, le.entryIdx))
	}
	return Aggregate(results)
}

// ClaudeRealizedPnL sums the actual realized P&L recorded for the loaded
// entries (for fidelity comparison against the percent-baseline backtest).
func ClaudeRealizedPnL(loaded []loadedEntry) float64 {
	var s float64
	for _, le := range loaded {
		s += le.entry.RealizedPnL
	}
	return s
}

// FidelityReport compares the percent-baseline backtest against Claude's actual
// realized P&L, returning a human-readable summary and the relative error.
func FidelityReport(loaded []loadedEntry, baseline ProtectionParams) (string, float64) {
	bt := RunParams(baseline, loaded)
	actual := ClaudeRealizedPnL(loaded)
	relErr := 0.0
	if actual != 0 {
		relErr = (bt.TotalPnL - actual) / absf(actual) * 100
	}
	msg := fmt.Sprintf(
		"fidelity: trades=%d | backtest_pnl=%.2f | claude_actual_pnl=%.2f | rel_err=%.1f%% | win%%=%.1f PF=%.2f",
		bt.Trades, bt.TotalPnL, actual, relErr, bt.WinRatePct, bt.ProfitFactor)
	return msg, relErr
}

// EvaluateCandidatesOnLoaded evaluates each candidate on already-prepared
// entries (e.g. real DB entries fetched once via PrepareEntries).
func EvaluateCandidatesOnLoaded(candidates []Candidate, loaded []loadedEntry) []CandidateEval {
	evals := make([]CandidateEval, 0, len(candidates))
	for _, c := range candidates {
		evals = append(evals, EvaluateCandidate(c, loaded))
	}
	return evals
}

// PrepareRealEntries loads Claude's real entries from the DB and fetches their
// OKX bars, returning prepared entries plus a skip count. Exposed so cmd can
// run candidate evaluation on the real-entry sample.
func PrepareRealEntries(loaded []Entry, tf string) ([]loadedEntry, int) {
	return PrepareEntries(loaded, tf, OKXBars)
}

func absf(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
