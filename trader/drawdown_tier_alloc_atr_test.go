package trader

import (
	"math"
	"strings"
	"testing"

	"nofx/store"
)

// Production shape from claude-ct30 (trader "claude", OKX), 2026-07-27:
//
//	dd1                 min=3 ATR  dd=1.8 ATR  close=100  (both modes "manual")
//	partial_profit_lock min=4 ATR  dd=1.2 ATR  close=30   (all modes "manual")
//
// The tier allocations logged "1 tiers → dd1 qty=380.000000 (100.0%) | peak_trigger=3.00%"
// — a RAW ATR multiple sitting in a percent-typed field — while the arm path armed the
// same position with the resolved 4.2012%. The partial tier then armed at the FULL
// position size on two live symbols:
//
//	WLDUSDT long  380 units  order 3778506109091213312  close_ratio_pct=100 (want 30)
//	CLUSDT  short 1.4 units  order 3778585660408365056  close_ratio_pct=100 (want 30)
//
// Both defects live in this file's init path, and both are exercised here.
func ct30ATRRules() []store.DrawdownTakeProfitRule {
	return []store.DrawdownTakeProfitRule{
		{
			MinProfitPct: 3, MinProfitUnit: store.ProtectionUnitATR, MinProfitMode: store.ProtectionValueModeManual,
			MaxDrawdownPct: 1.8, MaxDrawdownUnit: store.ProtectionUnitATR, MaxDrawdownMode: store.ProtectionValueModeManual,
			CloseRatioPct: 100, StageName: "dd1",
		},
		{
			MinProfitPct: 4, MinProfitUnit: store.ProtectionUnitATR, MinProfitMode: store.ProtectionValueModeManual,
			MaxDrawdownPct: 1.2, MaxDrawdownUnit: store.ProtectionUnitATR, MaxDrawdownMode: store.ProtectionValueModeManual,
			CloseRatioPct: 30, CloseRatioMode: store.ProtectionValueModeManual,
		},
	}
}

// atrTierFixture wires an AutoTrader with ATR protection on and a frozen ATR, so
// resolveDrawdownRulesATR produces the same effective percents the live trader saw.
func atrTierFixture(t *testing.T, symbol, side string, entry, atrValue float64) *AutoTrader {
	t.Helper()
	fake := &fakeVenueTrader{
		venue:     "okx",
		markPrice: entry,
		position: map[string]interface{}{
			"symbol": symbol, "side": side,
			"positionAmt": 380.0, "markPrice": entry, "entryPrice": entry,
		},
	}
	at := newVenueAutoTrader(t, "okx", fake)
	at.config.StrategyConfig.ATRProtection = store.ATRProtectionConfig{Enabled: true}
	at.config.StrategyConfig.Protection = store.ProtectionConfig{
		DrawdownTakeProfit: store.DrawdownTakeProfitConfig{
			Enabled: true, Mode: store.ProtectionModeManual, Rules: ct30ATRRules(),
		},
	}

	key := frozenATRKey(at.id, symbol, store.ATRProtectionConfig{}.WithDefaults().Timeframe, strings.ToUpper(side))
	frozenATRMu.Lock()
	frozenATRCache[key] = frozenATREntry{entryPrice: entry, atr: atrValue}
	frozenATRMu.Unlock()
	t.Cleanup(func() {
		frozenATRMu.Lock()
		delete(frozenATRCache, key)
		frozenATRMu.Unlock()
	})
	return at
}

// TestTierAllocsAreStoredInPercentNotATRMultiples pins the root cause. Even though the
// caller passes rules it has already resolved, resolveDrawdownRulesWithModes rebuilds each
// output from the RAW strategy rule (result := base) and only copies a caller value when
// that field's mode is "ai" — so a fully-manual strategy silently reverts the resolution.
// tier.MinProfitPct is read as a percent by updateDrawdownTierStates, so a raw 3.0 would
// flip the tier to "tracking" at 3.00% PnL instead of the true 4.2012%.
func TestTierAllocsAreStoredInPercentNotATRMultiples(t *testing.T) {
	const (
		symbol   = "WLDUSDT"
		side     = "long"
		entry    = 0.3562
		posQty   = 380.0
		atrValue = 0.0049866 // 1 ATR = 1.4000% of entry
	)
	at := atrTierFixture(t, symbol, side, entry, atrValue)

	at.initDrawdownTiersFromResolvedRules(symbol, side, posQty, entry, ct30ATRRules())

	allocs := at.getDrawdownTierAllocs(symbol, side)
	if len(allocs) == 0 {
		t.Fatalf("no tier allocations were stored")
	}
	// 3 ATR of a 1.4%-per-ATR instrument = 4.2%. The raw multiple is 3.0.
	wantDD1Min := 3 * 1.4
	for _, a := range allocs {
		if a.StageName != "dd1" {
			continue
		}
		if math.Abs(a.MinProfitPct-3.0) < 0.01 {
			t.Fatalf("dd1 MinProfitPct=%.4f is the RAW ATR multiple 3.0 stored in a percent field; "+
				"want the resolved %.4f%%. updateDrawdownTierStates would start tracking at 3%% PnL "+
				"instead of %.2f%%, and getCumulativeCloseRatioByRule can no longer match the arm "+
				"path's resolved rules.", a.MinProfitPct, wantDD1Min, wantDD1Min)
		}
		if math.Abs(a.MinProfitPct-wantDD1Min) > 0.05 {
			t.Fatalf("dd1 MinProfitPct: want ~%.4f%%, got %.4f", wantDD1Min, a.MinProfitPct)
		}
	}
}

// TestPartialTierKeepsItsOwnCloseRatio is the user-visible statement of the bug: the 30%
// tier must never arm for the whole position.
//
// The arm path calls getCumulativeCloseRatioByRule with an ATR-RESOLVED rule. Pre-fix, no
// alloc matched (allocs held raw multiples), and the "highest tier whose MinProfitPct <=
// the rule's" fallback adopted dd1 and summed its 100 — clamped to 100. That is how a
// close=30 rule produced a close=100 order.
func TestPartialTierKeepsItsOwnCloseRatio(t *testing.T) {
	const (
		symbol   = "WLDUSDT"
		side     = "long"
		entry    = 0.3562
		posQty   = 380.0
		atrValue = 0.0049866
	)
	at := atrTierFixture(t, symbol, side, entry, atrValue)
	at.initDrawdownTiersFromResolvedRules(symbol, side, posQty, entry, ct30ATRRules())

	// Exactly what auto_trader_risk.go hands the arm path.
	resolved := at.resolveDrawdownRulesATR(ct30ATRRules(), symbol, side, entry)
	for i := range resolved {
		resolved[i] = normalizeDrawdownRule(resolved[i])
	}

	for _, rule := range resolved {
		got := at.getCumulativeCloseRatioByRule(symbol, side, rule)
		if rule.CloseRatioPct >= 99.999 {
			continue // the full tier legitimately closes everything
		}
		if got >= 99.999 {
			t.Fatalf("partial tier (stage=%s close=%.1f%% min=%.4f) resolved to cumulative %.1f%% — "+
				"it would arm a trailing order for the ENTIRE position (%.0f units) instead of "+
				"%.0f. This is the live WLDUSDT/CLUSDT defect.",
				rule.StageName, rule.CloseRatioPct, rule.MinProfitPct, got,
				posQty, posQty*rule.CloseRatioPct/100)
		}
		if math.Abs(got-rule.CloseRatioPct) > 0.01 {
			t.Fatalf("partial tier cumulative ratio: want %.1f%% (its own, no ladder inheritance), got %.1f%%",
				rule.CloseRatioPct, got)
		}
	}
}

// TestCumulativeRatioStillInheritsWithinAGenuineLadder guards the fix from over-reaching.
// Cumulative inheritance is correct and must survive: with T1=60% and T2=25%, arming T2
// has to cover 85% of the original position, because T1's slice is T2's responsibility
// once T1 is superseded. Percent-unit rules match their allocs exactly, so the identity
// match resolves and the sum is taken as before.
func TestCumulativeRatioStillInheritsWithinAGenuineLadder(t *testing.T) {
	const (
		symbol = "BTCUSDT"
		side   = "long"
		entry  = 65000.0
		posQty = 1.0
	)
	rules := []store.DrawdownTakeProfitRule{
		{MinProfitPct: 2, MaxDrawdownPct: 1.0, CloseRatioPct: 60, StageName: "T1"},
		{MinProfitPct: 5, MaxDrawdownPct: 0.8, CloseRatioPct: 25, StageName: "T2"},
	}
	fake := &fakeVenueTrader{venue: "okx", markPrice: entry}
	at := newVenueAutoTrader(t, "okx", fake)
	at.config.StrategyConfig.Protection = store.ProtectionConfig{
		DrawdownTakeProfit: store.DrawdownTakeProfitConfig{
			Enabled: true, Mode: store.ProtectionModeManual, Rules: rules,
		},
	}
	at.initDrawdownTiersForPosition(symbol, side, posQty, entry, rules)

	if got := at.getCumulativeCloseRatioByRule(symbol, side, normalizeDrawdownRule(rules[0])); math.Abs(got-60) > 0.01 {
		t.Fatalf("T1 cumulative: want 60, got %.2f", got)
	}
	if got := at.getCumulativeCloseRatioByRule(symbol, side, normalizeDrawdownRule(rules[1])); math.Abs(got-85) > 0.01 {
		t.Fatalf("T2 cumulative: want 85 (60+25, inheriting the superseded T1 slice), got %.2f", got)
	}
}
