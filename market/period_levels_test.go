package market

import (
	"testing"
	"time"
)

// barAt builds a Kline anchored at a specific UTC time with given H/L.
func barAt(t time.Time, hi, lo float64) Kline {
	return Kline{
		OpenTime: t.UnixMilli(),
		High:     hi,
		Low:      lo,
		Close:    (hi + lo) / 2,
		Open:     (hi + lo) / 2,
		Volume:   100,
	}
}

func TestCalculatePeriodLevels_DayAndWeek(t *testing.T) {
	base := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC) // Monday
	var ks []Kline
	// Prev week (Mon-Fri of week before): put a clear high/low.
	prevWeekStart := base.AddDate(0, 0, -7)
	for d := 0; d < 5; d++ {
		day := prevWeekStart.AddDate(0, 0, d)
		for h := 0; h < 24; h += 4 {
			ks = append(ks, barAt(day.Add(time.Duration(h)*time.Hour), 90+float64(d), 80-float64(d)))
		}
	}
	// Current week: Mon (prev day) then Tue (current day).
	for h := 0; h < 24; h += 4 {
		ks = append(ks, barAt(base.Add(time.Duration(h)*time.Hour), 105, 95)) // Mon = prev day
	}
	tue := base.AddDate(0, 0, 1)
	for h := 0; h < 12; h += 4 {
		ks = append(ks, barAt(tue.Add(time.Duration(h)*time.Hour), 110, 100)) // Tue = current day
	}

	pl := CalculatePeriodLevels(ks)
	if pl == nil {
		t.Fatal("expected period levels")
	}
	if pl.CurrDayHigh != 110 || pl.CurrDayLow != 100 {
		t.Errorf("current day wrong: H=%.0f L=%.0f", pl.CurrDayHigh, pl.CurrDayLow)
	}
	if pl.PrevDayHigh != 105 || pl.PrevDayLow != 95 {
		t.Errorf("prev day wrong: H=%.0f L=%.0f", pl.PrevDayHigh, pl.PrevDayLow)
	}
	// Prev week high = max(90+4)=94, low = min(80-4)=76.
	if pl.PrevWeekHigh != 94 || pl.PrevWeekLow != 76 {
		t.Errorf("prev week wrong: H=%.0f L=%.0f", pl.PrevWeekHigh, pl.PrevWeekLow)
	}
}

func TestCalculatePeriodLevels_Adversary(t *testing.T) {
	cases := map[string][]Kline{
		"nil":    nil,
		"single": {barAt(time.Now().UTC(), 10, 9)},
		"no_ts":  {{High: 10, Low: 9}, {High: 11, Low: 8}},
	}
	for name, ks := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic on %s: %v", name, r)
				}
			}()
			pl := CalculatePeriodLevels(ks)
			if pl != nil {
				t.Errorf("%s: expected nil, got %+v", name, pl)
			}
		})
	}
}

func TestNormalizeToMillis(t *testing.T) {
	if got := normalizeToMillis(1_700_000_000); got != 1_700_000_000_000 {
		t.Errorf("seconds not upscaled: %d", got)
	}
	if got := normalizeToMillis(1_700_000_000_000); got != 1_700_000_000_000 {
		t.Errorf("millis mangled: %d", got)
	}
}
