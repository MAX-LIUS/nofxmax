// Close-event attribution backfill. Recomputes category/mechanism/close_reason
// for historical position_close_events rows that were dumped into the
// sync_external bucket before the close-intent ledger existed.
//
// Evidence source: the parent trader_positions.close_reason, which was already
// rebuilt deterministically (cmd/attribrebuild) from order tags + decision-cycle
// linkage. Each sync_external event inherits its position's resolved reason.
// Reasons that are themselves bare/unrecorded (market_close, close_long, empty)
// collapse to legacy_unknown so review can tell "genuinely unknown" apart from a
// real mechanism. Never overrides an already-classified (non-sync_external) row.
//
// Default is DRY-RUN. Pass --apply to write. Always run on a COPY first.
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

func main() {
	dbPath := flag.String("db", "/tmp/v.db", "path to SQLite DB (use a COPY first)")
	apply := flag.Bool("apply", false, "actually write changes (default: dry-run)")
	traderLike := flag.String("trader", "%", "trader_id LIKE filter")
	flag.Parse()

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT e.id, COALESCE(p.close_reason,'')
		FROM position_close_events e
		LEFT JOIN trader_positions p ON p.id = e.position_id
		WHERE e.mechanism = 'sync_external' AND e.trader_id LIKE ?`, *traderLike)
	if err != nil {
		log.Fatalf("query events: %v", err)
	}
	type upd struct {
		id        int64
		reason    string
		category  string
		mechanism string
	}
	var updates []upd
	newCounts := map[string]int{}
	total := 0
	for rows.Next() {
		var id int64
		var posReason string
		if err := rows.Scan(&id, &posReason); err != nil {
			log.Fatalf("scan: %v", err)
		}
		total++
		reason := resolveReason(posReason)
		cat, mech := classify(reason)
		newCounts[mech]++
		updates = append(updates, upd{id, reason, cat, mech})
	}
	rows.Close()

	fmt.Printf("backfill: %d sync_external events (apply=%v)\n", total, *apply)
	fmt.Println("==== NEW mechanism distribution ====")
	printSortedCounts(newCounts)

	if !*apply {
		fmt.Println("DRY-RUN: no changes written. Re-run with --apply to persist.")
		return
	}

	tx, err := db.Begin()
	if err != nil {
		log.Fatalf("begin tx: %v", err)
	}
	for _, u := range updates {
		if _, err := tx.Exec(
			`UPDATE position_close_events SET close_reason=?, category=?, mechanism=? WHERE id=?`,
			u.reason, u.category, u.mechanism, u.id); err != nil {
			tx.Rollback()
			log.Fatalf("update %d: %v", u.id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		log.Fatalf("commit: %v", err)
	}
	fmt.Printf("APPLIED: updated %d close-event rows\n", len(updates))
}

// resolveReason maps a parent position close_reason onto the event. Bare or
// empty/legacy reasons collapse to legacy_unknown.
func resolveReason(posReason string) string {
	r := strings.ToLower(strings.TrimSpace(posReason))
	switch r {
	case "", "close_long", "close_short", "market_close",
		"sync_absent_from_exchange", "sync_external", "unknown", "close_by_side":
		return "legacy_unknown"
	}
	return r
}

// classify mirrors store.ClassifyClose for the reasons this backfill produces.
// Kept local so the tool stays CGO-free (pure-Go sqlite driver). It must stay in
// sync with store/attribution.go.
func classify(rawReason string) (category, mechanism string) {
	r := strings.ToLower(strings.TrimSpace(rawReason))
	switch {
	case r == "":
		return "system", "unknown_close"
	case strings.HasPrefix(r, "manual_close") || r == "manual":
		return "manual", "manual_close"
	case strings.Contains(r, "liquidat") || r == "adl":
		return "exchange", "liquidation"
	case strings.Contains(r, "giveback_guard") || strings.Contains(r, "breadth_breaker") || strings.Contains(r, "breadth"):
		return "protection", "breadth_breaker"
	case strings.Contains(r, "managed_drawdown"):
		return "protection", "managed_drawdown"
	case strings.Contains(r, "native_trailing") || r == "trailing":
		return "protection", "native_trailing"
	case strings.Contains(r, "trailing_take_profit"):
		return "protection", "trailing_take_profit"
	case strings.Contains(r, "break_even"):
		return "protection", "break_even_stop"
	case strings.Contains(r, "ladder_tp"):
		return "protection", "ladder_tp"
	case strings.Contains(r, "ladder_sl"):
		return "protection", "ladder_sl"
	case strings.Contains(r, "fallback_maxloss"):
		return "protection", "fallback_maxloss_sl"
	case strings.Contains(r, "full_tp"):
		return "protection", "full_tp"
	case strings.Contains(r, "full_sl"):
		return "protection", "full_sl"
	case strings.Contains(r, "time_stop"):
		return "protection", "time_stop"
	case strings.Contains(r, "max_hold"):
		return "protection", "max_hold"
	case strings.Contains(r, "emergency"):
		return "protection", "emergency_protection_close"
	case strings.HasPrefix(r, "ai_close"):
		return "ai", "ai_close"
	case strings.Contains(r, "legacy_unknown"):
		return "system", "legacy_unknown"
	case strings.Contains(r, "sync_absent") || strings.Contains(r, "sync_external"):
		return "exchange", "sync_external"
	case r == "close_long" || r == "close_short":
		return "exchange", "sync_external"
	}
	return "system", "unknown_close"
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
