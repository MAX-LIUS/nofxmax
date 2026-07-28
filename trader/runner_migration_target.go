package trader

import "math"

// runnerMigrationTarget 是"要撤哪张在场 trailing 单"的判定结果。
//
// 抽成独立函数的原因:这个判定原先内联在 buildPositionProtectionRuntime 里,而进入
// 那段代码需要 higher_timeframe_runner 阶段 + 结构锚点 + 落库决策,导致这段风险最高的
// 逻辑(撤错档 = 摘掉那一档的止盈保护)实际上无法被直接测到。
type runnerMigrationTarget struct {
	OrderID       string
	ClientOrderID string
	Quantity      float64
	// Resolved 为 false 时调用方必须把 migration 标成不可动作,并且**不要**发计划。
	Resolved bool
	Reason   string
}

// resolveRunnerMigrationTarget 在在场 trailing 单里找出与 (liveActivation, liveCallback)
// 对应的那一张。
//
// 两条拒绝规则,都是"宁可不动作也不要动错对象":
//
//  1. candidateCount > 1:(liveActivation, liveCallback) 这对标量是 last-wins 取来的,
//     多档 trailing 常驻 2~4 张时无法确定它属于 runner 档还是某个部分档。
//  2. 精确匹配不上:原实现在这里退到 trailingOrders[0] —— 随便挑一张撤。撤错档的后果
//     不对称(部分档被当 runner 档撤掉,该档止盈保护消失),而 requires_confirmation
//     只能让人确认动作,无法让人发现 order_id 指错。
func resolveRunnerMigrationTarget(
	trailingOrders []map[string]interface{},
	liveActivation, liveCallback float64,
	candidateCount int,
) runnerMigrationTarget {
	if candidateCount > 1 {
		return runnerMigrationTarget{Reason: "ambiguous_live_trailing_multiple_candidates"}
	}
	if liveActivation <= 0 || liveCallback <= 0 {
		return runnerMigrationTarget{Reason: "missing_live_trailing"}
	}
	for _, order := range trailingOrders {
		triggerVal, _ := order["trigger_price"].(float64)
		callbackVal, _ := order["callback_rate"].(float64)
		if math.Abs(triggerVal-liveActivation) <= math.Max(0.01, liveActivation*0.0001) &&
			math.Abs(callbackVal-liveCallback) <= math.Max(0.000001, liveCallback*0.001) {
			orderID, _ := order["order_id"].(string)
			if orderID == "" {
				// 匹配到了但没有可撤的 ID,等同于认不出目标。
				continue
			}
			clientOrderID, _ := order["client_order_id"].(string)
			qty, _ := order["quantity"].(float64)
			return runnerMigrationTarget{
				OrderID:       orderID,
				ClientOrderID: clientOrderID,
				Quantity:      qty,
				Resolved:      true,
				Reason:        "manual_replace_ready",
			}
		}
	}
	return runnerMigrationTarget{Reason: "no_live_trailing_matched_migration_target"}
}
