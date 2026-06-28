package store

import (
	"path/filepath"
	"testing"
)

func TestFrozenATRStateRoundTripAndEvict(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "frozen-atr.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	key := "trader-1|ETHUSDT"
	rec := FrozenATRRecord{TraderID: "trader-1", Symbol: "ETHUSDT", EntryPrice: 1581.69, ATR: 10.3, UpdatedAt: 1}
	if err := s.SaveFrozenATRRecord(key, rec); err != nil {
		t.Fatalf("save frozen atr: %v", err)
	}

	state, err := s.LoadFrozenATRState()
	if err != nil {
		t.Fatalf("load frozen atr: %v", err)
	}
	got, ok := state.Records[key]
	if !ok {
		t.Fatalf("expected record at %q, got %+v", key, state.Records)
	}
	// The open-time ATR (10.3) must survive verbatim — this is what prevents the
	// post-restart drift to a later ATR (e.g. 15.8) that shifted activation prices.
	if got.ATR != 10.3 || got.EntryPrice != 1581.69 {
		t.Fatalf("frozen atr changed across round-trip: %+v", got)
	}

	if err := s.DeleteFrozenATRRecord(key); err != nil {
		t.Fatalf("delete frozen atr: %v", err)
	}
	state, err = s.LoadFrozenATRState()
	if err != nil {
		t.Fatalf("reload frozen atr: %v", err)
	}
	if _, ok := state.Records[key]; ok {
		t.Fatalf("expected record evicted after delete, still present: %+v", state.Records)
	}
}
