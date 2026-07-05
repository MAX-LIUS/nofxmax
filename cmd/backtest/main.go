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
	proxy := flag.Bool("proxy", false, "enable the non-price close proxy (time-stop/max-hold/AI) for higher fidelity")
	liveConfig := flag.Bool("liveconfig", false, "use the trader's LIVE strategy protection config (ATR/structural) as the replay baseline")
	variants := flag.Bool("variants", false, "compare pre-specified single-change optimization variants derived from the live baseline (keeps the structural stop type; faithful to live)")
	pertrade := flag.Bool("pertrade", false, "with -variants: decompose each variant vs baseline TRADE-BY-TRADE (winners cut early vs losers saved)")
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

	// 1) Baseline: either the hardcoded percent params, or the trader's LIVE
	//    strategy protection config (ATR/structural units) via -liveconfig. The
	//    live config is what makes trusted-subset fidelity meaningful.
	baseline := backtest.ClaudeBaselineParams()
	if *liveConfig {
		cfg, ctf, err := backtest.LoadTraderStrategyConfig(db, *traderLike)
		if err != nil {
			log.Fatalf("liveconfig: %v", err)
		}
		baseline = backtest.LiveConfigParams(cfg, backtest.TimeframeHours(ctf))
		fmt.Printf("(baseline from LIVE strategy config; primary_tf=%s, unit=%s, TP=%d BE=%d DD=%d SL_atr=%.1f)\n",
			ctf, baseline.Unit, len(baseline.TPLegs), len(baseline.BELegs), len(baseline.DDRules), baseline.StopLossATR)
	}
	if *proxy && !*liveConfig {
		baseline.CloseProxy = backtest.ClaudeCloseProxy(backtest.TimeframeHours(*tf))
	}
	report, relErr := backtest.FidelityReport(loaded, baseline)
	fmt.Println("==== FIDELITY (percent baseline vs Claude actual) ====")
	fmt.Printf("(close-proxy: %v)\n", *proxy)
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

	// 4) Faithful optimization variants: each is a single pre-specified change
	//    off the LIVE baseline, so it keeps the structural-stop reconstruction
	//    (same stop TYPE live runs). Deltas are attributable to the one change.
	//    Only meaningful with -liveconfig (needs the real structural/ATR spec).
	if *variants {
		if !*liveConfig {
			fmt.Println("\n(-variants requires -liveconfig to derive from the real structural spec; skipping)")
			return
		}
		fmt.Println("\n==== OPTIMIZATION VARIANTS (single change off live baseline; structural stop preserved) ====")
		fmt.Printf("%-24s %-10s %-7s %-6s %-10s %-8s\n", "variant", "TotalPnL", "Win%", "PF", "MaxDD", "dPnL")
		var basePnL float64
		for _, v := range backtest.LiveVariants(baseline) {
			r := backtest.RunParams(v.P, loaded)
			if v.Name == "live-baseline" {
				basePnL = r.TotalPnL
			}
			fmt.Printf("%-24s %-10.2f %-7.1f %-6.2f %-10.2f %-+8.2f\n",
				v.Name, r.TotalPnL, r.WinRatePct, r.ProfitFactor, r.MaxDrawdown, r.TotalPnL-basePnL)
		}
		fmt.Println("→ dPnL is vs the live-baseline row. These deltas are on the FULL loaded")
		fmt.Println("  sample (all close reasons) and use the same structural stop as live.")

		// Per-trade decomposition: does a variant cut winners early? Compare each
		// variant against the baseline trade-by-trade.
		if *pertrade {
			fmt.Println()
			vs := backtest.LiveVariants(baseline)
			for _, v := range vs {
				if v.Name == "live-baseline" {
					continue
				}
				fmt.Print(backtest.FormatPerTradeCompare(v.Name, baseline, v.P, loaded, 8))
				fmt.Println()
			}
		}
	}
}
