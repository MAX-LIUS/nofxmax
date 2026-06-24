package trader

import (
	"testing"

	"nofx/store"
	tradertypes "nofx/trader/types"
)

// idCancelReconcileTrader extends the reconcile fake with CancelAlgoOrderByID so
// the coverage-complete stale-duplicate fast path can be exercised end to end.
type idCancelReconcileTrader struct {
	fakeReconcileTrader
	canceledIDs []string
}

func (t *idCancelReconcileTrader) CancelAlgoOrderByID(symbol string, algoID string) error {
	t.canceledIDs = append(t.canceledIDs, algoID)
	filtered := t.openOrders[:0]
	for _, order := range t.openOrders {
		if order.Symbol == symbol && order.OrderID == algoID {
			continue
		}
		filtered = append(filtered, order)
	}
	t.openOrders = filtered
	return nil
}

// TestProtectionReconcilerCancelsStaleDuplicateTPWithoutReplaceWhenCoverageComplete
// reproduces the WLD churn on the take-profit side: a stale bot TP duplicate sits
// alongside the expected TP while both SL and TP coverage are complete. The
// reconciler must cancel the stale duplicate directly and NOT re-place the plan
// (which would add yet another same-price/different-qty order and never converge).
//
// Note: the SL-only duplicate case is already absorbed by the line-274 exemption
// ("preserving extra protective stop orders because stop coverage is satisfied"),
// so the churn that actually escaped was on the TP side, which had no such guard.
func TestProtectionReconcilerCancelsStaleDuplicateTPWithoutReplaceWhenCoverageComplete(t *testing.T) {
	// The reconcile cooldown map is a package global; clear our key before and
	// after so this test neither inherits nor leaks cooldown state across the suite.
	clearReconcileCooldown := func() {
		reconcileCooldownMutex.Lock()
		delete(reconcileCooldowns, "DUPUSDT_long")
		reconcileCooldownMutex.Unlock()
	}
	clearReconcileCooldown()
	defer clearReconcileCooldown()

	ft := &idCancelReconcileTrader{
		fakeReconcileTrader: fakeReconcileTrader{
			fakeOrderProtectionTrader: fakeOrderProtectionTrader{
				openOrders: []tradertypes.OpenOrder{
					// Expected stop (matches plan price 98).
					{OrderID: "full_sl_keep", Symbol: "DUPUSDT", PositionSide: "LONG", Type: "STOP_MARKET", StopPrice: 98, Quantity: 1, ClientOrderID: "full_sl_keep", Status: "NEW"},
					// Expected take-profit (matches plan price 105).
					{OrderID: "full_tp_keep", Symbol: "DUPUSDT", PositionSide: "LONG", Type: "TAKE_PROFIT_MARKET", StopPrice: 105, Quantity: 1, ClientOrderID: "full_tp_keep", Status: "NEW"},
					// Stale bot TP duplicate at a different price + old entry qty (bot marker => stale_bot_duplicate).
					{OrderID: "ladder_tp_stale", Symbol: "DUPUSDT", PositionSide: "LONG", Type: "TAKE_PROFIT_MARKET", StopPrice: 103, Quantity: 3, ClientOrderID: "ladder_tp_stale", Status: "NEW"},
				},
				positions: []map[string]interface{}{{"symbol": "DUPUSDT", "side": "long", "positionAmt": 1.0, "entryPrice": 100.0, "markPrice": 100.0}},
			},
			positions: []map[string]interface{}{{"symbol": "DUPUSDT", "side": "long", "positionAmt": 1.0, "entryPrice": 100.0, "markPrice": 100.0}},
		},
	}

	at := &AutoTrader{
		exchange: "okx",
		trader:   ft,
		config: AutoTraderConfig{StrategyConfig: &store.StrategyConfig{
			Protection: store.ProtectionConfig{FullTPSL: store.FullTPSLConfig{
				Enabled:           true,
				Mode:              store.ProtectionModeManual,
				StopLossEnabled:   true,
				StopLoss:          store.ProtectionValueSource{Mode: store.ProtectionValueModeManual, Value: 2},
				TakeProfitEnabled: true,
				TakeProfit:        store.ProtectionValueSource{Mode: store.ProtectionValueModeManual, Value: 5},
			}},
		}},
		protectionState:       make(map[string]string),
		breakEvenState:        make(map[string]string),
		breakEvenFingerprints: make(map[string]string),
		drawdownState:         make(map[string]string),
	}

	result, err := at.reconcileProtectionForPosition("DUPUSDT", "long", 1, 100, 100)
	if err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}
	if !result.ExchangeVerified {
		t.Fatalf("expected coverage-complete cleanup to verify, got %+v", result)
	}
	if len(ft.canceledIDs) != 1 || ft.canceledIDs[0] != "ladder_tp_stale" {
		t.Fatalf("expected only the stale TP duplicate canceled, got %v", ft.canceledIDs)
	}
	// No re-place: NO new SL or TP placed by the cleanup path.
	if len(ft.stopLossOrders) != 0 || len(ft.takeProfitOrders) != 0 {
		t.Fatalf("expected NO new placement (no re-place), got sl=%d tp=%d", len(ft.stopLossOrders), len(ft.takeProfitOrders))
	}
	remainingTPs := 0
	for _, o := range ft.openOrders {
		if o.Symbol == "DUPUSDT" && looksLikeTakeProfit(o) {
			remainingTPs++
		}
	}
	if remainingTPs != 1 {
		t.Fatalf("expected exactly one TP remaining after dedup, got %d (%+v)", remainingTPs, ft.openOrders)
	}
}
