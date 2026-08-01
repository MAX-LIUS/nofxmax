package market

import (
	"math"
	"sort"
	"time"
)

// SessionRange holds the opening range of the most recent trading session plus
// an expansion grade measuring how much of the daily ATR that opening range has
// already consumed.
//
// Rationale (evidence, not a gate): intraday traders anchor on the first N
// minutes of a session because that window is where the day's positioning is
// established. The ratio M = openingRange / dailyATR separates two very
// different days:
//
//   - M small  -> the session opened quietly; the day's range is still mostly
//     unspent, so a later expansion has room to run.
//   - M large  -> the opening window already burned most of a normal day's
//     range. Continuation from here is buying/selling into spent volatility.
//
// This struct is pure description. It never blocks or sizes a trade; it is
// rendered into the prompt so the model can see whether the current price sits
// inside, above, or below the session's opening balance.
type SessionRange struct {
	// Session is the label of the most recent session whose opening window has
	// started: "ASIA" (00:00 UTC), "EU" (07:00 UTC) or "US" (13:30 UTC).
	Session string `json:"session,omitempty"`
	// SessionStart is the session anchor in unix milliseconds (UTC).
	SessionStart int64 `json:"session_start,omitempty"`
	// ORHigh / ORLow are the high and low of the opening window.
	ORHigh float64 `json:"or_high,omitempty"`
	ORLow  float64 `json:"or_low,omitempty"`
	// ORWindowMinutes is the wall-clock span actually covered by the bars used.
	// With coarse timeframes this is the bar size, not the nominal 15m.
	ORWindowMinutes int `json:"or_window_minutes,omitempty"`
	// Complete reports whether the opening window has fully elapsed. When false
	// the values are developing and should be treated as provisional.
	Complete bool `json:"or_complete"`
	// DailyATR is the average true range of completed UTC days in the series.
	DailyATR float64 `json:"daily_atr,omitempty"`
	// ExpansionPct is 100 * (ORHigh-ORLow) / DailyATR. Reported for
	// comparability with the equity-market literature, but NOT used for grading:
	// its level is dominated by the window length, not by market state.
	ExpansionPct float64 `json:"expansion_pct,omitempty"`
	// ExpansionRatio is ExpansionPct divided by the range a random walk would
	// cover in the same window, sqrt(windowMinutes/1440). 1.0 means the opening
	// window was exactly as wide as its duration implies; 3.0 means three times
	// wider. This is the scale-free measure and the basis of ExpansionGrade.
	ExpansionRatio float64 `json:"expansion_ratio,omitempty"`
	// ExpansionGrade buckets ExpansionRatio: "compressed" (<0.6), "normal"
	// (0.6-1.3), "expanded" (1.3-2.5), "exhausted" (>=2.5). Bands are the
	// empirical quartiles of 14 days x 12 symbols of 15m crypto data.
	ExpansionGrade string `json:"expansion_grade,omitempty"`
	// Location describes where the latest close sits relative to the opening
	// range: "above_or", "inside_or" or "below_or".
	Location string `json:"location,omitempty"`
}

// sessionAnchors are minute-of-day UTC offsets for the three crypto sessions.
// Crypto trades continuously, but flow still clusters around these handoffs:
// Asia cash open, Europe open, and the US equity open (13:30 UTC).
var sessionAnchors = []struct {
	name   string
	minute int
}{
	{"ASIA", 0},
	{"EU", 7 * 60},
	{"US", 13*60 + 30},
}

// defaultORWindowMinutes is the nominal opening-range span.
const defaultORWindowMinutes = 15

// inferBarMinutes estimates bar spacing from the median positive delta between
// consecutive open times. Median (not mean) so a single gap from an exchange
// outage does not distort the result. Returns 0 when undeterminable.
func inferBarMinutes(klines []Kline) int {
	if len(klines) < 3 {
		return 0
	}
	deltas := make([]int64, 0, len(klines)-1)
	for i := 1; i < len(klines); i++ {
		a := normalizeToMillis(klines[i-1].OpenTime)
		b := normalizeToMillis(klines[i].OpenTime)
		if a <= 0 || b <= 0 || b <= a {
			continue
		}
		deltas = append(deltas, b-a)
	}
	if len(deltas) == 0 {
		return 0
	}
	sort.Slice(deltas, func(i, j int) bool { return deltas[i] < deltas[j] })
	med := deltas[len(deltas)/2]
	mins := int(med / 60000)
	if mins <= 0 {
		return 0
	}
	return mins
}

// calcDailyATR computes the mean true range over completed UTC days.
// True range per day = max(H-L, |H-prevClose|, |L-prevClose|). The in-progress
// final day is excluded so the value does not shrink as a new day begins.
// Returns 0 when fewer than 2 completed days are present.
func calcDailyATR(klines []Kline, maxDays int) float64 {
	if len(klines) < 2 {
		return 0
	}
	type dayAgg struct {
		high, low, close float64
	}
	agg := map[string]*dayAgg{}
	var order []string
	for _, k := range klines {
		if k.OpenTime <= 0 {
			continue
		}
		key := time.UnixMilli(normalizeToMillis(k.OpenTime)).UTC().Format("2006-01-02")
		d, ok := agg[key]
		if !ok {
			agg[key] = &dayAgg{high: k.High, low: k.Low, close: k.Close}
			order = append(order, key)
			continue
		}
		if k.High > d.high {
			d.high = k.High
		}
		if k.Low < d.low {
			d.low = k.Low
		}
		d.close = k.Close
	}
	// Drop the in-progress last day.
	if len(order) < 3 {
		return 0
	}
	order = order[:len(order)-1]
	// Also drop the first day: it is usually a partial bucket at the window edge.
	if len(order) < 3 {
		return 0
	}
	order = order[1:]
	if maxDays > 0 && len(order) > maxDays {
		order = order[len(order)-maxDays:]
	}
	var sum float64
	var n int
	for i, key := range order {
		d := agg[key]
		tr := d.high - d.low
		if i > 0 {
			prevClose := agg[order[i-1]].close
			if v := absFloat(d.high - prevClose); v > tr {
				tr = v
			}
			if v := absFloat(d.low - prevClose); v > tr {
				tr = v
			}
		}
		if tr > 0 {
			sum += tr
			n++
		}
	}
	if n < 2 {
		return 0
	}
	return sum / float64(n)
}

// latestSessionAnchor returns the name and unix-milli start of the most recent
// session anchor at or before ref. It looks back across the day boundary so an
// early-UTC timestamp resolves to the previous day's US session.
func latestSessionAnchor(ref time.Time) (string, int64) {
	ref = ref.UTC()
	midnight := time.Date(ref.Year(), ref.Month(), ref.Day(), 0, 0, 0, 0, time.UTC)
	best := ""
	var bestTime time.Time
	// Consider today's anchors and yesterday's, pick the latest <= ref.
	for _, dayOff := range []int{0, -1} {
		base := midnight.AddDate(0, 0, dayOff)
		for _, a := range sessionAnchors {
			t := base.Add(time.Duration(a.minute) * time.Minute)
			if t.After(ref) {
				continue
			}
			if best == "" || t.After(bestTime) {
				best, bestTime = a.name, t
			}
		}
	}
	if best == "" {
		return "", 0
	}
	return best, bestTime.UnixMilli()
}

// gradeExpansion buckets the duration-normalized expansion ratio.
//
// Calibration note: the raw OR/dailyATR ratio cannot be graded with fixed bands
// borrowed from single-session equity markets. Crypto trades 24h, so a 15m
// opening window is 1/96 of the day and its raw ratio clusters near
// sqrt(15/1440) = 10%. Measured over 14 days x 12 symbols of 15m bars the raw
// ratio had p50=9.7% and p99=52%, which means bands of 20/40/70% put 74% of all
// observations in one bucket and 0.3% in the top one — a non-discriminating
// scale. Dividing by the random-walk expectation removes the window-length
// dependence and yields p25=0.58, p50=0.95, p75=1.87, p90=2.90.
func gradeExpansion(ratio float64) string {
	switch {
	case ratio <= 0:
		return ""
	case ratio < 0.6:
		return "compressed"
	case ratio < 1.3:
		return "normal"
	case ratio < 2.5:
		return "expanded"
	default:
		return "exhausted"
	}
}

// randomWalkRangeFrac is the fraction of a full day's range a random walk is
// expected to cover in windowMinutes: sqrt(windowMinutes/1440).
func randomWalkRangeFrac(windowMinutes int) float64 {
	if windowMinutes <= 0 {
		return 0
	}
	return math.Sqrt(float64(windowMinutes) / 1440.0)
}

// CalculateSessionRange derives the most recent session's opening range and its
// expansion grade. orKlines should be the finest-granularity series available
// (it sets the resolution of the opening window); dailySrc should be the
// widest-span series (it sets the daily ATR baseline). Either may be the same
// slice. Returns nil when the inputs cannot support the calculation — callers
// must tolerate a nil result.
//
// Causality: only bars whose close time has passed are used, so the developing
// (unclosed) bar never leaks into the opening range.
func CalculateSessionRange(orKlines, dailySrc []Kline, nowMillis int64) *SessionRange {
	barMin := inferBarMinutes(orKlines)
	if barMin <= 0 || barMin > 60 {
		// Coarser than 1h cannot describe a 15m opening window meaningfully.
		return nil
	}
	if nowMillis <= 0 {
		last := orKlines[len(orKlines)-1]
		nowMillis = normalizeToMillis(last.OpenTime) + int64(barMin)*60000
	}

	// Keep only closed bars.
	barMillis := int64(barMin) * 60000
	closed := make([]Kline, 0, len(orKlines))
	for _, k := range orKlines {
		ot := normalizeToMillis(k.OpenTime)
		if ot <= 0 {
			continue
		}
		if ot+barMillis > nowMillis {
			continue
		}
		closed = append(closed, k)
	}
	if len(closed) == 0 {
		return nil
	}
	lastClosed := closed[len(closed)-1]
	lastOpen := normalizeToMillis(lastClosed.OpenTime)

	name, anchor := latestSessionAnchor(time.UnixMilli(lastOpen + barMillis).UTC())
	if name == "" || anchor <= 0 {
		return nil
	}

	// Window span: at least one bar, nominally 15 minutes.
	windowMin := defaultORWindowMinutes
	if barMin > windowMin {
		windowMin = barMin
	}

	// First bar at or after the anchor. With coarse bars the anchor may fall
	// mid-bar (e.g. 13:30 US open on 1h bars); starting from the next aligned
	// bar keeps the window inside the session rather than straddling it.
	startIdx := -1
	for i, k := range closed {
		if normalizeToMillis(k.OpenTime) >= anchor {
			startIdx = i
			break
		}
	}
	if startIdx < 0 {
		return nil
	}
	windowStart := normalizeToMillis(closed[startIdx].OpenTime)
	windowEnd := windowStart + int64(windowMin)*60000

	sr := &SessionRange{
		Session:         name,
		SessionStart:    anchor,
		ORWindowMinutes: windowMin,
	}
	var seen bool
	var covered int64
	for i := startIdx; i < len(closed); i++ {
		ot := normalizeToMillis(closed[i].OpenTime)
		if ot >= windowEnd {
			break
		}
		if !seen {
			sr.ORHigh, sr.ORLow = closed[i].High, closed[i].Low
			seen = true
		} else {
			if closed[i].High > sr.ORHigh {
				sr.ORHigh = closed[i].High
			}
			if closed[i].Low < sr.ORLow {
				sr.ORLow = closed[i].Low
			}
		}
		covered = ot + barMillis - windowStart
	}
	if !seen || sr.ORHigh <= 0 || sr.ORLow <= 0 || sr.ORHigh < sr.ORLow {
		return nil
	}
	sr.Complete = covered >= int64(windowMin)*60000
	if !sr.Complete {
		sr.ORWindowMinutes = int(covered / 60000)
	}

	if len(dailySrc) == 0 {
		dailySrc = orKlines
	}
	sr.DailyATR = calcDailyATR(dailySrc, 14)
	if sr.DailyATR > 0 {
		sr.ExpansionPct = (sr.ORHigh - sr.ORLow) / sr.DailyATR * 100
		// Normalize away the window-length dependence before grading. Use the
		// span actually covered, so a developing window is graded fairly.
		if base := randomWalkRangeFrac(sr.ORWindowMinutes); base > 0 {
			sr.ExpansionRatio = sr.ExpansionPct / 100.0 / base
			sr.ExpansionGrade = gradeExpansion(sr.ExpansionRatio)
		}
	}

	switch {
	case lastClosed.Close > sr.ORHigh:
		sr.Location = "above_or"
	case lastClosed.Close < sr.ORLow:
		sr.Location = "below_or"
	default:
		sr.Location = "inside_or"
	}
	return sr
}
