package binance

import (
	"context"
	"strings"
	"sync"
	"time"

	"nofx/logger"
)

// Symbol identity model
// ─────────────────────
// The rest of the system speaks ONLY in USDT symbols (e.g. "BTCUSDT"); protection
// state, posKey (symbol+"_"+side), frozen ATR, decisions and the UI are all keyed
// by the USDT symbol. To capture Binance's zero maker-fee USDC promo without
// leaking a second identity into that machinery, USDC is confined entirely here:
//
//   - toExecSymbol(internal): USDT -> USDC only for bases that have a tradeable
//     USDC perpetual, and only when preferUSDC is enabled. Applied at the moment
//     we call the Binance API (outbound).
//   - toInternalSymbol(exec): USDC -> USDT, applied to every symbol Binance hands
//     back (GetPositions / GetClosedPnL / GetOpenOrders). This reverse mapping is
//     mandatory: skipping it would make the caller look up protection state under
//     "BTCUSDC_long" while it was stored as "BTCUSDT_long" — i.e. an unprotected
//     ("naked") position. Reverse mapping keeps internal identity stable.
//
// Bases without a USDC perp fall through unchanged (BTCUSDT stays BTCUSDT).

const usdcRefreshInterval = 6 * time.Hour

// usdcPerpBases caches the set of base assets (e.g. "BTC","ETH") that have a
// TRADING USDC perpetual on Binance futures. Shared process-wide; refreshed lazily.
var (
	usdcBasesMu   sync.RWMutex
	usdcPerpBases map[string]bool
	usdcFetchedAt time.Time
)

// refreshUSDCPerpBases pulls exchangeInfo (through the Binance-scoped proxy) and
// records which bases have a tradeable USDC perpetual. Best-effort: on error the
// previous cache is kept (fail-safe = fewer USDC conversions, never a wrong one).
func (t *FuturesTrader) refreshUSDCPerpBases(force bool) {
	usdcBasesMu.RLock()
	fresh := usdcPerpBases != nil && time.Since(usdcFetchedAt) < usdcRefreshInterval
	usdcBasesMu.RUnlock()
	if fresh && !force {
		return
	}

	info, err := t.client.NewExchangeInfoService().Do(context.Background())
	if err != nil {
		logger.Warnf("⚠️ USDC map: exchangeInfo fetch failed (%v); keeping previous cache", err)
		return
	}
	bases := make(map[string]bool)
	for _, s := range info.Symbols {
		if s.QuoteAsset != "USDC" {
			continue
		}
		if s.ContractType != "PERPETUAL" || s.Status != "TRADING" {
			continue
		}
		bases[strings.ToUpper(s.BaseAsset)] = true
	}
	if len(bases) == 0 {
		logger.Warnf("⚠️ USDC map: exchangeInfo returned 0 USDC perps; keeping previous cache")
		return
	}
	usdcBasesMu.Lock()
	usdcPerpBases = bases
	usdcFetchedAt = time.Now()
	usdcBasesMu.Unlock()
	logger.Infof("✓ USDC map: %d USDC-perp bases cached for maker-fee routing", len(bases))
}

// baseHasUSDCPerp reports whether base (e.g. "BTC") has a tradeable USDC perp.
func baseHasUSDCPerp(base string) bool {
	usdcBasesMu.RLock()
	defer usdcBasesMu.RUnlock()
	if usdcPerpBases == nil {
		return false
	}
	return usdcPerpBases[strings.ToUpper(base)]
}

// usdtBase extracts the base asset from a USDT symbol ("BTCUSDT" -> "BTC").
// Returns ("",false) when sym is not a plain USDT symbol.
func usdtBase(sym string) (string, bool) {
	u := strings.ToUpper(sym)
	if !strings.HasSuffix(u, "USDT") {
		return "", false
	}
	return strings.TrimSuffix(u, "USDT"), true
}

// toExecSymbol maps an internal USDT symbol to the symbol we should trade on
// Binance. When preferUSDC is on and the base has a USDC perp, returns "<base>USDC";
// otherwise returns the input unchanged.
func (t *FuturesTrader) toExecSymbol(internal string) string {
	if !t.preferUSDC {
		return internal
	}
	base, ok := usdtBase(internal)
	if !ok {
		return internal
	}
	if baseHasUSDCPerp(base) {
		return base + "USDC"
	}
	return internal
}

// toInternalSymbol maps any symbol Binance returns back to the internal USDT
// symbol. "<base>USDC" -> "<base>USDT"; everything else is returned unchanged.
// This runs regardless of preferUSDC so that positions opened while the flag was
// on are still recognised if the flag is later turned off.
func toInternalSymbol(exec string) string {
	u := strings.ToUpper(exec)
	if strings.HasSuffix(u, "USDC") {
		return strings.TrimSuffix(u, "USDC") + "USDT"
	}
	return u
}
