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
