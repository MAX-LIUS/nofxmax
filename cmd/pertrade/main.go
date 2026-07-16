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

func main() {
	dbPath := flag.String("db", "/opt/webstack/nofx/data/data.db", "SQLite DB")
	traderLike := flag.String("trader", "", "trader_id LIKE")
	tf := flag.String("tf", "1h", "timeframe")
	horizon := flag.Int("horizon", 0, "forward horizon hours; 0=clamp at actual exit_time (faithful)")
	barcache := flag.String("barcache", "/tmp/bt_barcache_pt.gob", "bar cache")
	out := flag.String("out", "", "output csv")
	flag.Parse()

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil { log.Fatal(err) }
	defer db.Close()

	entries, err := backtest.LoadClaudeEntries(db, *traderLike)
	if err != nil { log.Fatal(err) }
	fmt.Fprintf(os.Stderr, "loaded %d entries\n", len(entries))

	cfg, ctf, err := backtest.LoadTraderStrategyConfig(db, *traderLike)
	if err != nil { log.Fatal(err) }
	baseline := backtest.LiveConfigParams(cfg, backtest.TimeframeHours(*tf))
	fmt.Fprintf(os.Stderr, "config_tf=%s TP=%d BE=%d DD=%d SLatr=%.1f\n", ctf, len(baseline.TPLegs), len(baseline.BELegs), len(baseline.DDRules), baseline.StopLossATR)

	var loaded []backtest.LoadedEntry
	var skipped int
	if *horizon > 0 {
		cache, err := backtest.LoadBarCache(*barcache, *tf)
		if err != nil { log.Fatal(err) }
		loaded, skipped, err = backtest.PrepareEntriesHorizon(entries, *tf, *horizon, cache, backtest.OKXBars)
		if err != nil { log.Fatal(err) }
		cache.Save(*barcache)
	} else {
		loaded, skipped = backtest.PrepareEntries(entries, *tf, backtest.OKXBars)
	}
	fmt.Fprintf(os.Stderr, "prepared %d (skipped %d) horizon=%d\n", len(loaded), skipped, *horizon)

	rows := backtest.ReplayLoadedPerTrade(baseline, loaded)
	f, err := os.Create(*out)
	if err != nil { log.Fatal(err) }
	defer f.Close()
	fmt.Fprintln(f, "symbol,side,entry_time,actual_pnl,replay_pnl,bars_held,fully_closed")
	for _, r := range rows {
		fmt.Fprintf(f, "%s,%s,%d,%.4f,%.4f,%d,%v\n", r.Symbol, r.Side, r.EntryTime, r.ActualPnL, r.ReplayPnL, r.BarsHeld, r.FullyClosed)
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d rows)\n", *out, len(rows))
}
