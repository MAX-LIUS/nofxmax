package backtest

import (
	"fmt"
	"strings"
	"time"

	"nofx/market"
)

// struct_tf_sweep.go answers ONE question cleanly: does sourcing the structural
// close-confirm stop from a TIGHTER timeframe (15m/30m) vs the live 1h improve the
// account, and are the trades it cuts EARLIER net winners (false kills) or losers
// (real saves)?
//
// The confound in a whole-engine -tf 15m replay is that a smaller timeframe also
// shrinks ATR, which tightens TP/BE/DD/backstop TOO — so you're testing a different
// strategy, not "just the structural stop". This sweep removes that confound:
//   - Engine mechanics (ATR, TP, BE, DD, backstop) all run on 1h bars (aggregated
//     from the same 15m fetch), identical across every variant.
//   - ONLY the close-confirm structural boundary changes, computed from the swing
//     window of a chosen source timeframe (15m / 30m / 1h) and injected via
//     Entry.StructBoundaryOverride. The boundary is clamped to the SAME [floor,
//     backstop] band using the 1h ATR, so tighter structure can't escape the band.
// The single free variable is therefore the structural stop's source timeframe.

// structTFVariant is one source-timeframe to test, as a multiple of the base fetch TF.
type structTFVariant struct {
	label string
	mult  int
}

// structTFPlan generalizes the isolation harness across traders with different native
// timeframes. base is the fetch granularity; refMult aggregates base bars up to the
// trader's NATIVE (reference) timeframe on which every non-structural lever (ATR/TP/
// BE/DD/backstop) is pinned; variants are the structural-boundary source TFs to sweep,
// each a multiple of the base. Defaults reproduce the original 15m-base / 1h-ref setup
// for the three 1h traders. claude-r (15m native) overrides to base=5m, refMult=3.
type structTFPlan struct {
	baseTF       string
	basePeriodMs int64
	refMult      int
	variants     []structTFVariant
}

// activeStructTFPlan is set by the CLI before the structtf functions run. Single-
// threaded research use, so package-level config avoids churning every signature.
var activeStructTFPlan = structTFPlan{
	baseTF:       "15m",
	basePeriodMs: 15 * 60 * 1000,
	refMult:      4,
	variants:     []structTFVariant{{"1h-ref (mult4)", 4}, {"30m (mult2)", 2}, {"15m (mult1)", 1}},
}

// tfPeriodMs maps a base-timeframe multiple to its millisecond period, anchoring
// aggregation to the wall clock, using the active plan's base period.
func tfPeriodMs(mult int) int64 {
	return int64(mult) * activeStructTFPlan.basePeriodMs
}

// SetStructTFPlan configures the isolation harness for a trader's native timeframe.
// It returns the base fetch TF the CLI must load (with enough pre-entry bars) and
// whether the native TF is supported. The reference (native) TF pins ATR/TP/BE/DD/
// backstop; the sweep tests the structural boundary sourced from the native TF and
// two tighter granularities.
//   1h native  → base 15m, ref mult4(1h); sweep 1h / 30m / 15m
//   15m native → base 5m,  ref mult3(15m); sweep 15m / 10m / 5m
func SetStructTFPlan(nativeTF string) (baseTF string, ok bool) {
	switch nativeTF {
	case "1h":
		activeStructTFPlan = structTFPlan{
			baseTF: "15m", basePeriodMs: 15 * 60 * 1000, refMult: 4,
			variants: []structTFVariant{{"1h-ref (mult4)", 4}, {"30m (mult2)", 2}, {"15m (mult1)", 1}},
		}
		return "15m", true
	case "15m":
		activeStructTFPlan = structTFPlan{
			baseTF: "5m", basePeriodMs: 5 * 60 * 1000, refMult: 3,
			variants: []structTFVariant{{"15m-ref (mult3)", 3}, {"10m (mult2)", 2}, {"5m (mult1)", 1}},
		}
		return "5m", true
	default:
		return "", false
	}
}

// StructTFNativeLabel returns the reference timeframe label for report headers.
func StructTFNativeLabel() string {
	if len(activeStructTFPlan.variants) > 0 {
		return activeStructTFPlan.variants[0].label
	}
	return "ref"
}

// aggregateByClock groups 15m base bars into higher-timeframe bars anchored to the
// WALL CLOCK (bucket = OpenTime / periodMs), NOT to base index 0. Index-anchored
// aggregation (aggregateHigherTF) gives phase-shifted "1h" bars whose OpenTimes
// straddle real hour boundaries; when such bars are used as a replay substrate the
// entry-index and ExitTime-clamp math misfires and short trades collapse to a single
// mark-to-market bar at entry. Clock-anchored grouping fixes that so every variant's
// bars line up on true period boundaries. Only fully-covered buckets are emitted in
// order; a bucket with any member bar is kept (partial trailing buckets included so
// the forward path isn't truncated).
func aggregateByClock(base []market.Kline, mult int) []market.Kline {
	if mult <= 1 || len(base) == 0 {
		return base
	}
	periodMs := tfPeriodMs(mult)
	var out []market.Kline
	var cur *market.Kline
	curBucket := int64(-1)
	for _, b := range base {
		bucket := b.OpenTime / periodMs
		if cur == nil || bucket != curBucket {
			if cur != nil {
				out = append(out, *cur)
			}
			nb := market.Kline{
				OpenTime: bucket * periodMs, Open: b.Open, High: b.High,
				Low: b.Low, Close: b.Close, CloseTime: b.CloseTime,
			}
			cur = &nb
			curBucket = bucket
			continue
		}
		if b.High > cur.High {
			cur.High = b.High
		}
		if b.Low < cur.Low {
			cur.Low = b.Low
		}
		cur.Close = b.Close
		cur.CloseTime = b.CloseTime
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

// structBoundaryFromTF computes the entry-frozen structural boundary from the
// pre-entry swing window of bars aggregated to `mult`× the base (15m) timeframe,
// then clamps the entry→boundary distance to [floor, backstop] ATR using atr1h.
// Returns ok=false when no structure exists on the protective side.
func structBoundaryFromTF(p ProtectionParams, e Entry, base15m []market.Kline, entryTimeMs int64, atr1h float64, isLong bool, mult int) (float64, bool) {
	tfBars := aggregateByClock(base15m, mult)
	if len(tfBars) < 4 {
		return 0, false
	}
	// entry index on the aggregated series = first bar whose open is > entryTime, minus 1.
	entryIdx := 0
	for i, b := range tfBars {
		if b.OpenTime > entryTimeMs {
			break
		}
		entryIdx = i
	}
	if entryIdx < 2 {
		return 0, false
	}
	// Reuse the live-faithful nearest-swing rule on this timeframe's window.
	raw, ok := rangeStructuralBoundary(p, e, tfBars, entryIdx, isLong)
	if !ok || raw <= 0 {
		return 0, false
	}
	// Clamp with the 1h ATR (engine ATR), NOT this timeframe's ATR — so the only
	// thing that changed is WHERE the raw structural level sits.
	entry := e.EntryPrice
	dist := entry - raw
	if dist < 0 {
		dist = -dist
	}
	m := dist / atr1h
	if p.RangeSLBackstopATR > 0 && m > p.RangeSLBackstopATR {
		m = p.RangeSLBackstopATR // beyond backstop: park at backstop (no fallback RR games here)
	}
	if p.RangeSLFloorATR > 0 && m < p.RangeSLFloorATR {
		m = p.RangeSLFloorATR
	}
	distPct := m * atr1h / entry * 100
	return priceAtDistance(entry, distPct, isLong, false /*adverse*/), true
}

// StructTFStat summarizes one source-timeframe variant vs the 1h-structure reference.
// The decomposition splits every trade whose realized PnL CHANGED vs the 1h reference
// into two clean money buckets by what the trade was UNDER 1h: a winner or a loser.
//   WinnerDeltaPnL = Σ (variantPnL - refPnL) over trades that were WINNERS under 1h.
//     Expected negative if the tighter stop clips winners (false kills).
//   LoserDeltaPnL  = Σ (variantPnL - refPnL) over trades that were LOSERS  under 1h.
//     Expected positive if the tighter stop genuinely cuts losers earlier (real save).
//   WinnerDeltaPnL + LoserDeltaPnL == DeltaPnL (identity — the whole effect is split).
type StructTFStat struct {
	Label        string
	Mult         int
	TotalPnL     float64
	DeltaPnL     float64 // vs 1h-structure reference
	WinRatePct   float64
	StructClosed int     // trades exited by the structural close-confirm stop
	BoundaryOK   int     // entries where this TF produced a structural boundary
	MedBoundPct  float64 // median entry→boundary distance (% of entry)
	// Risk metrics (the drawdown question) — same equity-curve methodology as Aggregate.
	MaxDrawdown float64 // peak-to-trough of the cumulative realized-PnL equity curve
	GrossLoss   float64 // Σ |PnL| over losing trades (total money lost on losers)
	AvgLoss     float64 // mean PnL of losing trades (negative)
	WorstLoss   float64 // single most-negative realized PnL
	WorstSymbol string  // symbol of the worst-loss trade
	WorstEntry  int64   // entry time (ms) of the worst-loss trade
	WorstWhy    string  // close reason of the worst-loss trade
	LoserCount  int     // number of losing trades
	// Per-trade decomposition vs the 1h reference (matched by entry index):
	ChangedN       int     // trades whose realized PnL differed from the 1h ref
	WinnersTouched int     // of ChangedN: ref trade was a WINNER
	LosersTouched  int     // of ChangedN: ref trade was a LOSER (incl. breakeven)
	WinnerDeltaPnL float64 // Σ delta on ref WINNERS (clip cost; expect < 0)
	LoserDeltaPnL  float64 // Σ delta on ref LOSERS  (loss saved; expect > 0)
}

// median helper for boundary-distance summary.
func medianOf(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cp := append([]float64(nil), xs...)
	for i := 1; i < len(cp); i++ {
		for j := i; j > 0 && cp[j] < cp[j-1]; j-- {
			cp[j], cp[j-1] = cp[j-1], cp[j]
		}
	}
	return cp[len(cp)/2]
}

// ComputeStructTFStats replays the base config over 1h-aggregated bars three ways:
// structural boundary sourced from 1h (reference), 30m, and 15m. Every other lever
// is identical. It matches trades by index and splits each per-trade delta by whether
// the 1h-reference trade was a winner or loser.
func ComputeStructTFStats(base ProtectionParams, loaded15m []loadedEntry) []StructTFStat {
	variants := activeStructTFPlan.variants
	refMult := activeStructTFPlan.refMult

	// For each entry, pin the 1h ATR once (from 1h-aggregated bars). This ATR is
	// injected into EVERY variant so the ATR-derived level distances never change —
	// only the structural stop's source timeframe (and its natural close-confirm
	// resolution) does. Entries that can't produce a valid 1h ATR are dropped.
	type prepped struct {
		e       Entry
		atr1h   float64
		isLong  bool
		base15m []market.Kline
		entryMs int64
	}
	preps := make([]prepped, 0, len(loaded15m))
	for _, le := range loaded15m {
		bars1h := aggregateByClock(le.bars, refMult)
		if len(bars1h) < atrLookback+2 {
			continue
		}
		e := le.entry
		entryMs := e.EntryTime
		idx := 0
		for i, b := range bars1h {
			if b.OpenTime > entryMs {
				break
			}
			idx = i
		}
		if idx < atrLookback {
			continue
		}
		h, l, c := sliceOHLC(bars1h[:idx], idx-1)
		atr1h := wilderATR(h, l, c, atrLookback)
		if atr1h <= 0 {
			continue
		}
		preps = append(preps, prepped{e, atr1h, strings.EqualFold(e.Side, "long"), le.bars, entryMs})
	}

	// Reference results (1h structure) captured for per-trade diffing.
	refPnL := make([]float64, len(preps))
	var stats []StructTFStat
	for _, v := range variants {
		st := StructTFStat{Label: v.label, Mult: v.mult}
		var boundPcts []float64
		var wins, closedN int
		results := make([]float64, len(preps))
		for i, pr := range preps {
			e := pr.e
			e.ATROverride = pr.atr1h // pin ATR across granularities
			// Replay on THIS variant's own granularity so the close-confirm fires at
			// its natural resolution (15m variant → 15m closes, etc.).
			vbars := aggregateByClock(pr.base15m, v.mult)
			vidx := 0
			for j, b := range vbars {
				if b.OpenTime > pr.entryMs {
					break
				}
				vidx = j
			}
			if b, ok := structBoundaryFromTF(base, e, pr.base15m, pr.entryMs, pr.atr1h, pr.isLong, v.mult); ok {
				e.StructBoundaryOverride = b
				st.BoundaryOK++
				d := b - e.EntryPrice
				if d < 0 {
					d = -d
				}
				boundPcts = append(boundPcts, d/e.EntryPrice*100)
			}
			r := ReplayEntry(base, e, vbars, vidx)
			results[i] = r.RealizedPnL
			st.TotalPnL += r.RealizedPnL
			if r.RealizedPnL > 0 {
				wins++
			}
			for _, cr := range r.CloseReasons {
				if cr == "structural_sl" {
					closedN++
					break
				}
			}
			// Per-trade decomposition vs reference (the refMult variant IS the ref).
			if v.mult == refMult {
				refPnL[i] = r.RealizedPnL
			} else {
				delta := r.RealizedPnL - refPnL[i]
				if delta > 1e-9 || delta < -1e-9 {
					st.ChangedN++
					// Split the whole delta by what the trade was UNDER 1h. This is an
					// exact partition: winner-bucket + loser-bucket == total DeltaPnL.
					if refPnL[i] > 0 {
						st.WinnersTouched++
						st.WinnerDeltaPnL += delta
					} else {
						st.LosersTouched++
						st.LoserDeltaPnL += delta
					}
				}
			}
		}
		if len(preps) > 0 {
			st.WinRatePct = float64(wins) / float64(len(preps)) * 100
		}
		st.StructClosed = closedN
		st.MedBoundPct = medianOf(boundPcts)
		// Risk metrics on this variant's equity curve (entry order, same as Aggregate).
		var equity, peakEquity, maxDD float64
		for _, pnl := range results {
			if pnl < 0 {
				st.LoserCount++
				st.GrossLoss += -pnl
				if pnl < st.WorstLoss {
					st.WorstLoss = pnl
				}
			}
			equity += pnl
			if equity > peakEquity {
				peakEquity = equity
			}
			if dd := peakEquity - equity; dd > maxDD {
				maxDD = dd
			}
		}
		st.MaxDrawdown = maxDD
		if st.LoserCount > 0 {
			st.AvgLoss = -st.GrossLoss / float64(st.LoserCount)
		}
		stats = append(stats, st)
	}
	// fill DeltaPnL vs ref (index 0)
	if len(stats) > 0 {
		ref := stats[0].TotalPnL
		for i := range stats {
			stats[i].DeltaPnL = stats[i].TotalPnL - ref
		}
	}
	return stats
}

// FormatStructTFStats renders the isolation table.
func FormatStructTFStats(base ProtectionParams, loaded15m []loadedEntry) string {
	stats := ComputeStructTFStats(base, loaded15m)
	var b strings.Builder
	nat := StructTFNativeLabel()
	b.WriteString(fmt.Sprintf("==== STRUCTURAL-TF ISOLATION (only the stop's source TF changes; ATR/TP/BE/DD/backstop all on native %s) ====\n", nat))
	b.WriteString(fmt.Sprintf("%-16s %-10s %-8s %-7s %-9s %-9s | %-9s %-9s %-9s %-13s %-13s\n",
		"structTF", "TotalPnL", "dPnL", "Win%", "structX", "medBnd%",
		"changedN", "onWinner", "onLoser", "winrDelta$", "losrDelta$"))
	for _, s := range stats {
		b.WriteString(fmt.Sprintf("%-16s %-10.2f %-+8.2f %-7.1f %-9d %-9.2f | %-9d %-9d %-9d %-+13.2f %-+13.2f\n",
			s.Label, s.TotalPnL, s.DeltaPnL, s.WinRatePct, s.StructClosed, s.MedBoundPct,
			s.ChangedN, s.WinnersTouched, s.LosersTouched, s.WinnerDeltaPnL, s.LoserDeltaPnL))
	}
	b.WriteString(fmt.Sprintf("→ Reference = native %s structure. dPnL>0 means the tighter TF made MORE money.\n", nat))
	b.WriteString(fmt.Sprintf("  winrDelta$ = Σ PnL change on trades that WON under %s (clip cost; expect <0).\n", nat))
	b.WriteString(fmt.Sprintf("  losrDelta$ = Σ PnL change on trades that LOST under %s (real save; expect >0).\n", nat))
	b.WriteString("  Identity: winrDelta$ + losrDelta$ == dPnL (the whole effect, partitioned).\n")
	// RISK view — the drawdown question, isolated (only the structural stop's TF changes).
	b.WriteString("\n---- RISK METRICS (same isolation; equity-curve drawdown & loss profile) ----\n")
	b.WriteString(fmt.Sprintf("%-16s %-10s %-11s %-9s %-11s %-10s %-11s\n",
		"structTF", "TotalPnL", "MaxDrawdn", "losers", "grossLoss", "avgLoss", "worstLoss"))
	for _, s := range stats {
		b.WriteString(fmt.Sprintf("%-16s %-10.2f %-11.2f %-9d %-11.2f %-10.3f %-11.3f\n",
			s.Label, s.TotalPnL, s.MaxDrawdown, s.LoserCount, s.GrossLoss, s.AvgLoss, s.WorstLoss))
	}
	b.WriteString("→ MaxDrawdn = peak-to-trough of cumulative realized PnL (quote ccy), trades in entry order.\n")
	b.WriteString("  This answers: does a TIGHTER structural stop cut drawdown? (ATR-shrink confound removed.)\n")
	return b.String()
}

// StructTFForensicRow is one trade's side-by-side 1h-ref vs a tighter-TF variant.
type StructTFForensicRow struct {
	Symbol   string
	Side     string
	RefPnL   float64
	VarPnL   float64
	Delta    float64
	RefBars  int
	VarBars  int
	RefBnd   float64
	VarBnd   float64
	RefExit  float64
	VarExit  float64
	RefWhy   string
	VarWhy   string
}

// FormatStructTFForensic replays the 1h reference and the 15m variant, matches by
// entry, and dumps the worst-delta LOSER-bucket trades (ref was a loser, variant did
// worse) so a human can see EXACTLY why — real whipsaw vs a harness artifact (e.g. a
// degenerate ref that marks-to-market at entry with no boundary). topN worst rows.
func FormatStructTFForensic(base ProtectionParams, loaded15m []loadedEntry, topN int) string {
	// Build the shared prep the same way ComputeStructTFStats does.
	type prepped struct {
		e       Entry
		atr1h   float64
		isLong  bool
		base15m []market.Kline
		entryMs int64
	}
	var preps []prepped
	for _, le := range loaded15m {
		bars1h := aggregateByClock(le.bars, 4)
		if len(bars1h) < atrLookback+2 {
			continue
		}
		e := le.entry
		idx := 0
		for i, b := range bars1h {
			if b.OpenTime > e.EntryTime {
				break
			}
			idx = i
		}
		if idx < atrLookback {
			continue
		}
		h, l, c := sliceOHLC(bars1h[:idx], idx-1)
		atr1h := wilderATR(h, l, c, atrLookback)
		if atr1h <= 0 {
			continue
		}
		preps = append(preps, prepped{e, atr1h, strings.EqualFold(e.Side, "long"), le.bars, e.EntryTime})
	}

	replayAt := func(pr prepped, mult int) (float64, int, float64, float64, string) {
		e := pr.e
		e.ATROverride = pr.atr1h
		vbars := aggregateByClock(pr.base15m, mult)
		vidx := 0
		for j, b := range vbars {
			if b.OpenTime > pr.entryMs {
				break
			}
			vidx = j
		}
		bnd := 0.0
		if b, ok := structBoundaryFromTF(base, e, pr.base15m, pr.entryMs, pr.atr1h, pr.isLong, mult); ok {
			e.StructBoundaryOverride = b
			bnd = b
		}
		r := ReplayEntry(base, e, vbars, vidx)
		why := ""
		if len(r.CloseReasons) > 0 {
			why = r.CloseReasons[len(r.CloseReasons)-1]
		}
		return r.RealizedPnL, r.BarsHeld, bnd, r.ExitPrice, why
	}

	var rows []StructTFForensicRow
	for _, pr := range preps {
		refPnL, refBars, refBnd, refExit, refWhy := replayAt(pr, 4)
		varPnL, varBars, varBnd, varExit, varWhy := replayAt(pr, 1)
		delta := varPnL - refPnL
		if refPnL <= 0 && delta < -1e-9 { // loser bucket, variant did worse
			rows = append(rows, StructTFForensicRow{
				pr.e.Symbol, pr.e.Side, refPnL, varPnL, delta, refBars, varBars,
				refBnd, varBnd, refExit, varExit, refWhy, varWhy,
			})
		}
	}
	// sort by most-negative delta
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].Delta < rows[j-1].Delta; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("==== STRUCT-TF FORENSIC: 15m-worse-on-losers (worst %d of %d) ====\n", topN, len(rows)))
	b.WriteString(fmt.Sprintf("%-12s %-5s %-8s %-8s %-8s %-6s %-6s %-11s %-11s %-9s %-9s\n",
		"symbol", "side", "refPnL", "15mPnL", "delta", "refN", "15mN", "refWhy", "15mWhy", "refBnd", "15mBnd"))
	for i, r := range rows {
		if i >= topN {
			break
		}
		b.WriteString(fmt.Sprintf("%-12s %-5s %-+8.3f %-+8.3f %-+8.3f %-6d %-6d %-11s %-11s %-9.4f %-9.4f\n",
			r.Symbol, r.Side, r.RefPnL, r.VarPnL, r.Delta, r.RefBars, r.VarBars,
			r.RefWhy, r.VarWhy, r.RefBnd, r.VarBnd))
	}
	b.WriteString("→ refN=1 & refWhy=mark_to_market & refBnd=0 ⇒ DEGENERATE ref (harness artifact).\n")
	b.WriteString("  refN large & both structural_sl at different prices ⇒ real whipsaw/mechanism.\n")
	return b.String()
}

// TraceWorstLoss finds the single worst-loss trade under the 1h-ref structural stop
// (mult4, ATR pinned) and dumps a BAR-BY-BAR trace: for each bar it prints O/H/L/C
// plus the SL/backstop price and the close-confirm boundary, and flags which stop
// SHOULD have fired. This is the ground-truth debugger for "SL & backstop both exist
// but neither triggered — only mark_to_market did". It replays exactly like the
// isolation (aggregateByClock to 1h, ATROverride, StructBoundaryOverride).
func TraceWorstLoss(base ProtectionParams, loaded15m []loadedEntry) string {
	type prepped struct {
		e       Entry
		atr1h   float64
		isLong  bool
		bars1h  []market.Kline
		base15m []market.Kline
		idx     int
	}
	var preps []prepped
	for _, le := range loaded15m {
		bars1h := aggregateByClock(le.bars, 4)
		if len(bars1h) < atrLookback+2 {
			continue
		}
		e := le.entry
		idx := 0
		for i, b := range bars1h {
			if b.OpenTime > e.EntryTime {
				break
			}
			idx = i
		}
		if idx < atrLookback {
			continue
		}
		h, l, c := sliceOHLC(bars1h[:idx], idx-1)
		atr1h := wilderATR(h, l, c, atrLookback)
		if atr1h <= 0 {
			continue
		}
		preps = append(preps, prepped{e, atr1h, strings.EqualFold(e.Side, "long"), bars1h, le.bars, idx})
	}

	// find worst-loss trade under 1h-ref
	worst := -1
	worstPnL := 0.0
	for i, pr := range preps {
		e := pr.e
		e.ATROverride = pr.atr1h
		if b, ok := structBoundaryFromTF(base, e, pr.base15m, pr.e.EntryTime, pr.atr1h, pr.isLong, 4); ok {
			e.StructBoundaryOverride = b
		}
		r := ReplayEntry(base, e, pr.bars1h, pr.idx)
		if r.RealizedPnL < worstPnL {
			worstPnL = r.RealizedPnL
			worst = i
		}
	}
	if worst < 0 {
		return "TraceWorstLoss: no trades\n"
	}

	pr := preps[worst]
	e := pr.e
	e.ATROverride = pr.atr1h
	// recompute boundary + backstop exactly as ReplayEntry would
	atr := pr.atr1h
	isLong := pr.isLong
	var confirmBoundary, slPrice float64
	if b, ok := structBoundaryFromTF(base, e, pr.base15m, e.EntryTime, atr, isLong, 4); ok {
		confirmBoundary = b
	}
	backDist := base.RangeSLBackstopATR
	if backDist <= 0 {
		backDist = 4.5
	}
	slPrice = priceAtDistance(e.EntryPrice, backDist*atr/e.EntryPrice*100, isLong, false)

	var b strings.Builder
	fmt.Fprintf(&b, "==== WORST-LOSS BAR TRACE: %s %s entry=%.4f qty=%.4f ====\n", e.Symbol, e.Side, e.EntryPrice, e.Quantity)
	// real exit price backed out from realized_pnl
	realExit := 0.0
	if e.Quantity > 0 {
		retPct := e.RealizedPnL / (e.EntryPrice * e.Quantity) * 100
		if isLong {
			realExit = e.EntryPrice * (1 + retPct/100)
		} else {
			realExit = e.EntryPrice * (1 - retPct/100)
		}
	}
	fmt.Fprintf(&b, "atr=%.4f  backstop(SL)=%.4f  confirmBoundary=%.4f  replayPnL=%.3f  realPnL=%.2f realExit≈%.4f why=%s\n",
		atr, slPrice, confirmBoundary, worstPnL, e.RealizedPnL, realExit, e.CloseReason)
	fmt.Fprintf(&b, "entryIdx=%d totalBars=%d exitTime=%d\n", pr.idx, len(pr.bars1h), e.ExitTime)
	fmt.Fprintf(&b, "%-5s %-19s %-9s %-9s %-9s %-9s | %-9s %-9s %-10s\n",
		"i", "openTime", "open", "high", "low", "close", "SLhit?", "confHit?", "note")
	endIdx := len(pr.bars1h) - 1
	for i := pr.idx; i <= endIdx; i++ {
		bar := pr.bars1h[i]
		past := e.ExitTime > 0 && bar.OpenTime > e.ExitTime
		slHit := (isLong && bar.Low <= slPrice) || (!isLong && bar.High >= slPrice)
		confHit := confirmBoundary > 0 && ((isLong && bar.Close < confirmBoundary) || (!isLong && bar.Close > confirmBoundary))
		note := ""
		if past {
			note = "<<past exitTime (loop breaks here)"
		}
		fmt.Fprintf(&b, "%-5d %-19s %-9.4f %-9.4f %-9.4f %-9.4f | %-9v %-9v %-10s\n",
			i, msToStr(bar.OpenTime), bar.Open, bar.High, bar.Low, bar.Close, slHit, confHit, note)
		if past {
			break
		}
	}
	return b.String()
}

func msToStr(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04")
}

// FormatEntryPriceSanity histograms how far each loaded entry's recorded entry_price
// sits from the actual bar price at its entry index. A large mismatch (BTC entry
// 75227 while the 1h bar traded 76715-77249) means the DB entry_price and the fetched
// bars disagree — the backstop/SL then fires on a phantom level and fabricates losses
// that pollute worstLoss/grossLoss/maxDrawdown. This quantifies how much of the risk
// table is contaminated. Uses the 1h-aggregated bars, same substrate as the isolation.
func FormatEntryPriceSanity(loaded15m []loadedEntry) string {
	buckets := []struct {
		lo, hi float64
		n      int
	}{{0, 0.1, 0}, {0.1, 0.25, 0}, {0.25, 0.5, 0}, {0.5, 1, 0}, {1, 2, 0}, {2, 5, 0}, {5, 1e9, 0}}
	var worst []string
	total := 0
	over1 := 0
	for _, le := range loaded15m {
		bars1h := aggregateByClock(le.bars, 4)
		e := le.entry
		idx := 0
		for i, b := range bars1h {
			if b.OpenTime > e.EntryTime {
				break
			}
			idx = i
		}
		if idx < 0 || idx >= len(bars1h) {
			continue
		}
		bar := bars1h[idx]
		// distance from entry_price to the bar's traded range (0 if inside [low,high]).
		dev := 0.0
		if e.EntryPrice > bar.High {
			dev = (e.EntryPrice - bar.High) / e.EntryPrice * 100
		} else if e.EntryPrice < bar.Low {
			dev = (bar.Low - e.EntryPrice) / e.EntryPrice * 100
		}
		total++
		for i := range buckets {
			if dev >= buckets[i].lo && dev < buckets[i].hi {
				buckets[i].n++
				break
			}
		}
		if dev >= 1 {
			over1++
			if len(worst) < 15 {
				worst = append(worst, fmt.Sprintf("  %s %s entry=%.4f barRange=[%.4f,%.4f] dev=%.2f%% t=%s",
					e.Symbol, e.Side, e.EntryPrice, bar.Low, bar.High, dev, msToStr(e.EntryTime)))
			}
		}
	}
	var b strings.Builder
	b.WriteString("==== ENTRY-PRICE vs BAR SANITY (entry_price outside the entry bar's [low,high]) ====\n")
	b.WriteString(fmt.Sprintf("total=%d  outside-by->=1%%: %d (%.1f%%)\n", total, over1, float64(over1)/float64(maxIntLocal(total, 1))*100))
	for _, bk := range buckets {
		hi := fmt.Sprintf("%.2f", bk.hi)
		if bk.hi > 1e8 {
			hi = "inf"
		}
		b.WriteString(fmt.Sprintf("  dev [%.2f, %s)%%  : %d\n", bk.lo, hi, bk.n))
	}
	if len(worst) > 0 {
		b.WriteString("worst offenders (dev>=1%, entry_price OUTSIDE the bar range — phantom fills):\n")
		for _, w := range worst {
			b.WriteString(w + "\n")
		}
	}
	b.WriteString("→ dev=0 means entry_price sits INSIDE the bar (healthy). Large dev ⇒ DB price and\n")
	b.WriteString("  fetched bars disagree; SL/backstop fires on a phantom level → fabricated loss.\n")
	return b.String()
}

func maxIntLocal(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// FilterAlignedEntries drops entries whose recorded entry_price sits OUTSIDE the
// entry bar's [low,high] by more than maxDevPct — phantom fills where the DB price
// and the fetched bars disagree (e.g. a stale/garbled entry_price). Such trades make
// the SL/backstop fire on a level the market never traded, fabricating losses that
// pollute worstLoss/grossLoss/maxDrawdown. Returns the clean slice and the drop count.
func FilterAlignedEntries(loaded15m []loadedEntry, maxDevPct float64) ([]loadedEntry, int) {
	var out []loadedEntry
	dropped := 0
	for _, le := range loaded15m {
		bars1h := aggregateByClock(le.bars, 4)
		e := le.entry
		idx := 0
		for i, b := range bars1h {
			if b.OpenTime > e.EntryTime {
				break
			}
			idx = i
		}
		if idx < 0 || idx >= len(bars1h) {
			dropped++
			continue
		}
		bar := bars1h[idx]
		dev := 0.0
		if e.EntryPrice > bar.High {
			dev = (e.EntryPrice - bar.High) / e.EntryPrice * 100
		} else if e.EntryPrice < bar.Low {
			dev = (bar.Low - e.EntryPrice) / e.EntryPrice * 100
		}
		if dev > maxDevPct {
			dropped++
			continue
		}
		out = append(out, le)
	}
	return out, dropped
}
