package trader

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nofx/store"
	tradertypes "nofx/trader/types"
)

// This file pins the per-venue reporting semantics of native trailing orders.
//
// The protection matchers are shared across exchanges, but the two venues report
// a resting trailing order very differently, and every trailing bug of 2026-07
// came from shared code assuming OKX's shape:
//
//	                  | OKX                        | Binance
//	 callbackRate     | reported                   | ABSENT (always 0)
//	 activatePrice    | reported (activePx)        | ABSENT
//	 StopPrice        | = activePx (the anchor)    | = triggerPrice (MOVING stop)
//	 ActivationStatus | pending_activation/activated | always "activated"
//
// Binance's algo-order list endpoint (SDK futures.GetAlgoOrderResp) carries
// neither callbackRate nor activatePrice, so trader/binance/futures_orders.go
// substitutes triggerPrice into StopPrice/ActivationPrice and reports
// ActivationStatus="activated". triggerPrice on an unactivated trailing order is
// the exchange's live moving-stop level, which bears no relation to the planned
// activation anchor.
//
// fakeVenueTrader replays those two shapes faithfully so the shared matchers are
// tested against what each venue actually returns rather than an idealised order.
type fakeVenueTrader struct {
	mu       sync.Mutex
	venue    string // "okx" | "binance"
	orders   []tradertypes.OpenOrder
	position map[string]interface{}
	placed   int
	// markPrice drives the Binance moving-stop simulation: an unactivated Binance
	// trailing order reports triggerPrice ≈ mark*(1-callback) for a long.
	markPrice float64
	// calls records the parameters the venue was ASKED for, independently of how it
	// reports them back. Necessary because Binance never echoes callbackRate, so a
	// test asserting on what we sent cannot read it out of GetOpenOrders.
	calls []placedTrailingCall
}

type placedTrailingCall struct {
	symbol          string
	positionSide    string
	activationPrice float64
	callbackRate    float64
	quantity        float64
	reasonTag       string
}

func (f *fakeVenueTrader) GetBalance() (map[string]interface{}, error) { return nil, nil }

func (f *fakeVenueTrader) GetPositions() ([]map[string]interface{}, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.position == nil {
		return nil, nil
	}
	return []map[string]interface{}{f.position}, nil
}

func (f *fakeVenueTrader) GetOpenOrders(symbol string) ([]tradertypes.OpenOrder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]tradertypes.OpenOrder(nil), f.orders...), nil
}

func (f *fakeVenueTrader) SetTrailingStopLoss(symbol string, positionSide string, activationPrice, callbackRate, quantity float64) error {
	_, err := f.SetTrailingStopLossTaggedWithID(symbol, positionSide, activationPrice, callbackRate, quantity, "")
	return err
}

func (f *fakeVenueTrader) SetTrailingStopLossTagged(symbol string, positionSide string, activationPrice, callbackRate, quantity float64, reasonTag string) error {
	_, err := f.SetTrailingStopLossTaggedWithID(symbol, positionSide, activationPrice, callbackRate, quantity, reasonTag)
	return err
}

// SetTrailingStopLossTaggedWithID places the order and appends it in the venue's
// own reporting shape — the whole point of this fake.
func (f *fakeVenueTrader) SetTrailingStopLossTaggedWithID(symbol string, positionSide string, activationPrice, callbackRate, quantity float64, reasonTag string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.placed++
	id := fmt.Sprintf("%s-algo-%d", f.venue, f.placed)
	f.calls = append(f.calls, placedTrailingCall{
		symbol: symbol, positionSide: positionSide, activationPrice: activationPrice,
		callbackRate: callbackRate, quantity: quantity, reasonTag: reasonTag,
	})
	f.orders = append(f.orders, f.reportShapeLocked(id, symbol, positionSide, activationPrice, callbackRate, quantity))
	return id, nil
}

func (f *fakeVenueTrader) trailingCalls() []placedTrailingCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]placedTrailingCall(nil), f.calls...)
}

// reportShapeLocked builds the OpenOrder exactly as each adapter would return it
// from GetOpenOrders. Caller holds f.mu.
func (f *fakeVenueTrader) reportShapeLocked(id, symbol, positionSide string, activationPrice, callbackRate, quantity float64) tradertypes.OpenOrder {
	oo := tradertypes.OpenOrder{
		OrderID:      id,
		Symbol:       symbol,
		PositionSide: positionSide,
		Type:         "TRAILING_STOP_MARKET",
		Quantity:     quantity,
		Status:       "NEW",
	}
	if f.venue == "binance" {
		// No callbackRate, no activatePrice. triggerPrice = live moving stop,
		// substituted into both StopPrice and ActivationPrice; always "activated".
		trigger := f.markPrice * (1 - callbackRate)
		oo.CallbackRate = 0
		oo.StopPrice = trigger
		oo.ActivationPrice = trigger
		oo.ActivationStatus = "activated"
		return oo
	}
	// OKX: faithful anchor + callback reporting (trader/okx/trader_orders.go).
	oo.CallbackRate = callbackRate
	oo.StopPrice = activationPrice
	oo.ActivationPrice = activationPrice
	oo.ActivationStatus = "pending_activation"
	if activationPrice <= 0 {
		oo.StopPrice = 0
		oo.ActivationStatus = "activated"
	}
	return oo
}

func (f *fakeVenueTrader) CancelAlgoOrderByID(symbol string, algoID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := make([]tradertypes.OpenOrder, 0, len(f.orders))
	for _, o := range f.orders {
		if o.OrderID == algoID {
			continue
		}
		kept = append(kept, o)
	}
	f.orders = kept
	return nil
}

// CancelTrailingStopOrdersByIDs must exist for the fake to satisfy the anonymous
// interface the OKX full-tier arm path asserts on (auto_trader_risk.go): without it
// the arm silently falls back to the untagged branch, which discards the order id —
// making the fake, not the code, the reason a test sees an empty ExchangeOrderID.
func (f *fakeVenueTrader) CancelTrailingStopOrdersByIDs(symbol string, orderIDs []string) error {
	for _, id := range orderIDs {
		if err := f.CancelAlgoOrderByID(symbol, id); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeVenueTrader) CancelTrailingStopOrders(symbol string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := make([]tradertypes.OpenOrder, 0, len(f.orders))
	for _, o := range f.orders {
		if strings.Contains(strings.ToUpper(o.Type), "TRAILING") {
			continue
		}
		kept = append(kept, o)
	}
	f.orders = kept
	return nil
}

func (f *fakeVenueTrader) trailingCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, o := range f.orders {
		if strings.Contains(strings.ToUpper(o.Type), "TRAILING") {
			n++
		}
	}
	return n
}

// Unused Trader surface.
func (f *fakeVenueTrader) OpenLong(string, float64, int) (map[string]interface{}, error) {
	return nil, nil
}
func (f *fakeVenueTrader) OpenShort(string, float64, int) (map[string]interface{}, error) {
	return nil, nil
}
func (f *fakeVenueTrader) CloseLong(string, float64) (map[string]interface{}, error) {
	return nil, nil
}
func (f *fakeVenueTrader) CloseShort(string, float64) (map[string]interface{}, error) {
	return nil, nil
}
func (f *fakeVenueTrader) SetStopLoss(string, string, float64, float64) error   { return nil }
func (f *fakeVenueTrader) SetTakeProfit(string, string, float64, float64) error { return nil }
func (f *fakeVenueTrader) CancelStopLossOrders(string) error                    { return nil }
func (f *fakeVenueTrader) CancelTakeProfitOrders(string) error                  { return nil }
func (f *fakeVenueTrader) CancelOrder(string, string) error                     { return nil }
func (f *fakeVenueTrader) CancelAllOrders(string) error                         { return nil }
func (f *fakeVenueTrader) CancelStopOrders(string) error                        { return nil }
func (f *fakeVenueTrader) SetLeverage(string, int) error                        { return nil }
func (f *fakeVenueTrader) SetMarginMode(string, bool) error                     { return nil }
func (f *fakeVenueTrader) GetMarketPrice(string) (float64, error)               { return 0, nil }
func (f *fakeVenueTrader) FormatQuantity(_ string, q float64) (string, error) {
	return fmt.Sprintf("%.8f", q), nil
}
func (f *fakeVenueTrader) GetOrderStatus(string, string) (map[string]interface{}, error) {
	return nil, nil
}
func (f *fakeVenueTrader) GetClosedPnL(time.Time, int) ([]tradertypes.ClosedPnLRecord, error) {
	return nil, nil
}

func newVenueAutoTrader(t *testing.T, venue string, fake *fakeVenueTrader) *AutoTrader {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "venue-"+venue+".db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	return &AutoTrader{
		id:                    "trader-" + venue,
		exchangeID:            "exchange-" + venue,
		store:                 st,
		exchange:              venue,
		trader:                fake,
		config:                AutoTraderConfig{StrategyConfig: &store.StrategyConfig{}},
		protectionState:       make(map[string]string),
		nativeTrailingArmTime: make(map[string]time.Time),
	}
}

// TestPartialTierMatchesAcrossVenues is the regression test for the 2026-07-27
// production leak: 174 identical HYPEUSDT partial trailing orders on Binance.
//
// It arms a partial tier once, then polls the arm gate repeatedly with the venue
// reporting the order in its OWN shape. A correct system arms exactly once; the
// pre-fix code re-armed on every poll on Binance because:
//
//  1. the stored-orderID path was empty (the Binance partial arm discarded the ID),
//     and
//  2. the fuzzy fallback could never match, because callbackOK compared Binance's
//     absent (0) callback against the planned 0.0074, and activationOK compared
//     the moving stop (~58.5) against the planned anchor (60.598).
//
// Both venues must converge on one order.
func TestPartialTierMatchesAcrossVenues(t *testing.T) {
	const (
		symbol = "HYPEUSDT"
		side   = "long"
		entry  = 59.141964
		posQty = 5.28
		mark   = 58.98 // below the planned activation: the order rests unactivated
	)
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 2.4616, MaxDrawdownPct: 0.7385, CloseRatioPct: 30}

	for _, venue := range []string{"okx", "binance"} {
		t.Run(venue, func(t *testing.T) {
			fake := &fakeVenueTrader{
				venue:     venue,
				markPrice: mark,
				position: map[string]interface{}{
					"symbol": symbol, "side": side,
					"entryPrice": entry, "markPrice": mark, "positionAmt": posQty,
				},
			}
			at := newVenueAutoTrader(t, venue, fake)

			if ok := at.applyNativeTrailingDrawdown(symbol, side, entry, mark, rule); !ok {
				t.Fatal("initial partial arm must succeed")
			}
			if got := fake.trailingCount(); got != 1 {
				t.Fatalf("expected exactly 1 trailing order after first arm, got %d", got)
			}

			// Poll the gate as the monitor loop does. None of these may re-select
			// the tier: the order is resting and must be recognised as such.
			for poll := 0; poll < 20; poll++ {
				rules := at.getDrawdownArmRulesForSelectedRule(entry, posQty, symbol, side, rule)
				if len(rules) != 0 {
					t.Fatalf("poll %d re-selected an already-armed tier (%d rules) — this is the leak: "+
						"on %s the live order was not recognised, so every poll placed another order",
						poll, len(rules), venue)
				}
			}
			if got := fake.trailingCount(); got != 1 {
				t.Fatalf("%s: expected still 1 trailing order after 20 polls, got %d", venue, got)
			}
		})
	}
}

// TestPartialTierFuzzyMatchSurvivesRecordLoss covers the harder case: the tier is
// resting on the exchange but the persisted record is GONE (record never written,
// GC'd, or the process restarted before the write). The stored-orderID path is
// then unavailable and the fuzzy fallback is the only line of defence.
//
// Before the fix the fallback was structurally dead on Binance, so a record loss
// meant unbounded re-arming. Now it must recognise the order on both venues.
func TestPartialTierFuzzyMatchSurvivesRecordLoss(t *testing.T) {
	const (
		symbol = "HYPEUSDT"
		side   = "long"
		entry  = 59.141964
		posQty = 5.28
		mark   = 58.98
	)
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 2.4616, MaxDrawdownPct: 0.7385, CloseRatioPct: 30}

	for _, venue := range []string{"okx", "binance"} {
		t.Run(venue, func(t *testing.T) {
			fake := &fakeVenueTrader{
				venue:     venue,
				markPrice: mark,
				position: map[string]interface{}{
					"symbol": symbol, "side": side,
					"entryPrice": entry, "markPrice": mark, "positionAmt": posQty,
				},
			}
			at := newVenueAutoTrader(t, venue, fake)
			if ok := at.applyNativeTrailingDrawdown(symbol, side, entry, mark, rule); !ok {
				t.Fatal("initial partial arm must succeed")
			}

			// Simulate total record loss: wipe protection state AND the arm-time
			// cooldown, leaving only the live exchange order.
			fp := stableDrawdownRuleFingerprint(entry, rule)
			if err := at.store.DeleteDynamicProtectionRecordsForInactive(at.id, map[string]struct{}{}); err != nil {
				t.Fatalf("wipe protection state: %v", err)
			}
			delete(at.nativeTrailingArmTime, fp)
			if recs := at.getArmedDrawdownRecordsForPosition(symbol, side, entry, posQty, 0); len(recs) != 0 {
				t.Fatalf("precondition: records must be gone, got %d", len(recs))
			}

			found, _, _, _ := at.findEquivalentPartialTrailingOrder(symbol, side, rule, entry, mustOpenOrders(t, fake, symbol))
			if found == nil {
				t.Fatalf("%s: fuzzy fallback must find the resting partial order when no record exists — "+
					"otherwise a record loss re-arms forever (Binance: callback absent, StopPrice is the moving stop)", venue)
			}
			if got := fake.trailingCount(); got != 1 {
				t.Fatalf("%s: matching must not place orders, got %d", venue, got)
			}
		})
	}
}

func mustOpenOrders(t *testing.T, fake *fakeVenueTrader, symbol string) []OpenOrder {
	t.Helper()
	orders, err := fake.GetOpenOrders(symbol)
	if err != nil {
		t.Fatalf("get open orders: %v", err)
	}
	return orders
}

// TestArmCooldownBoundsLeakWithoutRecord pins the structural backstop.
//
// Every trailing leak of 2026-07 (four independent instances: OKX dd1 hitting the
// 55-order cap as 51299, the OKX untagged fallback, and the Binance/Bitget partial
// branches) shared one amplifier: the 300s re-arm cooldown was checked INSIDE the
// `armedFingerprints[fingerprint]` branch, and armedFingerprints is built only from
// records whose ProtectionType passes isDynamicNativeProtectionType. So an arm path
// that placed an order but failed to persist a native record could not reach the
// cooldown at all, and re-armed on EVERY poll with no upper bound.
//
// The cooldown is now consulted before that branch, so a missing record degrades to
// one re-arm per 300s instead of one per poll. This test drives exactly that state:
// a live order plus an arm timestamp, but no record, and asserts the gate stays shut.
func TestArmCooldownBoundsLeakWithoutRecord(t *testing.T) {
	const (
		symbol = "HYPEUSDT"
		side   = "long"
		entry  = 59.141964
		posQty = 5.28
		mark   = 58.98
	)
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 2.4616, MaxDrawdownPct: 0.7385, CloseRatioPct: 30}
	fp := stableDrawdownRuleFingerprint(entry, rule)

	for _, venue := range []string{"okx", "binance"} {
		t.Run(venue, func(t *testing.T) {
			fake := &fakeVenueTrader{
				venue:     venue,
				markPrice: mark,
				position: map[string]interface{}{
					"symbol": symbol, "side": side,
					"entryPrice": entry, "markPrice": mark, "positionAmt": posQty,
				},
			}
			at := newVenueAutoTrader(t, venue, fake)
			if ok := at.applyNativeTrailingDrawdown(symbol, side, entry, mark, rule); !ok {
				t.Fatal("initial partial arm must succeed")
			}

			// Records gone, arm timestamp kept — the exact state a missing persist
			// leaves behind. The arm timestamp is the only surviving evidence.
			if err := at.store.DeleteDynamicProtectionRecordsForInactive(at.id, map[string]struct{}{}); err != nil {
				t.Fatalf("wipe protection state: %v", err)
			}
			if _, ok := at.nativeTrailingArmTime[fp]; !ok {
				t.Fatal("precondition: arm timestamp must be present")
			}

			for poll := 0; poll < 20; poll++ {
				if rules := at.getDrawdownArmRulesForSelectedRule(entry, posQty, symbol, side, rule); len(rules) != 0 {
					t.Fatalf("poll %d re-armed with no record present — the cooldown backstop is unreachable, "+
						"which is what made the production leak unbounded (174 orders)", poll)
				}
			}
			if got := fake.trailingCount(); got != 1 {
				t.Fatalf("%s: expected 1 trailing order, got %d", venue, got)
			}
		})
	}
}

// TestFullAndPartialTiersCoexistPerVenue guards the sibling-tier invariant under
// place-at-open: dd1 (full close) and a partial tier rest concurrently as separate
// exchange orders, and arming/matching one must never disturb the other. This is
// the v1.16.5 collapse regression (a full-tier migration cancelled every trailing
// order on the side, including a partial armed seconds earlier), checked per venue
// because the sibling orders are told apart by their persisted orderIDs — and on
// Binance qty/callback/activation are all too weak to distinguish them.
func TestFullAndPartialTiersCoexistPerVenue(t *testing.T) {
	const (
		symbol = "HYPEUSDT"
		side   = "long"
		entry  = 59.141964
		posQty = 5.28
		mark   = 58.98
	)
	partial := store.DrawdownTakeProfitRule{MinProfitPct: 2.4616, MaxDrawdownPct: 0.7385, CloseRatioPct: 30}
	full := store.DrawdownTakeProfitRule{MinProfitPct: 1.8462, MaxDrawdownPct: 1.1077, CloseRatioPct: 100}

	for _, venue := range []string{"okx", "binance"} {
		t.Run(venue, func(t *testing.T) {
			fake := &fakeVenueTrader{
				venue:     venue,
				markPrice: mark,
				position: map[string]interface{}{
					"symbol": symbol, "side": side,
					"entryPrice": entry, "markPrice": mark, "positionAmt": posQty,
				},
			}
			at := newVenueAutoTrader(t, venue, fake)

			if ok := at.applyNativeTrailingDrawdown(symbol, side, entry, mark, full); !ok {
				t.Fatal("full tier arm must succeed")
			}
			if ok := at.applyNativeTrailingDrawdown(symbol, side, entry, mark, partial); !ok {
				t.Fatal("partial tier arm must succeed")
			}
			if got := fake.trailingCount(); got != 2 {
				t.Fatalf("%s: both tiers must rest concurrently, got %d trailing orders", venue, got)
			}

			// Each tier resolves to its OWN order id, and neither is re-selected.
			fullID := at.storedTrailingOrderIDForRule(symbol, side, entry, full)
			partialID := at.storedTrailingOrderIDForRule(symbol, side, entry, partial)
			if fullID == "" || partialID == "" {
				t.Fatalf("%s: both tiers must persist an order id (full=%q partial=%q)", venue, fullID, partialID)
			}
			if fullID == partialID {
				t.Fatalf("%s: sibling tiers must not share an order id (%q)", venue, fullID)
			}
			for poll := 0; poll < 10; poll++ {
				if rules := at.getDrawdownArmRulesForSelectedRule(entry, posQty, symbol, side, partial); len(rules) != 0 {
					t.Fatalf("%s poll %d: partial tier re-selected while resting", venue, poll)
				}
				if rules := at.getDrawdownArmRulesForSelectedRule(entry, posQty, symbol, side, full); len(rules) != 0 {
					t.Fatalf("%s poll %d: full tier re-selected while resting", venue, poll)
				}
			}
			if got := fake.trailingCount(); got != 2 {
				t.Fatalf("%s: expected still 2 trailing orders after polling, got %d", venue, got)
			}
		})
	}
}
