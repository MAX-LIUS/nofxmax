package main

import (
	"time"

	"nofx/trader/backtest"
)

func msNow() int64 { return time.Now().UnixMilli() }

// l1StudyGrid isolates the question: should sub-3% profit/cost giveback be
// controlled? It sweeps ONLY the L1 min-peak arm threshold while holding the
// other L1 knobs fixed at the live values (gb40 cl50). Two families:
//
//	(a) L1-only (L2 off): pure effect of the arm threshold, isolated.
//	(b) L1+L2 (live L2 winner underneath): effect on top of the shipped guard.
//
// Thresholds span well below the live 3%: 0.5 / 1.0 / 1.5 / 2.0 / 3.0 / 5.0.
// Compared against the guard-OFF baseline the sweep already prints, this tells
// us whether protecting small (<3%) unrealized profit helps or just churns.
func l1StudyGrid() []backtest.GuardParams {
	var out []backtest.GuardParams
	thresholds := []float64{0.5, 1.0, 1.5, 2.0, 3.0, 5.0}

	// (a) L1 only — isolate the arm-threshold effect.
	for _, mp := range thresholds {
		out = append(out, backtest.GuardParams{
			Enabled:       true,
			L1Enabled:     true,
			L1GivebackPct: 40,
			L1MinPeakPct:  mp,
			L1ClosePct:    50,
		})
	}

	// (b) L1 (varying threshold) on top of the live L2 winner.
	for _, mp := range thresholds {
		out = append(out, backtest.GuardParams{
			Enabled:        true,
			L1Enabled:      true,
			L1GivebackPct:  40,
			L1MinPeakPct:   mp,
			L1ClosePct:     50,
			L2Enabled:      true,
			L2GivebackPct:  50,
			L2MinPeakQuote: 3,
			L2ClosePct:     50,
		})
	}

	return out
}

// guardGrid enumerates the giveback-guard parameter space to sweep. This is the
// REFINEMENT grid focused around the cross-validated winner (L2 gb50 mq3 cl50):
//   - fine L2 around the winner (gb 40-60, mq 2-4, cl 45-65)
//   - directional-concentration tightening with a HIGH base gb that only fires
//     early when the book is one-sided (the regime the user flagged)
//   - L1 per-symbol layer on top of the L2 winner
func guardGrid() []backtest.GuardParams {
	var out []backtest.GuardParams

	// Fine L2 around winner.
	for _, gb := range []float64{40, 45, 50, 55, 60} {
		for _, mq := range []float64{2, 3, 4} {
			for _, cl := range []float64{45, 50, 55, 65} {
				out = append(out, backtest.GuardParams{
					Enabled:        true,
					L2Enabled:      true,
					L2GivebackPct:  gb,
					L2MinPeakQuote: mq,
					L2ClosePct:     cl,
				})
			}
		}
	}

	// Concentration-tightening: high base gb (60) that normally waits, but when
	// the book is >= X% one-sided, the threshold tightens so it acts earlier on
	// correlated reversals specifically (the user's "most positions reverse" case).
	for _, conc := range []float64{0.6, 0.7, 0.8} {
		for _, mult := range []float64{0.5, 0.6, 0.7} {
			out = append(out, backtest.GuardParams{
				Enabled:          true,
				L2Enabled:        true,
				L2GivebackPct:    60,
				L2MinPeakQuote:   3,
				L2ClosePct:       50,
				ConcentrationPct: conc,
				ConcTightenMult:  mult,
			})
		}
	}

	// L1 per-symbol layer on top of the L2 winner (catch single-coin spikes the
	// portfolio guard misses).
	for _, l1gb := range []float64{40, 50} {
		for _, l1mp := range []float64{3, 5} {
			for _, l1cl := range []float64{33, 50} {
				out = append(out, backtest.GuardParams{
					Enabled:        true,
					L1Enabled:      true,
					L1GivebackPct:  l1gb,
					L1MinPeakPct:   l1mp,
					L1ClosePct:     l1cl,
					L2Enabled:      true,
					L2GivebackPct:  50,
					L2MinPeakQuote: 3,
					L2ClosePct:     50,
				})
			}
		}
	}

	// Trend-adaptive L2: close ratio chosen by entry-ADX of open winners.
	// Strong trend (ADX >= thresh) trims more; chop trims less. Sweep the
	// threshold and the trend/chop close pair to find the best adaptive split.
	for _, thr := range []float64{20, 25, 30} {
		for _, clTrend := range []float64{55, 65, 75} {
			for _, clChop := range []float64{30, 40, 50} {
				if clChop >= clTrend {
					continue // adaptive only meaningful when trend-close > chop-close
				}
				out = append(out, backtest.GuardParams{
					Enabled:           true,
					L2Enabled:         true,
					L2GivebackPct:     55,
					L2MinPeakQuote:    3,
					L2ClosePct:        50, // fallback (unused when AdaptiveClose)
					AdaptiveClose:     true,
					TrendADXThreshold: thr,
					L2ClosePctTrend:   clTrend,
					L2ClosePctChop:    clChop,
				})
			}
		}
	}

	return out
}
