package trader

import "strings"

// protectionState 这一格历史上是**一个 string 装五件互不相干的事**:
//
//  1. 动态保护的武装进度(native trailing / managed 回撤,arming / armed,全量 / 部分)
//  2. 交易所静态保护校验通过(exchange_protection_verified)
//  3. 对账失败(reconcile_failed: ...)
//  4. 回撤已触发并平过仓(drawdown_triggered*)
//  5. 面板展示用的最后一次观测
//
// 后果:任何写第 2~5 件事的人都会顺手擦掉第 1 件事。2026-08-03 线上 SOLUSDT long 的
// 死锁就是这么来的 —— 挂单被 OKX 51279 拒 → 写 "reconcile_failed: ..." → 覆盖掉
// native_trailing_armed → nativeTrailingArmed=false 使 classifyTrailing 判"不是我的"
// → 每轮 staleTrail/degraded;而账本还是 armed,武装路径"已 armed,跳过"→ 内存状态
// 永远回不来,reclaim 每 ~20s 一次,438 轮不收敛。
//
// 之前的修法是在每个覆盖点加白名单(protectionStateAfterReconcileFailure、
// reconciler 的 isDynamicDrawdownArmState 判断)。那是"哪漏补哪":漏点数量等于写入点
// 数量,任何人新增一个写入点就重新漏一次,而且漏的时候没有任何编译期或测试期信号。
//
// 这里改成**按维度分家**:武装进度存 at.protectionState,非武装观测存
// at.protectionObservation,两张 map 同一把锁。setProtectionState 依旧收字符串
// (20 个调用点、83 处测试断言都不用改),但它先判断这个字符串属于哪个维度,再只写
// 那一个维度。于是"写观测擦掉武装"从**需要每处提防**变成**结构上不可能**。
//
// getProtectionState 仍返回单个字符串,武装维度优先、其次观测维度 —— 与分家前
// "最后一个写的人赢"在武装态存在时的取值一致,所有读者与谓词无需改动。

// protectionStateDimension 标识一个 protectionState 字符串属于哪个语义维度。
type protectionStateDimension int

const (
	// protectionDimReset:空串,表示整格清空(两个维度都清)。
	protectionDimReset protectionStateDimension = iota
	// protectionDimArm:动态保护武装进度。native_*/managed_* 八个值。
	protectionDimArm
	// protectionDimObservation:非武装观测(交易所校验、对账失败)。不碰武装维度。
	protectionDimObservation
	// protectionDimObservationResetsArm:观测,且按语义必须同时清掉武装维度。
	// 目前只有 drawdown_triggered*:managed 已经平过仓,仓位数量变了,原武装记忆
	// 指向的仓位指纹已不成立,留着会让"这一档已武装"骗过重新武装的门禁。
	protectionDimObservationResetsArm
)

// classifyProtectionStateWrite 判定写入值属于哪个维度。
//
// 判据只看值本身,不看当前状态 —— 维度归属是这个字符串的固有属性。
func classifyProtectionStateWrite(state string) protectionStateDimension {
	if state == "" {
		return protectionDimReset
	}
	if isDynamicDrawdownArmState(state) {
		return protectionDimArm
	}
	if isDrawdownTriggeredState(state) {
		return protectionDimObservationResetsArm
	}
	return protectionDimObservation
}

// isDrawdownTriggeredState 覆盖 auto_trader_risk.go:497 的 "drawdown_triggered" 与
// :414 的 "drawdown_triggered_"+stage 两种写法。
func isDrawdownTriggeredState(state string) bool {
	return state == "drawdown_triggered" || strings.HasPrefix(state, "drawdown_triggered_")
}

// deriveProtectionState 把两个维度合成对外的单一字符串。
//
// 武装优先:武装进度是行为依据(classifyTrailing、getDrawdownExecutionMode、
// positionHasArmedProtection 都据此判断),观测只是附加事实。分家前
// exchange_protection_verified 覆盖 native_trailing_armed 属于信息丢失而非有意降级
// —— reconciler 那句 "preserving dynamic state" 就是在人工挽回这件事。
func deriveProtectionState(arm, observation string) string {
	if arm != "" {
		return arm
	}
	return observation
}
