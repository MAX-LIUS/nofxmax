package trader

import "testing"

func TestReconcilerOwnershipZeroOrdersCannotBeVerified(t *testing.T) {
	plan := &ProtectionPlan{NeedsStopLoss: true, StopLossPrice: 98, NeedsTakeProfit: true, TakeProfitPrice: 110}
	state := evaluateProtectionOwnership(nil, "SHORT", plan, breakEvenArmedOnly(false), nativeTrailingArmedOnly(false))
	if state.Verified {
		t.Fatalf("zero open orders must not verify protection ownership: %+v", state)
	}
	if state.State != "unprotected" || !state.MissingStop || !state.MissingProfit {
		t.Fatalf("expected explicit unprotected missing stop/profit state, got %+v", state)
	}
}

func TestReconcilerOwnershipDrawdownDoesNotSatisfyStopOwner(t *testing.T) {
	plan := &ProtectionPlan{NeedsStopLoss: true, StopLossPrice: 98, NeedsTakeProfit: true, TakeProfitPrice: 110}
	// armed 要成为 profit owner,必须有活 trailing 单背书(见 hasLiveTrailingOrder):
	// 没有活单的 armed 是幻影覆盖,不再有资格当 owner。这里给一张,以便测试的本意
	// ——"盈利侧有主人也不能顶替止损侧主人"——仍然被检验到。
	orders := []OpenOrder{{PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET", Quantity: 1, ActivationPrice: 94, CallbackRate: 1.0}}
	state := evaluateProtectionOwnership(orders, "SHORT", plan, breakEvenArmedOnly(false), nativeTrailingArmedOnly(true))
	if state.Verified {
		t.Fatalf("drawdown profit owner must not verify missing stop owner: %+v", state)
	}
	if state.StopOwner != "" || state.ProfitOwner != "drawdown" || !state.MissingStop || state.MissingProfit {
		t.Fatalf("expected drawdown-only degraded ownership, got %+v", state)
	}
}
