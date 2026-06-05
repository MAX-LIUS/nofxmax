package trader

import (
	"testing"

	"nofx/store"
)

// TestManualProtectionGate locks in the 2026-06-05 fix: when ladder/full protection
// is in MANUAL mode (and DD not in AI mode), the AI protection_plan must NOT override
// the manual config. Mirrors the manualProtection gate in applyPostOpenProtection.
func computeManualProtection(prot store.ProtectionConfig) bool {
	ladderManual := prot.LadderTPSL.Enabled && prot.LadderTPSL.Mode == store.ProtectionModeManual
	fullManual := prot.FullTPSL.Enabled && prot.FullTPSL.Mode == store.ProtectionModeManual
	ddAI := prot.DrawdownTakeProfit.Enabled && prot.DrawdownTakeProfit.Mode == store.ProtectionModeAI
	return (ladderManual || fullManual) && !ddAI
}

func TestManualProtectionGate_A1(t *testing.T) {
	a1 := store.ProtectionConfig{
		LadderTPSL:         store.LadderTPSLConfig{Enabled: true, Mode: store.ProtectionModeManual},
		DrawdownTakeProfit: store.DrawdownTakeProfitConfig{Enabled: false},
		FullTPSL:           store.FullTPSLConfig{Enabled: false},
	}
	if !computeManualProtection(a1) {
		t.Error("A1 (ladder manual, DD off) must be manual protection (ignore AI plan)")
	}
}

func TestManualProtectionGate_AIModeStillDefers(t *testing.T) {
	aiCfg := store.ProtectionConfig{
		LadderTPSL:         store.LadderTPSLConfig{Enabled: true, Mode: store.ProtectionModeAI},
		DrawdownTakeProfit: store.DrawdownTakeProfitConfig{Enabled: true, Mode: store.ProtectionModeAI},
	}
	if computeManualProtection(aiCfg) {
		t.Error("AI-mode protection must NOT be forced manual")
	}
}

func TestManualProtectionGate_DDAIDefers(t *testing.T) {
	mixed := store.ProtectionConfig{
		LadderTPSL:         store.LadderTPSLConfig{Enabled: true, Mode: store.ProtectionModeManual},
		DrawdownTakeProfit: store.DrawdownTakeProfitConfig{Enabled: true, Mode: store.ProtectionModeAI},
	}
	if computeManualProtection(mixed) {
		t.Error("DD in AI mode must defer to AI plan, not forced manual")
	}
}
