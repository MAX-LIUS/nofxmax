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

// positionServer serves a configurable position list plus contract metadata, and
// records order/leverage request bodies.
type positionServer struct {
	*httptest.Server
	mu        sync.Mutex
	positions []map[string]interface{}
	orders    []string
	leverage  []string
}

func newPositionServer(t *testing.T, positions []map[string]interface{}) *positionServer {
	ps := &positionServer{positions: positions}
	ps.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		path := r.URL.Path
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.Contains(path, "/leverage"):
			ps.mu.Lock()
			ps.leverage = append(ps.leverage, r.URL.RawQuery)
			ps.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]interface{}{"leverage": "0"})
		case strings.Contains(path, "/futures/usdt/positions"):
			ps.mu.Lock()
			pos := ps.positions
			ps.mu.Unlock()
			json.NewEncoder(w).Encode(pos)
		case strings.Contains(path, "/futures/usdt/contracts/"):
			json.NewEncoder(w).Encode(map[string]interface{}{
				"name":              "BTC_USDT",
				"quanto_multiplier": "0.001",
				"order_price_round": "0.1",
			})
		case strings.Contains(path, "/futures/usdt/orders") && r.Method == http.MethodPost:
			ps.mu.Lock()
			ps.orders = append(ps.orders, string(body))
			ps.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id": 555, "contract": "BTC_USDT", "fill_price": "50000",
				"status": "finished", "finish_as": "filled",
			})
		default:
			json.NewEncoder(w).Encode([]map[string]interface{}{})
		}
	}))
	t.Cleanup(ps.Close)
	return ps
}

func (ps *positionServer) trader() *GateTrader {
	tr := NewGateTrader("k", "s")
	tr.client.ChangeBasePath(ps.URL + "/api/v4")
	return tr
}

func (ps *positionServer) lastOrder(t *testing.T) gateapi.FuturesOrder {
	t.Helper()
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if len(ps.orders) == 0 {
		t.Fatal("no order reached the wire")
	}
	var got gateapi.FuturesOrder
	if err := json.Unmarshal([]byte(ps.orders[len(ps.orders)-1]), &got); err != nil {
		t.Fatalf("order body not a FuturesOrder: %v", err)
	}
	return got
}

// ============================================================================
// GetPositions — the field contract the whole protection stack depends on
// ============================================================================

func TestGetPositionsReturnsInternalSymbol(t *testing.T) {
	// This was the single most damaging gate bug: GetPositions returned Gate's native
	// "BTC_USDT" while GetOpenOrders returned "BTCUSDT". The reconciler matches
	// positions to protection orders by this exact string, so it NEVER matched and the
	// protection system was completely blind to gate positions.
	ps := newPositionServer(t, []map[string]interface{}{{
		"contract": "BTC_USDT", "size": 50, "entry_price": "50000",
		"mark_price": "50500", "unrealised_pnl": "25", "liq_price": "45000",
		"leverage": "10",
	}})
	positions, err := ps.trader().GetPositions()
	if err != nil {
		t.Fatalf("GetPositions: %v", err)
	}
	if len(positions) != 1 {
		t.Fatalf("got %d positions, want 1", len(positions))
	}
	if got := positions[0]["symbol"]; got != "BTCUSDT" {
		t.Fatalf("symbol = %v, want BTCUSDT (internal form). Gate's native BTC_USDT never matches order symbols", got)
	}
}

func TestGetPositionsLeverageIsFloat64ForGenericLayer(t *testing.T) {
	// The generic layer type-asserts pos["leverage"].(float64) in six places
	// (auto_trader_decision.go:197,398; auto_trader_replace.go:93;
	// auto_trader_risk.go:4575; auto_trader_loop.go:751; position_snapshot.go:54).
	// An int here fails every assertion silently and leverage reads as 0.
	ps := newPositionServer(t, []map[string]interface{}{{
		"contract": "BTC_USDT", "size": 50, "entry_price": "50000", "leverage": "10",
	}})
	positions, err := ps.trader().GetPositions()
	if err != nil {
		t.Fatalf("GetPositions: %v", err)
	}
	lev, ok := positions[0]["leverage"].(float64)
	if !ok {
		t.Fatalf("leverage is %T, want float64 to match okx/binance", positions[0]["leverage"])
	}
	if lev != 10 {
		t.Fatalf("leverage = %v, want 10", lev)
	}
}

func TestGetPositionsCrossMarginResolvesRealLeverage(t *testing.T) {
	// Gate reports leverage="0" for CROSS margin and puts the real multiplier in
	// cross_leverage_limit. Passing the raw 0 upstream makes every cross position look
	// like 0x, and the generic layer guards on `lev > 0`, so leverage-aware sizing and
	// risk math silently switch off.
	ps := newPositionServer(t, []map[string]interface{}{{
		"contract": "BTC_USDT", "size": 50, "entry_price": "50000",
		"leverage": "0", "cross_leverage_limit": "20",
	}})
	positions, err := ps.trader().GetPositions()
	if err != nil {
		t.Fatalf("GetPositions: %v", err)
	}
	if lev := positions[0]["leverage"].(float64); lev != 20 {
		t.Fatalf("cross leverage = %v, want 20 from cross_leverage_limit", lev)
	}
	if mode := positions[0]["mgnMode"]; mode != "cross" {
		t.Fatalf("mgnMode = %v, want cross (leverage==0 IS cross margin on Gate)", mode)
	}
}

func TestGetPositionsIsolatedMarginReportsIsolated(t *testing.T) {
	ps := newPositionServer(t, []map[string]interface{}{{
		"contract": "BTC_USDT", "size": 50, "entry_price": "50000", "leverage": "10",
	}})
	positions, _ := ps.trader().GetPositions()
	if mode := positions[0]["mgnMode"]; mode != "isolated" {
		t.Fatalf("mgnMode = %v, want isolated for a positive leverage", mode)
	}
}

func TestGetPositionsSideFromSizeSignAndDualMode(t *testing.T) {
	cases := []struct {
		name string
		pos  map[string]interface{}
		want string
	}{
		{"single mode long", map[string]interface{}{"contract": "BTC_USDT", "size": 50}, "long"},
		{"single mode short", map[string]interface{}{"contract": "BTC_USDT", "size": -50}, "short"},
		// In dual (hedge) mode Gate reports direction in `mode` and size stays positive
		// for the long leg, so the size sign alone is not authoritative.
		{"dual long", map[string]interface{}{"contract": "BTC_USDT", "size": 50, "mode": "dual_long"}, "long"},
		{"dual short", map[string]interface{}{"contract": "BTC_USDT", "size": 50, "mode": "dual_short"}, "short"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ps := newPositionServer(t, []map[string]interface{}{tc.pos})
			positions, err := ps.trader().GetPositions()
			if err != nil {
				t.Fatalf("GetPositions: %v", err)
			}
			if got := positions[0]["side"]; got != tc.want {
				t.Fatalf("side = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGetPositionsConvertsTimesToMilliseconds(t *testing.T) {
	// The generic layer reads createdTime as ms (auto_trader_loop.go:775,
	// protection_reconciler.go:1493). Gate returns seconds; passing them through makes
	// every position look ~56 years old, which max_hold would close immediately.
	ps := newPositionServer(t, []map[string]interface{}{{
		"contract": "BTC_USDT", "size": 50, "open_time": 1700000000, "update_time": 1700000060,
	}})
	positions, err := ps.trader().GetPositions()
	if err != nil {
		t.Fatalf("GetPositions: %v", err)
	}
	if got := positions[0]["createdTime"].(int64); got != 1700000000000 {
		t.Fatalf("createdTime = %d, want 1700000000000 ms", got)
	}
	if got := positions[0]["updatedTime"].(int64); got != 1700000060000 {
		t.Fatalf("updatedTime = %d, want 1700000060000 ms", got)
	}
}

// ============================================================================
// Close path
// ============================================================================

func TestFullCloseUsesExchangeContractCountNotFloatRoundTrip(t *testing.T) {
	// A full close must land the position exactly flat. Deriving contracts from the
	// float base quantity (contracts -> base qty -> back to contracts) and truncating
	// with int64() leaves a residual position, and by then the protection orders are
	// already cancelled — so the residual is unprotected.
	//
	// 2001 contracts at a 0.001 multiplier is a genuine loss case, verified by
	// arithmetic rather than assumed: 2001*0.001 = 2.001, and 2.001/0.001 evaluates to
	// 2000.9999999999998 in float64, which int64() truncates to 2000. One contract is
	// left open. (Many sizes DO round-trip exactly, which is what makes this class of
	// bug survive casual testing.)
	ps := newPositionServer(t, []map[string]interface{}{{
		"contract": "BTC_USDT", "size": 2001, "entry_price": "50000",
	}})
	if _, err := ps.trader().CloseLong("BTCUSDT", 0); err != nil {
		t.Fatalf("CloseLong: %v", err)
	}
	order := ps.lastOrder(t)
	if order.Size != -2001 {
		t.Fatalf("close size = %d, want -2001 exactly; anything smaller leaves an unprotected residual", order.Size)
	}
	if !order.ReduceOnly {
		t.Fatal("close order must be reduce-only so it can never flip the position")
	}
}

func TestFullCloseShortSendsPositiveSize(t *testing.T) {
	ps := newPositionServer(t, []map[string]interface{}{{
		"contract": "BTC_USDT", "size": -1500, "entry_price": "50000",
	}})
	if _, err := ps.trader().CloseShort("BTCUSDT", 0); err != nil {
		t.Fatalf("CloseShort: %v", err)
	}
	if order := ps.lastOrder(t); order.Size != 1500 {
		t.Fatalf("close size = %d, want +1500 (buying back a short)", order.Size)
	}
}

func TestPartialCloseRoundsRatherThanTruncates(t *testing.T) {
	// 0.0299 BTC / 0.001 = 29.9 contracts. Truncation gives 29 and leaves a residual;
	// rounding gives 30.
	ps := newPositionServer(t, []map[string]interface{}{{
		"contract": "BTC_USDT", "size": 100, "entry_price": "50000",
	}})
	if _, err := ps.trader().CloseLong("BTCUSDT", 0.0299); err != nil {
		t.Fatalf("CloseLong: %v", err)
	}
	if order := ps.lastOrder(t); order.Size != -30 {
		t.Fatalf("partial close size = %d, want -30 (rounded, not truncated to 29)", order.Size)
	}
}

func TestFullCloseFailsLoudlyWhenPositionMissing(t *testing.T) {
	// Silently sending size=0 would be a no-op the caller reads as success, leaving the
	// system believing it is flat when it is not.
	ps := newPositionServer(t, []map[string]interface{}{})
	if _, err := ps.trader().CloseLong("BTCUSDT", 0); err == nil {
		t.Fatal("expected an error when no long position exists")
	}
}

func TestCloseFullOnlyMatchesRequestedSide(t *testing.T) {
	// A short position must not satisfy a CloseLong request.
	ps := newPositionServer(t, []map[string]interface{}{{
		"contract": "BTC_USDT", "size": -1500, "entry_price": "50000",
	}})
	if _, err := ps.trader().CloseLong("BTCUSDT", 0); err == nil {
		t.Fatal("expected an error: only a SHORT position exists, CloseLong must not match it")
	}
}

// ============================================================================
// Margin mode
// ============================================================================

func TestSetMarginModeCrossIsEncodedInLeverageCall(t *testing.T) {
	// Gate has no margin-mode endpoint. Cross margin IS leverage=0 plus
	// cross_leverage_limit=<n>. The old stub returned nil without doing anything, so
	// is_cross_margin was silently ignored on every gate trade.
	ps := newPositionServer(t, nil)
	tr := ps.trader()
	if err := tr.SetMarginMode("BTCUSDT", true); err != nil {
		t.Fatalf("SetMarginMode: %v", err)
	}
	if err := tr.SetLeverage("BTCUSDT", 20); err != nil {
		t.Fatalf("SetLeverage: %v", err)
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if len(ps.leverage) == 0 {
		t.Fatal("no leverage request reached the wire")
	}
	q := ps.leverage[len(ps.leverage)-1]
	if !strings.Contains(q, "leverage=0") {
		t.Fatalf("leverage query = %q, want leverage=0 to select cross margin", q)
	}
	if !strings.Contains(q, "cross_leverage_limit=20") {
		t.Fatalf("leverage query = %q, want cross_leverage_limit=20 to carry the real multiplier", q)
	}
}

func TestSetMarginModeIsolatedSendsPositiveLeverage(t *testing.T) {
	ps := newPositionServer(t, nil)
	tr := ps.trader()
	if err := tr.SetMarginMode("BTCUSDT", false); err != nil {
		t.Fatalf("SetMarginMode: %v", err)
	}
	if err := tr.SetLeverage("BTCUSDT", 20); err != nil {
		t.Fatalf("SetLeverage: %v", err)
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	q := ps.leverage[len(ps.leverage)-1]
	if !strings.Contains(q, "leverage=20") {
		t.Fatalf("leverage query = %q, want leverage=20 for isolated margin", q)
	}
	if strings.Contains(q, "cross_leverage_limit") {
		t.Fatalf("leverage query = %q must not carry cross_leverage_limit in isolated mode", q)
	}
}

// ============================================================================
// Contract metadata guards
// ============================================================================

// badContractServer returns contract metadata with an unusable quanto_multiplier and
// otherwise behaves like a healthy venue, so a test failure can only come from the
// multiplier handling and not from an unrelated mock gap.
func badContractServer(t *testing.T) (*GateTrader, *[]string) {
	var orders []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		path := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(path, "/futures/usdt/contracts/"):
			// quanto_multiplier "0" is the hazard: quantity/0 is +Inf, and int64(+Inf)
			// in Go is -9223372036854775808. That is <= 0, so the `size <= 0 -> size = 1`
			// clamp silently converts a metadata failure into a 1-CONTRACT order — a
			// position sized nothing like what risk approved, reported as success.
			json.NewEncoder(w).Encode(map[string]interface{}{
				"name": "BTC_USDT", "quanto_multiplier": "0", "order_price_round": "0.1",
			})
		case strings.Contains(path, "/leverage"):
			json.NewEncoder(w).Encode(map[string]interface{}{"leverage": "10"})
		case strings.Contains(path, "/futures/usdt/orders") && r.Method == http.MethodPost:
			orders = append(orders, string(body))
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id": 900, "contract": "BTC_USDT", "fill_price": "50000",
				"status": "finished", "finish_as": "filled",
			})
		case strings.Contains(path, "/futures/usdt/price_orders") && r.Method == http.MethodPost:
			orders = append(orders, string(body))
			json.NewEncoder(w).Encode(map[string]interface{}{"id": 901})
		default:
			json.NewEncoder(w).Encode([]map[string]interface{}{})
		}
	}))
	t.Cleanup(srv.Close)
	tr := NewGateTrader("k", "s")
	tr.client.ChangeBasePath(srv.URL + "/api/v4")
	return tr, &orders
}

func TestOpenLongRejectsBadContractMetadata(t *testing.T) {
	tr, orders := badContractServer(t)
	if _, err := tr.OpenLong("BTCUSDT", 0.05, 10); err == nil {
		t.Fatal("expected an error for quanto_multiplier=0; without the guard the size clamp silently places a 1-contract order")
	}
	// The stronger assertion: nothing may reach the exchange at all.
	if len(*orders) != 0 {
		t.Fatalf("an order was sent despite unusable contract metadata: %v", *orders)
	}
}

func TestOpenShortRejectsBadContractMetadata(t *testing.T) {
	tr, orders := badContractServer(t)
	if _, err := tr.OpenShort("BTCUSDT", 0.05, 10); err == nil {
		t.Fatal("expected an error for quanto_multiplier=0")
	}
	if len(*orders) != 0 {
		t.Fatalf("an order was sent despite unusable contract metadata: %v", *orders)
	}
}

func TestProtectionRejectsBadContractMetadata(t *testing.T) {
	// Failing loudly is right here: a silently mis-sized protection order is worse than
	// none, because the caller records it as placed and stops retrying.
	tr, orders := badContractServer(t)
	if err := tr.SetStopLoss("BTCUSDT", "LONG", 0.05, 49000); err == nil {
		t.Fatal("expected an error for quanto_multiplier=0")
	}
	if len(*orders) != 0 {
		t.Fatalf("a protection order was sent despite unusable contract metadata: %v", *orders)
	}
}
