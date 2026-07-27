package trader

import (
	"fmt"
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

// ---------------------------------------------------------------------------
// Bug class instance #9 (2026-07-27): the full-close tier ate the partial tier's
// allocation budget, so the partial was dropped from the alloc table entirely.
//
// computeDrawdownTierAllocations sorts by MinProfitPct and clamps each tier against a
// running allocatedPct. claude-ct30's dd1 (3 ATR peak, close 100) sorts BEFORE the partial
// (4 ATR peak, close 30), fills allocatedPct to 100, and the partial gets
// ratioPct = 100-100 = 0 → `continue`. Live log, pid 2282277:
//
//	📊 Drawdown tier allocations set for WLDUSDT long: 1 tiers
//	  → dd1: qty=380.000000 (100.0%) | peak_trigger=4.20% | drawdown=2.52%
//
// The clamp assumes tiers are mutually exclusive slices of the position. Under place-at-open
// they are not: the full tier and the partials rest on the exchange CONCURRENTLY, each with
// its own callback.
// ---------------------------------------------------------------------------

// TestFullCloseTierDoesNotEatPartialTierAllocation pins the dropped tier.
func TestFullCloseTierDoesNotEatPartialTierAllocation(t *testing.T) {
	const (
		symbol   = "WLDUSDT"
		side     = "long"
		entry    = 0.3562
		posQty   = 380.0
		atrValue = 0.0049866
	)
	at := atrTierFixture(t, symbol, side, entry, atrValue)
	at.initDrawdownTiersFromResolvedRules(symbol, side, posQty, entry, ct30ATRRules())

	allocs := at.getDrawdownTierAllocs(symbol, side)
	if len(allocs) != 2 {
		var got []string
		for _, a := range allocs {
			got = append(got, fmt.Sprintf("%s(close=%.1f%% qty=%.2f)", a.StageName, a.CloseRatioPct, a.Quantity))
		}
		t.Fatalf("want 2 tiers (dd1 + partial_profit_lock), got %d: %s — the full-close tier "+
			"consumed the whole allocation budget and the 30%% partial was dropped. A dropped "+
			"tier is invisible to updateDrawdownTierStates (no high-water-mark tracking), to "+
			"evaluateDrawdownTiers (the managed fallback can never execute it), and to the "+
			"dashboard tier panel.", len(allocs), strings.Join(got, " "))
	}

	var partial, full *store.DrawdownTierAllocation
	for i := range allocs {
		switch allocs[i].StageName {
		case "dd1":
			full = &allocs[i]
		case "partial_profit_lock":
			partial = &allocs[i]
		}
	}
	if full == nil || partial == nil {
		t.Fatalf("expected stages dd1 + partial_profit_lock, got %+v", allocs)
	}
	if math.Abs(full.Quantity-posQty) > 0.001 {
		t.Fatalf("dd1 is a whole-position safety net: want qty=%.0f, got %.4f", posQty, full.Quantity)
	}
	wantPartialQty := posQty * 0.30
	if math.Abs(partial.Quantity-wantPartialQty) > 0.001 {
		t.Fatalf("partial tier qty: want %.2f (30%% of %.0f), got %.4f", wantPartialQty, posQty, partial.Quantity)
	}
}

// TestPartialTierMatchesItsAllocAndDoesNotInheritTheFullTier is the trap this fix could
// have walked into: restoring the dropped tier makes the identity match SUCCEED, so if the
// cumulative sum still counted the concurrent full tier's 100, every partial would clamp to
// 100 and instance #8 would return through the match path instead of the fallback.
func TestPartialTierMatchesItsAllocAndDoesNotInheritTheFullTier(t *testing.T) {
	const (
		symbol   = "CLUSDT"
		side     = "short"
		entry    = 84.40
		posQty   = 1.4
		atrValue = 0.7605 // 1 ATR = 0.9011% of entry
	)
	at := atrTierFixture(t, symbol, side, entry, atrValue)
	at.initDrawdownTiersFromResolvedRules(symbol, side, posQty, entry, ct30ATRRules())

	resolved := at.resolveDrawdownRulesATR(ct30ATRRules(), symbol, side, entry)
	for i := range resolved {
		resolved[i] = normalizeDrawdownRule(resolved[i])
	}

	for _, rule := range resolved {
		got := at.getCumulativeCloseRatioByRule(symbol, side, rule)
		if isFullCloseTierRule(rule) {
			if math.Abs(got-100) > 0.01 {
				t.Fatalf("dd1 cumulative: want 100, got %.2f", got)
			}
			continue
		}
		if math.Abs(got-30) > 0.01 {
			t.Fatalf("partial tier (stage=%s min=%.4f) cumulative=%.2f%% — want its own 30%%. "+
				"Summing the concurrent full-close tier's 100 would arm %.2f units instead of %.2f "+
				"(the live CLUSDT defect).", rule.StageName, rule.MinProfitPct, got,
				posQty*got/100, posQty*0.30)
		}
	}
}

// TestPartialTrackingDoesNotSupersedeTheFullCloseTier: with both tiers in the table, the
// supersede loop would mark dd1 "superseded" as soon as the partial starts tracking (dd1 has
// the lower index). dd1 keeps its own live exchange order, so cancelling it in-memory would
// silently disable the managed fallback for the whole-position exit.
func TestPartialTrackingDoesNotSupersedeTheFullCloseTier(t *testing.T) {
	const (
		symbol   = "WLDUSDT"
		side     = "long"
		entry    = 0.3562
		posQty   = 380.0
		atrValue = 0.0049866
	)
	at := atrTierFixture(t, symbol, side, entry, atrValue)
	at.initDrawdownTiersForPosition(symbol, side, posQty, entry, ct30ATRRules())

	// Past the partial's 5.6016% trigger, so both tiers are satisfied.
	at.updateDrawdownTierStates(symbol, side, 6.0, 6.0)

	for _, a := range at.getDrawdownTierAllocs(symbol, side) {
		if a.StageName == "dd1" && a.Status == "superseded" {
			t.Fatalf("dd1 (whole-position, close=100) was superseded by the partial tier; "+
				"its exchange order is still live, so the managed fallback must keep owning it. "+
				"allocs=%+v", at.getDrawdownTierAllocs(symbol, side))
		}
		if a.StageName == "partial_profit_lock" && a.Status != "tracking" {
			t.Fatalf("partial tier should be tracking at 6%% PnL (trigger 5.60%%), got %q", a.Status)
		}
	}
}

// TestFindRuleForTierMatchesByIdentityNotPosition: TierIndex indexes the SORTED slice built
// inside computeDrawdownTierAllocations, but findRuleForTier is handed the caller's unsorted
// slice. When config order and sorted order differ, the positional fallback names the wrong
// tier — which in the managed path picks the wrong runner policy and the wrong
// exchangeSideCoversDrawdownTier check.
func TestFindRuleForTierMatchesByIdentityNotPosition(t *testing.T) {
	// Config order deliberately reversed relative to MinProfitPct order.
	rules := []store.DrawdownTakeProfitRule{
		{MinProfitPct: 5.60, MaxDrawdownPct: 1.68, CloseRatioPct: 30, StageName: "partial_profit_lock"},
		{MinProfitPct: 4.20, MaxDrawdownPct: 2.52, CloseRatioPct: 100, StageName: "dd1"},
	}
	allocs := computeDrawdownTierAllocations(380, rules)
	if len(allocs) != 2 {
		t.Fatalf("want 2 allocs, got %d", len(allocs))
	}
	for i := range allocs {
		tier := &allocs[i]
		got := findRuleForTier(rules, tier)
		if got == nil {
			t.Fatalf("no rule found for tier %s", tier.StageName)
		}
		if got.StageName != tier.StageName {
			t.Fatalf("tier %s (index %d) matched rule %q — positional fallback crossed the "+
				"tiers because TierIndex refers to the sorted slice, not the caller's",
				tier.StageName, tier.TierIndex, got.StageName)
		}
	}
}
