package trader

import (
	"math"
	"path/filepath"
	"strings"
	"testing"

	"nofx/store"
)

// ============================================================================
// v1.17.2 —— 档位标识在 ATR 单位策略上静默失效,以及它连带撑出的"一单双主"账本腐烂。
//
// 2026-07-29 线上 (BN / WLDUSDT short, entry 0.3036, ATR(1h)=0.004616 → 1.5204%/ATR):
//
//   策略配置(全是 ATR 单位):
//     dd1                 min=2.5 ATR  dd=1.5 ATR  close=100
//     partial_profit_lock min=4.0 ATR  dd=1.2 ATR  close=30
//
//   现象 1(v1.17.1 的标识形同虚设):
//     两张单都以裸 reason="native_trailing" 挂出,clientOrderID 里没有 T<N>。
//     根因:drawdownTierTagIndex 拿 arm 路径给的**已解析**规则(min=3.6791)去比
//     config 里的**原始倍数**(min=2.5),精确浮点相等 → 恒不相等 → 序号恒 0。
//     4 个实盘交易员全是 ATR 单位,所以第二重保险在全部实盘上静默失效。
//
//   现象 2(reclaim 用未解析规则算出的记录污染账本):
//     4 条 armed 记录抢 2 张实物单,其中 2 个 orderID 各被双重认领,每 ~20s 重写一次。
//     根因:reclaimLiveDrawdownTrailingOrders 直接用 getActiveDrawdownRulesForPosition
//     的未解析规则,把 "2.5 ATR" 当 "2.5%" 算出 activation=0.296010(真单是 0.292060),
//     形状匹配的 1% 容差刚好卡不住(漂 1.33%),且落库的 fingerprint 带 min=2.5000,
//     与 arm 侧的 min=3.6791 在 drawdownRuleIdentity 看来"不是同一档",于是
//     supersedeOlderArmedRecords 的窄匹配永远碰不到它们。
//
// 这组测试对着**真实线上参数**写,反向验证过:把两处修复各自还原,对应用例即刻转红。
// ============================================================================

// bnWLDATRRules 是 2026-07-29 线上 BN WLDUSDT 的真实两档配置。
func bnWLDATRRules() []store.DrawdownTakeProfitRule {
	return []store.DrawdownTakeProfitRule{
		{
			MinProfitPct: 2.5, MinProfitUnit: store.ProtectionUnitATR, MinProfitMode: store.ProtectionValueModeManual,
			MaxDrawdownPct: 1.5, MaxDrawdownUnit: store.ProtectionUnitATR, MaxDrawdownMode: store.ProtectionValueModeManual,
			CloseRatioPct: 100, StageName: "dd1",
		},
		{
			MinProfitPct: 4, MinProfitUnit: store.ProtectionUnitATR, MinProfitMode: store.ProtectionValueModeManual,
			MaxDrawdownPct: 1.2, MaxDrawdownUnit: store.ProtectionUnitATR, MaxDrawdownMode: store.ProtectionValueModeManual,
			CloseRatioPct: 30, CloseRatioMode: store.ProtectionValueModeManual, StageName: "partial_profit_lock",
		},
	}
}

// bnWLDFixture 复用 atrTierFixture 的装配方式,只把规则换成 BN WLDUSDT 的真实两档。
func bnWLDFixture(t *testing.T, symbol, side string, entry, atrValue float64) *AutoTrader {
	t.Helper()
	at := atrTierFixture(t, symbol, side, entry, atrValue)
	at.config.StrategyConfig.Protection.DrawdownTakeProfit.Rules = bnWLDATRRules()
	return at
}

const (
	wldSymbol = "WLDUSDT"
	wldSide   = "short"
	wldEntry  = 0.3036
	wldATR    = 0.004616 // 1 ATR = 1.5204% of entry
)

// 决定性回归 1:ATR 单位策略的每一档都必须算出**互不相同的非零**档位序号。
//
// 还原修复(drawdownTierTagIndex 改回比 config 原始规则)后:两档都得 0,本用例转红。
func TestTierTag_ATRUnitRulesResolveToDistinctIndexes(t *testing.T) {
	at := bnWLDFixture(t, wldSymbol, wldSide, wldEntry, wldATR)

	// arm 路径拿到的正是这套已解析规则(auto_trader_risk.go:210)。
	resolved := at.resolveDrawdownRulesATR(bnWLDATRRules(), wldSymbol, wldSide, wldEntry)
	if len(resolved) != 2 {
		t.Fatalf("fixture: expected 2 resolved rules, got %d", len(resolved))
	}
	// 先确认解析真的发生了 —— 否则本用例会因为"没解析"而假绿。
	if math.Abs(resolved[0].MinProfitPct-2.5) < 0.01 {
		t.Fatalf("fixture: rules were not ATR-resolved (min still %.4f); the frozen ATR wiring is broken",
			resolved[0].MinProfitPct)
	}

	seen := map[int]string{}
	for _, rule := range resolved {
		idx := at.drawdownTierTagIndex(wldSymbol, wldSide, wldEntry, rule)
		if idx == 0 {
			t.Fatalf("ATR-resolved tier (min=%.4f%% dd=%.4f%% close=%.1f%%) got tag index 0 — "+
				"the tier tag degrades to a bare native_trailing on EVERY ATR-unit strategy, "+
				"i.e. on all four live traders, and the second line of defence is inert",
				rule.MinProfitPct, rule.MaxDrawdownPct, rule.CloseRatioPct)
		}
		if prev, dup := seen[idx]; dup {
			t.Fatalf("tiers %q and %q share tag index %d — indistinguishable on the exchange", prev, rule.StageName, idx)
		}
		seen[idx] = rule.StageName
	}
	// 序号必须按 MinProfitPct 升序:dd1(2.5 ATR) = 1, partial(4 ATR) = 2。
	if seen[1] != "dd1" || seen[2] != "partial_profit_lock" {
		t.Fatalf("tier ordering wrong: 1=%q 2=%q, want 1=dd1 2=partial_profit_lock", seen[1], seen[2])
	}
}

// 决定性回归 2:挂单 reason 必须带上 #N,且机制侧仍与裸 native_trailing 等价。
func TestTierTag_ATRUnitReasonCarriesTier(t *testing.T) {
	at := bnWLDFixture(t, wldSymbol, wldSide, wldEntry, wldATR)
	resolved := at.resolveDrawdownRulesATR(bnWLDATRRules(), wldSymbol, wldSide, wldEntry)

	tags := make([]string, 0, len(resolved))
	for _, rule := range resolved {
		tag := at.drawdownTrailingReasonTag(wldSymbol, wldSide, wldEntry, rule)
		if !strings.Contains(tag, "#") {
			t.Fatalf("ATR tier %q produced bare reason %q — the exchange-side tier identity is missing",
				rule.StageName, tag)
		}
		if got := store.NormalizeMechanism(tag); got != "native_trailing" {
			t.Fatalf("tag %q normalizes to %q, want native_trailing (mechanism comparisons must be unchanged)", tag, got)
		}
		if store.CodeForReason(tag) == "" {
			t.Fatalf("tag %q lost its mechanism code", tag)
		}
		tags = append(tags, tag)
	}
	if tags[0] == tags[1] {
		t.Fatalf("both ATR tiers produced the same reason tag %q", tags[0])
	}
}

// 决定性回归 3:reclaim 必须用 ATR 解析后的规则算 activation/callback/fingerprint。
//
// 这是"4 条记录抢 2 张单"的根因用例。还原修复(reclaim 改回用未解析规则)后:
// 认领出的 activation 会是 0.296010(未解析),与交易所上的真单差 1.33%,本用例转红。
func TestReclaim_ATRRulesAreResolvedBeforeMatching(t *testing.T) {
	at := bnWLDFixture(t, wldSymbol, wldSide, wldEntry, wldATR)
	resolved := at.resolveDrawdownRulesATR(bnWLDATRRules(), wldSymbol, wldSide, wldEntry)

	// 交易所上那两张单,就是按已解析规则挂的(线上实测 0.292060 / 0.285137)。
	orders := make([]OpenOrder, 0, 2)
	ids := make([]string, 0, 2)
	for i, rule := range resolved {
		act := calculateProfitBasedTrailingTriggerPrice(wldEntry, wldSide, rule.MinProfitPct)
		cb := calculateDrawdownRuleCallbackRatio(wldEntry, wldSide, rule)
		id := []string{"2000001319445192", "2000001319445212"}[i]
		orders = append(orders, OpenOrder{
			OrderID: id, Symbol: wldSymbol, PositionSide: "SHORT",
			Type: "TRAILING_STOP_MARKET", StopPrice: act, CallbackRate: cb,
			Quantity: 100, // 未带标识:走形状匹配,正是线上现存单的处境
		})
		ids = append(ids, id)
	}

	kept, matches := at.reclaimLiveDrawdownTrailingOrders(wldSymbol, wldSide, wldEntry, ids, orders)
	if len(kept) != 0 {
		t.Fatalf("live ATR-tier protective orders were left in the cancel list: %v — "+
			"reclaim computed its planned activation from RAW ATR multiples, so the 1%% shape "+
			"tolerance rejects the real orders", kept)
	}
	if len(matches) != 2 {
		t.Fatalf("expected both tiers reclaimed, got %d", len(matches))
	}
	// 认领记录里落库的参数必须是解析后的值 —— 否则记录与任何真单都对不上,却每轮重写。
	for _, m := range matches {
		if math.Abs(m.Rule.MinProfitPct-2.5) < 0.01 || math.Abs(m.Rule.MinProfitPct-4.0) < 0.01 {
			t.Fatalf("reclaim persisted a RAW ATR multiple as a percent (min=%.4f) for order %s; "+
				"the record can never match a real order and pollutes the ownership ledger",
				m.Rule.MinProfitPct, m.OrderID)
		}
	}
	// 一单一主:两张单必须认到两个不同的档。
	if matches[0].RuleFingerprint == matches[1].RuleFingerprint {
		t.Fatalf("both orders were claimed by the SAME tier fingerprint %q — one tier would be judged missing and re-armed",
			matches[0].RuleFingerprint)
	}
	// 而且认领的方向不能反:深档(min 大)对深 activation。short 侧 activation 越低越深。
	byID := map[string]reclaimableTrailingMatch{}
	for _, m := range matches {
		byID[m.OrderID] = m
	}
	shallow, deep := byID["2000001319445192"], byID["2000001319445212"]
	if !(shallow.Rule.MinProfitPct < deep.Rule.MinProfitPct) {
		t.Fatalf("tier↔order mapping is INVERTED: order …192 (act=%.6f) claimed min=%.4f%%, "+
			"order …212 (act=%.6f) claimed min=%.4f%%",
			shallow.ActivationPrice, shallow.Rule.MinProfitPct, deep.ActivationPrice, deep.Rule.MinProfitPct)
	}
}

// 决定性回归 4:带标识的单必须认给它自报的那一档 —— 在 ATR 单位策略上也成立。
//
// 这一条是 v1.17.1 想要、但在 ATR 策略上从未生效过的能力。
func TestReclaim_ATRTaggedOrderIsClaimedByItsOwnTier(t *testing.T) {
	at := bnWLDFixture(t, wldSymbol, wldSide, wldEntry, wldATR)
	resolved := at.resolveDrawdownRulesATR(bnWLDATRRules(), wldSymbol, wldSide, wldEntry)
	deep := resolved[1] // partial_profit_lock, 4 ATR
	tier := at.drawdownTierTagIndex(wldSymbol, wldSide, wldEntry, deep)
	if tier != 2 {
		t.Fatalf("fixture: deep tier index = %d, want 2", tier)
	}

	// 已 activated:形状匹配对 activated 单一律返回 true,所以没有标识时会被认给
	// "最低未用档"dd1 —— 那正是线上看到的反向映射。
	order := OpenOrder{
		OrderID: "algo-deep", Symbol: wldSymbol, PositionSide: "SHORT",
		Type: "TRAILING_STOP_MARKET", ActivationStatus: "activated",
		Quantity: 30, ProtectionTier: tier,
	}

	kept, matches := at.reclaimLiveDrawdownTrailingOrders(wldSymbol, wldSide, wldEntry, []string{"algo-deep"}, []OpenOrder{order})
	if len(kept) != 0 || len(matches) != 1 {
		t.Fatalf("tagged live order not reclaimed: kept=%v matches=%d", kept, len(matches))
	}
	if math.Abs(matches[0].Rule.CloseRatioPct-deep.CloseRatioPct) > 0.001 {
		t.Fatalf("self-reported tier %d was claimed by the WRONG tier (close=%.1f%%, want %.1f%%) — "+
			"the shallow full-close tier would then be judged missing and re-armed",
			tier, matches[0].Rule.CloseRatioPct, deep.CloseRatioPct)
	}
}

// 决定性回归 5:一张活单只能有一条 armed 记录认领它(账本核心不变量)。
//
// 2026-07-29 线上实况:reclaim 用未解析规则写出的记录(min=2.5000)与 arm 侧的
// (min=3.6791)在 drawdownRuleIdentity 看来"不是同一档",supersedeOlderArmedRecords
// 的窄匹配(同档 + 不同 orderID + 更旧)完全碰不到它们 —— 于是 orderID
// 2000001319445192 / 2000001319445212 各被两条 armed 记录同时认领,每 ~20s 重写。
//
// 危害:档位→单的解析依赖谁先被读到;30% 那一档成交后,100% 那一档可能把自己标成
// 已执行,**全平档的回撤保护静默消失**。
//
// 还原修复(去掉 supersedeConflictingOrderClaim 的调用)后本用例转红。
func TestSupersede_OneLiveOrderCannotHaveTwoTierOwners(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "conflict.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	traderID := "trader-conflict"
	const (
		symbol  = "WLDUSDT"
		side    = "short"
		entry   = 0.3036
		liveOID = "2000001319445192"
	)
	// 未解析(reclaim 旧行为写出的)与已解析(arm 写出的)—— 同一档的两种说法。
	stale := store.DrawdownTakeProfitRule{MinProfitPct: 2.5, MaxDrawdownPct: 1.5, CloseRatioPct: 100}
	live := store.DrawdownTakeProfitRule{MinProfitPct: 3.6791, MaxDrawdownPct: 2.2075, CloseRatioPct: 100}

	mk := func(ptype string, rule store.DrawdownTakeProfitRule, oid string, upd int64) store.DynamicProtectionRecord {
		r := store.DynamicProtectionRecord{
			TraderID: traderID, ExchangeID: "exchange-1", Symbol: symbol, Side: side,
			PositionFingerprint: positionFingerprint(entry, 100), ProtectionType: ptype,
			RuleFingerprint: stableDrawdownRuleFingerprint(entry, rule), CloseRatioPct: rule.CloseRatioPct,
			Status: "armed", ExchangeOrderID: oid, UpdatedAt: upd,
		}
		r.Key = store.BuildDynamicProtectionKey(r.TraderID, r.ExchangeID, r.Symbol, r.Side, r.PositionFingerprint, r.ProtectionType, r.RuleFingerprint, r.CloseRatioPct)
		return r
	}

	// 先落一条"陈旧解释"的 armed 记录,认领的是那张活单。
	staleRec := mk("native_trailing", stale, liveOID, 1000)
	if err := st.SaveDynamicProtectionRecord(staleRec); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	at := &AutoTrader{id: traderID, exchangeID: "exchange-1", store: st, exchange: "binance"}

	// arm 侧现在用已解析规则重新认领同一张单。
	current := mk("native_trailing", live, liveOID, 2000)
	if err := st.SaveDynamicProtectionRecord(current); err != nil {
		t.Fatalf("save current: %v", err)
	}
	at.supersedeOlderArmedRecords(current)

	state, err := st.LoadDynamicProtectionState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	armedClaims := 0
	for _, r := range state.Records {
		if r.Status == "armed" && r.ExchangeOrderID == liveOID {
			armedClaims++
		}
	}
	if armedClaims != 1 {
		t.Fatalf("exchange order %s is claimed by %d armed records, want exactly 1 — "+
			"tier→order resolution becomes read-order dependent, and when the partial tier fills "+
			"the full-close tier can mark itself executed, silently dropping whole-position "+
			"drawdown protection", liveOID, armedClaims)
	}
	// 胜者必须是新写入的那条(刚发生的事实),而不是陈旧解释。
	for _, r := range state.Records {
		if r.Status != "armed" || r.ExchangeOrderID != liveOID {
			continue
		}
		if drawdownRuleIdentity(r.RuleFingerprint) != drawdownRuleIdentity(current.RuleFingerprint) {
			t.Fatalf("the surviving claim is the STALE one (ruleID=%s); the freshly written claim must win",
				drawdownRuleIdentity(r.RuleFingerprint))
		}
	}
}

// 反向锁:不同 orderID 的不同档必须互不干扰 —— 新不变量不能变成"顺手退役别人"。
func TestSupersede_DistinctOrdersAcrossTiersAreUntouched(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "noharm.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	traderID := "trader-noharm"
	const (
		symbol = "WLDUSDT"
		side   = "short"
		entry  = 0.3036
	)
	full := store.DrawdownTakeProfitRule{MinProfitPct: 3.6791, MaxDrawdownPct: 2.2075, CloseRatioPct: 100}
	partial := store.DrawdownTakeProfitRule{MinProfitPct: 5.8865, MaxDrawdownPct: 1.7659, CloseRatioPct: 30}

	mk := func(ptype string, rule store.DrawdownTakeProfitRule, oid string, upd int64) store.DynamicProtectionRecord {
		r := store.DynamicProtectionRecord{
			TraderID: traderID, ExchangeID: "exchange-1", Symbol: symbol, Side: side,
			PositionFingerprint: positionFingerprint(entry, 100), ProtectionType: ptype,
			RuleFingerprint: stableDrawdownRuleFingerprint(entry, rule), CloseRatioPct: rule.CloseRatioPct,
			Status: "armed", ExchangeOrderID: oid, UpdatedAt: upd,
		}
		r.Key = store.BuildDynamicProtectionKey(r.TraderID, r.ExchangeID, r.Symbol, r.Side, r.PositionFingerprint, r.ProtectionType, r.RuleFingerprint, r.CloseRatioPct)
		return r
	}

	other := mk("native_partial_trailing", partial, "2000001319445212", 1000)
	if err := st.SaveDynamicProtectionRecord(other); err != nil {
		t.Fatalf("seed other tier: %v", err)
	}
	at := &AutoTrader{id: traderID, exchangeID: "exchange-1", store: st, exchange: "binance"}

	current := mk("native_trailing", full, "2000001319445192", 2000)
	if err := st.SaveDynamicProtectionRecord(current); err != nil {
		t.Fatalf("save current: %v", err)
	}
	at.supersedeOlderArmedRecords(current)

	state, err := st.LoadDynamicProtectionState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	for _, r := range state.Records {
		if r.ExchangeOrderID == "2000001319445212" && r.Status != "armed" {
			t.Fatalf("the OTHER tier's live claim was retired (status=%q) — arming one tier must never "+
				"drop another tier's protection ownership", r.Status)
		}
	}
}

// 端到端闭环:ATR 已解析规则 → reason tag → clientOrderID → 解回档位序号 → 认领到同一档。
//
// 这一条把三个包(trader 编码档位、store 编解码 clientID、trader 认领)串起来跑一遍。
// 单独测任一环都可能各自绿着而闭环断掉 —— v1.17.1 就是"编码环恒输出 0",三个环各自
// 的单测全绿,闭环却没有承载任何信息。
func TestTierTag_ATREndToEndClientIDRoundTrip(t *testing.T) {
	const prefix = "x-KzrpZaP9" // Binance broker 前缀,与 trader/binance 的 brOrderIDPrefix 同形
	at := bnWLDFixture(t, wldSymbol, wldSide, wldEntry, wldATR)
	resolved := at.resolveDrawdownRulesATR(bnWLDATRRules(), wldSymbol, wldSide, wldEntry)

	for want, rule := range resolved {
		wantTier := want + 1
		// 1) 挂单侧:算出带档位的 reason。
		reason := at.drawdownTrailingReasonTag(wldSymbol, wldSide, wldEntry, rule)
		// 2) 适配器:编进 clientOrderID。
		clientID := store.EncodeReasonClientID(prefix, reason)
		if clientID == "" {
			t.Fatalf("tier %d: reason %q produced no coded client id", wantTier, reason)
		}
		if len(clientID) > 32 {
			t.Fatalf("tier %d: client id %q exceeds the 32-char exchange limit", wantTier, clientID)
		}
		// 3) 读回:交易所回显的 clientID 解出档位。
		gotTier := store.DecodeTierFromClientID(prefix, clientID)
		if gotTier != wantTier {
			t.Fatalf("tier %d round-tripped to %d via client id %q — the exchange-side tier identity is lost",
				wantTier, gotTier, clientID)
		}
		// 机制必须仍然解回 native_trailing,否则归因链断。
		if got := store.DecodeReasonFromClientID(prefix, clientID); store.NormalizeMechanism(got) != "native_trailing" {
			t.Fatalf("tier %d: client id %q decodes mechanism %q, want native_trailing", wantTier, clientID, got)
		}
		// 4) 认领侧:用解出的序号必须落回同一档。
		order := OpenOrder{
			OrderID: "e2e", Symbol: wldSymbol, PositionSide: "SHORT",
			Type: "TRAILING_STOP_MARKET", ActivationStatus: "activated",
			Quantity: 100, ProtectionTier: gotTier,
		}
		ordered := at.canonicalDrawdownTierRules(wldSymbol, wldSide, wldEntry)
		idx := at.tierTaggedRuleIndex(order, wldSide, wldEntry, ordered)
		if idx < 0 {
			t.Fatalf("tier %d: reclaim could not resolve the self-reported tier", wantTier)
		}
		if !sameDrawdownTierIdentity(ordered[idx], rule) {
			t.Fatalf("tier %d: the loop closed on the WRONG tier (min=%.4f%% close=%.1f%%, want min=%.4f%% close=%.1f%%)",
				wantTier, ordered[idx].MinProfitPct, ordered[idx].CloseRatioPct, rule.MinProfitPct, rule.CloseRatioPct)
		}
	}
}
