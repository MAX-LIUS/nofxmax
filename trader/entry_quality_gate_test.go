package trader

import (
	"testing"

	"nofx/kernel"
	"nofx/store"
)

// mkConfRiskInput builds an entryGateInput that exercises evaluateConfidenceRiskGate's
// entry-quality hard blocks (block 3g). setupType and netRR are the two levers.
func mkConfRiskInput(setupType string, netRR float64) entryGateInput {
	sc := &store.StrategyConfig{}
	// WithDefaults() is applied inside the gate; construct a valid decision.
	return entryGateInput{
		Decision: &kernel.Decision{
			Action:      "open_long",
			TriggerType: "support_rejection_confirmed",
			SetupType:   setupType,
			Confidence:  70,
			EntryProtection: &kernel.AIEntryProtectionRationale{
				RiskReward: kernel.AIRiskRewardRationale{
					Entry:            100,
					Invalidation:     96,
					FirstTarget:      110,
					GrossEstimatedRR: netRR + 0.2,
					NetEstimatedRR:   netRR,
					MinRequiredRR:    1.5,
					Passed:           true,
				},
			},
		},
		StrategyConfig: sc,
	}
}

func TestBreakoutRetestIsSoftPenalized(t *testing.T) {
	// breakout_retest is now a HEAVY soft penalty (not a hard block): the check
	// fails but is NOT enforced, so it deducts size rather than rejecting.
	checks := evaluateConfidenceRiskGate(mkConfRiskInput("breakout_retest", 2.0))
	c := findCheck(checks, "breakout_retest_blocked")
	if c == nil {
		t.Fatal("expected breakout_retest_blocked check")
	}
	if c.Passed {
		t.Error("breakout_retest must not pass (net-negative in every backtest third)")
	}
	if c.Enforced {
		t.Error("breakout_retest must be a SOFT penalty now (Enforced=false), not a hard block")
	}
	// It must not hard-block: firstEnforcedFailure should ignore it.
	if blocked, code, _ := firstEnforcedFailure(checks); blocked && code == "breakout_retest_blocked" {
		t.Error("breakout_retest must not appear as an enforced (hard) failure")
	}
	// And it must carry the heavy penalty so size drops materially.
	score := computeGateScore(checks)
	if score > 60 {
		t.Errorf("breakout_retest heavy penalty should drop gate score to <=60 (got %d)", score)
	}
}

func TestNonBreakoutSetupIsNotBlocked(t *testing.T) {
	// Control: a normal setup type must not trigger the breakout_retest block.
	checks := evaluateConfidenceRiskGate(mkConfRiskInput("support_rejection", 2.0))
	if c := findCheck(checks, "breakout_retest_blocked"); c != nil {
		t.Error("non-breakout setup should not produce breakout_retest_blocked check")
	}
}

func TestBreakoutRetestBlockRespectsCaseAndSpace(t *testing.T) {
	// Case-insensitive + trimmed matching guards against schema drift.
	checks := evaluateConfidenceRiskGate(mkConfRiskInput("  Breakout_Retest ", 2.0))
	c := findCheck(checks, "breakout_retest_blocked")
	if c == nil {
		t.Fatal("expected breakout_retest_blocked for case/space variant")
	}
	if c.Passed || c.Enforced {
		t.Error("case/space variant must be soft-penalized (fails, not enforced)")
	}
}

func TestNetRRAboveMaxIsHardBlocked(t *testing.T) {
	// Default MaxNetRR is 2.8; netRR 3.5 must be blocked.
	checks := evaluateConfidenceRiskGate(mkConfRiskInput("support_rejection", 3.5))
	c := findCheck(checks, "net_rr_above_max")
	if c == nil {
		t.Fatal("expected net_rr_above_max check")
	}
	if c.Passed {
		t.Error("net RR above the ceiling must not pass (over-promised targets)")
	}
	if !c.Enforced {
		t.Error("net_rr_above_max must be hard-enforced")
	}
}

func TestNetRRBelowMaxIsNotBlocked(t *testing.T) {
	// netRR 2.5 is under the 2.8 ceiling → no net_rr_above_max check.
	checks := evaluateConfidenceRiskGate(mkConfRiskInput("support_rejection", 2.5))
	if c := findCheck(checks, "net_rr_above_max"); c != nil {
		t.Error("net RR below the ceiling should not produce net_rr_above_max check")
	}
}

func TestNetRRAtMaxIsNotBlocked(t *testing.T) {
	// Boundary: netRR exactly at the ceiling must NOT be blocked (strict >).
	checks := evaluateConfidenceRiskGate(mkConfRiskInput("support_rejection", 2.8))
	if c := findCheck(checks, "net_rr_above_max"); c != nil {
		t.Error("net RR exactly at the ceiling must not be blocked (strict greater-than)")
	}
}
