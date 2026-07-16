package trader

import "testing"

// TestShouldReplacePartialTrailingTier_CallbackGuard guards the SPCX/XAG/XAU
// Binance trailing churn fix (2026-07-17). Binance's algo-order list endpoint
// does not report a trailing order's callback rate, so OpenOrder.CallbackRate is
// 0 for every Binance trailing order. Comparing 0 against the planned ratio always
// exceeded the tolerance, forcing a re-arm every monitor cycle. When CallbackRate
// is unavailable we must trust the activation-price match alone; when it IS
// reported (OKX), the full callback comparison still applies.
func TestShouldReplacePartialTrailingTier_CallbackGuard(t *testing.T) {
	at := &AutoTrader{}

	tests := []struct {
		name             string
		existing         *nativeTrailingOrder
		plannedActivation float64
		plannedCallback  float64
		want             bool
	}{
		{
			name:              "binance unreported callback (0) with matching activation → keep",
			existing:          &nativeTrailingOrder{StopPrice: 130.622112, CallbackRate: 0},
			plannedActivation: 130.622112,
			plannedCallback:   0.017039,
			want:              false, // was TRUE before fix (0 vs 0.017 = false drift) → churn
		},
		{
			name:              "activation drift beyond tolerance → replace regardless of callback",
			existing:          &nativeTrailingOrder{StopPrice: 130.0, CallbackRate: 0},
			plannedActivation: 135.0,
			plannedCallback:   0.017039,
			want:              true,
		},
		{
			name:              "okx reported callback matches → keep",
			existing:          &nativeTrailingOrder{StopPrice: 130.622112, CallbackRate: 0.017},
			plannedActivation: 130.622112,
			plannedCallback:   0.017039,
			want:              false, // |0.017-0.017039|=0.000039 < 0.0002
		},
		{
			name:              "okx reported callback genuinely drifted → replace",
			existing:          &nativeTrailingOrder{StopPrice: 130.622112, CallbackRate: 0.025},
			plannedActivation: 130.622112,
			plannedCallback:   0.017039,
			want:              true, // |0.025-0.017039|=0.008 > 0.0002
		},
		{
			name:              "nil existing → keep",
			existing:          nil,
			plannedActivation: 130.0,
			plannedCallback:   0.017,
			want:              false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := at.shouldReplacePartialTrailingTier(tc.existing, tc.plannedActivation, tc.plannedCallback)
			if got != tc.want {
				t.Fatalf("shouldReplacePartialTrailingTier = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestFindExistingFullTrailingOrder_PreservesActivationStatus guards the second
// half of the Binance trailing churn fix (2026-07-17): findExistingFullTrailingOrder
// previously dropped ActivationStatus, so the full-trailing arm path could not tell
// an already-activated order (whose trigger is a moving trail level) from a resting
// one, and re-armed it every cycle by comparing the moving level against the fixed
// planned activation. The status must survive so the arm path can leave activated
// orders alone.
func TestFindExistingFullTrailingOrder_PreservesActivationStatus(t *testing.T) {
	at := &AutoTrader{}
	openOrders := []OpenOrder{
		{
			PositionSide:     "SHORT",
			Type:             "TRAILING_STOP_MARKET",
			StopPrice:        128.5, // moving trail level, NOT the fixed activation
			Quantity:         10,
			OrderID:          "algo-1",
			ActivationStatus: "activated",
		},
	}
	got := at.findExistingFullTrailingOrder("short", openOrders)
	if got == nil {
		t.Fatal("expected to find the trailing order")
	}
	if got.ActivationStatus != "activated" {
		t.Fatalf("ActivationStatus not preserved: got %q, want \"activated\"", got.ActivationStatus)
	}
}
