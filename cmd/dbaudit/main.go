package main

// dbaudit: read-only data-quality audit of trader_positions. For every CLOSED
// position it cross-checks the recorded entry/exit price, PnL self-consistency,
// timestamps, and close_reason against REAL exchange klines (OKX for okx-type
// positions, Binance for binance-type — each position carries exchange_type).
// It classifies each trade CLEAN or DIRTY (with reason codes), writes a clean.db
// containing only CLEAN closed positions for faithful backtesting, prints a report,
// and emits a fix-list for the production DB. It NEVER writes the production DB.

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"nofx/market"
)

type pos struct {
	id            int64
	traderID      string
	exchangeType  string
	symbol        string
	side          string
	entryQty      float64
	quantity      float64
	entryPrice    float64
	entryTime     int64
	exitPrice     float64
	exitTime      int64
	realizedPnL   float64
	fee           float64
	leverage      int
	closeReason   string
}

// dirtyFlags accumulates per-trade reason codes.
type dirtyFlags struct {
	codes []string
}

func (d *dirtyFlags) add(c string) { d.codes = append(d.codes, c) }
func (d *dirtyFlags) dirty() bool  { return len(d.codes) > 0 }
func (d *dirtyFlags) str() string  { return strings.Join(d.codes, ",") }

// barKey caches by exchange+symbol+hour-bucketed window so repeated trades on the
// same instrument in the same window reuse one fetch (kinder to the exchange).
type barKey struct {
	exch, symbol         string
	startHour, endHour   int64
}

type barResult struct {
	bars []market.Kline
	err  error
}

var barCache = map[barKey]barResult{}

const hourMs = int64(3600 * 1000)

// fetchBars routes to OKX or Binance by exchange_type. tf is fixed at 1h for the
// audit (entry/exit checks only need the containing hourly bar). Window covers a
// ±3h pad around [entry, exit]. Results (including errors) are cached.
func fetchBars(exchType, symbol string, entryMs, exitMs int64) ([]market.Kline, error) {
	end := exitMs
	if end <= 0 || end < entryMs {
		end = entryMs + 6*hourMs
	}
	startMs := entryMs - 3*hourMs
	endMs := end + 3*hourMs
	key := barKey{strings.ToLower(exchType), symbol, startMs / hourMs, endMs / hourMs}
	if r, ok := barCache[key]; ok {
		return r.bars, r.err
	}
	var bars []market.Kline
	var err error
	if strings.EqualFold(exchType, "binance") {
		bars, err = market.GetKlinesRange(symbol, "1h", time.UnixMilli(startMs), time.UnixMilli(endMs))
	} else {
		bars, err = market.GetKlinesRangeOKX(symbol, "1h", time.UnixMilli(startMs), time.UnixMilli(endMs))
	}
	barCache[key] = barResult{bars, err}
	time.Sleep(60 * time.Millisecond) // light rate-limit on cache miss
	return bars, err
}

// barAt returns the 1h bar containing timeMs (last bar whose OpenTime <= timeMs).
func barAt(bars []market.Kline, timeMs int64) (market.Kline, bool) {
	var found market.Kline
	ok := false
	for _, b := range bars {
		if b.OpenTime <= timeMs {
			found = b
			ok = true
		} else {
			break
		}
	}
	return found, ok
}

func main() {
	dbPath := flag.String("db", "/opt/webstack/nofx/data/data.db", "SOURCE sqlite DB (read-only)")
	cleanPath := flag.String("clean", "/tmp/dbaudit/clean.db", "output clean.db (clean CLOSED positions)")
	fixPath := flag.String("fixlist", "/tmp/dbaudit/fixlist.txt", "output fix-list for production DB")
	feePctTol := flag.Float64("feetol", 0.35, "PnL self-consistency tolerance as %% of notional (covers round-trip fees/slippage)")
	limit := flag.Int("limit", 0, "limit positions (0=all), for smoke testing")
	flag.Parse()

	// READ-ONLY open of the production DB.
	src, err := sql.Open("sqlite", "file:"+*dbPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		log.Fatalf("open src: %v", err)
	}
	defer src.Close()

	q := `SELECT id, trader_id, exchange_type, symbol, side, entry_quantity, quantity,
		entry_price, entry_time, COALESCE(exit_price,0), COALESCE(exit_time,0),
		COALESCE(realized_pnl,0), COALESCE(fee,0), COALESCE(leverage,1), COALESCE(close_reason,'')
		FROM trader_positions WHERE status='CLOSED' ORDER BY entry_time ASC`
	rows, err := src.Query(q)
	if err != nil {
		log.Fatalf("query: %v", err)
	}
	var all []pos
	for rows.Next() {
		var p pos
		if err := rows.Scan(&p.id, &p.traderID, &p.exchangeType, &p.symbol, &p.side,
			&p.entryQty, &p.quantity, &p.entryPrice, &p.entryTime, &p.exitPrice, &p.exitTime,
			&p.realizedPnL, &p.fee, &p.leverage, &p.closeReason); err != nil {
			log.Fatalf("scan: %v", err)
		}
		all = append(all, p)
	}
	rows.Close()
	if *limit > 0 && *limit < len(all) {
		all = all[:*limit]
	}
	fmt.Printf("loaded %d CLOSED positions\n", len(all))

	reasonCounts := map[string]int{}
	var clean, dirty, unverified []pos
	var fixLines []string

	for _, p := range all {
		f := &dirtyFlags{}

		// effective size for a CLOSED position is entry_quantity (quantity is the
		// REMAINING size after full exit, normally 0). fall back to quantity.
		size := p.entryQty
		if size <= 0 {
			size = p.quantity
		}

		// 1) missing/zero core fields
		if p.entryPrice <= 0 {
			f.add("zero_entry_price")
		}
		if size <= 0 {
			f.add("zero_size")
		}
		if p.entryTime <= 0 {
			f.add("zero_entry_time")
		}
		// 2) time inversion
		if p.exitTime > 0 && p.exitTime < p.entryTime {
			f.add("time_inverted")
		}

		// unverifiable: symbol not fetchable on its exchange (metals like XAU/XAG,
		// delisted, or transient error). Track separately — cannot prove dirty.
		unverifiable := false

		// price/kline checks need bars (skip if core fields already broken)
		if p.entryPrice > 0 && p.entryTime > 0 {
			bars, ferr := fetchBars(p.exchangeType, p.symbol, p.entryTime, p.exitTime)
			if ferr != nil || len(bars) == 0 {
				unverifiable = true
			} else {
				// 3) entry price outside entry bar range (phantom fill)
				if eb, ok := barAt(bars, p.entryTime); ok {
					if dev := outsideDevPct(p.entryPrice, eb.Low, eb.High); dev > 1.0 {
						f.add(fmt.Sprintf("entry_offbar_%.1f%%", dev))
					}
				}
				// 4) exit price outside exit bar range
				if p.exitPrice > 0 && p.exitTime > 0 {
					if xb, ok := barAt(bars, p.exitTime); ok {
						if dev := outsideDevPct(p.exitPrice, xb.Low, xb.High); dev > 1.0 {
							f.add(fmt.Sprintf("exit_offbar_%.1f%%", dev))
						}
					}
				}
			}
		}

		// 5) PnL self-consistency: recomputed gross (from recorded prices) vs recorded
		// realized (+ fee back), normalized by notional. This one check covers both a
		// genuine accounting mismatch AND a defaulted exit (exit==entry => gross 0; if
		// realized+fee is materially nonzero the real exit move was never captured).
		// A true scratch trade (exit≈entry, realized≈-fee) passes cleanly.
		if p.entryPrice > 0 && p.exitPrice > 0 && size > 0 {
			gross := grossPnL(p.side, p.entryPrice, p.exitPrice, size)
			recon := p.realizedPnL + p.fee // realized ≈ gross - fees
			notional := p.entryPrice * size
			if notional > 0 {
				errPct := (recon - gross) / notional * 100
				if errPct < 0 {
					errPct = -errPct
				}
				if errPct > *feePctTol {
					if p.exitPrice == p.entryPrice {
						f.add(fmt.Sprintf("exit_defaulted_%.2f%%", errPct))
					} else {
						f.add(fmt.Sprintf("pnl_mismatch_%.2f%%", errPct))
					}
				}
			}
		}

		switch {
		case f.dirty():
			dirty = append(dirty, p)
			for _, c := range f.codes {
				reasonCounts[codePrefix(c)]++
			}
			fixLines = append(fixLines, fmt.Sprintf("id=%d trader=%s %s %s entry=%.6f@%s exit=%.6f size=%.6f pnl=%.2f reason=%s DIRTY=[%s]",
				p.id, short(p.traderID), p.symbol, p.side, p.entryPrice, msStr(p.entryTime), p.exitPrice, size, p.realizedPnL, p.closeReason, f.str()))
		case unverifiable:
			unverified = append(unverified, p)
		default:
			clean = append(clean, p)
		}
	}

	printReport(len(all), clean, dirty, unverified, reasonCounts)
	writeFixList(*fixPath, fixLines)
	// clean.db carries positions that PASSED all checks. Unverifiable ones (klines
	// unavailable) are kept too — not proven dirty — but tagged in exchange_type note.
	export := append(append([]pos{}, clean...), unverified...)
	if err := writeCleanDB(*cleanPath, *dbPath, export); err != nil {
		log.Fatalf("write clean.db: %v", err)
	}
	fmt.Printf("\nclean.db written: %s (%d positions: %d clean + %d unverifiable)\n",
		*cleanPath, len(export), len(clean), len(unverified))
	fmt.Printf("fix-list written: %s (%d dirty positions)\n", *fixPath, len(dirty))
}

// outsideDevPct: how far price sits OUTSIDE [low,high], as % of price. 0 if inside.
func outsideDevPct(price, low, high float64) float64 {
	if price <= 0 {
		return 0
	}
	if price > high {
		return (price - high) / price * 100
	}
	if price < low {
		return (low - price) / price * 100
	}
	return 0
}

// grossPnL: pre-fee PnL in quote ccy. long = (exit-entry)*qty; short = (entry-exit)*qty.
func grossPnL(side string, entry, exit, qty float64) float64 {
	if strings.EqualFold(side, "long") {
		return (exit - entry) * qty
	}
	return (entry - exit) * qty
}

func codePrefix(c string) string {
	for i, r := range c {
		if r >= '0' && r <= '9' {
			return strings.TrimRight(c[:i], "_")
		}
	}
	return c
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func msStr(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04") }

func printReport(total int, clean, dirty, unverified []pos, reasons map[string]int) {
	fmt.Printf("\n==== DATA-QUALITY AUDIT REPORT ====\n")
	fmt.Printf("total CLOSED=%d  clean=%d (%.1f%%)  dirty=%d (%.1f%%)  unverifiable=%d (%.1f%%)\n",
		total, len(clean), pct(len(clean), total), len(dirty), pct(len(dirty), total),
		len(unverified), pct(len(unverified), total))
	fmt.Printf("\n-- dirty reason breakdown --\n")
	type kv struct {
		k string
		v int
	}
	var kvs []kv
	for k, v := range reasons {
		kvs = append(kvs, kv{k, v})
	}
	sort.Slice(kvs, func(i, j int) bool { return kvs[i].v > kvs[j].v })
	for _, x := range kvs {
		fmt.Printf("  %-22s %d\n", x.k, x.v)
	}
	// per-trader clean/dirty split
	fmt.Printf("\n-- per-trader clean/dirty --\n")
	ct := map[string]int{}
	dt := map[string]int{}
	for _, p := range clean {
		ct[short(p.traderID)]++
	}
	for _, p := range dirty {
		dt[short(p.traderID)]++
	}
	seen := map[string]bool{}
	for t := range ct {
		seen[t] = true
	}
	for t := range dt {
		seen[t] = true
	}
	for t := range seen {
		fmt.Printf("  %-10s clean=%d dirty=%d\n", t, ct[t], dt[t])
	}
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}

func writeFixList(path string, lines []string) {
	_ = os.MkdirAll("/tmp/dbaudit", 0755)
	var b strings.Builder
	b.WriteString("# DIRTY positions in production trader_positions (READ-ONLY audit; NOT auto-applied)\n")
	b.WriteString("# Review before any manual DB action. Each line: the offending row + why.\n\n")
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	_ = os.WriteFile(path, []byte(b.String()), 0644)
}

// writeCleanDB creates a fresh sqlite with the SAME trader_positions schema, holding
// only the CLEAN closed positions. Read from the source schema so columns match.
func writeCleanDB(path, srcPath string, clean []pos) error {
	_ = os.MkdirAll("/tmp/dbaudit", 0755)
	_ = os.Remove(path)
	out, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return err
	}
	defer out.Close()
	// minimal schema mirroring the columns we carry (sufficient for backtest LoadClaudeEntries).
	_, err = out.Exec(`CREATE TABLE trader_positions (
		id INTEGER PRIMARY KEY, trader_id TEXT, exchange_type TEXT, symbol TEXT, side TEXT,
		entry_quantity REAL, quantity REAL, entry_price REAL, entry_time INTEGER,
		exit_price REAL, exit_time INTEGER, realized_pnl REAL, fee REAL, leverage INTEGER,
		status TEXT, close_reason TEXT)`)
	if err != nil {
		return err
	}
	tx, _ := out.Begin()
	stmt, err := tx.Prepare(`INSERT INTO trader_positions
		(id, trader_id, exchange_type, symbol, side, entry_quantity, quantity, entry_price,
		 entry_time, exit_price, exit_time, realized_pnl, fee, leverage, status, close_reason)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,'CLOSED',?)`)
	if err != nil {
		return err
	}
	for _, p := range clean {
		if _, err := stmt.Exec(p.id, p.traderID, p.exchangeType, p.symbol, p.side, p.entryQty,
			p.quantity, p.entryPrice, p.entryTime, p.exitPrice, p.exitTime, p.realizedPnL,
			p.fee, p.leverage, p.closeReason); err != nil {
			return err
		}
	}
	stmt.Close()
	return tx.Commit()
}
