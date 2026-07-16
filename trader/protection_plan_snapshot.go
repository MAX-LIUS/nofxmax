package trader

import (
	"fmt"
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
	tiers := buildPlanSnapshotTiers(plan, drawdownRules, req.EntryPrice, req.Action)
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
func buildPlanSnapshotTiers(plan *ProtectionPlan, drawdownRules []store.DrawdownTakeProfitRule, entryPrice float64, action string) []store.ProtectionPlanTier {
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

	// Drawdown floors (percent-of-peak giveback; no absolute price). arms at
	// min_profit_pct.
	for i, r := range drawdownRules {
		if r.CloseRatioPct <= 0 {
			continue
		}
		label := "Drawdown"
		if len(drawdownRules) > 1 {
			label = fmt.Sprintf("Drawdown %d", i+1)
		}
		cr := r.CloseRatioPct
		out = append(out, store.ProtectionPlanTier{
			Mechanism: store.MechManagedDrawdown, Kind: "drawdown", Label: label,
			TriggerPct: roundSnap2(r.MinProfitPct), CloseRatioPct: &cr,
			Note: fmt.Sprintf("min +%.2g%% · giveback %.0f%%", r.MinProfitPct, r.MaxDrawdownPct),
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
