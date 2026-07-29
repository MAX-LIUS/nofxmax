package trader

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"nofx/store"
	"nofx/trader/binance"
)

// 实盘 2026-07-30 WLDUSDC short 的回归:交易所 03:00:03 平仓,本地 order_sync 到
// 03:00:22 才落账。19s 空窗里保护层按本地仍持仓去挂 native trailing,adapter 回
// "no open SHORT position",旧代码把它当"挂单失败",于是
//   1. 给一个已经不存在的仓位 arm 了 managed-drawdown 兜底监控;
//   2. 状态升级成 managed_drawdown_exchange_failed_armed,面板反色告警。
// 两者都是假警报。开仓侧早有 protectionFillGraceWindow 做"还不知道≠已经平了",
// 平仓侧此前没有对称闸门。
//
// 这两个测试是那道闸门的守卫:去掉 trailingFailureIsPositionGone 分支,它们必红。

func positionGoneFixture(t *testing.T, injected error) (*AutoTrader, *fakeProtectionTrader) {
	t.Helper()
	fake := &fakeProtectionTrader{
		positions: []map[string]interface{}{{
			"symbol":      "WLDUSDT",
			"side":        "short",
			"entryPrice":  0.3049,
			"markPrice":   0.2900,
			"positionAmt": 455.3,
		}},
		trailingPlaceErr: injected,
	}
	at := &AutoTrader{
		exchange: "binance",
		trader:   fake,
		config: AutoTraderConfig{
			StrategyConfig: &store.StrategyConfig{},
		},
		protectionState: make(map[string]string),
	}
	return at, fake
}

// 用实盘那档参数:close=100%,dd1。
func positionGoneRule() store.DrawdownTakeProfitRule {
	return store.DrawdownTakeProfitRule{
		MinProfitPct:   3.6791,
		MaxDrawdownPct: 2.2075,
		CloseRatioPct:  100,
	}
}

func TestTrailingPositionGoneMustNotArmLocalMonitorOrPanelWarning(t *testing.T) {
	// adapter 的真实错误形态:sentinel 被 %w 包进一句带交易所符号的话。
	gone := fmt.Errorf("%w: no open SHORT position on WLDUSDC to attach trailing stop", binance.ErrPositionGone)
	at, fake := positionGoneFixture(t, gone)

	ok := at.applyNativeTrailingDrawdown("WLDUSDT", "short", 0.3049, 0, positionGoneRule())
	if ok {
		t.Fatal("仓位已消失时不应报告挂单成功")
	}
	if fake.trailingCalls != 0 {
		t.Fatalf("不应有成功挂单计数, got %d", fake.trailingCalls)
	}
	// 核心断言:状态不得被升级成 exchange_failed 变体。
	state := at.getProtectionState("WLDUSDT", "short")
	if strings.Contains(state, "exchange_failed") {
		t.Fatalf("仓位已消失却点亮了 exchange_failed 面板告警: state=%s", state)
	}
	// 也不应留下任何 managed 兜底 armed 状态 —— 兜底监控不该挂在不存在的仓位上。
	if strings.Contains(state, "managed_") && strings.Contains(state, "armed") {
		t.Fatalf("给已消失的仓位 arm 了本地兜底监控: state=%s", state)
	}
}

func TestGenuineTrailingFailureStillFallsBackToLocalMonitor(t *testing.T) {
	// 对偶用例:真正的挂单失败必须仍然走本地兜底 + 面板告警。这条防止
	// "修假警报"顺手把真警报也一起吞掉。
	at, _ := positionGoneFixture(t, errors.New("APIError code=-1001 internal error"))

	at.applyNativeTrailingDrawdown("WLDUSDT", "short", 0.3049, 0, positionGoneRule())

	state := at.getProtectionState("WLDUSDT", "short")
	if !strings.Contains(state, "exchange_failed") {
		t.Fatalf("真实挂单失败必须升级为 exchange_failed 状态以点亮面板, got state=%s", state)
	}
}

func TestPositionGoneSentinelSurvivesWrapping(t *testing.T) {
	// sentinel 判据必须穿透多层 %w 包装,且不得误判其它错误。
	wrapped := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", binance.ErrPositionGone))
	if !trailingFailureIsPositionGone(wrapped) {
		t.Fatal("多层包装后应仍然识别为 position-gone")
	}
	if trailingFailureIsPositionGone(nil) {
		t.Fatal("nil 不是 position-gone")
	}
	if trailingFailureIsPositionGone(errors.New("no open SHORT position on WLDUSDC")) {
		t.Fatal("仅文案相同但不带 sentinel 的错误不应被识别 —— 判据必须只认 sentinel")
	}
}
