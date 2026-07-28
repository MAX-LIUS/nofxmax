package trader

import (
	"math"
	"strconv"
	"strings"
)

// protectionQtyQuantizer reports the quantity string a venue would actually store
// for a requested quantity. Every adapter already implements FormatQuantity for
// exactly this purpose (it is what the order-placement path sends on the wire),
// so equivalence can be decided by asking the venue instead of guessing.
type protectionQtyQuantizer func(symbol string, quantity float64) (string, error)

// protectionQtyRelTolerance is the fallback relative gap below which two
// quantities are considered the same order. It only applies when the venue's
// own granularity is unavailable (nil quantizer or a FormatQuantity error).
const protectionQtyRelTolerance = 0.05

// protectionQuantitiesEquivalent decides whether an order resting on the exchange
// with quantity `actual` is the same order the plan wants at quantity `target`.
//
// Why this cannot be a plain relative comparison (2026-07-28, ZECUSDT SHORT):
// the plan computes a tier quantity in base-asset units at full float precision
// (0.08 × 18% = 0.0144), but the venue stores only what its lot granularity
// permits (OKX ZEC-USDT-SWAP: ctVal 0.01, lotSz 1 ⇒ 1.44 contracts → 1 → 0.01
// base). The two numbers then differ by 30% — for no reason other than
// quantization — so the 5% relative check answered "different order", the place
// path re-placed a tier that was already resting, and the reconciler canceled the
// duplicate it had just created. 25 place+cancel rounds in one session on one
// symbol, ~150 wasted algo calls/hour, while the tier was correctly protected the
// whole time.
//
// The bug is not the 5% number. It is comparing a pre-quantization intent against
// a post-quantization fact: no fixed tolerance can be right, because the
// quantization error is a function of how many lots the tier spans. A tier
// spanning 1.44 lots has 30% error; the same ratio on a 100-lot tier has 0.5%.
// That is why symbols with large tier/lot ratios (SOL, SKHY) never churned and
// ZEC churned every cycle — the defect was latent in every narrow-tier position.
//
// So we quantize BOTH sides through the venue and compare the results. If the
// exchange cannot represent the difference, there is no difference: placing
// another order would produce a byte-identical request. This is unit-agnostic —
// OKX FormatQuantity returns contract count, Binance returns base quantity, and
// either way both sides pass through the same function, so only the comparison's
// meaning matters, not its units.
//
// The stale-order case this check exists to catch still works: an entry-quantity
// stop left behind after a TP fill (0.10 vs 0.08) quantizes to 10 vs 8 contracts
// — different strings, correctly reported as a different order.
func protectionQuantitiesEquivalent(quantize protectionQtyQuantizer, symbol string, target, actual float64) bool {
	if target <= 0 || actual <= 0 {
		// A missing/unknown quantity on either side is not evidence of a
		// mismatch. Treating it as one would re-place tiers against venues that
		// do not report order quantity, so fall back to "same order".
		return true
	}
	if quantize != nil {
		targetStr, targetErr := quantize(symbol, target)
		actualStr, actualErr := quantize(symbol, actual)
		if targetErr == nil && actualErr == nil {
			return quantizedQuantityStringsEqual(targetStr, actualStr)
		}
	}
	return math.Abs(actual-target)/math.Max(actual, target) <= protectionQtyRelTolerance
}

// quantizedQuantityStringsEqual compares two formatted quantities numerically
// rather than as raw strings. Adapters derive their decimal precision from lot
// size, so the same value can format as "1" and "1.0" across code paths; a raw
// string compare would call those different orders and reintroduce the churn.
// Falls back to string equality only if either side is unparseable.
func quantizedQuantityStringsEqual(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	av, aErr := strconv.ParseFloat(a, 64)
	bv, bErr := strconv.ParseFloat(b, 64)
	if aErr != nil || bErr != nil {
		return a == b
	}
	return av == bv
}

// protectionQtyQuantizerFor returns the venue's quantizer, or nil when the
// adapter is unavailable. Kept as a helper so call sites do not have to repeat
// the nil-trader guard.
func (at *AutoTrader) protectionQtyQuantizerFor() protectionQtyQuantizer {
	if at == nil || at.trader == nil {
		return nil
	}
	return at.trader.FormatQuantity
}
