package trader

import "testing"

// The builtin_regime_filter shadow rule must mirror the live gate's trend
// alignment: block a LONG in trending_down and a SHORT in trending_up, using the
// real classifyProtectionRegime label carried on the ctx.
func TestBuiltinRegimeShadowRule(t *testing.T) {
	var rule shadowRule
	for _, r := range shadowRules {
		if r.name == "builtin_regime_filter" {
			rule = r
			break
		}
	}
	if rule.name == "" {
		t.Fatal("builtin_regime_filter rule not registered")
	}
	cases := []struct {
		regime string
		side   string
		block  bool
	}{
		{"trending_down", "LONG", true},   // long into downtrend → block
		{"trending_up", "SHORT", true},    // short into uptrend → block
		{"trending_up", "LONG", false},    // long with uptrend → allow
		{"trending_down", "SHORT", false}, // short with downtrend → allow
		{"ranging", "LONG", false},        // no directional regime → allow
		{"", "SHORT", false},              // unknown regime → allow (fail-open)
	}
	for _, tc := range cases {
		ctx := shadowGateCtx{side: tc.side, builtinRegime: tc.regime}
		got, _, _ := rule.fn(ctx)
		if got != tc.block {
			t.Errorf("regime=%s side=%s: got block=%v want %v", tc.regime, tc.side, got, tc.block)
		}
	}
}
