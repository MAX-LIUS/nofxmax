package kernel

import (
	"strings"
	"testing"

	"nofx/store"
)

// gate with defaults applied (MaxTargetATRMul defaults to 5.0)
func gateWithMaxTarget() store.EntryGateConfig {
	return store.EntryGateConfig{}.WithDefaults()
}

// With entry=100 and ATR14Pct=3.0, atrAbs=3.0.
// target/ATR = rewardDistance / 3.0. Threshold 5.0 => rewardDistance > 15 blocks.

func TestTargetReachability_ReachableTargetPasses(t *testing.T) {
	// long: target 110 => reward 10 => 10/3 = 3.33x ATR (sweet spot), should pass
	r := &AIEntryProtectionRationale{
		VolatilityAdjustment: AIEntryVolatilityAdjustment{ATR14Pct: 3.0},
		RiskReward:           AIRiskRewardRationale{Entry: 100, Invalidation: 96, FirstTarget: 110},
	}
	if err := validateTargetReachability(r, gateWithMaxTarget()); err != nil {
		t.Fatalf("reachable target (3.33xATR) should pass, got: %v", err)
	}
}

func TestTargetReachability_UnreachableFarTargetBlocked(t *testing.T) {
	// long: target 120 => reward 20 => 20/3 = 6.67x ATR (>5), should block
	r := &AIEntryProtectionRationale{
		VolatilityAdjustment: AIEntryVolatilityAdjustment{ATR14Pct: 3.0},
		RiskReward:           AIRiskRewardRationale{Entry: 100, Invalidation: 96, FirstTarget: 120},
	}
	err := validateTargetReachability(r, gateWithMaxTarget())
	if err == nil {
		t.Fatal("unreachable far target (6.67xATR) should be blocked")
	}
	if !strings.Contains(err.Error(), "unreachable target") {
		t.Fatalf("expected unreachable-target error, got: %v", err)
	}
}

func TestTargetReachability_UnreachableShortTargetBlocked(t *testing.T) {
	// short: target 80 => reward 20 => 20/3 = 6.67x ATR (>5), should block
	r := &AIEntryProtectionRationale{
		VolatilityAdjustment: AIEntryVolatilityAdjustment{ATR14Pct: 3.0},
		RiskReward:           AIRiskRewardRationale{Entry: 100, Invalidation: 104, FirstTarget: 80},
	}
	if err := validateTargetReachability(r, gateWithMaxTarget()); err == nil {
		t.Fatal("unreachable short far target (6.67xATR) should be blocked")
	}
}

func TestTargetReachability_ExactBoundaryPasses(t *testing.T) {
	// target 115 => reward 15 => 15/3 = 5.0x exactly (not > 5), should pass
	r := &AIEntryProtectionRationale{
		VolatilityAdjustment: AIEntryVolatilityAdjustment{ATR14Pct: 3.0},
		RiskReward:           AIRiskRewardRationale{Entry: 100, Invalidation: 96, FirstTarget: 115},
	}
	if err := validateTargetReachability(r, gateWithMaxTarget()); err != nil {
		t.Fatalf("exact 5.0xATR boundary should pass, got: %v", err)
	}
}

func TestTargetReachability_DisabledWhenZero(t *testing.T) {
	// even an absurd 10x target passes when the gate is disabled (MaxTargetATRMul<=0)
	r := &AIEntryProtectionRationale{
		VolatilityAdjustment: AIEntryVolatilityAdjustment{ATR14Pct: 3.0},
		RiskReward:           AIRiskRewardRationale{Entry: 100, Invalidation: 96, FirstTarget: 200},
	}
	gate := store.EntryGateConfig{MaxTargetATRMul: -1} // negative => disabled after WithDefaults
	gate = gate.WithDefaults()
	if gate.MaxTargetATRMul != 0 {
		t.Fatalf("negative MaxTargetATRMul should disable (0), got %v", gate.MaxTargetATRMul)
	}
	if err := validateTargetReachability(r, gate); err != nil {
		t.Fatalf("disabled gate should never block, got: %v", err)
	}
}

func TestTargetReachability_NoATRDataDoesNotBlock(t *testing.T) {
	// without ATR context we cannot normalize; must not block (avoid false positives)
	r := &AIEntryProtectionRationale{
		VolatilityAdjustment: AIEntryVolatilityAdjustment{ATR14Pct: 0},
		RiskReward:           AIRiskRewardRationale{Entry: 100, Invalidation: 96, FirstTarget: 200},
	}
	if err := validateTargetReachability(r, gateWithMaxTarget()); err != nil {
		t.Fatalf("missing ATR data should not block, got: %v", err)
	}
}

func TestTargetReachability_DefaultIsFive(t *testing.T) {
	if got := (store.EntryGateConfig{}).WithDefaults().MaxTargetATRMul; got != 5.0 {
		t.Fatalf("MaxTargetATRMul default expected 5.0, got %v", got)
	}
}
