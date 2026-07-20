package backtest

import (
	"fmt"
	"sort"
	"strings"
)

// reward_atr_gate.go quantifies the "target too close" problem: it buckets every
// entry by the AI's REWARD distance measured in ATR multiples (first_target vs
// entry / entryATR) and, in parallel, by the ENFORCED structural stop distance in
// ATR. It then reports realized PnL per bucket so we can read directly: do trades
// whose target sits < X ATR away actually lose money after the wide structural
// stop is applied? This is the evidence for a minimum-reward-ATR entry gate and a
// re-scoped [floor, backstop] band, independent of any replay assumptions (uses
// live RealizedPnL).
//
// It also simulates the effect of a reward-ATR floor gate: for a grid of minimum
// reward-ATR thresholds, sum the RealizedPnL of the entries that would be BLOCKED
// (target below threshold) vs KEPT, so the PnL delta of enforcing that floor is explicit.

type rewardATRRow struct {
	label      string
	n          int
	pnl        float64
	wins       int
	rrMedian   float64 // median reward-ATR in the bucket
}

// rewardATRForEntry returns (rewardATR, slATR, ok). rewardATR uses the AI
// first_target when present, else the nearest structural TP leg. slATR uses the
// AI structural SL anchor distance when present. Both normalized by entryATR.
func rewardATRForEntry(le loadedEntry) (rewardATR, slATR float64, ok bool) {
	atr := entryATR(le.bars, le.entryIdx)
	if atr <= 0 || le.entry.EntryPrice <= 0 || le.entry.Structural == nil {
		return 0, 0, false
	}
	e := le.entry.EntryPrice
	s := le.entry.Structural
	var target float64
	if s.FirstTargetPrice > 0 {
		target = s.FirstTargetPrice
	} else if len(s.TPLegs) > 0 {
		target = s.TPLegs[0].Price
	} else {
		return 0, 0, false
	}
	rewardATR = absF(target-e) / atr
	if s.SLPrice > 0 {
		slATR = absF(e-s.SLPrice) / atr
	}
	return rewardATR, slATR, true
}

// FormatRewardATRBuckets renders reward-ATR buckets with realized PnL, and a
// reward-ATR floor-gate simulation.
func FormatRewardATRBuckets(trader string, loaded []loadedEntry, floorATR, backstopATR float64) string {
	var sb strings.Builder
	type item struct {
		rewardATR, slATR, realATR, pnl float64
		win                            bool
	}
	items := make([]item, 0, len(loaded))
	for _, le := range loaded {
		rATR, slATR, ok := rewardATRForEntry(le)
		if !ok {
			continue
		}
		// realATR = the ENFORCED stop distance in ATR after clamp to [floor, backstop].
		realATR := slATR
		if realATR < floorATR {
			realATR = floorATR
		}
		if realATR > backstopATR {
			realATR = backstopATR
		}
		items = append(items, item{
			rewardATR: rATR, slATR: slATR, realATR: realATR,
			pnl: le.entry.RealizedPnL, win: le.entry.RealizedPnL >= 0,
		})
	}
	sb.WriteString(fmt.Sprintf("==== REWARD-ATR BUCKETS: %s (n=%d w/ structural target; floor=%.1f backstop=%.1f) ====\n",
		trader, len(items), floorATR, backstopATR))
	if len(items) == 0 {
		sb.WriteString("  (no entries carry a structural target)\n")
		return sb.String()
	}

	// Bucket by reward-ATR.
	edges := []float64{0, 1.0, 1.5, 2.0, 3.0, 1e9}
	names := []string{"<1.0", "1.0-1.5", "1.5-2.0", "2.0-3.0", ">=3.0"}
	rows := make([]rewardATRRow, len(names))
	buckRR := make([][]float64, len(names))
	for i := range rows {
		rows[i].label = names[i]
	}
	for _, it := range items {
		for b := 0; b < len(names); b++ {
			if it.rewardATR >= edges[b] && it.rewardATR < edges[b+1] {
				rows[b].n++
				rows[b].pnl += it.pnl
				if it.win {
					rows[b].wins++
				}
				buckRR[b] = append(buckRR[b], it.rewardATR)
				break
			}
		}
	}
	sb.WriteString(fmt.Sprintf("  %-9s %-5s %-10s %-7s %-10s\n", "rewardATR", "n", "realPnL", "win%", "medReward"))
	for b := range rows {
		r := rows[b]
		if r.n == 0 {
			sb.WriteString(fmt.Sprintf("  %-9s %-5d %-10s %-7s %-10s\n", r.label, 0, "-", "-", "-"))
			continue
		}
		med := medianF(buckRR[b])
		sb.WriteString(fmt.Sprintf("  %-9s %-5d %-+10.2f %-7.1f %-10.2f\n",
			r.label, r.n, r.pnl, 100*float64(r.wins)/float64(r.n), med))
	}

	// Effective-RR bucket: reward-ATR / enforced-stop-ATR (post-clamp).
	sb.WriteString("\n  -- effective RR = rewardATR / enforced-stop-ATR (post floor/backstop clamp) --\n")
	effEdges := []float64{0, 0.5, 0.8, 1.0, 1.5, 1e9}
	effNames := []string{"<0.5", "0.5-0.8", "0.8-1.0", "1.0-1.5", ">=1.5"}
	effRows := make([]rewardATRRow, len(effNames))
	for i := range effRows {
		effRows[i].label = effNames[i]
	}
	for _, it := range items {
		if it.realATR <= 0 {
			continue
		}
		eff := it.rewardATR / it.realATR
		for b := 0; b < len(effNames); b++ {
			if eff >= effEdges[b] && eff < effEdges[b+1] {
				effRows[b].n++
				effRows[b].pnl += it.pnl
				if it.win {
					effRows[b].wins++
				}
				break
			}
		}
	}
	sb.WriteString(fmt.Sprintf("  %-9s %-5s %-10s %-7s\n", "effRR", "n", "realPnL", "win%"))
	for b := range effRows {
		r := effRows[b]
		if r.n == 0 {
			sb.WriteString(fmt.Sprintf("  %-9s %-5d %-10s %-7s\n", r.label, 0, "-", "-"))
			continue
		}
		sb.WriteString(fmt.Sprintf("  %-9s %-5d %-+10.2f %-7.1f\n",
			r.label, r.n, r.pnl, 100*float64(r.wins)/float64(r.n)))
	}

	// Reward-ATR floor GATE simulation: block entries with rewardATR < threshold.
	sb.WriteString("\n  -- reward-ATR floor GATE (block target-too-close): PnL kept vs removed --\n")
	sb.WriteString(fmt.Sprintf("  %-8s %-6s %-11s %-6s | %-8s %-11s\n", "minRew", "nKept", "keptPnL", "kW%", "nBlock", "blockPnL"))
	var total float64
	for _, it := range items {
		total += it.pnl
	}
	for _, thr := range []float64{0.8, 1.0, 1.25, 1.5, 1.75, 2.0, 2.25, 2.5} {
		var keptPnL, blockPnL float64
		var nKept, nBlock, keptWins int
		for _, it := range items {
			if it.rewardATR < thr {
				blockPnL += it.pnl
				nBlock++
			} else {
				keptPnL += it.pnl
				nKept++
				if it.win {
					keptWins++
				}
			}
		}
		kw := 0.0
		if nKept > 0 {
			kw = 100 * float64(keptWins) / float64(nKept)
		}
		sb.WriteString(fmt.Sprintf("  %-8.2f %-6d %-+11.2f %-6.1f | %-8d %-+11.2f\n",
			thr, nKept, keptPnL, kw, nBlock, blockPnL))
	}
	sb.WriteString(fmt.Sprintf("  (total realized PnL over %d structural-target entries = %+.2f)\n", len(items), total))

	// COMBINED gate: block if rewardATR < minRew OR effRR < minEffRR. effRR uses the
	// ENFORCED (clamped) stop distance, i.e. the real SPCX failure mode.
	sb.WriteString("\n  -- COMBINED gate (block if rewardATR<minRew OR effRR<minEffRR) --\n")
	sb.WriteString(fmt.Sprintf("  %-8s %-9s %-6s %-11s %-6s | %-8s %-11s\n", "minRew", "minEffRR", "nKept", "keptPnL", "kW%", "nBlock", "blockPnL"))
	type combo struct{ rew, eff float64 }
	for _, c := range []combo{{1.0, 0.5}, {1.0, 0.8}, {1.25, 0.5}, {1.25, 0.8}} {
		var keptPnL, blockPnL float64
		var nKept, nBlock, keptWins int
		for _, it := range items {
			eff := 0.0
			if it.realATR > 0 {
				eff = it.rewardATR / it.realATR
			}
			if it.rewardATR < c.rew || eff < c.eff {
				blockPnL += it.pnl
				nBlock++
			} else {
				keptPnL += it.pnl
				nKept++
				if it.win {
					keptWins++
				}
			}
		}
		kw := 0.0
		if nKept > 0 {
			kw = 100 * float64(keptWins) / float64(nKept)
		}
		sb.WriteString(fmt.Sprintf("  %-8.2f %-9.2f %-6d %-+11.2f %-6.1f | %-8d %-+11.2f\n",
			c.rew, c.eff, nKept, keptPnL, kw, nBlock, blockPnL))
	}
	return sb.String()
}

func medianF(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cp := append([]float64(nil), xs...)
	sort.Float64s(cp)
	return cp[len(cp)/2]
}
