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
