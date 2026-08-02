package store

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newExposureStore(t *testing.T) (*PositionStore, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&TraderPosition{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewPositionStore(db), db
}

func addPos(t *testing.T, db *gorm.DB, side string, entryMs, exitMs int64, price, qty float64) {
	t.Helper()
	status := "CLOSED"
	if exitMs == 0 {
		status = "OPEN"
	}
	p := &TraderPosition{
		TraderID: "t", Symbol: "BTCUSDT", Side: side,
		EntryPrice: price, EntryQuantity: qty, Quantity: qty,
		EntryTime: entryMs, ExitTime: exitMs, Status: status,
	}
	if err := db.Create(p).Error; err != nil {
		t.Fatalf("create position: %v", err)
	}
}

func TestExposureCountsOnlyPositionsOpenAtThatInstant(t *testing.T) {
	s, db := newExposureStore(t)
	addPos(t, db, "LONG", 1000, 5000, 100, 1)  // open [1000,5000]
	addPos(t, db, "SHORT", 3000, 9000, 200, 2) // open [3000,9000]

	got, err := s.GetExposureAtTimes("t", []int64{500, 2000, 4000, 6000, 10000})
	if err != nil {
		t.Fatalf("GetExposureAtTimes: %v", err)
	}
	wantCount := []int{0, 1, 2, 1, 0}
	wantNotional := []float64{0, 100, 500, 400, 0}
	for i := range got {
		if got[i].OpenCount != wantCount[i] {
			t.Errorf("t=%d count: want %d, got %d", got[i].TimeMs, wantCount[i], got[i].OpenCount)
		}
		if got[i].Notional != wantNotional[i] {
			t.Errorf("t=%d notional: want %v, got %v", got[i].TimeMs, wantNotional[i], got[i].Notional)
		}
	}
}

func TestExposureSplitsLongAndShort(t *testing.T) {
	s, db := newExposureStore(t)
	addPos(t, db, "LONG", 1000, 0, 100, 3)   // 300 long, still open
	addPos(t, db, "SHORT", 1000, 0, 50, 4)   // 200 short, still open
	got, err := s.GetExposureAtTimes("t", []int64{2000})
	if err != nil {
		t.Fatalf("GetExposureAtTimes: %v", err)
	}
	if got[0].LongNotional != 300 || got[0].ShortNotional != 200 {
		t.Fatalf("want long 300 / short 200, got %v / %v", got[0].LongNotional, got[0].ShortNotional)
	}
	if got[0].Notional != 500 {
		t.Fatalf("total notional want 500, got %v", got[0].Notional)
	}
}

// A row still OPEN (or one whose exit was never stamped) has no closing event and
// must stay counted through the end of the window rather than vanishing.
func TestExposureKeepsStillOpenPositionToEndOfWindow(t *testing.T) {
	s, db := newExposureStore(t)
	addPos(t, db, "LONG", 1000, 0, 100, 1)
	got, err := s.GetExposureAtTimes("t", []int64{2000, 999999999})
	if err != nil {
		t.Fatalf("GetExposureAtTimes: %v", err)
	}
	for _, g := range got {
		if g.OpenCount != 1 || g.Notional != 100 {
			t.Errorf("t=%d: want 1/100, got %d/%v", g.TimeMs, g.OpenCount, g.Notional)
		}
	}
}

// The result must line up with the caller's timestamps even when unsorted, because
// the API passes them in chart order and zips them back onto snapshots by index.
func TestExposurePreservesCallerOrderWithUnsortedInput(t *testing.T) {
	s, db := newExposureStore(t)
	addPos(t, db, "LONG", 1000, 5000, 100, 1)

	times := []int64{6000, 2000, 500, 4000}
	got, err := s.GetExposureAtTimes("t", times)
	if err != nil {
		t.Fatalf("GetExposureAtTimes: %v", err)
	}
	wantCount := []int{0, 1, 0, 1}
	for i := range got {
		if got[i].TimeMs != times[i] {
			t.Fatalf("index %d: time misaligned, want %d got %d", i, times[i], got[i].TimeMs)
		}
		if got[i].OpenCount != wantCount[i] {
			t.Errorf("index %d (t=%d): want count %d, got %d", i, times[i], wantCount[i], got[i].OpenCount)
		}
	}
}

func TestExposureBoundaryEntryOpenExitClosed(t *testing.T) {
	s, db := newExposureStore(t)
	addPos(t, db, "LONG", 1000, 5000, 100, 1)
	got, err := s.GetExposureAtTimes("t", []int64{1000, 5000})
	if err != nil {
		t.Fatalf("GetExposureAtTimes: %v", err)
	}
	if got[0].OpenCount != 1 {
		t.Errorf("a position entered exactly at t must count as open, got %d", got[0].OpenCount)
	}
	if got[1].OpenCount != 0 {
		t.Errorf("a position exited exactly at t must count as closed, got %d", got[1].OpenCount)
	}
}

func TestExposureIgnoresOtherTraders(t *testing.T) {
	s, db := newExposureStore(t)
	addPos(t, db, "LONG", 1000, 0, 100, 1)
	other := &TraderPosition{
		TraderID: "other", Symbol: "ETHUSDT", Side: "LONG",
		EntryPrice: 999, EntryQuantity: 9, Quantity: 9,
		EntryTime: 1000, Status: "OPEN",
	}
	if err := db.Create(other).Error; err != nil {
		t.Fatalf("create other: %v", err)
	}
	got, err := s.GetExposureAtTimes("t", []int64{2000})
	if err != nil {
		t.Fatalf("GetExposureAtTimes: %v", err)
	}
	if got[0].OpenCount != 1 || got[0].Notional != 100 {
		t.Fatalf("other trader leaked in: got %d/%v", got[0].OpenCount, got[0].Notional)
	}
}

func TestExposureEmptyInputs(t *testing.T) {
	s, db := newExposureStore(t)
	got, err := s.GetExposureAtTimes("t", nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("nil times: want empty/no error, got %v / %v", got, err)
	}
	addPos(t, db, "LONG", 1000, 0, 100, 1)
	got, err = s.GetExposureAtTimes("nobody", []int64{2000})
	if err != nil {
		t.Fatalf("unknown trader: %v", err)
	}
	if got[0].OpenCount != 0 || got[0].Notional != 0 {
		t.Fatalf("unknown trader should be flat, got %d/%v", got[0].OpenCount, got[0].Notional)
	}
}
