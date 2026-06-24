package backtest

// Aggregate combines per-trade results into a portfolio summary, including an
// equity-curve max drawdown computed in entry order.
func Aggregate(results []TradeResult) PortfolioResult {
	pr := PortfolioResult{Results: results, Trades: len(results)}
	if len(results) == 0 {
		return pr
	}
	var grossProfit, grossLoss, sumRet float64
	var equity, peakEquity, maxDD float64
	for _, r := range results {
		pr.TotalPnL += r.RealizedPnL
		sumRet += r.ReturnPct
		if r.RealizedPnL >= 0 {
			pr.Wins++
			grossProfit += r.RealizedPnL
		} else {
			pr.Losses++
			grossLoss += -r.RealizedPnL
		}
		equity += r.RealizedPnL
		if equity > peakEquity {
			peakEquity = equity
		}
		if dd := peakEquity - equity; dd > maxDD {
			maxDD = dd
		}
	}
	pr.AvgReturnPct = sumRet / float64(len(results))
	pr.WinRatePct = float64(pr.Wins) / float64(len(results)) * 100
	if grossLoss > 0 {
		pr.ProfitFactor = grossProfit / grossLoss
	} else if grossProfit > 0 {
		pr.ProfitFactor = 1e9 // no losses
	}
	pr.MaxDrawdown = maxDD
	return pr
}
