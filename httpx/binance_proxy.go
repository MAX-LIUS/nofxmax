// Package httpx provides HTTP client helpers for the trading system.
//
// The Binance-scoped proxy solves a specific problem: when the host runs in a
// region where Binance returns HTTP 451 (geo-block), Binance REST traffic must
// egress from an allowed region while every other exchange / AI endpoint keeps
// connecting directly.
//
// Design principle (migration-friendly):
//   - Behaviour is driven by a single env var BINANCE_PROXY_URL.
//   - Empty  -> hooks are a no-op, everything connects directly (identical to
//     the pre-proxy behaviour). Safe default for any host in an allowed region.
//   - Set    -> ONLY *.binance.com requests are routed through the proxy. All
//     other hosts (OKX, Anthropic, on-chain RPC, ...) stay direct, so the proxy
//     is never a single point of failure for non-Binance flows.
//
// Use an HTTP CONNECT proxy URL (http://host:port). CONNECT forwards the target
// hostname to the proxy, which lets sing-box/xray match its domain routing rule
// (binance.com -> JP outbound). A socks5 (non-h) URL would resolve DNS locally
// and hand the proxy a bare IP, defeating domain-based routing. socks5h works
// too because DNS is delegated to the proxy.
package httpx

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const binanceProxyEnv = "BINANCE_PROXY_URL"

var (
	proxyOnce sync.Once
	proxyURL  *url.URL
)

// loadProxyURL parses BINANCE_PROXY_URL once. Invalid values are ignored (treated
// as "not set") so a typo can never take the trader offline silently — it simply
// degrades to direct and the 451, if any, is visible in logs.
func loadProxyURL() *url.URL {
	proxyOnce.Do(func() {
		raw := strings.TrimSpace(os.Getenv(binanceProxyEnv))
		if raw == "" {
			return
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return
		}
		proxyURL = u
	})
	return proxyURL
}

// BinanceProxyEnabled reports whether a usable proxy URL is configured.
func BinanceProxyEnabled() bool { return loadProxyURL() != nil }

// BinanceProxyString returns the configured proxy URL string (for logging).
func BinanceProxyString() string {
	if u := loadProxyURL(); u != nil {
		return u.String()
	}
	return ""
}

// isBinanceHost matches binance.com and any subdomain (fapi/api/dapi/...).
func isBinanceHost(host string) bool {
	host = strings.ToLower(host)
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return host == "binance.com" || strings.HasSuffix(host, ".binance.com")
}

// binanceProxyFunc is an http.Transport.Proxy function: it returns the configured
// proxy only for Binance hosts, and falls back to the standard environment proxy
// (HTTP_PROXY/HTTPS_PROXY/NO_PROXY) for everything else.
func binanceProxyFunc(req *http.Request) (*url.URL, error) {
	if p := loadProxyURL(); p != nil && req != nil && req.URL != nil && isBinanceHost(req.URL.Host) {
		return p, nil
	}
	return http.ProxyFromEnvironment(req)
}

// newBinanceTransport builds a transport whose Proxy is scoped to Binance hosts.
func newBinanceTransport() *http.Transport {
	tr := &http.Transport{
		Proxy:                 binanceProxyFunc,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return tr
}

// WrapClient returns an *http.Client whose transport routes Binance hosts through
// the configured proxy. When no proxy is configured it still returns a working
// client (Binance path falls back to env proxy / direct), so callers can use it
// unconditionally. The provided client's Timeout is preserved; nil yields a new
// client with a sane default timeout.
func WrapClient(c *http.Client) *http.Client {
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	c.Transport = newBinanceTransport()
	return c
}

// NewBinanceClient returns a fresh client with the Binance-scoped proxy transport
// and the given timeout (0 -> 30s default).
func NewBinanceClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Timeout: timeout, Transport: newBinanceTransport()}
}
