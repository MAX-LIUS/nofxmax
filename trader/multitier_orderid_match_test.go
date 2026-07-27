package trader

import (
	"path/filepath"
	"testing"
	"time"

	"nofx/store"
)

// TestMultiTierTrailingMatchByOrderID reproduces the 2026-07 place-at-open
// multi-tier churn incident (HYPE/SPCX both DD tiers armed but panel RED;
// ZEC/CL only dd1; armed orders re-placed every ~300s). Root cause: under the
// place-at-open model, multiple trailing orders (dd1 + partial) rest
// concurrently per position, but the matchers fuzzy-matched by
// qty+activation+callback — which cannot tell sibling tiers apart. A partial
// tier's planned qty (30%) never matched a full-qty order, so the matcher
// judged it "missing" and the drawdown monitor re-armed every cooldown window.
//
// The fix: every tier persists its placement's exchange orderID keyed by
// RuleFingerprint; each tier matches on its OWN orderID first (collision-free),
// falling back to fuzzy matching only when no stored ID exists.
func TestMultiTierTrailingMatchByOrderID(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "multitier-orderid.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	traderID := "trader-multitier"
	symbol := "HYPEUSDT"
	side := "long"
	entry := 40.0
	qty := 100.0

	// Two concurrently-resting tiers under place-at-open:
	//   dd1     = full close (100%)
	//   partial = 30% close
	dd1 := store.DrawdownTakeProfitRule{MinProfitPct: 3, MaxDrawdownPct: 30, CloseRatioPct: 100}
	partial := store.DrawdownTakeProfitRule{MinProfitPct: 2, MaxDrawdownPct: 20, CloseRatioPct: 30}

	posFP := positionFingerprint(entry, qty)
	records := []store.DynamicProtectionRecord{
		{
			TraderID: traderID, ExchangeID: "exchange-1", Symbol: symbol, Side: side,
			PositionFingerprint: posFP, ProtectionType: "native_trailing",
			RuleFingerprint: stableDrawdownRuleFingerprint(entry, dd1), CloseRatioPct: 100,
			Status: "armed", ExchangeOrderID: "algo-dd1",
		},
		{
			TraderID: traderID, ExchangeID: "exchange-1", Symbol: symbol, Side: side,
			PositionFingerprint: posFP, ProtectionType: "native_partial_trailing",
			RuleFingerprint: stableDrawdownRuleFingerprint(entry, partial), CloseRatioPct: 30,
			Status: "armed", ExchangeOrderID: "algo-partial",
		},
	}
	for _, r := range records {
		if err := st.SaveDynamicProtectionRecord(r); err != nil {
			t.Fatalf("save record: %v", err)
		}
	}

	at := &AutoTrader{id: traderID, store: st, exchange: "okx"}

	// Both tiers' orders rest concurrently. Note the qty/activation of each order
	// deliberately do NOT line up with the OTHER tier's planned values — the whole
	// point is that ID matching does not rely on those.
	openOrders := []OpenOrder{
		{PositionSide: "LONG", Type: "TRAILING_STOP_MARKET", OrderID: "algo-dd1", Quantity: 100, StopPrice: 41.2, CallbackRate: 0.03},
		{PositionSide: "LONG", Type: "TRAILING_STOP_MARKET", OrderID: "algo-partial", Quantity: 30, StopPrice: 40.8, CallbackRate: 0.02},
	}

	t.Run("both tiers matched by their own orderID — no re-arm", func(t *testing.T) {
		if !at.hasMatchingNativeTrailingOrderForRule(symbol, side, entry, dd1, openOrders) {
			t.Fatal("dd1 must match its own order algo-dd1")
		}
		if !at.hasMatchingNativeTrailingOrderForRule(symbol, side, entry, partial, openOrders) {
			t.Fatal("partial must match its own order algo-partial")
		}
	})

	t.Run("stable across repeated polls (no churn)", func(t *testing.T) {
		for i := 0; i < 5; i++ {
			if !at.hasMatchingNativeTrailingOrderForRule(symbol, side, entry, dd1, openOrders) {
				t.Fatalf("poll %d: dd1 flapped to no-match", i)
			}
			if !at.hasMatchingNativeTrailingOrderForRule(symbol, side, entry, partial, openOrders) {
				t.Fatalf("poll %d: partial flapped to no-match", i)
			}
		}
	})

	t.Run("partial's order gone → partial re-arms, dd1 does NOT falsely bind", func(t *testing.T) {
		onlyDD1 := []OpenOrder{
			{PositionSide: "LONG", Type: "TRAILING_STOP_MARKET", OrderID: "algo-dd1", Quantity: 100, StopPrice: 41.2, CallbackRate: 0.03},
		}
		if !at.hasMatchingNativeTrailingOrderForRule(symbol, side, entry, dd1, onlyDD1) {
			t.Fatal("dd1 must still match its own order")
		}
		// The core anti-cross-binding guarantee: partial must report MISSING
		// (its stored ID algo-partial is absent), NOT falsely bind to dd1's order.
		if at.hasMatchingNativeTrailingOrderForRule(symbol, side, entry, partial, onlyDD1) {
			t.Fatal("partial must NOT match dd1's order — genuine gap should re-arm partial only")
		}
	})

	t.Run("findExistingFullTrailingOrder resolves dd1 by ID, not first trailing order", func(t *testing.T) {
		// Order book lists partial FIRST; the legacy fuzzy path returned the first
		// trailing order (partial), mis-binding dd1. ID matching must return dd1's.
		reordered := []OpenOrder{
			{PositionSide: "LONG", Type: "TRAILING_STOP_MARKET", OrderID: "algo-partial", Quantity: 30, StopPrice: 40.8, CallbackRate: 0.02},
			{PositionSide: "LONG", Type: "TRAILING_STOP_MARKET", OrderID: "algo-dd1", Quantity: 100, StopPrice: 41.2, CallbackRate: 0.03},
		}
		got := at.findExistingFullTrailingOrder(symbol, side, entry, dd1, reordered)
		if got == nil {
			t.Fatal("expected to resolve dd1's full trailing order")
		}
		if got.OrderID != "algo-dd1" {
			t.Fatalf("resolved wrong order by ID: got %q, want algo-dd1", got.OrderID)
		}
	})
}

// TestStoredTrailingOrderIDPrefersNewestRecord reproduces the 2026-07-27 production
// symptom that survived the cooldown fix: CL/HYPE/ETH still re-armed once per 300s
// cooldown window and the panel showed dd1/dd2 RED on some polls.
//
// A position can hold SEVERAL armed records with the SAME RuleFingerprint, because
// getArmedDrawdownRecordsForPosition matches position identity on ENTRY PRICE only
// (quantity legitimately changes on partial close, and the native-trailing rule
// fingerprint carries qty 0). Production CLUSDT held two armed dd1 records at entry
// 86.55 — one from the pre-partial-close 2.9-qty era (orderID 3774855642876379136,
// long dead on the exchange) and the live one from the 1.8-qty position (orderID
// 3777733524803973120). storedTrailingOrderIDForRule returned the FIRST map hit, so
// Go's randomized map iteration returned the dead ID on roughly half the polls →
// findTrailingOrderByID nil → tier judged missing → re-arm + red panel.
//
// The fix: among records matching the rule fingerprint, always return the most
// recently armed one (max UpdatedAt) — deterministically the live order.
func TestStoredTrailingOrderIDPrefersNewestRecord(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "stale-dup.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	traderID := "trader-staledup"
	symbol := "CLUSDT"
	side := "short"
	entry := 86.55
	dd1 := store.DrawdownTakeProfitRule{MinProfitPct: 2.5806, MaxDrawdownPct: 1.5484, CloseRatioPct: 100}
	ruleFP := stableDrawdownRuleFingerprint(entry, dd1)

	// Same entry price, same rule fingerprint, DIFFERENT position quantity and
	// orderID — exactly the production shape. The 2.9-qty record is older.
	stale := store.DynamicProtectionRecord{
		TraderID: traderID, ExchangeID: "exchange-1", Symbol: symbol, Side: side,
		PositionFingerprint: positionFingerprint(entry, 2.9), ProtectionType: "native_trailing",
		RuleFingerprint: ruleFP, CloseRatioPct: 100, Status: "armed",
		ExchangeOrderID: "dead-3774855642876379136", UpdatedAt: 1785001864926,
	}
	live := store.DynamicProtectionRecord{
		TraderID: traderID, ExchangeID: "exchange-1", Symbol: symbol, Side: side,
		PositionFingerprint: positionFingerprint(entry, 1.8), ProtectionType: "native_trailing",
		RuleFingerprint: ruleFP, CloseRatioPct: 100, Status: "armed",
		ExchangeOrderID: "live-3777733524803973120", UpdatedAt: 1785087632487,
	}
	for _, r := range []store.DynamicProtectionRecord{stale, live} {
		if err := st.SaveDynamicProtectionRecord(r); err != nil {
			t.Fatalf("save record: %v", err)
		}
	}

	at := &AutoTrader{id: traderID, store: st, exchange: "okx"}

	// Repeat enough times that randomized map order would surface the stale record.
	for i := 0; i < 30; i++ {
		got := at.storedTrailingOrderIDForRule(symbol, side, entry, dd1)
		if got != live.ExchangeOrderID {
			t.Fatalf("poll %d: resolved stale/duplicate record: got %q, want %q", i, got, live.ExchangeOrderID)
		}
	}

	// End-to-end: with only the live order resting, the tier must be seen as covered
	// on EVERY poll (no flapping → no 300s re-arm churn, panel stays green).
	openOrders := []OpenOrder{
		{PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET", OrderID: "live-3777733524803973120", Quantity: 1.8, StopPrice: 84.3, CallbackRate: 0.0155},
	}
	for i := 0; i < 30; i++ {
		if !at.hasMatchingNativeTrailingOrderForRule(symbol, side, entry, dd1, openOrders) {
			t.Fatalf("poll %d: dd1 judged missing despite its live order resting (churn + red panel bug)", i)
		}
	}
}

// TestClaimedTrailingOrderIDsProtectSiblingTierFromCollapse covers the 2026-07-27 SPCX
// case: the full-tier migration block canceled EVERY trailing order on the side, so
// arming dd1 destroyed the partial tier's live order placed seconds earlier (partial
// armed 01:56:41 → collapsed 01:56:45). The partial's record kept pointing at the dead
// algoId, so its matcher reported "missing" on every poll — panel red plus a re-arm
// each cooldown window. Sibling orders claimed by another armed record must survive;
// only unowned orphans may be collapsed.
func TestClaimedTrailingOrderIDsProtectSiblingTierFromCollapse(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "collapse-siblings.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	traderID := "trader-collapse"
	symbol := "SPCXUSDT"
	side := "long"
	entry := 112.06
	dd1 := store.DrawdownTakeProfitRule{MinProfitPct: 1.6246, MaxDrawdownPct: 0.9748, CloseRatioPct: 100}
	partial := store.DrawdownTakeProfitRule{MinProfitPct: 2.1662, MaxDrawdownPct: 0.6499, CloseRatioPct: 30}

	// The partial tier is armed and owns its live order; dd1 is the tier being armed now.
	if err := st.SaveDynamicProtectionRecord(store.DynamicProtectionRecord{
		TraderID: traderID, ExchangeID: "exchange-1", Symbol: symbol, Side: side,
		PositionFingerprint: positionFingerprint(entry, 0.42), ProtectionType: "native_partial_trailing",
		RuleFingerprint: stableDrawdownRuleFingerprint(entry, partial), CloseRatioPct: 30,
		Status: "armed", ExchangeOrderID: "partial-live", Quantity: 0.126, UpdatedAt: 1785088603100,
	}); err != nil {
		t.Fatalf("save partial record: %v", err)
	}

	at := &AutoTrader{id: traderID, store: st, exchange: "okx"}

	// Arming dd1: exclude dd1's own fingerprint, but the partial's order must be claimed.
	claimed := at.claimedTrailingOrderIDsForPosition(symbol, side, entry, stableDrawdownRuleFingerprint(entry, dd1))
	if _, ok := claimed["partial-live"]; !ok {
		t.Fatal("partial tier's live order must be claimed so full-tier migration preserves it")
	}

	// An unowned leftover must NOT be claimed — those are the genuine collapse targets.
	if _, ok := claimed["orphan-leftover"]; ok {
		t.Fatal("unowned orphan must not be reported as claimed")
	}

	// Simulate the collapse filter: only orphans are collected for cancellation.
	openOrders := []OpenOrder{
		{PositionSide: "LONG", Type: "TRAILING_STOP_MARKET", OrderID: "partial-live", Quantity: 0.126, StopPrice: 114.48, CallbackRate: 0.0065},
		{PositionSide: "LONG", Type: "TRAILING_STOP_MARKET", OrderID: "orphan-leftover", Quantity: 0.42, StopPrice: 113.0, CallbackRate: 0.0097},
	}
	toCancel := make([]string, 0)
	for _, o := range openOrders {
		if _, owned := claimed[o.OrderID]; owned {
			continue
		}
		toCancel = append(toCancel, o.OrderID)
	}
	if len(toCancel) != 1 || toCancel[0] != "orphan-leftover" {
		t.Fatalf("collapse must target only the orphan, got %v", toCancel)
	}

	// And the partial tier still matches its own live order afterwards.
	if !at.hasMatchingNativeTrailingOrderForRule(symbol, side, entry, partial, openOrders) {
		t.Fatal("partial tier must still match its preserved order")
	}
}

// TestSupersedeOlderArmedRecordsRetiresStaleTierRecords covers the hygiene half of the
// 2026-07-27 finding: every partial close re-arms a tier under a new
// PositionFingerprint and orderID while the previous record stays "armed", so records
// accumulate without bound (HYPE reached 4 armed dd1 records). A newer arm must retire
// the older same-tier records, while leaving other tiers and other symbols untouched.
func TestSupersedeOlderArmedRecordsRetiresStaleTierRecords(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "supersede.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	traderID := "trader-supersede"
	symbol := "HYPEUSDT"
	side := "long"
	entry := 58.665
	dd1 := store.DrawdownTakeProfitRule{MinProfitPct: 1.7562, MaxDrawdownPct: 1.0537, CloseRatioPct: 100}
	partial := store.DrawdownTakeProfitRule{MinProfitPct: 2.3416, MaxDrawdownPct: 0.7025, CloseRatioPct: 30}
	dd1FP := stableDrawdownRuleFingerprint(entry, dd1)
	partialFP := stableDrawdownRuleFingerprint(entry, partial)

	mk := func(ptype, ruleFP, oid string, qty float64, upd int64, closeRatio float64) store.DynamicProtectionRecord {
		r := store.DynamicProtectionRecord{
			TraderID: traderID, ExchangeID: "exchange-1", Symbol: symbol, Side: side,
			PositionFingerprint: positionFingerprint(entry, qty), ProtectionType: ptype,
			RuleFingerprint: ruleFP, CloseRatioPct: closeRatio, Status: "armed",
			ExchangeOrderID: oid, UpdatedAt: upd,
		}
		r.Key = store.BuildDynamicProtectionKey(r.TraderID, r.ExchangeID, r.Symbol, r.Side, r.PositionFingerprint, r.ProtectionType, r.RuleFingerprint, r.CloseRatioPct)
		return r
	}

	// Three stale dd1 arms from successive partial closes, one live partial tier.
	seed := []store.DynamicProtectionRecord{
		mk("native_trailing", dd1FP, "dd1-qty5", 5.0, 1000, 100),
		mk("native_trailing", dd1FP, "dd1-qty4", 4.0, 2000, 100),
		mk("native_trailing", dd1FP, "dd1-qty31", 3.1, 3000, 100),
		mk("native_partial_trailing", partialFP, "partial-live", 2.3, 3500, 30),
	}
	for _, r := range seed {
		if err := st.SaveDynamicProtectionRecord(r); err != nil {
			t.Fatalf("seed record: %v", err)
		}
	}

	at := &AutoTrader{id: traderID, exchangeID: "exchange-1", store: st, exchange: "okx"}

	// Newest dd1 arm lands (qty 2.3) and must retire the three older dd1 records.
	current := mk("native_trailing", dd1FP, "dd1-qty23", 2.3, 4000, 100)
	if err := st.SaveDynamicProtectionRecord(current); err != nil {
		t.Fatalf("save current: %v", err)
	}
	at.supersedeOlderArmedRecords(current)

	state, err := st.LoadDynamicProtectionState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	armedByOrder := map[string]string{}
	for _, r := range state.Records {
		armedByOrder[r.ExchangeOrderID] = r.Status
	}
	for _, dead := range []string{"dd1-qty5", "dd1-qty4", "dd1-qty31"} {
		if got := armedByOrder[dead]; got != "superseded" {
			t.Fatalf("stale dd1 record %s should be superseded, got %q", dead, got)
		}
	}
	if got := armedByOrder["dd1-qty23"]; got != "armed" {
		t.Fatalf("newest dd1 record must stay armed, got %q", got)
	}
	// The sibling partial tier is a DIFFERENT tier — it must not be retired.
	if got := armedByOrder["partial-live"]; got != "armed" {
		t.Fatalf("sibling partial tier must remain armed, got %q", got)
	}

	// After retirement, exactly one armed dd1 record remains and resolves to the live ID.
	if got := at.storedTrailingOrderIDForRule(symbol, side, entry, dd1); got != "dd1-qty23" {
		t.Fatalf("dd1 must resolve to newest live order, got %q", got)
	}
}

// TestPartialTierArmPersistsRecordAndCooldownPerExchange reproduces the 2026-07-27
// Binance leak: the partial-tier arm path placed the exchange order and then
// returned without persisting a DynamicProtectionRecord and without writing
// nativeTrailingArmTime. Both omissions matter, and each alone is enough to loop:
//
//   - No record → getArmedDrawdownRuleFingerprintsForPosition never sees the tier,
//     so getDrawdownArmRulesForSelectedRule re-selects it on every poll.
//   - No arm time → even with a record, the 300s cooldown has nothing to consult.
//
// Observed in production on the Binance trader (HYPEUSDT long): a fresh partial
// trailing order every ~10s with dynamicOwner climbing +2 per cycle, unbounded —
// the same leak class that hit the OKX 55-order cap as code=51299.
//
// Runs per exchange because the arm paths are a per-exchange switch: the same
// omission existed independently in the binance and bitget branches (and in OKX's
// untagged fallback), which is exactly how it survived three earlier fixes.
func TestPartialTierArmPersistsRecordAndCooldownPerExchange(t *testing.T) {
	for _, exchange := range []string{"binance", "okx", "bitget"} {
		t.Run(exchange, func(t *testing.T) {
			st, err := store.New(filepath.Join(t.TempDir(), "partial-arm-"+exchange+".db"))
			if err != nil {
				t.Fatalf("create store: %v", err)
			}
			fake := &fakeProtectionTrader{
				positions: []map[string]interface{}{{
					"symbol":      "HYPEUSDT",
					"side":        "long",
					"entryPrice":  59.14,
					"markPrice":   62.0,
					"positionAmt": 5.28,
				}},
			}
			at := &AutoTrader{
				id:                    "trader-partial",
				exchangeID:            "exchange-partial",
				store:                 st,
				exchange:              exchange,
				trader:                fake,
				config:                AutoTraderConfig{StrategyConfig: &store.StrategyConfig{}},
				protectionState:       make(map[string]string),
				nativeTrailingArmTime: make(map[string]time.Time),
			}

			// CloseRatioPct < 99.999 selects the partial branch.
			rule := store.DrawdownTakeProfitRule{MinProfitPct: 2.4616, MaxDrawdownPct: 0.7385, CloseRatioPct: 30}
			fp := stableDrawdownRuleFingerprint(59.14, rule)

			if ok := at.applyNativeTrailingDrawdown("HYPEUSDT", "long", 59.14, 62.0, rule); !ok {
				t.Fatal("expected partial native trailing drawdown to arm")
			}

			armed := at.getArmedDrawdownRecordsForPosition("HYPEUSDT", "long", 59.14, 5.28, 0)
			found := false
			for _, record := range armed {
				if record.ProtectionType == "native_partial_trailing" && record.RuleFingerprint == fp {
					found = true
				}
			}
			if !found {
				t.Fatal("partial arm must persist a native_partial_trailing armed record, otherwise the arm gate re-selects the tier every poll")
			}
			if _, ok := at.nativeTrailingArmTime[fp]; !ok {
				t.Fatal("partial arm must record nativeTrailingArmTime[fingerprint] to feed the 300s re-arm cooldown")
			}

			// The tier now resolves as covered, so it is not re-selected for arming.
			if rules := at.getDrawdownArmRulesForSelectedRule(59.14, 5.28, "HYPEUSDT", "long", rule); len(rules) != 0 {
				t.Fatalf("armed partial tier must not be re-selected for arming, got %d rules", len(rules))
			}
		})
	}
}

// TestFullTierFallbackSkipsSiblingClaimedOrder reproduces the 2026-07-27 BN SOLUSDT
// incident: a live position ran with NO full-close trailing protection while the log
// insisted, every 10s, that the tier was covered.
//
// Sequence: the raw-ATR-multiple dd1 order (placed before v1.16.9 resolved ATR units)
// was cancelled by hand. The resolved dd1 tier — a DIFFERENT fingerprint, since the
// resolver changes MaxDrawdownPct — therefore had no stored ExchangeOrderID and fell
// through to the legacy fallback: "return the first trailing order on this side".
// The only trailing order left was the 30% partial (qty 1.32 of 4.41), already owned
// by the dd2 record. dd1 bound to it; Binance hard-codes ActivationStatus="activated"
// for every trailing order, so applyNativeTrailingDrawdown short-circuited
// `if existing.ActivationStatus == "activated" { return true }` and armed nothing.
//
// The fallback must skip orders that a sibling record already claims, so the tier
// reports genuinely missing and gets armed.
func TestFullTierFallbackSkipsSiblingClaimedOrder(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "sibling-claim.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	traderID := "trader-sibling-claim"
	symbol := "SOLUSDT"
	side := "long"
	entry := 76.28
	qty := 4.41

	// Resolved tiers as they exist after ATR-unit resolution (v1.16.9).
	dd1 := store.DrawdownTakeProfitRule{MinProfitPct: 1.5183, MaxDrawdownPct: 0.9110, CloseRatioPct: 100, StageName: "dd1"}
	dd2 := store.DrawdownTakeProfitRule{MinProfitPct: 2.0245, MaxDrawdownPct: 0.6073, CloseRatioPct: 30, StageName: "dd2"}

	// Only dd2 has a live order on record. dd1's order was cancelled, and because the
	// raw-multiple order carried a different fingerprint, dd1 has NO stored ID at all.
	if err := st.SaveDynamicProtectionRecord(store.DynamicProtectionRecord{
		TraderID: traderID, ExchangeID: "exchange-1", Symbol: symbol, Side: side,
		PositionFingerprint: positionFingerprint(entry, qty), ProtectionType: "native_partial_trailing",
		RuleFingerprint: stableDrawdownRuleFingerprint(entry, dd2), CloseRatioPct: 30,
		Status: "armed", ExchangeOrderID: "2000001312535344",
	}); err != nil {
		t.Fatalf("save dd2 record: %v", err)
	}

	at := &AutoTrader{id: traderID, store: st, exchange: "binance"}

	// The single surviving trailing order — dd2's 30% partial. ActivationStatus is
	// "activated" because that is what the Binance adapter reports for every trailing
	// order (its open-algo endpoint returns neither callbackRate nor activatePrice).
	dd2Order := OpenOrder{
		PositionSide: "LONG", Type: "TRAILING_STOP_MARKET", OrderID: "2000001312535344",
		Quantity: 1.32, StopPrice: 75.8223, ActivationStatus: "activated",
	}
	openOrders := []OpenOrder{dd2Order}

	t.Run("dd1 must not bind to dd2's claimed order", func(t *testing.T) {
		got := at.findExistingFullTrailingOrder(symbol, side, entry, dd1, openOrders)
		if got != nil {
			t.Fatalf("dd1 bound to a sibling-claimed order (id=%s qty=%.4f) — the live position would run with no full-close trailing while the poll reports it covered", got.OrderID, got.Quantity)
		}
		if at.hasMatchingNativeTrailingOrderForRule(symbol, side, entry, dd1, openOrders) {
			t.Fatal("dd1 must report MISSING so the monitor re-arms it")
		}
	})

	t.Run("dd2 still matches its own order by stored ID", func(t *testing.T) {
		if !at.hasMatchingNativeTrailingOrderForRule(symbol, side, entry, dd2, openOrders) {
			t.Fatal("dd2 must keep matching its own order — the exclusion applies to SIBLINGS, not to the tier itself")
		}
	})

	t.Run("genuinely unclaimed trailing order is still adopted", func(t *testing.T) {
		// Regression guard: the fallback still exists for its legitimate case — an
		// order this position placed that no record claims (e.g. a record lost to a
		// restart before persistence). Adopting it avoids duplicate placements.
		unclaimed := OpenOrder{
			PositionSide: "LONG", Type: "TRAILING_STOP_MARKET", OrderID: "2000001399999999",
			Quantity: 4.41, StopPrice: 75.16, ActivationStatus: "activated",
		}
		got := at.findExistingFullTrailingOrder(symbol, side, entry, dd1, []OpenOrder{dd2Order, unclaimed})
		if got == nil {
			t.Fatal("an unclaimed trailing order must still be adopted by the fallback")
		}
		if got.OrderID != unclaimed.OrderID {
			t.Fatalf("fallback adopted the wrong order: got %s, want %s", got.OrderID, unclaimed.OrderID)
		}
	})
}
