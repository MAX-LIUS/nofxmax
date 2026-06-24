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
