package api

import (
	"nofx/store"
)

// downsampleSnapshots reduces an equity series to at most maxPoints samples while
// keeping every local extreme that defines the curve's shape.
//
// Naive stride sampling (take every Nth row) is wrong for this chart. The curve is
// used to read peaks and drawdowns, and a stride can land either side of a spike
// and erase it, so the same series can show a different maximum drawdown at
// different zoom levels. Instead the range is split into buckets and BOTH the
// minimum and maximum equity of each bucket are emitted, in chronological order.
// That bounds the output at 2 points per bucket and guarantees the highest high and
// lowest low of every bucket survive.
//
// The first and last samples are always kept so the visible range endpoints and the
// latest value stay exact.
func downsampleSnapshots(snaps []*store.EquitySnapshot, maxPoints int) []*store.EquitySnapshot {
	if maxPoints < 4 {
		maxPoints = 4
	}
	if len(snaps) <= maxPoints {
		return snaps
	}

	buckets := maxPoints / 2
	out := make([]*store.EquitySnapshot, 0, maxPoints+2)
	n := len(snaps)
	lastIdx := -1

	for b := 0; b < buckets; b++ {
		start := b * n / buckets
		end := (b + 1) * n / buckets
		if start >= n {
			break
		}
		if end > n {
			end = n
		}
		if start >= end {
			continue
		}

		minIdx, maxIdx := start, start
		for i := start + 1; i < end; i++ {
			if snaps[i].TotalEquity < snaps[minIdx].TotalEquity {
				minIdx = i
			}
			if snaps[i].TotalEquity > snaps[maxIdx].TotalEquity {
				maxIdx = i
			}
		}

		// Emit in time order, not min-then-max, or the line would zigzag backwards.
		first, second := minIdx, maxIdx
		if second < first {
			first, second = second, first
		}
		for _, idx := range [2]int{first, second} {
			if idx > lastIdx {
				out = append(out, snaps[idx])
				lastIdx = idx
			}
		}
	}

	// Pin the true endpoints: a bucket extreme is rarely the very first or last row,
	// and the last row is the freshest equity the leaderboard is showing.
	if len(out) == 0 || out[0] != snaps[0] {
		out = append([]*store.EquitySnapshot{snaps[0]}, out...)
	}
	if out[len(out)-1] != snaps[n-1] {
		out = append(out, snaps[n-1])
	}
	return out
}
