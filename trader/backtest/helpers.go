package backtest

import "nofx/market"

// priceAtDistance returns entry adjusted by distPct%. `favorable=true` moves in
// the profitable direction for the side; false moves adverse.
func priceAtDistance(entry, distPct float64, isLong, favorable bool) float64 {
	move := distPct / 100.0
	up := (isLong && favorable) || (!isLong && !favorable)
	if up {
		return entry * (1 + move)
	}
	return entry * (1 - move)
}

// cumulativeBERatio sums BE close ratios from tier 0..tier (inclusive), /100,
// capped at 1.0.
func cumulativeBERatio(legs []BELeg, tier int) float64 {
	if tier < 0 {
		return 0
	}
	cum := 0.0
	for i := 0; i <= tier && i < len(legs); i++ {
		cum += legs[i].CloseRatioPct
	}
	if cum > 100 {
		cum = 100
	}
	return cum / 100.0
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// lastIdxBefore returns the largest bar index whose OpenTime <= exitTime,
// searching from entryIdx forward. If exitTime<=0, returns the last index.
func lastIdxBefore(bars []market.Kline, exitTime int64, entryIdx int) int {
	if exitTime <= 0 {
		return len(bars) - 1
	}
	idx := entryIdx
	for i := entryIdx; i < len(bars); i++ {
		if bars[i].OpenTime <= exitTime {
			idx = i
		} else {
			break
		}
	}
	return idx
}
