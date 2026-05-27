package trader

import (
	"testing"
)

func TestGateScoreComputation(t *testing.T) {
	// All checks pass → score 100
	checks := []EntryGateCheck{
		{Code: "trigger_type_valid", Passed: true, Enforced: true},
		{Code: "ema20_direction_ok", Passed: true, Enforced: true},
		{Code: "trend_phase_ok", Passed: true, Enforced: true},
	}
	score := computeGateScore(checks)
	if score != 100 {
		t.Errorf("all passed: expected score 100, got %d", score)
	}

	// Soft failures deduct points
	checks = []EntryGateCheck{
		{Code: "trigger_type_valid", Passed: true, Enforced: true},
		{Code: "range_middle_without_edge_setup", Passed: false, Enforced: false},
		{Code: "unsupported_setup_type", Passed: false, Enforced: false},
	}
	score = computeGateScore(checks)
	// range_middle = -15, unsupported_setup = -10 → 75
	if score != 75 {
		t.Errorf("two soft failures: expected score 75, got %d", score)
	}

	// Custom penalty via Penalty field
	checks = []EntryGateCheck{
		{Code: "custom_check", Passed: false, Enforced: false, Penalty: 30},
	}
	score = computeGateScore(checks)
	if score != 70 {
		t.Errorf("custom penalty 30: expected score 70, got %d", score)
	}

	// Enforced failures don't affect score (they block before scoring)
	checks = []EntryGateCheck{
		{Code: "trigger_type_missing", Passed: false, Enforced: true},
	}
	score = computeGateScore(checks)
	if score != 100 {
		t.Errorf("enforced failure should not deduct: expected 100, got %d", score)
	}
}

func TestScoreToSizeMultiplier(t *testing.T) {
	tests := []struct {
		score    int
		expected float64
	}{
		{100, 1.0},
		{80, 1.0},
		{75, 1.0},
		{70, 0.83},  // 0.5 + (70-60)/15*0.5 = 0.5 + 0.33 = 0.83
		{60, 0.5},
		{50, 0.3},
		{0, 0.3},
	}

	for _, tt := range tests {
		got := scoreToSizeMultiplier(tt.score)
		// Allow small floating point tolerance
		diff := got - tt.expected
		if diff < 0 {
			diff = -diff
		}
		if diff > 0.02 {
			t.Errorf("score %d: expected multiplier %.2f, got %.2f", tt.score, tt.expected, got)
		}
	}
}
