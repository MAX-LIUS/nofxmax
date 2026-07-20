package backtest

import (
	"fmt"
	"strings"

	"nofx/market"
)

// StructuralBranchStats counts, per entry, which branch of the live structural-SL
// computation (structuralSLPercent) the entry would take under params p:
//
//	NoEdge     — no pre-entry structure on the correct side (entered at/through it);
//	             live falls back to the flat wide stop.
//	Floor      — nearest structure is CLOSER than FloorATRMul; stop raised to floor.
//	Structural — nearest structure sits within [floor, backstop]; the normal path
//	             (the stop IS the clamped structural distance).
//	Fallback   — nearest structure is BEYOND BackstopATRMul (no near structure); the
//	             stop degrades to FallbackATRMul, RR-capped by FallbackRRCapRatio×TP.
//	             THIS is the only branch where the 0.8 RR cap / 3.0 ATR fallback act.
//
// This makes concrete how often the FallbackRRCapRatio (0.8) even participates.
type StructuralBranchStats struct {
	Total      int
	NoEdge     int
	Floor      int
	Structural int
	Fallback   int
}

// ComputeStructuralBranchStats replays the boundary computation over loaded entries.
func ComputeStructuralBranchStats(p ProtectionParams, loaded []loadedEntry) StructuralBranchStats {
	var s StructuralBranchStats
	for _, le := range loaded {
		s.Total++
		e := le.entry
		isLong := strings.EqualFold(e.Side, "long")
		atr := entryATR(le.bars, le.entryIdx)
		if atr <= 0 {
			s.NoEdge++
			continue
		}
		boundary, ok := rangeStructuralBoundary(p, e, le.bars, le.entryIdx, isLong)
		if !ok || boundary <= 0 {
			s.NoEdge++
			continue
		}
		dist := e.EntryPrice - boundary
		if dist < 0 {
			dist = -dist
		}
		mult := dist / atr
		floor := p.RangeSLFloorATR
		backstop := p.RangeSLBackstopATR
		switch {
		case floor > 0 && mult < floor:
			s.Floor++
		case backstop > 0 && mult > backstop:
			s.Fallback++
		default:
			s.Structural++
		}
	}
	return s
}

// entryATR recomputes the entry-time ATR the same way ReplayEntry does (Wilder ATR
// over atrLookback preceding bars, no look-ahead).
func entryATR(bars []market.Kline, entryIdx int) float64 {
	if entryIdx < atrLookback {
		return 0
	}
	h, l, c := sliceOHLC(bars[:entryIdx], entryIdx-1)
	return wilderATR(h, l, c, atrLookback)
}

// FormatStructuralBranchStats renders the branch distribution as one line.
func FormatStructuralBranchStats(s StructuralBranchStats) string {
	pct := func(n int) float64 {
		if s.Total == 0 {
			return 0
		}
		return float64(n) / float64(s.Total) * 100
	}
	return fmt.Sprintf(
		"structural-SL branch mix (n=%d): structural=%d(%.1f%%) floor=%d(%.1f%%) fallback=%d(%.1f%%) no-edge=%d(%.1f%%)  ← 0.8RR only acts in 'fallback'",
		s.Total,
		s.Structural, pct(s.Structural),
		s.Floor, pct(s.Floor),
		s.Fallback, pct(s.Fallback),
		s.NoEdge, pct(s.NoEdge),
	)
}

// RRCapStat holds the distribution of the ATR-multiple that the fallback RR cap
// (ratio × TP) would impose, per entry, under a given TP anchor. It answers "is
// 0.8×TP tighter than the FloorATRMul?" — i.e. does the RR cap ever actually bind,
// or is it always swallowed by the floor?
type RRCapStat struct {
	Anchor        string // "max_tp" or "first_target"
	Ratio         float64
	N             int     // entries with a usable anchor TP
	BelowFloor    int     // 0.8×TP/atr% < floor → cap is masked by the floor (never binds)
	BindsInRange  int     // floor <= capMult <= backstop → cap would set the stop
	AboveBackstop int     // capMult > backstop → cap looser than backstop (irrelevant)
	MedianCapMult float64 // median ATR-multiple the cap yields
	MedianTPpct   float64 // median anchor TP move (% of entry)
}

// ComputeRRCapStat measures, for every entry, the ATR-multiple ratio×TP/atr% and
// buckets it against [floor, backstop]. anchor selects the TP reference.
func ComputeRRCapStat(p ProtectionParams, loaded []loadedEntry, anchor string, ratio float64) RRCapStat {
	s := RRCapStat{Anchor: anchor, Ratio: ratio}
	floor := p.RangeSLFloorATR
	backstop := p.RangeSLBackstopATR
	var capMults, tpPcts []float64
	for _, le := range loaded {
		e := le.entry
		atr := entryATR(le.bars, le.entryIdx)
		if atr <= 0 || e.EntryPrice <= 0 {
			continue
		}
		var tpPct float64
		if anchor == "first_target" {
			if e.Structural == nil || e.Structural.FirstTargetPrice <= 0 {
				continue // no AI first_target → can't test this anchor for this entry
			}
			tpPct = (e.Structural.FirstTargetPrice - e.EntryPrice) / e.EntryPrice * 100
			if tpPct < 0 {
				tpPct = -tpPct
			}
		} else {
			tpPct = p.maxTPTargetPct(atr, e.EntryPrice)
		}
		if tpPct <= 0 {
			continue
		}
		atrPct := atr / e.EntryPrice * 100
		if atrPct <= 0 {
			continue
		}
		capMult := ratio * tpPct / atrPct
		s.N++
		capMults = append(capMults, capMult)
		tpPcts = append(tpPcts, tpPct)
		switch {
		case floor > 0 && capMult < floor:
			s.BelowFloor++
		case backstop > 0 && capMult > backstop:
			s.AboveBackstop++
		default:
			s.BindsInRange++
		}
	}
	s.MedianCapMult = median(capMults)
	s.MedianTPpct = median(tpPcts)
	return s
}

func median(xs []float64) float64 {
	n := len(xs)
	if n == 0 {
		return 0
	}
	// simple insertion sort (small n) to avoid importing sort here
	for i := 1; i < n; i++ {
		v := xs[i]
		j := i - 1
		for j >= 0 && xs[j] > v {
			xs[j+1] = xs[j]
			j--
		}
		xs[j+1] = v
	}
	if n%2 == 1 {
		return xs[n/2]
	}
	return (xs[n/2-1] + xs[n/2]) / 2
}

// FormatRRCapStat renders one RR-cap distribution line.
func FormatRRCapStat(s RRCapStat) string {
	pct := func(n int) float64 {
		if s.N == 0 {
			return 0
		}
		return float64(n) / float64(s.N) * 100
	}
	return fmt.Sprintf(
		"RRcap ratio=%.2f anchor=%-12s n=%d | medianTP=%.2f%% → medianCapMult=%.2fxATR | belowFloor(masked)=%d(%.0f%%) binds=%d(%.0f%%) aboveBackstop=%d(%.0f%%)",
		s.Ratio, s.Anchor, s.N, s.MedianTPpct, s.MedianCapMult,
		s.BelowFloor, pct(s.BelowFloor), s.BindsInRange, pct(s.BindsInRange), s.AboveBackstop, pct(s.AboveBackstop),
	)
}
