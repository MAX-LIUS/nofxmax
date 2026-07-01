package binance

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/adshao/go-binance/v2/futures"
	"github.com/stretchr/testify/assert"
	"nofx/trader/testutil"
	"nofx/trader/types"
)

// ============================================================
// 1. BinanceFuturesTestSuite - Inherits base test suite
// ============================================================

// BinanceFuturesTestSuite Binance Futures trader test suite
// Inherits TraderTestSuite and adds Binance Futures specific mock logic
type BinanceFuturesTestSuite struct {
	*testutil.TraderTestSuite // Embeds base test suite
	mockServer                *httptest.Server
}

// NewBinanceFuturesTestSuite Creates Binance Futures test suite
func NewBinanceFuturesTestSuite(t *testing.T) *BinanceFuturesTestSuite {
	// Create mock HTTP server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return different mock responses based on URL path
		path := r.URL.Path

		var respBody interface{}

		switch {
		// Mock GetBalance - /fapi/v2/balance
		case path == "/fapi/v2/balance":
			respBody = []map[string]interface{}{
				{
					"accountAlias":       "test",
					"asset":              "USDT",
					"balance":            "10000.00",
					"crossWalletBalance": "10000.00",
					"crossUnPnl":         "100.50",
					"availableBalance":   "8000.00",
					"maxWithdrawAmount":  "8000.00",
				},
			}

		// Mock GetAccount - /fapi/v2/account
		case path == "/fapi/v2/account":
			respBody = map[string]interface{}{
				"totalWalletBalance":    "10000.00",
				"availableBalance":      "8000.00",
				"totalUnrealizedProfit": "100.50",
				"assets": []map[string]interface{}{
					{
						"asset":                  "USDT",
						"walletBalance":          "10000.00",
						"unrealizedProfit":       "100.50",
						"marginBalance":          "10100.50",
						"maintMargin":            "200.00",
						"initialMargin":          "2000.00",
						"positionInitialMargin":  "2000.00",
						"openOrderInitialMargin": "0.00",
						"crossWalletBalance":     "10000.00",
						"crossUnPnl":             "100.50",
						"availableBalance":       "8000.00",
						"maxWithdrawAmount":      "8000.00",
					},
				},
			}

		// Mock GetPositions - /fapi/v2/positionRisk
		case path == "/fapi/v2/positionRisk":
			respBody = []map[string]interface{}{
				{
					"symbol":           "BTCUSDT",
					"positionAmt":      "0.5",
					"entryPrice":       "50000.00",
					"markPrice":        "50500.00",
					"unRealizedProfit": "250.00",
					"liquidationPrice": "45000.00",
					"leverage":         "10",
					"positionSide":     "LONG",
				},
			}

		// Mock GetMarketPrice - /fapi/v1/ticker/price and /fapi/v2/ticker/price
		case path == "/fapi/v1/ticker/price" || path == "/fapi/v2/ticker/price":
			symbol := r.URL.Query().Get("symbol")
			if symbol == "" {
				// Return all prices
				respBody = []map[string]interface{}{
					{"Symbol": "BTCUSDT", "Price": "50000.00", "Time": 1234567890},
					{"Symbol": "ETHUSDT", "Price": "3000.00", "Time": 1234567890},
				}
			} else if symbol == "INVALIDUSDT" {
				// Return error
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"code": -1121,
					"msg":  "Invalid symbol.",
				})
				return
			} else {
				// Return single price (note: even with symbol parameter, return array)
				price := "50000.00"
				if symbol == "ETHUSDT" {
					price = "3000.00"
				}
				respBody = []map[string]interface{}{
					{
						"Symbol": symbol,
						"Price":  price,
						"Time":   1234567890,
					},
				}
			}

		// Mock ExchangeInfo - /fapi/v1/exchangeInfo
		case path == "/fapi/v1/exchangeInfo":
			respBody = map[string]interface{}{
				"symbols": []map[string]interface{}{
					{
						"symbol":             "BTCUSDT",
						"status":             "TRADING",
						"baseAsset":          "BTC",
						"quoteAsset":         "USDT",
						"pricePrecision":     2,
						"quantityPrecision":  3,
						"baseAssetPrecision": 8,
						"quotePrecision":     8,
						"filters": []map[string]interface{}{
							{
								"filterType": "PRICE_FILTER",
								"minPrice":   "0.01",
								"maxPrice":   "1000000",
								"tickSize":   "0.01",
							},
							{
								"filterType": "LOT_SIZE",
								"minQty":     "0.001",
								"maxQty":     "10000",
								"stepSize":   "0.001",
							},
						},
					},
					{
						"symbol":             "ETHUSDT",
						"status":             "TRADING",
						"baseAsset":          "ETH",
						"quoteAsset":         "USDT",
						"pricePrecision":     2,
						"quantityPrecision":  3,
						"baseAssetPrecision": 8,
						"quotePrecision":     8,
						"filters": []map[string]interface{}{
							{
								"filterType": "PRICE_FILTER",
								"minPrice":   "0.01",
								"maxPrice":   "100000",
								"tickSize":   "0.01",
							},
							{
								"filterType": "LOT_SIZE",
								"minQty":     "0.001",
								"maxQty":     "10000",
								"stepSize":   "0.001",
							},
						},
					},
				},
			}

		// Mock CreateOrder - /fapi/v1/order (POST)
		case path == "/fapi/v1/order" && r.Method == "POST":
			symbol := r.FormValue("symbol")
			if symbol == "" {
				symbol = "BTCUSDT"
			}
			respBody = map[string]interface{}{
				"orderId":       123456,
				"symbol":        symbol,
				"status":        "FILLED",
				"clientOrderId": r.FormValue("newClientOrderId"),
				"price":         r.FormValue("price"),
				"avgPrice":      r.FormValue("price"),
				"origQty":       r.FormValue("quantity"),
				"executedQty":   r.FormValue("quantity"),
				"cumQty":        r.FormValue("quantity"),
				"cumQuote":      "1000.00",
				"timeInForce":   r.FormValue("timeInForce"),
				"type":          r.FormValue("type"),
				"reduceOnly":    r.FormValue("reduceOnly") == "true",
				"side":          r.FormValue("side"),
				"positionSide":  r.FormValue("positionSide"),
				"stopPrice":     r.FormValue("stopPrice"),
				"workingType":   r.FormValue("workingType"),
			}

		// Mock CancelOrder - /fapi/v1/order (DELETE)
		case path == "/fapi/v1/order" && r.Method == "DELETE":
			respBody = map[string]interface{}{
				"orderId": 123456,
				"symbol":  r.URL.Query().Get("symbol"),
				"status":  "CANCELED",
			}

		// Mock ListOpenOrders - /fapi/v1/openOrders
		case path == "/fapi/v1/openOrders":
			respBody = []map[string]interface{}{}

		// Mock ListOpenAlgoOrders - /fapi/v1/openAlgoOrders
		// One TRAILING_STOP_MARKET short-close order, used to verify GetOpenOrders
		// reports native trailing as ActivationStatus="activated" (Binance-only fix
		// preventing the OKX-oriented phantom-activation misfire).
		case path == "/fapi/v1/openAlgoOrders":
			respBody = []map[string]interface{}{
				{
					"algoId":       int64(555001),
					"orderType":    "TRAILING_STOP_MARKET",
					"symbol":       "BTCUSDT",
					"side":         "BUY",
					"positionSide": "SHORT",
					"quantity":     "0.010",
					"algoStatus":   "WORKING",
					"triggerPrice": "50000.00",
					"price":        "0",
				},
			}

		// Mock CancelAllOrders - /fapi/v1/allOpenOrders (DELETE)
		case path == "/fapi/v1/allOpenOrders" && r.Method == "DELETE":
			respBody = map[string]interface{}{
				"code": 200,
				"msg":  "The operation of cancel all open order is done.",
			}

		// Mock SetLeverage - /fapi/v1/leverage
		case path == "/fapi/v1/leverage":
			// Convert string to integer
			leverageStr := r.FormValue("leverage")
			leverage := 10 // default value
			if leverageStr != "" {
				// Note: here we return an integer directly, not a string
				fmt.Sscanf(leverageStr, "%d", &leverage)
			}
			respBody = map[string]interface{}{
				"leverage":         leverage,
				"maxNotionalValue": "1000000",
				"symbol":           r.FormValue("symbol"),
			}

		// Mock SetMarginType - /fapi/v1/marginType
		case path == "/fapi/v1/marginType":
			respBody = map[string]interface{}{
				"code": 200,
				"msg":  "success",
			}

		// Mock ChangePositionMode - /fapi/v1/positionSide/dual
		case path == "/fapi/v1/positionSide/dual":
			respBody = map[string]interface{}{
				"code": 200,
				"msg":  "success",
			}

		// Mock ServerTime - /fapi/v1/time
		case path == "/fapi/v1/time":
			respBody = map[string]interface{}{
				"serverTime": 1234567890000,
			}

		// Default: empty response
		default:
			respBody = map[string]interface{}{}
		}

		// Serialize response
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(respBody)
	}))

	// Create futures.Client and configure to use mock server
	client := futures.NewClient("test_api_key", "test_secret_key")
	client.BaseURL = mockServer.URL
	client.HTTPClient = mockServer.Client()

	// Create FuturesTrader
	traderInstance := &FuturesTrader{
		client:        client,
		cacheDuration: 0, // disable cache for testing
	}

	// Create base suite
	baseSuite := testutil.NewTraderTestSuite(t, traderInstance)

	return &BinanceFuturesTestSuite{
		TraderTestSuite: baseSuite,
		mockServer:      mockServer,
	}
}

// Cleanup cleans up resources
func (s *BinanceFuturesTestSuite) Cleanup() {
	if s.mockServer != nil {
		s.mockServer.Close()
	}
	s.TraderTestSuite.Cleanup()
}

// ============================================================
// 2. Run common tests using BinanceFuturesTestSuite
// ============================================================

// TestFuturesTrader_InterfaceCompliance tests interface compliance
func TestFuturesTrader_InterfaceCompliance(t *testing.T) {
	var _ types.Trader = (*FuturesTrader)(nil)
}

// TestFuturesTrader_CommonInterface runs all common interface tests using test suite
func TestFuturesTrader_CommonInterface(t *testing.T) {
	// Create test suite
	suite := NewBinanceFuturesTestSuite(t)
	defer suite.Cleanup()

	// Run all common interface tests
	suite.RunAllTests()
}

// ============================================================
// 3. Binance Futures specific unit tests
// ============================================================

// TestNewFuturesTrader tests creating Binance Futures trader
func TestNewFuturesTrader(t *testing.T) {
	// Create mock HTTP server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		var respBody interface{}

		switch path {
		case "/fapi/v1/time":
			respBody = map[string]interface{}{
				"serverTime": 1234567890000,
			}
		case "/fapi/v1/positionSide/dual":
			respBody = map[string]interface{}{
				"code": 200,
				"msg":  "success",
			}
		default:
			respBody = map[string]interface{}{}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(respBody)
	}))
	defer mockServer.Close()

	// Test successful creation
	t1 := NewFuturesTrader("test_api_key", "test_secret_key", "test_user")

	// Modify client to use mock server
	t1.client.BaseURL = mockServer.URL
	t1.client.HTTPClient = mockServer.Client()

	assert.NotNil(t, t1)
	assert.NotNil(t, t1.client)
	assert.Equal(t, 15*time.Second, t1.cacheDuration)
}

// TestCalculatePositionSize tests position size calculation
func TestCalculatePositionSize(t *testing.T) {
	ft := &FuturesTrader{}

	tests := []struct {
		name         string
		balance      float64
		riskPercent  float64
		price        float64
		leverage     int
		wantQuantity float64
	}{
		{
			name:         "normal calculation",
			balance:      10000,
			riskPercent:  2,
			price:        50000,
			leverage:     10,
			wantQuantity: 0.04, // (10000 * 0.02 * 10) / 50000 = 0.04
		},
		{
			name:         "high leverage",
			balance:      10000,
			riskPercent:  1,
			price:        3000,
			leverage:     20,
			wantQuantity: 0.6667, // (10000 * 0.01 * 20) / 3000 = 0.6667
		},
		{
			name:         "low risk",
			balance:      5000,
			riskPercent:  0.5,
			price:        50000,
			leverage:     5,
			wantQuantity: 0.0025, // (5000 * 0.005 * 5) / 50000 = 0.0025
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			quantity := ft.CalculatePositionSize(tt.balance, tt.riskPercent, tt.price, tt.leverage)
			assert.InDelta(t, tt.wantQuantity, quantity, 0.0001, "calculated position size is incorrect")
		})
	}
}

// TestGetBrOrderID tests order ID generation
func TestGetBrOrderID(t *testing.T) {
	// Test 3 times to ensure each generated ID is unique
	ids := make(map[string]bool)
	for i := 0; i < 3; i++ {
		id := getBrOrderID()

		// Check format
		assert.True(t, strings.HasPrefix(id, "x-KzrpZaP9"), "order ID should start with x-KzrpZaP9")

		// Check length (should be <= 32)
		assert.LessOrEqual(t, len(id), 32, "order ID length should not exceed 32 characters")

		// Check uniqueness
		assert.False(t, ids[id], "order ID should be unique")
		ids[id] = true
	}
}

// TestIsMakerTakeProfitLimit locks in the exact signature that distinguishes our
// post-only maker take-profit from a maker entry limit or a foreign/grid order.
// Misclassifying an entry as a TP (or vice versa) would corrupt protection
// attribution, so each discriminating field is covered.
func TestIsMakerTakeProfitLimit(t *testing.T) {
	const ours = "x-KzrpZaP91234567890123abcd1234"
	mk := func(typ futures.OrderType, tif futures.TimeInForceType, side futures.SideType, ps futures.PositionSideType, cid string, ro bool) *futures.Order {
		return &futures.Order{Type: typ, TimeInForce: tif, Side: side, PositionSide: ps, ClientOrderID: cid, ReduceOnly: ro}
	}
	cases := []struct {
		name string
		o    *futures.Order
		want bool
	}{
		{"short maker TP (BUY closes short)", mk(futures.OrderTypeLimit, futures.TimeInForceTypeGTX, futures.SideTypeBuy, futures.PositionSideTypeShort, ours, false), true},
		{"long maker TP (SELL closes long)", mk(futures.OrderTypeLimit, futures.TimeInForceTypeGTX, futures.SideTypeSell, futures.PositionSideTypeLong, ours, false), true},
		{"short maker ENTRY (SELL opens short) - not TP", mk(futures.OrderTypeLimit, futures.TimeInForceTypeGTX, futures.SideTypeSell, futures.PositionSideTypeShort, ours, false), false},
		{"long maker ENTRY (BUY opens long) - not TP", mk(futures.OrderTypeLimit, futures.TimeInForceTypeGTX, futures.SideTypeBuy, futures.PositionSideTypeLong, ours, false), false},
		{"GTC grid limit - not post-only, not TP", mk(futures.OrderTypeLimit, futures.TimeInForceTypeGTC, futures.SideTypeBuy, futures.PositionSideTypeShort, ours, false), false},
		{"foreign order (no broker prefix) - not TP", mk(futures.OrderTypeLimit, futures.TimeInForceTypeGTX, futures.SideTypeBuy, futures.PositionSideTypeShort, "web_manual_123", false), false},
		{"non-LIMIT order - not a TP", mk(futures.OrderTypeMarket, futures.TimeInForceTypeGTC, futures.SideTypeBuy, futures.PositionSideTypeShort, ours, false), false},
		{"nil order", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isMakerTakeProfitLimit(c.o); got != c.want {
				t.Fatalf("isMakerTakeProfitLimit=%v, want %v", got, c.want)
			}
		})
	}
}

// TestCallbackRateNormalization documents the -2007 fix: Binance callbackRate
// must be in [0.1,5] with a 0.1 step. This mirrors the clamp+round applied in
// SetTrailingStopLoss so the invariant is locked even though the network call
// itself isn't exercised here.
func TestCallbackRateNormalization(t *testing.T) {
	norm := func(cb float64) string {
		if cb < 0.1 {
			cb = 0.1
		}
		if cb > 5 {
			cb = 5
		}
		cb = math.Round(cb*10) / 10
		return fmt.Sprintf("%.1f", cb)
	}
	cases := []struct {
		in   float64
		want string
	}{
		{1.0089, "1.0"}, // ETH full-path value that hit -2007
		{3.4147, "3.4"}, // WLD value that hit -2007
		{1.2000, "1.2"}, // previously-successful value, unchanged
		{0.0500, "0.1"}, // below min -> floor
		{7.5000, "5.0"}, // above max -> ceil
		{0.1499, "0.1"}, // rounds down to step
		{0.1500, "0.2"}, // rounds up to step
	}
	for _, c := range cases {
		if got := norm(c.in); got != c.want {
			t.Errorf("norm(%.4f)=%s, want %s", c.in, got, c.want)
		}
	}
}

// TestGetOpenOrders_NativeTrailingActivated verifies the Binance-only fix that
// reports a live native TRAILING_STOP_MARKET as ActivationStatus="activated".
// The shared drawdown reconciler (auto_trader_risk.go) only treats a trailing
// order as a "phantom" (and force-converts it to a MANAGED full-close) when
// ActivationStatus != "activated". That field is populated by OKX; on Binance it
// was always empty, so every in-profit position looked phantom and got auto-closed
// then re-armed every cycle. Binance activates native trailing reliably once price
// passes the activation price, so an order still present in the open list is armed.
func TestGetOpenOrders_NativeTrailingActivated(t *testing.T) {
	suite := NewBinanceFuturesTestSuite(t)
	defer suite.Cleanup()

	trader, ok := suite.Trader.(*FuturesTrader)
	if !ok {
		t.Fatalf("expected *FuturesTrader")
	}

	orders, err := trader.GetOpenOrders("BTCUSDT")
	if err != nil {
		t.Fatalf("GetOpenOrders error: %v", err)
	}

	var found bool
	for _, o := range orders {
		if !strings.Contains(strings.ToUpper(o.Type), "TRAILING") {
			continue
		}
		found = true
		if o.ActivationStatus != "activated" {
			t.Errorf("native trailing ActivationStatus=%q, want \"activated\" (prevents phantom force-close)", o.ActivationStatus)
		}
		if o.ActivationPrice != 50000.00 {
			t.Errorf("native trailing ActivationPrice=%.4f, want 50000.0000 (falls back to triggerPrice)", o.ActivationPrice)
		}
	}
	if !found {
		t.Fatalf("expected a TRAILING order in GetOpenOrders result, got none")
	}
}
