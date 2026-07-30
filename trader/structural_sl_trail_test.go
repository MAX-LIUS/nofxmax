package trader

import (
	"testing"

	"nofx/market"
	"nofx/store"
)

// btebool returns a *bool for config side gates.
func btebool(v bool) *bool { return &v }

func btef64(v float64) *float64 { return &v }

// mkBars builds a simple ascending window with the given lows/highs so a swing can
// be detected. Each bar i has Low=lows[i], High=highs[i], Close=closes[i].
func mkBars(lows, highs, closes []float64) []market.Kline {
	n := len(lows)
	out := make([]market.Kline, n)
	for i := 0; i < n; i++ {
		out[i] = market.Kline{
			OpenTime: int64(i) * 60000,
			Open:     closes[i],
			High:     highs[i],
			Low:      lows[i],
			Close:    closes[i],
		}
	}
	return out
}

func baseTrailCfg() store.StructuralSLConfig {
	return store.StructuralSLConfig{
		Enabled: true, CloseConfirm: true, TrailEnabled: true,
		FloorATRMul: 1.5, BackstopATRMul: 4.5, LookbackBars: 24, PivotStrength: 2,
		TrailTolATR: 0.0, TrailMode: "current", TrailMinProfitATR: btef64(1.0),
		TrailOnProfit: btebool(true), TrailOnLoss: btebool(true),
	}.WithDefaults()
}

// A long in profit with a nearer swing low below price should ratchet the boundary UP.
func TestComputeTrailBoundary_LongProfitRatchetsUp(t *testing.T) {
	ss := baseTrailCfg()
	// window: swing low at index 3 (value 100), price now 110, entry 100, atr 2
	lows := []float64{105, 104, 103, 100, 106, 108, 109}
	highs := []float64{107, 106, 105, 104, 110, 112, 113}
	closes := []float64{106, 105, 104, 102, 108, 110, 110}
	window := mkBars(lows, highs, closes)
	nb, nr := computeTrailBoundary(ss, trailRecomputeInput{
		window: window, curClose: 110, atr: 2, entry: 100, isLong: true,
		curBound: 90, ratchets: 0,
	})
	if nr != 1 {
		t.Fatalf("expected 1 ratchet, got %d (boundary %.4f)", nr, nb)
	}
	if nb <= 90 {
		t.Fatalf("expected boundary to tighten UP from 90, got %.4f", nb)
	}
}

// Loss-side gate off: a position in LOSS must not ratchet.
func TestComputeTrailBoundary_LossGateBlocks(t *testing.T) {
	ss := baseTrailCfg()
	ss.TrailOnLoss = btebool(false)
	// long, price 95 < entry 100 => in loss
	lows := []float64{96, 94, 92, 90, 93, 94, 95}
	highs := []float64{98, 97, 95, 94, 96, 97, 98}
	closes := []float64{97, 96, 94, 92, 94, 95, 95}
	window := mkBars(lows, highs, closes)
	_, nr := computeTrailBoundary(ss, trailRecomputeInput{
		window: window, curClose: 95, atr: 2, entry: 100, isLong: true,
		curBound: 85, ratchets: 0,
	})
	if nr != 0 {
		t.Fatalf("loss-gate off must block ratchet, got %d ratchets", nr)
	}
}

// Profit-side gate off: a position in PROFIT must not ratchet.
func TestComputeTrailBoundary_ProfitGateBlocks(t *testing.T) {
	ss := baseTrailCfg()
	ss.TrailOnProfit = btebool(false)
	lows := []float64{105, 104, 103, 100, 106, 108, 109}
	highs := []float64{107, 106, 105, 104, 110, 112, 113}
	closes := []float64{106, 105, 104, 102, 108, 110, 110}
	window := mkBars(lows, highs, closes)
	_, nr := computeTrailBoundary(ss, trailRecomputeInput{
		window: window, curClose: 110, atr: 2, entry: 100, isLong: true,
		curBound: 90, ratchets: 0,
	})
	if nr != 0 {
		t.Fatalf("profit-gate off must block ratchet, got %d ratchets", nr)
	}
}

// Max-ratchet cap: at the cap, no further tighten.
func TestComputeTrailBoundary_MaxRatchetCap(t *testing.T) {
	ss := baseTrailCfg()
	ss.TrailMaxRatchets = 2
	lows := []float64{105, 104, 103, 100, 106, 108, 109}
	highs := []float64{107, 106, 105, 104, 110, 112, 113}
	closes := []float64{106, 105, 104, 102, 108, 110, 110}
	window := mkBars(lows, highs, closes)
	nb, nr := computeTrailBoundary(ss, trailRecomputeInput{
		window: window, curClose: 110, atr: 2, entry: 100, isLong: true,
		curBound: 95, ratchets: 2, // already at cap
	})
	if nr != 2 || nb != 95 {
		t.Fatalf("at cap must not ratchet: got ratchets=%d boundary=%.4f", nr, nb)
	}
}

// Min-profit activation: below the threshold the trail does not arm.
func TestComputeTrailBoundary_MinProfitGate(t *testing.T) {
	ss := baseTrailCfg()
	ss.TrailMinProfitATR = btef64(3.0) // need +6 (3*atr) from entry; price only +2
	lows := []float64{105, 104, 103, 100, 101, 101, 102}
	highs := []float64{107, 106, 105, 104, 103, 103, 104}
	closes := []float64{106, 105, 104, 102, 102, 102, 102}
	window := mkBars(lows, highs, closes)
	_, nr := computeTrailBoundary(ss, trailRecomputeInput{
		window: window, curClose: 102, atr: 2, entry: 100, isLong: true,
		curBound: 90, ratchets: 0,
	})
	if nr != 0 {
		t.Fatalf("below min-profit must not arm, got %d ratchets", nr)
	}
}

// Tighten-only: a proposed LOOSER boundary must be rejected.
func TestComputeTrailBoundary_NeverLoosens(t *testing.T) {
	ss := baseTrailCfg()
	lows := []float64{105, 104, 103, 100, 106, 108, 109}
	highs := []float64{107, 106, 105, 104, 110, 112, 113}
	closes := []float64{106, 105, 104, 102, 108, 110, 110}
	window := mkBars(lows, highs, closes)
	// curBound already very tight (108, just below price); nearest swing (100) is looser
	nb, nr := computeTrailBoundary(ss, trailRecomputeInput{
		window: window, curClose: 110, atr: 2, entry: 100, isLong: true,
		curBound: 108, ratchets: 3,
	})
	if nr != 3 || nb != 108 {
		t.Fatalf("looser candidate must be rejected: got ratchets=%d boundary=%.4f", nr, nb)
	}
}

// min_profit=0 DISABLES the activation gate: a long barely in profit (favorable
// excursion < 1 ATR) must still ratchet, whereas with min_profit=1.0 it would not.
func TestComputeTrailBoundary_MinProfitZeroDisablesGate(t *testing.T) {
	// swing low at index 3 (100); price 100.5 = only +0.5 from entry 100, atr 2
	// → favMove 0.5 < 1.0*2 would block if min_profit were 1.0.
	lows := []float64{105, 104, 103, 100, 100.6, 100.7, 100.8}
	highs := []float64{107, 106, 105, 104, 101, 101, 101}
	closes := []float64{106, 105, 104, 102, 100.6, 100.6, 100.5}
	window := mkBars(lows, highs, closes)

	gated := baseTrailCfg()
	gated.TrailMinProfitATR = btef64(1.0)
	_, nrGated := computeTrailBoundary(gated, trailRecomputeInput{
		window: window, curClose: 100.5, atr: 2, entry: 100, isLong: true,
		curBound: 90, ratchets: 0,
	})
	if nrGated != 0 {
		t.Fatalf("min_profit=1.0 should block a +0.25ATR position, got %d ratchets", nrGated)
	}

	open := baseTrailCfg()
	open.TrailMinProfitATR = btef64(0) // disabled
	_, nrOpen := computeTrailBoundary(open, trailRecomputeInput{
		window: window, curClose: 100.5, atr: 2, entry: 100, isLong: true,
		curBound: 90, ratchets: 0,
	})
	if nrOpen != 1 {
		t.Fatalf("min_profit=0 should DISABLE the gate and allow the ratchet, got %d ratchets", nrOpen)
	}
}
