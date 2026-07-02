package kernel

import (
	"math"
	"strings"
	"testing"

	"nofx/store"
)

// gate with defaults applied (MaxTargetATRMul=5.0, mode="cap", realistic=0.8)
func gateWithMaxTarget() store.EntryGateConfig {
	return store.EntryGateConfig{}.WithDefaults()
}

// reject-mode gate (legacy hard-block behaviour)
func gateRejectMode() store.EntryGateConfig {
	g := store.EntryGateConfig{TargetReachabilityMode: "reject"}.WithDefaults()
	return g
}

// With entry=100 and ATR14Pct=3.0, atrAbs=3.0.
// target/ATR = rewardDistance / 3.0. Threshold 5.0 => rewardDistance > 15 acts.

func TestTargetReachability_ReachableTargetUntouched(t *testing.T) {
	// long: target 110 => reward 10 => 3.33x ATR (sweet spot), should pass untouched
	r := &AIEntryProtectionRationale{
		VolatilityAdjustment: AIEntryVolatilityAdjustment{ATR14Pct: 3.0},
		RiskReward:           AIRiskRewardRationale{Entry: 100, Invalidation: 96, FirstTarget: 110, GrossEstimatedRR: 2.5},
	}
	capped, err := enforceTargetReachability("open_long", r, gateWithMaxTarget())
	if err != nil {
		t.Fatalf("reachable target (3.33xATR) should pass, got: %v", err)
	}
	if capped {
		t.Fatal("reachable target must not be capped")
	}
	if r.RiskReward.FirstTarget != 110 {
		t.Fatalf("reachable target must be untouched, got %v", r.RiskReward.FirstTarget)
	}
}

func TestTargetReachability_CapModeRewritesLongTarget(t *testing.T) {
	// long: target 120 => reward 20 => 6.67x ATR (>5). Cap to 5x => target=100+15=115.
	r := &AIEntryProtectionRationale{
		VolatilityAdjustment: AIEntryVolatilityAdjustment{ATR14Pct: 3.0},
		RiskReward:           AIRiskRewardRationale{Entry: 100, Invalidation: 96, FirstTarget: 120, GrossEstimatedRR: 5.0},
	}
	capped, err := enforceTargetReachability("open_long", r, gateWithMaxTarget())
	if err != nil {
		t.Fatalf("cap mode should not error, got: %v", err)
	}
	if !capped {
		t.Fatal("unreachable target should have been capped")
	}
	if math.Abs(r.RiskReward.FirstTarget-115) > 1e-9 {
		t.Fatalf("long target should cap to 115 (5xATR), got %v", r.RiskReward.FirstTarget)
	}
	// RR recomputed: reward 15 / risk 4 = 3.75
	if math.Abs(r.RiskReward.GrossEstimatedRR-3.75) > 1e-9 {
		t.Fatalf("gross RR should recompute to 3.75, got %v", r.RiskReward.GrossEstimatedRR)
	}
}

func TestTargetReachability_CapModeRewritesShortTarget(t *testing.T) {
	// short: target 80 => reward 20 => 6.67x ATR (>5). Cap => target=100-15=85.
	r := &AIEntryProtectionRationale{
		VolatilityAdjustment: AIEntryVolatilityAdjustment{ATR14Pct: 3.0},
		RiskReward:           AIRiskRewardRationale{Entry: 100, Invalidation: 104, FirstTarget: 80, GrossEstimatedRR: 5.0},
	}
	capped, err := enforceTargetReachability("open_short", r, gateWithMaxTarget())
	if err != nil {
		t.Fatalf("cap mode should not error, got: %v", err)
	}
	if !capped {
		t.Fatal("unreachable short target should have been capped")
	}
	if math.Abs(r.RiskReward.FirstTarget-85) > 1e-9 {
		t.Fatalf("short target should cap to 85 (5xATR), got %v", r.RiskReward.FirstTarget)
	}
}

func TestTargetReachability_RejectModeBlocksFarTarget(t *testing.T) {
	// long: target 120 => 6.67x ATR (>5). reject mode => error, target untouched.
	r := &AIEntryProtectionRationale{
		VolatilityAdjustment: AIEntryVolatilityAdjustment{ATR14Pct: 3.0},
		RiskReward:           AIRiskRewardRationale{Entry: 100, Invalidation: 96, FirstTarget: 120},
	}
	capped, err := enforceTargetReachability("open_long", r, gateRejectMode())
	if err == nil {
		t.Fatal("reject mode: unreachable far target should be blocked")
	}
	if capped {
		t.Fatal("reject mode must not report capped")
	}
	if !strings.Contains(err.Error(), "unreachable target") {
		t.Fatalf("expected unreachable-target error, got: %v", err)
	}
	if r.RiskReward.FirstTarget != 120 {
		t.Fatalf("reject mode must not mutate target, got %v", r.RiskReward.FirstTarget)
	}
}

func TestTargetReachability_ExactBoundaryUntouched(t *testing.T) {
	// target 115 => reward 15 => 5.0x exactly (not > 5), should pass untouched
	r := &AIEntryProtectionRationale{
		VolatilityAdjustment: AIEntryVolatilityAdjustment{ATR14Pct: 3.0},
		RiskReward:           AIRiskRewardRationale{Entry: 100, Invalidation: 96, FirstTarget: 115},
	}
	capped, err := enforceTargetReachability("open_long", r, gateWithMaxTarget())
	if err != nil || capped {
		t.Fatalf("exact 5.0xATR boundary should pass untouched, got capped=%v err=%v", capped, err)
	}
}

func TestTargetReachability_DisabledWhenZero(t *testing.T) {
	// even an absurd 10x target passes when the gate is disabled (MaxTargetATRMul<=0)
	r := &AIEntryProtectionRationale{
		VolatilityAdjustment: AIEntryVolatilityAdjustment{ATR14Pct: 3.0},
		RiskReward:           AIRiskRewardRationale{Entry: 100, Invalidation: 96, FirstTarget: 200},
	}
	gate := store.EntryGateConfig{MaxTargetATRMul: -1}.WithDefaults()
	if gate.MaxTargetATRMul != 0 {
		t.Fatalf("negative MaxTargetATRMul should disable (0), got %v", gate.MaxTargetATRMul)
	}
	capped, err := enforceTargetReachability("open_long", r, gate)
	if err != nil || capped {
		t.Fatalf("disabled gate should never act, got capped=%v err=%v", capped, err)
	}
}

func TestTargetReachability_NoATRDataDoesNotAct(t *testing.T) {
	// without ATR context we cannot normalize; must not act (avoid false positives)
	r := &AIEntryProtectionRationale{
		VolatilityAdjustment: AIEntryVolatilityAdjustment{ATR14Pct: 0},
		RiskReward:           AIRiskRewardRationale{Entry: 100, Invalidation: 96, FirstTarget: 200},
	}
	capped, err := enforceTargetReachability("open_long", r, gateWithMaxTarget())
	if err != nil || capped {
		t.Fatalf("missing ATR data should not act, got capped=%v err=%v", capped, err)
	}
}

func TestTargetReachability_Defaults(t *testing.T) {
	g := (store.EntryGateConfig{}).WithDefaults()
	if g.MaxTargetATRMul != 5.0 {
		t.Fatalf("MaxTargetATRMul default expected 5.0, got %v", g.MaxTargetATRMul)
	}
	if g.TargetReachabilityMode != "cap" {
		t.Fatalf("TargetReachabilityMode default expected cap, got %q", g.TargetReachabilityMode)
	}
	if g.RealisticTargetRiskMul != 0.8 {
		t.Fatalf("RealisticTargetRiskMul default expected 0.8, got %v", g.RealisticTargetRiskMul)
	}
}
