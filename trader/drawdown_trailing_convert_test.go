package trader

import (
	"math"
	"testing"

	"nofx/store"
)

func approxEq(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestResolveDrawdownTrailing_PercentLong(t *testing.T) {
	// User's canonical example: peak-trigger 3%, drawdown distance 4%.
	rule := store.DrawdownTakeProfitRule{
		MinProfitPct:    3,
		MinProfitUnit:   store.ProtectionUnitPercent,
		MaxDrawdownPct:  4,
		MaxDrawdownUnit: store.ProtectionUnitPercent,
		CloseRatioPct:   100,
	}
	p := resolveDrawdownTrailingParams(10000, "long", rule, 0)
	if !p.Valid {
		t.Fatalf("expected valid params")
	}
	if !approxEq(p.ActivationPrice, 10300, 1e-6) {
		t.Fatalf("activation: got %.6f want 10300", p.ActivationPrice)
	}
	if !approxEq(p.CallbackRatio, 0.04, 1e-9) {
		t.Fatalf("callback: got %.9f want 0.04", p.CallbackRatio)
	}
	if p.ActivationImmediate {
		t.Fatalf("should not be immediate")
	}
}

func TestResolveDrawdownTrailing_PercentShort(t *testing.T) {
	rule := store.DrawdownTakeProfitRule{
		MinProfitPct: 3, MinProfitUnit: store.ProtectionUnitPercent,
		MaxDrawdownPct: 4, MaxDrawdownUnit: store.ProtectionUnitPercent, CloseRatioPct: 100,
	}
	p := resolveDrawdownTrailingParams(10000, "short", rule, 0)
	if !p.Valid || !approxEq(p.ActivationPrice, 9700, 1e-6) {
		t.Fatalf("short activation: got %.6f want 9700 (valid=%v)", p.ActivationPrice, p.Valid)
	}
	if !approxEq(p.CallbackRatio, 0.04, 1e-9) {
		t.Fatalf("short callback: got %.9f want 0.04", p.CallbackRatio)
	}
}

func TestResolveDrawdownTrailing_ATRLong(t *testing.T) {
	// peak 3 ATR, drawdown 1.2 ATR, atr=100, entry=10000
	rule := store.DrawdownTakeProfitRule{
		MinProfitPct: 3, MinProfitUnit: store.ProtectionUnitATR,
		MaxDrawdownPct: 1.2, MaxDrawdownUnit: store.ProtectionUnitATR, CloseRatioPct: 100,
	}
	p := resolveDrawdownTrailingParams(10000, "long", rule, 100)
	if !p.Valid {
		t.Fatalf("expected valid")
	}
	// activation = 10000 + 3*100 = 10300
	if !approxEq(p.ActivationPrice, 10300, 1e-6) {
		t.Fatalf("atr activation: got %.6f want 10300", p.ActivationPrice)
	}
	// callback = 1.2*100 / 10300 = 0.011650...
	if !approxEq(p.CallbackRatio, 120.0/10300.0, 1e-9) {
		t.Fatalf("atr callback: got %.9f want %.9f", p.CallbackRatio, 120.0/10300.0)
	}
}

func TestResolveDrawdownTrailing_ImmediateActivation(t *testing.T) {
	// No peak-trigger => immediate, callback anchored on entry.
	rule := store.DrawdownTakeProfitRule{
		MinProfitPct: 0,
		MaxDrawdownPct: 5, MaxDrawdownUnit: store.ProtectionUnitPercent, CloseRatioPct: 100,
	}
	p := resolveDrawdownTrailingParams(10000, "long", rule, 0)
	if !p.Valid || !p.ActivationImmediate {
		t.Fatalf("expected valid immediate (valid=%v imm=%v)", p.Valid, p.ActivationImmediate)
	}
	if p.ActivationPrice != 0 {
		t.Fatalf("immediate activation price must be 0, got %.6f", p.ActivationPrice)
	}
	if !approxEq(p.CallbackRatio, 0.05, 1e-9) {
		t.Fatalf("immediate callback: got %.9f want 0.05", p.CallbackRatio)
	}
}

func TestResolveDrawdownTrailing_ATRImmediate(t *testing.T) {
	// immediate + ATR drawdown anchors on entry price
	rule := store.DrawdownTakeProfitRule{
		MinProfitPct: 0,
		MaxDrawdownPct: 1.5, MaxDrawdownUnit: store.ProtectionUnitATR, CloseRatioPct: 100,
	}
	p := resolveDrawdownTrailingParams(10000, "long", rule, 100)
	if !p.Valid || !p.ActivationImmediate {
		t.Fatalf("expected valid immediate")
	}
	if !approxEq(p.CallbackRatio, 150.0/10000.0, 1e-9) {
		t.Fatalf("atr immediate callback: got %.9f want %.9f", p.CallbackRatio, 150.0/10000.0)
	}
}

func TestResolveDrawdownTrailing_Invalid(t *testing.T) {
	// ATR unit but no ATR provided => invalid
	rule := store.DrawdownTakeProfitRule{
		MinProfitPct: 3, MinProfitUnit: store.ProtectionUnitATR,
		MaxDrawdownPct: 1.2, MaxDrawdownUnit: store.ProtectionUnitATR, CloseRatioPct: 100,
	}
	if p := resolveDrawdownTrailingParams(10000, "long", rule, 0); p.Valid {
		t.Fatalf("expected invalid when ATR unit lacks ATR value")
	}
	// no drawdown distance => invalid
	rule2 := store.DrawdownTakeProfitRule{MinProfitPct: 3, MinProfitUnit: store.ProtectionUnitPercent, CloseRatioPct: 100}
	if p := resolveDrawdownTrailingParams(10000, "long", rule2, 0); p.Valid {
		t.Fatalf("expected invalid when no drawdown distance")
	}
}
