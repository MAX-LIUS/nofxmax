package backtest

import "sort"

// tpLevel is one take-profit rung consumed by the replay loop.
type tpLevel struct {
	price float64
	frac  float64
	fired bool
}

// collapseTPs reshapes a full TP ladder into the K tiers that survive an exchange
// min-lot collapse, closing 100% across those K tiers (no runner — the live collapse
// only fires for positions too small to hold the configured ladder). Two anchors:
//
//	"nearest"  — keep the K tiers CLOSEST to entry (live behavior). K=1 → the +1.1×ATR
//	             tier: high fill rate, tiny profit.
//	"aitarget" — re-anchor to the AI first_target pulled toward entry by CollapseTolPct.
//	             K=1 → single TP at first_target∓tol (captures the real objective). K>=2 →
//	             the farthest tier at first_target∓tol, the rest spaced evenly between
//	             entry and that target (near tiers fill early, far tier captures target).
//	             Falls back to "nearest" when the entry carries no usable first_target.
func collapseTPs(tps []tpLevel, p ProtectionParams, e Entry, atr float64, isLong bool) []tpLevel {
	k := p.CollapseK
	if k < 1 {
		k = 1
	}
	if k >= len(tps) {
		return tps // nothing to collapse; ladder already fits
	}

	// Sort by distance from entry (nearest first).
	sorted := append([]tpLevel(nil), tps...)
	sort.Slice(sorted, func(i, j int) bool {
		di := absF(sorted[i].price - e.EntryPrice)
		dj := absF(sorted[j].price - e.EntryPrice)
		return di < dj
	})

	// Resolve the AI target price for the aitarget anchor.
	target := 0.0
	if p.CollapseAnchor == "aitarget" && e.Structural != nil && e.Structural.FirstTargetPrice > 0 {
		ft := e.Structural.FirstTargetPrice
		// Sanity: target must be on the favorable side of entry.
		if (isLong && ft > e.EntryPrice) || (!isLong && ft < e.EntryPrice) {
			target = ft
		}
	}

	// aitarget with a usable target: build K tiers anchored to first_target∓tol.
	if target > 0 {
		tol := p.CollapseTolPct / 100.0 * e.EntryPrice
		var anchor float64
		if isLong {
			anchor = target - tol // pull toward entry so it fills easier
			if anchor <= e.EntryPrice {
				anchor = target
			}
		} else {
			anchor = target + tol
			if anchor >= e.EntryPrice {
				anchor = target
			}
		}
		out := make([]tpLevel, 0, k)
		if k == 1 {
			out = append(out, tpLevel{price: anchor, frac: 1.0})
			return out
		}
		// K>=2: farthest tier at the anchor, the rest evenly between entry and anchor.
		frac := 1.0 / float64(k)
		for i := 1; i <= k; i++ {
			// i=1 nearest … i=k at the anchor.
			ratio := float64(i) / float64(k)
			price := e.EntryPrice + (anchor-e.EntryPrice)*ratio
			out = append(out, tpLevel{price: price, frac: frac})
		}
		return out
	}

	// nearest anchor (or aitarget fallback): keep the K nearest tiers, close 100% total.
	out := append([]tpLevel(nil), sorted[:k]...)
	var sum float64
	for _, t := range out {
		sum += t.frac
	}
	if sum > 0 {
		for i := range out {
			out[i].frac = out[i].frac / sum // renormalize to 100%
		}
	} else {
		f := 1.0 / float64(k)
		for i := range out {
			out[i].frac = f
		}
	}
	return out
}

func absF(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// CompareCollapseAnchors evaluates, on the min-lot-collapse subset (entries that
// carry an AI first_target so the anchor choice actually differs), how the two
// collapse anchors (nearest vs aitarget) perform at K=1 and K=2, against the full
// (uncollapsed) ladder as reference. Answers "when a position is too small to hold
// the ladder, is it better to take the tiny nearest TP or re-anchor to first_target?"
func CompareCollapseAnchors(loaded []loadedEntry, base ProtectionParams, tolPct float64) ([]StructCompareRow, int) {
	// Filter to entries with a usable first_target (where the anchors diverge).
	var subset []loadedEntry
	for _, le := range loaded {
		if le.entry.Structural != nil && le.entry.Structural.FirstTargetPrice > 0 {
			subset = append(subset, le)
		}
	}
	mk := func(name string, k int, anchor string) StructCompareRow {
		p := base
		p.CollapseK = k
		p.CollapseAnchor = anchor
		p.CollapseTolPct = tolPct
		results := make([]TradeResult, 0, len(subset))
		for _, le := range subset {
			results = append(results, ReplayEntry(p, le.entry, le.bars, le.entryIdx))
		}
		pr := Aggregate(results)
		row := StructCompareRow{
			Name: name, Trades: pr.Trades, TotalPnL: pr.TotalPnL,
			WinRatePct: pr.WinRatePct, ProfitFactor: pr.ProfitFactor,
			MaxDrawdown: pr.MaxDrawdown, AvgReturnPct: pr.AvgReturnPct,
		}
		stopOuts, winN := 0, 0
		var winSum, lossSum float64
		lossN := 0
		for _, r := range results {
			for _, cr := range r.CloseReasons {
				if cr == "stop_loss" {
					stopOuts++
					break
				}
			}
			if r.RealizedPnL >= 0 {
				winN++
				winSum += r.ReturnPct
			} else {
				lossN++
				lossSum += r.ReturnPct
			}
		}
		if pr.Trades > 0 {
			row.StopOutRate = float64(stopOuts) / float64(pr.Trades) * 100
		}
		if winN > 0 {
			row.AvgWinCapture = winSum / float64(winN)
		}
		if lossN > 0 {
			row.AvgLossSize = lossSum / float64(lossN)
		}
		return row
	}
	rows := []StructCompareRow{
		mk("full-ladder(ref)", 0, ""),
		mk("K1-nearest", 1, "nearest"),
		mk("K1-aitarget", 1, "aitarget"),
		mk("K2-nearest", 2, "nearest"),
		mk("K2-aitarget", 2, "aitarget"),
	}
	return rows, len(subset)
}
