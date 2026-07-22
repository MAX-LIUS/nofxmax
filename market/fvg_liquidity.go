package market

import (
	"math"
	"sort"
)

// FairValueGap (FVG / imbalance) is a 3-candle pattern where the middle candle
// moves so fast that candle1 and candle3 do not overlap, leaving an unfilled
// price gap. These gaps act as magnets: price often returns to "fill" them.
// A bullish FVG (gap below) tends to be support on retrace; bearish is
// resistance. Evidence, not a hard rule.
type FairValueGap struct {
	Low       float64 `json:"low"`
	High      float64 `json:"high"`
	Mid       float64 `json:"mid"`
	Direction string  `json:"direction"` // "bullish" | "bearish"
	BarsAgo   int     `json:"bars_ago"`
	Filled    bool    `json:"filled"`     // price has since traded back through it
	FillRatio float64 `json:"fill_ratio"` // 0..1 how much of the gap has been retraced
	SizeATR   float64 `json:"size_atr"`   // gap height / ATR14 — significance
}

// LiquidityPool marks a cluster of equal highs or equal lows. Equal highs sit
// above price and pool buy-stops (a magnet for upside stop-runs); equal lows
// pool sell-stops below. Smart-money models treat these as liquidity targets.
type LiquidityPool struct {
	Price       float64 `json:"price"`
	Type        string  `json:"type"` // "equal_highs" | "equal_lows"
	Touches     int     `json:"touches"`
	BarsAgo     int     `json:"bars_ago"`     // most recent touch
	StrengthATR float64 `json:"strength_atr"` // tightness of the cluster in ATR
}

// DetectFairValueGaps scans for unfilled (or partially filled) 3-candle FVGs.
// atr14 scales significance; gaps smaller than a fraction of ATR are noise and
// dropped. Returns the most significant recent gaps only. Callers tolerate nil.
func DetectFairValueGaps(klines []Kline, atr14, currentPrice float64) []FairValueGap {
	n := len(klines)
	if n < 5 || atr14 <= 0 {
		return nil
	}
	minGapATR := 0.15 // ignore gaps smaller than 15% of ATR — noise
	var gaps []FairValueGap

	for i := 2; i < n; i++ {
		c1 := klines[i-2]
		c3 := klines[i]
		barsAgo := n - 1 - i

		// Bullish FVG: candle1.high < candle3.low → gap [c1.High, c3.Low].
		if c1.High < c3.Low {
			gap := FairValueGap{
				Low:       c1.High,
				High:      c3.Low,
				Direction: "bullish",
				BarsAgo:   barsAgo,
			}
			finalizeGap(&gap, klines, i, atr14)
			if gap.SizeATR >= minGapATR {
				gaps = append(gaps, gap)
			}
		}

		// Bearish FVG: candle1.low > candle3.high → gap [c3.High, c1.Low].
		if c1.Low > c3.High {
			gap := FairValueGap{
				Low:       c3.High,
				High:      c1.Low,
				Direction: "bearish",
				BarsAgo:   barsAgo,
			}
			finalizeGap(&gap, klines, i, atr14)
			if gap.SizeATR >= minGapATR {
				gaps = append(gaps, gap)
			}
		}
	}

	// Prefer still-unfilled gaps and larger ones; keep the closest few to price.
	sort.Slice(gaps, func(a, b int) bool {
		if gaps[a].Filled != gaps[b].Filled {
			return !gaps[a].Filled // unfilled first
		}
		return math.Abs(gaps[a].Mid-currentPrice) < math.Abs(gaps[b].Mid-currentPrice)
	})
	if len(gaps) > 6 {
		gaps = gaps[:6]
	}
	return gaps
}

// finalizeGap computes mid, size, and fill state by scanning bars after the gap
// formed (indices formIdx+1..end) to see how far price retraced into it.
func finalizeGap(g *FairValueGap, klines []Kline, formIdx int, atr14 float64) {
	g.Mid = (g.Low + g.High) / 2
	height := g.High - g.Low
	if height <= 0 {
		g.SizeATR = 0
		return
	}
	g.SizeATR = height / atr14

	// How deep did later bars penetrate the gap?
	deepest := 0.0
	for i := formIdx + 1; i < len(klines); i++ {
		k := klines[i]
		// overlap of the bar's range with the gap
		lo := math.Max(g.Low, k.Low)
		hi := math.Min(g.High, k.High)
		if hi > lo {
			pen := (hi - lo) / height
			if pen > deepest {
				deepest = pen
			}
		}
	}
	g.FillRatio = math.Min(deepest, 1.0)
	g.Filled = g.FillRatio >= 0.9
}

// DetectLiquidityPools finds clusters of near-equal swing highs (buy-side
// liquidity) and swing lows (sell-side liquidity). Equal levels within a small
// ATR tolerance are pooled; pools with >= 2 touches are returned. Evidence only.
func DetectLiquidityPools(klines []Kline, atr14, currentPrice float64, timeframe string) []LiquidityPool {
	n := len(klines)
	if n < 20 || atr14 <= 0 {
		return nil
	}
	lookback := swingLookbackForTimeframe(timeframe)
	tolATR := 0.2 // equal within 20% of ATR
	tol := atr14 * tolATR

	pools := clusterEqualLevels(findSwingHighs(klines, lookback), klines, true, tol, n, atr14)
	pools = append(pools, clusterEqualLevels(findSwingLows(klines, lookback), klines, false, tol, n, atr14)...)

	// Sort by proximity to price, keep the closest few.
	sort.Slice(pools, func(a, b int) bool {
		return math.Abs(pools[a].Price-currentPrice) < math.Abs(pools[b].Price-currentPrice)
	})
	if len(pools) > 6 {
		pools = pools[:6]
	}
	return pools
}

// clusterEqualLevels groups swing indices whose prices sit within tol of each
// other into equal-high / equal-low pools. Requires >= 2 members to qualify.
func clusterEqualLevels(idxs []int, klines []Kline, isHigh bool, tol float64, n int, atr14 float64) []LiquidityPool {
	if len(idxs) < 2 {
		return nil
	}
	type pt struct {
		price float64
		idx   int
	}
	pts := make([]pt, 0, len(idxs))
	for _, i := range idxs {
		p := klines[i].Low
		if isHigh {
			p = klines[i].High
		}
		pts = append(pts, pt{p, i})
	}
	sort.Slice(pts, func(a, b int) bool { return pts[a].price < pts[b].price })

	var pools []LiquidityPool
	i := 0
	for i < len(pts) {
		j := i + 1
		sum := pts[i].price
		maxIdx := pts[i].idx
		lo, hi := pts[i].price, pts[i].price
		for j < len(pts) && pts[j].price-pts[i].price <= tol {
			sum += pts[j].price
			if pts[j].idx > maxIdx {
				maxIdx = pts[j].idx
			}
			if pts[j].price < lo {
				lo = pts[j].price
			}
			if pts[j].price > hi {
				hi = pts[j].price
			}
			j++
		}
		count := j - i
		if count >= 2 {
			typ := "equal_lows"
			if isHigh {
				typ = "equal_highs"
			}
			pools = append(pools, LiquidityPool{
				Price:       sum / float64(count),
				Type:        typ,
				Touches:     count,
				BarsAgo:     n - 1 - maxIdx,
				StrengthATR: (hi - lo) / atr14,
			})
		}
		i = j
	}
	return pools
}
