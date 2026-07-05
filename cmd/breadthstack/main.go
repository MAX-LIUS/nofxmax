// breadthstack: evaluate the DEPLOYED ATR protection stack (TP/BE/DD/SL) through
// the time-synchronized PORTFOLIO simulator, with the live breadth circuit breaker
// OFF vs ON. This answers the question the per-position protectsim could not: how
// much of the reversal tail (correlated multi-position drawdowns ending in SL) the
// portfolio-level breadth breaker actually intercepts.
//
// Baseline = breaker OFF (each position runs its own TP/BE/DD/SL to exit/mark).
// Live     = breaker ON with the deployed config (min4 / frac0.7 / ATR2.0 / cut100
//            / vel0.25 / win6), cutting losing+retracing positions when a majority
//            of the open book reverses together.
//
// DD variants compared (TP/BE/SL identical to deployed):
//   T1     = deployed single-tier DD (3.0 ATR / 40% giveback / close 100%)
//   MULTI  = the 4-tier insurance design (3.0/4.5/6.5/9.0 ATR) for contrast
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"

	_ "modernc.org/sqlite"

	"nofx/trader/backtest"
)

func deployedATR(ddRules []backtest.DDRule) backtest.ProtectionParams {
	return backtest.ProtectionParams{
		Unit:        backtest.UnitATRMult,
		StopLossATR: 4.5, // deployed: partial 4.0/40% then full 4.5/60%; model has single SL => 4.5 (slightly pessimistic, no early 4.0 trim)
		TPLegs: []backtest.LadderLeg{
			{ATRMult: 1.1, CloseRatioPct: 20},
			{ATRMult: 1.7, CloseRatioPct: 18},
			{ATRMult: 2.5, CloseRatioPct: 15},
			{ATRMult: 3.6, CloseRatioPct: 12},
		},
		BELegs: []backtest.BELeg{
			{TriggerATR: 1.0, OffsetATR: 0.1, CloseRatioPct: 100},
			{TriggerATR: 2.0, OffsetATR: 1.0, CloseRatioPct: 100},
		},
		DDRules: ddRules,
	}
}

func liveBreadth(capital float64) backtest.GuardParams {
	// Mirrors deployed giveback_guard: breadth_min_pos=4, frac=0.7, loser_cut=100,
	// use_atr=true, atr_mult=2.0, vel_eps=0.25, vel_window=6.
	return backtest.GuardParams{
		Enabled: true, BreadthEnabled: true, StartCapital: capital,
		BreadthMinPos: 4, BreadthFrac: 0.7, BreadthLoserCutPct: 100,
		BreadthUseATR: true, BreadthATRMult: 2.0,
		BreadthVelEps: 0.25, L3VelWindow: 6,
	}
}

func row(label string, r backtest.SimResult) {
	fmt.Printf("%-28s | PnL=%9.2f  Win%%=%5.1f  MaxDD=%9.2f  Giveback=%9.2f  trades=%4d  breakerFires=%3d\n",
		label, r.TotalPnL, r.WinRatePct, r.MaxPortfolioDD, r.MaxGiveback, r.Trades, r.L3Fires)
}

func main() {
	dbPath := flag.String("db", "/opt/webstack/nofx/data/data.db", "SQLite DB path")
	traderLike := flag.String("trader", "%claude_1779550392", "trader_id LIKE pattern")
	tf := flag.String("tf", "1h", "timeframe")
	capital := flag.Float64("capital", 250, "account capital for breaker")
	limit := flag.Int("limit", 0, "limit entries (0=all)")
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
		entries = entries[len(entries)-*limit:]
	}
	fmt.Printf("trader=%s  loaded %d closed entries\n", *traderLike, len(entries))
	if len(entries) == 0 {
		log.Fatal("no entries")
	}
	fmt.Println("fetching OKX history per entry (network-bound)...")
	loaded, skipped := backtest.PrepareEntries(entries, *tf, backtest.OKXBars)
	fmt.Printf("prepared %d entries (%d skipped)\n\n", len(loaded), skipped)
	if len(loaded) == 0 {
		log.Fatal("no prepared entries")
	}

	t1 := []backtest.DDRule{{MinProfitATR: 3.0, MaxDrawdownPct: 40, CloseRatioPct: 100}}
	multi := []backtest.DDRule{
		{MinProfitATR: 3.0, MaxDrawdownPct: 40, CloseRatioPct: 40},
		{MinProfitATR: 4.5, MaxDrawdownPct: 28, CloseRatioPct: 15},
		{MinProfitATR: 6.5, MaxDrawdownPct: 20, CloseRatioPct: 15},
		{MinProfitATR: 9.0, MaxDrawdownPct: 13, CloseRatioPct: 30},
	}

	off := backtest.GuardParams{}
	br := liveBreadth(*capital)

	fmt.Println("==== DEPLOYED ATR STACK through PORTFOLIO sim (breaker OFF vs LIVE breadth) ====")
	fmt.Println("    TP 1.1/1.7/2.5/3.6 ATR (20/18/15/12) | BE 1/2 ATR | SL 4.5 ATR")
	fmt.Println()
	row("T1 single  breaker OFF", backtest.RunPortfolioSim(deployedATR(t1), off, loaded))
	row("T1 single  breaker LIVE", backtest.RunPortfolioSim(deployedATR(t1), br, loaded))
	fmt.Println()
	row("MULTI 4tier breaker OFF", backtest.RunPortfolioSim(deployedATR(multi), off, loaded))
	row("MULTI 4tier breaker LIVE", backtest.RunPortfolioSim(deployedATR(multi), br, loaded))
}
