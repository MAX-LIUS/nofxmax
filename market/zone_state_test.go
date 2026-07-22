package market

import "testing"

// zbar is a small kline helper for zone-state tests.
func zbar(o, h, l, c float64) Kline {
	return Kline{Open: o, High: h, Low: l, Close: c, Volume: 100}
}

// TestAnnotate_Fresh: a support zone below all price action, never touched.
func TestAnnotate_Fresh(t *testing.T) {
	zones := []StructuralZone{
		{Low: 90, High: 91, MidPrice: 90.5, Type: "support", ATRWidth: 0.5},
	}
	var ks []Kline
	for i := 0; i < 30; i++ {
		ks = append(ks, zbar(100, 101, 99, 100)) // never reaches 91
	}
	out := AnnotateZoneLifecycle(zones, ks, 1.0, 100)
	if out[0].State != ZoneStateFresh {
		t.Errorf("expected fresh, got %s", out[0].State)
	}
	if out[0].TestCount != 0 {
		t.Errorf("expected 0 tests, got %d", out[0].TestCount)
	}
}

// TestAnnotate_Reacted: one test that produces a strong bounce.
func TestAnnotate_Reacted(t *testing.T) {
	zones := []StructuralZone{
		{Low: 98, High: 99, MidPrice: 98.5, Type: "support", ATRWidth: 0.5},
	}
	var ks []Kline
	for i := 0; i < 10; i++ {
		ks = append(ks, zbar(105, 106, 104, 105))
	}
	// dip into the zone once
	ks = append(ks, zbar(100, 100, 98.5, 99))
	// strong bounce up (>1.5 ATR with atr=1)
	for i := 0; i < 8; i++ {
		ks = append(ks, zbar(100+float64(i), 101.5+float64(i), 100+float64(i), 101+float64(i)))
	}
	out := AnnotateZoneLifecycle(zones, ks, 1.0, 108)
	if out[0].State != ZoneStateReacted {
		t.Errorf("expected reacted, got %s (maxReact=%.2f)", out[0].State, out[0].MaxReactionATR)
	}
	if out[0].Role != ZoneRoleReversal {
		t.Errorf("expected reversal role, got %s", out[0].Role)
	}
}

// TestAnnotate_Invalid: a support tested long ago, price now far away and never
// returned -> stale/invalid so the AI down-weights it.
func TestAnnotate_Invalid(t *testing.T) {
	zones := []StructuralZone{
		{Low: 98, High: 99, MidPrice: 98.5, Type: "support", ATRWidth: 0.5},
	}
	var ks []Kline
	// one early test of the zone
	ks = append(ks, zbar(100, 100, 98.5, 99))
	// then price runs far above and stays there for many bars (> staleBarsInvalid)
	for i := 0; i < 50; i++ {
		p := 105.0 + float64(i)
		ks = append(ks, zbar(p, p+1, p-1, p))
	}
	last := ks[len(ks)-1].Close
	out := AnnotateZoneLifecycle(zones, ks, 1.0, last)
	if out[0].State != ZoneStateInvalid {
		t.Errorf("expected invalid, got %s (lastPrice=%.0f)", out[0].State, last)
	}
}

// TestAnnotate_Flipped: z.Flipped set by ApplyFlipLogic upstream -> flipped
// state regardless of test history (flip status is authoritative).
func TestAnnotate_Flipped(t *testing.T) {
	zones := []StructuralZone{
		{Low: 98, High: 99, MidPrice: 98.5, Type: "support", ATRWidth: 0.5, Flipped: true, FlipCount: 1},
	}
	var ks []Kline
	for i := 0; i < 20; i++ {
		ks = append(ks, zbar(101, 102, 100, 101))
	}
	out := AnnotateZoneLifecycle(zones, ks, 1.0, 101)
	if out[0].State != ZoneStateFlipped {
		t.Errorf("expected flipped, got %s", out[0].State)
	}
	if out[0].Role != ZoneRoleContinuation {
		t.Errorf("expected continuation role for flipped, got %s", out[0].Role)
	}
}

func TestAnnotate_Adversary(t *testing.T) {
	zones := []StructuralZone{{Low: 98, High: 99, MidPrice: 98.5, Type: "support"}}
	// zero atr -> unchanged (no state set)
	out := AnnotateZoneLifecycle(zones, make([]Kline, 30), 0, 100)
	if out[0].State != "" {
		t.Errorf("zero atr should leave state empty, got %s", out[0].State)
	}
	// too few klines
	out = AnnotateZoneLifecycle(zones, make([]Kline, 3), 1.0, 100)
	if out[0].State != "" {
		t.Errorf("too-few klines should leave state empty, got %s", out[0].State)
	}
}
