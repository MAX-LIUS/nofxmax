package store

import (
	"path/filepath"
	"testing"
	"time"
)

func newIntentStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "intent.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	return s
}

func TestCloseIntentOrderIDExactMatch(t *testing.T) {
	s := newIntentStore(t)
	ci := s.CloseIntent()
	if err := ci.Record("trader-1", "ex-1", "BTCUSDT", "LONG", "ai_close_long", 0.5, 42, "ORD-100"); err != nil {
		t.Fatalf("record: %v", err)
	}
	got, err := ci.MatchByOrderIDAndConsume("trader-1", "ORD-100")
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if got == nil || got.Reason != "ai_close_long" || got.DecisionCycle != 42 {
		t.Fatalf("unexpected intent: %+v", got)
	}
	// Order-id match is idempotent: a second fill of the SAME close order resolves
	// to the same reason (a market close can split into multiple fills). The intent
	// is marked consumed but still matchable by its order id.
	again, err := ci.MatchByOrderIDAndConsume("trader-1", "ORD-100")
	if err != nil {
		t.Fatalf("match2: %v", err)
	}
	if again == nil || again.Reason != "ai_close_long" {
		t.Fatalf("expected same reason on second fill, got %+v", again)
	}
	if !again.Consumed {
		t.Fatalf("intent should be marked consumed after first fill")
	}
}

func TestCloseIntentWindowFallback(t *testing.T) {
	s := newIntentStore(t)
	ci := s.CloseIntent()
	// No order id recorded; only symbol/side/time available.
	if err := ci.Record("trader-1", "ex-1", "ETHUSDT", "SHORT", "managed_drawdown", 1.0, 0, ""); err != nil {
		t.Fatalf("record: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	got, err := ci.MatchByWindowAndConsume("trader-1", "ETHUSDT", "SHORT", now, 5*60*1000)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if got == nil || got.Reason != "managed_drawdown" {
		t.Fatalf("unexpected intent: %+v", got)
	}
}

func TestCloseIntentWindowMissesOutsideWindow(t *testing.T) {
	s := newIntentStore(t)
	ci := s.CloseIntent()
	if err := ci.Record("trader-1", "ex-1", "ETHUSDT", "SHORT", "time_stop", 1.0, 0, ""); err != nil {
		t.Fatalf("record: %v", err)
	}
	// Fill 1 hour away with a 5-minute window must not match.
	far := time.Now().UTC().UnixMilli() + 60*60*1000
	got, err := ci.MatchByWindowAndConsume("trader-1", "ETHUSDT", "SHORT", far, 5*60*1000)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if got != nil {
		t.Fatalf("expected no match outside window, got %+v", got)
	}
}

func TestCloseIntentWindowSideIsolation(t *testing.T) {
	s := newIntentStore(t)
	ci := s.CloseIntent()
	if err := ci.Record("trader-1", "ex-1", "BTCUSDT", "LONG", "breadth_breaker", 1.0, 0, ""); err != nil {
		t.Fatalf("record: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	// A SHORT fill on the same symbol must not consume the LONG intent.
	got, err := ci.MatchByWindowAndConsume("trader-1", "BTCUSDT", "SHORT", now, 5*60*1000)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if got != nil {
		t.Fatalf("expected side isolation, got %+v", got)
	}
	// The LONG fill consumes it.
	got, err = ci.MatchByWindowAndConsume("trader-1", "BTCUSDT", "LONG", now, 5*60*1000)
	if err != nil {
		t.Fatalf("match long: %v", err)
	}
	if got == nil || got.Reason != "breadth_breaker" {
		t.Fatalf("unexpected intent: %+v", got)
	}
}

func TestCloseIntentMultiFillSameOrder(t *testing.T) {
	// One close order produces many fills (OKX splits a market close across price
	// levels). Every fill carries the same parent order id and must resolve to the
	// same reason — the intent must not be "used up" by the first fill.
	s := newIntentStore(t)
	ci := s.CloseIntent()
	if err := ci.Record("trader-1", "ex-1", "SUIUSDT", "SHORT", "giveback_guard_breadth", 70, 0, "ORD-XYZ"); err != nil {
		t.Fatalf("record: %v", err)
	}
	for i := 0; i < 4; i++ {
		got, err := ci.MatchByOrderIDAndConsume("trader-1", "ORD-XYZ")
		if err != nil {
			t.Fatalf("fill %d match: %v", i, err)
		}
		if got == nil || got.Reason != "giveback_guard_breadth" {
			t.Fatalf("fill %d: expected giveback_guard_breadth, got %+v", i, got)
		}
	}
	// A time-window fallback must NOT re-grab this already-consumed intent for an
	// unrelated fill.
	other, err := ci.MatchByWindowAndConsume("trader-1", "SUIUSDT", "SHORT", time.Now().UTC().UnixMilli(), 5*60*1000)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if other != nil {
		t.Fatalf("consumed intent must not be re-grabbed by window, got %+v", other)
	}
}

func TestCloseIntentPrune(t *testing.T) {
	s := newIntentStore(t)
	ci := s.CloseIntent()
	if err := ci.Record("trader-1", "ex-1", "BTCUSDT", "LONG", "ai_close_long", 0.5, 0, "ORD-1"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := ci.MatchByOrderIDAndConsume("trader-1", "ORD-1"); err != nil {
		t.Fatalf("consume: %v", err)
	}
	// Prune everything consumed before "now+1s".
	deleted, err := ci.PruneConsumed(time.Now().UTC().UnixMilli() + 1000)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 pruned, got %d", deleted)
	}
}

// A Binance native protection order (STOP/TP) recorded at placement time with its
// trigger price must be attributed to its mechanism by a later fill whose price ≈
// the trigger — the aged-order-survivable path that reaches OKX parity. This is
// the exact BN failure mode: no order-id intent, origType lookup would fail.
func TestCloseIntentTriggerPriceMatch(t *testing.T) {
	s := newIntentStore(t)
	ci := s.CloseIntent()
	// Two protection tiers placed at open: a TP at 472.6 and an SL at 446.2.
	if err := ci.RecordProtection("t", "ex", "ZECUSDT", "LONG", "ladder_tp", 0.3, 472.60, 10, "algo-1"); err != nil {
		t.Fatalf("record tp: %v", err)
	}
	if err := ci.RecordProtection("t", "ex", "ZECUSDT", "LONG", "full_sl", 1.0, 446.20, 10, "algo-2"); err != nil {
		t.Fatalf("record sl: %v", err)
	}
	// A fill at 472.55 (TP triggered) resolves to the TP tier, not the SL.
	got, err := ci.MatchByTriggerPriceAndConsume("t", "ZECUSDT", "LONG", 472.55, 0.15)
	if err != nil || got == nil {
		t.Fatalf("tp match: %v got=%+v", err, got)
	}
	if got.Reason != "ladder_tp" {
		t.Fatalf("expected ladder_tp, got %q", got.Reason)
	}
	// Consumed: a second fill at the same price must NOT re-grab the TP tier.
	again, _ := ci.MatchByTriggerPriceAndConsume("t", "ZECUSDT", "LONG", 472.55, 0.15)
	if again != nil {
		t.Fatalf("expected nil on consumed tier, got %+v", again)
	}
	// A fill far from any trigger (mid-price active close) resolves to nothing —
	// never fabricate a protection attribution for a mid-price close.
	none, _ := ci.MatchByTriggerPriceAndConsume("t", "ZECUSDT", "LONG", 460.00, 0.15)
	if none != nil {
		t.Fatalf("mid-price fill should not match any protection tier, got %+v", none)
	}
	// The SL tier remains matchable by a fill at its trigger.
	sl, _ := ci.MatchByTriggerPriceAndConsume("t", "ZECUSDT", "LONG", 446.18, 0.15)
	if sl == nil || sl.Reason != "full_sl" {
		t.Fatalf("expected full_sl, got %+v", sl)
	}
}

// Slippage tolerance: a Binance native stop slips ~3% past its trigger before
// the market fill lands. A wide tolerance (3.5%) on the ADVERSE side must still
// resolve the stop, so the close is attributed instead of dumped in sync_external.
func TestCloseIntentTriggerPriceSlippageAdverseSide(t *testing.T) {
	s := newIntentStore(t)
	ci := s.CloseIntent()
	// LONG ladder_sl trigger at 100; a real stop fills BELOW it (adverse slippage).
	if err := ci.RecordProtection("t", "ex", "SOLUSDT", "LONG", "ladder_sl", 1.0, 100.0, 1, ""); err != nil {
		t.Fatalf("record: %v", err)
	}
	// Fill at 97.0 (3% below trigger) — must match at 3.5% tolerance.
	got, err := ci.MatchByTriggerPriceAndConsume("t", "SOLUSDT", "LONG", 97.0, 3.5)
	if err != nil || got == nil || got.Reason != "ladder_sl" {
		t.Fatalf("adverse-side slippage fill should match ladder_sl, got=%+v err=%v", got, err)
	}
}

// Direction gate: a LONG stop fill that lands well ABOVE the stop trigger (the
// favourable side) is NOT that stop firing — it is a mid-price/AI close — and
// must NOT be mislabeled as the stop even inside the wide tolerance band. This
// is the mislabel case proven from real data (ETH SHORT filled below its SL).
func TestCloseIntentTriggerPriceDirectionGateRejectsWrongSide(t *testing.T) {
	s := newIntentStore(t)
	ci := s.CloseIntent()
	// LONG ladder_sl trigger at 100. A fill at 103 (3% ABOVE the stop) is on the
	// impossible side for a stop — reject despite being within 3.5% distance.
	if err := ci.RecordProtection("t", "ex", "SOLUSDT", "LONG", "ladder_sl", 1.0, 100.0, 1, ""); err != nil {
		t.Fatalf("record: %v", err)
	}
	if got, _ := ci.MatchByTriggerPriceAndConsume("t", "SOLUSDT", "LONG", 103.0, 3.5); got != nil {
		t.Fatalf("favourable-side fill must NOT be mislabeled as the stop, got=%+v", got)
	}
	// SHORT mirror: a SHORT stop trigger at 100 fills ABOVE it; a fill BELOW (97)
	// is the favourable side and must be rejected.
	if err := ci.RecordProtection("t", "ex", "ETHUSDT", "SHORT", "ladder_sl", 1.0, 100.0, 1, ""); err != nil {
		t.Fatalf("record short: %v", err)
	}
	if got, _ := ci.MatchByTriggerPriceAndConsume("t", "ETHUSDT", "SHORT", 97.0, 3.5); got != nil {
		t.Fatalf("SHORT favourable-side fill must NOT be mislabeled as the stop, got=%+v", got)
	}
}

// Direction gate on take-profit: a LONG TP fills at/above its trigger; a wide
// tolerance must still accept the adverse (slightly-below) side for rounding but
// the primary case is the favourable side being valid.
func TestCloseIntentTriggerPriceTakeProfitDirection(t *testing.T) {
	s := newIntentStore(t)
	ci := s.CloseIntent()
	if err := ci.RecordProtection("t", "ex", "BCHUSDT", "LONG", "ladder_tp", 0.5, 100.0, 1, ""); err != nil {
		t.Fatalf("record: %v", err)
	}
	// LONG TP fills at/above trigger — 102 (2% above) is valid.
	if got, _ := ci.MatchByTriggerPriceAndConsume("t", "BCHUSDT", "LONG", 102.0, 3.5); got == nil || got.Reason != "ladder_tp" {
		t.Fatalf("LONG TP fill above trigger should match, got=%+v", got)
	}
}

// Side isolation: a LONG protection intent must never be grabbed by a SHORT fill.
func TestCloseIntentTriggerPriceSideIsolation(t *testing.T) {
	s := newIntentStore(t)
	ci := s.CloseIntent()
	if err := ci.RecordProtection("t", "ex", "ETHUSDT", "LONG", "ladder_tp", 0.2, 1800.0, 1, ""); err != nil {
		t.Fatalf("record: %v", err)
	}
	if got, _ := ci.MatchByTriggerPriceAndConsume("t", "ETHUSDT", "SHORT", 1800.0, 0.15); got != nil {
		t.Fatalf("SHORT fill must not match LONG protection intent, got %+v", got)
	}
	if got, _ := ci.MatchByTriggerPriceAndConsume("t", "ETHUSDT", "LONG", 1800.0, 0.15); got == nil {
		t.Fatalf("LONG fill should match LONG protection intent")
	}
}
