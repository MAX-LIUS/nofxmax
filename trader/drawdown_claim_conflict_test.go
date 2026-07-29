package trader

import (
	"path/filepath"
	"testing"
	"time"

	tradertypes "nofx/trader/types"

	"nofx/store"
)

// ============================================================================
// 一单多主的收敛。用的是 2026-07-29 线上 BN WLDUSDT short 的**原始数值**,包括那个
// 最关键的细节:错的那两条记录 UpdatedAt 更新(坏掉的 reclaim 每 ~20s 重写一次)。
//
//	order 2000001319445192  native_trailing      close=100 act=0.29206045 cb=0.02280543 upd=1785326661802 (对)
//	                        native_partial_trailing close=30 act=0.29145600 cb=0.0120     upd=1785334682356 (错·更新)
//	order 2000001319445212  native_partial_trailing close=30 act=0.28513672 cb=0.01824434 upd=1785326662821 (对)
//	                        native_trailing      close=100 act=0.29601000 cb=0.0150      upd=1785334682353 (错·更新)
//
// 所以"取最新"是**错的**判据 —— 这组测试首先锁死这一点。
// ============================================================================

const (
	wldOIDFull    = "2000001319445192"
	wldOIDPartial = "2000001319445212"
)

type wldClaimFixture struct {
	at         *AutoTrader
	st         *store.Store
	liveFull   store.DrawdownTakeProfitRule
	livePart   store.DrawdownTakeProfitRule
	staleFull  store.DrawdownTakeProfitRule
	stalePart  store.DrawdownTakeProfitRule
	openOrders []OpenOrder
}

func newWLDClaimFixture(t *testing.T) *wldClaimFixture {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "claims.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	at := &AutoTrader{id: "trader-claim", exchangeID: "exch-claim", store: st, exchange: "binance"}

	f := &wldClaimFixture{
		at: at, st: st,
		// arm 侧(已 ATR 解析)—— 正确的两档。
		liveFull: store.DrawdownTakeProfitRule{MinProfitPct: 3.6791, MaxDrawdownPct: 2.2075, CloseRatioPct: 100, StageName: "dd1"},
		livePart: store.DrawdownTakeProfitRule{MinProfitPct: 5.8865, MaxDrawdownPct: 1.7659, CloseRatioPct: 30, StageName: "partial_profit_lock"},
		// 坏掉的 reclaim 侧(未解析,把 ATR 倍数当百分比)—— 错误的两档。
		staleFull: store.DrawdownTakeProfitRule{MinProfitPct: 2.5, MaxDrawdownPct: 1.5, CloseRatioPct: 100, StageName: "dd1"},
		stalePart: store.DrawdownTakeProfitRule{MinProfitPct: 4, MaxDrawdownPct: 1.2, CloseRatioPct: 30, StageName: "partial_profit_lock"},
	}
	// 交易所上那两张真单:按已解析规则挂的,都还没激活。
	f.openOrders = []OpenOrder{
		{OrderID: wldOIDFull, Symbol: wldSymbol, PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET",
			ActivationStatus: "pending", StopPrice: 0.29206045312286233, CallbackRate: 0.022805428610943913, Quantity: 100},
		{OrderID: wldOIDPartial, Symbol: wldSymbol, PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET",
			ActivationStatus: "pending", StopPrice: 0.2851367249965797, CallbackRate: 0.01824434288875513, Quantity: 30},
	}
	return f
}

func (f *wldClaimFixture) seed(t *testing.T, ptype string, rule store.DrawdownTakeProfitRule, oid string, act, cb float64, upd int64) store.DynamicProtectionRecord {
	t.Helper()
	r := store.DynamicProtectionRecord{
		TraderID: f.at.id, ExchangeID: f.at.exchangeID, Symbol: wldSymbol, Side: wldSide,
		PositionFingerprint: positionFingerprint(wldEntry, 100), ProtectionType: ptype,
		RuleFingerprint: stableDrawdownRuleFingerprint(wldEntry, rule), CloseRatioPct: rule.CloseRatioPct,
		Status: "armed", ExchangeOrderID: oid, ActivationPrice: act, CallbackRatio: cb, UpdatedAt: upd,
	}
	r.Key = store.BuildDynamicProtectionKey(r.TraderID, r.ExchangeID, r.Symbol, r.Side, r.PositionFingerprint, r.ProtectionType, r.RuleFingerprint, r.CloseRatioPct)
	if err := f.st.SaveDynamicProtectionRecord(r); err != nil {
		t.Fatalf("seed record: %v", err)
	}
	return r
}

func (f *wldClaimFixture) armedClaims(t *testing.T) map[string][]store.DynamicProtectionRecord {
	t.Helper()
	state, err := f.st.LoadDynamicProtectionState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	out := map[string][]store.DynamicProtectionRecord{}
	for _, r := range state.Records {
		if r.Status == "armed" && r.ExchangeOrderID != "" {
			out[r.ExchangeOrderID] = append(out[r.ExchangeOrderID], r)
		}
	}
	return out
}

// 决定性回归:线上原始状态(4 条抢 2 张,错的更新)必须收敛成一单一主,且**留下的是对的**。
//
// 还原修复(去掉 resolveConflictingTrailingClaims 的调用)后本用例转红。
// 把判据换成"取最新"也会转红 —— 那正是这组数据存在的意义。
func TestClaimConflict_LiveWLDStateConvergesToTheCorrectOwners(t *testing.T) {
	f := newWLDClaimFixture(t)
	correctFull := f.seed(t, "native_trailing", f.liveFull, wldOIDFull, 0.29206045312286233, 0.022805428610943913, 1785326661802)
	correctPart := f.seed(t, "native_partial_trailing", f.livePart, wldOIDPartial, 0.2851367249965797, 0.01824434288875513, 1785326662821)
	// 错的两条:认反了,而且更新。
	f.seed(t, "native_partial_trailing", f.stalePart, wldOIDFull, 0.29145599999999994, 0.012, 1785334682356)
	f.seed(t, "native_trailing", f.staleFull, wldOIDPartial, 0.29600999999999994, 0.015, 1785334682353)

	ordered := canonicalDrawdownTierOrder([]store.DrawdownTakeProfitRule{f.liveFull, f.livePart})
	retired := f.at.resolveConflictingTrailingClaims(wldSymbol, wldSide, wldEntry, f.openOrders, ordered)
	if retired != 2 {
		t.Fatalf("retired %d conflicting claims, want 2", retired)
	}

	claims := f.armedClaims(t)
	for _, oid := range []string{wldOIDFull, wldOIDPartial} {
		if len(claims[oid]) != 1 {
			t.Fatalf("order %s still has %d armed owners, want 1 — tier→order resolution stays "+
				"read-order/recency dependent, and the partial tier's fill can mark the full-close "+
				"tier executed", oid, len(claims[oid]))
		}
	}
	if got := claims[wldOIDFull][0].Key; got != correctFull.Key {
		t.Fatalf("order %s kept the WRONG owner (close=%.1f%% act=%.6f). The stale claims have NEWER "+
			"UpdatedAt on this live data, so a recency-based rule keeps the wrong one — the exchange "+
			"order shape must be the discriminator.",
			wldOIDFull, claims[wldOIDFull][0].CloseRatioPct, claims[wldOIDFull][0].ActivationPrice)
	}
	if got := claims[wldOIDPartial][0].Key; got != correctPart.Key {
		t.Fatalf("order %s kept the WRONG owner (close=%.1f%% act=%.6f)",
			wldOIDPartial, claims[wldOIDPartial][0].CloseRatioPct, claims[wldOIDPartial][0].ActivationPrice)
	}
}

// 第二级判据:那张单已经激活(形状无从对比)时,靠"档位是否还在当前生效规则集里"决胜。
func TestClaimConflict_ActivatedOrderFallsBackToTierSetMembership(t *testing.T) {
	f := newWLDClaimFixture(t)
	correct := f.seed(t, "native_trailing", f.liveFull, wldOIDFull, 0.29206045312286233, 0.022805428610943913, 1785326661802)
	f.seed(t, "native_partial_trailing", f.stalePart, wldOIDFull, 0.29145599999999994, 0.012, 1785334682356)

	// 已激活:Binance 不报 callback,StopPrice 是移动跟踪价 —— 形状判据必须弃权。
	activated := []OpenOrder{{
		OrderID: wldOIDFull, Symbol: wldSymbol, PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET",
		ActivationStatus: "activated", StopPrice: 0.2755, Quantity: 100,
	}}
	ordered := canonicalDrawdownTierOrder([]store.DrawdownTakeProfitRule{f.liveFull, f.livePart})
	if retired := f.at.resolveConflictingTrailingClaims(wldSymbol, wldSide, wldEntry, activated, ordered); retired != 1 {
		t.Fatalf("retired %d claims, want 1", retired)
	}
	claims := f.armedClaims(t)
	if len(claims[wldOIDFull]) != 1 || claims[wldOIDFull][0].Key != correct.Key {
		t.Fatalf("activated order kept the wrong owner; tier-set membership should have decided it "+
			"(kept close=%.1f%%)", claims[wldOIDFull][0].CloseRatioPct)
	}
}

// 反向锁 1:同一档的陈旧分叉不是"跨档冲突",这里不能插手 —— 那是
// supersedeOlderArmedRecords 的时序职责,越权会改变既有自愈语义。
func TestClaimConflict_SameTierDuplicatesAreLeftAlone(t *testing.T) {
	f := newWLDClaimFixture(t)
	f.seed(t, "native_trailing", f.liveFull, wldOIDFull, 0.29206045312286233, 0.022805428610943913, 1785326661802)
	// 同档、同 orderID、不同 activation(均价修正造成的分叉)。
	dup := f.liveFull
	f.seed(t, "native_trailing", dup, wldOIDFull, 0.29210, 0.02281, 1785334682356)

	ordered := canonicalDrawdownTierOrder([]store.DrawdownTakeProfitRule{f.liveFull, f.livePart})
	if retired := f.at.resolveConflictingTrailingClaims(wldSymbol, wldSide, wldEntry, f.openOrders, ordered); retired != 0 {
		t.Fatalf("retired %d same-tier records — cross-tier conflict resolution must not take over the "+
			"time-ordered same-tier path", retired)
	}
}

// 反向锁 2:正常的多档持仓(每档一张自己的单)必须一条都不动。
func TestClaimConflict_HealthyMultiTierLedgerIsUntouched(t *testing.T) {
	f := newWLDClaimFixture(t)
	f.seed(t, "native_trailing", f.liveFull, wldOIDFull, 0.29206045312286233, 0.022805428610943913, 1785326661802)
	f.seed(t, "native_partial_trailing", f.livePart, wldOIDPartial, 0.2851367249965797, 0.01824434288875513, 1785326662821)

	ordered := canonicalDrawdownTierOrder([]store.DrawdownTakeProfitRule{f.liveFull, f.livePart})
	if retired := f.at.resolveConflictingTrailingClaims(wldSymbol, wldSide, wldEntry, f.openOrders, ordered); retired != 0 {
		t.Fatalf("retired %d claims from a HEALTHY two-tier ledger — this would drop live protection ownership", retired)
	}
	claims := f.armedClaims(t)
	if len(claims[wldOIDFull]) != 1 || len(claims[wldOIDPartial]) != 1 {
		t.Fatalf("healthy ledger was altered: full=%d partial=%d", len(claims[wldOIDFull]), len(claims[wldOIDPartial]))
	}
}

// 反向锁 3:那张单已经不在活单列表里(已成交/已撤)时,不能因为"形状无从对比"就乱退役。
// 此时仍应靠档位在册决胜,且必须留下在册的那条。
func TestClaimConflict_MissingLiveOrderStillPrefersTheInTierClaim(t *testing.T) {
	f := newWLDClaimFixture(t)
	correct := f.seed(t, "native_trailing", f.liveFull, wldOIDFull, 0.29206045312286233, 0.022805428610943913, 1785326661802)
	f.seed(t, "native_partial_trailing", f.stalePart, wldOIDFull, 0.29145599999999994, 0.012, 1785334682356)

	ordered := canonicalDrawdownTierOrder([]store.DrawdownTakeProfitRule{f.liveFull, f.livePart})
	// openOrders 里故意不含这张单。
	other := []OpenOrder{{OrderID: "unrelated", Symbol: wldSymbol, PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET"}}
	if retired := f.at.resolveConflictingTrailingClaims(wldSymbol, wldSide, wldEntry, other, ordered); retired != 1 {
		t.Fatalf("retired %d claims, want 1", retired)
	}
	claims := f.armedClaims(t)
	if len(claims[wldOIDFull]) != 1 || claims[wldOIDFull][0].Key != correct.Key {
		t.Fatalf("with the order gone, the in-tier claim must survive (kept close=%.1f%% act=%.6f)",
			claims[wldOIDFull][0].CloseRatioPct, claims[wldOIDFull][0].ActivationPrice)
	}
}

// 调用点集成:真正跑一遍 reconcileProtectionForPosition,证明收敛确实被挂在 reconcile
// 路径上 —— 只测 resolveConflictingTrailingClaims 本身的话,把调用点删掉测试仍然全绿。
//
// 还原修复(把 protection_reconciler.go 里的调用删掉)后本用例转红。
func TestClaimConflict_ReconcilePathActuallyResolvesConflicts(t *testing.T) {
	const (
		symbol = "CONFLICTUSDT"
		side   = "long"
		entry  = 100.0
		oid    = "trail-1"
	)
	reconcileCooldownMutex.Lock()
	delete(reconcileCooldowns, symbol+"_"+side)
	reconcileCooldownMutex.Unlock()
	defer func() {
		reconcileCooldownMutex.Lock()
		delete(reconcileCooldowns, symbol+"_"+side)
		reconcileCooldownMutex.Unlock()
	}()

	full := store.DrawdownTakeProfitRule{MinProfitPct: 5, MaxDrawdownPct: 40, CloseRatioPct: 100, StageName: "dd1"}
	partial := store.DrawdownTakeProfitRule{MinProfitPct: 8, MaxDrawdownPct: 30, CloseRatioPct: 30, StageName: "partial"}
	fullAct := calculateProfitBasedTrailingTriggerPrice(entry, side, full.MinProfitPct)
	fullCB := calculateDrawdownRuleCallbackRatio(entry, side, full)

	ft := &fakeReconcileTrader{
		fakeOrderProtectionTrader: fakeOrderProtectionTrader{
			openOrders: []tradertypes.OpenOrder{
				{OrderID: "sl1", Symbol: symbol, PositionSide: "LONG", Type: "STOP_MARKET", StopPrice: 95, Quantity: 1, ClientOrderID: "ladder_sl_1", Status: "NEW"},
				{OrderID: oid, Symbol: symbol, PositionSide: "LONG", Type: "TRAILING_STOP_MARKET",
					StopPrice: fullAct, CallbackRate: fullCB, Quantity: 1, Status: "NEW"},
			},
			positions: []map[string]interface{}{{
				"symbol": symbol, "side": side, "positionAmt": 1.0, "entryPrice": entry, "markPrice": entry,
			}},
		},
		positions: []map[string]interface{}{{
			"symbol": symbol, "side": side, "positionAmt": 1.0, "entryPrice": entry, "markPrice": entry,
		}},
	}

	st, err := store.New(filepath.Join(t.TempDir(), "reconcile_conflict.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	at := &AutoTrader{
		id: "trader-rc-conflict", exchangeID: "exch-rc", exchange: "okx", trader: ft, store: st,
		config: AutoTraderConfig{StrategyConfig: &store.StrategyConfig{
			Protection: store.ProtectionConfig{
				LadderTPSL: store.LadderTPSLConfig{
					Enabled: true, Mode: store.ProtectionModeManual, StopLossEnabled: true,
					Rules: []store.LadderTPSLRule{{StopLossPct: 5, StopLossCloseRatioPct: 100}},
				},
				DrawdownTakeProfit: store.DrawdownTakeProfitConfig{
					Enabled: true, Mode: store.ProtectionModeManual,
					Rules: []store.DrawdownTakeProfitRule{full, partial},
				},
			},
		}},
		protectionState:       make(map[string]string),
		breakEvenState:        make(map[string]string),
		breakEvenFingerprints: make(map[string]string),
		drawdownState:         make(map[string]string),
		nativeTrailingArmTime: make(map[string]time.Time),
		drawdownSource:        make(map[string]string),
		reArmFailCache:        make(map[string]int),
		reArmTripTime:         make(map[string]time.Time),
		drawdownTierAllocs:    make(map[string][]store.DrawdownTierAllocation),
	}

	mk := func(ptype string, rule store.DrawdownTakeProfitRule, act, cb float64, upd int64) store.DynamicProtectionRecord {
		r := store.DynamicProtectionRecord{
			TraderID: at.id, ExchangeID: at.exchangeID, Symbol: symbol, Side: side,
			PositionFingerprint: positionFingerprint(entry, 1), ProtectionType: ptype,
			RuleFingerprint: stableDrawdownRuleFingerprint(entry, rule), CloseRatioPct: rule.CloseRatioPct,
			Status: "armed", ExchangeOrderID: oid, ActivationPrice: act, CallbackRatio: cb, UpdatedAt: upd,
		}
		r.Key = store.BuildDynamicProtectionKey(r.TraderID, r.ExchangeID, r.Symbol, r.Side, r.PositionFingerprint, r.ProtectionType, r.RuleFingerprint, r.CloseRatioPct)
		if err := st.SaveDynamicProtectionRecord(r); err != nil {
			t.Fatalf("seed: %v", err)
		}
		return r
	}
	// 一张单,两档认领,错的那条更新 —— 线上那个形状。
	correct := mk("native_trailing", full, fullAct, fullCB, 1000)
	mk("native_partial_trailing", partial, fullAct*1.02, fullCB/2, 9000)

	if _, err := at.reconcileProtectionForPosition(symbol, side, 1, entry, entry); err != nil {
		t.Logf("reconcile returned err (not fatal for this assertion): %v", err)
	}

	state, err := st.LoadDynamicProtectionState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	armed := make([]store.DynamicProtectionRecord, 0, 2)
	for _, r := range state.Records {
		if r.Status == "armed" && r.ExchangeOrderID == oid {
			armed = append(armed, r)
		}
	}
	if len(armed) != 1 {
		t.Fatalf("after a reconcile cycle, order %s still has %d armed owners, want 1 — the conflict "+
			"resolver is not wired into the reconcile path, so a real position's ledger never self-heals", oid, len(armed))
	}
	if armed[0].Key != correct.Key {
		t.Fatalf("reconcile kept the wrong owner (close=%.1f%%)", armed[0].CloseRatioPct)
	}
}
