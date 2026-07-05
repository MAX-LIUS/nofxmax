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
		// Phase-2 close-confirm: tight boundary enforced on bar close, wide
		// backstop is the resting intrabar stop. Mirrors runStructuralSLGuard.
		p.RangeSLCloseConfirm = ss.CloseConfirm
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

// rangeStructuralSLPrice reconstructs the live structural stop for one entry:
// boundary = lookback range low (long) / high (short) over the RangeSLLookback
// CLOSED bars BEFORE entry, with the entry→boundary distance clamped to
// [RangeSLFloorATR, RangeSLBackstopATR] ATR multiples. Mirrors live
// computeStructuralBoundary + structuralSLPercent. Returns (price, ok); ok=false
// when the range has no edge (entered at/through the boundary) so the caller
// falls back to the flat ATR stop.
func rangeStructuralSLPrice(p ProtectionParams, e Entry, bars []market.Kline, entryIdx int, atr float64, isLong bool) (float64, bool) {
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
	lo, hi := 0.0, 0.0
	for i := start; i < entryIdx; i++ {
		if lo == 0 || bars[i].Low < lo {
			lo = bars[i].Low
		}
		if bars[i].High > hi {
			hi = bars[i].High
		}
	}
	if lo <= 0 || hi <= 0 {
		return 0, false
	}
	var boundary float64
	if isLong {
		if lo >= e.EntryPrice { // entered at/below range floor — no edge
			return 0, false
		}
		boundary = lo
	} else {
		if hi <= e.EntryPrice {
			return 0, false
		}
		boundary = hi
	}
	// Clamp the entry→boundary distance to [floor, backstop] ATR multiples.
	dist := e.EntryPrice - boundary
	if dist < 0 {
		dist = -dist
	}
	mult := dist / atr
	if p.RangeSLFloorATR > 0 && mult < p.RangeSLFloorATR {
		mult = p.RangeSLFloorATR
	}
	if p.RangeSLBackstopATR > 0 && mult > p.RangeSLBackstopATR {
		mult = p.RangeSLBackstopATR
	}
	distPct := mult * atr / e.EntryPrice * 100
	return priceAtDistance(e.EntryPrice, distPct, isLong, false /*adverse*/), true
}
