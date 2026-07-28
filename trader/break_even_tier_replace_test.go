package trader

import (
	"testing"

	"nofx/store"
	tradertypes "nofx/trader/types"
)

// 逐档替换的判据测试。这里测的是纯函数 findStaleBreakEvenTierOrders —— 它决定
// "撤哪张单",是整条链上唯一有破坏性的判断,必须逐条钉死。

func beRecord(stage, orderID, status string) store.DynamicProtectionRecord {
	return store.DynamicProtectionRecord{
		TraderID:        "t1",
		Symbol:          "WLDUSDT",
		Side:            "short",
		ProtectionType:  "break_even_stop",
		RuleFingerprint: "0.36220000|95.00000000|0.5000|0.2000|" + stage,
		Status:          status,
		ExchangeOrderID: orderID,
	}
}

func beLiveOrder(orderID string, stop float64) tradertypes.OpenOrder {
	return tradertypes.OpenOrder{
		OrderID:       orderID,
		Symbol:        "WLDUSDT",
		PositionSide:  "SHORT",
		Type:          "STOP_MARKET",
		StopPrice:     stop,
		ClientOrderID: "be-stop-x",
	}
}

// 核心场景:均价漂移后同一档算出新价,旧单必须被认成过期并撤掉。
// 这正是 WLDUSDT 四张 BE 单的成因(两档 × 漂移前后两组价格)。
func TestFindStaleBreakEvenTierOrdersDetectsDriftedTierOrder(t *testing.T) {
	records := []store.DynamicProtectionRecord{beRecord("BE1", "old_be1", "armed")}
	orders := []tradertypes.OpenOrder{beLiveOrder("old_be1", 0.3604)}

	stale := findStaleBreakEvenTierOrders(records, "t1", "WLDUSDT", "short", "BE1", 0.3568, orders)
	if len(stale) != 1 || stale[0].OrderID != "old_be1" {
		t.Fatalf("漂移后的同档旧单应被判过期,got %+v", stale)
	}
	if stale[0].StopPrice != 0.3604 || stale[0].Stage != "BE1" {
		t.Fatalf("过期单应带上原价与档位,got %+v", stale[0])
	}
}

// 兄弟档绝不能被牵连 —— 这是整个修复最重要的一条安全性质。
// BE 所有档位的机制码都是 "BE",一旦判据放宽到"按 tag/按类型",BE2 就会被 BE1 的
// 替换顺手撤掉(v1.16.5「collapse 误撤兄弟档」那一类事故)。
func TestFindStaleBreakEvenTierOrdersNeverTouchesSiblingTier(t *testing.T) {
	records := []store.DynamicProtectionRecord{
		beRecord("BE1", "be1_order", "armed"),
		beRecord("BE2", "be2_order", "armed"),
	}
	orders := []tradertypes.OpenOrder{
		beLiveOrder("be1_order", 0.3604),
		beLiveOrder("be2_order", 0.3539),
	}

	stale := findStaleBreakEvenTierOrders(records, "t1", "WLDUSDT", "short", "BE1", 0.3568, orders)
	if len(stale) != 1 {
		t.Fatalf("只该动 BE1 自己那张,got %+v", stale)
	}
	if stale[0].OrderID != "be1_order" {
		t.Fatalf("撤错档位:got %s,想要 be1_order", stale[0].OrderID)
	}
}

// 价格实质未变(容差内)不得替换,否则均价末位抖动会引发无意义的撤挂对 —— 那本身
// 就是一个新的挂撤循环。
func TestFindStaleBreakEvenTierOrdersIgnoresPriceWithinTolerance(t *testing.T) {
	records := []store.DynamicProtectionRecord{beRecord("BE1", "be1_order", "armed")}
	// 0.36040 → 0.36045,相对差约 0.014%,在 0.05% 容差内。
	orders := []tradertypes.OpenOrder{beLiveOrder("be1_order", 0.36040)}

	stale := findStaleBreakEvenTierOrders(records, "t1", "WLDUSDT", "short", "BE1", 0.36045, orders)
	if len(stale) != 0 {
		t.Fatalf("容差内不应替换,got %+v", stale)
	}
}

// 三重与门的每一条都必须能单独否决撤单。
func TestFindStaleBreakEvenTierOrdersRequiresAllGuards(t *testing.T) {
	target := 0.3568
	cases := []struct {
		name    string
		records []store.DynamicProtectionRecord
		orders  []tradertypes.OpenOrder
		stage   string
	}{
		{
			name:    "记录非 armed(已被替换过)",
			records: []store.DynamicProtectionRecord{beRecord("BE1", "old_be1", "replaced")},
			orders:  []tradertypes.OpenOrder{beLiveOrder("old_be1", 0.3604)},
			stage:   "BE1",
		},
		{
			name:    "记录没有 order id(历史空 id 记录)",
			records: []store.DynamicProtectionRecord{beRecord("BE1", "", "armed")},
			orders:  []tradertypes.OpenOrder{beLiveOrder("some_be", 0.3604)},
			stage:   "BE1",
		},
		{
			name:    "该 id 已不在场(已成交/已撤)",
			records: []store.DynamicProtectionRecord{beRecord("BE1", "gone_be1", "armed")},
			orders:  []tradertypes.OpenOrder{beLiveOrder("other", 0.3604)},
			stage:   "BE1",
		},
		{
			name:    "id 对上但那张单不是保本止损(阶梯止损)",
			records: []store.DynamicProtectionRecord{beRecord("BE1", "ladder_one", "armed")},
			orders: []tradertypes.OpenOrder{{
				OrderID: "ladder_one", Symbol: "WLDUSDT", PositionSide: "SHORT",
				Type: "STOP_MARKET", StopPrice: 0.3604, ClientOrderID: "ladder_sl_1",
			}},
			stage: "BE1",
		},
		{
			name:    "id 对上但那张是 trailing",
			records: []store.DynamicProtectionRecord{beRecord("BE1", "trail_one", "armed")},
			orders: []tradertypes.OpenOrder{{
				OrderID: "trail_one", Symbol: "WLDUSDT", PositionSide: "SHORT",
				Type: "TRAILING_STOP_MARKET", StopPrice: 0.3604, CallbackRate: 0.3,
			}},
			stage: "BE1",
		},
		{
			name:    "别的 trader 的记录",
			records: []store.DynamicProtectionRecord{beRecord("BE1", "old_be1", "armed")},
			orders:  []tradertypes.OpenOrder{beLiveOrder("old_be1", 0.3604)},
			stage:   "BE1",
		},
		{
			name: "stage 解析不出来(fingerprint 段数不足)",
			records: []store.DynamicProtectionRecord{{
				TraderID: "t1", Symbol: "WLDUSDT", Side: "short",
				ProtectionType: "break_even_stop", Status: "armed",
				RuleFingerprint: "0.36220000|95.00000000|0.5000|0.2000",
				ExchangeOrderID: "old_be1",
			}},
			orders: []tradertypes.OpenOrder{beLiveOrder("old_be1", 0.3604)},
			stage:  "BE1",
		},
	}

	for _, c := range cases {
		traderID := "t1"
		if c.name == "别的 trader 的记录" {
			traderID = "t2"
		}
		stale := findStaleBreakEvenTierOrders(c.records, traderID, "WLDUSDT", "short", c.stage, target, c.orders)
		if len(stale) != 0 {
			t.Fatalf("%s:不应撤单,got %+v", c.name, stale)
		}
	}
}

// 反向:目标价或档位缺失时直接放弃,绝不"猜一个"。
func TestFindStaleBreakEvenTierOrdersRefusesWithoutStageOrTarget(t *testing.T) {
	records := []store.DynamicProtectionRecord{beRecord("BE1", "old_be1", "armed")}
	orders := []tradertypes.OpenOrder{beLiveOrder("old_be1", 0.3604)}

	if stale := findStaleBreakEvenTierOrders(records, "t1", "WLDUSDT", "short", "", 0.3568, orders); len(stale) != 0 {
		t.Fatalf("档位为空不应撤单,got %+v", stale)
	}
	if stale := findStaleBreakEvenTierOrders(records, "t1", "WLDUSDT", "short", "BE1", 0, orders); len(stale) != 0 {
		t.Fatalf("目标价无效不应撤单,got %+v", stale)
	}
}

// 反向:仓位不同(symbol/side)不得跨仓位撤单。
func TestFindStaleBreakEvenTierOrdersScopedToPosition(t *testing.T) {
	records := []store.DynamicProtectionRecord{beRecord("BE1", "old_be1", "armed")}
	orders := []tradertypes.OpenOrder{beLiveOrder("old_be1", 0.3604)}

	if stale := findStaleBreakEvenTierOrders(records, "t1", "ETHUSDT", "short", "BE1", 0.3568, orders); len(stale) != 0 {
		t.Fatalf("跨 symbol 不应撤单,got %+v", stale)
	}
	if stale := findStaleBreakEvenTierOrders(records, "t1", "WLDUSDT", "long", "BE1", 0.3568, orders); len(stale) != 0 {
		t.Fatalf("跨 side 不应撤单,got %+v", stale)
	}
}

// 同一 id 在多条记录里重复出现时只撤一次(避免对同一张单发两次撤单请求)。
func TestFindStaleBreakEvenTierOrdersDeduplicatesByOrderID(t *testing.T) {
	records := []store.DynamicProtectionRecord{
		beRecord("BE1", "old_be1", "armed"),
		beRecord("BE1", "old_be1", "armed"),
	}
	orders := []tradertypes.OpenOrder{beLiveOrder("old_be1", 0.3604)}

	stale := findStaleBreakEvenTierOrders(records, "t1", "WLDUSDT", "short", "BE1", 0.3568, orders)
	if len(stale) != 1 {
		t.Fatalf("同一 id 只该撤一次,got %+v", stale)
	}
}

// isBreakEvenTaggedOrder 抽出来之后,必须与"该档是否已在场"的判据保持同源。
// 两边一旦漂移就会出现"匹配不上所以挂新单、也不认它所以不撤旧单"的堆积。
func TestIsBreakEvenTaggedOrderAgreesWithMatcher(t *testing.T) {
	orders := []tradertypes.OpenOrder{beLiveOrder("be_x", 0.3604)}
	if !isBreakEvenTaggedOrder(orders[0], "SHORT") {
		t.Fatal("形状判据应认这张是保本止损")
	}
	if _, found := matchingBreakEvenOrderID(orders, "SHORT", 0.3604); !found {
		t.Fatal("价格匹配应命中同一张单")
	}
	// 方向不符时两边都要否决。
	if isBreakEvenTaggedOrder(orders[0], "LONG") {
		t.Fatal("方向不符不应认领")
	}
	if _, found := matchingBreakEvenOrderID(orders, "LONG", 0.3604); found {
		t.Fatal("方向不符不应命中")
	}
}
