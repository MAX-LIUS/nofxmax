package api

import (
	"encoding/json"
	"strings"

	"nofx/kernel"
	"nofx/store"
)

// realProtectionSnapshotFromDecisionJSON extracts the actual per-position
// protection plan the AI resolved at open time (structural + ATR derived,
// carrying concrete prices/anchors) from the raw decision payloads and maps it
// into the store.ProtectionSnapshot shape the frontend already renders.
//
// This is deliberately NOT the strategy-level template (record.ProtectionSnapshot,
// one row per cycle shared by every symbol). It is the real, symbol-specific plan
// persisted inside decision_json / raw_response. When no real plan exists for the
// matched (symbol, action) the function returns nil so callers can surface a
// "plan not recorded" state instead of falling back to a template.
func realProtectionSnapshotFromDecisionJSON(payloads []string, symbol, action string) *store.ProtectionSnapshot {
	for _, payload := range payloads {
		if strings.TrimSpace(payload) == "" {
			continue
		}
		var decisions []kernel.Decision
		if err := json.Unmarshal([]byte(payload), &decisions); err != nil {
			continue
		}
		for i := range decisions {
			d := decisions[i]
			if symbol != "" && !strings.EqualFold(d.Symbol, symbol) {
				continue
			}
			if action != "" && !strings.EqualFold(d.Action, action) {
				continue
			}
			if d.ProtectionPlan == nil {
				continue
			}
			if ps := protectionSnapshotFromAIPlan(d.ProtectionPlan); ps != nil {
				return ps
			}
		}
	}
	return nil
}

// protectionSnapshotFromAIPlan maps a resolved kernel.AIProtectionPlan into the
// store.ProtectionSnapshot shape. Returns nil if the plan carries no usable rows.
func protectionSnapshotFromAIPlan(plan *kernel.AIProtectionPlan) *store.ProtectionSnapshot {
	if plan == nil {
		return nil
	}
	ps := &store.ProtectionSnapshot{}
	has := false

	// Ladder tiers — the real per-tier resolved prices/pcts/anchors.
	if len(plan.LadderRules) > 0 {
		ladder := &store.ProtectionSnapshotLadder{
			Enabled: true,
			Mode:    plan.Mode,
		}
		for _, r := range plan.LadderRules {
			if r.TakeProfitPct != 0 || r.TakeProfitPrice != 0 {
				ladder.TakeProfitEnabled = true
			}
			if r.StopLossPct != 0 || r.StopLossPrice != 0 {
				ladder.StopLossEnabled = true
			}
			ladder.Rules = append(ladder.Rules, store.ProtectionSnapshotLadderRule{
				TakeProfitPct:           r.TakeProfitPct,
				TakeProfitPrice:         r.TakeProfitPrice,
				TakeProfitCloseRatioPct: r.TakeProfitCloseRatioPct,
				StopLossPct:             r.StopLossPct,
				StopLossPrice:           r.StopLossPrice,
				StopLossCloseRatioPct:   r.StopLossCloseRatioPct,
				StructuralAnchor:        r.StructuralAnchor,
				TakeProfitAnchor:        r.TakeProfitAnchor,
				StopLossAnchor:          r.StopLossAnchor,
				VolatilityBufferPct:     r.VolatilityBufferPct,
			})
		}
		ps.LadderTPSL = ladder
		has = true
	}

	// Drawdown floors — real per-stage min-profit/giveback resolved from structure.
	for _, r := range plan.DrawdownRules {
		ps.Drawdown = append(ps.Drawdown, store.ProtectionSnapshotDrawdown{
			Mode:              plan.Mode,
			Source:            r.BasisType,
			MinProfitPct:      r.MinProfitPct,
			MaxDrawdownPct:    r.MaxDrawdownPct,
			MaxDrawdownAbsPct: r.MaxDrawdownAbsPct,
			CloseRatioPct:     r.CloseRatioPct,
			PollIntervalS:     r.PollIntervalSeconds,
		})
		has = true
	}

	// Break-even — real armed trigger/offset from the plan.
	if plan.BreakEvenValue != 0 || plan.BreakEvenOffset != 0 || plan.BreakEvenTrigger != "" {
		ps.BreakEven = &store.ProtectionSnapshotBreakEven{
			Enabled:      true,
			Source:       plan.BreakEvenAnchor,
			TriggerMode:  plan.BreakEvenTrigger,
			TriggerValue: plan.BreakEvenValue,
			OffsetPct:    plan.BreakEvenOffset,
		}
		has = true
	}

	// Non-laddered full TP/SL — only when there are no ladder tiers, otherwise
	// the ladder rows already carry the real per-tier prices.
	if len(plan.LadderRules) == 0 && (plan.StopLossPct != 0 || plan.TakeProfitPct != 0) {
		full := &store.ProtectionSnapshotFullTPSL{Enabled: true, Mode: plan.Mode}
		if plan.TakeProfitPct != 0 {
			full.TakeProfit = store.ProtectionSnapshotValueSource{Mode: plan.TakeProfitAnchor, Value: plan.TakeProfitPct}
		}
		if plan.StopLossPct != 0 {
			full.StopLoss = store.ProtectionSnapshotValueSource{Mode: plan.StopLossAnchor, Value: plan.StopLossPct}
		}
		ps.FullTPSL = full
		has = true
	}

	if !has {
		return nil
	}
	return ps
}
