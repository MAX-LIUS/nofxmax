package trader

import (
	"fmt"
	"math"

	"nofx/logger"
)

// rr_audit.go records the gap between the RR the AI DECLARED and the RR the
// PLANNED protection ladder implies. Audit only: it computes, logs, and returns;
// it never blocks an entry, resizes a position, or edits a plan.
//
// Two plans are audited, not one (2026-08-01). auditPlanRiskReward itself is
// pure and knows nothing about the exchange, so the CALLER runs it twice: once on
// the planned ladder and once on the ladder validateProtectionPlanExecution says
// is actually placeable. Reporting only the planned figure hid a real defect —
// ZECUSDT placed 48.3% of a 65% ladder and the planned audit could not show it,
// because dropping the FAR tier leaves the nearest RR unchanged. Hence
// ProfitCoveragePct alongside the ratios, and hence placed_* is reported whenever
// it differs from planned_*.
//
// The placed figure is a faithful prediction, not a guarantee: it is computed from
// the mark price read microseconds before placement, so a fast move can still
// change the outcome. It is labelled placed_* because it is the same function,
// with the same inputs, that the placement path is about to use.
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
//     economically real ratio if every PLANNED tier both survives the
//     below-minimum filter and fills at its target. On instruments with a coarse
//     minimum size the surviving ladder is narrower, so treat this as the
//     configured intent rather than the realised ratio.
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
	// ProfitCoveragePct is how much of the position the take-profit side closes:
	// the sum of the ladder tiers' close ratios, or 100 for a single
	// full-position order. RR alone cannot reveal a narrowed ladder, because
	// dropping the FAR tier leaves the nearest RR untouched while cutting how
	// much of the position is protected — exactly what went unnoticed on ZECUSDT
	// (65% configured, 48.3% placed, nearest RR identical). See profitCoveragePct.
	ProfitCoveragePct float64
	// Reason explains an invalid result, for the log line.
	Reason string
}

// totalCloseRatioPct sums the close ratios of a ladder leg.
func totalCloseRatioPct(orders []ProtectionOrder) float64 {
	total := 0.0
	for _, o := range orders {
		if o.CloseRatioPct > 0 && !math.IsNaN(o.CloseRatioPct) && !math.IsInf(o.CloseRatioPct, 0) {
			total += o.CloseRatioPct
		}
	}
	return total
}

// profitCoveragePct reports how much of the position the take-profit side closes.
//
// It must account for the collapsed shape, not just the ladder. When every ladder
// tier falls below the contract minimum the plan is rewritten into a single
// full-position TakeProfitPrice with no ladder at all, so summing the (now empty)
// ladder would report 0% for what is really 100% coverage — reading as a total
// loss of protection when it is the opposite. Mirrors protectionLegPrices, which
// already falls back to the single-price field the same way.
func profitCoveragePct(plan *ProtectionPlan) float64 {
	if plan == nil {
		return 0
	}
	if total := totalCloseRatioPct(plan.TakeProfitOrders); total > 0 {
		return total
	}
	if plan.TakeProfitPrice > 0 && !math.IsNaN(plan.TakeProfitPrice) && !math.IsInf(plan.TakeProfitPrice, 0) {
		return 100
	}
	return 0
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
	res.ProfitCoveragePct = profitCoveragePct(plan)
	res.Valid = true
	return res
}

// rrAuditMaterialGap is the difference above which the gap is worth a log line.
// Set to the same 0.05 the kernel uses for its self-consistency check, so the two
// thresholds cannot drift apart and produce contradictory verdicts.
const rrAuditMaterialGap = 0.05

// auditPlannedAndPlacedRiskReward logs the RR/coverage gap for one entry, for both
// the planned ladder and the ladder that is actually placeable.
//
// Read-only by construction: validateProtectionPlanExecution copies the plan
// before filtering, and is called here with quiet=true so its drop/collapse
// warnings are not duplicated — the placement path logs those itself moments
// later, with quiet=false.
//
// Silent unless something is worth reading: either the declared RR misses the
// planned RR by more than rrAuditMaterialGap, or the placeable ladder differs from
// the planned one. A healthy entry produces no line.
func (at *AutoTrader) auditPlannedAndPlacedRiskReward(req *protectionExecutionRequest, plan *ProtectionPlan) {
	if req == nil || plan == nil || req.Decision.EntryProtection == nil {
		return
	}
	declaredRR := req.Decision.EntryProtection.RiskReward.GrossEstimatedRR
	planned := auditPlanRiskReward(plan, req.EntryPrice, declaredRR)
	if !planned.Valid {
		if planned.Reason != "" {
			logger.Debugf("  📐 RR audit skipped for %s %s: %s", req.Symbol, req.PositionSide, planned.Reason)
		}
		return
	}

	// What the exchange will actually accept. A failure here is not the audit's
	// business to surface — the placement path will report it — so fall back to
	// reporting the planned figures alone.
	placedPlan, err := at.validateProtectionPlanExecution(req.Symbol, req.PositionSide, req.Quantity, plan, true)
	placed := rrAuditResult{}
	if err == nil && placedPlan != nil {
		placed = auditPlanRiskReward(placedPlan, req.EntryPrice, declaredRR)
	}

	declaredGap := math.Abs(planned.NearestDiff) > rrAuditMaterialGap
	narrowed := placed.Valid && (placed.ProfitTiers != planned.ProfitTiers ||
		placed.StopTiers != planned.StopTiers ||
		math.Abs(placed.ProfitCoveragePct-planned.ProfitCoveragePct) > 0.01 ||
		math.Abs(placed.NearestRR-planned.NearestRR) > rrAuditMaterialGap)
	// The whole ladder failing to survive is the loudest case of all, and it does
	// not show up as "narrowed" because there is nothing left to compare.
	dropped := !placed.Valid

	if !declaredGap && !narrowed && !dropped {
		return
	}

	msg := fmt.Sprintf("  📐 RR audit %s %s: declared=%.2f planned_nearest=%.2f (%+.2f) planned_weighted=%.2f planned_tiers=%dSL/%dTP planned_cover=%.0f%% mode=%s",
		req.Symbol, req.PositionSide, planned.DeclaredRR, planned.NearestRR, planned.NearestDiff,
		planned.WeightedRR, planned.StopTiers, planned.ProfitTiers, planned.ProfitCoveragePct, plan.Mode)
	switch {
	case dropped:
		msg += fmt.Sprintf(" | placed=NONE (%s)", placedRRUnavailableReason(err, placed))
	case narrowed:
		msg += fmt.Sprintf(" | placed_nearest=%.2f placed_weighted=%.2f placed_tiers=%dSL/%dTP placed_cover=%.0f%%",
			placed.NearestRR, placed.WeightedRR, placed.StopTiers, placed.ProfitTiers, placed.ProfitCoveragePct)
	}
	logger.Infof("%s", msg)
}

// placedRRUnavailableReason explains why no placeable ladder could be audited.
func placedRRUnavailableReason(err error, placed rrAuditResult) string {
	if err != nil {
		return "validation error: " + err.Error()
	}
	if placed.Reason != "" {
		return placed.Reason
	}
	return "no executable protection"
}
