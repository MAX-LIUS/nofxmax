package market

import (
	"strings"
	"sync"
	"time"
)

// AssetClass classifies a tradable symbol by the nature of its underlying, which
// drives session handling (stocks/commodities have market hours & open-gap risk)
// and stop-loss confirmation-timeframe policy (crypto tolerates finer TFs; stocks
// whipsaw on fine TFs). Values are exchange-agnostic; the per-exchange metadata
// (OKX instCategory, Binance underlyingType) is mapped onto these.
type AssetClass string

const (
	AssetCrypto    AssetClass = "crypto"
	AssetStock     AssetClass = "stock"
	AssetCommodity AssetClass = "commodity"
	AssetUnknown   AssetClass = "unknown"
)

// assetClassCache holds base-asset → class, refreshed from the exchange instruments
// endpoint. Keyed by base asset (e.g. "SKHYNIX", "BTC") so "SKHYNIXUSDT",
// "SKHYNIX-USDT-SWAP" all resolve identically.
var (
	assetClassCache   = map[string]AssetClass{}
	assetClassExch    = map[string]string{} // base → exchange that classified it
	assetClassMu      sync.RWMutex
	assetClassLoadedAt time.Time
)

// baseAsset strips quote/suffix/prefix so any symbol form maps to its base ticker.
func baseAsset(symbol string) string {
	s := strings.ToUpper(strings.TrimSpace(symbol))
	if i := strings.Index(s, "XYZ:"); i == 0 {
		s = s[4:]
	}
	// OKX form BASE-QUOTE-SWAP
	if strings.Contains(s, "-") {
		s = strings.SplitN(s, "-", 2)[0]
		return s
	}
	// Binance form BASEUSDT / BASEUSDC / BASEUSD
	for _, q := range []string{"USDT", "USDC", "USD"} {
		if strings.HasSuffix(s, q) && len(s) > len(q) {
			return strings.TrimSuffix(s, q)
		}
	}
	return s
}

// ClassifyAsset returns the cached class for a symbol. Unknown (not yet fetched
// or fetch failed) defaults to crypto, the safe 24/7 default: it applies no
// session restriction, so an unclassified symbol behaves exactly as today.
func ClassifyAsset(symbol string) AssetClass {
	b := baseAsset(symbol)
	assetClassMu.RLock()
	c, ok := assetClassCache[b]
	assetClassMu.RUnlock()
	if ok {
		return c
	}
	return AssetCrypto
}

// IsScheduledAsset reports whether the symbol trades on a scheduled market
// (stock or commodity) and therefore needs session/open-gap handling.
func IsScheduledAsset(symbol string) bool {
	c := ClassifyAsset(symbol)
	return c == AssetStock || c == AssetCommodity
}

// setAssetClasses merges a freshly-fetched batch into the cache.
func setAssetClasses(exchange string, m map[string]AssetClass) {
	assetClassMu.Lock()
	defer assetClassMu.Unlock()
	for b, c := range m {
		assetClassCache[b] = c
		assetClassExch[b] = exchange
	}
	assetClassLoadedAt = time.Now()
}

// AssetClassCacheStats returns count + age for observability/logging.
func AssetClassCacheStats() (int, time.Duration) {
	assetClassMu.RLock()
	defer assetClassMu.RUnlock()
	return len(assetClassCache), time.Since(assetClassLoadedAt)
}
