package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

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
	trainfrac := flag.Float64("trainfrac", 0.7, "holdout: fraction of (time-ordered) entries used for training")
	lambda := flag.Float64("lambda", 0.15, "optimiser drawdown penalty: score = PnL - lambda*MaxDD")
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
