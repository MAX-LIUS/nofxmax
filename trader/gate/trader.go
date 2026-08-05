package gate

import (
	"context"
	"fmt"
	"nofx/trader/types"
	"strings"
	"sync"
	"time"

	"github.com/gateio/gateapi-go/v6"
)

// GateTrader implements types.Trader interface for Gate.io Futures
type GateTrader struct {
	apiKey    string
	secretKey string
	client    *gateapi.APIClient
	ctx       context.Context

	// Cache fields
	cachedBalance       map[string]interface{}
	balanceCacheTime    time.Time
	balanceCacheMutex   sync.RWMutex
	cachedPositions     []map[string]interface{}
	positionsCacheTime  time.Time
	positionsCacheMutex sync.RWMutex
	contractsCache      map[string]*gateapi.Contract
	contractsCacheMutex sync.RWMutex
	cacheDuration       time.Duration

	// crossMargin records the margin mode requested via SetMarginMode. Gate has no
	// standalone margin-mode endpoint: cross margin IS `leverage=0` plus
	// `cross_leverage_limit=<n>` on UpdatePositionLeverage. Since the generic layer
	// calls SetMarginMode *before* OpenLong/OpenShort (auto_trader_orders.go:274,480)
	// and those then call SetLeverage, the mode has to be remembered here so
	// SetLeverage can encode it. Without this the leverage call always wrote a
	// positive value, i.e. isolated margin, silently ignoring is_cross_margin.
	crossMargin      bool
	crossMarginMutex sync.RWMutex
}

// NewGateTrader creates a new Gate trader instance
func NewGateTrader(apiKey, secretKey string) *GateTrader {
	config := gateapi.NewConfiguration()
	config.AddDefaultHeader("X-Gate-Channel-Id", "nofx")
	client := gateapi.NewAPIClient(config)

	ctx := context.WithValue(context.Background(),
		gateapi.ContextGateAPIV4,
		gateapi.GateAPIV4{
			Key:    apiKey,
			Secret: secretKey,
		},
	)

	return &GateTrader{
		apiKey:         apiKey,
		secretKey:      secretKey,
		client:         client,
		ctx:            ctx,
		contractsCache: make(map[string]*gateapi.Contract),
		cacheDuration:  15 * time.Second,
	}
}

// convertSymbol converts symbol format (e.g., BTCUSDT -> BTC_USDT)
func (t *GateTrader) convertSymbol(symbol string) string {
	// If already in correct format
	if strings.Contains(symbol, "_") {
		return symbol
	}
	// Convert BTCUSDT to BTC_USDT
	if strings.HasSuffix(symbol, "USDT") {
		base := strings.TrimSuffix(symbol, "USDT")
		return base + "_USDT"
	}
	return symbol
}

// revertSymbol converts symbol back to standard format (e.g., BTC_USDT -> BTCUSDT)
func (t *GateTrader) revertSymbol(symbol string) string {
	return strings.ReplaceAll(symbol, "_", "")
}

// getContract fetches contract info with caching
func (t *GateTrader) getContract(symbol string) (*gateapi.Contract, error) {
	symbol = t.convertSymbol(symbol)

	// Check cache
	t.contractsCacheMutex.RLock()
	if contract, ok := t.contractsCache[symbol]; ok {
		t.contractsCacheMutex.RUnlock()
		return contract, nil
	}
	t.contractsCacheMutex.RUnlock()

	// Fetch from API
	contract, _, err := t.client.FuturesApi.GetFuturesContract(t.ctx, "usdt", symbol)
	if err != nil {
		return nil, fmt.Errorf("failed to get contract info: %w", err)
	}

	// Update cache
	t.contractsCacheMutex.Lock()
	t.contractsCache[symbol] = &contract
	t.contractsCacheMutex.Unlock()

	return &contract, nil
}

// clearCache clears all caches
func (t *GateTrader) clearCache() {
	t.balanceCacheMutex.Lock()
	t.cachedBalance = nil
	t.balanceCacheMutex.Unlock()

	t.positionsCacheMutex.Lock()
	t.cachedPositions = nil
	t.positionsCacheMutex.Unlock()
}

// Ensure GateTrader implements Trader interface
var _ types.Trader = (*GateTrader)(nil)

// Compile-time guard for the OPTIONAL tagged-protection contract.
//
// The generic protection layer reaches the tagged path only via anonymous interface
// assertions (protection_execution.go:885,921,1069; auto_trader_risk.go:3786). Those
// are runtime checks: if a signature here drifts by even the return type, the
// assertion just evaluates false and gate silently degrades to untagged protection
// with no protection intent recorded — no build error, no test failure, no log.
// Restating the exact shape here turns that class of silent regression into a
// compile error.
var _ interface {
	SetStopLoss(symbol string, positionSide string, quantity, stopPrice float64) error
	SetStopLossTagged(symbol string, positionSide string, quantity, stopPrice float64, reasonTag string) (string, error)
	SetTakeProfit(symbol string, positionSide string, quantity, takeProfitPrice float64) error
	SetTakeProfitTagged(symbol string, positionSide string, quantity, takeProfitPrice float64, reasonTag string) (string, error)
} = (*GateTrader)(nil)
