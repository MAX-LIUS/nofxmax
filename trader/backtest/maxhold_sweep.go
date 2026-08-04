package backtest

import (
	"fmt"
	"math/rand"
	"sort"
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
	// Full hours × exempt grid. The fixed list above omits the previously-live 18h
	// and probes exempt only at 2%, so it cannot separate "hours matter" from
	// "the profit exemption matters" nor show whether either axis is monotonic.
	// A grid that skips the contested point cannot settle the question.
	for _, h := range []float64{12, 18, 24, 36, 48, 72} {
		for _, ex := range []float64{0, 1, 2, 3, 5} {
			hh, exex := h, ex
			variants = append(variants, variant{
				fmt.Sprintf("grid-%.0fh/ex%.0f%%", hh, exex),
				func(p *ProtectionParams) {
					p.CloseProxy.MaxHoldHours = hh
					p.CloseProxy.MaxHoldProfitExemptPct = exex
				},
			})
		}
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

// FormatMaxHoldPlacebo answers the question the grid above cannot: is the gain
// from a max-hold threshold a property of the THRESHOLD, or just of truncating a
// losing ledger? equity-circuit-breaker registered that on a negative book any
// arbitrary truncation tends to improve PnL, and this replay's baseline IS negative
// (open-ended horizon, AI closes unmodelled), so the grid's best cell is suspect by
// construction.
//
// The control: keep the exemption condition byte-identical (pnl% < exempt) and keep
// the same expected hold budget, but randomise the hour threshold PER ENTRY. If the
// real fixed-H rule carries information, it must beat this placebo distribution's
// upper tail. Fire counts are printed alongside because a frequency mismatch would
// let "cuts more often" masquerade as "cuts at a better time".
// placeboLoMul/placeboHiMul bound the per-entry random H as a multiple of the real
// threshold. Exposed so the fire-count match can be tuned and reported.
var placeboLoMul, placeboHiMul = 0.6, 1.4

func FormatMaxHoldPlacebo(base ProtectionParams, loaded []loadedEntry, realH, exempt float64, seeds int) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("==== MAX-HOLD PLACEBO (real %.0fh/ex%.0f%% vs random-H, exemption identical, seeds=%d) ====\n",
		realH, exempt, seeds))
	if !base.CloseProxy.Enabled {
		sb.WriteString("  (CloseProxy disabled — run with -liveconfig -proxy)\n")
		return sb.String()
	}

	countFires := func(p ProtectionParams, entries []loadedEntry) (float64, int) {
		var tot float64
		fires := 0
		for _, le := range entries {
			res := ReplayEntry(p, le.entry, le.bars, le.entryIdx)
			tot += res.RealizedPnL
			for _, r := range res.CloseReasons {
				if r == "max_hold" {
					fires++
					break
				}
			}
		}
		return tot, fires
	}

	// Reference: max-hold OFF, same everything else.
	off := clone(base)
	off.CloseProxy.MaxHoldHours = 0
	offPnL, _ := countFires(off, loaded)

	// Real rule.
	real := clone(base)
	real.CloseProxy.MaxHoldHours = realH
	real.CloseProxy.MaxHoldProfitExemptPct = exempt
	realPnL, realFires := countFires(real, loaded)
	realDelta := realPnL - offPnL

	// Placebo: same exemption, H drawn uniformly per entry from [0.25*realH, 1.75*realH]
	// so the MEAN hold budget matches realH while the specific threshold is destroyed.
	pl := clone(base)
	pl.CloseProxy.MaxHoldHours = realH // ignored per entry via override
	pl.CloseProxy.MaxHoldProfitExemptPct = exempt
	// Range is deliberately narrow. A wide draw ([0.25,1.75]×) matches the MEAN hold
	// budget but not the FIRE COUNT: small H values fire far more often inside a fixed
	// horizon, so the placebo cuts more trades than the real rule and the comparison
	// mixes frequency with timing. Narrowing keeps fire counts comparable, which is the
	// condition under which the p-value measures timing alone.
	lo, hi := placeboLoMul*realH, placeboHiMul*realH
	deltas := make([]float64, 0, seeds)
	fireSum := 0
	for s := 0; s < seeds; s++ {
		rng := rand.New(rand.NewSource(int64(1000 + s)))
		mod := make([]loadedEntry, len(loaded))
		for i, le := range loaded {
			le.entry.MaxHoldHoursOverride = lo + rng.Float64()*(hi-lo)
			mod[i] = le
		}
		p, f := countFires(pl, mod)
		deltas = append(deltas, p-offPnL)
		fireSum += f
	}
	sort.Float64s(deltas)
	mean := 0.0
	for _, d := range deltas {
		mean += d
	}
	mean /= float64(len(deltas))
	beat := 0
	for _, d := range deltas {
		if d >= realDelta {
			beat++
		}
	}
	pct := func(q float64) float64 {
		i := int(q * float64(len(deltas)-1))
		return deltas[i]
	}
	sb.WriteString(fmt.Sprintf("  maxhold-OFF reference PnL      : %+.2f\n", offPnL))
	sb.WriteString(fmt.Sprintf("  real  %.0fh/ex%.0f%%   dPnL vs OFF : %+.2f   (fires %d)\n", realH, exempt, realDelta, realFires))
	sb.WriteString(fmt.Sprintf("  placebo random-H  dPnL vs OFF : mean %+.2f  p05 %+.2f  p50 %+.2f  p95 %+.2f  (mean fires %.1f)\n",
		mean, pct(0.05), pct(0.50), pct(0.95), float64(fireSum)/float64(seeds)))
	sb.WriteString(fmt.Sprintf("  placebo runs matching/beating real: %d/%d  → p=%.4f\n", beat, seeds, float64(beat)/float64(seeds)))
	if float64(beat)/float64(seeds) > 0.05 {
		sb.WriteString("  ⇒ NOT significant: the specific hour threshold carries no information beyond\n")
		sb.WriteString("    'cut a losing ledger somewhere'. Do not tune MaxHoldHours on this evidence.\n")
	} else {
		sb.WriteString("  ⇒ real rule beats the placebo tail — the threshold itself carries information.\n")
	}
	return sb.String()
}
