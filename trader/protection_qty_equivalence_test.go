package trader

import (
	"fmt"
	"math"
	"testing"

	tradertypes "nofx/trader/types"
)

// okxLikeQuantizer mimics OKXTrader.FormatQuantity: base quantity → contract
// count (÷ctVal), then rounded to the instrument's lot precision. lotSz>=1 means
// integral contracts, which is what makes narrow tiers quantize hard.
func okxLikeQuantizer(ctVal float64, lotIntegral bool) protectionQtyQuantizer {
	return func(_ string, quantity float64) (string, error) {
		sz := quantity / ctVal
		if lotIntegral {
			return fmt.Sprintf("%.0f", sz), nil
		}
		return fmt.Sprintf("%.2f", sz), nil
	}
}

// TestProtectionQtyEquivalence_ZECProductionCase is the exact churn observed on
// 2026-07-28: ZEC-USDT-SWAP (ctVal 0.01, lotSz 1), position 0.08, ladder tier
// ratios 18%/15%/12%. Each tier's planned quantity quantizes to the SAME single
// contract the exchange already holds, yet the legacy 5% relative check called
// every one of them a different order.
func TestProtectionQtyEquivalence_ZECProductionCase(t *testing.T) {
	quantize := okxLikeQuantizer(0.01, true)
	const restingQty = 0.01 // what OKX actually stored: 1 contract

	cases := []struct {
		ratioPct float64
		target   float64
		// churnedBefore records whether the LEGACY 5% rule rejected this tier.
		// Not every tier of the ladder did: quantization error depends on how far
		// the tier's lot count sits from an integer, so 18%/15% (1.44/1.20
		// contracts) churned while 12% (0.96 contracts, 4% gap) happened to land
		// inside tolerance and was stable. Recording this per-tier keeps the
		// negative control honest instead of over-claiming the blast radius.
		churnedBefore bool
	}{
		{18, 0.08 * 0.18, true},  // 0.0144 → 1.44 contracts → 1 (30% gap)
		{15, 0.08 * 0.15, true},  // 0.0120 → 1.20 contracts → 1 (17% gap)
		{12, 0.08 * 0.12, false}, // 0.0096 → 0.96 contracts → 1 (4% gap, was already fine)
	}
	for _, c := range cases {
		if !protectionQuantitiesEquivalent(quantize, "ZECUSDT", c.target, restingQty) {
			t.Errorf("ratio %.0f%%: target %.6f vs resting %.6f must be equivalent (both = 1 contract); this is the churn bug",
				c.ratioPct, c.target, restingQty)
		}
		legacyGap := math.Abs(restingQty-c.target) / math.Max(restingQty, c.target)
		legacyRejected := legacyGap > protectionQtyRelTolerance
		if legacyRejected != c.churnedBefore {
			t.Errorf("ratio %.0f%%: legacy gap %.4f ⇒ rejected=%v, expected rejected=%v — the fix's blast radius is not what this test claims",
				c.ratioPct, legacyGap, legacyRejected, c.churnedBefore)
		}
	}
}

// TestProtectionQtyEquivalence_StaleEntryQtyStopStillCaught is the case the
// quantity check exists for: a stop left behind at the ENTRY quantity after a TP
// fill shrank the position. Quantization must not paper this over.
func TestProtectionQtyEquivalence_StaleEntryQtyStopStillCaught(t *testing.T) {
	quantize := okxLikeQuantizer(0.01, true)
	// plan wants the remaining 0.08 (8 contracts); a 0.10 (10 contracts) order rests.
	if protectionQuantitiesEquivalent(quantize, "ZECUSDT", 0.08, 0.10) {
		t.Fatal("0.08 (8 contracts) vs 0.10 (10 contracts) must NOT be equivalent — stale entry-qty stop would go undetected")
	}
}

// TestProtectionQtyEquivalence_WideTierUnaffected pins that symbols which never
// churned keep their behaviour: when a tier spans many lots, quantization error
// is negligible and equivalence is decided the same way as before.
func TestProtectionQtyEquivalence_WideTierUnaffected(t *testing.T) {
	quantize := okxLikeQuantizer(0.01, true)
	// SOL-like: position 3.02, 18% tier = 0.5436 → 54.36 → 54 contracts = 0.54.
	if !protectionQuantitiesEquivalent(quantize, "SOLUSDT", 3.02*0.18, 0.54) {
		t.Error("wide tier must remain equivalent")
	}
	if protectionQuantitiesEquivalent(quantize, "SOLUSDT", 3.02*0.18, 0.30) {
		t.Error("a genuinely different size must remain non-equivalent")
	}
}

// TestProtectionQtyEquivalence_FallbackWhenVenueUnavailable pins the degraded
// path: with no quantizer (or a failing one) the legacy relative tolerance is
// used, so behaviour is never worse than before the fix.
func TestProtectionQtyEquivalence_FallbackWhenVenueUnavailable(t *testing.T) {
	if protectionQuantitiesEquivalent(nil, "ZECUSDT", 0.0144, 0.01) {
		t.Error("nil quantizer must fall back to the relative rule (30% gap ⇒ not equivalent)")
	}
	if !protectionQuantitiesEquivalent(nil, "ZECUSDT", 0.0100, 0.0102) {
		t.Error("nil quantizer must accept a 2% gap under the relative rule")
	}
	failing := func(string, float64) (string, error) { return "", fmt.Errorf("venue metadata unavailable") }
	if protectionQuantitiesEquivalent(failing, "ZECUSDT", 0.0144, 0.01) {
		t.Error("failing quantizer must fall back to the relative rule, not silently accept")
	}
}

// TestProtectionQtyEquivalence_MissingQuantityIsNotAMismatch pins that a venue
// which does not report order quantity cannot trigger re-placement.
func TestProtectionQtyEquivalence_MissingQuantityIsNotAMismatch(t *testing.T) {
	quantize := okxLikeQuantizer(0.01, true)
	if !protectionQuantitiesEquivalent(quantize, "ZECUSDT", 0.0144, 0) {
		t.Error("unreported resting quantity must not be read as a mismatch")
	}
	if !protectionQuantitiesEquivalent(quantize, "ZECUSDT", 0, 0.01) {
		t.Error("unknown target quantity must not be read as a mismatch")
	}
}

// TestQuantizedQuantityStringsEqual_NumericNotTextual pins that formatting
// differences across adapter code paths ("1" vs "1.0") do not reintroduce churn,
// while genuinely different sizes stay different.
func TestQuantizedQuantityStringsEqual_NumericNotTextual(t *testing.T) {
	if !quantizedQuantityStringsEqual("1", "1.0") {
		t.Error(`"1" and "1.0" are the same size`)
	}
	if !quantizedQuantityStringsEqual(" 8 ", "8.00") {
		t.Error("whitespace/precision differences are not size differences")
	}
	if quantizedQuantityStringsEqual("8", "10") {
		t.Error("8 and 10 contracts are different sizes")
	}
	if !quantizedQuantityStringsEqual("abc", "abc") {
		t.Error("unparseable but identical strings fall back to string equality")
	}
	if quantizedQuantityStringsEqual("abc", "def") {
		t.Error("unparseable and different strings must not be equal")
	}
}

// TestHasEquivalentProtectionOrder_UsesVenueGranularity drives the fix through
// the caller the place path actually uses, so the wiring is covered and not just
// the primitive.
func TestHasEquivalentProtectionOrder_UsesVenueGranularity(t *testing.T) {
	resting := []tradertypes.OpenOrder{{
		Type:         "TAKE_PROFIT_MARKET",
		PositionSide: "SHORT",
		StopPrice:    467.23,
		Quantity:     0.01,
	}}
	quantize := okxLikeQuantizer(0.01, true)
	target := 0.08 * 0.18

	if !hasExistingEquivalentProtection(resting, "SHORT", true, 467.23, target, quantize, "ZECUSDT") {
		t.Error("place path must recognise its own resting tier and skip re-placing it")
	}
	if hasExistingEquivalentProtection(resting, "SHORT", true, 462.25, target, quantize, "ZECUSDT") {
		t.Error("a different tier price must not be satisfied by this order")
	}
	if hasExistingEquivalentProtection(resting, "SHORT", false, 467.23, target, quantize, "ZECUSDT") {
		t.Error("a take-profit order must not satisfy a stop-loss target")
	}
	// Negative control on the wiring: without the venue quantizer this same call
	// returns false, which is exactly the production churn.
	if hasExistingEquivalentProtection(resting, "SHORT", true, 467.23, target, nil, "ZECUSDT") {
		t.Error("expected the legacy path to reject — if it accepts, this test no longer pins the fix")
	}
}
