package kernel

import "testing"

func TestFixUnbalancedBraces(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "extra closing brace before array end (opus-4-8 bug)",
			in:   `[{"a":1,"b":{"c":2}}}]`,
			want: `[{"a":1,"b":{"c":2}}]`,
		},
		{
			name: "two extra closing braces",
			in:   `[{"a":{"b":{"c":1}}}}}]`,
			want: `[{"a":{"b":{"c":1}}}]`,
		},
		{
			name: "well-formed unchanged",
			in:   `[{"a":1,"b":{"c":2}}]`,
			want: `[{"a":1,"b":{"c":2}}]`,
		},
		{
			name: "brace inside string not counted",
			in:   `[{"a":"}}}","b":1}]`,
			want: `[{"a":"}}}","b":1}]`,
		},
		{
			name: "surplus brace with whitespace before bracket",
			in:   "[{\"a\":1}} ]",
			want: "[{\"a\":1} ]",
		},
		{
			name: "not an array unchanged",
			in:   `{"a":1}}`,
			want: `{"a":1}}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := fixUnbalancedBraces(c.in)
			if got != c.want {
				t.Errorf("fixUnbalancedBraces()\n in:   %s\n got:  %s\n want: %s", c.in, got, c.want)
			}
		})
	}
}

func TestExtractDecisionsRepairsOpus48Output(t *testing.T) {
	// Mirrors the real failing payload: trailing }}}] before </decision>
	resp := `<decision>
[{"symbol":"CLUSDT","action":"open_long","leverage":5,"stop_loss":95.45,"take_profit":98.49,"confidence":68,"entry_protection_rationale":{"risk_reward":{"entry":96.22,"passed":true},"key_levels":{"support":[96.11]}},"protection_plan":{"drawdown_rules":[{"timeframe":"4h","close_ratio_pct":100}]}}}]
</decision>`
	decisions, _, err := extractDecisions(resp)
	if err != nil {
		t.Fatalf("expected repair to succeed, got error: %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}
	if decisions[0].Symbol != "CLUSDT" || decisions[0].Action != "open_long" {
		t.Errorf("unexpected decision parsed: %+v", decisions[0])
	}
}
