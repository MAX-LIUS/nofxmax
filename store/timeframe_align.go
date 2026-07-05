package store

// timeframe_align.go makes primary_timeframe the single source of truth.
//
// The bug this prevents: a strategy set its primary timeframe to 15m but left
// atr_protection.timeframe / breakout_entry.timeframe / breadth timeframe at the
// old 1h. Because ATRProtection.WithDefaults() silently fills an empty timeframe
// with "1h", the whole protection stack (ATR-unit TP ladder, drawdown arming,
// structural SL boundary + backstop, structural close-confirm bar) was computed
// on 1h data while entries fired on 15m — an internally inconsistent trader that
// clipped winners (TP targets ~2x too far) and never armed its drawdown guard.
//
// When TimeframeDiscipline == "follow_primary", AlignToPrimaryTimeframe pins
// every timeframe-derived field to a canonical ladder built from the primary,
// so any trader on any primary (1m … 1d) runs a fully self-consistent pipeline.

// tfLadder is the canonical ordered timeframe ladder used for adjacency/derivation.
var tfLadder = []string{"1m", "3m", "5m", "15m", "30m", "1h", "2h", "4h", "6h", "12h", "1d"}

// tfMinutes maps a timeframe token to its minute count. 0 = unknown.
func tfMinutes(tf string) int {
	switch tf {
	case "1m":
		return 1
	case "3m":
		return 3
	case "5m":
		return 5
	case "15m":
		return 15
	case "30m":
		return 30
	case "1h":
		return 60
	case "2h":
		return 120
	case "4h":
		return 240
	case "6h":
		return 360
	case "8h":
		return 480
	case "12h":
		return 720
	case "1d":
		return 1440
	default:
		return 0
	}
}

// containsTF reports whether tf is present in the list.
func containsTF(list []string, tf string) bool {
	for _, v := range list {
		if v == tf {
			return true
		}
	}
	return false
}

// moveTFFirst returns a copy of list with tf moved to the front (order of the
// rest preserved). If tf is absent, it is prepended. Used to guarantee the
// primary timeframe leads the analysis set without discarding the user's choice.
func moveTFFirst(list []string, tf string) []string {
	out := make([]string, 0, len(list)+1)
	out = append(out, tf)
	for _, v := range list {
		if v != tf {
			out = append(out, v)
		}
	}
	return out
}

// tfLadderIndex returns the index of tf in tfLadder, or -1 if not present.
func tfLadderIndex(tf string) int {
	for i, v := range tfLadder {
		if v == tf {
			return i
		}
	}
	return -1
}

// DerivedTimeframes holds the timeframe set computed from a primary timeframe.
type DerivedTimeframes struct {
	Primary      string   // the primary itself
	LowerAdjacent string  // one step below primary (or the step above if primary is the floor)
	Longer       string   // ~4x primary, used for higher-context + breadth-from-peak ATR
	Selected     []string // [lowerAdjacent, primary, longer] deduped, primary first
}

// DeriveTimeframes builds a self-consistent timeframe set from a primary token.
// Longer is chosen ~4 ladder-appropriate steps up (crypto convention: primary,
// primary*4, primary*16 → we take the *4-ish neighbour as "longer" context).
// Falls back gracefully at the ladder edges.
func DeriveTimeframes(primary string) DerivedTimeframes {
	idx := tfLadderIndex(primary)
	if idx < 0 {
		// Unknown primary: return it alone so we never fabricate bad data.
		return DerivedTimeframes{Primary: primary, Selected: []string{primary}}
	}

	// Lower adjacent: one step down; if primary is the floor, use one step up.
	lower := ""
	if idx > 0 {
		lower = tfLadder[idx-1]
	} else if idx+1 < len(tfLadder) {
		lower = tfLadder[idx+1]
	}

	// Longer context: nearest ladder entry to ~4x the primary's minutes.
	longer := deriveLonger(primary)

	sel := []string{primary}
	if lower != "" && lower != primary {
		sel = append(sel, lower)
	}
	if longer != "" && longer != primary && longer != lower {
		sel = append(sel, longer)
	}

	return DerivedTimeframes{
		Primary:       primary,
		LowerAdjacent: lower,
		Longer:        longer,
		Selected:      sel,
	}
}

// deriveLonger returns the ladder timeframe closest to 4x the primary's minutes,
// but never below the primary. Used for higher-timeframe context and the
// breadth-from-peak ATR (scale period should match the multi-bar judgment period).
func deriveLonger(primary string) string {
	pm := tfMinutes(primary)
	if pm <= 0 {
		return ""
	}
	target := pm * 4
	best := ""
	bestDelta := 1 << 30
	pidx := tfLadderIndex(primary)
	for i, tf := range tfLadder {
		if i <= pidx { // longer must be strictly above primary
			continue
		}
		m := tfMinutes(tf)
		if m <= 0 {
			continue
		}
		d := m - target
		if d < 0 {
			d = -d
		}
		// <= so that on a tie (e.g. 1m: 4min is equidistant to 3m and 5m) we
		// prefer the LARGER timeframe, keeping "longer" distinct from the
		// step-up lower-adjacent used at the ladder floor.
		if d <= bestDelta {
			bestDelta = d
			best = tf
		}
	}
	if best == "" {
		// primary is at/near the top of the ladder; keep the top entry.
		best = tfLadder[len(tfLadder)-1]
		if best == primary {
			best = ""
		}
	}
	return best
}

// TimeframeDisciplineFollowPrimary is the TimeframeDiscipline value that turns on
// full alignment of every timeframe-derived field to the primary timeframe.
const TimeframeDisciplineFollowPrimary = "follow_primary"

// AlignToPrimaryTimeframe force-aligns every timeframe-derived setting to the
// primary timeframe when TimeframeDiscipline == "follow_primary". It is a no-op
// otherwise (legacy configs keep their stored values). Idempotent and safe to
// call repeatedly; each StrategyConfig is per-trader so alignment is isolated.
//
// Aligned fields:
//   - Indicators.Klines.SelectedTimeframes / LongerTimeframe  (AI + execution data)
//   - ATRProtection.Timeframe                                 (ATR units: TP ladder, drawdown, structural SL)
//   - BreakoutEntry.Timeframe                                 (breakout scan)
//   - Protection.GivebackGuard.BreadthFromPeakTimeframe       (from-peak ATR scale)
//   - RiskControl time-stop / max-hold: bar-count → hours via primary (if *Bars set)
func (c *StrategyConfig) AlignToPrimaryTimeframe() {
	if c == nil || c.TimeframeDiscipline != TimeframeDisciplineFollowPrimary {
		return
	}
	primary := c.Indicators.Klines.PrimaryTimeframe
	if primary == "" {
		return // nothing to align to; leave config untouched
	}
	d := DeriveTimeframes(primary)
	if len(d.Selected) == 0 {
		return // unknown primary; do not fabricate
	}

	// 1) AI + execution CONTEXT timeframes (selected/longer). These are a
	//    deliberate multi-timeframe *analysis* choice, not a protection anchor,
	//    so we PRESERVE a user-chosen set that is already consistent (contains the
	//    primary) — we only guarantee the primary is present and listed first.
	//    We fully derive ONLY when the set is empty or omits the primary (the
	//    misconfigured / new-trader case). This keeps an already-profitable
	//    trader's AI inputs untouched while still auto-completing broken configs.
	if !containsTF(c.Indicators.Klines.SelectedTimeframes, primary) {
		c.Indicators.Klines.SelectedTimeframes = append([]string(nil), d.Selected...)
	} else {
		c.Indicators.Klines.SelectedTimeframes = moveTFFirst(c.Indicators.Klines.SelectedTimeframes, primary)
	}
	// Longer timeframe: only fill when unset (never override a deliberate choice).
	if c.Indicators.Klines.LongerTimeframe == "" && d.Longer != "" {
		c.Indicators.Klines.LongerTimeframe = d.Longer
	}

	// 2) Protection ATR timeframe — the field that silently defaulted to 1h.
	//    Everything ATR-unit (TP ladder, drawdown arming, structural boundary +
	//    backstop) resolves against this, so it MUST equal primary. This is the
	//    real timeframe-discipline anchor (the Claude-R mismatch bug).
	c.ATRProtection.Timeframe = primary

	// 3) Breakout scan timeframe — MUST equal primary so a closed-bar breakout is
	//    judged on the same series the trader trades.
	c.BreakoutEntry.Timeframe = primary

	// 4) Breadth from-peak ATR scale — only fill when unset, so a deliberate
	//    choice survives; empty configs get the ~4x longer (scale≈judgment period).
	if c.Protection.GivebackGuard.BreadthFromPeakTimeframe == "" && d.Longer != "" {
		c.Protection.GivebackGuard.BreadthFromPeakTimeframe = d.Longer
	}

	// 5) Bar-count holds override hours so "hold N bars" is timeframe-invariant.
	if pm := tfMinutes(primary); pm > 0 {
		if c.RiskControl.TimeStopBars > 0 {
			c.RiskControl.TimeStopHours = float64(c.RiskControl.TimeStopBars*pm) / 60.0
		}
		if c.RiskControl.MaxHoldBars > 0 {
			c.RiskControl.MaxHoldHours = float64(c.RiskControl.MaxHoldBars*pm) / 60.0
		}
	}
}
