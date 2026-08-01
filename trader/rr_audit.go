package trader

import "math"

// rr_audit.go records the gap between the RR the AI DECLARED and the RR the
// protection orders actually about to be placed IMPLY. Audit only: it computes,
// logs, and returns; it never blocks an entry, resizes a position, or edits a
// plan.
//
// Why this is not already covered. kernel/engine_analysis.go already rejects a
// decision whose declared gross_estimated_rr disagrees with its OWN
// entry/invalidation/first_target by more than 0.05. That catches arithmetic
// lies inside the declaration. It cannot catch the other gap: the declared RR is
// computed from the AI's INTENDED prices, while what reaches the exchange has
// been through tick rounding, ATR-protection clamping, drawdown-tier clamping to
// the RR target, ladder splitting, and the structural fallback. So a decision can
// be perfectly self-consistent and still be placed at a materially different RR.
//
// Two ratios are reported because they answer different questions and each is
// well defined on its own:
//
//   - nearestRR uses the closest stop tier and the closest take-profit tier. This
//     is the direct analogue of the declared number, which is built from
//     "first_target", so it is the one comparable to declaredRR.
//   - weightedRR uses the close-ratio-weighted average of each side. This is the
//     economically real ratio when every tier fills as placed.
//
// Neither is "the" RR: a ladder does not have a single RR. Reporting both, and
// naming which is which, is the honest form.

// rrAuditResult is the outcome of one audit. Valid=false means the inputs could
// not support a comparison, and callers must not log a difference in that case.
type rrAuditResult struct {
	Valid       bool
	DeclaredRR  float64
	NearestRR   float64
	WeightedRR  float64
	NearestDiff float64 // NearestRR - DeclaredRR
	StopTiers   int
	ProfitTiers int
	// Reason explains an invalid result, for the log line.
	Reason string
}

// protectionLegPrices extracts the tier prices and close ratios for one side,
// falling back to the plan's single-price fields when no ladder is present.
func protectionLegPrices(orders []ProtectionOrder, singlePrice float64) (prices, ratios []float64) {
	for _, o := range orders {
		if o.Price > 0 && !math.IsNaN(o.Price) && !math.IsInf(o.Price, 0) {
			r := o.CloseRatioPct
			if r <= 0 || math.IsNaN(r) || math.IsInf(r, 0) {
				r = 0
			}
			prices = append(prices, o.Price)
			ratios = append(ratios, r)
		}
	}
	if len(prices) == 0 && singlePrice > 0 && !math.IsNaN(singlePrice) && !math.IsInf(singlePrice, 0) {
		prices = append(prices, singlePrice)
		ratios = append(ratios, 100)
	}
	return prices, ratios
}

// nearestByDistance returns the price closest to entry.
func nearestByDistance(prices []float64, entry float64) float64 {
	best, bestDist := 0.0, math.Inf(1)
	for _, p := range prices {
		d := math.Abs(p - entry)
		if d < bestDist {
			best, bestDist = p, d
		}
	}
	return best
}

// weightedMean returns the ratio-weighted mean of prices. When the ratios carry
// no information (all zero) it degrades to a plain mean rather than returning
// nothing, because an unweighted ladder is still informative.
func weightedMean(prices, ratios []float64) float64 {
	var num, den float64
	for i, p := range prices {
		w := 0.0
		if i < len(ratios) {
			w = ratios[i]
		}
		num += p * w
		den += w
	}
	if den > 0 {
		return num / den
	}
	var sum float64
	for _, p := range prices {
		sum += p
	}
	if len(prices) == 0 {
		return 0
	}
	return sum / float64(len(prices))
}

// auditPlanRiskReward compares the declared RR against what the plan implies.
// Pure: no I/O, no mutation. entry must be the exchange-confirmed fill price.
func auditPlanRiskReward(plan *ProtectionPlan, entry, declaredRR float64) rrAuditResult {
	if plan == nil {
		return rrAuditResult{Reason: "no plan"}
	}
	if entry <= 0 || math.IsNaN(entry) || math.IsInf(entry, 0) {
		return rrAuditResult{Reason: "no entry price"}
	}
	if declaredRR <= 0 || math.IsNaN(declaredRR) || math.IsInf(declaredRR, 0) {
		return rrAuditResult{Reason: "no declared rr"}
	}

	slPrices, slRatios := protectionLegPrices(plan.StopLossOrders, plan.StopLossPrice)
	tpPrices, tpRatios := protectionLegPrices(plan.TakeProfitOrders, plan.TakeProfitPrice)
	if len(slPrices) == 0 {
		return rrAuditResult{Reason: "no stop price in plan"}
	}
	if len(tpPrices) == 0 {
		return rrAuditResult{Reason: "no target price in plan"}
	}

	res := rrAuditResult{
		DeclaredRR:  declaredRR,
		StopTiers:   len(slPrices),
		ProfitTiers: len(tpPrices),
	}

	nearRisk := math.Abs(entry - nearestByDistance(slPrices, entry))
	nearReward := math.Abs(nearestByDistance(tpPrices, entry) - entry)
	if nearRisk <= 0 {
		return rrAuditResult{Reason: "stop sits at entry"}
	}
	res.NearestRR = nearReward / nearRisk

	wRisk := math.Abs(entry - weightedMean(slPrices, slRatios))
	wReward := math.Abs(weightedMean(tpPrices, tpRatios) - entry)
	if wRisk > 0 {
		res.WeightedRR = wReward / wRisk
	}

	if math.IsNaN(res.NearestRR) || math.IsInf(res.NearestRR, 0) {
		return rrAuditResult{Reason: "non-finite recomputed rr"}
	}
	res.NearestDiff = res.NearestRR - res.DeclaredRR
	res.Valid = true
	return res
}

// rrAuditMaterialGap is the difference above which the gap is worth a log line.
// Set to the same 0.05 the kernel uses for its self-consistency check, so the two
// thresholds cannot drift apart and produce contradictory verdicts.
const rrAuditMaterialGap = 0.05
