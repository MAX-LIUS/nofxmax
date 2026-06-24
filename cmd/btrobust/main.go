package main

import (
	"flag"
	"fmt"
	"strings"

	"nofx/trader/backtest"
)

// Long-period multi-symbol robustness test. Generates EMA-cross mechanical
// entries over months of OKX 1h history across several symbols, then sweeps
// ATR-multiple protection params to confirm the best params generalize.
//
// Usage:
//   btrobust -symbols BTCUSDT,ETHUSDT,SOLUSDT -months 6 -top 12
func main() {
	symbolsCSV := flag.String("symbols", "BTCUSDT,ETHUSDT,SOLUSDT,BNBUSDT,XRPUSDT", "comma-separated symbols")
	months := flag.Int("months", 6, "months of history")
	tf := flag.String("tf", "1h", "timeframe")
	fast := flag.Int("fast", 20, "EMA fast period")
	slow := flag.Int("slow", 50, "EMA slow period")
	cooldown := flag.Int("cooldown", 12, "min bars between entries")
	top := flag.Int("top", 12, "best sweep points to print")
	flag.Parse()

	cfg := backtest.RobustConfig{
		Symbols:      strings.Split(*symbolsCSV, ","),
		Timeframe:    *tf,
		Months:       *months,
		EMAFast:      *fast,
		EMASlow:      *slow,
		CooldownBars: *cooldown,
	}
	for i := range cfg.Symbols {
		cfg.Symbols[i] = strings.TrimSpace(cfg.Symbols[i])
	}

	fmt.Printf("robustness: %d symbols, %d months, EMA%d/%d\n", len(cfg.Symbols), *months, *fast, *slow)
	res, err := backtest.RunRobust(cfg, backtest.DefaultATRGrid())
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}
	fmt.Printf("total mechanical entries: %d\n", res.TotalEntries)

	b := res.Baseline
	fmt.Println("==== BASELINE (Claude percent params) ====")
	fmt.Printf("TotalPnL=%.2f Win%%=%.1f PF=%.2f MaxDD=%.2f Trades=%d\n",
		b.TotalPnL, b.WinRatePct, b.ProfitFactor, b.MaxDrawdown, b.Trades)

	fmt.Println("==== ATR-MULTIPLE SWEEP (best by total PnL) ====")
	n := *top
	if n > len(res.Sweep) {
		n = len(res.Sweep)
	}
	fmt.Printf("%-4s %-6s %-6s %-6s %-6s %-6s | %-10s %-7s %-6s %-10s\n",
		"#", "SL", "TP1", "TP2", "BE1", "BE2", "TotalPnL", "Win%", "PF", "MaxDD")
	for i := 0; i < n; i++ {
		p := res.Sweep[i]
		r := p.Result
		fmt.Printf("%-4d %-6.1f %-6.1f %-6.1f %-6.1f %-6.1f | %-10.2f %-7.1f %-6.2f %-10.2f\n",
			i+1, p.SLATR, p.TP1ATR, p.TP2ATR, p.BE1ATR, p.BE2ATR,
			r.TotalPnL, r.WinRatePct, r.ProfitFactor, r.MaxDrawdown)
	}
}
