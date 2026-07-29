package trader

import (
	"path/filepath"
	"testing"
	"time"

	"nofx/store"
)

// ============================================================================
// RC-4 活单被自己撤掉:归属账本翻转 → 开仓时挂好的 DD 单落进 stale-duplicate 名单 → 被撤。
//
// 2026-07-28 生产实况 (GPT / SKHYNIXUSDT short):
//   09:03:14 开仓,dd1 正确挂上 algoId=3781523903370133504
//            (activation=1048.5615 callback=0.0233 qty=0.0350)
//   19:13:46 熔断跳闸,归属从 native_trailing 翻成 managed_drawdown
//   19:14:00 跳闸后 14 秒,reconciler 的"覆盖已完整,撤掉多余重复单"快速路径把这张
//            **仍然生效**的 dd1 一起撤了 —— 唯一理由是它已不在 Claimed 集合里。
//
// 用户要求:"这个是开仓就挂好的,怎么能随便动?只要是没有丢失,就应该一直在。"
// 归属记录翻转是我们自己的账本变动,不是"这张单失效了"。账本可以重写,交易所上
// 那张正在保护仓位的单不能因此被撤。
// ============================================================================

func reclaimFixture(t *testing.T, symbol, side string, rules []store.DrawdownTakeProfitRule) *AutoTrader {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "reclaim.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	at := &AutoTrader{
		id:         "trader-rc",
		exchangeID: "exch-rc",
		store:      st,
		exchange:   "okx",
		config: AutoTraderConfig{StrategyConfig: &store.StrategyConfig{
			Protection: store.ProtectionConfig{
				DrawdownTakeProfit: store.DrawdownTakeProfitConfig{
					Enabled: true, Mode: store.ProtectionModeManual, Rules: rules,
				},
			},
		}},
		protectionState:       make(map[string]string),
		nativeTrailingArmTime: make(map[string]time.Time),
		drawdownSource:        make(map[string]string),
		reArmFailCache:        make(map[string]int),
		reArmTripTime:         make(map[string]time.Time),
		drawdownTierAllocs:    make(map[string][]store.DrawdownTierAllocation),
	}
	return at
}

// 单档全平规则,百分比单位(不走 ATR 解析,便于精确算激活价)。
func plainFullRule() store.DrawdownTakeProfitRule {
	return store.DrawdownTakeProfitRule{
		MinProfitPct:   5.8175,
		MaxDrawdownPct: 2.327,
		CloseRatioPct:  100,
		StageName:      "dd1",
	}
}

// 决定性回归:一张仍然匹配某档 DD 形状的活 trailing 单,必须从撤单名单里被剔除。
func TestReclaim_LiveMatchingOrderIsNotCancelled(t *testing.T) {
	const (
		symbol = "SKHYNIXUSDT"
		side   = "short"
		entry  = 1113.0
	)
	rule := plainFullRule()
	at := reclaimFixture(t, symbol, side, []store.DrawdownTakeProfitRule{rule})

	// 按该档规则算出的计划激活价 —— 交易所上那张单就是照这个挂的。
	activation := calculateProfitBasedTrailingTriggerPrice(entry, side, rule.MinProfitPct)
	callback := calculateDrawdownRuleCallbackRatio(entry, side, rule)
	const orderID = "3781523903370133504"

	orders := []OpenOrder{{
		OrderID:      orderID,
		PositionSide: "SHORT",
		Type:         "TRAILING_STOP_MARKET",
		StopPrice:    activation,
		CallbackRate: callback,
		Quantity:     0.035,
	}}

	// 归属翻转后,这张单进了"多余待撤"名单。
	kept, matches := at.reclaimLiveDrawdownTrailingOrders(symbol, side, entry, []string{orderID}, orders)

	if len(matches) != 1 {
		t.Fatalf("匹配 DD 档形状的活单必须被认领回来,实得 %d 个匹配", len(matches))
	}
	for _, id := range kept {
		if id == orderID {
			t.Fatal("这张单仍在撤单名单里 —— 正是 2026-07-28 撤掉 SKHYNIX dd1 的那条路径")
		}
	}
	if matches[0].OrderID != orderID {
		t.Fatalf("认领的应是这张单,实得 %q", matches[0].OrderID)
	}
	// 认领必须落成账本记录,否则下一轮又会被当成无人认领而再次进入撤单名单。
	if got := at.storedTrailingOrderIDForRule(symbol, side, entry, rule); got != orderID {
		t.Fatalf("认领后账本应记住该 orderID(否则下一轮重新被判多余),实得 %q", got)
	}
}

// 已激活的单参数必然对不上(交易所报的 StopPrice 是移动中的跟踪价,Binance 完全不报
// callback)。这种单绝不能因为"参数失配"被撤 —— 它正是这一档的活保护。
func TestReclaim_ActivatedOrderIsReclaimedDespiteParamDrift(t *testing.T) {
	const (
		symbol = "SKHYNIXUSDT"
		side   = "short"
		entry  = 1113.0
	)
	at := reclaimFixture(t, symbol, side, []store.DrawdownTakeProfitRule{plainFullRule()})

	orders := []OpenOrder{{
		OrderID:          "activated-1",
		PositionSide:     "SHORT",
		Type:             "TRAILING_STOP_MARKET",
		ActivationStatus: "activated",
		StopPrice:        999.0, // 移动中的跟踪价,与计划激活价相差极大
		CallbackRate:     0,     // Binance 不报
		Quantity:         0.035,
	}}

	kept, matches := at.reclaimLiveDrawdownTrailingOrders(symbol, side, entry, []string{"activated-1"}, orders)

	if len(matches) != 1 || len(kept) != 0 {
		t.Fatalf("已激活的 trailing 单必须被认领而不是被撤,实得 matches=%d kept=%d", len(matches), len(kept))
	}
}

// 反向锁死:形状对不上任何一档的 trailing 单必须留在撤单名单里。否则"拒绝撤单"会
// 退化成"什么都不撤",真正的重复单/残留单会堆积,部分档重复触发会多平仓。
func TestReclaim_NonMatchingOrderStaysCancellable(t *testing.T) {
	const (
		symbol = "SKHYNIXUSDT"
		side   = "short"
		entry  = 1113.0
	)
	at := reclaimFixture(t, symbol, side, []store.DrawdownTakeProfitRule{plainFullRule()})

	orders := []OpenOrder{{
		OrderID:      "foreign-1",
		PositionSide: "SHORT",
		Type:         "TRAILING_STOP_MARKET",
		StopPrice:    500.0, // 与任何一档的计划激活价都差远了
		CallbackRate: 0.09,  // 也不匹配
		Quantity:     0.035,
	}}

	kept, matches := at.reclaimLiveDrawdownTrailingOrders(symbol, side, entry, []string{"foreign-1"}, orders)

	if len(matches) != 0 {
		t.Fatalf("形状不匹配的单不得被认领(否则真残留单永不清理→部分档重复触发多平仓),实得 %d", len(matches))
	}
	if len(kept) != 1 || kept[0] != "foreign-1" {
		t.Fatalf("不匹配的单必须留在撤单名单,实得 %v", kept)
	}
}

// 非 TRAILING 单不在本文件职责内(静态 SL/TP 有自己的等价性逻辑),必须原样交回。
func TestReclaim_StaticOrdersAreLeftAlone(t *testing.T) {
	const (
		symbol = "SKHYNIXUSDT"
		side   = "short"
		entry  = 1113.0
	)
	at := reclaimFixture(t, symbol, side, []store.DrawdownTakeProfitRule{plainFullRule()})

	orders := []OpenOrder{{
		OrderID:      "static-sl",
		PositionSide: "SHORT",
		Type:         "STOP_MARKET",
		StopPrice:    1150.0,
		Quantity:     0.035,
	}}

	kept, matches := at.reclaimLiveDrawdownTrailingOrders(symbol, side, entry, []string{"static-sl"}, orders)

	if len(matches) != 0 {
		t.Fatalf("静态单不属于本路径,不得被认领,实得 %d", len(matches))
	}
	if len(kept) != 1 {
		t.Fatalf("静态单必须原样交回,实得 %v", kept)
	}
}

// 一张单最多认领给一档:两档并存(place-at-open)时,同一张单不能同时算作两档的覆盖,
// 否则另一档看起来"已覆盖"而永远不被补挂 —— 又变成交易所少一档保护。
func TestReclaim_OneOrderClaimsAtMostOneTier(t *testing.T) {
	const (
		symbol = "SKHYNIXUSDT"
		side   = "short"
		entry  = 1113.0
	)
	// 两档:全平档 + 部分档。
	rules := []store.DrawdownTakeProfitRule{
		{MinProfitPct: 5.8175, MaxDrawdownPct: 2.327, CloseRatioPct: 100, StageName: "dd1"},
		{MinProfitPct: 8.0, MaxDrawdownPct: 1.5, CloseRatioPct: 30, StageName: "dd2"},
	}
	at := reclaimFixture(t, symbol, side, rules)

	// 两张都已激活 → 都会匹配,但必须各认一档。
	orders := []OpenOrder{
		{OrderID: "a1", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET", ActivationStatus: "activated", Quantity: 0.035},
		{OrderID: "a2", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET", ActivationStatus: "activated", Quantity: 0.010},
	}

	_, matches := at.reclaimLiveDrawdownTrailingOrders(symbol, side, entry, []string{"a1", "a2"}, orders)

	if len(matches) != 2 {
		t.Fatalf("两张活单应各认一档,实得 %d", len(matches))
	}
	if matches[0].Rule.MinProfitPct == matches[1].Rule.MinProfitPct {
		t.Fatalf("两张单不得认领同一档(否则另一档被判已覆盖而永不补挂),实得 %.4f / %.4f",
			matches[0].Rule.MinProfitPct, matches[1].Rule.MinProfitPct)
	}
}

// 认领必须清零该档熔断计数:交易所侧确实有活单在保护它,继续压着重挂会让下一次
// 真正丢单时无人补挂。
func TestReclaim_ResetsReArmBreakerForClaimedTier(t *testing.T) {
	const (
		symbol = "SKHYNIXUSDT"
		side   = "short"
		entry  = 1113.0
	)
	rule := plainFullRule()
	at := reclaimFixture(t, symbol, side, []store.DrawdownTakeProfitRule{rule})

	key := reArmFailKey(symbol, side, normalizeDrawdownRule(rule), entry)
	for i := 0; i < reArmBreakerLimit; i++ {
		at.bumpReArmFail(key)
	}
	if at.getReArmFail(key) != reArmBreakerLimit {
		t.Fatalf("前置条件:计数应为 %d", reArmBreakerLimit)
	}

	orders := []OpenOrder{{
		OrderID: "live-1", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET",
		ActivationStatus: "activated", Quantity: 0.035,
	}}
	at.reclaimLiveDrawdownTrailingOrders(symbol, side, entry, []string{"live-1"}, orders)

	if got := at.getReArmFail(key); got != 0 {
		t.Fatalf("认领回活单后必须清零熔断计数,实得 %d", got)
	}
}

// 破坏性:DD 配置未启用(无规则)时必须原样交回名单,不得吞掉待撤单。
func TestReclaim_NoRulesReturnsCandidatesUnchanged(t *testing.T) {
	at := reclaimFixture(t, "WLDUSDT", "short", nil)
	orders := []OpenOrder{{OrderID: "x", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET", Quantity: 1}}

	kept, matches := at.reclaimLiveDrawdownTrailingOrders("WLDUSDT", "short", 100, []string{"x"}, orders)

	if len(matches) != 0 || len(kept) != 1 {
		t.Fatalf("无 DD 规则时应原样交回,实得 kept=%v matches=%d", kept, len(matches))
	}
}

// 待撤 ID 不在活单列表里(已成交/已撤):没有实物需要保护,原样交回。
func TestReclaim_UnknownOrderIDPassesThrough(t *testing.T) {
	at := reclaimFixture(t, "SKHYNIXUSDT", "short", []store.DrawdownTakeProfitRule{plainFullRule()})

	kept, matches := at.reclaimLiveDrawdownTrailingOrders("SKHYNIXUSDT", "short", 1113.0, []string{"gone"}, nil)

	if len(matches) != 0 {
		t.Fatalf("不存在的单不得被认领,实得 %d", len(matches))
	}
	if len(kept) != 1 || kept[0] != "gone" {
		t.Fatalf("不存在的单应原样交回,实得 %v", kept)
	}
}

// 方向不符的 trailing 单(双向持仓模式下的另一侧)不得被本仓位认领。
func TestReclaim_WrongSideIsNotClaimed(t *testing.T) {
	at := reclaimFixture(t, "SKHYNIXUSDT", "short", []store.DrawdownTakeProfitRule{plainFullRule()})

	orders := []OpenOrder{{
		OrderID: "long-side", PositionSide: "LONG", Type: "TRAILING_STOP_MARKET",
		ActivationStatus: "activated", Quantity: 0.035,
	}}

	kept, matches := at.reclaimLiveDrawdownTrailingOrders("SKHYNIXUSDT", "short", 1113.0, []string{"long-side"}, orders)

	if len(matches) != 0 {
		t.Fatalf("另一侧的单不得被本仓位认领,实得 %d", len(matches))
	}
	if len(kept) != 1 {
		t.Fatalf("应原样交回,实得 %v", kept)
	}
}
