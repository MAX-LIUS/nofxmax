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
// Wire-level tests
//
// The unit tests above check pure functions. These assert the JSON we actually put
// on the wire, because the bugs that mattered most (Size sent together with Close,
// an untagged text, a price with more decimals than the contract accepts) are only
// visible in the serialized request.
// ============================================================================

// captureServer records every request body keyed by path suffix.
type captureServer struct {
	*httptest.Server
	mu     sync.Mutex
	bodies map[string][]string
	// requests preserves method+path in arrival order so cancel flows can be asserted.
	requests []string
}

func newCaptureServer(t *testing.T) *captureServer {
	cs := &captureServer{bodies: map[string][]string{}}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cs.mu.Lock()
		cs.bodies[r.URL.Path] = append(cs.bodies[r.URL.Path], string(body))
		cs.requests = append(cs.requests, r.Method+" "+r.URL.Path)
		cs.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/futures/usdt/contracts/"):
			// BTC_USDT: 1 contract = 0.001 BTC, price tick 0.1
			json.NewEncoder(w).Encode(map[string]interface{}{
				"name":              "BTC_USDT",
				"quanto_multiplier": "0.001",
				"order_price_round": "0.1",
			})
		case strings.Contains(path, "/futures/usdt/price_orders") && r.Method == http.MethodPost:
			json.NewEncoder(w).Encode(map[string]interface{}{"id": 987654})
		case strings.Contains(path, "/futures/usdt/price_orders"):
			json.NewEncoder(w).Encode([]map[string]interface{}{})
		default:
			json.NewEncoder(w).Encode(map[string]interface{}{})
		}
	}))
	t.Cleanup(cs.Close)
	return cs
}

func (cs *captureServer) lastBodyContaining(fragment string) string {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	for path, bodies := range cs.bodies {
		if !strings.Contains(path, fragment) {
			continue
		}
		if len(bodies) > 0 {
			return bodies[len(bodies)-1]
		}
	}
	return ""
}

// newTestTrader points a GateTrader at the capture server.
func newTestTrader(t *testing.T, cs *captureServer) *GateTrader {
	tr := NewGateTrader("test_key", "test_secret")
	tr.client.ChangeBasePath(cs.URL + "/api/v4")
	return tr
}

// decodeTriggerRequest unmarshals a captured price_orders POST body.
func decodeTriggerRequest(t *testing.T, body string) gateapi.FuturesPriceTriggeredOrder {
	t.Helper()
	if body == "" {
		t.Fatal("no price_orders request was captured — the order never reached the wire")
	}
	var got gateapi.FuturesPriceTriggeredOrder
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("captured body is not a FuturesPriceTriggeredOrder: %v\nbody=%s", err, body)
	}
	return got
}

func TestPartialProtectionSendsSignedSizeAndNeverClose(t *testing.T) {
	// Gate's contract: Size and Close are mutually exclusive. A partial close must
	// carry a signed Size with close unset. The pre-fix code sent BOTH, and if Gate
	// honours Close the tier becomes a FULL exit — the entire TP/SL ladder collapses
	// into one all-or-nothing order.
	cases := []struct {
		name     string
		side     string
		qty      float64
		wantSize int64
	}{
		// 0.05 BTC / 0.001 per contract = 50 contracts. Closing a long is negative.
		{"long partial", "LONG", 0.05, -50},
		{"short partial", "SHORT", 0.05, 50},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := newCaptureServer(t)
			tr := newTestTrader(t, cs)

			if err := tr.SetStopLoss("BTCUSDT", tc.side, tc.qty, 49000); err != nil {
				t.Fatalf("SetStopLoss: %v", err)
			}
			got := decodeTriggerRequest(t, cs.lastBodyContaining("price_orders"))

			if got.Initial.Size != tc.wantSize {
				t.Fatalf("Initial.Size = %d, want %d (sign encodes the closed side)", got.Initial.Size, tc.wantSize)
			}
			if got.Initial.Close {
				t.Fatal("Initial.Close must be false on a partial close; Size and Close are mutually exclusive")
			}
			if !got.Initial.ReduceOnly {
				t.Fatal("Initial.ReduceOnly must be true so a protection order can never open a position")
			}
		})
	}
}

func TestFullProtectionSendsCloseWithZeroSize(t *testing.T) {
	// quantity<=0 means "close everything", which is the ONLY case where Gate accepts
	// close=true, and it requires size=0.
	cs := newCaptureServer(t)
	tr := newTestTrader(t, cs)

	if err := tr.SetStopLoss("BTCUSDT", "LONG", 0, 49000); err != nil {
		t.Fatalf("SetStopLoss: %v", err)
	}
	got := decodeTriggerRequest(t, cs.lastBodyContaining("price_orders"))

	if got.Initial.Size != 0 {
		t.Fatalf("Initial.Size = %d, want 0 on a full close", got.Initial.Size)
	}
	if !got.Initial.Close {
		t.Fatal("Initial.Close must be true on a full close, otherwise Gate rejects the order")
	}
}

func TestProtectionTriggerRuleOnTheWire(t *testing.T) {
	// End-to-end confirmation that the corrected rule table survives all the way into
	// the request body, for the case that was most dangerous when inverted: a long's
	// stop loss must fire when price falls (rule 2), not when it rises.
	cs := newCaptureServer(t)
	tr := newTestTrader(t, cs)

	if err := tr.SetStopLoss("BTCUSDT", "LONG", 0.05, 49000); err != nil {
		t.Fatalf("SetStopLoss: %v", err)
	}
	got := decodeTriggerRequest(t, cs.lastBodyContaining("price_orders"))
	if got.Trigger.Rule != 2 {
		t.Fatalf("long stop loss Rule = %d, want 2 (<=). Rule 1 would fire the stop on the way UP", got.Trigger.Rule)
	}

	cs2 := newCaptureServer(t)
	tr2 := newTestTrader(t, cs2)
	if err := tr2.SetTakeProfit("BTCUSDT", "LONG", 0.05, 52000); err != nil {
		t.Fatalf("SetTakeProfit: %v", err)
	}
	got2 := decodeTriggerRequest(t, cs2.lastBodyContaining("price_orders"))
	if got2.Trigger.Rule != 1 {
		t.Fatalf("long take profit Rule = %d, want 1 (>=)", got2.Trigger.Rule)
	}
}

func TestProtectionPriceIsRoundedToContractTick(t *testing.T) {
	// order_price_round for BTC_USDT here is 0.1. A hardcoded %.8f (the pre-fix
	// behaviour) emits 49123.45678900, which Gate rejects — the protection order
	// simply never exists.
	cs := newCaptureServer(t)
	tr := newTestTrader(t, cs)

	if err := tr.SetStopLoss("BTCUSDT", "LONG", 0.05, 49123.456789); err != nil {
		t.Fatalf("SetStopLoss: %v", err)
	}
	got := decodeTriggerRequest(t, cs.lastBodyContaining("price_orders"))
	if got.Trigger.Price != "49123.5" {
		t.Fatalf("Trigger.Price = %q, want %q (rounded to the 0.1 tick)", got.Trigger.Price, "49123.5")
	}
}

func TestTaggedProtectionEncodesReasonInTextAndReturnsOrderID(t *testing.T) {
	cs := newCaptureServer(t)
	tr := newTestTrader(t, cs)

	orderID, err := tr.SetStopLossTagged("BTCUSDT", "LONG", 0.05, 49000, "break_even_stop")
	if err != nil {
		t.Fatalf("SetStopLossTagged: %v", err)
	}
	if orderID != "987654" {
		t.Fatalf("orderID = %q, want the exchange id 987654; the protection intent is recorded against it", orderID)
	}

	got := decodeTriggerRequest(t, cs.lastBodyContaining("price_orders"))
	if !strings.HasPrefix(got.Initial.Text, "t-") {
		t.Fatalf("Initial.Text = %q must start with \"t-\"; Gate rejects the whole order otherwise", got.Initial.Text)
	}
	// Gate's limit is 28 bytes excluding the "t-" prefix.
	if n := len(strings.TrimPrefix(got.Initial.Text, "t-")); n > 28 {
		t.Fatalf("Initial.Text payload is %d bytes, over Gate's 28-byte limit: %q", n, got.Initial.Text)
	}
	if decoded := decodeReasonFromClientID(got.Initial.Text); decoded != "break_even_stop" {
		t.Fatalf("decoded reason = %q, want break_even_stop (round trip through text failed)", decoded)
	}
}

func TestUntaggedProtectionStillCarriesBrokerTag(t *testing.T) {
	// Even without a reason, text must be a valid Gate broker tag: an empty or
	// malformed text is rejected, and losing the order is far worse than losing
	// attribution.
	cs := newCaptureServer(t)
	tr := newTestTrader(t, cs)

	if err := tr.SetTakeProfit("BTCUSDT", "SHORT", 0.05, 48000); err != nil {
		t.Fatalf("SetTakeProfit: %v", err)
	}
	got := decodeTriggerRequest(t, cs.lastBodyContaining("price_orders"))
	if got.Initial.Text != gateTag {
		t.Fatalf("Initial.Text = %q, want the bare broker tag %q", got.Initial.Text, gateTag)
	}
}
