package backtest

import (
	"fmt"
	"sort"
)

// NamedParams is a pre-specified protection config (no data fitting), so its
// full-sample performance is already a valid generalization estimate — this is
// the robust alternative to greedy in-sample optimisation, which overfits.
type NamedParams struct {
	Name string
	P    ProtectionParams
}

// UniversalCandidates is a curated set of sensible, regime-robust protection
// configs to rank on the broad multi-coin sample. They span: the live percent
// baseline, ATR variants from tight→wide, single vs two-tier TP, and a
// "chop-defensive" config (earlier first TP, larger first close ratio) to guard
// ranging-market profit. None are fit to the data, so the ranking generalizes.
func UniversalCandidates() []NamedParams {
	atr := func(sl, tp1, r1, tp2, r2, be, beoff, ber, ddArm, ddGb, ddR float64, twoTP bool) ProtectionParams {
		tp := []LadderLeg{{ATRMult: tp1, CloseRatioPct: r1}}
		if twoTP {
			tp = append(tp, LadderLeg{ATRMult: tp2, CloseRatioPct: r2})
		}
		var be1 []BELeg
		if be > 0 {
			be1 = []BELeg{{TriggerATR: be, OffsetATR: beoff, CloseRatioPct: ber}}
		}
		var dd []DDRule
		if ddArm > 0 {
			dd = []DDRule{{MinProfitATR: ddArm, MaxDrawdownPct: ddGb, CloseRatioPct: ddR}}
		}
		return ProtectionParams{Unit: UnitATRMult, StopLossATR: sl, TPLegs: tp, BELegs: be1, DDRules: dd}
	}
	return []NamedParams{
		{"percent-live", ClaudeBaselineParams()},
		{"atr-mid 2tp", atr(3.5, 1.5, 35, 5.0, 25, 1.5, 0.3, 50, 6.0, 40, 45, true)},
		{"atr-wide 2tp", atr(4.5, 2.0, 35, 7.0, 25, 2.0, 0.3, 50, 7.0, 40, 45, true)},
		{"atr-mid chop-defensive", atr(3.5, 1.0, 50, 5.0, 30, 1.0, 0.2, 50, 5.0, 40, 45, true)},
		{"atr-wide chop-defensive", atr(4.5, 1.5, 50, 7.0, 30, 1.5, 0.3, 50, 6.0, 40, 45, true)},
		{"atr-mid 1tp", atr(3.5, 2.0, 60, 0, 0, 1.5, 0.3, 50, 0, 0, 0, false)},
		{"atr-wide 1tp", atr(4.5, 2.5, 60, 0, 0, 2.0, 0.3, 50, 0, 0, 0, false)},
		{"atr-conservative wideSL", atr(5.0, 1.5, 40, 8.0, 30, 2.0, 0.3, 50, 6.0, 40, 45, true)},
	}
}

// CandRankRow is one candidate's full-sample (or test-set) outcome.
type CandRankRow struct {
	Name   string
	PnL    float64
	WinPct float64
	PF     float64
	MaxDD  float64
	Score  float64
}

// RankCandidates scores each curated candidate on the prepared sample and ranks
// by drawdown-aware score (PnL - lambda*MaxDD). Pre-specified params => valid
// generalization. trainFrac<1 splits chronologically and scores ONLY on the
// untouched tail (true OOS); trainFrac>=1 uses the full sample.
func RankCandidates(loaded []loadedEntry, lambda, trainFrac float64) []CandRankRow {
	score := loaded
	if trainFrac < 1.0 {
		sortLoadedByEntryTime(loaded)
		cut := int(float64(len(loaded)) * trainFrac)
		if cut < 0 {
			cut = 0
		}
		if cut >= len(loaded) {
			cut = len(loaded) - 1
		}
		score = loaded[cut:]
	}
	cands := UniversalCandidates()
	rows := make([]CandRankRow, 0, len(cands))
	for _, c := range cands {
		r := RunParams(c.P, score)
		row := CandRankRow{
			Name: c.Name, PnL: r.TotalPnL, WinPct: r.WinRatePct,
			PF: r.ProfitFactor, MaxDD: r.MaxDrawdown,
		}
		row.Score = row.PnL - lambda*row.MaxDD
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Score > rows[j].Score })
	return rows
}

// FormatCandRank renders the ranked candidates.
func FormatCandRank(rows []CandRankRow) string {
	s := fmt.Sprintf("%-26s | %10s %7s %6s %10s %10s\n", "candidate", "PnL", "Win%", "PF", "MaxDD", "score")
	for _, r := range rows {
		s += fmt.Sprintf("%-26s | %10.2f %7.1f %6.2f %10.2f %10.2f\n",
			r.Name, r.PnL, r.WinPct, r.PF, r.MaxDD, r.Score)
	}
	return s
}
