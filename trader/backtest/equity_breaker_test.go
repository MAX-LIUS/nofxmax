package backtest

import "testing"

// helper: a minimal open simPos (long) with the given remaining fraction.
func l3pos(entry, qty float64, remaining float64) *simPos {
	sp := &simPos{
		e:         Entry{Side: "LONG", EntryPrice: entry, Quantity: qty},
		isLong:    true,
		remaining: remaining,
	}
	return sp
}

// l3posVel builds a position whose pnl history changes at ~velPerBar profit%/bar
// over `bars` samples: velPerBar<0 = counter-trend (deteriorating), >0 = trend.
func l3posVel(entry, qty, remaining, velPerBar float64, bars int) *simPos {
	sp := l3pos(entry, qty, remaining)
	for i := 0; i < bars; i++ {
		sp.pnlHist = append(sp.pnlHist, velPerBar*float64(i))
	}
	return sp
}

// equity drawdown below the shallowest tier and no early trigger -> no fire.
func TestEquityBreaker_NoTrigger(t *testing.T) {
	g := GuardParams{
		L3Enabled: true,
		L3Tiers: []L3Tier{
			{DrawdownPct: 8, ClosePct: 25},
			{DrawdownPct: 15, ClosePct: 40},
		},
	}
	st := &guardState{}
	gps := []guardPos{{pos: l3posVel(100, 1, 1.0, -1, 8), price: 100}}
	// peak 1000, cur 980 => 2% DD < 8%
	n, closed := applyEquityBreaker(g, gps, 980, 1000, st)
	if n != 0 || closed != 0 {
		t.Fatalf("expected no fire at 2%% DD, got n=%d closed=%.4f", n, closed)
	}
}

// a fast plunge past several tiers fires the DEEPEST applicable rung once; a
// counter-trend position is cut at the tier's full ratio.
func TestEquityBreaker_DeepestTierWins(t *testing.T) {
	g := GuardParams{
		L3Enabled:       true,
		L3TrendKeepMult: 0,
		L3Tiers: []L3Tier{
			{DrawdownPct: 8, ClosePct: 25},
			{DrawdownPct: 15, ClosePct: 40},
			{DrawdownPct: 25, ClosePct: 60},
		},
	}
	st := &guardState{}
	p := l3posVel(100, 1, 1.0, -1, 8) // declining -> counter-trend
	gps := []guardPos{{pos: p, price: 100}}
	// peak 1000, cur 700 => 30% DD -> deepest tier (25%/60%) fires.
	n, closed := applyEquityBreaker(g, gps, 700, 1000, st)
	if n != 1 {
		t.Fatalf("expected 1 trim, got %d", n)
	}
	if closed < 0.59 || closed > 0.61 {
		t.Fatalf("expected ~0.60 closed (deepest tier), got %.4f", closed)
	}
	if !st.l3TierFiredDD[2] {
		t.Fatalf("expected deepest tier latched")
	}
}

// counter-trend positions are cut at full ratio; trend-aligned positions are
// preserved (keepMult=0) even though both are open when the breaker fires.
func TestEquityBreaker_CounterTrendFirst(t *testing.T) {
	g := GuardParams{
		L3Enabled:       true,
		L3TrendKeepMult: 0, // fully preserve trend-aligned
		L3VelWindow:     6,
		L3Tiers:         []L3Tier{{DrawdownPct: 10, ClosePct: 50}},
	}
	st := &guardState{}
	counter := l3posVel(100, 1, 1.0, -1.5, 8) // deteriorating
	trend := l3posVel(100, 1, 1.0, +1.5, 8)   // improving
	gps := []guardPos{{pos: counter, price: 100}, {pos: trend, price: 100}}

	n, _ := applyEquityBreaker(g, gps, 850, 1000, st) // 15% DD -> tier fires
	if n != 1 {
		t.Fatalf("expected only the counter-trend position trimmed, got %d trims", n)
	}
	if counter.remaining > 0.51 || counter.remaining < 0.49 {
		t.Fatalf("counter-trend should be cut 50%%, remaining=%.3f", counter.remaining)
	}
	if trend.remaining != 1.0 {
		t.Fatalf("trend-aligned should be preserved, remaining=%.3f", trend.remaining)
	}
}

// trend-aligned positions are trimmed lightly when keepMult>0.
func TestEquityBreaker_TrendKeepMult(t *testing.T) {
	g := GuardParams{
		L3Enabled:       true,
		L3TrendKeepMult: 0.4, // trim trend-aligned at 40% of tier ratio
		L3VelWindow:     6,
		L3Tiers:         []L3Tier{{DrawdownPct: 10, ClosePct: 50}},
	}
	st := &guardState{}
	trend := l3posVel(100, 1, 1.0, +1.5, 8)
	gps := []guardPos{{pos: trend, price: 100}}

	n, _ := applyEquityBreaker(g, gps, 850, 1000, st)
	if n != 1 {
		t.Fatalf("expected trend position lightly trimmed, got %d", n)
	}
	// 50% * 0.4 = 20% cut -> remaining 0.8
	if trend.remaining > 0.81 || trend.remaining < 0.79 {
		t.Fatalf("trend-aligned should be cut 20%%, remaining=%.3f", trend.remaining)
	}
}

// early rate-based trigger fires the shallowest tier before its DD threshold is
// reached, when equity is plunging fast past the floor.
func TestEquityBreaker_EarlyVelocityTrigger(t *testing.T) {
	g := GuardParams{
		L3Enabled:          true,
		L3TrendKeepMult:    0,
		L3VelWindow:        6,
		L3EquityVelTrigger: 3, // 3%/tick drop arms early
		L3EarlyFloorPct:    2, // need >=2% DD first
		L3Tiers:            []L3Tier{{DrawdownPct: 10, ClosePct: 30}},
	}
	st := &guardState{hasPrevEquity: true, prevEquity: 1000}
	counter := l3posVel(100, 1, 1.0, -1, 8)
	gps := []guardPos{{pos: counter, price: 100}}

	// cur 950 => 5% DD (< 10% tier) but equity fell 50/1000 = 5%/tick >= 3%.
	n, _ := applyEquityBreaker(g, gps, 950, 1000, st)
	if n != 1 {
		t.Fatalf("expected early velocity trigger to fire, got %d", n)
	}
}

// once a tier fires it does not re-fire on the next tick (ratchet), until equity
// makes a new high-water mark.
func TestEquityBreaker_RatchetAndRearm(t *testing.T) {
	g := GuardParams{
		L3Enabled:       true,
		L3TrendKeepMult: 0,
		L3Tiers:         []L3Tier{{DrawdownPct: 8, ClosePct: 25}},
	}
	st := &guardState{}
	p := l3posVel(100, 1, 1.0, -1, 8)
	gps := []guardPos{{pos: p, price: 100}}

	n, _ := applyEquityBreaker(g, gps, 900, 1000, st)
	if n != 1 {
		t.Fatalf("expected first fire, got %d", n)
	}
	n, _ = applyEquityBreaker(g, gps, 900, 1000, st)
	if n != 0 {
		t.Fatalf("expected no re-fire while latched, got %d", n)
	}
	n, _ = applyEquityBreaker(g, gps, 990, 1100, st)
	if n != 1 {
		t.Fatalf("expected re-arm + fire after new high-water, got %d", n)
	}
}

// V-bounce protection: a single down-wick bar must NOT fire the early trigger
// when confirm-bars >= 2; a sustained plunge across >=2 bars does fire.
func TestEquityBreaker_EarlyConfirmFiltersWick(t *testing.T) {
	g := GuardParams{
		L3Enabled:          true,
		L3TrendKeepMult:    0,
		L3VelWindow:        6,
		L3EquityVelTrigger: 3,
		L3EarlyFloorPct:    2,
		L3EarlyConfirmBars: 2, // need 2 consecutive plunge bars
		L3Tiers:            []L3Tier{{10, 30}},
	}
	st := &guardState{hasPrevEquity: true, prevEquity: 1000}
	gps := []guardPos{{pos: l3posVel(100, 1, 1.0, -1, 8), price: 100}}

	// Bar 1: equity 950 => 5% DD, vel -5%/bar. Streak=1 < confirm=2 -> NO fire.
	if n, _ := applyEquityBreaker(g, gps, 950, 1000, st); n != 0 {
		t.Fatalf("single wick should not fire with confirm=2, got %d", n)
	}
	// Bar 2: equity 905 => still plunging. Streak=2 == confirm -> fires.
	if n, _ := applyEquityBreaker(g, gps, 905, 1000, st); n != 1 {
		t.Fatalf("sustained 2-bar plunge should fire, got %d", n)
	}
}

// A wick that bounces resets the streak so no early fire occurs.
func TestEquityBreaker_WickBounceResetsStreak(t *testing.T) {
	g := GuardParams{
		L3Enabled:          true,
		L3TrendKeepMult:    0,
		L3VelWindow:        6,
		L3EquityVelTrigger: 3,
		L3EarlyFloorPct:    2,
		L3EarlyConfirmBars: 2,
		L3Tiers:            []L3Tier{{10, 30}},
	}
	st := &guardState{hasPrevEquity: true, prevEquity: 1000}
	gps := []guardPos{{pos: l3posVel(100, 1, 1.0, -1, 8), price: 100}}

	if n, _ := applyEquityBreaker(g, gps, 950, 1000, st); n != 0 {
		t.Fatalf("bar1 should not fire, got %d", n)
	}
	// Bounce up -> streak resets.
	if n, _ := applyEquityBreaker(g, gps, 985, 1000, st); n != 0 {
		t.Fatalf("bounce bar should not fire, got %d", n)
	}
	// Single plunge again -> streak=1 < 2, still no fire.
	if n, _ := applyEquityBreaker(g, gps, 940, 1000, st); n != 0 {
		t.Fatalf("post-bounce single plunge should not fire, got %d", n)
	}
}

// disabled or empty tiers -> no-op.
func TestEquityBreaker_Disabled(t *testing.T) {
	st := &guardState{}
	gps := []guardPos{{pos: l3pos(100, 1, 1.0), price: 100}}
	if n, _ := applyEquityBreaker(GuardParams{L3Enabled: false}, gps, 500, 1000, st); n != 0 {
		t.Fatalf("disabled should no-op, got %d", n)
	}
	if n, _ := applyEquityBreaker(GuardParams{L3Enabled: true}, gps, 500, 1000, st); n != 0 {
		t.Fatalf("empty tiers should no-op, got %d", n)
	}
}
