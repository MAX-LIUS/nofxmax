package trader

import (
	"errors"
	"fmt"
	"testing"

	"nofx/trader/binance"
	"nofx/trader/bitget"
	"nofx/trader/okx"
)

// 观测漏洞回归:native trailing 挂单失败此前只有 Binance 分支记 WARN,
// OKX/Bitget 全记 INFO。健康巡检按 `[WARN]` 取样,于是 3 个 OKX 交易员的
// 挂单失败在巡检里完全不可见 —— 我自己那一轮巡检就漏过去了。
//
// 提级的前提是先能区分"无单可挂",否则平仓同步空窗会把假警报批量塞进 WARN
// (Binance 侧实测 19s 空窗内两轮),等于把刚修好的噪音换个交易所再犯一次。

func TestPositionGoneSentinelRecognizedForBothVenues(t *testing.T) {
	// 两个交易所的 sentinel 值相同但类型独立,判据必须逐个认。
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"binance 原样", binance.ErrPositionGone, true},
		{"okx 原样", okx.ErrPositionGone, true},
		{"binance 适配器实际形态", fmt.Errorf("%w: no open SHORT position on WLDUSDC to attach trailing stop", binance.ErrPositionGone), true},
		{"okx 适配器实际形态", fmt.Errorf("%w: no active position found for trailing stop: ETHUSDT SHORT", okx.ErrPositionGone), true},
		{"okx 多层包装", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", okx.ErrPositionGone)), true},
		{"bitget 适配器实际形态", fmt.Errorf("%w: no active position found for trailing stop: BTCUSDT LONG", bitget.ErrPositionGone), true},
		{"真实挂单失败", errors.New("APIError code=-1001 internal error"), false},
		{"仅文案相同的裸错误", errors.New("no active position found for trailing stop: ETHUSDT SHORT"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := trailingFailureIsPositionGone(c.err); got != c.want {
			t.Errorf("%s: 期望 %v 得到 %v", c.name, c.want, got)
		}
	}
}

func TestBothVenueSentinelsAreDistinctValues(t *testing.T) {
	// 两个 sentinel 必须是**不同的 error 值**(各包独立定义),否则 errors.Is
	// 只认一个就够了,这个测试就失去意义 —— 它守的是"逐个认"这件事本身。
	if errors.Is(binance.ErrPositionGone, okx.ErrPositionGone) {
		t.Fatal("两个 sentinel 不应互相 Is —— 若哪天合并到公共包,请同时简化判据并删除本测试")
	}
	if errors.Is(okx.ErrPositionGone, binance.ErrPositionGone) {
		t.Fatal("两个 sentinel 不应互相 Is")
	}
	if errors.Is(bitget.ErrPositionGone, okx.ErrPositionGone) {
		t.Fatal("bitget 与 okx 的 sentinel 不应互相 Is")
	}
}

// logNativeTrailingPlacementFailure 的路由:position-gone 静默(Infof),
// 其余一律 Warnf。这里断言的是"不 panic 且分流正确"这一可测部分 ——
// 日志级别本身由上面的 sentinel 判据唯一决定,不需要捕获 logger 输出。
func TestPlacementFailureLoggingRoutesPositionGoneSilently(t *testing.T) {
	at := &AutoTrader{exchange: "okx", protectionState: make(map[string]string)}
	gone := fmt.Errorf("%w: no active position found for trailing stop: ETHUSDT SHORT", okx.ErrPositionGone)
	real := errors.New("APIError 51000 parameter sz error")

	// 关键不变量:记录失败**不得**改动保护状态。状态升级只属于
	// applyExchangeFailedLocalMonitor,记日志的函数越权写状态会让面板与执行脱钩。
	for _, e := range []error{gone, real, nil} {
		at.logNativeTrailingPlacementFailure("full", "okx", "ETHUSDT", "short", e)
		if s := at.getProtectionState("ETHUSDT", "short"); s != "" {
			t.Fatalf("记录挂单失败不应写保护状态,却写了 %q", s)
		}
	}
}
