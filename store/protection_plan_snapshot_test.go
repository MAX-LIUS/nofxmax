package store

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newSnapshotStore(t *testing.T) *ProtectionPlanSnapshotStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	s := NewProtectionPlanSnapshotStore(db)
	if err := s.InitTables(); err != nil {
		t.Fatalf("init tables: %v", err)
	}
	return s
}

func TestProtectionPlanSnapshot_SaveAndFindClosest(t *testing.T) {
	s := newSnapshotStore(t)
	price := 110.0
	ratio := 50.0
	tiers := []ProtectionPlanTier{
		{Mechanism: MechLadderTP, Kind: "tp", Label: "TP1", TriggerPct: 10, TriggerPrice: &price, CloseRatioPct: &ratio},
		{Mechanism: MechLadderSL, Kind: "sl", Label: "SL", TriggerPct: -5},
	}
	// Two snapshots for same key at different times; panel should pick closest.
	if err := s.Save("t", "ex", "BTCUSDT", "long", "combined", 100, 7, tiers, 100000); err != nil {
		t.Fatalf("save1: %v", err)
	}
	if err := s.Save("t", "ex", "BTCUSDT", "long", "combined", 100, 8, tiers, 500000); err != nil {
		t.Fatalf("save2: %v", err)
	}

	// entry near the second snapshot.
	snap, err := s.FindForPosition("t", "BTCUSDT", "LONG", 490000, 60000)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if snap == nil {
		t.Fatal("expected a snapshot, got nil")
	}
	if snap.SnapshotTime != 500000 {
		t.Fatalf("expected closest snapshot 500000, got %d", snap.SnapshotTime)
	}
	got, err := snap.Tiers()
	if err != nil {
		t.Fatalf("decode tiers: %v", err)
	}
	if len(got) != 2 || got[0].Label != "TP1" || got[0].TriggerPrice == nil || *got[0].TriggerPrice != 110 {
		t.Fatalf("tiers round-trip wrong: %+v", got)
	}
}

func TestProtectionPlanSnapshot_FindOutOfToleranceReturnsNil(t *testing.T) {
	s := newSnapshotStore(t)
	tiers := []ProtectionPlanTier{{Mechanism: MechFullSL, Kind: "sl", Label: "SL", TriggerPct: -4}}
	if err := s.Save("t", "ex", "ETHUSDT", "SHORT", "full", 100, 1, tiers, 1_000_000); err != nil {
		t.Fatalf("save: %v", err)
	}
	snap, err := s.FindForPosition("t", "ETHUSDT", "SHORT", 5_000_000, 60_000)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if snap != nil {
		t.Fatalf("expected nil out-of-tolerance, got %+v", snap)
	}
}
