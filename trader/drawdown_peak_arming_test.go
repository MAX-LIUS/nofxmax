package trader

import (
	"testing"

	"nofx/store"
)

// ============================================================================
// RC-6 managed 陪跑形同虚设:pending→tracking 的门禁曾经只看当前盈利。
//
// DD 档是移动止盈:交易所原生版本在 mark 越过 activePx 的那一刻就闩住,之后价格回撤
// 正是它要执行的场景。而 managed 侧用 `currentPnLPct >= MinProfitPct` 当门禁,语义正好相反
// —— 只能在**上涨过程中**武装;一旦回撤到触发线以下才轮询到,该档永远停在 pending。
// 没进 tracking 的档既不持有峰值、也算不出回撤,于是永不触发。
//
// 更糟的是分配表只在内存里(见 initDrawdownTiersForPosition 注释),每次重启都重建成
// pending/peak=0,而 peakPnLCache 是**持久化并会恢复**的。两者在只看当前值的门禁下永远
// 无法收敛:peak 说 8.11%,档位说 0.00%。
//
// 2026-07-29 线上实证 (GPT / SKHYNIXUSDT short):峰值 8.11%、触发 5.8175%、已回撤到
// 4.82%,dd1 在 520 条连续"managed monitor is co-running as second insurance"日志中
// 一直是 `pending peak=0.00%`。陪跑承诺是真的,但结构上不可能到达。
//
// 修复:门禁改为 max(current, peak) —— "盈利曾经到过触发线"即可武装。
// ============================================================================

func peakArmFixture(symbol, side string, minProfit float64) *AutoTrader {
	at := &AutoTrader{
		id:                 "trader-pk",
		exchange:           "okx",
		drawdownTierAllocs: make(map[string][]store.DrawdownTierAllocation),
	}
	at.setDrawdownTierAllocs(symbol, side, []store.DrawdownTierAllocation{{
		TierIndex:      0,
		StageName:      "dd1",
		MinProfitPct:   minProfit,
		MaxDrawdownPct: 2.327,
		CloseRatioPct:  100,
		Quantity:       0.017,
		Status:         "pending",
	}})
	return at
}

// 决定性回归:峰值早已越过触发线、当前已回撤到线下时,该档必须武装并立刻判定触发。
// 这正是 SKHYNIX 那 520 轮空转的形态。
func TestTierArm_PeakAboveTriggerArmsEvenAfterRetrace(t *testing.T) {
	const (
		symbol    = "SKHYNIXUSDT"
		side      = "short"
		minProfit = 5.8175
		peak      = 8.11
		current   = 4.82 // 已回撤到触发线以下
	)
	at := peakArmFixture(symbol, side, minProfit)

	triggered := at.evaluateDrawdownTiers(symbol, side, current, peak)

	allocs := at.getDrawdownTierAllocs(symbol, side)
	if allocs[0].Status == "pending" {
		t.Fatalf("峰值 %.2f%% 早已越过触发线 %.2f%%,该档必须武装,不得停在 pending(这就是陪跑形同虚设的机制)", peak, minProfit)
	}
	// 峰值必须从全局峰值继承,否则回撤算不出来。
	if allocs[0].PeakPnLPct < peak-0.001 {
		t.Fatalf("武装时应继承全局峰值 %.2f%%,实得 %.2f%%", peak, allocs[0].PeakPnLPct)
	}
	// 价格回撤 = ((8.11 - 4.82) / (100 + 8.11)) * 100 = 3.043% > 阈值 2.327% → 必须触发。
	if triggered == nil {
		t.Fatalf("从峰值 %.2f%% 回撤到 %.2f%%(价格回撤 3.04%% > 阈值 2.327%%)必须触发平仓", peak, current)
	}
}

// 重启失忆:分配表是内存态,重启后重建为 pending/peak=0;而 peakPnLCache 会被恢复。
// 传入恢复出来的峰值时,该档必须能凭峰值补上武装 —— 否则重启就等于永久丢失这一档保护。
func TestTierArm_RestartAmnesiaRecoversFromPersistedPeak(t *testing.T) {
	const (
		symbol    = "SKHYNIXUSDT"
		side      = "short"
		minProfit = 5.8175
	)
	// 重启后的状态:pending、peak=0(内存表新建),但 peakPnLCache 恢复出 8.11%。
	at := peakArmFixture(symbol, side, minProfit)
	if got := at.getDrawdownTierAllocs(symbol, side)[0].PeakPnLPct; got != 0 {
		t.Fatalf("前置条件:重启后档位峰值应为 0,实得 %.2f", got)
	}

	// 当前盈利必须**低于**触发线,否则只看当前值的旧门禁也会武装,这条测试就没有
	// 区分力了 —— 重启失忆的要害正是"当前值在线下、只有持久化峰值在线上"。
	const currentBelowTrigger = 5.0
	if currentBelowTrigger >= minProfit {
		t.Fatalf("测试自检:当前值 %.2f 必须低于触发线 %.2f", currentBelowTrigger, minProfit)
	}
	at.evaluateDrawdownTiers(symbol, side, currentBelowTrigger, 8.11)

	if got := at.getDrawdownTierAllocs(symbol, side)[0].Status; got == "pending" {
		t.Fatal("重启后凭恢复的持久化峰值必须能武装,否则重启=永久丢失该档保护")
	}
}

// 反向锁死:盈利从未到过触发线时,绝不能武装 —— 否则会在还没到止盈条件时就开始
// 按回撤平仓,把移动止盈变成随机平仓。
func TestTierArm_NeverReachedTriggerStaysPending(t *testing.T) {
	const (
		symbol    = "WLDUSDT"
		side      = "short"
		minProfit = 6.0
	)
	at := peakArmFixture(symbol, side, minProfit)

	// 当前 3%、历史峰值 4% —— 都没到 6%。
	triggered := at.evaluateDrawdownTiers(symbol, side, 3.0, 4.0)

	if got := at.getDrawdownTierAllocs(symbol, side)[0].Status; got != "pending" {
		t.Fatalf("盈利从未到过触发线时必须保持 pending,实得 %q", got)
	}
	if triggered != nil {
		t.Fatal("未武装的档不得触发平仓")
	}
}

// updateDrawdownTierStates 是 native 路径的孪生体,与 evaluateDrawdownTiers 改写同一张
// 分配表。两者门禁必须一致,否则同一档在两条路径里状态不同 → 状态机自相矛盾。
func TestTierArm_NativePathGateAgreesWithManagedPath(t *testing.T) {
	const (
		symbol    = "SKHYNIXUSDT"
		side      = "short"
		minProfit = 5.8175
		peak      = 8.11
		current   = 4.82
	)
	at := peakArmFixture(symbol, side, minProfit)

	at.updateDrawdownTierStates(symbol, side, current, peak)

	if got := at.getDrawdownTierAllocs(symbol, side)[0].Status; got == "pending" {
		t.Fatalf("native 路径(updateDrawdownTierStates)的门禁必须与 managed 路径一致,否则同一张分配表两条路径判定分歧,实得 %q", got)
	}
}
