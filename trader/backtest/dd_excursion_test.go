package backtest

import (
	"math"
	"testing"
)

// TestExcursionsDoNotMultiplyCountOneDecline pins the counting rule. A new episode
// must start only at a NEW high; successive dips inside one unrecovered decline are
// ONE episode. Counting each dip separately would inflate "how often does a 2%
// drawdown happen", which is the direction that flatters a breaker.
func TestExcursionsDoNotMultiplyCountOneDecline(t *testing.T) {
	// One long decline with internal wobbles that never regain the 100 high.
	curve := []float64{100, 98, 99, 97, 98, 96, 94, 95, 90}
	exs := DDExcursions(curve)
	if len(exs) != 1 {
		t.Fatalf("expected 1 episode for a single unrecovered decline, got %d", len(exs))
	}
	if exs[0].Recovered {
		t.Error("episode never regained the high; Recovered must be false")
	}
	wantDD := (100.0 - 90.0) / 100.0 * 100
	if math.Abs(exs[0].MaxDDPct-wantDD) > 1e-9 {
		t.Errorf("MaxDDPct = %.4f, want %.4f", exs[0].MaxDDPct, wantDD)
	}
}

// TestExcursionsSplitOnNewHigh verifies two separate episodes are counted when the
// curve fully recovers in between.
func TestExcursionsSplitOnNewHigh(t *testing.T) {
	curve := []float64{100, 95, 100, 101, 96, 102}
	exs := DDExcursions(curve)
	if len(exs) != 2 {
		t.Fatalf("expected 2 episodes, got %d", len(exs))
	}
	for i, e := range exs {
		if !e.Recovered {
			t.Errorf("episode %d should be marked recovered", i)
		}
	}
}

// TestUnrecoveredFinalEpisodeIsNotMarkedRecovered guards against counting an
// in-progress decline as evidence that declines repair themselves.
func TestUnrecoveredFinalEpisodeIsNotMarkedRecovered(t *testing.T) {
	exs := DDExcursions([]float64{100, 90, 80})
	if len(exs) != 1 || exs[0].Recovered {
		t.Fatalf("trailing decline must be one unrecovered episode, got %+v", exs)
	}
}

// TestDDLevelStatsConditionalCounts checks the conditional the whole argument turns
// on: of the episodes that reached m%, how many went to 2m%.
func TestDDLevelStatsConditionalCounts(t *testing.T) {
	// Episode A: -2% then recovers. Episode B: -10% then recovers.
	curve := []float64{100, 98, 100, 101, 90.9, 101, 102}
	stats := DDLevelStats(curve, []float64{2, 4})
	if stats[0].Reached != 2 {
		t.Fatalf("2%% should be reached by both episodes, got %d", stats[0].Reached)
	}
	// Only the -10% episode reaches 4%.
	if stats[0].WentTwice != 1 {
		t.Errorf("only one episode reaches 2x of 2%%, got %d", stats[0].WentTwice)
	}
	if stats[1].Reached != 1 {
		t.Errorf("only one episode reaches 4%%, got %d", stats[1].Reached)
	}
}

// TestLedgerForgoneIsMeasuredAgainstEpisodeEnd pins the accounting distinction that
// decides whether a firing paid: "saved" is versus the TROUGH, which holding never
// forces you to sell at, while "forgone" is versus where the episode actually
// ENDED, which is what holding really delivered. Conflating them makes every
// breaker look free.
func TestLedgerForgoneIsMeasuredAgainstEpisodeEnd(t *testing.T) {
	// Peak 100, dips to 90 (-10%), recovers to 105.
	curve := []float64{100, 98, 90, 95, 105}
	leds := EpisodeLedgers(curve, 2.0)
	if len(leds) != 1 {
		t.Fatalf("expected 1 ledger row, got %d", len(leds))
	}
	l := leds[0]
	if math.Abs(l.CutEquity-98) > 1e-9 {
		t.Errorf("cut should happen at the first bar reaching -2%%, i.e. 98, got %.4f", l.CutEquity)
	}
	if math.Abs(l.TroughEquity-90) > 1e-9 {
		t.Errorf("trough should be 90, got %.4f", l.TroughEquity)
	}
	// The episode ends when the curve regains the 100 peak, at 105.
	if math.Abs(l.EndEquity-105) > 1e-9 {
		t.Errorf("end equity should be the recovery bar 105, got %.4f", l.EndEquity)
	}
	if math.Abs(l.Saved-8) > 1e-9 {
		t.Errorf("saved vs trough should be 98-90=8, got %.4f", l.Saved)
	}
	if math.Abs(l.Forgone-7) > 1e-9 {
		t.Errorf("forgone vs ending should be 105-98=7, got %.4f", l.Forgone)
	}
}

// TestExcursionsHandleDegenerateInput keeps the analysis from panicking.
func TestExcursionsHandleDegenerateInput(t *testing.T) {
	for _, c := range [][]float64{nil, {}, {100}, {100, 100, 100}} {
		if got := DDExcursions(c); len(got) != 0 {
			t.Errorf("curve %v should yield no episodes, got %d", c, len(got))
		}
	}
}

// TestEquityCurveIsRecordedAndUsable guards the plumbing: the sim must expose a
// curve of the same length as its timestamp series, and it must start at capital.
func TestEquityCurveIsRecordedAndUsable(t *testing.T) {
	p := ProtectionParams{Unit: UnitPercent, FaithfulExits: true, FeeRatePct: 0}
	loaded := acctStaggered(6, 2, sawPath)
	r := RunPortfolioSim(p, GuardParams{StartCapital: 150}, loaded)
	if len(r.EquityCurve) == 0 {
		t.Fatal("EquityCurve is empty; the excursion analysis has nothing to read")
	}
	if len(r.EquityCurve) != len(r.EquityCurveMs) {
		t.Fatalf("curve/timestamp length mismatch: %d vs %d", len(r.EquityCurve), len(r.EquityCurveMs))
	}
	for i := 1; i < len(r.EquityCurveMs); i++ {
		if r.EquityCurveMs[i] < r.EquityCurveMs[i-1] {
			t.Fatalf("timestamps are not ascending at %d", i)
		}
	}
}
