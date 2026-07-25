package api

import (
	"testing"

	"nofx/store"
)

// TestBuildPlacedProtectionPlan verifies that real close_intents become ordered,
// price-anchored plan rows: ladder tiers indexed nearest-first, mechanisms mapped
// to the correct display kind, and MARKET/ai_close intents (no protection level)
// dropped.
func TestBuildPlacedProtectionPlan(t *testing.T) {
	entry := 513.17
	intents := []store.CloseIntent{
		{Reason: store.MechLadderTP, TriggerPrice: 529.75},
		{Reason: store.MechLadderTP, TriggerPrice: 521.69}, // closest TP -> TP1
		{Reason: store.MechLadderTP, TriggerPrice: 526.32},
		{Reason: store.MechLadderSL, TriggerPrice: 478.41},
		{Reason: store.MechBreakEven, TriggerPrice: 505.68},
		{Reason: "ai_close_long", TriggerPrice: 0}, // must be dropped (not a level)
	}
	plan := buildPlacedProtectionPlan(intents, entry, true /*isLong*/, 0 /*no window filter*/)

	// 3 TP + 1 SL + 1 BE = 5 rows; ai_close dropped.
	if len(plan) != 5 {
		t.Fatalf("want 5 plan rows, got %d: %+v", len(plan), plan)
	}

	// Ladder TP nearest-to-entry must be labeled TP1.
	var tp1 *placedProtectionItem
	for i := range plan {
		if plan[i].Mechanism == store.MechLadderTP && plan[i].Label == "TP1" {
			tp1 = &plan[i]
			break
		}
	}
	if tp1 == nil {
		t.Fatal("expected a TP1 row")
	}
	if tp1.TriggerPrice == nil || *tp1.TriggerPrice != 521.69 {
		t.Fatalf("TP1 should be the nearest-to-entry price 521.69, got %+v", tp1.TriggerPrice)
	}
	if tp1.Kind != "tp" {
		t.Fatalf("TP1 kind should be tp, got %q", tp1.Kind)
	}

	// BE maps to kind be.
	var haveBE bool
	for _, it := range plan {
		if it.Mechanism == store.MechBreakEven && it.Kind == "be" {
			haveBE = true
		}
	}
	if !haveBE {
		t.Fatal("expected a break-even row with kind be")
	}
}

// TestBuildProtectionDeviation verifies manual-vs-AI SL/TP deltas and R multiples.
func TestBuildProtectionDeviation(t *testing.T) {
	entry := 513.2
	placed := []placedProtectionItem{
		{Mechanism: store.MechLadderTP, Kind: "tp", TriggerPrice: f(521.69)},
		{Mechanism: store.MechLadderSL, Kind: "sl", TriggerPrice: f(478.41)},
	}
	aiSL, aiTP := 509.8, 520.98
	dev := buildProtectionDeviation(placed, aiSL, aiTP, 0, entry, true)
	if dev == nil {
		t.Fatal("deviation should not be nil when both sides present")
	}
	if dev.ManualTP == nil || *dev.ManualTP != 521.69 {
		t.Fatalf("manual TP wrong: %+v", dev.ManualTP)
	}
	if dev.AISL == nil || *dev.AISL != 509.8 {
		t.Fatalf("ai SL wrong: %+v", dev.AISL)
	}
	if dev.ManualRR == nil || dev.AIRR == nil {
		t.Fatal("both R multiples should be computed")
	}
	// AI RR ~ (520.98-513.2)/(513.2-509.8) = 7.78/3.4 = 2.29
	if *dev.AIRR < 2.0 || *dev.AIRR > 2.6 {
		t.Fatalf("ai RR out of expected range: %v", *dev.AIRR)
	}
}

// TestBuildProtectionDeviationEntry verifies the planned-vs-actual entry row: the
// AI entry surfaces and the slippage/chase percent is (actual-planned)/planned*100.
func TestBuildProtectionDeviationEntry(t *testing.T) {
	entry := 513.2 // actual fill
	placed := []placedProtectionItem{
		{Mechanism: store.MechLadderTP, Kind: "tp", TriggerPrice: f(521.69)},
		{Mechanism: store.MechLadderSL, Kind: "sl", TriggerPrice: f(478.41)},
	}
	aiEntry := 510.0 // planned
	dev := buildProtectionDeviation(placed, 509.8, 520.98, aiEntry, entry, true)
	if dev == nil || dev.AIEntry == nil {
		t.Fatal("ai entry should be present")
	}
	if *dev.AIEntry != 510.0 {
		t.Fatalf("ai entry wrong: %v", *dev.AIEntry)
	}
	if dev.EntryDiffPct == nil {
		t.Fatal("entry diff pct should be computed when both entries present")
	}
	// (513.2-510.0)/510.0*100 = 0.627 → round2 0.63
	if *dev.EntryDiffPct < 0.6 || *dev.EntryDiffPct > 0.66 {
		t.Fatalf("entry diff pct out of range: %v", *dev.EntryDiffPct)
	}
	// aiEntry alone (no SL/TP either side) still yields a non-nil deviation.
	if d2 := buildProtectionDeviation(nil, 0, 0, 500.0, 505.0, true); d2 == nil || d2.AIEntry == nil {
		t.Fatal("deviation should be non-nil when only aiEntry present")
	}
}

func f(v float64) *float64 { return &v }

// TestPlacedProtectionDedupAndEntryWindow reproduces the 42-row bug: protection
// re-armed/re-anchored throughout the hold writes duplicate close_intents rows for
// the same tier, and later trailing re-anchors add superseded tiers. The builder
// must (1) keep only intents within the entry window and (2) collapse identical
// mechanism@price tiers, so the ENTRY plan reads as the atomic initial placement.
func TestPlacedProtectionDedupAndEntryWindow(t *testing.T) {
	entry := 523.88
	var entryMs int64 = 1_000_000
	win := entryProtectionWindowMs
	intents := []store.CloseIntent{
		// Entry-time atomic placement: 1 SL + 2 TP tiers.
		{Reason: store.MechLadderSL, TriggerPrice: 490.23, IntentTime: entryMs + 1000},
		{Reason: store.MechLadderTP, TriggerPrice: 531.38, IntentTime: entryMs + 1000},
		{Reason: store.MechLadderTP, TriggerPrice: 541.66, IntentTime: entryMs + 1000},
		// Reconciliation re-records the SAME SL tier (duplicate) — must collapse.
		{Reason: store.MechLadderSL, TriggerPrice: 490.23, IntentTime: entryMs + 2000},
		// Hours-later trailing re-arm outside the window — must be excluded.
		{Reason: store.MechFullTP, TriggerPrice: 543.98, IntentTime: entryMs + win + 60_000},
		{Reason: store.MechBreakEven, TriggerPrice: 526.49, IntentTime: entryMs + win + 90_000},
		{Reason: store.MechLadderTP, TriggerPrice: 550.37, IntentTime: entryMs + win + 90_000},
	}
	plan := buildPlacedProtectionPlan(intents, entry, true, entryMs)

	// Only the 3 entry-window distinct tiers survive (SL dup collapsed, later
	// trailing rows excluded).
	if len(plan) != 3 {
		t.Fatalf("want 3 entry-window tiers, got %d: %+v", len(plan), plan)
	}
	for _, it := range plan {
		if it.Mechanism == store.MechFullTP || it.Mechanism == store.MechBreakEven {
			t.Fatalf("trailing re-arm leaked into entry plan: %+v", it)
		}
		if it.TriggerPrice != nil && *it.TriggerPrice == 550.37 {
			t.Fatalf("out-of-window ladder tier leaked: %+v", it)
		}
	}

	// Deviation must pick the NEAREST-to-entry TP (531.38), not a farther one.
	dev := buildProtectionDeviation(plan, 517.5, 534.97, 0, entry, true)
	if dev == nil || dev.ManualTP == nil {
		t.Fatal("deviation/manual TP missing")
	}
	if *dev.ManualTP != 531.38 {
		t.Fatalf("manual first TP should be nearest-to-entry 531.38, got %v", *dev.ManualTP)
	}
}
