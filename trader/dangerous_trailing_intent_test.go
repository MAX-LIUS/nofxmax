package trader

import (
	"path/filepath"
	"testing"
	"time"

	"nofx/store"
	tradertypes "nofx/trader/types"
)

// 这两组测试钉住 2026-07-28 生产实况 CLUSDT short 暴露的两个缺陷:
//
//  1. 门槛抖动自挂自撤:挂单那刻 pnl=3.29% > floor=3.1309% → 故意挂 activePx=0;
//     之后 pnl 漂到 3.12/3.13% → 同一张单被判 DANGEROUS 撤掉 → 重挂 → 再撤
//     (4 次 🔴)。两侧同一阈值、不同时刻、无滞回带。
//  2. 熔断被绕过:close=100% 档 14:44:31 跳闸("stop re-placing exchange trailing"),
//     之后 14:46/14:49/15:04/15:10 仍被 reconciler 一路重挂 —— 5 个
//     applyNativeTrailingDrawdown 调用点里只有 1 个查熔断。

func immediateTrailStore(t *testing.T, name string) *store.Store {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	return st
}

func clTrailingOrder(id string) tradertypes.OpenOrder {
	// 无激活价、已激活的立即跟踪单 —— 生产上 CLUSDT 那张 dd1 单的形状。
	return tradertypes.OpenOrder{
		OrderID: id, Symbol: "CLUSDT", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET",
		Quantity: 1.2, StopPrice: 82.45, ActivationPrice: 0, CallbackRate: 0.018786,
		ActivationStatus: "activated", Status: "NEW",
	}
}

func clImmediateTrailTrader(t *testing.T, dbName string, orders []tradertypes.OpenOrder) *AutoTrader {
	t.Helper()
	fake := &fakeProtectionTrader{
		positions: []map[string]interface{}{{
			"symbol":      "CLUSDT",
			"side":        "short",
			"entryPrice":  83.68,
			"markPrice":   80.93,
			"positionAmt": 1.2,
		}},
		openOrders: orders,
	}
	return &AutoTrader{
		id:                    "trader-cl",
		exchangeID:            "exchange-1",
		store:                 immediateTrailStore(t, dbName),
		exchange:              "okx",
		trader:                fake,
		config:                AutoTraderConfig{StrategyConfig: &store.StrategyConfig{}},
		protectionState:       make(map[string]string),
		reArmFailCache:        make(map[string]int),
		nativeTrailingArmTime: make(map[string]time.Time),
	}
}

// 生产实况的档位:floor=3.1309%,挂单时 pnl=3.29%,之后漂到 3.12%。
func clDD1Rule() store.DrawdownTakeProfitRule {
	return store.DrawdownTakeProfitRule{MinProfitPct: 3.1309, MaxDrawdownPct: 1.8786, CloseRatioPct: 100}
}

// A1. 我们自己挂的无激活价单,即使当前 pnl 掉到 floor 之下,也不能被撤 ——
// 锚点在下单那刻已固定在盈利处,只会朝有利方向棘轮。
func TestDeliberateImmediateTrailSurvivesPnLDippingBelowFloor(t *testing.T) {
	at := clImmediateTrailTrader(t, "cl-deliberate.db", []tradertypes.OpenOrder{clTrailingOrder("algo-cl-1")})
	rule := clDD1Rule()

	// 记录"我们故意挂了一张 activePx=0 的单"(生产上由 arm 路径写入)。
	at.persistDynamicProtectionRecordWithDetails("CLUSDT", "short", "native_trailing",
		stableDrawdownRuleFingerprint(83.68, rule), rule.CloseRatioPct, "armed",
		"algo-cl-1", 0, 0.018786, 1.2)

	ids := at.deliberateImmediateTrailIDsForPosition("CLUSDT", "short", 83.68)
	if _, ok := ids["algo-cl-1"]; !ok {
		t.Fatalf("持久化记录(activationPrice=0)应被识别为故意的立即跟踪单, got %v", ids)
	}

	// 当前 pnl 3.12% < floor 3.1309% —— 老逻辑正是在这里撤单。
	if n := at.reconcileDangerousTrailingOrders("CLUSDT", "short", 83.68, 3.12, rule.MinProfitPct); n != 0 {
		t.Fatalf("自己故意挂的无激活价单不得因当前 pnl 掉到 floor 之下被撤, cancelled=%d", n)
	}
}

// A2. 认不出归属的无激活价单(遗留/手工/交易所异常)仍然要按 pnl 门撤 ——
// 修复不能把危险单一起放过。
func TestForeignImmediateTrailBelowFloorStillCancelled(t *testing.T) {
	at := clImmediateTrailTrader(t, "cl-foreign.db", []tradertypes.OpenOrder{clTrailingOrder("algo-foreign")})
	rule := clDD1Rule()

	// 记录指向另一张单 —— 在场那张没有归属证据。
	at.persistDynamicProtectionRecordWithDetails("CLUSDT", "short", "native_trailing",
		stableDrawdownRuleFingerprint(83.68, rule), rule.CloseRatioPct, "armed",
		"algo-other", 0, 0.018786, 1.2)

	if n := at.reconcileDangerousTrailingOrders("CLUSDT", "short", 83.68, 3.12, rule.MinProfitPct); n != 1 {
		t.Fatalf("无归属证据的无激活价单在 floor 之下必须撤, cancelled=%d", n)
	}
}

// A3. 判别读的是"下单那刻"而不是"现在":同一张已登记的单,pnl 在 floor 上下来回,
// 结论必须恒定 —— 这才是消掉 ping-pong 的充分条件。
func TestDeliberateImmediateTrailVerdictIsStableAcrossFloorOscillation(t *testing.T) {
	rule := clDD1Rule()
	for _, pnl := range []float64{3.29, 3.13, 3.12, 3.18, 3.10, 3.31} {
		at := clImmediateTrailTrader(t, "cl-osc.db", []tradertypes.OpenOrder{clTrailingOrder("algo-cl-1")})
		at.persistDynamicProtectionRecordWithDetails("CLUSDT", "short", "native_trailing",
			stableDrawdownRuleFingerprint(83.68, rule), rule.CloseRatioPct, "armed",
			"algo-cl-1", 0, 0.018786, 1.2)
		if n := at.reconcileDangerousTrailingOrders("CLUSDT", "short", 83.68, pnl, rule.MinProfitPct); n != 0 {
			t.Fatalf("pnl=%.2f%% 时结论翻转(撤了 %d 张) —— 判别仍依赖当前 pnl", pnl, n)
		}
	}
}

// A4. 覆盖判定必须与撤单判定同源。只修撤单侧的话,同一次抖动会把这一档判成
// "present but NOT effective",连续 3 轮误跳熔断 —— 而熔断一跳,配合 B 的门控就是
// 本仓位余生不再在交易所侧维护这一档。这条钉住"两处同源"。
func TestDeliberateImmediateTrailCountsAsCoverageBelowFloor(t *testing.T) {
	rule := clDD1Rule()
	// 覆盖匹配走 findEquivalentPartialTrailingOrder,单子形状要与该档的计划一致。
	cb := calculateDrawdownRuleCallbackRatio(83.68, "short", normalizeDrawdownRule(rule))
	order := clTrailingOrder("algo-cl-1")
	order.CallbackRate = cb

	at := clImmediateTrailTrader(t, "cl-coverage.db", []tradertypes.OpenOrder{order})
	at.persistDynamicProtectionRecordWithDetails("CLUSDT", "short", "native_trailing",
		stableDrawdownRuleFingerprint(83.68, rule), rule.CloseRatioPct, "armed",
		"algo-cl-1", 0, cb, 1.2)

	// mark=83.0 → pnl≈0.81% < floor 3.1309%:老逻辑在这里判"NOT effective"。
	if !at.exchangeSideCoversDrawdownTierWithOrders("CLUSDT", "short", normalizeDrawdownRule(rule), 83.68, 83.0, []tradertypes.OpenOrder{order}) {
		t.Fatal("自己故意挂的立即跟踪单在 pnl 掉到 floor 之下时仍是有效覆盖,否则会误跳熔断")
	}
}

// C1. 幻影判定要带容差带:我们拿自己的 mark 判 OKX 会不会触发,而 OKX 用它自己的
// 触发价源,边界上必然有几个基点分歧。严格不等号会把健康挂单判成幻影,3 轮误跳熔断。
// 数据取 2026-07-28 16:47 生产实况:CLUSDT short activePx=80.19 mark=80.15(越过 0.05%),
// 交易所仍报 pending_activation —— 它没触发,是我们判错了。
func TestRestingTrailingNotPhantomWithinPriceTolerance(t *testing.T) {
	order := &nativeTrailingOrder{
		PositionSide: "SHORT", ActivationStatus: "pending_activation",
		ActivationPrice: 80.19, StopPrice: 80.19, Quantity: 0.84, CallbackRate: 0.012524,
	}
	// 空头:mark 跌破 activePx 才算越过。80.15 只越过 0.05% —— 在 0.2% 容差带内。
	if !nativeTrailingEffective("okx", order, "short", 80.15, 4.22, 4.1746) {
		t.Fatal("越过量在容差带内(0.05% < 0.2%)的挂单不是幻影,判成幻影会误跳熔断")
	}
	// 真漂出容差带(越过 0.5%)才算幻影。
	if nativeTrailingEffective("okx", order, "short", 79.79, 4.22, 4.1746) {
		t.Fatal("越过量超出容差带(0.5% > 0.2%)应判幻影,否则真死单会伪装成覆盖")
	}
	// 多头对称。
	longOrder := &nativeTrailingOrder{
		PositionSide: "LONG", ActivationStatus: "pending_activation",
		ActivationPrice: 100.0, StopPrice: 100.0, Quantity: 1, CallbackRate: 0.01,
	}
	if !nativeTrailingEffective("okx", longOrder, "long", 100.05, 5, 4) {
		t.Fatal("多头越过 0.05% 在容差带内,不应判幻影")
	}
	if nativeTrailingEffective("okx", longOrder, "long", 100.5, 5, 4) {
		t.Fatal("多头越过 0.5% 超出容差带,应判幻影")
	}
}

// B1. 熔断跳闸后,applyNativeTrailingDrawdown 自身必须拒绝重挂 ——
// 门控是不变量,不能依赖调用方自觉。这条直接钉住 reconciler 绕过熔断的那个缺陷:
// 它调的就是这个函数。
func TestTrippedBreakerSuppressesArmAtApplyLayer(t *testing.T) {
	at := clImmediateTrailTrader(t, "cl-breaker.db", nil)
	rule := clDD1Rule()

	key := reArmFailKey("CLUSDT", "short", normalizeDrawdownRule(rule), 83.68)
	for i := 0; i < reArmBreakerLimit; i++ {
		at.bumpReArmFail(key)
	}
	if !at.reArmBreakerTripped("CLUSDT", "short", rule, 83.68) {
		t.Fatal("前提不成立:熔断应已跳闸")
	}

	if at.applyNativeTrailingDrawdown("CLUSDT", "short", 83.68, 80.93, rule) {
		t.Fatal("熔断已跳闸,applyNativeTrailingDrawdown 必须返回 false 且不在交易所侧重挂")
	}
}

// B2. 未跳闸时不得被误挡 —— 防止把门控写成"总是拒绝"。
func TestUntrippedBreakerStillAllowsArm(t *testing.T) {
	at := clImmediateTrailTrader(t, "cl-breaker-ok.db", nil)
	rule := clDD1Rule()

	if at.reArmBreakerTripped("CLUSDT", "short", rule, 83.68) {
		t.Fatal("前提不成立:熔断不应跳闸")
	}
	if !at.applyNativeTrailingDrawdown("CLUSDT", "short", 83.68, 80.93, rule) {
		t.Fatal("熔断未跳闸时这一档应正常武装")
	}
}
