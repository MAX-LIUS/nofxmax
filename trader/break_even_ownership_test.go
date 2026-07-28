package trader

import (
	"path/filepath"
	"sync"
	"testing"

	"nofx/store"
)

// 本文件钉住"保本止损两档互相驱逐"的挂撤循环。
//
// 线上实证(claude/WLDUSDT SHORT):07-26 12:29~14:54,qty 恒为 95,价格在 0.3594 与
// 0.3539 之间严格交替,每 3 分钟一轮,约 50 张全部 CANCELED。两个价就是同一组 ATR 下
// 的 BE1(0.5×ATR)与 BE2(1.5×ATR):一档挂上,另一档因为"额度只有一张"被判
// stale_bot_duplicate 撤掉,下一轮 BE 监控发现该档缺单又挂回来,反过来撤另一档。

func beStop(id string, price float64) OpenOrder {
	return OpenOrder{OrderID: id, PositionSide: "SHORT", Type: "STOP_MARKET", StopPrice: price}
}

// 两档都被 armed 记录按 order id 认领 → 两张都必须是 expected_dynamic_owner,
// 一张都不能进撤单集合。改造前额度固定 1,第二张必然 stale_bot_duplicate。
func TestBreakEvenTwoTiersClaimedByIDAreBothExpected(t *testing.T) {
	ownership := breakEvenOwnership{
		Armed:   true,
		Claimed: map[string]struct{}{"be1_order": {}, "be2_order": {}},
		Tiers:   2,
	}
	orders := []OpenOrder{beStop("be1_order", 0.3594), beStop("be2_order", 0.3539)}

	summary := classifyUnexpectedProtectionOrders(orders, "SHORT", nil, ownership, nativeTrailingArmedOnly(false), true)
	if summary.ExpectedDynamicOwner != 2 || summary.ExpectedDynamicStop != 2 {
		t.Fatalf("两档已认领的保本止损应全部盖章为 owner(total=2 be=2),got %+v", summary)
	}
	if summary.StaleBotDuplicate != 0 || summary.ManualOrForeign != 0 {
		t.Fatalf("已认领的保本止损不得被判异常,got %+v", summary)
	}

	ids := collectUnexpectedProtectionOrderIDs(orders, "SHORT", nil, ownership, nativeTrailingArmedOnly(false))
	if len(ids) != 0 {
		t.Fatalf("已认领的两档不该进撤单集合,got %+v", ids)
	}
}

// 历史记录 exchange_order_id 为空(线上全库 17 条 BE 记录都是空串)→ 认领集合为空,
// 但 fingerprint 里的 stage 能数出 2 档 → 额度 2,两张都不该被撤。
// 这是把挂撤循环真正堵死的一路:不依赖交易所回不回 id。
func TestBreakEvenTwoTiersWithoutIDsUseStageQuota(t *testing.T) {
	ownership := breakEvenOwnership{Armed: true, Claimed: map[string]struct{}{}, Tiers: 2}
	orders := []OpenOrder{beStop("live_be_a", 0.3594), beStop("live_be_b", 0.3539)}

	summary := classifyUnexpectedProtectionOrders(orders, "SHORT", nil, ownership, nativeTrailingArmedOnly(false), true)
	if summary.ExpectedDynamicStop != 2 {
		t.Fatalf("两档 stage 应给出额度 2,got %+v", summary)
	}
	if summary.StaleBotDuplicate != 0 {
		t.Fatalf("额度足够时不得判 stale 重复单,got %+v", summary)
	}
}

// 额度必须有上限:配两档却在场三张,第三张仍要落到 stale_bot_duplicate。
// 否则这个修复就变成"BE 单一律不撤",真残留单会永久堆积。
func TestBreakEvenQuotaStillCapsExtraOrders(t *testing.T) {
	ownership := breakEvenOwnership{Armed: true, Tiers: 2}
	orders := []OpenOrder{
		beStop("4c363c81edc5bcde_be_a", 0.3594),
		beStop("4c363c81edc5bcde_be_b", 0.3539),
		beStop("4c363c81edc5bcde_be_c", 0.3510),
	}

	summary := classifyUnexpectedProtectionOrders(orders, "SHORT", nil, ownership, nativeTrailingArmedOnly(false), true)
	if summary.ExpectedDynamicStop != 2 {
		t.Fatalf("额度 2 应只盖章两张,got %+v", summary)
	}
	if summary.StaleBotDuplicate != 1 || len(summary.StaleBotDuplicateIDs) != 1 {
		t.Fatalf("超出额度的第三张应判 stale 重复单,got %+v", summary)
	}
}

// 认领集合非空时按 id 判:不在集合里的止损单不得被盖章。
func TestBreakEvenUnclaimedStopIsNotOwner(t *testing.T) {
	ownership := breakEvenOwnership{
		Armed:   true,
		Claimed: map[string]struct{}{"be1_order": {}},
		Tiers:   2,
	}
	orders := []OpenOrder{beStop("be1_order", 0.3594), beStop("4c363c81edc5bcde_be_ghost", 0.3539)}

	summary := classifyUnexpectedProtectionOrders(orders, "SHORT", nil, ownership, nativeTrailingArmedOnly(false), true)
	if summary.ExpectedDynamicStop != 1 {
		t.Fatalf("只有认领的那张该盖章,got %+v", summary)
	}
	if summary.StaleBotDuplicate != 1 || summary.StaleBotDuplicateIDs[0] != "4c363c81edc5bcde_be_ghost" {
		t.Fatalf("未认领的止损单应判 stale 重复单,got %+v", summary)
	}
}

// 认领集合非空但交易所没回 order id:容忍(撤错在场保护单的后果远重于多留一张)。
func TestBreakEvenEmptyOrderIDToleratedWhenClaimSetPresent(t *testing.T) {
	ownership := breakEvenOwnership{Armed: true, Claimed: map[string]struct{}{"be1_order": {}}, Tiers: 2}
	orders := []OpenOrder{{OrderID: "", PositionSide: "SHORT", Type: "STOP_MARKET", StopPrice: 0.3539}}

	summary := classifyUnexpectedProtectionOrders(orders, "SHORT", nil, ownership, nativeTrailingArmedOnly(false), true)
	if summary.ExpectedDynamicStop != 1 || summary.StaleBotDuplicate != 0 {
		t.Fatalf("无 id 的保本止损应被容忍,got %+v", summary)
	}
}

// 老语义必须原样保留:breakEvenArmedOnly(true) = 额度恰好一张。
func TestBreakEvenArmedOnlyKeepsSingleTierSemantics(t *testing.T) {
	orders := []OpenOrder{
		beStop("4c363c81edc5bcde_be_a", 0.3594),
		beStop("4c363c81edc5bcde_be_b", 0.3539),
	}
	summary := classifyUnexpectedProtectionOrders(orders, "SHORT", nil, breakEvenArmedOnly(true), nativeTrailingArmedOnly(false), true)
	if summary.ExpectedDynamicStop != 1 || summary.StaleBotDuplicate != 1 {
		t.Fatalf("单档语义应只盖章一张,got %+v", summary)
	}

	summary = classifyUnexpectedProtectionOrders(orders, "SHORT", nil, breakEvenArmedOnly(false), nativeTrailingArmedOnly(false), true)
	if summary.ExpectedDynamicStop != 0 || summary.StaleBotDuplicate != 2 {
		t.Fatalf("未武装时保本止损不该被盖章,got %+v", summary)
	}
}

// 部署/重启窗口期:一个仓位只剩一条 armed 记录(旧独占逻辑把兄弟档降级成 replaced),
// 而交易所上两张 BE 单都在场。此时档位数必须仍然数到 2,否则额度 1 会撤掉一张真实的
// 保护单 —— 也就是不能靠 reconciler 的容忍分支来盖住。
func TestBreakEvenTierCountIncludesNonArmedRecords(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "be-tier-count.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	at := &AutoTrader{id: "t1", store: st, protectionStateMutex: sync.RWMutex{}}

	mk := func(stage, status, orderID string) store.DynamicProtectionRecord {
		return store.DynamicProtectionRecord{
			TraderID:        "t1",
			Symbol:          "WLDUSDT",
			Side:            "short",
			ProtectionType:  "break_even_stop",
			RuleFingerprint: "0.36220000|95.00000000|0.5000|0.2000|" + stage,
			Status:          status,
			ExchangeOrderID: orderID,
		}
	}
	// 模拟部署前的线上形态:BE1 被降级成 replaced,只有 BE2 还 armed,两条都没有 id。
	if err := st.SaveDynamicProtectionRecord(mk("BE1", "replaced", "")); err != nil {
		t.Fatalf("save BE1: %v", err)
	}
	if err := st.SaveDynamicProtectionRecord(mk("BE2", "armed", "")); err != nil {
		t.Fatalf("save BE2: %v", err)
	}

	claimed, tiers := at.claimedBreakEvenOrderIDsForPosition("WLDUSDT", "short")
	if tiers != 2 {
		t.Fatalf("档位数应按全部 BE 记录数到 2(否则重启窗口会撤掉真实保护单),got %d", tiers)
	}
	if len(claimed) != 0 {
		t.Fatalf("空 id 记录不该进认领集合,got %v", claimed)
	}

	// 额度 2 → 在场两张都被容忍。
	ownership := breakEvenOwnership{Armed: true, Claimed: claimed, Tiers: tiers}
	orders := []OpenOrder{
		beStop("4c363c81edc5bcde_be_a", 0.3594),
		beStop("4c363c81edc5bcde_be_b", 0.3539),
	}
	summary := classifyUnexpectedProtectionOrders(orders, "SHORT", nil, ownership, nativeTrailingArmedOnly(false), true)
	if summary.ExpectedDynamicStop != 2 || summary.StaleBotDuplicate != 0 {
		t.Fatalf("重启窗口期两张都该被容忍,got %+v", summary)
	}
}

// 非 armed 记录的 order id 绝不能进认领集合:那些单要么已撤、要么已被替换,
// 认了它们会让在场的新单反而落在集合外被判 stale。
func TestBreakEvenClaimSetExcludesNonArmedOrderIDs(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "be-claim-armed-only.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	at := &AutoTrader{id: "t1", store: st, protectionStateMutex: sync.RWMutex{}}

	mk := func(stage, status, orderID string) store.DynamicProtectionRecord {
		return store.DynamicProtectionRecord{
			TraderID:        "t1",
			Symbol:          "WLDUSDT",
			Side:            "short",
			ProtectionType:  "break_even_stop",
			RuleFingerprint: "0.36220000|95.00000000|0.5000|0.2000|" + stage,
			Status:          status,
			ExchangeOrderID: orderID,
		}
	}
	if err := st.SaveDynamicProtectionRecord(mk("BE1", "replaced", "dead-order")); err != nil {
		t.Fatalf("save replaced: %v", err)
	}
	if err := st.SaveDynamicProtectionRecord(mk("BE2", "armed", "live-order")); err != nil {
		t.Fatalf("save armed: %v", err)
	}

	claimed, _ := at.claimedBreakEvenOrderIDsForPosition("WLDUSDT", "short")
	if _, ok := claimed["dead-order"]; ok {
		t.Fatalf("已替换记录的 id 不该进认领集合,got %v", claimed)
	}
	if _, ok := claimed["live-order"]; !ok {
		t.Fatalf("armed 记录的 id 应进认领集合,got %v", claimed)
	}
}

// 未武装时额度必须为 0,不能因为 Tiers>0 就放行。
func TestBreakEvenQuotaZeroWhenNotArmed(t *testing.T) {
	if q := (breakEvenOwnership{Armed: false, Tiers: 3}).breakEvenQuota(); q != 0 {
		t.Fatalf("未武装额度应为 0,got %d", q)
	}
	if q := (breakEvenOwnership{Armed: true, Tiers: 0}).breakEvenQuota(); q != 1 {
		t.Fatalf("Tiers 缺失应退回 1(改造前行为),got %d", q)
	}
	if q := (breakEvenOwnership{Armed: true, Tiers: 2, Claimed: map[string]struct{}{"x": {}}}).breakEvenQuota(); q != 0 {
		t.Fatalf("有认领集合时不走额度,应为 0,got %d", q)
	}
}

// stage 解析:段数不足绝不猜,否则两档会被并成一档,后果比不去重严重。
func TestBreakEvenStageFromFingerprint(t *testing.T) {
	cases := []struct {
		fingerprint string
		want        string
	}{
		{"0.36220000|95.00000000|0.5000|0.2000|BE1", "BE1"},
		{"0.36220000|95.00000000|1.5000|0.6000| BE2 ", "BE2"},
		{"0.36220000|95.00000000|0.5000|0.2000", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := breakEvenStageFromFingerprint(c.fingerprint); got != c.want {
			t.Fatalf("fingerprint %q 应解析出 stage %q,got %q", c.fingerprint, c.want, got)
		}
	}
}

// 认领集合必须限定在当前仓位。
//
// 这个漏洞是 Phase 1「给 BE 记录写入真实 order id」之后才到得了的:以前 BE 记录的
// exchange_order_id 全是空串,认领集合恒为空,同 symbol/side 上一个已平仓位的残留记录
// 进不进来都无所谓。开始写真实 id 之后,若把已平仓位的 id 认成自己的,在场的新单反倒
// 落在集合外 → 被判 stale 重复单 → 挂撤循环换条路回来。
func TestBreakEvenRecordBelongsToCurrentPosition(t *testing.T) {
	rec := func(cTime int64, entry float64) store.DynamicProtectionRecord {
		fp := ""
		if entry > 0 {
			fp = entryPositionFingerprint(entry) + "|95.00000000"
		}
		return store.DynamicProtectionRecord{PositionCreatedTime: cTime, PositionFingerprint: fp}
	}

	// cTime 双方都有:精确比对,加减仓造成的均价漂移不影响判定。
	if !breakEvenRecordBelongsToCurrentPosition(rec(1700, 0.3622), 1700, 0.3697) {
		t.Fatal("cTime 相同应认定为同一仓位(均价漂移无关)")
	}
	if breakEvenRecordBelongsToCurrentPosition(rec(1700, 0.3622), 1800, 0.3622) {
		t.Fatal("cTime 不同必须判为不同仓位,即使开仓价一样")
	}

	// cTime 拿不到(Binance 不回 / 历史记录没有):退回开仓价容差。
	if !breakEvenRecordBelongsToCurrentPosition(rec(0, 0.3622), 0, 0.3623) {
		t.Fatal("无 cTime 时容差内应认定为同一仓位")
	}
	if breakEvenRecordBelongsToCurrentPosition(rec(0, 0.3622), 0, 0.5000) {
		t.Fatal("无 cTime 且开仓价差很远应判为不同仓位")
	}

	// 两种身份都无从判断时放行 —— 改造前行为,判不了就别判。
	if !breakEvenRecordBelongsToCurrentPosition(rec(0, 0), 0, 0) {
		t.Fatal("完全无从判断时应放行(不可错撤在场保护单)")
	}
	if !breakEvenRecordBelongsToCurrentPosition(rec(0, 0), 0, 0.3622) {
		t.Fatal("记录侧无身份信息时应放行")
	}
	if !breakEvenRecordBelongsToCurrentPosition(rec(1700, 0.3622), 0, 0) {
		t.Fatal("当前侧读不到身份时应放行")
	}
}

// trailing 单不得消耗保本额度:TRAILING_STOP_MARKET 的 Type 含 "STOP",
// 若按 looksLikeStopLoss 消耗额度,排在 trailing 之后的真保本止损会被判外来单。
func TestBreakEvenQuotaNotConsumedByTrailing(t *testing.T) {
	trailOwnership := nativeTrailingOwnership{Armed: true, Claimed: map[string]struct{}{"trail_1": {}}}
	orders := []OpenOrder{
		{OrderID: "trail_1", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET", StopPrice: 0.3700, CallbackRate: 0.3},
		beStop("be_1", 0.3594),
	}
	summary := classifyUnexpectedProtectionOrders(orders, "SHORT", nil, breakEvenArmedOnly(true), trailOwnership, true)
	if summary.ExpectedDynamicTrailing != 1 || summary.ExpectedDynamicStop != 1 {
		t.Fatalf("trailing 不该吃掉保本额度,got %+v", summary)
	}
	if summary.ManualOrForeign != 0 || summary.StaleBotDuplicate != 0 {
		t.Fatalf("两张都是自己的单,got %+v", summary)
	}
}
