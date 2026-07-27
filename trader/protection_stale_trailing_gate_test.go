package trader

import "testing"

// bnEthShape 复刻 2026-07-27 线上 BN/ETHUSDT long 的真实形状:
// 止损 1 张 + 档位止盈 4 张(全部在场)+ trailing 3 张,而 armed 记录只认领 2 张。
func bnEthShape() ([]OpenOrder, *ProtectionPlan, nativeTrailingOwnership) {
	orders := []OpenOrder{
		{OrderID: "2000001313524615", Type: "STOP_MARKET", Side: "SELL", PositionSide: "LONG", Quantity: 0.134, StopPrice: 1922.39, ClientOrderID: "x-KzrpZaP9LS94035e3b45c8"},
		{OrderID: "77226623323", Type: "TAKE_PROFIT", Side: "SELL", PositionSide: "LONG", Quantity: 0.027, StopPrice: 1990.0, ClientOrderID: "x-KzrpZaP9LTa3bf8707bef9"},
		{OrderID: "77226623406", Type: "TAKE_PROFIT", Side: "SELL", PositionSide: "LONG", Quantity: 0.024, StopPrice: 2010.0, ClientOrderID: "x-KzrpZaP9LT83a57d7d6215"},
		{OrderID: "77226623509", Type: "TAKE_PROFIT", Side: "SELL", PositionSide: "LONG", Quantity: 0.020, StopPrice: 2030.0, ClientOrderID: "x-KzrpZaP9LT63f506e2a849"},
		{OrderID: "77226623608", Type: "TAKE_PROFIT", Side: "SELL", PositionSide: "LONG", Quantity: 0.016, StopPrice: 2050.0, ClientOrderID: "x-KzrpZaP9LTb3fcbce130cb"},
		{OrderID: "2000001313524663", Type: "TRAILING_STOP_MARKET", Side: "SELL", PositionSide: "LONG", Quantity: 0.134, ClientOrderID: "x-KzrpZaP9NTca0ce8958b90", ActivationStatus: "activated"},
		{OrderID: "2000001313524686", Type: "TRAILING_STOP_MARKET", Side: "SELL", PositionSide: "LONG", Quantity: 0.040, ClientOrderID: "x-KzrpZaP9NT3118ef311fdf", ActivationStatus: "activated"},
		{OrderID: "2000001313524718", Type: "TRAILING_STOP_MARKET", Side: "SELL", PositionSide: "LONG", Quantity: 0.067, ClientOrderID: "x-KzrpZaP92734975092190b2ac4588", ActivationStatus: "activated"},
	}
	plan := &ProtectionPlan{
		NeedsStopLoss: true,
		StopLossPrice: 1922.39,
		TakeProfitOrders: []ProtectionOrder{
			{Price: 1990.0, CloseRatioPct: 20},
			{Price: 2010.0, CloseRatioPct: 18},
			{Price: 2030.0, CloseRatioPct: 15},
			{Price: 2050.0, CloseRatioPct: 12},
		},
	}
	ownership := nativeTrailingOwnership{
		Armed: true,
		Claimed: map[string]struct{}{
			"2000001313524663": {},
			"2000001313524686": {},
		},
	}
	return orders, plan, ownership
}

// 撤单快路径(protection_reconciler.go 的 coverage-complete 分支)的闸门是
//
//	!missingSL && !missingTP && len(unexpectedIDs)>0 && ManualOrForeign==0 &&
//	(StaleBotDuplicate+OrphanForInactive) == (unexpectedStops+unexpectedTPs)
//
// 只测分类器不够 —— 分类对了但闸门不成立,多余单照样撤不掉。这里把闸门的
// 每一项都按线上真实形状算一遍。
func TestStaleTrailingReachesCancelFastPath(t *testing.T) {
	orders, plan, ownership := bnEthShape()

	missingSL, missingTP := detectMissingProtection(orders, "LONG", plan, true)
	if missingSL || missingTP {
		t.Fatalf("覆盖齐全时不应判缺档(missingSL=%t missingTP=%t),否则快路径进不去", missingSL, missingTP)
	}
	// 说明:这里 missingSL=false 走的是 breakEvenSatisfied 分支(plan 无 ladder SL
	// 且 breakEvenArmed=true,trailing 本身也算 looksLikeStopLoss),属设计如此,
	// 不要当成"止损单在场"的强断言。真正敏感的是 missingTP —— 少一张档位止盈
	// 就会翻真(已反向验证),那才是可能把流程踢出快路径的那一项。
	if _, tpAfterTrim := detectMissingProtection(append(append([]OpenOrder{}, orders[:4]...), orders[5:]...), "LONG", plan, true); !tpAfterTrim {
		t.Fatal("缺一张档位止盈时 missingTP 必须为真,否则本测试对 TP 覆盖没有区分力")
	}

	unexpectedStops, unexpectedTPs := detectUnexpectedProtectionOrders(orders, "LONG", plan, true, ownership)
	summary := classifyUnexpectedProtectionOrders(orders, "LONG", plan, true, ownership, true)
	ids := collectUnexpectedProtectionOrderIDs(orders, "LONG", plan, true, ownership)

	if len(ids) != 1 || ids[0] != "2000001313524718" {
		t.Fatalf("待撤列表应只含未认领的 0.067 那张,got %v", ids)
	}
	if summary.ManualOrForeign != 0 {
		t.Fatalf("ManualOrForeign>0 会直接关掉快路径,got %d(IDs=%v)", summary.ManualOrForeign, summary.ManualOrForeignIDs)
	}
	if got, want := summary.StaleBotDuplicate+summary.OrphanForInactive, unexpectedStops+unexpectedTPs; got != want {
		t.Fatalf("快路径要求可清理数等于 unexpected 总数,got %d want %d", got, want)
	}
	if summary.StaleTrailingDuplicate != 1 {
		t.Fatalf("StaleTrailingDuplicate 必须为 1,否则上游容忍分支会把它吞掉,got %d", summary.StaleTrailingDuplicate)
	}

	// 反向验证:旧布尔语义下 unexpected 数为 0,闸门根本不会被触发 ——
	// 这正是这张单能在线上长期躺着的原因。
	legacyStops, legacyTPs := detectUnexpectedProtectionOrders(orders, "LONG", plan, true, nativeTrailingArmedOnly(true))
	if legacyStops != 0 || legacyTPs != 0 {
		t.Fatalf("旧语义本应一张都不判 unexpected,got SL=%d TP=%d", legacyStops, legacyTPs)
	}
}

// 容忍分支(unexpectedStops>0 && unexpectedTPs==0 && !missingSL && StopOwner!="")
// 会把 unexpectedStops 归零。trailing 单被算进 unexpectedStops,所以必须靠
// StaleTrailingDuplicate 把它挡在这条分支之外。
func TestToleranceBranchDoesNotSwallowStaleTrailing(t *testing.T) {
	orders, plan, ownership := bnEthShape()
	unexpectedStops, unexpectedTPs := detectUnexpectedProtectionOrders(orders, "LONG", plan, true, ownership)
	summary := classifyUnexpectedProtectionOrders(orders, "LONG", plan, true, ownership, true)

	// 这就是容忍分支的原始条件(不含新增那一项)。
	legacyConditionWouldSwallow := unexpectedStops > 0 && unexpectedTPs == 0 &&
		summary.StaleBotDuplicate <= 5
	if !legacyConditionWouldSwallow {
		t.Fatal("测试构造无效:这个形状本来就不会触发容忍分支,钉不住修复")
	}

	// 加上新增的 StaleTrailingDuplicate == 0 之后必须不再成立。
	if summary.StaleTrailingDuplicate == 0 {
		t.Fatal("StaleTrailingDuplicate 为 0,容忍分支仍会吞掉这张 trailing 单")
	}
}
