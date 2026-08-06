package gate

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gateio/gateapi-go/v6"
)

// ============================================================================
// Production regression: 2026-08-06 SKHYNIXUSDT / XAUUSDT
//
// The gate trader opened 5 positions and closed every one of them within 3-5
// seconds at a loss, burning 0.89 USDT in round-trip fees for zero exposure.
// Root cause in the deployed (pre-bed9b92) binary:
//
//	AUTO_INVALID_PARAM_INITIAL_SIZE: invalid argument: initial.size close size must zero
//
// Gate rejected EVERY protection tier because Size and Close were sent together.
// All 5 tiers failed -> protection plan failed 2/2 -> fallback failed 2/2 ->
// auto_trader_orders.go:531 "Protection setup failed, closing position immediately".
//
// The system behaved correctly: refusing to hold an unprotected position is the
// right call. The defect was purely in the gate adapter.
//
// These tests replay the exact production tier ladder so the regression cannot
// come back silently.
// ============================================================================

// gateRejectingServer mimics Gate's real validation: it rejects any trigger order
// that sends a non-zero Size together with Close=true, exactly as production did.
type gateRejectingServer struct {
	*httptest.Server
	mu       sync.Mutex
	rejected []string
	accepted []gateapi.FuturesPriceTriggeredOrder
}

func newGateRejectingServer(t *testing.T) *gateRejectingServer {
	gs := &gateRejectingServer{}
	gs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path

		switch {
		case strings.Contains(path, "/futures/usdt/contracts/"):
			// SKHYNIX_USDT as it really is on Gate: 1 contract = 0.001, tick 0.01.
			json.NewEncoder(w).Encode(map[string]interface{}{
				"name":              "SKHYNIX_USDT",
				"quanto_multiplier": "0.001",
				"order_price_round": "0.01",
			})

		case strings.Contains(path, "/futures/usdt/price_orders") && r.Method == http.MethodPost:
			var got gateapi.FuturesPriceTriggeredOrder
			if err := json.Unmarshal(body, &got); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"label": "BAD_JSON"})
				return
			}
			// This is the production rejection, verbatim.
			if got.Initial.Size != 0 && got.Initial.Close {
				gs.mu.Lock()
				gs.rejected = append(gs.rejected, string(body))
				gs.mu.Unlock()
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{
					"label":   "AUTO_INVALID_PARAM_INITIAL_SIZE",
					"message": "invalid argument: initial.size close size must zero",
				})
				return
			}
			gs.mu.Lock()
			gs.accepted = append(gs.accepted, got)
			gs.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]interface{}{"id": 356347321247402891})

		default:
			json.NewEncoder(w).Encode(map[string]interface{}{})
		}
	}))
	t.Cleanup(gs.Close)
	return gs
}

func newRejectingTrader(t *testing.T, gs *gateRejectingServer) *GateTrader {
	tr := NewGateTrader("test_key", "test_secret")
	tr.client.ChangeBasePath(gs.URL + "/api/v4")
	return tr
}

// TestProductionLadderSurvivesGateValidation replays the exact 1SL/4TP ladder that
// failed in production at 10:43:54 on 2026-08-06 and asserts every tier is accepted.
func TestProductionLadderSurvivesGateValidation(t *testing.T) {
	gs := newGateRejectingServer(t)
	tr := newRejectingTrader(t, gs)

	// The real position: SKHYNIXUSDT SHORT, 0.066 base (66 contracts @ 0.001).
	const totalQty = 0.066

	// The real ladder from the production log line at 10:43:53/54.
	tiers := []struct {
		kind  string
		price float64
		ratio float64
	}{
		{"sl", 1150.571827, 100.00},
		{"tp", 1079.581510, 20.00},
		{"tp", 1063.163018, 15.00},
		{"tp", 1028.199495, 15.00},
		{"tp", 1017.273394, 15.00},
	}

	for _, tier := range tiers {
		orderQty := totalQty * tier.ratio / 100.0
		var err error
		if tier.kind == "sl" {
			_, err = tr.SetStopLossTagged("SKHYNIXUSDT", "SHORT", orderQty, tier.price, "ladder_sl")
		} else {
			_, err = tr.SetTakeProfitTagged("SKHYNIXUSDT", "SHORT", orderQty, tier.price, "ladder_tp")
		}
		if err != nil {
			t.Fatalf("tier %s @ %.6f (ratio %.2f%%) was rejected by Gate: %v\n"+
				"this is the production failure returning — protection would fail and the "+
				"position would be closed immediately at a loss", tier.kind, tier.price, tier.ratio, err)
		}
	}

	gs.mu.Lock()
	defer gs.mu.Unlock()
	if len(gs.rejected) != 0 {
		t.Fatalf("Gate rejected %d tier(s); first body:\n%s", len(gs.rejected), gs.rejected[0])
	}
	if len(gs.accepted) != len(tiers) {
		t.Fatalf("accepted %d tiers, want %d — a tier never reached the wire", len(gs.accepted), len(tiers))
	}

	// Every tier is a PARTIAL close on a short: positive size, close unset.
	for i, got := range gs.accepted {
		if got.Initial.Close {
			t.Fatalf("tier %d sent Close=true with Size=%d; Size and Close are mutually exclusive", i, got.Initial.Size)
		}
		if got.Initial.Size <= 0 {
			t.Fatalf("tier %d Size=%d; closing a SHORT must be a positive size", i, got.Initial.Size)
		}
		if !got.Initial.ReduceOnly {
			t.Fatalf("tier %d is not reduce_only; a protection order must never open a position", i)
		}
	}

	// The 100% SL tier is 66 contracts; the 20%/15% TP tiers are 13/9 contracts
	// (0.066*0.20/0.001 = 13.2 -> 13, 0.066*0.15/0.001 = 9.9 -> 10).
	if gs.accepted[0].Initial.Size != 66 {
		t.Fatalf("SL tier Size=%d, want 66 contracts for the full 0.066 position", gs.accepted[0].Initial.Size)
	}
}

// TestProductionTriggerRuleMatchesShortLadder pins the trigger direction for the
// production short. A short stop is ABOVE entry (rule 1 = fire when price >=), a
// short target is BELOW entry (rule 2 = fire when price <=). The pre-fix code had
// these inverted, so Gate would either reject the order or fire it the wrong way.
func TestProductionTriggerRuleMatchesShortLadder(t *testing.T) {
	gs := newGateRejectingServer(t)
	tr := newRejectingTrader(t, gs)

	if _, err := tr.SetStopLossTagged("SKHYNIXUSDT", "SHORT", 0.066, 1150.571827, "ladder_sl"); err != nil {
		t.Fatalf("SL: %v", err)
	}
	if _, err := tr.SetTakeProfitTagged("SKHYNIXUSDT", "SHORT", 0.0132, 1079.581510, "ladder_tp"); err != nil {
		t.Fatalf("TP: %v", err)
	}

	gs.mu.Lock()
	defer gs.mu.Unlock()
	if len(gs.accepted) != 2 {
		t.Fatalf("accepted %d orders, want 2", len(gs.accepted))
	}
	// SL at 1150.57 is ABOVE the 1085 entry: must fire on price >= trigger.
	if gs.accepted[0].Trigger.Rule != 1 {
		t.Fatalf("short SL rule=%d, want 1 (fire when price >= 1150.57, i.e. loss deepens)", gs.accepted[0].Trigger.Rule)
	}
	// TP at 1079.58 is BELOW the 1085 entry: must fire on price <= trigger.
	if gs.accepted[1].Trigger.Rule != 2 {
		t.Fatalf("short TP rule=%d, want 2 (fire when price <= 1079.58, i.e. profit taken)", gs.accepted[1].Trigger.Rule)
	}
	// Price must respect the 0.01 tick, not the old hardcoded %.8f.
	if strings.Contains(gs.accepted[0].Trigger.Price, "1150.57182") {
		t.Fatalf("trigger price %q kept 8 decimals; Gate rejects prices finer than the contract tick", gs.accepted[0].Trigger.Price)
	}
}
