package kernel

import (
	"encoding/json"
	"testing"
)

// TestDecisionQualityScoreBareNumber verifies that Decision.UnmarshalJSON
// tolerates quality_score being a bare number (which the AI sometimes emits)
// instead of only accepting the full object form.
func TestDecisionQualityScoreBareNumber(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantErr  bool
		wantQS   int // expected quality_score.total
	}{
		{
			name:    "bare number",
			input:   `{"symbol":"BTCUSDT","action":"open_long","quality_score":74}`,
			wantErr: false,
			wantQS:  74,
		},
		{
			name:    "bare numeric string",
			input:   `{"symbol":"BTCUSDT","action":"open_long","quality_score":"85"}`,
			wantErr: false,
			wantQS:  85,
		},
		{
			name:    "full object form",
			input:   `{"symbol":"BTCUSDT","action":"open_long","quality_score":{"total":90,"trend_alignment":20}}`,
			wantErr: false,
			wantQS:  90,
		},
		{
			name:    "null quality_score",
			input:   `{"symbol":"BTCUSDT","action":"open_long","quality_score":null}`,
			wantErr: false,
			wantQS:  0,
		},
		{
			name:    "missing quality_score",
			input:   `{"symbol":"BTCUSDT","action":"open_long"}`,
			wantErr: false,
			wantQS:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d Decision
			err := json.Unmarshal([]byte(tt.input), &d)
			if (err != nil) != tt.wantErr {
				t.Errorf("Unmarshal() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}
			got := 0
			if d.QualityScore != nil {
				got = d.QualityScore.Total
			}
			if got != tt.wantQS {
				t.Errorf("quality_score.total = %d, want %d", got, tt.wantQS)
			}
		})
	}
}
