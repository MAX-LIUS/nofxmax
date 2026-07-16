package kernel

import (
	"encoding/json"
	"testing"
)

// TestEmptyActionNormalizedToWait guards the fix for the SAFE MODE trip observed
// in production: the model emitted a decision object whose action was empty, which
// slipped past the missing-JSON SafeFallback and failed kernel validation, tripping
// SAFE MODE for the whole trader. It must now be repaired to a safe no-op "wait".
func TestEmptyActionNormalizedToWait(t *testing.T) {
	resp := `[{"symbol":"BTCUSDT","action":"","reasoning":"model omitted action"}]`
	decision, err := parseFullDecisionResponse(resp, 1000, 5, 3, 0.2, 0.1, nil)
	if err != nil {
		t.Fatalf("empty action must not fail validation, got error: %v", err)
	}
	if len(decision.Decisions) != 1 {
		t.Fatalf("expected 1 decision, got %+v", decision.Decisions)
	}
	if decision.Decisions[0].Action != "wait" {
		t.Fatalf("expected empty action repaired to 'wait', got %q", decision.Decisions[0].Action)
	}
}

// TestQualityScoreScalarTolerated guards the fix for the GPT SAFE MODE trip:
// the model sometimes emits quality_score as a bare number instead of the
// breakdown object, which crashed json.Unmarshal. It must now parse, folding
// the scalar into Total.
func TestQualityScoreScalarTolerated(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		total int
	}{
		{"bare number", `{"quality_score":85}`, 85},
		{"numeric string", `{"quality_score":"72"}`, 72},
		{"object form", `{"quality_score":{"total":90,"trend_alignment":18}}`, 90},
		{"null", `{"quality_score":null}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var d Decision
			if err := json.Unmarshal([]byte(tc.raw), &d); err != nil {
				t.Fatalf("quality_score %s must parse, got error: %v", tc.name, err)
			}
			if tc.total == 0 {
				if d.QualityScore != nil && d.QualityScore.Total != 0 {
					t.Fatalf("expected zero/absent total, got %+v", d.QualityScore)
				}
				return
			}
			if d.QualityScore == nil {
				t.Fatalf("expected quality_score populated, got nil")
			}
			if d.QualityScore.Total != tc.total {
				t.Fatalf("expected total=%d, got %d", tc.total, d.QualityScore.Total)
			}
		})
	}
}
