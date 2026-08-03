package trader

import (
	"testing"

	"nofx/store"
)

// TestConsumeAllowedProtectionSlotRejectsCrossRole 复现 ZECUSDT 的档位互撞:
// TP1 (ladder_tp @488.8636) 与 BE1 (break_even_stop @488.6686) 相距 0.0399%,
// 远小于 protectionPriceTolerancePct = 0.2%。按价格匹配时,先到的那张会吃掉另一张
// 的槽位;真正的主人随后匹配不上,被判 stale_bot_duplicate 并撤单。
func TestConsumeAllowedProtectionSlotRejectsCrossRole(t *testing.T) {
	slots := []allowedProtectionSlot{{Price: 488.8636, Role: store.MechLadderTP}}

	// 一张保本止损(不同机制)撞在同一价位上,不得吃掉 ladder_tp 的槽位。
	if consumeAllowedProtectionSlot(&slots, 488.6686, store.MechBreakEven) {
		t.Fatal("break_even order consumed the ladder_tp slot; cross-role collision not blocked")
	}
	if len(slots) != 1 {
		t.Fatalf("slot was removed despite role mismatch: %+v", slots)
	}

	// 真正的主人仍然能认领。
	if !consumeAllowedProtectionSlot(&slots, 488.8636, store.MechLadderTP) {
		t.Fatal("ladder_tp order failed to claim its own slot")
	}
	if len(slots) != 0 {
		t.Fatalf("slot not consumed: %+v", slots)
	}
}

// 角色未知(历史挂单/无法解码的 clientOrderID)必须退回按价格匹配 —— 严格化不能
// 把改造前能认领的单变成"多余单"。
func TestConsumeAllowedProtectionSlotUnknownRoleFallsBackToPrice(t *testing.T) {
	slots := []allowedProtectionSlot{{Price: 100, Role: store.MechLadderSL}}
	if !consumeAllowedProtectionSlot(&slots, 100.05, "") {
		t.Fatal("unknown-role order was rejected; must degrade to price-only match")
	}
	if len(slots) != 0 {
		t.Fatalf("slot not consumed: %+v", slots)
	}
}

// 槽位没有记录角色(容忍名单)时,任何角色都能认领。
func TestConsumeAllowedProtectionSlotAnyRoleSlot(t *testing.T) {
	slots := []allowedProtectionSlot{{Price: 200}}
	if !consumeAllowedProtectionSlot(&slots, 200, store.MechManagedDrawdown) {
		t.Fatal("role-less slot rejected a known-role order")
	}
}

// 同价位同时存在两个槽位时,必须优先按角色配对,而不是按顺序取第一个 —— 否则
// 先遍历到的槽位会被错认,把另一张单挤成多余单。
func TestConsumeAllowedProtectionSlotPrefersRoleOverOrder(t *testing.T) {
	slots := []allowedProtectionSlot{
		{Price: 500.0323, Role: store.MechLadderTP},
		{Price: 500.0323, Role: store.MechManagedDrawdown},
	}
	if !consumeAllowedProtectionSlot(&slots, 500.0323, store.MechManagedDrawdown) {
		t.Fatal("managed_drawdown order failed to claim its slot")
	}
	if len(slots) != 1 || slots[0].Role != store.MechLadderTP {
		t.Fatalf("wrong slot consumed: %+v", slots)
	}
	if !consumeAllowedProtectionSlot(&slots, 500.0323, store.MechLadderTP) {
		t.Fatal("ladder_tp order failed to claim the remaining slot")
	}
}

// protectionRoleOfOrder 只认注册过的机制。enrichProtectionOrders 会把空的
// ProtectionRole 回填成粗粒度桶("stop_loss"/"take_profit"/...),若把它们当机制,
// 每张止损都会变成"机制 stop_loss",匹配不到任何槽位而被判多余。
func TestProtectionRoleOfOrderIgnoresCoarseBuckets(t *testing.T) {
	for _, coarse := range []string{"stop_loss", "take_profit", "trailing", "unknown", ""} {
		if got := protectionRoleOfOrder(OpenOrder{ProtectionRole: coarse}); got != "" {
			t.Fatalf("coarse role %q leaked through as mechanism %q", coarse, got)
		}
	}
	if got := protectionRoleOfOrder(OpenOrder{ProtectionRole: "ladder_tp"}); got != store.MechLadderTP {
		t.Fatalf("registered mechanism lost: got %q", got)
	}
	// 动态变体要折叠到基机制上(managed_drawdown_stage2 → managed_drawdown)。
	if got := protectionRoleOfOrder(OpenOrder{ProtectionRole: "managed_drawdown_stage2"}); got != store.MechManagedDrawdown {
		t.Fatalf("dynamic variant not normalized: got %q", got)
	}
}

// 端到端:一张活的 ladder_tp 与保本止损同价位共存时,不得被判 stale_bot_duplicate。
func TestClassifyKeepsLadderTPCollidingWithBreakEvenStop(t *testing.T) {
	plan := &ProtectionPlan{
		TakeProfitOrders: []ProtectionOrder{{Price: 488.8636, CloseRatioPct: 20}},
		NeedsTakeProfit:  true,
	}
	orders := []OpenOrder{
		// Binance 的 maker TP 是 post-only reduce-only LIMIT,但适配器已把 Type 上报为
		// TAKE_PROFIT(见 futures_orders.go 的 isMakerTakeProfitLimit),否则共享代码
		// 会把它当成缺失的 TP 每轮重挂。
		{OrderID: "1984555938", Type: "TAKE_PROFIT", Price: 488.8636, PositionSide: "LONG",
			ProtectionRole: store.MechLadderTP},
	}
	summary := classifyUnexpectedProtectionOrders(orders, "LONG", plan, breakEvenOwnership{}, nativeTrailingOwnership{}, true)
	if summary.StaleBotDuplicate != 0 {
		t.Fatalf("live ladder_tp classified stale_bot_duplicate: %+v", summary)
	}
	if summary.ExpectedStaticOwner != 1 {
		t.Fatalf("ladder_tp not recognized as static owner: %+v", summary)
	}
}
