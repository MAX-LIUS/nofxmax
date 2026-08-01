package kernel

import (
	"strings"
	"testing"

	"nofx/market"
)

func TestFormatSessionRangeNil(t *testing.T) {
	if got := formatSessionRange(&market.Data{}, 100, true); got != "" {
		t.Fatalf("nil SessionRange should render empty, got %q", got)
	}
	// Degenerate values must also be suppressed.
	d := &market.Data{SessionRange: &market.SessionRange{Session: "EU"}}
	if got := formatSessionRange(d, 100, true); got != "" {
		t.Fatalf("zero OR should render empty, got %q", got)
	}
}

func TestFormatSessionRangeRenders(t *testing.T) {
	d := &market.Data{SessionRange: &market.SessionRange{
		Session:         "US",
		ORHigh:          106,
		ORLow:           104,
		ORWindowMinutes: 15,
		Complete:        true,
		DailyATR:        4,
		ExpansionPct:    50,
		ExpansionRatio:  4.9,
		ExpansionGrade:  "exhausted",
		Location:        "above_or",
	}}
	zh := formatSessionRange(d, 107, true)
	for _, want := range []string{"时段开盘区间", "US", "15", "50%", "4.9倍常态", "扩张耗尽", "开盘区间上方", "追已耗尽的波动"} {
		if !strings.Contains(zh, want) {
			t.Fatalf("zh output missing %q:\n%s", want, zh)
		}
	}
	if strings.Contains(zh, "developing") || strings.Contains(zh, "未完成") {
		t.Fatalf("complete window must not be marked developing:\n%s", zh)
	}
	en := formatSessionRange(d, 107, false)
	for _, want := range []string{"Session Opening Range", "US", "4.9x normal", "exhausted", "above the opening range", "spent volatility"} {
		if !strings.Contains(en, want) {
			t.Fatalf("en output missing %q:\n%s", want, en)
		}
	}
}

func TestFormatSessionRangeDevelopingFlag(t *testing.T) {
	d := &market.Data{SessionRange: &market.SessionRange{
		Session:         "ASIA",
		ORHigh:          2,
		ORLow:           1,
		ORWindowMinutes: 5,
		Complete:        false,
		Location:        "inside_or",
	}}
	zh := formatSessionRange(d, 1.5, true)
	if !strings.Contains(zh, "未完成") {
		t.Fatalf("developing window must be flagged:\n%s", zh)
	}
	// No ATR => no expansion line.
	if strings.Contains(zh, "扩张=") {
		t.Fatalf("expansion line should be omitted without ATR:\n%s", zh)
	}
	en := formatSessionRange(d, 1.5, false)
	if !strings.Contains(en, "developing") {
		t.Fatalf("en developing flag missing:\n%s", en)
	}
}

// The exhaustion note must appear only for the exhausted grade, otherwise it
// would nudge the model away from every continuation entry.
func TestFormatSessionRangeNoteOnlyWhenExhausted(t *testing.T) {
	for _, grade := range []string{"compressed", "normal", "expanded"} {
		d := &market.Data{SessionRange: &market.SessionRange{
			Session: "EU", ORHigh: 106, ORLow: 104, ORWindowMinutes: 15,
			Complete: true, DailyATR: 10, ExpansionPct: 20,
			ExpansionRatio: 1.0, ExpansionGrade: grade, Location: "inside_or",
		}}
		if zh := formatSessionRange(d, 105, true); strings.Contains(zh, "追已耗尽的波动") {
			t.Fatalf("grade %q must not carry the exhaustion note:\n%s", grade, zh)
		}
		if en := formatSessionRange(d, 105, false); strings.Contains(en, "spent volatility") {
			t.Fatalf("grade %q must not carry the exhaustion note (en):\n%s", grade, en)
		}
	}
}
