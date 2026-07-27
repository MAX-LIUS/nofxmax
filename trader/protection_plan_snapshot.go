package trader

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"nofx/logger"
	"nofx/market"
	"nofx/store"
)

// snapshotResolvedPlan captures the fully-resolved protection plan at open time as
// ONE duplicate-free row in protection_plan_snapshots. This is the canonical
// "entry protection plan" the history panel reads — distinct from the close_intents
// placement ledger, which re-records the same tier on every reconcile/trailing
// re-arm. Best-effort: any error is logged, never fails the open.
//
// drawdownRules is passed separately because manual drawdown is resolved just
// after the plan is materialized (see applyNativeProtectionTargetsAfterOpen); when
// the plan already carries AI drawdown rules we prefer those.
func (at *AutoTrader) snapshotResolvedPlan(req *protectionExecutionRequest, plan *ProtectionPlan, drawdownRules []store.DrawdownTakeProfitRule) {
	if at == nil || at.store == nil || req == nil {
		return
	}
	// 结构位两级从共享解析器取(面板走同一个函数),否则快照里这道保护档缺失。
	structLevels := at.resolveStructuralSLLevels(req.Symbol, req.PositionSide, req.EntryPrice, 0)
	tiers := buildPlanSnapshotTiers(plan, drawdownRules, req.EntryPrice, req.Action, structLevels)
	if len(tiers) == 0 {
		return
	}
	mode := ""
	if plan != nil {
		mode = plan.Mode
	}
	if err := at.store.ProtectionPlanSnapshot().Save(
		at.id, at.exchangeID, market.Normalize(req.Symbol), req.PositionSide, mode,
		req.EntryPrice, at.cycleNumber, tiers, time.Now().UTC().UnixMilli(),
	); err != nil {
		logger.Warnf("⚠️ Failed to snapshot protection plan for %s %s: %v", req.Symbol, req.PositionSide, err)
	}
}

// buildPlanSnapshotTiers converts a resolved plan (+ effective drawdown rules) into
// the canonical tier list. Mechanism labels MUST match the placement path's reason
// tags (ladder_tp/ladder_sl/full_tp/full_sl/fallback_maxloss_sl/break_even_stop/
// managed_drawdown) so the panel's fired-tier highlight keeps aligning with
// close_events. entryPrice+action anchor the signed % move.
func buildPlanSnapshotTiers(plan *ProtectionPlan, drawdownRules []store.DrawdownTakeProfitRule, entryPrice float64, action string, structLevels structuralSLLevels) []store.ProtectionPlanTier {
	if entryPrice <= 0 {
		return nil
	}
	isLong := action == "open_long"
	out := make([]store.ProtectionPlanTier, 0, 8)

	pct := func(price float64) float64 {
		p := (price - entryPrice) / entryPrice * 100
		if !isLong {
			p = -p
		}
		return roundSnap2(p)
	}
	pricePtr := func(v float64) *float64 { x := v; return &x }
	ratioPtr := func(v float64) *float64 {
		if v <= 0 {
			return nil
		}
		x := v
		return &x
	}

	if plan != nil {
		// Ladder TP tiers (multiple → TP1..TPn). These are the primary source of
		// the old duplicate clutter; here each resolved tier appears exactly once.
		tpLadder := len(plan.TakeProfitOrders) > 0
		for i, o := range plan.TakeProfitOrders {
			label := "TP"
			if len(plan.TakeProfitOrders) > 1 {
				label = fmt.Sprintf("TP%d", i+1)
			}
			out = append(out, store.ProtectionPlanTier{
				Mechanism: store.MechLadderTP, Kind: "tp", Label: label,
				TriggerPct: pct(o.Price), TriggerPrice: pricePtr(o.Price), CloseRatioPct: ratioPtr(o.CloseRatioPct),
			})
		}
		slLadder := len(plan.StopLossOrders) > 0
		for i, o := range plan.StopLossOrders {
			label := "SL"
			if len(plan.StopLossOrders) > 1 {
				label = fmt.Sprintf("SL%d", i+1)
			}
			out = append(out, store.ProtectionPlanTier{
				Mechanism: store.MechLadderSL, Kind: "sl", Label: label,
				TriggerPct: pct(o.Price), TriggerPrice: pricePtr(o.Price), CloseRatioPct: ratioPtr(o.CloseRatioPct),
			})
		}
		// Non-laddered full TP/SL (only when ladder didn't cover that side).
		if !tpLadder && plan.NeedsTakeProfit && plan.TakeProfitPrice > 0 {
			out = append(out, store.ProtectionPlanTier{
				Mechanism: store.MechFullTP, Kind: "tp", Label: "Full TP",
				TriggerPct: pct(plan.TakeProfitPrice), TriggerPrice: pricePtr(plan.TakeProfitPrice),
			})
		}
		if !slLadder && plan.NeedsStopLoss && plan.StopLossPrice > 0 {
			out = append(out, store.ProtectionPlanTier{
				Mechanism: store.MechFullSL, Kind: "sl", Label: "Full SL",
				TriggerPct: pct(plan.StopLossPrice), TriggerPrice: pricePtr(plan.StopLossPrice),
			})
		}
		if !slLadder && !plan.NeedsStopLoss && plan.FallbackMaxLossPrice > 0 {
			out = append(out, store.ProtectionPlanTier{
				Mechanism: store.MechFallbackSL, Kind: "sl", Label: "Fallback SL",
				TriggerPct: pct(plan.FallbackMaxLossPrice), TriggerPrice: pricePtr(plan.FallbackMaxLossPrice),
			})
		}
		// Break-even arm tiers (price = entry + offset%).
		if plan.BreakEvenConfig != nil && plan.BreakEvenConfig.Enabled {
			out = append(out, breakEvenSnapshotTiers(plan.BreakEvenConfig, entryPrice, isLong)...)
		}
	}

	// Drawdown floors (percent-of-peak giveback). 激活价 = entry ×(1 ± minProfit%),
	// 但**成交价**在峰值回吐 callback 之后才发生 —— 两个价位必须都落库,否则回撤档
	// 无法参与按价格的排序(早先只存 TriggerPct、连价格都没有)。
	for i, r := range drawdownRules {
		if r.CloseRatioPct <= 0 {
			continue
		}
		label := "Drawdown"
		if len(drawdownRules) > 1 {
			label = fmt.Sprintf("Drawdown %d", i+1)
		}
		cr := r.CloseRatioPct
		side := "short"
		if isLong {
			side = "long"
		}
		activation := entryPrice * (1 + sign(isLong)*r.MinProfitPct/100)
		callback := calculateDrawdownRuleCallbackRatio(entryPrice, side, r)
		tier := store.ProtectionPlanTier{
			Mechanism: store.MechManagedDrawdown, Kind: "drawdown", Label: label,
			TriggerPct: roundSnap2(r.MinProfitPct), CloseRatioPct: &cr,
			// giveback 用 %.2f:早先是 %.0f,把 1.4142% 印成 "giveback 1%",
			// ATR 换算出来的回撤值(1.8×ATR 之类)全被抹成整数,note 失真。
			Note: fmt.Sprintf("min +%.2f%% · giveback %.2f%%", r.MinProfitPct, r.MaxDrawdownPct),
		}
		if activation > 0 {
			tier.TriggerPrice = pricePtr(activation)
			// 开仓快照:此刻峰值就是入场价,还没有已实现峰值,所以成交价按
			// "峰值最少走到激活价" 这个下限算 —— 即这一档最早可能的成交价。
			if exec := drawdownTierExecutionPrice(side, activation, 0, callback); exec > 0 {
				tier.ExecutionPrice = pricePtr(exec)
				tier.ExecutionPct = pricePtr(pct(exec))
			}
		}
		out = append(out, tier)
	}

	// 结构位止损:区间锚定的两级(收盘确认边界 + 挂在交易所的安全网)。它们是真实
	// 会成交的保护档,早先完全没落库 —— 快照里看不到,也就没参与排序。
	out = append(out, structuralSnapshotTiers(structLevels, pct, pricePtr)...)

	// 按成交价排序:多头由近到远 = 价格由高到低,空头反之。排的是"接下来先碰到
	// 哪一道",所以键必须是成交价(回撤档的激活价会把它顶到远端)。没有价格的档
	// (理论上不该有)沉到末尾,保持稳定顺序。
	sortPlanSnapshotTiers(out, isLong)
	return out
}

// tierSortPrice 取一档用于排序的价格:优先成交价,退回激活/触发价。
func tierSortPrice(t store.ProtectionPlanTier) float64 {
	if t.ExecutionPrice != nil && *t.ExecutionPrice > 0 {
		return *t.ExecutionPrice
	}
	if t.TriggerPrice != nil && *t.TriggerPrice > 0 {
		return *t.TriggerPrice
	}
	return 0
}

func sortPlanSnapshotTiers(tiers []store.ProtectionPlanTier, isLong bool) {
	sort.SliceStable(tiers, func(i, j int) bool {
		pi, pj := tierSortPrice(tiers[i]), tierSortPrice(tiers[j])
		if (pi > 0) != (pj > 0) {
			return pi > 0 // 有价格的排在无价格的前面
		}
		if pi == pj {
			return false // SliceStable 保留原相对顺序
		}
		if isLong {
			return pi > pj
		}
		return pi < pj
	})
}

// structuralSnapshotTiers 落库结构位止损的两级。Phase 2 边界价按 K 线收盘确认才平,
// Phase 1 安全网是真的挂在交易所的单(防宕机/跳空)。
func structuralSnapshotTiers(levels structuralSLLevels,
	pct func(float64) float64, pricePtr func(float64) *float64) []store.ProtectionPlanTier {
	if !levels.Enabled {
		return nil
	}
	out := make([]store.ProtectionPlanTier, 0, 2)
	full := 100.0
	if p := levels.BoundaryPrice; p > 0 {
		out = append(out, store.ProtectionPlanTier{
			Mechanism: store.MechStructuralSL, Kind: "structural", Label: "Struct",
			TriggerPct: pct(p), TriggerPrice: pricePtr(p), ExecutionPrice: pricePtr(p),
			ExecutionPct: pricePtr(pct(p)), CloseRatioPct: &full,
			// 措辞对多空中性:多头是收盘跌破、空头是收盘涨破,统称"越过"。
			Note: "结构位边界:K线收盘越过即平",
		})
	}
	if p := levels.BackstopPrice; p > 0 {
		out = append(out, store.ProtectionPlanTier{
			Mechanism: store.MechStructuralSL, Kind: "structural", Label: "Backstop",
			TriggerPct: pct(p), TriggerPrice: pricePtr(p), ExecutionPrice: pricePtr(p),
			ExecutionPct: pricePtr(pct(p)), CloseRatioPct: &full,
			Note: "结构位安全网:挂交易所,防宕机/跳空",
		})
	}
	return out
}

func breakEvenSnapshotTiers(be *store.BreakEvenStopConfig, entryPrice float64, isLong bool) []store.ProtectionPlanTier {
	mk := func(trigger, offset float64, name string) store.ProtectionPlanTier {
		offsetPrice := entryPrice * (1 + sign(isLong)*offset/100)
		p := offsetPrice
		label := "Break-even"
		if name != "" {
			label = "BE:" + name
		}
		return store.ProtectionPlanTier{
			Mechanism: store.MechBreakEven, Kind: "be", Label: label,
			TriggerPct: roundSnap2(trigger), TriggerPrice: &p,
			Note: fmt.Sprintf("arms +%.2g%% → stop @ +%.2g%%", trigger, offset),
		}
	}
	out := make([]store.ProtectionPlanTier, 0, 2)
	if len(be.Rules) > 0 {
		for _, r := range be.Rules {
			out = append(out, mk(r.TriggerValue, r.OffsetPct, strings.TrimSpace(r.StageName)))
		}
		return out
	}
	if be.TriggerValue != 0 || be.OffsetPct != 0 {
		out = append(out, mk(be.TriggerValue, be.OffsetPct, ""))
	}
	return out
}

func sign(isLong bool) float64 {
	if isLong {
		return 1
	}
	return -1
}

func roundSnap2(f float64) float64 {
	if f < 0 {
		return -float64(int64(-f*100+0.5)) / 100
	}
	return float64(int64(f*100+0.5)) / 100
}
