package market

import (
	"fmt"
	"time"
)

// PeriodLevels holds previous-day and previous-week high/low reference levels.
// These are among the most-watched liquidity references in every market:
// prior-day and prior-week high/low frequently act as magnets, breakout
// triggers, and stop-run targets. They are evidence, not hard gates.
type PeriodLevels struct {
	PrevDayHigh  float64 `json:"prev_day_high,omitempty"`
	PrevDayLow   float64 `json:"prev_day_low,omitempty"`
	PrevWeekHigh float64 `json:"prev_week_high,omitempty"`
	PrevWeekLow  float64 `json:"prev_week_low,omitempty"`
	// CurrentDayHigh/Low are the developing (in-progress) session extremes,
	// useful because a break of the current-day high/low often signals intraday
	// continuation.
	CurrDayHigh float64 `json:"curr_day_high,omitempty"`
	CurrDayLow  float64 `json:"curr_day_low,omitempty"`
}

// normalizeToMillis coerces a timestamp that may be in seconds to milliseconds.
// Threshold ~ year 2001 in ms; anything smaller is treated as seconds.
func normalizeToMillis(ts int64) int64 {
	if ts > 0 && ts < 1_000_000_000_000 {
		return ts * 1000
	}
	return ts
}

// CalculatePeriodLevels aggregates klines into UTC calendar days and ISO weeks,
// then extracts the previous completed day/week high-low and the developing
// current-day extremes. Returns nil when timestamps are missing or the window
// does not span enough history. Callers must tolerate a nil result.
func CalculatePeriodLevels(klines []Kline) *PeriodLevels {
	if len(klines) < 2 {
		return nil
	}
	// Require usable timestamps.
	if klines[len(klines)-1].OpenTime <= 0 {
		return nil
	}

	type ext struct {
		high, low float64
		seen      bool
	}
	dayExt := map[string]*ext{}
	weekExt := map[string]*ext{}
	var dayOrder, weekOrder []string

	accumulate := func(m map[string]*ext, order *[]string, key string, hi, lo float64) {
		e, ok := m[key]
		if !ok {
			e = &ext{high: hi, low: lo, seen: true}
			m[key] = e
			*order = append(*order, key)
			return
		}
		if hi > e.high {
			e.high = hi
		}
		if lo < e.low {
			e.low = lo
		}
	}

	for _, k := range klines {
		if k.OpenTime <= 0 {
			continue
		}
		t := time.UnixMilli(normalizeToMillis(k.OpenTime)).UTC()
		dayKey := t.Format("2006-01-02")
		wy, ww := t.ISOWeek()
		weekKey := isoWeekKey(wy, ww)
		accumulate(dayExt, &dayOrder, dayKey, k.High, k.Low)
		accumulate(weekExt, &weekOrder, weekKey, k.High, k.Low)
	}

	pl := &PeriodLevels{}
	// Current day = last day bucket; previous day = the one before it.
	if n := len(dayOrder); n >= 1 {
		cur := dayExt[dayOrder[n-1]]
		pl.CurrDayHigh = cur.high
		pl.CurrDayLow = cur.low
		if n >= 2 {
			prev := dayExt[dayOrder[n-2]]
			pl.PrevDayHigh = prev.high
			pl.PrevDayLow = prev.low
		}
	}
	// Previous week = the one before the last (in-progress) week.
	if n := len(weekOrder); n >= 2 {
		prev := weekExt[weekOrder[n-2]]
		pl.PrevWeekHigh = prev.high
		pl.PrevWeekLow = prev.low
	}

	// If nothing meaningful was filled, signal nil.
	if pl.CurrDayHigh == 0 && pl.PrevDayHigh == 0 && pl.PrevWeekHigh == 0 {
		return nil
	}
	return pl
}

// isoWeekKey builds a sortable "YYYY-Www" key. Since our windows are short
// (< ~35 days) year rollover ordering is not a concern for adjacent weeks.
func isoWeekKey(year, week int) string {
	return fmt.Sprintf("%d-W%02d", year, week)
}
