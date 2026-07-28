package trader

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"nofx/store"
	tradertypes "nofx/trader/types"
)

// ============================================================================
// DD 移动止盈可靠性大修 — 仿真 + 破坏性测试
//
// 覆盖 (对应 plan 三、可靠性工程):
//   A. 逻辑: nativeTrailingEffective 四态×多空 / computeExchangeLight 四色
//   B. 兜底: exchangeSideCoversDrawdownTier 四态矩阵 / allSatisfiedNativeTiersEffective
//   C. 破坏性: GetOpenOrders 报错 / 重复单 / activePx=0
//   D. 回归: Binance activated 识别不破
// ============================================================================

// ---- A1. nativeTrailingEffective: 四态 × 多空 -------------------------------

func TestNativeTrailingEffective_Matrix(t *testing.T) {
	cases := []struct {
		name       string
		side       string
		status     string
		activePx   float64
		markPrice  float64
		wantEffect bool
	}{
		// activated → 永远有效
		{"long_activated", "long", "activated", 106, 999, true},
		{"short_activated", "short", "activated", 94, 1, true},
		// resting 未越过 activePx → 有效(会自动激活)
		{"long_resting_not_passed", "long", "pending_activation", 106, 104, true},
		{"short_resting_not_passed", "short", "pending_activation", 94, 96, true},
		// phantom: resting 且已越过 activePx 未激活 → 无效
		{"long_phantom_passed", "long", "pending_activation", 106, 108, false},
		{"short_phantom_passed", "short", "pending_activation", 94, 92, false},
		// 无 activePx → 无效(有瑕疵)
		{"no_activepx", "long", "pending_activation", 0, 104, false},
		// markPrice<=0 无法判断 → 保守无效
		{"no_mark", "long", "pending_activation", 106, 0, false},
		// nil 保护
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			order := &nativeTrailingOrder{
				ActivationStatus: c.status,
				ActivationPrice:  c.activePx,
				StopPrice:        c.activePx,
			}
			got := nativeTrailingEffective("okx", order, c.side, c.markPrice, 0, 0)
			if got != c.wantEffect {
				t.Fatalf("nativeTrailingEffective(%s)=%v want %v", c.name, got, c.wantEffect)
			}
		})
	}
	if nativeTrailingEffective("okx", nil, "long", 100, 0, 0) {
		t.Fatal("nil order must not be effective")
	}
}

// StopPrice fallback: OKX resting 单用 StopPrice 携带 activePx (ActivationPrice 为 0 时)
func TestNativeTrailingEffective_StopPriceFallback(t *testing.T) {
	// short resting, activePx via StopPrice=94, mark=96 未越过 → 有效
	order := &nativeTrailingOrder{ActivationStatus: "pending_activation", ActivationPrice: 0, StopPrice: 94}
	if !nativeTrailingEffective("okx", order, "short", 96, 0, 0) {
		t.Fatal("short resting via StopPrice fallback should be effective when mark above activePx")
	}
	// short, mark=92 已越过 → phantom 无效
	if nativeTrailingEffective("okx", order, "short", 92, 0, 0) {
		t.Fatal("short phantom via StopPrice fallback should be ineffective when mark below activePx")
	}
}

// ---- A2. computeExchangeLight: 四色判定 -------------------------------------

func TestComputeExchangeLight_FourColors(t *testing.T) {
	// rule: min 6%, dd 30% giveback, close 100%
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 6, MaxDrawdownPct: 30, CloseRatioPct: 100}
	const entry = 100.0
	// long activation price = 100*1.06 = 106
	plannedActivation := 106.0

	cases := []struct {
		name        string
		side        string
		mark        float64
		curPnL      float64
		drawdown    float64
		matchedLive bool
		matchStatus string
		matchPx     float64
		mode        string
		want        string
	}{
		// blue: 触发 (profit≥min 且 giveback≥dd)
		{"blue_triggered", "long", 107, 8, 40, true, "activated", 106, "native_trailing_full", "blue"},
		// green: activated
		{"green_activated", "long", 110, 5, 0, true, "activated", 106, "native_trailing_full", "green"},
		// green: resting 未越 activePx (long profit<min so not reached; resting waiting)
		{"green_resting_waiting", "long", 104, 4, 0, true, "pending_activation", 106, "native_trailing_full", "green"},
		// yellow: phantom — profit≥min but resting & mark passed activePx not activated
		{"yellow_phantom", "long", 108, 6, 0, true, "pending_activation", 106, "native_trailing_full", "yellow"},
		// yellow: order present, no activePx
		{"yellow_no_activepx", "long", 104, 4, 0, true, "pending_activation", 0, "native_trailing_full", "yellow"},
		// red: tier reached (profit≥min) but no matching order
		{"red_missing", "long", 107, 7, 0, false, "", 0, "native_trailing_full", "red"},
		// managed mode → green (in-process monitor)
		{"green_managed", "long", 104, 4, 0, false, "", 0, "managed_drawdown", "green"},
		// managed exchange-failed → green (in-process monitor IS protecting; the
		// panel flashes it green via the exchange_order_failed flag, no longer yellow)
		{"green_managed_failed", "long", 104, 4, 0, false, "", 0, "managed_drawdown_exchange_failed", "green"},
		// red: place-at-open means EVERY tier carries a resting order from open; a
		// missing order is a genuine gap (self-heals next poll), even below profit floor.
		{"red_missing_below_floor", "long", 102, 2, 0, false, "", 0, "native_trailing_full", "red"},
		// short phantom
		{"short_yellow_phantom", "short", 92, 6, 0, true, "pending_activation", 94, "native_trailing_full", "yellow"},
		// short green resting (mark above short activePx 94)
		{"short_green_resting", "short", 96, 4, 0, true, "pending_activation", 94, "native_trailing_full", "green"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			px := plannedActivation
			if c.side == "short" {
				px = 94
			}
			got := computeExchangeLight(
				c.side, c.mark, c.curPnL, c.drawdown, rule,
				c.matchedLive, c.matchStatus, c.matchPx, px, true, c.mode)
			if got != c.want {
				t.Fatalf("computeExchangeLight(%s)=%q want %q", c.name, got, c.want)
			}
		})
	}
	_ = entry
}

// 非原生交易所: 一律绿(本地监控)
func TestComputeExchangeLight_NonNativeVenue(t *testing.T) {
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 6, MaxDrawdownPct: 30, CloseRatioPct: 100}
	got := computeExchangeLight("long", 104, 4, 0, rule, false, "", 0, 106, false, "code_managed")
	if got != "green" {
		t.Fatalf("non-native venue want green got %q", got)
	}
}

// ---- B. exchangeSideCoversDrawdownTier: 四态矩阵 (决定性修复) ----------------
//
// short 100 entry, min 6% → activePx = 100*0.94 = 94. close 100%.
// 位置数量必须匹配 (findEquivalentPartialTrailingOrder 用 cumulative ratio)。

func ddReliabilityShortSetup(orders []tradertypes.OpenOrder) *AutoTrader {
	fake := &fakeProtectionTrader{
		positions: []map[string]interface{}{{
			"symbol":      "WLDUSDT",
			"side":        "short",
			"entryPrice":  100.0,
			"markPrice":   92.0,
			"positionAmt": 10.0,
		}},
		openOrders: orders,
	}
	return &AutoTrader{exchange: "okx", trader: fake}
}

func ddShortRule() store.DrawdownTakeProfitRule {
	return normalizeDrawdownRule(store.DrawdownTakeProfitRule{MinProfitPct: 6, MaxDrawdownPct: 30, CloseRatioPct: 100})
}

func TestExchangeSideCoversDrawdownTier_Matrix(t *testing.T) {
	rule := ddShortRule()
	// callback the equivalent-order matcher expects
	cb := calculateDrawdownRuleCallbackRatio(100.0, "short", rule)
	activePx := calculateProfitBasedTrailingTriggerPrice(100.0, "short", rule.MinProfitPct) // 94

	mkOrder := func(status string, activation float64) tradertypes.OpenOrder {
		return tradertypes.OpenOrder{
			OrderID: "t1", Symbol: "WLDUSDT", PositionSide: "SHORT",
			Type: "TRAILING_STOP_MARKET", Quantity: 10.0,
			StopPrice: activation, ActivationPrice: activation,
			CallbackRate: cb, ActivationStatus: status, Status: "NEW",
		}
	}

	// activated → covered (suppress managed)
	t.Run("activated_covers", func(t *testing.T) {
		at := ddReliabilityShortSetup([]tradertypes.OpenOrder{mkOrder("activated", activePx)})
		if !at.exchangeSideCoversDrawdownTier("WLDUSDT", "short", rule, 100.0, 92.0) {
			t.Fatal("activated order must cover the tier")
		}
	})
	// resting, activePx not yet passed (mark 96 > 94) → covered
	t.Run("resting_not_passed_covers", func(t *testing.T) {
		at := ddReliabilityShortSetup([]tradertypes.OpenOrder{mkOrder("pending_activation", activePx)})
		if !at.exchangeSideCoversDrawdownTier("WLDUSDT", "short", rule, 100.0, 96.0) {
			t.Fatal("resting order not-yet-passed must cover the tier")
		}
	})
	// phantom: resting, mark 92 < activePx 94 (passed) not activated → NOT covered
	t.Run("phantom_not_covers", func(t *testing.T) {
		at := ddReliabilityShortSetup([]tradertypes.OpenOrder{mkOrder("pending_activation", activePx)})
		if at.exchangeSideCoversDrawdownTier("WLDUSDT", "short", rule, 100.0, 92.0) {
			t.Fatal("phantom order must NOT cover — managed must supplement")
		}
	})
	// missing → NOT covered
	t.Run("missing_not_covers", func(t *testing.T) {
		at := ddReliabilityShortSetup(nil)
		if at.exchangeSideCoversDrawdownTier("WLDUSDT", "short", rule, 100.0, 92.0) {
			t.Fatal("missing order must NOT cover")
		}
	})
}

// ---- C. 破坏性测试 ----------------------------------------------------------

// GetOpenOrders 报错 → 保守放行 managed (不抑制 → 不漏平)
func TestExchangeSideCovers_GetOpenOrdersError_AllowsManaged(t *testing.T) {
	rule := ddShortRule()
	fake := &fakeProtectionTrader{
		positions: []map[string]interface{}{{
			"symbol": "WLDUSDT", "side": "short", "entryPrice": 100.0, "markPrice": 92.0, "positionAmt": 10.0,
		}},
		getOpenOrdersErr: errors.New("network error"),
	}
	at := &AutoTrader{exchange: "okx", trader: fake}
	if at.exchangeSideCoversDrawdownTier("WLDUSDT", "short", rule, 100.0, 92.0) {
		t.Fatal("on GetOpenOrders error must NOT suppress managed (conservative)")
	}
}

// 重复 trailing 单 (同 tier 两条) → phantom 仍不覆盖 (取第一条匹配, 判无效)
func TestExchangeSideCovers_DuplicatePhantomOrders_StillNotCovered(t *testing.T) {
	rule := ddShortRule()
	cb := calculateDrawdownRuleCallbackRatio(100.0, "short", rule)
	activePx := calculateProfitBasedTrailingTriggerPrice(100.0, "short", rule.MinProfitPct)
	dup := func(id string) tradertypes.OpenOrder {
		return tradertypes.OpenOrder{
			OrderID: id, Symbol: "WLDUSDT", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET",
			Quantity: 10.0, StopPrice: activePx, ActivationPrice: activePx, CallbackRate: cb,
			ActivationStatus: "pending_activation", Status: "NEW",
		}
	}
	at := ddReliabilityShortSetup([]tradertypes.OpenOrder{dup("a"), dup("b")})
	// mark 92 passed activePx 94 → both phantom → not covered
	if at.exchangeSideCoversDrawdownTier("WLDUSDT", "short", rule, 100.0, 92.0) {
		t.Fatal("duplicate phantom orders must NOT cover")
	}
}

// activePx=0 的单 → 判无效 (无激活价) → 不覆盖
func TestExchangeSideCovers_ZeroActivePx_NotCovered(t *testing.T) {
	rule := ddShortRule()
	cb := calculateDrawdownRuleCallbackRatio(100.0, "short", rule)
	activePx := calculateProfitBasedTrailingTriggerPrice(100.0, "short", rule.MinProfitPct)
	// activation matches (StopPrice≈94 for equivalence) but ActivationPrice=0 and status pending
	order := tradertypes.OpenOrder{
		OrderID: "z", Symbol: "WLDUSDT", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET",
		Quantity: 10.0, StopPrice: activePx, ActivationPrice: 0, CallbackRate: cb,
		ActivationStatus: "pending_activation", Status: "NEW",
	}
	at := ddReliabilityShortSetup([]tradertypes.OpenOrder{order})
	// mark 96 not passed → StopPrice fallback makes it effective; so use mark 92 (passed) → phantom
	if at.exchangeSideCoversDrawdownTier("WLDUSDT", "short", rule, 100.0, 92.0) {
		t.Fatal("phantom (activePx via StopPrice passed) must NOT cover")
	}
}

// allSatisfiedNativeTiersEffective: 满足的 tier 有 phantom → false (放行 managed)
func TestAllSatisfiedNativeTiersEffective(t *testing.T) {
	rule := ddShortRule()
	cb := calculateDrawdownRuleCallbackRatio(100.0, "short", rule)
	activePx := calculateProfitBasedTrailingTriggerPrice(100.0, "short", rule.MinProfitPct)
	phantom := tradertypes.OpenOrder{
		OrderID: "p", Symbol: "WLDUSDT", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET",
		Quantity: 10.0, StopPrice: activePx, ActivationPrice: activePx, CallbackRate: cb,
		ActivationStatus: "pending_activation", Status: "NEW",
	}
	rules := []store.DrawdownTakeProfitRule{rule}

	// profit 8% ≥ min 6% (satisfied), mark 92 passed → phantom → NOT all effective
	at := ddReliabilityShortSetup([]tradertypes.OpenOrder{phantom})
	if at.allSatisfiedNativeTiersEffective("WLDUSDT", "short", 100.0, 92.0, 8.0, rules) {
		t.Fatal("satisfied tier with phantom must return false (allow managed)")
	}

	// profit 3% < min 6% (not satisfied) → no coverage required → true
	at2 := ddReliabilityShortSetup([]tradertypes.OpenOrder{phantom})
	if !at2.allSatisfiedNativeTiersEffective("WLDUSDT", "short", 100.0, 96.0, 3.0, rules) {
		t.Fatal("unsatisfied tier requires no coverage → true")
	}
}

// ---- D. 回归: Binance activated 识别不破 -------------------------------------

func TestNativeTrailingEffective_BinanceActivatedRegression(t *testing.T) {
	// Binance activated 单: CallbackRate 常为 0, StopPrice 为移动值 —— 只要 status=activated 即有效
	order := &nativeTrailingOrder{ActivationStatus: "activated", CallbackRate: 0, StopPrice: 0, ActivationPrice: 0}
	if !nativeTrailingEffective("binance", order, "long", 100, 0, 0) {
		t.Fatal("activated order must stay effective even with 0 callback/stop (Binance)")
	}
}

// ---- B4. 空转死循环复现→归零 (决定性仿真) -----------------------------------
//
// WLD short 幻影: 现价已越过 activePx、OKX 未激活。修复前 monitor+checkAndFix 会
// place→phantom→cancel→managed→re-arm 无限循环 (线上 26min: 208 set / 106 cancel /
// 212 phantom)。修复后: 幻影单不再撤、dedup 识别其存在→不重挂。断言多周期后:
//   - cancelTrailingCalls == 0 (不再撤单churn)
//   - trailingCalls 有界 (不再重挂churn)
func TestPhantomTrailing_NoChurnAcrossManyCycles(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "phantom-churn.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	entry := 100.0
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 6, MaxDrawdownPct: 30, CloseRatioPct: 100}
	nRule := normalizeDrawdownRule(rule)
	activePx := calculateProfitBasedTrailingTriggerPrice(entry, "short", nRule.MinProfitPct) // 94
	cb := calculateDrawdownRuleCallbackRatio(entry, "short", nRule)

	// Pre-place a matching phantom resting order (mark 92 already past activePx 94).
	phantom := tradertypes.OpenOrder{
		OrderID: "phantom-1", Symbol: "WLDUSDT", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET",
		Quantity: 10.0, StopPrice: activePx, ActivationPrice: activePx, CallbackRate: cb,
		ActivationStatus: "pending_activation", Status: "NEW",
	}
	fake := &fakeProtectionTrader{
		positions: []map[string]interface{}{{
			"symbol": "WLDUSDT", "side": "short", "entryPrice": entry, "markPrice": 92.0, "positionAmt": 10.0,
		}},
		openOrders: []tradertypes.OpenOrder{phantom},
	}
	at := &AutoTrader{
		id:         "trader-churn",
		exchangeID: "exch-churn",
		store:      st,
		exchange:   "okx",
		trader:     fake,
		config: AutoTraderConfig{StrategyConfig: &store.StrategyConfig{
			Protection: store.ProtectionConfig{
				DrawdownTakeProfit: store.DrawdownTakeProfitConfig{Enabled: true, Rules: []store.DrawdownTakeProfitRule{rule}},
			},
		}},
		protectionState: map[string]string{"WLDUSDT_short": "native_trailing_armed"},
		drawdownState:   make(map[string]string),
		peakPnLCache:    map[string]float64{"WLDUSDT_short": 8.0},
	}

	for i := 0; i < 20; i++ {
		at.checkPositionDrawdown()
	}

	if fake.cancelTrailingCalls != 0 {
		t.Fatalf("CHURN: expected 0 trailing cancellations across 20 cycles, got %d", fake.cancelTrailingCalls)
	}
	// The phantom already matches the tier, so the dedup gate must prevent re-placing.
	// Allow at most a tiny bound to absorb one-time state init, but 0 is the target.
	if fake.trailingCalls > 1 {
		t.Fatalf("CHURN: expected ≤1 trailing placement across 20 cycles (phantom left alone), got %d", fake.trailingCalls)
	}
}

// ---- B6. 正常 activated: 不被误撤/重挂, managed 让位 (不双平) ------------------
func TestActivatedTrailing_NotCancelled_ManagedYields(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "activated-yield.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	entry := 100.0
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 6, MaxDrawdownPct: 30, CloseRatioPct: 100}
	nRule := normalizeDrawdownRule(rule)
	cb := calculateDrawdownRuleCallbackRatio(entry, "short", nRule)
	activated := tradertypes.OpenOrder{
		OrderID: "act-1", Symbol: "WLDUSDT", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET",
		Quantity: 10.0, StopPrice: 90.0, ActivationPrice: 94.0, CallbackRate: cb,
		ActivationStatus: "activated", Status: "NEW",
	}
	fake := &fakeProtectionTrader{
		positions: []map[string]interface{}{{
			"symbol": "WLDUSDT", "side": "short", "entryPrice": entry, "markPrice": 90.0, "positionAmt": 10.0,
		}},
		openOrders: []tradertypes.OpenOrder{activated},
	}
	at := &AutoTrader{
		id: "trader-act", exchangeID: "exch-act", store: st, exchange: "okx", trader: fake,
		config: AutoTraderConfig{StrategyConfig: &store.StrategyConfig{
			Protection: store.ProtectionConfig{
				DrawdownTakeProfit: store.DrawdownTakeProfitConfig{Enabled: true, Rules: []store.DrawdownTakeProfitRule{rule}},
			},
		}},
		protectionState: map[string]string{"WLDUSDT_short": "native_trailing_armed"},
		drawdownState:   make(map[string]string),
		peakPnLCache:    map[string]float64{"WLDUSDT_short": 10.0},
	}
	for i := 0; i < 10; i++ {
		at.checkPositionDrawdown()
	}
	if fake.cancelTrailingCalls != 0 {
		t.Fatalf("activated order must not be cancelled, got %d cancels", fake.cancelTrailingCalls)
	}
	if fake.closeShortCalls != 0 {
		t.Fatalf("managed must YIELD to effective activated exchange order (no double close), got %d closes", fake.closeShortCalls)
	}
}

// ---- B5. 幻影存在时 managed 仍在 giveback 平仓 (BTC 场景决定性修复) -----------
//
// 修复前: 幻影单被 exchangeSideCoversDrawdownTier 当"已覆盖"→抑制 managed→回撤不平仓
// (BTC 从 +4.19ATR 回退到 +0.69)。修复后: 幻影无效→不抑制→managed 在 giveback 平仓。
func TestPhantomPresent_ManagedStillClosesOnGiveback(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "phantom-giveback.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	entry := 100.0
	// min 0.7%, giveback 1.5%, close 100%. short.
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 0.7, MaxDrawdownPct: 1.5, CloseRatioPct: 100}
	nRule := normalizeDrawdownRule(rule)
	activePx := calculateProfitBasedTrailingTriggerPrice(entry, "short", nRule.MinProfitPct) // 99.3
	cb := calculateDrawdownRuleCallbackRatio(entry, "short", nRule)

	// mark 99 → short profit +1%; peak 3% (cache) → drawdownFromPeak=(3-1)/103≈1.94%≥1.5% → trigger.
	// mark 99 ≤ activePx 99.3 → phantom (short passed activation, not activated).
	phantom := tradertypes.OpenOrder{
		OrderID: "phantom-gb", Symbol: "BTCUSDT", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET",
		Quantity: 10.0, StopPrice: activePx, ActivationPrice: activePx, CallbackRate: cb,
		ActivationStatus: "pending_activation", Status: "NEW",
	}
	fake := &fakeProtectionTrader{
		positions: []map[string]interface{}{{
			"symbol": "BTCUSDT", "side": "short", "entryPrice": entry, "markPrice": 99.0, "positionAmt": 10.0,
		}},
		openOrders: []tradertypes.OpenOrder{phantom},
	}
	at := &AutoTrader{
		id: "trader-gb", exchangeID: "exch-gb", store: st, exchange: "okx", trader: fake,
		config: AutoTraderConfig{StrategyConfig: &store.StrategyConfig{
			Protection: store.ProtectionConfig{
				DrawdownTakeProfit: store.DrawdownTakeProfitConfig{Enabled: true, Rules: []store.DrawdownTakeProfitRule{rule}},
			},
		}},
		protectionState: map[string]string{"BTCUSDT_short": "native_trailing_armed"},
		drawdownState:   make(map[string]string),
		peakPnLCache:    map[string]float64{"BTCUSDT_short": 3.0},
	}

	at.checkPositionDrawdown()

	if fake.closeShortCalls != 1 {
		t.Fatalf("DECISIVE: managed must close ONCE on giveback despite phantom present, got %d closes", fake.closeShortCalls)
	}
	// And it must not have cancelled the phantom (leave it alone; managed did the work).
	if fake.cancelTrailingCalls != 0 {
		t.Fatalf("expected phantom left alone (0 cancels), got %d", fake.cancelTrailingCalls)
	}
}

// ==== E. 无激活价危险单 (no-activation) + 断路器 (re-arm breaker) ==============
//
// 用户模型: 无激活价的移动止盈止损单会被 OKX 立即激活, 从现价跟踪, 任意小回撤即误平
// —— 必须撤单重挂 (幻影单则相反, 已越过activePx不会误平, 放任由 managed 兜底)。
// 断路器: 连续 reArmBreakerLimit(3) 次重挂后仍无有效覆盖 → 停挂 + managed 接管。
// 危险规则是 OKX 专属 (Binance activated 单永远带 activePx=triggerPrice>0)。

// E1. 判别器: activated & activePx<=0 & OKX → 危险; 其余 → 否。
func TestDangerousNoActivation_Detected(t *testing.T) {
	cases := []struct {
		name     string
		exchange string
		status   string
		activePx float64
		want     bool
	}{
		{"okx_activated_zero_activepx", "okx", "activated", 0, true},
		{"okx_activated_negative_activepx", "okx", "activated", -1, true},
		{"okx_activated_positive_activepx", "okx", "activated", 94, false},    // 真激活, 安全
		{"okx_resting_zero_activepx", "okx", "pending_activation", 0, false},  // 未激活, 非此类危险
		{"binance_activated_zero_activepx", "binance", "activated", 0, false}, // 交易所门控: Binance 不适用
		{"nil_order", "okx", "activated", 0, false},                           // nil 保护 (下方单独判)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var order *nativeTrailingOrder
			if c.name != "nil_order" {
				order = &nativeTrailingOrder{ActivationStatus: c.status, ActivationPrice: c.activePx}
			}
			// 0,0 = no profit floor gate → pure structural danger check
			got := trailingOrderIsDangerousNoActivation(c.exchange, order, 0, 0)
			if got != c.want {
				t.Fatalf("trailingOrderIsDangerousNoActivation(%s)=%v want %v", c.name, got, c.want)
			}
		})
	}
	if trailingOrderIsDangerousNoActivation("okx", nil, 0, 0) {
		t.Fatal("nil order must never be dangerous")
	}
}

// E2. reconcileDangerousTrailingOrders: OKX 无激活价危险单 → 被撤 (返回撤单数)。
func TestDangerousNoActivation_CancelledAndReplaced(t *testing.T) {
	danger := tradertypes.OpenOrder{
		OrderID: "danger-1", Symbol: "WLDUSDT", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET",
		Quantity: 10.0, StopPrice: 91.0, ActivationPrice: 0, CallbackRate: 0.03,
		ActivationStatus: "activated", Status: "NEW",
	}
	at := ddReliabilityShortSetup([]tradertypes.OpenOrder{danger})
	// currentPnL 0 with no floor → structural danger stands (near-entry immediate-active)
	n := at.reconcileDangerousTrailingOrders("WLDUSDT", "short", 0, 0, 0)
	if n != 1 {
		t.Fatalf("expected 1 dangerous order cancelled, got %d", n)
	}
	fake := at.trader.(*fakeProtectionTrader)
	if fake.cancelTrailingCalls != 1 {
		t.Fatalf("expected exactly 1 cancel call, got %d", fake.cancelTrailingCalls)
	}
	// 危险单已从簿中移除 → arm 循环随后会以正确 activePx 重挂 (此处只验证撤单侧)。
	for _, o := range fake.openOrders {
		if o.OrderID == "danger-1" {
			t.Fatal("dangerous order must be removed from the book after cancel")
		}
	}
}

// E3. reconcile 只撤危险单, 不动幻影 (幻影 activePx>0, 已越过, 不会误平)。
func TestPhantom_NotTreatedAsDangerous_NoCancel(t *testing.T) {
	// 幻影: pending_activation, activePx=94>0, mark 92 已越过 → 不是无激活价危险单。
	phantom := tradertypes.OpenOrder{
		OrderID: "phantom-x", Symbol: "WLDUSDT", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET",
		Quantity: 10.0, StopPrice: 94.0, ActivationPrice: 94.0, CallbackRate: 0.03,
		ActivationStatus: "pending_activation", Status: "NEW",
	}
	at := ddReliabilityShortSetup([]tradertypes.OpenOrder{phantom})
	n := at.reconcileDangerousTrailingOrders("WLDUSDT", "short", 0, 0, 0)
	if n != 0 {
		t.Fatalf("phantom must NOT be treated as dangerous, got %d cancelled", n)
	}
	fake := at.trader.(*fakeProtectionTrader)
	if fake.cancelTrailingCalls != 0 {
		t.Fatalf("phantom must be left alone (0 cancels), got %d", fake.cancelTrailingCalls)
	}
}

// E4. reconcile: 真激活单 (activePx>0) 不被撤。
func TestGenuinelyActivated_activePxPositive_NotCancelled(t *testing.T) {
	activated := tradertypes.OpenOrder{
		OrderID: "act-ok", Symbol: "WLDUSDT", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET",
		Quantity: 10.0, StopPrice: 90.0, ActivationPrice: 94.0, CallbackRate: 0.03,
		ActivationStatus: "activated", Status: "NEW",
	}
	at := ddReliabilityShortSetup([]tradertypes.OpenOrder{activated})
	if n := at.reconcileDangerousTrailingOrders("WLDUSDT", "short", 0, 0, 0); n != 0 {
		t.Fatalf("genuinely activated (activePx>0) must NOT be cancelled, got %d", n)
	}
}

// E5. computeExchangeLight: activated & activePx<=0 → yellow (不再误报 green)。
func TestComputeExchangeLight_ActivatedNoActivePx_Yellow(t *testing.T) {
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 6, MaxDrawdownPct: 30, CloseRatioPct: 100}
	// long, activated, matchPx=0 → 危险无激活价 → yellow
	got := computeExchangeLight("long", 104, 4, 0, rule, true, "activated", 0, 106, true, "native_trailing_full")
	if got != "yellow" {
		t.Fatalf("activated no-activePx must be yellow (dangerous), got %q", got)
	}
	// 对照: activated & activePx>0 → green
	if g := computeExchangeLight("long", 104, 4, 0, rule, true, "activated", 106, 106, true, "native_trailing_full"); g != "green" {
		t.Fatalf("activated WITH activePx must stay green, got %q", g)
	}
}

// E6. 断路器: 连续 3 次未有效覆盖 → 第 3 次跳闸 → 状态升级 exchange_failed → managed 陪跑执行。
//
// 2026-07-27 语义变更(co-run,不接管): 跳闸后本函数必须报告 NOT covered。
// 原来报 covered 的依据是"applyExchangeFailedLocalMonitor 已接管该档",但对 close>=100 的
// 档位那个 helper 什么都不挂、也不注册执行者,只写状态;同时 arm 循环因跳闸不再挂单 →
// 交易所无单 + managed 被跳过 = 零保护,而面板显示 armed。所以跳闸只代表"交易所侧不再维护",
// 必须让 managed 成为真正的执行者。
func TestReArmBreaker_TripsAfter3Fails(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "breaker-trip.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	entry := 100.0
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 6, MaxDrawdownPct: 30, CloseRatioPct: 100}
	rules := []store.DrawdownTakeProfitRule{rule}
	// 无任何交易所单 → 该 tier 永远无覆盖 → 每次 bump。
	fake := &fakeProtectionTrader{
		positions: []map[string]interface{}{{
			"symbol": "WLDUSDT", "side": "short", "entryPrice": entry, "markPrice": 92.0, "positionAmt": 10.0,
		}},
	}
	at := &AutoTrader{
		id: "trader-brk", exchangeID: "exch-brk", store: st, exchange: "okx", trader: fake,
		protectionState: map[string]string{"WLDUSDT_short": "managed_drawdown_armed"},
		reArmFailCache:  make(map[string]int),
	}
	// currentPnL 8% ≥ min 6% → tier 满足, 需覆盖。
	// 第 1、2 次: 未覆盖 → bump, allCovered=false。
	for i := 1; i <= 2; i++ {
		if at.accountReArmBreaker("WLDUSDT", "short", entry, 92.0, 8.0, rules) {
			t.Fatalf("call %d: breaker not yet tripped, must report NOT covered", i)
		}
	}
	// 第 3 次: fails 达到 limit → 跳闸 → applyExchangeFailedLocalMonitor,且必须报 NOT covered。
	if at.accountReArmBreaker("WLDUSDT", "short", entry, 92.0, 8.0, rules) {
		t.Fatal("3rd call: breaker TRIPPED — must report NOT covered so the managed monitor executes; " +
			"reporting covered leaves a close=100% tier with no exchange order AND no executor")
	}
	// 第 4 次(跳闸后再轮询): 仍报 NOT covered,且不得重复调用 applyExchangeFailedLocalMonitor
	// (对局部档它会真的下单 → 正是断路器要消除的 churn)。计数不再增长即为证据。
	failsAfterTrip := at.getReArmFail(reArmFailKey("WLDUSDT", "short", normalizeDrawdownRule(rule), entry))
	if at.accountReArmBreaker("WLDUSDT", "short", entry, 92.0, 8.0, rules) {
		t.Fatal("4th call: a tripped tier must keep reporting NOT covered")
	}
	if got := at.getReArmFail(reArmFailKey("WLDUSDT", "short", normalizeDrawdownRule(rule), entry)); got != failsAfterTrip {
		t.Fatalf("tripped tier must short-circuit before bump/re-arm: fails %d → %d (re-arming every poll is churn)", failsAfterTrip, got)
	}
	// 跳闸后状态升级, execution mode 反映 exchange_failed。
	if mode := at.getDrawdownExecutionMode("WLDUSDT", "short"); mode != "managed_drawdown_exchange_failed" {
		t.Fatalf("after trip execution mode must be managed_drawdown_exchange_failed, got %q", mode)
	}
	// reArmBreakerTripped 谓词一致。
	if !at.reArmBreakerTripped("WLDUSDT", "short", rule, entry) {
		t.Fatal("reArmBreakerTripped must report true after limit reached")
	}
}

// E7. 断路器: 新开仓 → clearReArmFailForPosition 归零计数 (计数不跨仓)。
func TestReArmBreaker_ResetsOnNewPosition(t *testing.T) {
	at := &AutoTrader{exchange: "okx", reArmFailCache: make(map[string]int)}
	rule := normalizeDrawdownRule(store.DrawdownTakeProfitRule{MinProfitPct: 6, MaxDrawdownPct: 30, CloseRatioPct: 100})
	key := reArmFailKey("WLDUSDT", "short", rule, 100.0)
	at.bumpReArmFail(key)
	at.bumpReArmFail(key)
	if at.getReArmFail(key) != 2 {
		t.Fatalf("expected fail count 2, got %d", at.getReArmFail(key))
	}
	at.clearReArmFailForPosition("WLDUSDT", "short")
	if at.getReArmFail(key) != 0 {
		t.Fatalf("new position must zero the breaker, got %d", at.getReArmFail(key))
	}
}

// E8. 断路器: 一次有效覆盖 → resetReArmFail 清零 (成功回滚失败累计)。
func TestReArmBreaker_SuccessResetsCounter(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "breaker-reset.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	entry := 100.0
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 6, MaxDrawdownPct: 30, CloseRatioPct: 100}
	nRule := normalizeDrawdownRule(rule)
	rules := []store.DrawdownTakeProfitRule{rule}
	cb := calculateDrawdownRuleCallbackRatio(entry, "short", nRule)
	activePx := calculateProfitBasedTrailingTriggerPrice(entry, "short", nRule.MinProfitPct) // 94
	// 有效覆盖单: activated, activePx>0 → 覆盖该 tier。
	// StopPrice≈activePx(94) 以匹配 findEquivalentPartialTrailingOrder (用 StopPrice 判等价)。
	covering := tradertypes.OpenOrder{
		OrderID: "cover-1", Symbol: "WLDUSDT", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET",
		Quantity: 10.0, StopPrice: activePx, ActivationPrice: activePx, CallbackRate: cb,
		ActivationStatus: "activated", Status: "NEW",
	}
	fake := &fakeProtectionTrader{
		positions: []map[string]interface{}{{
			"symbol": "WLDUSDT", "side": "short", "entryPrice": entry, "markPrice": 96.0, "positionAmt": 10.0,
		}},
		openOrders: []tradertypes.OpenOrder{covering},
	}
	at := &AutoTrader{
		id: "trader-rst", exchangeID: "exch-rst", store: st, exchange: "okx", trader: fake,
		protectionState: map[string]string{"WLDUSDT_short": "native_trailing_armed"},
		reArmFailCache:  make(map[string]int),
	}
	// 预置 2 次失败累计。
	key := reArmFailKey("WLDUSDT", "short", nRule, entry)
	at.bumpReArmFail(key)
	at.bumpReArmFail(key)
	// 有效覆盖 (activated, activePx>0) → 覆盖判定 true → reset 计数。
	if !at.accountReArmBreaker("WLDUSDT", "short", entry, 96.0, 8.0, rules) {
		t.Fatal("effective coverage must report covered")
	}
	if at.getReArmFail(key) != 0 {
		t.Fatalf("success must reset breaker counter to 0, got %d", at.getReArmFail(key))
	}
}

// E9. 断路器: GetOpenOrders 报错 → 不跳闸也不谎报覆盖 (读失败保守放行 managed)。
func TestReArmBreaker_GetOpenOrdersError_NoTripNoClaim(t *testing.T) {
	entry := 100.0
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 6, MaxDrawdownPct: 30, CloseRatioPct: 100}
	rules := []store.DrawdownTakeProfitRule{rule}
	fake := &fakeProtectionTrader{
		positions: []map[string]interface{}{{
			"symbol": "WLDUSDT", "side": "short", "entryPrice": entry, "markPrice": 92.0, "positionAmt": 10.0,
		}},
		getOpenOrdersErr: errors.New("network error"),
	}
	at := &AutoTrader{exchange: "okx", trader: fake, reArmFailCache: make(map[string]int)}
	if at.accountReArmBreaker("WLDUSDT", "short", entry, 92.0, 8.0, rules) {
		t.Fatal("on GetOpenOrders error must NOT claim coverage (managed supplements)")
	}
	key := reArmFailKey("WLDUSDT", "short", normalizeDrawdownRule(rule), entry)
	if at.getReArmFail(key) != 0 {
		t.Fatalf("read failure must NOT bump the breaker, got %d", at.getReArmFail(key))
	}
}

// E10. 集成 (v-next 决定性): profit ≥ floor 的"无激活价"单是"故意的立即跟踪" —
// checkPositionDrawdown 既不撤它 (非危险), 也不 managed 双平 (它是有效覆盖, managed 让位)。
//
// 关键不变式: 危险判别的 floor = 最低档 MinProfitPct。profit ≥ floor 时任一档已达标,
// 无激活价单即"锁在利润里的立即跟踪" (峰值锚在盈利区, giveback 由交易所单自己平),
// 因此 reconcile 不撤、managed 让位, 无 churn 无双平。profit < floor 才是近入场误平
// 危险单 (但那时无档达标, managed 本就不会平 → 危险撤单与 managed 平仓天然互斥)。
func TestNoActivePxAtProfitFloor_KeptAndManagedYields(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "immediate-trail-kept.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	entry := 100.0
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 0.7, MaxDrawdownPct: 1.5, CloseRatioPct: 100}
	// mark 99 → short profit +1% ≥ floor 0.7% → 故意立即跟踪 (activated, activePx=0)。
	immediate := tradertypes.OpenOrder{
		OrderID: "imm-gb", Symbol: "BTCUSDT", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET",
		Quantity: 10.0, StopPrice: 99.0, ActivationPrice: 0, CallbackRate: 0.015,
		ActivationStatus: "activated", Status: "NEW",
	}
	fake := &fakeProtectionTrader{
		positions: []map[string]interface{}{{
			"symbol": "BTCUSDT", "side": "short", "entryPrice": entry, "markPrice": 99.0, "positionAmt": 10.0,
		}},
		openOrders: []tradertypes.OpenOrder{immediate},
	}
	at := &AutoTrader{
		id: "trader-imm", exchangeID: "exch-imm", store: st, exchange: "okx", trader: fake,
		config: AutoTraderConfig{StrategyConfig: &store.StrategyConfig{
			Protection: store.ProtectionConfig{
				DrawdownTakeProfit: store.DrawdownTakeProfitConfig{Enabled: true, Rules: []store.DrawdownTakeProfitRule{rule}},
			},
		}},
		protectionState: map[string]string{"BTCUSDT_short": "native_trailing_armed"},
		drawdownState:   make(map[string]string),
		peakPnLCache:    map[string]float64{"BTCUSDT_short": 3.0},
		reArmFailCache:  make(map[string]int),
	}
	at.checkPositionDrawdown()
	// 故意立即跟踪单 (profit ≥ floor) 不是危险单 → 不撤。
	if fake.cancelTrailingCalls != 0 {
		t.Fatalf("deliberate immediate-trail (profit≥floor) must NOT be cancelled, got %d", fake.cancelTrailingCalls)
	}
	// 它是有效覆盖 → managed 让位, 不双平 (交易所单自己在 giveback 平)。
	if fake.closeShortCalls != 0 {
		t.Fatalf("DECISIVE: managed must YIELD to effective immediate-trail (no double close), got %d", fake.closeShortCalls)
	}
}

// E11. 集成: 断路器跳闸后 arm 循环不再重挂 (reArmBreakerTripped → skip applyNativeTrailingDrawdown)。
func TestReArmBreaker_TrippedStopsRePlacing(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "breaker-stop.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	entry := 100.0
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 6, MaxDrawdownPct: 30, CloseRatioPct: 100}
	nRule := normalizeDrawdownRule(rule)
	fake := &fakeProtectionTrader{
		positions: []map[string]interface{}{{
			"symbol": "WLDUSDT", "side": "short", "entryPrice": entry, "markPrice": 92.0, "positionAmt": 10.0,
		}},
	}
	at := &AutoTrader{
		id: "trader-stop", exchangeID: "exch-stop", store: st, exchange: "okx", trader: fake,
		config: AutoTraderConfig{StrategyConfig: &store.StrategyConfig{
			Protection: store.ProtectionConfig{
				DrawdownTakeProfit: store.DrawdownTakeProfitConfig{Enabled: true, Rules: []store.DrawdownTakeProfitRule{rule}},
			},
		}},
		protectionState: map[string]string{"WLDUSDT_short": "native_trailing_armed"},
		drawdownState:   make(map[string]string),
		peakPnLCache:    map[string]float64{"WLDUSDT_short": 8.0},
		reArmFailCache:  make(map[string]int),
	}
	// 预置跳闸: 直接把该 tier 的失败计数打到 limit。
	key := reArmFailKey("WLDUSDT", "short", nRule, entry)
	for i := 0; i < reArmBreakerLimit; i++ {
		at.bumpReArmFail(key)
	}
	if !at.reArmBreakerTripped("WLDUSDT", "short", rule, entry) {
		t.Fatal("precondition: breaker must be tripped")
	}
	before := fake.trailingCalls
	for i := 0; i < 5; i++ {
		at.checkPositionDrawdown()
	}
	// 跳闸后 arm 循环必须 skip → 无新的交易所挂单尝试。
	if fake.trailingCalls != before {
		t.Fatalf("tripped breaker must STOP re-placing on exchange, got %d new placements", fake.trailingCalls-before)
	}
}

// ==== F. 利润感知 (profit-aware) 立即挂 / 危险判别 — v-next 统一保护 ============
//
// 用户模型 (2026-07-26): 越过 activePx 的丢失档, 允许挂"离他最近的无激活价/触发价"
// 的移动止盈止损 —— 只要利润 ≥ 该档 MinProfitPct, 就是"故意的、锁在利润里的立即跟踪",
// 峰值锚在盈利区, 最坏 = 该档设计的保留利润, 绝不亏新钱。低于档位 floor 才是近入场
// 立即激活的误平危险单, 必须撤。判别与有效性都要 profit-aware, 否则会误撤/误报。

// F1. lowestTierMinProfit: 取最小正 MinProfitPct (多档取最低门槛作 safe floor)。
func TestLowestTierMinProfit(t *testing.T) {
	rules := []store.DrawdownTakeProfitRule{
		{MinProfitPct: 6, MaxDrawdownPct: 30, CloseRatioPct: 40},
		{MinProfitPct: 3, MaxDrawdownPct: 20, CloseRatioPct: 35},
		{MinProfitPct: 12, MaxDrawdownPct: 40, CloseRatioPct: 100},
	}
	if got := lowestTierMinProfit(rules); got != 3 {
		t.Fatalf("lowestTierMinProfit=%.1f want 3 (smallest positive)", got)
	}
	// 非法档 (min<=0 / dd<=0 / close<=0) 跳过。
	bad := []store.DrawdownTakeProfitRule{
		{MinProfitPct: 0, MaxDrawdownPct: 30, CloseRatioPct: 40},
		{MinProfitPct: 5, MaxDrawdownPct: 0, CloseRatioPct: 40},
	}
	if got := lowestTierMinProfit(bad); got != 0 {
		t.Fatalf("lowestTierMinProfit(all-invalid)=%.1f want 0", got)
	}
}

// F2. 危险判别 profit-aware: OKX activated & activePx<=0, 利润 ≥ floor → 安全 (故意立即跟踪);
// 利润 < floor → 危险 (近入场误平)。
func TestDangerousNoActivation_ProfitAware(t *testing.T) {
	order := &nativeTrailingOrder{ActivationStatus: "activated", ActivationPrice: 0}
	// floor 6%, 利润 8% ≥ floor → 安全, 不撤。
	if trailingOrderIsDangerousNoActivation("okx", order, 8, 6) {
		t.Fatal("no-activePx at/above profit floor is a DELIBERATE immediate-trail — must NOT be dangerous")
	}
	// floor 6%, 利润 2% < floor → 危险, 要撤。
	if !trailingOrderIsDangerousNoActivation("okx", order, 2, 6) {
		t.Fatal("no-activePx BELOW profit floor mis-closes on retrace — must be dangerous")
	}
	// floor=0 (无档) → 退化为纯结构判别, 仍危险 (保守)。
	if !trailingOrderIsDangerousNoActivation("okx", order, 8, 0) {
		t.Fatal("no floor configured → structural danger stands")
	}
}

// F3. 有效性 profit-aware: OKX activated & activePx<=0, 利润 ≥ floor → 有效 (锁利润的立即跟踪会在
// giveback 平仓); 利润 < floor → 无效 (交给 managed + reconciler)。
func TestNativeTrailingEffective_ProfitAware_NoActivePx(t *testing.T) {
	order := &nativeTrailingOrder{ActivationStatus: "activated", ActivationPrice: 0}
	// 利润 8% ≥ floor 6% → 有效。
	if !nativeTrailingEffective("okx", order, "long", 108, 8, 6) {
		t.Fatal("activated no-activePx at/above floor is a profit-locked immediate-trail — must be EFFECTIVE")
	}
	// 利润 2% < floor 6% → 无效。
	if nativeTrailingEffective("okx", order, "long", 102, 2, 6) {
		t.Fatal("activated no-activePx BELOW floor mis-closes — must NOT be effective")
	}
	// Binance: activated 永远有效, 与 activePx/floor 无关 (门控只对 OKX)。
	if !nativeTrailingEffective("binance", order, "long", 102, 2, 6) {
		t.Fatal("Binance activated must stay effective regardless of activePx/profit")
	}
}

// F4. reconcile profit-aware: 同一"无激活价"单, 利润 ≥ floor 时不撤 (视为故意立即跟踪);
// 利润 < floor 时才撤。决定性防止误撤刚放行的立即挂单 (否则又 churn)。
func TestReconcile_ProfitAware_KeepsDeliberateImmediateTrail(t *testing.T) {
	mk := func() tradertypes.OpenOrder {
		return tradertypes.OpenOrder{
			OrderID: "imm-1", Symbol: "WLDUSDT", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET",
			Quantity: 10.0, StopPrice: 91.0, ActivationPrice: 0, CallbackRate: 0.03,
			ActivationStatus: "activated", Status: "NEW",
		}
	}
	// 利润 8% ≥ floor 6% → 故意立即跟踪, 不撤。
	atKeep := ddReliabilityShortSetup([]tradertypes.OpenOrder{mk()})
	if n := atKeep.reconcileDangerousTrailingOrders("WLDUSDT", "short", 0, 8, 6); n != 0 {
		t.Fatalf("deliberate profit-locked immediate-trail (profit≥floor) must NOT be cancelled, got %d", n)
	}
	// 利润 2% < floor 6% → 近入场误平危险单, 要撤。
	atCancel := ddReliabilityShortSetup([]tradertypes.OpenOrder{mk()})
	if n := atCancel.reconcileDangerousTrailingOrders("WLDUSDT", "short", 0, 2, 6); n != 1 {
		t.Fatalf("near-entry no-activePx (profit<floor) must be cancelled, got %d", n)
	}
}

// F5. computeExchangeLight profit-aware: activated & activePx<=0, 利润 ≥ min → green
// (故意立即跟踪), 利润 < min → yellow (危险近入场单)。呼应 F2/F3 的面板呈现。
func TestComputeExchangeLight_ActivatedNoActivePx_ProfitAware(t *testing.T) {
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 6, MaxDrawdownPct: 30, CloseRatioPct: 100}
	// 利润 8% ≥ min 6%, activated, matchPx=0 → 故意立即跟踪 → green。
	if g := computeExchangeLight("long", 108, 8, 0, rule, true, "activated", 0, 106, true, "native_trailing_full"); g != "green" {
		t.Fatalf("activated no-activePx at/above profit floor must be green, got %q", g)
	}
	// 利润 2% < min 6% → 危险近入场单 → yellow。
	if g := computeExchangeLight("long", 102, 2, 0, rule, true, "activated", 0, 106, true, "native_trailing_full"); g != "yellow" {
		t.Fatalf("activated no-activePx below profit floor must be yellow, got %q", g)
	}
}

// F6. 越过档立即挂用 activePx=0(无激活价)而非近价锚定 — 复现并锁死 WLD 生产 churn。
//
// 生产 bug(2026-07-26 v1.16 首版):越过 activePx 的丢失档用 activePx=mark×0.9999
// 立即锚定。OKX tick 取整把 0.333867 进位到 0.334(> mark 0.3339,short 错误一侧)→
// 永不激活 pending_activation → 判幻影 → 每 poll 重挂(~5次/分)。修法:activePx=0,
// OKX 立即从现价激活,取整无从谈起。断言:幻影档被重挂一次为 activePx=0,之后 activated
// 单被 matcher(markPassedPlanned)+shouldReplace(activated 不换)认下 → 不再 churn。
func TestPassedTier_ReplacedWithZeroActivePx_NoChurn(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "zero-activepx-nochurn.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	entry := 100.0
	// short, minProfit 4.6% → planned activePx = 100*(1-0.046)=95.4;close 100%。
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 4.6, MaxDrawdownPct: 2.8, CloseRatioPct: 100}
	nRule := normalizeDrawdownRule(rule)
	plannedActivePx := calculateProfitBasedTrailingTriggerPrice(entry, "short", nRule.MinProfitPct)
	cb := calculateDrawdownRuleCallbackRatio(entry, "short", nRule)
	// mark 92 → short profit +8% ≥ floor;mark 92 ≤ planned 95.4 → 已越过 → 幻影(未激活)。
	phantom := tradertypes.OpenOrder{
		OrderID: "phantom-wld", Symbol: "WLDUSDT", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET",
		Quantity: 10.0, StopPrice: plannedActivePx, ActivationPrice: plannedActivePx, CallbackRate: cb,
		ActivationStatus: "pending_activation", Status: "NEW",
	}
	fake := &fakeProtectionTrader{
		positions: []map[string]interface{}{{
			"symbol": "WLDUSDT", "side": "short", "entryPrice": entry, "markPrice": 92.0, "positionAmt": 10.0,
		}},
		openOrders: []tradertypes.OpenOrder{phantom},
	}
	at := &AutoTrader{
		id: "trader-zap", exchangeID: "exch-zap", store: st, exchange: "okx", trader: fake,
		config: AutoTraderConfig{StrategyConfig: &store.StrategyConfig{
			Protection: store.ProtectionConfig{
				DrawdownTakeProfit: store.DrawdownTakeProfitConfig{Enabled: true, Rules: []store.DrawdownTakeProfitRule{rule}},
			},
		}},
		protectionState:       map[string]string{"WLDUSDT_short": "native_trailing_armed"},
		drawdownState:         make(map[string]string),
		peakPnLCache:          map[string]float64{"WLDUSDT_short": 8.0},
		reArmFailCache:        make(map[string]int),
		nativeTrailingArmTime: make(map[string]time.Time),
	}

	for i := 0; i < 20; i++ {
		at.checkPositionDrawdown()
	}

	// 幻影被重挂过(立即单),但最后一次挂单必须是 activePx=0(无激活价),不是近价锚定。
	if fake.trailingActivation != 0 {
		t.Fatalf("passed tier must be re-placed with activePx=0 (immediate), got activation=%.6f", fake.trailingActivation)
	}
	// 关键:重挂被 activated 单认下 → 不再 churn。允许开头 1~2 次(撤幻影+挂立即单),但不得无界。
	if fake.trailingCalls > 2 {
		t.Fatalf("CHURN: passed tier must place immediate order ≤2 times across 20 cycles, got %d", fake.trailingCalls)
	}
}

// ---- E8~E10. co-run(双保险,不接管) 2026-07-27 -------------------------------
//
// 产品决定: managed 监控永远陪跑,交易所侧只在 per-tier 门禁处让位。
// 原来主循环是账户级 `if nativeTrailingHandled { continue }` —— 一个聚合判定就把整个
// 仓位的 managed 关掉。下面三例分别钉住这个改动的三条腿。

// E8. 断路器跳闸 + 交易所仍留着一张"看起来有效"的单 → managed 必须仍然平仓。
//
// 这是零保护缺口的完整复现:跳闸后 arm 循环不再挂单(反 churn,正确),而
// applyExchangeFailedLocalMonitor 对 close=100% 的档什么都不挂、不注册执行者,只写状态。
// 修复前 accountReArmBreaker 把跳闸档当"已覆盖"→ nativeTrailingHandled=true →
// 主循环 continue → 交易所无维护 + managed 被跳过 = 谁都不平仓,而面板显示 armed。
func TestTrippedBreaker_ManagedStillCloses(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "tripped-corun.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	entry := 100.0
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 0.7, MaxDrawdownPct: 1.5, CloseRatioPct: 100}
	nRule := normalizeDrawdownRule(rule)
	activePx := calculateProfitBasedTrailingTriggerPrice(entry, "short", nRule.MinProfitPct)
	cb := calculateDrawdownRuleCallbackRatio(entry, "short", nRule)
	// 一张 activated 单 —— 平时会被门禁判为"有效覆盖"并抑制 managed。
	stale := tradertypes.OpenOrder{
		OrderID: "stale-act", Symbol: "BTCUSDT", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET",
		Quantity: 10.0, StopPrice: activePx, ActivationPrice: activePx, CallbackRate: cb,
		ActivationStatus: "activated", Status: "NEW",
	}
	fake := &fakeProtectionTrader{
		positions: []map[string]interface{}{{
			"symbol": "BTCUSDT", "side": "short", "entryPrice": entry, "markPrice": 99.0, "positionAmt": 10.0,
		}},
		openOrders: []tradertypes.OpenOrder{stale},
	}
	at := &AutoTrader{
		id: "trader-trip", exchangeID: "exch-trip", store: st, exchange: "okx", trader: fake,
		config: AutoTraderConfig{StrategyConfig: &store.StrategyConfig{
			Protection: store.ProtectionConfig{
				DrawdownTakeProfit: store.DrawdownTakeProfitConfig{Enabled: true, Rules: []store.DrawdownTakeProfitRule{rule}},
			},
		}},
		protectionState: map[string]string{"BTCUSDT_short": "native_trailing_armed"},
		drawdownState:   make(map[string]string),
		peakPnLCache:    map[string]float64{"BTCUSDT_short": 3.0},
		reArmFailCache:  make(map[string]int),
	}
	// 预置该档为已跳闸。
	key := reArmFailKey("BTCUSDT", "short", nRule, entry)
	for i := 0; i < reArmBreakerLimit; i++ {
		at.bumpReArmFail(key)
	}

	at.checkPositionDrawdown()

	if fake.closeShortCalls != 1 {
		t.Fatalf("DECISIVE: tier 的断路器已跳闸(交易所侧不再维护),managed 必须平仓一次,got %d。"+
			"报 covered 会让该档既无交易所单也无执行者 = 零保护而面板显示 armed", fake.closeShortCalls)
	}
	// 反 churn 必须同时保住: 跳闸档不得再挂单。
	if fake.trailingCalls != 0 {
		t.Fatalf("跳闸档不得再向交易所挂单(反 churn),got %d", fake.trailingCalls)
	}
}

// E9. 门禁抑制时,档位状态必须回滚为 tracking。
//
// evaluateDrawdownTiers 在返回前就把档位标成 executed,外层门禁再 continue。
// 不回滚 → 该档被后续所有轮次过滤掉(executed 不再评估)→ managed 永久停止看护一个
// 它从未平掉的档,且 hasAllTiersCompleted 会宣称仓位已全部退出。
// managed 现在每轮都评估,所以这条从"偶发"变成"必然"。
func TestGateSuppressedTier_StaysTrackingNotExecuted(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "gate-revert.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	entry := 100.0
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 0.7, MaxDrawdownPct: 1.5, CloseRatioPct: 100}
	nRule := normalizeDrawdownRule(rule)
	activePx := calculateProfitBasedTrailingTriggerPrice(entry, "short", nRule.MinProfitPct)
	cb := calculateDrawdownRuleCallbackRatio(entry, "short", nRule)
	activated := tradertypes.OpenOrder{
		OrderID: "eff-act", Symbol: "BTCUSDT", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET",
		Quantity: 10.0, StopPrice: activePx, ActivationPrice: activePx, CallbackRate: cb,
		ActivationStatus: "activated", Status: "NEW",
	}
	fake := &fakeProtectionTrader{
		positions: []map[string]interface{}{{
			"symbol": "BTCUSDT", "side": "short", "entryPrice": entry, "markPrice": 99.0, "positionAmt": 10.0,
		}},
		openOrders: []tradertypes.OpenOrder{activated},
	}
	at := &AutoTrader{
		id: "trader-gate", exchangeID: "exch-gate", store: st, exchange: "okx", trader: fake,
		config: AutoTraderConfig{StrategyConfig: &store.StrategyConfig{
			Protection: store.ProtectionConfig{
				DrawdownTakeProfit: store.DrawdownTakeProfitConfig{Enabled: true, Rules: []store.DrawdownTakeProfitRule{rule}},
			},
		}},
		protectionState: map[string]string{"BTCUSDT_short": "native_trailing_armed"},
		drawdownState:   make(map[string]string),
		peakPnLCache:    map[string]float64{"BTCUSDT_short": 3.0},
		reArmFailCache:  make(map[string]int),
	}

	// 回撤已触发,但交易所单有效 → 门禁抑制 managed 平仓。
	at.checkPositionDrawdown()
	if fake.closeShortCalls != 0 {
		t.Fatalf("交易所单有效时 managed 必须让位(不重复平仓),got %d closes", fake.closeShortCalls)
	}
	allocs := at.getDrawdownTierAllocs("BTCUSDT", "short")
	if len(allocs) == 0 {
		t.Fatal("expected tier allocations to be initialized")
	}
	for _, a := range allocs {
		if a.Status == "executed" {
			t.Fatalf("档位 %s 被门禁抑制却留在 executed —— 后续轮次会把它过滤掉,"+
				"managed 从此不再看护一个从未平掉的档(hasAllTiersCompleted 还会宣称已全退出);"+
				"应回滚为 tracking。allocs=%+v", a.StageName, allocs)
		}
	}

	// 再跑若干轮:档位必须始终保持可评估(不被 executed 过滤),且仍然不重复平仓。
	for i := 0; i < 5; i++ {
		at.checkPositionDrawdown()
	}
	if fake.closeShortCalls != 0 {
		t.Fatalf("交易所单持续有效,managed 必须一直让位,got %d closes", fake.closeShortCalls)
	}
	if hasAllTiersCompleted(at.getDrawdownTierAllocs("BTCUSDT", "short")) {
		t.Fatal("门禁抑制不得让 hasAllTiersCompleted 宣称仓位已全部退出(该档从未真正平仓)")
	}
	// 决定性断言:该档仍能被 managed 评估出触发 —— 状态没有被 executed 永久吞掉。
	// (直接调用会把状态置为 executed,断言后立即回滚。)
	triggered := at.evaluateDrawdownTiers("BTCUSDT", "short", 1.0, 3.0)
	if triggered == nil {
		t.Fatal("档位应仍可被评估触发;返回 nil 说明它已被 executed 过滤,managed 再也不会看护它 —— " +
			"这正是门禁抑制时不回滚状态的后果")
	}
	at.updateTierAlloc("BTCUSDT", "short", triggered.TierIndex, func(a *store.DrawdownTierAllocation) {
		a.Status = "tracking"
	})
}

// E10. 交易所侧完全有效时,managed 陪跑但不重复平仓 —— 双保险不得变成双下单。
// (与 E8/E9 一起:门禁是唯一去重点,且它按"有效性"而非"存在性"判定。)
func TestCoRun_EffectiveExchangeSide_NoDoubleClose(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "corun-nodouble.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	entry := 100.0
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 0.7, MaxDrawdownPct: 1.5, CloseRatioPct: 100}
	nRule := normalizeDrawdownRule(rule)
	activePx := calculateProfitBasedTrailingTriggerPrice(entry, "short", nRule.MinProfitPct)
	cb := calculateDrawdownRuleCallbackRatio(entry, "short", nRule)
	activated := tradertypes.OpenOrder{
		OrderID: "eff-2", Symbol: "BTCUSDT", PositionSide: "SHORT", Type: "TRAILING_STOP_MARKET",
		Quantity: 10.0, StopPrice: activePx, ActivationPrice: activePx, CallbackRate: cb,
		ActivationStatus: "activated", Status: "NEW",
	}
	fake := &fakeProtectionTrader{
		positions: []map[string]interface{}{{
			"symbol": "BTCUSDT", "side": "short", "entryPrice": entry, "markPrice": 99.0, "positionAmt": 10.0,
		}},
		openOrders: []tradertypes.OpenOrder{activated},
	}
	at := &AutoTrader{
		id: "trader-corun", exchangeID: "exch-corun", store: st, exchange: "okx", trader: fake,
		config: AutoTraderConfig{StrategyConfig: &store.StrategyConfig{
			Protection: store.ProtectionConfig{
				DrawdownTakeProfit: store.DrawdownTakeProfitConfig{Enabled: true, Rules: []store.DrawdownTakeProfitRule{rule}},
			},
		}},
		protectionState: map[string]string{"BTCUSDT_short": "native_trailing_armed"},
		drawdownState:   make(map[string]string),
		peakPnLCache:    map[string]float64{"BTCUSDT_short": 3.0},
		reArmFailCache:  make(map[string]int),
	}
	for i := 0; i < 20; i++ {
		at.checkPositionDrawdown()
	}
	if fake.closeShortCalls != 0 {
		t.Fatalf("co-run 不得变成双下单: 交易所单持续有效,managed 必须 20 轮都让位,got %d closes", fake.closeShortCalls)
	}
	if fake.cancelTrailingCalls != 0 {
		t.Fatalf("有效单不得被撤,got %d cancels", fake.cancelTrailingCalls)
	}
}
