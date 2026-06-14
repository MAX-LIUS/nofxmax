package trader

import "testing"

func TestShouldMaxHoldClose(t *testing.T) {
	const h = 3600 * 1000
	now := int64(1000 * h)
	// hours=18, exemptPct=2.0
	cases := []struct {
		name         string
		side         string
		entry, mark  float64
		createdMsAgo float64 // hours ago
		wantTrigger  bool
	}{
		// flat grinder held 20h, ~0% pnl → trigger (time-stop would miss this)
		{"long flat grinder 20h", "long", 100, 100, 20, true},
		// dust/break-even tail held 30h, +0.1% → trigger
		{"short dust tail 30h flat", "short", 100, 99.9, 30, true},
		// slow-bleed loss held 19h, -1% → trigger (below exempt)
		{"long small loss 19h", "long", 100, 99, 19, true},
		// profitable runner held 24h, +3% → NOT trigger (>= exempt 2%)
		{"long runner 24h +3pct", "long", 100, 103, 24, false},
		// runner exactly at +2% exempt boundary → NOT trigger
		{"long runner 24h +2pct boundary", "long", 100, 102, 24, false},
		// just under exempt, +1.9% held 24h → trigger
		{"long 24h +1.9pct under exempt", "long", 100, 101.9, 24, true},
		// not held long enough: 17.9h flat → NOT trigger
		{"long flat 17.9h too early", "long", 100, 100, 17.9, false},
		// boundary: held exactly 18h, flat → trigger
		{"long flat 18h boundary", "long", 100, 100, 18, true},
		// short runner +3% (price down 3%) held 40h → NOT trigger
		{"short runner 40h profit", "short", 100, 97, 40, false},
	}
	for _, c := range cases {
		created := now - int64(c.createdMsAgo*float64(h))
		got, _, _ := shouldMaxHoldClose(c.side, c.entry, c.mark, created, now, 18, 2.0)
		if got != c.wantTrigger {
			t.Errorf("%s: got trigger=%v want %v", c.name, got, c.wantTrigger)
		}
	}
}

func TestShouldMaxHoldCloseDisabled(t *testing.T) {
	const h = 3600 * 1000
	now := int64(1000 * h)
	created := now - int64(48*h)
	// disabled when hours<=0
	if g, _, _ := shouldMaxHoldClose("long", 100, 90, created, now, 0, 2.0); g {
		t.Fatal("expected disabled when hours=0")
	}
	// guards: zero/negative prices or created time
	if g, _, _ := shouldMaxHoldClose("long", 0, 90, created, now, 18, 2.0); g {
		t.Fatal("expected no trigger when entry<=0")
	}
	if g, _, _ := shouldMaxHoldClose("long", 100, 0, created, now, 18, 2.0); g {
		t.Fatal("expected no trigger when mark<=0")
	}
	if g, _, _ := shouldMaxHoldClose("long", 100, 90, 0, now, 18, 2.0); g {
		t.Fatal("expected no trigger when createdTime<=0")
	}
}

// exemptPct=0 means only strictly-profitable runners (>0) are spared; flat/loss are cut.
func TestShouldMaxHoldCloseZeroExempt(t *testing.T) {
	const h = 3600 * 1000
	now := int64(1000 * h)
	created := now - int64(20*h)
	// flat 0% with exempt 0 → trigger (0 < 0 is false... boundary: pnl>=exempt spares it)
	if g, _, _ := shouldMaxHoldClose("long", 100, 100, created, now, 18, 0); g {
		t.Fatal("flat pnl=0 with exempt=0 should be spared (pnl>=exempt)")
	}
	// tiny loss -0.5% with exempt 0 → trigger
	if g, _, _ := shouldMaxHoldClose("long", 100, 99.5, created, now, 18, 0); !g {
		t.Fatal("tiny loss with exempt=0 should trigger")
	}
}
