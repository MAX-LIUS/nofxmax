package trader

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"nofx/store"
	"nofx/trader/okx"
)

// 本文件钉住 2026-07-31 线上 ETHUSDT short 的"一张单两个主人"缺陷族。
//
// 实况:ATR 解析后 BE1 offset=0.3000、BE2 offset=0.4498,挂单价 1862.1467/1859.3489,
// 相对间距 0.1503% < protectionPriceTolerancePct(0.2%)。于是
//   - matchingBreakEvenOrderID 只按价格匹配,BE2 匹配到了 BE1 那张单;
//   - 两档都把同一个 orderID 写成 armed;
//   - supersedeConflictingOrderClaim 的"新写入者胜"在两个 writer 交替写入时不收敛,
//     22:06:49→22:11:09 翻转 53 次,直到仓位平掉才停。
//
// 三条防线各自钉一个用例:档位抑制、确定性裁决、reconciler 与监控同源解析。

// ① 两档撞进容差带 → 只有一档认领,且被抑制那档的存量 armed 记录被退役。
// 关键不变量:不撤任何交易所挂单(被抑制档从未挂出属于自己的单)。
func TestSuppressedBreakEvenTierRetiresItsStaleArmedRecord(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "be-collision.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	at := &AutoTrader{id: "t1", store: st, protectionStateMutex: sync.RWMutex{}}

	// 上线前遗留:BE1 与 BE2 两条 armed 记录认领同一张 _sl。
	for _, r := range []struct {
		stage   string
		trigger float64
		offset  float64
	}{{"BE1", 0.9638, 0.3000}, {"BE2", 1.6064, 0.4498}} {
		rec := store.DynamicProtectionRecord{
			TraderID:        "t1",
			Symbol:          "ETHUSDT",
			Side:            "short",
			ProtectionType:  "break_even_stop",
			RuleFingerprint: fmt.Sprintf("1867.75000000|0.08320000|%.4f|%.4f|%s", r.trigger, r.offset, r.stage),
			Status:          "armed",
			ExchangeOrderID: "3791769934100062208_sl",
		}
		if err := st.SaveDynamicProtectionRecord(rec); err != nil {
			t.Fatalf("seed %s: %v", r.stage, err)
		}
	}

	at.retireSuppressedBreakEvenTierRecord("ETHUSDT", "short", "BE2", "BE1")

	state, err := st.LoadDynamicProtectionState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	armedByStage := map[string]int{}
	for _, rec := range state.Records {
		if rec.ProtectionType != "break_even_stop" || rec.Status != "armed" {
			continue
		}
		armedByStage[breakEvenStageFromFingerprint(rec.RuleFingerprint)]++
	}
	if armedByStage["BE2"] != 0 {
		t.Fatalf("被抑制的 BE2 不得再有 armed 记录,got %+v", armedByStage)
	}
	if armedByStage["BE1"] != 1 {
		t.Fatalf("留存档 BE1 的 armed 记录必须原样保留(它是那张实物单的主人),got %+v", armedByStage)
	}
}

// 抑制只针对被抑制的那一档:不能顺手把留存档也退役掉,
// 否则那张在场的单会变成无主单,分类器下一轮就把它判成 stale 重复单撤掉。
func TestRetireSuppressedTierDoesNotTouchWinnerTier(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "be-collision-winner.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	at := &AutoTrader{id: "t1", store: st, protectionStateMutex: sync.RWMutex{}}

	rec := store.DynamicProtectionRecord{
		TraderID:        "t1",
		Symbol:          "ETHUSDT",
		Side:            "short",
		ProtectionType:  "break_even_stop",
		RuleFingerprint: "1867.75000000|0.08320000|0.9638|0.3000|BE1",
		Status:          "armed",
		ExchangeOrderID: "live_sl",
	}
	if err := st.SaveDynamicProtectionRecord(rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// 抑制 BE2(它根本没有记录)——不得影响 BE1。
	at.retireSuppressedBreakEvenTierRecord("ETHUSDT", "short", "BE2", "BE1")

	state, _ := st.LoadDynamicProtectionState()
	found := false
	for _, r := range state.Records {
		if breakEvenStageFromFingerprint(r.RuleFingerprint) == "BE1" {
			found = true
			if r.Status != "armed" {
				t.Fatalf("BE1 必须保持 armed,got %s", r.Status)
			}
			if r.ExchangeOrderID != "live_sl" {
				t.Fatalf("BE1 认领的 order id 不得被改写,got %s", r.ExchangeOrderID)
			}
		}
	}
	if !found {
		t.Fatal("BE1 记录不该消失")
	}
}

// ② 确定性裁决:交替调用 supersedeConflictingOrderClaim(模拟两个 writer 轮流写入)
// 必须收敛到**同一个赢家**,而不是每轮翻转。这是 53 次翻转的直接钉子。
func TestOwnershipConflictConvergesRegardlessOfWriteOrder(t *testing.T) {
	mkRec := func(stage string, trigger, offset float64, orderID string) store.DynamicProtectionRecord {
		return store.DynamicProtectionRecord{
			TraderID:        "t1",
			Symbol:          "ETHUSDT",
			Side:            "short",
			ProtectionType:  "break_even_stop",
			RuleFingerprint: fmt.Sprintf("1867.75000000|0.08320000|%.4f|%.4f|%s", trigger, offset, stage),
			Status:          "armed",
			ExchangeOrderID: orderID,
		}
	}

	// 两种写入顺序都跑一遍,赢家必须一致。
	winners := make([]string, 0, 2)
	for _, order := range [][2]store.DynamicProtectionRecord{
		{mkRec("BE1", 0.9638, 0.3000, "sl_1"), mkRec("BE2", 1.6064, 0.4498, "sl_1")},
		{mkRec("BE2", 1.6064, 0.4498, "sl_1"), mkRec("BE1", 0.9638, 0.3000, "sl_1")},
	} {
		st, err := store.New(filepath.Join(t.TempDir(), "be-converge.db"))
		if err != nil {
			t.Fatalf("create store: %v", err)
		}
		at := &AutoTrader{id: "t1", store: st, protectionStateMutex: sync.RWMutex{}}

		record, current := order[0], order[1]
		if err := st.SaveDynamicProtectionRecord(record); err != nil {
			t.Fatalf("seed record: %v", err)
		}
		if err := st.SaveDynamicProtectionRecord(current); err != nil {
			t.Fatalf("seed current: %v", err)
		}
		at.supersedeConflictingOrderClaim(record, current)

		state, _ := st.LoadDynamicProtectionState()
		armed := make([]string, 0, 2)
		for _, r := range state.Records {
			if r.Status == "armed" && r.ProtectionType == "break_even_stop" {
				armed = append(armed, breakEvenStageFromFingerprint(r.RuleFingerprint))
			}
		}
		if len(armed) != 1 {
			t.Fatalf("裁决后必须只剩一个主人,got %+v", armed)
		}
		winners = append(winners, armed[0])
	}
	if winners[0] != winners[1] {
		t.Fatalf("赢家必须与写入顺序无关(否则两个 writer 交替写入时永不收敛),got %+v", winners)
	}
	// 身份串字典序小者留 = 低档位留 = 实物单真实挂单价所属档位。
	if winners[0] != "BE1" {
		t.Fatalf("低档位应留下(它才是那张实物单的挂单价所属档位),got %s", winners[0])
	}
}

// 跨类型冲突保持原"current 胜"语义:那一路只有一个 writer,不存在交替写入,
// 且新写入的确实是刚发生的事实(reclaim 认领成功)。不能被 ② 的改动带偏。
func TestCrossTypeConflictKeepsCurrentWinsSemantics(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "cross-type.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	at := &AutoTrader{id: "t1", store: st, protectionStateMutex: sync.RWMutex{}}

	old := store.DynamicProtectionRecord{
		TraderID: "t1", Symbol: "WLDUSDT", Side: "short",
		ProtectionType:  "managed_drawdown",
		RuleFingerprint: "0.36000000|0.00000000|2.5000|1.2500|100.0000|dd1",
		Status:          "armed", ExchangeOrderID: "shared_order",
	}
	fresh := store.DynamicProtectionRecord{
		TraderID: "t1", Symbol: "WLDUSDT", Side: "short",
		ProtectionType:  "native_trailing",
		RuleFingerprint: "0.36000000|0.00000000|3.6791|1.2500|100.0000|dd1",
		Status:          "armed", ExchangeOrderID: "shared_order",
	}
	if err := st.SaveDynamicProtectionRecord(old); err != nil {
		t.Fatalf("seed old: %v", err)
	}
	if err := st.SaveDynamicProtectionRecord(fresh); err != nil {
		t.Fatalf("seed fresh: %v", err)
	}
	at.supersedeConflictingOrderClaim(old, fresh)

	state, _ := st.LoadDynamicProtectionState()
	for _, r := range state.Records {
		if r.ProtectionType == "managed_drawdown" && r.Status == "armed" {
			t.Fatal("跨类型冲突中旧记录应被退役(current 胜)")
		}
		if r.ProtectionType == "native_trailing" && r.Status != "armed" {
			t.Fatalf("跨类型冲突中新写入的应留下,got %s", r.Status)
		}
	}
}

// ②的边界:同一档(stage 相同)但身份串不同,属于**同档重新解析**(ATR 口径变了),
// 不是跨档抢单。这一路必须保持"current 胜",让账本指向最新解析口径;
// 若误用字典序,账本会被钉死在旧 ATR 上,后续核对会拿一个交易所上不存在的价去比。
//
// 它不会退化成翻转:reconciler 与监控现在同源(getActiveBreakEvenRulesATR +
// resolveBreakEvenRulesForPosition),同一轮算出的身份串一致,会在身份串相等的早退处返回;
// 只有 ATR 快照真的移动时才发生一次过渡,过渡后两个 writer 又一致。
func TestSameStageBreakEvenReResolutionKeepsFreshATR(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "be-same-stage.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	at := &AutoTrader{id: "t1", store: st, protectionStateMutex: sync.RWMutex{}}

	// 同为 BE1,只是 ATR 解析口径不同(1.5000 未解析 → 0.9638 已解析)。
	stale := store.DynamicProtectionRecord{
		TraderID: "t1", Symbol: "ETHUSDT", Side: "short",
		ProtectionType:  "break_even_stop",
		RuleFingerprint: "1867.75000000|0.08320000|1.5000|0.3000|BE1",
		Status:          "armed", ExchangeOrderID: "shared_sl",
	}
	fresh := store.DynamicProtectionRecord{
		TraderID: "t1", Symbol: "ETHUSDT", Side: "short",
		ProtectionType:  "break_even_stop",
		RuleFingerprint: "1867.75000000|0.08320000|0.9638|0.3000|BE1",
		Status:          "armed", ExchangeOrderID: "shared_sl",
	}
	if err := st.SaveDynamicProtectionRecord(stale); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	if err := st.SaveDynamicProtectionRecord(fresh); err != nil {
		t.Fatalf("seed fresh: %v", err)
	}
	at.supersedeConflictingOrderClaim(stale, fresh)

	state, err := st.LoadDynamicProtectionState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	armed := []string{}
	for _, r := range state.Records {
		if r.Status == "armed" && r.ExchangeOrderID == "shared_sl" {
			armed = append(armed, drawdownRuleIdentity(r.RuleFingerprint))
		}
	}
	if len(armed) != 1 {
		t.Fatalf("一张活单只能有一个 armed 主人,got %d: %v", len(armed), armed)
	}
	if armed[0] != drawdownRuleIdentity(fresh.RuleFingerprint) {
		t.Fatalf("同档重新解析必须留下最新 ATR 口径,got %s want %s",
			armed[0], drawdownRuleIdentity(fresh.RuleFingerprint))
	}
}

// ④ 触发价已被市价越过的 TP 拒单必须是可识别的 sentinel,
// 这样 ladder 挂单路径才能把它记为"跳过"而不是 tier 失败(否则整份计划连坐)。
func TestTriggerPriceAlreadyPassedIsDistinguishable(t *testing.T) {
	// 线上 BTCUSDT SHORT 实况:code=51277,空头 TP 触发价高于最新价。
	passed := fmt.Errorf("OKX take profit rejected: code=51277 msg=TP trigger price cannot be higher than the last price: %w", okx.ErrTriggerPriceAlreadyPassed)
	if !errors.Is(passed, okx.ErrTriggerPriceAlreadyPassed) {
		t.Fatal("51277 族必须能被 errors.Is 识别为触发价已越过")
	}

	// 真实拒因不得被误判成"可跳过"——那会把挂单失败静默吞掉。
	real := errors.New("OKX take profit rejected: code=51000 msg=Parameter instId error")
	if errors.Is(real, okx.ErrTriggerPriceAlreadyPassed) {
		t.Fatal("非触发价拒因不得被判为可跳过")
	}
}

// 多轮交替调用(完全复刻线上 53 次翻转的调用模式):赢家必须在第一轮就定局,
// 之后每一轮都是幂等的 no-op。翻转的本质是"每轮都有一条记录被改状态",
// 所以这里直接断言"第一轮之后状态不再变化"。
func TestOwnershipConflictStopsFlipFloppingAcrossRounds(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "be-flipflop.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	at := &AutoTrader{id: "t1", store: st, protectionStateMutex: sync.RWMutex{}}

	mk := func(stage string, trigger, offset float64) store.DynamicProtectionRecord {
		return store.DynamicProtectionRecord{
			TraderID: "t1", Symbol: "ETHUSDT", Side: "short",
			ProtectionType:  "break_even_stop",
			RuleFingerprint: fmt.Sprintf("1867.75000000|0.08320000|%.4f|%.4f|%s", trigger, offset, stage),
			Status:          "armed", ExchangeOrderID: "sl_1",
		}
	}
	be1, be2 := mk("BE1", 0.9638, 0.3000), mk("BE2", 1.6064, 0.4498)
	if err := st.SaveDynamicProtectionRecord(be1); err != nil {
		t.Fatalf("seed be1: %v", err)
	}
	if err := st.SaveDynamicProtectionRecord(be2); err != nil {
		t.Fatalf("seed be2: %v", err)
	}

	snapshot := func() string {
		state, _ := st.LoadDynamicProtectionState()
		out := ""
		for _, stage := range []string{"BE1", "BE2"} {
			for _, r := range state.Records {
				if r.ProtectionType == "break_even_stop" && breakEvenStageFromFingerprint(r.RuleFingerprint) == stage {
					out += stage + "=" + r.Status + ";"
				}
			}
		}
		return out
	}

	// 第一轮:两个 writer 各调一次(线上就是这么交替的)。
	at.supersedeConflictingOrderClaim(be1, be2)
	at.supersedeConflictingOrderClaim(be2, be1)
	settled := snapshot()

	// 再跑 10 轮交替 —— 状态必须一动不动。
	// 注意传入的是**当前库里的**记录,而不是最初的种子:线上每轮都是重新读库再比,
	// 传旧快照会掩盖幂等闸(它按 Status 判"是否已收敛")。
	reload := func(stage string) store.DynamicProtectionRecord {
		state, _ := st.LoadDynamicProtectionState()
		for _, r := range state.Records {
			if r.ProtectionType == "break_even_stop" && breakEvenStageFromFingerprint(r.RuleFingerprint) == stage {
				return r
			}
		}
		t.Fatalf("记录 %s 不见了", stage)
		return store.DynamicProtectionRecord{}
	}
	for i := 0; i < 10; i++ {
		a, b := reload("BE1"), reload("BE2")
		at.supersedeConflictingOrderClaim(a, b)
		at.supersedeConflictingOrderClaim(b, a)
		if got := snapshot(); got != settled {
			t.Fatalf("第 %d 轮后状态发生变化,说明仍在翻转:settled=%q got=%q", i+1, settled, got)
		}
	}
	// 且必须恰好一个 armed 主人。
	armedCount := 0
	state, _ := st.LoadDynamicProtectionState()
	for _, r := range state.Records {
		if r.ProtectionType == "break_even_stop" && r.Status == "armed" {
			armedCount++
		}
	}
	if armedCount != 1 {
		t.Fatalf("收敛后必须恰好一个主人,got %d (%s)", armedCount, settled)
	}
}
