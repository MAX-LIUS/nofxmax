package market

import (
	"strings"
	"sync"
	"time"
)

// Method: session management for scheduled assets (stocks/commodities). Tokenized
// equity/commodity perpetuals trade 24/7 on the exchange, but their PRICE gaps when
// the UNDERLYING cash market opens (open-auction repricing on overnight news). The
// product rule (user, 2026-07): forbid OPENING new positions in the [open-window,
// open) pre-open window, where window defaults to 60 min. Crypto is unaffected
// (24/7, no scheduled open).
//
// Timezones use IANA names (America/New_York, Asia/Seoul, ...) so DST is handled by
// the system tzdata automatically — we never hardcode EST/EDT offsets. (As of 2026
// the US still switches DST; the Sunshine Protection Act passed the Senate in 2022
// but was never enacted. If that ever changes, updating tzdata is enough.)

// Market describes one cash-market session for open-gap gating.
type Market struct {
	Name string // human label, e.g. "US_EQUITY"
	TZ   string // IANA timezone name
	// OpenHHMM is the regular-session open in local exchange time, "HH:MM".
	OpenHHMM string
	// tradingWeekday reports whether the market trades on the given weekday (local).
	// nil → Mon-Fri default. Holidays are layered on top via cal (see isTradingDay).
	tradingWeekday func(time.Weekday) bool
	// cal selects the holiday calendar applied on top of the weekday rule.
	cal holidayCalendar
}

func monFri(d time.Weekday) bool { return d >= time.Monday && d <= time.Friday }

// isTradingDay reports whether the market has a session on the given LOCAL date:
// a trading weekday AND not a full-closure holiday. Holiday data that is missing for
// the year (hardcoded calendars beyond their table) degrades to weekday-only.
func (m Market) isTradingDay(local time.Time) bool {
	trades := monFri
	if m.tradingWeekday != nil {
		trades = m.tradingWeekday
	}
	if !trades(local.Weekday()) {
		return false
	}
	return !isMarketHoliday(m.cal, local)
}

// Known markets. Commodity uses the CME Globex daily reopen (18:00 ET Sun–Fri): the
// main overnight-gap risk for metals/energy is the Sunday/holiday reopen, so we gate
// the hour before the 18:00 ET electronic reopen.
var (
	marketUSEquity  = Market{Name: "US_EQUITY", TZ: "America/New_York", OpenHHMM: "09:30", tradingWeekday: monFri, cal: calUS}
	marketKRX       = Market{Name: "KRX", TZ: "Asia/Seoul", OpenHHMM: "09:00", tradingWeekday: monFri, cal: calKRX}
	marketTSE       = Market{Name: "TSE", TZ: "Asia/Tokyo", OpenHHMM: "09:00", tradingWeekday: monFri, cal: calTSE}
	marketCommodity = Market{Name: "COMMODITY", TZ: "America/New_York", OpenHHMM: "18:00",
		tradingWeekday: func(d time.Weekday) bool { return d != time.Friday && d != time.Saturday }, cal: calCME} // Sun–Thu 18:00 reopen into next session (Fri eve has no reopen: closed to Sun)
)

// stockMarketOverride maps a base ticker to a NON-US home market. Anything not listed
// (and classified as a stock) defaults to US_EQUITY — correct for US listings, US
// ETFs, and US-listed ADRs (TSM/ASML/ARM/NOK/…, which gap at the US open, not their
// home market). Only tickers whose PRIMARY liquid venue is foreign belong here.
var stockMarketOverride = map[string]Market{
	// Korea (no liquid US ADR → gaps at KRX open)
	"SKHYNIX": marketKRX,
	"SKHY":    marketKRX,
	"SAMSUNG": marketKRX,
	"HYUNDAI": marketKRX,
	// Japan (Tokyo-primary)
	"SONY":     marketTSE,
	"SOFTBANK": marketTSE,
	"KIOXIA":   marketTSE,
}

// tzCache memoizes loaded *time.Location (LoadLocation hits the filesystem).
var (
	tzCache   = map[string]*time.Location{}
	tzCacheMu sync.Mutex
)

func loadTZ(name string) *time.Location {
	tzCacheMu.Lock()
	defer tzCacheMu.Unlock()
	if loc, ok := tzCache[name]; ok {
		return loc
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		loc = time.UTC // fail-safe: UTC never matches an equity open window → no gating
	}
	tzCache[name] = loc
	return loc
}

// MarketForSymbol resolves the cash market whose open gaps this symbol. Returns
// (market, true) for stocks/commodities; (_, false) for crypto (no scheduled open).
func MarketForSymbol(symbol string) (Market, bool) {
	switch ClassifyAsset(symbol) {
	case AssetCommodity:
		return marketCommodity, true
	case AssetStock:
		b := baseAsset(symbol)
		if m, ok := stockMarketOverride[strings.ToUpper(b)]; ok {
			return m, true
		}
		return marketUSEquity, true
	default:
		return Market{}, false
	}
}

// InPreOpenWindow reports whether now falls in the [open - window, open) pre-open
// window for the market, in the market's local timezone. windowMinutes<=0 disables
// (returns false). Minute-of-day based → DST-correct via the IANA location. Holidays
// are applied via isTradingDay: the window only fires ahead of a REAL session, so a
// holiday (or weekend) suppresses the block — there is no open to gap on a closed day.
func (m Market) InPreOpenWindow(now time.Time, windowMinutes int) bool {
	if windowMinutes <= 0 {
		return false
	}
	loc := loadTZ(m.TZ)
	local := now.In(loc)

	openMin, ok := hhmmToMinutes(m.OpenHHMM)
	if !ok {
		return false
	}
	nowMin := local.Hour()*60 + local.Minute()
	winStart := openMin - windowMinutes

	// Window may cross midnight (winStart < 0): e.g. commodity 18:00 open with a
	// window that starts the same evening never crosses, but a large window on an
	// early open could. Handle both by normalizing onto a 0..1440 day.
	if winStart >= 0 {
		// Same-day pre-open: gate only if TODAY is a real trading day.
		if !m.isTradingDay(local) {
			return false
		}
		return nowMin >= winStart && nowMin < openMin
	}
	// winStart < 0: the window bleeds into the PREVIOUS calendar day.
	// Part A (previous evening): [1440+winStart, 1440) — the upcoming session is
	// TOMORROW, so gate only if TOMORROW is a real trading day.
	if nowMin >= 1440+winStart {
		return m.isTradingDay(local.AddDate(0, 0, 1))
	}
	// Part B (open morning): [0, openMin) — gate only if TODAY is a real trading day.
	if nowMin < openMin {
		return m.isTradingDay(local)
	}
	return false
}

func hhmmToMinutes(s string) (int, bool) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, false
	}
	h := atoiSafe(parts[0])
	m := atoiSafe(parts[1])
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return -1
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// InPreOpenBlock is the top-level gate: true means OPENING a new position in symbol
// should be blocked right now because its underlying cash market opens within
// windowMinutes. Crypto always returns false. Safe default: an unclassified symbol
// resolves as crypto → never blocked.
func InPreOpenBlock(symbol string, now time.Time, windowMinutes int) (bool, string) {
	m, ok := MarketForSymbol(symbol)
	if !ok {
		return false, ""
	}
	if m.InPreOpenWindow(now, windowMinutes) {
		return true, m.Name
	}
	return false, ""
}
