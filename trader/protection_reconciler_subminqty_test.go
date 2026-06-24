package trader

import (
	"testing"

	"nofx/store"
	tradertypes "nofx/trader/types"
)

// Form-2 churn fix: a ladder TP tier whose size is below the exchange
// minimum-contract floor is dropped by the place path. Before the fix the
// reconciler kept demanding that dropped tier (missingTP=true forever) and
// re-placed every cycle. After the fix the reconcile plan is filtered through
// the same executability gate, so only the placeable tier is "expected" and a
// position already carrying that tier verifies clean (no churn).
func TestProtectionReconciler_SubMinTierDoesNotCauseMissingChurn(t *testing.T) {
	// Clear any leftover cooldown for this key.
	reconcileCooldownMutex.Lock()
	delete(reconcileCooldowns, "SUBMINUSDT_long")
	reconcileCooldownMutex.Unlock()
	defer func() {
		reconcileCooldownMutex.Lock()
		delete(reconcileCooldowns, "SUBMINUSDT_long")
		reconcileCooldownMutex.Unlock()
	}()

	ft := &fakeReconcileTrader{
		fakeOrderProtectionTrader: fakeOrderProtectionTrader{
			// Exchange holds: a full SL @95 and ONLY the executable TP tier @105.
			// The sub-min TP tier @110 (qty 0.4 < 0.5 floor) was never placeable.
			openOrders: []tradertypes.OpenOrder{
				{OrderID: "sl1", Symbol: "SUBMINUSDT", PositionSide: "LONG", Type: "STOP_MARKET", StopPrice: 95, Quantity: 1, ClientOrderID: "ladder_sl_1", Status: "NEW"},
				{OrderID: "tp1", Symbol: "SUBMINUSDT", PositionSide: "LONG", Type: "TAKE_PROFIT_MARKET", StopPrice: 105, Quantity: 0.6, ClientOrderID: "ladder_tp_1", Status: "NEW"},
			},
			// Sub-min floor: any protection order < 0.5 contracts is rejected.
			validateQtyErrBelow: 0.5,
			positions: []map[string]interface{}{{
				"symbol": "SUBMINUSDT", "side": "long", "positionAmt": 1.0, "entryPrice": 100.0, "markPrice": 100.0,
			}},
		},
		positions: []map[string]interface{}{{
			"symbol": "SUBMINUSDT", "side": "long", "positionAmt": 1.0, "entryPrice": 100.0, "markPrice": 100.0,
		}},
	}

	at := &AutoTrader{
		exchange: "okx",
		trader:   ft,
		config: AutoTraderConfig{StrategyConfig: &store.StrategyConfig{
			Protection: store.ProtectionConfig{
				LadderTPSL: store.LadderTPSLConfig{
					Enabled:           true,
					Mode:              store.ProtectionModeManual,
					StopLossEnabled:   true,
					TakeProfitEnabled: true,
					Rules: []store.LadderTPSLRule{
						{StopLossPct: 5, StopLossCloseRatioPct: 100, TakeProfitPct: 5, TakeProfitCloseRatioPct: 60},
						{TakeProfitPct: 10, TakeProfitCloseRatioPct: 40},
					},
				},
			},
		}},
		protectionState:       make(map[string]string),
		breakEvenState:        make(map[string]string),
		breakEvenFingerprints: make(map[string]string),
		drawdownState:         make(map[string]string),
	}

	result, err := at.reconcileProtectionForPosition("SUBMINUSDT", "long", 1, 100, 100)
	if err != nil {
		t.Fatalf("reconcile returned error (churn): %v", err)
	}
	if !result.ExchangeVerified {
		t.Fatalf("expected coverage verified with sub-min tier filtered out, got %+v", result)
	}
	// No re-placement attempts for the doomed sub-min tier.
	for _, o := range ft.takeProfitOrders {
		if o.price > 109 { // the @110 sub-min tier must never be re-placed
			t.Fatalf("sub-min TP tier was re-placed (churn): %+v", o)
		}
	}
}
