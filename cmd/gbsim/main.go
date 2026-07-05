package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"nofx/trader/backtest"
)

// gbsim = giveback-guard simulator. Two modes:
//
//	DB mode (default): loads a trader's CLOSED positions, fetches OKX 1h history
//	  per entry, runs the time-synchronized PORTFOLIO simulation under Claude
//	  baseline protection, then sweeps the giveback-guard overlay.
//
//	Robust mode (-robust): generates EMA-cross mechanical entries over N months
//	  of OKX history across many symbols (no DB), then runs the same portfolio
//	  sweep. This is the LONG-PERIOD out-of-sample validation (6-12 months).
//
// Usage:
//
//	gbsim -db /app/data/data.db -trader %claude_1779550392 -top 20
//	gbsim -robust -months 12 -symbols BTCUSDT,ETHUSDT,SOLUSDT -top 20
func main() {
	dbPath := flag.String("db", "/app/data/data.db", "path to SQLite DB")
	traderLike := flag.String("trader", "%claude_1779550392", "trader_id LIKE pattern")
	tf := flag.String("tf", "1h", "timeframe for replay")
	top := flag.Int("top", 20, "how many best sweep points to print")
	limit := flag.Int("limit", 0, "limit number of entries (0 = all)")
	days := flag.Int("days", 0, "only entries within last N days (0 = all)")
	robust := flag.Bool("robust", false, "long-period mode: EMA-cross entries over OKX history (no DB)")
	months := flag.Int("months", 12, "robust mode: months of OKX history")
	symbolsCSV := flag.String("symbols", "BTCUSDT,ETHUSDT,SOLUSDT,BNBUSDT,XRPUSDT,DOGEUSDT,AVAXUSDT,LINKUSDT", "robust mode: symbols")
	walkfwd := flag.Bool("walkforward", false, "walk-forward: pick params in-sample (first -ismonths), validate out-of-sample")
	isMonths := flag.Int("ismonths", 6, "walk-forward: in-sample months (rest = OOS)")
	signal := flag.String("signal", "ema", "robust/walkforward entry signal: ema | breakout")
	l1study := flag.Bool("l1study", false, "use the L1 min-peak threshold study grid (sub-3% giveback control study)")
	liveconfig := flag.Bool("liveconfig", false, "evaluate ONLY the deployed guard config (baseline-vs-live PnL-cost decomposition)")
	adaptive := flag.Bool("adaptive", false, "sweep trend-adaptive L2 close ratios (ADX-gated) to recover trend-regime PnL")
	ablation := flag.Bool("ablation", false, "ablation: measure each protection layer's (DD/BE/TP/SL) marginal PnL contribution (guard off)")
	unitcompare := flag.Bool("unitcompare", false, "compare percent-mode vs ATR-mode protection PnL on identical entries (guard off)")
	optimize := flag.Bool("optimize", false, "staged ATR protection optimiser: sweep SL/TP/BE/DD distances, ratios, tiers, disable unfavourable layers")
	holdout := flag.Bool("holdout", false, "out-of-sample validation: optimise on train split, score on untouched test split")
	rank := flag.Bool("rank", false, "rank curated regime-robust candidate configs (no fitting -> generalizes); use -trainfrac<1 for OOS-tail scoring")
	structural := flag.Bool("structural", false, "structural protection study: compare percent/ATR vs AI structural levels (Variant A) + config-buffer sweep (Variant B), on structurally-matched real entries")
	matchwin := flag.Int("matchwin", 120, "structural: max minutes between an open decision and the entry to pair its structural protection_plan")
	bufsweep := flag.String("bufsweep", "0.2,0.3,0.5,0.8,1.0", "structural Variant B: comma-separated ATR-buffer multiples k (SL = anchor ± k*ATR)")
	testtail := flag.Float64("testtail", 0, "structural: score only the most-recent fraction of entries (out-of-sample tail); 0 = use all")
	trainfrac := flag.Float64("trainfrac", 0.7, "holdout: fraction of (time-ordered) entries used for training")
	lambda := flag.Float64("lambda", 0.15, "optimiser drawdown penalty: score = PnL - lambda*MaxDD")
	l3 := flag.Bool("l3", false, "account-level EQUITY drawdown circuit breaker study: sweep staged-cut tier ladders on real entries")
	capital := flag.Float64("capital", 250, "l3: account start capital (USDT) anchoring the equity-drawdown %")
	l3trace := flag.Bool("l3trace", false, "l3: run the production preset with a per-firing trace log (position-level decisions)")
	breadth := flag.Bool("breadth", false, "breadth breaker study: per-symbol majority-retrace gate that cuts losers only (winners ride BE). Sweep quorum/frac/retrace defs; works with -robust or DB entries")
	whipsaw := flag.Bool("whipsaw", false, "breadth whipsaw-lever study: hold the live breadth gate fixed and sweep ONLY vel_eps + cooldown (the V-bottom false-positive controls). Row 1 = live config")
	strictgate := flag.Bool("strictgate", false, "breadth strict-gate study: hold cd6 fixed and tighten quorum/frac/ATR-mult to test whether the breaker can fire LESS often (rare true breaker) without losing tail protection. Row 1 = live gate + cd6")
	atrsweep := flag.Bool("atrsweep", false, "breadth atr_mult-only study: hold each live anchor fixed (vel0.25/cd0) and sweep ONLY the ATR-from-peak retrace depth around the live value. Isolates whether atr_mult should change")
	reversal := flag.Bool("reversal", false, "trend-reversal flip study: at each bar evaluate the 4-rule flip methodology (opposite EMA-cross + breadth + age + exhaustion); close original early and open reverse. Compares vs no-flip baseline held to exit")
	revmode := flag.String("revmode", "rules", "reversal study grid: rules (rule attribution) | age (MinAgeBars sweep) | safety (whipsaw safeguards)")
	fine := flag.Bool("fine", false, "breadth: use the narrow refinement grid around min4/f60%/ATR1.0 (frac step 0.05, ATR step 0.1)")
	flag.Parse()

	// Select the parameter grid to sweep.
	grid := guardGrid
	if *l1study {
		grid = l1StudyGrid
	}
	if *liveconfig {
		grid = liveConfigGrid
	}
	if *adaptive {
		grid = adaptiveCloseGrid
	}

	// Protection-layer analysis modes (guard-independent). These short-circuit
	// the guard sweep: they replay baseline protection variants on the same
	// entries (robust mechanical or real DB) and print attribution tables.
	if *ablation || *unitcompare {
		runProtectionAnalysis(*ablation, *unitcompare, analysisInputs{
			robust: *robust, symbolsCSV: *symbolsCSV, tf: *tf, months: *months,
			signal: *signal, dbPath: *dbPath, traderLike: *traderLike,
			days: *days, limit: *limit,
		})
		return
	}

	if *optimize {
		runOptimize(*lambda, analysisInputs{
			robust: *robust, symbolsCSV: *symbolsCSV, tf: *tf, months: *months,
			signal: *signal, dbPath: *dbPath, traderLike: *traderLike,
			days: *days, limit: *limit,
		})
		return
	}

	if *holdout {
		if *robust {
			syms := strings.Split(*symbolsCSV, ",")
			for i := range syms {
				syms[i] = strings.TrimSpace(syms[i])
			}
			fmt.Printf("HOLDOUT (robust): %d symbols, %d months, signal=%s, trainfrac=%.2f\n",
				len(syms), *months, *signal, *trainfrac)
			h, per, err := backtest.OptimizeHoldoutRobust(backtest.RobustConfig{
				Symbols: syms, Timeframe: *tf, Months: *months, Signal: *signal,
			}, *lambda, *trainfrac)
			if err != nil {
				log.Fatalf("holdout robust: %v", err)
			}
			fmt.Printf("per-symbol entries: %v\n", per)
			fmt.Println(backtest.FormatHoldout(h))
			return
		}
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
		fmt.Printf("loaded %d closed entries for trader=%s\n", len(entries), *traderLike)
		if len(entries) == 0 {
			os.Exit(1)
		}
		fmt.Println("fetching OKX history per entry (network-bound)...")
		h, prepared, skipped := backtest.OptimizeHoldoutFromEntries(entries, *tf, *lambda, *trainfrac)
		fmt.Printf("prepared %d entries (%d skipped)\n", prepared, skipped)
		fmt.Println(backtest.FormatHoldout(h))
		return
	}

	if *rank {
		if *robust {
			syms := strings.Split(*symbolsCSV, ",")
			for i := range syms {
				syms[i] = strings.TrimSpace(syms[i])
			}
			fmt.Printf("CANDIDATE RANK (robust): %d symbols, %d months, signal=%s, trainfrac=%.2f\n",
				len(syms), *months, *signal, *trainfrac)
			rows, per, err := backtest.RankCandidatesRobust(backtest.RobustConfig{
				Symbols: syms, Timeframe: *tf, Months: *months, Signal: *signal,
			}, *lambda, *trainfrac)
			if err != nil {
				log.Fatalf("rank robust: %v", err)
			}
			fmt.Printf("per-symbol entries: %v\n", per)
			fmt.Println(backtest.FormatCandRank(rows))
			return
		}
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
		fmt.Printf("loaded %d closed entries for trader=%s\n", len(entries), *traderLike)
		if len(entries) == 0 {
			os.Exit(1)
		}
		fmt.Println("fetching OKX history per entry (network-bound)...")
		rows, prepared, skipped := backtest.RankCandidatesFromEntries(entries, *tf, *lambda, *trainfrac)
		fmt.Printf("prepared %d entries (%d skipped)\n", prepared, skipped)
		fmt.Println(backtest.FormatCandRank(rows))
		return
	}

	if *structural {
		db, err := sql.Open("sqlite", *dbPath)
		if err != nil {
			log.Fatalf("open db: %v", err)
		}
		defer db.Close()
		matchWindowMs := int64(*matchwin) * 60 * 1000
		entries, matched, err := backtest.LoadStructuralEntries(db, *traderLike, matchWindowMs)
		if err != nil {
			log.Fatalf("load structural entries: %v", err)
		}
		if *days > 0 {
			cutoff := time.Now().AddDate(0, 0, -*days).UnixMilli()
			var f []backtest.Entry
			for _, e := range entries {
				if e.EntryTime >= cutoff {
					f = append(f, e)
				}
			}
			entries = f
		}
		if *limit > 0 && *limit < len(entries) {
			entries = entries[len(entries)-*limit:]
		}
		fmt.Printf("loaded %d closed entries for trader=%s (%d matched a structural protection_plan within %dmin)\n",
			len(entries), *traderLike, matched, *matchwin)
		if matched == 0 {
			log.Fatalf("no entries matched a structural protection_plan; widen -matchwin or check trader")
		}
		fmt.Println("fetching OKX history per entry (network-bound)...")
		rows, used, skipped := backtest.CompareStructuralFromEntries(entries, *tf, parseFloatsCSV(*bufsweep), *testtail)
		fmt.Printf("prepared entries (%d skipped); structurally-matched used in comparison=%d\n", skipped, used)
		fmt.Println(backtest.FormatStructCompare(rows, used))
		return
	}

	if *walkfwd {
		syms := strings.Split(*symbolsCSV, ",")
		for i := range syms {
			syms[i] = strings.TrimSpace(syms[i])
		}
		fmt.Printf("WALK-FORWARD: %d symbols, %d months total, IS=first %dmo, OOS=last %dmo\n",
			len(syms), *months, *isMonths, *months-*isMonths)
		wf, err := backtest.WalkForward(backtest.RobustConfig{
			Symbols: syms, Timeframe: *tf, Months: *months, Signal: *signal,
		}, grid(), *isMonths, *top)
		if err != nil {
			log.Fatalf("walk-forward: %v", err)
		}
		printWalkForward(wf)
		return
	}

	if *robust {
		syms := strings.Split(*symbolsCSV, ",")
		for i := range syms {
			syms[i] = strings.TrimSpace(syms[i])
		}
		// L3 rolling sweep on long-period robust data (no DB).
		if *l3 {
			fmt.Printf("L3 ROLLING SWEEP (robust): %d symbols, %d months, signal=%s, capital=%.0f\n",
				len(syms), *months, *signal, *capital)
			base, rows, per, err := backtest.SweepEquityBreakerRobust(backtest.RobustConfig{
				Symbols: syms, Timeframe: *tf, Months: *months, Signal: *signal,
			}, *capital)
			if err != nil {
				log.Fatalf("l3 robust: %v", err)
			}
			fmt.Printf("per-symbol entries: %v\n", per)
			printL3Sweep(base, rows, *capital, *top)
			return
		}
		if *breadth {
			fmt.Printf("BREADTH BREAKER SWEEP (robust): %d symbols, %d months, signal=%s, capital=%.0f\n",
				len(syms), *months, *signal, *capital)
			base, rows, per, err := backtest.SweepBreadthRobust(backtest.RobustConfig{
				Symbols: syms, Timeframe: *tf, Months: *months, Signal: *signal,
			}, *capital, *fine)
			if err != nil {
				log.Fatalf("breadth robust: %v", err)
			}
			fmt.Printf("per-symbol entries: %v\n", per)
			printL3Sweep(base, rows, *capital, *top)
			return
		}
		if *whipsaw {
			fmt.Printf("BREADTH WHIPSAW-LEVER SWEEP (robust): %d symbols, %d months, signal=%s, capital=%.0f\n",
				len(syms), *months, *signal, *capital)
			base, rows, per, err := backtest.SweepBreadthWhipsawRobust(backtest.RobustConfig{
				Symbols: syms, Timeframe: *tf, Months: *months, Signal: *signal,
			}, *capital)
			if err != nil {
				log.Fatalf("whipsaw robust: %v", err)
			}
			fmt.Printf("per-symbol entries: %v\n", per)
			printL3Sweep(base, rows, *capital, *top)
			return
		}
		if *strictgate {
			fmt.Printf("BREADTH STRICT-GATE SWEEP (robust): %d symbols, %d months, signal=%s, capital=%.0f\n",
				len(syms), *months, *signal, *capital)
			base, rows, per, err := backtest.SweepBreadthStrictGateRobust(backtest.RobustConfig{
				Symbols: syms, Timeframe: *tf, Months: *months, Signal: *signal,
			}, *capital)
			if err != nil {
				log.Fatalf("strictgate robust: %v", err)
			}
			fmt.Printf("per-symbol entries: %v\n", per)
			printL3Sweep(base, rows, *capital, *top)
			return
		}
		if *atrsweep {
			fmt.Printf("BREADTH ATR-MULT SWEEP (robust): %d symbols, %d months, signal=%s, capital=%.0f\n",
				len(syms), *months, *signal, *capital)
			base, rows, per, err := backtest.SweepBreadthAtrRobust(backtest.RobustConfig{
				Symbols: syms, Timeframe: *tf, Months: *months, Signal: *signal,
			}, *capital)
			if err != nil {
				log.Fatalf("atrsweep robust: %v", err)
			}
			fmt.Printf("per-symbol entries: %v\n", per)
			printL3Sweep(base, rows, *capital, *top)
			return
		}
		if *reversal {
			fmt.Printf("TREND-REVERSAL FLIP SWEEP (robust): %d symbols, %d months, signal=%s\n",
				len(syms), *months, *signal)
			base, rows, per, err := backtest.SweepReversalRobust(backtest.RobustConfig{
				Symbols: syms, Timeframe: *tf, Months: *months, Signal: *signal,
			})
			if err != nil {
				log.Fatalf("reversal robust: %v", err)
			}
			fmt.Printf("per-symbol entries: %v\n", per)
			printReversalSweep(base, rows)
			return
		}
		fmt.Printf("ROBUST mode: %d symbols, %d months, signal=%s\n", len(syms), *months, *signal)
		base, rows, per, err := backtest.SweepGuardsRobust(backtest.RobustConfig{
			Symbols: syms, Timeframe: *tf, Months: *months, Signal: *signal,
		}, grid())
		if err != nil {
			log.Fatalf("robust sweep: %v", err)
		}
		fmt.Printf("per-symbol entries: %v\n", per)
		printSweep(base, rows, *top)
		return
	}

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	entries, err := backtest.LoadClaudeEntries(db, *traderLike)
	if err != nil {
		log.Fatalf("load entries: %v", err)
	}
	if *days > 0 {
		cutoff := msNow() - int64(*days)*86400_000
		f := entries[:0]
		for _, e := range entries {
			if e.EntryTime >= cutoff {
				f = append(f, e)
			}
		}
		entries = f
	}
	if *limit > 0 && *limit < len(entries) {
		entries = entries[len(entries)-*limit:]
	}
	fmt.Printf("loaded %d closed entries for trader=%s\n", len(entries), *traderLike)
	if len(entries) == 0 {
		os.Exit(1)
	}
	fmt.Println("fetching OKX history per entry (network-bound)...")
	if *l3trace {
		base, l3res, trace, prepared, skipped := backtest.TraceEquityBreakerFromEntries(entries, *tf, *capital)
		fmt.Printf("prepared %d entries (%d skipped)\n", prepared, skipped)
		if prepared == 0 {
			os.Exit(1)
		}
		printL3Trace(base, l3res, trace, *capital)
		return
	}
	if *l3 {
		base, rows, prepared, skipped := backtest.SweepEquityBreakerFromEntries(entries, *tf, *capital)
		fmt.Printf("prepared %d entries (%d skipped)\n", prepared, skipped)
		if prepared == 0 {
			os.Exit(1)
		}
		printL3Sweep(base, rows, *capital, *top)
		return
	}
	if *breadth {
		base, rows, prepared, skipped := backtest.SweepBreadthFromEntries(entries, *tf, *capital, *fine)
		fmt.Printf("prepared %d entries (%d skipped)\n", prepared, skipped)
		if prepared == 0 {
			os.Exit(1)
		}
		printL3Sweep(base, rows, *capital, *top)
		return
	}
	if *whipsaw {
		base, rows, prepared, skipped := backtest.SweepBreadthWhipsawFromEntries(entries, *tf, *capital)
		fmt.Printf("prepared %d entries (%d skipped)\n", prepared, skipped)
		if prepared == 0 {
			os.Exit(1)
		}
		printL3Sweep(base, rows, *capital, *top)
		return
	}
	if *strictgate {
		base, rows, prepared, skipped := backtest.SweepBreadthStrictGateFromEntries(entries, *tf, *capital)
		fmt.Printf("prepared %d entries (%d skipped)\n", prepared, skipped)
		if prepared == 0 {
			os.Exit(1)
		}
		printL3Sweep(base, rows, *capital, *top)
		return
	}
	if *atrsweep {
		base, rows, prepared, skipped := backtest.SweepBreadthAtrFromEntries(entries, *tf, *capital)
		fmt.Printf("prepared %d entries (%d skipped)\n", prepared, skipped)
		if prepared == 0 {
			os.Exit(1)
		}
		printL3Sweep(base, rows, *capital, *top)
		return
	}
	if *reversal {
		base, rows, prepared, skipped := backtest.SweepReversalFromEntries(entries, *tf, *revmode, 9, 21)
		fmt.Printf("prepared %d entries (%d skipped) [revmode=%s]\n", prepared, skipped, *revmode)
		if prepared == 0 {
			os.Exit(1)
		}
		printReversalSweep(base, rows)
		return
	}
	base, rows, prepared, skipped := backtest.SweepGuardsFromEntries(entries, *tf, grid())
	fmt.Printf("prepared %d entries (%d skipped)\n", prepared, skipped)
	if prepared == 0 {
		os.Exit(1)
	}
	printSweep(base, rows, *top)
}

func printSweep(base backtest.SimResult, rows []backtest.GuardSweepRow, top int) {
	fmt.Println("==== BASELINE (guard OFF, Claude % protection) ====")
	fmt.Printf("PnL=%.2f Win%%=%.1f MaxPortfolioDD=%.2f MaxGiveback=%.2f trades=%d\n",
		base.TotalPnL, base.WinRatePct, base.MaxPortfolioDD, base.MaxGiveback, base.Trades)
	fmt.Println("==== GUARD SWEEP (ranked: DD cut minus PnL cost) ====")
	n := top
	if n > len(rows) {
		n = len(rows)
	}
	fmt.Printf("%-3s %-46s | %-8s %-7s %-9s %-9s %-7s %-6s\n",
		"#", "guard", "PnL", "Win%", "MaxDD", "Giveback", "trims", "score")
	for i := 0; i < n; i++ {
		r := rows[i]
		fmt.Printf("%-3d %-46s | %-8.2f %-7.1f %-9.2f %-9.2f %-7d %-6.2f\n",
			i+1, describe(r.Guard), r.Result.TotalPnL, r.Result.WinRatePct, r.Result.MaxPortfolioDD,
			r.Result.MaxGiveback, r.Result.GuardTrims, r.Score)
	}
}

// printReversalSweep prints the trend-reversal flip study results: each config's
// total PnL vs the no-flip baseline, flip count/win-rate, and the net edge.
func printReversalSweep(base backtest.ReversalResult, rows []backtest.ReversalSweepRow) {
	fmt.Printf("==== BASELINE (no flip — entries held to exit) ====\n")
	fmt.Printf("PnL=%.2f Win%%=%.1f trades=%d  (held-to-exit counterfactual PnL=%.2f)\n",
		base.TotalPnL, base.WinRatePct, base.Trades, base.BaselineHeldPnL)
	fmt.Println("==== TREND-REVERSAL FLIP SWEEP (row 0 = baseline; edge = PnL - baseline) ====")
	fmt.Printf("%-3s %-46s | %-9s %-7s %-6s %-7s %-9s %-9s %-9s\n",
		"#", "config", "PnL", "Win%", "flips", "flipW%", "flipPnL", "origCut", "edge")
	for i, r := range rows {
		flipWinPct := 0.0
		if r.Flips > 0 {
			flipWinPct = float64(r.FlipWins) / float64(r.Flips) * 100
		}
		exhTag := ""
		if r.ExhaustionCloses > 0 {
			exhTag = fmt.Sprintf(" exh=%d", r.ExhaustionCloses)
		}
		fmt.Printf("%-3d %-46s | %-9.2f %-7.1f %-6d %-7.1f %-9.2f %-9.2f %+-9.2f%s\n",
			i, r.Label, r.TotalPnL, r.WinRatePct, r.Flips, flipWinPct,
			r.FlipPnL, r.OriginalCutPnL, r.Edge, exhTag)
	}
}

// printL3Sweep prints the equity-circuit-breaker baseline then each tier ladder
// ranked by net benefit (DD cut minus PnL cost), with how many times L3 fired.
func printL3Sweep(base backtest.SimResult, rows []backtest.EquityBreakerRow, capital float64, top int) {
	fmt.Printf("==== BASELINE (L3 OFF, capital=%.0f USDT) ====\n", capital)
	fmt.Printf("PnL=%.2f Win%%=%.1f MaxPortfolioDD=%.2f MaxGiveback=%.2f trades=%d\n",
		base.TotalPnL, base.WinRatePct, base.MaxPortfolioDD, base.MaxGiveback, base.Trades)
	fmt.Println("==== L3 EQUITY-BREAKER SWEEP (ranked: DD cut minus PnL cost) ====")
	fmt.Printf("%-3s %-34s | %-8s %-7s %-9s %-8s %-8s %-7s\n",
		"#", "tiers + variant", "PnL", "Win%", "MaxDD", "DDcut", "PnLcost", "L3fires")
	n := top
	if n > len(rows) {
		n = len(rows)
	}
	for i := 0; i < n; i++ {
		r := rows[i]
		fmt.Printf("%-3d %-34s | %-8.2f %-7.1f %-9.2f %-8.2f %-8.2f %-7d\n",
			i+1, l3Describe(r.Guard), r.Result.TotalPnL, r.Result.WinRatePct,
			r.Result.MaxPortfolioDD, r.DDCut, r.PnLCost, r.Result.L3Fires)
	}
}

// printL3Trace prints a position-level trace of every L3 firing: the equity
// snapshot, each open position's PnL%/velocity/trend classification, what was
// cut (counter-trend full vs trend-aligned light), and the resulting summary.
func printL3Trace(base, l3 backtest.SimResult, trace []backtest.L3FireEvent, capital float64) {
	fmt.Printf("==== L3 PRODUCTION PRESET TRACE (k30 v3/f2 w6, capital=%.0f USDT) ====\n", capital)
	fmt.Printf("BASELINE (L3 off): PnL=%.2f Win%%=%.1f MaxPortfolioDD=%.2f\n",
		base.TotalPnL, base.WinRatePct, base.MaxPortfolioDD)
	fmt.Printf("WITH L3        : PnL=%.2f Win%%=%.1f MaxPortfolioDD=%.2f  (DDcut=%.2f PnLcost=%.2f)\n",
		l3.TotalPnL, l3.WinRatePct, l3.MaxPortfolioDD,
		base.MaxPortfolioDD-l3.MaxPortfolioDD, base.TotalPnL-l3.TotalPnL)
	fmt.Printf("L3 fired %d time(s).\n\n", len(trace))

	for i, ev := range trace {
		kind := "TIER"
		if ev.Early {
			kind = "EARLY(vel)"
		}
		fmt.Printf("─── Fire #%d  [%s]  t=%s ───\n", i+1, kind, msToStr(ev.Tick))
		fmt.Printf("    equity: peak=%.2f cur=%.2f  DD=%.2f%%  equityVel=%.2f%%/bar  tier=%d (%.0f%%->cut%.0f%%)\n",
			ev.EquityPeak, ev.EquityCur, ev.DDPct, ev.EquityVel, ev.TierIdx, ev.TierDDPct, ev.TierClose)
		fmt.Printf("    %-14s %-6s %9s %12s %-10s %8s %s\n",
			"symbol", "side", "PnL%", "vel%/bar", "class", "cut%", "remaining(before->after)")
		var counterN, trendN int
		for _, ps := range ev.Positions {
			class := "顺势 trend"
			if ps.CounterTrend {
				class = "逆势 COUNTER"
				counterN++
			} else {
				trendN++
			}
			fmt.Printf("    %-14s %-6s %9.2f %12.3f %-10s %8.1f   %.2f -> %.2f\n",
				ps.Symbol, ps.Side, ps.PnLPct, ps.Velocity, class,
				ps.CutFrac*100, ps.RemainingBefore, ps.RemainingAfter)
		}
		fmt.Printf("    => cut %d counter-trend (full), %d trend-aligned (light); total closed fraction=%.2f\n\n",
			counterN, trendN, ev.TotalCutQty)
	}
}

// msToStr renders a millisecond epoch as a compact UTC timestamp.
func msToStr(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("01-02 15:04")
}

// l3Describe renders an L3 config: its tier ladder plus the velocity-refinement
// variant (counter-trend-first / flat, early-trigger settings, vel window).
func l3Describe(g backtest.GuardParams) string {
	if g.BreadthEnabled {
		retr := fmt.Sprintf("gb%.0f", g.BreadthGivebackPct)
		if g.BreadthUseATR {
			retr = fmt.Sprintf("atr%.1f", g.BreadthATRMult)
		}
		veps := "off"
		if g.BreadthVelEps < 1e8 {
			veps = fmt.Sprintf("%.2f", g.BreadthVelEps)
		}
		return fmt.Sprintf("BREADTH min%d f%.0f%% %s vel%s cut%.0f cd%d", g.BreadthMinPos, g.BreadthFrac*100, retr, veps, g.BreadthLoserCutPct, g.L3FireCooldownBars)
	}
	if !g.L3Enabled {
		return "off"
	}
	if g.L3Mode == "rolling" {
		pol := "all"
		if g.L3RollCounterOnly {
			pol = "ct"
			if g.L3TrendKeepMult > 0 {
				pol = fmt.Sprintf("ct+k%.0f", g.L3TrendKeepMult*100)
			}
		}
		return fmt.Sprintf("ROLL -%.0f%%/cut%.0f%% %s cd%d ra%.0f w%d", g.L3RollDropPct, g.L3RollCutPct, pol, g.L3FireCooldownBars, g.L3RollReArmPct, g.L3VelWindow)
	}
	if len(g.L3Tiers) == 0 {
		return "off"
	}
	tiers := ""
	for i, t := range g.L3Tiers {
		if i > 0 {
			tiers += "/"
		}
		tiers += fmt.Sprintf("%d-%d", int(t.DrawdownPct), int(t.ClosePct))
	}
	keep := "ct"
	if g.L3TrendKeepMult >= 1.0 {
		keep = "flat"
	} else if g.L3TrendKeepMult > 0 {
		keep = fmt.Sprintf("k%.0f", g.L3TrendKeepMult*100)
	}
	early := "-"
	if g.L3EquityVelTrigger > 0 {
		early = fmt.Sprintf("v%.0f/f%.0f", g.L3EquityVelTrigger, g.L3EarlyFloorPct)
	}
	return fmt.Sprintf("%s %s %s w%d", tiers, keep, early, g.L3VelWindow)
}

// printWalkForward prints the IS-selected configs and their OOS performance/rank.
// The verdict column flags overfit: a config in IS-top that ranks poorly OOS
// (OOSRank in the bottom half of the grid) is suspect.
func printWalkForward(wf backtest.WalkForwardResult) {
	fmt.Printf("split: IS entries=%d  OOS entries=%d\n", wf.ISCount, wf.OOSCount)
	if wf.ISCount == 0 || wf.OOSCount == 0 {
		fmt.Println("insufficient data for split")
		return
	}
	fmt.Printf("IS  baseline: PnL=%.2f MaxDD=%.2f Giveback=%.2f\n",
		wf.ISBaseline.TotalPnL, wf.ISBaseline.MaxPortfolioDD, wf.ISBaseline.MaxGiveback)
	fmt.Printf("OOS baseline: PnL=%.2f MaxDD=%.2f Giveback=%.2f\n",
		wf.OOSBaseline.TotalPnL, wf.OOSBaseline.MaxPortfolioDD, wf.OOSBaseline.MaxGiveback)
	fmt.Println("==== IS-selected configs, validated OUT-OF-SAMPLE ====")
	fmt.Printf("%-3s %-40s | %-9s %-9s | %-9s %-9s %-9s %-10s\n",
		"IS#", "guard", "IS_PnL", "IS_DD", "OOS_PnL", "OOS_DD", "OOS_GB", "OOS_rank")
	for i, r := range wf.Rows {
		// OOS PnL delta vs OOS baseline (did the IS-picked guard help OOS?).
		oosPnLDelta := r.OOS.TotalPnL - wf.OOSBaseline.TotalPnL
		oosDDDelta := wf.OOSBaseline.MaxPortfolioDD - r.OOS.MaxPortfolioDD
		verdict := fmt.Sprintf("%d/%d", r.OOSRank, r.OOSTotal)
		fmt.Printf("%-3d %-40s | %-9.2f %-9.2f | %-9.2f %-9.2f %-9.2f %-10s  ΔPnL=%+.1f ΔDD=%+.1f\n",
			i+1, describe(r.Guard), r.IS.TotalPnL, r.IS.MaxPortfolioDD,
			r.OOS.TotalPnL, r.OOS.MaxPortfolioDD, r.OOS.MaxGiveback, verdict,
			oosPnLDelta, oosDDDelta)
	}
}

func describe(g backtest.GuardParams) string {
	s := ""
	if g.L1Enabled {
		s += fmt.Sprintf("L1[gb%.0f mp%.1f cl%.0f]", g.L1GivebackPct, g.L1MinPeakPct, g.L1ClosePct)
	}
	if g.L2Enabled {
		if g.AdaptiveClose {
			s += fmt.Sprintf("L2[gb%.0f mq%.0f adx%.0f cT%.0f/cC%.0f",
				g.L2GivebackPct, g.L2MinPeakQuote, g.TrendADXThreshold, g.L2ClosePctTrend, g.L2ClosePctChop)
		} else {
			s += fmt.Sprintf("L2[gb%.0f mq%.0f cl%.0f", g.L2GivebackPct, g.L2MinPeakQuote, g.L2ClosePct)
		}
		if g.ConcentrationPct > 0 {
			s += fmt.Sprintf(" c%.0f*%.1f", g.ConcentrationPct*100, g.ConcTightenMult)
		}
		s += "]"
	}
	if s == "" {
		s = "(off)"
	}
	return s
}

type analysisInputs struct {
	robust     bool
	symbolsCSV string
	tf         string
	months     int
	signal     string
	dbPath     string
	traderLike string
	days       int
	limit      int
}

func runProtectionAnalysis(doAblation, doUnitCompare bool, in analysisInputs) {
	if in.robust {
		syms := strings.Split(in.symbolsCSV, ",")
		for i := range syms {
			syms[i] = strings.TrimSpace(syms[i])
		}
		cfg := backtest.RobustConfig{
			Symbols: syms, Timeframe: in.tf, Months: in.months, Signal: in.signal,
		}
		fmt.Printf("PROTECTION ANALYSIS (robust): %d symbols, %d months, signal=%s\n",
			len(syms), in.months, in.signal)
		if doAblation {
			rows, per, err := backtest.AblateProtectionRobust(cfg)
			if err != nil {
				log.Fatalf("ablate robust: %v", err)
			}
			fmt.Printf("per-symbol entries: %v\n", per)
			fmt.Println(backtest.FormatAblation(rows))
		}
		if doUnitCompare {
			rows, per, err := backtest.CompareUnitsRobust(cfg)
			if err != nil {
				log.Fatalf("compare units robust: %v", err)
			}
			fmt.Printf("per-symbol entries: %v\n", per)
			fmt.Println(backtest.FormatUnitCompare(rows))
		}
		return
	}
	runProtectionAnalysisDB(doAblation, doUnitCompare, in)
}

func runProtectionAnalysisDB(doAblation, doUnitCompare bool, in analysisInputs) {
	db, err := sql.Open("sqlite", in.dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	entries, err := backtest.LoadClaudeEntries(db, in.traderLike)
	if err != nil {
		log.Fatalf("load entries: %v", err)
	}
	if in.days > 0 {
		cutoff := msNow() - int64(in.days)*86400_000
		f := entries[:0]
		for _, e := range entries {
			if e.EntryTime >= cutoff {
				f = append(f, e)
			}
		}
		entries = f
	}
	if in.limit > 0 && in.limit < len(entries) {
		entries = entries[len(entries)-in.limit:]
	}
	fmt.Printf("loaded %d closed entries for trader=%s\n", len(entries), in.traderLike)
	if len(entries) == 0 {
		os.Exit(1)
	}
	fmt.Println("fetching OKX history per entry (network-bound)...")

	if doAblation {
		rows, prepared, skipped := backtest.AblateProtectionFromEntries(entries, in.tf)
		fmt.Printf("prepared %d entries (%d skipped)\n", prepared, skipped)
		fmt.Println(backtest.FormatAblation(rows))
	}
	if doUnitCompare {
		rows, prepared, skipped := backtest.CompareUnitsFromEntries(entries, in.tf)
		fmt.Printf("prepared %d entries (%d skipped)\n", prepared, skipped)
		fmt.Println(backtest.FormatUnitCompare(rows))
	}
}

func runOptimize(lambda float64, in analysisInputs) {
	if in.robust {
		syms := strings.Split(in.symbolsCSV, ",")
		for i := range syms {
			syms[i] = strings.TrimSpace(syms[i])
		}
		cfg := backtest.RobustConfig{
			Symbols: syms, Timeframe: in.tf, Months: in.months, Signal: in.signal,
		}
		fmt.Printf("OPTIMIZE (robust): %d symbols, %d months, signal=%s, lambda=%.2f\n",
			len(syms), in.months, in.signal, lambda)
		stages, final, per, err := backtest.OptimizeProtectionRobust(cfg, lambda)
		if err != nil {
			log.Fatalf("optimize robust: %v", err)
		}
		fmt.Printf("per-symbol entries: %v\n", per)
		fmt.Println(backtest.FormatOptStages(stages))
		fmt.Printf("FINAL PARAMS: %s\n", backtest.DescribeParams(final))
		return
	}

	db, err := sql.Open("sqlite", in.dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	entries, err := backtest.LoadClaudeEntries(db, in.traderLike)
	if err != nil {
		log.Fatalf("load entries: %v", err)
	}
	if in.days > 0 {
		cutoff := msNow() - int64(in.days)*86400_000
		f := entries[:0]
		for _, e := range entries {
			if e.EntryTime >= cutoff {
				f = append(f, e)
			}
		}
		entries = f
	}
	if in.limit > 0 && in.limit < len(entries) {
		entries = entries[len(entries)-in.limit:]
	}
	fmt.Printf("loaded %d closed entries for trader=%s, lambda=%.2f\n", len(entries), in.traderLike, lambda)
	if len(entries) == 0 {
		os.Exit(1)
	}
	fmt.Println("fetching OKX history per entry (network-bound)...")
	stages, final, prepared, skipped := backtest.OptimizeProtectionFromEntries(entries, in.tf, lambda)
	fmt.Printf("prepared %d entries (%d skipped)\n", prepared, skipped)
	fmt.Println(backtest.FormatOptStages(stages))
	fmt.Printf("FINAL PARAMS: %s\n", backtest.DescribeParams(final))
}

// parseFloatsCSV parses "0.2,0.3,0.5" into []float64, skipping bad tokens.
func parseFloatsCSV(s string) []float64 {
	var out []float64
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if v, err := strconv.ParseFloat(tok, 64); err == nil {
			out = append(out, v)
		}
	}
	return out
}
