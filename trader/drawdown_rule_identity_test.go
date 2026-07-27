package trader

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"nofx/store"
)

// TestDrawdownRuleIdentityIgnoresEntryDrift 是身份层的单元约束:同一档梯度在不同开仓
// 均价下必须归一到同一个身份,不同档必须仍然分开。
//
// 线上取值来自 2026-07-27 的 SOXLUSDT:place-at-open 用计划价 149.09 武装,运行时用
// 交易所同步回来的真实成交均价 149.07246479 重新武装,于是两档策略挂出 4 张单。
func TestDrawdownRuleIdentityIgnoresEntryDrift(t *testing.T) {
	dd1 := store.DrawdownTakeProfitRule{MinProfitPct: 3, MaxDrawdownPct: 1.8, CloseRatioPct: 100, StageName: "dd1"}
	dd2 := store.DrawdownTakeProfitRule{MinProfitPct: 4, MaxDrawdownPct: 1.2, CloseRatioPct: 30, StageName: "dd2"}

	const plannedEntry = 149.09
	const filledEntry = 149.07246479

	planned := drawdownRuleIdentity(stableDrawdownRuleFingerprint(plannedEntry, dd1))
	filled := drawdownRuleIdentity(stableDrawdownRuleFingerprint(filledEntry, dd1))
	if planned != filled {
		t.Fatalf("同一档梯度在开仓均价 %.8f / %.8f 下身份不同:\n  %s\n  %s",
			plannedEntry, filledEntry, planned, filled)
	}

	// 反向:身份不能糊到把不同档并成一档,否则 dd2 会被当成"dd1 已武装"而永不挂单。
	if drawdownRuleIdentity(stableDrawdownRuleFingerprint(plannedEntry, dd2)) == planned {
		t.Fatal("dd1 与 dd2 的身份相同 —— 归一过度,会让第二档永远认为自己已武装")
	}

	// 数量也必须被抹平:部分平仓后重新武装时数量会变,历史记录里可能带着真实数量。
	if drawdownRuleIdentity(drawdownRuleFingerprint(plannedEntry, 2.43, dd1)) != planned {
		t.Fatal("身份受数量影响 —— 部分平仓后同一档会分叉")
	}

	// 字段数必须保持不变:auto_trader_risk.go 有两处按下标解析 parts[2] 取 MinProfitPct
	// (清理不可达记录 / 重启后重建防降级地板)。身份串若改变字段布局会静默读错档位。
	parts := strings.Split(planned, "|")
	if len(parts) != len(strings.Split(stableDrawdownRuleFingerprint(plannedEntry, dd1), "|")) {
		t.Fatalf("身份串字段数变了(%d),按下标解析 parts[2] 的代码会读错", len(parts))
	}
	got, err := strconv.ParseFloat(parts[2], 64)
	if err != nil || got != dd1.MinProfitPct {
		t.Fatalf("parts[2] 不再是 MinProfitPct:got %q err=%v", parts[2], err)
	}
}

// TestNativeTrailingArmKeyIsolatesPositions 钉住冷却 key 的隔离性。
//
// 身份不含开仓均价之后,"不同币种/不同方向"再也无法靠开仓价碰巧不同来区分 —— 冷却
// key 必须显式带 symbol|side,否则一个仓位武装完会把另一个仓位挡 300s。
func TestNativeTrailingArmKeyIsolatesPositions(t *testing.T) {
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 3, MaxDrawdownPct: 1.8, CloseRatioPct: 100, StageName: "dd1"}
	fpA := stableDrawdownRuleFingerprint(100, rule)
	fpB := stableDrawdownRuleFingerprint(100, rule) // 同策略、同开仓价:旧实现下 key 完全相同

	if nativeTrailingArmKey("BTCUSDT", "long", fpA) == nativeTrailingArmKey("ETHUSDT", "long", fpB) {
		t.Fatal("不同币种共用冷却 key —— 一个仓位武装会挡住另一个")
	}
	if nativeTrailingArmKey("BTCUSDT", "long", fpA) == nativeTrailingArmKey("BTCUSDT", "short", fpB) {
		t.Fatal("同币种 long/short 共用冷却 key —— 对冲模式下一边武装会挡住另一边")
	}
	// 而开仓均价漂移必须落在同一个桶里,冷却才真正起作用。
	if nativeTrailingArmKey("SOXLUSDT", "long", stableDrawdownRuleFingerprint(149.09, rule)) !=
		nativeTrailingArmKey("SOXLUSDT", "long", stableDrawdownRuleFingerprint(149.07246479, rule)) {
		t.Fatal("开仓均价漂移换了冷却桶 —— 300s 重复武装保险失效")
	}
}

// TestEntryDriftDoesNotForkTierIdentity 是端到端的那一条:记录以 A 价武装并存下
// orderID,运行时以 B 价来查同一档 —— 必须找回自己的单,而不是判定"缺单"再挂一张。
//
// 这是线上 SOXLUSDT 的真实形状:2 档策略 / 4 条 armed 记录 / 交易所 4 张挂单。
func TestEntryDriftDoesNotForkTierIdentity(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "entry-drift.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	const (
		traderID     = "trader-drift"
		symbol       = "SOXLUSDT"
		side         = "long"
		plannedEntry = 149.09       // place-at-open 武装时用的价
		filledEntry  = 149.07246479 // 交易所同步回来的真实成交均价
		qty          = 2.43
	)
	dd1 := store.DrawdownTakeProfitRule{MinProfitPct: 3, MaxDrawdownPct: 1.8, CloseRatioPct: 100, StageName: "dd1"}
	dd2 := store.DrawdownTakeProfitRule{MinProfitPct: 4, MaxDrawdownPct: 1.2, CloseRatioPct: 30, StageName: "dd2"}

	now := time.Now().UTC().UnixMilli()
	posFP := positionFingerprint(plannedEntry, qty)
	for _, r := range []store.DynamicProtectionRecord{
		{
			TraderID: traderID, ExchangeID: "exchange-1", Symbol: symbol, Side: side,
			PositionFingerprint: posFP, ProtectionType: "native_trailing",
			RuleFingerprint: stableDrawdownRuleFingerprint(plannedEntry, dd1), CloseRatioPct: 100,
			Status: "armed", ExchangeOrderID: "algo-dd1", UpdatedAt: now,
		},
		{
			TraderID: traderID, ExchangeID: "exchange-1", Symbol: symbol, Side: side,
			PositionFingerprint: posFP, ProtectionType: "native_partial_trailing",
			RuleFingerprint: stableDrawdownRuleFingerprint(plannedEntry, dd2), CloseRatioPct: 30,
			Status: "armed", ExchangeOrderID: "algo-dd2", UpdatedAt: now,
		},
	} {
		r.Key = store.BuildDynamicProtectionKey(r.TraderID, r.ExchangeID, r.Symbol, r.Side, r.PositionFingerprint, r.ProtectionType, r.RuleFingerprint, r.CloseRatioPct)
		if err := st.SaveDynamicProtectionRecord(r); err != nil {
			t.Fatalf("save record: %v", err)
		}
	}

	at := &AutoTrader{id: traderID, store: st, exchange: "okx"}

	// 运行时用真实成交均价来查两档的 orderID。开仓价 0.012% 的漂移落在
	// getArmedDrawdownRecordsForPosition 的 0.5% 容差内,所以记录本身能被认领 ——
	// 分叉只发生在**规则**身份这一层,这正是本用例要钉住的地方。
	if got := at.storedTrailingOrderIDForRule(symbol, side, filledEntry, dd1); got != "algo-dd1" {
		t.Fatalf("dd1 在修正后的开仓均价下找不回自己的单:got %q want algo-dd1 —— 会被判缺单并重复挂出", got)
	}
	if got := at.storedTrailingOrderIDForRule(symbol, side, filledEntry, dd2); got != "algo-dd2" {
		t.Fatalf("dd2 在修正后的开仓均价下找不回自己的单:got %q want algo-dd2", got)
	}

	// armedFingerprints 这一路也必须认得出来(它是"要不要挂新单"的门禁)。
	armed := at.getArmedDrawdownRuleFingerprintsForPosition(symbol, side, filledEntry, qty)
	for name, rule := range map[string]store.DrawdownTakeProfitRule{"dd1": dd1, "dd2": dd2} {
		id := drawdownRuleIdentity(stableDrawdownRuleFingerprint(filledEntry, rule))
		if _, ok := armed[id]; !ok {
			t.Fatalf("%s 在修正后的开仓均价下不在已武装集合里 —— 门禁会放行第二次挂单", name)
		}
	}

	// 反向:排除自己这一档时也必须按身份排除,否则本档会把自己上一次挂的单
	// 当成兄弟档的单保护起来,永远撤不掉。
	claimed := at.claimedTrailingOrderIDsForPosition(symbol, side, filledEntry, stableDrawdownRuleFingerprint(filledEntry, dd1))
	if _, ok := claimed["algo-dd1"]; ok {
		t.Fatal("按修正后的开仓均价排除 dd1 失败 —— dd1 自己的单被当成兄弟档保护起来了")
	}
	if _, ok := claimed["algo-dd2"]; !ok {
		t.Fatal("dd2 的单没有被认领 —— 会被当成孤儿单撤掉")
	}
}

// TestPositionIdentityDoesNotClobberExecutedGuard 钉住 drawdownState 一格两用的
// 静默失效:refreshDrawdownExecutionFingerprint 每轮 monitor 都在最前面跑,曾经往
// drawdownState 写仓位 cTime,把"这一档已执行过"的规则 fingerprint 覆盖掉,于是
// :469 的重复平仓门禁永远匹配不上。两个语义现在各自一格。
func TestPositionIdentityDoesNotClobberExecutedGuard(t *testing.T) {
	const (
		symbol = "BTCUSDT"
		side   = "long"
	)
	at := &AutoTrader{
		drawdownState:       make(map[string]string),
		drawdownPosIdentity: make(map[string]string),
		drawdownRunnerState: make(map[string]DrawdownRunnerState),
	}
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 3, MaxDrawdownPct: 1.8, CloseRatioPct: 50, StageName: "dd1"}
	fp := stableDrawdownRuleFingerprint(100, rule)

	const cTime int64 = 1784259700000
	at.refreshDrawdownExecutionFingerprint(symbol, side, cTime) // 首次见到该仓位
	at.setDrawdownExecutionFingerprint(symbol, side, fp)        // 该档已执行

	// 后续若干轮 monitor:仓位没变,已执行标记必须活着。
	for i := 0; i < 3; i++ {
		if changed := at.refreshDrawdownExecutionFingerprint(symbol, side, cTime); changed {
			t.Fatalf("poll %d: 同一仓位被误判为换仓", i)
		}
		if got := at.getDrawdownExecutionFingerprint(symbol, side); got != fp {
			t.Fatalf("poll %d: 已执行标记被仓位身份覆盖:got %q want %q —— 重复平仓门禁失效", i, got, fp)
		}
	}

	// 真的换了仓位:已执行标记必须一起清掉,否则新仓位第一档被旧记忆挡住不平。
	if changed := at.refreshDrawdownExecutionFingerprint(symbol, side, cTime+60_000); !changed {
		t.Fatal("cTime 变了却没检测到换仓")
	}
	if got := at.getDrawdownExecutionFingerprint(symbol, side); got != "" {
		t.Fatalf("换仓后旧的已执行标记还在:%q —— 新仓位这一档会被误判为已平过", got)
	}
}

// TestRestartDoesNotDisableManagedFallback 是"managed 一直陪跑、双保险"的重启侧约束。
//
// drawdownState 只有一个读者:auto_trader_risk.go:469 的"这一档已经平过了,别重复平"。
// 恢复流程曾经把 armed 的 native / armed 的 managed 记录也写进这一格 —— 于是重启后
// 交易所挂单万一失效(被撤/没成交/查不到),唯一的兜底执行器会因为"以为平过了"而
// 拒绝动手。这条用例把"armed 不进门禁、只有 executed 进门禁"钉死。
func TestRestartDoesNotDisableManagedFallback(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "restart-fallback.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	const traderID = "trader-restart"
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 3, MaxDrawdownPct: 1.8, CloseRatioPct: 50, StageName: "dd1"}
	fp := stableDrawdownRuleFingerprint(100, rule)

	for _, r := range []store.DynamicProtectionRecord{
		// 交易所挂单已武装(native)。
		{TraderID: traderID, ExchangeID: "e1", Symbol: "BTCUSDT", Side: "long", PositionFingerprint: positionFingerprint(100, 1),
			ProtectionType: "native_trailing", RuleFingerprint: fp, CloseRatioPct: 100, Status: "armed", ExchangeOrderID: "algo-1"},
		// managed 兜底已武装,但还没执行。
		{TraderID: traderID, ExchangeID: "e1", Symbol: "ETHUSDT", Side: "long", PositionFingerprint: positionFingerprint(200, 1),
			ProtectionType: "managed_drawdown", RuleFingerprint: fp, CloseRatioPct: 50, Status: "armed"},
		// managed 兜底确实执行过了 —— 只有这一条该挡住重复平仓。
		{TraderID: traderID, ExchangeID: "e1", Symbol: "SOLUSDT", Side: "long", PositionFingerprint: positionFingerprint(50, 1),
			ProtectionType: "managed_drawdown", RuleFingerprint: fp, CloseRatioPct: 50, Status: "executed"},
	} {
		r.Key = store.BuildDynamicProtectionKey(r.TraderID, r.ExchangeID, r.Symbol, r.Side, r.PositionFingerprint, r.ProtectionType, r.RuleFingerprint, r.CloseRatioPct)
		if err := st.SaveDynamicProtectionRecord(r); err != nil {
			t.Fatalf("save record: %v", err)
		}
	}

	at := &AutoTrader{id: traderID, store: st}
	at.loadDynamicProtectionStateFromStore()

	if got := at.getDrawdownExecutionFingerprint("BTCUSDT", "long"); got != "" {
		t.Fatalf("armed native 进了已执行门禁(%q)—— 重启后 managed 兜底被自己关掉", got)
	}
	if got := at.getDrawdownExecutionFingerprint("ETHUSDT", "long"); got != "" {
		t.Fatalf("armed managed 进了已执行门禁(%q)—— 兜底武装完就再也不会真的平仓", got)
	}
	if got := at.getDrawdownExecutionFingerprint("SOLUSDT", "long"); got != fp {
		t.Fatalf("executed managed 没有恢复(%q)—— 重启后会对同一档重复平仓", got)
	}
}

// TestArmCooldownClearedOnNewPosition:冷却 key 不再含开仓均价,所以不会自然过期 ——
// 开新仓时必须显式清掉,否则平仓后 300s 内在同一 symbol|side 重开,保护单挂不上去。
func TestArmCooldownClearedOnNewPosition(t *testing.T) {
	const (
		symbol = "SOXLUSDT"
		side   = "long"
	)
	at := &AutoTrader{
		peakPnLCache:          make(map[string]float64),
		troughPnLCache:        make(map[string]float64),
		peakAtrMultCache:      make(map[string]float64),
		troughAtrMultCache:    make(map[string]float64),
		gbPnlHist:             make(map[string][]float64),
		structSLFiredBar:      make(map[string]int64),
		protectionState:       make(map[string]string),
		breakEvenState:        make(map[string]string),
		breakEvenFingerprints: make(map[string]string),
		breakEvenSource:       make(map[string]string),
		drawdownState:         make(map[string]string),
		drawdownPosIdentity:   make(map[string]string),
		drawdownSource:        make(map[string]string),
		drawdownAIRules:       make(map[string][]store.DrawdownTakeProfitRule),
		drawdownRunnerState:   make(map[string]DrawdownRunnerState),
		drawdownTierAllocs:    make(map[string][]store.DrawdownTierAllocation),
		immediateTrailingIDs:  make(map[string]string),
		nativeTrailingArmTime: make(map[string]time.Time),
		reArmFailCache:        make(map[string]int),
		candidateATRCache:     make(map[string]float64),
		candidateATRAtMs:      make(map[string]int64),
	}
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 3, MaxDrawdownPct: 1.8, CloseRatioPct: 100, StageName: "dd1"}
	key := nativeTrailingArmKey(symbol, side, stableDrawdownRuleFingerprint(149.09, rule))
	other := nativeTrailingArmKey("ETHUSDT", "long", stableDrawdownRuleFingerprint(1943.65, rule))
	at.nativeTrailingArmTime[key] = time.Now()
	at.nativeTrailingArmTime[other] = time.Now()

	at.resetPerPositionStateOnOpen(symbol, side)

	if _, ok := at.nativeTrailingArmTime[key]; ok {
		t.Fatal("开新仓后旧的武装冷却还在 —— 新仓位 300s 内挂不上保护单")
	}
	if _, ok := at.nativeTrailingArmTime[other]; !ok {
		t.Fatal("清理波及了别的仓位 —— 那个仓位的重复武装保险被拆掉了")
	}
}
