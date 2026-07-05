package httpx

import (
	"net/http"
	"net/url"
	"testing"
)

func TestIsBinanceHost(t *testing.T) {
	cases := map[string]bool{
		"fapi.binance.com":      true,
		"fapi.binance.com:443":  true,
		"api.binance.com":       true,
		"binance.com":           true,
		"BINANCE.COM":           true,
		"www.okx.com":           false,
		"api.anthropic.com":     false,
		"notbinance.com":        false,
		"binance.com.evil.com":  false,
		"fapi.binance.comx.com": false,
	}
	for host, want := range cases {
		if got := isBinanceHost(host); got != want {
			t.Errorf("isBinanceHost(%q)=%v want %v", host, got, want)
		}
	}
}

// binanceProxyFunc must return the configured proxy ONLY for binance hosts.
func TestBinanceProxyFuncScoping(t *testing.T) {
	// Force a known proxy URL regardless of env.
	proxyOnce.Do(func() {})
	saved := proxyURL
	defer func() { proxyURL = saved }()
	proxyURL, _ = url.Parse("http://s-ui:10808")

	mk := func(raw string) *http.Request {
		u, _ := url.Parse(raw)
		return &http.Request{URL: u}
	}

	if got, _ := binanceProxyFunc(mk("https://fapi.binance.com/fapi/v1/ping")); got == nil || got.Host != "s-ui:10808" {
		t.Fatalf("binance host should use proxy, got %v", got)
	}
	// Non-binance falls back to env proxy (nil when no env set in test).
	if got, _ := binanceProxyFunc(mk("https://www.okx.com/api/v5/public/time")); got != nil && got.Host == "s-ui:10808" {
		t.Fatalf("okx host must NOT use binance proxy, got %v", got)
	}
}

func TestWrapClientAlwaysUsable(t *testing.T) {
	c := WrapClient(nil)
	if c == nil || c.Transport == nil {
		t.Fatal("WrapClient(nil) must return usable client with transport")
	}
	if _, ok := c.Transport.(*http.Transport); !ok {
		t.Fatal("transport must be *http.Transport")
	}
}
