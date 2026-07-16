package store

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newFMStore(t *testing.T) (*ShadowGateStore, *gorm.DB) {
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

// A forward (live) verdict and a backfill verdict for the same rule must be
// separable by source, and the footprint/matrix must classify each into the right
// quadrant using the linked position's real PnL.
func TestFootprintAndMatrix_SourceSeparation(t *testing.T) {
	s, db := newFMStore(t)

	// Forward: created ≈ observed (same cycle). Kept a LOSER (keep_loss).
	pFwd := &TraderPosition{TraderID: "t", Symbol: "BTCUSDT", Side: "LONG", Status: "CLOSED", RealizedPnL: -4, ExitTime: 2000}
	db.Create(pFwd)
	db.Create(&ShadowGateVerdict{TraderID: "t", Symbol: "BTCUSDT", Side: "LONG", RuleName: "countertrend_slope30",
		WouldBlock: false, PositionID: pFwd.ID, ObservedAt: 1000, CreatedAt: 1000})

	// Backfill: created far after observed (>1h). Blocked a LOSER (block_loss).
	pBf := &TraderPosition{TraderID: "t", Symbol: "ETHUSDT", Side: "LONG", Status: "CLOSED", RealizedPnL: -6, ExitTime: 5000}
	db.Create(pBf)
	db.Create(&ShadowGateVerdict{TraderID: "t", Symbol: "ETHUSDT", Side: "LONG", RuleName: "countertrend_slope30",
		WouldBlock: true, PositionID: pBf.ID, ObservedAt: 1000, CreatedAt: 1000 + 2*3600*1000})

	// Footprint filtered to forward: only the BTC keep_loss point.
	fwd, err := s.RuleFootprint("t", "countertrend_slope30", "forward")
	if err != nil {
		t.Fatalf("footprint forward: %v", err)
	}
	if len(fwd) != 1 || fwd[0].Symbol != "BTCUSDT" || fwd[0].Quadrant != "keep_loss" {
		t.Fatalf("forward footprint wrong: %+v", fwd)
	}

	// Footprint filtered to backfill: only the ETH block_loss point.
	bf, err := s.RuleFootprint("t", "countertrend_slope30", "backfill")
	if err != nil {
		t.Fatalf("footprint backfill: %v", err)
	}
	if len(bf) != 1 || bf[0].Symbol != "ETHUSDT" || bf[0].Quadrant != "block_loss" {
		t.Fatalf("backfill footprint wrong: %+v", bf)
	}

	// Matrix over backfill: the rule has one block_loss (correct block).
	m, err := s.GateMatrix("t", "backfill")
	if err != nil {
		t.Fatalf("matrix: %v", err)
	}
	if len(m) != 1 {
		t.Fatalf("expected 1 gate row, got %d: %+v", len(m), m)
	}
	if m[0].BlockLossN != 1 || m[0].BlockLossPnL != -6 || m[0].KeepLossN != 0 {
		t.Fatalf("matrix backfill wrong: %+v", m[0])
	}
}

// The canonical UI order must contain every registered rule name exactly once
// (guards against a new rule silently missing from the fixed order).
func TestCanonicalOrderNoDuplicates(t *testing.T) {
	seen := map[string]bool{}
	for _, n := range CanonicalShadowRuleOrder {
		if seen[n] {
			t.Fatalf("duplicate in canonical order: %s", n)
		}
		seen[n] = true
	}
	if len(CanonicalShadowRuleOrder) < 11 {
		t.Fatalf("expected >=11 canonical rules, got %d", len(CanonicalShadowRuleOrder))
	}
}
