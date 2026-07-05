package store

import (
	"encoding/json"
	"math"
	"testing"
)

func TestATRProtection_EffectivePercent(t *testing.T) {
	c := ATRProtectionConfig{Enabled: true}
	// entry=100, ATR=2 → 1×ATR = 2% ; 2.5×ATR = 5%
	if pct, ok := c.EffectivePercent(1.0, 2.0, 100); !ok || math.Abs(pct-2.0) > 1e-9 {
		t.Fatalf("1xATR want 2%%, got %.4f ok=%v", pct, ok)
	}
	if pct, ok := c.EffectivePercent(2.5, 2.0, 100); !ok || math.Abs(pct-5.0) > 1e-9 {
		t.Fatalf("2.5xATR want 5%%, got %.4f ok=%v", pct, ok)
	}
}

func TestATRProtection_Bounds(t *testing.T) {
	c := ATRProtectionConfig{Enabled: true, MinEffPct: 0.5, MaxEffPct: 10}
	// Tiny ATR → floored to MinEffPct
	if pct, _ := c.EffectivePercent(1.0, 0.01, 100); pct != 0.5 {
		t.Fatalf("floor want 0.5, got %.4f", pct)
	}
	// Huge ATR → capped at MaxEffPct
	if pct, _ := c.EffectivePercent(1.0, 50, 100); pct != 10 {
		t.Fatalf("cap want 10, got %.4f", pct)
	}
}

func TestATRProtection_DisabledDimensionReturnsNotOK(t *testing.T) {
	c := ATRProtectionConfig{Enabled: true}
	// multiple<=0 means "stay on configured percent" → ok=false
	if _, ok := c.EffectivePercent(0, 2.0, 100); ok {
		t.Fatalf("zero multiple must return ok=false")
	}
}

// Adding the ATRProtection field must not break existing strategy JSON.
func TestStrategyConfig_BackwardCompatJSON(t *testing.T) {
	legacy := `{"risk_control":{},"protection":{"full_tp_sl":{"enabled":true}}}`
	var cfg StrategyConfig
	if err := json.Unmarshal([]byte(legacy), &cfg); err != nil {
		t.Fatalf("legacy config must still parse: %v", err)
	}
	if cfg.ATRProtection.Enabled {
		t.Fatalf("ATRProtection must default to disabled (no-op)")
	}
}

func TestStructuralSLConfig_WithDefaults(t *testing.T) {
	got := StructuralSLConfig{}.WithDefaults()
	if got.FloorATRMul != 1.5 {
		t.Fatalf("floor default want 1.5, got %.3f", got.FloorATRMul)
	}
	if got.BackstopATRMul != 4.5 {
		t.Fatalf("backstop default want 4.5, got %.3f", got.BackstopATRMul)
	}
	if got.LookbackBars != 24 {
		t.Fatalf("lookback default want 24, got %d", got.LookbackBars)
	}
	// Explicit values are preserved.
	custom := StructuralSLConfig{FloorATRMul: 2.0, BackstopATRMul: 5.0, LookbackBars: 12}.WithDefaults()
	if custom.FloorATRMul != 2.0 || custom.BackstopATRMul != 5.0 || custom.LookbackBars != 12 {
		t.Fatalf("explicit values not preserved: %+v", custom)
	}
}

// Structural SL config must round-trip and default to disabled (no-op) for legacy JSON.
func TestStructuralSL_JSONRoundTripAndDefault(t *testing.T) {
	legacy := `{"protection":{"ladder_tp_sl":{"enabled":true}}}`
	var cfg StrategyConfig
	if err := json.Unmarshal([]byte(legacy), &cfg); err != nil {
		t.Fatalf("legacy ladder config must parse: %v", err)
	}
	if cfg.Protection.LadderTPSL.StructuralSL.Enabled {
		t.Fatalf("StructuralSL must default disabled")
	}
	// Round-trip with structural enabled + a structural SL rule.
	cfg.Protection.LadderTPSL.StructuralSL = StructuralSLConfig{Enabled: true, FloorATRMul: 1.5, BackstopATRMul: 4.5, LookbackBars: 24, CloseConfirm: true}
	cfg.Protection.LadderTPSL.Rules = []LadderTPSLRule{{StopLossPct: 4.5, StopLossUnit: ProtectionUnitStructural, StopLossCloseRatioPct: 100}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back StrategyConfig
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ss := back.Protection.LadderTPSL.StructuralSL
	if !ss.Enabled || !ss.CloseConfirm || ss.FloorATRMul != 1.5 || ss.BackstopATRMul != 4.5 {
		t.Fatalf("structural SL not preserved: %+v", ss)
	}
	if back.Protection.LadderTPSL.Rules[0].StopLossUnit != ProtectionUnitStructural {
		t.Fatalf("structural SL unit not preserved")
	}
}
