package trader

import (
	"testing"

	"nofx/store"
	tradertypes "nofx/trader/types"
)

// 相邻阶梯档位互相认领对方在场单造成的 churn —— 2026-08-03 claude/SOLUSDT LONG。
//
// 现场:22:55 / 22:57 / 22:59 三轮,每轮撤掉一张好单再挂回来,同价位 SL 挂了 3 次、
// TP 73.6931 挂了 3 次,第四轮撞上 OKX 51279 拒单。
//
//	TP1 = 73.6931  want = 0.42 × 20% = 0.084
//	TP2 = 73.7761  live = 0.42 × 15% = 0.063 → 量化 0.06
//	两档相距 0.1126% < approximatelyEqualPrice 的 0.2% 容差
//	TP1 自己的单已于 22:55:57 成交 ⇒ TP1 的 target 认领了 TP2 的单
//	判成 "covers 0.06 of 0.084 (71.4%)" → 撤 → plan 按 TP2 比例挂回 0.06 → 再判不足
//
// 判据改成"每张单唯一归属到最近的相容档位"之后,TP2 的单只归 TP2,TP1 无单可认领
// (candidate == nil)直接跳过,不再报欠量。
func solTPOrder(orderID string, price, qty float64, role string) tradertypes.OpenOrder {
	return tradertypes.OpenOrder{
		OrderID:      orderID,
		Symbol:       "SOLUSDT",
		PositionSide: "LONG",
		Type:         "TAKE_PROFIT_MARKET",
		StopPrice:    price,
		Quantity:     qty,
		// 适配器从 algoClOrdId 解出来的细粒度角色(OKX: reasonFromAlgoIDs)。
		ProtectionRole: role,
	}
}

// 四档 TP 的 plan,价位与比例取自线上快照 346。
func solLadderPlan() *ProtectionPlan {
	return &ProtectionPlan{
		Mode: "manual",
		TakeProfitOrders: []ProtectionOrder{
			{Price: 73.6931, CloseRatioPct: 20},
			{Price: 73.7761, CloseRatioPct: 15},
			{Price: 74.3026, CloseRatioPct: 15},
			{Price: 74.4671, CloseRatioPct: 15},
		},
	}
}

// TP1 的单已成交离场,只剩 TP2 的单在场。TP1 不得认领它。
func TestFindUndercovered_NeighborTierNotClaimedAfterOwnFill(t *testing.T) {
	plan := solLadderPlan()
	// 仓位 0.42 时下的单:TP2 = 0.42 × 15% = 0.063 → 量化 0.06。
	live := []tradertypes.OpenOrder{
		solTPOrder("3800594038026682368", 73.7761, 0.06, store.MechLadderTP),
	}

	got := findUndercoveredProtectionOrders(plan, live, "LONG", 0.42, nil, "SOLUSDT", nil)
	for _, u := range got {
		if u.OrderID == "3800594038026682368" {
			t.Fatalf("TP2 的在场单被邻档 TP1 认领并判欠量,这就是线上那条 churn: %s", u)
		}
	}
	if len(got) != 0 {
		t.Fatalf("不该报任何欠量,got %v", got)
	}
}

// 两档单都在场时,各归各档,谁都不欠量。
func TestFindUndercovered_AdjacentTiersEachOwnTheirOrder(t *testing.T) {
	plan := solLadderPlan()
	live := []tradertypes.OpenOrder{
		solTPOrder("tp1", 73.6931, 0.084, store.MechLadderTP),
		solTPOrder("tp2", 73.7761, 0.063, store.MechLadderTP),
	}

	got := findUndercoveredProtectionOrders(plan, live, "LONG", 0.42, nil, "SOLUSDT", nil)
	if len(got) != 0 {
		t.Fatalf("两档各自足额,不该报欠量,got %v", got)
	}
}

// 收窄不能把真欠量放过:TP2 的单确实只有该档应有量的一半,且无邻档干扰。
func TestFindUndercovered_GenuineShortfallStillReported(t *testing.T) {
	plan := &ProtectionPlan{
		Mode:             "manual",
		TakeProfitOrders: []ProtectionOrder{{Price: 73.7761, CloseRatioPct: 15}},
	}
	live := []tradertypes.OpenOrder{
		solTPOrder("tp2-small", 73.7761, 0.03, store.MechLadderTP),
	}

	got := findUndercoveredProtectionOrders(plan, live, "LONG", 0.42, nil, "SOLUSDT", nil)
	if len(got) != 1 || got[0].OrderID != "tp2-small" {
		t.Fatalf("真欠量必须仍被报出,got %v", got)
	}
}

// 角色矛盾的单不得被认领:一张 ladder_sl 角色的单落在 TP 档位价带里也不算 TP 的量。
// (Type 仍设成 TP 才能过方向谓词 —— 单靠价格与方向无法区分,角色才是判据。)
func TestFindUndercovered_RoleContradictionExcluded(t *testing.T) {
	plan := &ProtectionPlan{
		Mode:             "manual",
		TakeProfitOrders: []ProtectionOrder{{Price: 73.7761, CloseRatioPct: 15}},
	}
	live := []tradertypes.OpenOrder{
		solTPOrder("foreign", 73.7761, 0.06, store.MechLadderSL),
	}

	got := findUndercoveredProtectionOrders(plan, live, "LONG", 0.42, nil, "SOLUSDT", nil)
	if len(got) != 0 {
		t.Fatalf("角色矛盾的单不该被当成本档的量,更不该报欠量,got %v", got)
	}
}

// 角色解不出来(老单/无 clientOrderID)必须退回纯价格行为,不能因收紧而漏判。
func TestFindUndercovered_UnknownRoleDegradesToPriceOnly(t *testing.T) {
	plan := &ProtectionPlan{
		Mode:             "manual",
		TakeProfitOrders: []ProtectionOrder{{Price: 73.7761, CloseRatioPct: 15}},
	}
	live := []tradertypes.OpenOrder{
		solTPOrder("legacy", 73.7761, 0.03, ""),
	}

	got := findUndercoveredProtectionOrders(plan, live, "LONG", 0.42, nil, "SOLUSDT", nil)
	if len(got) != 1 || got[0].OrderID != "legacy" {
		t.Fatalf("角色未知应退回纯价格判据并仍能报欠量,got %v", got)
	}
}

// 两档收敛到同一价位(线上确实存在:SL 与 Backstop 同价)时,一张单不得被数两遍。
// 归属唯一化必须保留这条既有语义。
func TestFindUndercovered_CollapsedPricesShareSingleOrderOnce(t *testing.T) {
	plan := &ProtectionPlan{
		Mode: "manual",
		TakeProfitOrders: []ProtectionOrder{
			{Price: 73.7761, CloseRatioPct: 15},
			{Price: 73.7761, CloseRatioPct: 15},
		},
	}
	live := []tradertypes.OpenOrder{
		solTPOrder("only-one", 73.7761, 0.063, store.MechLadderTP),
	}

	got := findUndercoveredProtectionOrders(plan, live, "LONG", 0.42, nil, "SOLUSDT", nil)
	// 第一档足额;第二档没单可归(已被第一档归属)→ candidate==nil → 跳过。
	if len(got) != 0 {
		t.Fatalf("同价两档只有一张单时不该报欠量(避免误撤唯一的保护单),got %v", got)
	}
}
