package market

import (
	"math"
	"sort"
)

// VolumeProfile is a real fixed-range volume profile computed from klines.
// It answers "where was price accepted vs rejected" — the core question the
// expert Market Structure Map ranks as priority #1. Unlike detectVolumeClusters
// (which only tags high-volume swing candles), this distributes each candle's
// volume across the price range it actually traded, then derives the classic
// POC / Value Area / HVN / LVN structure.
type VolumeProfile struct {
	POC       float64   `json:"poc"`        // Point of Control — price bin with the most volume
	VAH       float64   `json:"vah"`        // Value Area High (upper bound of 70% volume band)
	VAL       float64   `json:"val"`        // Value Area Low (lower bound of 70% volume band)
	HVNs      []float64 `json:"hvns"`       // High Volume Nodes — acceptance shelves (support/resistance)
	LVNs      []float64 `json:"lvns"`       // Low Volume Nodes — rejection gaps (price moves fast through these)
	RangeLow  float64   `json:"range_low"`  // profiled price range low
	RangeHigh float64   `json:"range_high"` // profiled price range high
	TotalVol  float64   `json:"total_vol"`
	Timeframe string    `json:"timeframe"`
	BinCount  int       `json:"bin_count"`
}

// volumeProfileBins controls histogram resolution. 50 bins balances detail vs
// noise for a 200-bar window (the fetch depth used in data.go).
const volumeProfileBins = 50

// valueAreaPct is the standard 70% volume band used to define VAH/VAL.
const valueAreaPct = 0.70

// CalculateVolumeProfile builds a fixed-range volume profile from klines.
// Returns nil when there is insufficient or degenerate data (callers must
// tolerate a nil result — the profile is evidence, never a hard requirement).
func CalculateVolumeProfile(klines []Kline, timeframe string) *VolumeProfile {
	if len(klines) < 20 {
		return nil
	}

	// 1. Establish the price range to profile.
	rangeLow := math.Inf(1)
	rangeHigh := math.Inf(-1)
	var totalVol float64
	for _, k := range klines {
		if k.Low < rangeLow {
			rangeLow = k.Low
		}
		if k.High > rangeHigh {
			rangeHigh = k.High
		}
		totalVol += k.Volume
	}
	// Degenerate: flat price or no traded volume — profile is meaningless.
	if !(rangeHigh > rangeLow) || totalVol <= 0 {
		return nil
	}

	binCount := volumeProfileBins
	binWidth := (rangeHigh - rangeLow) / float64(binCount)
	if binWidth <= 0 {
		return nil
	}

	// 2. Distribute each candle's volume across the bins its [low, high] spans,
	//    proportional to how much of each bin the candle's range overlaps.
	//    This is more faithful than dumping all volume at the typical price.
	bins := make([]float64, binCount)
	for _, k := range klines {
		if k.Volume <= 0 {
			continue
		}
		lo, hi := k.Low, k.High
		if hi <= lo {
			// Zero-range candle: dump volume into its single bin.
			idx := binIndex(k.Close, rangeLow, binWidth, binCount)
			bins[idx] += k.Volume
			continue
		}
		span := hi - lo
		loIdx := binIndex(lo, rangeLow, binWidth, binCount)
		hiIdx := binIndex(hi, rangeLow, binWidth, binCount)
		for b := loIdx; b <= hiIdx; b++ {
			binLo := rangeLow + float64(b)*binWidth
			binHi := binLo + binWidth
			overlap := math.Min(hi, binHi) - math.Max(lo, binLo)
			if overlap <= 0 {
				continue
			}
			bins[b] += k.Volume * (overlap / span)
		}
	}

	// 3. Point of Control — the fullest bin.
	pocIdx := 0
	for i := 1; i < binCount; i++ {
		if bins[i] > bins[pocIdx] {
			pocIdx = i
		}
	}

	// 4. Value Area — expand outward from POC, always grabbing the richer of the
	//    two neighbouring bins, until 70% of total volume is enclosed.
	lowIdx, highIdx := pocIdx, pocIdx
	acc := bins[pocIdx]
	target := totalVol * valueAreaPct
	for acc < target && (lowIdx > 0 || highIdx < binCount-1) {
		var below, above float64 = -1, -1
		if lowIdx > 0 {
			below = bins[lowIdx-1]
		}
		if highIdx < binCount-1 {
			above = bins[highIdx+1]
		}
		if above >= below {
			highIdx++
			acc += above
		} else {
			lowIdx--
			acc += below
		}
	}

	// 5. HVN / LVN — local maxima / minima in the histogram relative to the mean.
	mean := totalVol / float64(binCount)
	hvns, lvns := detectVolumeNodes(bins, rangeLow, binWidth, mean)

	return &VolumeProfile{
		POC:       binCenter(pocIdx, rangeLow, binWidth),
		VAH:       rangeLow + float64(highIdx+1)*binWidth,
		VAL:       rangeLow + float64(lowIdx)*binWidth,
		HVNs:      hvns,
		LVNs:      lvns,
		RangeLow:  rangeLow,
		RangeHigh: rangeHigh,
		TotalVol:  totalVol,
		Timeframe: timeframe,
		BinCount:  binCount,
	}
}

// binIndex maps a price to its histogram bin, clamped into range.
func binIndex(price, rangeLow, binWidth float64, binCount int) int {
	idx := int((price - rangeLow) / binWidth)
	if idx < 0 {
		idx = 0
	}
	if idx > binCount-1 {
		idx = binCount - 1
	}
	return idx
}

// binCenter returns the mid price of a bin.
func binCenter(idx int, rangeLow, binWidth float64) float64 {
	return rangeLow + (float64(idx)+0.5)*binWidth
}

// detectVolumeNodes finds acceptance shelves (HVN) and rejection gaps (LVN) as
// local extrema in the histogram. HVN bins sit above the mean and are peaks;
// LVN bins sit below the mean and are valleys. Results are capped to the most
// significant few so the AI is not flooded with lines.
func detectVolumeNodes(bins []float64, rangeLow, binWidth, mean float64) (hvns, lvns []float64) {
	type node struct {
		price float64
		vol   float64
	}
	var highs, lows []node
	for i := 1; i < len(bins)-1; i++ {
		v := bins[i]
		// Local peak clearly above average → acceptance shelf.
		if v > bins[i-1] && v >= bins[i+1] && v > mean*1.3 {
			highs = append(highs, node{binCenter(i, rangeLow, binWidth), v})
		}
		// Local valley clearly below average → rejection gap.
		if v < bins[i-1] && v <= bins[i+1] && v < mean*0.5 {
			lows = append(lows, node{binCenter(i, rangeLow, binWidth), v})
		}
	}
	// Keep strongest HVNs (highest volume) and deepest LVNs (lowest volume).
	sort.Slice(highs, func(a, b int) bool { return highs[a].vol > highs[b].vol })
	sort.Slice(lows, func(a, b int) bool { return lows[a].vol < lows[b].vol })
	for i := 0; i < len(highs) && i < 4; i++ {
		hvns = append(hvns, highs[i].price)
	}
	for i := 0; i < len(lows) && i < 4; i++ {
		lvns = append(lvns, lows[i].price)
	}
	sort.Float64s(hvns)
	sort.Float64s(lvns)
	return hvns, lvns
}
