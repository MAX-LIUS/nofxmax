package backtest

import (
	"fmt"
	"math"
	"strings"

	"nofx/market"
)

// confirm_timing.go studies the REAL tradeoff behind the fake_retest gate: instead
// of the live 0.15% distance proxy, it re-derives each entry at a candidate
// closed-candle confirmation rule and measures what confirmation TIMING and
// TIMEFRAME actually cost/save.
//
// Per entry (that carries an adverse-side SLAnchor) it:
//  1. finds the anchor TOUCH near the AI's entry (last bar reaching the level zone);
//  2. applies a confirmation rule (N consecutive closes back on the correct side,
//     optionally accepting one strong wick-rejection bar) to find the CONFIRM bar;
//  3. sets the confirmed entry = that bar's close, stop = anchor, and simulates
//     forward to measure risk% (entry→anchor), MFE in R, and whether the stop got
//     hit first (a realized fake-retest).
//
// The point: earlier/looser confirmation keeps the stop tight (good RR) but lets
// more fakes through (higher stop-hit); later/stricter confirmation cuts fakes but
// drifts the entry away from the level (bigger risk%, smaller MFE-in-R). The sweep
// exposes the balance and does it on 15m vs 1h so the timeframe question is answered.

// ctOutcome is one entry's re-derived result under a given rule+TF.
type ctOutcome struct {
	confirmed  bool
	barsToConf int     // bars from touch to confirm
	riskPct    float64 // |confirmEntry-anchor|/confirmEntry*100
	mfeR       float64 // max favorable excursion / risk (unitless R)
	maeR       float64 // max adverse excursion / risk
	stopHit    bool    // did price reach the anchor stop within the forward window
	reached1R  bool    // did MFE reach >=1R before the stop
	fullWindow bool    // was a full forward window available
}

// detectTouchIdx finds the latest bar index at/around the AI entry where price
// reached the anchor zone (Low<=anchor for long-support, High>=anchor for
// short-resistance), scanning back up to `look` bars from the entry position.
// Returns -1 when no touch is found.
func detectTouchIdx(bars []market.Kline, entryPos int, anchor float64, isLong bool, tol float64, look int) int {
	lo := entryPos - look
	if lo < 0 {
		lo = 0
	}
	for i := entryPos; i >= lo; i-- {
		b := bars[i]
		if isLong {
			if b.Low <= anchor*(1+tol) {
				return i
			}
		} else {
			if b.High >= anchor*(1-tol) {
				return i
			}
		}
	}
	return -1
}

// isWickReject reports whether bar b is a strong wick-rejection on the correct side
// (wick >= wickMult*body AND close decisively on the correct side of the anchor).
func isWickReject(b market.Kline, anchor float64, isLong bool, wickMult float64) bool {
	body := math.Abs(b.Close - b.Open)
	if body <= 0 {
		body = 1e-9
	}
	if isLong {
		lowerWick := math.Min(b.Open, b.Close) - b.Low
		return lowerWick >= wickMult*body && b.Close > anchor
	}
	upperWick := b.High - math.Max(b.Open, b.Close)
	return upperWick >= wickMult*body && b.Close < anchor
}

// confirmFrom applies the confirmation rule starting AFTER the touch bar and returns
// the confirm bar index (-1 if none within maxWait bars). Rule: `closesReq`
// consecutive closes on the correct side of the anchor; if wickAccept, a single
// strong wick-rejection bar also confirms immediately.
func confirmFrom(bars []market.Kline, touchIdx int, anchor float64, isLong bool, closesReq int, wickAccept bool, maxWait int) int {
	streak := 0
	end := touchIdx + maxWait
	if end >= len(bars) {
		end = len(bars) - 1
	}
	for i := touchIdx + 1; i <= end; i++ {
		b := bars[i]
		if wickAccept && isWickReject(b, anchor, isLong, 1.5) {
			return i
		}
		correct := (isLong && b.Close > anchor) || (!isLong && b.Close < anchor)
		if correct {
			streak++
			if streak >= closesReq {
				return i
			}
		} else {
			streak = 0
		}
	}
	return -1
}

var _ = market.Kline{}

// simForwardRisk simulates from the confirm bar: stop 1R away, measure MFE/MAE in R
// over up to `horizon` forward bars, flag whether the stop was hit (fake retest
// realized) and whether MFE reached >=1R first. `risk` is the (floored) price
// distance that defines 1R, passed in so the caller can clamp degenerate near-zero
// stops to an executable minimum.
func simForwardRisk(bars []market.Kline, confirmIdx int, entry, risk float64, isLong bool, horizon int) (mfeR, maeR float64, stopHit, reached1R, full bool) {
	if risk <= 0 {
		return 0, 0, false, false, false
	}
	end := confirmIdx + horizon
	if end >= len(bars) {
		end = len(bars) - 1
	} else {
		full = true
	}
	firstStop, first1R := -1, -1
	for i := confirmIdx + 1; i <= end; i++ {
		b := bars[i]
		var fav, adv float64
		if isLong {
			fav = (b.High - entry) / risk
			adv = (entry - b.Low) / risk
		} else {
			fav = (entry - b.Low) / risk
			adv = (b.High - entry) / risk
		}
		if fav > mfeR {
			mfeR = fav
		}
		if adv > maeR {
			maeR = adv
		}
		if adv >= 1.0 && firstStop < 0 {
			firstStop = i
		}
		if fav >= 1.0 && first1R < 0 {
			first1R = i
		}
	}
	stopHit = firstStop >= 0
	reached1R = first1R >= 0 && (firstStop < 0 || first1R <= firstStop)
	return
}

// evalEntryConfirm re-derives one entry under a rule on the given (already-TF)
// bars and entry position. Returns confirmed=false when no touch/confirm found.
func evalEntryConfirm(bars []market.Kline, entryPos int, anchor, atr float64, isLong bool, closesReq int, wickAccept bool, touchLook, maxWait, horizon int) ctOutcome {
	var o ctOutcome
	if anchor <= 0 || entryPos < 2 || entryPos >= len(bars) {
		return o
	}
	touchIdx := detectTouchIdx(bars, entryPos, anchor, isLong, 0.001, touchLook)
	if touchIdx < 0 {
		return o
	}
	confIdx := confirmFrom(bars, touchIdx, anchor, isLong, closesReq, wickAccept, maxWait)
	if confIdx < 0 {
		return o
	}
	o.confirmed = true
	o.barsToConf = confIdx - touchIdx
	entry := bars[confIdx].Close
	// Floor the stop distance so a confirm bar that closes right on the anchor doesn't
	// produce a near-zero R and blow up the R-multiples. Prefer 0.15 ATR; when ATR is
	// unavailable (e.g. a high-TF rung without deep pre-history) fall back to a 0.05%
	// price floor. A live stop tighter than this isn't executable anyway.
	risk := math.Abs(entry - anchor)
	if atr > 0 {
		if risk < 0.15*atr {
			risk = 0.15 * atr
		}
	} else if minPct := 0.0005 * entry; risk < minPct {
		risk = minPct
	}
	o.riskPct = risk / entry * 100
	o.mfeR, o.maeR, o.stopHit, o.reached1R, o.fullWindow = simForwardRisk(bars, confIdx, entry, risk, isLong, horizon)
	return o
}

// ctRuleAgg aggregates outcomes for one (rule,TF) cell.
type ctRuleAgg struct {
	total     int // entries with a detectable touch (the denominator for fill%)
	confirmed int
	sumBars   float64
	sumRisk   float64
	sumMFER   float64
	sumMAER   float64
	stopHits  int
	won1R     int
}

func (a *ctRuleAgg) add(o ctOutcome) {
	if !o.confirmed {
		return
	}
	a.confirmed++
	a.sumBars += float64(o.barsToConf)
	a.sumRisk += o.riskPct
	a.sumMFER += o.mfeR
	a.sumMAER += o.maeR
	if o.stopHit {
		a.stopHits++
	}
	if o.reached1R {
		a.won1R++
	}
}

// entryPosInTF maps the AI entry time onto an aggregated-TF bar slice: the last bar
// whose OpenTime <= entryTime (the bar that was forming at entry). Returns -1 if none.
func entryPosInTF(tfBars []market.Kline, entryTimeMs int64) int {
	pos := -1
	for i, b := range tfBars {
		if b.OpenTime <= entryTimeMs {
			pos = i
		} else {
			break
		}
	}
	return pos
}

// ctRule names a confirmation rule.
type ctRule struct {
	name      string
	closesReq int
	wick      bool
}

// FormatConfirmTiming sweeps confirmation rules × timeframe over the loaded entries
// (fetched at 15m base) and prints the timing/RR tradeoff. mult=1 → 15m, mult=4 → 1h.
func FormatConfirmTiming(trader string, loaded []loadedEntry, horizon int) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("==== CONFIRMATION TIMING × TIMEFRAME: %s ====\n", trader))
	sb.WriteString(fmt.Sprintf("(re-derives entry at each rule from the anchor touch; stop=anchor; R=|entry-anchor|; forward horizon=%d bars)\n", horizon))
	sb.WriteString("fill%=confirmed/touched  bars=touch→confirm  risk%=entry→anchor  MFE_R/MAE_R in R  stopHit%=fake realized  win1R%=hit +1R before stop\n")

	rules := []ctRule{
		{"1close", 1, false},
		{"2close", 2, false},
		{"3close", 3, false},
		{"1close+wick", 1, true},
		{"2close+wick", 2, true},
	}
	tfs := []struct {
		label string
		mult  int
	}{{"15m", 1}, {"1h", 4}}

	touchLook, maxWait := 10, 12

	for _, tf := range tfs {
		sb.WriteString(fmt.Sprintf("\n── timeframe %s ──\n", tf.label))
		sb.WriteString(fmt.Sprintf("  %-12s %6s %6s %7s %7s %7s %9s %8s\n",
			"rule", "fill%", "bars", "risk%", "MFE_R", "MAE_R", "stopHit%", "win1R%"))
		for _, r := range rules {
			var agg ctRuleAgg
			for _, le := range loaded {
				if le.entry.Structural == nil || le.entry.Structural.SLAnchor <= 0 {
					continue
				}
				isLong := strings.EqualFold(le.entry.Side, "LONG")
				anchor := le.entry.Structural.SLAnchor
				bars := le.bars
				entryPos := le.entryIdx
				if tf.mult > 1 {
					bars = aggregateByClock(le.bars, tf.mult)
					entryPos = entryPosInTF(bars, le.entry.EntryTime)
				}
				if entryPos < 2 || entryPos >= len(bars) {
					continue
				}
				// count as "touched" only if a touch exists (denominator)
				if detectTouchIdx(bars, entryPos, anchor, isLong, 0.001, touchLook) < 0 {
					continue
				}
				atr := entryATR(bars, entryPos) // ATR on the same TF as the replay bars
				agg.total++
				o := evalEntryConfirm(bars, entryPos, anchor, atr, isLong, r.closesReq, r.wick, touchLook, maxWait, horizon)
				agg.add(o)
			}
			if agg.total == 0 {
				sb.WriteString(fmt.Sprintf("  %-12s   (no touches detected)\n", r.name))
				continue
			}
			c := float64(agg.confirmed)
			if c == 0 {
				sb.WriteString(fmt.Sprintf("  %-12s %5.0f%%   (0 confirmed)\n", r.name, 100*c/float64(agg.total)))
				continue
			}
			sb.WriteString(fmt.Sprintf("  %-12s %5.0f%% %6.1f %6.2f %7.2f %7.2f %8.0f%% %7.0f%%\n",
				r.name,
				100*c/float64(agg.total),
				agg.sumBars/c,
				agg.sumRisk/c,
				agg.sumMFER/c,
				agg.sumMAER/c,
				100*float64(agg.stopHits)/c,
				100*float64(agg.won1R)/c,
			))
		}
	}
	sb.WriteString("\nRead: fewer/looser closes → tighter risk% + more MFE_R but higher stopHit%% (fakes);\n")
	sb.WriteString("      more/stricter closes → fewer fakes but risk% widens and MFE_R shrinks (RR decays).\n")
	sb.WriteString("      Best = highest win1R% at acceptable stopHit%, before risk% inflates.\n")
	return sb.String()
}

// aggByBaseMs aggregates base bars into `mult`×base buckets by wall clock, without
// relying on the global activeStructTFPlan (unlike aggregateByClock). baseMs is the
// base bar period in ms.
func aggByBaseMs(base []market.Kline, mult int, baseMs int64) []market.Kline {
	if mult <= 1 || len(base) == 0 {
		return base
	}
	periodMs := int64(mult) * baseMs
	var out []market.Kline
	var cur *market.Kline
	curBucket := int64(-1)
	for _, b := range base {
		bucket := b.OpenTime / periodMs
		if cur == nil || bucket != curBucket {
			if cur != nil {
				out = append(out, *cur)
			}
			nb := market.Kline{OpenTime: bucket * periodMs, Open: b.Open, High: b.High, Low: b.Low, Close: b.Close, CloseTime: b.CloseTime}
			cur = &nb
			curBucket = bucket
			continue
		}
		if b.High > cur.High {
			cur.High = b.High
		}
		if b.Low < cur.Low {
			cur.Low = b.Low
		}
		cur.Close = b.Close
		cur.CloseTime = b.CloseTime
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

// FormatConfirmTFLadder answers "which confirmation TF, RELATIVE to the primary TF,
// is best?" It fixes the confirmation RULE (1 close + wick-accept — the winner from
// the rule sweep) and sweeps the confirmation TIMEFRAME up a ladder from baseTF.
// The forward horizon is held constant in WALL-CLOCK hours across every rung so the
// comparison is apples-to-apples (the earlier bug compared equal BAR counts, i.e.
// wildly different wall-clock windows). Read each rung as: if your PRIMARY TF is X,
// confirming at rung Y (<=X) costs/saves this much.
func FormatConfirmTFLadder(trader, baseTF string, loaded []loadedEntry, mults []int, horizonHours float64) string {
	var sb strings.Builder
	baseHrs := TimeframeHours(baseTF)
	baseMs := int64(baseHrs * 3600 * 1000)
	sb.WriteString(fmt.Sprintf("==== CONFIRMATION-TF LADDER: %s (base=%s, rule=1close+wick, horizon=%.0fh wall-clock) ====\n", trader, baseTF, horizonHours))
	sb.WriteString("confirmTF  fill%  h2conf  risk%  MFE_R  MAE_R  stopHit%  win1R%   (h2conf=hours touch→confirm)\n")
	return sb.String() + confirmTFLadderRows(loaded, baseTF, baseMs, baseHrs, mults, horizonHours)
}

// confirmTFLadderRows computes one row per confirmation-TF rung. Rule is fixed at
// 1close+wick. Forward horizon is horizonHours wall-clock, converted to a bar count
// per rung so every TF sees the SAME wall-clock window.
func confirmTFLadderRows(loaded []loadedEntry, baseTF string, baseMs int64, baseHrs float64, mults []int, horizonHours float64) string {
	var sb strings.Builder
	for _, mult := range mults {
		rungHrs := baseHrs * float64(mult)
		horizonBars := int(horizonHours / rungHrs)
		if horizonBars < 4 {
			horizonBars = 4
		}
		// touch-look and max-wait scale with the rung so wall-clock windows are comparable
		touchLook := int(6.0/rungHrs) + 2 // ~6h of touch lookback
		if touchLook < 3 {
			touchLook = 3
		}
		maxWait := int(12.0/rungHrs) + 2 // wait up to ~12h for confirmation
		if maxWait < 3 {
			maxWait = 3
		}
		var agg ctRuleAgg
		var sumHrs float64
		for _, le := range loaded {
			if le.entry.Structural == nil || le.entry.Structural.SLAnchor <= 0 {
				continue
			}
			isLong := strings.EqualFold(le.entry.Side, "LONG")
			anchor := le.entry.Structural.SLAnchor
			bars := aggByBaseMs(le.bars, mult, baseMs)
			entryPos := entryPosInTF(bars, le.entry.EntryTime)
			if entryPos < 2 || entryPos >= len(bars) {
				continue
			}
			if detectTouchIdx(bars, entryPos, anchor, isLong, 0.001, touchLook) < 0 {
				continue
			}
			atr := entryATR(bars, entryPos)
			agg.total++
			o := evalEntryConfirm(bars, entryPos, anchor, atr, isLong, 1, true, touchLook, maxWait, horizonBars)
			agg.add(o)
			if o.confirmed {
				sumHrs += float64(o.barsToConf) * rungHrs
			}
		}
		label := tfLabelFromHours(rungHrs)
		if agg.total == 0 || agg.confirmed == 0 {
			sb.WriteString(fmt.Sprintf("%-9s   (n=%d confirmed=%d — insufficient)\n", label, agg.total, agg.confirmed))
			continue
		}
		c := float64(agg.confirmed)
		sb.WriteString(fmt.Sprintf("%-9s %5.0f%% %6.1f %6.2f %6.2f %6.2f %8.0f%% %7.0f%%\n",
			label,
			100*c/float64(agg.total),
			sumHrs/c,
			agg.sumRisk/c,
			agg.sumMFER/c,
			agg.sumMAER/c,
			100*float64(agg.stopHits)/c,
			100*float64(agg.won1R)/c,
		))
	}
	return sb.String()
}

func tfLabelFromHours(h float64) string {
	switch {
	case h < 1:
		return fmt.Sprintf("%.0fm", h*60)
	case h == math.Trunc(h):
		return fmt.Sprintf("%.0fh", h)
	default:
		return fmt.Sprintf("%.1fh", h)
	}
}
