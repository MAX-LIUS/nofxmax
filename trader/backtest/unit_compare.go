package backtest

import "fmt"

// UnitCompareRow is one protection-unit configuration and its outcome.
type UnitCompareRow struct {
	Label  string
	PnL    float64
	WinPct float64
	PF     float64
	MaxDD  float64
	Trades int
}

// CompareUnits replays the same entries under percent-mode (Claude live %
// protection) and several ATR-mode configs (Claude-R style), answering the
// % vs AI-ATR profit question on identical data. The ATR configs reuse the
// percent baseline's close ratios / give-back; only the distance UNIT changes
// (fixed % of entry vs ATR multiples). atrSweepBest adds the grid-optimised pt.
func CompareUnits(loaded []loadedEntry, atrSweepBest *ProtectionParams) []UnitCompareRow {
	pct := ClaudeBaselineParams()

	atrTight := buildATRParams(2.5, 1.0, 3.0, 1.0, 2.5)
	atrMid := buildATRParams(3.5, 1.5, 5.0, 1.5, 3.0)
	atrWide := buildATRParams(4.5, 2.0, 7.0, 2.0, 3.5)

	configs := []struct {
		label string
		p     ProtectionParams
	}{
		{"PERCENT baseline (TP3/6 SL5 BE2/4 DD6)", pct},
		{"ATR tight (SL2.5 TP1.0/3.0 BE1.0/2.5)", atrTight},
		{"ATR mid   (SL3.5 TP1.5/5.0 BE1.5/3.0)", atrMid},
		{"ATR wide  (SL4.5 TP2.0/7.0 BE2.0/3.5)", atrWide},
	}
	if atrSweepBest != nil {
		configs = append(configs, struct {
			label string
			p     ProtectionParams
		}{"ATR grid-best (swept)", *atrSweepBest})
	}

	rows := make([]UnitCompareRow, 0, len(configs))
	for _, c := range configs {
		r := RunParams(c.p, loaded)
		rows = append(rows, UnitCompareRow{
			Label: c.label, PnL: r.TotalPnL, WinPct: r.WinRatePct,
			PF: r.ProfitFactor, MaxDD: r.MaxDrawdown, Trades: r.Trades,
		})
	}
	return rows
}

// FormatUnitCompare renders the unit-comparison rows as a table.
func FormatUnitCompare(rows []UnitCompareRow) string {
	s := fmt.Sprintf("%-40s | %9s %7s %6s %9s\n", "config", "PnL", "Win%", "PF", "MaxDD")
	for _, r := range rows {
		s += fmt.Sprintf("%-40s | %9.2f %7.1f %6.2f %9.2f\n",
			r.Label, r.PnL, r.WinPct, r.PF, r.MaxDD)
	}
	return s
}
