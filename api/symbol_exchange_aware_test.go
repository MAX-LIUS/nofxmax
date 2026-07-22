package api

import "testing"

func TestExchangeNativeSymbol(t *testing.T) {
	cases := map[string]string{
		"xyz:PLTR": "PLTRUSDT", // internal DEX form -> CEX native
		"PLTRUSDT": "PLTRUSDT", // already native
		"pltr":     "PLTRUSDT", // bare base, lower case
		"MU":       "MUUSDT",   // bare stock base
		"xyz:MU":   "MUUSDT",
		"BTCUSDT":  "BTCUSDT", // plain crypto unchanged
		"ETHUSDC":  "ETHUSDC", // USDC pair preserved
		"BTC":      "BTCUSDT",
		"":         "",
	}
	for in, want := range cases {
		if got := exchangeNativeSymbol(in); got != want {
			t.Errorf("exchangeNativeSymbol(%q)=%q want %q", in, got, want)
		}
	}
}

func TestIsDexExchange(t *testing.T) {
	for _, ex := range []string{"hyperliquid", "hyperliquid-xyz", "xyz", "HYPERLIQUID"} {
		if !isDexExchange(ex) {
			t.Errorf("isDexExchange(%q) = false, want true", ex)
		}
	}
	for _, ex := range []string{"binance", "okx", "bybit", "bitget", ""} {
		if isDexExchange(ex) {
			t.Errorf("isDexExchange(%q) = true, want false", ex)
		}
	}
}

func TestIsInvalidSymbolErr(t *testing.T) {
	if !isInvalidSymbolErr(errStr("<APIError> code=-1121, msg=Invalid symbol.")) {
		t.Error("expected -1121 to be treated as invalid symbol")
	}
	if !isInvalidSymbolErr(errStr("Invalid symbol")) {
		t.Error("expected 'Invalid symbol' to match")
	}
	if isInvalidSymbolErr(errStr("connection reset by peer")) {
		t.Error("network error must NOT be treated as invalid symbol (should surface as 500)")
	}
	if isInvalidSymbolErr(nil) {
		t.Error("nil err must be false")
	}
}

type errStr string

func (e errStr) Error() string { return string(e) }
