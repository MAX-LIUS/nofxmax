package trader

import (
	"testing"

	tradertypes "nofx/trader/types"
)

// 加仓后保护单量不跟涨的检测判据测试。测的是纯函数
// protectionQtyTargetsForPlan / findUndercoveredProtectionOrders —— 决定
// "撤哪张量不足的静态保护单",是这条修复链上唯一有破坏性的判断,逐条钉死。

func slOrder(orderID string, stop, qty float64) tradertypes.OpenOrder {
	return tradertypes.OpenOrder{
		OrderID:      orderID,
		Symbol:       "SKHYNIXUSDT",
		PositionSide: "SHORT",
		Type:         "STOP_MARKET",
		StopPrice:    stop,
		Quantity:     qty,
	}
}

func tpOrder(orderID string, price, qty float64) tradertypes.OpenOrder {
	return tradertypes.OpenOrder{
		OrderID:      orderID,
		Symbol:       "SKHYNIXUSDT",
		PositionSide: "SHORT",
		Type:         "TAKE_PROFIT_MARKET",
		StopPrice:    price,
		Quantity:     qty,
	}
}

// 核心生产场景 (GPT/SKHYNIXUSDT SHORT): 仓位加仓涨到 0.035,而全量止损单
// 还停在 0.017。价格对得上,量只有一半 —— 必须被判过期并撤掉。
func TestFindUndercovered_FullStopStaleAfterAddOn(t *testing.T) {
	plan := &ProtectionPlan{NeedsStopLoss: true, StopLossPrice: 1113.0}
	orders := []tradertypes.OpenOrder{slOrder("sl_old", 1113.0, 0.017)}

	stale := findUndercoveredProtectionOrders(plan, orders, "SHORT", 0.035, nil, "SKHYNIXUSDT", nil)
	if len(stale) != 1 || stale[0].OrderID != "sl_old" {
		t.Fatalf("加仓后欠量的全量止损单应被判过期,got %+v", stale)
	}
	if stale[0].Kind != "full_sl" || stale[0].WantQty != 0.035 || stale[0].LiveQty != 0.017 {
		t.Fatalf("应带上档位类型与量,got %+v", stale[0])
	}
}

// 量达标就绝不动 —— 撤一张在场的止损单代价远高于多留。
func TestFindUndercovered_AdequateStopUntouched(t *testing.T) {
	plan := &ProtectionPlan{NeedsStopLoss: true, StopLossPrice: 1113.0}
	orders := []tradertypes.OpenOrder{slOrder("sl_ok", 1113.0, 0.035)}

	if stale := findUndercoveredProtectionOrders(plan, orders, "SHORT", 0.035, nil, "SKHYNIXUSDT", nil); len(stale) != 0 {
		t.Fatalf("量达标不应撤单,got %+v", stale)
	}
}

// 超量绝不动:reduce-only 下超量止损无害,去撤会和 reconciler "多一张止损只是多一层
// 保险" 的容忍策略对打(文件头第 1 条)。
func TestFindUndercovered_OversizedStopUntouched(t *testing.T) {
	plan := &ProtectionPlan{NeedsStopLoss: true, StopLossPrice: 1113.0}
	// 部分止盈缩仓后,止损单量比现仓位还大。
	orders := []tradertypes.OpenOrder{slOrder("sl_big", 1113.0, 0.05)}

	if stale := findUndercoveredProtectionOrders(plan, orders, "SHORT", 0.035, nil, "SKHYNIXUSDT", nil); len(stale) != 0 {
		t.Fatalf("超量止损单不应撤,got %+v", stale)
	}
}

// 同价位多张单的合计量达标就算达标:阶梯档被拆成两张各半的正常中间态不能误判。
func TestFindUndercovered_AggregateCoverageCountsAsCovered(t *testing.T) {
	plan := &ProtectionPlan{NeedsStopLoss: true, StopLossPrice: 1113.0}
	orders := []tradertypes.OpenOrder{
		slOrder("sl_a", 1113.0, 0.020),
		slOrder("sl_b", 1113.0, 0.015),
	}

	if stale := findUndercoveredProtectionOrders(plan, orders, "SHORT", 0.035, nil, "SKHYNIXUSDT", nil); len(stale) != 0 {
		t.Fatalf("同价位合计 0.035 达标不应撤,got %+v", stale)
	}
}

// 场内不报量(Quantity<=0)一律放过:unknown ≠ inadequate。
func TestFindUndercovered_UnknownQtyPassesThrough(t *testing.T) {
	plan := &ProtectionPlan{NeedsStopLoss: true, StopLossPrice: 1113.0}
	orders := []tradertypes.OpenOrder{slOrder("sl_noqty", 1113.0, 0)}

	if stale := findUndercoveredProtectionOrders(plan, orders, "SHORT", 0.035, nil, "SKHYNIXUSDT", nil); len(stale) != 0 {
		t.Fatalf("量未知不应撤单(unknown ≠ inadequate),got %+v", stale)
	}
}

// 欠量但没有单号:撤不了就别报(报了只能降级成 tag 撤,会连坐兄弟档)。
func TestFindUndercovered_NoOrderIDNotReported(t *testing.T) {
	plan := &ProtectionPlan{NeedsStopLoss: true, StopLossPrice: 1113.0}
	orders := []tradertypes.OpenOrder{slOrder("", 1113.0, 0.017)}

	if stale := findUndercoveredProtectionOrders(plan, orders, "SHORT", 0.035, nil, "SKHYNIXUSDT", nil); len(stale) != 0 {
		t.Fatalf("没有单号不应报(撤不了),got %+v", stale)
	}
}

// 阶梯档:每档量 = 仓位 × CloseRatioPct/100,必须和下单路径逐字一致。
func TestFindUndercovered_LadderTierStale(t *testing.T) {
	plan := &ProtectionPlan{
		StopLossOrders: []ProtectionOrder{
			{Price: 1120.0, CloseRatioPct: 40},
			{Price: 1130.0, CloseRatioPct: 60},
		},
	}
	// 仓位 0.035:两档应有 0.014 / 0.021。第一档在场只有 0.007(欠),第二档达标。
	orders := []tradertypes.OpenOrder{
		slOrder("t1", 1120.0, 0.007),
		slOrder("t2", 1130.0, 0.021),
	}

	stale := findUndercoveredProtectionOrders(plan, orders, "SHORT", 0.035, nil, "SKHYNIXUSDT", nil)
	if len(stale) != 1 || stale[0].OrderID != "t1" {
		t.Fatalf("只有欠量的第一档应被判过期,got %+v", stale)
	}
	if stale[0].Kind != "ladder_sl" {
		t.Fatalf("应标为 ladder_sl,got %s", stale[0].Kind)
	}
}

// 有阶梯时就不再对全量 SL/TP 报缺 —— 与下单路径的 fullStop/fullTP 判据一致,
// 否则会去撤一张 plan 根本不维护的单。
func TestProtectionQtyTargets_LadderSuppressesFull(t *testing.T) {
	plan := &ProtectionPlan{
		NeedsStopLoss:  true,
		StopLossPrice:  1113.0,
		StopLossOrders: []ProtectionOrder{{Price: 1120.0, CloseRatioPct: 100}},
	}
	targets := protectionQtyTargetsForPlan(plan, 0.035)
	for _, tg := range targets {
		if tg.Kind == "full_sl" {
			t.Fatalf("有阶梯 SL 时不应再产生 full_sl target,got %+v", targets)
		}
	}
}

// TP 与 SL 同价位时(收敛),一张单不能同时兑现两个档位:claimed 去重。
func TestFindUndercovered_OneOrderNotDoubleCounted(t *testing.T) {
	// 两个止损档价格撞在一起,合计需求 0.035,但在场只有一张 0.017。
	plan := &ProtectionPlan{
		StopLossOrders: []ProtectionOrder{
			{Price: 1120.0, CloseRatioPct: 50},
			{Price: 1120.0, CloseRatioPct: 50},
		},
	}
	// 每档需求 0.0175;在场唯一一张只有 0.010(单档就欠 >5%,排除容差干扰)。
	orders := []tradertypes.OpenOrder{slOrder("only", 1120.0, 0.010)}

	stale := findUndercoveredProtectionOrders(plan, orders, "SHORT", 0.035, nil, "SKHYNIXUSDT", nil)
	// 第一个档认领了这张单判欠量→报;第二个档没单可认→跳过。总之不能把一张单数两遍当足额。
	if len(stale) != 1 || stale[0].OrderID != "only" {
		t.Fatalf("一张单只能认领一次,应报一次欠量,got %+v", stale)
	}
}

// TP 单只按 TP 档匹配,不能被 SL 档认领(方向/类型隔离)。
func TestFindUndercovered_TakeProfitStale(t *testing.T) {
	plan := &ProtectionPlan{
		TakeProfitOrders: []ProtectionOrder{{Price: 1090.0, CloseRatioPct: 100}},
	}
	orders := []tradertypes.OpenOrder{tpOrder("tp_old", 1090.0, 0.017)}

	stale := findUndercoveredProtectionOrders(plan, orders, "SHORT", 0.035, nil, "SKHYNIXUSDT", nil)
	if len(stale) != 1 || stale[0].OrderID != "tp_old" || !stale[0].IsProfit {
		t.Fatalf("欠量止盈单应被判过期且标 IsProfit,got %+v", stale)
	}
}

// trailing 单不归这条路径管(有自己的归属账本)。
func TestFindUndercovered_TrailingIgnored(t *testing.T) {
	plan := &ProtectionPlan{NeedsStopLoss: true, StopLossPrice: 1113.0}
	orders := []tradertypes.OpenOrder{{
		OrderID: "trail", Symbol: "SKHYNIXUSDT", PositionSide: "SHORT",
		Type: "TRAILING_STOP_MARKET", StopPrice: 1113.0, Quantity: 0.017, CallbackRate: 0.3,
	}}

	if stale := findUndercoveredProtectionOrders(plan, orders, "SHORT", 0.035, nil, "SKHYNIXUSDT", nil); len(stale) != 0 {
		t.Fatalf("trailing 单不归此路径,got %+v", stale)
	}
}

// 认领给 BE 逐档路径的止损单,即便价位撞进某个 plan SL 价的容差、量也欠,
// 静态 resize 也必须让开 —— 否则两条路径抢同一张单,一撤一挂成 churn。
func TestFindUndercovered_BEOwnedOrderExcluded(t *testing.T) {
	plan := &ProtectionPlan{NeedsStopLoss: true, StopLossPrice: 1113.0}
	orders := []tradertypes.OpenOrder{slOrder("be_owned", 1113.0, 0.017)}
	beOwned := map[string]struct{}{"be_owned": {}}

	if stale := findUndercoveredProtectionOrders(plan, orders, "SHORT", 0.035, nil, "SKHYNIXUSDT", beOwned); len(stale) != 0 {
		t.Fatalf("BE 认领的单应让 BE 路径管,静态 resize 不应撤,got %+v", stale)
	}
	// 同一张单不在 BE 认领集合里时,照常按欠量处理(对照)。
	if stale := findUndercoveredProtectionOrders(plan, orders, "SHORT", 0.035, nil, "SKHYNIXUSDT", nil); len(stale) != 1 {
		t.Fatalf("非 BE 认领的欠量单应正常报,got %+v", stale)
	}
}

// 反向:空 plan / 零仓位不产生任何 target。
func TestProtectionQtyTargets_EmptyInputs(t *testing.T) {
	if tg := protectionQtyTargetsForPlan(nil, 0.035); tg != nil {
		t.Fatalf("nil plan 应返回 nil,got %+v", tg)
	}
	if tg := protectionQtyTargetsForPlan(&ProtectionPlan{NeedsStopLoss: true, StopLossPrice: 1113.0}, 0); tg != nil {
		t.Fatalf("零仓位应返回 nil,got %+v", tg)
	}
}

// 量化后等价就不撤:窄档位量化误差不该引发无意义撤挂(复用 ZEC 那次的教训)。
func TestFindUndercovered_QuantizedEquivalentNotStale(t *testing.T) {
	plan := &ProtectionPlan{NeedsStopLoss: true, StopLossPrice: 1113.0}
	quantize := okxLikeQuantizer(0.01, true) // 整数张
	// 目标 0.035 → 3.5 张 → "4"(四舍五入的实现是 %.0f=4);在场 0.034 → 3.4 张 → "3"。
	// 用一个两边量化到同一张的例子:目标 0.030 → "3",在场 0.028 → "3"。
	plan.StopLossPrice = 1113.0
	orders := []tradertypes.OpenOrder{slOrder("q", 1113.0, 0.028)}

	if stale := findUndercoveredProtectionOrders(plan, orders, "SHORT", 0.030, quantize, "SKHYNIXUSDT", nil); len(stale) != 0 {
		t.Fatalf("量化后同为 3 张应视作等价,不撤,got %+v", stale)
	}
}
