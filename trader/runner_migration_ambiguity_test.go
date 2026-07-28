package trader

import "testing"

// runner migration 的"在场 trailing"原先是两个各自独立赋值的标量:
//
//	if triggerPrice > 0       { liveTrailingTriggerPrice = triggerPrice }
//	if order.CallbackRate > 0 { liveTrailingCallbackRate = order.CallbackRate }
//
// 在多档 trailing(一个仓位常驻 2~4 张)下,这会拼出一对**不属于同一张单**的
// (trigger, callback):trigger 来自最后一张有 trigger 的单,callback 来自最后一张有
// callback 的单。这对幻影参数随后驱动漂移判定与 would_loosen_protection 安全结论,
// 再由 trailingOrders[0] 兜底"随便挑一张单来撤" —— 撤错档等于摘掉那一档的止盈保护。
//
// 判定已抽到 resolveRunnerMigrationTarget,所以这里能直接驱动它,不必先把仓位摆成
// higher_timeframe_runner 阶段。

func trailingOrderMap(orderID string, trigger, callback, qty float64) map[string]interface{} {
	return map[string]interface{}{
		"order_id":        orderID,
		"client_order_id": "cid-" + orderID,
		"trigger_price":   trigger,
		"callback_rate":   callback,
		"quantity":        qty,
	}
}

// 多张在场 trailing ⇒ 无法确定哪张属于 runner 档 ⇒ 拒绝动作。
func TestResolveRunnerMigrationTargetRefusesWhenMultipleCandidates(t *testing.T) {
	orders := []map[string]interface{}{
		trailingOrderMap("aaa", 81.06, 0.018786, 2.8),
		trailingOrderMap("bbb", 80.18, 0.012524, 0.84),
	}

	// 即便标量恰好精确命中其中一张,张数 >1 就说明这对标量的归属不可知。
	target := resolveRunnerMigrationTarget(orders, 81.06, 0.018786, 2)
	if target.Resolved {
		t.Fatalf("多张在场 trailing 时不应给出撤单目标,实得 %+v", target)
	}
	if target.Reason != "ambiguous_live_trailing_multiple_candidates" {
		t.Fatalf("reason = %q,应为 ambiguous_live_trailing_multiple_candidates", target.Reason)
	}
	if target.OrderID != "" {
		t.Fatalf("拒绝时不得返回 order_id,实得 %q", target.OrderID)
	}
}

// 反向对照:单张无歧义时必须能正常解析,证明修复不是"一律拒绝"。
func TestResolveRunnerMigrationTargetResolvesSingleCandidate(t *testing.T) {
	orders := []map[string]interface{}{
		trailingOrderMap("only-one", 81.06, 0.018786, 2.8),
	}

	target := resolveRunnerMigrationTarget(orders, 81.06, 0.018786, 1)
	if !target.Resolved {
		t.Fatalf("单张在场 trailing 应可解析,实得 %+v", target)
	}
	if target.OrderID != "only-one" || target.ClientOrderID != "cid-only-one" {
		t.Fatalf("撤单目标不对: %+v", target)
	}
	if target.Quantity != 2.8 {
		t.Fatalf("quantity = %v, want 2.8", target.Quantity)
	}
}

// 匹配不上时必须拒绝,而**不是**退回 trailingOrders[0]。这条正是旧兜底的替身测试:
// 传入一张与标量完全不符的单,断言不会把它当成撤单目标。
func TestResolveRunnerMigrationTargetRefusesInsteadOfPickingFirst(t *testing.T) {
	orders := []map[string]interface{}{
		trailingOrderMap("unrelated-partial-tier", 70.00, 0.030000, 0.84),
	}

	target := resolveRunnerMigrationTarget(orders, 81.06, 0.018786, 1)
	if target.Resolved {
		t.Fatalf("与在场标量不符时不应解析出目标,实得 %+v", target)
	}
	if target.OrderID == "unrelated-partial-tier" {
		t.Fatal("退回了列表第一张单 —— 这正是会撤错档的旧兜底行为")
	}
	if target.Reason != "no_live_trailing_matched_migration_target" {
		t.Fatalf("reason = %q,应为 no_live_trailing_matched_migration_target", target.Reason)
	}
}

// 在场标量缺一半(只有 trigger 或只有 callback)时不得解析。这条锁住"成对取值":
// 若哪天有人把两个 if 拆回独立赋值,拼出的幻影对会在这里暴露。
func TestResolveRunnerMigrationTargetRequiresBothScalars(t *testing.T) {
	orders := []map[string]interface{}{
		trailingOrderMap("aaa", 81.06, 0.018786, 2.8),
	}

	for _, c := range []struct {
		name       string
		activation float64
		callback   float64
	}{
		{"missing callback", 81.06, 0},
		{"missing activation", 0, 0.018786},
		{"both missing", 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			target := resolveRunnerMigrationTarget(orders, c.activation, c.callback, 1)
			if target.Resolved {
				t.Fatalf("标量不全时不应解析,实得 %+v", target)
			}
			if target.Reason != "missing_live_trailing" {
				t.Fatalf("reason = %q, want missing_live_trailing", target.Reason)
			}
		})
	}
}

// 匹配到了但那张单没有 order_id ⇒ 没有可撤对象,等同于认不出目标。
func TestResolveRunnerMigrationTargetSkipsMatchWithoutOrderID(t *testing.T) {
	orders := []map[string]interface{}{
		{"trigger_price": 81.06, "callback_rate": 0.018786, "quantity": 2.8},
	}

	target := resolveRunnerMigrationTarget(orders, 81.06, 0.018786, 1)
	if target.Resolved {
		t.Fatalf("没有 order_id 时不应解析,实得 %+v", target)
	}
	if target.Reason != "no_live_trailing_matched_migration_target" {
		t.Fatalf("reason = %q", target.Reason)
	}
}

// 容差边界:匹配用的是相对容差,同一张单的读回值有微小浮动时仍应认出来。
func TestResolveRunnerMigrationTargetToleratesTinyDrift(t *testing.T) {
	orders := []map[string]interface{}{
		trailingOrderMap("aaa", 81.060004, 0.0187861, 2.8),
	}

	target := resolveRunnerMigrationTarget(orders, 81.06, 0.018786, 1)
	if !target.Resolved {
		t.Fatalf("微小浮动应仍匹配同一张单,实得 %+v", target)
	}
	if target.OrderID != "aaa" {
		t.Fatalf("order_id = %q, want aaa", target.OrderID)
	}
}
