package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

// Attribution rebuild tool. Recomputes trader_positions.close_reason for every
// CLOSED position from canonical order data, using a deterministic taxonomy:
//
//	1. protection tag on the dominant closing order (full_tp/full_sl/
//	   break_even_stop/native_trailing/ladder_*/managed_drawdown)
//	2. ai_close   — exit_decision_cycle>0 links to a real decision record
//	3. market_close — none of the above (origin genuinely not recorded)
//
// Default is DRY-RUN (no writes). Pass --apply to write. Always run on a copy
// first and keep the backup.
func main() {
	dbPath := flag.String("db", "/tmp/v.db", "path to SQLite DB (use a COPY first)")
	apply := flag.Bool("apply", false, "actually write changes (default: dry-run)")
	traderLike := flag.String("trader", "%", "trader_id LIKE filter")
	showDiff := flag.Bool("diff", false, "print the old->new transition matrix")
	safe := flag.Bool("safe", false, "additive recovery only: upgrade data-loss/sync rows to a recovered protection label, but NEVER overwrite an already-trusted or portfolio-level label")
	flag.Parse()

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT id, trader_id, exchange_id, symbol, side, quantity, entry_price,
		       exit_decision_cycle, close_reason
		FROM trader_positions
		WHERE status='CLOSED' AND trader_id LIKE ?`, *traderLike)
	if err != nil {
		log.Fatalf("query positions: %v", err)
	}
	type posRow struct {
		id            int64
		traderID      string
		exchangeID    string
		symbol, side  string
		qty, entry    float64
		exitCycle     int64
		oldReason     string
	}
	var positions []posRow
	for rows.Next() {
		var p posRow
		var exitCycle sql.NullInt64
		var oldReason sql.NullString
		if err := rows.Scan(&p.id, &p.traderID, &p.exchangeID, &p.symbol, &p.side,
			&p.qty, &p.entry, &exitCycle, &oldReason); err != nil {
			log.Fatalf("scan: %v", err)
		}
		p.exitCycle = exitCycle.Int64
		p.oldReason = oldReason.String
		positions = append(positions, p)
	}
	rows.Close()

	fmt.Printf("rebuild: %d closed positions (apply=%v)\n", len(positions), *apply)

	newCounts := map[string]int{}
	changed := 0
	transitions := map[string]int{} // "old -> new" : count
	var updates []struct {
		id     int64
		reason string
	}
	skippedProtected := 0
	for _, p := range positions {
		reason := deriveReasonForPosition(db, p.id, p.exchangeID, p.symbol, p.side, p.entry, p.exitCycle, p.traderID)
		// SAFE mode: only touch rows whose CURRENT label is untrusted
		// (data-loss/sync/empty), and only when the rebuild recovers a genuine
		// trusted mechanism. Never overwrite an already-trusted or
		// portfolio-level label, and never rewrite one untrusted label to
		// another untrusted one (e.g. close_short -> ai_close is not a recovery).
		if *safe {
			if isTrustedOrPortfolio(p.oldReason) || !isTrustedOrPortfolio(reason) {
				if isTrustedOrPortfolio(p.oldReason) && reason != p.oldReason {
					skippedProtected++
				}
				newCounts[normReason(p.oldReason)]++
				continue
			}
		}
		newCounts[reason]++
		if reason != p.oldReason {
			changed++
			old := p.oldReason
			if old == "" {
				old = "(empty)"
			}
			transitions[old+" -> "+reason]++
			updates = append(updates, struct {
				id     int64
				reason string
			}{p.id, reason})
		}
	}
	if *safe {
		fmt.Printf("SAFE mode: protected %d already-trusted/portfolio rows from overwrite\n", skippedProtected)
	}

	fmt.Println("==== NEW close_reason distribution ====")
	printSortedCounts(newCounts)
	fmt.Printf("changed rows: %d / %d\n", changed, len(positions))

	if *showDiff {
		fmt.Println("==== TRANSITIONS (old -> new) ====")
		printSortedCounts(transitions)
	}

	if !*apply {
		fmt.Println("DRY-RUN: no changes written. Re-run with --apply to persist.")
		return
	}

	tx, err := db.Begin()
	if err != nil {
		log.Fatalf("begin tx: %v", err)
	}
	for _, u := range updates {
		if _, err := tx.Exec(`UPDATE trader_positions SET close_reason=? WHERE id=?`, u.reason, u.id); err != nil {
			tx.Rollback()
			log.Fatalf("update %d: %v", u.id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		log.Fatalf("commit: %v", err)
	}
	fmt.Printf("APPLIED: updated %d position rows\n", len(updates))
}

func printSortedCounts(m map[string]int) {
	type kv struct {
		k string
		v int
	}
	var items []kv
	for k, v := range m {
		items = append(items, kv{k, v})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].v > items[j].v })
	for _, it := range items {
		fmt.Printf("  %-28s %d\n", it.k, it.v)
	}
}

// isTrustedOrPortfolio reports whether a label is a genuine protection mechanism
// (per-entry) or a portfolio-level trigger — i.e. attribution we already trust
// and must NOT overwrite in safe mode.
func isTrustedOrPortfolio(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "full_sl", "ladder_sl", "fallback_maxloss_sl",
		"full_tp", "ladder_tp",
		"break_even_stop",
		"managed_drawdown", "native_trailing", "trailing_take_profit",
		"max_hold", "time_stop",
		"trend_reversal_flip",
		"ai_close", "ai_close_long", "ai_close_short",
		"liquidation", "emergency_protection_close",
		"giveback_guard_breadth", "breadth_breaker",
		"equity_breaker", "portfolio_breaker":
		return true
	default:
		return false
	}
}

func normReason(r string) string {
	if r == "" {
		return "(empty)"
	}
	return r
}

var _ = strings.Contains
