package main

import (
	"database/sql"
	"flag"
	"fmt"
	"math"
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
	variantDump := flag.String("variantdump", "", "with -variants: write per-variant per-trade replay rows to this TSV path for external robustness analysis (leave-one-out, time split, per-symbol)")
	trail := flag.Bool("trail", false, "sweep the ratcheting structural stop (TrailStruct*) vs the frozen baseline: fire count, profit locked, profit given up early (needs -liveconfig with a RangeSLCloseConfirm stop)")
	entryQual := flag.Bool("entryqual", false, "profile ENTRY quality independent of stops: post-entry MAE/MFE in ATR, immediate-adverse rate, never-green rate. Isolates whether the entry price itself is bad.")
	rewardATR := flag.Bool("rewardatr", false, "bucket entries by AI reward distance in ATR (first_target/entryATR) and by effective RR after floor/backstop clamp, with realized PnL per bucket, plus a reward-ATR floor-gate PnL simulation. Uses -slfloor/-slbackstop for the clamp band.")
	slFloor := flag.Float64("slfloor", 1.5, "structural SL floor ATR multiple used by -rewardatr clamp")
	slBackstop := flag.Float64("slbackstop", 4.5, "structural SL backstop ATR multiple used by -rewardatr clamp")
	fbSweep := flag.Bool("fbsweep", false, "2D sweep of the structural stop band [floor, backstop], driving backstop DOWN toward the floor to find how tight the stop can get before net PnL degrades. Needs -liveconfig with a structural (RangeSL) baseline.")
	ddSource := flag.Bool("ddsource", false, "localize big-drawdown source: bucket entries by post-entry MAE (ATR) with realized PnL + dominant close_reason, entry-bar-green split, and a no-progress early-exit PnL simulation.")
	confirmStop := flag.Bool("confirmstop", false, "sweep a FIXED close-confirm adverse-excursion stop at N×ATR (active while underwater, close-confirmed, backstop kept for wicks) vs the live swing-confirm. Needs -liveconfig with a structural (RangeSL) baseline.")
	combinedGate := flag.Bool("combinedgate", false, "replay full set under baseline vs entry-gate-only (skip target<minRewardATR) vs band-only (backstop tighten) vs both stacked. Needs -liveconfig.")
	maxHold := flag.Bool("maxholdsweep", false, "sweep the max-hold time exit: live vs drop-profit-exemption vs disabled vs alternative hours. Needs -liveconfig -proxy.")
	mhPlaceboH := flag.Float64("mhplacebo", 0, "with -maxholdsweep: run the max-hold PLACEBO at this hour threshold (random per-entry H with identical exemption) to test whether the threshold carries information or merely truncates a losing ledger")
	mhPlaceboEx := flag.Float64("mhplaceboex", 3, "profit-exempt %% used by -mhplacebo (must match the grid cell being defended)")
	mhPlaceboSeeds := flag.Int("mhplaceboseeds", 400, "number of placebo seeds for -mhplacebo")
	anchorProx := flag.Bool("anchorprox", false, "bucket entries by entry→SL-anchor distance in ATR with realized PnL, plus a gate sim blocking entries whose confirmed anchor is farther than N×ATR (no-mans-land detection).")
	minSLGate := flag.Bool("minslgate", false, "simulate the sl_distance_below_atr_min entry gate across thresholds (0.5-1.5): realized PnL kept vs blocked, to size the frequency/PnL tradeoff of raising the min-SL floor.")
	provenSL := flag.Bool("provensl", false, "compare the baseline structural stop (fractal pivot) vs PreferProvenLevels (order-block edge, never wider than pivot): realized PnL, win%, drawdown, stop-hit% for fractal vs proven. Needs -liveconfig with a RangeSL baseline.")
	bsMin := flag.Float64("bsmin", 1.5, "min backstop ATR for -fbsweep fine grid")
	bsMax := flag.Float64("bsmax", 4.5, "max backstop ATR for -fbsweep fine grid")
	bsStep := flag.Float64("bsstep", 0.5, "backstop ATR step for -fbsweep fine grid")
	configTrader := flag.String("configtrader", "", "when set with -liveconfig, load the protection CONFIG from THIS trader pattern while entries still come from -trader. Lets a large pooled entry set (many traders) be replayed under one trader's config (e.g. Claude-R 15m).")
	horizonHours := flag.Int("horizon", 0, "when >0, replay OPEN-ENDED over this forward horizon (hours) past each entry instead of clamping at the live exit time. Makes time-based exits (max-hold/time-stop) testable. Uses a per-symbol disk bar cache (-barcache).")
	barCachePath := flag.String("barcache", "/tmp/bt_barcache.gob", "path to the persisted per-symbol bar cache used by -horizon")
	fakeRetest := flag.Bool("fakeretest", false, "sweep the structural_fit fake_retest_trap gate: current 0.15% fixed vs %-sweep vs ATR-scaled vs real closed-candle confirmation; splits PASS/BLOCK realized PnL to test whether the gate removes worse trades.")
	confirmTFLadder := flag.Bool("confirmtfladder", false, "sweep the CONFIRMATION TIMEFRAME up a ladder (base→2×→4×→...) with a fixed wall-clock horizon, so you can read the best confirm TF RELATIVE to any primary TF (15m/1h/4h). Fetch with -tf 15m and -horizon (e.g. 48). Rule fixed at 1close+wick.")
	confirmTiming := flag.Bool("confirmtiming", false, "study confirmation TIMING × TIMEFRAME: re-derive each entry from the anchor touch at 1/2/3-close (+wick) rules on 15m and 1h, measure fill%, bars-to-confirm, risk%, MFE/MAE in R and stop-hit% — the too-early(fakes) vs too-late(RR decay) tradeoff. Fetch with -tf 15m.")
	structTF := flag.Bool("structtf", false, "ISOLATION: replay on the trader's NATIVE-TF bars (from a finer fetch) with ATR/TP/BE/DD/backstop fixed, sweeping ONLY the structural stop's source timeframe DOWN (1h native→1h/30m/15m; 15m native→15m/10m/5m). Requires -liveconfig with a close-confirm structural baseline; native TF is read from the config.")
	exchange := flag.String("exchange", "okx", "bar-data exchange for replay: okx | binance. Binance (proxy-aware) is for binance-type traders like BN so the replay reads the exchange the trades executed on.")
	flag.Parse()

	// Select the bar provider by exchange so binance traders (BN) don't get OKX prices.
	barProvider := backtest.OKXBars
	if *exchange == "binance" {
		barProvider = backtest.BinanceBars
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
	// Attach structural plans + AI first_target so the RangeSL fallback-anchor
	// sweep (fbanchor-firsttarget) can read risk_reward.first_target per entry.
	// Match window = 2h before entry (nearest preceding open decision).
	if n, aerr := backtest.AttachStructuralPlans(db, *traderLike, 2*60*60*1000, entries); aerr != nil {
		fmt.Printf("(warn: could not attach structural plans: %v)\n", aerr)
	} else {
		fmt.Printf("attached structural plans (with AI first_target) to %d/%d entries\n", n, len(entries))
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

	var loaded []backtest.LoadedEntry
	var skipped int
	if *horizonHours > 0 {
		fmt.Printf("HORIZON mode: open-ended replay %dh past each entry (bar cache: %s)\n", *horizonHours, *barCachePath)
		cache, cerr := backtest.LoadBarCache(*barCachePath, *tf)
		if cerr != nil {
			log.Fatalf("load bar cache: %v", cerr)
		}
		var herr error
		loaded, skipped, herr = backtest.PrepareEntriesHorizon(entries, *tf, *horizonHours, cache, barProvider)
		if herr != nil {
			log.Fatalf("prepare horizon: %v", herr)
		}
		if serr := cache.Save(*barCachePath); serr != nil {
			fmt.Printf("(warn: could not persist bar cache: %v)\n", serr)
		}
	} else {
		fmt.Println("fetching OKX history per entry (network-bound, please wait)...")
		loaded, skipped = backtest.PrepareEntries(entries, *tf, barProvider)
	}
	fmt.Printf("prepared %d entries (%d skipped: no/short data)\n", len(loaded), skipped)
	if len(loaded) == 0 {
		os.Exit(1)
	}

	// 1) Baseline: either the hardcoded percent params, or the trader's LIVE
	//    strategy protection config (ATR/structural units) via -liveconfig. The
	//    live config is what makes trusted-subset fidelity meaningful.
	baseline := backtest.ClaudeBaselineParams()
	if *liveConfig {
		cfgPattern := *traderLike
		if *configTrader != "" {
			cfgPattern = *configTrader
		}
		cfg, ctf, err := backtest.LoadTraderStrategyConfig(db, cfgPattern)
		if err != nil {
			log.Fatalf("liveconfig: %v", err)
		}
		// Replay at the requested -tf, not the config's own primary_tf, so a pooled
		// entry set can be evaluated at a chosen granularity (e.g. 15m).
		baseline = backtest.LiveConfigParams(cfg, backtest.TimeframeHours(*tf))
		fmt.Printf("(baseline from LIVE config of %q; config_primary_tf=%s replay_tf=%s unit=%s TP=%d BE=%d DD=%d SL_atr=%.1f timestop=%.0fh/%.1f%% maxhold=%.0fh/%.1f%%)\n",
			cfgPattern, ctf, *tf, baseline.Unit, len(baseline.TPLegs), len(baseline.BELegs), len(baseline.DDRules), baseline.StopLossATR,
			baseline.CloseProxy.TimeStopHours, baseline.CloseProxy.TimeStopLossPct, baseline.CloseProxy.MaxHoldHours, baseline.CloseProxy.MaxHoldProfitExemptPct)
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

	// 1c) Structural-SL branch mix: how often the fallback branch (the ONLY place the
	//     0.8 RR cap / 3.0 ATR fallback participate) actually fires vs the normal
	//     clamped-structural path. Only meaningful with the live RangeSL spec.
	if *liveConfig && baseline.RangeSLEnabled {
		fmt.Println("==== STRUCTURAL-SL BRANCH MIX ====")
		fmt.Println(backtest.FormatStructuralBranchStats(backtest.ComputeStructuralBranchStats(baseline, loaded)))
		// RR-cap distribution: what ATR-multiple does ratio×TP impose, and is it
		// masked by the floor? Shows whether 0.8 even binds, under both anchors.
		fmt.Println("==== RR-CAP DISTRIBUTION (does 0.8 ever bind, or is the floor tighter?) ====")
		for _, anchor := range []string{"max_tp", "first_target"} {
			for _, r := range []float64{0.8, 1.25, 2.0} {
				fmt.Println(backtest.FormatRRCapStat(backtest.ComputeRRCapStat(baseline, loaded, anchor, r)))
			}
		}
		// Min-lot TP collapse: nearest tier vs AI first_target re-anchor (tol 0.2%).
		fmt.Println("==== TP-COLLAPSE ANCHOR (min-lot: nearest tier vs AI first_target+tol) ====")
		crows, cn := backtest.CompareCollapseAnchors(loaded, baseline, 0.2)
		fmt.Print(backtest.FormatStructCompare(crows, cn))
	}

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
		// Aggregate dPnL hides whether a delta rests on a handful of trades. Dump
		// per-variant per-trade replay PnL so leave-one-out / time-split /
		// per-symbol robustness can be judged outside the engine.
		if *variantDump != "" {
			f, err := os.Create(*variantDump)
			if err != nil {
				log.Fatalf("variantdump: %v", err)
			}
			defer f.Close()
			fmt.Fprintf(f, "variant\tsymbol\tside\tentry_time\tactual_pnl\treplay_pnl\tbars_held\tfully_closed\n")
			for _, v := range backtest.LiveVariants(baseline) {
				for _, r := range backtest.ReplayLoadedPerTrade(v.P, loaded) {
					fmt.Fprintf(f, "%s\t%s\t%s\t%d\t%.6f\t%.6f\t%d\t%t\n",
						v.Name, r.Symbol, r.Side, r.EntryTime, r.ActualPnL, r.ReplayPnL, r.BarsHeld, r.FullyClosed)
				}
			}
			fmt.Printf("→ per-trade rows written to %s\n", *variantDump)
		}

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

	// 5) Trailing (ratchet) structural-stop sweep: how often it fires, how much
	//    PnL it adds vs the frozen boundary, and how much profit it gives up early.
	if *trail {
		if !*liveConfig {
			fmt.Println("\n(-trail requires -liveconfig for the real structural spec; skipping)")
			return
		}
		if !baseline.RangeSLEnabled || !baseline.RangeSLCloseConfirm {
			fmt.Println("\n(-trail needs a RangeSLCloseConfirm structural baseline; this trader's config doesn't run one; skipping)")
			return
		}
		fmt.Println("\n==== TRAILING STRUCTURAL-STOP SWEEP (ratchet vs frozen boundary) ====")
		fmt.Print(backtest.FormatTrailDiag(baseline, loaded))
		fmt.Print(backtest.FormatTrailStats(baseline, loaded))
		fmt.Println("\n==== HIGHER-PERIOD STOP SWEEP (loose higher-TF structure as a tighter stop) ====")
		fmt.Print(backtest.FormatHigherTFStopStats(baseline, loaded))
	}

	// 5z) Reward-ATR bucket analysis: does "target too close" lose money?
	if *rewardATR {
		fmt.Println()
		fmt.Print(backtest.FormatRewardATRBuckets(*traderLike, loaded, *slFloor, *slBackstop))
		fmt.Println()
		return
	}

	// 5z-4) Min-SL-distance gate simulation.
	if *minSLGate {
		fmt.Println()
		fmt.Print(backtest.FormatMinSLGate(*traderLike, loaded, []float64{0.5, 0.8, 1.0, 1.2, 1.5}))
		fmt.Println()
		return
	}

	// 5z-3) Anchor-proximity analysis.
	if *anchorProx {
		fmt.Println()
		fmt.Print(backtest.FormatAnchorProximity(*traderLike, loaded))
		fmt.Println()
		return
	}

	// 5z-2) Max-hold time-exit sweep.
	if *maxHold {
		fmt.Println()
		fmt.Print(backtest.FormatMaxHoldSweep(*traderLike, baseline, loaded))
		fmt.Println()
		if *mhPlaceboH > 0 {
			fmt.Print(backtest.FormatMaxHoldPlacebo(baseline, loaded, *mhPlaceboH, *mhPlaceboEx, *mhPlaceboSeeds))
			fmt.Println()
		}
		return
	}

	// 5z-1) Combined entry-gate + band replay. Uses -slbackstop for the band.
	if *combinedGate {
		fmt.Println()
		fmt.Print(backtest.FormatCombinedGate(*traderLike, baseline, loaded, 1.0, *slBackstop))
		fmt.Println()
		return
	}

	// 5y) PreferProvenLevels A/B: baseline (fractal pivot) vs proven (order-block edge).
	if *provenSL {
		fmt.Println()
		fmt.Print(backtest.FormatProvenSLCompare(baseline, loaded))
		fmt.Println()
		return
	}

	// 5z0) Close-confirm adverse-excursion stop sweep.
	if *confirmStop {
		fmt.Println()
		stops := []float64{1.5, 2.0, 2.25, 2.5, 3.0, 3.5}
		fmt.Print(backtest.FormatConfirmStopSweep(*traderLike, baseline, loaded, stops))
		fmt.Println()
		return
	}

	// 5z1) Drawdown source localization.
	if *ddSource {
		fmt.Println()
		fmt.Print(backtest.FormatDrawdownSource(*traderLike, loaded))
		fmt.Println()
		return
	}

	// 5z2) Floor×backstop 2D sweep: how tight can the backstop get?
	if *fbSweep {
		fmt.Println()
		floors := []float64{*slFloor}
		// Fine backstop grid over [bsMin, bsMax] at bsStep resolution.
		backstops := []float64{}
		for b := *bsMin; b <= *bsMax+1e-9; b += *bsStep {
			backstops = append(backstops, roundTo(b, 1))
		}
		fmt.Print(backtest.FormatFloorBackstopSweep(*traderLike, baseline, loaded, floors, backstops))
		fmt.Println()
		return
	}

	// 6) Entry-quality profile: is the ENTRY itself bad, independent of stops?
	if *entryQual {
		fmt.Println()
		fmt.Print(backtest.FormatEntryQuality(*traderLike, loaded, 10))
		fmt.Println()
		// Split by the LIVE chart_trend gate (BN's enforce gate) to see whether it
		// selects better entries: win=30 align>=0.55 r2>=0.60 (the live params).
		fmt.Print(backtest.FormatGateEntryQuality(*traderLike, loaded, 30, 0.55, 0.60))
		fmt.Println()
		// Timing-filter sweep: does confirmation / delay / spike-avoidance help?
		fmt.Print(backtest.FormatTimingFilters(*traderLike, baseline, loaded))
		fmt.Println()
		// Stack the direction gate with the causal timing filter.
		fmt.Print(backtest.FormatGatePlusTiming(*traderLike, baseline, loaded, 30, 0.55, 0.60))
		fmt.Println()
		// Decompose WHY shift-confirm helps/hurts: dropped winners/losers + slippage.
		fmt.Print(backtest.FormatShiftConfirmDecomp(*traderLike, baseline, loaded))
		fmt.Println()
		// Isolate slippage: enter at bar+1 OPEN vs CLOSE.
		fmt.Print(backtest.FormatShiftConfirmOpen(*traderLike, baseline, loaded))
		fmt.Println()
		// #1 CAUSAL intrabar momentum-confirm: stop-entry at entry+k*ATR.
		fmt.Print(backtest.FormatTriggerEntry(*traderLike, baseline, loaded))
		fmt.Println()
		// #1b RIGOROUS full 2D sweep with 3-force decomposition + robustness.
		fmt.Print(backtest.FormatTriggerEntrySweep(*traderLike, baseline, loaded))
		fmt.Println()
		// #1c SKEPTIC battery: placebo, out-of-sample, decomposition.
		fmt.Print(backtest.FormatTriggerEntryValidate(*traderLike, baseline, loaded, 0.05, 5))
		fmt.Println()
		fmt.Print(backtest.FormatTriggerEntryValidate(*traderLike, baseline, loaded, 0.10, 5))
	}

	// 7) Structural-TF isolation: engine on 1h bars, ONLY the structural stop's
	//    source timeframe changes (1h/30m/15m). Removes the ATR-shrink confound of
	//    a whole-engine -tf 15m replay. Requires -tf 15m so we hold the 15m fetch.
	if *fakeRetest {
		fmt.Println()
		fmt.Print(backtest.FormatFakeRetestSweep(*traderLike, loaded))
	}

	if *confirmTiming {
		fmt.Println()
		fmt.Print(backtest.FormatConfirmTiming(*traderLike, loaded, 32))
	}

	if *confirmTFLadder {
		fmt.Println()
		hzn := float64(*horizonHours)
		if hzn <= 0 {
			hzn = 48 // default wall-clock horizon
		}
		// ladder rungs adapt to the base TF so the rung labels land on real TFs.
		//   base 15m → 15m,30m,1h,2h,4h   (mult 1,2,4,8,16)
		//   base 5m  → 5m,10m,15m,30m,1h  (mult 1,2,3,6,12)
		mults := []int{1, 2, 4, 8, 16}
		if *tf == "5m" {
			mults = []int{1, 2, 3, 6, 12}
		}
		fmt.Print(backtest.FormatConfirmTFLadder(*traderLike, *tf, loaded, mults, hzn))
	}

	if *structTF {
		if !*liveConfig {
			fmt.Println("\n(-structtf requires -liveconfig for the real structural spec; skipping)")
			return
		}
		if !baseline.RangeSLEnabled || !baseline.RangeSLCloseConfirm {
			fmt.Println("\n(-structtf needs a RangeSLCloseConfirm structural baseline; skipping)")
			return
		}
		// Configure the isolation plan from the trader's NATIVE (config primary) TF, so
		// every non-structural lever pins at the native TF and only the structural
		// boundary source is swept DOWN. 1h native → sweep 1h/30m/15m; 15m native
		// (claude-r) → sweep 15m/10m/5m.
		_, ctf, cerr := backtest.LoadTraderStrategyConfig(db, cfgPatternForStructTF(*traderLike, *configTrader))
		if cerr != nil {
			fmt.Printf("\n(-structtf: cannot read native TF: %v; skipping)\n", cerr)
			return
		}
		baseTF, ok := backtest.SetStructTFPlan(ctf)
		if !ok {
			fmt.Printf("\n(-structtf: native TF %q not supported (only 1h/15m); skipping)\n", ctf)
			return
		}
		// Re-fetch with MORE pre-entry base bars so the aggregated native series spans
		// the structural lookback (24 native bars) + ATR warmup. A thin fetch leaves the
		// native reference without a computable boundary (degenerate ref). 120 base bars
		// covers 24×refMult for both plans (1h: 96 15m; 15m: 72 5m) plus ATR warmup.
		fmt.Printf("\n(structtf: native TF=%s → fetching %s base with 200 pre-entry bars for a faithful reference...)\n", ctf, baseTF)
		stfLoaded, stfSkipped := backtest.PrepareEntriesPre(entries, baseTF, 200, barProvider)
		fmt.Printf("structtf prepared %d entries (%d skipped)\n", len(stfLoaded), stfSkipped)
		fmt.Println()
		fmt.Print(backtest.FormatEntryPriceSanity(stfLoaded))
		// Drop phantom-fill entries (DB entry_price outside the bar range >1%) so the
		// risk metrics aren't polluted by fabricated backstop losses.
		var stfDropped int
		stfLoaded, stfDropped = backtest.FilterAlignedEntries(stfLoaded, 1.0)
		fmt.Printf("\n(filtered %d phantom-fill entries; %d clean entries remain)\n", stfDropped, len(stfLoaded))
		fmt.Println()
		fmt.Print(backtest.FormatStructTFStats(baseline, stfLoaded))
		fmt.Println()
		fmt.Print(backtest.FormatStructTFForensic(baseline, stfLoaded, 15))
		fmt.Println()
		fmt.Print(backtest.TraceWorstLoss(baseline, stfLoaded))
	}
}

// cfgPatternForStructTF picks the trader pattern whose LIVE config supplies the native
// timeframe: the explicit -configtrader when set, else the -trader pattern.
func cfgPatternForStructTF(traderLike, configTrader string) string {
	if configTrader != "" {
		return configTrader
	}
	return traderLike
}

// roundTo rounds x to n decimal places (used to clean float accumulation in grids).
func roundTo(x float64, n int) float64 {
	p := math.Pow(10, float64(n))
	return math.Round(x*p) / p
}
