package store

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newUnknownRecoveryStore(t *testing.T) (*PositionStore, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&TraderPosition{}, &TraderOrder{}, &TraderFill{}, &PositionCloseEvent{}, &DecisionRecordDB{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewPositionStore(db), db
}

// A close event whose closeReason is the bare literal "unknown_close" but whose
// executionSource carries a real mechanism (ladder_tp) must be recovered to that
// mechanism. Regression for the 7 claude ZEC ladder-TP fills stranded as
// unknown_close because the recovery gate only fired on sync_external.
func TestLogCloseEvent_UnknownCloseRecoversFromExecutionSource(t *testing.T) {
	s, db := newUnknownRecoveryStore(t)
	pos := &TraderPosition{
		TraderID: "claude", ExchangeID: "ex", Symbol: "ZECUSDT", Side: "LONG",
		Quantity: 1, EntryQuantity: 1, EntryPrice: 50, EntryTime: 1, Status: "CLOSED",
	}
	if err := db.Create(pos).Error; err != nil {
		t.Fatalf("create position: %v", err)
	}
	if err := s.logCloseEvent(pos, "unknown_close", "ladder_tp", "MARKET", "", 0.3, 52, 0, -0.09, time.Now().UTC().UnixMilli()); err != nil {
		t.Fatalf("logCloseEvent: %v", err)
	}
	var ev PositionCloseEvent
	if err := db.Where("position_id = ?", pos.ID).First(&ev).Error; err != nil {
		t.Fatalf("reload close event: %v", err)
	}
	if ev.Category != CategoryProtection || ev.Mechanism != MechLadderTP {
		t.Fatalf("expected protection/ladder_tp, got category=%q mechanism=%q reason=%q", ev.Category, ev.Mechanism, ev.CloseReason)
	}
}

// When neither closeReason nor executionSource resolves, the event stays
// unknown_close (no spurious upgrade).
func TestLogCloseEvent_UnknownCloseStaysWhenNoRecovery(t *testing.T) {
	s, db := newUnknownRecoveryStore(t)
	pos := &TraderPosition{
		TraderID: "claude", ExchangeID: "ex", Symbol: "ZECUSDT", Side: "LONG",
		Quantity: 1, EntryQuantity: 1, EntryPrice: 50, EntryTime: 1, Status: "CLOSED",
	}
	if err := db.Create(pos).Error; err != nil {
		t.Fatalf("create position: %v", err)
	}
	if err := s.logCloseEvent(pos, "unknown_close", "unknown_close", "MARKET", "", 1, 52, 0, -0.5, time.Now().UTC().UnixMilli()); err != nil {
		t.Fatalf("logCloseEvent: %v", err)
	}
	var ev PositionCloseEvent
	if err := db.Where("position_id = ?", pos.ID).First(&ev).Error; err != nil {
		t.Fatalf("reload close event: %v", err)
	}
	if ev.Mechanism != MechUnknownClose {
		t.Fatalf("expected unknown_close to remain, got mechanism=%q", ev.Mechanism)
	}
}
