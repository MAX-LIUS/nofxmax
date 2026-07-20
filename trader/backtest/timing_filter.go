package backtest

import (
	"fmt"
	"strings"
)

// timing_filter.go tests whether an ENTRY-TIMING filter improves outcomes,
// separately from the direction/regime chart_trend gate. Two families:
//
//  FILTER modes (skip the trade if the timing condition fails; kept trades replay
//  at the ORIGINAL entry): measures whether timing predicts a bad trade.
//
//  SHIFT modes (delay entry by 1 bar, re-price at next bar, replay the full trade
//  from the shifted index): measures whether waiting for confirmation helps the
//  SAME trades rather than dropping them.
//
// All PnL is full-trade realized PnL via ReplayEntry under the live baseline, so
// results are comparable to the variant sweep.

// TimingVariant is one timing rule.
type TimingVariant struct {
	Name string
	// apply returns (keep, newEntryIdx, newEntryPrice). keep=false drops the trade.
	apply func(le loadedEntry) (bool, int, float64)
}

// TimingVariants builds the candidate timing rules.
func TimingVariants() []TimingVariant {
	long := func(le loadedEntry) bool { return strings.EqualFold(le.entry.Side, "LONG") }

	return []TimingVariant{
		// Baseline: take every trade at its recorded entry.
		{Name: "baseline", apply: func(le loadedEntry) (bool, int, float64) {
			return true, le.entryIdx, le.entry.EntryPrice
		}},

		// FILTER: entry bar must CLOSE in the trade direction (momentum confirm).
		{Name: "filter-entrybar-green", apply: func(le loadedEntry) (bool, int, float64) {
			b := le.bars[le.entryIdx]
			ok := (long(le) && b.Close >= le.entry.EntryPrice) || (!long(le) && b.Close <= le.entry.EntryPrice)
			return ok, le.entryIdx, le.entry.EntryPrice
		}},

		// FILTER: next bar must confirm direction (skip if entry bar+1 goes adverse).
		{Name: "filter-nextbar-confirm", apply: func(le loadedEntry) (bool, int, float64) {
			if le.entryIdx+1 >= len(le.bars) {
				return true, le.entryIdx, le.entry.EntryPrice
			}
			n := le.bars[le.entryIdx+1]
			ok := (long(le) && n.Close > le.entry.EntryPrice) || (!long(le) && n.Close < le.entry.EntryPrice)
			return ok, le.entryIdx, le.entry.EntryPrice
		}},

		// SHIFT: wait one bar, enter at next bar's close only if it confirmed
		// direction; otherwise drop. Re-prices and re-indexes.
		{Name: "shift-confirm-1bar", apply: func(le loadedEntry) (bool, int, float64) {
			ni := le.entryIdx + 1
			if ni >= len(le.bars) {
				return false, 0, 0
			}
			n := le.bars[ni]
			confirmed := (long(le) && n.Close > le.entry.EntryPrice) || (!long(le) && n.Close < le.entry.EntryPrice)
			if !confirmed {
				return false, 0, 0
			}
			return true, ni, n.Close
		}},

		// SHIFT: always wait one bar and enter at next close (no confirm condition) —
		// isolates the pure "delay" effect vs the "confirm" effect above.
		{Name: "shift-delay-1bar", apply: func(le loadedEntry) (bool, int, float64) {
			ni := le.entryIdx + 1
			if ni >= len(le.bars) {
				return false, 0, 0
			}
			return true, ni, le.bars[ni].Close
		}},

		// FILTER: skip entries whose entry bar range is a big adverse wick against us
		// (>1 ATR adverse intrabar) — proxy for "chasing into a spike".
		{Name: "filter-no-adverse-spike", apply: func(le loadedEntry) (bool, int, float64) {
			atr := entryATR(le.bars, le.entryIdx)
			if atr <= 0 {
				return true, le.entryIdx, le.entry.EntryPrice
			}
			b := le.bars[le.entryIdx]
			var adv float64
			if long(le) {
				adv = (le.entry.EntryPrice - b.Low) / atr
			} else {
				adv = (b.High - le.entry.EntryPrice) / atr
			}
			return adv < 1.0, le.entryIdx, le.entry.EntryPrice
		}},
	}
}

// TimingStat is the outcome of one timing rule over the loaded set.
type TimingStat struct {
	Name       string
	Kept       int
	Dropped    int
	TotalPnL   float64
	DeltaPnL   float64 // vs baseline total
	DroppedPnL float64 // baseline PnL of the trades this rule dropped
}

// RunTimingFilters replays each timing variant under the live baseline params.
func RunTimingFilters(base ProtectionParams, loaded []loadedEntry) []TimingStat {
	// Precompute each trade's baseline PnL (original entry) for drop accounting.
	basePnL := make([]float64, len(loaded))
	var baseTotal float64
	for i, le := range loaded {
		r := ReplayEntry(base, le.entry, le.bars, le.entryIdx)
		basePnL[i] = r.RealizedPnL
		baseTotal += r.RealizedPnL
	}

	var stats []TimingStat
	for _, v := range TimingVariants() {
		st := TimingStat{Name: v.Name}
		for i, le := range loaded {
			keep, idx, px := v.apply(le)
			if !keep {
				st.Dropped++
				st.DroppedPnL += basePnL[i]
				continue
			}
			st.Kept++
			e := le.entry
			e.EntryPrice = px
			r := ReplayEntry(base, e, le.bars, idx)
			st.TotalPnL += r.RealizedPnL
		}
		st.DeltaPnL = st.TotalPnL - baseTotal
		stats = append(stats, st)
	}
	return stats
}

// FormatTimingFilters renders the timing-filter comparison.
func FormatTimingFilters(trader string, base ProtectionParams, loaded []loadedEntry) string {
	stats := RunTimingFilters(base, loaded)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("==== TIMING-FILTER SWEEP: %s (full-trade PnL under live baseline) ====\n", trader))
	sb.WriteString(fmt.Sprintf("%-26s %-6s %-8s %-10s %-10s %-10s\n",
		"rule", "kept", "dropped", "totalPnL", "dPnL", "droppedPnL"))
	for _, s := range stats {
		sb.WriteString(fmt.Sprintf("%-26s %-6d %-8d %-10.2f %-+10.2f %-10.2f\n",
			s.Name, s.Kept, s.Dropped, s.TotalPnL, s.DeltaPnL, s.DroppedPnL))
	}
	sb.WriteString("→ dPnL vs baseline. FILTER rules drop trades (droppedPnL = baseline PnL of dropped).\n")
	sb.WriteString("  SHIFT rules re-enter at next bar; a positive dPnL means waiting helped the kept trades.\n")
	return sb.String()
}

// FormatGatePlusTiming stacks the chart_trend gate with shift-confirm-1bar timing
// to see whether direction filtering + timing confirmation compound. Reports each
// stage's kept count and full-trade PnL under the live baseline.
func FormatGatePlusTiming(trader string, base ProtectionParams, loaded []loadedEntry, window int, alignMin, r2Min float64) string {
	var baseTotal float64
	for _, le := range loaded {
		r := ReplayEntry(base, le.entry, le.bars, le.entryIdx)
		baseTotal += r.RealizedPnL
	}

	long := func(le loadedEntry) bool { return strings.EqualFold(le.entry.Side, "LONG") }
	// Stage counters.
	var gateOnly, timingOnly, both struct {
		kept int
		pnl  float64
	}
	for _, le := range loaded {
		allow, _, _, _ := chartTrendAllow(le, window, alignMin, r2Min)
		// timing (shift-confirm-1bar) verdict + shifted entry
		ni := le.entryIdx + 1
		timingKeep := false
		var tIdx int
		var tPx float64
		if ni < len(le.bars) {
			n := le.bars[ni]
			timingKeep = (long(le) && n.Close > le.entry.EntryPrice) || (!long(le) && n.Close < le.entry.EntryPrice)
			tIdx, tPx = ni, n.Close
		}

		if allow {
			r := ReplayEntry(base, le.entry, le.bars, le.entryIdx)
			gateOnly.kept++
			gateOnly.pnl += r.RealizedPnL
		}
		if timingKeep {
			e := le.entry
			e.EntryPrice = tPx
			r := ReplayEntry(base, e, le.bars, tIdx)
			timingOnly.kept++
			timingOnly.pnl += r.RealizedPnL
		}
		if allow && timingKeep {
			e := le.entry
			e.EntryPrice = tPx
			r := ReplayEntry(base, e, le.bars, tIdx)
			both.kept++
			both.pnl += r.RealizedPnL
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("==== GATE + TIMING STACK: %s ====\n", trader))
	sb.WriteString(fmt.Sprintf("%-24s %-6s %-10s %-10s\n", "combo", "kept", "totalPnL", "dPnL"))
	sb.WriteString(fmt.Sprintf("%-24s %-6d %-10.2f %-+10.2f\n", "baseline (all)", len(loaded), baseTotal, 0.0))
	sb.WriteString(fmt.Sprintf("%-24s %-6d %-10.2f %-+10.2f\n", "chart_trend only", gateOnly.kept, gateOnly.pnl, gateOnly.pnl-baseTotal))
	sb.WriteString(fmt.Sprintf("%-24s %-6d %-10.2f %-+10.2f\n", "shift-confirm only", timingOnly.kept, timingOnly.pnl, timingOnly.pnl-baseTotal))
	sb.WriteString(fmt.Sprintf("%-24s %-6d %-10.2f %-+10.2f\n", "gate + shift-confirm", both.kept, both.pnl, both.pnl-baseTotal))
	sb.WriteString("→ both stages are causal (no look-ahead). dPnL vs baseline.\n")
	return sb.String()
}

// FormatShiftConfirmDecomp answers "WHY does shift-confirm help/hurt this trader?"
// by splitting the effect into (a) which trades got dropped (winners we lost vs
// losers we avoided) and (b) the entry-price slippage cost on the KEPT trades from
// entering one bar later. This isolates a real edge signal from a mere entry-price
// artifact — and shows whether the "confirm" indicator is picking the right trades.
func FormatShiftConfirmDecomp(trader string, base ProtectionParams, loaded []loadedEntry) string {
	long := func(le loadedEntry) bool { return strings.EqualFold(le.entry.Side, "LONG") }

	var (
		keptBaseline            float64 // kept trades, PnL at ORIGINAL entry
		keptShifted             float64 // kept trades, PnL at SHIFTED entry
		droppedPnL              float64 // dropped trades, PnL at ORIGINAL entry
		keptWin, keptLoss       int
		dropWin, dropLoss       int
		dropWinPnL, dropLossPnL float64
	)
	for _, le := range loaded {
		ni := le.entryIdx + 1
		confirmed := false
		var tPx float64
		if ni < len(le.bars) {
			n := le.bars[ni]
			confirmed = (long(le) && n.Close > le.entry.EntryPrice) || (!long(le) && n.Close < le.entry.EntryPrice)
			tPx = n.Close
		}
		origPnL := ReplayEntry(base, le.entry, le.bars, le.entryIdx).RealizedPnL
		if !confirmed {
			droppedPnL += origPnL
			if origPnL > 0 {
				dropWin++
				dropWinPnL += origPnL
			} else {
				dropLoss++
				dropLossPnL += origPnL
			}
			continue
		}
		keptBaseline += origPnL
		if origPnL > 0 {
			keptWin++
		} else {
			keptLoss++
		}
		e := le.entry
		e.EntryPrice = tPx
		keptShifted += ReplayEntry(base, e, le.bars, ni).RealizedPnL
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("==== SHIFT-CONFIRM DECOMP: %s ====\n", trader))
	sb.WriteString(fmt.Sprintf("  DROPPED (bar+1 did not confirm): %d trades, PnL=%.2f\n", dropWin+dropLoss, droppedPnL))
	sb.WriteString(fmt.Sprintf("    of which winners dropped: %d (PnL +%.2f) <- edge LOST if large\n", dropWin, dropWinPnL))
	sb.WriteString(fmt.Sprintf("    of which losers avoided : %d (PnL %.2f)  <- edge GAINED if large\n", dropLoss, dropLossPnL))
	sb.WriteString(fmt.Sprintf("  KEPT (confirmed): %d trades  winners=%d losers=%d\n", keptWin+keptLoss, keptWin, keptLoss))
	sb.WriteString(fmt.Sprintf("    kept PnL at ORIGINAL entry: %.2f\n", keptBaseline))
	sb.WriteString(fmt.Sprintf("    kept PnL at SHIFTED  entry: %.2f\n", keptShifted))
	sb.WriteString(fmt.Sprintf("    entry-slippage cost of waiting 1 bar: %+.2f  <- price paid for confirmation\n", keptShifted-keptBaseline))
	sb.WriteString(fmt.Sprintf("  NET vs baseline = dropped-avoided(%.2f) + slippage(%+.2f) = %+.2f\n",
		-droppedPnL, keptShifted-keptBaseline, -droppedPnL+(keptShifted-keptBaseline)))
	return sb.String()
}

// FormatShiftConfirmOpen re-runs shift-confirm but enters at bar+1 OPEN (not close).
// The open is the earliest fill available on the confirming bar, so its slippage is
// smaller than the close. If a trader that lost under close-entry turns positive
// under open-entry, the earlier loss was SLIPPAGE, not a bad confirmation signal.
func FormatShiftConfirmOpen(trader string, base ProtectionParams, loaded []loadedEntry) string {
	long := func(le loadedEntry) bool { return strings.EqualFold(le.entry.Side, "LONG") }
	var baseTotal, closeTotal, openTotal float64
	var kept int
	for _, le := range loaded {
		baseTotal += ReplayEntry(base, le.entry, le.bars, le.entryIdx).RealizedPnL
		ni := le.entryIdx + 1
		if ni >= len(le.bars) {
			continue
		}
		n := le.bars[ni]
		confirmed := (long(le) && n.Close > le.entry.EntryPrice) || (!long(le) && n.Close < le.entry.EntryPrice)
		if !confirmed {
			continue
		}
		kept++
		ec := le.entry
		ec.EntryPrice = n.Close
		closeTotal += ReplayEntry(base, ec, le.bars, ni).RealizedPnL
		eo := le.entry
		eo.EntryPrice = n.Open
		openTotal += ReplayEntry(base, eo, le.bars, ni).RealizedPnL
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("==== SHIFT-CONFIRM ENTRY-PRICE TEST: %s (kept=%d) ====\n", trader, kept))
	sb.WriteString(fmt.Sprintf("  baseline (all, orig entry):        %.2f\n", baseTotal))
	sb.WriteString(fmt.Sprintf("  shift-confirm @ bar+1 CLOSE: total=%.2f  dPnL=%+.2f\n", closeTotal, closeTotal-baseTotal))
	sb.WriteString(fmt.Sprintf("  shift-confirm @ bar+1 OPEN : total=%.2f  dPnL=%+.2f\n", openTotal, openTotal-baseTotal))
	sb.WriteString(fmt.Sprintf("  open-vs-close gap (pure slippage of the confirm bar): %+.2f\n", openTotal-closeTotal))
	return sb.String()
}

// FormatTriggerEntry tests a CAUSAL intrabar momentum-confirm entry: after the
// signal bar, arm a stop-entry at entry +/- k*ATR in the trade direction. If a
// later bar's high (long) / low (short) reaches that trigger within maxWait bars,
// fill AT THE TRIGGER PRICE (a stop order fills when touched — no look-ahead, no
// waiting for a close). Otherwise the trade is skipped. This captures the confirm
// alpha WITHOUT paying the full one-bar close slippage.
func FormatTriggerEntry(trader string, base ProtectionParams, loaded []loadedEntry) string {
	long := func(le loadedEntry) bool { return strings.EqualFold(le.entry.Side, "LONG") }
	var baseTotal float64
	for _, le := range loaded {
		baseTotal += ReplayEntry(base, le.entry, le.bars, le.entryIdx).RealizedPnL
	}

	type cfg struct {
		k       float64
		maxWait int
	}
	configs := []cfg{{0.15, 3}, {0.3, 3}, {0.3, 5}, {0.5, 3}, {0.5, 5}}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("==== TRIGGER-ENTRY SWEEP: %s (stop-entry at entry+k*ATR, fill at trigger) ====\n", trader))
	sb.WriteString(fmt.Sprintf("  baseline (all, orig entry): %.2f\n", baseTotal))
	sb.WriteString(fmt.Sprintf("%-16s %-6s %-8s %-10s %-10s\n", "rule", "filled", "skipped", "totalPnL", "dPnL"))

	for _, c := range configs {
		var total float64
		var filled, skipped int
		for _, le := range loaded {
			atr := entryATR(le.bars, le.entryIdx)
			if atr <= 0 {
				// no ATR -> take at original entry (rare)
				total += ReplayEntry(base, le.entry, le.bars, le.entryIdx).RealizedPnL
				filled++
				continue
			}
			var trig float64
			if long(le) {
				trig = le.entry.EntryPrice + c.k*atr
			} else {
				trig = le.entry.EntryPrice - c.k*atr
			}
			// scan bars from entryIdx forward up to maxWait for a touch of trig
			hit := -1
			for j := le.entryIdx; j <= le.entryIdx+c.maxWait && j < len(le.bars); j++ {
				b := le.bars[j]
				if long(le) && b.High >= trig {
					hit = j
					break
				}
				if !long(le) && b.Low <= trig {
					hit = j
					break
				}
			}
			if hit < 0 {
				skipped++
				continue
			}
			filled++
			e := le.entry
			e.EntryPrice = trig // stop fills at the trigger price
			total += ReplayEntry(base, e, le.bars, hit).RealizedPnL
		}
		name := fmt.Sprintf("k=%.2f w=%d", c.k, c.maxWait)
		sb.WriteString(fmt.Sprintf("%-16s %-6d %-8d %-10.2f %-+10.2f\n", name, filled, skipped, total, total-baseTotal))
	}
	sb.WriteString("→ CAUSAL: stop-entry fills when price touches trigger intrabar; no look-ahead.\n")
	return sb.String()
}

// FormatTriggerEntrySweep is the RIGOROUS 2D sweep that decomposes the trigger-entry
// dPnL into its three underlying forces so the parameters are chosen from evidence,
// not from a sparse hand-picked grid:
//
//   dPnL = (loss avoided on skipped losers)          [benefit]
//        + (compression cost on filled winners)      [cost, already inside filled PnL]
//        + (profit forgone on skipped would-be winners = "killed winners")  [cost]
//
// For every (k, maxWait) it reports:
//   fill%      : filled / total
//   skipWin%   : of the SKIPPED trades, how many would have WON at original entry.
//                Low skipWin% validates the premise "the filter removes losers".
//   killWin    : count of skipped trades that would have WON (killed winners).
//   killWinPnL : summed baseline PnL forgone by killing those winners (a real cost).
//   avdLossPnL : summed baseline PnL of skipped LOSERS (negative => loss we dodged).
//   comprPct   : average price compression on FILLED trades, as % of entry price.
//   netDPnL    : total(filled@trigger) - baseline(all@orig).
//   top1/top3% : share of the (signed) per-trade dPnL contribution coming from the
//                single / three largest movers — high => result is not robust.
func FormatTriggerEntrySweep(trader string, base ProtectionParams, loaded []loadedEntry) string {
	long := func(le loadedEntry) bool { return strings.EqualFold(le.entry.Side, "LONG") }

	// Precompute per-entry baseline (original entry, original index) once.
	type baserow struct {
		pnl   float64
		valid bool
	}
	baserows := make([]baserow, len(loaded))
	var baseTotal float64
	for i, le := range loaded {
		p := ReplayEntry(base, le.entry, le.bars, le.entryIdx).RealizedPnL
		baserows[i] = baserow{pnl: p, valid: true}
		baseTotal += p
	}

	ks := []float64{0.05, 0.10, 0.15, 0.20, 0.25, 0.30, 0.40, 0.50}
	waits := []int{1, 2, 3, 4, 5, 6, 8, 10, 12, 16, 24}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("==== TRIGGER-ENTRY FULL 2D SWEEP: %s ====\n", trader))
	sb.WriteString(fmt.Sprintf("  entries=%d  baseline(all@orig)=%.2f\n", len(loaded), baseTotal))
	sb.WriteString(fmt.Sprintf("%-11s %-6s %-7s %-7s %-11s %-11s %-8s %-10s %-7s %-7s\n",
		"rule", "fill%", "skipWin%", "killWin", "killWinPnL", "avdLossPnL", "comprPct", "netDPnL", "top1%", "top3%"))

	for _, w := range waits {
		for _, k := range ks {
			var filled, skipped int
			var total float64
			var skipWin int
			var killWinPnL, avdLossPnL float64
			var comprSum float64
			var comprN int
			// per-trade dPnL contribution vs that trade's own baseline (for concentration)
			contribs := make([]float64, 0, len(loaded))

			for i, le := range loaded {
				atr := entryATR(le.bars, le.entryIdx)
				bp := baserows[i].pnl
				if atr <= 0 {
					// no ATR -> always fill at original entry (rare); zero delta
					filled++
					total += bp
					contribs = append(contribs, 0)
					continue
				}
				var trig float64
				if long(le) {
					trig = le.entry.EntryPrice + k*atr
				} else {
					trig = le.entry.EntryPrice - k*atr
				}
				hit := -1
				for j := le.entryIdx; j <= le.entryIdx+w && j < len(le.bars); j++ {
					b := le.bars[j]
					if long(le) && b.High >= trig {
						hit = j
						break
					}
					if !long(le) && b.Low <= trig {
						hit = j
						break
					}
				}
				if hit < 0 {
					skipped++
					if bp > 0 { // killed winner
						skipWin++
						killWinPnL += bp
					} else {
						avdLossPnL += bp
					}
					// contribution of skipping = removing this trade's baseline PnL
					contribs = append(contribs, -bp)
					continue
				}
				filled++
				e := le.entry
				e.EntryPrice = trig
				tp := ReplayEntry(base, e, le.bars, hit).RealizedPnL
				total += tp
				// price compression as % of entry (always >= 0: we pay to enter later in-trend)
				if le.entry.EntryPrice > 0 {
					comprSum += (k * atr) / le.entry.EntryPrice * 100
					comprN++
				}
				contribs = append(contribs, tp-bp)
			}

			netD := total - baseTotal
			fillPct := 0.0
			if n := filled + skipped; n > 0 {
				fillPct = float64(filled) / float64(n) * 100
			}
			skipWinPct := 0.0
			if skipped > 0 {
				skipWinPct = float64(skipWin) / float64(skipped) * 100
			}
			comprPct := 0.0
			if comprN > 0 {
				comprPct = comprSum / float64(comprN)
			}
			top1, top3 := concentration(contribs, netD)

			name := fmt.Sprintf("k=%.2f w=%d", k, w)
			sb.WriteString(fmt.Sprintf("%-11s %-6.1f %-7.1f %-7d %-11.2f %-11.2f %-8.3f %-+10.2f %-7.1f %-7.1f\n",
				name, fillPct, skipWinPct, skipWin, killWinPnL, avdLossPnL, comprPct, netD, top1, top3))
		}
		sb.WriteString("  ----\n")
	}
	sb.WriteString("→ skipWin% low + killWin small + comprPct small + netDPnL>0 + top3% low = a real, robust edge.\n")
	return sb.String()
}

// concentration returns the % share of the largest single and three largest
// per-trade dPnL contributions relative to the net dPnL magnitude. It measures
// robustness: if a couple of trades drive everything, the "edge" is noise.
func concentration(contribs []float64, netD float64) (top1, top3 float64) {
	denom := netD
	if denom < 0 {
		denom = -denom
	}
	if denom == 0 || len(contribs) == 0 {
		return 0, 0
	}
	// sort a copy by absolute contribution, descending
	cp := make([]float64, len(contribs))
	copy(cp, contribs)
	for i := 0; i < len(cp); i++ {
		mi := i
		for j := i + 1; j < len(cp); j++ {
			if abs(cp[j]) > abs(cp[mi]) {
				mi = j
			}
		}
		cp[i], cp[mi] = cp[mi], cp[i]
	}
	top1 = abs(cp[0]) / denom * 100
	sum3 := 0.0
	for i := 0; i < 3 && i < len(cp); i++ {
		sum3 += abs(cp[i])
	}
	top3 = sum3 / denom * 100
	return top1, top3
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// FormatTriggerEntryValidate is the SKEPTIC'S battery. A high "filter removes
// losers" rate is nearly a tautology (a trade that never moved even 0.05*ATR in
// your favor for hours is a bad trade by definition), so the raw dPnL means
// nothing on its own. This runs four adversarial checks for a fixed (k,w):
//
//  1. DECOMP: split dPnL into (a) benefit of EXCLUDING skipped trades vs
//     (b) change on the KEPT trades (compression + shifted entry). If (b) ~ 0,
//     the whole effect is just "don't take trades that immediately go against
//     you" — risk management, not alpha.
//
//  2. PLACEBO (random skip): remove the SAME NUMBER of trades at random, 2000x.
//     Report mean/p5/p95 of the resulting dPnL and the percentile rank of the
//     real filter. If the filter is inside the random cloud, it is NOT selecting
//     losers — it is just that removing trades from a losing book helps.
//
//  3. OUT-OF-SAMPLE: pick best k on the FIRST half of entries (by time), then
//     apply that SAME k to the SECOND half. If second-half dPnL collapses, the
//     parameter is overfit.
//
//  4. IMMEDIATE-ADVERSE profile of skipped trades: what % went straight against
//     us. Confirms the mechanism is mechanical, not predictive.
func FormatTriggerEntryValidate(trader string, base ProtectionParams, loaded []loadedEntry, k float64, w int) string {
	long := func(le loadedEntry) bool { return strings.EqualFold(le.entry.Side, "LONG") }

	type row struct {
		basePnL  float64
		filled   bool
		trigPnL  float64 // valid only if filled
		hitIdx   int
		entryT   int64
	}
	rows := make([]row, len(loaded))
	var baseTotal, filledTrig float64
	var filledN, skipN int
	var keptBaseSum float64 // baseline PnL of the trades we KEPT

	for i, le := range loaded {
		bp := ReplayEntry(base, le.entry, le.bars, le.entryIdx).RealizedPnL
		baseTotal += bp
		r := row{basePnL: bp, entryT: le.entry.EntryTime}
		atr := entryATR(le.bars, le.entryIdx)
		if atr <= 0 {
			r.filled = true
			r.trigPnL = bp
			r.hitIdx = le.entryIdx
			filledTrig += bp
			keptBaseSum += bp
			filledN++
			rows[i] = r
			continue
		}
		var trig float64
		if long(le) {
			trig = le.entry.EntryPrice + k*atr
		} else {
			trig = le.entry.EntryPrice - k*atr
		}
		hit := -1
		for j := le.entryIdx; j <= le.entryIdx+w && j < len(le.bars); j++ {
			b := le.bars[j]
			if long(le) && b.High >= trig {
				hit = j
				break
			}
			if !long(le) && b.Low <= trig {
				hit = j
				break
			}
		}
		if hit < 0 {
			skipN++
			rows[i] = r // filled=false
			continue
		}
		e := le.entry
		e.EntryPrice = trig
		tp := ReplayEntry(base, e, le.bars, hit).RealizedPnL
		r.filled = true
		r.trigPnL = tp
		r.hitIdx = hit
		filledTrig += tp
		keptBaseSum += bp
		filledN++
		rows[i] = r
	}

	netD := filledTrig - baseTotal
	// (1) DECOMP
	// benefit of excluding = -(sum of skipped baseline) = -(baseTotal - keptBaseSum)
	exclBenefit := -(baseTotal - keptBaseSum)
	keptDelta := filledTrig - keptBaseSum // compression+shift effect on kept trades only

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("==== TRIGGER-ENTRY VALIDATION: %s  (k=%.2f w=%d) ====\n", trader, k, w))
	sb.WriteString(fmt.Sprintf("  entries=%d  filled=%d  skipped=%d  baseline=%.2f  filledTrig=%.2f  netD=%+.2f\n",
		len(loaded), filledN, skipN, baseTotal, filledTrig, netD))
	sb.WriteString("  [1] DECOMPOSITION -----------------------------------------\n")
	sb.WriteString(fmt.Sprintf("      exclude-skipped benefit : %+.2f   (%.0f%% of netD)\n", exclBenefit, pctOf(exclBenefit, netD)))
	sb.WriteString(fmt.Sprintf("      kept-trades delta       : %+.2f   (%.0f%% of netD)  <- compression+shift; ~0 => pure risk-mgmt, no alpha\n", keptDelta, pctOf(keptDelta, netD)))

	// (2) PLACEBO: remove skipN trades at random, 2000 iterations
	sb.WriteString("  [2] PLACEBO (remove same # at random) ---------------------\n")
	if skipN == 0 {
		sb.WriteString("      no trades skipped; placebo N/A\n")
	} else {
		basePnls := make([]float64, len(rows))
		for i, r := range rows {
			basePnls[i] = r.basePnL
		}
		rng := newLCG(0x9E3779B97F4A7C15)
		const iters = 2000
		results := make([]float64, iters)
		idxBuf := make([]int, len(basePnls))
		for it := 0; it < iters; it++ {
			for i := range idxBuf {
				idxBuf[i] = i
			}
			// partial Fisher-Yates: pick skipN to remove
			removed := 0.0
			for pick := 0; pick < skipN; pick++ {
				j := pick + int(rng.next()%uint64(len(idxBuf)-pick))
				idxBuf[pick], idxBuf[j] = idxBuf[j], idxBuf[pick]
				removed += basePnls[idxBuf[pick]]
			}
			// dPnL of random-skip = -(removed baseline). (kept trades unchanged, no trigger reprice)
			results[it] = -removed
		}
		mean, p5, p95 := statSummary(results)
		// percentile rank of the exclude-only benefit within the placebo cloud
		below := 0
		for _, v := range results {
			if v < exclBenefit {
				below++
			}
		}
		rank := float64(below) / float64(iters) * 100
		sb.WriteString(fmt.Sprintf("      random-skip dPnL: mean=%+.2f  p5=%+.2f  p95=%+.2f\n", mean, p5, p95))
		sb.WriteString(fmt.Sprintf("      real exclude-benefit=%+.2f  -> percentile rank vs random = %.1f%%\n", exclBenefit, rank))
		sb.WriteString("      (rank ~50%% => filter is NO better than random removal; >95%% => genuinely selects losers)\n")
	}

	// (3) OUT-OF-SAMPLE split by time
	sb.WriteString("  [3] OUT-OF-SAMPLE (fit k on 1st half, apply to 2nd) -------\n")
	oos := oosSplitEval(trader, base, loaded, w)
	sb.WriteString(oos)

	// (4) immediate-adverse profile of skipped trades
	sb.WriteString("  [4] SKIPPED-TRADE PROFILE ---------------------------------\n")
	skWin, skLoss := 0, 0
	for _, r := range rows {
		if r.filled {
			continue
		}
		if r.basePnL > 0 {
			skWin++
		} else {
			skLoss++
		}
	}
	sb.WriteString(fmt.Sprintf("      skipped winners=%d  skipped losers=%d  (loser share=%.0f%%)\n",
		skWin, skLoss, pctOf(float64(skLoss), float64(skWin+skLoss))))
	sb.WriteString("      NOTE: high loser-share is near-tautological (a trade that never moved k*ATR in your favor for w bars is a bad trade by construction). Treat this as an adverse-excursion STOP, not alpha.\n")
	return sb.String()
}

func pctOf(x, total float64) float64 {
	if total == 0 {
		return 0
	}
	return x / total * 100
}

// oosSplitEval sweeps k on the first-half (by entry time) and applies the winner
// to the second half, for the given wait window.
func oosSplitEval(trader string, base ProtectionParams, loaded []loadedEntry, w int) string {
	long := func(le loadedEntry) bool { return strings.EqualFold(le.entry.Side, "LONG") }
	// order by entry time
	ord := make([]int, len(loaded))
	for i := range ord {
		ord[i] = i
	}
	for i := 0; i < len(ord); i++ {
		mi := i
		for j := i + 1; j < len(ord); j++ {
			if loaded[ord[j]].entry.EntryTime < loaded[ord[mi]].entry.EntryTime {
				mi = j
			}
		}
		ord[i], ord[mi] = ord[mi], ord[i]
	}
	half := len(ord) / 2
	if half < 5 {
		return "      too few entries for OOS split\n"
	}
	first := ord[:half]
	second := ord[half:]

	evalD := func(idxs []int, k float64) float64 {
		var baseT, trigT float64
		for _, ix := range idxs {
			le := loaded[ix]
			bp := ReplayEntry(base, le.entry, le.bars, le.entryIdx).RealizedPnL
			baseT += bp
			atr := entryATR(le.bars, le.entryIdx)
			if atr <= 0 {
				trigT += bp
				continue
			}
			var trig float64
			if long(le) {
				trig = le.entry.EntryPrice + k*atr
			} else {
				trig = le.entry.EntryPrice - k*atr
			}
			hit := -1
			for j := le.entryIdx; j <= le.entryIdx+w && j < len(le.bars); j++ {
				b := le.bars[j]
				if long(le) && b.High >= trig {
					hit = j
					break
				}
				if !long(le) && b.Low <= trig {
					hit = j
					break
				}
			}
			if hit < 0 {
				continue
			}
			e := le.entry
			e.EntryPrice = trig
			trigT += ReplayEntry(base, e, le.bars, hit).RealizedPnL
		}
		return trigT - baseT
	}

	ks := []float64{0.05, 0.10, 0.15, 0.20, 0.25, 0.30, 0.40, 0.50}
	bestK, bestD := ks[0], evalD(first, ks[0])
	for _, k := range ks[1:] {
		d := evalD(first, k)
		if d > bestD {
			bestD, bestK = d, k
		}
	}
	oosD := evalD(second, bestK)
	inSameSign := (bestD > 0) == (oosD > 0)
	verdict := "HOLDS (same sign)"
	if !inSameSign {
		verdict = "FAILS (sign flip => overfit)"
	}
	return fmt.Sprintf("      best k on 1st-half = %.2f (dPnL=%+.2f) ; applied to 2nd-half dPnL=%+.2f  -> %s\n",
		bestK, bestD, oosD, verdict)
}

// newLCG / next: tiny deterministic PRNG so the placebo is reproducible.
type lcg struct{ s uint64 }

func newLCG(seed uint64) *lcg { return &lcg{s: seed} }
func (l *lcg) next() uint64 {
	l.s = l.s*6364136223846793005 + 1442695040888963407
	return l.s >> 11
}

func statSummary(v []float64) (mean, p5, p95 float64) {
	if len(v) == 0 {
		return 0, 0, 0
	}
	cp := make([]float64, len(v))
	copy(cp, v)
	// insertion-ish sort (iters is small enough at 2000 -> use simple sort)
	for i := 1; i < len(cp); i++ {
		x := cp[i]
		j := i - 1
		for j >= 0 && cp[j] > x {
			cp[j+1] = cp[j]
			j--
		}
		cp[j+1] = x
	}
	var sum float64
	for _, x := range cp {
		sum += x
	}
	mean = sum / float64(len(cp))
	p5 = cp[int(0.05*float64(len(cp)))]
	p95 = cp[int(0.95*float64(len(cp)))]
	return
}
