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
