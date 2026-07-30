package store

import (
	"encoding/json"
	"testing"
)

// The live bug this file guards: TrailMinProfitATR was a plain float64 with
// `omitempty`, so "unset" and "explicit 0" were indistinguishable. Every live
// strategy left it unset, JSON carried no key, and WithDefaults read it back as 0 —
// the min-profit gate was OFF in production while the doc comment and the UI
// (`?? 1.0`) both claimed 1.0. Real consequence 2026-07-29: an ETHUSDT LONG opened
// at 00:23 ratcheted at 00:24 with the position not in profit at all.
//
// These tests assert the DEFAULTS, not the arithmetic — defaults were the defect.
func TestStructuralSLTrailDefaults_UnsetGatesRatchetToProfitOnly(t *testing.T) {
	// A config as it arrives from a strategy that never touched the trail fields.
	var c StructuralSLConfig
	if err := json.Unmarshal([]byte(`{"enabled":true,"close_confirm":true,"trail_enabled":true}`), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.TrailMinProfitATR != nil {
		t.Fatalf("pre-normalization the field must stay nil to remain distinguishable from 0, got %v", *c.TrailMinProfitATR)
	}
	d := c.WithDefaults()

	if got := d.TrailMinProfitATRValue(); got != 1.0 {
		t.Errorf("unset min-profit gate must default to 1.0 ATR, got %v — this is the exact regression that let a just-opened position ratchet", got)
	}
	if d.TrailRatchetOnLoss() {
		t.Error("unset trail_on_loss must default to FALSE: a ratchet fired while underwater cannot lock profit, it only drags the invalidation level into the noise band")
	}
	if !d.TrailRatchetOnProfit() {
		t.Error("unset trail_on_profit must default to TRUE: locking profit is the whole point of the ratchet")
	}
}

// Reading without WithDefaults must not silently fall back to the old 0 = off.
func TestStructuralSLTrailDefaults_AccessorsAreNilSafe(t *testing.T) {
	var zero StructuralSLConfig
	if got := zero.TrailMinProfitATRValue(); got != 1.0 {
		t.Errorf("nil-safe accessor must yield 1.0 on a zero-value config, got %v", got)
	}
	if zero.TrailRatchetOnLoss() {
		t.Error("nil-safe accessor must yield false for the in-loss gate on a zero-value config")
	}
}

// An explicit 0 still means "no gate" — the UI exposes 0 as a valid choice
// (min="0"), so the pointer must preserve it through a JSON round trip rather than
// letting WithDefaults overwrite it with 1.0.
func TestStructuralSLTrailDefaults_ExplicitZeroSurvivesNormalization(t *testing.T) {
	var c StructuralSLConfig
	if err := json.Unmarshal([]byte(`{"trail_enabled":true,"trail_min_profit_atr":0}`), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.TrailMinProfitATR == nil {
		t.Fatal("an explicit 0 must unmarshal to a non-nil pointer, otherwise it is indistinguishable from unset")
	}
	if got := c.WithDefaults().TrailMinProfitATRValue(); got != 0 {
		t.Errorf("explicit 0 must stay 0 (gate disabled), got %v", got)
	}
}

// Explicit opt-in must still work: an operator who deliberately wants the in-loss
// ratchet gets it, so the new default is a default and not a hard removal.
func TestStructuralSLTrailDefaults_ExplicitOptInStillHonoured(t *testing.T) {
	var c StructuralSLConfig
	if err := json.Unmarshal([]byte(`{"trail_enabled":true,"trail_on_loss":true,"trail_min_profit_atr":2.5}`), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	d := c.WithDefaults()
	if !d.TrailRatchetOnLoss() {
		t.Error("explicit trail_on_loss=true must be honoured")
	}
	if got := d.TrailMinProfitATRValue(); got != 2.5 {
		t.Errorf("explicit min-profit must be honoured, got %v", got)
	}
}

// Negatives are nonsense input; clamp to 0 rather than inverting the comparison.
func TestStructuralSLTrailDefaults_NegativeClampsToZero(t *testing.T) {
	neg := -3.0
	d := StructuralSLConfig{TrailEnabled: true, TrailMinProfitATR: &neg}.WithDefaults()
	if got := d.TrailMinProfitATRValue(); got != 0 {
		t.Errorf("negative min-profit must clamp to 0, got %v", got)
	}
}

// WithDefaults must be idempotent: read sites call it on every poll, and a
// non-idempotent normalization would let a value drift over time.
func TestStructuralSLTrailDefaults_WithDefaultsIsIdempotent(t *testing.T) {
	var c StructuralSLConfig
	once := c.WithDefaults()
	twice := once.WithDefaults()
	if once.TrailMinProfitATRValue() != twice.TrailMinProfitATRValue() {
		t.Errorf("min-profit not idempotent: %v then %v", once.TrailMinProfitATRValue(), twice.TrailMinProfitATRValue())
	}
	if once.TrailRatchetOnLoss() != twice.TrailRatchetOnLoss() {
		t.Error("in-loss gate not idempotent across repeated normalization")
	}
}
