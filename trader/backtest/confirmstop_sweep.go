package backtest

import (
	"fmt"
	"sort"
	"strings"
)

// confirmstop_sweep.go tests the proposed FIXED close-confirm adverse-excursion stop:
// cut a trade when a bar CLOSES beyond N×ATR from entry, active even while underwater
// (unlike the live trail that only ratchets after profit). The wide backstop resting
// stop is kept for intrabar wicks. This isolates "what N×ATR close stop minimizes the
// deep-MAE drawdown without clipping normal-volatility winners" — the answer to the
// reasonable-stop question the MAE buckets pointed at (loss cliff beyond 2.0 ATR).

// ConfirmStopSweep builds variants over a grid of fixed close-confirm ATR stops,
// keeping every other live field constant. Baseline (swing-derived confirm) included.
func ConfirmStopSweep(base ProtectionParams, stops []float64) []ConfigVariant {
	vs := []ConfigVariant{{Name: "LIVE(swing-confirm)", P: clone(base)}}
	if !base.RangeSLEnabled {
		return vs
	}
	for _, s := range stops {
		v := clone(base)
		v.RangeSLCloseConfirm = true
		v.ConfirmStopATR = s
		vs = append(vs, ConfigVariant{Name: fmt.Sprintf("confirm-%.1fATR", s), P: v})
	}
	return vs
}

// FormatConfirmStopSweep renders the fixed close-confirm stop sweep sorted by PnL.
func FormatConfirmStopSweep(trader string, base ProtectionParams, loaded []loadedEntry, stops []float64) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("==== CLOSE-CONFIRM ADVERSE-EXCURSION STOP SWEEP: %s (n=%d) ====\n", trader, len(loaded)))
	if !base.RangeSLEnabled {
		sb.WriteString("  (baseline is not structural RangeSL — nothing to sweep)\n")
		return sb.String()
	}
	variants := ConfirmStopSweep(base, stops)
	type row struct {
		name   string
		stop   float64
		r      PortfolioResult
		isLive bool
	}
	rows := make([]row, 0, len(variants))
	for _, v := range variants {
		r := RunParams(v.P, loaded)
		rows = append(rows, row{name: v.Name, stop: v.P.ConfirmStopATR, r: r, isLive: strings.HasPrefix(v.Name, "LIVE")})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].r.TotalPnL > rows[j].r.TotalPnL })
	var liveP float64
	for _, x := range rows {
		if x.isLive {
			liveP = x.r.TotalPnL
		}
	}
	sb.WriteString(fmt.Sprintf("  %-20s %-8s | %-10s %-6s %-6s %-10s %-8s\n",
		"variant", "stopATR", "TotalPnL", "Win%", "PF", "MaxDD", "Trades"))
	for _, x := range rows {
		flag := ""
		if x.isLive {
			flag = " <-LIVE"
		}
		sb.WriteString(fmt.Sprintf("  %-20s %-8.1f | %-+10.2f %-6.1f %-6.2f %-10.2f %-8d Δ%+.2f%s\n",
			x.name, x.stop, x.r.TotalPnL, x.r.WinRatePct, x.r.ProfitFactor, x.r.MaxDrawdown, x.r.Trades, x.r.TotalPnL-liveP, flag))
	}
	return sb.String()
}
