package store

import (
	"reflect"
	"testing"
)

func TestDeriveTimeframes(t *testing.T) {
	cases := []struct {
		primary      string
		wantLower    string
		wantLonger   string
		wantSelected []string
	}{
		{"1m", "3m", "5m", []string{"1m", "3m", "5m"}},   // floor: lower uses step-up
		{"5m", "3m", "15m", []string{"5m", "3m", "15m"}}, // 4x5m=20 → nearest 15m
		{"15m", "5m", "1h", []string{"15m", "5m", "1h"}}, // 4x15m=60 → 1h
		{"1h", "30m", "4h", []string{"1h", "30m", "4h"}}, // 4x60=240 → 4h
		{"4h", "2h", "12h", []string{"4h", "2h", "12h"}}, // 4x240=960 → nearest 12h(720)
	}
	for _, tc := range cases {
		d := DeriveTimeframes(tc.primary)
		if d.LowerAdjacent != tc.wantLower {
			t.Errorf("%s lower: got %q want %q", tc.primary, d.LowerAdjacent, tc.wantLower)
		}
		if d.Longer != tc.wantLonger {
			t.Errorf("%s longer: got %q want %q", tc.primary, d.Longer, tc.wantLonger)
		}
		if !reflect.DeepEqual(d.Selected, tc.wantSelected) {
			t.Errorf("%s selected: got %v want %v", tc.primary, d.Selected, tc.wantSelected)
		}
	}
}

func TestDeriveTimeframesUnknownPrimary(t *testing.T) {
	d := DeriveTimeframes("7m")
	if len(d.Selected) != 1 || d.Selected[0] != "7m" {
		t.Errorf("unknown primary should return itself alone, got %v", d.Selected)
	}
}

func newAlignCfg(primary, discipline string) *StrategyConfig {
	c := &StrategyConfig{TimeframeDiscipline: discipline}
	c.Indicators.Klines.PrimaryTimeframe = primary
	// deliberately stale/mismatched values that alignment must correct:
	c.ATRProtection.Timeframe = "1h"
	c.BreakoutEntry.Timeframe = "1h"
	c.Protection.GivebackGuard.BreadthFromPeakTimeframe = "4h"
	return c
}

// Protection anchors (ATR + breakout) MUST always be forced to primary — the
// real timeframe-discipline bug that misconfigured Claude-R. A user-set breadth
// value is preserved (not clobbered); only an empty one gets derived.
func TestAlignToPrimary_ForcesProtectionAnchors(t *testing.T) {
	c := newAlignCfg("15m", TimeframeDisciplineFollowPrimary)
	c.AlignToPrimaryTimeframe()

	if c.ATRProtection.Timeframe != "15m" {
		t.Errorf("ATR timeframe not aligned: got %q want 15m", c.ATRProtection.Timeframe)
	}
	if c.BreakoutEntry.Timeframe != "15m" {
		t.Errorf("breakout timeframe not aligned: got %q want 15m", c.BreakoutEntry.Timeframe)
	}
	// breadth was explicitly "4h" in newAlignCfg → preserved, not overridden.
	if c.Protection.GivebackGuard.BreadthFromPeakTimeframe != "4h" {
		t.Errorf("breadth should be preserved: got %q want 4h", c.Protection.GivebackGuard.BreadthFromPeakTimeframe)
	}
}

// Empty or primary-missing selected set → fully derived; empty longer/breadth filled.
func TestAlignToPrimary_DerivesWhenMissing(t *testing.T) {
	c := &StrategyConfig{TimeframeDiscipline: TimeframeDisciplineFollowPrimary}
	c.Indicators.Klines.PrimaryTimeframe = "15m"
	c.AlignToPrimaryTimeframe()
	wantSel := []string{"15m", "5m", "1h"}
	if !reflect.DeepEqual(c.Indicators.Klines.SelectedTimeframes, wantSel) {
		t.Errorf("derived selected: got %v want %v", c.Indicators.Klines.SelectedTimeframes, wantSel)
	}
	if c.Indicators.Klines.LongerTimeframe != "1h" {
		t.Errorf("derived longer: got %q want 1h", c.Indicators.Klines.LongerTimeframe)
	}
	if c.Protection.GivebackGuard.BreadthFromPeakTimeframe != "1h" {
		t.Errorf("derived breadth: got %q want 1h", c.Protection.GivebackGuard.BreadthFromPeakTimeframe)
	}
}

// A user-chosen selected set already containing primary is preserved (primary
// moved first); a deliberate longer value is kept.
func TestAlignToPrimary_PreservesValidContext(t *testing.T) {
	c := newAlignCfg("1h", TimeframeDisciplineFollowPrimary)
	c.Indicators.Klines.SelectedTimeframes = []string{"1h", "15m", "4h"}
	c.Indicators.Klines.LongerTimeframe = "4h"
	c.AlignToPrimaryTimeframe()
	wantSel := []string{"1h", "15m", "4h"}
	if !reflect.DeepEqual(c.Indicators.Klines.SelectedTimeframes, wantSel) {
		t.Errorf("selected should be preserved: got %v want %v", c.Indicators.Klines.SelectedTimeframes, wantSel)
	}
	if c.Indicators.Klines.LongerTimeframe != "4h" {
		t.Errorf("longer should be preserved: got %q want 4h", c.Indicators.Klines.LongerTimeframe)
	}
}

// Primary present but not first → moved to front, rest order preserved.
func TestAlignToPrimary_ReordersPrimaryFirst(t *testing.T) {
	c := newAlignCfg("15m", TimeframeDisciplineFollowPrimary)
	c.Indicators.Klines.SelectedTimeframes = []string{"5m", "15m", "1h"}
	c.AlignToPrimaryTimeframe()
	wantSel := []string{"15m", "5m", "1h"}
	if !reflect.DeepEqual(c.Indicators.Klines.SelectedTimeframes, wantSel) {
		t.Errorf("selected: got %v want %v", c.Indicators.Klines.SelectedTimeframes, wantSel)
	}
}

func TestAlignToPrimary_Legacy_NoOp(t *testing.T) {
	c := newAlignCfg("15m", "") // discipline OFF
	c.AlignToPrimaryTimeframe()
	if c.ATRProtection.Timeframe != "1h" {
		t.Errorf("legacy config must be untouched, ATR tf changed to %q", c.ATRProtection.Timeframe)
	}
	if c.BreakoutEntry.Timeframe != "1h" {
		t.Errorf("legacy config must be untouched, breakout tf changed to %q", c.BreakoutEntry.Timeframe)
	}
}

func TestAlignToPrimary_BarCountHolds(t *testing.T) {
	c := newAlignCfg("15m", TimeframeDisciplineFollowPrimary)
	c.RiskControl.TimeStopBars = 96 // 96 * 15m = 24h
	c.RiskControl.MaxHoldBars = 72  // 72 * 15m = 18h
	c.AlignToPrimaryTimeframe()
	if c.RiskControl.TimeStopHours != 24 {
		t.Errorf("time_stop bars→hours: got %v want 24", c.RiskControl.TimeStopHours)
	}
	if c.RiskControl.MaxHoldHours != 18 {
		t.Errorf("max_hold bars→hours: got %v want 18", c.RiskControl.MaxHoldHours)
	}

	// Same bar counts on 1m primary must yield proportionally smaller hours.
	c2 := newAlignCfg("1m", TimeframeDisciplineFollowPrimary)
	c2.RiskControl.TimeStopBars = 96
	c2.RiskControl.MaxHoldBars = 72
	c2.AlignToPrimaryTimeframe()
	if c2.RiskControl.TimeStopHours != 1.6 { // 96 min
		t.Errorf("1m time_stop: got %v want 1.6", c2.RiskControl.TimeStopHours)
	}
	if c2.RiskControl.MaxHoldHours != 1.2 { // 72 min
		t.Errorf("1m max_hold: got %v want 1.2", c2.RiskControl.MaxHoldHours)
	}
}

func TestAlignToPrimary_EmptyPrimary_NoOp(t *testing.T) {
	c := newAlignCfg("", TimeframeDisciplineFollowPrimary)
	c.AlignToPrimaryTimeframe()
	if c.ATRProtection.Timeframe != "1h" {
		t.Errorf("empty primary must be no-op, got %q", c.ATRProtection.Timeframe)
	}
}
