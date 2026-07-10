// Command shadowlink — reconciles forward-live shadow verdicts to their real
// position.
//
// A forward verdict is recorded at the moment of the OPEN decision, BEFORE the
// position exists, so its position_id is 0. The (trader,cycle,symbol) key that
// backfill uses does NOT work forward: live positions arrive via exchange sync
// with entry_decision_cycle=0 (or a different counter than the AI cycleNumber
// the verdict stored). The reliable key is TIME: a verdict's observed_at matches
// the resulting position's entry_time within seconds (execution latency).
//
// This tool assigns position_id to forward verdicts (position_id=0) by matching,
// per (trader_id, symbol, side), the position whose entry_time is nearest to the
// verdict's observed_at within a tight window. Ambiguous matches (two positions
// within the window) are skipped and left unlinked. Idempotent: only touches
// rows still at position_id=0. After linking, forward verdicts join to positions
// by position_id exactly like backfill — one clean, uniform path.
//
// Usage: shadowlink -db /opt/webstack/nofx/data/data.db [-window 300] [-dry]
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

func main() {
	dbPath := flag.String("db", "/opt/webstack/nofx/data/data.db", "sqlite db path")
	windowSec := flag.Int64("window", 300, "max |observed_at - entry_time| seconds to accept a match")
	dry := flag.Bool("dry", false, "report only, do not write position_id")
	flag.Parse()

	mode := "rwc"
	if *dry {
		mode = "ro"
	}
	db, err := sql.Open("sqlite", "file:"+*dbPath+"?mode="+mode)
	must(err)
	defer db.Close()

	run(db, *windowSec*1000, *dry)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

// pos is one closed-or-open position candidate for matching.
type pos struct {
	id      int64
	entryMs int64
	used    bool // a verdict-decision already claimed this position
}

// decision groups all rule-verdicts of ONE forward open decision. They share
// trader/symbol/side/observed_at, so they must all link to the SAME position.
type decision struct {
	trader, symbol, side string
	observedMs           int64
	verdictIDs           []int64
}

func run(db *sql.DB, windowMs int64, dry bool) {
	// 1) Load forward verdicts still unlinked (position_id=0), grouped into
	//    decisions by (trader,symbol,side,observed_at). Forward rows are the ones
	//    written live: observed_at is "now" at decision time (not entry_time like
	//    backfill), but either way position_id=0 marks "needs linking".
	rows, err := db.Query(`
		SELECT id, trader_id, symbol, side, observed_at
		FROM shadow_gate_verdicts
		WHERE position_id = 0
		ORDER BY observed_at ASC`)
	must(err)
	decByKey := map[string]*decision{}
	var order []string
	total := 0
	for rows.Next() {
		var id, obs int64
		var trader, symbol, side string
		must(rows.Scan(&id, &trader, &symbol, &side, &obs))
		total++
		k := fmt.Sprintf("%s|%s|%s|%d", trader, symbol, side, obs)
		d := decByKey[k]
		if d == nil {
			d = &decision{trader: trader, symbol: symbol, side: strings.ToUpper(side), observedMs: obs}
			decByKey[k] = d
			order = append(order, k)
		}
		d.verdictIDs = append(d.verdictIDs, id)
	}
	rows.Close()
	fmt.Printf("unlinked forward verdicts: %d across %d decisions\n", total, len(order))
	if len(order) == 0 {
		fmt.Println("nothing to link.")
		return
	}

	// 2) Candidate positions keyed by (trader,symbol,side). side is stored UPPER
	//    on positions. Load id + entry_time; match nearest entry_time to a
	//    decision's observed_at within the window.
	posByKey := map[string][]*pos{}
	prows, err := db.Query(`
		SELECT id, trader_id, symbol, side, entry_time
		FROM trader_positions
		WHERE entry_price > 0 AND entry_quantity > 0`)
	must(err)
	for prows.Next() {
		var id, entryMs int64
		var trader, symbol, side string
		must(prows.Scan(&id, &trader, &symbol, &side, &entryMs))
		k := trader + "|" + symbol + "|" + strings.ToUpper(side)
		posByKey[k] = append(posByKey[k], &pos{id: id, entryMs: entryMs})
	}
	prows.Close()

	// 3) Match. Process decisions in time order; for each, pick the nearest
	//    unused position within the window. If TWO positions fall inside the
	//    window (ambiguous), skip — better unlinked than mis-attributed.
	linked, ambiguous, nomatch := 0, 0, 0
	type upd struct {
		posID      int64
		verdictIDs []int64
	}
	var updates []upd
	for _, k := range order {
		d := decByKey[k]
		cands := posByKey[d.trader+"|"+d.symbol+"|"+d.side]
		var best *pos
		var bestDiff int64 = 1 << 62
		within := 0
		for _, c := range cands {
			if c.used {
				continue
			}
			diff := c.entryMs - d.observedMs
			if diff < 0 {
				diff = -diff
			}
			if diff <= windowMs {
				within++
				if diff < bestDiff {
					bestDiff, best = diff, c
				}
			}
		}
		switch {
		case within == 0:
			nomatch++
		case within > 1:
			// Two positions in-window for the same trader/symbol/side — cannot
			// disambiguate by time alone. Leave unlinked (audit-safe).
			ambiguous++
		default:
			best.used = true
			linked++
			updates = append(updates, upd{posID: best.id, verdictIDs: d.verdictIDs})
		}
	}

	fmt.Printf("linked: %d decisions | ambiguous(skipped): %d | no-match-in-window: %d\n",
		linked, ambiguous, nomatch)

	if dry {
		fmt.Println("[dry-run] no writes. re-run without -dry to persist position_id.")
		return
	}
	if len(updates) == 0 {
		fmt.Println("no writable links.")
		return
	}

	// 4) Persist. Stable order for deterministic runs.
	sort.Slice(updates, func(i, j int) bool { return updates[i].posID < updates[j].posID })
	tx, err := db.Begin()
	must(err)
	stmt, err := tx.Prepare(`UPDATE shadow_gate_verdicts SET position_id = ? WHERE id = ?`)
	must(err)
	wrote := 0
	for _, u := range updates {
		for _, vid := range u.verdictIDs {
			if _, err := stmt.Exec(u.posID, vid); err != nil {
				stmt.Close()
				tx.Rollback()
				must(err)
			}
			wrote++
		}
	}
	stmt.Close()
	must(tx.Commit())
	fmt.Printf("persisted position_id on %d verdict rows (%d decisions).\n", wrote, linked)
	fmt.Println("forward verdicts now join by position_id — same uniform path as backfill.")
}
