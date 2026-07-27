package trader

import (
	"path/filepath"
	"testing"
	"time"

	"nofx/store"
	tradertypes "nofx/trader/types"
)

// ── 2026-07-27 线上回归:双档仓位 DD1/DD2 恒红灯 ───────────────────────────────
//
// 线上 CLUSDT / SKHYUSDT 都是 dd1(close=100%)+ partial_profit_lock(close=30%)双档。
// 双档同时武装 ⇒ getDrawdownExecutionMode 返回 "native_trailing_tiers",而面板 runtime
// 的归属判断写的是字面量白名单 {native_partial_trailing, native_trailing_full} ——
// 两个都不等,整段"按 algoId 找回交易所那张单"的匹配被跳过,matchedLive 恒 false,
// computeExchangeLight 走 `if !matchedLive { return "red" }`。
//
// 于是:交易所上两张 trailing 单健康挂着、armed 记录里 algoId 齐全、reconciler 报
// dynamicOwner=2 claimedTrail=2 一切正常,**只有面板两档全红**。误报方向最坏 ——
// 它训练人忽略红灯。
//
// 这个测试走完整的 buildPositionProtectionRuntime 路径(而不是只测 computeExchangeLight,
// 那个函数从来没坏),所以它能真正钉住缺陷。

type ddLightMultiTierTrader struct {
	orders []tradertypes.OpenOrder
}

func (f *ddLightMultiTierTrader) GetBalance() (map[string]interface{}, error) { return nil, nil }
func (f *ddLightMultiTierTrader) GetPositions() ([]map[string]interface{}, error) {
	return []map[string]interface{}{{
		"symbol":    "CLUSDT",
		"side":      "short",
		"markPrice": 82.5,
	}}, nil
}
func (f *ddLightMultiTierTrader) OpenLong(symbol string, quantity float64, leverage int) (map[string]interface{}, error) {
	return nil, nil
}
func (f *ddLightMultiTierTrader) OpenShort(symbol string, quantity float64, leverage int) (map[string]interface{}, error) {
	return nil, nil
}
func (f *ddLightMultiTierTrader) CloseLong(symbol string, quantity float64) (map[string]interface{}, error) {
	return nil, nil
}
func (f *ddLightMultiTierTrader) CloseShort(symbol string, quantity float64) (map[string]interface{}, error) {
	return nil, nil
}
func (f *ddLightMultiTierTrader) SetLeverage(symbol string, leverage int) error      { return nil }
func (f *ddLightMultiTierTrader) SetMarginMode(symbol string, isCross bool) error    { return nil }
func (f *ddLightMultiTierTrader) GetMarketPrice(symbol string) (float64, error)      { return 82.5, nil }
func (f *ddLightMultiTierTrader) CancelStopLossOrders(symbol string) error           { return nil }
func (f *ddLightMultiTierTrader) CancelTakeProfitOrders(symbol string) error         { return nil }
func (f *ddLightMultiTierTrader) CancelAllOrders(symbol string) error                { return nil }
func (f *ddLightMultiTierTrader) CancelStopOrders(symbol string) error               { return nil }
func (f *ddLightMultiTierTrader) SetStopLoss(s, ps string, q, p float64) error       { return nil }
func (f *ddLightMultiTierTrader) SetTakeProfit(s, ps string, q, p float64) error     { return nil }
func (f *ddLightMultiTierTrader) FormatQuantity(s string, q float64) (string, error) { return "", nil }
func (f *ddLightMultiTierTrader) GetOrderStatus(s, o string) (map[string]interface{}, error) {
	return nil, nil
}
func (f *ddLightMultiTierTrader) GetClosedPnL(start time.Time, limit int) ([]tradertypes.ClosedPnLRecord, error) {
	return nil, nil
}
func (f *ddLightMultiTierTrader) GetOpenOrders(symbol string) ([]tradertypes.OpenOrder, error) {
	return f.orders, nil
}

// ddLightTierRules 复刻线上的两档形态:dd1 全量 + partial_profit_lock 部分。
// 百分比用纯 percent 单位,避开 ATR 冻结路径 —— 本测试要钉的是"归属判断",
// 不是 ATR 换算。
func ddLightTierRules() []store.DrawdownTakeProfitRule {
	return []store.DrawdownTakeProfitRule{
		{MinProfitPct: 3.13, MaxDrawdownPct: 30, CloseRatioPct: 100, StageName: "dd1"},
		{MinProfitPct: 4.17, MaxDrawdownPct: 30, CloseRatioPct: 30, StageName: "partial_profit_lock"},
	}
}

func ddLightTierExchangeLights(t *testing.T, at *AutoTrader, openOrders []tradertypes.OpenOrder) []string {
	t.Helper()
	rt := at.buildPositionProtectionRuntime("CLUSDT", "short", 2.8, 83.68, openOrders)
	tiers, ok := rt["scheduled_tiers"].([]map[string]interface{})
	if !ok {
		t.Fatalf("scheduled_tiers missing or wrong type: %T", rt["scheduled_tiers"])
	}
	lights := make([]string, 0, len(tiers))
	for _, tier := range tiers {
		light, _ := tier["exchange_light"].(string)
		lights = append(lights, light)
	}
	return lights
}

func TestDDLight_TwoArmedTiersWithLiveOrdersAreNotRed(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "dd-light-multitier.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	const traderID = "trader-dd-light"
	const entry = 83.68
	rules := ddLightTierRules()

	// 线上的两张 algo 单:dd1 全量 + partial 30%。activation price 都还没被 mark(82.5)
	// 触达(空单要 mark <= activePx 才算越过),所以健康 resting ⇒ 应该是 green。
	orders := []tradertypes.OpenOrder{
		{OrderID: "3780478498666631168", Symbol: "CLUSDT", Side: "BUY", PositionSide: "SHORT",
			Type: "TRAILING_STOP_MARKET", Quantity: 2.8, ActivationPrice: 81.06, CallbackRate: 0.018786},
		{OrderID: "3780478547286982656", Symbol: "CLUSDT", Side: "BUY", PositionSide: "SHORT",
			Type: "TRAILING_STOP_MARKET", Quantity: 0.84, ActivationPrice: 80.18, CallbackRate: 0.012524},
	}

	// 两条 armed 记录,各自带自己的 algoId,RuleFingerprint 用生产同一个函数生成。
	records := []store.DynamicProtectionRecord{
		{TraderID: traderID, Symbol: "CLUSDT", Side: "short", PositionFingerprint: positionFingerprint(entry, 2.8),
			ProtectionType: "native_trailing", RuleFingerprint: stableDrawdownRuleFingerprint(entry, rules[0]),
			CloseRatioPct: 100, Status: "armed", ExchangeOrderID: "3780478498666631168",
			ActivationPrice: 81.06, CallbackRatio: 0.018786, UpdatedAt: 1000},
		{TraderID: traderID, Symbol: "CLUSDT", Side: "short", PositionFingerprint: positionFingerprint(entry, 2.8),
			ProtectionType: "native_partial_trailing", RuleFingerprint: stableDrawdownRuleFingerprint(entry, rules[1]),
			CloseRatioPct: 30, Status: "armed", ExchangeOrderID: "3780478547286982656",
			ActivationPrice: 80.18, CallbackRatio: 0.012524, Quantity: 0.84, UpdatedAt: 2000},
	}
	for _, record := range records {
		if err := st.SaveDynamicProtectionRecord(record); err != nil {
			t.Fatalf("save dynamic protection record: %v", err)
		}
	}

	at := &AutoTrader{
		id:       traderID,
		exchange: "okx",
		store:    st,
		trader:   &ddLightMultiTierTrader{orders: orders},
		config: AutoTraderConfig{
			StrategyConfig: &store.StrategyConfig{},
		},
	}
	at.config.StrategyConfig.Protection.DrawdownTakeProfit = store.DrawdownTakeProfitConfig{
		Enabled: true,
		Mode:    store.ProtectionModeManual,
		Rules:   rules,
	}
	at.loadDynamicProtectionStateFromStore()

	// 前置条件:双档武装 ⇒ mode 必须是 tiers 形态(这正是白名单漏掉的那个值)。
	if mode := at.getDrawdownExecutionMode("CLUSDT", "short"); mode != "native_trailing_tiers" {
		t.Fatalf("precondition: expected native_trailing_tiers, got %q", mode)
	}

	lights := ddLightTierExchangeLights(t, at, orders)
	if len(lights) != 2 {
		t.Fatalf("expected 2 scheduled tiers, got %d (%v)", len(lights), lights)
	}
	for i, light := range lights {
		if light == "red" {
			t.Errorf("tier %d exchange_light=red, but its trailing order is live on the exchange with a matching algoId — 这正是线上 DD1/DD2 恒红灯的症状", i+1)
		}
		if light != "green" {
			t.Errorf("tier %d exchange_light=%q, want green (resting, activePx 未触达)", i+1, light)
		}
	}
}

// 修复不能把真红灯一起抹掉:同样的双档 mode 下,交易所上一张单都没有时两档都必须红。
func TestDDLight_TwoArmedTiersWithNoLiveOrdersStayRed(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "dd-light-multitier-missing.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	const traderID = "trader-dd-light-missing"
	const entry = 83.68
	rules := ddLightTierRules()

	records := []store.DynamicProtectionRecord{
		{TraderID: traderID, Symbol: "CLUSDT", Side: "short", PositionFingerprint: positionFingerprint(entry, 2.8),
			ProtectionType: "native_trailing", RuleFingerprint: stableDrawdownRuleFingerprint(entry, rules[0]),
			CloseRatioPct: 100, Status: "armed", ExchangeOrderID: "gone-1", UpdatedAt: 1000},
		{TraderID: traderID, Symbol: "CLUSDT", Side: "short", PositionFingerprint: positionFingerprint(entry, 2.8),
			ProtectionType: "native_partial_trailing", RuleFingerprint: stableDrawdownRuleFingerprint(entry, rules[1]),
			CloseRatioPct: 30, Status: "armed", ExchangeOrderID: "gone-2", UpdatedAt: 2000},
	}
	for _, record := range records {
		if err := st.SaveDynamicProtectionRecord(record); err != nil {
			t.Fatalf("save dynamic protection record: %v", err)
		}
	}

	at := &AutoTrader{
		id:       traderID,
		exchange: "okx",
		store:    st,
		trader:   &ddLightMultiTierTrader{orders: nil},
		config: AutoTraderConfig{
			StrategyConfig: &store.StrategyConfig{},
		},
	}
	at.config.StrategyConfig.Protection.DrawdownTakeProfit = store.DrawdownTakeProfitConfig{
		Enabled: true,
		Mode:    store.ProtectionModeManual,
		Rules:   rules,
	}
	at.loadDynamicProtectionStateFromStore()

	lights := ddLightTierExchangeLights(t, at, nil)
	for i, light := range lights {
		if light != "red" {
			t.Errorf("tier %d exchange_light=%q, want red — 单真的不在交易所上时必须报红,修复不能顺手把真红灯抹掉", i+1, light)
		}
	}
}
