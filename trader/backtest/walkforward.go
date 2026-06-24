package backtest

import (
	"fmt"
	"time"
)

// WFRow is one guard config selected in-sample (IS) and re-evaluated
// out-of-sample (OOS), with its OOS rank among the full grid.
type WFRow struct {
	Guard    GuardParams
	IS       SimResult
	ISScore  float64
	OOS      SimResult
	OOSScore float64
	OOSRank  int
	OOSTotal int
}

// WalkForwardResult holds the IS/OOS split outcome.
type WalkForwardResult struct {
	CutoffMs    int64
	ISCount     int
	OOSCount    int
	ISBaseline  SimResult
	OOSBaseline SimResult
	Rows        []WFRow
}

// splitByEntryTime partitions loaded entries into (before, after) the cutoff by
// their EntryTime. The entry (decision point) determines the bucket; a position
// opened in-sample may close after the cutoff — that is correct walk-forward.
func splitByEntryTime(loaded []loadedEntry, cutoffMs int64) (is, oos []loadedEntry) {
	for _, le := range loaded {
		if le.entry.EntryTime < cutoffMs {
			is = append(is, le)
		} else {
			oos = append(oos, le)
		}
	}
	return
}

// __APPEND_MARKER__

// WalkForward runs anti-overfit validation: generates cfg.Months of mechanical
// entries, splits at isMonths (first isMonths = in-sample, remainder = OOS),
// sweeps the grid in-sample, takes the top topK IS configs, and re-evaluates
// each on OOS — reporting where each lands among the full grid ranked on OOS.
// A config that wins IS and stays near the top OOS is robust; one that craters
// OOS is overfit.
func WalkForward(cfg RobustConfig, grid []GuardParams, isMonths, topK int) (WalkForwardResult, error) {
	if cfg.Months <= 0 {
		cfg.Months = 12
	}
	if isMonths <= 0 || isMonths >= cfg.Months {
		isMonths = cfg.Months / 2
	}
	merged, _, err := PrepareRobustPortfolioEntries(cfg)
	if err != nil {
		return WalkForwardResult{}, err
	}
	end := time.Now()
	cutoff := end.AddDate(0, -(cfg.Months - isMonths), 0).UnixMilli()

	isE, oosE := splitByEntryTime(merged, cutoff)
	res := WalkForwardResult{CutoffMs: cutoff, ISCount: len(isE), OOSCount: len(oosE)}
	if len(isE) == 0 || len(oosE) == 0 {
		return res, nil
	}

	isBase, isRows := SweepGuards(grid, isE)
	res.ISBaseline = isBase
	oosBase, oosRows := SweepGuards(grid, oosE)
	res.OOSBaseline = oosBase

	rankByKey := map[string]int{}
	resByKey := map[string]SimResult{}
	scoreByKey := map[string]float64{}
	for i, r := range oosRows {
		k := guardKey(r.Guard)
		rankByKey[k] = i + 1
		resByKey[k] = r.Result
		scoreByKey[k] = r.Score
	}

	if topK > len(isRows) {
		topK = len(isRows)
	}
	for i := 0; i < topK; i++ {
		g := isRows[i].Guard
		k := guardKey(g)
		res.Rows = append(res.Rows, WFRow{
			Guard: g, IS: isRows[i].Result, ISScore: isRows[i].Score,
			OOS: resByKey[k], OOSScore: scoreByKey[k],
			OOSRank: rankByKey[k], OOSTotal: len(oosRows),
		})
	}
	return res, nil
}

// guardKey is a stable identity for a GuardParams (for cross-sweep matching).
func guardKey(g GuardParams) string {
	return fmt.Sprintf("L1:%v|%.1f,%.1f,%.1f|L2:%v|%.1f,%.1f,%.1f,%.2f,%.2f",
		g.L1Enabled, g.L1GivebackPct, g.L1MinPeakPct, g.L1ClosePct,
		g.L2Enabled, g.L2GivebackPct, g.L2MinPeakQuote, g.L2ClosePct,
		g.ConcentrationPct, g.ConcTightenMult)
}
