package backtest

import (
	"math"
	"testing"
	"time"

	"nofx/market"
)

// synthBars builds a simple ascending 1h series from closes (high=low=close+/-,
// open irrelevant for these tests). highOff/lowOff widen the bar range.
func synthBars(start int64, closes []float64, rng float64) []market.Kline {
	bars := make([]market.Kline, len(closes))
	t := start
	for i, c := range closes {
		bars[i] = market.Kline{
			OpenTime: t,
			Open:     c,
			High:     c + rng,
			Low:      c - rng,
			Close:    c,
		}
		t += int64(time.Hour / time.Millisecond)
	}
	return bars
}

// TestPortfolioSimGuardDisabledMatchesBaseline verifies that with GuardParams{}
// (disabled) the portfolio sim's total PnL equals the sum of ReplayEntry over
// the same entries — i.e. the overlay is a true no-op when off.
func TestPortfolioSimGuardDisabledMatchesBaseline(t *testing.T) {
	p := ClaudeBaselineParams()

	// Two entries on different "symbols" with distinct price paths.
	start := int64(1_700_000_000_000)
	// give >= atrLookback(14) pre-entry bars so ATR resolves (percent mode
	// ignores ATR, but entryIdx must be >= atrLookback for parity).
	mk := func(sym string, base float64, fwd []float64) loadedEntry {
		pre := make([]float64, 20)
		for i := range pre {
			pre[i] = base
		}
		closes := append(pre, fwd...)
		bars := synthBars(start, closes, base*0.005)
		return loadedEntry{
			entry: Entry{
				Symbol:     sym,
				Side:       "LONG",
				EntryPrice: base,
				EntryTime:  bars[20].OpenTime,
				Quantity:   1,
			},
			bars:     bars,
			entryIdx: 20,
		}
	}
	// A: rises then falls. B: steady small gain.
	a := mk("AAA", 100, []float64{102, 105, 108, 104, 100, 98})
	b := mk("BBB", 50, []float64{50.5, 51, 51.2, 51, 50.8, 50.6})
	loaded := []loadedEntry{a, b}

	// Baseline via independent ReplayEntry.
	var baseSum float64
	for _, le := range loaded {
		r := ReplayEntry(p, le.entry, le.bars, le.entryIdx)
		baseSum += r.RealizedPnL
	}

	sim := RunPortfolioSim(p, GuardParams{}, loaded)

	if math.Abs(sim.TotalPnL-baseSum) > 1e-6 {
		t.Fatalf("guard-disabled sim PnL %.6f != baseline sum %.6f", sim.TotalPnL, baseSum)
	}
	if sim.GuardTrims != 0 {
		t.Fatalf("guard disabled but GuardTrims=%d", sim.GuardTrims)
	}
}

// TestPortfolioSimL2ReducesGiveback verifies the L2 circuit breaker trims
// winning positions on a portfolio giveback and reduces MaxGiveback vs baseline.
func TestPortfolioSimL2ReducesGiveback(t *testing.T) {
	// Use a no-TP/no-DD params so baseline holds the full runner and the
	// giveback is fully exposed; the guard is the only thing that can act.
	p := ProtectionParams{
		Unit:        UnitPercent,
		StopLossPct: 50, // far away, won't trigger
	}
	start := int64(1_700_000_000_000)
	mk := func(sym string, base float64, fwd []float64) loadedEntry {
		pre := make([]float64, 20)
		for i := range pre {
			pre[i] = base
		}
		closes := append(pre, fwd...)
		bars := synthBars(start, closes, base*0.001)
		return loadedEntry{
			entry: Entry{Symbol: sym, Side: "LONG", EntryPrice: base, EntryTime: bars[20].OpenTime, Quantity: 1},
			bars:  bars, entryIdx: 20,
		}
	}
	// Both rise strongly then give back hard (correlated reversal).
	a := mk("AAA", 100, []float64{110, 120, 130, 115, 100})
	b := mk("BBB", 100, []float64{108, 116, 124, 112, 100})
	loaded := []loadedEntry{a, b}

	base := RunPortfolioSim(p, GuardParams{}, loaded)
	guard := RunPortfolioSim(p, GuardParams{
		Enabled:        true,
		L2Enabled:      true,
		L2GivebackPct:  25,
		L2MinPeakQuote: 5,
		L2ClosePct:     50,
	}, loaded)

	if guard.GuardTrims == 0 {
		t.Fatalf("expected L2 guard to trim on giveback, got 0 trims")
	}
	// The guard locks profit into realized equity, so the success metric is the
	// equity (realized+unrealized) max drawdown, NOT the raw unrealized swing
	// (which always returns to ~0 once positions exit). Guard must lower it.
	if !(guard.MaxPortfolioDD < base.MaxPortfolioDD) {
		t.Fatalf("expected guard MaxPortfolioDD %.2f < baseline %.2f", guard.MaxPortfolioDD, base.MaxPortfolioDD)
	}
	t.Logf("baseline DD=%.2f pnl=%.2f | guard DD=%.2f pnl=%.2f trims=%d",
		base.MaxPortfolioDD, base.TotalPnL, guard.MaxPortfolioDD, guard.TotalPnL, guard.GuardTrims)
}
