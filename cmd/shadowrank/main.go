// shadowrank ranks every shadow entry-gate rule on FORWARD live data with the
// SAME rigor used on the historical book: for each rule it reconstructs the real
// closed-position book, removes exactly the positions that rule WOULD have
// blocked, and measures the change in total PnL and CVaR95 — then compares that
// against a RANDOM control that drops the same COUNT of positions at random
// (5000 trials), and against an H1/H2 walk-forward split by entry_time.
//
// A rule is only "promising" if: (a) blkAvg << keepAvg, (b) it beats the random
// control on PnL AND CVaR at >95%, and (c) both halves (H1/H2) agree. Anything
// that passes full-sample but fails H1/H2 is the classic overfit trap the
// DecisionLog documents repeatedly — do NOT enforce it.
//
// Read-only. Usage: shadowrank -db /app/data/data.db [-trader %pat] [-trials 5000]
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"sort"

	_ "modernc.org/sqlite"
)

// posRow is one closed position (the "book" unit). A rule maps to the SET of
// positions it would block.
type posRow struct {
	pnl     float64
	entryMs int64
}

func main() {
	dbPath := flag.String("db", "/app/data/data.db", "sqlite path")
	traderLike := flag.String("trader", "%", "trader_id LIKE pattern")
	trials := flag.Int("trials", 5000, "random-control trials")
	minN := flag.Int("minn", 5, "min blocked count to score a rule")
	flag.Parse()

	db, err := sql.Open("sqlite", "file:"+*dbPath+"?mode=ro&immutable=1")
	if err != nil {
		panic(err)
	}
	defer db.Close()

	// One row per (rule, position). pkey = the exact position id, so the book is
	// keyed by real positions (no collapsing of distinct positions that share a
	// (trader,cycle,symbol) key). Join: backfill rows carry position_id (1:1 by
	// id); forward-live rows (position_id=0) fall back to the per-decision unique
	// (trader,cycle,symbol) with cycle>0, excluding ambiguous keys.
	rows, err := db.Query(`
		SELECT v.rule_name, v.would_block,
		       CAST(p.id AS TEXT) AS pkey,
		       p.realized_pnl, p.entry_time
		FROM shadow_gate_verdicts v
		JOIN trader_positions p
		  ON p.status = 'CLOSED'
		 AND (
		       (v.position_id > 0 AND p.id = v.position_id)
		    OR (v.position_id = 0 AND v.cycle > 0
		        AND p.trader_id = v.trader_id
		        AND p.entry_decision_cycle = v.cycle
		        AND p.symbol = v.symbol
		        AND p.entry_decision_cycle NOT IN (
		          SELECT entry_decision_cycle FROM trader_positions
		          WHERE status='CLOSED' AND entry_price>0
		          GROUP BY trader_id, entry_decision_cycle, symbol HAVING COUNT(*)>1
		        ))
		     )
		WHERE v.trader_id LIKE ?`, *traderLike)
	if err != nil {
		fmt.Println("no shadow_gate_verdicts table yet — deploy the shadow-gate build first, let it run, then re-run.")
		return
	}
	defer rows.Close()

	book := map[string]posRow{}              // pkey -> position (unique)
	blockedSet := map[string]map[string]bool{} // rule -> set of pkeys it blocks (deduped)
	ruleSet := map[string]bool{}
	for rows.Next() {
		var rule, pkey string
		var block bool
		var pnl float64
		var entryMs int64
		if err := rows.Scan(&rule, &block, &pkey, &pnl, &entryMs); err != nil {
			panic(err)
		}
		ruleSet[rule] = true
		book[pkey] = posRow{pnl: pnl, entryMs: entryMs}
		if block {
			if blockedSet[rule] == nil {
				blockedSet[rule] = map[string]bool{}
			}
			blockedSet[rule][pkey] = true
		}
	}
	if len(book) == 0 {
		fmt.Println("no matched verdict-position pairs yet — shadow gates need forward live data.")
		fmt.Println("(let the deployed build run until AI opens positions and they close, then re-run.)")
		return
	}

	// Build the book arrays + a stable pkey order + median entry time for H1/H2.
	pkeys := make([]string, 0, len(book))
	for k := range book {
		pkeys = append(pkeys, k)
	}
	sort.Strings(pkeys)
	times := make([]int64, 0, len(book))
	for _, k := range pkeys {
		times = append(times, book[k].entryMs)
	}
	sortedTimes := append([]int64(nil), times...)
	sort.Slice(sortedTimes, func(i, j int) bool { return sortedTimes[i] < sortedTimes[j] })
	medT := sortedTimes[len(sortedTimes)/2]

	baseAll := bookPnL(book, pkeys)
	fmt.Printf("book: %d unique closed positions matched to shadow verdicts\n", len(book))
	fmt.Printf("baseline total PnL = %+.2f | CVaR95 = %.3f\n\n", baseAll, cvarOf(book, pkeys))

	rules := make([]string, 0, len(ruleSet))
	for r := range ruleSet {
		rules = append(rules, r)
	}
	sort.Strings(rules)

	fmt.Printf("%-26s %5s %9s %8s | %-16s | %-22s\n",
		"rule", "blkN", "blkPnL", "blkAvg", "vs-random(PnL/CVaR)", "H1 / H2 (PnL beats rand)")
	rng := rand.New(rand.NewSource(42))
	for _, rule := range rules {
		bl := blockedSet[rule]
		if bl == nil {
			bl = map[string]bool{}
		}
		var blkPnL float64
		for k := range bl {
			blkPnL += book[k].pnl
		}
		n := len(bl)
		if n < *minN {
			fmt.Printf("%-26s %5d %9s  (need >=%d blocked to score)\n", rule, n, "-", *minN)
			continue
		}
		blkAvg := blkPnL / float64(n)

		pPnL, pCVaR := randomControl(book, pkeys, bl, *trials, rng)
		h1P := halfControl(book, pkeys, bl, times, medT, true, *trials, rng)
		h2P := halfControl(book, pkeys, bl, times, medT, false, *trials, rng)

		fmt.Printf("%-26s %5d %+9.2f %+8.3f | %5.1f%% / %5.1f%% | %5.1f%% / %5.1f%%\n",
			rule, n, blkPnL, blkAvg, pPnL, pCVaR, h1P, h2P)
	}
	fmt.Println("\nPASS bar: vs-random PnL & CVaR both >95%, AND H1 & H2 both >95%.")
	fmt.Println("full-sample good but H1/H2 split = overfit (see DecisionLog DEC-0110..0116). Do not enforce.")
}

// gatedPnL / cvar of the book after removing the blocked set.
func gatedPnL(book map[string]posRow, pkeys []string, drop map[string]bool) (float64, []float64) {
	sum := 0.0
	kept := make([]float64, 0, len(pkeys))
	for _, k := range pkeys {
		if drop[k] {
			continue
		}
		sum += book[k].pnl
		kept = append(kept, book[k].pnl)
	}
	return sum, kept
}

// randomControl: gate-vs-random on PnL and CVaR over the whole book.
func randomControl(book map[string]posRow, pkeys []string, blocked map[string]bool, trials int, rng *rand.Rand) (float64, float64) {
	gSum, gKept := gatedPnL(book, pkeys, blocked)
	gCVaR := cvar95(gKept)
	k := len(blocked)
	betterPnL, betterCVaR := 0, 0
	for t := 0; t < trials; t++ {
		perm := rng.Perm(len(pkeys))[:k]
		drop := make(map[string]bool, k)
		for _, i := range perm {
			drop[pkeys[i]] = true
		}
		rSum, rKept := gatedPnL(book, pkeys, drop)
		if gSum > rSum {
			betterPnL++
		}
		if gCVaR > cvar95(rKept) {
			betterCVaR++
		}
	}
	return 100 * float64(betterPnL) / float64(trials), 100 * float64(betterCVaR) / float64(trials)
}

// halfControl: same as randomControl but restricted to H1 (early) or H2 (late).
func halfControl(book map[string]posRow, pkeys []string, blocked map[string]bool, times []int64, medT int64, early bool, trials int, rng *rand.Rand) float64 {
	var sub []string
	for i, k := range pkeys {
		if (early && times[i] < medT) || (!early && times[i] >= medT) {
			sub = append(sub, k)
		}
	}
	subBlocked := map[string]bool{}
	for _, k := range sub {
		if blocked[k] {
			subBlocked[k] = true
		}
	}
	k := len(subBlocked)
	if k == 0 {
		return 0
	}
	gSum, _ := gatedPnL(book, sub, subBlocked)
	better := 0
	for t := 0; t < trials; t++ {
		perm := rng.Perm(len(sub))[:k]
		drop := make(map[string]bool, k)
		for _, i := range perm {
			drop[sub[i]] = true
		}
		rSum, _ := gatedPnL(book, sub, drop)
		if gSum > rSum {
			better++
		}
	}
	return 100 * float64(better) / float64(trials)
}

func bookPnL(book map[string]posRow, pkeys []string) float64 {
	s := 0.0
	for _, k := range pkeys {
		s += book[k].pnl
	}
	return s
}
func cvarOf(book map[string]posRow, pkeys []string) float64 {
	p := make([]float64, 0, len(pkeys))
	for _, k := range pkeys {
		p = append(p, book[k].pnl)
	}
	return cvar95(p)
}
func cvar95(p []float64) float64 {
	if len(p) == 0 {
		return 0
	}
	s := append([]float64(nil), p...)
	sort.Float64s(s)
	n := int(math.Ceil(0.05 * float64(len(s))))
	if n < 1 {
		n = 1
	}
	sum := 0.0
	for i := 0; i < n; i++ {
		sum += s[i]
	}
	return sum / float64(n)
}
