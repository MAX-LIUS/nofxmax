package trader

import (
	"strings"
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

	tiers := buildPlanSnapshotTiers(plan, dd, 100, "open_long", structuralSLLevels{})

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
	tiers := buildPlanSnapshotTiers(plan, nil, 100, "open_short", structuralSLLevels{})

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
	tiers := buildPlanSnapshotTiers(plan, nil, 100, "open_long", structuralSLLevels{})
	fb, ok := findTier(tiers, store.MechFallbackSL, "")
	if !ok || fb.Kind != "sl" || fb.TriggerPct != -8 {
		t.Fatalf("fallback wrong: %+v ok=%v", fb, ok)
	}
}

// 结构位止损早先完全没落库 —— 快照里看不到,也就没参与排序。
func TestBuildPlanSnapshotTiers_IncludesStructuralLevels(t *testing.T) {
	plan := &ProtectionPlan{Mode: "combined", NeedsStopLoss: true,
		StopLossOrders: []ProtectionOrder{{Price: 95, CloseRatioPct: 100}}}
	levels := structuralSLLevels{Enabled: true, CloseConfirm: true,
		BoundaryPrice: 97, BackstopPrice: 96}

	tiers := buildPlanSnapshotTiers(plan, nil, 100, "open_long", levels)

	boundary, ok := findTier(tiers, store.MechStructuralSL, "Struct")
	if !ok || boundary.Kind != "structural" {
		t.Fatalf("structural boundary missing: %+v ok=%v", boundary, ok)
	}
	if boundary.TriggerPrice == nil || *boundary.TriggerPrice != 97 {
		t.Fatalf("boundary price wrong: %+v", boundary)
	}
	// 静态档的成交价等于触发价,必须都落库(否则排不进价格序)。
	if boundary.ExecutionPrice == nil || *boundary.ExecutionPrice != 97 {
		t.Fatalf("boundary execution price wrong: %+v", boundary)
	}
	if boundary.TriggerPct != -3 {
		t.Fatalf("boundary pct = %v, want -3", boundary.TriggerPct)
	}
	backstop, ok := findTier(tiers, store.MechStructuralSL, "Backstop")
	if !ok || backstop.TriggerPrice == nil || *backstop.TriggerPrice != 96 {
		t.Fatalf("backstop wrong: %+v ok=%v", backstop, ok)
	}
	// 未启用时不落任何结构位档。
	off := buildPlanSnapshotTiers(plan, nil, 100, "open_long", structuralSLLevels{})
	if _, ok := findTier(off, store.MechStructuralSL, ""); ok {
		t.Fatalf("structural tier emitted while disabled: %+v", off)
	}
}

// 回撤档必须带上激活价 + 成交价,并按成交价参与全表排序。
func TestBuildPlanSnapshotTiers_DrawdownCarriesExecutionPriceAndSorts(t *testing.T) {
	plan := &ProtectionPlan{
		Mode: "combined", NeedsStopLoss: true, NeedsTakeProfit: true,
		TakeProfitOrders: []ProtectionOrder{
			{Price: 102.2, CloseRatioPct: 40},
			{Price: 103.4, CloseRatioPct: 35},
		},
		StopLossOrders: []ProtectionOrder{{Price: 97, CloseRatioPct: 100}},
	}
	// 激活 +6%(=3ATR@2%),回撤 40% 的利润 => 成交价 106×(1-0.4×6/106)…
	// 用绝对口径更直观:MinProfit 6%、giveback 2.4% 利润 => 成交价 ≈ 103.6
	dd := []store.DrawdownTakeProfitRule{
		{MinProfitPct: 6, MaxDrawdownPct: 40, CloseRatioPct: 100},
	}

	tiers := buildPlanSnapshotTiers(plan, dd, 100, "open_long", structuralSLLevels{})

	dtier, ok := findTier(tiers, store.MechManagedDrawdown, "")
	if !ok {
		t.Fatalf("drawdown tier missing: %+v", tiers)
	}
	if dtier.TriggerPrice == nil || *dtier.TriggerPrice != 106 {
		t.Fatalf("drawdown activation price wrong: %+v", dtier)
	}
	if dtier.ExecutionPrice == nil || *dtier.ExecutionPrice <= 0 {
		t.Fatalf("drawdown execution price missing: %+v", dtier)
	}
	exec := *dtier.ExecutionPrice
	if exec >= 106 {
		t.Fatalf("execution price %v must sit below the 106 activation price", exec)
	}
	// 成交价 ≈ 103.6,落在 TP2(103.4)与激活价之间 —— 排序键用的是成交价,
	// 所以这一档不能被顶到列表最前(那是按激活价排的老行为)。
	// 多头降序:排序后每一项的价格必须单调不增。
	prev := -1.0
	for i, tr := range tiers {
		p := tierSortPrice(tr)
		if p <= 0 {
			continue
		}
		if prev >= 0 && p > prev {
			t.Fatalf("tiers not sorted desc at %d (%s %v > %v): %+v", i, tr.Label, p, prev, tiers)
		}
		prev = p
	}
	// giveback 保留两位小数,不再把 1.4142% 印成 "giveback 1%"。
	if !strings.Contains(dtier.Note, "giveback 40.00%") {
		t.Fatalf("drawdown note wrong: %q", dtier.Note)
	}
}

func TestBuildPlanSnapshotTiers_ZeroEntryReturnsNil(t *testing.T) {
	if tiers := buildPlanSnapshotTiers(&ProtectionPlan{}, nil, 0, "open_long", structuralSLLevels{}); tiers != nil {
		t.Fatalf("expected nil for zero entry, got %+v", tiers)
	}
}
