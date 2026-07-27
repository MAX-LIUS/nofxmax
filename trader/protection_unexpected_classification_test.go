package trader

import "testing"

func TestClassifyUnexpectedProtectionOrdersSeparatesManualForeignFromBotDuplicate(t *testing.T) {
	plan := &ProtectionPlan{NeedsStopLoss: true, StopLossPrice: 100.9}
	orders := []OpenOrder{
		{OrderID: "keep_sl", PositionSide: "SHORT", Type: "STOP_MARKET", StopPrice: 100.9},
		{OrderID: "4c363c81edc5bcde_ladder_sl_old", PositionSide: "SHORT", Type: "STOP_MARKET", StopPrice: 105},
		{OrderID: "manual-protective-stop", PositionSide: "SHORT", Type: "STOP_MARKET", StopPrice: 110},
	}

	summary := classifyUnexpectedProtectionOrders(orders, "SHORT", plan, false, nativeTrailingArmedOnly(false), true)
	if summary.ExpectedStaticOwner != 1 {
		t.Fatalf("expected one static owner, got %+v", summary)
	}
	if summary.StaleBotDuplicate != 1 || len(summary.StaleBotDuplicateIDs) != 1 || summary.StaleBotDuplicateIDs[0] != "4c363c81edc5bcde_ladder_sl_old" {
		t.Fatalf("expected one stale bot duplicate id, got %+v", summary)
	}
	if summary.ManualOrForeign != 1 || len(summary.ManualOrForeignIDs) != 1 || summary.ManualOrForeignIDs[0] != "manual-protective-stop" {
		t.Fatalf("expected one manual/foreign id, got %+v", summary)
	}

	ids := collectUnexpectedProtectionOrderIDs(orders, "SHORT", plan, false, nativeTrailingArmedOnly(false))
	if len(ids) != 1 || ids[0] != "4c363c81edc5bcde_ladder_sl_old" {
		t.Fatalf("expected cleanup ids to include only stale bot duplicate, got %+v", ids)
	}
}

func TestClassifyUnexpectedProtectionOrdersOrphanForInactivePosition(t *testing.T) {
	orders := []OpenOrder{{OrderID: "native_trailing_old", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET", StopPrice: 99, CallbackRate: 0.02}}
	summary := classifyUnexpectedProtectionOrders(orders, "SHORT", nil, false, nativeTrailingArmedOnly(false), false)
	if summary.OrphanForInactive != 1 || len(summary.OrphanForInactiveIDs) != 1 || summary.OrphanForInactiveIDs[0] != "native_trailing_old" {
		t.Fatalf("expected inactive trailing order classified as orphan, got %+v", summary)
	}
}

// TestClassifyUnexpectedProtectionOrdersSplitsDynamicOwnerByKind 钉住 dynamicOwner
// 的成分拆分。线上现象:同一 symbol 上 dynamicOwner 在 2/3 之间来回,原因是有的
// trader 已把止损推到保本(2 trailing + 1 BE = 3),有的还没(2 trailing = 2)。
// 混计成一个数字后,日志读不出"多的那张是 trailing(会多平仓)还是保本止损(正常)",
// 每次都要翻上下文猜。拆分后必须能一眼分辨。
func TestClassifyUnexpectedProtectionOrdersSplitsDynamicOwnerByKind(t *testing.T) {
	twoTrailingIDs := map[string]struct{}{"trail_dd1": {}, "trail_partial": {}}
	ownership := nativeTrailingOwnership{Armed: true, Claimed: twoTrailingIDs}
	trailingOrders := []OpenOrder{
		{OrderID: "trail_dd1", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET", StopPrice: 81.06, CallbackRate: 0.3},
		{OrderID: "trail_partial", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET", StopPrice: 80.18, CallbackRate: 0.3},
	}

	// 保本止损尚未推出:只有两张 trailing。
	summary := classifyUnexpectedProtectionOrders(trailingOrders, "SHORT", nil, false, ownership, true)
	if summary.ExpectedDynamicOwner != 2 || summary.ExpectedDynamicTrailing != 2 || summary.ExpectedDynamicStop != 0 {
		t.Fatalf("两张 trailing 应为 total=2 trail=2 be=0,got %+v", summary)
	}

	// 保本止损已推出:总数变 3,但多出来的那张必须归到 be,不能污染 trail 计数。
	withBE := append(append([]OpenOrder{}, trailingOrders...),
		OpenOrder{OrderID: "be_stop", PositionSide: "SHORT", Type: "STOP_MARKET", StopPrice: 83.68})
	summary = classifyUnexpectedProtectionOrders(withBE, "SHORT", nil, true, ownership, true)
	if summary.ExpectedDynamicOwner != 3 || summary.ExpectedDynamicTrailing != 2 || summary.ExpectedDynamicStop != 1 {
		t.Fatalf("两张 trailing + 保本止损应为 total=3 trail=2 be=1,got %+v", summary)
	}
	if summary.StaleTrailingDuplicate != 0 || summary.ManualOrForeign != 0 {
		t.Fatalf("认领内的单不该被判异常,got %+v", summary)
	}

	// 真问题的形态:第三张 trailing 未被认领 → 必须落在 staleTrail,而不是被 be 吸收。
	withExtraTrail := append(append([]OpenOrder{}, trailingOrders...),
		OpenOrder{OrderID: "native_trailing_orphan", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET", StopPrice: 79.5, CallbackRate: 0.3})
	summary = classifyUnexpectedProtectionOrders(withExtraTrail, "SHORT", nil, false, ownership, true)
	if summary.ExpectedDynamicTrailing != 2 || summary.StaleTrailingDuplicate != 1 {
		t.Fatalf("未认领的第三张 trailing 应记为 staleTrail=1,got %+v", summary)
	}
}

// TestClassifyUnexpectedProtectionOrdersBreakEvenBudgetIsOrderIndependent 钉住一个
// 概率性误判:"保本止损额度只有一张"的消耗判定原本用 looksLikeStopLoss(order),
// 而 TRAILING_STOP_MARKET 的 Type 里含 "STOP" → 第一张 trailing 就把额度吃掉,
// 真正的保本止损若排在 trailing 之后就被判 manual_or_foreign(外来单),ownership 降级。
// 交易所返回挂单的顺序不做保证,所以两种顺序必须得到同一结论。
func TestClassifyUnexpectedProtectionOrdersBreakEvenBudgetIsOrderIndependent(t *testing.T) {
	ownership := nativeTrailingOwnership{Armed: true, Claimed: map[string]struct{}{"trail_a": {}, "trail_b": {}}}
	trailA := OpenOrder{OrderID: "trail_a", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET", StopPrice: 81.06, CallbackRate: 0.3}
	trailB := OpenOrder{OrderID: "trail_b", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET", StopPrice: 80.18, CallbackRate: 0.3}
	beStop := OpenOrder{OrderID: "be_stop", PositionSide: "SHORT", Type: "STOP_MARKET", StopPrice: 83.68}

	for name, orders := range map[string][]OpenOrder{
		"be_first": {beStop, trailA, trailB},
		"be_last":  {trailA, trailB, beStop},
		"be_mid":   {trailA, beStop, trailB},
	} {
		summary := classifyUnexpectedProtectionOrders(orders, "SHORT", nil, true, ownership, true)
		if summary.ManualOrForeign != 0 {
			t.Fatalf("%s:保本止损被误判为外来单,got %+v", name, summary)
		}
		if summary.ExpectedDynamicOwner != 3 || summary.ExpectedDynamicTrailing != 2 || summary.ExpectedDynamicStop != 1 {
			t.Fatalf("%s:应为 total=3 trail=2 be=1,got %+v", name, summary)
		}
	}

	// 额度确实只有一张:两张止损时第二张不能也蹭到保本额度。
	secondStop := OpenOrder{OrderID: "extra_stop", PositionSide: "SHORT", Type: "STOP_MARKET", StopPrice: 85.5}
	summary := classifyUnexpectedProtectionOrders([]OpenOrder{trailA, beStop, secondStop}, "SHORT", nil, true, ownership, true)
	if summary.ExpectedDynamicStop != 1 {
		t.Fatalf("保本额度应只够一张止损,got %+v", summary)
	}
}

func TestClassifyUnexpectedProtectionOrdersExpectedDynamicOwners(t *testing.T) {
	orders := []OpenOrder{
		{OrderID: "be-stop", PositionSide: "LONG", Type: "STOP_MARKET", StopPrice: 100},
		{OrderID: "native_trailing_1", PositionSide: "LONG", Type: "TRAILING_STOP_MARKET", StopPrice: 105, CallbackRate: 0.02},
	}
	summary := classifyUnexpectedProtectionOrders(orders, "LONG", nil, true, nativeTrailingArmedOnly(true), true)
	if summary.ExpectedDynamicOwner != 2 {
		t.Fatalf("expected break-even and trailing as dynamic owners, got %+v", summary)
	}
	if summary.StaleBotDuplicate != 0 || summary.ManualOrForeign != 0 {
		t.Fatalf("expected no unexpected categories for dynamic owners, got %+v", summary)
	}
}

// TestBinanceBrokerPrefixRecognizedAsBot verifies that a stale Binance protection
// order — whose client ID carries ONLY the Binance broker prefix (x-KzrpZaP9),
// not any semantic tag — is classified as a stale bot duplicate (cleanable), not
// manual/foreign (preserved forever). This is the fix for phantom stop-order
// accumulation across re-entries on Binance traders.
func TestBinanceBrokerPrefixRecognizedAsBot(t *testing.T) {
	plan := &ProtectionPlan{NeedsStopLoss: true, StopLossPrice: 100.9}
	orders := []OpenOrder{
		{OrderID: "keep_sl", ClientOrderID: "x-KzrpZaP91234567890abcd", PositionSide: "LONG", Type: "STOP_MARKET", StopPrice: 100.9},
		// stale bot stop from a prior entry: only the Binance broker prefix, no semantic tag
		{OrderID: "998877", ClientOrderID: "x-KzrpZaP99876543210wxyz", PositionSide: "LONG", Type: "STOP_MARKET", StopPrice: 95.0},
		// a genuine foreign/manual order (no broker prefix) must still be preserved
		{OrderID: "manual-1", ClientOrderID: "someones-manual-stop", PositionSide: "LONG", Type: "STOP_MARKET", StopPrice: 90.0},
	}
	summary := classifyUnexpectedProtectionOrders(orders, "LONG", plan, false, nativeTrailingArmedOnly(false), true)
	if summary.StaleBotDuplicate != 1 || len(summary.StaleBotDuplicateIDs) != 1 {
		t.Fatalf("expected the Binance-prefixed stale stop to be a bot duplicate, got %+v", summary)
	}
	if summary.ManualOrForeign != 1 || len(summary.ManualOrForeignIDs) != 1 || summary.ManualOrForeignIDs[0] != "manual-1" {
		t.Fatalf("expected only the true manual order preserved as foreign, got %+v", summary)
	}
}

func TestIsLikelyBotProtectionOrder_BinanceAndOKXPrefixes(t *testing.T) {
	cases := []struct {
		name   string
		order  OpenOrder
		wantBot bool
	}{
		{"binance broker prefix", OpenOrder{ClientOrderID: "x-KzrpZaP91700000000001a2"}, true},
		{"okx broker prefix", OpenOrder{ClientOrderID: "4c363c81edc5BCDE-full_sl"}, true},
		{"semantic full_ tag", OpenOrder{ClientOrderID: "full_sl_something"}, true},
		{"genuine manual", OpenOrder{ClientOrderID: "my-manual-stop"}, false},
		{"empty", OpenOrder{}, false},
	}
	for _, c := range cases {
		if got := isLikelyBotProtectionOrder(c.order); got != c.wantBot {
			t.Errorf("%s: isLikelyBotProtectionOrder=%v want %v", c.name, got, c.wantBot)
		}
	}
}
