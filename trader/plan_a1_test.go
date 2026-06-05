package trader

import (
	"testing"

	"nofx/store"
)

// TestPlanA1LadderTPSLBundle verifies the A1 protection scheme:
// Ladder alone handles both 2-tier take-profit (落袋: +3%×40%, +6%×35%)
// and an 8% stop-loss. DD/Full/BE disabled. No owner conflict.
// Validated design 2026-06-04: only fixed-point laddered take-profit was
// backtest-positive (+36.8%); DD/BE trailing lose in trend-following.
func planA1Trader() *AutoTrader {
	return &AutoTrader{
		config: AutoTraderConfig{
			StrategyConfig: &store.StrategyConfig{
				Protection: store.ProtectionConfig{
					LadderTPSL: store.LadderTPSLConfig{
						Enabled:           true,
						Mode:              store.ProtectionModeManual,
						TakeProfitEnabled: true,
						StopLossEnabled:   true,
						TakeProfitPrice:   store.ProtectionValueSource{Mode: store.ProtectionValueModeManual, Value: 1},
						TakeProfitSize:    store.ProtectionValueSource{Mode: store.ProtectionValueModeManual, Value: 1},
						StopLossPrice:     store.ProtectionValueSource{Mode: store.ProtectionValueModeManual, Value: 1},
						StopLossSize:      store.ProtectionValueSource{Mode: store.ProtectionValueModeManual, Value: 1},
						Rules: []store.LadderTPSLRule{
							{TakeProfitPct: 3, TakeProfitCloseRatioPct: 40, StopLossPct: 8, StopLossCloseRatioPct: 100},
							{TakeProfitPct: 6, TakeProfitCloseRatioPct: 35},
						},
					},
					// DD / Full / BE all disabled
					DrawdownTakeProfit: store.DrawdownTakeProfitConfig{Enabled: false},
					FullTPSL:           store.FullTPSLConfig{Enabled: false},
					BreakEvenStop:      store.BreakEvenStopConfig{Enabled: false},
				},
			},
		},
	}
}

func TestPlanA1_LongProtectionPlan(t *testing.T) {
	at := planA1Trader()
	plan, err := at.BuildConfiguredProtectionPlan(100, "open_long")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("expected a plan")
	}
	// Two TP 落袋 orders: +3% → 103 (40%), +6% → 106 (35%)
	if len(plan.TakeProfitOrders) != 2 {
		t.Fatalf("expected 2 TP ladder orders, got %d: %+v", len(plan.TakeProfitOrders), plan.TakeProfitOrders)
	}
	if !almostEqual(plan.TakeProfitOrders[0].Price, 103) || !almostEqual(plan.TakeProfitOrders[0].CloseRatioPct, 40) {
		t.Errorf("TP1 wrong: %+v (want price=103 ratio=40)", plan.TakeProfitOrders[0])
	}
	if !almostEqual(plan.TakeProfitOrders[1].Price, 106) || !almostEqual(plan.TakeProfitOrders[1].CloseRatioPct, 35) {
		t.Errorf("TP2 wrong: %+v (want price=106 ratio=35)", plan.TakeProfitOrders[1])
	}
	// 8% stop-loss: long → 92
	if !plan.NeedsStopLoss {
		t.Error("expected stop loss")
	}
	if len(plan.StopLossOrders) == 0 || !almostEqual(plan.StopLossOrders[0].Price, 92) {
		t.Errorf("expected 8%% SL at 92, got %+v", plan.StopLossOrders)
	}
}

func TestPlanA1_ShortProtectionPlan(t *testing.T) {
	at := planA1Trader()
	plan, err := at.BuildConfiguredProtectionPlan(100, "open_short")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("expected a plan")
	}
	// Short: TP +3% → 97, +6% → 94; SL 8% → 108
	if len(plan.TakeProfitOrders) != 2 {
		t.Fatalf("expected 2 TP orders, got %d", len(plan.TakeProfitOrders))
	}
	if !almostEqual(plan.TakeProfitOrders[0].Price, 97) || !almostEqual(plan.TakeProfitOrders[1].Price, 94) {
		t.Errorf("short TP prices wrong: %+v", plan.TakeProfitOrders)
	}
	if len(plan.StopLossOrders) == 0 || !almostEqual(plan.StopLossOrders[0].Price, 108) {
		t.Errorf("expected 8%% SL at 108, got %+v", plan.StopLossOrders)
	}
}

func TestPlanA1_OwnerPolicyNoConflict(t *testing.T) {
	at := planA1Trader()
	policy := evaluateProtectionOwnerPolicy(at.config.StrategyConfig.Protection)
	// Ladder owns both stop and profit; no drawdown suppression.
	if policy.StopOwner != "ladder" {
		t.Errorf("expected StopOwner=ladder, got %q", policy.StopOwner)
	}
	if policy.ProfitOwner != "ladder" {
		t.Errorf("expected ProfitOwner=ladder, got %q", policy.ProfitOwner)
	}
	if policy.UseDrawdownTP {
		t.Error("DD must be off — UseDrawdownTP should be false")
	}
	if policy.SuppressStaticTP {
		t.Error("SuppressStaticTP must be false so ladder TP survives")
	}
}
