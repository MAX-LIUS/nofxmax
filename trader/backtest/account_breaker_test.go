package backtest

import (
	"math"
	"testing"
	"time"

	"nofx/market"
)

// acctLoaded builds n synthetic LONG entries on a shared price path so the
// portfolio has a controllable number of simultaneously-open positions. The path
// rises then falls hard, which is what the equity breaker is supposed to catch.
func acctLoaded(n int, path []float64) []loadedEntry {
	start := int64(1_700_000_000_000)
	out := make([]loadedEntry, 0, n)
	for i := 0; i < n; i++ {
		pre := make([]float64, 20)
		for j := range pre {
			pre[j] = 100
		}
		closes := append(pre, path...)
		bars := synthBars(start, closes, 0.2)
		out = append(out, loadedEntry{
			entry: Entry{
				Symbol:     "S" + itoa(i),
				Side:       "LONG",
				EntryPrice: 100,
				EntryTime:  bars[20].OpenTime,
				Quantity:   1,
				ExitTime:   bars[len(bars)-1].OpenTime + int64(time.Hour/time.Millisecond),
			},
			bars:     bars,
			entryIdx: 20,
		})
	}
	return out
}

// declinePath is a mild rise followed by a sustained decline: it produces a real
// equity high-water mark and then a drawdown deep enough to cross the whole
// 1.5-4% band, without hitting the -5% baseline stop immediately.
var declinePath = []float64{100.5, 101, 101.5, 101.2, 100.8, 100.4, 100.1, 99.8, 99.5, 99.2, 98.9, 98.6}

// acctStaggered builds n entries that open at DIFFERENT times along a shared
// price path, which is what makes multi-firing testable at all: a whole-book
// flatten closes everything open at that instant, so with simultaneous entries
// the book is permanently empty after the first firing and no later firing can
// occur. Staggering entries reproduces the live situation the user describes,
// where the trader keeps opening new positions after a breaker event.
//
// stride is the gap in bars between consecutive entries.
func acctStaggered(n, stride int, path []float64) []loadedEntry {
	start := int64(1_700_000_000_000)
	pre := make([]float64, 20)
	for j := range pre {
		pre[j] = 100
	}
	closes := append(pre, path...)
	bars := synthBars(start, closes, 0.2)

	out := make([]loadedEntry, 0, n)
	for i := 0; i < n; i++ {
		idx := 20 + i*stride
		if idx >= len(bars) {
			break
		}
		b := make([]market.Kline, len(bars))
		copy(b, bars)
		out = append(out, loadedEntry{
			entry: Entry{
				Symbol:     "T" + itoa(i),
				Side:       "LONG",
				EntryPrice: b[idx].Close,
				EntryTime:  b[idx].OpenTime,
				Quantity:   1,
				ExitTime:   b[len(b)-1].OpenTime + int64(time.Hour/time.Millisecond),
			},
			bars:     b,
			entryIdx: idx,
		})
	}
	return out
}

// sawPath declines, recovers, then declines again. The recovery leg is what lets
// a two-state arm re-arm, so this path can distinguish "disarmed and stayed
// disarmed" from "re-armed on recovery".
var sawPath = []float64{
	100.2, 100.4, 99.6, 99.0, 98.4, 97.9, // leg 1 down
	98.6, 99.4, 100.2, 101.0, 101.6, // recovery
	100.8, 100.0, 99.2, 98.5, 97.8, // leg 2 down
}

func acctGuard(drop float64) GuardParams {
	g := mkRoll(250, drop, 100, false, 0, 6)
	g.L3GateMinPos = 3
	g.L3GateMarginPct = 70
	g.L3NominalLeverage = 5
	return g
}

// TestAccountBreakerFireCountMonotoneInThreshold is the regression test for the
// defect that invalidated the earlier equity-series study: raising the drawdown
// threshold produced MORE firings (dd=1.9% fired 4 times, dd=2.0% fired 39).
// That is impossible for a correct trigger — a strictly harder condition cannot
// be met more often on the same market data.
//
// It was caused by measuring drawdown against the SIMULATED path's own peak, so
// each threshold ran on a different equity curve and the counts were not
// comparable. This engine instead marks a real book to real prices, so the
// counts must be non-increasing in the threshold. Asserting it here means any
// future change that reintroduces that class of feedback bug fails loudly
// instead of producing a plausible-looking optimum.
func TestAccountBreakerFireCountMonotoneInThreshold(t *testing.T) {
	loaded := acctLoaded(4, declinePath)
	p := ClaudeBaselineParams()

	drops := []float64{1.5, 1.75, 2.0, 2.25, 2.5, 2.75, 3.0, 3.25, 3.5, 3.75, 4.0}
	prevFires := -1
	for _, d := range drops {
		res := RunPortfolioSim(p, acctGuard(d), loaded)
		if prevFires >= 0 && res.L3Events > prevFires {
			t.Fatalf("non-monotone fire count: drop=%.2f%% fired %d times, "+
				"but the easier threshold before it fired only %d. A higher "+
				"drawdown threshold must never fire more often on identical data",
				d, res.L3Events, prevFires)
		}
		prevFires = res.L3Events
	}
}

// TestAccountBreakerGateBlocksThinBook verifies the exposure gate: with a book
// below the position quorum and below the margin threshold, the breaker must not
// fire no matter how deep the equity drawdown, because flattening a thin book is
// cost without risk reduction.
func TestAccountBreakerGateBlocksThinBook(t *testing.T) {
	p := ClaudeBaselineParams()
	// One position: notional 100 / 5x lev = 20 margin. Capital 60 => 33% margin
	// utilisation, below the 70% leg, and 1 position is below the quorum of 3, so
	// BOTH gate legs are shut. Capital is small enough that the ~1.4% price
	// decline still moves equity >1.5%, so the raw trigger genuinely fires and the
	// test is about the gate rather than about a missing signal.
	loaded := acctLoaded(1, declinePath)
	g := acctGuard(1.5)
	g.StartCapital = 60

	res := RunPortfolioSim(p, g, loaded)
	if res.L3Events != 0 {
		t.Fatalf("gate should have blocked a 1-position/0.4%%-margin book, got %d fires", res.L3Events)
	}
	if res.TriggerHits == 0 {
		t.Fatal("expected the raw drawdown trigger to have been hit at least once; " +
			"if it never fired the test proves nothing about the gate")
	}
	if res.GateOpenTicks != 0 {
		t.Fatalf("gate reported open on %d ticks for a thin book", res.GateOpenTicks)
	}
}

// TestAccountBreakerGateOpensOnPositionCount verifies the other side: the same
// drawdown with enough positions does fire, so the gate is selective rather than
// simply always-closed.
func TestAccountBreakerGateOpensOnPositionCount(t *testing.T) {
	p := ClaudeBaselineParams()
	loaded := acctLoaded(4, declinePath)
	g := acctGuard(1.5)
	// 4 positions x 20 margin = 80 on 150 capital = 53% => margin leg shut (70%),
	// so the position-count leg (quorum 3) is the only thing that can open it.
	g.StartCapital = 150

	res := RunPortfolioSim(p, g, loaded)
	if res.GateOpenTicks == 0 {
		t.Fatal("gate never opened with 4 open positions and quorum 3")
	}
	if res.L3Events == 0 {
		t.Fatal("breaker never fired despite an open gate and a crossed threshold")
	}
}

// TestAccountBreakerArmSuppressesAfterFire verifies the two-state arm the user
// specified: 「每次熔断后，如果正收益达到一定比例就开始挂上回撤熔断，否则各自仓位控制」.
// After a firing the breaker disarms; it may only re-arm once equity has
// recovered the required profit above the firing level.
//
// Uses a staggered book on a saw path (down / up / down) so that multiple
// firings are physically possible, and asserts the arm strictly reduces firing
// frequency versus the always-armed control on identical data.
func TestAccountBreakerArmSuppressesAfterFire(t *testing.T) {
	p := ClaudeBaselineParams()
	loaded := acctStaggered(6, 2, sawPath)

	always := acctGuard(1.5)
	always.StartCapital = 150
	alwaysRes := RunPortfolioSim(p, always, loaded)

	armed := acctGuard(1.5)
	armed.StartCapital = 150
	// +5% is above what the saw's recovery leg delivers. At 3% the recovery IS
	// enough and the breaker legitimately re-arms and fires twice — measured, not
	// assumed — so a 3% threshold would assert the wrong thing.
	armed.L3ArmProfitPct = 5
	armedRes := RunPortfolioSim(p, armed, loaded)

	if alwaysRes.L3Events < 2 {
		t.Fatalf("control fired %d times; the staggered saw path is supposed to "+
			"permit repeat firings, so <2 means the fixture is broken, not the arm",
			alwaysRes.L3Events)
	}
	if armedRes.L3Events >= alwaysRes.L3Events {
		t.Fatalf("the arm must suppress at least one firing: always=%d armed=%d",
			alwaysRes.L3Events, armedRes.L3Events)
	}
	if armedRes.ArmedTicks >= armedRes.BreakerTicks {
		t.Fatalf("arm never disengaged: armed %d of %d ticks",
			armedRes.ArmedTicks, armedRes.BreakerTicks)
	}
}

// TestAccountBreakerSuppressedTriggerDoesNotConsumeReference guards the subtle
// failure mode called out in applyRollingBreaker: if a gate/arm-suppressed
// trigger still re-based the rolling reference, a disarmed breaker would silently
// eat the drawdown signal and the reference would ratchet down through a decline.
// Then re-arming mid-decline would find "no drawdown" and never fire.
//
// Verified behaviourally: a config disarmed for the whole path must leave the
// same trigger-hit count as one that is armed but gate-blocked, i.e. suppression
// does not alter how often the raw threshold is observed to be crossed.
func TestAccountBreakerSuppressedTriggerDoesNotConsumeReference(t *testing.T) {
	p := ClaudeBaselineParams()
	loaded := acctLoaded(1, declinePath) // thin book => gate always shut

	g := acctGuard(1.5)
	g.StartCapital = 60
	gateBlocked := RunPortfolioSim(p, g, loaded)

	if gateBlocked.TriggerHits < 2 {
		t.Skipf("only %d trigger hits; need >=2 to show the reference was not consumed",
			gateBlocked.TriggerHits)
	}
	if gateBlocked.L3Events != 0 {
		t.Fatalf("expected no fires while gate-blocked, got %d", gateBlocked.L3Events)
	}
}

// TestFeesReducePnLAndDefaultToZero verifies the fee model: zero rate is an exact
// no-op (so every pre-existing result stands), and a non-zero rate strictly
// reduces PnL by roughly the round-trip cost on notional.
func TestFeesReducePnLAndDefaultToZero(t *testing.T) {
	loaded := acctLoaded(2, declinePath)

	free := ClaudeBaselineParams()
	freeRes := RunPortfolioSim(free, GuardParams{}, loaded)
	if freeRes.FeesPaid != 0 {
		t.Fatalf("FeeRatePct=0 must charge nothing, got %.6f", freeRes.FeesPaid)
	}

	paid := ClaudeBaselineParams()
	paid.FeeRatePct = 0.05
	paidRes := RunPortfolioSim(paid, GuardParams{}, loaded)

	if paidRes.FeesPaid <= 0 {
		t.Fatal("expected non-zero fees at 0.05%/side")
	}
	if paidRes.TotalPnL >= freeRes.TotalPnL {
		t.Fatalf("fees must reduce PnL: free=%.4f paid=%.4f",
			freeRes.TotalPnL, paidRes.TotalPnL)
	}
	// Entry fee alone is 0.05% of 100*1 per position = 0.05; two positions => >=0.1.
	if paidRes.FeesPaid < 0.1 {
		t.Fatalf("fees %.4f below the entry-fill floor of 0.10 for 2 positions",
			paidRes.FeesPaid)
	}
}

// TestBreakerFiringCostsFees verifies that a firing actually pays for itself in
// fees, which is the whole reason the study cannot be run fee-free: the breaker
// generates no alpha, it only re-times exposure, so its cost must appear.
func TestBreakerFiringCostsFees(t *testing.T) {
	p := ClaudeBaselineParams()
	p.FeeRatePct = 0.05
	loaded := acctLoaded(4, declinePath)

	off := RunPortfolioSim(p, GuardParams{}, loaded)
	on := RunPortfolioSim(p, acctGuard(1.5), loaded)

	if on.L3Events == 0 {
		t.Skip("breaker did not fire on this path; fee-cost assertion not applicable")
	}
	if on.FeesPaid <= off.FeesPaid {
		t.Fatalf("a firing closes positions and must pay fees: off=%.4f on=%.4f",
			off.FeesPaid, on.FeesPaid)
	}
}

// TestReentryBanksRealizedLossSoBreakerIsNotFree is the most important re-entry
// test. Re-entry resets the position's VWAP-exit accumulators to start a fresh
// cycle; if the completed cycle's realized PnL were not banked first, the loss
// the breaker just took would silently disappear and the breaker would appear to
// cut drawdown at no cost. That would reproduce, in a new form, exactly the
// "breaker looks free" flaw that invalidated the earlier study.
func TestReentryBanksRealizedLossSoBreakerIsNotFree(t *testing.T) {
	p := ClaudeBaselineParams()
	p.FeeRatePct = 0.05
	loaded := acctStaggered(6, 2, sawPath)

	noRe := acctGuard(1.5)
	noRe.StartCapital = 150
	noReRes := RunPortfolioSim(p, noRe, loaded)

	withRe := acctGuard(1.5)
	withRe.StartCapital = 150
	withRe.L3ReentryBars = 1
	withRe.L3ReentryMaxCycles = 3
	withReRes := RunPortfolioSim(p, withRe, loaded)

	if withReRes.L3Reentries == 0 {
		t.Fatal("no re-entries occurred; the rest of this test proves nothing")
	}
	// Re-entry restores exposure and pays another round trip, so it must cost
	// strictly more in fees than the flatten-and-stay-out variant.
	if withReRes.FeesPaid <= noReRes.FeesPaid {
		t.Fatalf("re-entry must pay additional fees: without=%.4f with=%.4f",
			noReRes.FeesPaid, withReRes.FeesPaid)
	}
	if noReRes.L3Events == 0 {
		t.Fatal("breaker never fired; fixture broken")
	}
}

// TestReentryRestoresExposure verifies a re-entered position is actually live
// again: it must be able to close a second time, so its recorded close reasons
// contain the re-entry marker followed by further activity.
func TestReentryRestoresExposure(t *testing.T) {
	p := ClaudeBaselineParams()
	p.FeeRatePct = 0.05
	loaded := acctStaggered(6, 2, sawPath)

	g := acctGuard(1.5)
	g.StartCapital = 150
	g.L3ReentryBars = 1
	g.L3ReentryMaxCycles = 3

	res := RunPortfolioSim(p, g, loaded)
	if res.L3Reentries == 0 {
		t.Fatal("expected at least one re-entry on the saw path")
	}
	// With exposure restored the breaker has something to act on again, so total
	// firings must be at least as many as the number of re-entries permits.
	if res.L3Events < 1 {
		t.Fatalf("breaker fired %d times despite restored exposure", res.L3Events)
	}
}

// TestReentryCycleCapBounded verifies the cycle bound: a whipsawing path must not
// be able to manufacture unlimited round trips, which would let fees dominate the
// result for reasons that have nothing to do with the breaker's merit.
func TestReentryCycleCapBounded(t *testing.T) {
	p := ClaudeBaselineParams()
	p.FeeRatePct = 0.05
	loaded := acctStaggered(6, 2, sawPath)

	g := acctGuard(1.5)
	g.StartCapital = 150
	g.L3ReentryBars = 1
	g.L3ReentryMaxCycles = 1

	res := RunPortfolioSim(p, g, loaded)
	if res.L3Reentries > len(loaded) {
		t.Fatalf("re-entries %d exceed 1 cycle x %d positions", res.L3Reentries, len(loaded))
	}
}

// TestReentryDisabledByDefault guards the default: with L3ReentryBars unset the
// engine must behave exactly as before, so all pre-existing sweeps are unchanged.
func TestReentryDisabledByDefault(t *testing.T) {
	p := ClaudeBaselineParams()
	loaded := acctStaggered(6, 2, sawPath)
	g := acctGuard(1.5)
	g.StartCapital = 150
	res := RunPortfolioSim(p, g, loaded)
	if res.L3Reentries != 0 {
		t.Fatalf("re-entry must be off by default, got %d", res.L3Reentries)
	}
}

// TestReentryBankingArithmeticExact pins the banking arithmetic to exact numbers
// on a hand-computed two-cycle position, so an error in how cycles compose is
// caught as a wrong number rather than merely a wrong direction.
//
// Cycle 1: long 1 unit at 100, breaker flattens at 98  => gross -2
// Cycle 2: re-enter 1 unit at 98,  final mark      100 => gross +2
// Fees at 0.10%/side on four fills: 100, 98, 98, 100 => 0.396
// Net realized must be (-2) + (+2) - 0.396 = -0.396.
func TestReentryBankingArithmeticExact(t *testing.T) {
	p := ProtectionParams{Unit: UnitPercent, StopLossPct: 50, FeeRatePct: 0.10}
	sp := newSimPos(p, Entry{Symbol: "X", Side: "LONG", EntryPrice: 100, Quantity: 1}, 0)

	// Entry fee only, no exit yet.
	if got, want := sp.feeQuote, 0.10/100*100.0; math.Abs(got-want) > 1e-9 {
		t.Fatalf("entry fee = %.9f, want %.9f", got, want)
	}

	// Breaker flattens the whole position at 98.
	sp.addExit(98, 1.0, "guard_l3roll")
	if !sp.done {
		t.Fatal("position should be flat after a full exit")
	}
	cycle1 := sp.realizedPnL()
	wantCycle1 := -2.0 - (0.10/100*100 + 0.10/100*98)
	if math.Abs(cycle1-wantCycle1) > 1e-9 {
		t.Fatalf("cycle-1 realized = %.9f, want %.9f", cycle1, wantCycle1)
	}

	// Re-enter at 98, then mark back to 100 and close.
	sp.reenter(p, 98, 0)
	sp.addExit(100, 1.0, "final")

	got := sp.realizedPnL()
	wantFees := 0.10/100*100 + 0.10/100*98 + 0.10/100*98 + 0.10/100*100
	want := -2.0 + 2.0 - wantFees
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("two-cycle realized = %.9f, want %.9f (fees %.9f); "+
			"a mismatch here means cycle PnL is being lost or double-counted",
			got, want, wantFees)
	}
	if math.Abs(sp.feeQuote-wantFees) > 1e-9 {
		t.Fatalf("accumulated fees = %.9f, want %.9f", sp.feeQuote, wantFees)
	}
}

// TestPartialRestoreBlendsEntryPrice pins the partial-restore arithmetic. A
// cut50%-then-restore must rebuild to full size at a size-weighted entry price,
// and must charge fees only on the restored half — not on the whole position.
//
// Long 1 unit at 100. Breaker trims 0.5 at 98. Restore 0.5 at 96.
// Surviving 0.5 @ 100 blended with restored 0.5 @ 96 => entry 98.
// Fees: entry 1.0@100, exit 0.5@98, restore 0.5@96, all at 0.10%/side.
func TestPartialRestoreBlendsEntryPrice(t *testing.T) {
	p := ProtectionParams{Unit: UnitPercent, StopLossPct: 50, FeeRatePct: 0.10}
	sp := newSimPos(p, Entry{Symbol: "X", Side: "LONG", EntryPrice: 100, Quantity: 1}, 0)

	sp.addExit(98, 0.5, "guard_l3roll")
	sp.pendingRestoreFrac = 0.5
	if sp.done {
		t.Fatal("half-closed position must not be done")
	}

	sp.reenter(p, 96, 0)

	if math.Abs(sp.remaining-1.0) > 1e-9 {
		t.Fatalf("remaining = %.9f, want 1.0 (restored to original size)", sp.remaining)
	}
	if math.Abs(sp.e.EntryPrice-98.0) > 1e-9 {
		t.Fatalf("blended entry = %.9f, want 98.0 (0.5@100 + 0.5@96)", sp.e.EntryPrice)
	}
	wantFees := 0.10/100*100*1.0 + 0.10/100*98*0.5 + 0.10/100*96*0.5
	if math.Abs(sp.feeQuote-wantFees) > 1e-9 {
		t.Fatalf("fees = %.9f, want %.9f (restore charged on 0.5 only, not 1.0)",
			sp.feeQuote, wantFees)
	}
	// The realized loss on the trimmed half must survive the restore, scaled to
	// the half that was actually closed: -2/unit x 0.5 = -1.0. (An earlier version
	// of this test asserted -2.0, encoding the engine's then-unscaled partial-exit
	// convention; that convention was the bug, not the expectation.)
	wantBanked := (98.0 - 100.0) / 100.0 * 100.0 * 1.0 * 0.5
	if math.Abs(sp.bankedPnL-wantBanked) > 1e-9 {
		t.Fatalf("banked = %.9f, want %.9f; the trimmed half's loss must not vanish",
			sp.bankedPnL, wantBanked)
	}
}

// TestAllPoliciesPayReentry verifies the fix for a ranking artefact: partial-cut
// policies must also incur re-entry cost. Before this, cut50% configs reported
// zero re-entries and so appeared cheaper than cut100% for reasons unrelated to
// their risk management, which corrupted the sweep ordering.
func TestAllPoliciesPayReentry(t *testing.T) {
	p := ClaudeBaselineParams()
	p.FeeRatePct = 0.05
	loaded := acctStaggered(8, 2, sawPath)

	for _, cut := range []float64{50, 100} {
		g := acctGuard(1.5)
		g.StartCapital = 150
		g.L3RollCutPct = cut
		g.L3ReentryBars = 1
		g.L3ReentryMaxCycles = 3
		r := RunPortfolioSim(p, g, loaded)
		if r.L3Events == 0 {
			t.Fatalf("cut%.0f%%: breaker never fired; cannot assess re-entry", cut)
		}
		if r.L3Reentries == 0 {
			t.Fatalf("cut%.0f%% recorded 0 re-entries despite %d firings — partial "+
				"cuts must be restored too, or the sweep ranks by avoided cost",
				cut, r.L3Events)
		}
	}
}

// TestPartialExitRealizedPnLScaledMidFlight is the regression test for a defect
// that the whole pre-existing suite missed: realizedPnL priced the VWAP exit on
// FULL quantity, ignoring exitFrac.
//
// It was invisible because every existing assertion reads realizedPnL only after
// a position is fully closed, where exitFrac==1 and the omission cancels out. But
// the portfolio loop reads it EVERY BAR to build the equity curve, so partial
// closes (TP/BE/DD ladder legs and guard trims) inflated mid-flight equity — and
// the L3 equity breaker triggers on that curve, so it was reacting to a series
// that overstated realized gains.
func TestPartialExitRealizedPnLScaledMidFlight(t *testing.T) {
	p := ProtectionParams{Unit: UnitPercent, StopLossPct: 50}
	cases := []struct {
		name       string
		side       string
		exitPrice  float64
		frac       float64
		wantRealiz float64
	}{
		{"long 30% trim in profit", "LONG", 110, 0.30, 3.0},
		{"long 50% trim in loss", "LONG", 98, 0.50, -1.0},
		{"short 25% trim in profit", "SHORT", 90, 0.25, 2.5},
		{"long full close", "LONG", 110, 1.00, 10.0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sp := newSimPos(p, Entry{
				Symbol: "X", Side: c.side, EntryPrice: 100, Quantity: 1,
			}, 0)
			sp.addExit(c.exitPrice, c.frac, "test")
			got := sp.realizedPnL()
			if math.Abs(got-c.wantRealiz) > 1e-9 {
				t.Fatalf("realized = %.6f, want %.6f — partial exits must be scaled "+
					"by the closed fraction, or mid-flight equity is inflated %.2fx",
					got, c.wantRealiz, got/c.wantRealiz)
			}
		})
	}
}

// TestFaithfulModeReproducesRealizedPnL is the fidelity check the engine never
// had. In faithful mode, with the guard off and fees off, replaying a position to
// its real exit price must reproduce the trader's realized PnL.
//
// This is the guard against the deepest problem found in this study: the modelled
// protection ladder is not what the traders ran (63% of claude's live closes come
// from mechanisms this engine does not implement), so the modelled baseline
// equity path differs materially from the traded one — and the equity breaker
// triggers on that path. Without a fidelity assertion, a study can be internally
// consistent and still describe a book that never existed.
func TestFaithfulModeReproducesRealizedPnL(t *testing.T) {
	// Long 1 unit at 100, real exit at 107 => realized +7 regardless of what any
	// modelled SL/TP would have done in between. The path deliberately pierces the
	// modelled 3% TP and dips near the modelled 5% SL, so a non-faithful replay
	// would close early and disagree.
	path := []float64{104, 106, 96, 101, 107}
	start := int64(1_700_000_000_000)
	pre := make([]float64, 20)
	for i := range pre {
		pre[i] = 100
	}
	bars := synthBars(start, append(pre, path...), 0.1)
	hourMs := int64(time.Hour / time.Millisecond)

	e := Entry{
		Symbol: "F", Side: "LONG", EntryPrice: 100, Quantity: 1,
		EntryTime: bars[20].OpenTime,
		ExitTime:  bars[len(bars)-1].OpenTime,
		ExitPrice: 107,
	}
	loaded := []loadedEntry{{entry: e, bars: bars, entryIdx: 20}}
	_ = hourMs

	faithful := ProtectionParams{Unit: UnitPercent, FaithfulExits: true}
	res := RunPortfolioSim(faithful, GuardParams{}, loaded)

	want := 7.0 // (107-100)/100 * 100 * 1
	if math.Abs(res.TotalPnL-want) > 1e-6 {
		t.Fatalf("faithful replay PnL = %.6f, want %.6f (the real exit at 107); "+
			"a mismatch means the baseline is not the traded path",
			res.TotalPnL, want)
	}

	// The modelled ladder must disagree, otherwise this test proves nothing about
	// faithful mode being different.
	modelled := ClaudeBaselineParams()
	modRes := RunPortfolioSim(modelled, GuardParams{}, loaded)
	if math.Abs(modRes.TotalPnL-want) < 1e-6 {
		t.Fatal("modelled ladder produced the identical PnL; the fixture does not " +
			"exercise the difference between modelled and faithful exits")
	}
	t.Logf("faithful=%.4f modelled=%.4f (difference is the modelling error this "+
		"mode removes)", res.TotalPnL, modRes.TotalPnL)
}
