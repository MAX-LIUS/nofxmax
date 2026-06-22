package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"strings"

	_ "modernc.org/sqlite"

	"nofx/trader/backtest"
)

// Dual-sample candidate validation: evaluates the compromise ATR candidates on
// BOTH the real Claude entries and the long-period mechanical entries, printing
// each candidate's PnL/PF/MaxDD and its rank within the grid sweep of each
// sample. A robust candidate ranks well in BOTH (not just one).
func main() {
	dbPath := flag.String("db", "/tmp/bt.db", "SQLite DB (clean copy)")
	traderLike := flag.String("trader", "%claude_1779550392", "trader_id LIKE")
	symbolsCSV := flag.String("symbols", "BTCUSDT,ETHUSDT,SOLUSDT,BNBUSDT,XRPUSDT", "mech symbols")
	months := flag.Int("months", 6, "mech history months")
	flag.Parse()

	cands := backtest.DefaultCandidates()

	// ---- Sample A: real Claude entries ----
	fmt.Println("==== SAMPLE A: real Claude entries ====")
	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	entries, err := backtest.LoadClaudeEntries(db, *traderLike)
	db.Close()
	if err != nil {
		log.Fatalf("load entries: %v", err)
	}
	realLoaded, skipped := backtest.PrepareRealEntries(entries, "1h")
	fmt.Printf("real entries prepared=%d skipped=%d\n", len(realLoaded), skipped)
	realEvals := backtest.EvaluateCandidatesOnLoaded(cands, realLoaded)
	printEvals(realEvals)

	// ---- Sample B: long-period mechanical entries ----
	fmt.Println("==== SAMPLE B: mechanical entries (long period) ====")
	cfg := backtest.RobustConfig{
		Symbols: strings.Split(*symbolsCSV, ","),
		Months:  *months,
	}
	for i := range cfg.Symbols {
		cfg.Symbols[i] = strings.TrimSpace(cfg.Symbols[i])
	}
	mechEvals, err := backtest.EvaluateCandidatesRobust(cfg, cands)
	if err != nil {
		log.Fatalf("mech eval: %v", err)
	}
	printEvals(mechEvals)

	// ---- Cross-sample verdict ----
	fmt.Println("==== CROSS-SAMPLE (lower percentile = better in both) ====")
	fmt.Printf("%-20s %-18s %-18s\n", "candidate", "real top%", "mech top%")
	for i := range cands {
		fmt.Printf("%-20s %-18.1f %-18.1f\n",
			cands[i].Name, realEvals[i].PercentileTop, mechEvals[i].PercentileTop)
	}
}

func printEvals(evals []backtest.CandidateEval) {
	fmt.Printf("%-20s %-10s %-7s %-6s %-10s %-12s\n",
		"candidate", "PnL", "Win%", "PF", "MaxDD", "rank")
	for _, e := range evals {
		r := e.Result
		fmt.Printf("%-20s %-10.2f %-7.1f %-6.2f %-10.2f %d/%d (top %.1f%%)\n",
			e.Name, r.TotalPnL, r.WinRatePct, r.ProfitFactor, r.MaxDrawdown,
			e.RankByPnL, e.GridSize, e.PercentileTop)
	}
}
