package backtest

// PerTradeRow is one entry's replay outcome alongside its live actuals, for
// external (gate-filtering) analysis.
type PerTradeRow struct {
	Symbol      string
	Side        string
	EntryTime   int64
	ActualPnL   float64
	ReplayPnL   float64
	BarsHeld    int
	FullyClosed bool
}

// ReplayLoadedPerTrade replays each prepared entry under p and returns a
// per-trade row (live actual + replay result). Exported so cmd/ analysis can
// join replay PnL to external gate decisions by (symbol, side, entry_time).
func ReplayLoadedPerTrade(p ProtectionParams, loaded []LoadedEntry) []PerTradeRow {
	out := make([]PerTradeRow, 0, len(loaded))
	for _, le := range loaded {
		res := ReplayEntry(p, le.entry, le.bars, le.entryIdx)
		out = append(out, PerTradeRow{
			Symbol:      le.entry.Symbol,
			Side:        le.entry.Side,
			EntryTime:   le.entry.EntryTime,
			ActualPnL:   le.entry.RealizedPnL,
			ReplayPnL:   res.RealizedPnL,
			BarsHeld:    res.BarsHeld,
			FullyClosed: res.FullyClosed,
		})
	}
	return out
}
