package trader

import (
	"path/filepath"
	"testing"
	"time"

	"nofx/store"
)

// TestExchangeFailedStateIsNotAOneWayLatch pins that the exchange-failed panel state
// is cleared once exchange coverage is confirmed again.
//
// Regression: applyExchangeFailedLocalMonitor upgraded the state to
// *_exchange_failed_armed and NOTHING ever wrote it back. drawdown_order_reclaim.go
// rewrites the ownership ledger to match the exchange but contains zero
// setProtectionState calls, so the panel kept rendering
// "交易所挂单失败·本地保护(无交易所单)" while the reconciler, in the SAME poll and
// over the SAME orders, logged "state=protected verified=true ... coverage is complete".
// Production 2026-08-02 ETHUSDT short: one transient event produced ~410 consecutive
// false panel reports while algos 3791832803965304832 and 3794662695304069120 were
// live on OKX the whole time.
func TestExchangeFailedStateIsNotAOneWayLatch(t *testing.T) {
	cases := []struct {
		name    string
		tripped string
		want    string
	}{
		{"full tier", "managed_drawdown_exchange_failed_armed", "managed_drawdown_armed"},
		{"partial tier", "managed_partial_drawdown_exchange_failed_armed", "managed_partial_drawdown_armed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			at := &AutoTrader{protectionState: make(map[string]string)}
			at.setProtectionState("ETHUSDT", "short", tc.tripped)

			at.clearExchangeFailedProtectionState("ETHUSDT", "short")

			got := at.getProtectionState("ETHUSDT", "short")
			if got != tc.want {
				t.Fatalf("state = %q, want %q", got, tc.want)
			}
			// The managed monitor is still armed and still co-running; only the claim
			// "the exchange has no order" was disproven. Losing that memory would make
			// the arm loop re-arm every poll (protection_reconciler.go:98 preserve-list).
			if !isManagedDrawdownProtectionState(got) {
				t.Fatalf("state %q no longer reads as managed-armed; the co-running monitor's arm memory was erased", got)
			}
			// And the panel must stop reporting an exchange failure.
			if mode := at.getDrawdownExecutionMode("ETHUSDT", "short"); mode == "managed_drawdown_exchange_failed" {
				t.Fatalf("execution mode still %q — panel would keep claiming there is no exchange order", mode)
			}
		})
	}
}

// TestClearExchangeFailedLeavesOtherStatesAlone pins that the demotion is scoped to
// the two failure states. Rewriting any other state here would silently reclassify a
// position's protection ownership.
func TestClearExchangeFailedLeavesOtherStatesAlone(t *testing.T) {
	for _, state := range []string{
		"native_trailing_armed",
		"native_partial_trailing_armed",
		"managed_drawdown_armed",
		"managed_partial_drawdown_armed",
		"native_trailing_arming",
		"exchange_protection_verified",
		"",
	} {
		at := &AutoTrader{protectionState: make(map[string]string)}
		at.setProtectionState("ETHUSDT", "short", state)
		at.clearExchangeFailedProtectionState("ETHUSDT", "short")
		if got := at.getProtectionState("ETHUSDT", "short"); got != state {
			t.Fatalf("state %q was rewritten to %q", state, got)
		}
	}
}

// TestTrippedBreakerStillClearsStalePanelWarning pins that the panel stops claiming
// "no exchange order" as soon as a live effective order is observed — even while the
// re-arm breaker is still tripped.
//
// The breaker holds a tier for reArmBreakerCooldown (30min) and, while tripped, the arm
// loop deliberately skips the coverage query. So the demotion that sits behind the
// coverage check could not run, and a panel warning could outlive its cause by half an
// hour. Execution ownership must NOT change here: the managed monitor still owns the
// tier until the breaker cools down. Only the display claim is corrected.
func TestTrippedBreakerStillClearsStalePanelWarning(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	// The order OKX actually had live throughout: activated, carrying its moving trigger.
	const liveOID = "3791832803965304832"
	openOrders := []OpenOrder{{
		OrderID: liveOID, Symbol: "ETHUSDT", PositionSide: "SHORT",
		Type: "TRAILING_STOP_MARKET", Quantity: 0.156,
		ActivationPrice: 1833.77, StopPrice: 1841.36,
		ActivationStatus: "activated", CallbackRate: 0.010917, Status: "NEW",
	}}
	at := &AutoTrader{
		id: "trader-panel-truth", exchangeID: "exch-panel-truth", store: st, exchange: "okx",
		// ddLightMultiTierTrader implements the full Trader surface; only GetPositions
		// and GetOpenOrders are reached by this path.
		trader:          &ddLightMultiTierTrader{orders: openOrders},
		protectionState: make(map[string]string),
		reArmFailCache:  make(map[string]int),
		reArmTripTime:   make(map[string]time.Time),
	}
	rule := normalizeDrawdownRule(store.DrawdownTakeProfitRule{
		MinProfitPct: 1.8195, MaxDrawdownPct: 1.0917, CloseRatioPct: 100, StageName: "dd1",
	})
	entryPrice := 1867.75
	// Mark is past activation — exactly the geometry that used to be judged phantom.
	markPrice := 1827.42

	// Armed record carrying the live order id, so the tier is matched authoritatively
	// (that is how a real position tracks its own trailing order).
	rec := store.DynamicProtectionRecord{
		TraderID: at.id, ExchangeID: at.exchangeID, Symbol: "ETHUSDT", Side: "short",
		PositionFingerprint: positionFingerprint(entryPrice, 0.156),
		ProtectionType:      "native_trailing",
		RuleFingerprint:     stableDrawdownRuleFingerprint(entryPrice, rule),
		CloseRatioPct:       rule.CloseRatioPct, Status: "armed",
		ExchangeOrderID: liveOID, ActivationPrice: 1833.77, CallbackRatio: 0.010917,
		UpdatedAt: time.Now().UnixMilli(),
	}
	rec.Key = store.BuildDynamicProtectionKey(rec.TraderID, rec.ExchangeID, rec.Symbol, rec.Side,
		rec.PositionFingerprint, rec.ProtectionType, rec.RuleFingerprint, rec.CloseRatioPct)
	if err := st.SaveDynamicProtectionRecord(rec); err != nil {
		t.Fatalf("seed armed record: %v", err)
	}

	if !at.exchangeHasEffectiveTrailingForTier("ETHUSDT", "short", rule, entryPrice, markPrice, openOrders) {
		t.Fatal("a live activated trailing order was not recognised as effective")
	}

	// Trip the breaker for this tier, as production did at 02:45:45.
	key := reArmFailKey("ETHUSDT", "short", rule, entryPrice)
	for i := 0; i < reArmBreakerLimit; i++ {
		at.bumpReArmFail(key)
	}
	at.setProtectionState("ETHUSDT", "short", "managed_drawdown_exchange_failed_armed")

	covered := at.accountReArmBreaker("ETHUSDT", "short", entryPrice, markPrice,
		calculatePositionPnLPct("short", entryPrice, markPrice), []store.DrawdownTakeProfitRule{rule})

	// Execution ownership unchanged: a tripped tier is still reported uncovered.
	if covered {
		t.Fatal("tripped tier reported as covered — the managed monitor would stop executing it")
	}
	// But the panel claim is corrected.
	if got := at.getProtectionState("ETHUSDT", "short"); got != "managed_drawdown_armed" {
		t.Fatalf("state = %q, want managed_drawdown_armed — panel would keep claiming no exchange order exists", got)
	}
	if at.getReArmFail(key) < reArmBreakerLimit {
		t.Fatal("breaker was reset as a side effect; the display fix must not hand execution back early")
	}
}
