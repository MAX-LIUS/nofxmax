// Package proxyhook wires the Binance-scoped proxy (see package httpx) into the
// two extension points the codebase already exposes:
//
//   - SET_HTTP_CLIENT    : used by market.NewAPIClient() for all Binance REST
//     market data (klines, exchangeInfo, open interest, funding, hot coins).
//   - NEW_BINANCE_TRADER : used by binance.NewFuturesTrader() for signed trading
//     calls (orders, account, positions) via the go-binance futures.Client.
//
// Registration is idempotent and safe when no proxy is configured: the hooks are
// still registered but the transport falls back to env-proxy/direct for Binance,
// so nothing changes versus the pre-proxy behaviour.
package proxyhook

import (
	"net/http"
	"nofx/hook"
	"nofx/httpx"
	"nofx/logger"

	"github.com/adshao/go-binance/v2/futures"
)

// Register installs the Binance proxy hooks. Call once during startup, after
// config.Init() and before any trader/market client is constructed.
func Register() {
	if httpx.BinanceProxyEnabled() {
		logger.Infof("🌐 Binance proxy ENABLED: %s (only *.binance.com is routed; other hosts stay direct)", httpx.BinanceProxyString())
	} else {
		logger.Info("🌐 Binance proxy disabled (BINANCE_PROXY_URL unset) — Binance connects directly")
	}

	// Market data client: wrap whatever client market.NewAPIClient() built so its
	// transport applies the Binance-scoped proxy.
	hook.RegisterHook(hook.SET_HTTP_CLIENT, func(args ...any) any {
		var c *http.Client
		if len(args) > 0 {
			c, _ = args[0].(*http.Client)
		}
		return &hook.SetHttpClientResult{Client: httpx.WrapClient(c)}
	})

	// Trading client: give the go-binance futures.Client a proxied HTTP client.
	hook.RegisterHook(hook.NEW_BINANCE_TRADER, func(args ...any) any {
		var client *futures.Client
		if len(args) >= 2 {
			client, _ = args[1].(*futures.Client)
		}
		if client == nil {
			return &hook.NewBinanceTraderResult{Client: client}
		}
		client.HTTPClient = httpx.WrapClient(client.HTTPClient)
		return &hook.NewBinanceTraderResult{Client: client}
	})
}
