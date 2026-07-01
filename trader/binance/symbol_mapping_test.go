package binance

import "testing"

func withUSDCBases(bases map[string]bool, fn func()) {
	usdcBasesMu.Lock()
	prev := usdcPerpBases
	usdcPerpBases = bases
	usdcBasesMu.Unlock()
	defer func() {
		usdcBasesMu.Lock()
		usdcPerpBases = prev
		usdcBasesMu.Unlock()
	}()
	fn()
}

func TestToInternalSymbol(t *testing.T) {
	cases := map[string]string{
		"BTCUSDC": "BTCUSDT",
		"ETHUSDC": "ETHUSDT",
		"BTCUSDT": "BTCUSDT",
		"btcusdc": "BTCUSDT",
		"SOLUSDT": "SOLUSDT",
	}
	for in, want := range cases {
		if got := toInternalSymbol(in); got != want {
			t.Errorf("toInternalSymbol(%q)=%q want %q", in, got, want)
		}
	}
}

func TestToExecSymbol_DisabledIsNoop(t *testing.T) {
	tr := &FuturesTrader{preferUSDC: false}
	withUSDCBases(map[string]bool{"BTC": true}, func() {
		if got := tr.toExecSymbol("BTCUSDT"); got != "BTCUSDT" {
			t.Fatalf("disabled preferUSDC must be no-op, got %q", got)
		}
	})
}

func TestToExecSymbol_EnabledMapsOnlyWhitelisted(t *testing.T) {
	tr := &FuturesTrader{preferUSDC: true}
	withUSDCBases(map[string]bool{"BTC": true, "ETH": true}, func() {
		if got := tr.toExecSymbol("BTCUSDT"); got != "BTCUSDC" {
			t.Errorf("BTC has USDC perp -> want BTCUSDC, got %q", got)
		}
		// SPCX has no USDC perp -> stays USDT
		if got := tr.toExecSymbol("SPCXUSDT"); got != "SPCXUSDT" {
			t.Errorf("no USDC perp -> want SPCXUSDT, got %q", got)
		}
		// non-USDT symbol untouched
		if got := tr.toExecSymbol("xyz:MU"); got != "xyz:MU" {
			t.Errorf("non-USDT -> unchanged, got %q", got)
		}
	})
}

func TestToExecSymbol_Idempotent(t *testing.T) {
	tr := &FuturesTrader{preferUSDC: true}
	withUSDCBases(map[string]bool{"BTC": true}, func() {
		once := tr.toExecSymbol("BTCUSDT") // BTCUSDC
		twice := tr.toExecSymbol(once)     // must stay BTCUSDC (not BTCUSDCUSDC)
		if once != "BTCUSDC" || twice != "BTCUSDC" {
			t.Fatalf("idempotency broken: once=%q twice=%q", once, twice)
		}
	})
}

func TestRoundTripIdentity(t *testing.T) {
	tr := &FuturesTrader{preferUSDC: true}
	withUSDCBases(map[string]bool{"BTC": true}, func() {
		exec := tr.toExecSymbol("BTCUSDT") // BTCUSDC
		back := toInternalSymbol(exec)     // BTCUSDT
		if back != "BTCUSDT" {
			t.Fatalf("round-trip: BTCUSDT -> %q -> %q", exec, back)
		}
	})
}
