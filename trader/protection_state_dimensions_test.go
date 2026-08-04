package trader

import (
	"sync"
	"testing"
)

// 维度分家后最容易新引入的 bug 在 CAS 这一条:调用方传进来的 expectedCurrentState 来自
// getProtectionState(**合成值**),若 claimProtectionArmingState 只拿武装维度去比,
// 那么"武装维度为空、观测维度是 exchange_protection_verified"的仓位(线上最常见的形态)
// 会永远 CAS 不成立 —— 一个从未武装过的仓位再也武装不上,比原 bug 更严重。
func TestClaimArmingComparesAgainstDerivedState(t *testing.T) {
	at := &AutoTrader{protectionStateMutex: sync.RWMutex{}}
	at.setProtectionState("SOLUSDT", "long", "exchange_protection_verified")

	current := at.getProtectionState("SOLUSDT", "long")
	claimed, prevArm, actual := at.claimProtectionArmingState("SOLUSDT", "long", current, "native_trailing_arming")
	if !claimed {
		t.Fatalf("已校验但未武装的仓位必须能抢到武装权:actual=%q", actual)
	}
	if prevArm != "" {
		t.Fatalf("抢占前武装维度本应为空,got %q", prevArm)
	}
	if got := at.getProtectionArmState("SOLUSDT", "long"); got != "native_trailing_arming" {
		t.Fatalf("抢占后武装维度应为 arming,got %q", got)
	}
}

// arming 期间别人往观测维度写了新值,回滚不得把它抹掉,也不得把观测值当武装值写回去。
func TestRollbackOnlyTouchesArmDimension(t *testing.T) {
	at := &AutoTrader{protectionStateMutex: sync.RWMutex{}}
	at.setProtectionState("SOLUSDT", "long", "exchange_protection_verified")
	current := at.getProtectionState("SOLUSDT", "long")
	claimed, prevArm, _ := at.claimProtectionArmingState("SOLUSDT", "long", current, "native_trailing_arming")
	if !claimed {
		t.Fatal("应抢到武装权")
	}
	// arming 期间对账失败,观测维度被改写。
	at.setProtectionState("SOLUSDT", "long", "reconcile_failed: 51279")

	at.rollbackProtectionArmingState("SOLUSDT", "long", "native_trailing_arming", prevArm)

	if got := at.getProtectionArmState("SOLUSDT", "long"); got != "" {
		t.Fatalf("回滚后武装维度应清空,got %q", got)
	}
	if got := at.getProtectionState("SOLUSDT", "long"); got != "reconcile_failed: 51279" {
		t.Fatalf("回滚不得抹掉 arming 期间的观测值,got %q", got)
	}
}

// 抢占前武装维度已有 managed 记忆时,回滚必须还原成 managed,而不是清空 ——
// 清空等于把"managed 兜底已武装"这件事丢掉,下一轮会重新武装一次。
func TestRollbackRestoresPreviousManagedArm(t *testing.T) {
	at := &AutoTrader{protectionStateMutex: sync.RWMutex{}}
	at.setProtectionState("SOLUSDT", "long", "managed_drawdown_armed")
	current := at.getProtectionState("SOLUSDT", "long")
	claimed, prevArm, _ := at.claimProtectionArmingState("SOLUSDT", "long", current, "native_trailing_arming")
	if !claimed {
		t.Fatal("managed 已武装的仓位仍可升级为 native arming")
	}
	if prevArm != "managed_drawdown_armed" {
		t.Fatalf("prevArm 应是 managed 记忆,got %q", prevArm)
	}
	at.rollbackProtectionArmingState("SOLUSDT", "long", "native_trailing_arming", prevArm)
	if got := at.getProtectionState("SOLUSDT", "long"); got != "managed_drawdown_armed" {
		t.Fatalf("回滚应还原 managed 记忆,got %q", got)
	}
}

// 武装成功后 arming 已被替换成 armed,此时迟到的回滚必须什么都不做(原实现的 defer
// 语义:只有这一格还是我写的 armingState 时才还原)。
func TestRollbackNoopAfterSuccessfulArm(t *testing.T) {
	at := &AutoTrader{protectionStateMutex: sync.RWMutex{}}
	claimed, prevArm, _ := at.claimProtectionArmingState("SOLUSDT", "long", "", "native_trailing_arming")
	if !claimed {
		t.Fatal("空状态应抢到武装权")
	}
	at.setProtectionState("SOLUSDT", "long", "native_trailing_armed")
	at.rollbackProtectionArmingState("SOLUSDT", "long", "native_trailing_arming", prevArm)
	if got := at.getProtectionState("SOLUSDT", "long"); got != "native_trailing_armed" {
		t.Fatalf("武装成功后迟到的回滚不得生效,got %q", got)
	}
}

// arming 中的重复抢占仍要被拒(去重语义不能因分家而失效)。
func TestClaimRejectsDuplicateArming(t *testing.T) {
	at := &AutoTrader{protectionStateMutex: sync.RWMutex{}}
	if claimed, _, _ := at.claimProtectionArmingState("SOLUSDT", "long", "", "native_trailing_arming"); !claimed {
		t.Fatal("首次应抢到")
	}
	claimed, _, actual := at.claimProtectionArmingState("SOLUSDT", "long", "", "native_partial_trailing_arming")
	if claimed {
		t.Fatal("arming 中不得被重复抢占")
	}
	if actual != "native_trailing_arming" {
		t.Fatalf("actual 应报告在场的 arming 态,got %q", actual)
	}
}

// 维度归属判定表:新增状态值时这张表就是唯一需要维护的地方。
func TestClassifyProtectionStateWrite(t *testing.T) {
	for state, want := range map[string]protectionStateDimension{
		"":                               protectionDimReset,
		"native_trailing_armed":          protectionDimArm,
		"native_partial_trailing_arming": protectionDimArm,
		"managed_drawdown_armed":         protectionDimArm,
		"managed_partial_drawdown_exchange_failed_armed": protectionDimArm,
		"exchange_protection_verified":                   protectionDimObservation,
		"reconcile_failed: boom":                         protectionDimObservation,
		"drawdown_triggered":                             protectionDimObservationResetsArm,
		"drawdown_triggered_dd2":                         protectionDimObservationResetsArm,
	} {
		if got := classifyProtectionStateWrite(state); got != want {
			t.Fatalf("state=%q got dim=%d want=%d", state, got, want)
		}
	}
}

// 跨仓位隔离:两个维度都必须按 symbol_side 分格,不能互相串。
func TestDimensionsAreIsolatedPerPosition(t *testing.T) {
	at := &AutoTrader{protectionStateMutex: sync.RWMutex{}}
	at.setProtectionState("SOLUSDT", "long", "native_trailing_armed")
	at.setProtectionState("BTCUSDT", "long", "reconcile_failed: boom")
	at.clearProtectionState("BTCUSDT", "long")
	if got := at.getProtectionState("SOLUSDT", "long"); got != "native_trailing_armed" {
		t.Fatalf("清 BTC 不该动 SOL,got %q", got)
	}
}
