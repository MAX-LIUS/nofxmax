package trader

import (
	"testing"

	"nofx/store"
)

// TestSoftRegimeStructureFit_Default verifies the flag defaults to OFF so the
// regime_structure_mismatch check stays a hard gate unless explicitly enabled.
func TestSoftRegimeStructureFit_Default(t *testing.T) {
	gate := store.EntryGateConfig{}.WithDefaults()
	if gate.SoftRegimeStructureFit == nil {
		t.Fatal("SoftRegimeStructureFit should be defaulted, got nil")
	}
	if *gate.SoftRegimeStructureFit {
		t.Error("SoftRegimeStructureFit must default to false (opt-in)")
	}
}

// TestSoftRegimeStructureFit_ExplicitTrue verifies an explicit true survives
// WithDefaults (not overwritten).
func TestSoftRegimeStructureFit_ExplicitTrue(t *testing.T) {
	on := true
	gate := store.EntryGateConfig{SoftRegimeStructureFit: &on}.WithDefaults()
	if gate.SoftRegimeStructureFit == nil || !*gate.SoftRegimeStructureFit {
		t.Error("explicit true SoftRegimeStructureFit must be preserved")
	}
}

// TestRegimeMismatch_HardVsSoft verifies the enforcement/scoring semantics the
// flag toggles: hard mode blocks (Enforced=true fails the stage); soft mode
// does not block but deducts the registered penalty from the gate score.
func TestRegimeMismatch_HardVsSoft(t *testing.T) {
	// Hard mode: mismatch check enforced+failed -> firstEnforcedFailure blocks.
	hard := []EntryGateCheck{
		{Code: "regime_structure_mismatch", Passed: false, Enforced: true},
	}
	if blocked, code, _ := firstEnforcedFailure(hard); !blocked || code != "regime_structure_mismatch" {
		t.Errorf("hard mode should block on regime_structure_mismatch, got blocked=%t code=%s", blocked, code)
	}

	// Soft mode: same failure but Enforced=false -> does NOT block, deducts 20.
	soft := []EntryGateCheck{
		{Code: "regime_structure_mismatch", Passed: false, Enforced: false},
	}
	if blocked, _, _ := firstEnforcedFailure(soft); blocked {
		t.Error("soft mode must NOT hard-block on regime_structure_mismatch")
	}
	score := computeGateScore(soft)
	if score != 80 {
		t.Errorf("soft regime_structure_mismatch penalty should drop score 100->80, got %d", score)
	}
	// score 80 >= 75 -> full size retained for a high-conviction counter-structure
	// trade; the penalty only bites when stacked with other soft failures.
	if mult := scoreToSizeMultiplier(score); mult != 1.0 {
		t.Errorf("score 80 should map to full size 1.0, got %.2f", mult)
	}
}
