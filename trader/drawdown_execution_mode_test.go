package trader

import (
	"testing"

	"nofx/store"
)

// 2026-07-27 线上回归:CLUSDT / SKHYUSDT 两个仓位都是 dd1(close=100%)+ partial_profit_lock
// (close=30%)双档。双档同时武装时 getDrawdownExecutionMode 返回 "native_trailing_tiers",
// 而面板 runtime 的字面量白名单只认 "native_partial_trailing" / "native_trailing_full"
// —— 整段 ID 匹配被跳过,matchedLive 恒 false,DD1/DD2 同时红灯,而交易所上两张 trailing
// 单都健康挂着。这一组测试把"归属判断必须覆盖全部 native_* / managed*"钉住。
func TestDrawdownExecutionModeIsNative_CoversEveryNativeMode(t *testing.T) {
	// getDrawdownExecutionMode 能返回的全部 native 家族取值。
	nativeModes := []string{
		"native_trailing_pending",
		"native_trailing_arming",
		"native_trailing_tiers",
		"native_partial_trailing_tiers",
		"native_trailing_full",
		"native_partial_trailing",
	}
	for _, mode := range nativeModes {
		if !drawdownExecutionModeIsNative(mode) {
			t.Errorf("drawdownExecutionModeIsNative(%q)=false, want true — 漏掉任何一个都会让该形态的仓位 DD 档恒红灯", mode)
		}
		if drawdownExecutionModeIsManaged(mode) {
			t.Errorf("drawdownExecutionModeIsManaged(%q)=true, want false — native 与 managed 归属必须互斥", mode)
		}
	}

	managedModes := []string{
		"managed_drawdown",
		"managed_partial_drawdown",
		"managed_drawdown_exchange_failed",
	}
	for _, mode := range managedModes {
		if !drawdownExecutionModeIsManaged(mode) {
			t.Errorf("drawdownExecutionModeIsManaged(%q)=false, want true", mode)
		}
		if drawdownExecutionModeIsNative(mode) {
			t.Errorf("drawdownExecutionModeIsNative(%q)=true, want false", mode)
		}
	}

	// 既不是 native 也不是 managed 的取值:不能被任一谓词认领。
	for _, mode := range []string{"disabled", "local_fallback", "", "code_managed_disabled"} {
		if drawdownExecutionModeIsNative(mode) {
			t.Errorf("drawdownExecutionModeIsNative(%q)=true, want false", mode)
		}
	}
	// local_fallback 不含 managed 前缀,不应被 managed 谓词认领(它走 !supportsNative 那条路)。
	if drawdownExecutionModeIsManaged("local_fallback") {
		t.Errorf("drawdownExecutionModeIsManaged(\"local_fallback\")=true, want false")
	}
}

// TestExchangeLight_MultiTierNativeModeIsGreenWhenOrderLive 复现线上症状本身:
// 双档 mode 下,一张健康的 trailing 单必须让该档变绿,而不是因为归属判断没认出
// native 就报红。matchedLive 由调用方的 ID 匹配决定,这里直接断言 light 函数在
// 多档 mode 字符串下的行为与单档一致。
func TestExchangeLight_MultiTierNativeModeIsGreenWhenOrderLive(t *testing.T) {
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 6, MaxDrawdownPct: 30, CloseRatioPct: 100}
	for _, mode := range []string{"native_trailing_full", "native_trailing_tiers", "native_partial_trailing_tiers"} {
		// 空仓侧:mark 104,活价 106 尚未触达 → resting 健康 → green。
		got := computeExchangeLight("long", 104, 4, 0, rule, true, "", 106, 106, true, mode)
		if got != "green" {
			t.Errorf("mode=%s: computeExchangeLight=%q want green", mode, got)
		}
		// 同一 mode 下,单确实不存在时仍必须报红 —— 修复不能把红灯一起抹掉。
		if got := computeExchangeLight("long", 104, 4, 0, rule, false, "", 0, 106, true, mode); got != "red" {
			t.Errorf("mode=%s: missing order light=%q want red", mode, got)
		}
	}
}

// managed_drawdown_armed 曾经没有映射,掉到函数尾部按交易所能力返回
// native_trailing_pending —— 一个纯 managed 兜底的仓位被判成"native 待挂单",面板
// 去交易所找单找不到就红灯。归属修正后它是 managed → 绿灯(进程内监控在保护)。
func TestExchangeLight_ManagedDrawdownModeIsGreenWithoutExchangeOrder(t *testing.T) {
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 6, MaxDrawdownPct: 30, CloseRatioPct: 100}
	if got := computeExchangeLight("long", 104, 4, 0, rule, false, "", 0, 106, true, "managed_drawdown"); got != "green" {
		t.Fatalf("computeExchangeLight(managed_drawdown)=%q want green", got)
	}
	// 修复前的错误归属会走 native 分支 → 红灯,这里把两者的差异写清楚。
	if got := computeExchangeLight("long", 104, 4, 0, rule, false, "", 0, 106, true, "native_trailing_pending"); got != "red" {
		t.Fatalf("computeExchangeLight(native_trailing_pending, no order)=%q want red", got)
	}
}

// protectionState 那一格是动态保护武装进度的唯一记忆。exchange_protection_verified
// 不能覆盖它 —— 手写并集曾漏 managed_drawdown_armed,导致校验通过那一轮把 managed
// 武装记忆抹掉,下一轮重新武装。
func TestIsDynamicDrawdownArmState_CoversEverySetProtectionStateWrite(t *testing.T) {
	armStates := []string{
		"native_trailing_arming",
		"native_trailing_armed",
		"native_partial_trailing_arming",
		"native_partial_trailing_armed",
		"managed_drawdown_armed",
		"managed_partial_drawdown_armed",
		"managed_drawdown_exchange_failed_armed",
		"managed_partial_drawdown_exchange_failed_armed",
	}
	for _, state := range armStates {
		if !isDynamicDrawdownArmState(state) {
			t.Errorf("isDynamicDrawdownArmState(%q)=false, want true — 漏掉的状态会被 exchange_protection_verified 覆盖,武装记忆丢失", state)
		}
	}
	for _, state := range []string{"exchange_protection_verified", "drawdown_triggered", "", "reconcile_failed: boom"} {
		if isDynamicDrawdownArmState(state) {
			t.Errorf("isDynamicDrawdownArmState(%q)=true, want false", state)
		}
	}
}
