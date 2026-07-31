package trader

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"nofx/trader/okx"
)

// 钉住 2026-07-31 线上 BTCUSDT SHORT 的"一档连坐整份计划":
// 加仓后 qty resize 撤掉一张覆盖不足的 ladder_tp,计划重新物化 4 档 TP,其中一档触发价
// 62861.4135 已被市价 62860.10 越过(空头 TP 必须低于最新价),OKX 回 51277。
// 这一档 2 秒后就以 62860.10 成交 —— 它本来就在成交。但它被计入 tier 失败,
// 于是整份计划判失败:2 次重试全败 → 标不可重试 → ❌ reconcile failed,
// 而其余 3 档 TP 和 SL 其实都挂上了。
//
// 修复后:该档记为"跳过",不进 placeErrs,其余档位正常收尾。

func withFastVerify(t *testing.T) {
	t.Helper()
	original := protectionVerifyDelay
	protectionVerifyDelay = time.Millisecond
	t.Cleanup(func() { protectionVerifyDelay = original })
}

func ladderPlan() *ProtectionPlan {
	return &ProtectionPlan{
		StopLossOrders:   []ProtectionOrder{{Price: 64000, CloseRatioPct: 100}},
		TakeProfitOrders: []ProtectionOrder{{Price: 62861.4135, CloseRatioPct: 25}},
	}
}

// 触发价已被越过 → 不得进入 tier 失败聚合。
// 判据:返回的错误不再是 "ladder protection placement: N tier(s) failed"。
// (校验环节仍会失败,因为 fake 不把挂出的单回显进 GetOpenOrders —— 那是另一件事,
// 这里只区分"挂单阶段是否把它当失败"。)
func TestLadderTPSkipsTriggerAlreadyPassed(t *testing.T) {
	withFastVerify(t)
	fake := &fakeProtectionTrader{
		setTakeProfitErr: fmt.Errorf("OKX take profit rejected: code=51277 msg=TP trigger price cannot be higher than the last price: %w", okx.ErrTriggerPriceAlreadyPassed),
	}
	at := &AutoTrader{id: "t1", exchange: "okx", trader: fake}

	err := at.placeAndVerifyLadderProtection("BTCUSDT", "short", 0.01, ladderPlan())
	if err == nil {
		// fake 不回显挂单,校验必然不过;若为 nil 说明测试假设变了。
		t.Skip("fake 已回显挂单,本用例的判据需要重写")
	}
	if strings.Contains(err.Error(), "tier(s) failed") {
		t.Fatalf("触发价已被越过的档位不得计入 tier 失败(否则整份计划连坐),got: %v", err)
	}
	if errors.Is(err, okx.ErrTriggerPriceAlreadyPassed) {
		t.Fatalf("该档已被跳过,其错误不该继续向上传播,got: %v", err)
	}
	if fake.setTakeProfitCalls != 1 {
		t.Fatalf("TP 挂单应被尝试恰好 1 次,got %d", fake.setTakeProfitCalls)
	}
}

// 反向锁:真实拒因必须**照旧**计入 tier 失败并向上聚合 ——
// 静默吞掉挂单失败比连坐危险得多。
func TestLadderTPStillFailsOnRealRejection(t *testing.T) {
	withFastVerify(t)
	fake := &fakeProtectionTrader{
		setTakeProfitErr: errors.New("OKX take profit rejected: code=51000 msg=Parameter instId error"),
	}
	at := &AutoTrader{id: "t1", exchange: "okx", trader: fake}

	err := at.placeAndVerifyLadderProtection("BTCUSDT", "short", 0.01, ladderPlan())
	if err == nil {
		t.Fatal("真实拒因必须回错误")
	}
	if !strings.Contains(err.Error(), "tier(s) failed") {
		t.Fatalf("真实拒因必须计入 tier 失败并聚合上抛,got: %v", err)
	}
	if !strings.Contains(err.Error(), "51000") {
		t.Fatalf("原始拒因必须保留在错误链里,got: %v", err)
	}
}

// SL 侧不受影响:sentinel 只用于 TP 档(51277/51278 都是 TP 触发价方向错)。
// 止损挂不上永远是真失败,不得被任何跳过逻辑吞掉。
func TestLadderSLNeverSkipped(t *testing.T) {
	withFastVerify(t)
	fake := &fakeProtectionTrader{
		setStopLossErr: fmt.Errorf("OKX stop loss rejected: code=51277 msg=trigger price: %w", okx.ErrTriggerPriceAlreadyPassed),
	}
	at := &AutoTrader{id: "t1", exchange: "okx", trader: fake}

	err := at.placeAndVerifyLadderProtection("BTCUSDT", "short", 0.01, ladderPlan())
	if err == nil {
		t.Fatal("止损挂不上必须回错误")
	}
	if !strings.Contains(err.Error(), "tier(s) failed") {
		t.Fatalf("止损失败必须计入 tier 失败(绝不跳过),got: %v", err)
	}
	if !strings.Contains(err.Error(), "ladder stop loss") {
		t.Fatalf("错误必须指明是止损档,got: %v", err)
	}
}
