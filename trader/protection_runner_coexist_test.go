package trader

import (
	"testing"

	"nofx/store"
)

func coexistCfg(ladderTP, ddOn bool) store.ProtectionConfig {
	p := store.ProtectionConfig{}
	p.LadderTPSL.Enabled = ladderTP
	p.LadderTPSL.Mode = store.ProtectionModeManual
	p.LadderTPSL.TakeProfitEnabled = ladderTP
	if ladderTP {
		p.LadderTPSL.Rules = []store.LadderTPSLRule{
			{TakeProfitPct: 3, TakeProfitCloseRatioPct: 40, StopLossPct: 5, StopLossCloseRatioPct: 100},
			{TakeProfitPct: 6, TakeProfitCloseRatioPct: 35},
		}
	}
	if ddOn {
		p.DrawdownTakeProfit.Enabled = true
		p.DrawdownTakeProfit.Rules = []store.DrawdownTakeProfitRule{
			{MinProfitPct: 6, MaxDrawdownPct: 40, CloseRatioPct: 100},
		}
	}
	return p
}

func TestLadderRunnerCoexistsWithDrawdown(t *testing.T) {
	if !ladderRunnerCoexistsWithDrawdown(coexistCfg(true, true)) {
		t.Fatal("expected coexistence when both ladder TP and DD enabled")
	}
	if ladderRunnerCoexistsWithDrawdown(coexistCfg(true, false)) {
		t.Fatal("expected no coexistence when DD disabled")
	}
	if ladderRunnerCoexistsWithDrawdown(coexistCfg(false, true)) {
		t.Fatal("expected no coexistence when ladder TP disabled")
	}
}

func TestLadderTakeProfitCloseRatioTotal(t *testing.T) {
	got := ladderTakeProfitCloseRatioTotal(coexistCfg(true, true).LadderTPSL)
	if got != 75 {
		t.Fatalf("expected cumulative TP close ratio 75, got %.1f", got)
	}
}

func TestLadderRunnerStageReached(t *testing.T) {
	// ladder closes 75%. threshold = 75*0.9 = 67.5% of entry must be closed.
	cases := []struct {
		name     string
		entry    float64
		current  float64
		want     bool
	}{
		{"full position, nothing closed", 100, 100, false},
		{"only TP1 filled (40% closed)", 100, 60, false},   // 40% < 67.5%
		{"TP1+TP2 filled (75% closed)", 100, 25, true},      // 75% >= 67.5%
		{"near-complete via tolerance (68% closed)", 100, 32, true}, // 68% >= 67.5%
		{"just below threshold (67% closed)", 100, 33, false},       // 67% < 67.5%
		{"runner fully gone", 100, 0, false},                // current<=0 guard
		{"position grew (avg-in)", 100, 120, false},
	}
	for _, c := range cases {
		got := ladderRunnerStageReached(c.entry, c.current, 75)
		if got != c.want {
			t.Errorf("%s: entry=%.0f cur=%.0f → got %v want %v", c.name, c.entry, c.current, got, c.want)
		}
	}
}
