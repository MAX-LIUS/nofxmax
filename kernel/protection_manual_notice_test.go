package kernel

import (
	"strings"
	"testing"

	"nofx/store"
)

// TestManualProtectionLegsEmitManualNotice verifies that when ladder/drawdown are
// in manual mode, the system prompt tells the AI those legs are manually managed
// (and to omit them from protection_plan) while STILL requiring the top-level
// structural SL/TP + R opinion. Regression guard for the manual-mode display
// pollution bug (AI emitted a discarded half-plan because it was never told the
// strategy manages protection manually).
func TestManualProtectionLegsEmitManualNotice(t *testing.T) {
	cfg := &store.StrategyConfig{}
	cfg.Protection.LadderTPSL = store.LadderTPSLConfig{
		Enabled: true,
		Mode:    store.ProtectionModeManual,
		Rules: []store.LadderTPSLRule{
			{TakeProfitPct: 1.1, TakeProfitCloseRatioPct: 20},
			{TakeProfitPct: 1.7, StopLossPct: 4.5, StopLossCloseRatioPct: 100},
		},
	}
	cfg.Protection.DrawdownTakeProfit = store.DrawdownTakeProfitConfig{
		Enabled: true,
		Mode:    store.ProtectionModeManual,
		Rules: []store.DrawdownTakeProfitRule{
			{MinProfitPct: 1, MaxDrawdownPct: 60, CloseRatioPct: 65},
			{MinProfitPct: 2.4, MaxDrawdownPct: 55, CloseRatioPct: 80},
		},
	}

	engine := NewStrategyEngine(cfg)
	p := engine.BuildSystemPrompt(1000, "balanced")

	mustContain := []string{
		"Some protection legs are MANUALLY managed",
		"Ladder TP/SL: MANUAL",
		"Drawdown take-profit: MANUAL",
		"you MUST STILL provide",
	}
	for _, needle := range mustContain {
		if !strings.Contains(p, needle) {
			t.Fatalf("manual-mode prompt should contain %q", needle)
		}
	}
	// It must NOT emit the AI hard-requirement block for a manual drawdown leg.
	if strings.Contains(p, "Drawdown Take Profit is in AI mode") {
		t.Fatal("manual drawdown must not trigger the AI-mode requirement block")
	}
}

// TestAIProtectionLegsSkipManualNotice verifies the manual notice is NOT emitted
// when the legs are in AI mode (so the AI-mode requirement path stays intact).
func TestAIProtectionLegsSkipManualNotice(t *testing.T) {
	cfg := &store.StrategyConfig{}
	cfg.Protection.LadderTPSL = store.LadderTPSLConfig{Enabled: true, Mode: store.ProtectionModeAI}
	cfg.Protection.DrawdownTakeProfit = store.DrawdownTakeProfitConfig{
		Enabled: true, Mode: store.ProtectionModeAI,
		Rules: []store.DrawdownTakeProfitRule{{MinProfitPct: 1, MaxDrawdownPct: 60, CloseRatioPct: 65}},
	}
	engine := NewStrategyEngine(cfg)
	p := engine.BuildSystemPrompt(1000, "balanced")
	if strings.Contains(p, "Some protection legs are MANUALLY managed") {
		t.Fatal("AI-mode legs must not emit the manual notice")
	}
}
