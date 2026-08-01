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
	base := SaveProtectionPlanSnapshotInput{
		TraderID: "t", ExchangeID: "ex", Symbol: "BTCUSDT", Side: "long",
		Mode: "combined", EntryPrice: 100, Tiers: tiers,
		ATRValue: 2.5, ATRTimeframe: "15m",
	}
	// Two snapshots for same key at different times; panel should pick closest.
	first := base
	first.DecisionCycle, first.SnapshotTimeMs = 7, 100000
	if err := s.Save(first); err != nil {
		t.Fatalf("save1: %v", err)
	}
	second := base
	second.DecisionCycle, second.SnapshotTimeMs = 8, 500000
	if err := s.Save(second); err != nil {
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
	// The ATR context must survive the round-trip: it is the only record of the
	// denominator behind the stored percents once the position closes.
	if snap.ATRValue != 2.5 || snap.ATRTimeframe != "15m" {
		t.Fatalf("ATR context lost: value=%v tf=%q", snap.ATRValue, snap.ATRTimeframe)
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
	if err := s.Save(SaveProtectionPlanSnapshotInput{
		TraderID: "t", ExchangeID: "ex", Symbol: "ETHUSDT", Side: "SHORT",
		Mode: "full", EntryPrice: 100, DecisionCycle: 1, Tiers: tiers,
		SnapshotTimeMs: 1_000_000,
	}); err != nil {
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
