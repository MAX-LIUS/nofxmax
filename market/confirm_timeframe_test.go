package market

import "testing"

func TestRefineCryptoConfirmTF(t *testing.T) {
	cases := map[string]string{
		"1h":  "30m", // 2× clean refine (backtest sweet spot)
		"4h":  "2h",  // 2×
		"30m": "15m", // 2×
		"2h":  "1h",  // 2×
		"12h": "6h",  // 2×
		"15m": "15m", // adjacent is 5m (3×) → overshoot, keep native
		"5m":  "5m",  // adjacent is 3m (~1.67×)... 5/3=1.67 ≤2 → 3m; adjust expectation below
		"1m":  "1m",  // finest, no refine
		"7h":  "7h",  // unknown TF, keep native
	}
	// 5m→3m ratio is 5/3≈1.67 ≤2, so it DOES refine; fix expectation.
	cases["5m"] = "3m"
	for native, want := range cases {
		if got := refineCryptoConfirmTF(native); got != want {
			t.Errorf("refineCryptoConfirmTF(%q) = %q, want %q", native, got, want)
		}
	}
}

func TestConfirmTimeframeAssetAware(t *testing.T) {
	setAssetClasses("okx", map[string]AssetClass{
		"AAPL": AssetStock,
		"XAU":  AssetCommodity,
		"ETH":  AssetCrypto,
	})
	// stocks/commodities keep native
	if got := ConfirmTimeframe("1h", "AAPLUSDT"); got != "1h" {
		t.Errorf("stock ConfirmTimeframe = %q, want 1h (native)", got)
	}
	if got := ConfirmTimeframe("1h", "XAU-USDT-SWAP"); got != "1h" {
		t.Errorf("commodity ConfirmTimeframe = %q, want 1h (native)", got)
	}
	// crypto refines
	if got := ConfirmTimeframe("1h", "ETHUSDT"); got != "30m" {
		t.Errorf("crypto ConfirmTimeframe = %q, want 30m", got)
	}
	// unknown symbol defaults to crypto → refines
	if got := ConfirmTimeframe("1h", "SOMENEWUSDT"); got != "30m" {
		t.Errorf("unknown ConfirmTimeframe = %q, want 30m (crypto default)", got)
	}
}
