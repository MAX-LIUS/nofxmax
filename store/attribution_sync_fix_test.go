package store

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newAttribFixStore(t *testing.T) (*PositionStore, *gorm.DB) {
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

// When a position is detected absent from the exchange but a tagged FILLED
// closing order exists, the close reason must be derived from the order tag
// (e.g. full_sl) instead of the generic sync_absent_from_exchange.
func TestSyncAbsent_DerivesProtectionReasonFromOrder(t *testing.T) {
	s, db := newAttribFixStore(t)

	pos := &TraderPosition{
		TraderID: "t", ExchangeID: "ex", Symbol: "BTCUSDT", Side: "LONG",
		Quantity: 1, EntryQuantity: 1, EntryPrice: 100, EntryTime: 1, Status: "OPEN",
	}
	if err := s.CreateOpenPosition(pos); err != nil {
		t.Fatalf("create position: %v", err)
	}

	// A FILLED stop-loss close order linked to the position, tagged full_sl.
	ord := &TraderOrder{
		TraderID: "t", ExchangeID: "ex", ExchangeOrderID: "SL-1",
		Symbol: "BTCUSDT", Side: "SELL", Type: "STOP_MARKET", Status: "FILLED",
		FilledQuantity: 1, AvgFillPrice: 95, OrderAction: "full_sl",
		ClientOrderID: "full_sl_tag", RelatedPositionID: pos.ID,
	}
	if err := db.Create(ord).Error; err != nil {
		t.Fatalf("create order: %v", err)
	}

	updated, err := s.MarkOpenPositionsAbsentFromExchangeClosed("t", map[string]float64{}, "sync_absent_from_exchange")
	if err != nil {
		t.Fatalf("mark absent: %v", err)
	}
	if updated != 1 {
		t.Fatalf("expected 1 update, got %d", updated)
	}

	var closed TraderPosition
	if err := db.First(&closed, pos.ID).Error; err != nil {
		t.Fatalf("reload position: %v", err)
	}
	if closed.CloseReason != "full_sl" {
		t.Fatalf("expected close_reason=full_sl (derived), got %q", closed.CloseReason)
	}
}

// When no linked closing order exists, the generic sync reason is preserved.
func TestSyncAbsent_FallsBackToGenericWhenNoOrder(t *testing.T) {
	s, db := newAttribFixStore(t)
	pos := &TraderPosition{
		TraderID: "t", ExchangeID: "ex", Symbol: "ETHUSDT", Side: "SHORT",
		Quantity: 1, EntryQuantity: 1, EntryPrice: 2000, EntryTime: 1, Status: "OPEN",
	}
	if err := s.CreateOpenPosition(pos); err != nil {
		t.Fatalf("create position: %v", err)
	}
	if _, err := s.MarkOpenPositionsAbsentFromExchangeClosed("t", map[string]float64{}, "sync_absent_from_exchange"); err != nil {
		t.Fatalf("mark absent: %v", err)
	}
	var closed TraderPosition
	if err := db.First(&closed, pos.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if closed.CloseReason != "sync_absent_from_exchange" {
		t.Fatalf("expected generic sync reason preserved, got %q", closed.CloseReason)
	}
}
