package backtest

import "strconv"

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

	return vs
}
