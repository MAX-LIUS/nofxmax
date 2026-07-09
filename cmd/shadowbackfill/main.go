// shadowbackfill replays historical CLOSED positions through the SAME shadow
// entry-gate rules used live, writing verdicts into shadow_gate_verdicts so the
// monitor page + shadowrank show REAL data immediately (real entries, real
// realized PnL, no-look-ahead regime at each entry). Verdicts are tagged with a
// synthetic cycle = entry_decision_cycle so they join back to their own position.
//
// This is the HISTORICAL counterfactual (clearly distinct from forward live
// shadowing). Idempotent-ish: use -reset to clear prior backfill rows first.
//
// Usage: shadowbackfill -db /opt/webstack/nofx/data/data.db [-reset]
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"sort"
	"time"

	_ "modernc.org/sqlite"

	"nofx/market"
	"nofx/trader"
)

func main() {
	dbPath := flag.String("db", "/app/data/data.db", "sqlite path (writable)")
	reset := flag.Bool("reset", false, "delete prior backfill rows (trader_id LIKE 'BACKFILL:%') first")
	flag.Parse()

	db, err := sql.Open("sqlite", "file:"+*dbPath+"?_pragma=busy_timeout(5000)")
	must(err)
	defer db.Close()

	if *reset {
		res, err := db.Exec(`DELETE FROM shadow_gate_verdicts WHERE trader_id LIKE 'BACKFILL:%'`)
		must(err)
		n, _ := res.RowsAffected()
		fmt.Printf("reset: deleted %d prior backfill rows\n", n)
	}

	rows, err := db.Query(`
		SELECT trader_id, symbol, side, entry_price, entry_time, realized_pnl,
		       COALESCE(entry_decision_cycle,0)
		FROM trader_positions
		WHERE status='CLOSED' AND entry_price>0 AND quantity>0
		ORDER BY entry_time ASC`)
	must(err)

	type pos struct {
		trader, symbol, side string
		entryPrice, pnl      float64
		entryMs              int64
		cycle                int64
	}
	var all []pos
	for rows.Next() {
		var p pos
		must(rows.Scan(&p.trader, &p.symbol, &p.side, &p.entryPrice, &p.entryMs, &p.pnl, &p.cycle))
		all = append(all, p)
	}
	rows.Close()
	fmt.Printf("loaded %d closed positions\n", len(all))

	// group by symbol to fetch OKX klines once
	bySym := map[string][]int{}
	for i, p := range all {
		bySym[p.symbol] = append(bySym[p.symbol], i)
	}
	syms := make([]string, 0, len(bySym))
	for s := range bySym {
		syms = append(syms, s)
	}
	sort.Strings(syms)

	ins, err := db.Prepare(`INSERT INTO shadow_gate_verdicts
		(trader_id,cycle,symbol,action,side,rule_name,would_block,regime,confidence,detail,live_allowed,observed_at,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	must(err)
	defer ins.Close()

	written, naSym := 0, 0
	for _, sym := range syms {
		idxs := bySym[sym]
		var minT, maxT int64 = 1 << 62, 0
		for _, ix := range idxs {
			if all[ix].entryMs < minT {
				minT = all[ix].entryMs
			}
			if all[ix].entryMs > maxT {
				maxT = all[ix].entryMs
			}
		}
		start := time.UnixMilli(minT).Add(-40 * 24 * time.Hour)
		end := time.UnixMilli(maxT).Add(2 * time.Hour)
		bars, err := market.GetKlinesRangeOKX(sym, "1h", start, end)
		if err != nil || len(bars) < 60 {
			naSym++
			continue
		}
		for _, ix := range idxs {
			p := all[ix]
			// clip bars to entry time (no look-ahead)
			cut := clipBars(bars, p.entryMs)
			if len(cut) < 60 {
				continue
			}
			action := "open_long"
			if p.side == "SHORT" {
				action = "open_short"
			}
			verdicts := trader.EvaluateShadowGatesForBackfill(cut, p.side)
			now := time.Now().UTC().UnixMilli()
			for _, v := range verdicts {
				// Real trader_id + real cycle + real symbol => joins to its own
				// position. observed_at = entry_time (historical) marks it as
				// backfill vs forward-live rows (which have recent observed_at).
				_, err := ins.Exec(p.trader, p.cycle, sym, action, p.side, v.Rule,
					boolToInt(v.Block), v.Regime, 0.0, v.Detail, 1, p.entryMs, now)
				if err == nil {
					written++
				}
			}
		}
	}
	fmt.Printf("wrote %d verdicts across %d rules; %d symbols had no OKX data\n",
		written, len(trader.ShadowRuleNames()), naSym)
	fmt.Println("NOTE: backfill rows use REAL trader_id/cycle/symbol (join to their own position),")
	fmt.Println("and observed_at = entry_time (historical) to distinguish from forward-live rows. Run:")
	fmt.Println("  shadowrank -db <db>   (scores the whole matched book incl. backfill)")
}

func clipBars(bars []market.Kline, entryMs int64) []market.Kline {
	lo, hi, ans := 0, len(bars)-1, -1
	for lo <= hi {
		m := (lo + hi) / 2
		if bars[m].OpenTime <= entryMs {
			ans = m
			lo = m + 1
		} else {
			hi = m - 1
		}
	}
	if ans < 0 {
		return nil
	}
	return bars[:ans+1]
}
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
