package trader

import (
	"strings"
	"testing"
)

func TestEvaluateProtectionOwnership_FullManualProtected(t *testing.T) {
	plan := &ProtectionPlan{NeedsStopLoss: true, StopLossPrice: 98, NeedsTakeProfit: true, TakeProfitPrice: 110}
	orders := []OpenOrder{
		{PositionSide: "LONG", Type: "STOP_MARKET", StopPrice: 98},
		{PositionSide: "LONG", Type: "TAKE_PROFIT_MARKET", StopPrice: 110},
	}

	state := evaluateProtectionOwnership(orders, "LONG", plan, breakEvenArmedOnly(false), nativeTrailingArmedOnly(false))
	if !state.Verified || state.State != "protected" || state.StopOwner != "full_sl" || state.ProfitOwner != "full_tp" {
		t.Fatalf("unexpected ownership state: %+v", state)
	}
}

func TestEvaluateProtectionOwnership_LadderManualProtected(t *testing.T) {
	plan := &ProtectionPlan{
		NeedsStopLoss:    true,
		NeedsTakeProfit:  true,
		StopLossOrders:   []ProtectionOrder{{Price: 98, CloseRatioPct: 50}, {Price: 96, CloseRatioPct: 50}},
		TakeProfitOrders: []ProtectionOrder{{Price: 105, CloseRatioPct: 50}, {Price: 110, CloseRatioPct: 50}},
	}
	orders := []OpenOrder{
		{PositionSide: "LONG", Type: "STOP_MARKET", StopPrice: 98},
		{PositionSide: "LONG", Type: "STOP_MARKET", StopPrice: 96},
		{PositionSide: "LONG", Type: "TAKE_PROFIT_MARKET", StopPrice: 105},
		{PositionSide: "LONG", Type: "TAKE_PROFIT_MARKET", StopPrice: 110},
	}

	state := evaluateProtectionOwnership(orders, "LONG", plan, breakEvenArmedOnly(false), nativeTrailingArmedOnly(false))
	if !state.Verified || state.StopOwner != "ladder_sl" || state.ProfitOwner != "ladder_tp" {
		t.Fatalf("unexpected ownership state: %+v", state)
	}
}

func TestEvaluateProtectionOwnership_ActivePositionWithZeroOrdersIsUnprotected(t *testing.T) {
	plan := &ProtectionPlan{NeedsStopLoss: true, StopLossPrice: 98, NeedsTakeProfit: true, TakeProfitPrice: 110}
	state := evaluateProtectionOwnership(nil, "LONG", plan, breakEvenArmedOnly(false), nativeTrailingArmedOnly(false))
	if state.Verified || state.State != "unprotected" || !state.MissingStop || !state.MissingProfit {
		t.Fatalf("expected unprotected zero-order state, got %+v", state)
	}
}

func TestEvaluateProtectionOwnership_DrawdownArmedCanOwnProfitButNotStop(t *testing.T) {
	plan := &ProtectionPlan{NeedsStopLoss: true, StopLossPrice: 98, NeedsTakeProfit: true, TakeProfitPrice: 110}
	// TRAILING 单是 armed 的活单背书;它的价位与计划 TP(110)不同,所以不会满足
	// detectMissingProtection 的 TP 匹配 —— 掩蔽语义仍然是被检验的那一条。
	orders := []OpenOrder{
		{PositionSide: "LONG", Type: "STOP_MARKET", StopPrice: 98},
		{PositionSide: "LONG", Type: "TRAILING_STOP_MARKET", Quantity: 1, ActivationPrice: 106, CallbackRate: 1.0},
	}

	state := evaluateProtectionOwnership(orders, "LONG", plan, breakEvenArmedOnly(false), nativeTrailingArmedOnly(true))
	if !state.Verified || state.StopOwner != "full_sl" || state.ProfitOwner != "drawdown" {
		t.Fatalf("expected drawdown to own profit and full SL to own stop, got %+v", state)
	}
}

func TestEvaluateProtectionOwnership_DrawdownArmedWithoutStopIsNotVerified(t *testing.T) {
	plan := &ProtectionPlan{NeedsStopLoss: true, StopLossPrice: 98, NeedsTakeProfit: true, TakeProfitPrice: 110}
	orders := []OpenOrder{{PositionSide: "LONG", Type: "TRAILING_STOP_MARKET", Quantity: 1, ActivationPrice: 106, CallbackRate: 1.0}}
	state := evaluateProtectionOwnership(orders, "LONG", plan, breakEvenArmedOnly(false), nativeTrailingArmedOnly(true))
	if state.Verified || state.ProfitOwner != "drawdown" || state.StopOwner != "" {
		t.Fatalf("expected drawdown profit owner without stop to be degraded/unprotected, got %+v", state)
	}
}

// 幻影覆盖:账本 armed=true,交易所零 trailing 单。修复前 armed 会把 missingTP 抹平,
// 于是 reconciler 每轮报"盈利侧有主人",而交易所上一张 DD 单都没有(2026-07-28
// GPT/SKHYNIXUSDT:峰值 8.11% 越过两档 DD,交易所零委托)。修复后必须暴露成缺口。
func TestEvaluateProtectionOwnership_ArmedWithoutLiveTrailingIsPhantom(t *testing.T) {
	plan := &ProtectionPlan{NeedsStopLoss: true, StopLossPrice: 98, NeedsTakeProfit: true, TakeProfitPrice: 110}
	// 只有止损单在场,没有任何 trailing 单 —— 但账本说 native trailing 已武装。
	orders := []OpenOrder{{PositionSide: "LONG", Type: "STOP_MARKET", StopPrice: 98}}

	state := evaluateProtectionOwnership(orders, "LONG", plan, breakEvenArmedOnly(false), nativeTrailingArmedOnly(true))
	if !state.MissingProfit {
		t.Fatalf("账本 armed 但交易所无活 trailing 单时,MissingProfit 必须为 true(幻影覆盖不得掩蔽缺口),实得 %+v", state)
	}
	if state.ProfitOwner == "drawdown" {
		t.Fatalf("幻影 armed 不得充当 profit owner,实得 %+v", state)
	}
	if state.Verified {
		t.Fatalf("幻影覆盖下不得判 verified,实得 %+v", state)
	}
	// 必须在 reasons 里自证,否则线上只能靠读源码才知道为什么突然报缺。
	found := false
	for _, r := range state.Reasons {
		if strings.Contains(r, "phantom coverage") {
			found = true
		}
	}
	if !found {
		t.Fatalf("应给出 phantom coverage 原因,实得 reasons=%v", state.Reasons)
	}
}

// 反向锁死:交易所真有活 trailing 单时,掩蔽语义必须照旧生效 —— 这条防止修复过头,
// 把落盘窗口期/正常武装误判成缺口而触发无谓重挂。
func TestEvaluateProtectionOwnership_ArmedWithLiveTrailingStillMasks(t *testing.T) {
	plan := &ProtectionPlan{NeedsStopLoss: true, StopLossPrice: 98, NeedsTakeProfit: true, TakeProfitPrice: 110}
	orders := []OpenOrder{
		{PositionSide: "LONG", Type: "STOP_MARKET", StopPrice: 98},
		{PositionSide: "LONG", Type: "TRAILING_STOP_MARKET", Quantity: 1, ActivationPrice: 106, CallbackRate: 1.0},
	}

	state := evaluateProtectionOwnership(orders, "LONG", plan, breakEvenArmedOnly(false), nativeTrailingArmedOnly(true))
	if state.MissingProfit {
		t.Fatalf("有活 trailing 单时掩蔽语义应照旧,MissingProfit 应为 false,实得 %+v", state)
	}
	if state.ProfitOwner != "drawdown" {
		t.Fatalf("有活 trailing 单的 armed 应作为 profit owner,实得 %+v", state)
	}
}

// pending_activation(未激活)的 trailing 单仍然"在场":幻影单的**有效性**由
// exchangeSideCoversDrawdownTier 单独判定,不属于"在不在场"的问题。这两层判据
// 必须分开,否则未激活单会被当成不存在而触发重挂,制造重复单。
func TestHasLiveTrailingOrder_PendingActivationCounts(t *testing.T) {
	orders := []OpenOrder{{PositionSide: "LONG", Type: "TRAILING_STOP_MARKET", Quantity: 1, ActivationStatus: "pending_activation", ActivationPrice: 106}}
	if !hasLiveTrailingOrder(orders, "LONG") {
		t.Fatalf("pending_activation 的 trailing 单应算在场")
	}
	// 方向不符的单不算(双向持仓模式)。
	if hasLiveTrailingOrder(orders, "SHORT") {
		t.Fatalf("LONG 的 trailing 单不应算作 SHORT 的在场保护")
	}
	// PositionSide 为空 = 单向持仓交易所不回该字段,不能按方向排除。
	oneWay := []OpenOrder{{Type: "TRAILING_STOP_MARKET", Quantity: 1}}
	if !hasLiveTrailingOrder(oneWay, "SHORT") {
		t.Fatalf("PositionSide 为空时应容忍(单向持仓模式)")
	}
	// 数量为 0 的残留记录不算在场保护。
	zeroQty := []OpenOrder{{PositionSide: "LONG", Type: "TRAILING_STOP_MARKET", Quantity: 0}}
	if hasLiveTrailingOrder(zeroQty, "LONG") {
		t.Fatalf("数量为 0 的 trailing 单不应算在场")
	}
}

func TestEvaluateProtectionOwnership_BreakEvenOwnsStopAndLadderOwnsProfit(t *testing.T) {
	plan := &ProtectionPlan{
		NeedsStopLoss:    true,
		NeedsTakeProfit:  true,
		StopLossOrders:   []ProtectionOrder{{Price: 98, CloseRatioPct: 100}},
		TakeProfitOrders: []ProtectionOrder{{Price: 110, CloseRatioPct: 100}},
	}
	orders := []OpenOrder{{PositionSide: "LONG", Type: "TAKE_PROFIT_MARKET", StopPrice: 110}}
	state := evaluateProtectionOwnership(orders, "LONG", plan, breakEvenArmedOnly(true), nativeTrailingArmedOnly(false))
	if !state.Verified || state.StopOwner != "breakeven" || state.ProfitOwner != "ladder_tp" {
		t.Fatalf("expected break-even stop owner and ladder TP owner, got %+v", state)
	}
}

func TestEvaluateProtectionOwnership_FallbackStopOnlyWhenProfitNotRequired(t *testing.T) {
	plan := &ProtectionPlan{FallbackMaxLossPrice: 95}
	orders := []OpenOrder{{PositionSide: "LONG", Type: "STOP_MARKET", StopPrice: 95}}
	state := evaluateProtectionOwnership(orders, "LONG", plan, breakEvenArmedOnly(false), nativeTrailingArmedOnly(false))
	if !state.Verified || state.StopOwner != "fallback" || state.ProfitOwner != "" {
		t.Fatalf("expected fallback-only verified when profit owner not required, got %+v", state)
	}
}

func TestEvaluateProtectionOwnership_FallbackIsReportedAsVisibleOwnerWhenPrimaryStopMissing(t *testing.T) {
	plan := &ProtectionPlan{NeedsStopLoss: true, StopLossPrice: 98, FallbackMaxLossPrice: 95}
	orders := []OpenOrder{{PositionSide: "LONG", Type: "STOP_MARKET", StopPrice: 95}}
	state := evaluateProtectionOwnership(orders, "LONG", plan, breakEvenArmedOnly(false), nativeTrailingArmedOnly(false))
	if !state.Verified || state.StopOwner != "fallback" || state.MissingStop {
		t.Fatalf("expected fallback to satisfy stop ownership when primary stop missing, got %+v", state)
	}
}
