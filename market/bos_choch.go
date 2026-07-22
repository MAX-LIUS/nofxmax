package market

import (
	"math"
	"sort"
)

// StructureBreak represents a Break of Structure (BOS) or Change of Character
// (CHoCH). A BOS is a continuation signal (price breaks a swing in the direction
// of the prevailing trend); a CHoCH is a reversal signal (price breaks a swing
// against the prevailing trend). The broken level becomes a retest zone that
// often flips polarity (broken resistance -> support and vice versa).
//
// This is EVIDENCE for the AI, never a hard gate on its own.
type StructureBreak struct {
	Type       string  `json:"type"`       // "BOS" or "CHOCH"
	Direction  string  `json:"direction"`  // "bullish" or "bearish"
	BreakLevel float64 `json:"breakLevel"` // the swing price that was broken
	BarsAgo    int     `json:"barsAgo"`    // bars since the breaking close
	RetestLow  float64 `json:"retestLow"`  // retest zone lower bound
	RetestHigh float64 `json:"retestHigh"` // retest zone upper bound
	Retested   bool    `json:"retested"`   // price has returned to the broken level
	SizeATR    float64 `json:"sizeATR"`    // magnitude of the breaking move in ATR
}

// OrderBlock (supply/demand zone) is the last opposite-direction candle before
// an impulsive move that broke structure. Demand blocks (bullish origin) tend to
// act as support on retest; supply blocks (bearish origin) tend to act as
// resistance. Mitigated means price has already traded back into the zone.
type OrderBlock struct {
	Low       float64 `json:"low"`
	High      float64 `json:"high"`
	Mid       float64 `json:"mid"`
	Direction string  `json:"direction"` // "demand" or "supply"
	BarsAgo   int     `json:"barsAgo"`
	Mitigated bool    `json:"mitigated"`
	SizeATR   float64 `json:"sizeATR"` // size of the impulsive move it originated
}

const (
	bosMinMoveATR  = 0.5  // impulsive break close must clear the level by this many ATR
	bosRetestBandF = 0.25 // retest band = level +/- this * ATR
	maxStructBreaks = 4   // cap surfaced breaks
	maxOrderBlocks  = 4   // cap surfaced order blocks
)

type structPivot struct {
	idx    int
	price  float64
	isHigh bool
}

// DetectStructureBreaks scans the kline series for BOS / CHoCH events and the
// order blocks that originated them. Returns nil slices when data is
// insufficient. atr14 is used for noise/size normalization; a non-positive atr14
// disables the module.
func DetectStructureBreaks(klines []Kline, atr14, currentPrice float64, timeframe string) ([]StructureBreak, []OrderBlock) {
	if len(klines) < 20 || atr14 <= 0 {
		return nil, nil
	}

	lookback := swingLookbackForTimeframe(timeframe)
	highIdx := findSwingHighs(klines, lookback)
	lowIdx := findSwingLows(klines, lookback)
	if len(highIdx) == 0 && len(lowIdx) == 0 {
		return nil, nil
	}

	// Merge pivots into one chronological sequence.
	pivots := make([]structPivot, 0, len(highIdx)+len(lowIdx))
	for _, i := range highIdx {
		pivots = append(pivots, structPivot{idx: i, price: klines[i].High, isHigh: true})
	}
	for _, i := range lowIdx {
		pivots = append(pivots, structPivot{idx: i, price: klines[i].Low, isHigh: false})
	}
	sort.Slice(pivots, func(a, b int) bool { return pivots[a].idx < pivots[b].idx })

	minMove := atr14 * bosMinMoveATR
	band := atr14 * bosRetestBandF
	n := len(klines)

	var breaks []StructureBreak
	var blocks []OrderBlock

	// trend: +1 last break was bullish, -1 bearish, 0 unknown.
	trend := 0

	// For each swing pivot, look forward for the first close that decisively
	// breaks it. A broken swing high = bullish break; broken swing low = bearish.
	for _, p := range pivots {
		if p.isHigh {
			// find first later close that clears the high by minMove
			for k := p.idx + 1; k < n; k++ {
				if klines[k].Close > p.price+minMove {
					dir := "bullish"
					typ := "BOS"
					if trend == -1 {
						typ = "CHOCH"
					}
					sb := buildBreak(klines, k, p.price, dir, typ, band, atr14, currentPrice, n)
					breaks = append(breaks, sb)
					if ob, ok := findOrderBlock(klines, p.idx, k, "demand", atr14, currentPrice, n); ok {
						blocks = append(blocks, ob)
					}
					trend = 1
					break
				}
			}
		} else {
			for k := p.idx + 1; k < n; k++ {
				if klines[k].Close < p.price-minMove {
					dir := "bearish"
					typ := "BOS"
					if trend == 1 {
						typ = "CHOCH"
					}
					sb := buildBreak(klines, k, p.price, dir, typ, band, atr14, currentPrice, n)
					breaks = append(breaks, sb)
					if ob, ok := findOrderBlock(klines, p.idx, k, "supply", atr14, currentPrice, n); ok {
						blocks = append(blocks, ob)
					}
					trend = -1
					break
				}
			}
		}
	}

	breaks = dedupBreaks(breaks)
	blocks = dedupBlocks(blocks)

	// Sort: most recent first (smallest BarsAgo), cap.
	sort.Slice(breaks, func(a, b int) bool { return breaks[a].BarsAgo < breaks[b].BarsAgo })
	sort.Slice(blocks, func(a, b int) bool { return blocks[a].BarsAgo < blocks[b].BarsAgo })
	if len(breaks) > maxStructBreaks {
		breaks = breaks[:maxStructBreaks]
	}
	if len(blocks) > maxOrderBlocks {
		blocks = blocks[:maxOrderBlocks]
	}
	return breaks, blocks
}

// buildBreak assembles a StructureBreak with retest detection.
func buildBreak(klines []Kline, breakIdx int, level float64, dir, typ string, band, atr14, currentPrice float64, n int) StructureBreak {
	sb := StructureBreak{
		Type:       typ,
		Direction:  dir,
		BreakLevel: level,
		BarsAgo:    n - 1 - breakIdx,
		RetestLow:  level - band,
		RetestHigh: level + band,
		SizeATR:    math.Abs(klines[breakIdx].Close-level) / atr14,
	}
	// Retested if any bar after the break traded back into the retest band.
	for k := breakIdx + 1; k < n; k++ {
		if klines[k].Low <= sb.RetestHigh && klines[k].High >= sb.RetestLow {
			sb.Retested = true
			break
		}
	}
	return sb
}

// findOrderBlock locates the last opposite-direction candle in (pivotIdx, breakIdx]
// that precedes the impulsive breaking move. direction is the block's role.
func findOrderBlock(klines []Kline, pivotIdx, breakIdx int, direction string, atr14, currentPrice float64, n int) (OrderBlock, bool) {
	start := pivotIdx
	if start < 0 {
		start = 0
	}
	obIdx := -1
	for k := breakIdx; k > start; k-- {
		if direction == "demand" {
			// last down candle (close < open) before the up-break
			if klines[k].Close < klines[k].Open {
				obIdx = k
				break
			}
		} else {
			// last up candle before the down-break
			if klines[k].Close > klines[k].Open {
				obIdx = k
				break
			}
		}
	}
	if obIdx < 0 {
		return OrderBlock{}, false
	}
	lo := klines[obIdx].Low
	hi := klines[obIdx].High
	if hi <= lo {
		return OrderBlock{}, false
	}
	ob := OrderBlock{
		Low:       lo,
		High:      hi,
		Mid:       (lo + hi) / 2,
		Direction: direction,
		BarsAgo:   n - 1 - obIdx,
		SizeATR:   math.Abs(klines[breakIdx].Close-klines[obIdx].Open) / atr14,
	}
	// Mitigated if a later bar traded back into the block.
	for k := obIdx + 1; k < n; k++ {
		if klines[k].Low <= ob.High && klines[k].High >= ob.Low {
			ob.Mitigated = true
			break
		}
	}
	return ob, true
}

// dedupBreaks removes breaks whose levels are near-identical (same direction),
// keeping the most recent.
func dedupBreaks(in []StructureBreak) []StructureBreak {
	if len(in) <= 1 {
		return in
	}
	sort.Slice(in, func(a, b int) bool { return in[a].BarsAgo < in[b].BarsAgo })
	var out []StructureBreak
	for _, b := range in {
		dup := false
		for _, kept := range out {
			if kept.Direction == b.Direction && kept.BreakLevel != 0 &&
				math.Abs(kept.BreakLevel-b.BreakLevel)/math.Abs(kept.BreakLevel) < 0.001 {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, b)
		}
	}
	return out
}

// dedupBlocks removes overlapping order blocks of the same direction, keeping
// the most recent.
func dedupBlocks(in []OrderBlock) []OrderBlock {
	if len(in) <= 1 {
		return in
	}
	sort.Slice(in, func(a, b int) bool { return in[a].BarsAgo < in[b].BarsAgo })
	var out []OrderBlock
	for _, b := range in {
		dup := false
		for _, kept := range out {
			if kept.Direction == b.Direction && b.High >= kept.Low && b.Low <= kept.High {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, b)
		}
	}
	return out
}
