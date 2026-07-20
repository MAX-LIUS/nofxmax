package backtest

import (
	"fmt"
	"sort"
	"strings"
)

// entry_quality.go answers "is the ENTRY itself bad?" independent of stop
// placement. For each entry it measures, over a fixed post-entry window, how far
// price runs AGAINST the entry (MAE) and FOR it (MFE) in ATR units, plus whether
// the trade ever goes favorable at all and how quickly it goes underwater. Poor
// entries show large MAE, small MFE, and immediate adverse drift regardless of
// where the stop sits.

// EntryQuality holds one entry's excursion profile.
type EntryQuality struct {
	Symbol    string
	Side      string
	MAEatr    float64 // max adverse excursion in ATR (>=0)
	MFEatr    float64 // max favorable excursion in ATR (>=0)
	FirstMove float64 // signed ATR move of the FIRST bar after entry (+ = favorable)
	BarsToMFE int     // bars until the favorable peak
	EverGreen bool    // did it ever trade favorable beyond 0.1 ATR?
	Immediate bool    // did it go >0.5 ATR adverse before any >0.5 ATR favorable?
}

// windowBars is the post-entry horizon used to profile the entry, in bars.
const eqWindowBars = 24

// AnalyzeEntryQuality computes the excursion profile for every loaded entry.
func AnalyzeEntryQuality(loaded []loadedEntry) []EntryQuality {
	out := make([]EntryQuality, 0, len(loaded))
	for _, le := range loaded {
		atr := entryATR(le.bars, le.entryIdx)
		if atr <= 0 || le.entry.EntryPrice <= 0 {
			continue
		}
		isLong := strings.EqualFold(le.entry.Side, "LONG")
		entry := le.entry.EntryPrice
		eq := EntryQuality{Symbol: le.entry.Symbol, Side: le.entry.Side}
		end := le.entryIdx + eqWindowBars
		if end >= len(le.bars) {
			end = len(le.bars) - 1
		}
		firstFav, firstAdv := -1, -1
		for i := le.entryIdx; i <= end; i++ {
			b := le.bars[i]
			var favExc, advExc float64
			if isLong {
				favExc = (b.High - entry) / atr
				advExc = (entry - b.Low) / atr
			} else {
				favExc = (entry - b.Low) / atr
				advExc = (b.High - entry) / atr
			}
			if favExc > eq.MFEatr {
				eq.MFEatr = favExc
				eq.BarsToMFE = i - le.entryIdx
			}
			if advExc > eq.MAEatr {
				eq.MAEatr = advExc
			}
			if favExc > 0.5 && firstFav < 0 {
				firstFav = i
			}
			if advExc > 0.5 && firstAdv < 0 {
				firstAdv = i
			}
			if i == le.entryIdx {
				// first-bar signed move using close
				if isLong {
					eq.FirstMove = (b.Close - entry) / atr
				} else {
					eq.FirstMove = (entry - b.Close) / atr
				}
			}
		}
		eq.EverGreen = eq.MFEatr > 0.1
		eq.Immediate = firstAdv >= 0 && (firstFav < 0 || firstAdv < firstFav)
		out = append(out, eq)
	}
	return out
}

// FormatEntryQuality renders aggregate entry-quality stats plus the worst entries.
func FormatEntryQuality(trader string, loaded []loadedEntry, topWorst int) string {
	eqs := AnalyzeEntryQuality(loaded)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("==== ENTRY QUALITY: %s (n=%d, window=%d bars) ====\n", trader, len(eqs), eqWindowBars))
	if len(eqs) == 0 {
		sb.WriteString("  (no analyzable entries)\n")
		return sb.String()
	}
	var sumMAE, sumMFE, sumFirst float64
	var immediate, neverGreen int
	for _, e := range eqs {
		sumMAE += e.MAEatr
		sumMFE += e.MFEatr
		sumFirst += e.FirstMove
		if e.Immediate {
			immediate++
		}
		if !e.EverGreen {
			neverGreen++
		}
	}
	n := float64(len(eqs))
	sb.WriteString(fmt.Sprintf("  avg MAE=%.2f ATR   avg MFE=%.2f ATR   MFE/MAE=%.2f\n",
		sumMAE/n, sumMFE/n, (sumMFE/n)/((sumMAE/n)+1e-9)))
	sb.WriteString(fmt.Sprintf("  avg first-bar move=%+.2f ATR\n", sumFirst/n))
	sb.WriteString(fmt.Sprintf("  immediate-adverse (went >0.5ATR against BEFORE >0.5ATR for): %d/%d = %.0f%%\n",
		immediate, len(eqs), 100*float64(immediate)/n))
	sb.WriteString(fmt.Sprintf("  never-green (MFE < 0.1 ATR ever): %d/%d = %.0f%%\n",
		neverGreen, len(eqs), 100*float64(neverGreen)/n))

	// distribution of MAE
	sort.Slice(eqs, func(i, j int) bool { return eqs[i].MAEatr > eqs[j].MAEatr })
	var p50, p75, p90 float64
	p50 = eqs[len(eqs)/2].MAEatr
	p75 = eqs[len(eqs)/4].MAEatr
	p90 = eqs[len(eqs)/10].MAEatr
	sb.WriteString(fmt.Sprintf("  MAE percentiles: p50=%.2f  p75=%.2f  p90=%.2f ATR\n", p50, p75, p90))

	if topWorst > 0 {
		sb.WriteString(fmt.Sprintf("\n  -- %d worst entries (highest MAE, low MFE) --\n", topWorst))
		shown := 0
		for _, e := range eqs {
			sb.WriteString(fmt.Sprintf("    %-12s %-5s MAE=%-6.2f MFE=%-6.2f first=%-+6.2f barsToMFE=%-3d green=%-5v imm=%v\n",
				e.Symbol, e.Side, e.MAEatr, e.MFEatr, e.FirstMove, e.BarsToMFE, e.EverGreen, e.Immediate))
			shown++
			if shown >= topWorst {
				break
			}
		}
	}
	return sb.String()
}
