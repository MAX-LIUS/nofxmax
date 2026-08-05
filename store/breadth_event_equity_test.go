package store

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestBreadthEventEquityColumns verifies the account-value breaker's columns are
// created by InitTables (AutoMigrate) and round-trip through Record/ListByTrader.
// Without this, an equity fire would be persisted with silently-dropped context
// and the event log could not explain why it fired.
func TestBreadthEventEquityColumns(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s := NewBreadthEventStore(db)
	if err := s.InitTables(); err != nil {
		t.Fatalf("init: %v", err)
	}
	// Re-init must be a safe no-op (AutoMigrate is run on every boot).
	if err := s.InitTables(); err != nil {
		t.Fatalf("re-init: %v", err)
	}

	// Values taken from the measured 2026-08-05 claude drawdown.
	want := &BreadthEvent{
		TraderID: "t1", Outcome: "fired", FirePath: "equity",
		Total: 4, Retracing: 2, RetracingFrac: 0.5, ThresholdFrac: 0.55, MinPos: 3,
		CutCount: 2, UseATR: true,
		EquityPeak: 162.399, EquityCur: 153.418, EquityDDPct: 5.5305, EquityDDAbs: 8.981,
		EquityThrPct: 5, EquityThrAbs: 0, EquityScope: "retracing", EquityFetchOK: true,
		ObservedAt: 1, CreatedAt: 1,
	}
	if err := s.Record(want); err != nil {
		t.Fatalf("record: %v", err)
	}
	rows, err := s.ListByTrader("t1", 1)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	got := rows[0]
	if got.FirePath != "equity" {
		t.Fatalf("fire_path: got %q want %q", got.FirePath, "equity")
	}
	if got.EquityPeak != want.EquityPeak || got.EquityCur != want.EquityCur {
		t.Fatalf("equity peak/cur: got %.4f/%.4f want %.4f/%.4f",
			got.EquityPeak, got.EquityCur, want.EquityPeak, want.EquityCur)
	}
	if got.EquityDDPct != want.EquityDDPct || got.EquityDDAbs != want.EquityDDAbs {
		t.Fatalf("equity dd: got %.4f%%/%.4f want %.4f%%/%.4f",
			got.EquityDDPct, got.EquityDDAbs, want.EquityDDPct, want.EquityDDAbs)
	}
	if got.EquityThrPct != want.EquityThrPct || got.EquityScope != "retracing" || !got.EquityFetchOK {
		t.Fatalf("equity thresholds/scope/fetch_ok regressed: %+v", got)
	}
	// Pre-existing columns must be unaffected by the schema addition.
	if got.Total != 4 || got.Retracing != 2 || got.RetracingFrac != 0.5 || got.CutCount != 2 || !got.UseATR {
		t.Fatalf("pre-existing fields regressed: %+v", got)
	}
}
