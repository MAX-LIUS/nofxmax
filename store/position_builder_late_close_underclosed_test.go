package store

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

// TestPositionBuilderBackfillsLateCloseAfterReasonRewrittenClosure is the SOL 1627
// regression: a position whose tail closed on the exchange via a resting protection
// order (break-even) but whose local row was marked CLOSED by reconcile with a
// REWRITTEN reason ("ladder_tp", not sync_absent) before the closing fills synced.
// The late fills must still be attributed (qty + PnL), not dropped.
func TestPositionBuilderBackfillsLateCloseAfterReasonRewrittenClosure(t *testing.T) {
	st, err := New(filepath.Join(t.TempDir(), "late-close-underclosed.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	positionStore := st.Position()
	builder := NewPositionBuilder(positionStore)

	base := time.Now().UTC().UnixMilli()
	pos := &TraderPosition{
		TraderID:           "t",
		ExchangeID:         "ex",
		ExchangeType:       "okx",
		ExchangePositionID: "sync_SOLUSDT_SHORT_1",
		Symbol:             "SOLUSDT",
		Side:               "SHORT",
		Quantity:           1.46,
		EntryQuantity:      1.46,
		EntryPrice:         75.54,
		Status:             "OPEN",
		Source:             "sync",
		EntryTime:          base,
	}
	if err := positionStore.CreateOpenPosition(pos); err != nil {
		t.Fatalf("create position: %v", err)
	}
	// Ladder TP closes 0.29 first (the only recorded close event).
	if err := positionStore.ReducePositionQuantity(pos.ID, 0.29, 74.90, 0.001, 0.19, "ladder_tp", "ladder_tp", "MARKET", "ladder-1", base+1000); err != nil {
		t.Fatalf("partial ladder close: %v", err)
	}
	// Reconcile marks the row CLOSED with a REWRITTEN specific reason (not sync_absent),
	// simulating MarkOpenPositionsAbsentFromExchangeClosed's attribution step.
	if err := positionStore.ClosePositionWithAccurateData(pos.ID, 75.31, "reconcile", base+2000, 0.19, 0.001, "ladder_tp"); err != nil {
		t.Fatalf("reconcile close: %v", err)
	}

	// The sync_absent-only matcher must NOT match (reason is ladder_tp).
	if m, err := positionStore.GetRecentlyClosedSyncAbsentPosition("t", "SOLUSDT", "SHORT", base+3000, 5*time.Second); err != nil {
		t.Fatalf("sync-absent matcher: %v", err)
	} else if m != nil {
		t.Fatal("sync_absent matcher must NOT match a ladder_tp-closed position")
	}
	// The broadened under-closed matcher MUST match (0.29 recorded < 1.46 entry).
	if m, err := positionStore.GetRecentlyClosedUnderClosedPosition("t", "SOLUSDT", "SHORT", base+3000, 5*time.Second); err != nil {
		t.Fatalf("under-closed matcher: %v", err)
	} else if m == nil {
		t.Fatal("under-closed matcher must match the under-closed ladder_tp position")
	}

	// The late break-even fills (1.17 total) arrive and must be attributed.
	if err := builder.ProcessTrade("t", "ex", "okx", "SOLUSDT", "SHORT", "close_short", 1.17, 75.31, 0.004, 0, base+3000, "be-late"); err != nil {
		t.Fatalf("process late break-even close: %v", err)
	}

	closedList, err := positionStore.GetClosedPositions("t", 10)
	if err != nil {
		t.Fatalf("get closed positions: %v", err)
	}
	var closed *TraderPosition
	for _, p := range closedList {
		if p.ID == pos.ID {
			closed = p
			break
		}
	}
	if closed == nil {
		t.Fatalf("closed position %d not found", pos.ID)
	}
	// PnL: ladder leg 0.19 + break-even leg (75.54-75.31)*1.17 = 0.2691 → ~0.46 total.
	expectedBePnL := math.Round((75.54-75.31)*1.17*100) / 100
	if math.Abs(closed.RealizedPnL-(0.19+expectedBePnL)) > 0.02 {
		t.Fatalf("expected realized pnl ~%.2f, got %.4f", 0.19+expectedBePnL, closed.RealizedPnL)
	}

	events, err := st.PositionClose().ListByPositionID(pos.ID)
	if err != nil {
		t.Fatalf("list close events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 close events (ladder + late break-even), got %d", len(events))
	}
	var recordedQty float64
	for _, e := range events {
		recordedQty += e.CloseQuantity
	}
	if math.Abs(recordedQty-1.46) > 0.0001 {
		t.Fatalf("expected recorded close qty to reach entry 1.46, got %.4f", recordedQty)
	}
}

// TestUnderClosedMatcher_IgnoresFullyClosedPosition guards the safety gate: a fully
// closed position (recorded closes == entry qty) must NOT match, so a later fill for
// a brand-new same-symbol position can't be misattributed to the old one.
func TestUnderClosedMatcher_IgnoresFullyClosedPosition(t *testing.T) {
	st, err := New(filepath.Join(t.TempDir(), "fully-closed.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	positionStore := st.Position()
	base := time.Now().UTC().UnixMilli()
	pos := &TraderPosition{
		TraderID: "t", ExchangeID: "ex", ExchangeType: "okx",
		ExchangePositionID: "sync_SOLUSDT_SHORT_2", Symbol: "SOLUSDT", Side: "SHORT",
		Quantity: 1.0, EntryQuantity: 1.0, EntryPrice: 75.0, Status: "OPEN",
		Source: "sync", EntryTime: base,
	}
	if err := positionStore.CreateOpenPosition(pos); err != nil {
		t.Fatalf("create position: %v", err)
	}
	// Fully close 1.0 (recorded == entry).
	if err := positionStore.ReducePositionQuantity(pos.ID, 1.0, 74.5, 0.001, 0.5, "ladder_tp", "ladder_tp", "MARKET", "full", base+1000); err != nil {
		t.Fatalf("full close: %v", err)
	}
	if m, err := positionStore.GetRecentlyClosedUnderClosedPosition("t", "SOLUSDT", "SHORT", base+2000, 5*time.Second); err != nil {
		t.Fatalf("under-closed matcher: %v", err)
	} else if m != nil {
		t.Fatal("fully-closed position must NOT match under-closed matcher")
	}
}
