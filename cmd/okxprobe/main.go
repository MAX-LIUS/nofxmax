package main

import (
	"fmt"
	"time"

	"nofx/market"
)

// okxprobe checks how far back OKX history-candles serves 1h bars per symbol,
// to bound how large a long-period backtest sample we can build.
func main() {
	syms := []string{"BTCUSDT", "ETHUSDT", "SOLUSDT", "LINKUSDT", "DOGEUSDT", "ADAUSDT", "AVAXUSDT", "LTCUSDT"}
	for _, mo := range []int{12, 18, 24, 36} {
		end := time.Now()
		start := end.AddDate(0, -mo, 0)
		bars, err := market.GetKlinesRangeOKX("BTCUSDT", "1h", start, end)
		if err != nil {
			fmt.Printf("%dmo: ERR %v\n", mo, err)
			continue
		}
		if len(bars) > 0 {
			fmt.Printf("BTC %dmo: %d bars, earliest=%s\n",
				mo, len(bars), time.UnixMilli(bars[0].OpenTime).UTC().Format("2006-01-02"))
		}
	}
	fmt.Println("--- per-symbol max-depth (24mo request) ---")
	end := time.Now()
	start := end.AddDate(0, -24, 0)
	for _, s := range syms {
		bars, err := market.GetKlinesRangeOKX(s, "1h", start, end)
		if err != nil || len(bars) == 0 {
			fmt.Printf("  %s: ERR/empty %v\n", s, err)
			continue
		}
		fmt.Printf("  %s: %d bars, earliest=%s\n",
			s, len(bars), time.UnixMilli(bars[0].OpenTime).UTC().Format("2006-01-02"))
	}
}
