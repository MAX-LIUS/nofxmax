package store

import (
	"path/filepath"
	"testing"
)

func TestGivebackGuardStateRoundTrip(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "giveback-guard.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	want := GivebackGuardState{
		PortfolioPeakUnreal: 1234.56,
		L2FiredAtPeak:       1000.00,
		L1FiredAtPeak: map[string]float64{
			"BTCUSDT_long":  5.5,
			"ETHUSDT_short": 3.2,
		},
	}
	if err := s.SaveGivebackGuardState("trader-1", want); err != nil {
		t.Fatalf("save guard state: %v", err)
	}

	got, err := s.LoadGivebackGuardState("trader-1")
	if err != nil {
		t.Fatalf("load guard state: %v", err)
	}
	if got.PortfolioPeakUnreal != want.PortfolioPeakUnreal {
		t.Fatalf("portfolio peak: want %.2f got %.2f", want.PortfolioPeakUnreal, got.PortfolioPeakUnreal)
	}
	if got.L2FiredAtPeak != want.L2FiredAtPeak {
		t.Fatalf("l2 latch: want %.2f got %.2f", want.L2FiredAtPeak, got.L2FiredAtPeak)
	}
	if len(got.L1FiredAtPeak) != 2 || got.L1FiredAtPeak["BTCUSDT_long"] != 5.5 || got.L1FiredAtPeak["ETHUSDT_short"] != 3.2 {
		t.Fatalf("l1 ratchets mismatch: %+v", got.L1FiredAtPeak)
	}
}

func TestGivebackGuardStateMissingReturnsZeroValue(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "giveback-guard-empty.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	got, err := s.LoadGivebackGuardState("nobody")
	if err != nil {
		t.Fatalf("load missing guard state should not error: %v", err)
	}
	if got.PortfolioPeakUnreal != 0 || got.L2FiredAtPeak != 0 {
		t.Fatalf("missing state should be zero, got %+v", got)
	}
	if got.L1FiredAtPeak == nil || len(got.L1FiredAtPeak) != 0 {
		t.Fatalf("missing state should have empty (non-nil) L1 map, got %+v", got.L1FiredAtPeak)
	}
}

func TestGivebackGuardStateUpsertOverwrites(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "giveback-guard-upsert.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := s.SaveGivebackGuardState("t", GivebackGuardState{PortfolioPeakUnreal: 100, L1FiredAtPeak: map[string]float64{"A_long": 1}}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := s.SaveGivebackGuardState("t", GivebackGuardState{PortfolioPeakUnreal: 250, L1FiredAtPeak: map[string]float64{"B_short": 2}}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	got, err := s.LoadGivebackGuardState("t")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.PortfolioPeakUnreal != 250 {
		t.Fatalf("upsert peak: want 250 got %.2f", got.PortfolioPeakUnreal)
	}
	if _, stale := got.L1FiredAtPeak["A_long"]; stale {
		t.Fatalf("upsert should replace L1 map, stale key remains: %+v", got.L1FiredAtPeak)
	}
	if got.L1FiredAtPeak["B_short"] != 2 {
		t.Fatalf("upsert L1: %+v", got.L1FiredAtPeak)
	}
}
