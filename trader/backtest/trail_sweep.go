package backtest

import (
	"fmt"
	"sort"
	"strings"
)

// trail_sweep.go evaluates the ratcheting structural stop (TrailStruct*) against
// the trader's live baseline. It reports, per parameter set: how OFTEN the trail
// armed and moved (fire count), how much total PnL it ADDED vs the frozen-boundary
// baseline, and — trade by trade — how much profit it GAVE UP EARLY (trades the
// trail closed for less than the baseline kept) vs LOCKED IN (trades it saved).
//
// The baseline here is the same live config with TrailStructEnabled=false, so every
// delta is attributable purely to turning the trail on (and its tol/mode).

// TrailVariant is one trailing parameter set to evaluate.
type TrailVariant struct {
	Name string
	P    ProtectionParams
}

// TrailVariants builds the trailing sweep off a live baseline: it only makes sense
// when the baseline runs the close-confirm structural stop (RangeSLCloseConfirm),
// since the trail ratchets that boundary. Sweeps tolerance × mode.
func TrailVariants(base ProtectionParams) []TrailVariant {
	if !base.RangeSLEnabled || !base.RangeSLCloseConfirm {
		return nil
	}
	var vs []TrailVariant
	// tolerance cushion in ATR multiples beyond structure (volatility tolerance +
	// anti-jitter step). Too small = whipsaw; too large = trail never binds.
	tols := []float64{0.25, 0.5, 0.75, 1.0, 1.5}
	// mode: current-period structure, higher-tf only, or the looser of both.
	modes := []string{"current", "higher", "both"}
	for _, mode := range modes {
		for _, tol := range tols {
			v := clone(base)
			v.TrailStructEnabled = true
			v.TrailStructTolATR = tol
			v.TrailStructMode = mode
			if v.TrailStructHigherMult < 2 {
				v.TrailStructHigherMult = 4
			}
			// Inherit the baseline min-profit gate as-is: 0 (disabled) stays disabled so
			// the sweep reflects the live semantics, a positive baseline keeps its cushion.
			vs = append(vs, TrailVariant{
				Name: fmt.Sprintf("trail-%s-tol%s", mode, trimHours(tol)),
				P:    v,
			})
			// Runner variant: keep ratcheting through the BE phase so the runner
			// rides tightening structure instead of the fixed BE offset. This is
			// where a trend-follow trail is expected to earn its keep.
			vr := clone(v)
			vr.TrailStructAfterBE = true
			vs = append(vs, TrailVariant{
				Name: fmt.Sprintf("runner-%s-tol%s", mode, trimHours(tol)),
				P:    vr,
			})
		}
	}
	return vs
}

// HigherTFStopVariants builds STOP-LOSS-focused variants: higher-period structure
// only ("higher" mode), swept across higher-TF multiplier (4h/6h/12h from 1h base)
// and a WIDE tolerance band (loose, anti-whipsaw). The intent is to pull in the too-
// loose 4.5-ATR backstop toward a higher-period structural level without the current-
// period whipsaw — used as a tighter STOP, not a profit lock. Both pre-BE (default)
// and runner (through-BE) forms are generated.
func HigherTFStopVariants(base ProtectionParams) []TrailVariant {
	if !base.RangeSLEnabled || !base.RangeSLCloseConfirm {
		return nil
	}
	var vs []TrailVariant
	mults := []int{4, 6, 12} // 4h / 6h / 12h higher-period structure
	tols := []float64{1.0, 1.5, 2.0}
	for _, m := range mults {
		for _, tol := range tols {
			v := clone(base)
			v.TrailStructEnabled = true
			v.TrailStructMode = "higher"
			v.TrailStructHigherMult = m
			v.TrailStructTolATR = tol
			// Inherit baseline min-profit as-is (0 = gate disabled, matching live).
			v.TrailStructMinProfitATR = base.TrailStructMinProfitATR
			name := fmt.Sprintf("hi%dh-tol%s", m, trimHours(tol))
			vs = append(vs, TrailVariant{Name: name, P: clone(v)})
			vr := clone(v)
			vr.TrailStructAfterBE = true
			vs = append(vs, TrailVariant{Name: "runner-" + name, P: vr})
			// SPLIT: current-period tight stop BEFORE BE, higher-period runner trail AFTER BE.
			vsp := clone(v)
			vsp.TrailStructOnlyAfterBE = true
			vs = append(vs, TrailVariant{Name: "split-" + name, P: vsp})
		}
	}
	return vs
}

// TrailStat summarizes one trailing variant vs the frozen baseline.
type TrailStat struct {
	Name           string
	TotalPnL       float64
	DeltaPnL       float64 // vs frozen-boundary baseline
	TradesArmed    int     // trades where the trail armed & ratcheted at least once
	TotalRatchets  int     // sum of ratchet moves across all trades
	TrailExits     int     // trades the ratcheted boundary actually closed
	ProfitLocked   float64 // sum of positive per-trade deltas (trail did better)
	ProfitGivenUp  float64 // sum of negative per-trade deltas (trail closed early for less)
	TradesImproved int
	TradesWorsened int
	WinRatePct     float64
	MaxDrawdown    float64
	// STOP-LOSS lens: split the per-trade delta by whether the FROZEN baseline
	// trade was a loser or a winner. LossSaved = Σ positive delta on baseline
	// LOSERS (trail cut the loss earlier — the stop-loss win). WinnerClipped =
	// Σ negative delta on baseline WINNERS (trail clipped a winner — the cost).
	LossSaved     float64
	WinnerClipped float64
}

// ComputeTrailStats replays the frozen baseline once, then each variant from the
// supplied generator, diffing per-trade realized PnL. It splits the delta by whether
// the frozen baseline trade was a loser (LossSaved) or winner (WinnerClipped) so the
// STOP-LOSS value (cutting losers earlier) is separated from the cost (clipping winners).
func ComputeTrailStats(base ProtectionParams, loaded []loadedEntry, gen func(ProtectionParams) []TrailVariant) []TrailStat {
	// Frozen baseline: trailing OFF, everything else identical.
	frozen := clone(base)
	frozen.TrailStructEnabled = false
	baseRes := make([]TradeResult, len(loaded))
	var basePnL float64
	for i, le := range loaded {
		baseRes[i] = ReplayEntry(frozen, le.entry, le.bars, le.entryIdx)
		basePnL += baseRes[i].RealizedPnL
	}

	var stats []TrailStat
	for _, v := range gen(base) {
		st := TrailStat{Name: v.Name}
		results := make([]TradeResult, len(loaded))
		for i, le := range loaded {
			r := ReplayEntry(v.P, le.entry, le.bars, le.entryIdx)
			results[i] = r
			st.TotalRatchets += r.TrailRatchets
			if r.TrailRatchets > 0 {
				st.TradesArmed++
			}
			if r.TrailExit {
				st.TrailExits++
			}
			d := r.RealizedPnL - baseRes[i].RealizedPnL
			baseLoser := baseRes[i].RealizedPnL < 0
			switch {
			case d > 1e-9:
				st.ProfitLocked += d
				st.TradesImproved++
				if baseLoser {
					st.LossSaved += d // cut a losing trade's loss (the stop-loss win)
				}
			case d < -1e-9:
				st.ProfitGivenUp += -d
				st.TradesWorsened++
				if !baseLoser {
					st.WinnerClipped += -d // clipped a winner (the cost)
				}
			}
		}
		agg := Aggregate(results)
		st.TotalPnL = agg.TotalPnL
		st.DeltaPnL = agg.TotalPnL - basePnL
		st.WinRatePct = agg.WinRatePct
		st.MaxDrawdown = agg.MaxDrawdown
		stats = append(stats, st)
	}
	// Best delta first.
	sort.Slice(stats, func(i, j int) bool { return stats[i].DeltaPnL > stats[j].DeltaPnL })
	return stats
}

// FormatTrailStats renders the full trailing sweep table (tol × mode).
func FormatTrailStats(base ProtectionParams, loaded []loadedEntry) string {
	return formatTrailTable(base, loaded, TrailVariants,
		"→ dPnL vs frozen baseline (trailing OFF). lossSaved+=Σ cut on baseline LOSERS\n"+
			"  (stop-loss win); winClip-=Σ clipped on baseline WINNERS (the cost). Net = dPnL.\n")
}

// FormatHigherTFStopStats renders the STOP-LOSS-focused higher-period sweep
// (4h/6h/12h × wide tolerance), the configuration meant to tighten the too-loose
// backstop without current-period whipsaw.
func FormatHigherTFStopStats(base ProtectionParams, loaded []loadedEntry) string {
	return formatTrailTable(base, loaded, HigherTFStopVariants,
		"→ STOP-LOSS lens: lossSaved+ = loss cut on baseline LOSERS (the goal);\n"+
			"  winClip- = profit clipped on baseline WINNERS (the cost). Higher-period\n"+
			"  structure + wide tolerance = looser stop, less whipsaw. Net = dPnL.\n")
}

func formatTrailTable(base ProtectionParams, loaded []loadedEntry, gen func(ProtectionParams) []TrailVariant, footer string) string {
	stats := ComputeTrailStats(base, loaded, gen)
	if len(stats) == 0 {
		return "(sweep needs a RangeSLCloseConfirm baseline; skipped)\n"
	}
	var b strings.Builder
	frozen := clone(base)
	frozen.TrailStructEnabled = false
	fr := RunParams(frozen, loaded)
	fmt.Fprintf(&b, "frozen-baseline: TotalPnL=%.2f Win%%=%.1f MaxDD=%.2f trades=%d\n",
		fr.TotalPnL, fr.WinRatePct, fr.MaxDrawdown, fr.Trades)
	fmt.Fprintf(&b, "%-22s %-9s %-8s %-7s %-8s %-8s %-10s %-10s %-7s\n",
		"variant", "TotalPnL", "dPnL", "armed", "ratchet", "trailX", "lossSaved+", "winClip-", "impr/wrs")
	for _, s := range stats {
		fmt.Fprintf(&b, "%-22s %-9.2f %-+8.2f %-7d %-8d %-8d %-10.2f %-10.2f %d/%d\n",
			s.Name, s.TotalPnL, s.DeltaPnL, s.TradesArmed, s.TotalRatchets, s.TrailExits,
			s.LossSaved, s.WinnerClipped, s.TradesImproved, s.TradesWorsened)
	}
	b.WriteString(footer)
	return b.String()
}

// TrailDiag explains WHY the trail did or didn't arm on a sample: it counts, over
// the frozen baseline, how many trades ran a live close-confirm structural stop
// (confirmBoundary>0) vs armed break-even (which gates the trail off), so a
// zero-arm result is attributable to BE occupying the profit-locking window.
func FormatTrailDiag(base ProtectionParams, loaded []loadedEntry) string {
	frozen := clone(base)
	frozen.TrailStructEnabled = false
	var withStructExit, withBE, withTP, withStop int
	for _, le := range loaded {
		r := ReplayEntry(frozen, le.entry, le.bars, le.entryIdx)
		be, se, tp, sl := false, false, false, false
		for _, cr := range r.CloseReasons {
			switch cr {
			case "break_even":
				be = true
			case "structural_sl":
				se = true
			case "take_profit":
				tp = true
			case "stop_loss":
				sl = true
			}
		}
		if be {
			withBE++
		}
		if se {
			withStructExit++
		}
		if tp {
			withTP++
		}
		if sl {
			withStop++
		}
	}
	return fmt.Sprintf("diag(frozen): trades=%d | closed_by_structural=%d break_even=%d take_profit=%d stop_loss=%d\n"+
		"  (the trail only operates BEFORE break-even arms; high break_even share = little room to trail)\n",
		len(loaded), withStructExit, withBE, withTP, withStop)
}
