package trader

import (
	"path/filepath"
	"testing"

	"nofx/store"
)

// 钉住 2026-07-31 线上"日志在说一件没发生的事":time-stop 在 10s 内触发两次,
// 第二次撞上 NO_POSITION,却照样打出 `✅ Time-stop closed`(CLUSDT、ETHUSDT 各一次)。
// 幂等性本身没坏(没有重复下单),坏的是事后按这行日志计数会把平仓次数数成 2 倍。
//
// 这里锁的是"跳过"与"平掉"必须在返回值上可区分,而不是靠读日志猜。

func TestCloseOrderSkippedRecognizesNonExecution(t *testing.T) {
	skipped := []string{"NO_POSITION", "SKIPPED", "POSITION_DUST"}
	for _, s := range skipped {
		got, status := closeOrderSkipped(map[string]interface{}{"status": s})
		if !got || status != s {
			t.Fatalf("%s 必须被判为未执行,got skipped=%t status=%q", s, got, status)
		}
	}

	// 真成交与未知状态都不得被判成"跳过" —— 误判会让真实平仓被记成没发生。
	executed := []map[string]interface{}{
		{"orderId": "close-long-1"},
		{"status": "FILLED"},
		{"status": ""},
		nil,
	}
	for i, order := range executed {
		if got, status := closeOrderSkipped(order); got {
			t.Fatalf("case %d 不得被判为跳过(status=%q)", i, status)
		}
	}
}

func newOutcomeTrader(t *testing.T, fake *fakeProtectionTrader) *AutoTrader {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "close-outcome.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	return &AutoTrader{id: "t1", exchange: "paper", trader: fake, store: st}
}

// 交易所确实收下平仓单 → executed=true。
func TestClosePositionWithOutcomeReportsExecuted(t *testing.T) {
	for _, side := range []string{"long", "short"} {
		fake := &fakeProtectionTrader{}
		at := newOutcomeTrader(t, fake)
		executed, err := at.closePositionByReasonWithOutcome("ETHUSDT", side, 0.5, "time_stop")
		if err != nil {
			t.Fatalf("%s close: %v", side, err)
		}
		if !executed {
			t.Fatalf("%s: 交易所收下了平仓单,executed 必须为 true", side)
		}
	}
}

// 仓位早就没了 → err 仍为 nil(幂等,不该报错重试),但 executed 必须为 false。
// 这正是旧代码把它当成"平掉了"的那条路径。
func TestClosePositionWithOutcomeReportsSkip(t *testing.T) {
	for _, status := range []string{"NO_POSITION", "SKIPPED", "POSITION_DUST"} {
		for _, side := range []string{"long", "short"} {
			fake := &fakeProtectionTrader{closeStatus: status}
			at := newOutcomeTrader(t, fake)
			executed, err := at.closePositionByReasonWithOutcome("ETHUSDT", side, 0.5, "time_stop")
			if err != nil {
				t.Fatalf("%s/%s: 跳过不是错误,不该回 err: %v", status, side, err)
			}
			if executed {
				t.Fatalf("%s/%s: 什么都没做,executed 必须为 false", status, side)
			}
		}
	}
}

// 兼容锁:老签名 closePositionByReason 的行为一个字都不能变(它有一堆调用方)。
// 跳过在它眼里仍是 nil —— 这是有意保留的,只有需要区分的调用方才改用 WithOutcome。
func TestClosePositionByReasonKeepsLegacyNilOnSkip(t *testing.T) {
	fake := &fakeProtectionTrader{closeStatus: "NO_POSITION"}
	at := newOutcomeTrader(t, fake)
	if err := at.closePositionByReason("ETHUSDT", "long", 0.5, "time_stop"); err != nil {
		t.Fatalf("老签名在跳过时必须仍回 nil,got %v", err)
	}
	if fake.closeLongCalls != 1 {
		t.Fatalf("平仓应被调用恰好 1 次,got %d", fake.closeLongCalls)
	}
}
