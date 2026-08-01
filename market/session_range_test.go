package market

import (
	"testing"
	"time"
)

// mk builds a 5m kline series starting at the given UTC time.
func mkSeries(start time.Time, n int, barMin int, gen func(i int) (o, h, l, c float64)) []Kline {
	ks := make([]Kline, 0, n)
	for i := 0; i < n; i++ {
		o, h, l, c := gen(i)
		ot := start.Add(time.Duration(i*barMin) * time.Minute)
		ks = append(ks, Kline{
			OpenTime:  ot.UnixMilli(),
			Open:      o,
			High:      h,
			Low:       l,
			Close:     c,
			CloseTime: ot.Add(time.Duration(barMin) * time.Minute).UnixMilli(),
		})
	}
	return ks
}

func TestInferBarMinutes(t *testing.T) {
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	ks := mkSeries(start, 10, 5, func(i int) (float64, float64, float64, float64) {
		return 100, 101, 99, 100
	})
	if got := inferBarMinutes(ks); got != 5 {
		t.Fatalf("inferBarMinutes = %d, want 5", got)
	}
	if got := inferBarMinutes(ks[:2]); got != 0 {
		t.Fatalf("inferBarMinutes on short series = %d, want 0", got)
	}
	// A single gap must not move the median.
	ks[6].OpenTime = start.Add(200 * time.Minute).UnixMilli()
	if got := inferBarMinutes(ks); got != 5 {
		t.Fatalf("inferBarMinutes with gap = %d, want 5", got)
	}
}

func TestLatestSessionAnchor(t *testing.T) {
	cases := []struct {
		ref  time.Time
		want string
		hh   int
		mm   int
	}{
		{time.Date(2026, 7, 20, 0, 5, 0, 0, time.UTC), "ASIA", 0, 0},
		{time.Date(2026, 7, 20, 6, 59, 0, 0, time.UTC), "ASIA", 0, 0},
		{time.Date(2026, 7, 20, 7, 0, 0, 0, time.UTC), "EU", 7, 0},
		{time.Date(2026, 7, 20, 13, 29, 0, 0, time.UTC), "EU", 7, 0},
		{time.Date(2026, 7, 20, 13, 30, 0, 0, time.UTC), "US", 13, 30},
		{time.Date(2026, 7, 20, 23, 59, 0, 0, time.UTC), "US", 13, 30},
	}
	for _, c := range cases {
		name, ms := latestSessionAnchor(c.ref)
		if name != c.want {
			t.Fatalf("ref %s: session = %q, want %q", c.ref, name, c.want)
		}
		got := time.UnixMilli(ms).UTC()
		if got.Hour() != c.hh || got.Minute() != c.mm {
			t.Fatalf("ref %s: anchor = %02d:%02d, want %02d:%02d", c.ref, got.Hour(), got.Minute(), c.hh, c.mm)
		}
	}
}

func TestGradeExpansion(t *testing.T) {
	cases := map[float64]string{
		0: "", -5: "", 0.1: "compressed", 0.59: "compressed",
		0.6: "normal", 1.29: "normal", 1.3: "expanded",
		2.49: "expanded", 2.5: "exhausted", 9: "exhausted",
	}
	for ratio, want := range cases {
		if got := gradeExpansion(ratio); got != want {
			t.Fatalf("gradeExpansion(%.2f) = %q, want %q", ratio, got, want)
		}
	}
}

func TestRandomWalkRangeFrac(t *testing.T) {
	if got := randomWalkRangeFrac(0); got != 0 {
		t.Fatalf("randomWalkRangeFrac(0) = %v, want 0", got)
	}
	// A full day must normalize to 1.0.
	if got := randomWalkRangeFrac(1440); got < 0.999 || got > 1.001 {
		t.Fatalf("randomWalkRangeFrac(1440) = %v, want 1.0", got)
	}
	// 15m over a 1440m day => sqrt(1/96) ~= 0.1021.
	if got := randomWalkRangeFrac(15); got < 0.101 || got > 0.103 {
		t.Fatalf("randomWalkRangeFrac(15) = %v, want ~0.102", got)
	}
}

// TestExpansionRatioIsWindowNeutral is the point of the normalization: the same
// market state observed over a longer window must not change the grade merely
// because the window is longer.
func TestExpansionRatioIsWindowNeutral(t *testing.T) {
	// A random-walk-consistent range scales with sqrt(t), so a 60m window that
	// is 2x the 15m width is the same state and must produce the same ratio.
	atr := 100.0
	w15 := 0.102 * atr
	w60 := 0.204 * atr // sqrt(60/1440) = 0.204
	r15 := (w15 / atr) / randomWalkRangeFrac(15)
	r60 := (w60 / atr) / randomWalkRangeFrac(60)
	if d := r15 - r60; d > 0.01 || d < -0.01 {
		t.Fatalf("ratio not window-neutral: r15=%.3f r60=%.3f", r15, r60)
	}
	if gradeExpansion(r15) != gradeExpansion(r60) {
		t.Fatalf("grades differ across windows: %q vs %q", gradeExpansion(r15), gradeExpansion(r60))
	}
}

func TestCalculateSessionRangeBasic(t *testing.T) {
	// 4 days of 5m bars ending in the EU session of day 4.
	start := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	// 3 full days + 8h = 3*288 + 96 bars.
	n := 3*288 + 96
	ks := mkSeries(start, n, 5, func(i int) (float64, float64, float64, float64) {
		base := 100.0 + float64(i%20)*0.1
		return base, base + 0.5, base - 0.5, base
	})
	// Day-4 EU session starts at 07:00 => bar index 3*288 + 84.
	euIdx := 3*288 + 84
	// Give the first three EU bars (15m) a distinct 2.0-wide range.
	for i := euIdx; i < euIdx+3; i++ {
		ks[i].High = 106
		ks[i].Low = 104
		ks[i].Close = 105
	}
	// Bars after the window close above the OR high.
	for i := euIdx + 3; i < n; i++ {
		ks[i].High = 108
		ks[i].Low = 106.5
		ks[i].Close = 107
	}
	now := time.UnixMilli(ks[n-1].OpenTime).Add(5 * time.Minute).UnixMilli()

	sr := CalculateSessionRange(ks, ks, now)
	if sr == nil {
		t.Fatal("CalculateSessionRange returned nil")
	}
	if sr.Session != "EU" {
		t.Fatalf("Session = %q, want EU", sr.Session)
	}
	if !sr.Complete {
		t.Fatalf("Complete = false, want true (window %d min)", sr.ORWindowMinutes)
	}
	if sr.ORWindowMinutes != 15 {
		t.Fatalf("ORWindowMinutes = %d, want 15", sr.ORWindowMinutes)
	}
	if sr.ORHigh != 106 || sr.ORLow != 104 {
		t.Fatalf("OR = [%.2f, %.2f], want [104.00, 106.00]", sr.ORLow, sr.ORHigh)
	}
	if sr.Location != "above_or" {
		t.Fatalf("Location = %q, want above_or", sr.Location)
	}
	if sr.DailyATR <= 0 {
		t.Fatal("DailyATR not computed")
	}
	wantPct := 2.0 / sr.DailyATR * 100
	if d := sr.ExpansionPct - wantPct; d > 0.01 || d < -0.01 {
		t.Fatalf("ExpansionPct = %.3f, want %.3f", sr.ExpansionPct, wantPct)
	}
	wantRatio := wantPct / 100.0 / randomWalkRangeFrac(15)
	if d := sr.ExpansionRatio - wantRatio; d > 0.01 || d < -0.01 {
		t.Fatalf("ExpansionRatio = %.3f, want %.3f", sr.ExpansionRatio, wantRatio)
	}
	if sr.ExpansionGrade != gradeExpansion(sr.ExpansionRatio) {
		t.Fatalf("grade %q inconsistent with ratio %.2f", sr.ExpansionGrade, sr.ExpansionRatio)
	}
}

func TestCalculateSessionRangeExcludesUnclosedBar(t *testing.T) {
	start := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	// day-4 07:00 + 3 bars, the last one still open => only 10 min closed.
	n := 3*288 + 87
	ks := mkSeries(start, n, 5, func(i int) (float64, float64, float64, float64) {
		base := 100.0 + float64(i%20)*0.1
		return base, base + 0.5, base - 0.5, base
	})
	euIdx := 3*288 + 84
	ks[euIdx].High, ks[euIdx].Low = 106, 104
	ks[euIdx+1].High, ks[euIdx+1].Low = 105, 103
	// The final bar is still open at `now`; its extreme must be ignored.
	ks[n-1].High, ks[n-1].Low = 999, 1

	now := time.UnixMilli(ks[n-1].OpenTime).Add(1 * time.Minute).UnixMilli()
	sr := CalculateSessionRange(ks, ks, now)
	if sr == nil {
		t.Fatal("CalculateSessionRange returned nil")
	}
	if sr.ORHigh >= 999 || sr.ORLow <= 1 {
		t.Fatalf("unclosed bar leaked into OR: [%.2f, %.2f]", sr.ORLow, sr.ORHigh)
	}
	if sr.Complete {
		t.Fatal("Complete = true, want false for a developing window")
	}
	if sr.ORWindowMinutes != 10 {
		t.Fatalf("ORWindowMinutes = %d, want 10", sr.ORWindowMinutes)
	}
}

func TestCalculateSessionRangeNilGuards(t *testing.T) {
	if sr := CalculateSessionRange(nil, nil, 0); sr != nil {
		t.Fatal("nil klines should yield nil")
	}
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	// 4h bars: too coarse for an opening window.
	coarse := mkSeries(start, 30, 240, func(i int) (float64, float64, float64, float64) {
		return 100, 101, 99, 100
	})
	if sr := CalculateSessionRange(coarse, coarse, 0); sr != nil {
		t.Fatalf("4h series should yield nil, got %+v", sr)
	}
	// Single day of 5m bars: no completed days => no daily ATR, but the OR is
	// still describable.
	oneDay := mkSeries(start, 60, 5, func(i int) (float64, float64, float64, float64) {
		return 100, 101, 99, 100
	})
	sr := CalculateSessionRange(oneDay, oneDay, 0)
	if sr == nil {
		t.Fatal("one-day series should still yield an OR")
	}
	if sr.DailyATR != 0 || sr.ExpansionGrade != "" {
		t.Fatalf("expected no ATR/grade without completed days, got atr=%.4f grade=%q", sr.DailyATR, sr.ExpansionGrade)
	}
}
