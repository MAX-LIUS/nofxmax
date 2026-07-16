package store

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newTSStore(t *testing.T) (*ShadowGateStore, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&ShadowGateVerdict{}, &TraderPosition{}, &BlockedSimOutcome{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewShadowGateStore(db), db
}

// Kept trades use real closed-position PnL; blocked trades fall back to blocksim
// sim_pnl. The four categories must partition correctly and bucket by period.
func TestRuleTimeSeries_FourCategoriesAndBlocksimFallback(t *testing.T) {
	s, db := newTSStore(t)
	const hour = 3600_000
	base := int64(1_000_000_000_000)
	base = base - base%hour // align to 1h bucket

	// A KEPT winner: verdict would_block=false, linked to a closed +PnL position.
	pWin := &TraderPosition{TraderID: "t", Symbol: "BTCUSDT", Side: "LONG", Status: "CLOSED", RealizedPnL: 5, ExitTime: base + 100}
	db.Create(pWin)
	db.Create(&ShadowGateVerdict{TraderID: "t", Symbol: "BTCUSDT", Side: "LONG", RuleName: "r", WouldBlock: false, PositionID: pWin.ID, ObservedAt: base + 50})

	// A KEPT loser in the SAME bucket.
	pLoss := &TraderPosition{TraderID: "t", Symbol: "ETHUSDT", Side: "LONG", Status: "CLOSED", RealizedPnL: -3, ExitTime: base + 200}
	db.Create(pLoss)
	db.Create(&ShadowGateVerdict{TraderID: "t", Symbol: "ETHUSDT", Side: "LONG", RuleName: "r", WouldBlock: false, PositionID: pLoss.ID, ObservedAt: base + 60})

	// A BLOCKED trade (no position) resolved via blocksim as a loss (correct block),
	// in the NEXT bucket.
	db.Create(&BlockedSimOutcome{TraderID: "t", Symbol: "SOLUSDT", Side: "SHORT", Cycle: 7, SimStatus: "done", SimPnL: -2, SimAt: base + hour + 10})
	db.Create(&ShadowGateVerdict{TraderID: "t", Symbol: "SOLUSDT", Side: "SHORT", Cycle: 7, RuleName: "r", WouldBlock: true, ObservedAt: base + hour + 5})

	pts, err := s.RuleTimeSeries("t", "r", "1h", "")
	if err != nil {
		t.Fatalf("timeseries: %v", err)
	}
	if len(pts) != 2 {
		t.Fatalf("expected 2 buckets, got %d: %+v", len(pts), pts)
	}
	b0 := pts[0]
	if b0.KeepWinN != 1 || b0.KeepWinPnL != 5 {
		t.Errorf("bucket0 keep-win wrong: %+v", b0)
	}
	if b0.KeepLossN != 1 || b0.KeepLossPnL != -3 {
		t.Errorf("bucket0 keep-loss wrong: %+v", b0)
	}
	b1 := pts[1]
	if b1.BlockLossN != 1 || b1.BlockLossPnL != -2 {
		t.Errorf("bucket1 block-loss (blocksim fallback) wrong: %+v", b1)
	}
	if b1.BucketStart <= b0.BucketStart {
		t.Errorf("buckets not ascending: %d then %d", b0.BucketStart, b1.BucketStart)
	}
}
