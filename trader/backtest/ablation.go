package backtest

import "fmt"

// AblationRow is one protection-layer configuration and its aggregate outcome.
type AblationRow struct {
	Label    string
	PnL      float64
	WinPct   float64
	PF       float64
	MaxDD    float64
	Trades   int
	DPnL     float64 // PnL delta vs full baseline (this row - full)
}

// withoutDD/BE/TP/SL return copies of p with one layer removed.
func cloneParams(p ProtectionParams) ProtectionParams {
	cp := p
	cp.TPLegs = append([]LadderLeg(nil), p.TPLegs...)
	cp.BELegs = append([]BELeg(nil), p.BELegs...)
	cp.DDRules = append([]DDRule(nil), p.DDRules...)
	return cp
}

// AblateProtection measures each protection layer's marginal contribution to
// PnL by removing one layer at a time from the percent baseline and replaying
// the same entries (guard OFF, so this isolates DD/BE/TP/SL — not the guard).
// All rows replay the identical loaded set, so PnL deltas are directly
// attributable to the toggled layer.
func AblateProtection(loaded []loadedEntry) []AblationRow {
	base := ClaudeBaselineParams()

	noDD := cloneParams(base)
	noDD.DDRules = nil

	noBE := cloneParams(base)
	noBE.BELegs = nil

	noTP := cloneParams(base)
	noTP.TPLegs = nil

	noSL := cloneParams(base)
	noSL.StopLossPct = 0

	slOnly := ProtectionParams{Unit: UnitPercent, StopLossPct: base.StopLossPct}

	tpOnly := cloneParams(base)
	tpOnly.BELegs = nil
	tpOnly.DDRules = nil

	configs := []struct {
		label string
		p     ProtectionParams
	}{
		{"FULL baseline (TP+SL+BE+DD)", base},
		{"  - no DD (drawdown tiers off)", noDD},
		{"  - no BE (break-even off)", noBE},
		{"  - no TP (ladder TP off)", noTP},
		{"  - no SL (stop-loss off)", noSL},
		{"SL only", slOnly},
		{"TP+SL only (no BE/DD)", tpOnly},
	}

	var fullPnL float64
	rows := make([]AblationRow, 0, len(configs))
	for i, c := range configs {
		r := RunParams(c.p, loaded)
		if i == 0 {
			fullPnL = r.TotalPnL
		}
		rows = append(rows, AblationRow{
			Label: c.label, PnL: r.TotalPnL, WinPct: r.WinRatePct,
			PF: r.ProfitFactor, MaxDD: r.MaxDrawdown, Trades: r.Trades,
			DPnL: r.TotalPnL - fullPnL,
		})
	}
	return rows
}

// FormatAblation renders the ablation rows as a fixed-width table.
func FormatAblation(rows []AblationRow) string {
	s := fmt.Sprintf("%-34s | %9s %7s %6s %9s %7s\n",
		"config", "PnL", "Win%", "PF", "MaxDD", "ΔPnL")
	for _, r := range rows {
		s += fmt.Sprintf("%-34s | %9.2f %7.1f %6.2f %9.2f %7.2f\n",
			r.Label, r.PnL, r.WinPct, r.PF, r.MaxDD, r.DPnL)
	}
	return s
}
