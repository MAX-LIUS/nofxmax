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

// TestAnnotate_Broken: support decisively broken and price far away.
func TestAnnotate_Broken(t *testing.T) {
	zones := []StructuralZone{
		{Low: 98, High: 99, MidPrice: 98.5, Type: "support", ATRWidth: 0.5},
	}
	var ks []Kline
	for i := 0; i < 5; i++ {
		ks = append(ks, zbar(100, 101, 99.5, 100))
	}
	// break below and stay below (2 consecutive closes < 98)
	ks = append(ks, zbar(99, 99, 96, 97))
	ks = append(ks, zbar(97, 97.5, 95, 96))
	for i := 0; i < 5; i++ {
		ks = append(ks, zbar(96, 96.5, 95, 95.5))
	}
	out := AnnotateZoneLifecycle(zones, ks, 1.0, 95.5)
	if out[0].State != ZoneStateBroken {
		t.Errorf("expected broken, got %s", out[0].State)
	}
}

// TestAnnotate_Flipped: support broken then reclaimed -> flipped.
func TestAnnotate_Flipped(t *testing.T) {
	zones := []StructuralZone{
		{Low: 98, High: 99, MidPrice: 98.5, Type: "support", ATRWidth: 0.5},
	}
	var ks []Kline
	for i := 0; i < 5; i++ {
		ks = append(ks, zbar(100, 101, 99.5, 100))
	}
	// break below
	ks = append(ks, zbar(99, 99, 96, 97))
	ks = append(ks, zbar(97, 97.5, 95, 96))
	// reclaim above zone high 99
	ks = append(ks, zbar(97, 100, 97, 100))
	for i := 0; i < 5; i++ {
		ks = append(ks, zbar(101, 102, 100, 101))
	}
	out := AnnotateZoneLifecycle(zones, ks, 1.0, 101)
	if out[0].State != ZoneStateFlipped {
		t.Errorf("expected flipped, got %s", out[0].State)
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
