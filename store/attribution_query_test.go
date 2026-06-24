package store

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newTestEventStore(t *testing.T) *PositionCloseEventStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&PositionCloseEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewPositionCloseEventStore(db)
}

func TestGetAttributionSummaryAndBackfill(t *testing.T) {
	s := newTestEventStore(t)

	// Seed events: 2 managed_drawdown (protection), 1 ai_close (ai), 1 manual,
	// and 1 legacy bare close_long with empty category (to be backfilled).
	seed := []PositionCloseEvent{
		{TraderID: "t1", Symbol: "BTCUSDT", Side: "LONG", CloseReason: "managed_drawdown_runner_exit", Category: CategoryProtection, Mechanism: MechManagedDrawdown, RealizedPnLDelta: 5, FeeDelta: 0.1, EventTime: 2000},
		{TraderID: "t1", Symbol: "ETHUSDT", Side: "LONG", CloseReason: "managed_drawdown", Category: CategoryProtection, Mechanism: MechManagedDrawdown, RealizedPnLDelta: 3, FeeDelta: 0.1, EventTime: 2100},
		{TraderID: "t1", Symbol: "SOLUSDT", Side: "LONG", CloseReason: "ai_close_long", Category: CategoryAI, Mechanism: MechAIClose, RealizedPnLDelta: -2, FeeDelta: 0.05, EventTime: 2200},
		{TraderID: "t1", Symbol: "WLDUSDT", Side: "SHORT", CloseReason: "manual_close_short", Category: CategoryManual, Mechanism: MechManualClose, RealizedPnLDelta: 1, FeeDelta: 0.02, EventTime: 2300},
		{TraderID: "t1", Symbol: "TAOUSDT", Side: "LONG", CloseReason: "managed_drawdown_runner_exit", ExecutionSource: "managed_drawdown_runner_exit", EventTime: 2400}, // empty category -> backfill
	}
	for i := range seed {
		if err := s.Create(&seed[i]); err != nil {
			t.Fatalf("seed create: %v", err)
		}
	}

	// Backfill the legacy row.
	n, err := s.BackfillAttribution("t1")
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if n != 1 {
		t.Errorf("backfill updated=%d want 1", n)
	}

	rows, err := s.GetAttributionSummary("t1", 0)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}

	// Expect protection/managed_drawdown count=3 after backfill.
	var protMD int64
	var aiCount int64
	var manualCount int64
	for _, r := range rows {
		if r.Category == CategoryProtection && r.Mechanism == MechManagedDrawdown {
			protMD = r.Count
		}
		if r.Category == CategoryAI {
			aiCount += r.Count
		}
		if r.Category == CategoryManual {
			manualCount += r.Count
		}
	}
	if protMD != 3 {
		t.Errorf("protection managed_drawdown count=%d want 3", protMD)
	}
	if aiCount != 1 {
		t.Errorf("ai count=%d want 1", aiCount)
	}
	if manualCount != 1 {
		t.Errorf("manual count=%d want 1", manualCount)
	}

	// Backfill again should be idempotent (0 rows).
	n2, _ := s.BackfillAttribution("t1")
	if n2 != 0 {
		t.Errorf("second backfill updated=%d want 0", n2)
	}
}
