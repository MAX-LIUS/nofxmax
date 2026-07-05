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
		gbPnlHist:       make(map[string][]float64),
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

// breadthCfg builds a pnl%-mode breadth config (ATR disabled so tests need no
// network) that fires when >= frac of >= minPos positions give back >= 5%.
func breadthCfg(minPos int, frac float64) store.GivebackGuardConfig {
	return store.GivebackGuardConfig{
		Enabled: true, DryRun: false,
		BreadthEnabled:     true,
		BreadthMinPos:      minPos,
		BreadthFrac:        frac,
		BreadthLoserCutPct: 100,
		BreadthUseATR:      false,
		BreadthGivebackPct: 5,
		BreadthVelEps:      0,
	}
}

// __TESTS__

// TestGivebackGuardBreadthCutsLosersOnly: a majority retrace together; the
// LOSING retracing positions are cut, and a retracing WINNER that ALREADY HAS
// armed protection is spared (it rides its own stop).
func TestGivebackGuardBreadthCutsLosersOnly(t *testing.T) {
	fake := &fakeProtectionTrader{positions: []map[string]interface{}{
		longPos("ADAUSDT", 100, 97, 200),  // -3%, peak 5 => giveback 8 (loser, retracing)
		longPos("SOLUSDT", 100, 96, 200),  // -4%, peak 4 => giveback 8 (loser, retracing)
		longPos("DOTUSDT", 100, 98, 200),  // -2%, peak 6 => giveback 8 (loser, retracing)
		longPos("BTCUSDT", 100, 104, 200), // +4%, peak 12 => giveback 8 (WINNER, retracing but PROTECTED)
	}}
	peaks := map[string]float64{
		"ADAUSDT_long": 5, "SOLUSDT_long": 4, "DOTUSDT_long": 6, "BTCUSDT_long": 12,
	}
	at := newGuardTrader(fake, breadthCfg(3, 0.7), peaks)
	// The winner has armed downside protection => spared (rides its own stop).
	at.protectionState["BTCUSDT_long"] = "native_trailing_armed"

	at.runGivebackGuard()
	if fake.closeLongCalls != 3 {
		t.Fatalf("breadth should cut 3 losers only (PROTECTED retracing winner spared), got %d", fake.closeLongCalls)
	}
	if tag := lastTag(fake.taggedCloseLongs); tag != "giveback_guard_breadth" {
		t.Fatalf("expected tag giveback_guard_breadth, got %q", tag)
	}
	for _, q := range fake.closeLongQtys {
		if q != 200 {
			t.Fatalf("expected full (200) cut per loser, got %+v", fake.closeLongQtys)
		}
	}
}

// TestGivebackGuardBreadthCutsNakedWinner: a retracing WINNER with NO armed
// protection (naked) is cut alongside losers — in a correlated reversal an
// unprotected gain would otherwise be fully given back.
func TestGivebackGuardBreadthCutsNakedWinner(t *testing.T) {
	fake := &fakeProtectionTrader{positions: []map[string]interface{}{
		longPos("ADAUSDT", 100, 97, 200),  // -3%, peak 5 (loser, retracing)
		longPos("SOLUSDT", 100, 96, 200),  // -4%, peak 4 (loser, retracing)
		longPos("DOTUSDT", 100, 98, 200),  // -2%, peak 6 (loser, retracing)
		longPos("BTCUSDT", 100, 104, 200), // +4%, peak 12 (WINNER, retracing, NAKED — no protection)
	}}
	peaks := map[string]float64{
		"ADAUSDT_long": 5, "SOLUSDT_long": 4, "DOTUSDT_long": 6, "BTCUSDT_long": 12,
	}
	at := newGuardTrader(fake, breadthCfg(3, 0.7), peaks)
	// No protectionState set for BTCUSDT => naked winner => cut like a loser.

	at.runGivebackGuard()
	if fake.closeLongCalls != 4 {
		t.Fatalf("breadth should cut all 4 (3 losers + 1 NAKED retracing winner), got %d", fake.closeLongCalls)
	}
}

// TestGivebackGuardBreadthCutWinners: with BreadthCutWinners enabled the gate
// becomes a full deleverage breaker — retracing WINNERS are cut too, so all 4
// retracing positions (3 losers + 1 winner) get closed.
func TestGivebackGuardBreadthCutWinners(t *testing.T) {
	fake := &fakeProtectionTrader{positions: []map[string]interface{}{
		longPos("ADAUSDT", 100, 97, 200),  // -3%, peak 5 => giveback 8 (loser, retracing)
		longPos("SOLUSDT", 100, 96, 200),  // -4%, peak 4 => giveback 8 (loser, retracing)
		longPos("DOTUSDT", 100, 98, 200),  // -2%, peak 6 => giveback 8 (loser, retracing)
		longPos("BTCUSDT", 100, 104, 200), // +4%, peak 12 => giveback 8 (WINNER, retracing)
	}}
	peaks := map[string]float64{
		"ADAUSDT_long": 5, "SOLUSDT_long": 4, "DOTUSDT_long": 6, "BTCUSDT_long": 12,
	}
	cfg := breadthCfg(3, 0.7)
	cfg.BreadthCutWinners = true
	at := newGuardTrader(fake, cfg, peaks)

	at.runGivebackGuard()
	if fake.closeLongCalls != 4 {
		t.Fatalf("cut-winners mode should cut all 4 retracing positions, got %d", fake.closeLongCalls)
	}
}

// TestGivebackGuardBreadthNoQuorum: too few positions => the "majority" gate is
// meaningless and must not fire even when every position is retracing.
func TestGivebackGuardBreadthNoQuorum(t *testing.T) {
	fake := &fakeProtectionTrader{positions: []map[string]interface{}{
		longPos("ADAUSDT", 100, 97, 200),
		longPos("SOLUSDT", 100, 96, 200),
	}}
	peaks := map[string]float64{"ADAUSDT_long": 5, "SOLUSDT_long": 4}
	at := newGuardTrader(fake, breadthCfg(5, 0.7), peaks) // minPos 5 > 2 positions

	at.runGivebackGuard()
	if fake.closeLongCalls != 0 {
		t.Fatalf("no quorum must not fire, got %d", fake.closeLongCalls)
	}
}

// __TESTS2__

// TestGivebackGuardBreadthNotMajority: a single symbol retracing is not a
// correlated reversal; the gate must leave every position to its own SL/BE.
func TestGivebackGuardBreadthNotMajority(t *testing.T) {
	fake := &fakeProtectionTrader{positions: []map[string]interface{}{
		longPos("ADAUSDT", 100, 97, 200),  // -3%, peak 5 => retracing loser
		longPos("SOLUSDT", 100, 102, 200), // +2%, peak 2 => not retracing
		longPos("DOTUSDT", 100, 103, 200), // +3%, peak 3 => not retracing
		longPos("BTCUSDT", 100, 104, 200), // +4%, peak 4 => not retracing
	}}
	peaks := map[string]float64{
		"ADAUSDT_long": 5, "SOLUSDT_long": 2, "DOTUSDT_long": 3, "BTCUSDT_long": 4,
	}
	at := newGuardTrader(fake, breadthCfg(3, 0.7), peaks) // 1/4 = 0.25 < 0.7

	at.runGivebackGuard()
	if fake.closeLongCalls != 0 {
		t.Fatalf("single-symbol retrace must not fire, got %d", fake.closeLongCalls)
	}
}

func TestGivebackGuardDryRunNoOrders(t *testing.T) {
	fake := &fakeProtectionTrader{positions: []map[string]interface{}{
		longPos("ADAUSDT", 100, 97, 200),
		longPos("SOLUSDT", 100, 96, 200),
		longPos("DOTUSDT", 100, 98, 200),
	}}
	peaks := map[string]float64{"ADAUSDT_long": 5, "SOLUSDT_long": 4, "DOTUSDT_long": 6}
	cfg := breadthCfg(3, 0.7)
	cfg.DryRun = true
	at := newGuardTrader(fake, cfg, peaks)

	at.runGivebackGuard()
	if fake.closeLongCalls != 0 {
		t.Fatalf("DryRun must not close, got %d", fake.closeLongCalls)
	}
}

func TestGivebackGuardDisabledNoOp(t *testing.T) {
	fake := &fakeProtectionTrader{positions: []map[string]interface{}{
		longPos("ADAUSDT", 100, 97, 200),
		longPos("SOLUSDT", 100, 96, 200),
		longPos("DOTUSDT", 100, 98, 200),
	}}
	peaks := map[string]float64{"ADAUSDT_long": 5, "SOLUSDT_long": 4, "DOTUSDT_long": 6}
	cfg := breadthCfg(3, 0.7)
	cfg.Enabled = false
	at := newGuardTrader(fake, cfg, peaks)

	at.runGivebackGuard()
	if fake.closeLongCalls != 0 {
		t.Fatalf("disabled guard must be no-op, got %d", fake.closeLongCalls)
	}
}
