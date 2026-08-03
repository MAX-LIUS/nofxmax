package trader

import "testing"

// 核心保证(用户 2026-07-29「不应该重挂,应该始终保持存在」):价位仍在当前计划里的
// 委托,无论分类器怎么判,都不许进撤单集合。
func TestFilterCancelIDsAgainstPlanSparesLivePlanTier(t *testing.T) {
	plan := &ProtectionPlan{
		TakeProfitOrders: []ProtectionOrder{{Price: 488.8636, CloseRatioPct: 20}},
		StopLossOrders:   []ProtectionOrder{{Price: 471.848, CloseRatioPct: 100}},
		NeedsTakeProfit:  true,
		NeedsStopLoss:    true,
	}
	openOrders := []OpenOrder{
		{OrderID: "tp1", Type: "TAKE_PROFIT", Price: 488.8636},
		{OrderID: "debris", Type: "STOP_MARKET", StopPrice: 400.0},
	}
	keep, spared := filterCancelIDsAgainstPlan([]string{"tp1_tp", "debris_sl"}, openOrders, plan)
	if len(spared) != 1 || spared[0] != "tp1_tp" {
		t.Fatalf("live plan tier not spared: keep=%v spared=%v", keep, spared)
	}
	if len(keep) != 1 || keep[0] != "debris_sl" {
		t.Fatalf("genuine debris was spared: keep=%v", keep)
	}
}

// 容忍名单(锚点掉档的 TP / 守卫自持的止损)同样算计划内 —— 撤掉它们会让"已成交"的
// 推断变成事实,计划每轮震荡。
func TestFilterCancelIDsAgainstPlanSparesToleranceList(t *testing.T) {
	plan := &ProtectionPlan{
		AllowedExtraTakeProfitPrices: []float64{500.0323},
		AllowedExtraStopPrices:       []float64{460.0},
	}
	openOrders := []OpenOrder{
		{OrderID: "dropped_tp", Type: "TAKE_PROFIT", Price: 500.0323},
		{OrderID: "guard_stop", Type: "STOP_MARKET", StopPrice: 460.0},
	}
	keep, spared := filterCancelIDsAgainstPlan([]string{"dropped_tp", "guard_stop"}, openOrders, plan)
	if len(keep) != 0 {
		t.Fatalf("tolerated prices were queued for cancel: %v", keep)
	}
	if len(spared) != 2 {
		t.Fatalf("expected both tolerated orders spared, got %v", spared)
	}
}

// 查不到价位就不豁免:闸门只在有确证时拦,否则一个失效的 openOrders 快照会让所有
// 清理停摆,真正的垃圾单永久堆积。
func TestFilterCancelIDsAgainstPlanDoesNotSpareUnknownOrders(t *testing.T) {
	plan := &ProtectionPlan{TakeProfitOrders: []ProtectionOrder{{Price: 100}}}
	keep, spared := filterCancelIDsAgainstPlan([]string{"ghost"}, nil, plan)
	if len(keep) != 1 || len(spared) != 0 {
		t.Fatalf("unknown order must stay cancellable: keep=%v spared=%v", keep, spared)
	}
}

// 空计划不得让闸门吞掉清理 —— 无计划时不存在"计划内的价位"。
func TestFilterCancelIDsAgainstPlanNoPlanCancelsAll(t *testing.T) {
	ids := []string{"a", "b"}
	keep, spared := filterCancelIDsAgainstPlan(ids, []OpenOrder{{OrderID: "a", StopPrice: 1}}, nil)
	if len(keep) != 2 || len(spared) != 0 {
		t.Fatalf("nil plan must not spare anything: keep=%v spared=%v", keep, spared)
	}
}

func TestSubtractOrderIDsIgnoresSuffix(t *testing.T) {
	got := subtractOrderIDs([]string{"tp1", "x9"}, []string{"tp1_tp"})
	if len(got) != 1 || got[0] != "x9" {
		t.Fatalf("suffix-insensitive subtraction failed: %v", got)
	}
}

// 同价位重复单必须仍然可撤:档位已由另一张存活单覆盖,撤掉多余那张不产生真空。
// 这是 coverage-complete 分支原本要解决的场景(TP 成交缩仓后遗留的整仓数量止损与
// 新挂的余量止损同价),闸门不能把它变成永久驻留 —— 否则每轮重复告警且永不收敛。
func TestFilterCancelIDsAgainstPlanCancelsDuplicateWhenTierStillCovered(t *testing.T) {
	plan := &ProtectionPlan{
		StopLossOrders: []ProtectionOrder{{Price: 471.848, CloseRatioPct: 100}},
		NeedsStopLoss:  true,
	}
	openOrders := []OpenOrder{
		{OrderID: "keeper", Type: "STOP_MARKET", StopPrice: 471.848, Quantity: 0.29},
		{OrderID: "stale_dup", Type: "STOP_MARKET", StopPrice: 471.848, Quantity: 0.36},
	}
	// 只有 stale_dup 进撤单集合 → keeper 存活并覆盖该档 → 不必救 stale_dup。
	keep, spared := filterCancelIDsAgainstPlan([]string{"stale_dup"}, openOrders, plan)
	if len(spared) != 0 {
		t.Fatalf("duplicate spared although the tier is still covered: spared=%v", spared)
	}
	if len(keep) != 1 || keep[0] != "stale_dup" {
		t.Fatalf("duplicate should stay cancellable: keep=%v", keep)
	}
}

// 两张同价位单**都**被判多余时,必须恰好救回一张:档位保持在场,重复的那张仍被清掉。
// 一次只救一张是关键 —— 全救等于不收敛,全撤等于真空。
func TestFilterCancelIDsAgainstPlanRescuesExactlyOneWhenAllCandidatesCollide(t *testing.T) {
	plan := &ProtectionPlan{
		TakeProfitOrders: []ProtectionOrder{{Price: 488.8636, CloseRatioPct: 20}},
		NeedsTakeProfit:  true,
	}
	openOrders := []OpenOrder{
		{OrderID: "tpA", Type: "TAKE_PROFIT", Price: 488.8636},
		{OrderID: "tpB", Type: "TAKE_PROFIT", Price: 488.8636},
	}
	keep, spared := filterCancelIDsAgainstPlan([]string{"tpA", "tpB"}, openOrders, plan)
	if len(spared) != 1 {
		t.Fatalf("expected exactly one rescue, got spared=%v", spared)
	}
	if len(keep) != 1 {
		t.Fatalf("expected exactly one cancel, got keep=%v", keep)
	}
	if spared[0] == keep[0] {
		t.Fatalf("same id both spared and cancelled: %v / %v", spared, keep)
	}
}

// 多档场景:两个不同价位的档位各自只有一张单,两张都被判多余 → 两张都要救回。
func TestFilterCancelIDsAgainstPlanRescuesEachUncoveredTier(t *testing.T) {
	plan := &ProtectionPlan{
		TakeProfitOrders: []ProtectionOrder{
			{Price: 488.8636, CloseRatioPct: 20},
			{Price: 491.9796, CloseRatioPct: 15},
		},
		NeedsTakeProfit: true,
	}
	openOrders := []OpenOrder{
		{OrderID: "tp1", Type: "TAKE_PROFIT", Price: 488.8636},
		{OrderID: "tp2", Type: "TAKE_PROFIT", Price: 491.9796},
	}
	keep, spared := filterCancelIDsAgainstPlan([]string{"tp1", "tp2"}, openOrders, plan)
	if len(keep) != 0 {
		t.Fatalf("both tiers are uncovered; nothing may be cancelled: keep=%v", keep)
	}
	if len(spared) != 2 {
		t.Fatalf("expected both tiers rescued, got %v", spared)
	}
}

// 门禁滤掉的档位若在交易所还挂着,必须转为容忍 —— 这是 ZECUSDT TP1「撤了且永不重挂」
// 的直接机制:档位从计划里消失(missing 检测看不到→不重挂)+ 不在 allowed 里
// (分类器判 stale duplicate→撤掉),两者合起来就是永久真空。
func TestRegisterDroppedTiersAsToleratedKeepsLiveOrder(t *testing.T) {
	original := &ProtectionPlan{
		TakeProfitOrders: []ProtectionOrder{
			{Price: 488.8636, CloseRatioPct: 20},
			{Price: 491.9796, CloseRatioPct: 15},
		},
		StopLossOrders: []ProtectionOrder{{Price: 471.848, CloseRatioPct: 100}},
	}
	// 门禁滤掉了 TP1 和那档止损。
	narrowed := &ProtectionPlan{
		TakeProfitOrders: []ProtectionOrder{{Price: 491.9796, CloseRatioPct: 15}},
	}
	openOrders := []OpenOrder{
		{OrderID: "tp1", Type: "TAKE_PROFIT", Price: 488.8636, PositionSide: "LONG"},
		{OrderID: "tp2", Type: "TAKE_PROFIT", Price: 491.9796, PositionSide: "LONG"},
		{OrderID: "sl", Type: "STOP_MARKET", StopPrice: 471.848, PositionSide: "LONG"},
	}
	registerDroppedTiersAsTolerated(original, narrowed, openOrders, "LONG")

	if len(narrowed.AllowedExtraTakeProfitPrices) != 1 || narrowed.AllowedExtraTakeProfitPrices[0] != 488.8636 {
		t.Fatalf("dropped-but-live TP not tolerated: %v", narrowed.AllowedExtraTakeProfitPrices)
	}
	if len(narrowed.AllowedExtraStopPrices) != 1 || narrowed.AllowedExtraStopPrices[0] != 471.848 {
		t.Fatalf("dropped-but-live stop not tolerated: %v", narrowed.AllowedExtraStopPrices)
	}
	// 容忍后分类器必须不再把它判成多余。
	summary := classifyUnexpectedProtectionOrders(openOrders, "LONG", narrowed, breakEvenOwnership{}, nativeTrailingOwnership{}, true)
	if summary.StaleBotDuplicate != 0 {
		t.Fatalf("tolerated order still classified stale: %+v", summary)
	}
}

// 档位被滤掉但交易所上**没有**这张单时不得登记 —— 容忍名单不是给幽灵价位开的后门,
// 否则将来任何恰好落在这个价位的陌生单都会被无条件放过。
func TestRegisterDroppedTiersIgnoresAbsentOrders(t *testing.T) {
	original := &ProtectionPlan{TakeProfitOrders: []ProtectionOrder{{Price: 488.8636, CloseRatioPct: 20}}}
	narrowed := &ProtectionPlan{}
	registerDroppedTiersAsTolerated(original, narrowed, nil, "LONG")
	if len(narrowed.AllowedExtraTakeProfitPrices) != 0 {
		t.Fatalf("absent order was tolerated: %v", narrowed.AllowedExtraTakeProfitPrices)
	}
}

// 档位没被滤掉时不重复登记(否则容忍名单会随轮次无限增长)。
func TestRegisterDroppedTiersNoOpWhenNothingDropped(t *testing.T) {
	tiers := []ProtectionOrder{{Price: 100, CloseRatioPct: 50}}
	original := &ProtectionPlan{TakeProfitOrders: tiers}
	narrowed := &ProtectionPlan{TakeProfitOrders: tiers}
	openOrders := []OpenOrder{{OrderID: "tp", Type: "TAKE_PROFIT", Price: 100}}
	registerDroppedTiersAsTolerated(original, narrowed, openOrders, "")
	if len(narrowed.AllowedExtraTakeProfitPrices) != 0 {
		t.Fatalf("no tier was dropped; nothing should be tolerated: %v", narrowed.AllowedExtraTakeProfitPrices)
	}
}

// trailing 单不得被当成 TP/SL 的在场证据:它有独立的归属账本,把它当证据会让一个
// 早已不该存在的静态档位凭空获得容忍。
func TestRegisterDroppedTiersIgnoresTrailingOrders(t *testing.T) {
	original := &ProtectionPlan{TakeProfitOrders: []ProtectionOrder{{Price: 500.0323, CloseRatioPct: 100}}}
	narrowed := &ProtectionPlan{}
	openOrders := []OpenOrder{
		{OrderID: "trail", Type: "TRAILING_STOP_MARKET", StopPrice: 500.0323, PositionSide: "LONG"},
	}
	registerDroppedTiersAsTolerated(original, narrowed, openOrders, "LONG")
	if len(narrowed.AllowedExtraTakeProfitPrices) != 0 {
		t.Fatalf("trailing order used as evidence for a static tier: %v", narrowed.AllowedExtraTakeProfitPrices)
	}
}
