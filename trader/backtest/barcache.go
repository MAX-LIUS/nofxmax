package backtest

import (
	"encoding/gob"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"nofx/market"
)

// barcache.go builds a per-symbol, per-timeframe bar cache so a large pooled
// entry set can be replayed over an OPEN-ENDED forward horizon without a
// network fetch per entry. The live-ExitTime-bounded fetchEntryBars cannot test
// time-based exits (max-hold / time-stop) because it never has bars past the
// live close; this cache fetches each symbol's FULL date range once and slices
// each entry's window (including a forward horizon beyond the live exit) from
// memory. Persisted to disk (gob) so repeated optimization runs are instant.

// SymbolBars holds one symbol/timeframe ascending bar series with its covered range.
type SymbolBars struct {
	Symbol    string
	Timeframe string
	StartMs   int64
	EndMs     int64
	Bars      []market.Kline
}

// BarCache maps "symbol|tf" to its full series.
type BarCache struct {
	mu    sync.Mutex
	byKey map[string]*SymbolBars
	tf    string
}

func barKey(symbol, tf string) string { return symbol + "|" + tf }

// NewBarCache creates an empty cache for a timeframe.
func NewBarCache(tf string) *BarCache {
	return &BarCache{byKey: make(map[string]*SymbolBars), tf: tf}
}

// LoadBarCache reads a gob-persisted cache from disk (or returns an empty cache
// when the file is absent). The tf is validated against the persisted content.
func LoadBarCache(path, tf string) (*BarCache, error) {
	c := NewBarCache(tf)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return nil, err
	}
	defer f.Close()
	var stored map[string]*SymbolBars
	if err := gob.NewDecoder(f).Decode(&stored); err != nil {
		return nil, fmt.Errorf("decode bar cache: %w", err)
	}
	// Keep only entries for this timeframe.
	for k, v := range stored {
		if v != nil && v.Timeframe == tf {
			c.byKey[k] = v
		}
	}
	return c, nil
}

// Save persists the cache to disk (gob).
func (c *BarCache) Save(path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return gob.NewEncoder(f).Encode(c.byKey)
}

// EnsureSymbol fetches [startMs,endMs] for a symbol if not already covered by the
// cache, extending the cached series as needed. Fetch is network-bound; call it
// once per symbol before slicing. provider is the raw range fetcher (OKXBars).
func (c *BarCache) EnsureSymbol(symbol string, startMs, endMs int64, provider BarsProvider) error {
	c.mu.Lock()
	sb, ok := c.byKey[barKey(symbol, c.tf)]
	c.mu.Unlock()
	if ok && sb.StartMs <= startMs && sb.EndMs >= endMs {
		return nil // already covered
	}
	// Widen the fetch to the union of any existing coverage and the request.
	fetchStart, fetchEnd := startMs, endMs
	if ok {
		if sb.StartMs < fetchStart {
			fetchStart = sb.StartMs
		}
		if sb.EndMs > fetchEnd {
			fetchEnd = sb.EndMs
		}
	}
	bars, err := provider(symbol, c.tf, time.UnixMilli(fetchStart), time.UnixMilli(fetchEnd))
	if err != nil {
		return err
	}
	if len(bars) == 0 {
		return fmt.Errorf("no bars for %s", symbol)
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].OpenTime < bars[j].OpenTime })
	c.mu.Lock()
	c.byKey[barKey(symbol, c.tf)] = &SymbolBars{
		Symbol: symbol, Timeframe: c.tf,
		StartMs: bars[0].OpenTime, EndMs: bars[len(bars)-1].OpenTime, Bars: bars,
	}
	c.mu.Unlock()
	return nil
}

// slice returns the ascending bars in [startMs,endMs] for a symbol from cache.
func (c *BarCache) slice(symbol string, startMs, endMs int64) []market.Kline {
	c.mu.Lock()
	sb, ok := c.byKey[barKey(symbol, c.tf)]
	c.mu.Unlock()
	if !ok {
		return nil
	}
	lo := sort.Search(len(sb.Bars), func(i int) bool { return sb.Bars[i].OpenTime >= startMs })
	hi := sort.Search(len(sb.Bars), func(i int) bool { return sb.Bars[i].OpenTime > endMs })
	if lo >= hi {
		return nil
	}
	out := make([]market.Kline, hi-lo)
	copy(out, sb.Bars[lo:hi])
	return out
}
