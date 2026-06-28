package trader

import (
	"math"
	"strings"

	"nofx/store"
)

// DrawdownTrailingParams is the canonical, exchange-agnostic representation of a
// drawdown take-profit rule expressed as a trailing stop: an activation price
// (where the exchange begins tracking the peak) and a callback ratio in PRICE
// space (the fraction the price may retrace from the running peak before the
// stop fires). Every exchange adapter consumes these two numbers; per-exchange
// unit quirks (decimal vs percent, min/max clamps) are applied at the adapter
// boundary, not here.
//
// ActivationImmediate is true when the rule has no peak-trigger threshold
// (MinProfitPct <= 0): the trailing stop is armed immediately from the current
// price and ActivationPrice is 0 (adapters must omit the activation field).
type DrawdownTrailingParams struct {
	ActivationPrice     float64
	ActivationImmediate bool
	CallbackRatio       float64 // decimal price fraction, e.g. 0.04 == 4%
	Valid               bool
}

// resolveDrawdownTrailingParams converts a single drawdown rule into canonical
// trailing params using the new unambiguous panel semantics:
//
//   Peak-trigger (MinProfit):
//     percent: activation = entry * (1 +/- MinProfitPct/100)
//     atr:     activation = entry +/- MinProfitPct * atr
//     <= 0:    immediate activation (no threshold)
//
//   Drawdown distance (MaxDrawdown) — a price retracement from the peak:
//     percent: callback = MaxDrawdownPct/100               (e.g. 4% -> 0.04)
//     atr:     callback = (MaxDrawdownPct * atr) / activationPrice
//
// atr may be 0 when no field opts into ATR units; in that case ATR-unit fields
// are treated as invalid (caller should have resolved ATR earlier).
func resolveDrawdownTrailingParams(entryPrice float64, side string, rule store.DrawdownTakeProfitRule, atr float64) DrawdownTrailingParams {
	out := DrawdownTrailingParams{}
	if entryPrice <= 0 {
		return out
	}
	long := strings.EqualFold(side, "long")
	short := strings.EqualFold(side, "short")
	if !long && !short {
		return out
	}

	// --- Peak-trigger / activation price ---
	var activationDistance float64 // absolute price distance from entry
	if rule.MinProfitPct > 0 {
		if rule.MinProfitUnit == store.ProtectionUnitATR {
			if atr <= 0 {
				return out
			}
			activationDistance = rule.MinProfitPct * atr
		} else {
			activationDistance = entryPrice * (rule.MinProfitPct / 100.0)
		}
	}

	if activationDistance <= 0 {
		// No peak-trigger threshold => activate immediately. The reference price
		// for an ATR callback is the entry price itself (best available anchor
		// at arm time; the exchange then trails from the live peak).
		out.ActivationImmediate = true
		out.ActivationPrice = 0
	} else if long {
		out.ActivationPrice = entryPrice + activationDistance
	} else {
		out.ActivationPrice = entryPrice - activationDistance
	}
	if !out.ActivationImmediate && out.ActivationPrice <= 0 {
		return out
	}

	// --- Drawdown distance / callback ratio (price space) ---
	if rule.MaxDrawdownPct <= 0 {
		return out
	}
	// Reference price the callback ratio is measured against. For an activated
	// rule that is the activation price; for an immediate rule we anchor on entry.
	refPrice := out.ActivationPrice
	if out.ActivationImmediate {
		refPrice = entryPrice
	}
	if refPrice <= 0 {
		return out
	}

	var callback float64
	if rule.MaxDrawdownUnit == store.ProtectionUnitATR {
		if atr <= 0 {
			return out
		}
		callback = (rule.MaxDrawdownPct * atr) / refPrice
	} else {
		// percent mode: the panel value IS the price-retracement percentage.
		callback = rule.MaxDrawdownPct / 100.0
	}
	if callback <= 0 || math.IsNaN(callback) || math.IsInf(callback, 0) {
		return out
	}
	if callback > 1 {
		callback = 1
	}
	out.CallbackRatio = callback
	out.Valid = true
	return out
}
