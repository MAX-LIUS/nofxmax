package market

import "math"

// Zone lifecycle states — replaces the single Flipped bool with an explicit
// state machine so the AI can weigh a zone's maturity and reliability.
const (
	ZoneStateFresh     = "fresh"      // level exists but price has not returned to test it
	ZoneStateFirstTest = "first_test" // price has tested it exactly once
	ZoneStateReacted   = "reacted"    // a test produced a strong (>= threshold) reaction
	ZoneStateRetested  = "retested"   // held through 2+ tests without breaking
	ZoneStateWeakened  = "weakened"   // multiple tests with fading reaction (being absorbed)
	ZoneStateBroken    = "broken"     // price closed decisively through and has not reclaimed
	ZoneStateFlipped   = "flipped"    // broken, then acted as opposite-role S/R on retest
	ZoneStateInvalid   = "invalid"    // broken long ago and price left the area (stale)
)

// Structural roles — how a zone is most likely to behave, for the AI to reason
// about entries/targets. Evidence, not a gate.
const (
	ZoneRoleReversal      = "reversal"              // strong rejection zone, tends to turn price
	ZoneRoleContinuation  = "continuation"          // gets broken through in trend direction
	ZoneRoleAcceptance    = "acceptance"            // price consolidates inside (value area style)
	ZoneRoleAccelBoundary = "acceleration_boundary" // edge of a fast move; break -> expansion
	ZoneRoleLiquidity     = "liquidity_target"      // stop cluster / sweep target
)

const (
	reactionStrongATR = 1.5 // reaction >= this many ATR counts as a strong rejection
	reactionWeakATR   = 0.5 // reaction <= this many ATR counts as weak/absorbed
	reactionLookahead = 10  // bars after a test to measure the reaction
	staleBarsInvalid  = 40  // broken + this many bars since last touch -> invalid
	staleDistInvalid  = 4.0 // broken + price this many ATR away -> invalid
)

// testEvent groups contiguous touching bars into a single test with its reaction.
type testEvent struct {
	startIdx    int
	endIdx      int
	reactionATR float64
}

// AnnotateZoneLifecycle computes State, Role and reaction metrics for each zone
// by replaying klines against the zone band. Preserves the existing Flipped /
// FlipCount fields. atr14 must be > 0; otherwise the zones are returned as-is.
func AnnotateZoneLifecycle(zones []StructuralZone, klines []Kline, atr14, currentPrice float64) []StructuralZone {
	if atr14 <= 0 || len(klines) < 5 {
		return zones
	}
	n := len(klines)
	for i := range zones {
		z := &zones[i]
		events, breakIdx, reclaimed := scanZoneInteractions(*z, klines, atr14)

		// reaction metrics
		var maxReact, sumReact float64
		for _, e := range events {
			if e.reactionATR > maxReact {
				maxReact = e.reactionATR
			}
			sumReact += e.reactionATR
		}
		z.TestCount = len(events)
		z.MaxReactionATR = roundSig(maxReact, 4)
		if len(events) > 0 {
			z.AvgReactionATR = roundSig(sumReact/float64(len(events)), 4)
		}

		z.State = classifyZoneState(*z, events, breakIdx, reclaimed, klines, atr14, currentPrice, n)
		z.Role = classifyZoneRole(*z, events)
	}
	return zones
}

// scanZoneInteractions replays klines and returns test events, the index of a
// decisive break (or -1), and whether price reclaimed the zone after a break.
func scanZoneInteractions(z StructuralZone, klines []Kline, atr14 float64) ([]testEvent, int, bool) {
	n := len(klines)
	var events []testEvent
	breakIdx := -1
	reclaimed := false

	inTouch := false
	touchStart := 0
	for i := 0; i < n; i++ {
		k := klines[i]
		touching := k.Low <= z.High && k.High >= z.Low
		if touching && !inTouch {
			inTouch = true
			touchStart = i
		} else if !touching && inTouch {
			inTouch = false
			ev := testEvent{startIdx: touchStart, endIdx: i - 1}
			ev.reactionATR = measureReaction(z, klines, i-1, atr14)
			events = append(events, ev)
		}
	}
	if inTouch {
		ev := testEvent{startIdx: touchStart, endIdx: n - 1}
		ev.reactionATR = measureReaction(z, klines, n-1, atr14)
		events = append(events, ev)
	}

	// decisive break: 2 consecutive closes beyond the far side
	for i := 1; i < n; i++ {
		if z.Type == "support" {
			if klines[i-1].Close < z.Low && klines[i].Close < z.Low {
				breakIdx = i
				break
			}
		} else { // resistance
			if klines[i-1].Close > z.High && klines[i].Close > z.High {
				breakIdx = i
				break
			}
		}
	}
	// reclaimed: after a break, a later close returns to the original side
	if breakIdx >= 0 {
		for i := breakIdx + 1; i < n; i++ {
			if z.Type == "support" && klines[i].Close > z.High {
				reclaimed = true
				break
			}
			if z.Type == "resistance" && klines[i].Close < z.Low {
				reclaimed = true
				break
			}
		}
	}
	return events, breakIdx, reclaimed
}

// measureReaction returns the max favorable move away from the zone in ATR over
// the reactionLookahead bars following a test that ended at endIdx.
func measureReaction(z StructuralZone, klines []Kline, endIdx int, atr14 float64) float64 {
	n := len(klines)
	last := endIdx + reactionLookahead
	if last >= n {
		last = n - 1
	}
	best := 0.0
	for i := endIdx + 1; i <= last; i++ {
		var move float64
		if z.Type == "support" {
			move = (klines[i].High - z.High) / atr14 // bounce up
		} else {
			move = (z.Low - klines[i].Low) / atr14 // reject down
		}
		if move > best {
			best = move
		}
	}
	return best
}

// classifyZoneState maps interactions to a lifecycle state.
func classifyZoneState(z StructuralZone, events []testEvent, breakIdx int, reclaimed bool, klines []Kline, atr14, currentPrice float64, n int) string {
	if breakIdx >= 0 {
		if reclaimed || z.Flipped {
			return ZoneStateFlipped
		}
		barsSinceBreak := n - 1 - breakIdx
		dist := math.Abs(currentPrice-z.MidPrice) / atr14
		if barsSinceBreak >= staleBarsInvalid && dist >= staleDistInvalid {
			return ZoneStateInvalid
		}
		return ZoneStateBroken
	}

	switch len(events) {
	case 0:
		return ZoneStateFresh
	case 1:
		if events[0].reactionATR >= reactionStrongATR {
			return ZoneStateReacted
		}
		return ZoneStateFirstTest
	default:
		// multiple tests, not broken. Weakening if reactions are fading.
		if reactionsFading(events) {
			return ZoneStateWeakened
		}
		return ZoneStateRetested
	}
}

// reactionsFading reports whether the later half of test reactions is materially
// weaker than the earlier half (zone being absorbed).
func reactionsFading(events []testEvent) bool {
	if len(events) < 2 {
		return false
	}
	mid := len(events) / 2
	var earlyMax, lateMax float64
	for i, e := range events {
		if i < mid {
			if e.reactionATR > earlyMax {
				earlyMax = e.reactionATR
			}
		} else {
			if e.reactionATR > lateMax {
				lateMax = e.reactionATR
			}
		}
	}
	return lateMax <= reactionWeakATR && earlyMax > reactionWeakATR
}

// classifyZoneRole infers the most likely behaviour from state and reactions.
func classifyZoneRole(z StructuralZone, events []testEvent) string {
	switch z.State {
	case ZoneStateFlipped:
		return ZoneRoleContinuation // flipped levels tend to support trend continuation
	case ZoneStateBroken, ZoneStateInvalid:
		return ZoneRoleContinuation
	case ZoneStateReacted, ZoneStateRetested:
		if z.MaxReactionATR >= reactionStrongATR {
			return ZoneRoleReversal
		}
		return ZoneRoleAcceptance
	case ZoneStateWeakened:
		return ZoneRoleAccelBoundary // fading defense -> break likely to expand
	}
	// fresh / first_test: infer from width and volume backing
	if z.ATRWidth >= 2.0 {
		return ZoneRoleAcceptance // wide zone = value/consolidation area
	}
	if z.MaxReactionATR >= reactionStrongATR {
		return ZoneRoleReversal
	}
	return ZoneRoleAcceptance
}
