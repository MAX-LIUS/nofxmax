package trader

import (
	"testing"

	"nofx/store"
)

// computeExchangeLightReason 的每个返回值都必须对应 computeExchangeLight 真的会给出
// yellow 的那个状态。这组测试把两个函数**成对**驱动:先让 computeExchangeLight 产出
// yellow,再把同一批入参喂给 reason,断言 reason 解释的正是那个 yellow 的成因。
// 这样"reason 分支永远进不去"或"reason 把 A 状态解释成 B"都会变成测试失败。
func TestExchangeLightReasonPairsWithLight(t *testing.T) {
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 3, MaxDrawdownPct: 30}

	cases := []struct {
		name             string
		side             string
		markPrice        float64
		currentPnLPct    float64
		drawdownPct      float64
		matchedLive      bool
		activationStatus string
		activationPrice  float64
		wantLight        string
		wantReason       string
	}{
		{
			// 在场单被标为 activated 却没有激活价,且当前利润在门槛下 —— 危险的
			// 立即激活单,任意回撤都会误平。
			name:             "activated without activation price below floor is no_activation",
			side:             "long",
			markPrice:        100,
			currentPnLPct:    1,
			matchedLive:      true,
			activationStatus: "activated",
			activationPrice:  0,
			wantLight:        "yellow",
			wantReason:       "no_activation",
		},
		{
			// 在场休眠单自身没有激活锚点 —— 同样归为 no_activation。
			name:            "resting order missing its own activation price is no_activation",
			side:            "long",
			markPrice:       100,
			currentPnLPct:   1,
			matchedLive:     true,
			activationPrice: 0,
			wantLight:       "yellow",
			wantReason:      "no_activation",
		},
		{
			// 激活价已被行情越过而交易所始终没激活 —— 幻影单。
			name:            "passed activation but never activated is phantom",
			side:            "long",
			markPrice:       120,
			currentPnLPct:   1,
			matchedLive:     true,
			activationPrice: 110,
			wantLight:       "yellow",
			wantReason:      "phantom",
		},
		{
			// 标记价不可用:computeExchangeLight 明确说"无法评估可达性,不敢报 green"。
			// 这**不是**幻影 —— 幻影是"确认交易所跳过了激活"这个正向发现。
			// 修复前这条会落到 phantom,把"判不了"讲成"这张单已死"。
			name:            "unusable mark price is unknown_mark not phantom",
			side:            "long",
			markPrice:       0,
			currentPnLPct:   1,
			matchedLive:     true,
			activationPrice: 110,
			wantLight:       "yellow",
			wantReason:      "unknown_mark",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			light := computeExchangeLight(
				c.side, c.markPrice, c.currentPnLPct, c.drawdownPct, rule,
				c.matchedLive, c.activationStatus, c.activationPrice,
				0, true, "native_trailing_tiers",
			)
			if light != c.wantLight {
				t.Fatalf("light = %q, want %q (reason 的前提不成立,这条用例已不再测它想测的东西)", light, c.wantLight)
			}
			reason := computeExchangeLightReason(
				c.side, c.markPrice, light, "native_trailing_tiers",
				c.matchedLive, c.activationStatus, c.activationPrice,
			)
			if reason != c.wantReason {
				t.Fatalf("reason = %q, want %q", reason, c.wantReason)
			}
		})
	}
}

// 缺单必须是 RED 而不是 yellow —— 所以 reason 里那条 !matchedLive 分支在真实调用
// 链上不可达。这条测试把这个不变量钉住:如果哪天 computeExchangeLight 把缺单改成
// yellow,这里会失败,提醒同时给面板补一个能渲染的成因。
func TestMissingOrderIsRedNotYellow(t *testing.T) {
	rule := store.DrawdownTakeProfitRule{MinProfitPct: 3, MaxDrawdownPct: 30}
	light := computeExchangeLight(
		"long", 100, 1, 0, rule,
		false, "", 0, 105, true, "native_trailing_tiers",
	)
	if light != "red" {
		t.Fatalf("light = %q, want red:开仓即挂之下缺单是真缺口,不能是软 yellow", light)
	}
	// 非 yellow 时 reason 必须是空串,面板不该收到成因。
	if reason := computeExchangeLightReason("long", 100, light, "native_trailing_tiers", false, "", 0); reason != "" {
		t.Fatalf("reason = %q, want empty for non-yellow light", reason)
	}
}

// 只有 yellow 才有成因;green/blue 都必须返回空。
func TestNonYellowLightsCarryNoReason(t *testing.T) {
	for _, light := range []string{"green", "blue", "red"} {
		if reason := computeExchangeLightReason("long", 100, light, "native_trailing_tiers", true, "activated", 95); reason != "" {
			t.Fatalf("light=%s reason = %q, want empty", light, reason)
		}
	}
}
