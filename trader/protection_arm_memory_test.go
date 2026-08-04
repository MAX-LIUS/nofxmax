package trader

import (
	"path/filepath"
	"sync"
	"testing"

	"nofx/store"
)

// 动态保护的"武装记忆"活在 AutoTrader 的**内存 map** protectionState 里,而所有权账本
// 是持久化的。两者一旦不一致就会死锁,而且是**静默**的死锁:
//
//	账本 armed  → 武装路径判"已 armed,跳过重新武装"
//	内存 false  → classifyTrailing 第一行就 return false,在场的 trailing 单
//	              每轮被记成 staleTrail → state=degraded → reclaim 重写账本
//	              → 下一轮一模一样。谁都不会去修对方。
//
// 2026-08-03 线上 SOLUSDT long(claude-hhhl, OKX)完整链条:
//
//	22:57:07  qty resize 误撤邻档 TP(见 protection_qty_resize.go 的 SOL 证据)
//	22:59:09  plan 重挂被 OKX 拒(51279)→ reconciler 把这一格写成
//	          "reconcile_failed: ..." → 覆盖掉 native_trailing_armed
//	23:01:47  起 438 轮 degraded + 433 次 reclaim,持续 2.5 小时未收敛,
//	          期间面板一直红,而交易所侧 5 张保护单其实都在场。
//
// 两条防线各钉一组用例:①任何非武装写入都擦不掉武装记忆(不变量钉在存取器上,对所有
// 写入点一次成立);②账本+交易所双重实证可恢复记忆(进程重启同样丢内存 map,每次部署
// 都会踩,所以必须能自愈)。

// ① 观测类写入不得擦掉动态武装记忆 —— 钉在存取器层,覆盖所有写入点。
//
// 以前这条不变量钉在 protectionStateAfterReconcileFailure 这一个写入点上,于是只有
// "对账失败"这一条路被保护;exchange_protection_verified 那条路靠另一份手写并集,
// 而那份并集漏掉过 managed_drawdown_armed。现在归属判定集中在
// classifyProtectionStateWrite,任何写入点都自动受保护。
func TestObservationWritesNeverEraseArmMemory(t *testing.T) {
	boom := "reconcile_failed: re-apply manual protection plan: ladder placement rejected 51279"
	armStates := []string{
		"native_trailing_armed",
		"native_partial_trailing_armed",
		"native_trailing_arming",
		"native_partial_trailing_arming",
		"managed_drawdown_armed",
		"managed_partial_drawdown_armed",
		"managed_drawdown_exchange_failed_armed",
		"managed_partial_drawdown_exchange_failed_armed",
	}
	observations := []string{boom, "exchange_protection_verified"}
	for _, arm := range armStates {
		for _, obs := range observations {
			at := &AutoTrader{protectionStateMutex: sync.RWMutex{}}
			at.setProtectionState("SOLUSDT", "long", arm)
			at.setProtectionState("SOLUSDT", "long", obs)
			if got := at.getProtectionState("SOLUSDT", "long"); got != arm {
				t.Fatalf("arm=%q 被观测 %q 覆盖成 %q", arm, obs, got)
			}
			if got := at.getProtectionArmState("SOLUSDT", "long"); got != arm {
				t.Fatalf("arm=%q 写观测 %q 后武装维度变成 %q", arm, obs, got)
			}
		}
	}
}

// 观测维度自身要能被读到:武装维度为空时,getProtectionState 返回观测值 ——
// giveback_guard 的 state == "exchange_protection_verified" 判断依赖这一点。
func TestObservationVisibleWhenNoArmState(t *testing.T) {
	at := &AutoTrader{protectionStateMutex: sync.RWMutex{}}
	at.setProtectionState("BTCUSDT", "long", "exchange_protection_verified")
	if got := at.getProtectionState("BTCUSDT", "long"); got != "exchange_protection_verified" {
		t.Fatalf("未武装时应看到观测值,got %q", got)
	}
	// 对账失败时同理:未武装的仓位必须能看到失败原因,否则面板/守卫失去唯一信号。
	at.setProtectionState("BTCUSDT", "long", "reconcile_failed: boom")
	if got := at.getProtectionState("BTCUSDT", "long"); got != "reconcile_failed: boom" {
		t.Fatalf("未武装时应看到对账失败,got %q", got)
	}
}

// drawdown_triggered* 是唯一"观测且必须同时清掉武装记忆"的语义:managed 已经平过仓,
// 仓位数量变了,原武装记忆指向的仓位指纹不再成立,留着会骗过重新武装的门禁。
func TestDrawdownTriggeredResetsArmMemory(t *testing.T) {
	for _, triggered := range []string{"drawdown_triggered", "drawdown_triggered_dd1"} {
		at := &AutoTrader{protectionStateMutex: sync.RWMutex{}}
		at.setProtectionState("ETHUSDT", "short", "native_trailing_armed")
		at.setProtectionState("ETHUSDT", "short", triggered)
		if got := at.getProtectionArmState("ETHUSDT", "short"); got != "" {
			t.Fatalf("%s 之后武装维度应清空,got %q", triggered, got)
		}
		if got := at.getProtectionState("ETHUSDT", "short"); got != triggered {
			t.Fatalf("%s 之后应看到该观测,got %q", triggered, got)
		}
	}
}

// 空串是整格重置:两个维度都清。clearProtectionState 同理。
func TestEmptyWriteAndClearResetBothDimensions(t *testing.T) {
	for name, reset := range map[string]func(*AutoTrader){
		"空串写入": func(at *AutoTrader) { at.setProtectionState("XRPUSDT", "long", "") },
		"显式清空": func(at *AutoTrader) { at.clearProtectionState("XRPUSDT", "long") },
	} {
		at := &AutoTrader{protectionStateMutex: sync.RWMutex{}}
		at.setProtectionState("XRPUSDT", "long", "native_trailing_armed")
		at.setProtectionState("XRPUSDT", "long", "exchange_protection_verified")
		reset(at)
		if got := at.getProtectionState("XRPUSDT", "long"); got != "" {
			t.Fatalf("%s 之后应两维度全空,got %q", name, got)
		}
		if got := at.getProtectionArmState("XRPUSDT", "long"); got != "" {
			t.Fatalf("%s 之后武装维度应空,got %q", name, got)
		}
	}
}

// solArmMemoryTrader 复刻线上 SOL 的账本:dd1 全平档 armed,认领 ...704。
func solArmMemoryTrader(t *testing.T, name string, records ...store.DynamicProtectionRecord) *AutoTrader {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	at := &AutoTrader{id: "claude", store: st, protectionStateMutex: sync.RWMutex{}}
	for _, r := range records {
		if err := st.SaveDynamicProtectionRecord(r); err != nil {
			t.Fatalf("seed record: %v", err)
		}
	}
	return at
}

func solDD1Record(protectionType, orderID string) store.DynamicProtectionRecord {
	return store.DynamicProtectionRecord{
		TraderID:            "claude",
		Symbol:              "SOLUSDT",
		Side:                "long",
		PositionFingerprint: "73.48000000|0.12000000",
		PositionCreatedTime: 1785768513695,
		ProtectionType:      protectionType,
		RuleFingerprint:     "73.48000000|0.00000000|1.1195|0.6717|100.0000|dd1|0.0000|break_even|||",
		CloseRatioPct:       100,
		Status:              "armed",
		ExchangeOrderID:     orderID,
	}
}

func solTrailingOpenOrder(orderID string) OpenOrder {
	return OpenOrder{OrderID: orderID, Symbol: "SOLUSDT", Type: "TRAILING_STOP_MARKET", PositionSide: "LONG", Quantity: 0.12}
}

// ② 账本 armed + 交易所在场 ⇒ 恢复武装记忆(重启/被覆盖后自愈)。
func TestRehydrateNativeTrailingStateFromLedgerAndExchange(t *testing.T) {
	at := solArmMemoryTrader(t, "sol-rehydrate.db", solDD1Record("native_trailing", "3800580264636616704"))
	got := at.rehydratedNativeTrailingState("SOLUSDT", "long", 73.48, 0.12,
		[]OpenOrder{solTrailingOpenOrder("3800580264636616704")})
	if got != "native_trailing_armed" {
		t.Fatalf("账本认领的单在交易所在场,必须恢复 native_trailing_armed,got %q", got)
	}
}

// ②-a 只有账本、交易所没有那张单 ⇒ 绝不恢复。
// 这条比死锁更重要:陈旧 armed 记录一旦能恢复状态,missingTP 会被永久掩蔽,
// 该补的止盈永远不补 —— 那是真正的裸奔。
func TestRehydrateRefusesWithoutLiveExchangeOrder(t *testing.T) {
	at := solArmMemoryTrader(t, "sol-no-live.db", solDD1Record("native_trailing", "3800580264636616704"))
	for _, tc := range []struct {
		name  string
		order []OpenOrder
	}{
		{"交易所空挂单", nil},
		{"在场单是别的 orderID(原单已成交/被撤)", []OpenOrder{solTrailingOpenOrder("9999999999999999999")}},
		{"在场单不是 trailing 类型", []OpenOrder{{OrderID: "3800580264636616704", Symbol: "SOLUSDT", Type: "STOP_MARKET", PositionSide: "LONG"}}},
	} {
		if got := at.rehydratedNativeTrailingState("SOLUSDT", "long", 73.48, 0.12, tc.order); got != "" {
			t.Fatalf("%s: 无双重实证不得恢复,got %q", tc.name, got)
		}
	}
}

// ②-b 账本里那条不是 armed(已 superseded/已执行) ⇒ 不恢复。
func TestRehydrateRefusesForNonArmedRecord(t *testing.T) {
	rec := solDD1Record("native_trailing", "3800580264636616704")
	rec.Status = "superseded"
	at := solArmMemoryTrader(t, "sol-superseded.db", rec)
	if got := at.rehydratedNativeTrailingState("SOLUSDT", "long", 73.48, 0.12,
		[]OpenOrder{solTrailingOpenOrder("3800580264636616704")}); got != "" {
		t.Fatalf("非 armed 记录不得恢复武装记忆,got %q", got)
	}
}

// ②-c 部分档单独在场 ⇒ 恢复 partial;与全平档并存时全平档优先(结果必须稳定,
// 不能随 map 遍历顺序抖动)。
func TestRehydratePrefersFullCloseTierDeterministically(t *testing.T) {
	partial := solDD1Record("native_partial_trailing", "3800580264636616704")
	partial.CloseRatioPct = 40
	partial.RuleFingerprint = "73.48000000|0.00000000|1.1195|0.6717|40.0000|dd1|0.0000|break_even|||"
	at := solArmMemoryTrader(t, "sol-partial.db", partial)
	if got := at.rehydratedNativeTrailingState("SOLUSDT", "long", 73.48, 0.12,
		[]OpenOrder{solTrailingOpenOrder("3800580264636616704")}); got != "native_partial_trailing_armed" {
		t.Fatalf("只有部分档在场时应恢复 partial,got %q", got)
	}

	both := solArmMemoryTrader(t, "sol-both.db", partial, solDD1Record("native_trailing", "3800580264636616705"))
	for i := 0; i < 20; i++ {
		got := both.rehydratedNativeTrailingState("SOLUSDT", "long", 73.48, 0.12,
			[]OpenOrder{solTrailingOpenOrder("3800580264636616704"), solTrailingOpenOrder("3800580264636616705")})
		if got != "native_trailing_armed" {
			t.Fatalf("两档并存时必须稳定优先全平档,第 %d 次得到 %q", i, got)
		}
	}
}

// ③ 同档同单的重复 armed 记录必须收敛。
//
// 线上形态:同一张 ...704 被两条 native_trailing armed 记录认领,规则身份完全相同,
// 只差 position_fingerprint 里的仓位数量(0.42 开仓量 vs 0.12 部分平仓后)——
// 因为账本键含仓位数量,部分平仓后 reclaim 写的是**新键**,旧记录留在原地。
//
// 以前 supersedeOlderArmedRecords 在"同一 orderID"分支无条件 continue,
// 而 supersedeConflictingOrderClaim 在"同档"时第一时间 return:两条路互相推诿。
func TestSameTierDuplicateArmedRecordsConvergeByTime(t *testing.T) {
	stale := solDD1Record("native_trailing", "3800580264636616704")
	stale.PositionFingerprint = "73.48000000|0.42000000"
	stale.UpdatedAt = 1785768518734 // 22:48:38 初次武装
	fresh := solDD1Record("native_trailing", "3800580264636616704")
	fresh.UpdatedAt = 1785777468631 // 01:17:48 最近一轮 reclaim
	at := solArmMemoryTrader(t, "sol-dup.db", stale, fresh)

	at.supersedeOlderArmedRecords(fresh)

	state, err := at.store.LoadDynamicProtectionState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	var armed, superseded int
	for _, rec := range state.Records {
		if rec.ProtectionType != "native_trailing" {
			continue
		}
		switch rec.Status {
		case "armed":
			armed++
			if rec.PositionFingerprint != "73.48000000|0.12000000" {
				t.Fatalf("留下的应是最新那条,got posFP=%s", rec.PositionFingerprint)
			}
		case "superseded":
			superseded++
		}
	}
	if armed != 1 || superseded != 1 {
		t.Fatalf("同档同单必须收敛到 1 条 armed + 1 条 superseded,got armed=%d superseded=%d", armed, superseded)
	}

	// 幂等:再跑一遍不得把仅剩的那条也退役 —— 那张活单会变成零主人。
	at.supersedeOlderArmedRecords(fresh)
	state, _ = at.store.LoadDynamicProtectionState()
	armed = 0
	for _, rec := range state.Records {
		if rec.ProtectionType == "native_trailing" && rec.Status == "armed" {
			armed++
		}
	}
	if armed != 1 {
		t.Fatalf("重复收敛后活单必须仍有恰好 1 个 armed 主人,got %d", armed)
	}
}

// ③-a 真正的跨档冲突仍由 supersedeConflictingOrderClaim 处理,且绝不能把两条都退役。
func TestCrossTierConflictLeavesExactlyOneOwner(t *testing.T) {
	dd1 := solDD1Record("native_trailing", "3800580264636616704")
	dd1.UpdatedAt = 1785768518734
	dd2 := solDD1Record("native_partial_trailing", "3800580264636616704")
	dd2.RuleFingerprint = "73.48000000|0.00000000|2.2390|0.6717|40.0000|dd2|0.0000|break_even|||"
	dd2.CloseRatioPct = 40
	dd2.UpdatedAt = 1785777468631
	at := solArmMemoryTrader(t, "sol-cross.db", dd1, dd2)

	at.supersedeOlderArmedRecords(dd2)

	state, err := at.store.LoadDynamicProtectionState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	armed := 0
	for _, rec := range state.Records {
		if rec.Status == "armed" && rec.ExchangeOrderID == "3800580264636616704" {
			armed++
		}
	}
	if armed != 1 {
		t.Fatalf("一张活单必须恰好一个 armed 主人,got %d", armed)
	}
}

// ②-d 别的 trader 的记录不得被本 trader 拿去恢复状态。
func TestRehydrateIgnoresOtherTradersRecords(t *testing.T) {
	rec := solDD1Record("native_trailing", "3800580264636616704")
	rec.TraderID = "someone-else"
	at := solArmMemoryTrader(t, "sol-foreign.db", rec)
	if got := at.rehydratedNativeTrailingState("SOLUSDT", "long", 73.48, 0.12,
		[]OpenOrder{solTrailingOpenOrder("3800580264636616704")}); got != "" {
		t.Fatalf("跨 trader 记录不得恢复武装记忆,got %q", got)
	}
}
