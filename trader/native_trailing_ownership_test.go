package trader

import "testing"

// 线上实测形状(2026-07-27 BN/ETHUSDT long):交易所躺着 3 张 trailing,
// armed 记录只认领 2 张。多出来的 0.067(半仓)止损价紧贴止损线。
// 改造前 reconciler 每轮打 dynamicOwner=3 unexpectedTP=0(三张都算"我的"),
// 于是这张永远不会被撤。
func TestUnclaimedTrailingIsStaleDuplicate(t *testing.T) {
	orders := []OpenOrder{
		{OrderID: "2000001313524663", Type: "TRAILING_STOP_MARKET", Side: "SELL", PositionSide: "LONG", Quantity: 0.134, ClientOrderID: "x-KzrpZaP9NTca0ce8958b90", ActivationStatus: "activated"},
		{OrderID: "2000001313524686", Type: "TRAILING_STOP_MARKET", Side: "SELL", PositionSide: "LONG", Quantity: 0.040, ClientOrderID: "x-KzrpZaP9NT3118ef311fdf", ActivationStatus: "activated"},
		{OrderID: "2000001313524718", Type: "TRAILING_STOP_MARKET", Side: "SELL", PositionSide: "LONG", Quantity: 0.067, ClientOrderID: "x-KzrpZaP92734975092190b2ac4588", ActivationStatus: "activated"},
	}
	ownership := nativeTrailingOwnership{
		Armed: true,
		Claimed: map[string]struct{}{
			"2000001313524663": {},
			"2000001313524686": {},
		},
	}

	summary := classifyUnexpectedProtectionOrders(orders, "LONG", nil, false, ownership, true)
	if summary.ExpectedDynamicOwner != 2 {
		t.Fatalf("认领的两张应判为 expected_dynamic_owner,got %d", summary.ExpectedDynamicOwner)
	}
	if summary.StaleTrailingDuplicate != 1 {
		t.Fatalf("未被认领的那张应判为 stale trailing duplicate,got %d", summary.StaleTrailingDuplicate)
	}
	if len(summary.StaleBotDuplicateIDs) != 1 || summary.StaleBotDuplicateIDs[0] != "2000001313524718" {
		t.Fatalf("待撤 ID 应只有 0.067 那张,got %v", summary.StaleBotDuplicateIDs)
	}
	if summary.ManualOrForeign != 0 {
		t.Fatalf("带 broker 前缀的单不能判成 manual/foreign(那样永不清理),got %d", summary.ManualOrForeign)
	}

	// 反向验证:回到旧的布尔语义,这张就会被盖章成"我的",一张都撤不掉 ——
	// 证明本测试测的是真实修复,而不是恒真断言。
	legacy := classifyUnexpectedProtectionOrders(orders, "LONG", nil, false, nativeTrailingArmedOnly(true), true)
	if legacy.StaleTrailingDuplicate != 0 || legacy.ExpectedDynamicOwner != 3 {
		t.Fatalf("旧布尔语义本应把 3 张全算预期,got expected=%d staleTrail=%d", legacy.ExpectedDynamicOwner, legacy.StaleTrailingDuplicate)
	}
}

// SOXL 形状:全平档和 30% 档各有两张(开仓价被修正导致的身份分叉)。
// 两张部分档同时触发会平掉 60% 而不是 30%,是真实的多平。
func TestForkedTierPairLeavesOnlyClaimedTrailing(t *testing.T) {
	orders := []OpenOrder{
		{OrderID: "3779671521690570752", Type: "TRAILING_STOP_MARKET", Side: "SELL", PositionSide: "LONG", Quantity: 1.42, ClientOrderID: "4c363c81edc5bcdenative_trailing"},
		{OrderID: "3779671563633610752", Type: "TRAILING_STOP_MARKET", Side: "SELL", PositionSide: "LONG", Quantity: 0.43, ClientOrderID: "4c363c81edc5bcdenative_trailing"},
		{OrderID: "3779671636513812480", Type: "TRAILING_STOP_MARKET", Side: "SELL", PositionSide: "LONG", Quantity: 1.42, ClientOrderID: "4c363c81edc5bcdenative_trailing"},
		{OrderID: "3779671668927393792", Type: "TRAILING_STOP_MARKET", Side: "SELL", PositionSide: "LONG", Quantity: 0.43, ClientOrderID: "4c363c81edc5bcdenative_trailing"},
	}
	ownership := nativeTrailingOwnership{
		Armed: true,
		Claimed: map[string]struct{}{
			"3779671521690570752": {},
			"3779671563633610752": {},
		},
	}
	ids := collectUnexpectedProtectionOrderIDs(orders, "LONG", nil, false, ownership)
	if len(ids) != 2 {
		t.Fatalf("应撤掉未认领的那一对,got %v", ids)
	}
	for _, id := range ids {
		if id == "3779671521690570752" || id == "3779671563633610752" {
			t.Fatalf("认领中的单绝不能进待撤列表:%s", id)
		}
	}
}

// 安全边界:认领视图缺失或为空时必须容忍在场的 trailing 单。
// 撤掉一张在场的保护单,后果远重于多留一张。
func TestMissingClaimViewToleratesLiveTrailing(t *testing.T) {
	orders := []OpenOrder{
		{OrderID: "111", Type: "TRAILING_STOP_MARKET", Side: "SELL", PositionSide: "LONG", Quantity: 1, ClientOrderID: "native_trailing"},
	}
	cases := map[string]nativeTrailingOwnership{
		"nil 认领集合(调用方给不出视图)": {Armed: true, Claimed: nil},
		"空认领集合(记录还没落盘)":      {Armed: true, Claimed: map[string]struct{}{}},
	}
	for name, ownership := range cases {
		summary := classifyUnexpectedProtectionOrders(orders, "LONG", nil, false, ownership, true)
		if summary.StaleTrailingDuplicate != 0 || summary.ExpectedDynamicOwner != 1 {
			t.Fatalf("%s:应容忍在场单,got expected=%d staleTrail=%d", name, summary.ExpectedDynamicOwner, summary.StaleTrailingDuplicate)
		}
	}

	// 没武装时行为不变:仍按 bot 标记走 stale/manual 分流。
	summary := classifyUnexpectedProtectionOrders(orders, "LONG", nil, false,
		nativeTrailingOwnership{Armed: false, Claimed: map[string]struct{}{"111": {}}}, true)
	if summary.StaleBotDuplicate != 1 {
		t.Fatalf("未武装时带 bot 标记的 trailing 应仍判 stale,got %d", summary.StaleBotDuplicate)
	}
}

// 交易所不回 order ID 时无法比对,只能容忍 —— 否则会把一张认不出身份的
// 在场保护单当垃圾撤掉。
func TestEmptyOrderIDIsTolerated(t *testing.T) {
	ownership := nativeTrailingOwnership{Armed: true, Claimed: map[string]struct{}{"other": {}}}
	if !ownership.classifyTrailing("") {
		t.Fatal("空 order ID 必须容忍")
	}
	if ownership.classifyTrailing("unknown") {
		t.Fatal("有认领视图且 ID 不在其中时,必须判为非我所有")
	}
}

// 归属判定(profitOwner)只看武装状态,不看认领集合:记录落盘窗口期不能
// 瞬间判成 missingProfit 而触发一轮无谓重挂。
func TestOwnerJudgementUsesArmedNotClaimSet(t *testing.T) {
	orders := []OpenOrder{
		{OrderID: "999", Type: "TRAILING_STOP_MARKET", Side: "SELL", PositionSide: "LONG", Quantity: 1, ClientOrderID: "native_trailing"},
	}
	state := evaluateProtectionOwnership(orders, "LONG", nil, false,
		nativeTrailingOwnership{Armed: true, Claimed: map[string]struct{}{}})
	if state.ProfitOwner != "drawdown" {
		t.Fatalf("武装即有 profit owner(与认领集合无关),got %q", state.ProfitOwner)
	}
}
