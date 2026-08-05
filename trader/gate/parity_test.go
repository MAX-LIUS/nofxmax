package gate

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"nofx/store"
	"nofx/trader/types"
)

// parityServer serves an order book, accepts orders, and records cancel calls so the
// optional-capability methods can be asserted at the wire level.
type parityServer struct {
	*httptest.Server
	mu             sync.Mutex
	orders         []string
	cancelledPaths []string
	// failRegularCancel makes the regular cancel endpoint 404 so the fallback to the
	// trigger namespace can be exercised.
	failRegularCancel bool
	// goneOnCancel makes the regular cancel endpoint report the order as already gone.
	goneOnCancel bool
}

func newParityServer(t *testing.T) *parityServer {
	ps := &parityServer{}
	ps.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		path := r.URL.Path
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.Contains(path, "/futures/usdt/order_book"):
			json.NewEncoder(w).Encode(map[string]interface{}{
				// Gate reports depth sizes in CONTRACTS.
				"bids": []map[string]interface{}{{"p": "49999.5", "s": 100}, {"p": "49999.0", "s": 250}},
				"asks": []map[string]interface{}{{"p": "50000.5", "s": 120}, {"p": "50001.0", "s": 300}},
			})
		case strings.Contains(path, "/futures/usdt/contracts/"):
			json.NewEncoder(w).Encode(map[string]interface{}{
				"name": "BTC_USDT", "quanto_multiplier": "0.001", "order_price_round": "0.1",
			})
		case strings.Contains(path, "/futures/usdt/orders/") && r.Method == http.MethodDelete:
			ps.mu.Lock()
			ps.cancelledPaths = append(ps.cancelledPaths, "regular:"+path[strings.LastIndex(path, "/")+1:])
			ps.mu.Unlock()
			if ps.goneOnCancel {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]interface{}{"label": "ORDER_NOT_FOUND"})
				return
			}
			if ps.failRegularCancel {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]interface{}{"label": "INVALID_REQUEST"})
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"id": 42, "contract": "BTC_USDT"})
		case strings.Contains(path, "/futures/usdt/price_orders/") && r.Method == http.MethodDelete:
			ps.mu.Lock()
			ps.cancelledPaths = append(ps.cancelledPaths, "trigger:"+path[strings.LastIndex(path, "/")+1:])
			ps.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]interface{}{"id": 42})
		case strings.Contains(path, "/futures/usdt/orders") && r.Method == http.MethodPost:
			ps.mu.Lock()
			ps.orders = append(ps.orders, string(body))
			ps.mu.Unlock()
			var sent map[string]interface{}
			_ = json.Unmarshal(body, &sent)
			resp := map[string]interface{}{
				"id": 77, "contract": "BTC_USDT", "status": "open",
				"price": sent["price"], "size": sent["size"], "left": sent["size"],
				"text": sent["text"],
			}
			json.NewEncoder(w).Encode(resp)
		case strings.Contains(path, "/leverage"):
			json.NewEncoder(w).Encode(map[string]interface{}{"leverage": "10"})
		case strings.Contains(path, "/futures/usdt/positions"):
			json.NewEncoder(w).Encode([]map[string]interface{}{
				{"contract": "BTC_USDT", "size": 100, "entry_price": "50000"},
			})
		default:
			json.NewEncoder(w).Encode([]map[string]interface{}{})
		}
	}))
	t.Cleanup(ps.Close)
	return ps
}

func (ps *parityServer) trader() *GateTrader {
	tr := NewGateTrader("k", "s")
	tr.client.ChangeBasePath(ps.URL + "/api/v4")
	return tr
}

// ============================================================================
// Optional-capability parity
//
// These methods are not in the base Trader interface. The generic layer reaches them
// through anonymous interface assertions, so a missing method degrades SILENTLY: no
// error, no log, just a feature that never runs on this venue. The compile-time guards
// below are therefore part of the contract, not decoration.
// ============================================================================

func TestGateSatisfiesMakerEntryCapable(t *testing.T) {
	// Mirror of trader.makerEntryCapable (auto_trader_maker_entry.go:15). Missing any
	// one of these four makes every gate entry cross the spread and pay taker fees even
	// with maker entry enabled.
	var _ interface {
		PlaceLimitOrder(req *types.LimitOrderRequest) (*types.LimitOrderResult, error)
		CancelOrder(symbol, orderID string) error
		GetOrderBook(symbol string, depth int) (bids, asks [][]float64, err error)
		GetOrderStatus(symbol string, orderID string) (map[string]interface{}, error)
	} = (*GateTrader)(nil)
}

func TestGateSatisfiesOrderIDCanceller(t *testing.T) {
	// Mirror of trader.orderIDCanceller. cancelProtectionOrderByID refuses to cancel at
	// all without it (break_even_tier_replace.go:180) — correctly, since the only
	// alternative is cancel-by-tag which would take out sibling BE tiers. The effect was
	// that gate could never REPLACE a break-even tier, only stack new ones.
	var _ interface {
		CancelOrder(symbol, orderID string) error
	} = (*GateTrader)(nil)
}

func TestGateSatisfiesTaggedCloser(t *testing.T) {
	// Mirror of the taggedCloser in closePositionByReasonWithOutcome
	// (auto_trader_risk.go:3890). Without it every gate close is attributed by
	// heuristics instead of the mechanism that triggered it.
	var _ interface {
		CloseLongTagged(symbol string, quantity float64, reasonTag string) (map[string]interface{}, error)
		CloseShortTagged(symbol string, quantity float64, reasonTag string) (map[string]interface{}, error)
	} = (*GateTrader)(nil)
}

func TestGetOrderBookShapeMatchesOKX(t *testing.T) {
	// [][]float64{{price, qty}, ...} with quantities in BASE units, so the shared
	// maker-entry logic needs no per-venue branch. Gate reports depth in contracts.
	bids, asks, err := newParityServer(t).trader().GetOrderBook("BTCUSDT", 5)
	if err != nil {
		t.Fatalf("GetOrderBook: %v", err)
	}
	if len(bids) != 2 || len(asks) != 2 {
		t.Fatalf("got %d bids / %d asks, want 2 / 2", len(bids), len(asks))
	}
	if bids[0][0] != 49999.5 {
		t.Fatalf("best bid price = %v, want 49999.5", bids[0][0])
	}
	// 100 contracts * 0.001 = 0.1 base units.
	if bids[0][1] < 0.0999 || bids[0][1] > 0.1001 {
		t.Fatalf("best bid qty = %v, want 0.1 base units (100 contracts * 0.001)", bids[0][1])
	}
	if asks[0][0] != 50000.5 {
		t.Fatalf("best ask price = %v, want 50000.5", asks[0][0])
	}
	// Ordering matters: makerEntryPrice reads index 0 as top of book.
	if bids[0][0] <= bids[1][0] {
		t.Fatal("bids must be best-first (descending price)")
	}
	if asks[0][0] >= asks[1][0] {
		t.Fatal("asks must be best-first (ascending price)")
	}
}

func TestPlaceLimitOrderPostOnlyUsesPocTif(t *testing.T) {
	// Gate's post-only tif is "poc". Sending "gtc" for a PostOnly request would pay
	// taker fees on the very path whose purpose is earning the maker fee.
	ps := newParityServer(t)
	res, err := ps.trader().PlaceLimitOrder(&types.LimitOrderRequest{
		Symbol: "BTCUSDT", Side: "BUY", PositionSide: "LONG",
		Price: 49999.5, Quantity: 0.05, Leverage: 10, PostOnly: true,
	})
	if err != nil {
		t.Fatalf("PlaceLimitOrder: %v", err)
	}
	ps.mu.Lock()
	body := ps.orders[len(ps.orders)-1]
	ps.mu.Unlock()
	if !strings.Contains(body, `"tif":"poc"`) {
		t.Fatalf("order body %s lacks tif=poc; post-only would silently become a taker order", body)
	}
	if res.OrderID != "77" {
		t.Fatalf("OrderID = %q, want 77", res.OrderID)
	}
	if res.Symbol != "BTCUSDT" {
		t.Fatalf("Symbol = %q, want the internal form BTCUSDT", res.Symbol)
	}
	// 50 contracts * 0.001 = 0.05 base units back out again.
	if res.Quantity < 0.0499 || res.Quantity > 0.0501 {
		t.Fatalf("Quantity = %v, want 0.05 base units", res.Quantity)
	}
}

func TestPlaceLimitOrderSellSendsNegativeSize(t *testing.T) {
	ps := newParityServer(t)
	if _, err := ps.trader().PlaceLimitOrder(&types.LimitOrderRequest{
		Symbol: "BTCUSDT", Side: "SELL", Price: 50000.5, Quantity: 0.05, Leverage: 10,
	}); err != nil {
		t.Fatalf("PlaceLimitOrder: %v", err)
	}
	ps.mu.Lock()
	body := ps.orders[len(ps.orders)-1]
	ps.mu.Unlock()
	if !strings.Contains(body, `"size":-50`) {
		t.Fatalf("order body %s must carry a negative size for a SELL", body)
	}
	if !strings.Contains(body, `"tif":"gtc"`) {
		t.Fatalf("order body %s should default to gtc when PostOnly is not requested", body)
	}
}

func TestPlaceLimitOrderRejectsSubContractQuantity(t *testing.T) {
	// Rounding 0.0004 BTC up to one whole contract (0.001) would silently place 2.5x the
	// requested size. Refusing is the only honest outcome.
	ps := newParityServer(t)
	if _, err := ps.trader().PlaceLimitOrder(&types.LimitOrderRequest{
		Symbol: "BTCUSDT", Side: "BUY", Price: 49999.5, Quantity: 0.0004, Leverage: 10,
	}); err == nil {
		t.Fatal("expected an error when the quantity is below one contract")
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if len(ps.orders) != 0 {
		t.Fatalf("an order was sent for a sub-contract quantity: %v", ps.orders)
	}
}

func TestPlaceLimitOrderValidatesInputs(t *testing.T) {
	tr := newParityServer(t).trader()
	if _, err := tr.PlaceLimitOrder(nil); err == nil {
		t.Fatal("expected an error for a nil request")
	}
	if _, err := tr.PlaceLimitOrder(&types.LimitOrderRequest{Symbol: "BTCUSDT", Side: "BUY", Price: 0, Quantity: 1}); err == nil {
		t.Fatal("expected an error for a zero price")
	}
	if _, err := tr.PlaceLimitOrder(&types.LimitOrderRequest{Symbol: "BTCUSDT", Side: "BUY", Price: 100, Quantity: 0}); err == nil {
		t.Fatal("expected an error for a zero quantity")
	}
}

func TestCancelOrderUsesRegularNamespaceFirst(t *testing.T) {
	ps := newParityServer(t)
	if err := ps.trader().CancelOrder("BTCUSDT", "12345"); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if len(ps.cancelledPaths) != 1 || ps.cancelledPaths[0] != "regular:12345" {
		t.Fatalf("cancel calls = %v, want exactly [regular:12345]", ps.cancelledPaths)
	}
}

func TestCancelOrderFallsBackToTriggerNamespace(t *testing.T) {
	// Gate keeps regular and trigger orders in separate id namespaces, and callers hold
	// an id without knowing which one produced it. Failing on the first miss would leave
	// resting trigger orders uncancellable by id.
	ps := newParityServer(t)
	ps.failRegularCancel = true
	if err := ps.trader().CancelOrder("BTCUSDT", "999"); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if len(ps.cancelledPaths) != 2 || ps.cancelledPaths[1] != "trigger:999" {
		t.Fatalf("cancel calls = %v, want the trigger-namespace fallback", ps.cancelledPaths)
	}
}

func TestCancelOrderTreatsAlreadyGoneAsSuccess(t *testing.T) {
	// The caller's goal is "this order must not be resting". An order that is already
	// gone satisfies that, and reporting an error there makes idempotent retries look
	// like failures.
	ps := newParityServer(t)
	ps.goneOnCancel = true
	if err := ps.trader().CancelOrder("BTCUSDT", "555"); err != nil {
		t.Fatalf("CancelOrder on an already-gone order should succeed, got %v", err)
	}
}

func TestCancelOrderRejectsEmptyID(t *testing.T) {
	ps := newParityServer(t)
	if err := ps.trader().CancelOrder("BTCUSDT", "  "); err == nil {
		t.Fatal("expected an error for an empty order id")
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if len(ps.cancelledPaths) != 0 {
		t.Fatalf("a cancel was attempted for an empty id: %v", ps.cancelledPaths)
	}
}

func TestTaggedCloseEncodesReasonInText(t *testing.T) {
	ps := newParityServer(t)
	res, err := ps.trader().CloseLongTagged("BTCUSDT", 0, store.MechTimeStop)
	if err != nil {
		t.Fatalf("CloseLongTagged: %v", err)
	}
	ps.mu.Lock()
	body := ps.orders[len(ps.orders)-1]
	ps.mu.Unlock()

	want := "t-nofx" + store.CodeForReason(store.MechTimeStop)
	if !strings.Contains(body, `"text":"`+want) {
		t.Fatalf("close order body %s must carry the encoded mechanism prefix %q", body, want)
	}
	// orderId is the match key persistCloseReasonFromOrderResult uses to attribute the
	// close, so it must be present and non-empty.
	if id, _ := res["orderId"].(string); id == "" {
		t.Fatalf("close result %v carries no orderId; the close reason cannot be persisted", res)
	}
}

func TestSanitizeGateTextEnforcesGateRules(t *testing.T) {
	// A malformed text makes Gate reject the whole order, so an unusable client id must
	// degrade to something legal (or to empty, letting the caller use the broker tag)
	// rather than propagate.
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"t-nofxBE1234", "t-nofxBE1234"},
		{"nofxBE1234", "t-nofxBE1234"},
		{"bad/chars:here!", "t-badcharshere"},
		{"///", ""},
		{strings.Repeat("a", 60), "t-" + strings.Repeat("a", 28)},
	}
	for _, tc := range cases {
		got := sanitizeGateText(tc.in)
		if got != tc.want {
			t.Fatalf("sanitizeGateText(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if got != "" {
			if payload := strings.TrimPrefix(got, "t-"); len(payload) > 28 {
				t.Fatalf("sanitizeGateText(%q) payload is %d bytes, over Gate's 28-byte limit", tc.in, len(payload))
			}
		}
	}
}
