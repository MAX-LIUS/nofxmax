package store

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Regression: a blocked entry (LiveAllowed=false, WouldBlock=true) must persist
// its real boolean values. The GORM default:true tag on live_allowed previously
// caused false to be omitted from the INSERT, so the column default (true)
// silently overwrote every live block. RecordBatch now Selects the columns.
func TestRecordBatch_PersistsFalseLiveAllowed(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&ShadowGateVerdict{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s := NewShadowGateStore(db)

	v := &ShadowGateVerdict{
		TraderID: "t", Cycle: 1, Symbol: "BTCUSDT", Action: "open_long", Side: "LONG",
		RuleName: "countertrend_slope50", WouldBlock: true, LiveAllowed: false,
		ObservedAt: 1000, CreatedAt: 1000,
	}
	if err := s.RecordBatch([]*ShadowGateVerdict{v}); err != nil {
		t.Fatalf("record: %v", err)
	}

	var got ShadowGateVerdict
	if err := db.First(&got, v.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.LiveAllowed != false {
		t.Errorf("LiveAllowed should persist as false, got %v", got.LiveAllowed)
	}
	if got.WouldBlock != true {
		t.Errorf("WouldBlock should persist as true, got %v", got.WouldBlock)
	}
}
