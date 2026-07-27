package trader

import (
	"math"
	"strings"
	"testing"

	"nofx/kernel"
	"nofx/store"
)

// TestPostOpenDrawdownATRResolution pins the place-at-open half of the ATR-unit
// contract. Three call sites resolve ATR-unit drawdown rules to an effective
// percent before arming — the runtime monitor (auto_trader_risk.go:204), the
// decision path (auto_trader_decision.go:1223) and the reconciler
// (protection_reconciler.go:476, added by 26104e6 for exactly this bug class) —
// but applyNativeProtectionTargetsAfterOpen, introduced with place-at-open in
// a56f741 (v1.16.1), iterates prot.DrawdownTakeProfit.Rules RAW and hands them
// straight to applyNativeTrailingDrawdown.
//
// Consequence observed in production on 2026-07-27 across BOTH venues (BN
// Binance SOL/ETH, GPT + claude OKX KAITO/SPCX/WLD): a strategy declaring
// `max_drawdown_pct: 1.8, max_drawdown_unit: atr` armed at open with
// callbackRate 1.8% — the raw ATR multiple read as a percent — while the runtime
// path armed the SAME tier at the ATR-derived percent (SOL: 1.8 ATR = 0.911%).
// The two carry different rule fingerprints, so both records stay "armed" and
// both orders rest on the exchange: the tier count exceeds the configured tier
// count and the effective drawdown band is whichever of the two is wider.
//
// The error does not have a fixed sign — it is (raw multiple) vs (multiple × ATR
// / entry), so it is far too WIDE on low-volatility symbols (SOL: 1.8% vs
// 0.911%) and far too TIGHT on high-volatility ones (KAITO: 1.2% vs 3.44%).
// Neither direction is the configured strategy.
//
// The assertion is on the callbackRate the exchange actually receives, not on an
// intermediate value, so it holds regardless of how the resolution is wired in.
func TestPostOpenDrawdownATRResolution(t *testing.T) {
	const (
		symbol   = "SOLUSDT"
		side     = "long"
		venue    = "binance"
		entry    = 76.28
		posQty   = 4.41
		atrValue = 0.386065 // ATR(1h) logged for the live SOLUSDT position
	)

	// 1 ATR = 0.386065 / 76.28 = 0.50611% of entry.
	// dd1  : MinProfit 3 ATR -> 1.51832%, MaxDrawdown 1.8 ATR -> 0.91099%.
	const wantFullCallback = 1.8 * atrValue / entry // 0.0091099 as a ratio

	tf := store.ATRProtectionConfig{}.WithDefaults().Timeframe
	key := frozenATRKey("trader-"+venue, symbol, tf, strings.ToUpper(side))
	frozenATRMu.Lock()
	frozenATRCache[key] = frozenATREntry{entryPrice: entry, atr: atrValue}
	frozenATRMu.Unlock()
	t.Cleanup(func() {
		frozenATRMu.Lock()
		delete(frozenATRCache, key)
		frozenATRMu.Unlock()
	})

	fake := &fakeVenueTrader{
		venue:     venue,
		markPrice: entry,
		position: map[string]interface{}{
			"symbol": symbol, "side": side,
			"positionAmt": posQty, "markPrice": entry, "entryPrice": entry,
		},
	}
	at := newVenueAutoTrader(t, venue, fake)
	at.config.StrategyConfig.ATRProtection = store.ATRProtectionConfig{Enabled: true}
	at.config.StrategyConfig.Protection = store.ProtectionConfig{
		DrawdownTakeProfit: store.DrawdownTakeProfitConfig{
			Enabled: true,
			Mode:    store.ProtectionModeManual,
			// Mirrors the live BN / claude-ct30 strategy: two tiers, ATR units.
			Rules: []store.DrawdownTakeProfitRule{
				{
					MinProfitPct: 3, MinProfitUnit: store.ProtectionUnitATR,
					MaxDrawdownPct: 1.8, MaxDrawdownUnit: store.ProtectionUnitATR,
					CloseRatioPct: 100, StageName: "dd1",
				},
			},
		},
	}

	req := &protectionExecutionRequest{
		Symbol:       symbol,
		Action:       "open_long",
		PositionSide: strings.ToUpper(side),
		Quantity:     posQty,
		EntryPrice:   entry,
		Decision:     &kernel.Decision{Symbol: symbol, Action: "open_long"},
	}
	if err := at.applyNativeProtectionTargetsAfterOpen(req, nil); err != nil {
		t.Fatalf("applyNativeProtectionTargetsAfterOpen: %v", err)
	}

	placed := fake.trailingCalls()
	if len(placed) == 0 {
		t.Fatalf("place-at-open armed no trailing order for the configured ATR tier")
	}
	if len(placed) > 1 {
		t.Fatalf("place-at-open armed %d trailing orders for ONE configured tier: %+v", len(placed), placed)
	}

	got := placed[0].callbackRate
	if math.Abs(got-1.8/100) < 1e-9 {
		t.Fatalf("callbackRate %.8f is the RAW ATR multiple 1.8 read as 1.8%%; want the ATR-resolved %.8f "+
			"(1.8 ATR = %.4f%% of entry). place-at-open is missing resolveDrawdownRulesATR, so it arms a "+
			"second tier that the runtime path never matches.", got, wantFullCallback, wantFullCallback*100)
	}
	if math.Abs(got-wantFullCallback) > 1e-6 {
		t.Fatalf("callbackRate: want %.8f (1.8 ATR of entry %.2f), got %.8f", wantFullCallback, entry, got)
	}
}

// TestPostOpenAndRuntimeConvergeOnOneTier is the end-to-end statement of the same
// defect, and the one that matches what the user actually saw on the exchange: a
// strategy with TWO configured tiers showing THREE resting trailing orders.
//
// It arms via place-at-open, then re-polls the way the runtime drawdown monitor
// does (auto_trader_risk.go resolves the rules with resolveDrawdownRulesATR before
// arming). If the two paths disagree on the effective percent they also disagree on
// stableDrawdownRuleFingerprint, so the runtime path finds no stored order for "its"
// rule and places a second one. Both venues must end with exactly one order per
// configured tier.
func TestPostOpenAndRuntimeConvergeOnOneTier(t *testing.T) {
	const (
		symbol   = "WLDUSDT"
		side     = "long"
		entry    = 0.3562
		posQty   = 280.0
		atrValue = 0.0049866 // 1 ATR = 1.4000% of entry
	)

	// claude-ct30's live shape: 2 ATR-unit tiers (dd1 full close + one partial).
	rules := []store.DrawdownTakeProfitRule{
		{
			MinProfitPct: 3, MinProfitUnit: store.ProtectionUnitATR,
			MaxDrawdownPct: 1.8, MaxDrawdownUnit: store.ProtectionUnitATR,
			CloseRatioPct: 100, StageName: "dd1",
		},
		{
			MinProfitPct: 4, MinProfitUnit: store.ProtectionUnitATR,
			MaxDrawdownPct: 1.2, MaxDrawdownUnit: store.ProtectionUnitATR,
			CloseRatioPct: 30, StageName: "dd2",
		},
	}

	for _, venue := range []string{"okx", "binance"} {
		t.Run(venue, func(t *testing.T) {
			fake := &fakeVenueTrader{
				venue:     venue,
				markPrice: entry,
				position: map[string]interface{}{
					"symbol": symbol, "side": side,
					"positionAmt": posQty, "markPrice": entry, "entryPrice": entry,
				},
			}
			at := newVenueAutoTrader(t, venue, fake)

			key := frozenATRKey(at.id, symbol, store.ATRProtectionConfig{}.WithDefaults().Timeframe, strings.ToUpper(side))
			frozenATRMu.Lock()
			frozenATRCache[key] = frozenATREntry{entryPrice: entry, atr: atrValue}
			frozenATRMu.Unlock()
			t.Cleanup(func() {
				frozenATRMu.Lock()
				delete(frozenATRCache, key)
				frozenATRMu.Unlock()
			})

			at.config.StrategyConfig.ATRProtection = store.ATRProtectionConfig{Enabled: true}
			at.config.StrategyConfig.Protection = store.ProtectionConfig{
				DrawdownTakeProfit: store.DrawdownTakeProfitConfig{
					Enabled: true, Mode: store.ProtectionModeManual, Rules: rules,
				},
			}

			req := &protectionExecutionRequest{
				Symbol:       symbol,
				Action:       "open_long",
				PositionSide: strings.ToUpper(side),
				Quantity:     posQty,
				EntryPrice:   entry,
				Decision:     &kernel.Decision{Symbol: symbol, Action: "open_long"},
			}
			if err := at.applyNativeProtectionTargetsAfterOpen(req, nil); err != nil {
				t.Fatalf("place-at-open: %v", err)
			}
			afterOpen := fake.trailingCount()
			if afterOpen != len(rules) {
				t.Fatalf("place-at-open: want %d trailing orders (one per configured tier), got %d", len(rules), afterOpen)
			}

			// Runtime monitor path: same rules, resolved the way auto_trader_risk.go does.
			runtimeRules := at.resolveDrawdownRulesATR(rules, symbol, side, entry)
			for _, rule := range runtimeRules {
				at.applyNativeTrailingDrawdown(symbol, side, entry, entry, rule)
			}

			if got := fake.trailingCount(); got != len(rules) {
				t.Fatalf("after runtime re-poll: want %d trailing orders, got %d — the two arm paths "+
					"disagree on the effective percent and each armed its own tier (this is the "+
					"3-orders-for-2-configured-tiers report)", len(rules), got)
			}

			// Sizing, not just count. The 30% tier must be sized at 30% of the position.
			// The unresolved duplicate was armed for the FULL quantity (live ETHUSDT log
			// 2026-07-27 10:24:46: "close=100.0%(cumul) qty=0.1010" on a 0.101 position),
			// because getCumulativeCloseRatioByRule matches tiers on MinProfitPct and the
			// raw multiple (4.0) matched no resolved tier — so its fallback took the highest
			// tier index and summed 100+30 → clamped to 100.
			var partialQty float64
			for _, c := range fake.trailingCalls() {
				if c.quantity > 0 && c.quantity < posQty*0.99 {
					partialQty = c.quantity
				}
			}
			if partialQty <= 0 {
				t.Fatalf("no partial-sized trailing order was placed; calls=%+v", fake.trailingCalls())
			}
			if want := posQty * 0.30; math.Abs(partialQty-want) > want*0.02 {
				t.Fatalf("partial tier quantity: want ~%.4f (30%% of %.2f), got %.4f", want, posQty, partialQty)
			}
		})
	}
}

// TestPostOpenEntryDriftDoesNotSplitTiers covers the SECOND divergence found in the
// same production snapshot, which the ATR fix alone does not address.
//
// place-at-open arms with the FILL price from the order response; the runtime monitor
// arms with the exchange's synced average entry price, and the two differ slightly
// (live 2026-07-27: BN SOLUSDT record entry 76.36 vs position entry 76.28; BN ETHUSDT
// 1944.01 vs 1943.65). Entry price is the first field of stableDrawdownRuleFingerprint
// AND the divisor in the ATR→percent conversion, so a drifted entry yields a different
// fingerprint even when the strategy and the ATR are identical — storedTrailingOrderIDForRule
// finds nothing and the tier is a candidate for a second arm.
//
// This test pins whether the fuzzy matchers absorb that drift. If they do not, the tier
// count exceeds the configured count for a second, independent reason.
func TestPostOpenEntryDriftDoesNotSplitTiers(t *testing.T) {
	const (
		symbol    = "SOLUSDT"
		side      = "long"
		fillEntry = 76.36 // what place-at-open sees
		syncEntry = 76.28 // what the runtime monitor sees moments later
		posQty    = 4.41
		atrValue  = 0.386065
	)
	rules := []store.DrawdownTakeProfitRule{
		{
			MinProfitPct: 3, MinProfitUnit: store.ProtectionUnitATR,
			MaxDrawdownPct: 1.8, MaxDrawdownUnit: store.ProtectionUnitATR,
			CloseRatioPct: 100, StageName: "dd1",
		},
		{
			MinProfitPct: 4, MinProfitUnit: store.ProtectionUnitATR,
			MaxDrawdownPct: 1.2, MaxDrawdownUnit: store.ProtectionUnitATR,
			CloseRatioPct: 30, StageName: "dd2",
		},
	}

	for _, venue := range []string{"okx", "binance"} {
		t.Run(venue, func(t *testing.T) {
			fake := &fakeVenueTrader{
				venue:     venue,
				markPrice: syncEntry,
				position: map[string]interface{}{
					"symbol": symbol, "side": side,
					"positionAmt": posQty, "markPrice": syncEntry, "entryPrice": syncEntry,
				},
			}
			at := newVenueAutoTrader(t, venue, fake)

			// One frozen ATR entry covers both prices: entrySamePosition tolerance is
			// 0.05%, and |76.36-76.28|/76.28 = 0.105% — so it does NOT. Register both,
			// mirroring production where the freeze happens once per position.
			tf := store.ATRProtectionConfig{}.WithDefaults().Timeframe
			for _, e := range []float64{fillEntry, syncEntry} {
				k := frozenATRKey(at.id, symbol, tf, strings.ToUpper(side))
				frozenATRMu.Lock()
				frozenATRCache[k] = frozenATREntry{entryPrice: e, atr: atrValue}
				frozenATRMu.Unlock()
			}
			t.Cleanup(func() {
				frozenATRMu.Lock()
				delete(frozenATRCache, frozenATRKey(at.id, symbol, tf, strings.ToUpper(side)))
				frozenATRMu.Unlock()
			})

			at.config.StrategyConfig.ATRProtection = store.ATRProtectionConfig{Enabled: true}
			at.config.StrategyConfig.Protection = store.ProtectionConfig{
				DrawdownTakeProfit: store.DrawdownTakeProfitConfig{
					Enabled: true, Mode: store.ProtectionModeManual, Rules: rules,
				},
			}

			// EntryPrice here is what the open path passes: marketData.CurrentPrice from
			// the pre-trade analysis snapshot, NOT the fill. syncRequestEntryPriceToExchange
			// is what has to reconcile it, so drive the real entry point.
			req := &protectionExecutionRequest{
				Symbol: symbol, Action: "open_long", PositionSide: strings.ToUpper(side),
				Quantity: posQty, EntryPrice: fillEntry,
				Decision: &kernel.Decision{Symbol: symbol, Action: "open_long"},
			}
			at.syncRequestEntryPriceToExchange(req)
			if math.Abs(req.EntryPrice-syncEntry) > 1e-9 {
				t.Fatalf("entry sync: want the exchange's %.4f, got %.4f — protection is still "+
					"anchored on the pre-trade snapshot price", syncEntry, req.EntryPrice)
			}
			if err := at.applyNativeProtectionTargetsAfterOpen(req, nil); err != nil {
				t.Fatalf("place-at-open: %v", err)
			}

			// Runtime monitor, now on the SYNCED entry price.
			for _, rule := range at.resolveDrawdownRulesATR(rules, symbol, side, syncEntry) {
				at.applyNativeTrailingDrawdown(symbol, side, syncEntry, syncEntry, rule)
			}

			if got := fake.trailingCount(); got != len(rules) {
				t.Fatalf("entry drift %.2f→%.2f split the tiers: want %d trailing orders, got %d",
					fillEntry, syncEntry, len(rules), got)
			}
		})
	}
}
