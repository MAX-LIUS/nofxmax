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
