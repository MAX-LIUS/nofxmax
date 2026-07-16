package api

import (
	"encoding/json"
	"testing"

	"nofx/store"
)

// TestThrottleConfigRoundTrip proves the 4 throttle fields sent from the frontend
// survive the deep-merge save path and land on the typed struct — i.e. the UI is
// truly wired to the backend, not cosmetic.
func TestThrottleConfigRoundTrip(t *testing.T) {
	existing := store.StrategyConfig{}
	existing.EntryStructure.Enabled = true

	// simulate exactly what the frontend PATCHes when the user edits the controls
	incoming := json.RawMessage(`{
		"entry_structure": {
			"entry_gate": {
				"correlated_adverse_throttle": false,
				"throttle_window_hours": 8,
				"throttle_min_closes": 4,
				"throttle_loss_rate": 0.75
			}
		}
	}`)

	merged, err := mergeStrategyConfig(existing, incoming)
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	g := merged.EntryStructure.EntryGate
	if g.CorrelatedAdverseThrottle == nil || *g.CorrelatedAdverseThrottle != false {
		t.Errorf("correlated_adverse_throttle did not round-trip: %v", g.CorrelatedAdverseThrottle)
	}
	if g.ThrottleWindowHours != 8 {
		t.Errorf("throttle_window_hours = %v, want 8", g.ThrottleWindowHours)
	}
	if g.ThrottleMinCloses != 4 {
		t.Errorf("throttle_min_closes = %v, want 4", g.ThrottleMinCloses)
	}
	if g.ThrottleLossRate != 0.75 {
		t.Errorf("throttle_loss_rate = %v, want 0.75", g.ThrottleLossRate)
	}

	// and that WithDefaults leaves explicit values intact (only fills zeros)
	wd := g.WithDefaults()
	if wd.ThrottleWindowHours != 8 || wd.ThrottleMinCloses != 4 || wd.ThrottleLossRate != 0.75 {
		t.Errorf("WithDefaults clobbered explicit throttle values: %+v", wd)
	}
	if wd.CorrelatedAdverseThrottleEnabled() != false {
		t.Errorf("explicit disable must survive WithDefaults/Enabled check")
	}
}
