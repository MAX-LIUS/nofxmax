package trader

import (
	"testing"

	"nofx/store"
)

func findTier(tiers []store.ProtectionPlanTier, mech, label string) (store.ProtectionPlanTier, bool) {
	for _, t := range tiers {
		if t.Mechanism == mech && (label == "" || t.Label == label) {
			return t, true
		}
	}
	return store.ProtectionPlanTier{}, false
}

func TestBuildPlanSnapshotTiers_LongLadderAndDrawdown(t *testing.T) {
	plan := &ProtectionPlan{
		Mode:            "combined",
		NeedsStopLoss:   true,
		NeedsTakeProfit: true,
		TakeProfitOrders: []ProtectionOrder{
			{Price: 110, CloseRatioPct: 50},
			{Price: 120, CloseRatioPct: 50},
		},
		StopLossOrders: []ProtectionOrder{
			{Price: 95, CloseRatioPct: 100},
		},
		BreakEvenConfig: &store.BreakEvenStopConfig{
			Enabled: true, TriggerValue: 3, OffsetPct: 0.2,
		},
	}
	dd := []store.DrawdownTakeProfitRule{
		{MinProfitPct: 5, MaxDrawdownPct: 40, CloseRatioPct: 100},
	}

	tiers := buildPlanSnapshotTiers(plan, dd, 100, "open_long")

	// Two ladder TP tiers, labeled TP1/TP2, signed pct positive for long.
	tp1, ok := findTier(tiers, store.MechLadderTP, "TP1")
	if !ok || tp1.Kind != "tp" || tp1.TriggerPct != 10 {
		t.Fatalf("TP1 wrong: %+v ok=%v", tp1, ok)
	}
	if tp1.TriggerPrice == nil || *tp1.TriggerPrice != 110 {
		t.Fatalf("TP1 price wrong: %+v", tp1)
	}
	tp2, ok := findTier(tiers, store.MechLadderTP, "TP2")
	if !ok || tp2.TriggerPct != 20 {
		t.Fatalf("TP2 wrong: %+v ok=%v", tp2, ok)
	}
	// Single ladder SL labeled "SL", pct is loss-side => negative.
	sl, ok := findTier(tiers, store.MechLadderSL, "SL")
	if !ok || sl.Kind != "sl" || sl.TriggerPct != -5 {
		t.Fatalf("SL wrong: %+v ok=%v", sl, ok)
	}
	// Break-even tier present.
	if _, ok := findTier(tiers, store.MechBreakEven, ""); !ok {
		t.Fatalf("expected break-even tier, tiers=%+v", tiers)
	}
	// Drawdown tier present with close ratio.
	dtier, ok := findTier(tiers, store.MechManagedDrawdown, "")
	if !ok || dtier.Kind != "drawdown" || dtier.TriggerPct != 5 {
		t.Fatalf("drawdown tier wrong: %+v ok=%v", dtier, ok)
	}
}

func TestBuildPlanSnapshotTiers_ShortFullAndFallback(t *testing.T) {
	// Short: profit is below entry => positive pct for TP, negative for SL.
	plan := &ProtectionPlan{
		Mode:            "full",
		NeedsStopLoss:   true,
		NeedsTakeProfit: true,
		TakeProfitPrice: 90,  // -10% raw, long-signed; short => +10
		StopLossPrice:   105, // +5% raw; short => -5
	}
	tiers := buildPlanSnapshotTiers(plan, nil, 100, "open_short")

	tp, ok := findTier(tiers, store.MechFullTP, "")
	if !ok || tp.TriggerPct != 10 {
		t.Fatalf("full TP wrong: %+v ok=%v", tp, ok)
	}
	sl, ok := findTier(tiers, store.MechFullSL, "")
	if !ok || sl.TriggerPct != -5 {
		t.Fatalf("full SL wrong: %+v ok=%v", sl, ok)
	}
}

func TestBuildPlanSnapshotTiers_FallbackOnlyWhenNoLadderNoSL(t *testing.T) {
	plan := &ProtectionPlan{
		Mode:                 "fallback_max_loss",
		NeedsStopLoss:        false,
		FallbackMaxLossPrice: 92,
	}
	tiers := buildPlanSnapshotTiers(plan, nil, 100, "open_long")
	fb, ok := findTier(tiers, store.MechFallbackSL, "")
	if !ok || fb.Kind != "sl" || fb.TriggerPct != -8 {
		t.Fatalf("fallback wrong: %+v ok=%v", fb, ok)
	}
}

func TestBuildPlanSnapshotTiers_ZeroEntryReturnsNil(t *testing.T) {
	if tiers := buildPlanSnapshotTiers(&ProtectionPlan{}, nil, 0, "open_long"); tiers != nil {
		t.Fatalf("expected nil for zero entry, got %+v", tiers)
	}
}
