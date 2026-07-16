package store

import "testing"

func TestClassifyCloseMechanisms(t *testing.T) {
	cases := []struct {
		reason   string
		category string
		mech     string
	}{
		{"ai_close_long", CategoryAI, MechAIClose},
		{"ai_close_short", CategoryAI, MechAIClose},
		{"trend_reversal_flip", CategoryAI, MechTrendReversal},
		{"giveback_guard_breadth", CategoryProtection, MechBreadthBreaker},
		{"breadth_breaker", CategoryProtection, MechBreadthBreaker},
		{"managed_drawdown_runner_exit", CategoryProtection, MechManagedDrawdown},
		{"time_stop", CategoryProtection, MechTimeStop},
		{"max_hold", CategoryProtection, MechMaxHold},
		{"trailing_take_profit", CategoryProtection, MechTrailingTP},
		{"native_trailing", CategoryProtection, MechNativeTrailing},
		{"break_even_stop", CategoryProtection, MechBreakEven},
		{"full_sl", CategoryProtection, MechFullSL},
		{"structural_sl", CategoryProtection, MechStructuralSL},
		{"full_tp", CategoryProtection, MechFullTP},
		{"ladder_tp", CategoryProtection, MechLadderTP},
		{"ladder_sl", CategoryProtection, MechLadderSL},
		{"manual_close_long", CategoryManual, MechManualClose},
		{"liquidation", CategoryExchange, MechLiquidation},
		{"close_long", CategoryExchange, MechSyncExternal},
		{"close_short", CategoryExchange, MechSyncExternal},
		{"sync_absent_from_exchange", CategoryExchange, MechSyncExternal},
		{"", CategorySystem, MechUnknownClose},
	}
	for _, c := range cases {
		got := ClassifyClose(c.reason)
		if got.Category != c.category || got.Mechanism != c.mech {
			t.Errorf("ClassifyClose(%q) = {%s,%s}, want {%s,%s}",
				c.reason, got.Category, got.Mechanism, c.category, c.mech)
		}
	}
}

// trailing_take_profit must not be swallowed by the native_trailing substring
// rule. native_trailing is checked first and contains "trailing"; ensure the
// dedicated trailing_take_profit reason still classifies distinctly.
func TestClassifyCloseTrailingTakeProfitNotSwallowed(t *testing.T) {
	got := ClassifyClose("trailing_take_profit")
	if got.Mechanism != MechTrailingTP {
		t.Fatalf("trailing_take_profit classified as %s, want %s", got.Mechanism, MechTrailingTP)
	}
}
