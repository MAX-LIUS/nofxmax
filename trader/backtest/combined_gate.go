package backtest

import (
	"fmt"
	"strings"
)

// combined_gate.go replays the FULL loaded set under a proposed config that stacks
// two independent levers found earlier:
//   (1) entry gate: skip entries carrying a structural target whose reward distance
//       is < MinRewardATR (target-too-close). Entries without a structural target are
//       kept (the gate can't judge them); this is conservative.
//   (2) protection band: backstop tightened to BackstopATR (floor kept).
// It prints baseline vs gate-only vs band-only vs combined so each lever's marginal
// contribution and the stacked total are explicit. Replay PnL is directional.
func FormatCombinedGate(trader string, base ProtectionParams, loaded []loadedEntry, minRewardATR, backstopATR float64) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("==== COMBINED GATE+BAND: %s (n=%d, minRewardATR=%.2f backstop=%.1f) ====\n",
		trader, len(loaded), minRewardATR, backstopATR))

	// Entry-gate filter: drop structural-target entries below the reward-ATR floor.
	kept := make([]loadedEntry, 0, len(loaded))
	var dropped int
	for _, le := range loaded {
		rATR, _, ok := rewardATRForEntry(le)
		if ok && rATR < minRewardATR {
			dropped++
			continue
		}
		kept = append(kept, le)
	}

	bandOf := func(p ProtectionParams, b float64) ProtectionParams {
		v := clone(p)
		if v.RangeSLEnabled {
			v.RangeSLBackstopATR = b
			v.StopLossATR = b
		}
		return v
	}

	baseR := RunParams(base, loaded)
	gateR := RunParams(base, kept)
	bandR := RunParams(bandOf(base, backstopATR), loaded)
	combR := RunParams(bandOf(base, backstopATR), kept)

	row := func(name string, r PortfolioResult, n int) {
		sb.WriteString(fmt.Sprintf("  %-22s n=%-4d TotalPnL=%-+8.2f Win%%=%-5.1f PF=%-5.2f MaxDD=%-7.2f\n",
			name, n, r.TotalPnL, r.WinRatePct, r.ProfitFactor, r.MaxDrawdown))
	}
	row("baseline(live)", baseR, len(loaded))
	row("gate-only", gateR, len(kept))
	row("band-only(back="+trimHours(backstopATR)+")", bandR, len(loaded))
	row("COMBINED", combR, len(kept))
	sb.WriteString(fmt.Sprintf("  (entry gate dropped %d of %d entries with target < %.2f×ATR)\n",
		dropped, len(loaded), minRewardATR))
	sb.WriteString(fmt.Sprintf("  Δ combined vs baseline: PnL %+.2f | MaxDD %+.2f\n",
		combR.TotalPnL-baseR.TotalPnL, combR.MaxDrawdown-baseR.MaxDrawdown))
	return sb.String()
}
