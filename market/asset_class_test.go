package market

import "testing"

func TestBaseAsset(t *testing.T) {
	cases := map[string]string{
		"BTCUSDT":            "BTC",
		"ETHUSDC":            "ETH",
		"BTC-USDT-SWAP":      "BTC",
		"SKHYNIX-USDT-SWAP":  "SKHYNIX",
		"SKHYNIXUSDT":        "SKHYNIX",
		"xyz:AAPLUSDT":       "AAPL",
		"XYZ:SPCX-USDT-SWAP": "SPCX",
		"btcusdt":            "BTC",
		"USDT":               "USDT", // pure quote, no strip (len guard)
	}
	for in, want := range cases {
		if got := baseAsset(in); got != want {
			t.Errorf("baseAsset(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClassifyAssetDefaultsCrypto(t *testing.T) {
	// unclassified symbol → crypto (safe 24/7 default)
	if c := ClassifyAsset("NEVERSEENUSDT"); c != AssetCrypto {
		t.Errorf("unclassified defaulted to %q, want crypto", c)
	}
	if IsScheduledAsset("NEVERSEENUSDT") {
		t.Error("unclassified should not be scheduled")
	}
}

func TestSetAndClassify(t *testing.T) {
	setAssetClasses("okx", map[string]AssetClass{
		"SKHYNIX": AssetStock,
		"GOLD":    AssetCommodity,
		"BTC":     AssetCrypto,
	})
	if c := ClassifyAsset("SKHYNIX-USDT-SWAP"); c != AssetStock {
		t.Errorf("SKHYNIX = %q, want stock", c)
	}
	if c := ClassifyAsset("GOLDUSDT"); c != AssetCommodity {
		t.Errorf("GOLD = %q, want commodity", c)
	}
	if !IsScheduledAsset("SKHYNIXUSDT") {
		t.Error("stock should be scheduled")
	}
	if IsScheduledAsset("BTCUSDT") {
		t.Error("crypto should not be scheduled")
	}
	if n, _ := AssetClassCacheStats(); n < 3 {
		t.Errorf("cache count = %d, want >=3", n)
	}
}
