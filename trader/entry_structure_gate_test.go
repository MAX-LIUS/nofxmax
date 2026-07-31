package trader

import (
	"math"
	"testing"

	"nofx/market"
	"nofx/store"
)

// bar builds a KlineBar; open/close are set to the midpoint so only H/L matter
// for pivot detection.
func sgBar(h, l float64) market.KlineBar {
	m := (h + l) / 2
	return market.KlineBar{Open: m, High: h, Low: l, Close: m}
}

// zigzag builds a series whose swing highs and lows both step by `step` per leg,
// so the HH/HL sequence is unambiguous. Negative step = downtrend.
//
// Values are linearly interpolated between alternating troughs and peaks so that
// EVERY bar is distinct. Flat runs of identical bars would register several
// adjacent pivots at the same price (pivot detection uses >=/<=, so ties count),
// and equal pivots are not strictly monotonic — the sequence would read as "no
// structure" and the test would fail for a reason unrelated to the logic.
func zigzag(legs int, base, step, amp float64) []market.KlineBar {
	const per = 5 // bars per half-leg; >= lb+1 so a lb=3 fractal can resolve
	// The leg amplitude must exceed the per-leg drift, otherwise the next trough
	// lands beyond the previous peak and the "zigzag" is really a monotonic ramp
	// with no pivots at all. Enforce it here rather than trusting callers.
	if amp <= math.Abs(step) {
		amp = math.Abs(step) * 2
	}
	// Alternating extremes: trough, peak, trough, peak, ...
	var ext []float64
	lvl := base
	for i := 0; i < legs; i++ {
		ext = append(ext, lvl, lvl+amp)
		lvl += step
	}
	var vals []float64
	for i := 0; i+1 < len(ext); i++ {
		for j := 0; j < per; j++ {
			f := float64(j) / float64(per)
			vals = append(vals, ext[i]+(ext[i+1]-ext[i])*f)
		}
	}
	vals = append(vals, ext[len(ext)-1])
	out := make([]market.KlineBar, 0, len(vals))
	for _, v := range vals {
		out = append(out, sgBar(v+0.1, v-0.1))
	}
	return out
}

func TestSwingSeqDir_RisingSequenceIsLong(t *testing.T) {
	bars := zigzag(6, 100, 5, 4)
	if got := swingSeqDir(bars, 3, 3); got != 1 {
		t.Fatalf("rising zigzag should be +1 (HH+HL), got %d", got)
	}
}

func TestSwingSeqDir_FallingSequenceIsShort(t *testing.T) {
	bars := zigzag(6, 150, -5, 4)
	if got := swingSeqDir(bars, 3, 3); got != -1 {
		t.Fatalf("falling zigzag should be -1 (LH+LL), got %d", got)
	}
}

// A chop range must read as "no structure" (0), not as a weak direction. This is
// the whole point of using a strict monotonic test instead of chartSwingAlign's
// agreeing-fraction score, which would return ~0.5 here and pass any alignMin
// below that.
func TestSwingSeqDir_ChopIsNoStructure(t *testing.T) {
	var bars []market.KlineBar
	for i := 0; i < 10; i++ {
		up := i%2 == 0
		for j := 0; j < 4; j++ {
			if up {
				bars = append(bars, sgBar(110, 108))
			} else {
				bars = append(bars, sgBar(102, 100))
			}
		}
	}
	if got := swingSeqDir(bars, 3, 3); got != 0 {
		t.Fatalf("flat alternating range should be 0 (no clean structure), got %d", got)
	}
}

func TestSwingSeqDir_InsufficientBarsAbstains(t *testing.T) {
	bars := []market.KlineBar{sgBar(10, 9), sgBar(11, 10), sgBar(12, 11)}
	if got := swingSeqDir(bars, 3, 3); got != 0 {
		t.Fatalf("too few bars must abstain with 0, got %d", got)
	}
}

// Guards the degenerate lb that would make every bar a pivot, and the n<2 case
// where "sequence" is meaningless. Both are clamped inside the function; a
// caller passing 0 must not get a spurious direction.
func TestSwingSeqDir_DegenerateParamsClamped(t *testing.T) {
	bars := zigzag(6, 100, 5, 4)
	if got := swingSeqDir(bars, 0, 0); got != 1 {
		t.Fatalf("lb=0,n=0 should clamp to defaults and still read the uptrend, got %d", got)
	}
}

// A rising sequence is NOT a valid short, and vice versa. Direction disagreement
// is the primary block condition.
func TestSwingSeqDir_DirectionIsSideSpecific(t *testing.T) {
	up := swingSeqDir(zigzag(6, 100, 5, 4), 3, 3)
	down := swingSeqDir(zigzag(6, 150, -5, 4), 3, 3)
	if up == down {
		t.Fatalf("up and down series must not read the same direction (both %d)", up)
	}
}

func TestNearestBlockingLevel_LongPicksNearestHighAbove(t *testing.T) {
	// Peaks at 120 and 140; entry 100 → nearest blocking is 120 = 20%.
	var bars []market.KlineBar
	push := func(h, l float64, n int) {
		for i := 0; i < n; i++ {
			bars = append(bars, sgBar(h, l))
		}
	}
	push(100, 99, 4)
	push(140, 139, 4)
	push(100, 99, 4)
	push(120, 119, 4)
	push(100, 99, 4)
	dist, ok := nearestBlockingLevelPct(bars, 100, true, 3)
	if !ok {
		t.Fatal("expected a blocking level above entry")
	}
	if dist < 19.9 || dist > 20.1 {
		t.Fatalf("expected ~20%% to the nearer 120 peak, got %.4f", dist)
	}
}

func TestNearestBlockingLevel_ShortPicksNearestLowBelow(t *testing.T) {
	var bars []market.KlineBar
	push := func(h, l float64, n int) {
		for i := 0; i < n; i++ {
			bars = append(bars, sgBar(h, l))
		}
	}
	push(100, 99, 4)
	push(61, 60, 4)
	push(100, 99, 4)
	push(81, 80, 4)
	push(100, 99, 4)
	dist, ok := nearestBlockingLevelPct(bars, 100, false, 3)
	if !ok {
		t.Fatal("expected a blocking level below entry")
	}
	if dist < 19.9 || dist > 20.1 {
		t.Fatalf("expected ~20%% to the nearer 80 trough, got %.4f", dist)
	}
}

// No pivot in the path = clear path = ok:false, which the gate treats as PASS.
// This mirrors the research finding that "no structure ahead" was not a
// predictor of failure (34 trades 68% +8.23 vs 20 at 70% -8.25, p=0.51).
func TestNearestBlockingLevel_ClearPathReportsNotFound(t *testing.T) {
	bars := zigzag(6, 100, -5, 4) // all structure below
	if _, ok := nearestBlockingLevelPct(bars, 1000, true, 3); ok {
		t.Fatal("entry far above all pivots should report no blocking level")
	}
}

func TestNearestBlockingLevel_RejectsBadInput(t *testing.T) {
	bars := zigzag(6, 100, 5, 4)
	if _, ok := nearestBlockingLevelPct(bars, 0, true, 3); ok {
		t.Fatal("entry<=0 must not report a level")
	}
	if _, ok := nearestBlockingLevelPct(bars, 100, true, 0); ok {
		t.Fatal("lb<1 must not report a level")
	}
}

func TestStructuralGateDefaults_OffWithConcreteParams(t *testing.T) {
	c := store.EntryGateConfig{}.WithDefaults()
	if c.StructuralAlignmentEnabled() {
		t.Fatal("structural gate must ship OFF by default")
	}
	if c.StructuralPivotLookback != 3 {
		t.Fatalf("pivot lookback default want 3, got %d", c.StructuralPivotLookback)
	}
	// n=2 is the validated recommendation: 11.5 entries/day, 28 coins, all 4 traders
	// improved on real July PnL, out-of-sample increment +0.272 (p=0.0012).
	if c.StructuralSwingCount != 2 {
		t.Fatalf("swing count default want 2, got %d", c.StructuralSwingCount)
	}
	// 0 = direction check only, deliberately. A 0.05-step sweep picked 0.05 and that
	// pick was rejected on review: it beats 0 by noise on real PnL while being a
	// tuned value, and the tuned cells DEGRADE out-of-sample while pct=0 holds.
	if c.StructuralMinBlockingPctValue() != 0 {
		t.Fatalf("min blocking pct default want 0 (direction only), got %.3f", c.StructuralMinBlockingPctValue())
	}
	if c.StructuralAuditOnlyEnabled() {
		t.Fatal("audit-only must default false")
	}
}

// Explicit 0 on the blocking-distance sub-check means "direction only" and must
// survive normalisation — the same unset-vs-explicit-0 trap that made
// TrailMinProfitATR silently disable itself in production (v1.17.5).
func TestStructuralGateDefaults_ExplicitZeroBlockingPctSurvives(t *testing.T) {
	// nil (field absent from JSON) takes the default, which is now 0 = direction only.
	c := store.EntryGateConfig{}.WithDefaults()
	if c.StructuralMinBlockingPctValue() != 0 {
		t.Fatalf("unset must take the 0 default, got %.3f", c.StructuralMinBlockingPctValue())
	}
	// Explicit 0 means "direction only" and must survive normalisation.
	zero := 0.0
	c0 := store.EntryGateConfig{StructuralMinBlockingPct: &zero}.WithDefaults()
	if c0.StructuralMinBlockingPctValue() != 0 {
		t.Fatalf("explicit 0 must survive as 0 (direction-only), got %.3f", c0.StructuralMinBlockingPctValue())
	}
	// An explicit positive value must NOT be reset to the default. With the default
	// now 0, this is what still gives the pointer its purpose: if the default ever
	// moves off 0 again, unset-vs-explicit must stay distinguishable (the
	// TrailMinProfitATR v1.17.5 trap).
	pos := 0.35
	cp := store.EntryGateConfig{StructuralMinBlockingPct: &pos}.WithDefaults()
	if cp.StructuralMinBlockingPctValue() != 0.35 {
		t.Fatalf("explicit 0.35 must survive, got %.3f", cp.StructuralMinBlockingPctValue())
	}
	// Negative clamps to 0.
	neg := -1.0
	c2 := store.EntryGateConfig{StructuralMinBlockingPct: &neg}.WithDefaults()
	if c2.StructuralMinBlockingPctValue() != 0 {
		t.Fatalf("negative must clamp to 0, got %.3f", c2.StructuralMinBlockingPctValue())
	}
}

func TestStructuralGateDefaults_ParamsClamped(t *testing.T) {
	c := store.EntryGateConfig{StructuralPivotLookback: 99, StructuralSwingCount: 99}.WithDefaults()
	if c.StructuralPivotLookback != 10 {
		t.Fatalf("pivot lookback should clamp to 10, got %d", c.StructuralPivotLookback)
	}
	if c.StructuralSwingCount != 6 {
		t.Fatalf("swing count should clamp to 6, got %d", c.StructuralSwingCount)
	}
}

func TestStructuralGateDefaults_ExplicitOptInHonoured(t *testing.T) {
	yes := true
	c := store.EntryGateConfig{StructuralAlignment: &yes}.WithDefaults()
	if !c.StructuralAlignmentEnabled() {
		t.Fatal("explicit true must enable the gate")
	}
}

// Audit-only failures must deduct nothing, so observation cannot quietly shrink
// position size while evidence is still being gathered.
func TestStructuralGate_AuditPenaltyIsZero(t *testing.T) {
	for _, code := range []string{"structural_alignment_missing", "structural_blocking_level_too_close"} {
		score := computeGateScore([]EntryGateCheck{{
			Code: code, Stage: string(EntryGateStageStructuralFit), Passed: false, Enforced: false,
		}})
		if score != 100 {
			t.Fatalf("%s in audit mode should not deduct (want 100, got %d)", code, score)
		}
	}
}
