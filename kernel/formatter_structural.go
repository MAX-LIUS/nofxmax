package kernel

import (
	"fmt"
	"math"
	"nofx/market"
	"nofx/store"
	"sort"
	"strings"
)

// formatSentimentDataZH formats market sentiment data (Chinese)
func formatSentimentDataZH(mdata *market.Data, indicators ...store.IndicatorConfig) string {
	// Determine which sentiment fields to show based on indicator config
	showLS := true
	showTT := true
	showTBS := true
	showDepth := true
	if len(indicators) > 0 {
		ind := indicators[0]
		showLS = ind.EnableLongShortRatio
		showTT = ind.EnableTopTraderRatio
		showTBS = ind.EnableTakerBuySellRatio
		showDepth = ind.EnableOrderBookDepth
	}

	hasData := (showLS && mdata.LongShortRatio != nil) ||
		(showTT && mdata.TopTraderRatio != nil) ||
		(showTBS && mdata.TakerBuySellRatio != nil) ||
		(showDepth && mdata.DepthImbalance != nil)
	if !hasData {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("**市场情绪**:\n")

	if showLS && mdata.LongShortRatio != nil {
		bias := "多头偏多"
		if *mdata.LongShortRatio < 1 {
			bias = "空头偏多"
		}
		sb.WriteString(fmt.Sprintf("- 多空比: %.2f (%s)\n", *mdata.LongShortRatio, bias))
	}
	if showTT && mdata.TopTraderRatio != nil {
		bias := "大户偏多"
		if *mdata.TopTraderRatio < 1 {
			bias = "大户偏空"
		}
		sb.WriteString(fmt.Sprintf("- 大户多空比: %.2f (%s)\n", *mdata.TopTraderRatio, bias))
	}
	if showTBS && mdata.TakerBuySellRatio != nil {
		bias := "买方主导"
		if *mdata.TakerBuySellRatio < 1 {
			bias = "卖方主导"
		}
		sb.WriteString(fmt.Sprintf("- 主动买卖比: %.2f (%s)\n", *mdata.TakerBuySellRatio, bias))
	}
	if showDepth && mdata.DepthImbalance != nil {
		bias := "买盘偏重, 支撑倾向"
		if *mdata.DepthImbalance < 0 {
			bias = "卖盘偏重, 压力倾向"
		}
		sb.WriteString(fmt.Sprintf("- 深度失衡: %+.2f (%s)\n", *mdata.DepthImbalance, bias))
	}

	sb.WriteString("\n")
	return sb.String()
}

// formatSentimentDataEN formats market sentiment data (English)
func formatSentimentDataEN(mdata *market.Data, indicators ...store.IndicatorConfig) string {
	// Determine which sentiment fields to show based on indicator config
	showLS := true
	showTT := true
	showTBS := true
	showDepth := true
	if len(indicators) > 0 {
		ind := indicators[0]
		showLS = ind.EnableLongShortRatio
		showTT = ind.EnableTopTraderRatio
		showTBS = ind.EnableTakerBuySellRatio
		showDepth = ind.EnableOrderBookDepth
	}

	hasData := (showLS && mdata.LongShortRatio != nil) ||
		(showTT && mdata.TopTraderRatio != nil) ||
		(showTBS && mdata.TakerBuySellRatio != nil) ||
		(showDepth && mdata.DepthImbalance != nil)
	if !hasData {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("**Market Sentiment**:\n")

	if showLS && mdata.LongShortRatio != nil {
		bias := "more longs"
		if *mdata.LongShortRatio < 1 {
			bias = "more shorts"
		}
		sb.WriteString(fmt.Sprintf("- Long/Short Ratio: %.2f (%s)\n", *mdata.LongShortRatio, bias))
	}
	if showTT && mdata.TopTraderRatio != nil {
		bias := "top traders long-biased"
		if *mdata.TopTraderRatio < 1 {
			bias = "top traders short-biased"
		}
		sb.WriteString(fmt.Sprintf("- Top Trader L/S: %.2f (%s)\n", *mdata.TopTraderRatio, bias))
	}
	if showTBS && mdata.TakerBuySellRatio != nil {
		bias := "buyers dominant"
		if *mdata.TakerBuySellRatio < 1 {
			bias = "sellers dominant"
		}
		sb.WriteString(fmt.Sprintf("- Taker Buy/Sell: %.2f (%s)\n", *mdata.TakerBuySellRatio, bias))
	}
	if showDepth && mdata.DepthImbalance != nil {
		bias := "bid-heavy, support bias"
		if *mdata.DepthImbalance < 0 {
			bias = "ask-heavy, resistance bias"
		}
		sb.WriteString(fmt.Sprintf("- Depth Imbalance: %+.2f (%s)\n", *mdata.DepthImbalance, bias))
	}

	sb.WriteString("\n")
	return sb.String()
}

// formatStructuralLevelsZH formats structural levels (Chinese) — evaluated version
func formatStructuralLevelsZH(mdata *market.Data) string {
	return formatStructuralLevelsEvaluated(mdata, true)
}

// formatStructuralLevelsEN formats structural levels (English) — evaluated version
func formatStructuralLevelsEN(mdata *market.Data) string {
	return formatStructuralLevelsEvaluated(mdata, false)
}

// formatStructuralLevelsEvaluated formats structural levels grouped by trading usage.
// When zones are available, uses the new zone-based format (top 3 per direction).
func formatStructuralLevelsEvaluated(mdata *market.Data, zh bool) string {
	// Use zone-based format if zones are available
	if len(mdata.StructuralZones) > 0 {
		return formatStructuralZones(mdata, zh)
	}

	if len(mdata.StructuralLevels) == 0 && mdata.FibonacciLevels == nil {
		return ""
	}

	// Get ATR14 from primary timeframe data
	atr14 := extractPrimaryATR14(mdata)
	currentPrice := mdata.CurrentPrice

	// Combine structural levels with fibonacci extensions for uncharted territory
	allLevels := append([]market.StructuralLevel{}, mdata.StructuralLevels...)
	if mdata.FibonacciLevels != nil {
		extLevels := market.GenerateFibExtensionLevels(mdata.FibonacciLevels, currentPrice, mdata.FibonacciLevels.Timeframe)
		allLevels = append(allLevels, extLevels...)
	}

	// Evaluate levels for trading context (direction unknown at prompt time)
	evaluated := market.EvaluateForTrading(allLevels, currentPrice, atr14, "")
	groups := market.GroupByUsage(evaluated)

	var sb strings.Builder

	if zh {
		sb.WriteString("**关键结构性价位** (按交易用途分组评估):\n")
	} else {
		sb.WriteString("**Key Structural Levels** (grouped by trading usage):\n")
	}

	// ATR context line
	if atr14 > 0 {
		atrPct := (atr14 / currentPrice) * 100
		sb.WriteString(fmt.Sprintf("- context: current_price=%s atr14=%s (%.2f%%)\n\n",
			formatAIFloat(currentPrice), formatAIFloat(atr14), atrPct))
	}

	// SL Candidates
	slGroup := groups["sl_anchor"]
	if len(slGroup) > 0 {
		limit := 4
		if len(slGroup) < limit {
			limit = len(slGroup)
		}
		label := "[SL Candidates]"
		if zh {
			label = "[止损锚点候选]"
		}
		sb.WriteString(fmt.Sprintf("- %s:\n", label))
		for _, l := range slGroup[:limit] {
			sb.WriteString(formatEvaluatedLevelRow(l, zh))
		}
	}

	// TP Candidates
	tpGroup := groups["tp_target"]
	if len(tpGroup) > 0 {
		limit := 6
		if len(tpGroup) < limit {
			limit = len(tpGroup)
		}
		label := "[TP Candidates]"
		if zh {
			label = "[止盈目标候选]"
		}
		sb.WriteString(fmt.Sprintf("- %s:\n", label))
		for _, l := range tpGroup[:limit] {
			sb.WriteString(formatEvaluatedLevelRow(l, zh))
		}
	}

	// Entry Triggers
	entryGroup := groups["entry_trigger"]
	if len(entryGroup) > 0 {
		limit := 2
		if len(entryGroup) < limit {
			limit = len(entryGroup)
		}
		label := "[Entry Triggers]"
		if zh {
			label = "[入场触发位]"
		}
		sb.WriteString(fmt.Sprintf("- %s:\n", label))
		for _, l := range entryGroup[:limit] {
			sb.WriteString(formatEvaluatedLevelRow(l, zh))
		}
	}

	// Context Only (max 3, only if there are few actionable levels)
	ctxGroup := groups["context_only"]
	if len(ctxGroup) > 0 && (len(slGroup)+len(tpGroup)+len(entryGroup)) < 4 {
		limit := 3
		if len(ctxGroup) < limit {
			limit = len(ctxGroup)
		}
		label := "[Context Only]"
		if zh {
			label = "[仅供参考]"
		}
		sb.WriteString(fmt.Sprintf("- %s:\n", label))
		for _, l := range ctxGroup[:limit] {
			sb.WriteString(formatEvaluatedLevelRow(l, zh))
		}
	}

	// Fibonacci context
	if mdata.FibonacciLevels != nil {
		fib := mdata.FibonacciLevels
		dir := fib.Direction
		if zh {
			dir = "回撤向下"
			if fib.Direction == "retracement_up" {
				dir = "回撤向上"
			}
		}
		sb.WriteString(fmt.Sprintf("- fibonacci_context: timeframe=%s swing_low=%s swing_high=%s direction=%s\n",
			fib.Timeframe, formatAIFloat(fib.SwingLow), formatAIFloat(fib.SwingHigh), dir))
		keys := sortedFibKeys(fib.Levels)
		for _, k := range keys {
			sb.WriteString(fmt.Sprintf("  - fib_%s=%s\n", k, formatAIFloat(fib.Levels[k])))
		}
	}

	// Quality advisory — only when NO candidates at all (not just missing high quality)
	hasSL := market.HasHighQualitySLCandidates(evaluated)
	hasTP := market.HasHighQualityTPCandidates(evaluated)
	if !hasSL || !hasTP {
		sb.WriteString("\n")
		if zh {
			if !hasSL && !hasTP {
				sb.WriteString("📐 结构位稀疏区域 — 可使用 ATR-based 止损 (1.5-2x ATR) 和 fibonacci extension 目标。basis_type 标注为 \"atr_based\"。仍需确保 RR 达标且入场有结构依托。\n")
			} else if !hasSL {
				sb.WriteString("📐 止损方向无近距结构位 — 可使用 ATR-based 止损 (1.5-2x ATR from entry)，basis_type 标注为 \"atr_based\"。确保止损距离合理且 RR 达标。\n")
			} else {
				sb.WriteString("📐 止盈方向结构目标有限 — 可使用 fibonacci extension 或 ATR-based 目标，basis_type 标注为 \"atr_based\" 或 \"fibonacci\"。确保目标距离满足 RR 要求。\n")
			}
		} else {
			if !hasSL && !hasTP {
				sb.WriteString("📐 Sparse structure zone — use ATR-based stop (1.5-2x ATR) and fibonacci extension targets. Mark basis_type as \"atr_based\". Still require valid RR and structural entry justification.\n")
			} else if !hasSL {
				sb.WriteString("📐 No nearby SL structure — use ATR-based stop (1.5-2x ATR from entry), mark basis_type as \"atr_based\". Ensure stop distance is reasonable and RR meets threshold.\n")
			} else {
				sb.WriteString("📐 Limited TP structure — use fibonacci extension or ATR-based target, mark basis_type as \"atr_based\" or \"fibonacci\". Ensure target distance satisfies RR requirement.\n")
			}
		}
	}

	sb.WriteString("\n")
	return sb.String()
}

// formatStructuralZones formats zones in the new compact zone-based format for AI.
func formatStructuralZones(mdata *market.Data, zh bool) string {
	currentPrice := mdata.CurrentPrice
	atr14 := extractPrimaryATR14(mdata)

	zones := market.FilterTopZonesForAI(mdata.StructuralZones, currentPrice, 3)

	var support, resistance []market.StructuralZone
	for _, z := range zones {
		if z.Type == "support" {
			support = append(support, z)
		} else {
			resistance = append(resistance, z)
		}
	}

	var sb strings.Builder
	if zh {
		sb.WriteString("**关键结构区间** (每方向 top 3):\n")
	} else {
		sb.WriteString("**Key Structural Zones** (top 3 per direction):\n")
	}

	if atr14 > 0 {
		atrPct := (atr14 / currentPrice) * 100
		sb.WriteString(fmt.Sprintf("- context: current_price=%s atr14=%s (%.2f%%)\n\n",
			formatAIFloat(currentPrice), formatAIFloat(atr14), atrPct))
	}

	// Resistance zones
	if len(resistance) > 0 {
		if zh {
			sb.WriteString("阻力区间 (价格上方):\n")
		} else {
			sb.WriteString("RESISTANCE ZONES (above price):\n")
		}
		for i, z := range resistance {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, formatZoneRow(z, atr14, currentPrice, zh)))
		}
	}

	// Support zones
	if len(support) > 0 {
		if zh {
			sb.WriteString("支撑区间 (价格下方):\n")
		} else {
			sb.WriteString("SUPPORT ZONES (below price):\n")
		}
		for i, z := range support {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, formatZoneRow(z, atr14, currentPrice, zh)))
		}
	}

	// Nearest zone summary
	nearest := findNearestZone(zones, currentPrice)
	if nearest != nil {
		dist := ""
		if atr14 > 0 {
			atrDist := math.Abs(nearest.MidPrice-currentPrice) / atr14
			pos := "below"
			if nearest.MidPrice > currentPrice {
				pos = "above"
			}
			if zh {
				pos = "下方"
				if nearest.MidPrice > currentPrice {
					pos = "上方"
				}
			}
			dist = fmt.Sprintf("at %.1fx ATR %s", atrDist, pos)
		}
		if zh {
			sb.WriteString(fmt.Sprintf("\n最近区间: %s [%s – %s] %s\n",
				nearest.Type, formatAIFloat(nearest.Low), formatAIFloat(nearest.High), dist))
		} else {
			sb.WriteString(fmt.Sprintf("\nNEAREST ZONE: %s [%s – %s] %s\n",
				nearest.Type, formatAIFloat(nearest.Low), formatAIFloat(nearest.High), dist))
		}
	}

	// Fibonacci context (still useful)
	if mdata.FibonacciLevels != nil {
		fib := mdata.FibonacciLevels
		dir := fib.Direction
		if zh {
			dir = "回撤向下"
			if fib.Direction == "retracement_up" {
				dir = "回撤向上"
			}
		}
		sb.WriteString(fmt.Sprintf("- fibonacci_context: timeframe=%s swing_low=%s swing_high=%s direction=%s\n",
			fib.Timeframe, formatAIFloat(fib.SwingLow), formatAIFloat(fib.SwingHigh), dir))
	}

	// Volume profile — where price was ACCEPTED (POC/value area/HVN) vs REJECTED (LVN).
	// This is evidence to refine confidence and target/invalidation choice, not a hard rule.
	sb.WriteString(formatVolumeProfile(mdata, currentPrice, zh))

	// Anchored VWAP — fair value since the last significant swing; tells whether
	// participants since that pivot are net long/short in profit. Evidence only.
	sb.WriteString(formatAnchoredVWAPs(mdata, currentPrice, zh))

	// Prev day/week high-low — heavily-watched liquidity references. Breaks and
	// rejections here are meaningful, but they are evidence, not a hard rule.
	sb.WriteString(formatPeriodLevels(mdata, currentPrice, zh))

	// FVG imbalances (unfilled gaps act as magnets) + equal-high/low liquidity
	// pools (stop-run targets). Evidence for target/entry timing, not gates.
	sb.WriteString(formatFVGAndLiquidity(mdata, currentPrice, zh))

	// BOS/CHoCH structure breaks + supply/demand order blocks. A recent CHoCH
	// warns of a trend flip; a BOS confirms continuation; the broken level and
	// order block are retest zones. Evidence for bias/timing, not hard gates.
	sb.WriteString(formatStructureBreaks(mdata, currentPrice, zh))

	sb.WriteString("\n")
	return sb.String()
}

// formatStructureBreaks renders recent Break-of-Structure / Change-of-Character
// events and the supply/demand order blocks that spawned them. Kept short: the
// few most recent, framed as continuation vs reversal evidence.
func formatStructureBreaks(mdata *market.Data, currentPrice float64, zh bool) string {
	var sb strings.Builder

	if len(mdata.StructureBreaks) > 0 {
		if zh {
			sb.WriteString("\n**结构突破(BOS/CHoCH)** (趋势延续 vs 转变的证据, 非硬性规则):\n")
		} else {
			sb.WriteString("\n**Structure Breaks (BOS/CHoCH)** (continuation vs reversal evidence — not a hard rule):\n")
		}
		shown := 0
		for _, b := range mdata.StructureBreaks {
			if shown >= 3 {
				break
			}
			shown++
			dir := translateBreakDir(b.Direction, zh)
			retest := ""
			if b.Retested {
				if zh {
					retest = ", 已回踩"
				} else {
					retest = ", retested"
				}
			}
			if zh {
				fmt.Fprintf(&sb, "- %s %s @ %s (回踩区 %s-%s, %d根前, %.1fxATR%s)\n",
					b.Type, dir, formatAIFloat(b.BreakLevel), formatAIFloat(b.RetestLow), formatAIFloat(b.RetestHigh), b.BarsAgo, b.SizeATR, retest)
			} else {
				fmt.Fprintf(&sb, "- %s %s @ %s (retest zone %s-%s, %d bars ago, %.1fxATR%s)\n",
					b.Type, dir, formatAIFloat(b.BreakLevel), formatAIFloat(b.RetestLow), formatAIFloat(b.RetestHigh), b.BarsAgo, b.SizeATR, retest)
			}
		}
	}

	if len(mdata.OrderBlocks) > 0 {
		if zh {
			sb.WriteString("**供需区(Order Block)** (回踩常见反应位, 非硬性规则):\n")
		} else {
			sb.WriteString("**Supply/Demand Order Blocks** (common reaction zones on retest — not a hard rule):\n")
		}
		shown := 0
		for _, ob := range mdata.OrderBlocks {
			if shown >= 3 {
				break
			}
			shown++
			role := translateOrderBlock(ob.Direction, zh)
			var state string
			if ob.Mitigated {
				if zh {
					state = ", 已触及"
				} else {
					state = ", mitigated"
				}
			} else {
				if zh {
					state = ", 未触及"
				} else {
					state = ", fresh"
				}
			}
			if zh {
				fmt.Fprintf(&sb, "- %s区 %s-%s (%d根前, 源自 %.1fxATR 冲量%s)\n",
					role, formatAIFloat(ob.Low), formatAIFloat(ob.High), ob.BarsAgo, ob.SizeATR, state)
			} else {
				fmt.Fprintf(&sb, "- %s zone %s-%s (%d bars ago, from %.1fxATR impulse%s)\n",
					role, formatAIFloat(ob.Low), formatAIFloat(ob.High), ob.BarsAgo, ob.SizeATR, state)
			}
		}
	}

	return sb.String()
}

// translateBreakDir renders bullish/bearish direction.
func translateBreakDir(dir string, zh bool) string {
	if zh {
		switch dir {
		case "bullish":
			return "向上"
		case "bearish":
			return "向下"
		}
		return dir
	}
	return dir
}

// translateOrderBlock renders demand/supply role.
func translateOrderBlock(dir string, zh bool) string {
	if zh {
		switch dir {
		case "demand":
			return "需求"
		case "supply":
			return "供给"
		}
		return dir
	}
	switch dir {
	case "demand":
		return "Demand"
	case "supply":
		return "Supply"
	}
	return dir
}

// formatFVGAndLiquidity renders the nearest unfilled fair-value gaps and the
// nearest equal-high/low liquidity pools. Kept short: only the closest few, and
// FVGs are labelled with fill state so the AI can weigh how fresh they are.
func formatFVGAndLiquidity(mdata *market.Data, currentPrice float64, zh bool) string {
	var sb strings.Builder

	// Fair value gaps — prefer unfilled, show at most 3 nearest.
	if len(mdata.FairValueGaps) > 0 {
		shown := 0
		var buf strings.Builder
		for _, g := range mdata.FairValueGaps {
			if g.Filled {
				continue // only surface still-actionable gaps
			}
			dir := g.Direction
			if zh {
				dir = "看涨缺口(下方易成支撑)"
				if g.Direction == "bearish" {
					dir = "看跌缺口(上方易成阻力)"
				}
			}
			if zh {
				buf.WriteString(fmt.Sprintf("- %s [%s ~ %s], %.0f%%回补, %.1fxATR, %d根前\n",
					dir, formatAIFloat(g.Low), formatAIFloat(g.High), g.FillRatio*100, g.SizeATR, g.BarsAgo))
			} else {
				buf.WriteString(fmt.Sprintf("- %s FVG [%s ~ %s], %.0f%% filled, %.1fxATR, %d bars ago\n",
					g.Direction, formatAIFloat(g.Low), formatAIFloat(g.High), g.FillRatio*100, g.SizeATR, g.BarsAgo))
			}
			shown++
			if shown >= 3 {
				break
			}
		}
		if shown > 0 {
			if zh {
				sb.WriteString("\n**未回补缺口(FVG)** (价格常回补, 参考证据):\n")
			} else {
				sb.WriteString("\n**Unfilled FVGs** (price tends to fill — evidence):\n")
			}
			sb.WriteString(buf.String())
		}
	}

	// Liquidity pools — nearest 3.
	if len(mdata.LiquidityPools) > 0 {
		if zh {
			sb.WriteString("\n**流动性池(等高/等低)** (止损聚集, 常成扫单目标):\n")
		} else {
			sb.WriteString("\n**Liquidity Pools (equal H/L)** (stop clusters, sweep targets):\n")
		}
		for i, p := range mdata.LiquidityPools {
			if i >= 3 {
				break
			}
			typ := p.Type
			if zh {
				typ = "等高(上方买方止损)"
				if p.Type == "equal_lows" {
					typ = "等低(下方卖方止损)"
				}
			}
			if zh {
				sb.WriteString(fmt.Sprintf("- %s @ %s, %d次触及, %d根前\n",
					typ, formatAIFloat(p.Price), p.Touches, p.BarsAgo))
			} else {
				sb.WriteString(fmt.Sprintf("- %s @ %s, %d touches, %d bars ago\n",
					typ, formatAIFloat(p.Price), p.Touches, p.BarsAgo))
			}
		}
	}

	return sb.String()
}

// formatPeriodLevels renders previous-day / previous-week high-low plus the
// developing current-day extremes. These are the most-watched liquidity refs
// across markets and often act as magnets or breakout triggers.
func formatPeriodLevels(mdata *market.Data, currentPrice float64, zh bool) string {
	pl := mdata.PeriodLevels
	if pl == nil {
		return ""
	}
	var sb strings.Builder
	if zh {
		sb.WriteString("\n**周期关键位** (前日/前周高低点, 流动性参考证据):\n")
	} else {
		sb.WriteString("\n**Period Levels** (prev day/week high-low — liquidity reference):\n")
	}
	// Filter out period levels too far from price to be actionable — they add
	// noise, not signal. ~8x ATR is well beyond any reasonable trade horizon.
	atr14 := extractPrimaryATR14(mdata)
	maxDist := currentPrice * 0.10 // fallback: 10% when ATR unavailable
	if atr14 > 0 {
		maxDist = atr14 * 8
	}
	row := func(labelZH, labelEN string, v float64) {
		if v <= 0 {
			return
		}
		if math.Abs(v-currentPrice) > maxDist {
			return
		}
		side := "above"
		if currentPrice >= v {
			side = "below"
		}
		if zh {
			sb.WriteString(fmt.Sprintf("- %s=%s (%s当前)\n", labelZH, formatAIFloat(v), translatePocSide(side, true)))
		} else {
			sb.WriteString(fmt.Sprintf("- %s=%s (%s current)\n", labelEN, formatAIFloat(v), side))
		}
	}
	row("前日高", "prev_day_high", pl.PrevDayHigh)
	row("前日低", "prev_day_low", pl.PrevDayLow)
	row("前周高", "prev_week_high", pl.PrevWeekHigh)
	row("前周低", "prev_week_low", pl.PrevWeekLow)
	row("今日高", "curr_day_high", pl.CurrDayHigh)
	row("今日低", "curr_day_low", pl.CurrDayLow)
	return sb.String()
}

// formatAnchoredVWAPs renders anchored VWAPs (from recent swing high/low) as a
// fair-value reference. Price above an anchored VWAP means buyers since that
// pivot are in profit (bullish acceptance); below means sellers dominate.
func formatAnchoredVWAPs(mdata *market.Data, currentPrice float64, zh bool) string {
	if len(mdata.AnchoredVWAPs) == 0 {
		return ""
	}
	var sb strings.Builder
	if zh {
		sb.WriteString("\n**锚定 VWAP** (自最近摆动点的公允价, 参考证据):\n")
	} else {
		sb.WriteString("\n**Anchored VWAP** (fair value since last swing — evidence):\n")
	}
	for _, v := range mdata.AnchoredVWAPs {
		side := "above"
		if currentPrice < v.VWAP {
			side = "below"
		}
		anchorLabel := translateAnchor(v.Anchor, zh)
		if zh {
			sb.WriteString(fmt.Sprintf("- 锚点=%s (%d根前): VWAP=%s [%s ~ %s], 当前价%s\n",
				anchorLabel, v.AnchorBars, formatAIFloat(v.VWAP),
				formatAIFloat(v.LowerBand), formatAIFloat(v.UpperBand), translatePocSide(side, true)))
		} else {
			sb.WriteString(fmt.Sprintf("- anchor=%s (%d bars ago): VWAP=%s [%s ~ %s], price %s\n",
				anchorLabel, v.AnchorBars, formatAIFloat(v.VWAP),
				formatAIFloat(v.LowerBand), formatAIFloat(v.UpperBand), side))
		}
	}
	return sb.String()
}

func translateAnchor(anchor string, zh bool) string {
	if !zh {
		return anchor
	}
	switch anchor {
	case "swing_high":
		return "摆动高点"
	case "swing_low":
		return "摆动低点"
	case "window_start":
		return "窗口起点"
	default:
		return anchor
	}
}

// formatVolumeProfile renders the primary-timeframe volume profile for the AI.
// It is intentionally framed as acceptance/rejection evidence: POC and value
// area mark fair-value / mean-reversion magnets; LVNs mark thin zones price
// tends to travel through quickly (good for targets, poor for resting stops).
func formatVolumeProfile(mdata *market.Data, currentPrice float64, zh bool) string {
	vp := mdata.VolumeProfile
	if vp == nil || vp.POC <= 0 {
		return ""
	}
	pocSide := "at"
	if vp.POC > currentPrice {
		pocSide = "above"
	} else if vp.POC < currentPrice {
		pocSide = "below"
	}
	var sb strings.Builder
	if zh {
		sb.WriteString("\n**成交量分布** (价格被接受/拒绝的区域, 参考证据非硬性规则):\n")
		sb.WriteString(fmt.Sprintf("- POC(最大成交价)=%s (%s当前价), 价值区 VAL=%s ~ VAH=%s\n",
			formatAIFloat(vp.POC), translatePocSide(pocSide, true),
			formatAIFloat(vp.VAL), formatAIFloat(vp.VAH)))
		if len(vp.HVNs) > 0 {
			sb.WriteString(fmt.Sprintf("- HVN(高成交/接受区, 易成支撑阻力): %s\n", formatFloatList(vp.HVNs)))
		}
		if len(vp.LVNs) > 0 {
			sb.WriteString(fmt.Sprintf("- LVN(低成交/拒绝区, 价格易快速穿越, 适合作目标不适合放止损): %s\n", formatFloatList(vp.LVNs)))
		}
	} else {
		sb.WriteString("\n**Volume Profile** (where price was accepted/rejected — evidence, not a hard rule):\n")
		sb.WriteString(fmt.Sprintf("- POC=%s (%s current), value area VAL=%s ~ VAH=%s\n",
			formatAIFloat(vp.POC), pocSide, formatAIFloat(vp.VAL), formatAIFloat(vp.VAH)))
		if len(vp.HVNs) > 0 {
			sb.WriteString(fmt.Sprintf("- HVN (acceptance shelves, tend to act as S/R): %s\n", formatFloatList(vp.HVNs)))
		}
		if len(vp.LVNs) > 0 {
			sb.WriteString(fmt.Sprintf("- LVN (rejection gaps, price travels fast — good for targets, poor for resting stops): %s\n", formatFloatList(vp.LVNs)))
		}
	}
	return sb.String()
}

func translatePocSide(side string, zh bool) string {
	if !zh {
		return side
	}
	switch side {
	case "above":
		return "高于"
	case "below":
		return "低于"
	default:
		return "接近"
	}
}

func formatFloatList(vals []float64) string {
	parts := make([]string, 0, len(vals))
	for _, v := range vals {
		parts = append(parts, formatAIFloat(v))
	}
	return strings.Join(parts, ", ")
}

func formatZoneRow(z market.StructuralZone, atr14, currentPrice float64, zh bool) string {
	tfs := strings.Join(z.Timeframes, "+")
	sources := strings.Join(z.Sources, "+")

	atrDist := ""
	if atr14 > 0 {
		dist := math.Abs(z.MidPrice-currentPrice) / atr14
		atrDist = fmt.Sprintf(", %.1fx ATR", dist)
	}

	extra := ""
	if z.TouchCount > 1 {
		if zh {
			extra += fmt.Sprintf(", %d次触及", z.TouchCount)
		} else {
			extra += fmt.Sprintf(", %d touches", z.TouchCount)
		}
	}
	// Phase 2: lifecycle state + behavioural role (evidence for the AI).
	if z.State != "" {
		extra += ", " + translateZoneState(z.State, zh)
	}
	if z.Role != "" {
		extra += "/" + translateZoneRole(z.Role, zh)
	}
	if z.MaxReactionATR > 0 {
		if zh {
			extra += fmt.Sprintf(", 反应%.1fxATR", z.MaxReactionATR)
		} else {
			extra += fmt.Sprintf(", reaction %.1fxATR", z.MaxReactionATR)
		}
	}
	// Only show flipped when the state machine didn't already surface it.
	if z.Flipped && z.State != "flipped" {
		if zh {
			extra += ", 已翻转"
		} else {
			extra += ", flipped"
		}
	}

	return fmt.Sprintf("[%s] %s – %s (%s, %s, conf=%.0f%s%s)",
		z.QualityGrade, formatAIFloat(z.Low), formatAIFloat(z.High),
		tfs, sources, z.Confidence, atrDist, extra)
}

// translateZoneState maps a lifecycle state to a bilingual label.
func translateZoneState(state string, zh bool) string {
	if !zh {
		return state
	}
	switch state {
	case "fresh":
		return "未测试"
	case "first_test":
		return "首次测试"
	case "reacted":
		return "强反应"
	case "retested":
		return "多次守住"
	case "weakened":
		return "走弱"
	case "broken":
		return "已突破"
	case "flipped":
		return "已翻转"
	case "invalid":
		return "已失效"
	}
	return state
}

// translateZoneRole maps a behavioural role to a bilingual label.
func translateZoneRole(role string, zh bool) string {
	if !zh {
		return role
	}
	switch role {
	case "reversal":
		return "反转位"
	case "continuation":
		return "延续位"
	case "acceptance":
		return "接受区"
	case "acceleration_boundary":
		return "加速边界"
	case "liquidity_target":
		return "流动性目标"
	}
	return role
}

func findNearestZone(zones []market.StructuralZone, currentPrice float64) *market.StructuralZone {
	if len(zones) == 0 {
		return nil
	}
	nearest := &zones[0]
	minDist := math.Abs(zones[0].MidPrice - currentPrice)
	for i := 1; i < len(zones); i++ {
		d := math.Abs(zones[i].MidPrice - currentPrice)
		if d < minDist {
			minDist = d
			nearest = &zones[i]
		}
	}
	return nearest
}

func formatEvaluatedLevelRow(l market.EvaluatedLevel, zh bool) string {
	source := l.Source
	if zh {
		source = translateSource(l.Source, true)
	}
	return fmt.Sprintf("  - price=%s tf=%s source=%s conf=%.0f atr_dist=%.1f quality=%s",
		formatAIFloat(l.Price), l.Timeframe, source, l.Confidence, l.ATRDistance, l.QualityGrade) +
		formatEvaluatedLevelExtra(l) + "\n"
}

func formatEvaluatedLevelExtra(l market.EvaluatedLevel) string {
	var parts []string
	if l.MultiTFCount > 0 {
		parts = append(parts, fmt.Sprintf("mtf=%d", l.MultiTFCount))
	}
	if l.TouchCount > 1 {
		parts = append(parts, fmt.Sprintf("touches=%d", l.TouchCount))
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, " ")
}

// extractPrimaryATR14 gets ATR14 from the best available timeframe in market data
func extractPrimaryATR14(mdata *market.Data) float64 {
	if mdata.TimeframeData == nil {
		return 0
	}
	// Prefer 15m > 5m > 1h as primary ATR reference
	for _, tf := range []string{"1h", "15m", "4h", "30m", "5m"} {
		if series, ok := mdata.TimeframeData[tf]; ok && series.ATR14 > 0 {
			return series.ATR14
		}
	}
	return 0
}

func formatAIFloat(v float64) string {
	s := fmt.Sprintf("%.8f", v)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "-0" || s == "" {
		return "0"
	}
	return s
}

func formatAISignedFloat(v float64) string {
	if v > 0 {
		return "+" + formatAIFloat(v)
	}
	return formatAIFloat(v)
}

func sortedFibKeys(levels map[string]float64) []string {
	keys := make([]string, 0, len(levels))
	for k := range levels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func translateSource(source string, zh bool) string {
	if !zh {
		return source
	}
	switch source {
	case "swing_point":
		return "波段点"
	case "volume_cluster":
		return "成交量集中区"
	case "fibonacci":
		return "斐波那契"
	default:
		return source
	}
}
