package backtest

import (
	"fmt"
	"sort"
	"strings"
)

// StructCompareRow is one scheme's result in the structural comparison.
type StructCompareRow struct {
	Name         string
	Trades       int
	TotalPnL     float64
	WinRatePct   float64
	ProfitFactor float64
	MaxDrawdown  float64
	AvgReturnPct float64
	// Diagnostics targeting the two failure modes the user named:
	StopOutRate   float64 // % of trades that closed via stop_loss (wick-out proxy)
	AvgWinCapture float64 // avg ReturnPct of winning trades (TP-scalping proxy)
	AvgLossSize   float64 // avg ReturnPct of losing trades (bleed proxy)
}

// schemeFor builds a ProtectionParams for a named scheme, given a base config
// for BE/DD (kept constant across schemes so SL/TP differences are isolated).
func structSchemeParams(name string, base ProtectionParams) ProtectionParams {
	p := base
	switch {
	case name == "percent-5%SL":
		p.Unit = UnitPercent
		p.StopLossPct = 5
		p.TPLegs = []LadderLeg{{DistPct: 3, CloseRatioPct: 35}, {DistPct: 6, CloseRatioPct: 25}}
	case name == "atr-wide-2tp":
		p.Unit = UnitATRMult
		p.StopLossATR = 4.5
		p.TPLegs = []LadderLeg{{ATRMult: 2.0, CloseRatioPct: 35}, {ATRMult: 7.0, CloseRatioPct: 25}}
	case name == "structural-AIbuffer":
		// Variant A: AI's own buffered SL price + AI structural TP ladder.
		p.Unit = UnitStructural
		p.StructBufferATR = 0
		p.StructMinSLPct = 0.4 // guardrail: not absurdly tight
		p.StructMaxSLPct = 8.0 // guardrail: not absurdly wide
	case strings.HasPrefix(name, "structural-buf"):
		// Variant B: re-derive SL from bare anchor as anchor ± k×ATR.
		p.Unit = UnitStructural
		p.StructMinSLPct = 0.4
		p.StructMaxSLPct = 8.0
	case name == "hybrid-structTP-atrSL4.5":
		// Hybrid: structural TP ladder + ATR-wide (4.5×ATR) SL.
		p.Unit = UnitStructural
		p.StructUseATRSL = true
		p.StopLossATR = 4.5
	case name == "hybrid-structTP-atrSL3.0":
		p.Unit = UnitStructural
		p.StructUseATRSL = true
		p.StopLossATR = 3.0
	}
	return p
}

// runScheme replays a scheme over loaded entries and computes diagnostics.
func runStructScheme(name string, p ProtectionParams, loaded []loadedEntry) StructCompareRow {
	results := make([]TradeResult, 0, len(loaded))
	for _, le := range loaded {
		results = append(results, ReplayEntry(p, le.entry, le.bars, le.entryIdx))
	}
	pr := Aggregate(results)
	row := StructCompareRow{
		Name:         name,
		Trades:       pr.Trades,
		TotalPnL:     pr.TotalPnL,
		WinRatePct:   pr.WinRatePct,
		ProfitFactor: pr.ProfitFactor,
		MaxDrawdown:  pr.MaxDrawdown,
		AvgReturnPct: pr.AvgReturnPct,
	}
	stopOuts, winN, lossN := 0, 0, 0
	var winSum, lossSum float64
	for _, r := range results {
		for _, cr := range r.CloseReasons {
			if cr == "stop_loss" {
				stopOuts++
				break
			}
		}
		if r.RealizedPnL >= 0 {
			winN++
			winSum += r.ReturnPct
		} else {
			lossN++
			lossSum += r.ReturnPct
		}
	}
	if pr.Trades > 0 {
		row.StopOutRate = float64(stopOuts) / float64(pr.Trades) * 100
	}
	if winN > 0 {
		row.AvgWinCapture = winSum / float64(winN)
	}
	if lossN > 0 {
		row.AvgLossSize = lossSum / float64(lossN)
	}
	return row
}

// CompareStructural replays the percent baseline, the live ATR scheme, the
// AI-buffer structural variant, a sweep of config-buffer structural variants, and
// structural-TP+ATR-SL hybrids over the SAME structurally-matched entries.
// bufferSweep are k values (×ATR) for Variant B. testTailFrac in (0,1) restricts
// scoring to the chronologically-last fraction (out-of-sample tail); 0 = use all.
// Only entries with a structural plan are used. Returns rows + matched-entry count.
func CompareStructural(loaded []loadedEntry, base ProtectionParams, bufferSweep []float64, testTailFrac float64) ([]StructCompareRow, int) {
	// Filter to entries that actually carry a structural plan.
	var withStruct []loadedEntry
	for _, le := range loaded {
		if le.entry.Structural != nil {
			withStruct = append(withStruct, le)
		}
	}
	// Out-of-sample tail: keep only the most recent testTailFrac of entries.
	if testTailFrac > 0 && testTailFrac < 1 && len(withStruct) > 0 {
		sortLoadedByEntryTime(withStruct)
		cut := int(float64(len(withStruct)) * (1 - testTailFrac))
		if cut < 0 {
			cut = 0
		}
		if cut < len(withStruct) {
			withStruct = withStruct[cut:]
		}
	}
	rows := []StructCompareRow{
		runStructScheme("percent-5%SL", structSchemeParams("percent-5%SL", base), withStruct),
		runStructScheme("atr-wide-2tp", structSchemeParams("atr-wide-2tp", base), withStruct),
		runStructScheme("structural-AIbuffer", structSchemeParams("structural-AIbuffer", base), withStruct),
	}
	sort.Float64s(bufferSweep)
	for _, k := range bufferSweep {
		name := fmt.Sprintf("structural-buf%.1fATR", k)
		p := structSchemeParams(name, base)
		p.StructBufferATR = k
		rows = append(rows, runStructScheme(name, p, withStruct))
	}
	// Hybrid schemes: structural TP + ATR-wide SL.
	for _, name := range []string{"hybrid-structTP-atrSL3.0", "hybrid-structTP-atrSL4.5"} {
		rows = append(rows, runStructScheme(name, structSchemeParams(name, base), withStruct))
	}
	return rows, len(withStruct)
}

// FormatStructCompare renders the comparison as an aligned table.
func FormatStructCompare(rows []StructCompareRow, matched int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "STRUCTURAL PROTECTION COMPARISON (matched entries=%d)\n", matched)
	fmt.Fprintf(&b, "%-22s %6s %9s %7s %6s %8s %9s %9s %9s\n",
		"scheme", "trades", "totPnL", "win%", "PF", "maxDD", "stopOut%", "avgWin%", "avgLoss%")
	for _, r := range rows {
		fmt.Fprintf(&b, "%-22s %6d %9.2f %7.1f %6.2f %8.2f %9.1f %9.2f %9.2f\n",
			r.Name, r.Trades, r.TotalPnL, r.WinRatePct, r.ProfitFactor, r.MaxDrawdown,
			r.StopOutRate, r.AvgWinCapture, r.AvgLossSize)
	}
	return b.String()
}

