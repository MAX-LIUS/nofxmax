package trader

import (
	"errors"
	"path/filepath"
	"testing"

	"nofx/store"
	tradertypes "nofx/trader/types"
)

var errTestCancelRejected = errors.New("venue rejected cancel")

// 这一组测试钉住"每张交易所 trailing 单必须有且只有一个已落库的归属"这条不变式,
// 以及它的三个后果。全部反向验证过:去掉对应修复,对应用例即失败。
//
// 线上证据 (2026-07-27):
//   - Binance BN CLUSDT 一个空头挂了三张 trailing:2.43(dd1) + 0.73(30%档) + 1.22(=50%,无主)
//   - OKX ETHUSDT 订单 3779472770300542976 qty 0.120/0.239,在保护 blob 原文里出现 0 次

func immediateOwnerFixture(t *testing.T, dbName string) (*AutoTrader, *fakeProtectionTrader) {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), dbName))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	fake := &fakeProtectionTrader{
		positions: []map[string]interface{}{{
			"symbol": "CLUSDT", "side": "short", "entryPrice": 83.54,
			"markPrice": 83.0, "positionAmt": -2.43,
		}},
	}
	at := &AutoTrader{
		id: "trader-imm-owner", exchangeID: "exch-imm-owner", store: st,
		exchange: "binance", trader: fake,
		protectionState: make(map[string]string),
		drawdownState:   make(map[string]string),
		peakPnLCache:    make(map[string]float64),
		reArmFailCache:  make(map[string]int),
	}
	return at, fake
}

// I1. 归属必须落库:重启(清空内存 map)后仍能撤掉那张 immediate trailing 单。
// 修复前 cancelImmediateTrailing 在 ID 为空时直接 return,单子永久留在交易所。
func TestImmediateTrailingOwnerSurvivesRestart(t *testing.T) {
	at, fake := immediateOwnerFixture(t, "imm-restart.db")

	at.placeImmediateTrailing("CLUSDT", "short", 83.54, 0.0306)
	if fake.trailingCalls != 1 {
		t.Fatalf("前提:应挂出 1 张 immediate trailing,got %d", fake.trailingCalls)
	}
	memID := at.getImmediateTrailingOrderID("CLUSDT", "short")
	if memID == "" {
		t.Fatal("前提:内存里应有 orderID")
	}
	if got := at.persistedImmediateTrailingOrderID("CLUSDT", "short"); got != memID {
		t.Fatalf("归属必须落库:期望 %q,got %q", memID, got)
	}

	// 模拟重启:内存 map 清空,DB 保留。
	at.clearImmediateTrailingOrderID("CLUSDT", "short")
	if at.getImmediateTrailingOrderID("CLUSDT", "short") != "" {
		t.Fatal("前提:内存 hint 应已清空")
	}

	at.cancelImmediateTrailing("CLUSDT", "short")
	if fake.cancelTrailingCalls != 1 {
		t.Fatalf("重启后必须仍能撤掉那张单(靠落库归属),got %d cancels", fake.cancelTrailingCalls)
	}
	if got := at.persistedImmediateTrailingOrderID("CLUSDT", "short"); got != "" {
		t.Fatalf("撤成功后归属记录应转为 cleared,仍读到 %q", got)
	}
}

// I2. 决定性:那张 50% 单必须进 claimed 集合 —— 否则全平档的位置兜底
// ("本方向第一张未被认领的 trailing 单就是我")会认领它,只保护半仓却报告已覆盖。
func TestFullTierDoesNotAdoptImmediateTrailingOrder(t *testing.T) {
	at, fake := immediateOwnerFixture(t, "imm-noadopt.db")
	entry := 83.54

	at.placeImmediateTrailing("CLUSDT", "short", entry, 0.0306)
	immID := at.getImmediateTrailingOrderID("CLUSDT", "short")
	if immID == "" {
		t.Fatal("前提:immediate trailing 应已挂出")
	}

	claimed := at.claimedTrailingOrderIDsForPosition("CLUSDT", "short", entry, "")
	if _, ok := claimed[immID]; !ok {
		t.Fatalf("immediate trailing 单 %q 必须在 claimed 集合里(它也是有主的 trailing 单)", immID)
	}

	// dd1 全平档,没有 stored orderID → 走位置兜底。
	dd1 := store.DrawdownTakeProfitRule{MinProfitPct: 2.9627, MaxDrawdownPct: 1.7776, CloseRatioPct: 100}
	got := at.findExistingFullTrailingOrder("CLUSDT", "short", entry, dd1, fake.openOrders)
	if got != nil {
		t.Fatalf("决定性:全平档不得认领 immediate trailing 单(qty=%.4f 只有半仓),却认领了 %q",
			got.Quantity, got.OrderID)
	}
}

// I3. 语义护栏:即使是完全无主的单(旧版本遗留/手工单),覆盖率不足也不得被全平档认领。
// 这一条独立于归属登记 —— 归属修的是"我们自己挂的单",这一条修的是"来源不明的单"。
func TestFullTierRefusesUndersizedUnownedOrder(t *testing.T) {
	at, fake := immediateOwnerFixture(t, "imm-coverage.go.db")
	entry := 83.54
	dd1 := store.DrawdownTakeProfitRule{MinProfitPct: 2.9627, MaxDrawdownPct: 1.7776, CloseRatioPct: 100}

	// 无主的半仓单(线上 2000001313247150 的形状:1.22 / 2.43)。
	undersized := tradertypes.OpenOrder{
		OrderID: "orphan-half", Symbol: "CLUSDT", PositionSide: "SHORT",
		Type: "TRAILING_STOP_MARKET", Quantity: 1.22, StopPrice: 86.17,
		ActivationStatus: "activated", Status: "NEW",
	}
	fake.openOrders = []tradertypes.OpenOrder{undersized}
	if got := at.findExistingFullTrailingOrder("CLUSDT", "short", entry, dd1, fake.openOrders); got != nil {
		t.Fatalf("覆盖率 %.0f%% 的无主单不得被全平档认领,却认领了 %q",
			undersized.Quantity/2.43*100, got.OrderID)
	}

	// 反面:足额的无主单仍可认领(不能因为护栏把正常兜底一起废掉,
	// 例如 E10 那种"故意的立即跟踪"整仓单)。
	full := undersized
	full.OrderID = "orphan-full"
	full.Quantity = 2.43
	fake.openOrders = []tradertypes.OpenOrder{full}
	got := at.findExistingFullTrailingOrder("CLUSDT", "short", entry, dd1, fake.openOrders)
	if got == nil || got.OrderID != "orphan-full" {
		t.Fatal("足额的无主单必须仍能被兜底认领,否则会误判档位缺失并重挂")
	}
}

// I4. 加仓漏单:档位"已覆盖"时,旧代码在 applyNativeTrailingDrawdown 内部提前
// return true,走不到它末尾那句 cancelImmediateTrailing → 每次加仓都留下一张新的
// 50% 单。修复把撤单点收口到 applyNativeTrailingDrawdown 的返回值上:只要它报告
// "这一档此刻交易所侧确有有效保护",无论刚挂上还是本来就覆盖,都撤。
//
// 因此本测试直接断言"调用它就该撤掉",而不是断言撤单发生在某个特定调用点 ——
// 后者会把当时的代码结构写进断言,结构一改测试就假失败。
func TestCoveredTierStillCancelsImmediateTrailing(t *testing.T) {
	at, fake := immediateOwnerFixture(t, "imm-addon.db")
	entry := 83.54
	dd1 := store.DrawdownTakeProfitRule{MinProfitPct: 2.9627, MaxDrawdownPct: 1.7776, CloseRatioPct: 100}

	// 先造出"档位已覆盖"的状态:一张属于 dd1 的整仓 trailing 单 + armed 状态。
	at.setProtectionState("CLUSDT", "short", "native_trailing_armed")
	fake.openOrders = []tradertypes.OpenOrder{{
		OrderID: "dd1-live", Symbol: "CLUSDT", PositionSide: "SHORT",
		Type: "TRAILING_STOP_MARKET", Quantity: 2.43, StopPrice: 85.08,
		ActivationStatus: "activated", Status: "NEW",
	}}
	at.persistDynamicProtectionRecordWithDetails("CLUSDT", "short", "native_trailing",
		stableDrawdownRuleFingerprint(entry, dd1), 100, "armed", "dd1-live", 81.06, 0.017776, 2.43)

	// 加仓场景:又挂了一张 immediate trailing。
	at.placeImmediateTrailing("CLUSDT", "short", entry, 0.0306)
	immID := at.getImmediateTrailingOrderID("CLUSDT", "short")
	if immID == "" {
		t.Fatal("前提:immediate trailing 应已挂出")
	}

	// 档位已覆盖 → 报告 true 且必须顺手撤掉那张 50% 单。
	if !at.applyNativeTrailingDrawdown("CLUSDT", "short", entry, 83.0, dd1) {
		t.Fatal("前提:档位应报告已覆盖")
	}
	if fake.cancelTrailingCalls != 1 {
		t.Fatalf("档位已覆盖后必须撤掉 immediate trailing(否则每次加仓漏一张),got %d 次撤单", fake.cancelTrailingCalls)
	}

	// 幂等:重复轮询不得反复发撤单请求。
	at.cancelImmediateTrailing("CLUSDT", "short")
	if fake.cancelTrailingCalls != 1 {
		t.Fatalf("撤单应幂等,重复调用又发了请求:got %d", fake.cancelTrailingCalls)
	}
	for _, o := range fake.openOrders {
		if o.OrderID == immID {
			t.Fatalf("那张 50%% 单应已从交易所移除,仍在:%q", immID)
		}
	}
}

// I5. 撤单失败的两种情形必须区别对待,依据是"交易所还看不看得见这张单",
// 不是错误文本(各交易所各原因的文本都不一样)。
//
//	看得见 → 单子还活着,保留归属记录,下一轮重试(不能丢主,否则又变无主单)。
//	看不见 → 单子已经没了(平仓时的一刀切撤单/手工撤/交易所过期),
//	         必须退休归属记录,否则每轮都对一个死 ID 报警并把它留在 claimed 集合里。
func TestCancelFailureRetiresRecordOnlyWhenOrderGone(t *testing.T) {
	t.Run("交易所仍可见:保留归属,可重试", func(t *testing.T) {
		at, fake := immediateOwnerFixture(t, "imm-cancel-visible.db")
		at.placeImmediateTrailing("CLUSDT", "short", 83.54, 0.0306)
		immID := at.getImmediateTrailingOrderID("CLUSDT", "short")
		if immID == "" {
			t.Fatal("前提:immediate trailing 应已挂出")
		}
		// 单子仍在 openOrders 里(placeImmediateTrailing 已追加),但撤单被拒。
		fake.cancelTrailingErr = errTestCancelRejected
		at.cancelImmediateTrailing("CLUSDT", "short")

		if got := at.persistedImmediateTrailingOrderID("CLUSDT", "short"); got != immID {
			t.Fatalf("单子还在交易所上,归属记录不能退休(否则重新变成无主单,档位会把它当整仓保护认领):got %q want %q", got, immID)
		}
		// 放开错误后下一轮必须真的撤掉 —— 证明"保留"是为了重试,不是死锁。
		fake.cancelTrailingErr = nil
		at.cancelImmediateTrailing("CLUSDT", "short")
		if got := at.persistedImmediateTrailingOrderID("CLUSDT", "short"); got != "" {
			t.Fatalf("重试成功后归属记录应退休,仍为 %q", got)
		}
	})

	t.Run("交易所已不可见:退休归属", func(t *testing.T) {
		at, fake := immediateOwnerFixture(t, "imm-cancel-gone.db")
		at.placeImmediateTrailing("CLUSDT", "short", 83.54, 0.0306)
		immID := at.getImmediateTrailingOrderID("CLUSDT", "short")
		if immID == "" {
			t.Fatal("前提:immediate trailing 应已挂出")
		}
		// 模拟单子已被别处撤掉(如平仓一刀切撤单),之后我们的撤单请求当然失败。
		fake.mu.Lock()
		fake.openOrders = nil
		fake.mu.Unlock()
		fake.cancelTrailingErr = errTestCancelRejected

		at.cancelImmediateTrailing("CLUSDT", "short")
		if got := at.persistedImmediateTrailingOrderID("CLUSDT", "short"); got != "" {
			t.Fatalf("单子已不在交易所,归属记录必须退休,否则每轮都对死 ID 重试报警:仍为 %q", got)
		}
	})

	t.Run("查不到订单时按未知处理,不得退休", func(t *testing.T) {
		at, fake := immediateOwnerFixture(t, "imm-cancel-unknown.db")
		at.placeImmediateTrailing("CLUSDT", "short", 83.54, 0.0306)
		immID := at.getImmediateTrailingOrderID("CLUSDT", "short")
		if immID == "" {
			t.Fatal("前提:immediate trailing 应已挂出")
		}
		// 撤单失败 + 查询也失败:此时"看不见"不等于"不存在"。
		fake.cancelTrailingErr = errTestCancelRejected
		fake.getOpenOrdersErr = errTestCancelRejected

		at.cancelImmediateTrailing("CLUSDT", "short")
		if got := at.persistedImmediateTrailingOrderID("CLUSDT", "short"); got != immID {
			t.Fatalf("查询失败只能算未知,不能当作单子已消失而退休归属记录:got %q want %q", got, immID)
		}
	})
}
