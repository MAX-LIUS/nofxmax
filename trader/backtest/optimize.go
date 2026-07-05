package backtest

import (
	"fmt"
	"sort"
)

// OptConfig is one fully-specified ATR-mode protection candidate the optimizer
// evaluates. Zero-length TP/BE/DD slices mean that layer is disabled, which is
// how the optimizer tests "turn the unfavourable layer off".
type OptResult struct {
	Label  string
	Params ProtectionParams
	PnL    float64
	WinPct float64
	PF     float64
	MaxDD  float64
	Trades int
}

// scoreOf ranks candidates by a drawdown-aware utility: PnL minus a small
// penalty on drawdown so a config that earns slightly less but with far lower
// risk can win. lambda=0.15 means 1 unit of MaxDD costs 0.15 units of PnL.
func scoreOf(r OptResult, lambda float64) float64 { return r.PnL - lambda*r.MaxDD }

func evalATR(label string, p ProtectionParams, loaded []loadedEntry) OptResult {
	res := RunParams(p, loaded)
	res.Results = nil
	return OptResult{
		Label: label, Params: p, PnL: res.TotalPnL, WinPct: res.WinRatePct,
		PF: res.ProfitFactor, MaxDD: res.MaxDrawdown, Trades: res.Trades,
	}
}

// atrParams builds an ATR-mode ProtectionParams from explicit tiers. nil/empty
// slices disable that layer; slATR<=0 disables the hard stop.
func atrParams(slATR float64, tp []LadderLeg, be []BELeg, dd []DDRule) ProtectionParams {
	return ProtectionParams{
		Unit: UnitATRMult, StopLossATR: slATR,
		TPLegs: tp, BELegs: be, DDRules: dd,
	}
}

// OptimizeProtectionATR runs a STAGED optimisation so each dimension's marginal
// effect is observable (the user asked to see distances, ratios, tiers, and the
// effect of turning unfavourable layers off). Stages, each locking the prior
// winner by drawdown-aware score:
//
//	S1 SL-only: best hard-stop distance (the proven critical guardrail).
//	S2 +TP1: sweep first TP distance × close-ratio (ratio 0 = no TP).
//	S3 +TP2: optional second TP tier distance × ratio (or none).
//	S4 +BE:  break-even trigger × offset × ratio (or none).
//	S5 +DD:  drawdown-runner arm × giveback × ratio (or none).
//	S6 ratio refine: re-sweep the winner's TP close ratios at fine granularity,
//	   since the user flagged close-ratio sensitivity specifically.
//
// Returns the per-stage best (for the narrative) plus the final params.
func OptimizeProtectionATR(loaded []loadedEntry, lambda float64) ([]OptResult, ProtectionParams) {
	var stages []OptResult

	// Reference points for context.
	stages = append(stages, evalATR("REF percent-baseline", ClaudeBaselineParams(), loaded))
	stages = append(stages, evalATR("REF ATR-wide (current best guess)",
		buildATRParams(4.5, 2.0, 7.0, 2.0, 3.5), loaded))

	// ---- S1: SL-only ----
	bestSL, bestSLr := 0.0, OptResult{PnL: -1e18}
	for _, sl := range []float64{1.5, 2.0, 2.5, 3.0, 3.5, 4.0, 4.5, 5.0, 6.0, 7.0, 8.0} {
		r := evalATR(fmt.Sprintf("S1 SL=%.1f only", sl), atrParams(sl, nil, nil, nil), loaded)
		if scoreOf(r, lambda) > scoreOf(bestSLr, lambda) {
			bestSLr, bestSL = r, sl
		}
	}
	stages = append(stages, withTag(bestSLr, "S1*"))

	// ---- S2: + TP1 (distance × ratio); ratio 0 == skip ----
	bestTP1 := []LadderLeg(nil)
	bestS2 := bestSLr
	for _, d := range []float64{1.0, 1.5, 2.0, 2.5, 3.0, 4.0, 5.0} {
		for _, ratio := range []float64{20, 35, 50, 65, 80} {
			tp := []LadderLeg{{ATRMult: d, CloseRatioPct: ratio}}
			r := evalATR(fmt.Sprintf("S2 SL=%.1f TP1=%.1f@%.0f%%", bestSL, d, ratio),
				atrParams(bestSL, tp, nil, nil), loaded)
			if scoreOf(r, lambda) > scoreOf(bestS2, lambda) {
				bestS2, bestTP1 = r, tp
			}
		}
	}
	stages = append(stages, withTag(bestS2, "S2*"))

	// ---- S3: + TP2 (distance × ratio) on top of TP1; or none ----
	bestTP := bestTP1
	bestS3 := bestS2
	if len(bestTP1) == 1 {
		for _, d := range []float64{4.0, 5.0, 6.0, 7.0, 8.0, 10.0, 12.0} {
			if d <= bestTP1[0].ATRMult {
				continue
			}
			for _, ratio := range []float64{15, 25, 35, 50} {
				tp := []LadderLeg{bestTP1[0], {ATRMult: d, CloseRatioPct: ratio}}
				r := evalATR(fmt.Sprintf("S3 +TP2=%.1f@%.0f%%", d, ratio),
					atrParams(bestSL, tp, nil, nil), loaded)
				if scoreOf(r, lambda) > scoreOf(bestS3, lambda) {
					bestS3, bestTP = r, tp
				}
			}
		}
	}
	stages = append(stages, withTag(bestS3, "S3*"))

	// ---- S4: + BE (trigger × offset × ratio); or none ----
	bestBE := []BELeg(nil)
	bestS4 := bestS3
	for _, trig := range []float64{1.0, 1.5, 2.0, 2.5, 3.0} {
		for _, off := range []float64{0.1, 0.3, 0.5} {
			for _, ratio := range []float64{33, 50, 65} {
				be := []BELeg{{TriggerATR: trig, OffsetATR: off, CloseRatioPct: ratio}}
				r := evalATR(fmt.Sprintf("S4 +BE trig=%.1f off=%.1f @%.0f%%", trig, off, ratio),
					atrParams(bestSL, bestTP, be, nil), loaded)
				if scoreOf(r, lambda) > scoreOf(bestS4, lambda) {
					bestS4, bestBE = r, be
				}
			}
		}
	}
	stages = append(stages, withTag(bestS4, "S4*"))

	// ---- S5: + DD runner (arm × giveback × ratio); or none ----
	bestDD := []DDRule(nil)
	bestS5 := bestS4
	for _, arm := range []float64{3.0, 5.0, 7.0} {
		for _, gb := range []float64{30, 40, 50} {
			for _, ratio := range []float64{25, 45, 65} {
				dd := []DDRule{{MinProfitATR: arm, MaxDrawdownPct: gb, CloseRatioPct: ratio}}
				r := evalATR(fmt.Sprintf("S5 +DD arm=%.1f gb=%.0f%% @%.0f%%", arm, gb, ratio),
					atrParams(bestSL, bestTP, bestBE, dd), loaded)
				if scoreOf(r, lambda) > scoreOf(bestS5, lambda) {
					bestS5, bestDD = r, dd
				}
			}
		}
	}
	stages = append(stages, withTag(bestS5, "S5*"))

	// ---- S6: refine TP close ratios on the winner (close-ratio sensitivity) ----
	bestFinal := bestS5
	finalTP := bestTP
	if len(bestTP) >= 1 {
		r1set := []float64{15, 20, 25, 30, 35, 40, 50, 65}
		for _, r1 := range r1set {
			tp := cloneLegs(bestTP)
			tp[0].CloseRatioPct = r1
			if len(tp) == 2 {
				for _, r2 := range []float64{10, 15, 20, 25, 35} {
					tp[1].CloseRatioPct = r2
					r := evalATR(fmt.Sprintf("S6 TP ratios %.0f%%/%.0f%%", r1, r2),
						atrParams(bestSL, tp, bestBE, bestDD), loaded)
					if scoreOf(r, lambda) > scoreOf(bestFinal, lambda) {
						bestFinal, finalTP = r, cloneLegs(tp)
					}
				}
			} else {
				r := evalATR(fmt.Sprintf("S6 TP ratio %.0f%%", r1),
					atrParams(bestSL, tp, bestBE, bestDD), loaded)
				if scoreOf(r, lambda) > scoreOf(bestFinal, lambda) {
					bestFinal, finalTP = r, cloneLegs(tp)
				}
			}
		}
	}
	stages = append(stages, withTag(bestFinal, "FINAL*"))

	return stages, atrParams(bestSL, finalTP, bestBE, bestDD)
}

func cloneLegs(in []LadderLeg) []LadderLeg { return append([]LadderLeg(nil), in...) }
func withTag(r OptResult, tag string) OptResult {
	r.Label = tag + " " + r.Label
	return r
}

// FormatOptStages renders the staged optimisation results.
func FormatOptStages(rows []OptResult) string {
	s := fmt.Sprintf("%-44s | %9s %7s %6s %9s\n", "stage / config", "PnL", "Win%", "PF", "MaxDD")
	for _, r := range rows {
		s += fmt.Sprintf("%-44s | %9.2f %7.1f %6.2f %9.2f\n",
			r.Label, r.PnL, r.WinPct, r.PF, r.MaxDD)
	}
	return s
}

// DescribeParams renders an ATR ProtectionParams compactly for the final report.
func DescribeParams(p ProtectionParams) string {
	s := fmt.Sprintf("SL=%.1fATR", p.StopLossATR)
	for i, l := range p.TPLegs {
		s += fmt.Sprintf(" TP%d=%.1fATR@%.0f%%", i+1, l.ATRMult, l.CloseRatioPct)
	}
	if len(p.TPLegs) == 0 {
		s += " TP=off"
	}
	for i, b := range p.BELegs {
		s += fmt.Sprintf(" BE%d=%.1f/%.1f@%.0f%%", i+1, b.TriggerATR, b.OffsetATR, b.CloseRatioPct)
	}
	if len(p.BELegs) == 0 {
		s += " BE=off"
	}
	for _, d := range p.DDRules {
		s += fmt.Sprintf(" DD=arm%.1f/gb%.0f%%@%.0f%%", d.MinProfitATR, d.MaxDrawdownPct, d.CloseRatioPct)
	}
	if len(p.DDRules) == 0 {
		s += " DD=off"
	}
	return s
}

// sortByScore is a helper used by callers that want a ranked table.
func sortByScore(rows []OptResult, lambda float64) {
	sort.Slice(rows, func(i, j int) bool { return scoreOf(rows[i], lambda) > scoreOf(rows[j], lambda) })
}
