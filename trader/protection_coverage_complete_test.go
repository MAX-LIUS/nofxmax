package trader

import "testing"

// protectionCoverageComplete 是唯一能把 state 从 degraded 翻回 protected/verified 的判据,
// 两条分支(容忍、reclaim)共用它。错判方向不对称:
//   - 该 true 判成 false → 账本说谎(2026-07-31 BTCUSDT LONG 连续 1900 轮 degraded,
//     7688 条 not verified 警告),保护其实在;
//   - 该 false 判成 true → 真缺保护却被标已验证,后续修复流程不会介入。
//
// 后者是灾难性的,所以下面的用例逐项拆掉四个条件,确认任一不满足就必须 false。
func TestProtectionCoverageCompleteRequiresAllFourConditions(t *testing.T) {
	planWithTP := &ProtectionPlan{TakeProfitOrders: []ProtectionOrder{{Price: 100}}}
	planNoTP := &ProtectionPlan{}

	cases := []struct {
		name            string
		ownership       ProtectionOwnershipState
		plan            *ProtectionPlan
		missingSL       bool
		unexpectedStops int
		unexpectedTPs   int
		want            bool
	}{
		{
			name:      "四项全中(reclaim 后覆盖完整)",
			ownership: ProtectionOwnershipState{StopOwner: "drawdown", ProfitOwner: "ladder"},
			plan:      planWithTP,
			want:      true,
		},
		{
			name:      "计划不要求止盈主人时,止盈主人缺席也算完整",
			ownership: ProtectionOwnershipState{StopOwner: "drawdown"},
			plan:      planNoTP,
			want:      true,
		},
		{
			name:      "plan 为 nil:不要求止盈主人",
			ownership: ProtectionOwnershipState{StopOwner: "drawdown"},
			plan:      nil,
			want:      true,
		},
		{
			name:      "缺止损 → 绝不允许标已验证",
			ownership: ProtectionOwnershipState{StopOwner: "drawdown", ProfitOwner: "ladder"},
			plan:      planWithTP,
			missingSL: true,
			want:      false,
		},
		{
			name:      "没有止损主人 → false",
			ownership: ProtectionOwnershipState{ProfitOwner: "ladder"},
			plan:      planWithTP,
			want:      false,
		},
		{
			name:            "仍有多余止损单 → 清理没做完,不算完整",
			ownership:       ProtectionOwnershipState{StopOwner: "drawdown", ProfitOwner: "ladder"},
			plan:            planWithTP,
			unexpectedStops: 1,
			want:            false,
		},
		{
			name:          "仍有多余止盈单 → 会多平仓,不算完整",
			ownership:     ProtectionOwnershipState{StopOwner: "drawdown", ProfitOwner: "ladder"},
			plan:          planWithTP,
			unexpectedTPs: 1,
			want:          false,
		},
		{
			// 2026-07-28 GPT SKHYNIXUSDT 的 58 轮 "missing profit owner":
			// 止损单被认领回来不代表止盈侧有主人,不能顺手标已验证。
			name:      "计划要求止盈主人但缺席 → 必须保持未验证",
			ownership: ProtectionOwnershipState{StopOwner: "drawdown", ProfitOwner: ""},
			plan:      planWithTP,
			want:      false,
		},
		{
			// NeedsTakeProfit + 单价格的老式计划形态也必须被识别为"要求止盈主人"。
			name:      "老式 NeedsTakeProfit 计划同样要求止盈主人",
			ownership: ProtectionOwnershipState{StopOwner: "drawdown", ProfitOwner: ""},
			plan:      &ProtectionPlan{NeedsTakeProfit: true, TakeProfitPrice: 100},
			want:      false,
		},
		{
			name:            "负数计数(不该出现)也按'不为 0'处理,宁可不标已验证",
			ownership:       ProtectionOwnershipState{StopOwner: "drawdown", ProfitOwner: "ladder"},
			plan:            planWithTP,
			unexpectedStops: -1,
			want:            false,
		},
		{
			name:      "零值 ownership → false",
			ownership: ProtectionOwnershipState{},
			plan:      planWithTP,
			want:      false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := protectionCoverageComplete(c.ownership, c.plan, c.missingSL, c.unexpectedStops, c.unexpectedTPs)
			if got != c.want {
				t.Fatalf("got %t want %t", got, c.want)
			}
		})
	}
}
