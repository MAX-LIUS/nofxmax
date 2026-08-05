package gate

import (
	"testing"

	"github.com/gateio/gateapi-go/v6"
)

// ============================================================================
// Trigger rule direction
//
// These four cases are the whole reason gate protection was broken: the original
// code had Gate's Rule semantics inverted (it documented 1 as "<=" and 2 as ">="),
// so every stop and every target was placed with the wrong comparison direction.
// A long's stop then fired on the way UP. The official SDK comment on
// FuturesPriceTrigger.Rule is authoritative:
//
//	1: >= (requires trigger price > last price)
//	2: <= (requires trigger price < last price)
// ============================================================================

func TestTriggerRuleForAllSideKindCombinations(t *testing.T) {
	cases := []struct {
		name     string
		kind     protectionKind
		side     string
		wantRule int32
		why      string
	}{
		{"long stop loss", protectionKindStopLoss, "LONG", 2, "a long is stopped out when price FALLS to the stop"},
		{"long take profit", protectionKindTakeProfit, "LONG", 1, "a long takes profit when price RISES to the target"},
		{"short stop loss", protectionKindStopLoss, "SHORT", 1, "a short is stopped out when price RISES to the stop"},
		{"short take profit", protectionKindTakeProfit, "SHORT", 2, "a short takes profit when price FALLS to the target"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := triggerRuleFor(tc.kind, tc.side); got != tc.wantRule {
				t.Fatalf("triggerRuleFor(%v, %q) = %d, want %d — %s",
					tc.kind, tc.side, got, tc.wantRule, tc.why)
			}
		})
	}
}

func TestTriggerRuleForSideCaseInsensitiveAndDefaultsLong(t *testing.T) {
	// The protection stack passes side strings in mixed case ("LONG", "long",
	// "Short"). An exact == "SHORT" comparison would silently treat "short" as long
	// and invert the rule, so case-insensitivity is load-bearing, not cosmetic.
	if triggerRuleFor(protectionKindStopLoss, "short") != 1 {
		t.Fatal("lowercase \"short\" must be recognised as SHORT")
	}
	if triggerRuleFor(protectionKindStopLoss, "Short") != 1 {
		t.Fatal("mixed-case \"Short\" must be recognised as SHORT")
	}
	// An unknown/empty side must degrade to LONG rather than produce a rule of 0,
	// which Gate would reject outright.
	if got := triggerRuleFor(protectionKindStopLoss, ""); got != 2 {
		t.Fatalf("empty side rule = %d, want the LONG default 2", got)
	}
}

// ============================================================================
// Classification of resting trigger orders
//
// The old classifier read Trigger.Rule alone ("Rule==2 -> take profit"), which is
// wrong for shorts: Rule 2 is a LONG's stop but a SHORT's target. That made the
// reconciler see missingSL forever on every short position while treating the real
// stop as a take profit.
// ============================================================================

func triggerOrder(orderType string, size int64, rule int32) gateapi.FuturesPriceTriggeredOrder {
	return gateapi.FuturesPriceTriggeredOrder{
		Initial:   gateapi.FuturesInitialOrder{Size: size},
		Trigger:   gateapi.FuturesPriceTrigger{Rule: rule},
		OrderType: orderType,
	}
}

func TestClassifyTriggerOrderAllSideKindCombinations(t *testing.T) {
	cases := []struct {
		name     string
		order    gateapi.FuturesPriceTriggeredOrder
		wantKind protectionKind
	}{
		{"long stop (rule 2)", triggerOrder("close-long-order", -10, 2), protectionKindStopLoss},
		{"long target (rule 1)", triggerOrder("close-long-order", -10, 1), protectionKindTakeProfit},
		{"short stop (rule 1)", triggerOrder("close-short-order", 10, 1), protectionKindStopLoss},
		{"short target (rule 2)", triggerOrder("close-short-order", 10, 2), protectionKindTakeProfit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := classifyTriggerOrder(tc.order)
			if !ok {
				t.Fatal("classifyTriggerOrder reported not-ok for a fully specified order")
			}
			if got != tc.wantKind {
				t.Fatalf("classified as %v, want %v", got, tc.wantKind)
			}
		})
	}
}

func TestClassifyTriggerOrderUsesOrderTypeWhenSizeHasNoSign(t *testing.T) {
	// A FULL close carries Size=0, so the size sign cannot reveal the side. Gate's
	// read-only order_type is the only remaining signal; without it these orders are
	// unclassifiable and a filtered cancel would either skip or wrongly sweep them.
	sl, ok := classifyTriggerOrder(triggerOrder("close-long-position", 0, 2))
	if !ok || sl != protectionKindStopLoss {
		t.Fatalf("full-close long stop: got (%v, %v), want (stop loss, true)", sl, ok)
	}
	tp, ok := classifyTriggerOrder(triggerOrder("plan-close-short-position", 0, 2))
	if !ok || tp != protectionKindTakeProfit {
		t.Fatalf("full-close short target: got (%v, %v), want (take profit, true)", tp, ok)
	}
}

func TestClassifyTriggerOrderReportsNotOkWhenUndecidable(t *testing.T) {
	// No order_type, no size sign: the side is genuinely unknown. Guessing here is how
	// a "cancel the stops" sweep silently destroys a take profit, so the classifier
	// must admit failure and let the caller preserve the order.
	if _, ok := classifyTriggerOrder(triggerOrder("", 0, 2)); ok {
		t.Fatal("expected ok=false when neither order_type nor size sign identifies the side")
	}
	// Known side but a rule Gate never issues: still undecidable.
	if _, ok := classifyTriggerOrder(triggerOrder("close-long-order", -10, 0)); ok {
		t.Fatal("expected ok=false for an unknown trigger rule")
	}
}

func TestTriggerOrderSidePrefersOrderTypeOverSizeSign(t *testing.T) {
	// If the two ever disagree, order_type wins: it is set by the exchange, whereas
	// the size sign is our own encoding and is absent on full closes.
	side, ok := triggerOrderSide(triggerOrder("close-short-order", -10, 1))
	if !ok || side != "SHORT" {
		t.Fatalf("got (%q, %v), want (SHORT, true) — order_type must outrank the size sign", side, ok)
	}
}
