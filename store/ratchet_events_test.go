package store

import (
	"path/filepath"
	"testing"
)

func TestRatchetEventsAppendLoadDelete(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "ratchet-events.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	key := "trader-1|ETHUSDT|1h|SHORT"

	// Empty log loads as nil (not an error).
	if evs, err := s.LoadRatchetEvents(key); err != nil || evs != nil {
		t.Fatalf("expected empty load nil/nil, got %v / %v", evs, err)
	}

	// Append two tightens in order.
	e1 := RatchetEvent{TraderID: "trader-1", Symbol: "ETHUSDT", Side: "SHORT", EntryPrice: 1858.0, Seq: 1, Boundary: 1870.0, PrevBoundary: 1880.0, PriceAtEvent: 1855.0, DistPct: 0.81, AtrMult: 1.5, Timestamp: 100}
	e2 := RatchetEvent{TraderID: "trader-1", Symbol: "ETHUSDT", Side: "SHORT", EntryPrice: 1858.0, Seq: 2, Boundary: 1862.0, PrevBoundary: 1870.0, PriceAtEvent: 1840.0, DistPct: 1.20, AtrMult: 2.1, Timestamp: 200}
	if err := s.AppendRatchetEvent(key, e1); err != nil {
		t.Fatalf("append e1: %v", err)
	}
	if err := s.AppendRatchetEvent(key, e2); err != nil {
		t.Fatalf("append e2: %v", err)
	}

	evs, err := s.LoadRatchetEvents(key)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("expected 2 events, got %d", len(evs))
	}
	// Order preserved; values survive round-trip.
	if evs[0].Seq != 1 || evs[1].Seq != 2 {
		t.Fatalf("order not preserved: %d, %d", evs[0].Seq, evs[1].Seq)
	}
	if evs[1].Boundary != 1862.0 || evs[1].AtrMult != 2.1 || evs[1].DistPct != 1.20 {
		t.Fatalf("event values changed across round-trip: %+v", evs[1])
	}

	// A second key is isolated.
	other := "trader-1|BTCUSDT|1h|LONG"
	if err := s.AppendRatchetEvent(other, RatchetEvent{Symbol: "BTCUSDT", Seq: 1}); err != nil {
		t.Fatalf("append other: %v", err)
	}
	if evs, _ := s.LoadRatchetEvents(key); len(evs) != 2 {
		t.Fatalf("cross-key contamination: key now has %d", len(evs))
	}

	// Delete removes only the target key.
	if err := s.DeleteRatchetEvents(key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if evs, _ := s.LoadRatchetEvents(key); evs != nil {
		t.Fatalf("expected nil after delete, got %v", evs)
	}
	if evs, _ := s.LoadRatchetEvents(other); len(evs) != 1 {
		t.Fatalf("delete leaked to other key: %d", len(evs))
	}

	// Deleting a missing key is a no-op (no error).
	if err := s.DeleteRatchetEvents("nonexistent"); err != nil {
		t.Fatalf("delete missing key should be no-op: %v", err)
	}
}
