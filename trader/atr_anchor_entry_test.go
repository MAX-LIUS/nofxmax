package trader

import (
	"math"
	"strings"
	"testing"

	"nofx/store"
)

// TestATRPercentAnchoredToFrozenEntry 是"同一档梯度挂出两张单"的**根因**用例。
//
// drawdownRuleIdentity 抹平 fingerprint 第 0 段只解决了一半:开仓均价在 fingerprint
// 里出现两次 —— 第 0 段(单独一个字段,可以抹平)**以及 ATR→百分比换算的分母**
// (算进 MinProfitPct/MaxDrawdownPct 的数值里,抹不掉)。
//
// 线上 SOXLUSDT 的库内实证(2026-07-27,同一仓位 4 条 armed 记录):
//
//	entry 149.09000000 → ident *|*|3.8025|2.2815|100.0000|dd1|...
//	entry 149.07246479 → ident *|*|3.8029|2.2818|100.0000|dd1|...
//	3.8029/3.8025 = 1.000105 = 149.09/149.07246479 —— 差异完全来自分母。
//
// 修法:换算分母改用 ATR 冻结时的那个 entry(anchorEntry),让"这一档等于百分之几"
// 和 ATR 一样冻住。本用例先按计划价冻结,再按修正后的成交均价解析,要求两次得到
// **完全相同**的规则和身份。
func TestATRPercentAnchoredToFrozenEntry(t *testing.T) {
	const (
		symbol       = "SOXLUSDT"
		side         = "long"
		plannedEntry = 149.09       // place-at-open 冻结 ATR 时用的价
		filledEntry  = 149.07246479 // 交易所同步回来的真实成交均价(漂 0.0118%,在 0.05% 容差内)
		atrValue     = 1.8898       // 1 ATR ≈ 1.2675% of entry
	)
	rules := []store.DrawdownTakeProfitRule{
		{MinProfitPct: 3, MinProfitUnit: store.ProtectionUnitATR,
			MaxDrawdownPct: 1.8, MaxDrawdownUnit: store.ProtectionUnitATR,
			CloseRatioPct: 100, StageName: "dd1"},
		{MinProfitPct: 4, MinProfitUnit: store.ProtectionUnitATR,
			MaxDrawdownPct: 1.2, MaxDrawdownUnit: store.ProtectionUnitATR,
			CloseRatioPct: 30, StageName: "dd2"},
	}

	at := &AutoTrader{
		id:     "trader-anchor",
		config: AutoTraderConfig{StrategyConfig: &store.StrategyConfig{}},
	}
	at.config.StrategyConfig.ATRProtection = store.ATRProtectionConfig{Enabled: true}

	// 按计划价冻结一次(place-at-open 的动作)。
	tf := store.ATRProtectionConfig{}.WithDefaults().Timeframe
	key := frozenATRKey(at.id, symbol, tf, side)
	frozenATRMu.Lock()
	frozenATRCache[key] = frozenATREntry{entryPrice: plannedEntry, atr: atrValue}
	frozenATRMu.Unlock()
	t.Cleanup(func() {
		frozenATRMu.Lock()
		delete(frozenATRCache, key)
		frozenATRMu.Unlock()
	})

	atPlanned := at.resolveDrawdownRulesATR(rules, symbol, side, plannedEntry)
	atFilled := at.resolveDrawdownRulesATR(rules, symbol, side, filledEntry)
	if len(atPlanned) != len(rules) || len(atFilled) != len(rules) {
		t.Fatalf("解析后档数变了:%d / %d,want %d", len(atPlanned), len(atFilled), len(rules))
	}

	for i := range rules {
		if math.Abs(atPlanned[i].MinProfitPct-atFilled[i].MinProfitPct) > 1e-9 ||
			math.Abs(atPlanned[i].MaxDrawdownPct-atFilled[i].MaxDrawdownPct) > 1e-9 {
			t.Fatalf("第 %d 档换算结果随开仓均价漂了:\n  计划价 %.8f → min=%.6f dd=%.6f\n  成交价 %.8f → min=%.6f dd=%.6f\n"+
				"分母没锚在冻结价上 → 百分比进 ruleFP → 同一档分叉成两个身份 → 交易所挂两张单",
				i, plannedEntry, atPlanned[i].MinProfitPct, atPlanned[i].MaxDrawdownPct,
				filledEntry, atFilled[i].MinProfitPct, atFilled[i].MaxDrawdownPct)
		}
		// 身份必须逐字节相同 —— 这是撮合器认单的依据。
		idA := drawdownRuleIdentity(stableDrawdownRuleFingerprint(plannedEntry, atPlanned[i]))
		idB := drawdownRuleIdentity(stableDrawdownRuleFingerprint(filledEntry, atFilled[i]))
		if idA != idB {
			t.Fatalf("第 %d 档身份分叉:\n  %s\n  %s", i, idA, idB)
		}
	}

	// 反向护栏:换算必须真的发生过(否则本用例会因为"两次都没换算"而假通过)。
	if math.Abs(atPlanned[0].MinProfitPct-3) < 1e-9 {
		t.Fatal("MinProfitPct 仍是裸 ATR 倍数 3 —— 换算没发生,用例失去意义")
	}
	// 且必须等于按冻结价算出来的那个值。
	want := 3 * atrValue / plannedEntry * 100
	if math.Abs(atPlanned[0].MinProfitPct-want) > 1e-9 {
		t.Fatalf("dd1 min%%:want %.8f(按冻结价 %.8f),got %.8f", want, plannedEntry, atPlanned[0].MinProfitPct)
	}
}

// TestATRAnchorFallsBackToCallerEntry:拿不到冻结锚时必须退回调用方 entry,
// 也就是旧行为 —— 绝不能因为没有锚就放弃换算(那会把裸 ATR 倍数当百分比挂单,
// 正是 v1.16.9/v1.16.11 那一族的事故)。
func TestATRAnchorFallsBackToCallerEntry(t *testing.T) {
	const (
		symbol = "NOFREEZEUSDT"
		side   = "long"
		entry  = 100.0
	)
	at := &AutoTrader{
		id:     "trader-anchor-fallback",
		config: AutoTraderConfig{StrategyConfig: &store.StrategyConfig{}},
	}
	at.config.StrategyConfig.ATRProtection = store.ATRProtectionConfig{Enabled: true}

	// 冻结缓存里放一条 entryPrice=0 的条目:entrySamePosition 对 0 返回 false,
	// 所以这条命中不了 —— 等价于"锚不可用"。不预置任何可命中的条目,
	// atrForProtection 会去抓行情(离线环境下失败),两条路径都必须退回调用方 entry。
	tf := store.ATRProtectionConfig{}.WithDefaults().Timeframe
	key := frozenATRKey(at.id, symbol, tf, side)
	frozenATRMu.Lock()
	frozenATRCache[key] = frozenATREntry{entryPrice: 0, atr: 2.0}
	frozenATRMu.Unlock()
	t.Cleanup(func() {
		frozenATRMu.Lock()
		delete(frozenATRCache, key)
		frozenATRMu.Unlock()
	})

	// 直接钉住 anchor 选择逻辑,不走会触发行情抓取的解析路径。
	if got := anchorOrCallerEntry(0, entry); got != entry {
		t.Fatalf("锚为 0 时必须退回调用方 entry:got %.8f want %.8f", got, entry)
	}
	if got := anchorOrCallerEntry(-1, entry); got != entry {
		t.Fatalf("锚为负数时必须退回调用方 entry:got %.8f", got)
	}
	if got := anchorOrCallerEntry(149.09, entry); got != 149.09 {
		t.Fatalf("锚可用时必须用锚:got %.8f want 149.09", got)
	}
}

// TestFrozenATRAnchorSurvivesRestart:重启后从持久化记录恢复时,锚必须是**记录里**
// 那个 entry,不是调用方当下的 entry —— 否则重启就等于一次身份分叉。
func TestFrozenATRAnchorSurvivesRestart(t *testing.T) {
	const (
		symbol       = "SOXLUSDT"
		side         = "long"
		frozenEntry  = 149.09
		currentEntry = 149.07246479
	)
	at := &AutoTrader{
		id:     "trader-anchor-restart",
		config: AutoTraderConfig{StrategyConfig: &store.StrategyConfig{}},
	}
	cfg := store.ATRProtectionConfig{Enabled: true}
	tf := cfg.WithDefaults().Timeframe
	key := frozenATRKey(at.id, symbol, tf, side)

	// 内存缓存为空(重启),但缓存里放一条"冻结价 = frozenEntry"的记录来模拟恢复后的状态。
	frozenATRMu.Lock()
	frozenATRCache[key] = frozenATREntry{entryPrice: frozenEntry, atr: 1.8898}
	frozenATRMu.Unlock()
	t.Cleanup(func() {
		frozenATRMu.Lock()
		delete(frozenATRCache, key)
		frozenATRMu.Unlock()
	})

	_, anchor, ok := at.frozenATRAndEntryForPosition(symbol, side, currentEntry, cfg)
	if !ok {
		t.Fatal("应当命中冻结缓存")
	}
	if math.Abs(anchor-frozenEntry) > 1e-9 {
		t.Fatalf("锚不是冻结价:got %.8f want %.8f —— 每次恢复都会换一次身份", anchor, frozenEntry)
	}
	// side 大小写不该影响 key(线上两条路径一个传 "long" 一个传 "LONG")。
	if _, a2, ok2 := at.frozenATRAndEntryForPosition(symbol, strings.ToUpper(side), currentEntry, cfg); ok2 {
		if math.Abs(a2-frozenEntry) > 1e-9 {
			t.Fatalf("side 大小写不同拿到了不同的锚:%.8f vs %.8f", a2, frozenEntry)
		}
	}
}
