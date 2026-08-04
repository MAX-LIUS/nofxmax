package backtest

import (
	"math"
	"strconv"
)

// config_variants.go builds FAITHFUL optimization candidates for a live trader:
// each variant starts from the trader's actual LiveConfigParams (so it keeps the
// same unit=ATR ladder AND the RangeSL structural-stop reconstruction — the same
// stop TYPE the live system runs) and applies ONE targeted, pre-specified tweak.
//
// This is the credible way to evaluate the proposed protection changes: the
// built-in ATR sweep swaps the structural stop for a flat StopLossATR, so its SL
// dimension is directional only. Deriving from the live baseline keeps the stop
// type fixed, so a variant's delta reflects the actual proposed change, not a
// change of stop mechanism. None of the tweaks are fit to the data.

// ConfigVariant is a named single-change derivative of a live baseline.
type ConfigVariant struct {
	Name string
	P    ProtectionParams
}

// trimHours formats a float hour/pct value compactly for a variant name
// (e.g. 9 -> "9", 1.5 -> "1.5").
func trimHours(h float64) string {
	s := strconv.FormatFloat(h, 'f', -1, 64)
	return s
}

// clone deep-copies the slices so a variant tweak never mutates the baseline.
func clone(p ProtectionParams) ProtectionParams {
	c := p
	c.TPLegs = append([]LadderLeg(nil), p.TPLegs...)
	c.BELegs = append([]BELeg(nil), p.BELegs...)
	c.DDRules = append([]DDRule(nil), p.DDRules...)
	return c
}

// LiveVariants returns the live baseline plus a set of pre-specified single-change
// variants covering the proposals: tighter structural backstop, tighter DD
// give-back, earlier BE1, and tighter time-stop. Each keeps every other field at
// the live value so its delta is attributable to the one change.
func LiveVariants(base ProtectionParams) []ConfigVariant {
	vs := []ConfigVariant{{Name: "live-baseline", P: clone(base)}}

	// --- tighter structural backstop (sweep the cap below the live 4.5) ---
	if base.RangeSLEnabled {
		for _, b := range []float64{1.5, 1.8, 2.0, 2.5, 3.0, 3.5, 4.0} {
			v := clone(base)
			v.RangeSLBackstopATR = b
			v.StopLossATR = b // flat fallback matches the cap
			vs = append(vs, ConfigVariant{Name: "backstop-" + trimHours(b) + "ATR", P: v})
		}
	}

	// --- tighter floor (whipsaw guard down to 1.0 ATR gives closer stops) ---
	if base.RangeSLEnabled {
		v := clone(base)
		v.RangeSLFloorATR = 1.0
		vs = append(vs, ConfigVariant{Name: "floor-1.0ATR", P: v})
	}

	// --- FLOOR ladder. A single 1.0 probe cannot tell a real gradient from one
	//     lucky point, and the floor is the clamp that decides whether the stop may
	//     sit where structure actually is. Sweep it so monotonicity is checkable;
	//     skip the live value and the 1.0 point already emitted above.
	if base.RangeSLEnabled {
		for _, f := range []float64{0.5, 0.75, 1.25, 1.5, 1.75, 2.0} {
			if math.Abs(f-base.RangeSLFloorATR) < 1e-9 {
				continue
			}
			v := clone(base)
			v.RangeSLFloorATR = f
			vs = append(vs, ConfigVariant{Name: "floor-" + trimHours(f) + "ATR", P: v})
		}
	}

	// --- tighter DD give-back on the first tier (retrace 30% vs live 40) ---
	if len(base.DDRules) > 0 {
		v := clone(base)
		if v.DDRules[0].MaxDrawdownATR > 0 {
			v.DDRules[0].MaxDrawdownATR *= 0.75
		} else {
			v.DDRules[0].MaxDrawdownPct *= 0.75
		}
		vs = append(vs, ConfigVariant{Name: "dd-tighter-25pct", P: v})
	}

	// DD tier-1 grid. managed_drawdown is GPT's highest per-trade earner, so its
	// arm (min profit) and give-back (max drawdown) are the two most load-bearing
	// numbers in the stack; a single -25%% probe cannot show whether either axis is
	// monotonic or where the peak sits.
	if len(base.DDRules) > 0 && base.DDRules[0].MinProfitATR > 0 && base.DDRules[0].MaxDrawdownATR > 0 {
		for _, arm := range []float64{0.6, 0.9, 1.2, 1.5, 2.0, 2.5, 3.0, 3.5} {
			for _, gb := range []float64{0.3, 0.45, 0.6, 0.8, 1.1, 1.5, 2.0} {
				a, g := arm, gb
				if math.Abs(a-base.DDRules[0].MinProfitATR) < 1e-9 && math.Abs(g-base.DDRules[0].MaxDrawdownATR) < 1e-9 {
					continue
				}
				v := clone(base)
				v.DDRules[0].MinProfitATR = a
				v.DDRules[0].MaxDrawdownATR = g
				vs = append(vs, ConfigVariant{Name: "dd-arm" + trimHours(a) + "-gb" + trimHours(g), P: v})
			}
		}
	}

	// --- earlier BE1 arm (arm break-even sooner to lock gains) ---
	if len(base.BELegs) > 0 {
		v := clone(base)
		if v.BELegs[0].TriggerATR > 0 {
			v.BELegs[0].TriggerATR *= 0.75
		} else {
			v.BELegs[0].TriggerPct *= 0.75
		}
		vs = append(vs, ConfigVariant{Name: "be1-earlier-25pct", P: v})
	}

	// --- ABSOLUTE BE1 arm ladder in ATR units. The 25%-relative variant above can
	//     only probe one point next to whatever the config happens to hold, which is
	//     useless when the live arm sits far out (GPT: 1.1 ATR) and the registered
	//     entry-gate finding is about an absolute level (0.30 ATR). Sweep the level
	//     itself so the arm's PnL curve is readable, and skip points equal to live.
	if len(base.BELegs) > 0 && base.BELegs[0].TriggerATR > 0 {
		for _, a := range []float64{0.3, 0.5, 0.7, 0.9, 1.3, 1.6} {
			if math.Abs(a-base.BELegs[0].TriggerATR) < 1e-9 {
				continue
			}
			v := clone(base)
			v.BELegs[0].TriggerATR = a
			vs = append(vs, ConfigVariant{Name: "be1arm-" + trimHours(a) + "ATR", P: v})
		}
	}

	// --- COUPLED BE1 arm ladder: move the arm AND scale the rest-offset with it.
	//     The absolute ladder above varies TriggerATR alone, which silently changes
	//     TWO things: where BE arms, and how much room the resting stop has. Live is
	//     arm 1.1 / offset 0.25 ATR, i.e. the stop sits 0.85 ATR BELOW the arming
	//     price. Setting arm=0.3 while offset stays 0.25 leaves only 0.05 ATR of
	//     room, so the tier is stopped out by noise the moment it arms — that is a
	//     broken config, not an "earlier break-even" test, and it is unfalsifiable on
	//     1h bars because the intrabar fire is invisible. Holding offset/arm at the
	//     live ratio keeps the tier's geometry intact so the arm LEVEL is the only
	//     thing under test.
	if len(base.BELegs) > 0 && base.BELegs[0].TriggerATR > 0 && base.BELegs[0].OffsetATR > 0 {
		ratio := base.BELegs[0].OffsetATR / base.BELegs[0].TriggerATR
		for _, a := range []float64{0.3, 0.5, 0.7, 0.9, 1.3, 1.6} {
			if math.Abs(a-base.BELegs[0].TriggerATR) < 1e-9 {
				continue
			}
			v := clone(base)
			v.BELegs[0].TriggerATR = a
			v.BELegs[0].OffsetATR = a * ratio
			vs = append(vs, ConfigVariant{Name: "be1coup-" + trimHours(a) + "ATR", P: v})
		}
	}

	// --- tighter time-stop (cut the -1.5% time exit to fire 25% sooner) ---
	if base.CloseProxy.Enabled && base.CloseProxy.TimeStopHours > 0 {
		v := clone(base)
		v.CloseProxy.TimeStopHours *= 0.75
		vs = append(vs, ConfigVariant{Name: "timestop-25pct-sooner", P: v})
	}

	// --- max-hold sweep (the parameter that ACTUALLY fires live; the time-stop
	//     above is dead on Claude-R because max-hold at 18h < time-stop 24h closes
	//     every non-runner first). Test tighter and looser max-hold plus dropping
	//     the redundant time-stop entirely. ---
	if base.CloseProxy.Enabled && base.CloseProxy.MaxHoldHours > 0 {
		for _, h := range []float64{9, 12, 24, 36} {
			v := clone(base)
			v.CloseProxy.MaxHoldHours = h
			vs = append(vs, ConfigVariant{Name: "maxhold-" + trimHours(h) + "h", P: v})
		}

		// Profit-exemption sweep: how forgiving the runner exemption is.
		for _, ex := range []float64{1.0, 3.0} {
			v := clone(base)
			v.CloseProxy.MaxHoldProfitExemptPct = ex
			vs = append(vs, ConfigVariant{Name: "maxhold-exempt-" + trimHours(ex) + "pct", P: v})
		}

		// Drop the dead time-stop entirely (max-hold already dominates it).
		v := clone(base)
		v.CloseProxy.TimeStopHours = 0
		vs = append(vs, ConfigVariant{Name: "no-timestop", P: v})

		// Repurpose time-stop as a MEANINGFUL early loss-cut: fire it BEFORE
		// max-hold (at 8h / 12h, still-in-loss ≤ -1.5%) so it actually catches
		// slow bleeders instead of sitting dead behind the 18h max-hold.
		for _, h := range []float64{8, 12} {
			v := clone(base)
			v.CloseProxy.TimeStopHours = h
			vs = append(vs, ConfigVariant{Name: "timestop-early-" + trimHours(h) + "h", P: v})
		}
		// Early loss-cut at a shallower loss threshold (fire at -1.0% by 12h).
		v3 := clone(base)
		v3.CloseProxy.TimeStopHours = 12
		v3.CloseProxy.TimeStopLossPct = -1.0
		vs = append(vs, ConfigVariant{Name: "timestop-12h-loss1.0", P: v3})

		// Disable max-hold too — isolate its net contribution.
		v2 := clone(base)
		v2.CloseProxy.MaxHoldHours = 0
		vs = append(vs, ConfigVariant{Name: "no-maxhold", P: v2})
	}

	// --- combined: backstop 3.0 + tighter DD + earlier BE1 ---
	if base.RangeSLEnabled {
		v := clone(base)
		v.RangeSLBackstopATR = 3.0
		v.StopLossATR = 3.0
		if len(v.DDRules) > 0 {
			if v.DDRules[0].MaxDrawdownATR > 0 {
				v.DDRules[0].MaxDrawdownATR *= 0.75
			} else {
				v.DDRules[0].MaxDrawdownPct *= 0.75
			}
		}
		if len(v.BELegs) > 0 {
			if v.BELegs[0].TriggerATR > 0 {
				v.BELegs[0].TriggerATR *= 0.75
			} else {
				v.BELegs[0].TriggerPct *= 0.75
			}
		}
		vs = append(vs, ConfigVariant{Name: "combined-tight", P: v})
	}

	// --- fallback RR-cap anchor: first_target vs live max-TP ---
	// The live fallback (no-near-structure path) RR-caps the stop against the FARTHEST
	// TP leg (lowest fill probability), inflating the "guaranteed" RR. These variants
	// re-anchor the cap to the AI's risk_reward.first_target — the numerator of the AI's
	// authoritative RR — so the structural fallback and the AI RR use one yardstick.
	// Also sweep the cap ratio (live 0.8 was hand-picked, not backtested).
	if base.RangeSLEnabled {
		// Pure anchor switch at the live ratio.
		va := clone(base)
		va.RangeSLFallbackAnchor = "first_target"
		vs = append(vs, ConfigVariant{Name: "fbanchor-firsttarget", P: va})

		// Cap-ratio sweep under the live (max-TP) anchor — isolates the ratio effect.
		for _, r := range []float64{0.6, 0.8, 1.0, 1.25} {
			v := clone(base)
			v.RangeSLFallbackRRCapRatio = r
			vs = append(vs, ConfigVariant{Name: "fbratio-" + trimHours(r) + "-maxtp", P: v})
		}
		// Cap-ratio sweep under the first_target anchor — the combined proposal.
		for _, r := range []float64{0.6, 0.8, 1.0, 1.25} {
			v := clone(base)
			v.RangeSLFallbackAnchor = "first_target"
			v.RangeSLFallbackRRCapRatio = r
			vs = append(vs, ConfigVariant{Name: "fbratio-" + trimHours(r) + "-firsttarget", P: v})
		}
	}

	// --- RR-cap AS PRIMARY: "AI RR is the authoritative RR" architecture ---
	// The RR cap (ratio × first_target) becomes a UNIVERSAL ceiling on every structural
	// stop, not just the rare fallback branch. Floor lowered to 0.5×ATR so a tight cap
	// actually binds (otherwise the 1.5 floor swallows it, as the diagnostic showed).
	// This directly tests whether tightening stops to a fraction of the AI first_target
	// helps or hurts, and what ratio is optimal. NOT a live change — sweep only.
	if base.RangeSLEnabled {
		for _, r := range []float64{0.8, 1.0, 1.25, 1.5, 2.0} {
			v := clone(base)
			v.RRCapPrimary = true
			v.RangeSLFallbackAnchor = "first_target"
			v.RangeSLFallbackRRCapRatio = r
			v.RangeSLFloorATR = 0.5 // let the cap bind below the live 1.5 floor
			vs = append(vs, ConfigVariant{Name: "rrcapAI-" + trimHours(r) + "-floor0.5", P: v})
		}
		// Same sweep keeping the live 1.5 floor, to isolate the floor's protective role.
		for _, r := range []float64{0.8, 1.25, 2.0} {
			v := clone(base)
			v.RRCapPrimary = true
			v.RangeSLFallbackAnchor = "first_target"
			v.RangeSLFallbackRRCapRatio = r
			vs = append(vs, ConfigVariant{Name: "rrcapAI-" + trimHours(r) + "-floor1.5", P: v})
		}
	}

	return vs
}
