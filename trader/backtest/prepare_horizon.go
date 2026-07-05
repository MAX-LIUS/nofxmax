package backtest

import (
	"time"
)

// prepare_horizon.go builds loadedEntry windows that extend a fixed forward
// HORIZON past each entry (independent of the live ExitTime), sliced from a
// pre-fetched per-symbol BarCache. This is what makes time-based exits
// (max-hold / time-stop) testable: the raw ExitTime-bounded PrepareEntries has
// no bars past the live close, so loosening a hold does nothing. With a forward
// horizon the replay runs the position to a mechanical exit and we can see what
// a longer/shorter hold would actually have done.
func PrepareEntriesHorizon(entries []Entry, tf string, horizonHours int, cache *BarCache, provider BarsProvider) ([]LoadedEntry, int, error) {
	tfDur := tfDuration(tf)
	preDur := time.Duration(preBarsForATR+2) * tfDur
	fwdDur := time.Duration(horizonHours) * time.Hour

	// 1) Per symbol, compute the union range needed and fetch once.
	type rng struct{ start, end int64 }
	need := map[string]*rng{}
	for _, e := range entries {
		s := time.UnixMilli(e.EntryTime).Add(-preDur).UnixMilli()
		en := time.UnixMilli(e.EntryTime).Add(fwdDur).UnixMilli()
		if r, ok := need[e.Symbol]; ok {
			if s < r.start {
				r.start = s
			}
			if en > r.end {
				r.end = en
			}
		} else {
			need[e.Symbol] = &rng{start: s, end: en}
		}
	}
	for sym, r := range need {
		// A symbol with no history is not fatal; its entries just get skipped.
		_ = cache.EnsureSymbol(sym, r.start, r.end, provider)
	}

	// 2) Slice each entry's window from the cache.
	var loaded []LoadedEntry
	skipped := 0
	for _, e := range entries {
		start := time.UnixMilli(e.EntryTime).Add(-preDur).UnixMilli()
		end := time.UnixMilli(e.EntryTime).Add(fwdDur).UnixMilli()
		bars := cache.slice(e.Symbol, start, end)
		if len(bars) == 0 {
			skipped++
			continue
		}
		entryIdx := -1
		for i, b := range bars {
			if b.OpenTime >= e.EntryTime {
				entryIdx = i
				break
			}
		}
		if entryIdx < 2 {
			skipped++
			continue
		}
		// Clear ExitTime so the replay runs open-ended to a mechanical exit over
		// the forward horizon instead of clamping at the live close.
		he := e
		he.ExitTime = 0
		loaded = append(loaded, LoadedEntry{entry: he, bars: bars, entryIdx: entryIdx})
	}
	return loaded, skipped, nil
}
