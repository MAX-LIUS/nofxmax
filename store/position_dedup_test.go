package store

import (
	"math"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newDedupStore(t *testing.T) (*PositionStore, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&TraderPosition{}, &TraderOrder{}, &TraderFill{}, &PositionCloseEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewPositionStore(db), db
}

func approxEq(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// Two OPEN LONG rows for the same symbol (the HYPE churn scenario) must collapse
// into one net row: summed quantity, weighted entry, earliest entry_time kept.
func TestMergeDuplicateOpenPositions_CollapsesToNetRow(t *testing.T) {
	s, db := newDedupStore(t)

	// Insert two OPEN rows directly (bypassing the CreateOpenPosition guard) to
	// simulate the legacy duplicate state already in production.
	a := &TraderPosition{TraderID: "t", ExchangeID: "ex", ExchangePositionID: "sync_HYPE_LONG_1", Symbol: "HYPEUSDT", Side: "LONG", Quantity: 0.4, EntryQuantity: 0.4, EntryPrice: 67.29, EntryTime: 1000, Fee: 0.01, Status: "OPEN"}
	b := &TraderPosition{TraderID: "t", ExchangeID: "ex", ExchangePositionID: "sync_HYPE_LONG_2", Symbol: "HYPEUSDT", Side: "LONG", Quantity: 0.7, EntryQuantity: 0.7, EntryPrice: 67.86, EntryTime: 2000, Fee: 0.02, Status: "OPEN"}
	if err := db.Create(a).Error; err != nil {
		t.Fatalf("create a: %v", err)
	}
	if err := db.Create(b).Error; err != nil {
		t.Fatalf("create b: %v", err)
	}

	res, err := s.MergeDuplicateOpenPositionsForKey("t", "HYPEUSDT", "LONG", false)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if res == nil {
		t.Fatal("expected a merge result")
	}
	if res.KeptID != a.ID {
		t.Fatalf("expected earliest row %d kept, got %d", a.ID, res.KeptID)
	}
	if !approxEq(res.NetQuantity, 1.1) {
		t.Fatalf("expected net qty 1.1, got %.8f", res.NetQuantity)
	}
	// weighted entry = (67.29*0.4 + 67.86*0.7)/1.1 = 67.6527...
	wantEntry := (67.29*0.4 + 67.86*0.7) / 1.1
	if math.Abs(res.WeightedEntry-wantEntry) > 0.01 {
		t.Fatalf("expected weighted entry ~%.4f, got %.4f", wantEntry, res.WeightedEntry)
	}

	// Only ONE OPEN row should remain after merge.
	var openRows []TraderPosition
	if err := db.Where("trader_id = ? AND symbol = ? AND side = ? AND status = ?", "t", "HYPEUSDT", "LONG", "OPEN").Find(&openRows).Error; err != nil {
		t.Fatalf("query open rows: %v", err)
	}
	if len(openRows) != 1 {
		t.Fatalf("expected exactly 1 OPEN net row after merge, got %d", len(openRows))
	}
	if openRows[0].ID != a.ID || !approxEq(openRows[0].Quantity, 1.1) {
		t.Fatalf("net row mismatch: id=%d qty=%.8f", openRows[0].ID, openRows[0].Quantity)
	}
	if !approxEq(openRows[0].Fee, 0.03) {
		t.Fatalf("expected summed fee 0.03, got %.8f", openRows[0].Fee)
	}

	// The secondary row must be retired as merged.
	var retired TraderPosition
	if err := db.First(&retired, b.ID).Error; err != nil {
		t.Fatalf("reload merged row: %v", err)
	}
	if retired.Status != "CLOSED" || retired.CloseReason != "merged_into_net_position" {
		t.Fatalf("expected merged row CLOSED/merged_into_net_position, got status=%q reason=%q", retired.Status, retired.CloseReason)
	}
}

// Dry-run must compute the same plan but not write anything.
func TestMergeDuplicateOpenPositions_DryRunNoWrite(t *testing.T) {
	s, db := newDedupStore(t)
	a := &TraderPosition{TraderID: "t", Symbol: "HYPEUSDT", Side: "LONG", Quantity: 0.4, EntryQuantity: 0.4, EntryPrice: 67.29, EntryTime: 1000, Status: "OPEN"}
	b := &TraderPosition{TraderID: "t", Symbol: "HYPEUSDT", Side: "LONG", Quantity: 0.7, EntryQuantity: 0.7, EntryPrice: 67.86, EntryTime: 2000, Status: "OPEN"}
	_ = db.Create(a).Error
	_ = db.Create(b).Error

	res, err := s.MergeDuplicateOpenPositionsForKey("t", "HYPEUSDT", "LONG", true)
	if err != nil {
		t.Fatalf("dry-run merge: %v", err)
	}
	if res == nil || !approxEq(res.NetQuantity, 1.1) {
		t.Fatalf("dry-run should still compute net plan, got %+v", res)
	}
	var openRows []TraderPosition
	_ = db.Where("status = ?", "OPEN").Find(&openRows).Error
	if len(openRows) != 2 {
		t.Fatalf("dry-run must not write; expected 2 OPEN rows, got %d", len(openRows))
	}
}

// The CreateOpenPosition net guard must merge a same-side open instead of
// creating a second OPEN row, even when the exchange_position_id differs.
func TestCreateOpenPosition_NetGuardMergesSameSide(t *testing.T) {
	s, db := newDedupStore(t)

	first := &TraderPosition{TraderID: "t", ExchangeID: "ex", ExchangePositionID: "sync_HYPE_LONG_1", Symbol: "HYPEUSDT", Side: "LONG", Quantity: 0.4, EntryQuantity: 0.4, EntryPrice: 67.29, EntryTime: 1000, Status: "OPEN"}
	if err := s.CreateOpenPosition(first); err != nil {
		t.Fatalf("create first: %v", err)
	}

	// Second sync open with a DIFFERENT exchange_position_id (the race condition).
	second := &TraderPosition{TraderID: "t", ExchangeID: "ex", ExchangePositionID: "sync_HYPE_LONG_2", Symbol: "HYPEUSDT", Side: "LONG", Quantity: 0.7, EntryQuantity: 0.7, EntryPrice: 67.86, EntryTime: 2000, Status: "OPEN"}
	if err := s.CreateOpenPosition(second); err != nil {
		t.Fatalf("create second: %v", err)
	}

	var openRows []TraderPosition
	if err := db.Where("trader_id = ? AND symbol = ? AND side = ? AND status = ?", "t", "HYPEUSDT", "LONG", "OPEN").Find(&openRows).Error; err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(openRows) != 1 {
		t.Fatalf("net guard should keep a single OPEN net row, got %d", len(openRows))
	}
	if !approxEq(openRows[0].Quantity, 1.1) {
		t.Fatalf("expected merged qty 1.1, got %.8f", openRows[0].Quantity)
	}
}

// A single OPEN row must be a no-op for the merge.
func TestMergeDuplicateOpenPositions_SingleRowNoop(t *testing.T) {
	s, db := newDedupStore(t)
	a := &TraderPosition{TraderID: "t", Symbol: "BTCUSDT", Side: "LONG", Quantity: 1, EntryQuantity: 1, EntryPrice: 100, EntryTime: 1, Status: "OPEN"}
	_ = db.Create(a).Error
	res, err := s.MergeDuplicateOpenPositionsForKey("t", "BTCUSDT", "LONG", false)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if res != nil {
		t.Fatalf("expected no merge for single row, got %+v", res)
	}
}
