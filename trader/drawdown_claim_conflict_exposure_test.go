package trader

import (
	"path/filepath"
	"testing"

	"nofx/store"
)

// ============================================================================
// 认领敞口越界(活单认领总和 > 100%)。用的是 2026-07-29 线上 OKX ETHUSDT short 的
// **原始数值**,entry 1890.56,4 张活的 trailing 单合计认领 330%:
//
//	3784785480328314880  native_trailing         close=100 act=1842.030903 (min=2.5669 max=1.0268) 旧 ATR
//	3784966212015259648  native_trailing         close=100 act=1850.119086 (min=2.1391 max=0.7701) 旧倍数
//	3784969143967977472  native_trailing         close=100 act=1850.119086 (min=2.1391 max=1.2835) ← 当前档
//	3784966244730830848  native_partial_trailing close=30  act=1825.854538                          ← 当前档
//
// 三张全平单同时在场:谁先触发谁平掉整个仓位,而最紧的 callback 来自被改掉的旧参数,
// 会比当前策略意图更早平仓。旧记录还挂着 armed,把旧单从 stale 清理路径里屏蔽掉了,
// 所以 reconciler 每轮都报 staleTrail=0 —— 账本"自洽",交易所在流血。
//
// 注意不变量的口径:**不是**"比例总和 ≤ 100%"。100%+30%=130% 正是正常的两档配置
// (第一版实现按总和判越界,被 TestOverClaimedExposure_LiveETHStateConvergesToCurrentTiers
// 当场证伪:收敛到正确的两档后总和仍然是 130%)。真正的不变量是基数 ——
// 活的被认领 trailing 单数 ≤ 当前档数,且不存在"活着、被认领、却不属于任何当前档"的单。
// ============================================================================

const (
	ethSymbol = "ETHUSDT"
	ethSide   = "short"
	ethEntry  = 1890.56

	ethOIDStaleATR   = "3784785480328314880"
	ethOIDStaleMult  = "3784966212015259648"
	ethOIDCurrent    = "3784969143967977472"
	ethOIDPartialCur = "3784966244730830848"
)

type ethExposureFixture struct {
	at          *AutoTrader
	st          *store.Store
	currentFull store.DrawdownTakeProfitRule
	currentPart store.DrawdownTakeProfitRule
	staleATR    store.DrawdownTakeProfitRule
	staleMult   store.DrawdownTakeProfitRule
	openOrders  []OpenOrder
}

func newETHExposureFixture(t *testing.T) *ethExposureFixture {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "exposure.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	f := &ethExposureFixture{
		at: &AutoTrader{id: "trader-exp", exchangeID: "exch-exp", store: st, exchange: "okx"},
		st: st,
		// 当前生效的两档(ATR 16.176366 / 2.5×,1.5× 与 3.4226/1.5401)。
		currentFull: store.DrawdownTakeProfitRule{MinProfitPct: 2.1391, MaxDrawdownPct: 1.2835, CloseRatioPct: 100, StageName: "dd1"},
		currentPart: store.DrawdownTakeProfitRule{MinProfitPct: 3.4226, MaxDrawdownPct: 1.5401, CloseRatioPct: 30, StageName: "partial_profit_lock"},
		// 泄漏的两档:同一个 stage,但解析值来自更早的 ATR / 更早的 maxDrawdown 倍数。
		staleATR:  store.DrawdownTakeProfitRule{MinProfitPct: 2.5669, MaxDrawdownPct: 1.0268, CloseRatioPct: 100, StageName: "dd1"},
		staleMult: store.DrawdownTakeProfitRule{MinProfitPct: 2.1391, MaxDrawdownPct: 0.7701, CloseRatioPct: 100, StageName: "dd1"},
	}
	f.openOrders = []OpenOrder{
		f.order(ethOIDStaleATR, 1842.030903170205, 0.010268),
		f.order(ethOIDStaleMult, 1850.1190859751707, 0.007701),
		f.order(ethOIDCurrent, 1850.1190859751707, 0.012835),
		f.order(ethOIDPartialCur, 1825.8545375602732, 0.015401),
	}
	return f
}

func (f *ethExposureFixture) order(oid string, act, cb float64) OpenOrder {
	return OpenOrder{
		OrderID: oid, Symbol: ethSymbol, PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET",
		ActivationStatus: "pending", StopPrice: act, CallbackRate: cb,
	}
}

func (f *ethExposureFixture) seed(t *testing.T, ptype string, rule store.DrawdownTakeProfitRule, oid string, act, cb float64, upd int64) store.DynamicProtectionRecord {
	t.Helper()
	r := store.DynamicProtectionRecord{
		TraderID: f.at.id, ExchangeID: f.at.exchangeID, Symbol: ethSymbol, Side: ethSide,
		PositionFingerprint: positionFingerprint(ethEntry, 0.052), ProtectionType: ptype,
		RuleFingerprint: stableDrawdownRuleFingerprint(ethEntry, rule), CloseRatioPct: rule.CloseRatioPct,
		Status: "armed", ExchangeOrderID: oid, ActivationPrice: act, CallbackRatio: cb, UpdatedAt: upd,
	}
	r.Key = store.BuildDynamicProtectionKey(r.TraderID, r.ExchangeID, r.Symbol, r.Side, r.PositionFingerprint, r.ProtectionType, r.RuleFingerprint, r.CloseRatioPct)
	if err := f.st.SaveDynamicProtectionRecord(r); err != nil {
		t.Fatalf("seed record: %v", err)
	}
	return r
}

func (f *ethExposureFixture) seedLiveState(t *testing.T) {
	t.Helper()
	f.seed(t, "native_trailing", f.staleATR, ethOIDStaleATR, 1842.030903170205, 0.010268, 1785297797142)
	f.seed(t, "native_trailing", f.staleMult, ethOIDStaleMult, 1850.1190859751707, 0.007701, 1785303183421)
	f.seed(t, "native_trailing", f.currentFull, ethOIDCurrent, 1850.1190859751707, 0.012835, 1785303270750)
	f.seed(t, "native_partial_trailing", f.currentPart, ethOIDPartialCur, 1825.8545375602732, 0.015401, 1785303185731)
}

func (f *ethExposureFixture) armedByOrder(t *testing.T) map[string]store.DynamicProtectionRecord {
	t.Helper()
	state, err := f.st.LoadDynamicProtectionState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	out := map[string]store.DynamicProtectionRecord{}
	for _, r := range state.Records {
		if r.Status == "armed" && r.ExchangeOrderID != "" {
			out[r.ExchangeOrderID] = r
		}
	}
	return out
}

func (f *ethExposureFixture) currentTiers() []store.DrawdownTakeProfitRule {
	return canonicalDrawdownTierOrder([]store.DrawdownTakeProfitRule{f.currentFull, f.currentPart})
}

// 决定性回归:线上 330% 必须收敛到 130%(当前两档),且退役的**只是**泄漏的那两条。
// 去掉 resolveOverClaimedTrailingExposure 的调用后本用例转红。
func TestOverClaimedExposure_LiveETHStateConvergesToCurrentTiers(t *testing.T) {
	f := newETHExposureFixture(t)
	f.seedLiveState(t)

	retired := f.at.resolveOverClaimedTrailingExposure(ethSymbol, ethSide, ethEntry, f.openOrders, f.currentTiers())
	if retired != 2 {
		t.Fatalf("retired %d over-claimed records, want 2 (the two leaked full-close tiers)", retired)
	}

	armed := f.armedByOrder(t)
	for _, oid := range []string{ethOIDCurrent, ethOIDPartialCur} {
		if _, ok := armed[oid]; !ok {
			t.Fatalf("order %s lost its claim — it matches a CURRENT tier and must keep it, "+
				"otherwise the reconciler will cancel a correct live protective order", oid)
		}
	}
	for _, oid := range []string{ethOIDStaleATR, ethOIDStaleMult} {
		if _, ok := armed[oid]; ok {
			t.Fatalf("order %s still claimed — its ledger claim keeps shielding it from stale-order "+
				"cleanup, so three full-close trails stay live and the tightest stale callback closes "+
				"the whole position earlier than any configured tier intends", oid)
		}
	}

	// 基数不变量:活的被认领单数必须收敛到 ≤ 当前档数(2)。
	if len(armed) > len(f.currentTiers()) {
		t.Fatalf("%d live claimed trailing orders remain for %d configured tiers — cardinality "+
			"invariant still violated", len(armed), len(f.currentTiers()))
	}
	// 只有一张全平单:多张同时在场时最紧的那张会抢先平掉整个仓位。
	fullCloses := 0
	for _, r := range armed {
		if r.CloseRatioPct >= 100 {
			fullCloses++
		}
	}
	if fullCloses != 1 {
		t.Fatalf("%d full-close trailing orders still claimed, want exactly 1", fullCloses)
	}
}

// 正常的两档(100% 全平 + 30% 部分)一条都不许动 —— 这正是第一版"总和 ≤ 100%"实现
// 会误伤的场景,锁死它。
func TestOverClaimedExposure_NormalTwoTierPositionIsNeverTouched(t *testing.T) {
	f := newETHExposureFixture(t)
	f.seed(t, "native_trailing", f.currentFull, ethOIDCurrent, 1850.1190859751707, 0.012835, 1785303270750)
	f.seed(t, "native_partial_trailing", f.currentPart, ethOIDPartialCur, 1825.8545375602732, 0.015401, 1785303185731)

	live := []OpenOrder{
		f.order(ethOIDCurrent, 1850.1190859751707, 0.012835),
		f.order(ethOIDPartialCur, 1825.8545375602732, 0.015401),
	}
	if retired := f.at.resolveOverClaimedTrailingExposure(ethSymbol, ethSide, ethEntry, live, f.currentTiers()); retired != 0 {
		t.Fatalf("retired %d records on a perfectly normal 100%%+30%% two-tier position — the close "+
			"ratios summing past 100%% is BY DESIGN (partial tier fires first, full tier takes the rest)", retired)
	}
	if got := len(f.armedByOrder(t)); got != 2 {
		t.Fatalf("armed claims = %d, want both tiers intact", got)
	}
}

// 已消失的单不占基数:账本里有两条记录,交易所上只剩一张,不算越界。
func TestOverClaimedExposure_VanishedOrderDoesNotCountTowardCardinality(t *testing.T) {
	f := newETHExposureFixture(t)
	f.seed(t, "native_trailing", f.currentFull, ethOIDCurrent, 1850.1190859751707, 0.012835, 1785303270750)
	f.seed(t, "native_trailing", f.staleATR, ethOIDStaleATR, 1842.030903170205, 0.010268, 1785297797142)

	live := []OpenOrder{f.order(ethOIDCurrent, 1850.1190859751707, 0.012835)}
	if retired := f.at.resolveOverClaimedTrailingExposure(ethSymbol, ethSide, ethEntry, live, f.currentTiers()); retired != 0 {
		t.Fatalf("retired %d records while only one order was actually live — a vanished order does "+
			"not occupy a tier slot, and touching claims here would churn correct protection", retired)
	}
	if _, ok := f.armedByOrder(t)[ethOIDCurrent]; !ok {
		t.Fatal("the current tier's live claim was dropped even though nothing was over-claimed")
	}
}

// 同一档在**解析值完全相同**时重复武装到另一张单上,账本 key 相同 → 新记录直接覆盖旧的,
// 旧单因此变成"无人认领",自然落进 reconciler 既有的 stale trailing 清理路径。
// 这条特性是本不变量成立的前提之一(所以同档分叉只可能在解析值不同时出现),锁死它。
func TestOverClaimedExposure_IdenticalTierRearmOverwritesTheLedgerKey(t *testing.T) {
	f := newETHExposureFixture(t)
	f.seed(t, "native_trailing", f.currentFull, ethOIDCurrent, 1850.1190859751707, 0.012835, 1785303270750)
	f.seed(t, "native_trailing", f.currentFull, ethOIDStaleMult, 1850.1190859751707, 0.012835, 1785303280750)

	armed := f.armedByOrder(t)
	if len(armed) != 1 {
		t.Fatalf("armed claims = %d, want 1 — an identical-tier re-arm must overwrite the same ledger "+
			"key, leaving the previous order unclaimed so cleanup can reach it", len(armed))
	}
	if _, ok := armed[ethOIDStaleMult]; !ok {
		t.Fatal("the newer arm did not take over the ledger key")
	}
}

// 同档被两张单在容差内同时认下(解析值有微小差异 → key 不同,但形状都对得上当前档):
// 一条都不许退役 —— 挑错会摘掉正在生效那张单的屏蔽。函数只喊,不动手。
func TestOverClaimedExposure_SameTierForkIsReportedNotGuessed(t *testing.T) {
	f := newETHExposureFixture(t)
	// 分叉档:minProfit 相同(activation 一致),maxDrawdown 只差 0.0065 →
	// fingerprint(4 位小数)不同,但 callback 差值远小于 0.00025 的匹配容差。
	forked := store.DrawdownTakeProfitRule{MinProfitPct: 2.1391, MaxDrawdownPct: 1.2900, CloseRatioPct: 100, StageName: "dd1"}
	act := calculateProfitBasedTrailingTriggerPrice(ethEntry, ethSide, f.currentFull.MinProfitPct)
	cbCur := calculateDrawdownRuleCallbackRatio(ethEntry, ethSide, f.currentFull)
	cbFork := calculateDrawdownRuleCallbackRatio(ethEntry, ethSide, forked)
	if diff := cbFork - cbCur; diff > 0.00025 || diff < -0.00025 {
		t.Fatalf("fixture invalid: callback diff %.6f exceeds the match tolerance, the forked order "+
			"would not be recognized by the current tier", diff)
	}

	f.seed(t, "native_trailing", f.currentFull, ethOIDCurrent, act, cbCur, 1785303270750)
	f.seed(t, "native_trailing", forked, ethOIDStaleMult, act, cbFork, 1785303280750)
	if got := len(f.armedByOrder(t)); got != 2 {
		t.Fatalf("fixture invalid: %d armed records, want 2 distinct ledger keys", got)
	}

	live := []OpenOrder{f.order(ethOIDCurrent, act, cbCur), f.order(ethOIDStaleMult, act, cbFork)}
	ordered := canonicalDrawdownTierOrder([]store.DrawdownTakeProfitRule{f.currentFull})
	if retired := f.at.resolveOverClaimedTrailingExposure(ethSymbol, ethSide, ethEntry, live, ordered); retired != 0 {
		t.Fatalf("retired %d records for a same-tier fork — both orders answer to the same tier within "+
			"tolerance, so picking one would unshield the working order; this must only be reported", retired)
	}
	if got := len(f.armedByOrder(t)); got != 2 {
		t.Fatalf("armed claims = %d, want both left intact for the same-tier fork path", got)
	}
}

// 拿不到当前档位集时保留现状:没有事实来源时猜着退役保护单,比多留一条记录危险得多。
func TestOverClaimedExposure_EmptyTierSetIsNeverTreatedAsEvidence(t *testing.T) {
	f := newETHExposureFixture(t)
	f.seedLiveState(t)
	if retired := f.at.resolveOverClaimedTrailingExposure(ethSymbol, ethSide, ethEntry, f.openOrders, nil); retired != 0 {
		t.Fatalf("retired %d records with NO current tier set available — with no ground truth the "+
			"only safe action is to leave every live claim alone", retired)
	}
	if got := len(f.armedByOrder(t)); got != 4 {
		t.Fatalf("armed claims = %d, want all 4 left intact", got)
	}
}

// 基数门禁存在的真正理由:ATR 重解析的瞬时窗口。此刻当前档的解析值刚变,交易所上那张
// 按旧值挂的单形状**暂时**对不上任何当前档 —— 但单数并没有超过档数,说明没有多挂出单,
// 没有实际伤害。此时必须什么都不做,等 reconciler 按既有路径替换;若在这里就摘掉它的
// 屏蔽,正在生效的唯一一张保护单会被撤,仓位裸奔到下一次武装成功。
//
// 去掉 resolveOverClaimedTrailingExposure 里两道 len(...) <= len(ordered) 门禁后本用例转红。
func TestOverClaimedExposure_ATRReresolutionWindowMustNotUnshieldTheOnlyLiveOrder(t *testing.T) {
	f := newETHExposureFixture(t)
	// 账本里两条(旧档 + 新档),但交易所上只有按**旧**值挂的那两张,形状都对不上新档。
	f.seed(t, "native_trailing", f.staleMult, ethOIDStaleMult, 1850.1190859751707, 0.007701, 1785303183421)
	f.seed(t, "native_partial_trailing", f.currentPart, ethOIDPartialCur, 1825.8545375602732, 0.015401, 1785303185731)

	// 当前档位集刚被重解析成两档新值,单数(2)== 档数(2),没有越界。
	live := []OpenOrder{
		f.order(ethOIDStaleMult, 1850.1190859751707, 0.007701),
		f.order(ethOIDPartialCur, 1825.8545375602732, 0.015401),
	}
	if retired := f.at.resolveOverClaimedTrailingExposure(ethSymbol, ethSide, ethEntry, live, f.currentTiers()); retired != 0 {
		t.Fatalf("retired %d records during an ATR re-resolution window — order count never exceeded "+
			"tier count, so nothing was over-claimed; unshielding here cancels the only live protective "+
			"order and leaves the position naked until the next arm succeeds", retired)
	}
	if got := len(f.armedByOrder(t)); got != 2 {
		t.Fatalf("armed claims = %d, want both left intact through the re-resolution window", got)
	}
}

// 已激活的单靠 clientOrderID 里的档位标识认领:形状匹配在激活后必然失配,若只有形状
// 一条判据,一张**正确的**已激活单会被判成泄漏并丢掉认领。
func TestOverClaimedExposure_ActivatedOrderIsKeptByItsTierTag(t *testing.T) {
	f := newETHExposureFixture(t)
	f.seedLiveState(t)

	orders := make([]OpenOrder, len(f.openOrders))
	copy(orders, f.openOrders)
	for i := range orders {
		if orders[i].OrderID == ethOIDCurrent {
			// 已激活:StopPrice 变成移动中的跟踪价,callback 不再可比。
			orders[i].ActivationStatus = "activated"
			orders[i].StopPrice = 1861.44
			orders[i].CallbackRate = 0
			orders[i].ProtectionTier = 1 // 挂单时编进 clientOrderID 的档位标识
		}
	}

	if retired := f.at.resolveOverClaimedTrailingExposure(ethSymbol, ethSide, ethEntry, orders, f.currentTiers()); retired != 2 {
		t.Fatalf("retired %d, want 2 (the leaked tiers only)", retired)
	}
	if _, ok := f.armedByOrder(t)[ethOIDCurrent]; !ok {
		t.Fatal("the ACTIVATED current-tier order lost its claim — shape matching cannot work after " +
			"activation, so the tier tag must carry it; without this a live, working trail gets cancelled")
	}
}

// 破坏性:敌意档位标识(超出当前档数)不得成为保留依据,否则伪造/陈旧的标识可以让任何
// 泄漏单永久免疫清理。
func TestOverClaimedExposure_OutOfRangeTierTagIsNotEvidence(t *testing.T) {
	f := newETHExposureFixture(t)
	f.seedLiveState(t)

	orders := make([]OpenOrder, len(f.openOrders))
	copy(orders, f.openOrders)
	for i := range orders {
		if orders[i].OrderID == ethOIDStaleATR {
			orders[i].ActivationStatus = "activated"
			orders[i].CallbackRate = 0
			orders[i].ProtectionTier = 7 // 当前只有 2 档
		}
	}

	if retired := f.at.resolveOverClaimedTrailingExposure(ethSymbol, ethSide, ethEntry, orders, f.currentTiers()); retired != 2 {
		t.Fatalf("retired %d, want 2 — an out-of-range tier tag must not immunize a leaked order", retired)
	}
	if _, ok := f.armedByOrder(t)[ethOIDStaleATR]; ok {
		t.Fatal("leaked order kept its claim on the strength of a tier tag pointing at a tier that " +
			"does not exist")
	}
}
