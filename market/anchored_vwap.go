package market

import "math"

// AnchoredVWAP is a volume-weighted average price computed forward from a
// meaningful anchor bar (a swing high, swing low, or the start of the window)
// rather than from a fixed session boundary. Traders use it to judge whether
// buyers/sellers who entered since a pivotal event are in profit — a live
// acceptance/rejection reference. Bands (±1σ of price-vs-vwap) mark stretch.
type AnchoredVWAP struct {
	Anchor      string  `json:"anchor"`       // "swing_high" | "swing_low" | "window_start"
	AnchorPrice float64 `json:"anchor_price"` // price at the anchor bar
	AnchorBars  int     `json:"anchor_bars"`  // bars ago the anchor sits
	VWAP        float64 `json:"vwap"`         // anchored VWAP value at latest bar
	UpperBand   float64 `json:"upper_band"`   // VWAP + 1σ
	LowerBand   float64 `json:"lower_band"`   // VWAP - 1σ
	Timeframe   string  `json:"timeframe"`
}

// CalculateAnchoredVWAPs returns anchored VWAPs from the most recent significant
// swing high and swing low. Returns nil entries are omitted. These are evidence
// references, never hard requirements — callers must tolerate an empty slice.
func CalculateAnchoredVWAPs(klines []Kline, timeframe string) []AnchoredVWAP {
	if len(klines) < 20 {
		return nil
	}
	lookback := swingLookbackForTimeframe(timeframe)

	var out []AnchoredVWAP
	if highs := findSwingHighs(klines, lookback); len(highs) > 0 {
		idx := highs[len(highs)-1] // most recent swing high
		if v := anchoredVWAPFrom(klines, idx, "swing_high", timeframe); v != nil {
			out = append(out, *v)
		}
	}
	if lows := findSwingLows(klines, lookback); len(lows) > 0 {
		idx := lows[len(lows)-1] // most recent swing low
		if v := anchoredVWAPFrom(klines, idx, "swing_low", timeframe); v != nil {
			out = append(out, *v)
		}
	}
	return out
}

// anchoredVWAPFrom computes VWAP forward from anchorIdx (inclusive) to the last
// bar, plus ±1σ bands of the price distribution weighted by volume.
func anchoredVWAPFrom(klines []Kline, anchorIdx int, anchor, timeframe string) *AnchoredVWAP {
	n := len(klines)
	if anchorIdx < 0 || anchorIdx >= n {
		return nil
	}
	// Need a few bars of accumulation for the VWAP to mean anything.
	if n-anchorIdx < 3 {
		return nil
	}

	var sumPV, sumV, sumP2V float64
	for i := anchorIdx; i < n; i++ {
		k := klines[i]
		if k.Volume <= 0 {
			continue
		}
		tp := (k.High + k.Low + k.Close) / 3
		sumPV += tp * k.Volume
		sumV += k.Volume
		sumP2V += tp * tp * k.Volume
	}
	if sumV <= 0 {
		return nil
	}
	vwap := sumPV / sumV
	// Volume-weighted variance = E[p^2] - (E[p])^2, floored at 0.
	variance := sumP2V/sumV - vwap*vwap
	if variance < 0 {
		variance = 0
	}
	sd := math.Sqrt(variance)

	return &AnchoredVWAP{
		Anchor:      anchor,
		AnchorPrice: klines[anchorIdx].Close,
		AnchorBars:  n - 1 - anchorIdx,
		VWAP:        vwap,
		UpperBand:   vwap + sd,
		LowerBand:   vwap - sd,
		Timeframe:   timeframe,
	}
}
