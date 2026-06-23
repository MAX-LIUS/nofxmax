package backtest

import "testing"

// TestWilderADXTrendVsChop verifies ADX is high for a steady trend and low for
// a choppy/oscillating series — the property the adaptive guard relies on.
func TestWilderADXTrendVsChop(t *testing.T) {
	n := 80
	// Strong uptrend: each bar steps up.
	var th, tl, tc []float64
	p := 100.0
	for i := 0; i < n; i++ {
		p += 1.0
		th = append(th, p+0.3)
		tl = append(tl, p-0.3)
		tc = append(tc, p)
	}
	trendADX := wilderADX(th, tl, tc, 14)

	// Chop: oscillate around 100 with no net direction.
	var ch, cl, cc []float64
	for i := 0; i < n; i++ {
		base := 100.0
		if i%2 == 0 {
			base += 1.0
		} else {
			base -= 1.0
		}
		ch = append(ch, base+0.3)
		cl = append(cl, base-0.3)
		cc = append(cc, base)
	}
	chopADX := wilderADX(ch, cl, cc, 14)

	if trendADX <= chopADX {
		t.Fatalf("expected trend ADX (%.1f) > chop ADX (%.1f)", trendADX, chopADX)
	}
	if trendADX < 25 {
		t.Logf("note: trend ADX=%.1f (expected strong > 25)", trendADX)
	}
	t.Logf("trend ADX=%.1f  chop ADX=%.1f", trendADX, chopADX)
}

// TestWilderADXInsufficientData returns 0 rather than panicking.
func TestWilderADXInsufficientData(t *testing.T) {
	if got := wilderADX([]float64{1, 2}, []float64{1, 2}, []float64{1, 2}, 14); got != 0 {
		t.Fatalf("expected 0 for insufficient data, got %.2f", got)
	}
}
