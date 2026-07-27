package trader

import "testing"

// venueWithoutPriceTargetedTPCancel 模拟除 OKX 之外的交易所:没有
// CancelTakeProfitOrdersByPrices,只有广撤的 CancelTakeProfitOrders。
type venueWithoutPriceTargetedTPCancel struct {
	fakeReconcileTrader
	broadTPCalls []string
}

func (v *venueWithoutPriceTargetedTPCancel) CancelTakeProfitOrders(symbol string) error {
	v.broadTPCalls = append(v.broadTPCalls, symbol)
	return nil
}

// 原实现在交易所不支持按价格定向撤单时,退化为 CancelTakeProfitOrders(symbol) ——
// 把该 symbol 上**所有**止盈单(包括健康的档位止盈)一并撤掉,而这个函数的契约
// 只是"撤掉失败的回撤单"。只有 OKX 实现了定向接口,所以其余所有交易所都会走到
// 这条广撤分支。现在必须跳过而不是广撤。
func TestOrphanedDrawdownCleanupNeverBroadCancelsTakeProfits(t *testing.T) {
	venue := &venueWithoutPriceTargetedTPCancel{}
	at := &AutoTrader{trader: venue}

	plan := &ProtectionPlan{
		TakeProfitOrders: []ProtectionOrder{
			{Price: 1990.0, CloseRatioPct: 20},
			{Price: 2010.0, CloseRatioPct: 18},
		},
	}

	at.cancelOrphanedDrawdownOrders("ETHUSDT", plan)

	if len(venue.broadTPCalls) != 0 {
		t.Fatalf("不支持定向撤单的交易所必须跳过,不能广撤所有止盈单,got %v", venue.broadTPCalls)
	}
}

// 支持定向撤单时,必须只撤计划里的那些价格。
type venueWithPriceTargetedTPCancel struct {
	fakeReconcileTrader
	targetedCalls [][]float64
	broadTPCalls  []string
}

func (v *venueWithPriceTargetedTPCancel) CancelTakeProfitOrdersByPrices(symbol string, prices []float64) error {
	v.targetedCalls = append(v.targetedCalls, prices)
	return nil
}

func (v *venueWithPriceTargetedTPCancel) CancelTakeProfitOrders(symbol string) error {
	v.broadTPCalls = append(v.broadTPCalls, symbol)
	return nil
}

func TestOrphanedDrawdownCleanupUsesTargetedPricesWhenAvailable(t *testing.T) {
	venue := &venueWithPriceTargetedTPCancel{}
	at := &AutoTrader{trader: venue}

	plan := &ProtectionPlan{
		TakeProfitOrders: []ProtectionOrder{
			{Price: 1990.0, CloseRatioPct: 20},
			{Price: 2010.0, CloseRatioPct: 18},
		},
	}

	at.cancelOrphanedDrawdownOrders("ETHUSDT", plan)

	if len(venue.targetedCalls) != 1 || len(venue.targetedCalls[0]) != 2 {
		t.Fatalf("应按计划价格定向撤单,got %v", venue.targetedCalls)
	}
	if len(venue.broadTPCalls) != 0 {
		t.Fatalf("定向撤单成功后不得再广撤,got %v", venue.broadTPCalls)
	}
}
