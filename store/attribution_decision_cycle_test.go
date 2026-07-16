package store

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newDecisionCycleStore(t *testing.T) (*PositionStore, *gorm.DB) {
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

// A bare close_long fill with no close-intent (the Binance failure mode) must be
// upgraded to ai_close_long when the exit decision cycle links to a real AI close
// decision for that symbol/side — making the stored attribution deterministic
// instead of leaving it as sync_external (未归因).
func TestLogCloseEvent_DecisionCycleUpgradesBareCloseToAIClose(t *testing.T) {
	s, db := newDecisionCycleStore(t)

	// A real AI close decision recorded for cycle 304, BCHUSDT long.
	rec := &DecisionRecordDB{
		TraderID:    "bn",
		CycleNumber: 304,
		Timestamp:   time.Now().UTC(),
		Decisions:   `[{"action":"close_long","symbol":"BCHUSDT"},{"action":"wait","symbol":"ETHUSDT"}]`,
	}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("create decision: %v", err)
	}

	pos := &TraderPosition{
		TraderID: "bn", ExchangeID: "ex", Symbol: "BCHUSDT", Side: "LONG",
		Quantity: 1, EntryQuantity: 1, EntryPrice: 240, EntryTime: 1, Status: "CLOSED",
		ExitDecisionCycle: 304,
	}
	if err := db.Create(pos).Error; err != nil {
		t.Fatalf("create position: %v", err)
	}

	// Bare close_long, no order, no intent — the exact Binance situation.
	if err := s.logCloseEvent(pos, "close_long", "close_long", "MARKET", "", 1, 236, 0, -1.45, time.Now().UTC().UnixMilli()); err != nil {
		t.Fatalf("logCloseEvent: %v", err)
	}

	var ev PositionCloseEvent
	if err := db.Where("position_id = ?", pos.ID).First(&ev).Error; err != nil {
		t.Fatalf("reload close event: %v", err)
	}
	if ev.Category != CategoryAI || ev.Mechanism != MechAIClose {
		t.Fatalf("expected AI/ai_close attribution, got category=%q mechanism=%q reason=%q", ev.Category, ev.Mechanism, ev.CloseReason)
	}
}

// A bare close with a decision cycle that has NO matching close decision must be
// left as sync_external — never fabricate an AI attribution.
func TestLogCloseEvent_NoMatchingDecisionKeepsSyncExternal(t *testing.T) {
	s, db := newDecisionCycleStore(t)

	// Cycle exists but only holds an open decision for a different symbol.
	rec := &DecisionRecordDB{
		TraderID:    "bn",
		CycleNumber: 305,
		Timestamp:   time.Now().UTC(),
		Decisions:   `[{"action":"open_long","symbol":"SOLUSDT"}]`,
	}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("create decision: %v", err)
	}

	pos := &TraderPosition{
		TraderID: "bn", ExchangeID: "ex", Symbol: "BCHUSDT", Side: "LONG",
		Quantity: 1, EntryQuantity: 1, EntryPrice: 240, EntryTime: 1, Status: "CLOSED",
		ExitDecisionCycle: 305,
	}
	if err := db.Create(pos).Error; err != nil {
		t.Fatalf("create position: %v", err)
	}

	if err := s.logCloseEvent(pos, "close_long", "close_long", "MARKET", "", 1, 236, 0, -1.45, time.Now().UTC().UnixMilli()); err != nil {
		t.Fatalf("logCloseEvent: %v", err)
	}

	var ev PositionCloseEvent
	if err := db.Where("position_id = ?", pos.ID).First(&ev).Error; err != nil {
		t.Fatalf("reload close event: %v", err)
	}
	if ev.Mechanism != MechSyncExternal {
		t.Fatalf("expected sync_external (no matching decision), got mechanism=%q", ev.Mechanism)
	}
}

// FindEntryDecisionCycleForPosition must pick the open decision NEAREST the
// entry time, not merely the highest cycle number that a corrupted SQL datetime
// comparison lets through. This reproduces the modernc `datetime`-coercion bug:
// a later re-entry of the same symbol (cycle 102 @ 21:22) previously tested as
// `<= entry+90s` and got linked instead of the true entry cycle (89 @ 12:23).
func TestFindEntryDecisionCycle_PicksNearestNotLatest(t *testing.T) {
	s, db := newDecisionCycleStore(t)

	base := time.Date(2026, 7, 2, 12, 23, 10, 0, time.UTC)
	// True entry cycle: open_long ETHUSDT ~8s after entry.
	mustDecision(t, db, "bn", 89, base, `[{"action":"open_long","symbol":"ETHUSDT"}]`)
	// A later re-entry of the SAME symbol, ~9 hours later.
	mustDecision(t, db, "bn", 102, base.Add(9*time.Hour), `[{"action":"open_long","symbol":"ETHUSDT"}]`)
	// An earlier re-entry too, for good measure.
	mustDecision(t, db, "bn", 43, base.Add(-15*time.Hour), `[{"action":"open_long","symbol":"ETHUSDT"}]`)

	entryMs := base.Add(-8 * time.Second).UnixMilli() // order filled ~8s before decision record
	got := s.FindEntryDecisionCycleForPosition("bn", "ETHUSDT", "LONG", entryMs)
	if got != 89 {
		t.Fatalf("expected nearest cycle 89, got %d", got)
	}
}

// A matching open decision that is far outside the 6h skew bound must NOT be
// linked (guards against fabricating a link from a stale historical re-entry).
func TestFindEntryDecisionCycle_RejectsBeyondSkewBound(t *testing.T) {
	s, db := newDecisionCycleStore(t)
	base := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	mustDecision(t, db, "bn", 500, base, `[{"action":"open_long","symbol":"ETHUSDT"}]`)
	// Entry is 12h before the only matching decision — beyond the 6h bound.
	entryMs := base.Add(-12 * time.Hour).UnixMilli()
	if got := s.FindEntryDecisionCycleForPosition("bn", "ETHUSDT", "LONG", entryMs); got != 0 {
		t.Fatalf("expected 0 (beyond skew bound), got %d", got)
	}
}

func mustDecision(t *testing.T, db *gorm.DB, trader string, cycle int, ts time.Time, decisions string) {
	t.Helper()
	rec := &DecisionRecordDB{
		TraderID:    trader,
		CycleNumber: cycle,
		Timestamp:   ts,
		CreatedAt:   ts,
		Success:     true,
		Decisions:   decisions,
	}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("create decision cycle %d: %v", cycle, err)
	}
}
