package market

// Method 4: asset-adaptive stop-loss confirmation timeframe.
//
// Backtest finding (prior session): confirming the structural close-confirm stop on
// a timeframe ~2× FINER than the native/primary TF is optimal — 1h→30m gave +10.81
// PnL and cut max drawdown 88→77. Going 4× finer (or finer) whipsaws: the confirm
// bar closes on noise. Stocks/commodities whipsaw WORST on fine TFs (open-gap +
// session microstructure), so they must keep the native TF. Crypto (24/7, deeper
// microstructure) tolerates the 2× refine.
//
// Implementation is deliberately conservative: we only refine when we can land on a
// CLEAN ~2× step. The canonical ladder is ~2× between adjacent tokens in the mid/high
// band (15m→30m→1h→2h→4h) but 3× in the low band (5m→15m). Refining a 3× step would
// overshoot into whipsaw territory, so there we keep the native TF.

// confirmTFLadder is the ordered timeframe ladder with minute counts, used to find
// the adjacent finer step. Mirrors store.tfLadder / tfMinutes (kept local to avoid a
// market→store import cycle).
var confirmTFLadder = []struct {
	tf  string
	min int
}{
	{"1m", 1}, {"3m", 3}, {"5m", 5}, {"15m", 15}, {"30m", 30},
	{"1h", 60}, {"2h", 120}, {"4h", 240}, {"6h", 360}, {"12h", 720}, {"1d", 1440},
}

func confirmTFIndex(tf string) int {
	for i, e := range confirmTFLadder {
		if e.tf == tf {
			return i
		}
	}
	return -1
}

// ConfirmTimeframe returns the stop-loss confirmation timeframe for a symbol given
// its native/primary TF. For stocks and commodities (and any unknown that resolves
// as scheduled) it returns nativeTF unchanged. For crypto it returns the adjacent
// finer ladder step IF that step is a clean ~2× refine (ratio ≤ 2); otherwise it
// keeps nativeTF (never refines into a 3× overshoot, never goes coarser).
func ConfirmTimeframe(nativeTF, symbol string) string {
	if IsScheduledAsset(symbol) {
		return nativeTF // stocks/commodities: keep native, fine TFs whipsaw worst
	}
	return refineCryptoConfirmTF(nativeTF)
}

// refineCryptoConfirmTF is the pure crypto refinement: adjacent finer step only when
// the native/finer minute ratio is ≤ 2. Exported behaviour is via ConfirmTimeframe.
func refineCryptoConfirmTF(nativeTF string) string {
	idx := confirmTFIndex(nativeTF)
	if idx <= 0 {
		return nativeTF // unknown TF, or already the finest (1m): nothing to refine
	}
	native := confirmTFLadder[idx]
	finer := confirmTFLadder[idx-1]
	if finer.min <= 0 {
		return nativeTF
	}
	// Only refine on a clean ~2× step. Low-band adjacency (e.g. 15m→5m) is 3× and
	// overshoots into whipsaw; keep native there.
	if float64(native.min)/float64(finer.min) <= 2.0 {
		return finer.tf
	}
	return nativeTF
}
