package backtest

import (
	"fmt"
	"time"

	"nofx/market"
)

// DefaultCandidates returns the compromise candidate set to validate across
// both samples, framed around the cross-sample-stable findings (BE2≈2.5, TP1
// in 2-3) and a middle ground for the divergent dims (SL, TP2).
func DefaultCandidates() []Candidate {
	return []Candidate{
		{Name: "real_opt", SL: 2.5, TP1: 2.0, TP2: 3.0, BE1: 1.0, BE2: 2.5},
		{Name: "mech_opt", SL: 4.0, TP1: 3.0, TP2: 6.0, BE1: 2.0, BE2: 2.5},
		{Name: "compromise", SL: 3.0, TP1: 2.5, TP2: 4.0, BE1: 1.5, BE2: 2.5},
		{Name: "compromise_wideSL", SL: 3.5, TP1: 2.5, TP2: 5.0, BE1: 1.5, BE2: 2.5},
	}
}

// EvaluateCandidatesRobust fetches the same long-history mechanical entries as
// RunRobust and evaluates each candidate on them.
func EvaluateCandidatesRobust(cfg RobustConfig, candidates []Candidate) ([]CandidateEval, error) {
	if cfg.Timeframe == "" {
		cfg.Timeframe = "1h"
	}
	if cfg.EMAFast <= 0 {
		cfg.EMAFast = 20
	}
	if cfg.EMASlow <= 0 {
		cfg.EMASlow = 50
	}
	if cfg.CooldownBars <= 0 {
		cfg.CooldownBars = 12
	}
	if cfg.Months <= 0 {
		cfg.Months = 6
	}
	end := time.Now()
	start := end.AddDate(0, -cfg.Months, 0)

	var merged []loadedEntry
	for _, sym := range cfg.Symbols {
		if sym == "" {
			continue
		}
		bars, err := market.GetKlinesRangeOKX(sym, cfg.Timeframe, start, end)
		if err != nil || len(bars) < 250 {
			continue
		}
		entries := GenerateEMACrossEntries(sym, bars, cfg.EMAFast, cfg.EMASlow, cfg.CooldownBars, 1000.0)
		merged = append(merged, PrepareEntriesFromBars(entries, bars)...)
	}
	if len(merged) == 0 {
		return nil, fmt.Errorf("no entries generated")
	}
	evals := make([]CandidateEval, 0, len(candidates))
	for _, c := range candidates {
		evals = append(evals, EvaluateCandidate(c, merged))
	}
	return evals, nil
}

// GetKlinesRangeForBacktest is the exported OKX range fetcher for cmd use.
func GetKlinesRangeForBacktest(symbol, tf string, start, end time.Time) ([]market.Kline, error) {
	return market.GetKlinesRangeOKX(symbol, tf, start, end)
}

// RobustConfig configures a long-period multi-symbol robustness run.
type RobustConfig struct {
	Symbols      []string
	Timeframe    string
	Months       int
	EMAFast      int
	EMASlow      int
	CooldownBars int
	Signal       string // "ema" (default) or "breakout"
	BreakoutLook int    // Donchian lookback bars (breakout signal)
}

// RobustResult holds the baseline and sweep outcome of a robustness run.
type RobustResult struct {
	TotalEntries int
	PerSymbol    map[string]int
	Baseline     PortfolioResult
	Sweep        []SweepPoint
}

// RunRobust fetches long history per symbol, generates EMA-cross mechanical
// entries, merges them, and runs baseline + ATR sweep. All bar fetching and
// loadedEntry handling stays inside the package (loadedEntry is unexported).
func RunRobust(cfg RobustConfig, grid ATRGrid) (RobustResult, error) {
	if cfg.Timeframe == "" {
		cfg.Timeframe = "1h"
	}
	if cfg.EMAFast <= 0 {
		cfg.EMAFast = 20
	}
	if cfg.EMASlow <= 0 {
		cfg.EMASlow = 50
	}
	if cfg.CooldownBars <= 0 {
		cfg.CooldownBars = 12
	}
	if cfg.Months <= 0 {
		cfg.Months = 6
	}

	end := time.Now()
	start := end.AddDate(0, -cfg.Months, 0)

	res := RobustResult{PerSymbol: map[string]int{}}
	var merged []loadedEntry

	for _, sym := range cfg.Symbols {
		if sym == "" {
			continue
		}
		bars, err := market.GetKlinesRangeOKX(sym, cfg.Timeframe, start, end)
		if err != nil || len(bars) < 250 {
			fmt.Printf("  skip %s: err=%v bars=%d\n", sym, err, len(bars))
			continue
		}
		entries := GenerateEMACrossEntries(sym, bars, cfg.EMAFast, cfg.EMASlow, cfg.CooldownBars, 1000.0)
		loaded := PrepareEntriesFromBars(entries, bars)
		res.PerSymbol[sym] = len(loaded)
		res.TotalEntries += len(loaded)
		merged = append(merged, loaded...)
		fmt.Printf("  %s: %d bars, %d entries\n", sym, len(bars), len(loaded))
	}

	if res.TotalEntries == 0 {
		return res, fmt.Errorf("no entries generated")
	}

	res.Baseline = RunParams(ClaudeBaselineParams(), merged)
	res.Sweep = Sweep(grid, merged)
	return res, nil
}

// PrepareRobustPortfolioEntries fetches long-period history per symbol, generates
// EMA-cross mechanical entries, and returns them as a merged loadedEntry slice
// suitable for RunPortfolioSim. Unlike RunRobust (which replays each entry
// independently), this preserves the real entry/exit timestamps so the portfolio
// simulator can model positions that COEXIST in time — the precondition for
// testing the portfolio giveback guard over months of data.
func PrepareRobustPortfolioEntries(cfg RobustConfig) ([]loadedEntry, map[string]int, error) {
	if cfg.Timeframe == "" {
		cfg.Timeframe = "1h"
	}
	if cfg.EMAFast <= 0 {
		cfg.EMAFast = 20
	}
	if cfg.EMASlow <= 0 {
		cfg.EMASlow = 50
	}
	if cfg.CooldownBars <= 0 {
		cfg.CooldownBars = 12
	}
	if cfg.Months <= 0 {
		cfg.Months = 6
	}

	end := time.Now()
	start := end.AddDate(0, -cfg.Months, 0)

	perSymbol := map[string]int{}
	var merged []loadedEntry
	for _, sym := range cfg.Symbols {
		if sym == "" {
			continue
		}
		bars, err := market.GetKlinesRangeOKX(sym, cfg.Timeframe, start, end)
		if err != nil || len(bars) < 250 {
			fmt.Printf("  skip %s: err=%v bars=%d\n", sym, err, len(bars))
			continue
		}
		var entries []Entry
		if cfg.Signal == "breakout" {
			look := cfg.BreakoutLook
			if look <= 0 {
				look = 20
			}
			entries = GenerateBreakoutEntries(sym, bars, look, cfg.CooldownBars, 1000.0)
		} else {
			entries = GenerateEMACrossEntries(sym, bars, cfg.EMAFast, cfg.EMASlow, cfg.CooldownBars, 1000.0)
		}
		loaded := PrepareEntriesFromBars(entries, bars)
		perSymbol[sym] = len(loaded)
		merged = append(merged, loaded...)
		fmt.Printf("  %s: %d bars, %d entries\n", sym, len(bars), len(loaded))
	}
	if len(merged) == 0 {
		return nil, perSymbol, fmt.Errorf("no entries generated")
	}
	return merged, perSymbol, nil
}

// SweepGuardsRobust prepares long-period mechanical entries and runs the guard
// sweep over them. Returns baseline, ranked rows, per-symbol counts, error.
func SweepGuardsRobust(cfg RobustConfig, grid []GuardParams) (SimResult, []GuardSweepRow, map[string]int, error) {
	merged, per, err := PrepareRobustPortfolioEntries(cfg)
	if err != nil {
		return SimResult{}, nil, per, err
	}
	base, rows := SweepGuards(grid, merged)
	return base, rows, per, nil
}

// SweepGuardsFromEntries fetches OKX bars per entry then runs the guard sweep.
// Returns baseline, ranked rows, prepared count, skipped count.
func SweepGuardsFromEntries(entries []Entry, tf string, grid []GuardParams) (SimResult, []GuardSweepRow, int, int) {
	loaded, skipped := PrepareEntries(entries, tf, OKXBars)
	if len(loaded) == 0 {
		return SimResult{}, nil, 0, skipped
	}
	base, rows := SweepGuards(grid, loaded)
	return base, rows, len(loaded), skipped
}
