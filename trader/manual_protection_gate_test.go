package trader

import (
	"testing"

	"nofx/store"
)

// TestUsesManualProtection locks in the 2026-06-05/06 fix: when ladder/full
// protection is MANUAL (and DD not AI), the AI protection_plan must NOT override
// the manual config — enforced both at open (applyPostOpenProtection) and on every
// reconcile (protection_reconciler). Tests the shared at.usesManualProtection().
func mkAT(prot store.ProtectionConfig) *AutoTrader {
	return &AutoTrader{config: AutoTraderConfig{StrategyConfig: &store.StrategyConfig{Protection: prot}}}
}

func TestUsesManualProtection_A1(t *testing.T) {
	at := mkAT(store.ProtectionConfig{
		LadderTPSL:         store.LadderTPSLConfig{Enabled: true, Mode: store.ProtectionModeManual},
		DrawdownTakeProfit: store.DrawdownTakeProfitConfig{Enabled: false},
		FullTPSL:           store.FullTPSLConfig{Enabled: false},
	})
	if !at.usesManualProtection() {
		t.Error("A1 (ladder manual, DD off) must be manual protection (ignore AI plan)")
	}
}

func TestUsesManualProtection_AIModeDefers(t *testing.T) {
	at := mkAT(store.ProtectionConfig{
		LadderTPSL:         store.LadderTPSLConfig{Enabled: true, Mode: store.ProtectionModeAI},
		DrawdownTakeProfit: store.DrawdownTakeProfitConfig{Enabled: true, Mode: store.ProtectionModeAI},
	})
	if at.usesManualProtection() {
		t.Error("AI-mode protection must NOT be forced manual")
	}
}

func TestUsesManualProtection_DDAIDefers(t *testing.T) {
	at := mkAT(store.ProtectionConfig{
		LadderTPSL:         store.LadderTPSLConfig{Enabled: true, Mode: store.ProtectionModeManual},
		DrawdownTakeProfit: store.DrawdownTakeProfitConfig{Enabled: true, Mode: store.ProtectionModeAI},
	})
	if at.usesManualProtection() {
		t.Error("DD in AI mode must defer to AI plan, not forced manual")
	}
}

func TestUsesManualProtection_NilConfig(t *testing.T) {
	at := &AutoTrader{}
	if at.usesManualProtection() {
		t.Error("nil config must not be treated as manual protection")
	}
}
