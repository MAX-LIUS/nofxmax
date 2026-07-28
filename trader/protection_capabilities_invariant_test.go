package trader

import "testing"

// TestBinanceCannotFuzzyMatchTrailingTiers pins the venue fact that drove the
// 2026-07-27 HYPEUSDT 174-order leak: Binance's algo-order list carries no
// callbackRate, so an armed native-trailing record with an empty ExchangeOrderID
// can never be re-found by findEquivalentPartialTrailingOrder — the tier reads as
// "missing" on every poll and is re-armed without bound.
//
// This used to be knowable only from comments at the matcher plus value-sniffing
// (`order.CallbackRate <= 0`), which cannot distinguish "venue does not report it"
// from "this order really has 0". It is now data.
func TestBinanceCannotFuzzyMatchTrailingTiers(t *testing.T) {
	at := &AutoTrader{exchange: "binance"}
	if at.canFuzzyMatchTrailingTiers() {
		t.Fatal("binance must NOT be considered fuzzy-matchable: the algo-order list omits callbackRate, so an ID-less armed tier is unrecoverable and re-arms forever")
	}
	if at.GetProtectionCapabilities().ReportsTrailingCallbackRate {
		t.Fatal("binance ReportsTrailingCallbackRate must be false")
	}
}

// TestCallbackReportingVenuesCanFuzzyMatch guards the other direction: OKX and
// Bitget DO echo callbackRate back (okx trader_orders.go:1514,
// bitget trader_orders.go:753), so their existing empty-ExchangeOrderID arm paths
// stay valid and must not be downgraded to the local monitor by the new invariant.
func TestCallbackReportingVenuesCanFuzzyMatch(t *testing.T) {
	for _, ex := range []string{"okx", "bitget"} {
		at := &AutoTrader{exchange: ex}
		if !at.canFuzzyMatchTrailingTiers() {
			t.Fatalf("%s reports trailing callback rate, so it must remain fuzzy-matchable (its arm paths legitimately persist an empty order ID)", ex)
		}
	}
}

// TestVenuesWithoutNativeTrailingAreNotFuzzyMatchable is the conservative default:
// any venue we have not verified must be treated as unable to fuzzy-match, so the
// ID-less invariant fails safe (local monitor) rather than silently leaking orders.
func TestVenuesWithoutNativeTrailingAreNotFuzzyMatchable(t *testing.T) {
	for _, ex := range []string{"gate", "kucoin", "bybit", "aster", "lighter", "hyperliquid", "", "some-future-venue"} {
		at := &AutoTrader{exchange: ex}
		if at.canFuzzyMatchTrailingTiers() {
			t.Fatalf("unverified venue %q must default to NOT fuzzy-matchable (fail safe)", ex)
		}
	}
}

// TestTrailingRecordIsUnrecoverable covers the named invariant that both arm-branch
// guards route through: an armed record must be re-findable either by exchange order
// ID or by the fuzzy matcher. Neither ⇒ unbounded re-arm, so it must be reported as
// unrecoverable and the caller drops to the local managed monitor.
func TestTrailingRecordIsUnrecoverable(t *testing.T) {
	bn := &AutoTrader{exchange: "binance"} // cannot fuzzy-match
	okx := &AutoTrader{exchange: "okx"}    // can fuzzy-match

	// A real order ID is always recoverable, on any venue.
	if bn.trailingRecordIsUnrecoverable("123456789") {
		t.Fatal("a non-empty order ID must always be recoverable")
	}
	if okx.trailingRecordIsUnrecoverable("123456789") {
		t.Fatal("a non-empty order ID must always be recoverable")
	}

	// Empty ID on a venue that cannot fuzzy-match: unrecoverable — this is the leak.
	if !bn.trailingRecordIsUnrecoverable("") {
		t.Fatal("empty order ID on binance must be unrecoverable (no ID, no callbackRate ⇒ re-armed forever)")
	}

	// Whitespace is exactly as unusable as empty.
	for _, blank := range []string{" ", "\t", "\n", "  \t "} {
		if !bn.trailingRecordIsUnrecoverable(blank) {
			t.Fatalf("blank order ID %q on binance must be treated as unrecoverable, not as a usable handle", blank)
		}
	}

	// Empty ID where the venue DOES echo callbackRate: the fuzzy matcher can still
	// find it, so OKX/Bitget's existing ID-less arm paths must NOT be downgraded.
	if okx.trailingRecordIsUnrecoverable("") {
		t.Fatal("empty order ID on okx must stay recoverable via fuzzy matching — downgrading it would break a working path")
	}
	if (&AutoTrader{exchange: "bitget"}).trailingRecordIsUnrecoverable("") {
		t.Fatal("empty order ID on bitget must stay recoverable via fuzzy matching")
	}
}

// TestProtectionCapabilitiesFieldsAreKeyedNotPositional is a semantic regression
// guard for the struct-literal hazard. The per-venue literals were unkeyed
// positional lists of 10 bools; Go only rejects a WRONG COUNT, so inserting a field
// in the MIDDLE of the struct silently shifted every later flag on every exchange
// and still compiled — protection misbehaviour with no compile error.
//
// Rather than inspect source text, this asserts the flag combinations that a shift
// would scramble: each venue's meaning must still line up with its documented
// capability profile.
func TestProtectionCapabilitiesFieldsAreKeyedNotPositional(t *testing.T) {
	// binance: full native support, algo orders, no OCO, no callback reporting.
	bn := (&AutoTrader{exchange: "binance"}).GetProtectionCapabilities()
	if !bn.NativeStopLoss || !bn.NativeTakeProfit || !bn.NativePartialClose || !bn.NativeReduceOnly {
		t.Fatal("binance native SL/TP/partial/reduceOnly must all be true")
	}
	if !bn.CanAmendProtection || !bn.CanDistinguishStopTP || !bn.SupportsAlgoOrders {
		t.Fatal("binance amend/distinguish/algo must all be true")
	}
	if bn.SupportsOCO {
		t.Fatal("binance SupportsOCO must be false — a true here means the literal shifted")
	}
	if !bn.SupportsNativeFullTrailing || !bn.SupportsNativePartialTrailing {
		t.Fatal("binance native full/partial trailing must both be true")
	}

	// bitget: native trailing yes, but NOT an algo-order venue and cannot amend.
	// A positional shift most visibly corrupts exactly these.
	bg := (&AutoTrader{exchange: "bitget"}).GetProtectionCapabilities()
	if bg.SupportsAlgoOrders || bg.SupportsOCO || bg.CanAmendProtection {
		t.Fatal("bitget algo/OCO/amend must all be false — a true here means the literal shifted")
	}
	if !bg.SupportsNativeFullTrailing || !bg.SupportsNativePartialTrailing {
		t.Fatal("bitget native trailing flags must be true")
	}

	// hyperliquid: the one venue that canNOT distinguish stop vs TP cancellations.
	hl := (&AutoTrader{exchange: "hyperliquid"}).GetProtectionCapabilities()
	if hl.CanDistinguishStopTP {
		t.Fatal("hyperliquid CanDistinguishStopTP must stay false")
	}
	if !hl.NativeStopLoss || !hl.NativeTakeProfit {
		t.Fatal("hyperliquid native SL/TP must be true")
	}
	if hl.SupportsNativeFullTrailing || hl.SupportsNativePartialTrailing {
		t.Fatal("hyperliquid must not claim native trailing")
	}

	// lighter: no native partial close / reduce-only. These sit mid-struct, so a
	// shift would flip them to true and wrongly enable partial protection.
	lt := (&AutoTrader{exchange: "lighter"}).GetProtectionCapabilities()
	if lt.NativePartialClose || lt.NativeReduceOnly {
		t.Fatal("lighter has no native partial close / reduce-only — a true here means the literal shifted")
	}
	if !lt.NativeStopLoss || !lt.NativeTakeProfit || !lt.CanDistinguishStopTP {
		t.Fatal("lighter native SL/TP + distinguish must be true")
	}

	// default/unknown venue must claim nothing at all.
	def := (&AutoTrader{exchange: "totally-unknown"}).GetProtectionCapabilities()
	if def != (ProtectionCapabilities{}) {
		t.Fatalf("unknown venue must claim no capabilities, got %+v", def)
	}
}
