package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestLegacyEvolutionKeyIgnored pins the migration contract for the coin
// evolution profile engine removed on 2026-08-04.
//
// Every strategy row in production still carries an "evolution" object in its
// stored config JSON (5 of 5 live strategies had evolution.enabled=1 at removal
// time). Nothing rewrites those rows, so StrategyConfig must keep unmarshalling
// them without error and must NOT resurrect the block on re-marshal. If someone
// later adds json.Decoder.DisallowUnknownFields anywhere on this path, this test
// is the thing that fails instead of every live trader at once.
func TestLegacyEvolutionKeyIgnored(t *testing.T) {
	raw := []byte(`{
		"evolution": {
			"enabled": true,
			"half_life_days": 14,
			"min_sample_size": 5,
			"inject_to_prompt": true
		},
		"indicators": {"klines": {"primary_timeframe": "1h", "primary_count": 22}}
	}`)

	var cfg StrategyConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("legacy config with evolution block must still unmarshal: %v", err)
	}
	// A field that comes AFTER the removed one must still be populated, which is
	// what proves the decoder skipped the unknown object rather than bailing.
	if got := cfg.Indicators.Klines.PrimaryTimeframe; got != "1h" {
		t.Fatalf("primary_timeframe = %q, want %q", got, "1h")
	}

	out, err := json.Marshal(&cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), `"evolution"`) {
		t.Fatalf("re-marshalled config must not re-emit the evolution block: %s", out)
	}
}
