package trader

import (
	"testing"

	"nofx/store"
)

// activePx for a runner DD at +6% must match the min_profit=6 rule, so the phantom
// conversion arms the correct managed tier (Plan A, 2026-06-09).
func TestMatchDrawdownRuleByActivation(t *testing.T) {
	rules := []store.DrawdownTakeProfitRule{
		{MinProfitPct: 6, MaxDrawdownPct: 40, CloseRatioPct: 25, StageName: "runner_exit"},
	}
	entry := 90.13
	// short +6% activation = entry * (1 - 0.06) = 84.7222
	activePx := entry * (1 - 0.06)
	r, ok := matchDrawdownRuleByActivation(rules, entry, "short", activePx)
	if !ok || r.MinProfitPct != 6 {
		t.Fatalf("expected match min_profit=6, got ok=%v rule=%+v", ok, r)
	}

	// long +6% activation = entry * 1.06
	entryL := 100.0
	r2, ok2 := matchDrawdownRuleByActivation(rules, entryL, "long", entryL*1.06)
	if !ok2 || r2.MinProfitPct != 6 {
		t.Fatalf("expected long match min_profit=6, got ok=%v rule=%+v", ok2, r2)
	}

	// no rules → no match
	if _, ok3 := matchDrawdownRuleByActivation(nil, entry, "short", activePx); ok3 {
		t.Fatal("expected no match with empty rules")
	}

	// activePx far off → falls back to highest-minProfit rule (still ok=true)
	multi := []store.DrawdownTakeProfitRule{
		{MinProfitPct: 3, MaxDrawdownPct: 50, CloseRatioPct: 40},
		{MinProfitPct: 6, MaxDrawdownPct: 40, CloseRatioPct: 25},
	}
	rf, okf := matchDrawdownRuleByActivation(multi, entry, "short", 999.0)
	if !okf || rf.MinProfitPct != 6 {
		t.Fatalf("expected fallback to highest min_profit=6, got ok=%v rule=%+v", okf, rf)
	}
}

// Regression: adjacent SPCXUSDT tiers sit ~1% apart, so both fall inside the 1%
// tolerance for a given activePx. The old "first-within-tolerance" match could
// misattribute the 30% partial-lock tier to the neighbouring 100% full-close tier,
// forcing a full close instead of the strategy's partial reduction. Closest-match
// must resolve to the exact 30% tier whose activation price equals activePx.
func TestMatchDrawdownRuleByActivation_ClosestNotFirst(t *testing.T) {
	entry := 175.26 // SPCX short entry; +6% dd1 ≈ 164.74, +4% lock ≈ 163.05
	rules := []store.DrawdownTakeProfitRule{
		// dd1: min_profit=3 → short activation entry*(1-0.03) ≈ 170.0 (far)
		{MinProfitPct: 3, MaxDrawdownPct: 1.8, CloseRatioPct: 100, StageName: "dd1"},
		// partial lock: min_profit=4 → short activation entry*(1-0.04) ≈ 168.25
		{MinProfitPct: 4, MaxDrawdownPct: 1.2, CloseRatioPct: 30, StageName: "partial_profit_lock"},
	}
	// activePx exactly at the +4% (30%) tier activation.
	activePx := entry * (1 - 0.04)
	r, ok := matchDrawdownRuleByActivation(rules, entry, "short", activePx)
	if !ok {
		t.Fatalf("expected a match, got none")
	}
	if r.CloseRatioPct != 30 || r.MinProfitPct != 4 {
		t.Fatalf("expected 30%% partial tier (min_profit=4), got close=%.1f%% min_profit=%.2f — full-close misattribution regressed", r.CloseRatioPct, r.MinProfitPct)
	}

	// And activePx exactly at the +3% (100%) tier must resolve to that one.
	activePxFull := entry * (1 - 0.03)
	rFull, okFull := matchDrawdownRuleByActivation(rules, entry, "short", activePxFull)
	if !okFull || rFull.CloseRatioPct != 100 {
		t.Fatalf("expected 100%% tier at its own activation, got close=%.1f%%", rFull.CloseRatioPct)
	}
}
