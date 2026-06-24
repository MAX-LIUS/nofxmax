package trader

import (
	"nofx/store"
	"testing"
	"time"
)

func newReplaceTestTrader(rc store.RiskControlConfig) *AutoTrader {
	return &AutoTrader{
		id:                    "t1",
		exchange:              "paper",
		config:                AutoTraderConfig{StrategyConfig: &store.StrategyConfig{RiskControl: rc}},
		positionFirstSeenTime: make(map[string]int64),
	}
}

func pos(symbol, side string, amt, markPrice, unrealized, leverage float64) map[string]interface{} {
	return map[string]interface{}{
		"symbol":           symbol,
		"side":             side,
		"positionAmt":      amt,
		"markPrice":        markPrice,
		"unRealizedProfit": unrealized,
		"leverage":         leverage,
	}
}

func TestGetReplaceWeakestConfigDefaults(t *testing.T) {
	at := newReplaceTestTrader(store.RiskControlConfig{ReplaceWeakestEnabled: true})
	cfg := at.getReplaceWeakestConfig()
	if !cfg.Enabled {
		t.Fatal("expected enabled")
	}
	if cfg.MinConfidence != 80 || cfg.MinHoldMinutes != 30 || cfg.MaxVictimProfitPct != 1.0 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}

	off := newReplaceTestTrader(store.RiskControlConfig{ReplaceWeakestEnabled: false})
	if off.getReplaceWeakestConfig().Enabled {
		t.Fatal("expected disabled when flag false")
	}
}

func TestSelectReplaceVictimPrefersSmallOldWeak(t *testing.T) {
	cfg := replaceWeakestConfig{Enabled: true, MinConfidence: 80, MinHoldMinutes: 30, MaxVictimProfitPct: 1.0}
	at := newReplaceTestTrader(store.RiskControlConfig{ReplaceWeakestEnabled: true})
	now := time.Now().UnixMilli()
	// big fresh winner — protected by profit + freshness
	at.positionFirstSeenTime["BIGUSDT_long"] = now - 10*60*1000 // 10m (too new)
	// small old loser — ideal victim
	at.positionFirstSeenTime["SMALLUSDT_long"] = now - 5*60*60*1000 // 5h
	// medium old slight loss
	at.positionFirstSeenTime["MEDUSDT_short"] = now - 2*60*60*1000 // 2h

	positions := []map[string]interface{}{
		pos("BIGUSDT", "long", 100, 50, 80, 10),  // notional 5000, +16% pnl-ish, fresh
		pos("SMALLUSDT", "long", 10, 20, -8, 10), // notional 200, negative pnl, old
		pos("MEDUSDT", "short", -30, 40, -5, 10), // notional 1200, slight loss, 2h
	}
	v := at.selectReplaceVictim(positions, "NEWUSDT", cfg)
	if v == nil {
		t.Fatal("expected a victim")
	}
	if v.Symbol != "SMALLUSDT" {
		t.Fatalf("expected SMALLUSDT (small+old+weak), got %s (score=%.3f)", v.Symbol, v.Score)
	}
}

func TestSelectReplaceVictimProtectsWinnersAndFresh(t *testing.T) {
	cfg := replaceWeakestConfig{Enabled: true, MinConfidence: 80, MinHoldMinutes: 30, MaxVictimProfitPct: 1.0}
	at := newReplaceTestTrader(store.RiskControlConfig{ReplaceWeakestEnabled: true})
	now := time.Now().UnixMilli()
	at.positionFirstSeenTime["WINUSDT_long"] = now - 5*60*60*1000 // old but winner
	at.positionFirstSeenTime["FRESHUSDT_long"] = now - 5*60*1000  // loser but fresh (5m)

	positions := []map[string]interface{}{
		pos("WINUSDT", "long", 10, 100, 50, 10),   // +50% pnl → protected
		pos("FRESHUSDT", "long", 10, 100, -5, 10), // loss but too new → protected
	}
	if v := at.selectReplaceVictim(positions, "NEWUSDT", cfg); v != nil {
		t.Fatalf("expected no eligible victim, got %s", v.Symbol)
	}
}

func TestSelectReplaceVictimExcludesSameSymbol(t *testing.T) {
	cfg := replaceWeakestConfig{Enabled: true, MinConfidence: 80, MinHoldMinutes: 30, MaxVictimProfitPct: 1.0}
	at := newReplaceTestTrader(store.RiskControlConfig{ReplaceWeakestEnabled: true})
	now := time.Now().UnixMilli()
	at.positionFirstSeenTime["NEWUSDT_short"] = now - 5*60*60*1000

	positions := []map[string]interface{}{
		pos("NEWUSDT", "short", 10, 100, -5, 10), // same symbol as incoming → excluded
	}
	if v := at.selectReplaceVictim(positions, "NEWUSDT", cfg); v != nil {
		t.Fatalf("expected same-symbol exclusion, got %s", v.Symbol)
	}
}
