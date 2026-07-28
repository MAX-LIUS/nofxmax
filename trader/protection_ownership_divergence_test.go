package trader

import "testing"

// 🧭 日志曾经打 ownership.MissingProfit,而真正驱动"重挂整条计划"的是
// detectMissingProtection 的原始 missingTP。两者在 trailing armed 时必然分歧,
// 于是日志会在**正在重挂的那一轮**读作 missingTP=false —— 排查 ZEC churn 时最大的
// 误导源。这组测试把分歧本身钉成一个已知事实,并锁住"两个值都要能取到"这个前提,
// 使日志格式无法再退回只打一个。
func TestOwnershipMissingProfitIsMaskedByArmedTrailing(t *testing.T) {
	// 计划要求一个止盈档,但交易所上没有对应价位的止盈单。
	plan := &ProtectionPlan{
		NeedsStopLoss:   true,
		StopLossPrice:   98,
		NeedsTakeProfit: true,
		TakeProfitPrice: 110,
	}
	orders := []OpenOrder{
		{PositionSide: "LONG", Type: "STOP_MARKET", StopPrice: 98},
	}

	// 原始判定:止盈价位确实不在场 —— 这是驱动重挂分支的那个值。
	_, missingTP := detectMissingProtection(orders, "LONG", plan, false)
	if !missingTP {
		t.Fatalf("detectMissingProtection 应报 missingTP=true(110 的止盈单不在场)")
	}

	// 有 trailing armed 时,ownership 按 `missingTP && !nativeTrailingArmed` 掩蔽,
	// 于是同一时刻 ownership.MissingProfit 为 false。
	armed := evaluateProtectionOwnership(orders, "LONG", plan, false, nativeTrailingArmedOnly(true))
	if armed.MissingProfit {
		t.Fatalf("trailing armed 时 ownership.MissingProfit 应被掩蔽为 false,实得 %+v", armed)
	}
	if armed.ProfitOwner != "drawdown" {
		t.Fatalf("armed trailing 应作为 profit owner,实得 %q", armed.ProfitOwner)
	}

	// 这就是分歧:一个 true 一个 false,同一批入参、同一轮。日志必须同时呈现。
	if missingTP == armed.MissingProfit {
		t.Fatalf("本测试的前提是两者分歧;若已收口一致,请同步更新 reconciler 日志与本测试")
	}

	// 没有 armed trailing 时两者应当一致 —— 掩蔽只发生在 armed 情况下。
	notArmed := evaluateProtectionOwnership(orders, "LONG", plan, false, nativeTrailingArmedOnly(false))
	if notArmed.MissingProfit != missingTP {
		t.Fatalf("无 armed trailing 时两者应一致,实得 ownership=%t detect=%t", notArmed.MissingProfit, missingTP)
	}
}

// 止损侧同理:breakEvenArmed 会掩蔽 MissingStop。reconciler 调
// detectMissingProtection 时传的是真实的 breakEvenArmed,而 ownership 内部固定传
// false 再自己掩蔽 —— 两条路径的入参就不一样,不能假设它们等价。
func TestOwnershipMissingStopMaskedByBreakEven(t *testing.T) {
	plan := &ProtectionPlan{NeedsStopLoss: true, StopLossPrice: 98}
	var orders []OpenOrder

	missingSL, _ := detectMissingProtection(orders, "LONG", plan, false)
	if !missingSL {
		t.Fatalf("止损单不在场时应报 missingSL=true")
	}

	state := evaluateProtectionOwnership(orders, "LONG", plan, true, nativeTrailingArmedOnly(false))
	if state.MissingStop {
		t.Fatalf("breakEvenArmed 应掩蔽 MissingStop,实得 %+v", state)
	}
	if state.StopOwner != "breakeven" {
		t.Fatalf("breakEvenArmed 应成为 stop owner,实得 %q", state.StopOwner)
	}
}
