package backtest

import (
	"fmt"
	"strings"
)

// FormatProvenSLCompare replays the same loaded entries under two boundary
// strategies and prints a side-by-side comparison:
//
//   Baseline   — fractal pivot (current live behaviour, RangeSLPreferProven=false)
//   Proven     — order-block edge preferred when it does not widen the stop
//                (RangeSLPreferProven=true, mirrors live PreferProvenLevels)
//
// Reports net PnL, win%, profit factor, max drawdown, and stop-hit rate so the
// reviewer can see whether anchoring to a proven structural level improves
// outcomes before enabling the flag on any live trader.
func FormatProvenSLCompare(base ProtectionParams, loaded []loadedEntry) string {
	if !base.RangeSLEnabled {
		return "ProvenSL compare: baseline must have RangeSLEnabled (use -liveconfig with a RangeSL strategy)\n"
	}

	fractalP := base
	fractalP.RangeSLPreferProven = false

	provenP := base
	provenP.RangeSLPreferProven = true

	fractalR := RunParams(fractalP, loaded)
	provenR := RunParams(provenP, loaded)

	slHits := func(pr PortfolioResult) int {
		n := 0
		for _, r := range pr.Results {
			for _, rc := range r.CloseReasons {
				if rc == "stop_loss" || rc == "structural_sl" {
					n++
					break
				}
			}
		}
		return n
	}
	slPct := func(pr PortfolioResult) float64 {
		if pr.Trades == 0 {
			return 0
		}
		return float64(slHits(pr)) / float64(pr.Trades) * 100
	}

	// Check how many entries actually changed boundary (proven found a different level).
	changed := 0
	for _, le := range loaded {
		e := le.entry
		isLong := strings.EqualFold(e.Side, "long")
		fb, fOK := rangeStructuralBoundary(fractalP, e, le.bars, le.entryIdx, isLong)
		pb, pOK := rangeStructuralBoundary(provenP, e, le.bars, le.entryIdx, isLong)
		if fOK != pOK || (fOK && pOK && abs64(fb-pb) > 1e-8) {
			changed++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "==== PROVEN-SL A/B: fractal-pivot vs order-block-edge (n=%d entries, %d boundary changes = %.1f%%) ====\n",
		len(loaded), changed, float64(changed)/float64(max1(len(loaded), 1))*100)
	fmt.Fprintf(&b, "\n%-14s %10s %8s %8s %12s %10s\n",
		"variant", "TotalPnL", "Win%", "PF", "MaxDD", "SL-hit%")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", 68))
	row := func(name string, r PortfolioResult) {
		fmt.Fprintf(&b, "%-14s %10.2f %8.1f %8.2f %12.2f %10.1f%%\n",
			name, r.TotalPnL, r.WinRatePct, r.ProfitFactor, r.MaxDrawdown, slPct(r))
	}
	row("fractal", fractalR)
	row("proven", provenR)

	diff := provenR.TotalPnL - fractalR.TotalPnL
	sign := "+"
	if diff < 0 {
		sign = ""
	}
	fmt.Fprintf(&b, "\nΔ proven vs fractal: PnL %s%.2f  Win%% %+.1f  DD %+.2f  SL-hit%% %+.1f%%\n",
		sign, diff,
		provenR.WinRatePct-fractalR.WinRatePct,
		provenR.MaxDrawdown-fractalR.MaxDrawdown,
		slPct(provenR)-slPct(fractalR))

	if changed == 0 {
		fmt.Fprintf(&b, "\n⚠  0 boundary changes — no order blocks found on the protective side of entry in this dataset.\n")
		fmt.Fprintf(&b, "   Possible causes: small/monotonic windows, wide floor clamping all differences, or dataset lacks BOS.\n")
	} else if diff > 0 {
		fmt.Fprintf(&b, "\n✓  Proven-levels improves net PnL — eligible for grayscale on one trader.\n")
	} else {
		fmt.Fprintf(&b, "\n✗  Proven-levels does not improve net PnL — keep dormant, investigate boundary changes.\n")
	}
	return b.String()
}

func abs64(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func max1(a, b int) int {
	if a > b {
		return a
	}
	return b
}
