package gate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nofx/store"
	"nofx/trader/types"
)

// ordersServer serves both the regular order list and the trigger order list.
func newOrdersServer(t *testing.T, regular, triggers []map[string]interface{}) *GateTrader {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(path, "/futures/usdt/price_orders"):
			json.NewEncoder(w).Encode(triggers)
		case strings.Contains(path, "/futures/usdt/orders"):
			json.NewEncoder(w).Encode(regular)
		case strings.Contains(path, "/futures/usdt/contracts/"):
			json.NewEncoder(w).Encode(map[string]interface{}{
				"name": "BTC_USDT", "quanto_multiplier": "0.001", "order_price_round": "0.1",
			})
		default:
			json.NewEncoder(w).Encode([]map[string]interface{}{})
		}
	}))
	t.Cleanup(srv.Close)
	tr := NewGateTrader("k", "s")
	tr.client.ChangeBasePath(srv.URL + "/api/v4")
	return tr
}

func findOrder(orders []types.OpenOrder, id string) (types.OpenOrder, bool) {
	for _, o := range orders {
		if o.OrderID == id {
			return o, true
		}
	}
	return types.OpenOrder{}, false
}

// ============================================================================
// GetOpenOrders field contract
// ============================================================================

func TestOpenOrdersPopulatePositionSide(t *testing.T) {
	// PositionSide was left empty for gate. The protection stack guards 18 side filters
	// as `if order.PositionSide != "" && !strings.EqualFold(...)`, so an empty value
	// does not fail closed — it DISABLES the filter and every order matches every
	// position, across sides.
	triggers := []map[string]interface{}{
		triggerOrderJSON(301, "close-long-order", -50, 2),
		triggerOrderJSON(302, "close-short-order", 50, 1),
	}
	orders, err := newOrdersServer(t, nil, triggers).GetOpenOrders("BTCUSDT")
	if err != nil {
		t.Fatalf("GetOpenOrders: %v", err)
	}
	long, ok := findOrder(orders, "301")
	if !ok {
		t.Fatal("trigger order 301 missing from GetOpenOrders")
	}
	if long.PositionSide != "LONG" {
		t.Fatalf("order 301 PositionSide = %q, want LONG; an empty value disables every side filter", long.PositionSide)
	}
	short, _ := findOrder(orders, "302")
	if short.PositionSide != "SHORT" {
		t.Fatalf("order 302 PositionSide = %q, want SHORT", short.PositionSide)
	}
}

func TestOpenOrdersClassifyStopVsTakeProfitPerSide(t *testing.T) {
	// Same rule, opposite meaning depending on side. Reading Rule alone (the pre-fix
	// behaviour) reports every short's stop as a take profit, so the reconciler sees
	// missingSL forever and re-arms without bound.
	triggers := []map[string]interface{}{
		triggerOrderJSON(401, "close-long-order", -50, 2), // long stop
		triggerOrderJSON(402, "close-short-order", 50, 1), // short stop, SAME direction of travel
		triggerOrderJSON(403, "close-long-order", -50, 1), // long target
		triggerOrderJSON(404, "close-short-order", 50, 2), // short target
	}
	orders, err := newOrdersServer(t, nil, triggers).GetOpenOrders("BTCUSDT")
	if err != nil {
		t.Fatalf("GetOpenOrders: %v", err)
	}
	want := map[string]struct{ typ, coarse string }{
		"401": {"STOP_MARKET", "stop_loss"},
		"402": {"STOP_MARKET", "stop_loss"},
		"403": {"TAKE_PROFIT_MARKET", "take_profit"},
		"404": {"TAKE_PROFIT_MARKET", "take_profit"},
	}
	for id, exp := range want {
		o, ok := findOrder(orders, id)
		if !ok {
			t.Fatalf("order %s missing", id)
		}
		if o.Type != exp.typ {
			t.Fatalf("order %s Type = %q, want %q", id, o.Type, exp.typ)
		}
		if o.ProtectionRoleCoarse != exp.coarse {
			t.Fatalf("order %s ProtectionRoleCoarse = %q, want %q", id, o.ProtectionRoleCoarse, exp.coarse)
		}
	}
}

func TestOpenOrdersMarkUnclassifiableAsUnknown(t *testing.T) {
	// "unknown" is a real value the generic layer understands. Silently labelling an
	// undecidable order "stop_loss" would make a filtered cancel destroy it.
	triggers := []map[string]interface{}{triggerOrderJSON(501, "", 0, 2)}
	orders, err := newOrdersServer(t, nil, triggers).GetOpenOrders("BTCUSDT")
	if err != nil {
		t.Fatalf("GetOpenOrders: %v", err)
	}
	o, _ := findOrder(orders, "501")
	if o.ProtectionRoleCoarse != "unknown" {
		t.Fatalf("ProtectionRoleCoarse = %q, want unknown", o.ProtectionRoleCoarse)
	}
}

func TestOpenOrdersDecodeReasonAndTierFromText(t *testing.T) {
	// The tier is the second identity in multi-tier DD protection: without it two
	// sibling tiers with equal clamped quantities are indistinguishable and get
	// re-armed forever.
	reason := store.ReasonWithTier(store.MechManagedDrawdown, 3)
	text := encodeReasonClientID(reason)
	if text == "" {
		t.Fatalf("encodeReasonClientID(%q) returned empty; %s must have a registered code",
			reason, store.MechManagedDrawdown)
	}
	trigger := triggerOrderJSON(601, "close-long-order", -50, 1)
	trigger["initial"].(map[string]interface{})["text"] = text

	orders, err := newOrdersServer(t, nil, []map[string]interface{}{trigger}).GetOpenOrders("BTCUSDT")
	if err != nil {
		t.Fatalf("GetOpenOrders: %v", err)
	}
	o, _ := findOrder(orders, "601")
	if o.ClientOrderID != text {
		t.Fatalf("ClientOrderID = %q, want the raw text %q", o.ClientOrderID, text)
	}
	if o.ProtectionRole != store.MechManagedDrawdown {
		t.Fatalf("ProtectionRole = %q, want %q", o.ProtectionRole, store.MechManagedDrawdown)
	}
	if o.ProtectionTier != 3 {
		t.Fatalf("ProtectionTier = %d, want 3; without the tier two sibling DD tiers with equal clamped quantities are indistinguishable", o.ProtectionTier)
	}
}

func TestGateTextStaysWithinGateLimitForEveryMechanism(t *testing.T) {
	// Gate rejects the WHOLE order when text is malformed: it must be prefixed "t-",
	// be at most 28 bytes excluding that prefix, and use only [0-9A-Za-z_-.]. A single
	// mechanism whose encoding overflows would make that protection order impossible to
	// place, so the budget is checked for all of them, tiered and untiered.
	mechanisms := []string{
		store.MechBreakEven, store.MechLadderSL, store.MechLadderTP,
		store.MechFullSL, store.MechFullTP, store.MechFallbackSL,
		store.MechNativeTrailing, store.MechTrailingTP, store.MechManagedDrawdown,
		store.MechStructuralSL, store.MechBreadthBreaker, store.MechTrendReversal,
		store.MechAIClose, store.MechManualClose, store.MechTimeStop,
		store.MechMaxHold, store.MechEmergency, store.MechLiquidation,
	}
	allowed := func(r rune) bool {
		return (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') ||
			(r >= 'a' && r <= 'z') || r == '_' || r == '-' || r == '.'
	}
	for _, mech := range mechanisms {
		for tier := 0; tier <= 9; tier++ {
			reason := mech
			if tier > 0 {
				reason = store.ReasonWithTier(mech, tier)
			}
			text := encodeReasonClientID(reason)
			if text == "" {
				t.Fatalf("%s (tier %d): no encoding produced", mech, tier)
			}
			if !strings.HasPrefix(text, "t-") {
				t.Fatalf("%s (tier %d): text %q lacks the required \"t-\" prefix", mech, tier, text)
			}
			payload := strings.TrimPrefix(text, "t-")
			if len(payload) > 28 {
				t.Fatalf("%s (tier %d): payload %d bytes > Gate's 28-byte limit: %q", mech, tier, len(payload), text)
			}
			for _, r := range payload {
				if !allowed(r) {
					t.Fatalf("%s (tier %d): text %q contains character %q which Gate rejects", mech, tier, text, r)
				}
			}
			if got := decodeReasonFromClientID(text); got != mech {
				t.Fatalf("%s (tier %d): round trip decoded %q", mech, tier, got)
			}
			if tier > 0 {
				if got := decodeTierFromClientID(text); got != tier {
					t.Fatalf("%s: tier round trip gave %d, want %d", mech, got, tier)
				}
			}
		}
	}
}

func TestOpenOrdersReturnInternalSymbol(t *testing.T) {
	// Must match GetPositions' symbol form or the reconciler cannot pair them.
	triggers := []map[string]interface{}{triggerOrderJSON(701, "close-long-order", -50, 2)}
	regular := []map[string]interface{}{{
		"id": 702, "contract": "BTC_USDT", "size": -50, "price": "50000",
		"is_reduce_only": true,
	}}
	orders, err := newOrdersServer(t, regular, triggers).GetOpenOrders("BTCUSDT")
	if err != nil {
		t.Fatalf("GetOpenOrders: %v", err)
	}
	if len(orders) != 2 {
		t.Fatalf("got %d orders, want 2 (one regular, one trigger)", len(orders))
	}
	for _, o := range orders {
		if o.Symbol != "BTCUSDT" {
			t.Fatalf("order %s Symbol = %q, want BTCUSDT", o.OrderID, o.Symbol)
		}
	}
}

func TestOpenOrdersConvertContractsToBaseQuantity(t *testing.T) {
	// 50 contracts * 0.001 = 0.05 BTC. Reporting the raw contract count would make the
	// reconciler's quantity comparisons off by the multiplier (1000x here), so no
	// existing protection order would ever be recognised as equivalent.
	triggers := []map[string]interface{}{triggerOrderJSON(801, "close-long-order", -50, 2)}
	orders, err := newOrdersServer(t, nil, triggers).GetOpenOrders("BTCUSDT")
	if err != nil {
		t.Fatalf("GetOpenOrders: %v", err)
	}
	o, _ := findOrder(orders, "801")
	if o.Quantity < 0.04999 || o.Quantity > 0.05001 {
		t.Fatalf("Quantity = %v, want 0.05 base units (50 contracts * 0.001)", o.Quantity)
	}
	if o.StopPrice != 49000 {
		t.Fatalf("StopPrice = %v, want 49000 from trigger.price", o.StopPrice)
	}
}
