package backtest

import (
	"fmt"
	"sort"
	"strings"
)

// floor_backstop_sweep.go sweeps the structural stop band [floor, backstop] in 2D,
// specifically driving the backstop DOWN toward the floor to answer: how tight can
// the resting/backstop stop get before it clips winners and net PnL degrades? Each
// cell replays the full loaded set under the live baseline with only RangeSLFloorATR
// and RangeSLBackstopATR (and the flat StopLossATR fallback) changed, so the delta is
// attributable to the band. Only meaningful with a live structural (RangeSL) baseline.

// FloorBackstopSweep returns variants over a floor grid × backstop grid, keeping
// backstop >= floor. It also always includes the exact live baseline as reference.
func FloorBackstopSweep(base ProtectionParams, floors, backstops []float64) []ConfigVariant {
	vs := []ConfigVariant{{Name: fmt.Sprintf("LIVE(floor=%.1f,back=%.1f)", base.RangeSLFloorATR, base.RangeSLBackstopATR), P: clone(base)}}
	if !base.RangeSLEnabled {
		return vs
	}
	for _, f := range floors {
		for _, b := range backstops {
			if b < f {
				continue
			}
			v := clone(base)
			v.RangeSLFloorATR = f
			v.RangeSLBackstopATR = b
			v.StopLossATR = b // flat fallback tracks the backstop cap
			vs = append(vs, ConfigVariant{
				Name: fmt.Sprintf("f%.1f_b%.1f", f, b),
				P:    v,
			})
		}
	}
	return vs
}

// FormatFloorBackstopSweep renders the 2D sweep sorted by TotalPnL, with the live
// baseline row flagged, plus a compact "band width" column (backstop-floor) so the
// tightening trend is readable.
func FormatFloorBackstopSweep(trader string, base ProtectionParams, loaded []loadedEntry, floors, backstops []float64) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("==== FLOOR×BACKSTOP SWEEP: %s (n=%d) ====\n", trader, len(loaded)))
	if !base.RangeSLEnabled {
		sb.WriteString("  (baseline is not a structural RangeSL config — nothing to sweep)\n")
		return sb.String()
	}
	variants := FloorBackstopSweep(base, floors, backstops)
	type row struct {
		name              string
		floor, back, band float64
		r                 PortfolioResult
		isLive            bool
	}
	rows := make([]row, 0, len(variants))
	for _, v := range variants {
		r := RunParams(v.P, loaded)
		rows = append(rows, row{
			name:   v.Name,
			floor:  v.P.RangeSLFloorATR,
			back:   v.P.RangeSLBackstopATR,
			band:   v.P.RangeSLBackstopATR - v.P.RangeSLFloorATR,
			r:      r,
			isLive: strings.HasPrefix(v.Name, "LIVE"),
		})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].r.TotalPnL > rows[j].r.TotalPnL })

	sb.WriteString(fmt.Sprintf("  %-10s %-6s %-6s %-6s | %-10s %-6s %-6s %-10s %-8s\n",
		"variant", "floor", "back", "band", "TotalPnL", "Win%", "PF", "MaxDD", "Trades"))
	var liveP float64
	for _, x := range rows {
		if x.isLive {
			liveP = x.r.TotalPnL
		}
	}
	for _, x := range rows {
		flag := ""
		if x.isLive {
			flag = " <-LIVE"
		}
		d := x.r.TotalPnL - liveP
		sb.WriteString(fmt.Sprintf("  %-10s %-6.1f %-6.1f %-6.1f | %-+10.2f %-6.1f %-6.2f %-10.2f %-8d Δ%+.2f%s\n",
			x.name, x.floor, x.back, x.band, x.r.TotalPnL, x.r.WinRatePct, x.r.ProfitFactor, x.r.MaxDrawdown, x.r.Trades, d, flag))
	}
	return sb.String()
}
