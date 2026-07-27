package trader

import (
	"strings"
	"testing"

	"nofx/kernel"
	"nofx/store"
)

// TestAddOnEntryDoesNotLeakImmediateTrailing is the end-to-end regression test for
// the leak observed 2026-07-27 in production: three trailing orders resting on one
// Binance BN CLUSDT short (2.43 dd1 + 0.73 partial + 1.22 leaked) and two on the
// OKX ETHUSDT position (0.239 dd1 + 0.120 leaked).
//
// Mechanism: applyNativeProtectionTargetsAfterOpen places a 50% "immediate
// trailing" at step 0 as first-line defence for the window BEFORE tier protection
// exists. Retiring it used to be the responsibility of applyNativeTrailingDrawdown's
// freshly-armed path (its final statement). But that function returns true from four
// EARLIER points when a tier is already covered, and on an add-on entry every tier is
// already covered — so the cancel was never reached. Each add-on placed a new 50%
// order and left the previous one resting forever.
//
// The test drives the real entry path twice with no intervening state surgery: the
// second call IS the add-on. A correct system converges on the configured tier count
// regardless of how many times an entry is added to.
func TestAddOnEntryDoesNotLeakImmediateTrailing(t *testing.T) {
	const (
		symbol  = "CLUSDT"
		side    = "long"
		venue   = "binance"
		entry   = 83.54
		posQty  = 2.43
		firstSL = 81.00 // 3.04% away -> immediate trailing callback 3.04%
	)

	fake := &fakeVenueTrader{
		venue:     venue,
		markPrice: entry,
		position: map[string]interface{}{
			"symbol": symbol, "side": side,
			"positionAmt": posQty, "markPrice": entry, "entryPrice": entry,
		},
	}
	at := newVenueAutoTrader(t, venue, fake)
	at.config.StrategyConfig.Protection = store.ProtectionConfig{
		DrawdownTakeProfit: store.DrawdownTakeProfitConfig{
			Enabled: true,
			Mode:    store.ProtectionModeManual,
		},
	}

	// One full-close tier, percent units so ATR resolution is a no-op here.
	rules := []store.DrawdownTakeProfitRule{{
		MinProfitPct: 3.0, MaxDrawdownPct: 1.8, CloseRatioPct: 100, StageName: "dd1",
	}}
	newPlan := func() *ProtectionPlan {
		return &ProtectionPlan{
			DrawdownRules:  rules,
			StopLossOrders: []ProtectionOrder{{Price: firstSL, CloseRatioPct: 100}},
		}
	}
	newReq := func() *protectionExecutionRequest {
		return &protectionExecutionRequest{
			Symbol:       symbol,
			Action:       "open_" + side,
			PositionSide: strings.ToUpper(side),
			Quantity:     posQty,
			EntryPrice:   entry,
			Decision:     &kernel.Decision{Symbol: symbol, Action: "open_" + side},
		}
	}

	// --- 首次开仓 ---
	if err := at.applyNativeProtectionTargetsAfterOpen(newReq(), newPlan()); err != nil {
		t.Fatalf("first entry: %v", err)
	}

	// The immediate trailing must actually have been placed — otherwise this test
	// would pass vacuously and prove nothing about the leak.
	var sawImmediate bool
	for _, c := range fake.trailingCalls() {
		if c.quantity < posQty*0.9 {
			sawImmediate = true
		}
	}
	if !sawImmediate {
		t.Fatalf("step 0 never placed the 50%% immediate trailing; calls=%+v — test cannot observe the leak", fake.trailingCalls())
	}
	if n := fake.trailingCount(); n != 1 {
		t.Fatalf("首次开仓后应只剩 1 个 trailing(dd1),实剩 %d 个: %+v", n, fake.orders)
	}

	// --- 加仓(同一档已被覆盖,applyNativeTrailingDrawdown 走提前 return true 分支) ---
	callsBefore := len(fake.trailingCalls())
	if err := at.applyNativeProtectionTargetsAfterOpen(newReq(), newPlan()); err != nil {
		t.Fatalf("add-on entry: %v", err)
	}
	if len(fake.trailingCalls()) == callsBefore {
		t.Fatal("加仓没有下任何 trailing —— 该场景没被复现,断言无意义")
	}

	if n := fake.trailingCount(); n != 1 {
		t.Fatalf("加仓后交易所侧剩 %d 个 trailing,配置只有 1 档 —— immediate trailing 泄漏了(生产上 BN CLUSDT 因此挂了三单): %+v",
			n, fake.orders)
	}

	// 再加两次:泄漏是每次加仓累加一单,所以只有反复加仓才能区分"修好了"和"少漏一次"。
	for i := 0; i < 2; i++ {
		if err := at.applyNativeProtectionTargetsAfterOpen(newReq(), newPlan()); err != nil {
			t.Fatalf("add-on entry %d: %v", i+2, err)
		}
	}
	if n := fake.trailingCount(); n != 1 {
		t.Fatalf("连续加仓 3 次后剩 %d 个 trailing,应恒为 1: %+v", n, fake.orders)
	}

	// 泄漏的反面同样是 bug:不能把 dd1 自己撤掉,只留下 50% 的 immediate trailing。
	// 注意全平档下单时 quantity=0(交易所"平掉全部"约定),所以只有落在 (0, 90%仓位)
	// 区间的数量才说明剩下的是那张 50% 的部分单。
	remaining := fake.orders[0]
	if remaining.Quantity > 0 && remaining.Quantity < posQty*0.9 {
		t.Fatalf("剩下的是 %.4f(仓位 %.4f)的部分 trailing 而不是 dd1 全平档 —— "+
			"撤单撤反了,仓位只剩一半保护", remaining.Quantity, posQty)
	}
}
