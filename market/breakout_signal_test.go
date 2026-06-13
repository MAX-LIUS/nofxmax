package market

import "testing"

func mkK(high, low, close float64) Kline { return Kline{High: high, Low: low, Close: close} }

func TestBreakoutSignal(t *testing.T) {
	// prior 3 bars: highs 10/11/12, lows 8/9/9 → hh=12, ll=8
	base := []Kline{mkK(10, 8, 9), mkK(11, 9, 10), mkK(12, 9, 11)}

	// last close 13 > hh 12 → long
	if g := BreakoutSignal(append(append([]Kline{}, base...), mkK(13, 11, 13)), 3); g != 1 {
		t.Fatalf("expected long(+1), got %d", g)
	}
	// last close 7 < ll 8 → short
	if g := BreakoutSignal(append(append([]Kline{}, base...), mkK(8, 7, 7)), 3); g != -1 {
		t.Fatalf("expected short(-1), got %d", g)
	}
	// last close 11 within [8,12] → none
	if g := BreakoutSignal(append(append([]Kline{}, base...), mkK(12, 10, 11)), 3); g != 0 {
		t.Fatalf("expected none(0), got %d", g)
	}
	// exactly equal to hh (12) → NOT a break (must be strictly >) → none
	if g := BreakoutSignal(append(append([]Kline{}, base...), mkK(12, 11, 12)), 3); g != 0 {
		t.Fatalf("expected none(0) at equal-to-hh, got %d", g)
	}
	// insufficient bars → 0
	if g := BreakoutSignal([]Kline{mkK(1, 1, 1)}, 3); g != 0 {
		t.Fatalf("expected 0 on insufficient bars, got %d", g)
	}
}
