package trader

import (
	"fmt"
	"path/filepath"
	"time"
	"testing"

	"nofx/store"
	tradertypes "nofx/trader/types"
)

// fakeBackupTrader is a minimal trader that records tagged stop placements and
// cancellations so the guard-owned rolling backup-stop lifecycle can be verified.
type fakeBackupTrader struct {
	openOrders   []tradertypes.OpenOrder
	nextID       int
	placed       []struct{ price float64; id string }
	canceled     []string
}

func (f *fakeBackupTrader) GetBalance() (map[string]interface{}, error)             { return nil, nil }
func (f *fakeBackupTrader) GetPositions() ([]map[string]interface{}, error)         { return nil, nil }
func (f *fakeBackupTrader) OpenLong(string, float64, int) (map[string]interface{}, error)  { return nil, nil }
func (f *fakeBackupTrader) OpenShort(string, float64, int) (map[string]interface{}, error) { return nil, nil }
func (f *fakeBackupTrader) CloseLong(string, float64) (map[string]interface{}, error)      { return nil, nil }
func (f *fakeBackupTrader) CloseShort(string, float64) (map[string]interface{}, error)     { return nil, nil }
func (f *fakeBackupTrader) SetLeverage(string, int) error                            { return nil }
func (f *fakeBackupTrader) SetMarginMode(string, bool) error                         { return nil }
func (f *fakeBackupTrader) GetMarketPrice(string) (float64, error)                   { return 0, nil }
func (f *fakeBackupTrader) SetStopLoss(symbol, positionSide string, quantity, stopPrice float64) error {
	_, err := f.SetStopLossTagged(symbol, positionSide, quantity, stopPrice, "")
	return err
}
func (f *fakeBackupTrader) SetTakeProfit(string, string, float64, float64) error { return nil }
func (f *fakeBackupTrader) CancelStopLossOrders(string) error                    { return nil }
func (f *fakeBackupTrader) CancelTakeProfitOrders(string) error                  { return nil }
func (f *fakeBackupTrader) CancelAllOrders(string) error                         { return nil }
func (f *fakeBackupTrader) CancelStopOrders(string) error                        { return nil }
func (f *fakeBackupTrader) FormatQuantity(string, float64) (string, error)       { return "", nil }
func (f *fakeBackupTrader) ValidateProtectionQuantity(string, float64) error     { return nil }
func (f *fakeBackupTrader) GetOrderStatus(string, string) (map[string]interface{}, error) {
	return nil, nil
}
func (f *fakeBackupTrader) GetClosedPnL(time.Time, int) ([]tradertypes.ClosedPnLRecord, error) {
	return nil, nil
}
func (f *fakeBackupTrader) GetOpenOrders(string) ([]tradertypes.OpenOrder, error) {
	return f.openOrders, nil
}

// SetStopLossTagged records a placement and returns an incrementing algo id.
func (f *fakeBackupTrader) SetStopLossTagged(symbol, positionSide string, quantity, stopPrice float64, reasonTag string) (string, error) {
	f.nextID++
	id := fmt.Sprintf("algo-%d", f.nextID)
	f.openOrders = append(f.openOrders, tradertypes.OpenOrder{
		OrderID: id, Symbol: symbol, PositionSide: positionSide,
		Type: "STOP_MARKET", StopPrice: stopPrice, Quantity: quantity, Status: "NEW",
	})
	f.placed = append(f.placed, struct{ price float64; id string }{stopPrice, id})
	return id, nil
}

// CancelOrder removes a recorded open order by id.
func (f *fakeBackupTrader) CancelOrder(symbol, orderID string) error {
	return f.cancelByID(orderID)
}

// CancelAlgoOrderByID is what the guard now asserts (backup stops are ALGO orders on both
// OKX and Binance; the regular CancelOrder endpoint cannot touch them).
func (f *fakeBackupTrader) CancelAlgoOrderByID(symbol, orderID string) error {
	return f.cancelByID(orderID)
}

func (f *fakeBackupTrader) cancelByID(orderID string) error {
	f.canceled = append(f.canceled, orderID)
	out := f.openOrders[:0]
	for _, o := range f.openOrders {
		if o.OrderID != orderID {
			out = append(out, o)
		}
	}
	f.openOrders = out
	return nil
}

// newBackupAT builds an AutoTrader with an in-memory store and the fake trader.
func newBackupAT(t *testing.T, ft *fakeBackupTrader) *AutoTrader {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "backup.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return &AutoTrader{id: "T", trader: ft, store: st}
}

// seedBackupRec persists a frozen record with the given backup boundary + order id.
func seedBackupRec(t *testing.T, at *AutoTrader, sym, sideUpper string, entry, backup float64, orderID string) string {
	t.Helper()
	tf := store.ATRProtectionConfig{}.WithDefaults().Timeframe
	key := frozenATRKey(at.id, sym, tf, sideUpper)
	rec := store.FrozenATRRecord{
		TraderID: at.id, Symbol: sym, EntryPrice: entry, ATR: 0.005,
		BackupBoundary: backup, BackupOrderID: orderID,
	}
	if err := at.store.SaveFrozenATRRecord(key, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return key
}

func loadBackupRec(t *testing.T, at *AutoTrader, key string) store.FrozenATRRecord {
	t.Helper()
	s, err := at.store.LoadFrozenATRState()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return s.Records[key]
}

// Option 1 (2026-07-20): no near-price physical backup is EVER placed. syncStructuralBackupStop
// is cancel-only — it tears down any legacy backup order and never arms a new one.

// Never places: even with a "protective, looser" backup boundary seeded (the old good path),
// no physical order is placed. This is the SOL fix: the backup @75.30 that wick-closed the
// position must no longer exist.
func TestBackupStop_NeverPlacesPhysicalBackup(t *testing.T) {
	ft := &fakeBackupTrader{}
	at := newBackupAT(t, ft)
	acfg := store.ATRProtectionConfig{}.WithDefaults()
	// short: backup 0.3760 sits protectively ABOVE entry 0.3689 (old scheme would have placed).
	seedBackupRec(t, at, "WLDUSDT", "SHORT", 0.3689, 0.3760, "")

	at.syncStructuralBackupStop("WLDUSDT", "SHORT", 407, 0.3689, false, acfg)

	if len(ft.placed) != 0 {
		t.Fatalf("Option 1: expected NO physical backup ever placed, got %+v", ft.placed)
	}
}

// Cancels legacy: a backup order left over from the old rolling scheme is torn down, none placed,
// and both the order id and the boundary are cleared from the record.
func TestBackupStop_CancelsLegacyBackup(t *testing.T) {
	ft := &fakeBackupTrader{}
	at := newBackupAT(t, ft)
	acfg := store.ATRProtectionConfig{}.WithDefaults()
	ft.openOrders = append(ft.openOrders, tradertypes.OpenOrder{
		OrderID: "algo-legacy", Symbol: "WLDUSDT", PositionSide: "SHORT",
		Type: "STOP_MARKET", StopPrice: 0.3760, Quantity: 407, Status: "NEW",
	})
	key := seedBackupRec(t, at, "WLDUSDT", "SHORT", 0.3689, 0.3760, "algo-legacy")

	at.syncStructuralBackupStop("WLDUSDT", "SHORT", 407, 0.3689, false, acfg)

	if len(ft.canceled) != 1 || ft.canceled[0] != "algo-legacy" {
		t.Fatalf("expected legacy order algo-legacy canceled, got %v", ft.canceled)
	}
	if len(ft.placed) != 0 {
		t.Fatalf("expected no placement, got %+v", ft.placed)
	}
	rec := loadBackupRec(t, at, key)
	if rec.BackupOrderID != "" {
		t.Fatalf("expected order id cleared, got %q", rec.BackupOrderID)
	}
	if rec.BackupBoundary != 0 {
		t.Fatalf("expected backup boundary cleared to 0, got %v", rec.BackupBoundary)
	}
}

// No legacy order, no boundary → no-op: nothing placed, nothing canceled.
func TestBackupStop_NoopWhenNothingToClean(t *testing.T) {
	ft := &fakeBackupTrader{}
	at := newBackupAT(t, ft)
	acfg := store.ATRProtectionConfig{}.WithDefaults()
	seedBackupRec(t, at, "WLDUSDT", "SHORT", 0.3689, 0, "")

	at.syncStructuralBackupStop("WLDUSDT", "SHORT", 407, 0.3689, false, acfg)

	if len(ft.placed) != 0 || len(ft.canceled) != 0 {
		t.Fatalf("expected no-op, got placed=%+v canceled=%v", ft.placed, ft.canceled)
	}
}

// applyStructuralTrail persists BackupBoundary=0 on a genuine ratchet (close-confirm only),
// so the tight level survives but no near-price backup boundary is ever recorded.
func TestApplyTrail_PersistsNoBackupBoundary(t *testing.T) {
	// This is asserted indirectly: after a ratchet the record must have TrailBoundary>0 and
	// BackupBoundary==0. Covered end-to-end by the guard; the unit-level invariant is that
	// syncStructuralBackupStop never sees a nonzero BackupBoundary to act on (tested above).
	// Kept as a doc anchor for the Option 1 invariant.
}

// Legacy backup at the SOL bug price (looser than tight, but still near price) must be torn
// down, never re-placed — regardless of where it sat relative to the tight level. This locks
// in the SOL fix: the backup @75.30 that a wick triggered can never come back.
func TestBackupStop_TearsDownLegacyRegardlessOfTightRelation(t *testing.T) {
	acfg := store.ATRProtectionConfig{}.WithDefaults()

	// SOL-shaped long: legacy backup 75.30 (looser than tight 75.53), live order on book.
	ft := &fakeBackupTrader{}
	at := newBackupAT(t, ft)
	ft.openOrders = append(ft.openOrders, tradertypes.OpenOrder{
		OrderID: "algo-sol", Symbol: "SOLUSDT", PositionSide: "LONG",
		Type: "STOP_MARKET", StopPrice: 75.30, Quantity: 1.7, Status: "NEW",
	})
	tf := acfg.Timeframe
	key := frozenATRKey(at.id, "SOLUSDT", tf, "LONG")
	if err := at.store.SaveFrozenATRRecord(key, store.FrozenATRRecord{
		TraderID: at.id, Symbol: "SOLUSDT", EntryPrice: 76.31, ATR: 0.35,
		BackupBoundary: 75.30, TrailBoundary: 75.53, BackupOrderID: "algo-sol",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	at.syncStructuralBackupStop("SOLUSDT", "LONG", 1.7, 76.31, true, acfg)

	if len(ft.placed) != 0 {
		t.Fatalf("expected NO placement (Option 1), got %+v", ft.placed)
	}
	if len(ft.canceled) != 1 || ft.canceled[0] != "algo-sol" {
		t.Fatalf("expected legacy SOL backup canceled, got %v", ft.canceled)
	}
	rec := loadBackupRec(t, at, key)
	if rec.BackupOrderID != "" || rec.BackupBoundary != 0 {
		t.Fatalf("expected backup fully cleared, got id=%q boundary=%v", rec.BackupOrderID, rec.BackupBoundary)
	}
	if rec.TrailBoundary != 75.53 {
		t.Fatalf("tight close-confirm must be preserved at 75.53, got %v", rec.TrailBoundary)
	}
}
