package trader

import (
	"nofx/store"
	"testing"
)

func newGuardTrader(fake *fakeProtectionTrader, cfg store.GivebackGuardConfig, peaks map[string]float64) *AutoTrader {
	if peaks == nil {
		peaks = map[string]float64{}
	}
	return &AutoTrader{
		exchange: "paper",
		trader:   fake,
		config: AutoTraderConfig{
			StrategyConfig: &store.StrategyConfig{
				Protection: store.ProtectionConfig{GivebackGuard: cfg},
			},
		},
		protectionState: make(map[string]string),
		drawdownState:   make(map[string]string),
		peakPnLCache:    peaks,
		gbL1FiredAtPeak: make(map[string]float64),
	}
}

func longPos(symbol string, entry, mark, qty float64) map[string]interface{} {
	return map[string]interface{}{
		"symbol": symbol, "side": "long",
		"entryPrice": entry, "markPrice": mark, "positionAmt": qty,
	}
}

func lastTag(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	return tags[len(tags)-1]
}

// __TESTS__

func TestGivebackGuardL1FiresOnceThenRatchets(t *testing.T) {
	fake := &fakeProtectionTrader{positions: []map[string]interface{}{longPos("ADAUSDT", 100, 103, 200)}}
	cfg := store.GivebackGuardConfig{
		Enabled: true, DryRun: false,
		L1Enabled: true, L1GivebackPct: 40, L1MinPeakPct: 3, L1ClosePct: 50,
	}
	at := newGuardTrader(fake, cfg, map[string]float64{"ADAUSDT_long": 10})

	at.runGivebackGuard()
	if fake.closeLongCalls != 1 {
		t.Fatalf("L1 expected 1 close, got %d", fake.closeLongCalls)
	}
	if len(fake.closeLongQtys) != 1 || fake.closeLongQtys[0] != 100 {
		t.Fatalf("L1 expected close qty 100 (50%% of 200), got %+v", fake.closeLongQtys)
	}
	if tag := lastTag(fake.taggedCloseLongs); tag != "giveback_guard_l1" {
		t.Fatalf("L1 expected tag giveback_guard_l1, got %q", tag)
	}

	at.runGivebackGuard()
	if fake.closeLongCalls != 1 {
		t.Fatalf("L1 ratchet should suppress repeat, got %d closes", fake.closeLongCalls)
	}
}

func TestGivebackGuardL1ReArmsOnNewHigh(t *testing.T) {
	fake := &fakeProtectionTrader{positions: []map[string]interface{}{longPos("ADAUSDT", 100, 103, 200)}}
	cfg := store.GivebackGuardConfig{
		Enabled: true, L1Enabled: true, L1GivebackPct: 40, L1MinPeakPct: 3, L1ClosePct: 50,
	}
	at := newGuardTrader(fake, cfg, map[string]float64{"ADAUSDT_long": 10})

	at.runGivebackGuard()
	if fake.closeLongCalls != 1 {
		t.Fatalf("expected first fire, got %d", fake.closeLongCalls)
	}
	at.peakPnLCache["ADAUSDT_long"] = 20
	at.runGivebackGuard()
	if fake.closeLongCalls != 2 {
		t.Fatalf("expected re-arm fire after new high, got %d", fake.closeLongCalls)
	}
}

// __TESTS2__

func TestGivebackGuardDryRunNoOrders(t *testing.T) {
	fake := &fakeProtectionTrader{positions: []map[string]interface{}{longPos("ADAUSDT", 100, 103, 200)}}
	cfg := store.GivebackGuardConfig{
		Enabled: true, DryRun: true,
		L1Enabled: true, L1GivebackPct: 40, L1MinPeakPct: 3, L1ClosePct: 50,
	}
	at := newGuardTrader(fake, cfg, map[string]float64{"ADAUSDT_long": 10})

	at.runGivebackGuard()
	if fake.closeLongCalls != 0 {
		t.Fatalf("DryRun must not close, got %d", fake.closeLongCalls)
	}
}

func TestGivebackGuardDisabledNoOp(t *testing.T) {
	fake := &fakeProtectionTrader{positions: []map[string]interface{}{longPos("ADAUSDT", 100, 103, 200)}}
	cfg := store.GivebackGuardConfig{Enabled: false, L1Enabled: true, L1GivebackPct: 40, L1MinPeakPct: 3, L1ClosePct: 50}
	at := newGuardTrader(fake, cfg, map[string]float64{"ADAUSDT_long": 10})

	at.runGivebackGuard()
	if fake.closeLongCalls != 0 {
		t.Fatalf("disabled guard must be no-op, got %d", fake.closeLongCalls)
	}
}

func TestGivebackGuardL2PortfolioCircuitBreaker(t *testing.T) {
	fake := &fakeProtectionTrader{positions: []map[string]interface{}{
		longPos("ADAUSDT", 100, 110, 10),
		longPos("SOLUSDT", 100, 110, 10),
	}}
	cfg := store.GivebackGuardConfig{
		Enabled: true, L1Enabled: false,
		L2Enabled: true, L2GivebackPct: 50, L2MinPeakEquityPct: 0, L2ClosePct: 50,
	}
	at := newGuardTrader(fake, cfg, nil)

	at.runGivebackGuard()
	if fake.closeLongCalls != 0 {
		t.Fatalf("L2 tick1 should not fire, got %d", fake.closeLongCalls)
	}

	fake.positions = []map[string]interface{}{
		longPos("ADAUSDT", 100, 104, 10),
		longPos("SOLUSDT", 100, 104, 10),
	}
	at.runGivebackGuard()
	if fake.closeLongCalls != 2 {
		t.Fatalf("L2 tick2 should trim both winners, got %d", fake.closeLongCalls)
	}
	if tag := lastTag(fake.taggedCloseLongs); tag != "giveback_guard_l2" {
		t.Fatalf("L2 expected tag giveback_guard_l2, got %q", tag)
	}

	at.runGivebackGuard()
	if fake.closeLongCalls != 2 {
		t.Fatalf("L2 ratchet should suppress repeat, got %d", fake.closeLongCalls)
	}
}
