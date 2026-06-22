package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"math"

	_ "modernc.org/sqlite"
)

// Net-position merge tool (fix 2026-06-22 reconciler churn).
//
// The exchange runs one-way (net) mode, so there must be at most ONE OPEN
// trader_positions row per (trader_id, symbol, side). A sync race produced
// duplicate OPEN rows; the protection reconciler then sized ladder tiers from
// only the newest row's entry_quantity while the exchange held the merged net
// position, so plan TP prices never matched and the reconciler churned forever.
//
// This tool folds every duplicate OPEN group into a single net row:
//   - keep the earliest entry_time row (primary)
//   - quantity / entry_quantity / fee / realized_pnl summed
//   - entry_price = quantity-weighted average
//   - secondary rows -> CLOSED, quantity 0, close_reason merged_into_net_position
//
// Default is DRY-RUN. Pass --apply to write. Always run on the live DB only
// after a backup, or on a copy first.
func main() {
	dbPath := flag.String("db", "/tmp/v.db", "path to SQLite DB (back up first)")
	apply := flag.Bool("apply", false, "actually write changes (default: dry-run)")
	flag.Parse()

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Find (trader_id, symbol, side) groups with >1 OPEN row.
	keyRows, err := db.Query(`
		SELECT trader_id, symbol, side, COUNT(*) n
		FROM trader_positions
		WHERE status='OPEN'
		GROUP BY trader_id, symbol, side
		HAVING COUNT(*) > 1
		ORDER BY n DESC`)
	if err != nil {
		log.Fatalf("query duplicate keys: %v", err)
	}
	type key struct {
		trader, symbol, side string
		n                    int
	}
	var keys []key
	for keyRows.Next() {
		var k key
		if err := keyRows.Scan(&k.trader, &k.symbol, &k.side, &k.n); err != nil {
			log.Fatalf("scan key: %v", err)
		}
		keys = append(keys, k)
	}
	keyRows.Close()

	if len(keys) == 0 {
		fmt.Println("No duplicate OPEN net-position groups found. Nothing to merge.")
		return
	}

	fmt.Printf("Found %d duplicate OPEN net-position group(s):\n\n", len(keys))
	mode := "DRY-RUN"
	if *apply {
		mode = "APPLY"
	}

	totalMerged := 0
	for _, k := range keys {
		rows, err := db.Query(`
			SELECT id, quantity, entry_quantity, entry_price, fee, realized_pnl, entry_time
			FROM trader_positions
			WHERE trader_id=? AND symbol=? AND side=? AND status='OPEN'
			ORDER BY entry_time ASC, id ASC`, k.trader, k.symbol, k.side)
		if err != nil {
			log.Fatalf("query group rows: %v", err)
		}
		type row struct {
			id                                  int64
			qty, entryQty, entry, fee, pnl, etime float64
		}
		var legs []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.qty, &r.entryQty, &r.entry, &r.fee, &r.pnl, &r.etime); err != nil {
				log.Fatalf("scan leg: %v", err)
			}
			if r.entryQty == 0 {
				r.entryQty = r.qty
			}
			legs = append(legs, r)
		}
		rows.Close()
		if len(legs) <= 1 {
			continue
		}

		primary := legs[0]
		var sumQty, sumEntryQty, sumFee, sumPnL, wNum, wDen float64
		var mergedIDs []int64
		for _, r := range legs {
			sumQty += r.qty
			sumEntryQty += r.entryQty
			sumFee += r.fee
			sumPnL += r.pnl
			if r.entry > 0 && r.qty > 0 {
				wNum += r.entry * r.qty
				wDen += r.qty
			}
			if r.id != primary.id {
				mergedIDs = append(mergedIDs, r.id)
			}
		}
		wEntry := primary.entry
		if wDen > 0 {
			wEntry = wNum / wDen
		}
		sumQty = math.Round(sumQty*1e8) / 1e8
		sumEntryQty = math.Round(sumEntryQty*1e8) / 1e8

		fmt.Printf("[%s] %s %s %s | %d legs -> keep id=%d, fold %v\n",
			mode, k.trader, k.symbol, k.side, len(legs), primary.id, mergedIDs)
		fmt.Printf("        netQty=%.8f entryQty=%.8f weightedEntry=%.8f fee=%.6f pnl=%.6f\n",
			sumQty, sumEntryQty, wEntry, sumFee, sumPnL)

		if !*apply {
			continue
		}

		tx, err := db.Begin()
		if err != nil {
			log.Fatalf("begin tx: %v", err)
		}
		if _, err := tx.Exec(`
			UPDATE trader_positions
			SET quantity=?, entry_quantity=?, entry_price=?, fee=?, realized_pnl=?
			WHERE id=?`, sumQty, sumEntryQty, wEntry, sumFee, sumPnL, primary.id); err != nil {
			tx.Rollback()
			log.Fatalf("update primary %d: %v", primary.id, err)
		}
		for _, id := range mergedIDs {
			if _, err := tx.Exec(`
				UPDATE trader_positions
				SET status='CLOSED', quantity=0, close_reason='merged_into_net_position'
				WHERE id=?`, id); err != nil {
				tx.Rollback()
				log.Fatalf("retire merged %d: %v", id, err)
			}
		}
		if err := tx.Commit(); err != nil {
			log.Fatalf("commit: %v", err)
		}
		totalMerged += len(mergedIDs)
	}

	fmt.Println()
	if *apply {
		fmt.Printf("APPLIED: merged %d secondary row(s) across %d group(s).\n", totalMerged, len(keys))
	} else {
		fmt.Printf("DRY-RUN complete. %d group(s) would be merged. Re-run with --apply to write.\n", len(keys))
	}
}
