package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"

	_ "modernc.org/sqlite"

	"nofx/trader/backtest"
)

// Standalone backtest/optimization tool. Reads Claude's CLOSED positions from
// the SQLite DB, fetches OKX 1h history per entry, validates the percent-mode
// engine against Claude's actual realized P&L, then sweeps ATR-multiple params
// to find the best protection configuration.
//
// Usage:
//   backtest -db /app/data/data.db -trader %claude_1779550392 -top 15
func main() {
	dbPath := flag.String("db", "/app/data/data.db", "path to SQLite DB")
	traderLike := flag.String("trader", "%claude_1779550392", "trader_id LIKE pattern")
	tf := flag.String("tf", "1h", "timeframe for replay")
	top := flag.Int("top", 15, "how many best sweep points to print")
	limit := flag.Int("limit", 0, "limit number of entries (0 = all)")
	recent := flag.Bool("recent", false, "when limiting, take the most RECENT entries (default takes earliest)")
	flag.Parse()

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	entries, err := backtest.LoadClaudeEntries(db, *traderLike)
	if err != nil {
		log.Fatalf("load entries: %v", err)
	}
	if *limit > 0 && *limit < len(entries) {
		if *recent {
			entries = entries[len(entries)-*limit:] // most recent N (entries are ascending)
		} else {
			entries = entries[:*limit]
		}
	}
	fmt.Printf("loaded %d closed entries for trader=%s\n", len(entries), *traderLike)
	if len(entries) == 0 {
		os.Exit(1)
	}

	fmt.Println("fetching OKX history per entry (network-bound, please wait)...")
	loaded, skipped := backtest.PrepareEntries(entries, *tf, backtest.OKXBars)
	fmt.Printf("prepared %d entries (%d skipped: no/short data)\n", len(loaded), skipped)
	if len(loaded) == 0 {
		os.Exit(1)
	}

	// 1) Fidelity: percent-mode baseline vs Claude's actual realized P&L.
	baseline := backtest.ClaudeBaselineParams()
	report, relErr := backtest.FidelityReport(loaded, baseline)
	fmt.Println("==== FIDELITY (percent baseline vs Claude actual) ====")
	fmt.Println(report)
	if relErr > 50 || relErr < -50 {
		fmt.Printf("⚠️ fidelity rel_err=%.1f%% is large — engine semantics may diverge; treat sweep as directional only\n", relErr)
	}

	// 1b) Per-mechanism fidelity breakdown: shows which live close mechanisms the
	//     replay can/can't reproduce, and how much PnL error each contributes.
	fmt.Println("==== FIDELITY BY CLOSE MECHANISM ====")
	fmt.Print(backtest.FormatMechanismFidelity(loaded, baseline))

	// 2) ATR-multiple parameter sweep.
	fmt.Println("==== ATR-MULTIPLE SWEEP (best by total PnL) ====")
	points := backtest.Sweep(backtest.DefaultATRGrid(), loaded)
	n := *top
	if n > len(points) {
		n = len(points)
	}
	fmt.Printf("%-4s %-6s %-6s %-6s %-6s %-6s | %-10s %-7s %-6s %-10s\n",
		"#", "SL", "TP1", "TP2", "BE1", "BE2", "TotalPnL", "Win%", "PF", "MaxDD")
	for i := 0; i < n; i++ {
		p := points[i]
		r := p.Result
		fmt.Printf("%-4d %-6.1f %-6.1f %-6.1f %-6.1f %-6.1f | %-10.2f %-7.1f %-6.2f %-10.2f\n",
			i+1, p.SLATR, p.TP1ATR, p.TP2ATR, p.BE1ATR, p.BE2ATR,
			r.TotalPnL, r.WinRatePct, r.ProfitFactor, r.MaxDrawdown)
	}

	// 3) Baseline portfolio for reference.
	base := backtest.RunParams(baseline, loaded)
	fmt.Println("==== BASELINE (Claude percent params, replayed) ====")
	fmt.Printf("TotalPnL=%.2f Win%%=%.1f PF=%.2f MaxDD=%.2f Trades=%d\n",
		base.TotalPnL, base.WinRatePct, base.ProfitFactor, base.MaxDrawdown, base.Trades)
}
