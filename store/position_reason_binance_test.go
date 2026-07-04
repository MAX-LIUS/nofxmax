package store

import "testing"

// These tests model the Binance deterministic-attribution path: SyncOrdersFromBinance
// now writes the REAL originating order type (origType from GetOrder) onto the
// synced order record instead of a hardcoded "MARKET", and deriveCloseReason
// resolves the mechanism from that type with full position context. Binance
// protection has no broker reason-tag and records no close-intent (it fires
// exchange-side), so the order TYPE is the sole deterministic signal — exactly
// what these cases assert.

func TestDeriveCloseReason_Binance_StopMarketNearEntryIsBreakEven(t *testing.T) {
	s := newTestPositionStore(t)
	pos := seedTestPosition(t, s, "bn-1", 100, 100)
	// Native STOP_MARKET, no tag/action (Binance can't tag), full close near entry.
	seedTestOrder(t, s, "bn-1", "tr-1", "", "STOP_MARKET", "close_long")

	reason, source, execType := s.deriveCloseReason(pos, "tr-1", "close_long", 100, 100.2)
	if reason != "break_even_stop" || source != "break_even_stop" {
		t.Fatalf("expected break_even_stop (STOP near entry), got reason=%q source=%q", reason, source)
	}
	if execType != "STOP_MARKET" {
		t.Fatalf("expected STOP_MARKET exec type, got %q", execType)
	}
}

func TestDeriveCloseReason_Binance_StopMarketFarBelowIsFullSL(t *testing.T) {
	s := newTestPositionStore(t)
	pos := seedTestPosition(t, s, "bn-2", 100, 100)
	seedTestOrder(t, s, "bn-2", "tr-2", "", "STOP_MARKET", "close_long")

	// Full close well below entry (4.5%): a real stop-loss, not break-even.
	reason, source, _ := s.deriveCloseReason(pos, "tr-2", "close_long", 100, 95.5)
	if reason != "full_sl" || source != "full_sl" {
		t.Fatalf("expected full_sl (STOP far below entry), got reason=%q source=%q", reason, source)
	}
}

func TestDeriveCloseReason_Binance_TakeProfitPartialIsLadderTP(t *testing.T) {
	s := newTestPositionStore(t)
	pos := seedTestPosition(t, s, "bn-3", 100, 100)
	seedTestOrder(t, s, "bn-3", "tr-3", "", "TAKE_PROFIT_MARKET", "close_long")

	// Partial close (20 of 100) in profit: a ladder take-profit tier.
	reason, source, execType := s.deriveCloseReason(pos, "tr-3", "close_long", 20, 101.1)
	if reason != "ladder_tp" || source != "ladder_tp" {
		t.Fatalf("expected ladder_tp (partial TAKE_PROFIT), got reason=%q source=%q", reason, source)
	}
	if execType != "TAKE_PROFIT_MARKET" {
		t.Fatalf("expected TAKE_PROFIT_MARKET exec type, got %q", execType)
	}
}

func TestDeriveCloseReason_Binance_TakeProfitFullIsFullTP(t *testing.T) {
	s := newTestPositionStore(t)
	pos := seedTestPosition(t, s, "bn-4", 100, 100)
	seedTestOrder(t, s, "bn-4", "tr-4", "", "TAKE_PROFIT_MARKET", "close_long")

	// Full close in profit: full take-profit.
	reason, source, _ := s.deriveCloseReason(pos, "tr-4", "close_long", 100, 103.6)
	if reason != "full_tp" || source != "full_tp" {
		t.Fatalf("expected full_tp (full TAKE_PROFIT), got reason=%q source=%q", reason, source)
	}
}

func TestDeriveCloseReason_Binance_BotMarketCloseNormalizesManagedDrawdown(t *testing.T) {
	s := newTestPositionStore(t)
	pos := seedTestPosition(t, s, "bn-5", 100, 100)
	// Bot MARKET close: resolver stamped OrderAction from the close-intent reason.
	// The stage-suffixed reason is normalized to the canonical mechanism.
	seedTestOrder(t, s, "bn-5", "tr-5", "", "MARKET", "managed_drawdown_runner_exit")

	reason, source, _ := s.deriveCloseReason(pos, "tr-5", "close_long", 100, 101.5)
	if reason != "managed_drawdown" || source != "managed_drawdown" {
		t.Fatalf("expected managed_drawdown (normalized), got reason=%q source=%q", reason, source)
	}
}

func TestDeriveCloseReason_Binance_BotMarketCloseAdoptsAIClose(t *testing.T) {
	s := newTestPositionStore(t)
	pos := seedTestPosition(t, s, "bn-6", 100, 100)
	// AI close stamped from close-intent onto a bot MARKET close. No STOP/TP/
	// TRAILING type and no substring rule fires in the first switch, so the
	// final-adoption block carries the resolved action through verbatim.
	seedTestOrder(t, s, "bn-6", "tr-6", "", "MARKET", "ai_close_long")

	reason, source, _ := s.deriveCloseReason(pos, "tr-6", "close_long", 100, 100.5)
	if reason != "ai_close_long" || source != "ai_close_long" {
		t.Fatalf("expected ai_close_long adopted from action, got reason=%q source=%q", reason, source)
	}
	if ClassifyClose(reason).Category != CategoryAI {
		t.Fatalf("expected AI category for ai_close_long, got %q", ClassifyClose(reason).Category)
	}
}
