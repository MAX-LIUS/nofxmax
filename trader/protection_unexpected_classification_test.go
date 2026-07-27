package trader

import "testing"

func TestClassifyUnexpectedProtectionOrdersSeparatesManualForeignFromBotDuplicate(t *testing.T) {
	plan := &ProtectionPlan{NeedsStopLoss: true, StopLossPrice: 100.9}
	orders := []OpenOrder{
		{OrderID: "keep_sl", PositionSide: "SHORT", Type: "STOP_MARKET", StopPrice: 100.9},
		{OrderID: "4c363c81edc5bcde_ladder_sl_old", PositionSide: "SHORT", Type: "STOP_MARKET", StopPrice: 105},
		{OrderID: "manual-protective-stop", PositionSide: "SHORT", Type: "STOP_MARKET", StopPrice: 110},
	}

	summary := classifyUnexpectedProtectionOrders(orders, "SHORT", plan, false, nativeTrailingArmedOnly(false), true)
	if summary.ExpectedStaticOwner != 1 {
		t.Fatalf("expected one static owner, got %+v", summary)
	}
	if summary.StaleBotDuplicate != 1 || len(summary.StaleBotDuplicateIDs) != 1 || summary.StaleBotDuplicateIDs[0] != "4c363c81edc5bcde_ladder_sl_old" {
		t.Fatalf("expected one stale bot duplicate id, got %+v", summary)
	}
	if summary.ManualOrForeign != 1 || len(summary.ManualOrForeignIDs) != 1 || summary.ManualOrForeignIDs[0] != "manual-protective-stop" {
		t.Fatalf("expected one manual/foreign id, got %+v", summary)
	}

	ids := collectUnexpectedProtectionOrderIDs(orders, "SHORT", plan, false, nativeTrailingArmedOnly(false))
	if len(ids) != 1 || ids[0] != "4c363c81edc5bcde_ladder_sl_old" {
		t.Fatalf("expected cleanup ids to include only stale bot duplicate, got %+v", ids)
	}
}

func TestClassifyUnexpectedProtectionOrdersOrphanForInactivePosition(t *testing.T) {
	orders := []OpenOrder{{OrderID: "native_trailing_old", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET", StopPrice: 99, CallbackRate: 0.02}}
	summary := classifyUnexpectedProtectionOrders(orders, "SHORT", nil, false, nativeTrailingArmedOnly(false), false)
	if summary.OrphanForInactive != 1 || len(summary.OrphanForInactiveIDs) != 1 || summary.OrphanForInactiveIDs[0] != "native_trailing_old" {
		t.Fatalf("expected inactive trailing order classified as orphan, got %+v", summary)
	}
}

func TestClassifyUnexpectedProtectionOrdersExpectedDynamicOwners(t *testing.T) {
	orders := []OpenOrder{
		{OrderID: "be-stop", PositionSide: "LONG", Type: "STOP_MARKET", StopPrice: 100},
		{OrderID: "native_trailing_1", PositionSide: "LONG", Type: "TRAILING_STOP_MARKET", StopPrice: 105, CallbackRate: 0.02},
	}
	summary := classifyUnexpectedProtectionOrders(orders, "LONG", nil, true, nativeTrailingArmedOnly(true), true)
	if summary.ExpectedDynamicOwner != 2 {
		t.Fatalf("expected break-even and trailing as dynamic owners, got %+v", summary)
	}
	if summary.StaleBotDuplicate != 0 || summary.ManualOrForeign != 0 {
		t.Fatalf("expected no unexpected categories for dynamic owners, got %+v", summary)
	}
}

// TestBinanceBrokerPrefixRecognizedAsBot verifies that a stale Binance protection
// order — whose client ID carries ONLY the Binance broker prefix (x-KzrpZaP9),
// not any semantic tag — is classified as a stale bot duplicate (cleanable), not
// manual/foreign (preserved forever). This is the fix for phantom stop-order
// accumulation across re-entries on Binance traders.
func TestBinanceBrokerPrefixRecognizedAsBot(t *testing.T) {
	plan := &ProtectionPlan{NeedsStopLoss: true, StopLossPrice: 100.9}
	orders := []OpenOrder{
		{OrderID: "keep_sl", ClientOrderID: "x-KzrpZaP91234567890abcd", PositionSide: "LONG", Type: "STOP_MARKET", StopPrice: 100.9},
		// stale bot stop from a prior entry: only the Binance broker prefix, no semantic tag
		{OrderID: "998877", ClientOrderID: "x-KzrpZaP99876543210wxyz", PositionSide: "LONG", Type: "STOP_MARKET", StopPrice: 95.0},
		// a genuine foreign/manual order (no broker prefix) must still be preserved
		{OrderID: "manual-1", ClientOrderID: "someones-manual-stop", PositionSide: "LONG", Type: "STOP_MARKET", StopPrice: 90.0},
	}
	summary := classifyUnexpectedProtectionOrders(orders, "LONG", plan, false, nativeTrailingArmedOnly(false), true)
	if summary.StaleBotDuplicate != 1 || len(summary.StaleBotDuplicateIDs) != 1 {
		t.Fatalf("expected the Binance-prefixed stale stop to be a bot duplicate, got %+v", summary)
	}
	if summary.ManualOrForeign != 1 || len(summary.ManualOrForeignIDs) != 1 || summary.ManualOrForeignIDs[0] != "manual-1" {
		t.Fatalf("expected only the true manual order preserved as foreign, got %+v", summary)
	}
}

func TestIsLikelyBotProtectionOrder_BinanceAndOKXPrefixes(t *testing.T) {
	cases := []struct {
		name   string
		order  OpenOrder
		wantBot bool
	}{
		{"binance broker prefix", OpenOrder{ClientOrderID: "x-KzrpZaP91700000000001a2"}, true},
		{"okx broker prefix", OpenOrder{ClientOrderID: "4c363c81edc5BCDE-full_sl"}, true},
		{"semantic full_ tag", OpenOrder{ClientOrderID: "full_sl_something"}, true},
		{"genuine manual", OpenOrder{ClientOrderID: "my-manual-stop"}, false},
		{"empty", OpenOrder{}, false},
	}
	for _, c := range cases {
		if got := isLikelyBotProtectionOrder(c.order); got != c.wantBot {
			t.Errorf("%s: isLikelyBotProtectionOrder=%v want %v", c.name, got, c.wantBot)
		}
	}
}
