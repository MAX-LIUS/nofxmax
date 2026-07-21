package market

import (
	"fmt"
	"sync"
	"time"
)

// Market-holiday calendars for the session pre-open gate. A gate must NOT fire before
// a day the cash market is CLOSED (no open → no open-gap). Weekends are handled by the
// weekday check; this file adds public holidays.
//
// Two strategies:
//   - US_EQUITY / CME (COMMODITY): computed algorithmically (fixed-date + Nth-weekday
//     floating + weekend-observance + Good Friday via Easter). Correct for ANY year,
//     zero maintenance.
//   - KRX (Korea) / TSE (Japan): include LUNAR (Seollal, Chuseok, Buddha's Birthday)
//     and EQUINOX (Japan) holidays that cannot be computed with simple rules and that
//     I cannot fetch live. These use hardcoded BEST-EFFORT tables per year. Outside the
//     table's year range the calendar degrades to weekday-only (logged once) — a missed
//     holiday only causes a harmless ≤window spurious block or a skipped skip on a day
//     the market is closed anyway.
//
// A "date" key is "YYYY-MM-DD" in the market's LOCAL timezone.

type holidayCalendar string

const (
	calNone holidayCalendar = ""     // weekday-only (crypto never reaches here)
	calUS   holidayCalendar = "US"   // NYSE/Nasdaq
	calCME  holidayCalendar = "CME"  // CME Globex (metals/energy)
	calKRX  holidayCalendar = "KRX"  // Korea Exchange
	calTSE  holidayCalendar = "TSE"  // Tokyo Stock Exchange
)

func dkey(y int, m time.Month, d int) string {
	return fmt.Sprintf("%04d-%02d-%02d", y, m, d)
}

// nthWeekday returns the date of the nth (1-based) given weekday in month m of year y.
// nth<0 counts from the end (-1 = last).
func nthWeekday(y int, m time.Month, wd time.Weekday, nth int) time.Time {
	if nth > 0 {
		first := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
		offset := (int(wd) - int(first.Weekday()) + 7) % 7
		return first.AddDate(0, 0, offset+(nth-1)*7)
	}
	// last-of-month: start at the last day, walk back to the weekday.
	last := time.Date(y, m+1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	offset := (int(last.Weekday()) - int(wd) + 7) % 7
	return last.AddDate(0, 0, -offset)
}

// usObserved applies the US federal/NYSE weekend-observance rule: a Saturday holiday
// is observed the preceding Friday, a Sunday holiday the following Monday. (New Year's
// Day is the one NYSE exception — a Saturday Jan 1 is NOT observed on Dec 31 — handled
// by the caller skipping the Saturday→Friday shift for that date.)
func usObserved(d time.Time, shiftSaturday bool) time.Time {
	switch d.Weekday() {
	case time.Saturday:
		if shiftSaturday {
			return d.AddDate(0, 0, -1)
		}
		return d // not observed (e.g. New Year on Sat)
	case time.Sunday:
		return d.AddDate(0, 0, 1)
	default:
		return d
	}
}

// easterSunday computes Gregorian Easter (Meeus/Jones/Butcher). Good Friday = Easter−2.
func easterSunday(y int) time.Time {
	a := y % 19
	b := y / 100
	c := y % 100
	d := b / 4
	e := b % 4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i := c / 4
	k := c % 4
	l := (32 + 2*e + 2*i - h - k) % 7
	mm := (a + 11*h + 22*l) / 451
	month := (h + l - 7*mm + 114) / 31
	day := ((h + l - 7*mm + 114) % 31) + 1
	return time.Date(y, time.Month(month), day, 0, 0, 0, 0, time.UTC)
}

// usEquityHolidays returns the set of NYSE/Nasdaq full-closure dates for year y as
// "YYYY-MM-DD" keys (observed dates). Early-close days (e.g. day after Thanksgiving)
// are NOT closures — the market still OPENS at 09:30, so the open-gap gate still
// applies; they are intentionally excluded.
func usEquityHolidays(y int) map[string]bool {
	h := map[string]bool{}
	add := func(t time.Time) { h[dkey(t.Year(), t.Month(), t.Day())] = true }

	// New Year's Day (Jan 1) — Sunday→Mon; Saturday NOT observed on Fri.
	add(usObserved(time.Date(y, time.January, 1, 0, 0, 0, 0, time.UTC), false))
	// MLK Day — 3rd Monday of January.
	add(nthWeekday(y, time.January, time.Monday, 3))
	// Presidents' Day — 3rd Monday of February.
	add(nthWeekday(y, time.February, time.Monday, 3))
	// Good Friday — Easter − 2 (NYSE closes; it is not a federal holiday).
	add(easterSunday(y).AddDate(0, 0, -2))
	// Memorial Day — last Monday of May.
	add(nthWeekday(y, time.May, time.Monday, -1))
	// Juneteenth (Jun 19) — federal since 2021; NYSE observes from 2022.
	if y >= 2022 {
		add(usObserved(time.Date(y, time.June, 19, 0, 0, 0, 0, time.UTC), true))
	}
	// Independence Day (Jul 4).
	add(usObserved(time.Date(y, time.July, 4, 0, 0, 0, 0, time.UTC), true))
	// Labor Day — 1st Monday of September.
	add(nthWeekday(y, time.September, time.Monday, 1))
	// Thanksgiving — 4th Thursday of November.
	add(nthWeekday(y, time.November, time.Thursday, 4))
	// Christmas (Dec 25).
	add(usObserved(time.Date(y, time.December, 25, 0, 0, 0, 0, time.UTC), true))
	return h
}

// cmeCommodityHolidays returns CME Globex full-closure dates for metals/energy. CME
// differs from NYSE: it stays OPEN on MLK Day and Presidents' Day (shortened, but the
// evening reopen still happens), and closes for New Year, Good Friday, Memorial Day,
// Juneteenth, Independence Day, Labor Day, Thanksgiving, Christmas. We model the full
// closures that remove the 18:00 ET reopen.
func cmeCommodityHolidays(y int) map[string]bool {
	h := map[string]bool{}
	add := func(t time.Time) { h[dkey(t.Year(), t.Month(), t.Day())] = true }
	add(usObserved(time.Date(y, time.January, 1, 0, 0, 0, 0, time.UTC), false))
	add(easterSunday(y).AddDate(0, 0, -2)) // Good Friday
	add(nthWeekday(y, time.May, time.Monday, -1))
	if y >= 2022 {
		add(usObserved(time.Date(y, time.June, 19, 0, 0, 0, 0, time.UTC), true))
	}
	add(usObserved(time.Date(y, time.July, 4, 0, 0, 0, 0, time.UTC), true))
	add(nthWeekday(y, time.September, time.Monday, 1))
	add(nthWeekday(y, time.November, time.Thursday, 4))
	add(usObserved(time.Date(y, time.December, 25, 0, 0, 0, 0, time.UTC), true))
	return h
}

// krxHolidaysByYear / tseHolidaysByYear: BEST-EFFORT hardcoded closure dates. These
// markets include LUNAR (Seollal, Chuseok, Buddha's Birthday) and EQUINOX (Japan)
// holidays that cannot be computed by simple rules and that I cannot verify live.
// VERIFY against the official exchange calendar annually. Years absent here degrade to
// weekday-only (logged once via markMissingHolidayYear). Impact of an error is small:
// a wrong date at worst blocks new opens for ≤window on a day, or skips a skip on a day
// the market is closed anyway (no open-gap either way).
var krxHolidaysByYear = map[int][]string{
	2026: {
		"2026-01-01", // New Year
		"2026-02-16", "2026-02-17", "2026-02-18", // Seollal (Lunar New Year) + adjacent
		"2026-03-02", // Independence Movement Day observed (Mar 1 = Sun)
		"2026-05-05", // Children's Day
		"2026-05-25", // Buddha's Birthday observed (May 24 = Sun)
		"2026-06-08", // Memorial Day observed? (Jun 6 = Sat → Mon Jun 8)
		"2026-08-17", // Liberation Day observed (Aug 15 = Sat → Mon)
		"2026-09-24", "2026-09-25", "2026-09-26", // Chuseok
		"2026-10-05", // National Foundation Day observed (Oct 3 = Sat)
		"2026-10-09", // Hangeul Day
		"2026-12-25", // Christmas
		"2026-12-31", // KRX year-end closure
	},
	2027: {
		"2027-01-01",
		"2027-02-08", "2027-02-09", // Seollal window (Lunar New Year ~Feb 7 Sun)
		"2027-03-01", // Independence Movement Day (Mon)
		"2027-05-05", // Children's Day
		"2027-05-13", // Buddha's Birthday
		"2027-06-07", // Memorial Day observed (Jun 6 = Sun → Mon)
		"2027-08-16", // Liberation Day observed (Aug 15 = Sun → Mon)
		"2027-09-14", "2027-09-15", "2027-09-16", // Chuseok window
		"2027-10-04", // National Foundation Day observed (Oct 3 = Sun)
		"2027-10-11", // Hangeul Day observed (Oct 9 = Sat → Mon)
		"2027-12-27", // Christmas observed (Dec 25 = Sat)
		"2027-12-31",
	},
}

var tseHolidaysByYear = map[int][]string{
	2026: {
		"2026-01-01", "2026-01-02", "2026-01-03", // New Year (TSE always closed 1/1–1/3)
		"2026-01-12", // Coming of Age Day (2nd Mon)
		"2026-02-11", // National Foundation Day
		"2026-02-23", // Emperor's Birthday
		"2026-03-20", // Vernal Equinox
		"2026-04-29", // Showa Day
		"2026-05-04", // Greenery Day
		"2026-05-05", // Children's Day
		"2026-05-06", // Constitution Day observed (May 3 = Sun → Wed bridge)
		"2026-07-20", // Marine Day (3rd Mon)
		"2026-08-11", // Mountain Day
		"2026-09-21", // Respect for the Aged (3rd Mon)
		"2026-09-22", // Bridge/Citizens' holiday (between 21 and equinox 23)
		"2026-09-23", // Autumnal Equinox
		"2026-10-12", // Sports Day (2nd Mon)
		"2026-11-03", // Culture Day
		"2026-11-23", // Labor Thanksgiving Day
		"2026-12-31", // TSE year-end closure
	},
	2027: {
		"2027-01-01", "2027-01-02", "2027-01-03",
		"2027-01-11", // Coming of Age Day (2nd Mon)
		"2027-02-11", // National Foundation Day
		"2027-02-23", // Emperor's Birthday
		"2027-03-22", // Vernal Equinox observed (Mar 21 = Sun → Mon)
		"2027-04-29", // Showa Day
		"2027-05-03", // Constitution Day
		"2027-05-04", // Greenery Day
		"2027-05-05", // Children's Day
		"2027-07-19", // Marine Day (3rd Mon)
		"2027-08-11", // Mountain Day
		"2027-09-20", // Respect for the Aged (3rd Mon)
		"2027-09-23", // Autumnal Equinox
		"2027-10-11", // Sports Day (2nd Mon)
		"2027-11-03", // Culture Day
		"2027-11-23", // Labor Thanksgiving Day
		"2027-12-31",
	},
}

// holidayCache memoizes computed/looked-up sets per (calendar, year).
var (
	holidayCache   = map[string]map[string]bool{}
	holidayCacheMu sync.Mutex
	missingYearLogged = map[string]bool{}
)

func setFromList(list []string) map[string]bool {
	m := make(map[string]bool, len(list))
	for _, d := range list {
		m[d] = true
	}
	return m
}

// holidaysFor returns the closure-date set for a calendar+year, or (nil,false) when a
// hardcoded calendar has no data for that year (caller degrades to weekday-only).
func holidaysFor(cal holidayCalendar, y int) (map[string]bool, bool) {
	if cal == calNone {
		return nil, true
	}
	ck := string(cal) + ":" + fmt.Sprint(y)
	holidayCacheMu.Lock()
	defer holidayCacheMu.Unlock()
	if m, ok := holidayCache[ck]; ok {
		return m, true
	}
	var m map[string]bool
	switch cal {
	case calUS:
		m = usEquityHolidays(y)
	case calCME:
		m = cmeCommodityHolidays(y)
	case calKRX:
		list, ok := krxHolidaysByYear[y]
		if !ok {
			return nil, false
		}
		m = setFromList(list)
	case calTSE:
		list, ok := tseHolidaysByYear[y]
		if !ok {
			return nil, false
		}
		m = setFromList(list)
	default:
		return nil, true
	}
	holidayCache[ck] = m
	return m, true
}

// isMarketHoliday reports whether the given LOCAL date is a full-closure holiday for the
// calendar. Unknown year (hardcoded calendars only) → false (degrade to weekday-only).
func isMarketHoliday(cal holidayCalendar, local time.Time) bool {
	set, known := holidaysFor(cal, local.Year())
	if !known || set == nil {
		return false
	}
	return set[dkey(local.Year(), local.Month(), local.Day())]
}

// HolidayCalendarKnownYear reports whether a hardcoded calendar has data for a year;
// used by callers that want to log/observe degradation. US/CME are always known.
func HolidayCalendarKnownYear(cal holidayCalendar, y int) bool {
	_, known := holidaysFor(cal, y)
	return known
}
