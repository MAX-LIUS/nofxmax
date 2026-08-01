package trader

import (
	"errors"
	"math"
	"testing"
)

// fakeSizer models a linear USDT swap: contracts = quantity / ctVal, clamped up
// to minSz, then rounded to the lot grid the way OKX formatSize does (Go's
// banker's rounding via %.0f when lotSz >= 1).
type fakeSizer struct {
	ctVal float64
	minSz float64
	fail  bool
}

func (f *fakeSizer) ProtectionContractsForQuantity(symbol string, quantity float64) (float64, error) {
	if f.fail {
		return 0, errors.New("instrument unavailable")
	}
	sz := quantity / f.ctVal
	if sz < f.minSz {
		sz = f.minSz
	}
	if f.minSz >= 1 {
		// %.0f is round-half-to-even.
		return math.RoundToEven(sz), nil
	}
	return sz, nil
}

// zecLadder is the live Claude-R / GPT / BN configuration: 20/18/15/12%.
func zecLadder() []ProtectionOrder {
	return []ProtectionOrder{
		{Price: 470.745407, CloseRatioPct: 20},
		{Price: 468.659866, CloseRatioPct: 18},
		{Price: 467.140000, CloseRatioPct: 15},
		{Price: 465.620000, CloseRatioPct: 12},
	}
}

// TestLadderWouldOversellBoundary walks position size across the boundary. ZEC
// has ctVal 0.01 and minSz 1, so position contracts = quantity / 0.01.
//
// Below 5 contracts the 20% tier itself needs clamping, which is the signal that
// the whole ladder is in the all-drop region and must go to the collapse
// fallback. At and above 5 the largest tier stands on its own and clamping the
// small tiers cannot exceed the position.
func TestLadderWouldOversellBoundary(t *testing.T) {
	sizer := &fakeSizer{ctVal: 0.01, minSz: 1}
	cases := []struct {
		contracts float64
		want      bool
		why       string
	}{
		{1, true, "4 tiers clamp to 1 each = 4 > 1"},
		{2, true, "4 > 2"},
		{3, true, "4 > 3"},
		{4, true, "1+1+1+1 = 4 equals the position, and a 65% ladder must not close 100%"},
		{5, false, "1+1+1+1 = 4 <= 5"},
		{8, false, "the live ZEC case: 2+1+1+1 = 5 <= 8"},
		{20, false, "4+4+3+2 = 13 <= 20"},
		{100, false, "20+18+15+12 = 65 <= 100"},
	}
	for _, c := range cases {
		quantity := c.contracts * 0.01
		got := ladderWouldOversell(sizer, "ZECUSDT", quantity, zecLadder())
		if got != c.want {
			t.Errorf("position %.0f contracts: oversell=%v want %v (%s)", c.contracts, got, c.want, c.why)
		}
	}
}

// TestLadderWouldOversellLiveZEC pins the exact position that motivated the
// change: 8.276 contracts, whose 12% tier is 0.993 and used to be discarded.
func TestLadderWouldOversellLiveZEC(t *testing.T) {
	sizer := &fakeSizer{ctVal: 0.01, minSz: 1}
	if ladderWouldOversell(sizer, "ZECUSDT", 0.08276, zecLadder()) {
		t.Fatal("live ZEC position must NOT trip the guard; the 12% tier has to be placed")
	}
}

// TestLadderWouldOversellFailsOpen documents the deliberate direction of every
// uncertain answer: keep prior behaviour rather than drop a good ladder.
func TestLadderWouldOversellFailsOpen(t *testing.T) {
	sizer := &fakeSizer{ctVal: 0.01, minSz: 1}
	if ladderWouldOversell(nil, "ZECUSDT", 0.01, zecLadder()) {
		t.Error("nil sizer must fail open")
	}
	if ladderWouldOversell(sizer, "ZECUSDT", 0.01, nil) {
		t.Error("empty ladder must fail open")
	}
	if ladderWouldOversell(sizer, "ZECUSDT", 0, zecLadder()) {
		t.Error("zero quantity must fail open")
	}
	if ladderWouldOversell(&fakeSizer{ctVal: 0.01, minSz: 1, fail: true}, "ZECUSDT", 0.01, zecLadder()) {
		t.Error("sizer error must fail open")
	}
}

// TestLadderWouldOversellFineGrainedInstrument checks an instrument whose
// minimum is far below a single tier: nothing clamps, so nothing can oversell,
// at any position size.
func TestLadderWouldOversellFineGrainedInstrument(t *testing.T) {
	sizer := &fakeSizer{ctVal: 0.1, minSz: 0.01}
	for _, contracts := range []float64{0.1, 1, 5, 50} {
		if ladderWouldOversell(sizer, "ETHUSDT", contracts*0.1, zecLadder()) {
			t.Errorf("fine-grained instrument at %.2f contracts must never oversell", contracts)
		}
	}
}

// TestLadderWouldOversellSingleTierNeverOversells covers the SL side, which is
// one 100% tier: clamping a 100% tier can only ever equal the position.
func TestLadderWouldOversellSingleTierNeverOversells(t *testing.T) {
	sizer := &fakeSizer{ctVal: 0.01, minSz: 1}
	full := []ProtectionOrder{{Price: 457.856, CloseRatioPct: 100}}
	for _, contracts := range []float64{1, 2, 4, 8, 37} {
		if ladderWouldOversell(sizer, "ZECUSDT", contracts*0.01, full) {
			t.Errorf("single 100%% tier at %.0f contracts must never oversell", contracts)
		}
	}
}

// TestLadderWouldOversellEqualityDependsOnIntent pins the asymmetry at
// total == position: a partial ladder reaching 100% of the position by clamping
// has lost its runner and must collapse, while a ladder that intends to close
// 100% is doing exactly what it was configured to do.
func TestLadderWouldOversellEqualityDependsOnIntent(t *testing.T) {
	sizer := &fakeSizer{ctVal: 0.01, minSz: 1}
	// Two 50% tiers on a 2-contract position: 1+1 = 2 == position, intent 100%.
	fullSplit := []ProtectionOrder{
		{Price: 470, CloseRatioPct: 50},
		{Price: 469, CloseRatioPct: 50},
	}
	if ladderWouldOversell(sizer, "ZECUSDT", 0.02, fullSplit) {
		t.Error("a ladder intending 100% must be allowed to equal the position")
	}
	// Same arithmetic, but the ladder only intends 65%.
	partial := []ProtectionOrder{
		{Price: 470, CloseRatioPct: 35},
		{Price: 469, CloseRatioPct: 30},
	}
	if !ladderWouldOversell(sizer, "ZECUSDT", 0.02, partial) {
		t.Error("a 65% ladder that would close 100% must collapse instead")
	}
}

// TestLadderWouldOversellClaudeLadder covers the other live shape, claude's
// 20/15/15/15, whose max ratio is the same 20% so the boundary matches.
func TestLadderWouldOversellClaudeLadder(t *testing.T) {
	sizer := &fakeSizer{ctVal: 0.01, minSz: 1}
	ladder := []ProtectionOrder{
		{Price: 470, CloseRatioPct: 20},
		{Price: 469, CloseRatioPct: 15},
		{Price: 468, CloseRatioPct: 15},
		{Price: 467, CloseRatioPct: 15},
	}
	if !ladderWouldOversell(sizer, "ZECUSDT", 0.02, ladder) {
		t.Error("2-contract position must trip the guard")
	}
	if ladderWouldOversell(sizer, "ZECUSDT", 0.08, ladder) {
		t.Error("8-contract position must not trip the guard")
	}
}
