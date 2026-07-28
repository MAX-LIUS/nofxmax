package trader

import (
	"fmt"
	"path/filepath"
	"testing"

	"nofx/store"
	tradertypes "nofx/trader/types"
)

// beReplaceTrader 在 fakeProtectionTrader 之上补两件事:
//  1. 挂单时分配真实 order id(基类的 SetStopLoss 挂出来的单没有 id,而逐档替换整条链
//     的前提就是"记录里有 id");
//  2. 记下按 id 撤单的调用序列,用来断言"撤了哪一张"。
type beReplaceTrader struct {
	fakeProtectionTrader
	placed      int
	cancelledBy []string
	cancelErrID string
}

func (f *beReplaceTrader) SetStopLossTagged(symbol string, positionSide string, quantity, stopPrice float64, reasonTag string) (string, error) {
	f.placed++
	id := fmt.Sprintf("algo-%d", f.placed)
	f.openOrders = append(f.openOrders, tradertypes.OpenOrder{
		OrderID:       id,
		Symbol:        symbol,
		PositionSide:  positionSide,
		Type:          "STOP_MARKET",
		StopPrice:     stopPrice,
		Quantity:      quantity,
		ClientOrderID: reasonTag,
	})
	f.setStopLossCalls++
	f.lastStopPrice = stopPrice
	return id, nil
}

func (f *beReplaceTrader) CancelAlgoOrderByID(symbol string, algoID string) error {
	f.cancelledBy = append(f.cancelledBy, algoID)
	if f.cancelErrID != "" && f.cancelErrID == algoID {
		return fmt.Errorf("simulated cancel failure for %s", algoID)
	}
	filtered := make([]tradertypes.OpenOrder, 0, len(f.openOrders))
	for _, order := range f.openOrders {
		if order.OrderID == algoID {
			continue
		}
		filtered = append(filtered, order)
	}
	f.openOrders = filtered
	return nil
}

func newBEReplaceAutoTrader(t *testing.T, fake *beReplaceTrader, dbName string) *AutoTrader {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), dbName))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	return &AutoTrader{
		id:                    "trader-be",
		exchange:              "okx",
		trader:                fake,
		store:                 st,
		config:                AutoTraderConfig{StrategyConfig: &store.StrategyConfig{}},
		protectionState:       map[string]string{},
		breakEvenState:        map[string]string{},
		breakEvenFingerprints: map[string]string{},
		drawdownState:         map[string]string{},
		peakPnLCache:          map[string]float64{},
	}
}

func beRules() []store.BreakEvenStopRule {
	return []store.BreakEvenStopRule{
		{TriggerMode: store.BreakEvenTriggerProfitPct, TriggerValue: 0.7, OffsetPct: 0.3, CloseRatioPct: 100, StageName: "BE1"},
		{TriggerMode: store.BreakEvenTriggerProfitPct, TriggerValue: 1.9, OffsetPct: 0.6, CloseRatioPct: 100, StageName: "BE2"},
	}
}

func liveBEStops(orders []tradertypes.OpenOrder) []float64 {
	out := []float64{}
	for _, order := range orders {
		if isBreakEvenTaggedOrder(order, "LONG") || isBreakEvenTaggedOrder(order, "SHORT") {
			out = append(out, order.StopPrice)
		}
	}
	return out
}

// 端到端复现 WLDUSDT 的堆积,并证明逐档替换把它止住。
//
// 第一轮:均价 50000,两档挂出 50150(BE1)/ 50300(BE2)。
// 第二轮:加仓把均价推到 51000,两档目标价变成 51153 / 51306。
// 改造前:新价匹配不上旧单 → 只挂不撤 → 交易所上变成 4 张。
// 改造后:每档先撤自己那张旧的,再挂新的 → 恒为 2 张。
func TestApplyBreakEvenStopsReplacesTierOnEntryDrift(t *testing.T) {
	fake := &beReplaceTrader{}
	fake.positions = []map[string]interface{}{{"symbol": "BTCUSDT", "side": "long", "positionAmt": 1.0}}
	at := newBEReplaceAutoTrader(t, fake, "be-drift.db")

	if err := at.applyBreakEvenStops("BTCUSDT", "long", 1.0, 50000, 2.0, beRules()); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if got := liveBEStops(fake.openOrders); len(got) != 2 {
		t.Fatalf("第一轮应有两档在场,got %v", got)
	}

	// 均价漂移(加仓),同一组档位算出新价。
	if err := at.applyBreakEvenStops("BTCUSDT", "long", 1.0, 51000, 2.0, beRules()); err != nil {
		t.Fatalf("second pass: %v", err)
	}

	live := liveBEStops(fake.openOrders)
	if len(live) != 2 {
		t.Fatalf("漂移后仍应恒为两档在场(改造前会变成 4 张),got %v (撤单序列 %v)", live, fake.cancelledBy)
	}
	if len(fake.cancelledBy) != 2 {
		t.Fatalf("两档各撤一次旧单,got %v", fake.cancelledBy)
	}
	// 新价必须是按新均价算的。
	wantBE1, wantBE2 := 51000*1.003, 51000*1.006
	found1, found2 := false, false
	for _, price := range live {
		if approximatelyEqualPrice(price, wantBE1) {
			found1 = true
		}
		if approximatelyEqualPrice(price, wantBE2) {
			found2 = true
		}
	}
	if !found1 || !found2 {
		t.Fatalf("在场单应为新均价下的两档 %.2f/%.2f,got %v", wantBE1, wantBE2, live)
	}
}

// 均价没动时不得产生任何撤挂 —— 否则每轮监控都会撤挂一次,那本身就是新的循环。
func TestApplyBreakEvenStopsIdempotentWithoutDrift(t *testing.T) {
	fake := &beReplaceTrader{}
	fake.positions = []map[string]interface{}{{"symbol": "BTCUSDT", "side": "long", "positionAmt": 1.0}}
	at := newBEReplaceAutoTrader(t, fake, "be-idempotent.db")

	if err := at.applyBreakEvenStops("BTCUSDT", "long", 1.0, 50000, 2.0, beRules()); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	placedAfterFirst := fake.placed

	for i := 0; i < 3; i++ {
		if err := at.applyBreakEvenStops("BTCUSDT", "long", 1.0, 50000, 2.0, beRules()); err != nil {
			t.Fatalf("repeat pass %d: %v", i, err)
		}
	}

	if fake.placed != placedAfterFirst {
		t.Fatalf("均价未变不应重复挂单,%d → %d", placedAfterFirst, fake.placed)
	}
	if len(fake.cancelledBy) != 0 {
		t.Fatalf("均价未变不应撤任何单,got %v", fake.cancelledBy)
	}
	if got := liveBEStops(fake.openOrders); len(got) != 2 {
		t.Fatalf("应恒为两档在场,got %v", got)
	}
}

// 破坏性场景:撤单失败时不得放弃保护 —— 该档仍要挂上新单(旧单多留一张是 reduce-only,
// 无资金风险,由 reconciler 兜底);绝不能出现"撤失败 → 也不挂 → 该档裸奔"。
func TestApplyBreakEvenStopsPlacesNewOrderEvenIfCancelFails(t *testing.T) {
	fake := &beReplaceTrader{}
	fake.positions = []map[string]interface{}{{"symbol": "BTCUSDT", "side": "long", "positionAmt": 1.0}}
	at := newBEReplaceAutoTrader(t, fake, "be-cancel-fail.db")

	if err := at.applyBreakEvenStops("BTCUSDT", "long", 1.0, 50000, 2.0, beRules()); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	// 让 BE1 的旧单撤单失败。
	fake.cancelErrID = "algo-1"

	if err := at.applyBreakEvenStops("BTCUSDT", "long", 1.0, 51000, 2.0, beRules()); err != nil {
		t.Fatalf("second pass: %v", err)
	}

	live := liveBEStops(fake.openOrders)
	wantBE1 := 51000 * 1.003
	has := false
	for _, price := range live {
		if approximatelyEqualPrice(price, wantBE1) {
			has = true
		}
	}
	if !has {
		t.Fatalf("撤单失败也必须挂上该档新单(不能裸奔),在场 %v", live)
	}
}

// 破坏性场景:只有一档配置时行为必须与改造前一致 —— 漂移后替换,不堆积。
func TestApplyBreakEvenStopsSingleTierStillReplaces(t *testing.T) {
	fake := &beReplaceTrader{}
	fake.positions = []map[string]interface{}{{"symbol": "BTCUSDT", "side": "short", "positionAmt": -1.0}}
	at := newBEReplaceAutoTrader(t, fake, "be-single.db")

	rules := []store.BreakEvenStopRule{
		{TriggerMode: store.BreakEvenTriggerProfitPct, TriggerValue: 0.7, OffsetPct: 0.3, CloseRatioPct: 100, StageName: "BE1"},
	}
	if err := at.applyBreakEvenStops("BTCUSDT", "short", 1.0, 50000, 2.0, rules); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if err := at.applyBreakEvenStops("BTCUSDT", "short", 1.0, 49000, 2.0, rules); err != nil {
		t.Fatalf("second pass: %v", err)
	}

	if got := liveBEStops(fake.openOrders); len(got) != 1 {
		t.Fatalf("单档漂移后仍应只有一张,got %v", got)
	}
	if len(fake.cancelledBy) != 1 {
		t.Fatalf("单档应撤掉自己的旧单一次,got %v", fake.cancelledBy)
	}
}

// 被替换掉的记录必须退出 armed —— 否则认领集合里留着一个已撤的 id,
// 分类器会把一张不存在的单当自己的,而在场的新单反而被判无主。
func TestReplacedBreakEvenRecordLeavesClaimSet(t *testing.T) {
	fake := &beReplaceTrader{}
	fake.positions = []map[string]interface{}{{"symbol": "BTCUSDT", "side": "long", "positionAmt": 1.0}}
	at := newBEReplaceAutoTrader(t, fake, "be-claim.db")

	if err := at.applyBreakEvenStops("BTCUSDT", "long", 1.0, 50000, 2.0, beRules()); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if err := at.applyBreakEvenStops("BTCUSDT", "long", 1.0, 51000, 2.0, beRules()); err != nil {
		t.Fatalf("second pass: %v", err)
	}

	claimed, tiers := at.claimedBreakEvenOrderIDsForPosition("BTCUSDT", "long")
	if tiers != 2 {
		t.Fatalf("档位数应为 2,got %d", tiers)
	}
	if len(claimed) != 2 {
		t.Fatalf("认领集合应恰好是两张在场单,got %v", claimed)
	}
	liveIDs := map[string]struct{}{}
	for _, order := range fake.openOrders {
		liveIDs[order.OrderID] = struct{}{}
	}
	for id := range claimed {
		if _, ok := liveIDs[id]; !ok {
			t.Fatalf("认领集合里出现已不在场的 id %s(在场 %v)", id, liveIDs)
		}
	}

	// 两张在场单都必须被分类为自己的,一张都不能进撤单集合。
	ownership := at.breakEvenOwnershipForPosition("BTCUSDT", "long", true)
	summary := classifyUnexpectedProtectionOrders(fake.openOrders, "LONG", nil, ownership, nativeTrailingArmedOnly(false), true)
	if summary.ExpectedDynamicStop != 2 || summary.StaleBotDuplicate != 0 {
		t.Fatalf("替换后两档都该被认成自己的,got %+v", summary)
	}
}
