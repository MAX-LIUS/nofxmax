package backtest

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"nofx/market"
	"nofx/store"
)

// config_baseline.go loads a trader's LIVE strategy protection config and maps
// it to backtest ProtectionParams, so the replay uses the trader's ACTUAL
// protection rules (ATR/structural units) instead of the hardcoded percent
// baseline. This is what makes trusted-subset fidelity meaningful: the replay
// must use the same protection spec the live system ran.

// LiveConfigParams converts a store.StrategyConfig's protection block into
// backtest ProtectionParams. It targets the ATR-unit ladder/BE/DD used by the
// live traders. SL uses the structural backstop ATR multiple as a stop-distance
// approximation (the replay has no live range boundary). tfHours sizes the
// close-proxy held-bars→hours.
func LiveConfigParams(cfg *store.StrategyConfig, tfHours float64) ProtectionParams {
	p := ProtectionParams{Unit: UnitATRMult}
	prot := cfg.Protection

	// --- TP ladder (ATR-unit) ---
	for _, r := range prot.LadderTPSL.Rules {
		if r.TakeProfitCloseRatioPct <= 0 || r.TakeProfitPct <= 0 {
			continue
		}
		leg := LadderLeg{CloseRatioPct: r.TakeProfitCloseRatioPct}
		if r.TakeProfitUnit == store.ProtectionUnitATR {
			leg.ATRMult = r.TakeProfitPct
		} else {
			leg.DistPct = r.TakeProfitPct
		}
		p.TPLegs = append(p.TPLegs, leg)
	}

	// --- Stop loss: structural backstop ATR multiple as the wide stop ---
	// The live full stop is a structural boundary clamped to [floor, backstop]
	// ATR; without the live range we approximate the resting stop at the
	// backstop multiple (the safety-net level a replay can represent).
	ss := prot.LadderTPSL.StructuralSL.WithDefaults()
	if prot.LadderTPSL.StructuralSL.Enabled {
		// Faithful reconstruction: replay the live range-anchored boundary
		// clamped to [floor, backstop] ATR, instead of resting at the backstop.
		p.RangeSLEnabled = true
		p.RangeSLFloorATR = ss.FloorATRMul
		p.RangeSLBackstopATR = ss.BackstopATRMul
		p.RangeSLLookback = ss.LookbackBars
		// Nearest-swing anchoring + tighter fallback with RR cap (mirrors the
		// 2026-07 live fix): anchor to the NEAREST pre-entry swing beyond entry,
		// and when no near structure exists fall back to RangeSLFallbackATR
		// (RR-capped against the TP target) instead of the wide backstop.
		p.RangeSLPivotStrength = ss.PivotStrength
		p.RangeSLFallbackATR = ss.FallbackATRMul
		p.RangeSLFallbackRRCapRatio = ss.FallbackRRCapRatio
		// Max TP target (% of entry) across ladder tiers — the RR-cap reference.
		// ATR-unit tiers can't be resolved to % without a per-entry ATR, so the
		// replay resolves the cap per-entry (see rangeStructuralSLPrice); here we
		// pass through only the explicit percent tiers as a config-time hint. When
		// all TP tiers are ATR-unit this stays 0 and the per-entry path fills it.
		maxTPpct := 0.0
		for _, r := range prot.LadderTPSL.Rules {
			if r.TakeProfitPct > 0 && r.TakeProfitUnit != store.ProtectionUnitATR && r.TakeProfitPct > maxTPpct {
				maxTPpct = r.TakeProfitPct
			}
		}
		p.RangeSLMaxTPTargetPct = maxTPpct
		// Phase-2 close-confirm: tight boundary enforced on bar close, wide
		// backstop is the resting intrabar stop. Mirrors runStructuralSLGuard.
		p.RangeSLCloseConfirm = ss.CloseConfirm
		// --- Ratcheting (trailing) structural stop ---
		// Faithfully carry the live trail spec so GPT/BN (trail_enabled=true,
		// max_ratchets=1, trail_on_profit=false) replay with the same behavior.
		if ss.TrailEnabled {
			p.TrailStructEnabled = true
			p.TrailStructTolATR = ss.TrailTolATR
			p.TrailStructMode = ss.TrailMode
			p.TrailStructHigherMult = ss.TrailHigherMult
			p.TrailStructMinProfitATR = ss.TrailMinProfitATRValue()
			p.TrailStructMaxRatchets = ss.TrailMaxRatchets
			// Read the side gates through the store accessors instead of re-deriving
			// the nil defaults here. Hand-inlining them is how this file silently
			// drifted from live: it hard-coded "unset → true" for BOTH sides, so a
			// backtest kept ratcheting in loss even after live defaulted that off.
			p.TrailStructOnProfit = ss.TrailRatchetOnProfit()
			p.TrailStructOnLoss = ss.TrailRatchetOnLoss()
		}
		// Keep StopLossATR as the flat fallback (used when range has no edge).
		p.StopLossATR = ss.BackstopATRMul
	} else if ss.Enabled && ss.BackstopATRMul > 0 {
		p.StopLossATR = ss.BackstopATRMul
	} else {
		// fall back to any structural SL rule distance, else a wide default.
		for _, r := range prot.LadderTPSL.Rules {
			if r.StopLossPct > 0 {
				p.StopLossATR = r.StopLossPct
				break
			}
		}
		if p.StopLossATR == 0 {
			p.StopLossATR = 4.5
		}
	}

	// --- Break-even tiers (ATR-unit) ---
	for _, r := range prot.BreakEvenStop.Rules {
		if r.TriggerValue <= 0 {
			continue
		}
		leg := BELeg{CloseRatioPct: r.CloseRatioPct}
		if r.TriggerUnit == store.ProtectionUnitATR {
			leg.TriggerATR = r.TriggerValue
			leg.OffsetATR = r.OffsetPct
		} else {
			leg.TriggerPct = r.TriggerValue
			leg.OffsetPct = r.OffsetPct
		}
		p.BELegs = append(p.BELegs, leg)
	}

	// --- Drawdown tiers (ATR-unit give-back) ---
	for _, r := range prot.DrawdownTakeProfit.Rules {
		if r.CloseRatioPct <= 0 {
			continue
		}
		dr := DDRule{CloseRatioPct: r.CloseRatioPct}
		if r.MinProfitUnit == store.ProtectionUnitATR {
			dr.MinProfitATR = r.MinProfitPct
		} else {
			dr.MinProfitPct = r.MinProfitPct
		}
		if r.MaxDrawdownUnit == store.ProtectionUnitATR {
			dr.MaxDrawdownATR = r.MaxDrawdownPct
		} else {
			dr.MaxDrawdownPct = r.MaxDrawdownPct
		}
		p.DDRules = append(p.DDRules, dr)
	}

	// --- non-price close proxy from RiskControl (deterministic) ---
	rc := cfg.RiskControl
	if rc.TimeStopHours > 0 || rc.MaxHoldHours > 0 {
		p.CloseProxy = CloseProxyParams{
			Enabled:                true,
			TimeStopHours:          rc.TimeStopHours,
			TimeStopLossPct:        rc.TimeStopLossPct,
			MaxHoldHours:           rc.MaxHoldHours,
			MaxHoldProfitExemptPct: rc.MaxHoldProfitExemptPct,
			TimeframeHours:         tfHours,
		}
	}
	return p
}

// LoadTraderStrategyConfig loads and parses the strategy config for the trader
// matching traderIDLike (joins traders→strategies). Applies the same
// AlignToPrimaryTimeframe discipline the live loader uses, and returns the
// primary timeframe so the caller can replay on the right series.
func LoadTraderStrategyConfig(db *sql.DB, traderIDLike string) (*store.StrategyConfig, string, error) {
	row := db.QueryRow(`
		SELECT s.config FROM traders t
		JOIN strategies s ON s.id = t.strategy_id
		WHERE t.id LIKE ? LIMIT 1`, traderIDLike)
	var raw string
	if err := row.Scan(&raw); err != nil {
		return nil, "", fmt.Errorf("load strategy config: %w", err)
	}
	var cfg store.StrategyConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, "", fmt.Errorf("parse strategy config: %w", err)
	}
	cfg.Indicators.FillSentimentDefaults()
	cfg.AlignToPrimaryTimeframe()
	tf := cfg.Indicators.Klines.PrimaryTimeframe
	if tf == "" {
		tf = "1h"
	}
	return &cfg, tf, nil
}

// maxTPTargetPct returns the largest TP target move (% of entry) across the TP
// ladder legs, resolving ATR-unit legs with the per-entry ATR. This is the RR-cap
// reference the fallback structural stop tightens against, mirroring live
// ladderMaxTPTargetPct. Falls back to the config-time RangeSLMaxTPTargetPct hint.
func (p ProtectionParams) maxTPTargetPct(atr, entryPrice float64) float64 {
	maxPct := p.RangeSLMaxTPTargetPct
	if entryPrice <= 0 {
		return maxPct
	}
	for _, leg := range p.TPLegs {
		pct := leg.DistPct
		if leg.ATRMult > 0 && atr > 0 {
			pct = leg.ATRMult * atr / entryPrice * 100
		}
		if pct > maxPct {
			maxPct = pct
		}
	}
	return maxPct
}

// anchorTPTargetPct returns the RR-cap reference target (% of entry) per the
// configured RangeSLFallbackAnchor. "first_target" uses the AI's per-entry
// risk_reward.first_target (falling back to max-TP when the decision carried none);
// anything else uses the largest TP leg (live behavior). Returns 0 when no usable
// anchor exists (caller then skips the RR cap).
func (p ProtectionParams) anchorTPTargetPct(e Entry, atr, entryPrice float64) float64 {
	if p.RangeSLFallbackAnchor == "first_target" && e.Structural != nil && e.Structural.FirstTargetPrice > 0 && entryPrice > 0 {
		ft := e.Structural.FirstTargetPrice
		pct := (ft - entryPrice) / entryPrice * 100
		if pct < 0 {
			pct = -pct
		}
		if pct > 0 {
			return pct
		}
	}
	return p.maxTPTargetPct(atr, entryPrice)
}

// fallbackMultWithRRCap returns the ATR multiple used on the NO-NEAR-STRUCTURE path:
// RangeSLFallbackATR, additionally tightened so fallbackSL% <= ratio × TP target, with
// RangeSLFloorATR as a hard minimum. Mirrors live fallbackMultWithRRCap. anchorTPPct is
// the RR-cap reference target (resolved per RangeSLFallbackAnchor by the caller).
func (p ProtectionParams) fallbackMultWithRRCap(atr, entryPrice, anchorTPPct float64) float64 {
	mult := p.RangeSLFallbackATR
	if mult <= 0 {
		mult = 3.0
	}
	backstop := p.RangeSLBackstopATR
	if backstop > 0 && mult > backstop {
		mult = backstop
	}
	ratio := p.RangeSLFallbackRRCapRatio
	if ratio > 0 && atr > 0 && entryPrice > 0 {
		if tpPct := anchorTPPct; tpPct > 0 {
			atrPct := atr / entryPrice * 100
			if atrPct > 0 {
				if capMult := (ratio * tpPct) / atrPct; capMult < mult {
					mult = capMult
				}
			}
		}
	}
	if p.RangeSLFloorATR > 0 && mult < p.RangeSLFloorATR {
		mult = p.RangeSLFloorATR
	}
	return mult
}

// rangeStructuralSLPrice reconstructs the live structural stop for one entry:
// boundary = NEAREST pre-entry swing beyond entry (closest overhead swing high for a
// short / closest swing low below for a long) over the RangeSLLookback CLOSED bars
// BEFORE entry, with the entry→boundary distance clamped to [floor, backstop] ATR.
// When the nearest structure is still beyond the backstop, it falls back to the
// tighter RangeSLFallbackATR (RR-capped against the TP target). Mirrors live
// computeStructuralBoundary + structuralSLPercent (2026-07 nearest-swing fix).
// Returns (price, ok); ok=false when no structure is on the correct side of entry
// (entered at/through the boundary) so the caller falls back to the flat ATR stop.
// rangeStructuralBoundary returns the NEAREST pre-entry swing beyond entry (the raw
// structural boundary, unclamped) over the RangeSLLookback CLOSED bars before entry.
// Mirrors live computeStructuralBoundary (fractal pivot, else nearest bar extreme).
// Returns (boundary, ok); ok=false when no structure is on the correct side of entry.
func rangeStructuralBoundary(p ProtectionParams, e Entry, bars []market.Kline, entryIdx int, isLong bool) (float64, bool) {
	lb := p.RangeSLLookback
	if lb <= 0 {
		lb = 24
	}
	// Use the lb CLOSED bars before entry (bars[entryIdx] is the entry bar).
	start := entryIdx - lb
	if start < 0 {
		start = 0
	}
	if entryIdx-start < 2 {
		return 0, false
	}
	window := bars[start:entryIdx]
	entry := e.EntryPrice
	k := p.RangeSLPivotStrength
	if k < 1 {
		k = 1
	}

	// nearestSwing: fractal pivot (a bar more extreme than k bars on each side) on the
	// correct side of entry, CLOSEST to entry. Short => swing highs above entry; long
	// => swing lows below entry. Mirrors live computeStructuralBoundary.
	nearestSwing := func() (float64, bool) {
		best := 0.0
		found := false
		for i := k; i < len(window)-k; i++ {
			if !isLong {
				h := window[i].High
				if h <= entry {
					continue
				}
				isPivot := true
				for j := i - k; j <= i+k; j++ {
					if j != i && window[j].High > h {
						isPivot = false
						break
					}
				}
				if isPivot && (!found || h < best) {
					best, found = h, true
				}
			} else {
				l := window[i].Low
				if l >= entry {
					continue
				}
				isPivot := true
				for j := i - k; j <= i+k; j++ {
					if j != i && window[j].Low < l {
						isPivot = false
						break
					}
				}
				if isPivot && (!found || l > best) {
					best, found = l, true
				}
			}
		}
		return best, found
	}

	fractal, haveFractal := nearestSwing()

	// PreferProvenLevels: mirror live nearestProvenBoundary — prefer an order-block
	// edge on the protective side of entry over the raw fractal, but only when it does
	// not widen the stop past the nearest pivot. Uses the pre-entry window only (no
	// look-ahead: bars[start:entryIdx] excludes the entry bar and everything after).
	if p.RangeSLPreferProven {
		if proven, ok := provenBoundaryBT(window, entry, isLong); ok {
			if !haveFractal {
				return proven, true
			}
			if isLong && proven >= fractal { // higher low = tighter for a long
				return proven, true
			}
			if !isLong && proven <= fractal { // lower high = tighter for a short
				return proven, true
			}
		}
	}

	if haveFractal {
		return fractal, true
	}
	// No fractal pivot — nearest bar extreme on the correct side of entry (still
	// the CLOSEST level, not the absolute spike).
	best := 0.0
	found := false
	for i := 0; i < len(window); i++ {
		if !isLong {
			h := window[i].High
			if h > entry && (!found || h < best) {
				best, found = h, true
			}
		} else {
			l := window[i].Low
			if l < entry && (!found || l > best) {
				best, found = l, true
			}
		}
	}
	if !found || best <= 0 {
		return 0, false
	}
	return best, true
}

// provenBoundaryBT mirrors live AutoTrader.nearestProvenBoundary for the backtest:
// it detects order blocks over the pre-entry window and returns the closest
// protective-side block edge (demand High for a long, supply Low for a short).
// window must be the pre-entry bars only (no look-ahead).
func provenBoundaryBT(window []market.Kline, entry float64, isLong bool) (float64, bool) {
	if len(window) < 20 {
		return 0, false
	}
	// Wilder ATR14 over the window (drop the last bar to match live warmup shape).
	end := len(window) - 1
	if end < 15 {
		return 0, false
	}
	h, l, c := sliceOHLC(window[:end], end-1)
	atr14 := wilderATR(h, l, c, 14)
	if atr14 <= 0 {
		return 0, false
	}
	_, orderBlocks := market.DetectStructureBreaks(window, atr14, entry, "")
	best := 0.0
	found := false
	for _, ob := range orderBlocks {
		if isLong && ob.Direction == "demand" && ob.High < entry {
			if !found || ob.High > best {
				best, found = ob.High, true
			}
		}
		if !isLong && ob.Direction == "supply" && ob.Low > entry {
			if !found || ob.Low < best {
				best, found = ob.Low, true
			}
		}
	}
	return best, found
}

func rangeStructuralSLPrice(p ProtectionParams, e Entry, bars []market.Kline, entryIdx int, atr float64, isLong bool) (float64, bool) {
	boundary, ok := rangeStructuralBoundary(p, e, bars, entryIdx, isLong)
	if !ok || boundary <= 0 {
		return 0, false
	}
	entry := e.EntryPrice
	// Clamp the entry→boundary distance to [floor, backstop] ATR multiples; when
	// beyond the backstop (no near structure) use the RR-capped fallback multiple.
	dist := entry - boundary
	if dist < 0 {
		dist = -dist
	}
	mult := dist / atr
	// Normal path: clamp to [floor, backstop]; beyond backstop use the RR-capped fallback.
	if p.RangeSLBackstopATR > 0 && mult > p.RangeSLBackstopATR {
		mult = p.fallbackMultWithRRCap(atr, entry, p.anchorTPTargetPct(e, atr, entry))
	}
	// RRCapPrimary: apply the RR cap as a UNIVERSAL ceiling on the structural stop,
	// not just the fallback branch. Models "AI RR authoritative": stop distance must
	// not exceed ratio × anchor-TP (typically first_target). Applied BEFORE the floor
	// so the floor still guards the noise minimum.
	if p.RRCapPrimary && p.RangeSLFallbackRRCapRatio > 0 && entry > 0 {
		if tpPct := p.anchorTPTargetPct(e, atr, entry); tpPct > 0 {
			atrPct := atr / entry * 100
			if atrPct > 0 {
				if capMult := p.RangeSLFallbackRRCapRatio * tpPct / atrPct; capMult < mult {
					mult = capMult
				}
			}
		}
	}
	if p.RangeSLFloorATR > 0 && mult < p.RangeSLFloorATR {
		mult = p.RangeSLFloorATR
	}
	distPct := mult * atr / entry * 100
	return priceAtDistance(entry, distPct, isLong, false /*adverse*/), true
}
