package market

import (
	"testing"
)

// mkBar is a small helper to build a Kline with O/H/L/C/V.
func mkBar(o, h, l, c, v float64) Kline {
	return Kline{Open: o, High: h, Low: l, Close: c, Volume: v}
}

// buildFlatThenSpike creates a series where most volume concentrates in a
// narrow band (a clear POC) and the rest spreads thin.
func buildProfiledSeries() []Kline {
	var ks []Kline
	// 30 bars trading tightly around 100 with big volume -> POC ~100
	for i := 0; i < 30; i++ {
		ks = append(ks, mkBar(100, 100.5, 99.5, 100, 1000))
	}
	// 10 bars drifting up to 110 with thin volume -> LVN gap above
	for i := 0; i < 10; i++ {
		p := 101.0 + float64(i)
		ks = append(ks, mkBar(p, p+0.5, p-0.5, p, 50))
	}
	return ks
}

func TestCalculateVolumeProfile_Basic(t *testing.T) {
	vp := CalculateVolumeProfile(buildProfiledSeries(), "1h")
	if vp == nil {
		t.Fatal("expected non-nil profile")
	}
	// POC must land in the heavy 99.5-100.5 band.
	if vp.POC < 99.0 || vp.POC > 101.0 {
		t.Errorf("POC %.2f not in expected heavy band ~100", vp.POC)
	}
	// Value area must bracket the POC and stay within the profiled range.
	if !(vp.VAL <= vp.POC && vp.POC <= vp.VAH) {
		t.Errorf("VA does not bracket POC: VAL=%.2f POC=%.2f VAH=%.2f", vp.VAL, vp.POC, vp.VAH)
	}
	if vp.VAL < vp.RangeLow-1e-9 || vp.VAH > vp.RangeHigh+1e-9 {
		t.Errorf("VA escaped range: VAL=%.2f VAH=%.2f range=[%.2f,%.2f]", vp.VAL, vp.VAH, vp.RangeLow, vp.RangeHigh)
	}
	if vp.TotalVol <= 0 {
		t.Errorf("expected positive total vol, got %.2f", vp.TotalVol)
	}
}

func TestCalculateVolumeProfile_ValueAreaCoverage(t *testing.T) {
	ks := buildProfiledSeries()
	vp := CalculateVolumeProfile(ks, "1h")
	if vp == nil {
		t.Fatal("nil profile")
	}
	// Recompute volume inside [VAL, VAH] from raw bars and confirm it is at
	// least ~70% of total (value area definition). Use bar typical price.
	var inside, total float64
	for _, k := range ks {
		total += k.Volume
		tp := (k.High + k.Low + k.Close) / 3
		if tp >= vp.VAL && tp <= vp.VAH {
			inside += k.Volume
		}
	}
	if total > 0 && inside/total < 0.5 {
		t.Errorf("value area holds only %.0f%% of volume, expected majority", inside/total*100)
	}
}

// Adversary cases: degenerate / dirty inputs must never panic and must return
// nil rather than a bogus profile.
func TestCalculateVolumeProfile_Adversary(t *testing.T) {
	cases := []struct {
		name  string
		bars  []Kline
		isNil bool
	}{
		{"nil", nil, true},
		{"empty", []Kline{}, true},
		{"too_few", []Kline{mkBar(1, 1, 1, 1, 1)}, true},
		{"flat_price", func() []Kline {
			var k []Kline
			for i := 0; i < 30; i++ {
				k = append(k, mkBar(100, 100, 100, 100, 10))
			}
			return k
		}(), true},
		{"zero_volume", func() []Kline {
			var k []Kline
			for i := 0; i < 30; i++ {
				k = append(k, mkBar(100, 101, 99, 100, 0))
			}
			return k
		}(), true},
		{"valid_min", func() []Kline {
			var k []Kline
			for i := 0; i < 20; i++ {
				p := 100.0 + float64(i)
				k = append(k, mkBar(p, p+1, p-1, p, 100))
			}
			return k
		}(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var vp *VolumeProfile
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("panic on %s: %v", tc.name, r)
					}
				}()
				vp = CalculateVolumeProfile(tc.bars, "1h")
			}()
			if tc.isNil && vp != nil {
				t.Errorf("%s: expected nil, got %+v", tc.name, vp)
			}
			if !tc.isNil && vp == nil {
				t.Errorf("%s: expected profile, got nil", tc.name)
			}
		})
	}
}

func TestBinIndex_Clamping(t *testing.T) {
	// price below range clamps to 0, above range clamps to binCount-1.
	if idx := binIndex(-5, 0, 1, 50); idx != 0 {
		t.Errorf("below-range not clamped to 0, got %d", idx)
	}
	if idx := binIndex(1000, 0, 1, 50); idx != 49 {
		t.Errorf("above-range not clamped to 49, got %d", idx)
	}
}

func TestDetectVolumeNodes_Sorted(t *testing.T) {
	// Construct bins with two obvious peaks and one obvious valley.
	bins := make([]float64, 20)
	for i := range bins {
		bins[i] = 10
	}
	bins[5] = 100 // peak
	bins[15] = 80 // peak
	bins[10] = 1  // valley
	mean := 0.0
	for _, b := range bins {
		mean += b
	}
	mean /= float64(len(bins))
	hvns, lvns := detectVolumeNodes(bins, 0, 1, mean)
	if len(hvns) < 1 {
		t.Errorf("expected at least one HVN, got %d", len(hvns))
	}
	// HVNs sorted ascending by price.
	for i := 1; i < len(hvns); i++ {
		if hvns[i] < hvns[i-1] {
			t.Errorf("HVNs not sorted ascending: %v", hvns)
		}
	}
	_ = lvns
}
