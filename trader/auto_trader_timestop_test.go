package trader

import "testing"

func TestShouldTimeStop(t *testing.T) {
	const h = 3600 * 1000
	now := int64(1000 * h) // arbitrary
	// hours=24, lossPct=-1.5
	cases := []struct {
		name                    string
		side                    string
		entry, mark             float64
		createdMsAgo            float64 // hours ago
		wantTrigger             bool
	}{
		// ETH SHORT held 32.9h, price +2.37% (loss for short) → trigger
		{"short held 33h in loss", "short", 1631.21, 1669.94, 33, true},
		// winner held long (SPCX +9% 34h) → NOT trigger (loss condition fails)
		{"long held 34h in profit", "long", 100, 109, 34, false},
		// XAG short 65h but +1% profit → NOT trigger
		{"short held 65h in profit", "short", 100, 99, 65, false},
		// loss but held only 6.5h (< 24h) → NOT trigger (too early)
		{"long loss but only 6.5h", "long", 100, 95, 6.5, false},
		// held 24h but only -1% loss (better than -1.5%) → NOT trigger
		{"held 24h tiny loss -1pct", "long", 100, 99, 24, false},
		// held 24h and -1.5% exactly → trigger (boundary)
		{"held 24h loss -1.5pct boundary", "long", 100, 98.5, 24, true},
		// held 23.9h and -5% → NOT trigger (just under time)
		{"held 23.9h big loss", "long", 100, 95, 23.9, false},
	}
	for _, c := range cases {
		created := now - int64(c.createdMsAgo*float64(h))
		got, _, _ := shouldTimeStop(c.side, c.entry, c.mark, created, now, 24, -1.5)
		if got != c.wantTrigger {
			t.Errorf("%s: got trigger=%v want %v", c.name, got, c.wantTrigger)
		}
	}
}

func TestShouldTimeStopDisabled(t *testing.T) {
	const h = 3600 * 1000
	now := int64(1000 * h)
	created := now - int64(48*h)
	// disabled when hours<=0 or lossPct>=0
	if g, _, _ := shouldTimeStop("long", 100, 90, created, now, 0, -1.5); g {
		t.Fatal("expected disabled when hours=0")
	}
	if g, _, _ := shouldTimeStop("long", 100, 90, created, now, 24, 0); g {
		t.Fatal("expected disabled when lossPct=0")
	}
}
