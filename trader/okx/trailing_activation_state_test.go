package okx

import (
	"fmt"
	"testing"
)

// TestTrailingActivationReadFromRowNotQueryBucket pins that a trailing order's
// activation is decided by the row's own data.
//
// Regression: activationStatus used to be derived from which query bucket returned
// the row (state="" ⇒ pending_activation, state="effective" ⇒ activated). The ""
// bucket runs first and seenTrailingIDs dedupes the second, so any row OKX returns
// in the default bucket could never be reported activated no matter what it said.
// Production over 4 days: 1528 pending_activation vs 5 activated. nativeTrailingEffective
// then judged such an order phantom ("mark passed activePx and venue says not
// activated"), and an activated trail has always passed its activePx — so healthy
// orders were judged dead and 3 polls (~21s) tripped the re-arm breaker
// (2026-08-02 02:45:45 ETHUSDT short, algo stayed live on OKX until close).
func TestTrailingActivationReadFromRowNotQueryBucket(t *testing.T) {
	trailID := encodeReasonClientID("managed_drawdown")
	if trailID == "" {
		t.Fatal("codec produced no client id, test would prove nothing")
	}

	cases := []struct {
		name          string
		state         string
		activePx      string
		moveTriggerPx string
		wantStatus    string
		wantStopPrice float64
	}{
		// OKX's own word, arriving in the DEFAULT bucket — this is the case that used
		// to be mislabeled and is what tripped the breaker in production.
		{"effective in default bucket", "effective", "81.06", "0", "activated", 81.06},
		// moveTriggerPx is only published after activation, so it alone is sufficient
		// and it carries the live trail level.
		{"moving trigger published", "live", "81.06", "82.40", "activated", 82.40},
		// A genuinely resting order must stay pending so the phantom rule can still
		// catch real dead orders.
		{"resting", "live", "81.06", "0", "pending_activation", 81.06},
		// No activation anchor ⇒ OKX activated it immediately.
		{"no anchor", "live", "0", "0", "activated", 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			trailing := fmt.Sprintf(`{"code":"0","msg":"","data":[
				{"algoId":"a-trail","instId":"CL-USDT-SWAP","side":"buy","posSide":"short","activePx":"%s","callbackRatio":"0.55","moveTriggerPx":"%s","sz":"28","state":"%s","tag":"%s","algoClOrdId":"%s"}
			]}`, tc.activePx, tc.moveTriggerPx, tc.state, okxTag, trailID)

			orders, err := newOpenOrdersProbeTrader(`{"code":"0","msg":"","data":[]}`, trailing).GetOpenOrders("CLUSDT")
			if err != nil {
				t.Fatalf("GetOpenOrders failed: %v", err)
			}
			if len(orders) != 1 {
				t.Fatalf("expected 1 trailing order, got %d", len(orders))
			}
			got := orders[0]
			if got.ActivationStatus != tc.wantStatus {
				t.Fatalf("ActivationStatus = %q, want %q (state=%q moveTriggerPx=%q)",
					got.ActivationStatus, tc.wantStatus, tc.state, tc.moveTriggerPx)
			}
			if got.StopPrice != tc.wantStopPrice {
				t.Fatalf("StopPrice = %v, want %v (activated trail must report the MOVING trigger)",
					got.StopPrice, tc.wantStopPrice)
			}
		})
	}
}
