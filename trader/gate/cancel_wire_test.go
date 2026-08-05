package gate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// cancelServer lists a fixed set of resting trigger orders and records which ids
// were actually cancelled.
type cancelServer struct {
	*httptest.Server
	mu        sync.Mutex
	open      []map[string]interface{}
	cancelled []string
}

func newCancelServer(t *testing.T, open []map[string]interface{}) *cancelServer {
	cs := &cancelServer{open: open}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		// DELETE /price_orders/{id}
		case strings.Contains(path, "/futures/usdt/price_orders/") && r.Method == http.MethodDelete:
			id := path[strings.LastIndex(path, "/")+1:]
			cs.mu.Lock()
			cs.cancelled = append(cs.cancelled, id)
			cs.mu.Unlock()
			// id must be numeric here: Gate's real response types it as int64.
			numeric, _ := strconv.ParseInt(id, 10, 64)
			json.NewEncoder(w).Encode(map[string]interface{}{"id": numeric})
		case strings.Contains(path, "/futures/usdt/price_orders"):
			cs.mu.Lock()
			open := cs.open
			cs.mu.Unlock()
			json.NewEncoder(w).Encode(open)
		case strings.Contains(path, "/futures/usdt/contracts/"):
			json.NewEncoder(w).Encode(map[string]interface{}{
				"name": "BTC_USDT", "quanto_multiplier": "0.001", "order_price_round": "0.1",
			})
		default:
			json.NewEncoder(w).Encode([]map[string]interface{}{})
		}
	}))
	t.Cleanup(cs.Close)
	return cs
}

func (cs *cancelServer) trader() *GateTrader {
	tr := NewGateTrader("k", "s")
	tr.client.ChangeBasePath(cs.URL + "/api/v4")
	return tr
}

func (cs *cancelServer) cancelledIDs() []string {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	out := append([]string{}, cs.cancelled...)
	sort.Strings(out)
	return out
}

func triggerOrderJSON(id int, orderType string, size int64, rule int32) map[string]interface{} {
	return map[string]interface{}{
		"id":         id,
		"order_type": orderType,
		"initial":    map[string]interface{}{"contract": "BTC_USDT", "size": size},
		"trigger":    map[string]interface{}{"rule": rule, "price": "49000"},
	}
}

// ============================================================================
// Selective cancellation
//
// The pre-fix cancelTriggerOrders took an orderType argument and ignored it ("For
// simplicity, cancel all matching symbol orders"), so CancelStopLossOrders and
// CancelTakeProfitOrders were the same function. Consequences: moving a break-even
// stop up cancelled every take profit, and re-tiering a drawdown level cancelled the
// stop loss — the position went naked between cancel and re-place. This is the exact
// bug the Trader interface documents as fixed ("don't delete take-profit when
// adjusting stop-loss").
// ============================================================================

// A long position with one stop (rule 2) and one target (rule 1), plus a short
// position's stop (rule 1) to prove side is taken into account too.
func mixedTriggerOrders() []map[string]interface{} {
	return []map[string]interface{}{
		triggerOrderJSON(101, "close-long-order", -50, 2), // long stop loss
		triggerOrderJSON(102, "close-long-order", -50, 1), // long take profit
		triggerOrderJSON(103, "close-short-order", 50, 1), // short stop loss
		triggerOrderJSON(104, "close-short-order", 50, 2), // short take profit
	}
}

func TestCancelStopLossOrdersLeavesTakeProfitsAlone(t *testing.T) {
	cs := newCancelServer(t, mixedTriggerOrders())
	if err := cs.trader().CancelStopLossOrders("BTCUSDT"); err != nil {
		t.Fatalf("CancelStopLossOrders: %v", err)
	}
	got := cs.cancelledIDs()
	// 101 = long stop (rule 2), 103 = short stop (rule 1). Note the rules differ:
	// classification cannot be done from the rule alone.
	want := []string{"101", "103"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("cancelled %v, want only the stop losses %v — take profits must survive", got, want)
	}
}

func TestCancelTakeProfitOrdersLeavesStopLossesAlone(t *testing.T) {
	cs := newCancelServer(t, mixedTriggerOrders())
	if err := cs.trader().CancelTakeProfitOrders("BTCUSDT"); err != nil {
		t.Fatalf("CancelTakeProfitOrders: %v", err)
	}
	got := cs.cancelledIDs()
	want := []string{"102", "104"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("cancelled %v, want only the take profits %v — stop losses must survive", got, want)
	}
}

func TestCancelStopOrdersClearsEverything(t *testing.T) {
	// CancelStopOrders' contract is to leave no protection order behind, so it uses the
	// unfiltered path.
	cs := newCancelServer(t, mixedTriggerOrders())
	if err := cs.trader().CancelStopOrders("BTCUSDT"); err != nil {
		t.Fatalf("CancelStopOrders: %v", err)
	}
	got := cs.cancelledIDs()
	want := []string{"101", "102", "103", "104"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("cancelled %v, want all %v", got, want)
	}
}

func TestFilteredCancelPreservesUnclassifiableOrders(t *testing.T) {
	// An order with no order_type and size 0 has no discoverable side. Guessing would
	// risk destroying the leg we were asked to preserve, so a filtered cancel must skip
	// it — while the unfiltered sweep still clears it.
	orders := []map[string]interface{}{
		triggerOrderJSON(201, "close-long-order", -50, 2), // long stop loss
		triggerOrderJSON(202, "", 0, 2),                   // undecidable
	}
	cs := newCancelServer(t, orders)
	if err := cs.trader().CancelStopLossOrders("BTCUSDT"); err != nil {
		t.Fatalf("CancelStopLossOrders: %v", err)
	}
	if got := cs.cancelledIDs(); strings.Join(got, ",") != "201" {
		t.Fatalf("cancelled %v, want only 201; the undecidable order 202 must be preserved", got)
	}

	cs2 := newCancelServer(t, orders)
	if err := cs2.trader().CancelStopOrders("BTCUSDT"); err != nil {
		t.Fatalf("CancelStopOrders: %v", err)
	}
	if got := cs2.cancelledIDs(); strings.Join(got, ",") != "201,202" {
		t.Fatalf("unfiltered sweep cancelled %v, want 201,202", got)
	}
}
