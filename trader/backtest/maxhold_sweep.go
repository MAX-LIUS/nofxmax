package backtest

import (
	"fmt"
	"strings"
)

// maxhold_sweep.go tests changes to the max-hold time exit: the live rule force-closes
// a position at bar close once held ≥ MaxHoldHours UNLESS close PnL% ≥ the profit-exempt
// threshold (a profitable-runner exemption). This sweep isolates: (a) live, (b) drop the
// profit exemption (everything closes at MaxHoldHours), (c) disable max-hold entirely,
// plus a few alternative hours. Requires -liveconfig + -proxy so CloseProxy is active.
func FormatMaxHoldSweep(trader string, base ProtectionParams, loaded []loadedEntry) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("==== MAX-HOLD SWEEP: %s (n=%d) ====\n", trader, len(loaded)))
	if !base.CloseProxy.Enabled {
		sb.WriteString("  (CloseProxy disabled — run with -liveconfig -proxy)\n")
		return sb.String()
	}
	liveH := base.CloseProxy.MaxHoldHours
	liveExempt := base.CloseProxy.MaxHoldProfitExemptPct

	type variant struct {
		name string
		mut  func(p *ProtectionParams)
	}
	variants := []variant{
		{fmt.Sprintf("LIVE(%.0fh/exempt%.1f%%)", liveH, liveExempt), func(p *ProtectionParams) {}},
		{fmt.Sprintf("%.0fh/no-exempt", liveH), func(p *ProtectionParams) { p.CloseProxy.MaxHoldProfitExemptPct = 0 }},
		{"maxhold-DISABLED", func(p *ProtectionParams) { p.CloseProxy.MaxHoldHours = 0 }},
		{"24h/exempt2", func(p *ProtectionParams) { p.CloseProxy.MaxHoldHours = 24; p.CloseProxy.MaxHoldProfitExemptPct = 2 }},
		{"24h/no-exempt", func(p *ProtectionParams) { p.CloseProxy.MaxHoldHours = 24; p.CloseProxy.MaxHoldProfitExemptPct = 0 }},
		{"36h/no-exempt", func(p *ProtectionParams) { p.CloseProxy.MaxHoldHours = 36; p.CloseProxy.MaxHoldProfitExemptPct = 0 }},
	}

	var liveP float64
	type row struct {
		name string
		r    PortfolioResult
	}
	rows := make([]row, 0, len(variants))
	for _, v := range variants {
		p := clone(base)
		v.mut(&p)
		r := RunParams(p, loaded)
		rows = append(rows, row{v.name, r})
		if strings.HasPrefix(v.name, "LIVE") {
			liveP = r.TotalPnL
		}
	}
	sb.WriteString(fmt.Sprintf("  %-24s | %-10s %-6s %-6s %-10s %-8s\n",
		"variant", "TotalPnL", "Win%", "PF", "MaxDD", "Trades"))
	for _, x := range rows {
		sb.WriteString(fmt.Sprintf("  %-24s | %-+10.2f %-6.1f %-6.2f %-10.2f %-8d Δ%+.2f\n",
			x.name, x.r.TotalPnL, x.r.WinRatePct, x.r.ProfitFactor, x.r.MaxDrawdown, x.r.Trades, x.r.TotalPnL-liveP))
	}
	return sb.String()
}
