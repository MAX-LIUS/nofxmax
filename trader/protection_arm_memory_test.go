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
// 两条防线各钉一组用例:①失败不擦记忆(去掉入口);②账本+交易所双重实证可恢复记忆
// (进程重启同样丢内存 map,每次部署都会踩,所以必须能自愈)。

// ① 对账失败不得擦掉动态武装记忆。
func TestProtectionStateAfterReconcileFailureKeepsDynamicArmMemory(t *testing.T) {
	boom := errAfterReconcile("re-apply manual protection plan: ladder placement rejected 51279")
	for _, tc := range []struct {
		name     string
		previous string
		want     string
	}{
		{"native 全平档武装中不得被覆盖", "native_trailing_armed", ""},
		{"native 部分档武装中不得被覆盖", "native_partial_trailing_armed", ""},
		{"native arming 中同样不得被覆盖", "native_trailing_arming", ""},
		{"managed 武装记忆同样是唯一进度记忆", "managed_drawdown_armed", ""},
		{"交易所已校验不是武装记忆,可以覆盖", "exchange_protection_verified", "reconcile_failed: " + boom.Error()},
		{"空状态照常写入失败原因", "", "reconcile_failed: " + boom.Error()},
	} {
		if got := protectionStateAfterReconcileFailure(tc.previous, boom); got != tc.want {
			t.Fatalf("%s: previous=%q got=%q want=%q", tc.name, tc.previous, got, tc.want)
		}
	}
	if got := protectionStateAfterReconcileFailure("", nil); got != "" {
		t.Fatalf("没有错误就不该写状态,got %q", got)
	}
}

type errAfterReconcile string

func (e errAfterReconcile) Error() string { return string(e) }

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
