// Command vtbench — Virtual Trader Bench (CLI).
//
// Simulates "N traders, each with a different entry-gate rulebook, trading the
// SAME account" over the real executed book. Ground truth = every CLOSED
// position (real realized PnL under the real protection/exit system). Each
// virtual trader FILTERS that book: it takes every real trade except the ones
// its gate would have blocked, then runs the survivors chronologically on a
// shared account, measured in realized R. Output = per-trader scorecard
// (return, maxDD, CVaR95, Sortino, winRate, expectancy in R) plus random-control
// / H1-H2 / bootstrap CI significance, so we can tell a real edge from luck.
//
// Per-trade unit is REALIZED R = realized_pnl / initial_risk_usd, where
// initial_risk_usd = |entry_price - stop_loss| * entry_quantity and stop_loss is
// recovered from the opening decision (by AI cycle, or by time for synced
// positions whose cycle=0). R normalizes out size noise so we measure ENTRY-gate
// quality, not sizing.
//
// This is a THIN CLI over the shadoweval package — the exact same Load+Run the
// live API monitor serves — so CLI numbers and the web bench are always identical.
//
// LIMITATIONS (honest, by construction):
//   - Virtual traders can only be MORE restrictive than reality (a subset of
//     executed trades). A gate that would OPEN trades the live system rejected
//     has no ground truth. This is correct for an entry gate: it only removes.
//   - Exits are held at the real protection outcome, so we isolate entry quality.
//
// Usage: vtbench -db /opt/webstack/nofx/data/data.db [-cap 3] [-trials 5000]
package main

import (
	"database/sql"
	"flag"
	"fmt"

	"nofx/shadoweval"

	_ "modernc.org/sqlite"
)

func main() {
	dbPath := flag.String("db", "/opt/webstack/nofx/data/data.db", "sqlite db path")
	_ = flag.Float64("risk", 0.01, "deprecated; R is size-normalized (kept for compat)")
	trials := flag.Int("trials", 5000, "random-control trials")
	seed := flag.Int64("seed", 42, "rng seed")
	cap := flag.Float64("cap", 0, "winsorize realized R to +/-cap (0=off) — robustness vs fat-tail outliers")
	flag.Parse()

	db, err := sql.Open("sqlite", "file:"+*dbPath+"?mode=ro&immutable=1")
	if err != nil {
		panic(err)
	}
	defer db.Close()

	trades, err := shadoweval.Load(db)
	if err != nil {
		panic(err)
	}
	res := shadoweval.Run(trades, shadoweval.RuleNames(trades),
		shadoweval.Options{Trials: *trials, Cap: *cap, Seed: *seed})

	if *cap > 0 {
		fmt.Printf("[robustness] realized R winsorized to +/-%.1f\n", *cap)
	}
	fmt.Printf("book: %d closed trades | R-eligible (stop recovered): %d | forward (OOS): %d\n",
		res.Book, res.REligible, res.Forward)
	fmt.Printf("realized R dist: p5 %.2f | p50 %.2f | p95 %.2f (fat tails widen CI)\n",
		res.RP5, res.RP50, res.RP95)
	fmt.Println("unit = realized R (pnl / initial risk). fixed 1R per trade => equity is cumulative R.")
	fmt.Println("each row = a virtual trader running the SAME book minus the trades its gate blocks.")
	fmt.Println()

	printHeader()
	printRow(res.Baseline)
	for _, sc := range res.Traders {
		printRow(sc)
	}
	fmt.Printf("\nbaseline expectancy = %+.3fR over %d trades.\n", res.Baseline.ExpR, res.Baseline.NTrades)
	fmt.Println("PASS (✓) = final R > baseline AND vs-random(expR) full-sample & H1 & H2 all >95%")
	fmt.Println("          AND bootstrap 5% expR > 0. Full-sample-only winners that fail a half are OVERFIT.")
	fmt.Println("NOTE: virtual traders only REMOVE trades (subset of executed book); gates that would")
	fmt.Println("OPEN untraded ideas cannot be scored — no ground truth. Backfill chop rules see conf=0.")
}

func printHeader() {
	fmt.Printf("%-22s %6s %5s %8s %7s %7s %7s %8s %7s %7s %6s %s\n",
		"virtual_trader", "trades", "blkd", "finalR", "expR", "maxDD", "CVaR95", "vsRand", "H1", "H2", "ci5%", "")
}

func printRow(s shadoweval.Scorecard) {
	col := func(v float64, on bool) string {
		if !on {
			return "  -  "
		}
		return fmt.Sprintf("%.0f", v)
	}
	tag := ""
	if s.Pass {
		tag = "✓ PASS"
	} else if s.NBlocked > 0 {
		tag = "overfit/weak"
	}
	on := s.NBlocked > 0
	fmt.Printf("%-22s %6d %5d %+8.1f %+7.3f %7.1f %7.3f %8s %7s %7s %+6.2f %s\n",
		trunc(s.Name, 22), s.NTrades, s.NBlocked, s.RetR, s.ExpR, s.MaxDD, s.CVaR95,
		col(s.VsRand, on), col(s.H1, on), col(s.H2, on), s.CILo, tag)
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
