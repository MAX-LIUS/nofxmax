package trader

import (
	"fmt"
	"testing"

	tradertypes "nofx/trader/types"
)

// 这一组测试钉住 WLD 事故的治本点。
//
// 线上现象:claude 的 WLDUSDT SHORT 一度在交易所挂了 4 条 break_even_stop 单
// (0.3604/0.3594/0.3568/0.3539),而配置只有 2 档。根因是加仓把交易所持仓均价从
// 0.3622 推到 0.3697(漂 1.9%),超出 entrySamePosition 的 0.05% 容差 → 每轮都判成
// "换了新仓位" → 用当下市场重新冻结 ATR(1.54% → 0.99%)→ 同一档 offset 算出两个
// stop 价 → 2 档 × 2 个 ATR = 4 条单,而 applyBreakEvenStops 只在价格完全相同时跳过。
//
// 修法:仓位身份改用交易所 cTime(加减仓不变),entryPrice 容差退为无 cTime 时的兜底。

// TestFrozenIdentityMatchesSurvivesEntryDriftWhenCTimeKnown 是 WLD 那一幕本身:
// 入场价漂 1.9%(容差外),但 cTime 相同 → 必须判为同一仓位。
func TestFrozenIdentityMatchesSurvivesEntryDriftWhenCTimeKnown(t *testing.T) {
	const cTime int64 = 1784259700000
	// 线上真实数值:开仓 0.3622 → 加仓后 0.3697。
	if !frozenATRIdentityMatches(cTime, cTime, 0.3622, 0.3697) {
		t.Fatal("同一 cTime、入场价漂 1.9%:必须仍判为同一仓位(否则重新冻结 ATR,BE 单堆积)")
	}
	// 反向确认旧判据确实会漏:这就是事故的直接原因。
	if entrySamePosition(0.3622, 0.3697) {
		t.Fatal("前提失效:0.05% 容差不该容纳 1.9% 漂移,本测试的意义依赖于此")
	}
}

// TestFrozenIdentityRejectsDifferentCTime 换了真正的新仓位(cTime 不同)时,
// 即使入场价碰巧几乎一样,也必须判为不同仓位 —— 新仓位不能继承旧仓位的冻结值。
func TestFrozenIdentityRejectsDifferentCTime(t *testing.T) {
	if frozenATRIdentityMatches(1784259700000, 1784263300000, 0.3622, 0.36221) {
		t.Fatal("cTime 不同即换仓,入场价再接近也不能命中")
	}
}

// TestFrozenIdentityFallsBackToEntryToleranceWithoutCTime 覆盖 Binance:
// 它不报 createdTime(永远 0),行为必须与修改前完全一致 —— 容差内命中、容差外不命中。
func TestFrozenIdentityFallsBackToEntryToleranceWithoutCTime(t *testing.T) {
	cases := []struct {
		name                     string
		recordCTime, currentTime int64
		recordEntry, curEntry    float64
		want                     bool
	}{
		{"binance-两边都无cTime-容差内", 0, 0, 149.09, 149.07246479, true},
		{"binance-两边都无cTime-容差外", 0, 0, 0.3622, 0.3697, false},
		{"旧记录无cTime-本轮有-容差内", 0, 1784259700000, 149.09, 149.07246479, true},
		{"旧记录无cTime-本轮有-容差外", 0, 1784259700000, 0.3622, 0.3697, false},
		// 记录有 cTime 但本轮不知道(身份刚被换仓判定删掉的那一轮):按容差裁决,
		// 不能因为"拿不到当前 cTime"就把明明有效的冻结值丢掉。
		{"记录有cTime-本轮未知-容差内", 1784259700000, 0, 149.09, 149.07246479, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := frozenATRIdentityMatches(c.recordCTime, c.currentTime, c.recordEntry, c.curEntry)
			if got != c.want {
				t.Fatalf("frozenATRIdentityMatches(%d,%d,%v,%v) = %v, want %v",
					c.recordCTime, c.currentTime, c.recordEntry, c.curEntry, got, c.want)
			}
		})
	}
}

// TestCurrentPositionCreatedTimeReadsDrawdownIdentity 钉住那条取身份的旁路:
// drawdownPosIdentity 由 refreshDrawdownExecutionFingerprint 在每轮 monitor 最前面写,
// 冻结值的读取都在其后,所以不必把 posCreatedTime 穿过 7 层签名。
func TestCurrentPositionCreatedTimeReadsDrawdownIdentity(t *testing.T) {
	const (
		symbol       = "WLDUSDT"
		side         = "short"
		cTime  int64 = 1784259700000
	)
	at := &AutoTrader{
		drawdownState:       make(map[string]string),
		drawdownPosIdentity: make(map[string]string),
		drawdownRunnerState: make(map[string]DrawdownRunnerState),
	}

	if got := at.currentPositionCreatedTime(symbol, side); got != 0 {
		t.Fatalf("身份还没写入时必须回 0(触发容差兜底),got %d", got)
	}

	at.refreshDrawdownExecutionFingerprint(symbol, side, cTime)
	if got := at.currentPositionCreatedTime(symbol, side); got != cTime {
		t.Fatalf("currentPositionCreatedTime = %d, want %d", got, cTime)
	}

	// 带后缀的旧格式("cTime|其它")也要能取出第 0 段。
	at.protectionStateMutex.Lock()
	at.drawdownPosIdentity[positionKey(symbol, side)] = fmt.Sprintf("%d|legacy-suffix", cTime)
	at.protectionStateMutex.Unlock()
	if got := at.currentPositionCreatedTime(symbol, side); got != cTime {
		t.Fatalf("带后缀格式:got %d, want %d", got, cTime)
	}

	// 非数字(历史遗留的规则 fingerprint)必须回 0 而不是 panic/乱数。
	at.protectionStateMutex.Lock()
	at.drawdownPosIdentity[positionKey(symbol, side)] = "not-a-ctime"
	at.protectionStateMutex.Unlock()
	if got := at.currentPositionCreatedTime(symbol, side); got != 0 {
		t.Fatalf("非数字身份必须回 0,got %d", got)
	}

	// 换仓被判定后身份被删除,这一轮取不到 → 0 → 退回容差,不能误判成换仓。
	at.refreshDrawdownExecutionFingerprint(symbol, side, cTime)
	if changed := at.refreshDrawdownExecutionFingerprint(symbol, side, cTime+3600000); !changed {
		t.Fatal("cTime 变了必须判为换仓")
	}
	if got := at.currentPositionCreatedTime(symbol, side); got != 0 {
		t.Fatalf("换仓那一轮身份被删,必须回 0,got %d", got)
	}
}

// TestMatchingBreakEvenOrderIDReturnsHandle 钉住第三处:"BE 已在场"分支过去只回 bool,
// 持久化只能写空 exchange_order_id(线上全库 17 条 break_even_stop 记录都是空的),
// 于是"按档撤旧单"根本无从下手 —— 而所有档共用 mechanism code "BE",按 tag 撤会
// 连兄弟档一起撤掉。现在必须把命中那条单的 id 带出来。
func TestMatchingBreakEvenOrderIDReturnsHandle(t *testing.T) {
	orders := []tradertypes.OpenOrder{
		{OrderID: "ladder-1", PositionSide: "SHORT", Type: "STOP_MARKET", StopPrice: 0.3800, ClientOrderID: "nofx-ladder-sl"},
		{OrderID: "be-tier-1", PositionSide: "SHORT", Type: "STOP_MARKET", StopPrice: 0.3604, ClientOrderID: "nofx-break_even-be1"},
		{OrderID: "be-tier-2", PositionSide: "SHORT", Type: "STOP_MARKET", StopPrice: 0.3568, ClientOrderID: "nofx-break_even-be2"},
	}

	id, found := matchingBreakEvenOrderID(orders, "SHORT", 0.3604)
	if !found || id != "be-tier-1" {
		t.Fatalf("BE1 命中应返回 be-tier-1,got id=%q found=%v", id, found)
	}
	// 必须按价格区分档位,不能返回第一条 BE 单。
	id, found = matchingBreakEvenOrderID(orders, "SHORT", 0.3568)
	if !found || id != "be-tier-2" {
		t.Fatalf("BE2 命中应返回 be-tier-2,got id=%q found=%v", id, found)
	}
	// ladder SL 不是 BE,不能被认领。
	if id, found = matchingBreakEvenOrderID(orders, "SHORT", 0.3800); found && id == "ladder-1" {
		t.Fatal("ladder SL 被当成 BE 单认领了")
	}
	// 没有匹配时必须回空串,不能回垃圾 id。
	if id, found = matchingBreakEvenOrderID(orders, "SHORT", 0.1234); found || id != "" {
		t.Fatalf("无匹配应回 (\"\", false),got (%q,%v)", id, found)
	}
	// 与旧 bool 版判据保持一致。
	if !hasMatchingBreakEvenOrder(orders, "SHORT", 0.3604) {
		t.Fatal("hasMatchingBreakEvenOrder 与 matchingBreakEvenOrderID 判据不一致")
	}
}
