package backtest

import "testing"

// breadthGuard builds a breadth-breaker GuardParams (pnl% retrace mode).
func breadthGuard(minPos int, frac, giveback float64) GuardParams {
	return GuardParams{
		Enabled: true, BreadthEnabled: true,
		BreadthMinPos: minPos, BreadthFrac: frac,
		BreadthGivebackPct: giveback, BreadthLoserCutPct: 100,
		L3VelWindow: 6,
	}
}

// l3posPeak builds a position with a known peak profit% (no velocity history,
// so retracement is decided purely by peak-to-current giveback / ATR).
func l3posPeak(entry, qty, remaining, peakPct float64) *simPos {
	sp := l3pos(entry, qty, remaining)
	sp.peakPnlPct = peakPct
	return sp
}

// Below quorum: even if everything is retracing, too few positions => no fire.
func TestBreadth_NoQuorum(t *testing.T) {
	g := breadthGuard(3, 0.6, 2)
	st := &guardState{}
	a := l3posPeak(100, 1, 1.0, 0)
	b := l3posPeak(100, 1, 1.0, 0)
	gps := []guardPos{{pos: a, price: 90}, {pos: b, price: 90}} // -10% each
	n, _ := applyBreadthBreaker(g, gps, st)
	if n != 0 {
		t.Fatalf("below quorum (2<3) must not fire, got %d", n)
	}
}

// Minority retrace: quorum met but < frac retracing => no fire (single-symbol noise).
func TestBreadth_MinorityNoFire(t *testing.T) {
	g := breadthGuard(3, 0.6, 2)
	st := &guardState{}
	p1 := l3posPeak(100, 1, 1.0, 5) // peak 5, cur 0 => giveback 5 >= 2 => retracing
	p2 := l3posPeak(100, 1, 1.0, 0)
	p3 := l3posPeak(100, 1, 1.0, 0)
	p4 := l3posPeak(100, 1, 1.0, 0)
	gps := []guardPos{
		{pos: p1, price: 100}, {pos: p2, price: 100},
		{pos: p3, price: 100}, {pos: p4, price: 100},
	}
	n, _ := applyBreadthBreaker(g, gps, st)
	if n != 0 {
		t.Fatalf("minority retrace (1/4 < 0.6) must not fire, got %d", n)
	}
}

// Majority retrace: fires, cuts ONLY losing+retracing positions; winners untouched.
func TestBreadth_MajorityCutsLosersKeepsWinners(t *testing.T) {
	g := breadthGuard(3, 0.6, 2)
	st := &guardState{}
	// 4 positions, 3 retracing (>=2% giveback). 3/4=0.75 >= 0.6 => fire.
	loser1 := l3posPeak(100, 1, 1.0, 1)  // peak 1, cur -8 => retracing + losing => CUT
	loser2 := l3posPeak(100, 1, 1.0, 0)  // peak 0, cur -5 => retracing + losing => CUT
	winner := l3posPeak(100, 1, 1.0, 12) // peak 12, cur +8 => retracing but PROFIT => KEEP
	flat := l3posPeak(100, 1, 1.0, 0)    // peak 0, cur 0 => not retracing
	gps := []guardPos{
		{pos: loser1, price: 92}, {pos: loser2, price: 95},
		{pos: winner, price: 108}, {pos: flat, price: 100},
	}
	n, closed := applyBreadthBreaker(g, gps, st)
	if n != 2 {
		t.Fatalf("expected 2 losers cut, got %d (closed=%.3f)", n, closed)
	}
	if loser1.remaining > 1e-9 || loser2.remaining > 1e-9 {
		t.Fatalf("both losers should be fully cut: l1=%.3f l2=%.3f", loser1.remaining, loser2.remaining)
	}
	if winner.remaining != 1.0 {
		t.Fatalf("winner must be untouched (rides BE), remaining=%.3f", winner.remaining)
	}
	if flat.remaining != 1.0 {
		t.Fatalf("flat non-retracing position must be untouched, remaining=%.3f", flat.remaining)
	}
}

// A winning position that is retracing is NOT cut — only losers are.
func TestBreadth_RetracingWinnerNotCut(t *testing.T) {
	g := breadthGuard(3, 0.6, 2)
	st := &guardState{}
	w1 := l3posPeak(100, 1, 1.0, 10)
	w2 := l3posPeak(100, 1, 1.0, 10)
	w3 := l3posPeak(100, 1, 1.0, 10)
	gps := []guardPos{
		{pos: w1, price: 105}, {pos: w2, price: 105}, {pos: w3, price: 105}, // +5%, gave back 5
	}
	n, _ := applyBreadthBreaker(g, gps, st)
	if n != 0 {
		t.Fatalf("retracing winners must not be cut, got %d", n)
	}
	if w1.remaining != 1.0 || w2.remaining != 1.0 || w3.remaining != 1.0 {
		t.Fatalf("all winners must ride BE untouched")
	}
}

// ATR mode: retracement measured in ATR units from peak.
func TestBreadth_ATRMode(t *testing.T) {
	g := breadthGuard(3, 0.6, 0)
	g.BreadthUseATR = true
	g.BreadthATRMult = 1.5
	st := &guardState{}
	// atrPct 2%. peak +1%, cur -4% => adverse-from-peak = 5% = 2.5 ATR >= 1.5 => retracing+loser.
	mk := func(peak float64) *simPos { p := l3posPeak(100, 1, 1.0, peak); p.atrPct = 2.0; return p }
	a := mk(1)
	b := mk(1)
	c := mk(1)
	gps := []guardPos{{pos: a, price: 96}, {pos: b, price: 96}, {pos: c, price: 96}}
	n, _ := applyBreadthBreaker(g, gps, st)
	if n != 3 {
		t.Fatalf("3 losers at 2.5 ATR adverse must all be cut, got %d", n)
	}
}

