package trader

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"nofx/store"
)

// ============================================================================
// 破坏性测试:档位序号的新基准("该仓位当前生效的已解析规则集")在恶劣条件下不能塌。
//
// 新基准比旧基准(静态 config)多依赖两样东西:冻结 ATR 和 AI 规则覆盖。两者都会在
// 运行时变化,所以必须逐个压:
//   D1 冻结 ATR 缺失(冷启动/取不到 K 线)—— 不能编出错档,只能退化为不带标识;
//   D2 开仓均价被修正(加仓/部分成交)—— 序号必须不变,否则同一档在两轮里编出两个标识;
//   D3 AI 规则接管 —— 序号必须来自 AI 规则集,而不是静默退回 config;
//   D4 超过 9 档 —— 第 10 档只能退化为 0,不能溢出成单字符以外的东西;
//   D5 并发认领 —— 序号计算无共享可变状态,竞态下不能给出不一致的答案。
// ============================================================================

// D1:冻结 ATR 取不到时,ATR 单位规则无法解析。此时挂单侧和认领侧仍然共用同一套
// (未解析的)规则集,所以序号仍然自洽 —— 关键是**不能 panic,也不能给出跨基准的错档**。
func TestDestructive_MissingFrozenATRDegradesConsistently(t *testing.T) {
	// 故意不装配 frozenATRCache:atrTierFixture 的清理已经跑过的等价场景。
	at := reclaimFixture(t, wldSymbol, wldSide, bnWLDATRRules())
	at.config.StrategyConfig.ATRProtection = store.ATRProtectionConfig{Enabled: true}

	ordered := at.canonicalDrawdownTierRules(wldSymbol, wldSide, wldEntry)
	if len(ordered) != 2 {
		t.Fatalf("rule set collapsed without a frozen ATR: got %d tiers, want 2", len(ordered))
	}
	// 两侧同基准 ⇒ 每档仍得到唯一非零序号,且认领能落回同一档。
	seen := map[int]bool{}
	for _, rule := range ordered {
		idx := at.drawdownTierTagIndex(wldSymbol, wldSide, wldEntry, rule)
		if idx == 0 {
			t.Fatalf("tier min=%.4f got index 0 even though both sides share the same unresolved basis", rule.MinProfitPct)
		}
		if seen[idx] {
			t.Fatalf("duplicate tier index %d without a frozen ATR", idx)
		}
		seen[idx] = true
	}
}

// D2:开仓均价漂移时的契约。
//
// 挂单侧的真实调用形态是:checkPositionDrawdown 用 entryPrice 解析规则(:210),再用
// **同一个** entryPrice 去 arm。所以"同 entryPrice 幂等"才是必须成立的强契约。
//
// 而 entryPrice 漂到超出冻结记录的身份容差时(加仓/部分成交,且 cTime 还没被 adopt),
// 整套规则会在新 ATR 下重新解析,调用方手里那条旧规则在新集合里找不到 —— 此时唯一可
// 接受的结果是**退化为 0**(裸 tag + 形状匹配兜底),绝不能撞上"别的档"。撞上别的档
// 意味着认领时把一张单认给错的档,那才是真正危险的失败模式。
//
// 生产上 cTime 一旦 adopt(frozenATRIdentityMatches 只看 cTime),漂多少都命中同一条
// 冻结记录,序号恒定 —— 第三段就是锁这个。
func TestDestructive_TierIndexUnderEntryPriceDrift(t *testing.T) {
	at := bnWLDFixture(t, wldSymbol, wldSide, wldEntry, wldATR)
	resolved := at.resolveDrawdownRulesATR(bnWLDATRRules(), wldSymbol, wldSide, wldEntry)

	// 1) 同 entryPrice 幂等:重复调用必须给出同一个非零序号。
	for i := 0; i < 5; i++ {
		for want, rule := range resolved {
			if got := at.drawdownTierTagIndex(wldSymbol, wldSide, wldEntry, rule); got != want+1 {
				t.Fatalf("call #%d with the SAME entry: tier %d re-indexed to %d — the arm path resolves "+
					"and arms with one entryPrice, so this must be exactly idempotent", i, want+1, got)
			}
		}
	}

	// 2) 漂出容差、且没有 cTime:必须退化为 0,不能撞上别的档。
	for _, drifted := range []float64{wldEntry * 1.01, wldEntry * 0.97, wldEntry * 1.15} {
		for want, rule := range resolved {
			got := at.drawdownTierTagIndex(wldSymbol, wldSide, drifted, rule)
			if got != 0 && got != want+1 {
				t.Fatalf("entry drifted to %.6f: tier %d resolved to tier %d — a live order would be "+
					"tagged with the WRONG tier, and reclaim would hand it to the wrong rule. "+
					"Degrading to 0 (bare tag + shape matching) is the only acceptable outcome",
					drifted, want+1, got)
			}
		}
	}

	// 3) cTime 已 adopt(生产常态):漂多少序号都不变。
	at2 := bnWLDFixture(t, wldSymbol, wldSide, wldEntry, wldATR)
	const cTime int64 = 1753800000000
	at2.drawdownPosIdentity = map[string]string{
		positionKey(wldSymbol, wldSide): fmt.Sprintf("%d|x", cTime),
	}
	key := frozenATRKey(at2.id, wldSymbol, store.ATRProtectionConfig{}.WithDefaults().Timeframe, strings.ToUpper(wldSide))
	frozenATRMu.Lock()
	frozenATRCache[key] = frozenATREntry{entryPrice: wldEntry, atr: wldATR, posCreatedTime: cTime}
	frozenATRMu.Unlock()

	resolved2 := at2.resolveDrawdownRulesATR(bnWLDATRRules(), wldSymbol, wldSide, wldEntry)
	for _, drifted := range []float64{wldEntry, wldEntry * 1.01, wldEntry * 0.97, wldEntry * 1.15} {
		for want, rule := range resolved2 {
			if got := at2.drawdownTierTagIndex(wldSymbol, wldSide, drifted, rule); got != want+1 {
				t.Fatalf("with cTime adopted, entry drifted to %.6f: tier %d re-indexed to %d — "+
					"the frozen-ATR identity is supposed to make the tier tag immune to average-price "+
					"corrections for the life of the position", drifted, want+1, got)
			}
		}
	}
}

// D3:AI 规则接管时,序号必须来自 AI 规则集。
//
// 旧基准(static config)在 AI 模式下必然给 0,标识整体失效;新基准会跟着
// getActiveDrawdownRulesForPosition 走到 AI 规则,所以 AI 档位也能带标识。
func TestDestructive_AIRuleOverrideGetsItsOwnTierIndexes(t *testing.T) {
	at := reclaimFixture(t, "AIUSDT", "long", []store.DrawdownTakeProfitRule{
		{MinProfitPct: 2, MaxDrawdownPct: 20, CloseRatioPct: 100, StageName: "cfg-dd1"},
	})
	aiRules := []store.DrawdownTakeProfitRule{
		{MinProfitPct: 1.2, MaxDrawdownPct: 40, CloseRatioPct: 30, StageName: "ai-t1"},
		{MinProfitPct: 3.4, MaxDrawdownPct: 25, CloseRatioPct: 100, StageName: "ai-t2"},
	}
	at.drawdownAIRules = map[string][]store.DrawdownTakeProfitRule{
		positionKey("AIUSDT", "long"): aiRules,
	}

	ordered := at.canonicalDrawdownTierRules("AIUSDT", "long", 10)
	if len(ordered) != 2 || ordered[0].StageName != "ai-t1" || ordered[1].StageName != "ai-t2" {
		t.Fatalf("AI override did not become the tier basis: %+v", ordered)
	}
	for want, rule := range aiRules {
		if got := at.drawdownTierTagIndex("AIUSDT", "long", 10, rule); got != want+1 {
			t.Fatalf("AI tier %q got index %d, want %d", rule.StageName, got, want+1)
		}
	}
	// config 里那一档现在不生效,必须定不出序号(而不是被"顺手"编上)。
	cfgRule := store.DrawdownTakeProfitRule{MinProfitPct: 2, MaxDrawdownPct: 20, CloseRatioPct: 100}
	if got := at.drawdownTierTagIndex("AIUSDT", "long", 10, cfgRule); got != 0 {
		t.Fatalf("an inactive config tier got index %d, want 0 (must not tag a tier we are not arming)", got)
	}
}

// D4:超过 9 档时,第 10 档及之后只能退化为 0(clientID 里档位只占 1 个字符)。
func TestDestructive_MoreThanNineTiersDegradeInsteadOfOverflowing(t *testing.T) {
	rules := make([]store.DrawdownTakeProfitRule, 0, 12)
	for i := 1; i <= 12; i++ {
		rules = append(rules, store.DrawdownTakeProfitRule{
			MinProfitPct: float64(i), MaxDrawdownPct: 20, CloseRatioPct: 100,
		})
	}
	at := reclaimFixture(t, "MANYUSDT", "long", rules)

	for i, rule := range rules {
		got := at.drawdownTierTagIndex("MANYUSDT", "long", 100, rule)
		if i < 9 {
			if got != i+1 {
				t.Fatalf("tier %d got index %d", i+1, got)
			}
			continue
		}
		if got != 0 {
			t.Fatalf("tier %d got index %d, want 0 — a 2-digit tier would corrupt the client id layout", i+1, got)
		}
		// 而且 reason 必须退回裸的,不能编出半个标识。
		if tag := at.drawdownTrailingReasonTag("MANYUSDT", "long", 100, rule); tag != "native_trailing" {
			t.Fatalf("tier %d produced tag %q, want bare native_trailing", i+1, tag)
		}
	}
}

// D5:并发下序号必须始终一致。认领与挂单跑在不同 goroutine(monitor 循环 vs
// reconciler),如果序号计算依赖任何共享可变状态,竞态就会让两侧看到不同的档。
// 用 -race 跑本用例。
func TestDestructive_TierIndexIsConcurrencySafe(t *testing.T) {
	at := bnWLDFixture(t, wldSymbol, wldSide, wldEntry, wldATR)
	resolved := at.resolveDrawdownRulesATR(bnWLDATRRules(), wldSymbol, wldSide, wldEntry)

	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan string, workers*len(resolved))
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				for want, rule := range resolved {
					if got := at.drawdownTierTagIndex(wldSymbol, wldSide, wldEntry, rule); got != want+1 {
						errs <- "tier index raced"
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("%s — the arm side and the reclaim side could disagree on which tier an order belongs to", e)
	}
}

// D6:认领侧收到越界/伪造的档位序号时,必须拒绝而不是索引越界 panic。
// 交易所回显的 clientID 是外部输入,4 个交易员的历史单里什么都可能有。
func TestDestructive_OutOfRangeSelfReportedTierIsRejected(t *testing.T) {
	at := bnWLDFixture(t, wldSymbol, wldSide, wldEntry, wldATR)
	ordered := at.canonicalDrawdownTierRules(wldSymbol, wldSide, wldEntry)
	if len(ordered) != 2 {
		t.Fatalf("fixture: want 2 tiers, got %d", len(ordered))
	}
	for _, tier := range []int{-5, 0, 3, 9, 1 << 20} {
		order := OpenOrder{OrderID: "hostile", Type: "TRAILING_STOP_MARKET", ProtectionTier: tier}
		if got := at.tierTaggedRuleIndex(order, wldSide, wldEntry, ordered); got != -1 {
			t.Fatalf("self-reported tier %d resolved to index %d, want -1 (must fall back to shape matching, never index out of range)", tier, got)
		}
	}
	// 而且越界标识不能让这张单丢掉形状匹配的兜底能力。
	rule := ordered[0]
	act := calculateProfitBasedTrailingTriggerPrice(wldEntry, wldSide, rule.MinProfitPct)
	cb := calculateDrawdownRuleCallbackRatio(wldEntry, wldSide, rule)
	order := OpenOrder{
		OrderID: "bogus-tier", Symbol: wldSymbol, PositionSide: "SHORT",
		Type: "TRAILING_STOP_MARKET", StopPrice: act, CallbackRate: cb,
		Quantity: 100, ProtectionTier: 99,
	}
	kept, matches := at.reclaimLiveDrawdownTrailingOrders(wldSymbol, wldSide, wldEntry, []string{"bogus-tier"}, []OpenOrder{order})
	if len(matches) != 1 || len(kept) != 0 {
		t.Fatalf("a live order carrying a bogus tier lost its shape-matching reclaim: kept=%v matches=%d", kept, len(matches))
	}
}

// D7:挂单之后策略档位表被改 —— 自报序号会指向"另一个"档。必须靠形状交叉验证挡住,
// 退回形状匹配,而不是把这张单认给错的档。
//
// 认错档的后果分两级:轻的是另一档被判缺单、多挂一张(过度保护,方向安全);重的是
// 一档的单被当成另一档的,部分成交后把全平档标成"已执行",全平回撤保护静默消失。
func TestDestructive_TierListEditedAfterPlacementDoesNotMisclaim(t *testing.T) {
	at := bnWLDFixture(t, wldSymbol, wldSide, wldEntry, wldATR)
	resolved := at.resolveDrawdownRulesATR(bnWLDATRRules(), wldSymbol, wldSide, wldEntry)
	shallow := resolved[0] // dd1, 序号 1

	// 交易所上那张单是 dd1,带标识 T1,尚未激活(报了静态 StopPrice)。
	order := OpenOrder{
		OrderID: "placed-as-dd1", Symbol: wldSymbol, PositionSide: "SHORT",
		Type: "TRAILING_STOP_MARKET", ActivationStatus: "pending",
		StopPrice:      calculateProfitBasedTrailingTriggerPrice(wldEntry, wldSide, shallow.MinProfitPct),
		CallbackRate:   calculateDrawdownRuleCallbackRatio(wldEntry, wldSide, shallow),
		Quantity:       100,
		ProtectionTier: 1,
	}

	// 现在有人在策略里插了一档更浅的 —— 原来的 dd1 变成了序号 2,而这张单还自报 1。
	at.config.StrategyConfig.Protection.DrawdownTakeProfit.Rules = append(
		[]store.DrawdownTakeProfitRule{{
			MinProfitPct: 1.0, MinProfitUnit: store.ProtectionUnitATR, MinProfitMode: store.ProtectionValueModeManual,
			MaxDrawdownPct: 2.0, MaxDrawdownUnit: store.ProtectionUnitATR, MaxDrawdownMode: store.ProtectionValueModeManual,
			CloseRatioPct: 20, StageName: "inserted-shallow",
		}},
		bnWLDATRRules()...,
	)

	ordered := at.canonicalDrawdownTierRules(wldSymbol, wldSide, wldEntry)
	if len(ordered) != 3 || ordered[0].StageName != "inserted-shallow" {
		t.Fatalf("fixture: tier list edit did not take effect: %d tiers, first=%q", len(ordered), ordered[0].StageName)
	}

	// 自报序号 1 现在指向 inserted-shallow —— 形状不符,必须被拒。
	if got := at.tierTaggedRuleIndex(order, wldSide, wldEntry, ordered); got != -1 {
		t.Fatalf("stale tier tag 1 was trusted and resolved to index %d (%q) — the order was placed as dd1; "+
			"trusting the ordinal after a tier-list edit hands a live order to the wrong rule",
			got, ordered[got].StageName)
	}

	// 但形状匹配必须仍然把它认回真正的 dd1(现在是 index 1)。
	kept, matches := at.reclaimLiveDrawdownTrailingOrders(wldSymbol, wldSide, wldEntry, []string{"placed-as-dd1"}, []OpenOrder{order})
	if len(matches) != 1 || len(kept) != 0 {
		t.Fatalf("shape matching did not rescue the order after the tier edit: kept=%v matches=%d", kept, len(matches))
	}
	if matches[0].Rule.StageName != "dd1" {
		t.Fatalf("the order was reclaimed by tier %q, want dd1", matches[0].Rule.StageName)
	}
}
