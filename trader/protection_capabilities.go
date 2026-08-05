package trader

import "strings"

// ProtectionCapabilities describes what a specific exchange adapter can reliably support
// for protection-order execution and post-open lifecycle management.
type ProtectionCapabilities struct {
	NativeStopLoss                bool
	NativeTakeProfit              bool
	NativePartialClose            bool
	NativeReduceOnly              bool
	CanAmendProtection            bool
	CanDistinguishStopTP          bool
	SupportsAlgoOrders            bool
	SupportsOCO                   bool
	SupportsNativeFullTrailing    bool
	SupportsNativePartialTrailing bool

	// ReportsTrailingCallbackRate says whether GetOpenOrders echoes back the
	// callback rate of a resting/active trailing order.
	//
	// This is NOT cosmetic. It decides whether the fuzzy tier matcher in
	// findEquivalentPartialTrailingOrder can work at all. That matcher discriminates
	// sibling tiers by qty + callback, and after a partial close a high tier's
	// clamped qty equals the full tier's qty — so callback is the ONLY remaining
	// discriminator. Where it is absent, `callbackOK` degenerates to "always true"
	// and the fuzzy path is structurally dead: a tier with no stored order ID is
	// judged missing on every poll and re-armed forever (2026-07-27 HYPEUSDT: 174
	// identical partial trailing orders).
	//
	// Verified per adapter by where OpenOrder.CallbackRate is populated:
	//   okx    trader/okx/trader_orders.go:1514      → yes (normalized to a ratio)
	//   bitget trader/bitget/trader_orders.go:753    → yes (percentage form)
	//   binance trader/binance/futures_orders.go:994 → NO field at all; the algo-order
	//           list response (SDK GetAlgoOrderResp) carries no callbackRate, so it
	//           reads back as 0 for every Binance trailing order.
	//
	// Until 2026-07-28 this fact lived only in code comments at the matcher and was
	// re-derived by sniffing `order.CallbackRate <= 0`. Comments expire and value
	// sniffing cannot tell "venue does not report it" from "this order really has 0",
	// which is why it is now data.
	ReportsTrailingCallbackRate bool
}

// GetProtectionCapabilities returns a conservative capability profile for the current exchange.
// These flags are intentionally biased toward safety and will be refined per adapter in later phases.
//
// Keyed fields are mandatory here. These used to be unkeyed positional literals
// (`{true, true, true, ...}` × 10 bools). Go only rejects a WRONG COUNT, so inserting
// a field in the MIDDLE of the struct silently shifted every later flag on every
// exchange and still compiled — a whole class of protection misbehaviour with no
// compile error and no test failure. Adding ReportsTrailingCallbackRate was exactly
// such an insertion risk, so the literals were converted first.
func (at *AutoTrader) GetProtectionCapabilities() ProtectionCapabilities {
	switch strings.ToLower(at.exchange) {
	case "binance":
		return ProtectionCapabilities{
			NativeStopLoss:                true,
			NativeTakeProfit:              true,
			NativePartialClose:            true,
			NativeReduceOnly:              true,
			CanAmendProtection:            true,
			CanDistinguishStopTP:          true,
			SupportsAlgoOrders:            true,
			SupportsOCO:                   false,
			SupportsNativeFullTrailing:    true,
			SupportsNativePartialTrailing: true,
			ReportsTrailingCallbackRate:   false, // algo list omits callbackRate
		}
	case "okx":
		return ProtectionCapabilities{
			NativeStopLoss:                true,
			NativeTakeProfit:              true,
			NativePartialClose:            true,
			NativeReduceOnly:              true,
			CanAmendProtection:            true,
			CanDistinguishStopTP:          true,
			SupportsAlgoOrders:            true,
			SupportsOCO:                   false,
			SupportsNativeFullTrailing:    true,
			SupportsNativePartialTrailing: true,
			ReportsTrailingCallbackRate:   true,
		}
	case "gate":
		// Verified against the adapter, not assumed:
		//   NativeStopLoss/TakeProfit  price-triggered orders via CreatePriceTriggeredOrder
		//   NativePartialClose         signed Initial.Size (negative closes a long)
		//   NativeReduceOnly           Initial.ReduceOnly is set on every protection order
		//   CanDistinguishStopTP       classifyTriggerOrder decides from (side, rule)
		//                              TOGETHER. This flag was already true while the
		//                              classifier read Trigger.Rule alone, which inverts
		//                              the answer for every SHORT — the flag was
		//                              untruthful until that was fixed.
		//
		// Everything else stays false, deliberately:
		//   CanAmendProtection            Gate has no amend endpoint for trigger orders;
		//                                 changes are cancel + re-place.
		//   SupportsAlgoOrders            no OKX-style algo namespace.
		//   SupportsNativeFullTrailing    Gate futures has no native trailing order type
		//   SupportsNativePartialTrailing in this SDK, so DD trailing MUST fall to the
		//                                 local managed monitor (the native switch in
		//                                 auto_trader_risk.go handles only
		//                                 binance/bitget/okx). Declaring either true
		//                                 would route DD to a venue path that does not
		//                                 exist and silently drop the protection.
		//   ReportsTrailingCallbackRate   moot while trailing is managed locally; must
		//                                 stay false so the fuzzy tier matcher is never
		//                                 fed a callback rate the venue never returns.
		return ProtectionCapabilities{
			NativeStopLoss:                true,
			NativeTakeProfit:              true,
			NativePartialClose:            true,
			NativeReduceOnly:              true,
			CanAmendProtection:            false,
			CanDistinguishStopTP:          true,
			SupportsAlgoOrders:            false,
			SupportsOCO:                   false,
			SupportsNativeFullTrailing:    false,
			SupportsNativePartialTrailing: false,
			ReportsTrailingCallbackRate:   false,
		}
	case "kucoin":
		return ProtectionCapabilities{
			NativeStopLoss:       true,
			NativeTakeProfit:     true,
			NativePartialClose:   true,
			NativeReduceOnly:     true,
			CanDistinguishStopTP: true,
		}
	case "bybit":
		return ProtectionCapabilities{
			NativeStopLoss:       true,
			NativeTakeProfit:     true,
			NativePartialClose:   true,
			NativeReduceOnly:     true,
			CanDistinguishStopTP: true,
		}
	case "bitget":
		return ProtectionCapabilities{
			NativeStopLoss:                true,
			NativeTakeProfit:              true,
			NativePartialClose:            true,
			NativeReduceOnly:              true,
			CanDistinguishStopTP:          true,
			SupportsNativeFullTrailing:    true,
			SupportsNativePartialTrailing: true,
			ReportsTrailingCallbackRate:   true,
		}
	case "aster":
		return ProtectionCapabilities{
			NativeStopLoss:       true,
			NativeTakeProfit:     true,
			NativePartialClose:   true,
			NativeReduceOnly:     true,
			CanDistinguishStopTP: true,
		}
	case "lighter":
		return ProtectionCapabilities{
			NativeStopLoss:   true,
			NativeTakeProfit: true,
			// No native partial close / reduce-only.
			CanDistinguishStopTP: true,
		}
	case "hyperliquid":
		return ProtectionCapabilities{
			NativeStopLoss:     true,
			NativeTakeProfit:   true,
			NativePartialClose: true,
			NativeReduceOnly:   true,
			// Hyperliquid currently cannot reliably distinguish stop-loss vs
			// take-profit cancellations.
			CanDistinguishStopTP: false,
		}
	default:
		return ProtectionCapabilities{}
	}
}

// canFuzzyMatchTrailingTiers reports whether this venue supplies enough echoed-back
// order attributes for findEquivalentPartialTrailingOrder to identify a tier WITHOUT
// a stored exchange order ID.
//
// Consequence of a false here: persisting an armed native-trailing record with an
// empty ExchangeOrderID is unrecoverable — the tier can never be re-found, so it is
// re-armed on every cooldown window and orders accumulate without bound. Callers
// must treat "placed but no ID" as a placement FAILURE on such venues and drop to
// the local managed monitor instead (double cover, never takeover).
func (at *AutoTrader) canFuzzyMatchTrailingTiers() bool {
	return at.GetProtectionCapabilities().ReportsTrailingCallbackRate
}

// trailingRecordIsUnrecoverable is the single definition of the invariant:
// "an armed native-trailing record must be re-findable later".
//
// A record is re-findable by exchange order ID, or — failing that — by the fuzzy
// tier matcher, which needs the venue to echo back callbackRate. If NEITHER holds,
// the tier reads as missing on every poll and is re-armed on every cooldown window
// without bound (2026-07-27 HYPEUSDT: 174 identical partial trailing orders).
//
// Callers must treat true here as a placement FAILURE and drop to the LOCAL managed
// monitor (applyExchangeFailedLocalMonitor) — double cover, never takeover. It lives
// here rather than inline at each arm branch because there are four such branches
// (binance/bitget/okx partial + the shared full-tier tail) and an invariant enforced
// by convention at each call site is one that eventually drifts.
//
// Whitespace counts as empty: a blank ID is exactly as unusable as a missing one.
func (at *AutoTrader) trailingRecordIsUnrecoverable(exchangeOrderID string) bool {
	return strings.TrimSpace(exchangeOrderID) == "" && !at.canFuzzyMatchTrailingTiers()
}
