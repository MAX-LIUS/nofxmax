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

// equityCfg builds an account-value-breaker config. The quorum path is left
// UNCONFIGURED (frac/minPos zero) unless a test opts in, so these tests prove the
// equity path really is parallel and does not need the quorum gate to work.
func equityCfg(ddPct, ddAbs float64, scope string) store.GivebackGuardConfig {
	return store.GivebackGuardConfig{
		Enabled: true, DryRun: false,
		BreadthUseATR:        false,
		BreadthGivebackPct:   5,
		BreadthVelEps:        0,
		BreadthEquityEnabled: true,
		BreadthEquityDDPct:   ddPct,
		BreadthEquityDDAbs:   ddAbs,
		BreadthEquityScope:   scope,
		BreadthEquityCutPct:  100,
	}
}

// __TESTS__

// TestGivebackGuardEquityFiresBelowQuorum is the regression for the measured
// 2026-08-05 claude case: a minority of symbols individually retrace (so the
// breadth quorum is a genuine near-miss) while account equity has fallen 5.5%
// from its peak. The quorum path must stay silent and the account-value path
// must fire.
func TestGivebackGuardEquityFiresBelowQuorum(t *testing.T) {
	// 2 of 4 legs give back >= 5% (retracing), 2 barely move => frac 0.50 < 0.55.
	fake := &fakeProtectionTrader{
		positions: []map[string]interface{}{
			longPos("SOXLUSDT", 100, 97, 10), // -3%, peak 3.7 => giveback 6.7 (retracing loser)
			longPos("SKHYUSDT", 100, 97, 10), // -3%, peak 1.8 => giveback 4.8 -> below 5, see peaks
			longPos("ETHUSDT", 100, 100, 10), // flat, peak 0.7 => giveback 0.7 (not retracing)
			longPos("SOLUSDT", 100, 100, 10), // flat, peak 0.1 => giveback 0.1 (not retracing)
		},
		equity: 153.42, // peak 162.40 => -5.53%
	}
	peaks := map[string]float64{
		"SOXLUSDT_long": 3.7, "SKHYUSDT_long": 2.5, "ETHUSDT_long": 0.7, "SOLUSDT_long": 0.1,
	}
	cfg := equityCfg(5, 0, "retracing")
	// Quorum path configured exactly as live claude: it must NOT fire (2/4 = 0.50).
	cfg.BreadthEnabled = true
	cfg.BreadthMinPos = 3
	cfg.BreadthFrac = 0.55
	cfg.BreadthLoserCutPct = 100
	at := newGuardTrader(fake, cfg, peaks)
	at.gbEquityPeak = 162.40

	at.runGivebackGuard()
	// Scope "retracing" => only the 2 retracing losers are cut.
	if fake.closeLongCalls != 2 {
		t.Fatalf("equity breaker should cut the 2 retracing losers, got %d", fake.closeLongCalls)
	}
	if tag := lastTag(fake.taggedCloseLongs); tag != "giveback_guard_equity" {
		t.Fatalf("expected close reason giveback_guard_equity, got %q", tag)
	}
	// Peak must re-base to post-cut equity so it cannot fire again on the same drop.
	if at.gbEquityPeak != 153.42 {
		t.Fatalf("equity peak should re-base to %.2f after fire, got %.2f", 153.42, at.gbEquityPeak)
	}
}

// TestGivebackGuardEquityScopeAll: scope "all" closes every position, including
// legs that are NOT retracing and a winner WITH armed protection.
func TestGivebackGuardEquityScopeAll(t *testing.T) {
	fake := &fakeProtectionTrader{
		positions: []map[string]interface{}{
			longPos("SOXLUSDT", 100, 97, 10),  // retracing loser
			longPos("ETHUSDT", 100, 100, 10),  // flat, not retracing
			longPos("BTCUSDT", 100, 104, 10),  // winner, protected below
		},
		equity: 90,
	}
	peaks := map[string]float64{"SOXLUSDT_long": 3.7, "ETHUSDT_long": 0.7, "BTCUSDT_long": 4.2}
	at := newGuardTrader(fake, equityCfg(5, 0, "all"), peaks)
	at.gbEquityPeak = 100
	at.protectionState["BTCUSDT_long"] = "native_trailing_armed"

	at.runGivebackGuard()
	if fake.closeLongCalls != 3 {
		t.Fatalf("scope=all should close every position (3), got %d", fake.closeLongCalls)
	}
}

// TestGivebackGuardEquityAbsThreshold: the absolute USDT threshold fires on its
// own, and a percent threshold left at 0 must be treated as OFF (not "any drop").
func TestGivebackGuardEquityAbsThreshold(t *testing.T) {
	mk := func(equity float64) *fakeProtectionTrader {
		return &fakeProtectionTrader{
			positions: []map[string]interface{}{
				longPos("SOXLUSDT", 100, 97, 10),
				longPos("SKHYUSDT", 100, 97, 10),
			},
			equity: equity,
		}
	}
	peaks := map[string]float64{"SOXLUSDT_long": 3.7, "SKHYUSDT_long": 3.7}

	// -8 USDT from a 200 peak = -4%, under any sane pct threshold, but >= 8 abs.
	fake := mk(192)
	at := newGuardTrader(fake, equityCfg(0, 8, "retracing"), peaks)
	at.gbEquityPeak = 200
	at.runGivebackGuard()
	if fake.closeLongCalls != 2 {
		t.Fatalf("abs threshold should fire and cut 2, got %d", fake.closeLongCalls)
	}

	// -7.9 USDT: just under the abs threshold, and pct is 0 (=off) => no fire.
	fake2 := mk(192.1)
	at2 := newGuardTrader(fake2, equityCfg(0, 8, "retracing"), peaks)
	at2.gbEquityPeak = 200
	at2.runGivebackGuard()
	if fake2.closeLongCalls != 0 {
		t.Fatalf("below abs threshold with pct=0 (off) must not fire, got %d", fake2.closeLongCalls)
	}
}

// TestGivebackGuardEquityRatchetAndFailSafe: the peak ratchets UP with equity (so
// a new high raises the trigger line), and an unreadable equity skips the path
// instead of firing on a zero reading.
func TestGivebackGuardEquityRatchetAndFailSafe(t *testing.T) {
	peaks := map[string]float64{"SOXLUSDT_long": 3.7, "SKHYUSDT_long": 3.7}

	// Ratchet: equity 220 > stored peak 200 => peak becomes 220, no fire.
	fake := &fakeProtectionTrader{
		positions: []map[string]interface{}{
			longPos("SOXLUSDT", 100, 97, 10),
			longPos("SKHYUSDT", 100, 97, 10),
		},
		equity: 220,
	}
	at := newGuardTrader(fake, equityCfg(5, 0, "retracing"), peaks)
	at.gbEquityPeak = 200
	at.runGivebackGuard()
	if fake.closeLongCalls != 0 {
		t.Fatalf("new equity high must not fire, got %d", fake.closeLongCalls)
	}
	if at.gbEquityPeak != 220 {
		t.Fatalf("peak should ratchet to 220, got %.2f", at.gbEquityPeak)
	}

	// Fail-safe: equity unreadable (fake returns nil balance) => path skipped even
	// though the stored peak would imply a catastrophic drawdown.
	fake2 := &fakeProtectionTrader{
		positions: []map[string]interface{}{
			longPos("SOXLUSDT", 100, 97, 10),
			longPos("SKHYUSDT", 100, 97, 10),
		},
		equity: 0, // => GetBalance returns nil
	}
	at2 := newGuardTrader(fake2, equityCfg(5, 0, "retracing"), peaks)
	at2.gbEquityPeak = 1000
	at2.runGivebackGuard()
	if fake2.closeLongCalls != 0 {
		t.Fatalf("unreadable equity must skip the path (fail-safe), got %d", fake2.closeLongCalls)
	}
}

// TestGivebackGuardEquityDisabledNoOp: with the master switch off, a drawdown that
// would otherwise fire must do nothing — the feature is opt-in.
func TestGivebackGuardEquityDisabledNoOp(t *testing.T) {
	fake := &fakeProtectionTrader{
		positions: []map[string]interface{}{
			longPos("SOXLUSDT", 100, 97, 10),
			longPos("SKHYUSDT", 100, 97, 10),
		},
		equity: 90,
	}
	peaks := map[string]float64{"SOXLUSDT_long": 3.7, "SKHYUSDT_long": 3.7}
	cfg := equityCfg(5, 0, "retracing")
	cfg.BreadthEquityEnabled = false
	at := newGuardTrader(fake, cfg, peaks)
	at.gbEquityPeak = 100
	at.runGivebackGuard()
	if fake.closeLongCalls != 0 {
		t.Fatalf("disabled equity breaker must be a no-op, got %d", fake.closeLongCalls)
	}
}

// TestGivebackGuardEquityDryRunNoOrders: dry-run must place no orders but STILL
// re-base the peak, so observed dry-run trigger frequency matches live instead of
// firing every single cycle forever.
func TestGivebackGuardEquityDryRunNoOrders(t *testing.T) {
	fake := &fakeProtectionTrader{
		positions: []map[string]interface{}{
			longPos("SOXLUSDT", 100, 97, 10),
			longPos("SKHYUSDT", 100, 97, 10),
		},
		equity: 90,
	}
	peaks := map[string]float64{"SOXLUSDT_long": 3.7, "SKHYUSDT_long": 3.7}
	cfg := equityCfg(5, 0, "retracing")
	cfg.DryRun = true
	at := newGuardTrader(fake, cfg, peaks)
	at.gbEquityPeak = 100
	at.runGivebackGuard()
	if fake.closeLongCalls != 0 {
		t.Fatalf("dry-run must not place orders, got %d", fake.closeLongCalls)
	}
	if at.gbEquityPeak != 90 {
		t.Fatalf("dry-run should still re-base peak to 90, got %.2f", at.gbEquityPeak)
	}
}

// TestGivebackGuardQuorumStillFiresWithEquityOff: the pre-existing quorum path is
// unchanged when the equity path is disabled (no regression from the refactor).
func TestGivebackGuardQuorumStillFiresWithEquityOff(t *testing.T) {
	fake := &fakeProtectionTrader{positions: []map[string]interface{}{
		longPos("ADAUSDT", 100, 97, 200),
		longPos("SOLUSDT", 100, 96, 200),
		longPos("DOTUSDT", 100, 98, 200),
	}}
	peaks := map[string]float64{"ADAUSDT_long": 5, "SOLUSDT_long": 4, "DOTUSDT_long": 6}
	at := newGuardTrader(fake, breadthCfg(3, 0.7), peaks)
	at.gbEquityPeak = 1000 // would be a huge DD, but the equity path is off
	at.runGivebackGuard()
	if fake.closeLongCalls != 3 {
		t.Fatalf("quorum path should still cut 3, got %d", fake.closeLongCalls)
	}
	if tag := lastTag(fake.taggedCloseLongs); tag != "giveback_guard_breadth" {
		t.Fatalf("quorum fire must keep reason giveback_guard_breadth, got %q", tag)
	}
}

// TestGivebackGuardQuorumWinsWhenBothHit: when both modes trigger in the same
// cycle the quorum path owns the decision (more specific, cross-validated scope
// rules) — proven by the close reason and by a protected winner being spared.
func TestGivebackGuardQuorumWinsWhenBothHit(t *testing.T) {
	fake := &fakeProtectionTrader{
		positions: []map[string]interface{}{
			longPos("ADAUSDT", 100, 97, 200),  // retracing loser
			longPos("SOLUSDT", 100, 96, 200),  // retracing loser
			longPos("DOTUSDT", 100, 98, 200),  // retracing loser
			longPos("BTCUSDT", 100, 104, 200), // retracing winner, PROTECTED
		},
		equity: 50, // also a massive equity drawdown
	}
	peaks := map[string]float64{
		"ADAUSDT_long": 5, "SOLUSDT_long": 4, "DOTUSDT_long": 6, "BTCUSDT_long": 12,
	}
	cfg := breadthCfg(3, 0.7)
	cfg.BreadthEquityEnabled = true
	cfg.BreadthEquityDDPct = 5
	cfg.BreadthEquityScope = "all" // would cut all 4 if the equity path won
	at := newGuardTrader(fake, cfg, peaks)
	at.gbEquityPeak = 100
	at.protectionState["BTCUSDT_long"] = "native_trailing_armed"

	at.runGivebackGuard()
	if fake.closeLongCalls != 3 {
		t.Fatalf("quorum should win and spare the protected winner (3 cuts), got %d", fake.closeLongCalls)
	}
	if tag := lastTag(fake.taggedCloseLongs); tag != "giveback_guard_breadth" {
		t.Fatalf("expected quorum reason giveback_guard_breadth, got %q", tag)
	}
	// Quorum fire must NOT re-base the equity peak (that is the equity path's own
	// repeat-fire guard; moving it here would silently disarm the account stop).
	if at.gbEquityPeak != 100 {
		t.Fatalf("quorum fire must leave the equity peak at 100, got %.2f", at.gbEquityPeak)
	}
}

// TestGivebackGuardEquityMinPos: the optional per-path quorum blocks a fire when
// too few positions are open, and allows it once the count is met.
func TestGivebackGuardEquityMinPos(t *testing.T) {
	peaks := map[string]float64{"SOXLUSDT_long": 3.7, "SKHYUSDT_long": 3.7}
	mk := func(n int) *fakeProtectionTrader {
		pos := []map[string]interface{}{longPos("SOXLUSDT", 100, 97, 10)}
		if n > 1 {
			pos = append(pos, longPos("SKHYUSDT", 100, 97, 10))
		}
		return &fakeProtectionTrader{positions: pos, equity: 90}
	}
	cfg := equityCfg(5, 0, "retracing")
	cfg.BreadthEquityMinPos = 2

	fake := mk(1)
	at := newGuardTrader(fake, cfg, peaks)
	at.gbEquityPeak = 100
	at.runGivebackGuard()
	if fake.closeLongCalls != 0 {
		t.Fatalf("below equity min_pos must not fire, got %d", fake.closeLongCalls)
	}

	fake2 := mk(2)
	at2 := newGuardTrader(fake2, cfg, peaks)
	at2.gbEquityPeak = 100
	at2.runGivebackGuard()
	if fake2.closeLongCalls != 2 {
		t.Fatalf("at equity min_pos should fire and cut 2, got %d", fake2.closeLongCalls)
	}
}

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
