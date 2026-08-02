package backtest

import (
	"math"
	"testing"

	"nofx/market"
)

// TestFineGridCoversTheProposedConfig guards the specific defect that made the
// earlier coarse sweep unable to settle the argument: arm was only
// {0,1,2,3,5,8,12}, so "+4% gain arms, 2% drawdown fires" was never actually run
// and I reported on a grid that omitted the point in dispute.
func TestFineGridCoversTheProposedConfig(t *testing.T) {
	grid := L3CycleFineGrid(234.40, 10)
	want := [][2]float64{{2.0, 4.0}, {0.1, 0.0}, {2.0, 0.0}, {4.0, 4.0}, {6.0, 10.0}}
	for _, w := range want {
		found := false
		for _, g := range grid {
			if math.Abs(g.L3CycleDDPct-w[0]) < 1e-9 && math.Abs(g.L3CycleArmGainPct-w[1]) < 1e-9 {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("fine grid is missing dd=%.1f%% arm=%.1f%%", w[0], w[1])
		}
	}
}

// TestFineGridStepsAreTenthsOfAPercent verifies the requested 0.1% resolution on
// the drawdown axis, including that it reaches down to the take-profit limit.
func TestFineGridStepsAreTenthsOfAPercent(t *testing.T) {
	grid := L3CycleFineGrid(100, 10)
	seen := map[float64]bool{}
	for _, g := range grid {
		seen[g.L3CycleDDPct] = true
	}
	for i := 1; i <= 60; i++ {
		dd := float64(i) / 10
		if !seen[dd] {
			t.Fatalf("drawdown axis is missing the %.1f%% step", dd)
		}
	}
	if seen[0] {
		t.Error("dd=0 must not be present: L3CycleDDPct<=0 disables the breaker entirely, " +
			"so it would silently be a no-breaker row masquerading as the take-profit limit")
	}
}

// TestSplitIsChronologicalAndDisjoint. A random split would leak: positions opened
// at the same time share the same market path, so a random test set would contain
// near-copies of train rows and report a fake out-of-sample pass.
func TestSplitIsChronologicalAndDisjoint(t *testing.T) {
	loaded := acctStaggered(10, 2, sawPath)
	train, test := SplitEntriesByTime(loaded)
	if len(train)+len(test) != len(loaded) {
		t.Fatalf("split lost or duplicated entries: %d+%d != %d", len(train), len(test), len(loaded))
	}
	if len(train) == 0 || len(test) == 0 {
		t.Fatalf("split produced an empty half: %d/%d", len(train), len(test))
	}
	var maxTrain int64
	for _, e := range train {
		if e.entry.EntryTime > maxTrain {
			maxTrain = e.entry.EntryTime
		}
	}
	for _, e := range test {
		if e.entry.EntryTime < maxTrain {
			t.Fatalf("test entry at %d predates the end of train (%d); the split is not chronological",
				e.entry.EntryTime, maxTrain)
		}
	}
}

// TestSplitHandlesTinyInput keeps the sweep from panicking on short histories.
func TestSplitHandlesTinyInput(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3} {
		train, test := SplitEntriesByTime(acctStaggered(n, 2, sawPath))
		if len(train)+len(test) != n {
			t.Fatalf("n=%d: split lost entries (%d+%d)", n, len(train), len(test))
		}
	}
}

// TestSplitEdgeAccountingIsPerHalf verifies the reported edge is measured against
// each half's OWN baseline. Comparing a half's result to the whole run's baseline
// would mix two different books and make retention meaningless.
func TestSplitEdgeAccountingIsPerHalf(t *testing.T) {
	p := ProtectionParams{Unit: UnitPercent, FaithfulExits: true, FeeRatePct: 0.05}
	loaded := acctStaggered(12, 2, sawPath)
	rows, trainBase, testBase := SweepCycleFineSplit(p, loaded, 150, 10)
	if len(rows) == 0 {
		t.Fatal("sweep produced no rows")
	}
	for _, r := range rows {
		if r.TrainBase != trainBase.TotalPnL {
			t.Fatalf("train edge uses the wrong baseline: %.4f vs %.4f", r.TrainBase, trainBase.TotalPnL)
		}
		if r.TestBase != testBase.TotalPnL {
			t.Fatalf("test edge uses the wrong baseline: %.4f vs %.4f", r.TestBase, testBase.TotalPnL)
		}
	}
	if trainBase.TotalPnL == testBase.TotalPnL && len(loaded) > 4 {
		t.Log("note: both halves scored identically; fixture is symmetric")
	}
}

// TestDrawdownIsOnRawEquityNotLeverageAdjusted pins the user's clarification: the
// percentage is raw account equity (148 -> 146.52 is a 1% drop). Leverage must NOT
// be divided out of the trigger. It is used only for the margin-usage gate.
func TestDrawdownIsOnRawEquityNotLeverageAdjusted(t *testing.T) {
	// A single position whose mark drops so that portfolio equity falls just past
	// 1%. With capital 148 and a position sized so the loss is ~1.6 quote, equity
	// goes 148 -> ~146.4, i.e. a ~1.1% raw drop.
	//
	// dd=1.0% must fire and dd=1.2% must not. If the trigger divided by leverage
	// (10x here) the same move would read as 0.11% and NEITHER would fire, so this
	// test fails loudly if leverage ever leaks into the drawdown maths.
	entry := Entry{
		Symbol: "T", Side: "LONG", EntryPrice: 100, Quantity: 1.6,
		EntryTime: 0, ExitTime: 60 * 60 * 1000 * 10,
	}
	bars := []market.Kline{}
	// flat at 100 for 3 bars (build the peak), then 99 (1.6% notional loss = 1.6 quote)
	for i, px := range []float64{100, 100, 100, 99, 99, 99} {
		ts := int64(i) * 60 * 60 * 1000
		bars = append(bars, market.Kline{OpenTime: ts, Open: px, High: px, Low: px, Close: px, CloseTime: ts + 3599999})
	}
	loaded := []LoadedEntry{{entry: entry, bars: bars, entryIdx: 0}}
	p := ProtectionParams{Unit: UnitPercent, FaithfulExits: true, FeeRatePct: 0}

	mk := func(dd float64) int {
		g := GuardParams{
			Enabled: true, L3Enabled: true, StartCapital: 148,
			L3CycleEnabled: true, L3CycleDDPct: dd, L3VelWindow: 6,
			// No gate: this test is about the trigger arithmetic only.
		}
		return RunPortfolioSim(p, g, loaded).L3Cycles - 1
	}
	// equity peak 148, trough 148-1.6 = 146.4 -> drop = 1.081%
	if got := mk(1.0); got < 1 {
		t.Errorf("dd=1.0%% must fire on a 1.08%% raw equity drop, fired %d times "+
			"(leverage may be wrongly divided out of the trigger)", got)
	}
	if got := mk(1.2); got != 0 {
		t.Errorf("dd=1.2%% must NOT fire on a 1.08%% raw equity drop, fired %d times", got)
	}
}
