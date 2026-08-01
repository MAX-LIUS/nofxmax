package trader

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

// TestChartTrendGateEquivalence verifies the Go chart_trend gate reproduces the
// validated Python backtest decisions bit-for-bit on the BN sample. Cases exported
// from /tmp/okx_analysis/gate_cases.json (skipped if absent).
func TestChartTrendGateEquivalence(t *testing.T) {
	raw, err := os.ReadFile("/tmp/okx_analysis/gate_cases.json")
	if err != nil {
		t.Skip("gate_cases.json not present; skipping equivalence check")
	}
	var cases []struct {
		Sym        string    `json:"sym"`
		Side       string    `json:"side"`
		Closes     []float64 `json:"closes"`
		Highs      []float64 `json:"highs"`
		Lows       []float64 `json:"lows"`
		PyAlign    float64   `json:"py_align"`
		PyR2       float64   `json:"py_r2"`
		PyDirSlope float64   `json:"py_dirslope"`
		PyAllow    bool      `json:"py_allow"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("parse cases: %v", err)
	}
	mism, allowMism := 0, 0
	for _, c := range cases {
		ctx := shadowGateCtx{highs: c.Highs, lows: c.Lows, closes: c.Closes, side: c.Side}
		slopePct, r2 := chartRegChannel(c.Closes, 30)
		dirSlope := slopePct
		if c.Side == "SHORT" {
			dirSlope = -slopePct
		}
		align := chartSwingAlign(c.Highs, c.Lows, c.Side, 2)
		block, _ := chartTrendGate(ctx, 30, 0.55, 0.60, 2)
		goAllow := !block
		if math.Abs(align-c.PyAlign) > 1e-3 || math.Abs(r2-c.PyR2) > 1e-3 || math.Abs(dirSlope-c.PyDirSlope) > 1e-3 {
			mism++
			if mism <= 5 {
				t.Logf("NUM mismatch %s/%s: go(al=%.4f r2=%.4f ds=%.4f) py(al=%.4f r2=%.4f ds=%.4f)",
					c.Sym, c.Side, align, r2, dirSlope, c.PyAlign, c.PyR2, c.PyDirSlope)
			}
		}
		if goAllow != c.PyAllow {
			allowMism++
			t.Logf("ALLOW mismatch %s/%s: go=%v py=%v", c.Sym, c.Side, goAllow, c.PyAllow)
		}
	}
	t.Logf("cases=%d numeric_mismatch=%d allow_mismatch=%d", len(cases), mism, allowMism)
	if allowMism > 0 {
		t.Errorf("allow-decision mismatches: %d (Go gate diverges from validated Python backtest)", allowMism)
	}
	if mism > 0 {
		t.Errorf("numeric mismatches: %d", mism)
	}
}
