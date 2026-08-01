package trader

import (
	"math"
	"testing"
)

func TestAuditPlanRiskRewardNilAndDegenerate(t *testing.T) {
	// Every one of these must return Valid=false with a stated reason, never a
	// number. An audit that invents a number is worse than no audit.
	cases := []struct {
		name    string
		plan    *ProtectionPlan
		entry   float64
		declRR  float64
		wantWhy string
	}{
		{"nil plan", nil, 100, 2, "no plan"},
		{"zero entry", &ProtectionPlan{StopLossPrice: 99, TakeProfitPrice: 102}, 0, 2, "no entry price"},
		{"negative entry", &ProtectionPlan{StopLossPrice: 99, TakeProfitPrice: 102}, -5, 2, "no entry price"},
		{"NaN entry", &ProtectionPlan{StopLossPrice: 99, TakeProfitPrice: 102}, math.NaN(), 2, "no entry price"},
		{"Inf entry", &ProtectionPlan{StopLossPrice: 99, TakeProfitPrice: 102}, math.Inf(1), 2, "no entry price"},
		{"zero declared", &ProtectionPlan{StopLossPrice: 99, TakeProfitPrice: 102}, 100, 0, "no declared rr"},
		{"NaN declared", &ProtectionPlan{StopLossPrice: 99, TakeProfitPrice: 102}, 100, math.NaN(), "no declared rr"},
		{"no stop", &ProtectionPlan{TakeProfitPrice: 102}, 100, 2, "no stop price in plan"},
		{"no target", &ProtectionPlan{StopLossPrice: 99}, 100, 2, "no target price in plan"},
		{"stop at entry", &ProtectionPlan{StopLossPrice: 100, TakeProfitPrice: 102}, 100, 2, "stop sits at entry"},
		{"empty plan", &ProtectionPlan{}, 100, 2, "no stop price in plan"},
	}
	for _, c := range cases {
		got := auditPlanRiskReward(c.plan, c.entry, c.declRR)
		if got.Valid {
			t.Errorf("%s: must be invalid, got %+v", c.name, got)
		}
		if got.Reason != c.wantWhy {
			t.Errorf("%s: reason = %q, want %q", c.name, got.Reason, c.wantWhy)
		}
		if got.NearestRR != 0 || got.WeightedRR != 0 || got.NearestDiff != 0 {
			t.Errorf("%s: invalid result must carry no numbers, got %+v", c.name, got)
		}
	}
}

func TestAuditPlanRiskRewardSingleLeg(t *testing.T) {
	// entry 100, stop 98 (risk 2), target 106 (reward 6) => RR 3.0.
	plan := &ProtectionPlan{StopLossPrice: 98, TakeProfitPrice: 106}
	got := auditPlanRiskReward(plan, 100, 3.0)
	if !got.Valid {
		t.Fatalf("must be valid: %+v", got)
	}
	if math.Abs(got.NearestRR-3.0) > 1e-9 {
		t.Errorf("nearestRR = %.6f, want 3.0", got.NearestRR)
	}
	if math.Abs(got.WeightedRR-3.0) > 1e-9 {
		t.Errorf("weightedRR = %.6f, want 3.0 for a single leg", got.WeightedRR)
	}
	if math.Abs(got.NearestDiff) > 1e-9 {
		t.Errorf("diff = %.6f, want 0 when declared matches", got.NearestDiff)
	}
	if got.StopTiers != 1 || got.ProfitTiers != 1 {
		t.Errorf("tiers = %d/%d, want 1/1", got.StopTiers, got.ProfitTiers)
	}
}

func TestAuditPlanRiskRewardShortSideIsSymmetric(t *testing.T) {
	// A short: entry 100, stop ABOVE at 102, target BELOW at 94. Distances are
	// the same magnitudes as the long case, so the RR must be identical. Using
	// absolute distances is what makes the audit side-agnostic.
	short := auditPlanRiskReward(&ProtectionPlan{StopLossPrice: 102, TakeProfitPrice: 94}, 100, 3.0)
	long := auditPlanRiskReward(&ProtectionPlan{StopLossPrice: 98, TakeProfitPrice: 106}, 100, 3.0)
	if !short.Valid || !long.Valid {
		t.Fatalf("both must be valid: short=%+v long=%+v", short, long)
	}
	if math.Abs(short.NearestRR-long.NearestRR) > 1e-9 {
		t.Errorf("short RR %.6f != long RR %.6f; audit must be side-agnostic", short.NearestRR, long.NearestRR)
	}
}

func TestAuditPlanRiskRewardLadderUsesNearestAndWeighted(t *testing.T) {
	// entry 100; stop tiers 99 (risk 1) and 97 (risk 3); TP tiers 103 and 109.
	// nearest: reward 3 / risk 1 = 3.0
	// weighted: SL avg = (99*50 + 97*50)/100 = 98 -> risk 2
	//           TP avg = (103*50 + 109*50)/100 = 106 -> reward 6 => 3.0
	plan := &ProtectionPlan{
		StopLossOrders: []ProtectionOrder{
			{Price: 99, CloseRatioPct: 50},
			{Price: 97, CloseRatioPct: 50},
		},
		TakeProfitOrders: []ProtectionOrder{
			{Price: 103, CloseRatioPct: 50},
			{Price: 109, CloseRatioPct: 50},
		},
	}
	got := auditPlanRiskReward(plan, 100, 3.0)
	if !got.Valid {
		t.Fatalf("must be valid: %+v", got)
	}
	if math.Abs(got.NearestRR-3.0) > 1e-9 {
		t.Errorf("nearestRR = %.6f, want 3.0 (103 vs 99)", got.NearestRR)
	}
	if math.Abs(got.WeightedRR-3.0) > 1e-9 {
		t.Errorf("weightedRR = %.6f, want 3.0", got.WeightedRR)
	}
	if got.StopTiers != 2 || got.ProfitTiers != 2 {
		t.Errorf("tiers = %d/%d, want 2/2", got.StopTiers, got.ProfitTiers)
	}
}

func TestAuditPlanRiskRewardLadderNearestDiffersFromWeighted(t *testing.T) {
	// Asymmetric ladder where the two measures genuinely disagree, proving they
	// are not the same computation dressed twice.
	// entry 100; SL single 95 (risk 5); TP tiers 101 (10%) and 130 (90%).
	// nearest: 1/5 = 0.2 ; weighted TP = (101*10 + 130*90)/100 = 127.1 -> 27.1/5 = 5.42
	plan := &ProtectionPlan{
		StopLossPrice: 95,
		TakeProfitOrders: []ProtectionOrder{
			{Price: 101, CloseRatioPct: 10},
			{Price: 130, CloseRatioPct: 90},
		},
	}
	got := auditPlanRiskReward(plan, 100, 3.0)
	if !got.Valid {
		t.Fatalf("must be valid: %+v", got)
	}
	if math.Abs(got.NearestRR-0.2) > 1e-9 {
		t.Errorf("nearestRR = %.6f, want 0.2", got.NearestRR)
	}
	if math.Abs(got.WeightedRR-5.42) > 1e-6 {
		t.Errorf("weightedRR = %.6f, want 5.42", got.WeightedRR)
	}
	// Declared 3.0 while the first target only pays 0.2 — exactly the case this
	// audit exists to surface.
	if got.NearestDiff >= 0 {
		t.Errorf("diff = %+.6f, want negative (declared overstates the first target)", got.NearestDiff)
	}
}

func TestProtectionLegPricesSkipsGarbageAndFallsBack(t *testing.T) {
	// Non-finite and non-positive tier prices must be dropped, not propagated.
	orders := []ProtectionOrder{
		{Price: 0, CloseRatioPct: 50},
		{Price: -3, CloseRatioPct: 50},
		{Price: math.NaN(), CloseRatioPct: 50},
		{Price: math.Inf(1), CloseRatioPct: 50},
		{Price: 98, CloseRatioPct: 50},
	}
	prices, ratios := protectionLegPrices(orders, 0)
	if len(prices) != 1 || prices[0] != 98 {
		t.Fatalf("prices = %v, want [98]", prices)
	}
	if len(ratios) != 1 || ratios[0] != 50 {
		t.Fatalf("ratios = %v, want [50]", ratios)
	}
	// All-garbage ladder with a valid single price must fall back to it at 100%.
	prices, ratios = protectionLegPrices([]ProtectionOrder{{Price: 0}}, 97)
	if len(prices) != 1 || prices[0] != 97 || ratios[0] != 100 {
		t.Fatalf("fallback failed: prices=%v ratios=%v", prices, ratios)
	}
	// Nothing usable at all.
	prices, _ = protectionLegPrices(nil, 0)
	if len(prices) != 0 {
		t.Fatalf("prices = %v, want empty", prices)
	}
}

func TestWeightedMeanDegradesToPlainMean(t *testing.T) {
	// All-zero ratios must not divide by zero; the plain mean is the documented
	// degradation.
	got := weightedMean([]float64{10, 20}, []float64{0, 0})
	if math.Abs(got-15) > 1e-9 {
		t.Errorf("weightedMean = %.6f, want 15", got)
	}
	// Missing ratio slots must not panic.
	got = weightedMean([]float64{10, 20}, []float64{100})
	if math.Abs(got-10) > 1e-9 {
		t.Errorf("weightedMean = %.6f, want 10 (only the weighted one counts)", got)
	}
	if v := weightedMean(nil, nil); v != 0 {
		t.Errorf("weightedMean(nil) = %.6f, want 0", v)
	}
}

func TestNearestByDistance(t *testing.T) {
	if v := nearestByDistance([]float64{90, 99, 80}, 100); v != 99 {
		t.Errorf("nearest = %.2f, want 99", v)
	}
	// Ties resolve to the first seen; the value is what matters, not which index.
	if v := nearestByDistance([]float64{98, 102}, 100); v != 98 {
		t.Errorf("nearest = %.2f, want 98 (first of equidistant)", v)
	}
	if v := nearestByDistance(nil, 100); v != 0 {
		t.Errorf("nearest(nil) = %.2f, want 0", v)
	}
}

// TestRRAuditGapThresholdMatchesKernel pins the two thresholds together. If the
// kernel's self-consistency tolerance is ever changed, this test is the reminder
// that the audit threshold has to move with it or the two will disagree.
func TestRRAuditGapThresholdMatchesKernel(t *testing.T) {
	if rrAuditMaterialGap != 0.05 {
		t.Errorf("rrAuditMaterialGap = %v; kernel/engine_analysis.go uses 0.05 for the "+
			"same kind of comparison — keep them equal or document why they differ", rrAuditMaterialGap)
	}
}
