package trader

import (
	"fmt"
	"strings"
)

// ProtectionOwnershipState is a pure, exchange-agnostic summary of whether an
// active position has the minimum expected protection owners visible.
//
// This is intentionally not wired into live reconciliation yet. It is the testable
// ownership model that should replace scattered reconciler conditionals after the
// matrix is green.
type ProtectionOwnershipState struct {
	StaticOwner       string
	ProfitOwner       string
	StopOwner         string
	State             string
	Verified          bool
	MissingStop       bool
	MissingProfit     bool
	UnexpectedStops   int
	UnexpectedProfits int
	Reasons           []string
}

// hasLiveTrailingOrder 报告交易所此刻是否真的躺着至少一张本仓位方向的 trailing 单。
//
// 为什么归属判定必须要它:armed 是 DB 里的一个布尔量,它只说明"我们曾经武装过",
// 不说明"单子现在还在"。单子被撤掉/被交易所拒掉/被熔断改写归属之后,armed 依然
// 是 true —— 于是 missingProfit 被抹平,reconciler 每轮都报"盈利侧有主人",而交易所
// 上一张 DD 单都没有。2026-07-28 GPT/SKHYNIXUSDT 就是这个形态:账本 claimedTrail=1、
// 交易所 trail=0,峰值 8.11% 越过两档 DD 却无任何委托。
//
// 判据只用订单类型,不看激活状态:pending_activation 的单仍然是在场的保护(幻影单
// 的有效性由 exchangeSideCoversDrawdownTier 单独判定,不属于"在不在场"的问题)。
func hasLiveTrailingOrder(openOrders []OpenOrder, positionSide string) bool {
	for _, o := range openOrders {
		if !strings.Contains(strings.ToUpper(o.Type), "TRAILING") {
			continue
		}
		if o.Quantity <= 0 {
			continue
		}
		// PositionSide 为空表示交易所是单向持仓模式(不回该字段),此时不能按方向排除。
		if o.PositionSide != "" && !strings.EqualFold(o.PositionSide, positionSide) {
			continue
		}
		return true
	}
	return false
}

func evaluateProtectionOwnership(openOrders []OpenOrder, positionSide string, plan *ProtectionPlan, beOwnership breakEvenOwnership, trailingOwnership nativeTrailingOwnership) ProtectionOwnershipState {
	state := ProtectionOwnershipState{State: "unprotected"}
	positionSide = strings.ToUpper(positionSide)
	// 归属判断("盈利侧有没有主人")看的是"有没有武装",而不是"哪张单是我的"。
	// 认领集合只用于区分多余单,不参与 owner 判定 —— 否则记录落盘窗口期会瞬间
	// 判成 missingProfit 并触发一轮无谓的重挂。
	//
	// 但 armed 必须被"交易所真有活单"背书(见 hasLiveTrailingOrder):没有活单的 armed
	// 是幻影覆盖,它会抹掉 missingTP 让缺口永远不被上报。落盘窗口期不受影响 ——
	// 那个窗口里单子已经在交易所了,只是记录还没落库,活单判据照样为真。
	nativeTrailingArmed := trailingOwnership.Armed && hasLiveTrailingOrder(openOrders, positionSide)
	if trailingOwnership.Armed && !nativeTrailingArmed {
		state.Reasons = append(state.Reasons, "native trailing armed in ledger but no live trailing order on exchange (phantom coverage)")
	}
	// owner 判定只看"有没有武装",与认领集合无关(同 trailing:落盘窗口期不能瞬间判缺)。
	breakEvenArmed := beOwnership.Armed

	if plan == nil {
		if breakEvenArmed || nativeTrailingArmed {
			state.State = "protected"
			state.Verified = true
			if breakEvenArmed {
				state.StopOwner = "breakeven"
			}
			if nativeTrailingArmed {
				state.ProfitOwner = "drawdown"
			}
			return state
		}
		state.Reasons = append(state.Reasons, "no protection plan and no armed native owner")
		state.MissingStop = true
		return state
	}

	missingSL, missingTP := detectMissingProtection(openOrders, positionSide, plan, false)
	unexpectedSL, unexpectedTP := detectUnexpectedProtectionOrders(openOrders, positionSide, plan, beOwnership, trailingOwnership)
	state.MissingStop = missingSL && !breakEvenArmed
	state.MissingProfit = missingTP && !nativeTrailingArmed
	state.UnexpectedStops = unexpectedSL
	state.UnexpectedProfits = unexpectedTP

	if breakEvenArmed {
		state.StopOwner = "breakeven"
	} else {
		state.StopOwner = visiblePlanStopOwnerFromOrders(openOrders, positionSide, plan)
	}

	if nativeTrailingArmed {
		state.ProfitOwner = "drawdown"
	} else if hasVisiblePlanProfitOwner(openOrders, positionSide, plan) {
		state.ProfitOwner = visiblePlanProfitOwner(plan)
	}

	if len(plan.StopLossOrders) > 0 || (plan.NeedsStopLoss && plan.StopLossPrice > 0) || plan.FallbackMaxLossPrice > 0 {
		state.StaticOwner = state.StopOwner
	}

	if state.StopOwner == "" {
		state.Reasons = append(state.Reasons, "missing stop/fallback owner")
	}
	if planRequiresProfitOwner(plan) && state.ProfitOwner == "" {
		state.Reasons = append(state.Reasons, "missing profit owner")
	}
	if unexpectedSL > 0 || unexpectedTP > 0 {
		state.Reasons = append(state.Reasons, fmt.Sprintf("unexpected protection orders sl=%d tp=%d", unexpectedSL, unexpectedTP))
	}

	state.Verified = state.StopOwner != "" && !state.MissingStop && (!planRequiresProfitOwner(plan) || state.ProfitOwner != "") && unexpectedSL == 0 && unexpectedTP == 0
	if state.Verified {
		state.State = "protected"
	} else if state.StopOwner != "" || state.ProfitOwner != "" {
		state.State = "degraded"
	}
	return state
}

// protectionCoverageComplete 是"多余单已处理完之后,本仓位的保护覆盖是否完整"的**唯一**判据。
//
// 为什么必须收成一个函数:reconciler 里有两条分支会把 state 从 degraded 翻回
// protected/verified —— 容忍分支(多一张止损单只是多一层保险)和 reclaim 分支
// (多余单其实是本仓位的活 DD 档,已被认领回来)。两条分支原本各自内联了一份同样的
// 布尔式,注释写着"同源"其实并不同源;任何一天有人只改一处,两条路就会对同一个仓位
// 给出不同结论,而这类分歧在日志上表现为"verified 时有时无",极难查。
//
// 判据(四项全中才算完整):
//   - 有止损主人,且止损不缺 —— 这是底线,缺止损绝不允许标 verified;
//   - 没有多余止损单、没有多余止盈单 —— 还有没归属的单说明清理没做完;
//   - 计划若要求止盈主人,则止盈主人必须在场 —— SKHYNIXUSDT 那 58 轮
//     "missing profit owner" 不能因为止损单被认领就被标成已验证。
func protectionCoverageComplete(ownership ProtectionOwnershipState, plan *ProtectionPlan, missingSL bool, unexpectedStops, unexpectedTPs int) bool {
	if missingSL || ownership.StopOwner == "" {
		return false
	}
	if unexpectedStops != 0 || unexpectedTPs != 0 {
		return false
	}
	if planRequiresProfitOwner(plan) && ownership.ProfitOwner == "" {
		return false
	}
	return true
}

func planRequiresProfitOwner(plan *ProtectionPlan) bool {
	if plan == nil {
		return false
	}
	return len(plan.TakeProfitOrders) > 0 || (plan.NeedsTakeProfit && plan.TakeProfitPrice > 0)
}

func visiblePlanStopOwnerFromOrders(openOrders []OpenOrder, positionSide string, plan *ProtectionPlan) string {
	if plan == nil {
		return ""
	}
	for _, target := range plan.StopLossOrders {
		if hasMatchingProtectionOrder(openOrders, positionSide, false, target.Price) {
			return "ladder_sl"
		}
	}
	if plan.NeedsStopLoss && plan.StopLossPrice > 0 && hasMatchingProtectionOrder(openOrders, positionSide, false, plan.StopLossPrice) {
		return "full_sl"
	}
	if plan.FallbackMaxLossPrice > 0 && hasMatchingProtectionOrder(openOrders, positionSide, false, plan.FallbackMaxLossPrice) {
		return "fallback"
	}
	if visibleFallbackOwnerSatisfied(openOrders, positionSide) {
		return "fallback"
	}
	return ""
}

func hasVisiblePlanProfitOwner(openOrders []OpenOrder, positionSide string, plan *ProtectionPlan) bool {
	if plan == nil {
		return false
	}
	for _, target := range plan.TakeProfitOrders {
		if hasMatchingProtectionOrder(openOrders, positionSide, true, target.Price) {
			return true
		}
	}
	return plan.NeedsTakeProfit && plan.TakeProfitPrice > 0 && hasMatchingProtectionOrder(openOrders, positionSide, true, plan.TakeProfitPrice)
}

func visiblePlanStopOwner(plan *ProtectionPlan) string {
	if plan == nil {
		return ""
	}
	if len(plan.StopLossOrders) > 0 {
		return "ladder_sl"
	}
	if plan.NeedsStopLoss && plan.StopLossPrice > 0 {
		return "full_sl"
	}
	if plan.FallbackMaxLossPrice > 0 {
		return "fallback"
	}
	return ""
}

func visiblePlanProfitOwner(plan *ProtectionPlan) string {
	if plan == nil {
		return ""
	}
	if len(plan.TakeProfitOrders) > 0 {
		return "ladder_tp"
	}
	if plan.NeedsTakeProfit && plan.TakeProfitPrice > 0 {
		return "full_tp"
	}
	return ""
}
