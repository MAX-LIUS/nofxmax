package trader

import (
	"testing"

	"nofx/store"
)

// 这组测试锁死"挂单 tag 带档标识"这重保险的两端:
//   写侧 —— 每一档算出稳定且互不相同的序号,顺序不随配置里规则的书写顺序漂移;
//   读侧 —— 认领路径优先信任单子自报的档位,而不是"按 MinProfitPct 取最低未用档"
//           那个在多档并存时会认错档的兜底猜测。

// 复用 drawdown_order_reclaim_test.go 的 reclaimFixture:同一套 AutoTrader 装配
// (store / config.StrategyConfig.Protection.DrawdownTakeProfit / 各 map 初始化),
// 不另起一套等价 fixture。
func newTierTagTrader(t *testing.T, rules []store.DrawdownTakeProfitRule) *AutoTrader {
	t.Helper()
	return reclaimFixture(t, "TIERUSDT", "long", rules)
}

func TestTierTagIndex_IsStableAndUniquePerTier(t *testing.T) {
	// 故意乱序书写:序号必须按 MinProfitPct 升序算,与配置里的书写顺序无关。
	dd3 := store.DrawdownTakeProfitRule{MinProfitPct: 6, MaxDrawdownPct: 30, CloseRatioPct: 100}
	dd1 := store.DrawdownTakeProfitRule{MinProfitPct: 2, MaxDrawdownPct: 20, CloseRatioPct: 30}
	dd2 := store.DrawdownTakeProfitRule{MinProfitPct: 4, MaxDrawdownPct: 25, CloseRatioPct: 40}
	at := newTierTagTrader(t, []store.DrawdownTakeProfitRule{dd3, dd1, dd2})

	if got := at.drawdownTierTagIndex(dd1); got != 1 {
		t.Fatalf("dd1 (MinProfit=2) tag index = %d, want 1", got)
	}
	if got := at.drawdownTierTagIndex(dd2); got != 2 {
		t.Fatalf("dd2 (MinProfit=4) tag index = %d, want 2", got)
	}
	if got := at.drawdownTierTagIndex(dd3); got != 3 {
		t.Fatalf("dd3 (MinProfit=6) tag index = %d, want 3", got)
	}

	// 重排配置的书写顺序,序号必须完全不变 —— 否则同一档在两次进程/两次挂单里编出
	// 不同标识,标识就成了噪音。
	at2 := newTierTagTrader(t, []store.DrawdownTakeProfitRule{dd1, dd2, dd3})
	for _, tc := range []struct {
		rule store.DrawdownTakeProfitRule
		want int
	}{{dd1, 1}, {dd2, 2}, {dd3, 3}} {
		if got := at2.drawdownTierTagIndex(tc.rule); got != tc.want {
			t.Fatalf("after reordering config: tag index = %d, want %d", got, tc.want)
		}
	}
}

func TestTierTagIndex_SameMinProfitTiersGetDistinctIndexes(t *testing.T) {
	// 同一个 MinProfitPct 的两档(分批止盈常见):必须靠次级键分出稳定的先后,
	// 否则两档共用一个标识,读侧就分不开了。
	a := store.DrawdownTakeProfitRule{MinProfitPct: 3, MaxDrawdownPct: 20, CloseRatioPct: 40}
	b := store.DrawdownTakeProfitRule{MinProfitPct: 3, MaxDrawdownPct: 30, CloseRatioPct: 100}
	at := newTierTagTrader(t, []store.DrawdownTakeProfitRule{b, a})

	ia, ib := at.drawdownTierTagIndex(a), at.drawdownTierTagIndex(b)
	if ia == 0 || ib == 0 {
		t.Fatalf("both tiers must resolve to a tag index, got a=%d b=%d", ia, ib)
	}
	if ia == ib {
		t.Fatalf("tiers sharing MinProfitPct got the same tag index %d — they would be indistinguishable on the exchange", ia)
	}
	if ia != 1 || ib != 2 {
		t.Fatalf("secondary ordering (MaxDrawdownPct asc) not applied: a=%d b=%d, want a=1 b=2", ia, ib)
	}
}

func TestTierTagIndex_UnknownRuleAndEmptyConfigDegradeToZero(t *testing.T) {
	dd1 := store.DrawdownTakeProfitRule{MinProfitPct: 2, MaxDrawdownPct: 20, CloseRatioPct: 30}
	at := newTierTagTrader(t, []store.DrawdownTakeProfitRule{dd1})

	foreign := store.DrawdownTakeProfitRule{MinProfitPct: 99, MaxDrawdownPct: 99, CloseRatioPct: 100}
	if got := at.drawdownTierTagIndex(foreign); got != 0 {
		t.Fatalf("rule absent from config got tag index %d, want 0 (must not guess)", got)
	}

	empty := newTierTagTrader(t, nil)
	if got := empty.drawdownTierTagIndex(dd1); got != 0 {
		t.Fatalf("empty rule list got tag index %d, want 0", got)
	}
}

func TestTrailingReasonTag_CarriesTierAndStaysMechanismEquivalent(t *testing.T) {
	dd1 := store.DrawdownTakeProfitRule{MinProfitPct: 2, MaxDrawdownPct: 20, CloseRatioPct: 30}
	dd2 := store.DrawdownTakeProfitRule{MinProfitPct: 4, MaxDrawdownPct: 25, CloseRatioPct: 100}
	at := newTierTagTrader(t, []store.DrawdownTakeProfitRule{dd1, dd2})

	tag1 := at.drawdownTrailingReasonTag(dd1)
	tag2 := at.drawdownTrailingReasonTag(dd2)
	if tag1 == tag2 {
		t.Fatalf("both tiers produced the same reason tag %q", tag1)
	}
	// 机制侧必须与裸 native_trailing 完全等价,否则 OKX 定向清理会跳过、归因会断链。
	for _, tag := range []string{tag1, tag2} {
		if got := store.NormalizeMechanism(tag); got != "native_trailing" {
			t.Fatalf("tag %q normalizes to %q, want native_trailing", tag, got)
		}
		if store.CodeForReason(tag) == "" {
			t.Fatalf("tag %q lost its mechanism code", tag)
		}
	}
	// 无法定档时必须退回裸 reason,而不是编出半个标识。
	unknown := store.DrawdownTakeProfitRule{MinProfitPct: 77, MaxDrawdownPct: 77, CloseRatioPct: 100}
	if got := at.drawdownTrailingReasonTag(unknown); got != "native_trailing" {
		t.Fatalf("unresolvable tier produced %q, want bare native_trailing", got)
	}
}

func TestReclaim_TieredOrderIsClaimedByItsOwnTierNotTheLowestUnused(t *testing.T) {
	// 这是标识存在的理由。两档并存,交易所上只剩 dd2(高档)那张单,且它已经
	// activated —— 形状匹配对 activated 单一律返回 true,所以旧逻辑会把它认给
	// "最低未用档"dd1,dd2 于是被判缺单。带上自报档位后必须认给 dd2。
	dd1 := store.DrawdownTakeProfitRule{MinProfitPct: 2, MaxDrawdownPct: 20, CloseRatioPct: 30}
	dd2 := store.DrawdownTakeProfitRule{MinProfitPct: 4, MaxDrawdownPct: 25, CloseRatioPct: 100}
	at := newTierTagTrader(t, []store.DrawdownTakeProfitRule{dd1, dd2})

	symbol, side, entry := "SKHYNIXUSDT", "short", 1000.0
	wantTier := at.drawdownTierTagIndex(dd2)
	if wantTier != 2 {
		t.Fatalf("fixture: dd2 tag index = %d, want 2", wantTier)
	}

	order := OpenOrder{
		OrderID:          "algo-dd2",
		Symbol:           symbol,
		PositionSide:     "SHORT",
		Type:             "TRAILING_STOP_MARKET",
		ActivationStatus: "activated",
		Quantity:         0.035,
		ProtectionTier:   wantTier,
	}

	kept, matches := at.reclaimLiveDrawdownTrailingOrders(symbol, side, entry, []string{"algo-dd2"}, []OpenOrder{order})
	if len(kept) != 0 {
		t.Fatalf("a live tiered protective order must be removed from the cancel list, kept=%v", kept)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly 1 reclaim match, got %d", len(matches))
	}
	got := matches[0].Rule
	if got.CloseRatioPct != dd2.CloseRatioPct || got.MinProfitPct != dd2.MinProfitPct {
		t.Fatalf("order self-reporting tier %d was claimed by the wrong tier (MinProfit=%.1f close=%.1f%%); want dd2 (MinProfit=%.1f close=%.1f%%)",
			wantTier, got.MinProfitPct, got.CloseRatioPct, dd2.MinProfitPct, dd2.CloseRatioPct)
	}
}

func TestReclaim_UntaggedOrderStillFallsBackToShapeMatching(t *testing.T) {
	// 反向锁:标识只是第二重保险,不能让旧单(ProtectionTier=0)失去认领能力 ——
	// 那会把线上现存的、重启前挂的单全部变成可撤,正是最坏的退化。
	dd1 := store.DrawdownTakeProfitRule{MinProfitPct: 2, MaxDrawdownPct: 20, CloseRatioPct: 100}
	at := newTierTagTrader(t, []store.DrawdownTakeProfitRule{dd1})

	symbol, side, entry := "ZECUSDT", "short", 500.0
	order := OpenOrder{
		OrderID:          "algo-legacy",
		Symbol:           symbol,
		PositionSide:     "SHORT",
		Type:             "TRAILING_STOP_MARKET",
		ActivationStatus: "activated",
		Quantity:         0.06,
		// ProtectionTier 故意留 0:模拟本次改动之前挂出的单。
	}

	kept, matches := at.reclaimLiveDrawdownTrailingOrders(symbol, side, entry, []string{"algo-legacy"}, []OpenOrder{order})
	if len(matches) != 1 || len(kept) != 0 {
		t.Fatalf("untagged live order lost its shape-matching reclaim path: kept=%v matches=%d", kept, len(matches))
	}
}

func TestReclaim_DuplicateSelfReportedTierDoesNotDoubleClaim(t *testing.T) {
	// 两张单自报同一档 = 重复挂单。第一张认领,第二张不能顶掉它;第二张在只有一档
	// 配置、且该档已被占用时应交回撤单名单(它才是真正多余的那张)。
	dd1 := store.DrawdownTakeProfitRule{MinProfitPct: 2, MaxDrawdownPct: 20, CloseRatioPct: 100}
	at := newTierTagTrader(t, []store.DrawdownTakeProfitRule{dd1})

	symbol, side, entry := "HYPEUSDT", "long", 40.0
	tier := at.drawdownTierTagIndex(dd1)
	mk := func(id string) OpenOrder {
		return OpenOrder{
			OrderID: id, Symbol: symbol, PositionSide: "LONG",
			Type: "TRAILING_STOP_MARKET", ActivationStatus: "activated",
			Quantity: 1.0, ProtectionTier: tier,
		}
	}
	orders := []OpenOrder{mk("algo-a"), mk("algo-b")}

	kept, matches := at.reclaimLiveDrawdownTrailingOrders(symbol, side, entry, []string{"algo-a", "algo-b"}, orders)
	if len(matches) != 1 {
		t.Fatalf("one tier must claim at most one order, got %d matches", len(matches))
	}
	if matches[0].OrderID != "algo-a" {
		t.Fatalf("first order should win the claim, got %q", matches[0].OrderID)
	}
	if len(kept) != 1 || kept[0] != "algo-b" {
		t.Fatalf("the duplicate must return to the cancel list, kept=%v", kept)
	}
}
