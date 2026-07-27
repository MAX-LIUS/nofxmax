package trader

import "testing"

// 落库归因回归:适配器从 algoClOrdId / clientOrderID 解出的**细粒度**归因
// (break_even / ladder_sl / fallback_maxloss / structural_sl …)是交易所侧唯一
// 可靠的归因来源。enrichProtectionOrders 早先无条件用四类粗分类覆写它,落库和
// 面板都只剩"这是个止损",分不清保本 / 阶梯 / 兜底 / 结构位。
func TestEnrichProtectionOrdersPreservesFineGrainedRole(t *testing.T) {
	at := &AutoTrader{}
	orders := []OpenOrder{
		{OrderID: "1", Type: "STOP_MARKET", Side: "SELL", PositionSide: "LONG",
			StopPrice: 100, ProtectionRole: "break_even"},
		{OrderID: "2", Type: "STOP_MARKET", Side: "SELL", PositionSide: "LONG",
			StopPrice: 95, ProtectionRole: "fallback_maxloss"},
		{OrderID: "3", Type: "TAKE_PROFIT_MARKET", Side: "SELL", PositionSide: "LONG",
			StopPrice: 110, ProtectionRole: "ladder_tp"},
		{OrderID: "4", Type: "STOP_MARKET", Side: "SELL", PositionSide: "LONG",
			StopPrice: 97, ProtectionRole: "structural_sl"},
		// 适配器没给归因(binance 路径),这时才回退按订单类型粗推。
		{OrderID: "5", Type: "TRAILING_STOP_MARKET", Side: "SELL", PositionSide: "LONG",
			ActivationPrice: 106},
		{OrderID: "6", Type: "STOP_MARKET", Side: "SELL", PositionSide: "LONG",
			StopPrice: 93},
	}

	got := at.enrichProtectionOrders(orders)
	if len(got) != len(orders) {
		t.Fatalf("len = %d, want %d", len(got), len(orders))
	}

	wantFine := map[string]string{
		"1": "break_even", "2": "fallback_maxloss", "3": "ladder_tp",
		"4": "structural_sl", "5": "trailing", "6": "stop_loss",
	}
	// 粗粒度并存,给只需要"止损还是止盈"的消费者(阶梯/兜底计数)用。
	wantCoarse := map[string]string{
		"1": "stop_loss", "2": "stop_loss", "3": "take_profit",
		"4": "stop_loss", "5": "trailing", "6": "stop_loss",
	}
	for _, o := range got {
		if o.ProtectionRole != wantFine[o.OrderID] {
			t.Errorf("order %s fine role = %q, want %q", o.OrderID, o.ProtectionRole, wantFine[o.OrderID])
		}
		if o.ProtectionRoleCoarse != wantCoarse[o.OrderID] {
			t.Errorf("order %s coarse role = %q, want %q", o.OrderID, o.ProtectionRoleCoarse, wantCoarse[o.OrderID])
		}
		if o.ProtectionStatus == "" {
			t.Errorf("order %s status not enriched", o.OrderID)
		}
	}
}

// 幂等:enrich 跑第二遍不能把细粒度归因洗成粗粒度(同一批挂单会被多条读路径
// 反复 enrich)。
func TestEnrichProtectionOrdersIsIdempotent(t *testing.T) {
	at := &AutoTrader{}
	orders := []OpenOrder{
		{OrderID: "1", Type: "STOP_MARKET", Side: "SELL", PositionSide: "LONG",
			StopPrice: 100, ProtectionRole: "break_even"},
	}
	once := at.enrichProtectionOrders(orders)
	twice := at.enrichProtectionOrders(once)
	if twice[0].ProtectionRole != "break_even" {
		t.Fatalf("fine role lost on second pass: %q", twice[0].ProtectionRole)
	}
	if twice[0].ProtectionRoleCoarse != "stop_loss" {
		t.Fatalf("coarse role = %q, want stop_loss", twice[0].ProtectionRoleCoarse)
	}
}
