package trader

import (
	"errors"
	"testing"

	"nofx/kernel"
)

var errAuditBoom = errors.New("boom")

// zecLikePlan mirrors the live ZECUSDT 2026-08-01 ladder: a 3.2-ATR structural
// stop and a 20/18/15/12 take-profit ladder whose furthest tier is the one that
// fell below the contract minimum.
func zecLikePlan() *ProtectionPlan {
	return &ProtectionPlan{
		Mode:            "ladder",
		NeedsStopLoss:   true,
		NeedsTakeProfit: true,
		StopLossOrders: []ProtectionOrder{
			{Price: 457.856, CloseRatioPct: 100},
		},
		TakeProfitOrders: []ProtectionOrder{
			{Price: 470.745407, CloseRatioPct: 20},
			{Price: 468.659866, CloseRatioPct: 18},
			{Price: 467.140000, CloseRatioPct: 15},
			{Price: 472.500000, CloseRatioPct: 12}, // furthest tier
		},
	}
}

func auditReq(entry, qty float64, declaredRR float64) *protectionExecutionRequest {
	return &protectionExecutionRequest{
		Symbol:       "ZECUSDT",
		PositionSide: "LONG",
		Quantity:     qty,
		EntryPrice:   entry,
		Decision: &kernel.Decision{
			EntryProtection: &kernel.AIEntryProtectionRationale{
				RiskReward: kernel.AIRiskRewardRationale{GrossEstimatedRR: declaredRR},
			},
		},
	}
}

// TestAuditCoverageDetectsNarrowedLadder is the point of the whole change: the
// nearest RR is IDENTICAL whether or not the furthest tier survives, so coverage
// is the only field that can reveal a narrowed ladder.
func TestAuditCoverageDetectsNarrowedLadder(t *testing.T) {
	full := zecLikePlan()
	narrowed := zecLikePlan()
	narrowed.TakeProfitOrders = narrowed.TakeProfitOrders[:3] // lose the 12% far tier

	a := auditPlanRiskReward(full, 463.92, 0.7)
	b := auditPlanRiskReward(narrowed, 463.92, 0.7)
	if !a.Valid || !b.Valid {
		t.Fatalf("both audits must be valid: %+v / %+v", a, b)
	}
	if a.NearestRR != b.NearestRR {
		t.Fatalf("nearest RR must be unchanged by losing the FAR tier: %.6f vs %.6f", a.NearestRR, b.NearestRR)
	}
	if a.ProfitCoveragePct != 65 {
		t.Fatalf("planned coverage expected 65, got %.2f", a.ProfitCoveragePct)
	}
	if b.ProfitCoveragePct != 53 {
		t.Fatalf("narrowed coverage expected 53, got %.2f", b.ProfitCoveragePct)
	}
}

// TestTotalCloseRatioPct covers the summing helper, including the garbage the
// audit path is expected to tolerate rather than propagate as NaN.
func TestTotalCloseRatioPct(t *testing.T) {
	if got := totalCloseRatioPct(nil); got != 0 {
		t.Fatalf("nil ladder expected 0, got %v", got)
	}
	if got := totalCloseRatioPct([]ProtectionOrder{{Price: 1, CloseRatioPct: 100}}); got != 100 {
		t.Fatalf("single full tier expected 100, got %v", got)
	}
	orders := []ProtectionOrder{
		{Price: 1, CloseRatioPct: 20},
		{Price: 2, CloseRatioPct: 0},  // ignored
		{Price: 3, CloseRatioPct: -5}, // ignored
		{Price: 4, CloseRatioPct: 18},
	}
	if got := totalCloseRatioPct(orders); got != 38 {
		t.Fatalf("expected 38 ignoring non-positive ratios, got %v", got)
	}
}

// TestAuditPlannedAndPlacedIsReadOnly is the safety property that matters most:
// the audit must not mutate the plan it is handed, because it runs immediately
// before that same plan is placed.
func TestAuditPlannedAndPlacedIsReadOnly(t *testing.T) {
	fake := &fakeOrderProtectionTrader{
		positions: []map[string]interface{}{
			{"symbol": "ZECUSDT", "side": "LONG", "markPrice": 463.92},
		},
		validateQtyErrBelow: 1.0, // force the min-contract filter to bite
	}
	at := &AutoTrader{trader: fake, exchange: "okx"}

	plan := zecLikePlan()
	before := *plan
	beforeTPs := append([]ProtectionOrder(nil), plan.TakeProfitOrders...)
	beforeSLs := append([]ProtectionOrder(nil), plan.StopLossOrders...)

	at.auditPlannedAndPlacedRiskReward(auditReq(463.92, 0.08276, 0.7), plan)

	if plan.TakeProfitPrice != before.TakeProfitPrice || plan.StopLossPrice != before.StopLossPrice {
		t.Fatalf("audit mutated plan prices: %+v vs %+v", plan, before)
	}
	if len(plan.TakeProfitOrders) != len(beforeTPs) || len(plan.StopLossOrders) != len(beforeSLs) {
		t.Fatalf("audit mutated ladder length: TP %d→%d SL %d→%d",
			len(beforeTPs), len(plan.TakeProfitOrders), len(beforeSLs), len(plan.StopLossOrders))
	}
	for i := range beforeTPs {
		if plan.TakeProfitOrders[i] != beforeTPs[i] {
			t.Fatalf("audit mutated TP tier %d: %+v vs %+v", i, plan.TakeProfitOrders[i], beforeTPs[i])
		}
	}
	for i := range beforeSLs {
		if plan.StopLossOrders[i] != beforeSLs[i] {
			t.Fatalf("audit mutated SL tier %d: %+v vs %+v", i, plan.StopLossOrders[i], beforeSLs[i])
		}
	}
}

// TestAuditPlannedAndPlacedTolerantOfMissingInputs pins that the audit never
// panics or blocks on degenerate input — it is strictly observational.
func TestAuditPlannedAndPlacedTolerantOfMissingInputs(t *testing.T) {
	at := &AutoTrader{trader: &fakeOrderProtectionTrader{}, exchange: "okx"}
	at.auditPlannedAndPlacedRiskReward(nil, zecLikePlan())
	at.auditPlannedAndPlacedRiskReward(auditReq(463.92, 0.08276, 0.7), nil)
	// No declared RR, no entry price, no decision: all must be silent no-ops.
	at.auditPlannedAndPlacedRiskReward(auditReq(463.92, 0.08276, 0), zecLikePlan())
	at.auditPlannedAndPlacedRiskReward(auditReq(0, 0.08276, 0.7), zecLikePlan())
	req := auditReq(463.92, 0.08276, 0.7)
	req.Decision.EntryProtection = nil
	at.auditPlannedAndPlacedRiskReward(req, zecLikePlan())
}

// TestPlacedRRUnavailableReason covers the explanatory string for the loudest
// case, where nothing at all could be placed.
func TestPlacedRRUnavailableReason(t *testing.T) {
	if got := placedRRUnavailableReason(nil, rrAuditResult{}); got != "no executable protection" {
		t.Fatalf("unexpected default reason %q", got)
	}
	if got := placedRRUnavailableReason(nil, rrAuditResult{Reason: "no target price in plan"}); got != "no target price in plan" {
		t.Fatalf("reason should pass through, got %q", got)
	}
	if got := placedRRUnavailableReason(errAuditBoom, rrAuditResult{}); got != "validation error: boom" {
		t.Fatalf("error should be surfaced, got %q", got)
	}
}

// TestProfitCoveragePctHandlesCollapsedPlan pins the shape the collapse fallback
// produces: no ladder, one full-position TakeProfitPrice. Summing the empty
// ladder would report 0% for what is actually full coverage, which reads as a
// total loss of protection when it is the opposite.
func TestProfitCoveragePctHandlesCollapsedPlan(t *testing.T) {
	if got := profitCoveragePct(nil); got != 0 {
		t.Fatalf("nil plan expected 0, got %v", got)
	}
	ladder := &ProtectionPlan{TakeProfitOrders: []ProtectionOrder{
		{Price: 470, CloseRatioPct: 20},
		{Price: 471, CloseRatioPct: 18},
	}}
	if got := profitCoveragePct(ladder); got != 38 {
		t.Fatalf("ladder expected 38, got %v", got)
	}
	collapsed := &ProtectionPlan{TakeProfitPrice: 472.5}
	if got := profitCoveragePct(collapsed); got != 100 {
		t.Fatalf("collapsed full-position TP expected 100, got %v", got)
	}
	// Ladder wins when both are somehow set: the ladder is what gets placed.
	both := &ProtectionPlan{TakeProfitPrice: 472.5, TakeProfitOrders: []ProtectionOrder{
		{Price: 470, CloseRatioPct: 65},
	}}
	if got := profitCoveragePct(both); got != 65 {
		t.Fatalf("ladder must take precedence, got %v", got)
	}
	if got := profitCoveragePct(&ProtectionPlan{}); got != 0 {
		t.Fatalf("empty plan expected 0, got %v", got)
	}
}
