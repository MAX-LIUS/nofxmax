package store

import (
	"path/filepath"
	"testing"
)

func TestBreadthVelocityStateRoundTrip(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "breadth-vel.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	want := BreadthVelocityState{
		PnlHist: map[string][]float64{
			"BTCUSDT_long":  {1.2, 1.0, 0.7, 0.3},
			"ETHUSDT_short": {-0.5, -0.8},
		},
		LastBarMs:     1719400000000,
		BarsSinceFire: 3,
	}
	if err := s.SaveBreadthVelocityState("trader-1", want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.LoadBreadthVelocityState("trader-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.LastBarMs != want.LastBarMs || got.BarsSinceFire != want.BarsSinceFire {
		t.Fatalf("scalar mismatch: got %+v want %+v", got, want)
	}
	if len(got.PnlHist) != 2 ||
		len(got.PnlHist["BTCUSDT_long"]) != 4 || got.PnlHist["BTCUSDT_long"][3] != 0.3 ||
		len(got.PnlHist["ETHUSDT_short"]) != 2 || got.PnlHist["ETHUSDT_short"][0] != -0.5 {
		t.Fatalf("hist mismatch: %+v", got.PnlHist)
	}
}

func TestBreadthVelocityStateMissingReturnsEmpty(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "breadth-vel-empty.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	got, err := s.LoadBreadthVelocityState("nobody")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.PnlHist == nil || len(got.PnlHist) != 0 || got.LastBarMs != 0 {
		t.Fatalf("missing state should be empty non-nil, got %+v", got)
	}
}

func TestBreadthVelocityStateUpsert(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "breadth-vel-upsert.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := s.SaveBreadthVelocityState("t", BreadthVelocityState{
		PnlHist: map[string][]float64{"A_long": {1}}, LastBarMs: 100, BarsSinceFire: 1,
	}); err != nil {
		t.Fatalf("save1: %v", err)
	}
	if err := s.SaveBreadthVelocityState("t", BreadthVelocityState{
		PnlHist: map[string][]float64{"B_short": {2}}, LastBarMs: 200, BarsSinceFire: 0,
	}); err != nil {
		t.Fatalf("save2: %v", err)
	}
	got, err := s.LoadBreadthVelocityState("t")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, stale := got.PnlHist["A_long"]; stale {
		t.Fatalf("upsert should replace hist, stale key remains: %+v", got.PnlHist)
	}
	if got.LastBarMs != 200 || got.PnlHist["B_short"][0] != 2 {
		t.Fatalf("upsert mismatch: %+v", got)
	}
}

// TestBreadthEquityPeakRoundTrip: the account-value breaker's equity high-water
// mark must survive a save/load cycle (a restart that lost it would silently
// re-base the trigger line to the already-drawn-down equity), and re-running the
// migration must be idempotent and must not drop the stored peak.
func TestBreadthEquityPeakRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "breadth-equity.db")
	s, err := New(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	want := BreadthVelocityState{
		PnlHist:       map[string][]float64{"BTCUSDT_long": {1, 2, 3}},
		LastBarMs:     1785920999790,
		BarsSinceFire: 4,
		EquityPeak:    162.399,
		EquityPeakAt:  1785919332917,
	}
	if err := s.SaveBreadthVelocityState("t1", want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.LoadBreadthVelocityState("t1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.EquityPeak != want.EquityPeak || got.EquityPeakAt != want.EquityPeakAt {
		t.Fatalf("equity peak round-trip: got %.4f@%d want %.4f@%d",
			got.EquityPeak, got.EquityPeakAt, want.EquityPeak, want.EquityPeakAt)
	}
	if got.BarsSinceFire != want.BarsSinceFire || got.LastBarMs != want.LastBarMs {
		t.Fatalf("pre-existing fields regressed: %+v", got)
	}
	if len(got.PnlHist["BTCUSDT_long"]) != 3 {
		t.Fatalf("velocity history regressed: %+v", got.PnlHist)
	}

	// Idempotency: re-run the unified-protection migration on the same DB. The
	// ADD COLUMN guards must skip the now-existing columns instead of erroring,
	// and the stored peak must be untouched.
	if err := MigrateUnifiedProtection(s.DB()); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	got2, err := s.LoadBreadthVelocityState("t1")
	if err != nil {
		t.Fatalf("load after re-migrate: %v", err)
	}
	if got2.EquityPeak != want.EquityPeak {
		t.Fatalf("re-migration lost the equity peak: got %.4f", got2.EquityPeak)
	}
}
