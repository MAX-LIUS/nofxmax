package store

import "testing"

func TestClassifyClose(t *testing.T) {
	cases := []struct {
		in       string
		wantCat  string
		wantMech string
	}{
		{"managed_drawdown_runner_exit", CategoryProtection, MechManagedDrawdown},
		{"managed_drawdown", CategoryProtection, MechManagedDrawdown},
		{"native_trailing", CategoryProtection, MechNativeTrailing},
		{"trailing_take_profit", CategoryProtection, MechTrailingTP},
		{"break_even_stop", CategoryProtection, MechBreakEven},
		{"break_even_after_target", CategoryProtection, MechBreakEven},
		{"ladder_tp", CategoryProtection, MechLadderTP},
		{"ladder_sl", CategoryProtection, MechLadderSL},
		{"full_tp", CategoryProtection, MechFullTP},
		{"full_sl", CategoryProtection, MechFullSL},
		{"fallback_maxloss_sl", CategoryProtection, MechFallbackSL},
		{"time_stop", CategoryProtection, MechTimeStop},
		{"emergency_protection_close", CategoryProtection, MechEmergency},
		{"max_hold", CategoryProtection, MechMaxHold},
		{"close_by_side", CategorySystem, MechUnknownClose},
		{"ai_close_long", CategoryAI, MechAIClose},
		{"ai_close_short", CategoryAI, MechAIClose},
		{"manual_close_long", CategoryManual, MechManualClose},
		{"manual_close_short", CategoryManual, MechManualClose},
		{"liquidation", CategoryExchange, MechLiquidation},
		{"adl", CategoryExchange, MechLiquidation},
		{"sync_absent_from_exchange", CategoryExchange, MechSyncExternal},
		{"close_long", CategoryExchange, MechSyncExternal},
		{"close_short", CategoryExchange, MechSyncExternal},
		{"", CategorySystem, MechUnknownClose},
		{"some_garbage_reason", CategorySystem, MechUnknownClose},
		{"  Managed_Drawdown_Runner  ", CategoryProtection, MechManagedDrawdown}, // case/space tolerant
	}
	for _, c := range cases {
		got := ClassifyClose(c.in)
		if got.Category != c.wantCat || got.Mechanism != c.wantMech {
			t.Errorf("ClassifyClose(%q)=%+v want {%s %s}", c.in, got, c.wantCat, c.wantMech)
		}
	}
}

func TestClassifyOpen(t *testing.T) {
	cases := []struct {
		in       string
		wantCat  string
		wantMech string
	}{
		{"ai_open", CategoryAI, MechAIOpen},
		{"ai", CategoryAI, MechAIOpen},
		{"system", CategoryAI, MechAIOpen},
		{"breakout", CategoryAI, MechBreakout},
		{"breakout_entry", CategoryAI, MechBreakout},
		{"manual_open", CategoryManual, MechManualOpen},
		{"sync", CategoryExchange, MechSyncExtOpen},
		{"snapshot", CategoryExchange, MechSyncExtOpen},
		{"", CategorySystem, MechUnknownOpen},
		{"weird", CategorySystem, MechUnknownOpen},
	}
	for _, c := range cases {
		got := ClassifyOpen(c.in)
		if got.Category != c.wantCat || got.Mechanism != c.wantMech {
			t.Errorf("ClassifyOpen(%q)=%+v want {%s %s}", c.in, got, c.wantCat, c.wantMech)
		}
	}
}
