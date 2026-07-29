package trader

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"nofx/store"
	tradertypes "nofx/trader/types"
)

// ── 2026-07-29 线上回归:BN 面板把回撤档画到入场价下方(亏损侧) ─────────────────
//
// 线上 BN BTCUSDT LONG entry=64367.60,两档 dd1(+1.33% 启动 / 回吐 0.80% / 平 100%)
// 与 partial_profit_lock(+2.13% / 0.64% / 平 30%)。峰值只到 +0.42%,**两档都没激活**。
// 面板却把 DD-1 画在 -0.38%、DD-2 画在 -0.22%,而 protection_plan_snapshot 落库的是
// 正确的 +0.52% / +1.48%。回撤档是保盈机制,永远不该出现在入场价下方 —— 用户按面板
// 判断风险位置会被直接误导。
//
// 根因:buildPositionProtectionRuntime 里 applyMatch 把 activationPrice 覆盖成交易所
// 回读的 trigger_price(= OpenOrder.StopPrice),而两家交易所对 trailing 单 StopPrice
// 的语义不同:
//
//	OKX  stopPrice := activePx(未激活时),只有 trailingState=="effective" 才换成
//	     moveTriggerPx → 未激活时 trigger 恰好等于激活价,覆盖无害。
//	BN   算法单分支 StopPrice = algoOrder.TriggerPrice = **当前跟踪止损价**(跟着峰值
//	     棘轮上移),根本不是激活门槛。拿它当锚,execution = 跟踪价×(1-callback) 等于
//	     把 callback 减了第二次,档位就掉到亏损侧。
//
// 这解释了为什么三个 OKX 交易员没有这个症状,只有 BN 有。
//
// 修法是 execution_price 的锚下限改用 plannedActivationPrice(由规则算出,不受交易所
// 回读污染)。不改 BN 适配器,因为 BN 报 ActivationPrice=triggerPrice 是
// nativeTrailingEffective / 幻影激活守卫依赖的既定契约(auto_trader_risk.go:1876)。

type ddAnchorTrader struct {
	orders []tradertypes.OpenOrder
	mark   float64
}

func (f *ddAnchorTrader) GetBalance() (map[string]interface{}, error) { return nil, nil }
func (f *ddAnchorTrader) GetPositions() ([]map[string]interface{}, error) {
	return []map[string]interface{}{{
		"symbol":    "BTCUSDT",
		"side":      "long",
		"markPrice": f.mark,
	}}, nil
}
func (f *ddAnchorTrader) OpenLong(s string, q float64, l int) (map[string]interface{}, error) {
	return nil, nil
}
func (f *ddAnchorTrader) OpenShort(s string, q float64, l int) (map[string]interface{}, error) {
	return nil, nil
}
func (f *ddAnchorTrader) CloseLong(s string, q float64) (map[string]interface{}, error) {
	return nil, nil
}
func (f *ddAnchorTrader) CloseShort(s string, q float64) (map[string]interface{}, error) {
	return nil, nil
}
func (f *ddAnchorTrader) SetLeverage(s string, l int) error                  { return nil }
func (f *ddAnchorTrader) SetMarginMode(s string, c bool) error               { return nil }
func (f *ddAnchorTrader) GetMarketPrice(s string) (float64, error)           { return f.mark, nil }
func (f *ddAnchorTrader) CancelStopLossOrders(s string) error                { return nil }
func (f *ddAnchorTrader) CancelTakeProfitOrders(s string) error              { return nil }
func (f *ddAnchorTrader) CancelAllOrders(s string) error                     { return nil }
func (f *ddAnchorTrader) CancelStopOrders(s string) error                    { return nil }
func (f *ddAnchorTrader) SetStopLoss(s, ps string, q, p float64) error       { return nil }
func (f *ddAnchorTrader) SetTakeProfit(s, ps string, q, p float64) error     { return nil }
func (f *ddAnchorTrader) FormatQuantity(s string, q float64) (string, error) { return "", nil }
func (f *ddAnchorTrader) GetOrderStatus(s, o string) (map[string]interface{}, error) {
	return nil, nil
}
func (f *ddAnchorTrader) GetClosedPnL(t time.Time, l int) ([]tradertypes.ClosedPnLRecord, error) {
	return nil, nil
}
func (f *ddAnchorTrader) GetOpenOrders(s string) ([]tradertypes.OpenOrder, error) {
	return f.orders, nil
}

const (
	ddAnchorEntry = 64367.60
	ddAnchorQty   = 0.006
)

// 复刻线上 BN 的两档(百分比单位,避开 ATR 冻结路径 —— 本测试钉的是锚的取值)。
func ddAnchorRules() []store.DrawdownTakeProfitRule {
	return []store.DrawdownTakeProfitRule{
		{MinProfitPct: 1.3307, MaxDrawdownPct: 0.7984, CloseRatioPct: 100, StageName: "dd1"},
		{MinProfitPct: 2.1292, MaxDrawdownPct: 0.6387, CloseRatioPct: 30, StageName: "partial_profit_lock"},
	}
}

// buildDDAnchorTrader 组一个双档已武装的 AutoTrader。exchange 可换,用来证明
// 修复对 OKX 与 BN 同时成立。
func buildDDAnchorTrader(t *testing.T, exchange string, orders []tradertypes.OpenOrder, mark float64) *AutoTrader {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "dd-anchor.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	const traderID = "trader-dd-anchor"
	rules := ddAnchorRules()
	records := []store.DynamicProtectionRecord{
		{TraderID: traderID, Symbol: "BTCUSDT", Side: "long",
			PositionFingerprint: positionFingerprint(ddAnchorEntry, ddAnchorQty),
			ProtectionType:      "native_trailing",
			RuleFingerprint:     stableDrawdownRuleFingerprint(ddAnchorEntry, rules[0]),
			CloseRatioPct:       100, Status: "armed", ExchangeOrderID: orders[0].OrderID,
			ActivationPrice: orders[0].ActivationPrice, CallbackRatio: orders[0].CallbackRate,
			UpdatedAt: 1000},
		{TraderID: traderID, Symbol: "BTCUSDT", Side: "long",
			PositionFingerprint: positionFingerprint(ddAnchorEntry, ddAnchorQty),
			ProtectionType:      "native_partial_trailing",
			RuleFingerprint:     stableDrawdownRuleFingerprint(ddAnchorEntry, rules[1]),
			CloseRatioPct:       30, Status: "armed", ExchangeOrderID: orders[1].OrderID,
			ActivationPrice: orders[1].ActivationPrice, CallbackRatio: orders[1].CallbackRate,
			Quantity:  ddAnchorQty * 0.3,
			UpdatedAt: 2000},
	}
	for _, r := range records {
		if err := st.SaveDynamicProtectionRecord(r); err != nil {
			t.Fatalf("save dynamic protection record: %v", err)
		}
	}
	at := &AutoTrader{
		id:       traderID,
		exchange: exchange,
		store:    st,
		trader:   &ddAnchorTrader{orders: orders, mark: mark},
		config:   AutoTraderConfig{StrategyConfig: &store.StrategyConfig{}},
	}
	at.config.StrategyConfig.Protection.DrawdownTakeProfit = store.DrawdownTakeProfitConfig{
		Enabled: true,
		Mode:    store.ProtectionModeManual,
		Rules:   rules,
	}
	at.loadDynamicProtectionStateFromStore()
	if mode := at.getDrawdownExecutionMode("BTCUSDT", "long"); mode != "native_trailing_tiers" {
		t.Fatalf("precondition: expected native_trailing_tiers, got %q", mode)
	}
	return at
}

func ddAnchorTiers(t *testing.T, at *AutoTrader, orders []tradertypes.OpenOrder) []map[string]interface{} {
	t.Helper()
	rt := at.buildPositionProtectionRuntime("BTCUSDT", "long", ddAnchorQty, ddAnchorEntry, orders)
	tiers, ok := rt["scheduled_tiers"].([]map[string]interface{})
	if !ok {
		t.Fatalf("scheduled_tiers missing or wrong type: %T", rt["scheduled_tiers"])
	}
	if len(tiers) != 2 {
		t.Fatalf("expected 2 scheduled tiers, got %d", len(tiers))
	}
	return tiers
}

// 主锁:BN 的 StopPrice 是"当前跟踪止损价"(峰值派生),回撤档的成交价仍必须 >= 入场价。
//
// 未修复时:activationPrice 被覆盖成 64636.10,execution = 64636.10×(1-0.007984)
// = 64120.0 → -0.38%,断言直接红。
func TestDDExecutionPrice_BinanceTrailingStopPriceMustNotSinkTierBelowEntry(t *testing.T) {
	// 线上实况:峰值 +0.42% ⇒ 峰值价 64637.94。BN 算法单把 StopPrice 填成跟着峰值走的
	// 当前跟踪止损价,而 ActivationPrice 在算法单分支同样被填成该值(见
	// binance/futures_orders.go 算法单分支 oo.ActivationPrice = triggerPrice)。
	// 两个字段都被污染,所以只有"锚用规则算出的 plannedActivationPrice"才救得回来。
	const peakPrice = 64637.94
	orders := []tradertypes.OpenOrder{
		{OrderID: "3781523903370133504", Symbol: "BTCUSDT", Side: "SELL", PositionSide: "LONG",
			Type: "TRAILING_STOP_MARKET", Quantity: ddAnchorQty,
			StopPrice: peakPrice, ActivationPrice: peakPrice, ActivationStatus: "activated",
			CallbackRate: 0.7984},
		{OrderID: "3781523903370133505", Symbol: "BTCUSDT", Side: "SELL", PositionSide: "LONG",
			Type: "TRAILING_STOP_MARKET", Quantity: ddAnchorQty * 0.3,
			StopPrice: peakPrice, ActivationPrice: peakPrice, ActivationStatus: "activated",
			CallbackRate: 0.6387},
	}
	at := buildDDAnchorTrader(t, "binance", orders, 64620.00)
	tiers := ddAnchorTiers(t, at, orders)

	for i, tier := range tiers {
		exec, _ := tier["execution_price"].(float64)
		if exec <= 0 {
			t.Fatalf("tier %d: execution_price missing", i+1)
		}
		pct := (exec - ddAnchorEntry) / ddAnchorEntry * 100
		if exec < ddAnchorEntry {
			t.Errorf("tier %d: execution_price %.2f (%.2f%%) 落在入场价 %.2f 下方 —— "+
				"回撤档是保盈机制,永远不该出现在亏损侧。这正是 BN 面板 DD-1 -0.38%% / "+
				"DD-2 -0.22%% 的线上症状(BN 的 StopPrice 是峰值派生的当前跟踪止损价,"+
				"被当成激活价后 callback 减了第二次)",
				i+1, exec, pct, ddAnchorEntry)
		}
	}

	// 精确值锁:两档都未激活(峰值 +0.42% < +1.33%),所以成交价 = 激活价×(1-callback),
	// 也就是 protection_plan_snapshot 落库的那两个数。
	wantPct := []float64{0.52, 1.48}
	for i, tier := range tiers {
		exec, _ := tier["execution_price"].(float64)
		gotPct := (exec - ddAnchorEntry) / ddAnchorEntry * 100
		if math.Abs(gotPct-wantPct[i]) > 0.02 {
			t.Errorf("tier %d: execution_price %.2f = %+.2f%%, want %+.2f%% "+
				"(与 protection_plan_snapshot 落库值一致)", i+1, exec, gotPct, wantPct[i])
		}
	}
}

// 反向锁 1:OKX 必须完全不受影响。OKX 未激活时 StopPrice==activePx,修复前后同值。
func TestDDExecutionPrice_OKXRestingTierUnchanged(t *testing.T) {
	// OKX 语义:未激活 ⇒ stopPrice := activePx,两者都是真激活价。
	act1 := ddAnchorEntry * (1 + 1.3307/100)
	act2 := ddAnchorEntry * (1 + 2.1292/100)
	orders := []tradertypes.OpenOrder{
		{OrderID: "okx-algo-1", Symbol: "BTCUSDT", Side: "SELL", PositionSide: "LONG",
			Type: "TRAILING_STOP_MARKET", Quantity: ddAnchorQty,
			StopPrice: act1, ActivationPrice: act1, ActivationStatus: "pending_activation",
			CallbackRate: 0.007984},
		{OrderID: "okx-algo-2", Symbol: "BTCUSDT", Side: "SELL", PositionSide: "LONG",
			Type: "TRAILING_STOP_MARKET", Quantity: ddAnchorQty * 0.3,
			StopPrice: act2, ActivationPrice: act2, ActivationStatus: "pending_activation",
			CallbackRate: 0.006387},
	}
	at := buildDDAnchorTrader(t, "okx", orders, 64620.00)
	tiers := ddAnchorTiers(t, at, orders)

	wantPct := []float64{0.52, 1.48}
	for i, tier := range tiers {
		exec, _ := tier["execution_price"].(float64)
		gotPct := (exec - ddAnchorEntry) / ddAnchorEntry * 100
		if math.Abs(gotPct-wantPct[i]) > 0.02 {
			t.Errorf("OKX tier %d: execution_price %.2f = %+.2f%%, want %+.2f%% —— "+
				"OKX 语义下 trigger==activation,修复必须是恒等变换",
				i+1, exec, gotPct, wantPct[i])
		}
	}
}

// 反向锁 2:峰值一旦真的越过激活价,成交价必须跟着峰值棘轮上移 —— 修复不能把
// "锚 = max(激活价, 已实现峰值)" 这条棘轮语义压死成固定值。
//
// 没有这条锁,把锚硬写成 plannedActivationPrice 也能过主锁,但线上会变成:仓位涨到
// +5% 了,面板仍报成交价 +0.52%,严重低估已锁定的利润。
func TestDDExecutionPrice_RatchetsWithRealizedPeakOnceActivated(t *testing.T) {
	// mark 推到 +3.0%:已越过 dd1 激活价(+1.33%),未到 dd2(+2.13%)? 3.0% > 2.13%,
	// 两档都越过了。峰值 = 当前 PnL(peakPnLCache 为空时 buildPositionProtectionRuntime
	// 用 currentPnLPct 兜底)。
	mark := ddAnchorEntry * 1.03
	orders := []tradertypes.OpenOrder{
		{OrderID: "okx-algo-1", Symbol: "BTCUSDT", Side: "SELL", PositionSide: "LONG",
			Type: "TRAILING_STOP_MARKET", Quantity: ddAnchorQty,
			StopPrice: mark, ActivationPrice: ddAnchorEntry * (1 + 1.3307/100),
			ActivationStatus: "activated", CallbackRate: 0.007984},
		{OrderID: "okx-algo-2", Symbol: "BTCUSDT", Side: "SELL", PositionSide: "LONG",
			Type: "TRAILING_STOP_MARKET", Quantity: ddAnchorQty * 0.3,
			StopPrice: mark, ActivationPrice: ddAnchorEntry * (1 + 2.1292/100),
			ActivationStatus: "activated", CallbackRate: 0.006387},
	}
	at := buildDDAnchorTrader(t, "okx", orders, mark)
	tiers := ddAnchorTiers(t, at, orders)

	// 锚 = 峰值价(= mark, +3%),成交价 = 峰值×(1-callback)。
	for i, tier := range tiers {
		exec, _ := tier["execution_price"].(float64)
		cb, _ := tier["callback_rate"].(float64)
		want := mark * (1 - cb)
		if math.Abs(exec-want) > 1.0 {
			t.Errorf("tier %d: execution_price %.2f, want %.2f (峰值 %.2f × (1-%.6f)) —— "+
				"峰值越过激活价后必须按峰值棘轮上移,不能钉在激活价",
				i+1, exec, want, mark, cb)
		}
		if exec <= ddAnchorEntry {
			t.Errorf("tier %d: 已越过激活价却算出 %.2f <= 入场价", i+1, exec)
		}
	}
}
