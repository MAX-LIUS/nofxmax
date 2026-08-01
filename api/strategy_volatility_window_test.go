package api

import (
	"strings"
	"testing"

	"nofx/store"
)

func regimeConfig(rf store.RegimeFilterConfig) *store.StrategyConfig {
	cfg := &store.StrategyConfig{}
	cfg.Protection.RegimeFilter = rf
	return cfg
}

func hasWarningContaining(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

func TestValidateWarnsOnLowVolatilityWithoutThreshold(t *testing.T) {
	// Enabled with a 0 threshold: the gate skips itself at runtime, so without a
	// warning the operator sees a checked box that does nothing.
	w := validateStrategyConfig(regimeConfig(store.RegimeFilterConfig{
		Enabled:            true,
		BlockLowVolatility: true,
		MinATR14Pct:        0,
	}))
	if !hasWarningContaining(w, "min_atr14_pct must be > 0") {
		t.Fatalf("expected a min_atr14_pct warning, got %v", w)
	}
}

func TestValidateWarnsOnInvertedVolatilityWindow(t *testing.T) {
	// floor >= ceiling means no ATR can satisfy both — every entry is rejected.
	// Silently accepting this would look like a dead trader.
	w := validateStrategyConfig(regimeConfig(store.RegimeFilterConfig{
		Enabled:             true,
		BlockLowVolatility:  true,
		MinATR14Pct:         3.0,
		BlockHighVolatility: true,
		MaxATR14Pct:         1.0,
	}))
	if !hasWarningContaining(w, "must be < max_atr14_pct") {
		t.Fatalf("expected an inverted-window warning, got %v", w)
	}
}

func TestValidateWarnsWhenFloorEqualsCeiling(t *testing.T) {
	// Equality is also empty: nothing is both >= 1.0 and <= 1.0 except exactly
	// 1.0, which is not a usable window.
	w := validateStrategyConfig(regimeConfig(store.RegimeFilterConfig{
		Enabled:             true,
		BlockLowVolatility:  true,
		MinATR14Pct:         1.0,
		BlockHighVolatility: true,
		MaxATR14Pct:         1.0,
	}))
	if !hasWarningContaining(w, "must be < max_atr14_pct") {
		t.Fatalf("expected a warning when floor equals ceiling, got %v", w)
	}
}

func TestValidateAcceptsSaneVolatilityWindow(t *testing.T) {
	w := validateStrategyConfig(regimeConfig(store.RegimeFilterConfig{
		Enabled:             true,
		BlockLowVolatility:  true,
		MinATR14Pct:         0.3,
		BlockHighVolatility: true,
		MaxATR14Pct:         3.0,
	}))
	for _, bad := range []string{"min_atr14_pct", "max_atr14_pct"} {
		if hasWarningContaining(w, bad) {
			t.Fatalf("a 0.3–3.0 window should be accepted, got %v", w)
		}
	}
}

func TestValidateSilentWhenFloorDisabled(t *testing.T) {
	// A 0 threshold is fine while the flag is off — that is the default shipped
	// state and must not warn every operator who never touched the setting.
	w := validateStrategyConfig(regimeConfig(store.RegimeFilterConfig{
		Enabled:            true,
		BlockLowVolatility: false,
		MinATR14Pct:        0,
	}))
	if hasWarningContaining(w, "min_atr14_pct") {
		t.Fatalf("no warning expected while the floor is off, got %v", w)
	}
}

func TestValidateSilentWhenRegimeFilterDisabled(t *testing.T) {
	// The whole regime filter off: an inverted window is inert, so warning would
	// be noise.
	w := validateStrategyConfig(regimeConfig(store.RegimeFilterConfig{
		Enabled:             false,
		BlockLowVolatility:  true,
		MinATR14Pct:         5.0,
		BlockHighVolatility: true,
		MaxATR14Pct:         1.0,
	}))
	if hasWarningContaining(w, "atr14_pct") {
		t.Fatalf("no ATR warnings expected when regime_filter is disabled, got %v", w)
	}
}

func TestValidateWarnsOnFloorWithoutThresholdWhenRegimeFilterDisabled(t *testing.T) {
	// The configuration this warning exists for: parent switch off (as on 3 of the 4
	// live strategies), floor ticked, threshold left at 0. The gate skips itself at
	// runtime, so a silent validation here leaves the operator believing low-ATR
	// entries are being rejected when nothing is.
	w := validateStrategyConfig(regimeConfig(store.RegimeFilterConfig{
		Enabled:            false,
		BlockLowVolatility: true,
		MinATR14Pct:        0,
	}))
	if !hasWarningContaining(w, "min_atr14_pct must be > 0") {
		t.Fatalf("expected the floor warning with regime_filter disabled; got %v", w)
	}
}

func TestValidateSilentOnValidFloorWhenRegimeFilterDisabled(t *testing.T) {
	// A correctly configured standalone floor must not warn just because the parent
	// switch is off — that is now a supported combination, not a mistake.
	w := validateStrategyConfig(regimeConfig(store.RegimeFilterConfig{
		Enabled:            false,
		BlockLowVolatility: true,
		MinATR14Pct:        0.3,
	}))
	if hasWarningContaining(w, "min_atr14_pct") {
		t.Fatalf("expected no min_atr14_pct warning for a valid standalone floor; got %v", w)
	}
}
