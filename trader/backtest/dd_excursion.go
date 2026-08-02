package backtest

import "sort"

// Package-level note: this file answers a question that must be counted on the
// observed equity curve, not inferred from a simulation's firing counts:
//
//   「历史 pnl 曲线从峰值下跌 4% 的情况有几次？每次我都在 2% 截断熔断，
//     怎么会不是盈利？」
//
// The reasoning is sound as far as it goes: if a drawdown is going to reach 4%
// (or 10%), then cutting at 2% is strictly better. The gap is that a 2% rule
// cannot see which excursion it is in. It fires on EVERY excursion that reaches
// 2%, including the ones that would have stopped at 2.1% and recovered. So the
// decisive number is not "how many drawdowns exceeded 4%" but the CONDITIONAL:
// of all excursions that reached 2%, what fraction went on to reach 4%?
//
// That is what DDExcursions measures.

// DDExcursion is one peak-to-trough episode on the equity curve: the curve makes
// a new high, declines, and eventually either recovers to that high or the series
// ends.
type DDExcursion struct {
	PeakIdx   int
	PeakEquit float64
	TroughIdx int
	MinEquity float64
	// MaxDDPct is the deepest drawdown reached in this episode, in percent of the
	// peak equity (raw equity, no leverage adjustment).
	MaxDDPct float64
	// Recovered reports whether the curve returned to the peak before the series
	// ended. An unrecovered final episode is not evidence about recovery odds.
	Recovered bool
	// EndIdx is where the episode closed (recovery point, or the last sample).
	EndIdx int
}

// DDExcursions decomposes an equity curve into peak-to-recovery episodes.
//
// Definition choice, stated because it drives every number downstream: a new
// episode starts only when the curve sets a NEW high. Successive dips that never
// regain the prior high belong to the SAME episode. Counting each dip separately
// would multiply-count one decline and inflate "how often does a 2% drawdown
// happen" — precisely the direction that would flatter a breaker.
func DDExcursions(curve []float64) []DDExcursion {
	var out []DDExcursion
	if len(curve) < 2 {
		return out
	}
	peak := curve[0]
	peakIdx := 0
	inEpisode := false
	var cur DDExcursion
	for i := 1; i < len(curve); i++ {
		v := curve[i]
		if v >= peak {
			if inEpisode {
				cur.Recovered = true
				cur.EndIdx = i
				out = append(out, cur)
				inEpisode = false
			}
			peak, peakIdx = v, i
			continue
		}
		// below the running peak
		ddPct := 0.0
		if peak > 0 {
			ddPct = (peak - v) / peak * 100
		}
		if !inEpisode {
			inEpisode = true
			cur = DDExcursion{
				PeakIdx: peakIdx, PeakEquit: peak,
				TroughIdx: i, MinEquity: v, MaxDDPct: ddPct,
			}
			continue
		}
		if v < cur.MinEquity {
			cur.MinEquity, cur.TroughIdx, cur.MaxDDPct = v, i, ddPct
		}
	}
	if inEpisode {
		cur.EndIdx = len(curve) - 1
		out = append(out, cur) // Recovered stays false
	}
	return out
}

// DDLevelStat is the answer for one candidate breaker threshold m%.
type DDLevelStat struct {
	// LevelPct is the threshold m% being evaluated.
	LevelPct float64
	// Reached is how many excursions got at least this deep. This is the number of
	// times a breaker set at m% WOULD HAVE FIRED.
	Reached int
	// WentTwice / WentTriple: of those, how many went on to 2x / 3x this depth.
	WentTwice  int
	WentTriple int
	// Recovered is how many of the Reached excursions returned to their peak.
	// Each of these is a firing that cut a decline which repaired itself.
	Recovered int
	// MedianDeeperPct is the median final depth of the excursions that reached this
	// level — i.e. how bad it typically got, given that it got this far.
	MedianDeeperPct float64
}

// DDLevelStats answers the user's question at every candidate threshold at once:
// how many times each depth occurred, and CONDITIONAL on reaching it, how often
// it kept going versus repaired itself.
//
// Read it as the breaker's hit-rate table. At threshold m%, `Reached` firings buy
// protection against the `WentTwice` cases and pay for the `Recovered` cases.
func DDLevelStats(curve []float64, levels []float64) []DDLevelStat {
	exs := DDExcursions(curve)
	out := make([]DDLevelStat, 0, len(levels))
	for _, lv := range levels {
		st := DDLevelStat{LevelPct: lv}
		var depths []float64
		for _, e := range exs {
			if e.MaxDDPct+1e-12 < lv {
				continue
			}
			st.Reached++
			depths = append(depths, e.MaxDDPct)
			if e.MaxDDPct >= 2*lv {
				st.WentTwice++
			}
			if e.MaxDDPct >= 3*lv {
				st.WentTriple++
			}
			if e.Recovered {
				st.Recovered++
			}
		}
		if len(depths) > 0 {
			sort.Float64s(depths)
			st.MedianDeeperPct = depths[len(depths)/2]
		}
		out = append(out, st)
	}
	return out
}

// EpisodeLedger is the per-episode account of what a breaker set at LevelPct
// actually bought and paid in one drawdown episode.
type EpisodeLedger struct {
	Idx int
	// PeakEquity is the equity at the high that started the episode.
	PeakEquity float64
	// CutEquity is the equity when the drawdown first reached LevelPct — the price
	// the breaker realises when it fires.
	CutEquity float64
	// TroughEquity is the deepest equity reached in the episode: what riding it out
	// costs at the worst moment.
	TroughEquity float64
	// EndEquity is the equity when the episode closed (recovery to peak, or series
	// end).
	EndEquity float64
	// MaxDDPct is the deepest drawdown of the episode.
	MaxDDPct  float64
	Recovered bool
	// Saved is what cutting avoided versus the trough: CutEquity - TroughEquity.
	// Positive means the cut was above the worst point.
	Saved float64
	// Forgone is what cutting gave up versus how the episode actually ended:
	// EndEquity - CutEquity. Positive means the curve ended ABOVE the cut, so the
	// breaker sold into a recovery and left that much on the table.
	Forgone   float64
	Bars      int
	BarsToCut int
}

// EpisodeLedgers builds the per-episode account at one threshold. This is the
// decisive view: it separates "cutting avoided a deeper trough" (Saved) from
// "cutting sold into a recovery" (Forgone). A breaker is only worth having if
// Saved systematically exceeds Forgone — and note Saved is measured against the
// TROUGH, which nobody is forced to sell at, whereas Forgone is measured against
// where the episode actually ENDED, which is what holding really delivers.
func EpisodeLedgers(curve []float64, levelPct float64) []EpisodeLedger {
	exs := DDExcursions(curve)
	var out []EpisodeLedger
	for i, e := range exs {
		if e.MaxDDPct+1e-12 < levelPct {
			continue
		}
		// Find the first bar in this episode where the drawdown reached levelPct.
		cutIdx, cutEq := -1, 0.0
		for j := e.PeakIdx + 1; j <= e.EndIdx && j < len(curve); j++ {
			if e.PeakEquit > 0 && (e.PeakEquit-curve[j])/e.PeakEquit*100 >= levelPct {
				cutIdx, cutEq = j, curve[j]
				break
			}
		}
		if cutIdx < 0 {
			continue
		}
		endEq := curve[e.EndIdx]
		out = append(out, EpisodeLedger{
			Idx: i + 1, PeakEquity: e.PeakEquit, CutEquity: cutEq,
			TroughEquity: e.MinEquity, EndEquity: endEq,
			MaxDDPct: e.MaxDDPct, Recovered: e.Recovered,
			Saved:   cutEq - e.MinEquity,
			Forgone: endEq - cutEq,
			Bars:    e.EndIdx - e.PeakIdx, BarsToCut: cutIdx - e.PeakIdx,
		})
	}
	return out
}
