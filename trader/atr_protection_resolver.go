package trader

import (
	"strings"
	"sync"
	"time"

	"nofx/logger"
	"nofx/market"
	"nofx/store"
)

// frozenATREntry caches a position's ATR frozen at open so the reconciler reuses
// a stable value instead of recomputing ATR every cycle (which drifts bar-to-bar
// and made resting protection orders perpetually look "unexpected" → order churn).
type frozenATREntry struct {
	entryPrice float64
	atr        float64
}

var (
	frozenATRMu    sync.Mutex
	frozenATRCache = map[string]frozenATREntry{}
)

// frozenStructEntry pairs the frozen boundary with the entry price it was frozen
// against, so a cache hit can be rejected when it belongs to a different position
// on the same key (e.g. a new position reusing a not-yet-evicted slot). boundary 0
// means "computed, none available"; absence means not yet computed.
type frozenStructEntry struct {
	entryPrice float64
	boundary   float64
}

// frozenStructBoundary caches a position's pre-entry range boundary frozen at open,
// keyed like the ATR cache (trader|symbol|side[@tf]). Persisted via
// FrozenATRRecord.StructuralBoundary.
var (
	frozenStructMu    sync.Mutex
	frozenStructCache = map[string]frozenStructEntry{}
)

// frozenATRForPosition returns a stable ATR for a position keyed by symbol.
// First call (at open) computes fresh ATR and freezes it against the entry
// price; subsequent calls (reconcile cycles) reuse the frozen value. When the
// entry price changes (a new position on the same symbol) it recomputes and
// re-freezes.
//
// The frozen value is persisted to the store so a process restart reuses the
// open-time ATR instead of re-freezing at the then-current (drifted) ATR. This
// keeps ATR-mode activation/callback stable for the life of the position; the
// in-memory cache is a fast path in front of the persisted record.
func (at *AutoTrader) frozenATRForPosition(symbol, side string, entryPrice float64, cfg store.ATRProtectionConfig) (float64, bool) {
	key := frozenATRKey(at.id, symbol, cfg.WithDefaults().Timeframe, side)
	frozenATRMu.Lock()
	ent, ok := frozenATRCache[key]
	frozenATRMu.Unlock()
	if ok && entryPrice > 0 && entrySamePosition(ent.entryPrice, entryPrice) && ent.atr > 0 {
		return ent.atr, true
	}

	// Cache miss (cold start / restart): try the persisted record before
	// recomputing, so a restart does not re-freeze against today's ATR.
	if entryPrice > 0 && at.store != nil {
		if state, err := at.store.LoadFrozenATRState(); err == nil {
			if rec, found := state.Records[key]; found && rec.ATR > 0 && entrySamePosition(rec.EntryPrice, entryPrice) {
				frozenATRMu.Lock()
				frozenATRCache[key] = frozenATREntry{entryPrice: rec.EntryPrice, atr: rec.ATR}
				frozenATRMu.Unlock()
				return rec.ATR, true
			}
		}
	}

	atr, ok := at.atrForProtection(symbol, cfg)
	if !ok {
		return 0, false
	}
	frozenATRMu.Lock()
	frozenATRCache[key] = frozenATREntry{entryPrice: entryPrice, atr: atr}
	frozenATRMu.Unlock()
	// Persist the freshly frozen ATR so it survives a restart.
	if entryPrice > 0 && at.store != nil {
		rec := store.FrozenATRRecord{TraderID: at.id, Symbol: symbol, EntryPrice: entryPrice, ATR: atr, UpdatedAt: time.Now().Unix()}
		if err := at.store.SaveFrozenATRRecord(key, rec); err != nil {
			logger.Warnf("⚠️ Frozen ATR: failed to persist %s: %v", key, err)
		}
	}
	return atr, true
}

// frozenATRKey builds the cache/persistence key for a position's frozen ATR.
// The 1h timeframe (the protection default) keeps the legacy "id|symbol" form so
// existing persisted records and the ETH-drift fix are untouched. Other
// timeframes (e.g. the breadth from-peak path's 4h scale) get a "@tf" suffix so
// a position can hold an independent frozen ATR per timeframe without collision.
// side ("LONG"/"SHORT") is part of the key so a symbol held simultaneously on
// both sides (hedge positions) freezes an independent ATR/boundary per side.
// Without it, LONG and SHORT share one slot: because entrySamePosition guards on
// entry price and the two legs have different entries, each reconcile pass evicts
// the other's frozen value and recomputes against today's (drifted) ATR — which
// makes the structural close-confirm backstop price wobble and the reconciler
// churn duplicate stops. An empty side falls back to the legacy (side-less) key
// so callers without side context (and persisted pre-migration records) still
// resolve.
func frozenATRKey(traderID, symbol, timeframe, side string) string {
	base := traderID + "|" + symbol
	if s := normalizeFrozenSide(side); s != "" {
		base += "|" + s
	}
	if timeframe == "" || timeframe == "1h" {
		return base
	}
	return base + "@" + timeframe
}

// normalizeFrozenSide maps assorted side/action spellings to "LONG"/"SHORT" (or
// "" when unknown, which selects the legacy side-less key).
func normalizeFrozenSide(side string) string {
	switch strings.ToUpper(strings.TrimSpace(side)) {
	case "LONG", "BUY", "OPEN_LONG":
		return "LONG"
	case "SHORT", "SELL", "OPEN_SHORT":
		return "SHORT"
	default:
		return ""
	}
}

// entrySamePosition reports whether two entry prices refer to the same position
// (within 0.05% — covers minor fill-price rounding between open and reconcile).
func entrySamePosition(a, b float64) bool {
	if a <= 0 || b <= 0 {
		return false
	}
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff/b <= 0.0005
}

// atrFor1h returns the Wilder ATR(period) for the symbol on the configured
// timeframe, fetched fresh from market data. Returns (atr, ok).
func (at *AutoTrader) atrForProtection(symbol string, cfg store.ATRProtectionConfig) (float64, bool) {
	c := cfg.WithDefaults()
	bars, err := market.GetKlines(symbol, c.Timeframe, at.exchange, c.ATRPeriod*4+10)
	if err != nil || len(bars) < c.ATRPeriod+1 {
		return 0, false
	}
	highs := make([]float64, len(bars))
	lows := make([]float64, len(bars))
	closes := make([]float64, len(bars))
	for i, b := range bars {
		highs[i] = b.High
		lows[i] = b.Low
		closes[i] = b.Close
	}
	atr := wilderATRLast(highs, lows, closes, c.ATRPeriod)
	if atr <= 0 {
		return 0, false
	}
	return atr, true
}

// frozenStructBoundaryForPosition returns the pre-entry range boundary price frozen
// at open (swing low for long / swing high for short), used by the structural stop.
// In-memory cache → persisted record → (only when allowCompute) fresh compute + freeze.
//
// CRITICAL: allowCompute must be TRUE only at genuine position ENTRY. The boundary
// lives in the PRE-entry window; computing it later (reconcile / guard) would read a
// post-entry window and produce a wrong level that could trigger an unexpected exit.
// The reconcile path and the close-confirm guard pass allowCompute=false, so a
// position that never had a boundary frozen at entry (e.g. opened before the feature
// was enabled) is left to the resting backstop instead of a fabricated level.
func (at *AutoTrader) frozenStructBoundaryForPosition(symbol string, entryPrice float64, isLong bool, allowCompute bool, sscfg store.StructuralSLConfig, acfg store.ATRProtectionConfig) (float64, bool) {
	tf := acfg.WithDefaults().Timeframe
	side := "SHORT"
	if isLong {
		side = "LONG"
	}
	key := frozenATRKey(at.id, symbol, tf, side)

	frozenStructMu.Lock()
	ent, ok := frozenStructCache[key]
	frozenStructMu.Unlock()
	// Cache hit only counts when it belongs to THIS position. Guarding on entry
	// price stops a new position from inheriting a prior (not-yet-evicted) boundary
	// on the same key. entryPrice<=0 (callers without price context) trusts the hit.
	if ok && (entryPrice <= 0 || ent.entryPrice <= 0 || entrySamePosition(ent.entryPrice, entryPrice)) {
		return ent.boundary, ent.boundary > 0
	}

	// Persisted record (survives restart), guarded by entry price to avoid stale reuse.
	if entryPrice > 0 && at.store != nil {
		if state, err := at.store.LoadFrozenATRState(); err == nil {
			if rec, found := state.Records[key]; found && rec.StructuralBoundary > 0 &&
				entrySamePosition(rec.EntryPrice, entryPrice) {
				frozenStructMu.Lock()
				frozenStructCache[key] = frozenStructEntry{entryPrice: rec.EntryPrice, boundary: rec.StructuralBoundary}
				frozenStructMu.Unlock()
				return rec.StructuralBoundary, true
			}
		}
	}

	// Only compute a fresh boundary at genuine entry. Reconcile/guard get "none".
	if !allowCompute {
		return 0, false
	}

	// Fresh compute from the pre-entry window.
	boundary, ok := at.computeStructuralBoundary(symbol, entryPrice, isLong, sscfg, acfg)
	if !ok {
		// Cache the "none" result so we don't re-fetch every reconcile cycle.
		frozenStructMu.Lock()
		frozenStructCache[key] = frozenStructEntry{entryPrice: entryPrice, boundary: 0}
		frozenStructMu.Unlock()
		return 0, false
	}
	frozenStructMu.Lock()
	frozenStructCache[key] = frozenStructEntry{entryPrice: entryPrice, boundary: boundary}
	frozenStructMu.Unlock()

	// Persist onto the existing frozen-ATR record (same key) so both survive restart.
	if entryPrice > 0 && at.store != nil {
		if state, err := at.store.LoadFrozenATRState(); err == nil {
			rec := state.Records[key]
			rec.TraderID, rec.Symbol, rec.EntryPrice = at.id, symbol, entryPrice
			rec.StructuralBoundary = boundary
			rec.UpdatedAt = time.Now().Unix()
			if err := at.store.SaveFrozenATRRecord(key, rec); err != nil {
				logger.Warnf("⚠️ Structural SL: failed to persist boundary %s: %v", key, err)
			}
		}
	}
	return boundary, true
}

// computeStructuralBoundary fetches the ATR-timeframe bars and returns the NEAREST
// pre-entry swing level BEYOND entry (the closest overhead swing high for a short /
// the closest swing low below entry for a long), over the lookback window ending at
// the most recent CLOSED bar (excludes the entry-forming bar). Returns ok=false when
// no such level is on the correct side of entry (e.g. breakout entry above the range)
// — no structural edge, caller falls back to the fixed backstop.
//
// Why NEAREST swing, not the window ABSOLUTE extreme (fix 2026-07): the absolute
// extreme can be a distant large-degree spike (e.g. a short entered after a big drop
// from a peak far above). Anchoring to it pushed the stop out to the backstop
// (~11%+) even though a much closer, valid invalidation level existed just above
// entry. The structural stop must be the NEAREST invalidation, not the biggest.
func (at *AutoTrader) computeStructuralBoundary(symbol string, entryPrice float64, isLong bool, sscfg store.StructuralSLConfig, acfg store.ATRProtectionConfig) (float64, bool) {
	c := acfg.WithDefaults()
	ss := sscfg.WithDefaults()
	// Fetch a little more than the lookback so we can drop the live/forming bar.
	bars, err := market.GetKlines(symbol, c.Timeframe, at.exchange, ss.LookbackBars+4)
	if err != nil || len(bars) < 4 {
		return 0, false
	}
	// Use the last LookbackBars CLOSED bars (drop the final, still-forming bar).
	end := len(bars) - 1
	start := end - ss.LookbackBars
	if start < 0 {
		start = 0
	}
	window := bars[start:end]
	k := ss.PivotStrength
	if k < 1 {
		k = 1
	}

	// nearestSwing scans the window for fractal pivots (a bar more extreme than k bars
	// on each side) on the correct side of entry, and returns the one CLOSEST to entry.
	// For a short we want swing HIGHS above entry; for a long swing LOWS below entry.
	nearestSwing := func() (float64, bool) {
		best := 0.0
		found := false
		for i := k; i < len(window)-k; i++ {
			if !isLong {
				h := window[i].High
				if h <= entryPrice {
					continue
				}
				isPivot := true
				for j := i - k; j <= i+k; j++ {
					if j != i && window[j].High > h {
						isPivot = false
						break
					}
				}
				if isPivot && (!found || h < best) { // closest swing high above entry
					best, found = h, true
				}
			} else {
				l := window[i].Low
				if l >= entryPrice {
					continue
				}
				isPivot := true
				for j := i - k; j <= i+k; j++ {
					if j != i && window[j].Low < l {
						isPivot = false
						break
					}
				}
				if isPivot && (!found || l > best) { // closest swing low below entry
					best, found = l, true
				}
			}
		}
		return best, found
	}

	if b, ok := nearestSwing(); ok {
		return b, true
	}

	// No qualifying fractal pivot (e.g. very short window or monotonic run). Fall back
	// to the window's nearest bar extreme on the correct side of entry — still the
	// CLOSEST level, not the absolute spike.
	best := 0.0
	found := false
	for i := 0; i < len(window); i++ {
		if !isLong {
			h := window[i].High
			if h > entryPrice && (!found || h < best) {
				best, found = h, true
			}
		} else {
			l := window[i].Low
			if l < entryPrice && (!found || l > best) {
				best, found = l, true
			}
		}
	}
	if !found || best <= 0 {
		return 0, false
	}
	return best, true
}

// ladderMaxTPTargetPct returns the largest take-profit target move (as a percent of
// entry) across all ladder rules, resolving ATR-unit targets to percent. Used as the
// reference the fallback structural stop is RR-capped against. Returns 0 when no
// positive TP target exists.
func ladderMaxTPTargetPct(rules []store.LadderTPSLRule, atr, entryPrice float64, acfg store.ATRProtectionConfig) float64 {
	maxPct := 0.0
	for _, r := range rules {
		if r.TakeProfitPct <= 0 {
			continue
		}
		pct := r.TakeProfitPct
		if r.TakeProfitUnit == store.ProtectionUnitATR {
			if p, ok := acfg.EffectivePercent(r.TakeProfitPct, atr, entryPrice); ok {
				pct = p
			} else {
				continue
			}
		}
		if pct > maxPct {
			maxPct = pct
		}
	}
	return maxPct
}

// fallbackMultWithRRCap returns the ATR multiple to use on the NO-NEAR-STRUCTURE
// when a take-profit target is known, further tightens it so the stop distance stays
// below FallbackRRCapRatio × TP% (i.e. RR >= 1/ratio). FloorATRMul is re-applied as a
// hard minimum afterwards so the RR cap never drives the stop into the noise floor.
func fallbackMultWithRRCap(ss store.StructuralSLConfig, atr, entryPrice, tpTargetPct float64) float64 {
	mult := ss.FallbackATRMul
	if ss.FallbackRRCapRatio > 0 && tpTargetPct > 0 && atr > 0 && entryPrice > 0 {
		atrPct := atr / entryPrice * 100.0 // one ATR expressed as a percent of entry
		if atrPct > 0 {
			capMult := (ss.FallbackRRCapRatio * tpTargetPct) / atrPct
			if capMult < mult {
				mult = capMult
			}
		}
	}
	if mult < ss.FloorATRMul {
		mult = ss.FloorATRMul // floor stays a hard minimum vs the RR cap
	}
	return mult
}

// structuralSLPercent converts the frozen boundary into an effective stop-loss
// percent-of-entry, clamped to [floor, backstop] ATR multiples. tpTargetPct is the
// entry's take-profit target move (%); pass 0 to skip the fallback RR cap. Returns
// (pct, ok).
func structuralSLPercent(entryPrice, boundary, atr, tpTargetPct float64, sscfg store.StructuralSLConfig, acfg store.ATRProtectionConfig) (float64, bool) {
	if entryPrice <= 0 || boundary <= 0 || atr <= 0 {
		return 0, false
	}
	ss := sscfg.WithDefaults()
	dist := entryPrice - boundary
	if dist < 0 {
		dist = -dist
	}
	mult := dist / atr // structural distance in ATR multiples
	if mult < ss.FloorATRMul {
		mult = ss.FloorATRMul
	}
	if mult > ss.BackstopATRMul {
		// No near structure (nearest invalidation is beyond the backstop). Rather than
		// park the tight exit at the wide backstop, fall back to FallbackATRMul,
		// additionally RR-capped against the TP target.
		mult = fallbackMultWithRRCap(ss, atr, entryPrice, tpTargetPct)
	}
	return acfg.EffectivePercent(mult, atr, entryPrice)
}

// clampStructuralBoundary maps a RAW pre-entry swing boundary to the ACTUAL
// price the structural stop enforces, by clamping the entry→boundary distance to
// [FloorATRMul, BackstopATRMul] ATR — the same clamp structuralSLPercent applies
// to the resting order. Both the close-confirm guard and the UI must use THIS,
// not the raw swing, so the tight structural level never sits BEYOND the wide
// backstop (which would make the guard unreachable and the panel misleading).
// tpTargetPct is the entry's take-profit target move (%); pass 0 to skip the
// fallback RR cap. Returns (clampedPrice, true) when inputs are valid; else (raw, false).
func clampStructuralBoundary(entryPrice, rawBoundary, atr, tpTargetPct float64, isLong bool, sscfg store.StructuralSLConfig) (float64, bool) {
	if entryPrice <= 0 || rawBoundary <= 0 || atr <= 0 {
		return rawBoundary, false
	}
	ss := sscfg.WithDefaults()
	dist := entryPrice - rawBoundary
	if dist < 0 {
		dist = -dist
	}
	mult := dist / atr
	if mult < ss.FloorATRMul {
		mult = ss.FloorATRMul
	}
	if mult > ss.BackstopATRMul {
		// No near structure: fall back to FallbackATRMul (RR-capped against TP),
		// mirroring structuralSLPercent so the guard trigger and the UI show the same
		// tighter fallback level, not the wide backstop.
		mult = fallbackMultWithRRCap(ss, atr, entryPrice, tpTargetPct)
	}
	clampedDist := mult * atr
	if isLong {
		return entryPrice - clampedDist, true
	}
	return entryPrice + clampedDist, true
}

// wilderATRLast computes Wilder ATR(period) and returns the last value.
func wilderATRLast(highs, lows, closes []float64, period int) float64 {
	n := len(closes)
	if period <= 0 || n < period+1 {
		return 0
	}
	trs := make([]float64, 0, n-1)
	for i := 1; i < n; i++ {
		hl := highs[i] - lows[i]
		hc := absFloat(highs[i] - closes[i-1])
		lc := absFloat(lows[i] - closes[i-1])
		tr := hl
		if hc > tr {
			tr = hc
		}
		if lc > tr {
			tr = lc
		}
		trs = append(trs, tr)
	}
	if len(trs) < period {
		return 0
	}
	var sum float64
	for i := 0; i < period; i++ {
		sum += trs[i]
	}
	atr := sum / float64(period)
	for i := period; i < len(trs); i++ {
		atr = (atr*float64(period-1) + trs[i]) / float64(period)
	}
	return atr
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// resolveATRProtection returns a copy of the strategy's ProtectionConfig with
// each ATR-UNIT field (per-rule TakeProfitUnit/StopLossUnit/TriggerUnit/
// MinProfitUnit == "atr") converted to an effective percent using the frozen
// ATR. Percent-unit fields are left untouched. Returns (adjusted, true) when at
// least one field was ATR-resolved; (original, false) otherwise. This replaces
// the former global per-dimension overlay: the unit now lives on each rule, so
// the UI manages everything in the original TP/SL/BE/DD panels.
// atEntry must be true only when called at genuine position open (entry path). It
// gates fresh structural-boundary computation so the reconcile path never fabricates
// a boundary from a post-entry window.
func (at *AutoTrader) resolveATRProtection(entryPrice float64, symbol, action string, atEntry bool) (store.ProtectionConfig, bool) {
	if at.config.StrategyConfig == nil {
		return store.ProtectionConfig{}, false
	}
	base := at.config.StrategyConfig.Protection
	acfg := at.config.StrategyConfig.ATRProtection
	if !acfg.Enabled || entryPrice <= 0 {
		return base, false
	}
	// Only fetch ATR if some field actually opts into ATR units.
	if !protectionUsesATR(base) {
		return base, false
	}
	atr, ok := at.frozenATRForPosition(symbol, action, entryPrice, acfg)
	if !ok {
		logger.Warnf("  ⚠️ ATR-protection: no ATR for %s; falling back to configured percents", symbol)
		return base, false
	}

	adj := base // value copy; nested slices copied below before mutation
	applied := false

	// Ladder TP/SL: resolve each rule's TP and SL distance when its unit is "atr" or
	// "structural". Structural SL is derived from the frozen pre-entry range boundary.
	if len(base.LadderTPSL.Rules) > 0 {
		rules := make([]store.LadderTPSLRule, len(base.LadderTPSL.Rules))
		copy(rules, base.LadderTPSL.Rules)
		ss := base.LadderTPSL.StructuralSL.WithDefaults()
		isLong := action == "open_long"
		// TP target the fallback structural stop is RR-capped against (max across tiers).
		tpTargetPct := ladderMaxTPTargetPct(base.LadderTPSL.Rules, atr, entryPrice, acfg)
		// Resolve the structural stop percent ONCE (shared by all structural SL rules).
		structPct, structOK := 0.0, false
		if base.LadderTPSL.StructuralSL.Enabled {
			if boundary, ok := at.frozenStructBoundaryForPosition(symbol, entryPrice, isLong, atEntry, base.LadderTPSL.StructuralSL, acfg); ok {
				structPct, structOK = structuralSLPercent(entryPrice, boundary, atr, tpTargetPct, base.LadderTPSL.StructuralSL, acfg)
			}
			// Phase 2: when close-confirm is on, the RESTING stop is parked at the wide
			// backstop (bot-downtime safety net); the tight structural level is enforced
			// by the engine poll (runStructuralSLGuard) instead of the resting order.
			if ss.CloseConfirm {
				if pct, ok := acfg.EffectivePercent(ss.BackstopATRMul, atr, entryPrice); ok {
					structPct, structOK = pct, true
				}
			}
		}
		for i := range rules {
			switch rules[i].StopLossUnit {
			case store.ProtectionUnitATR:
				if rules[i].StopLossPct > 0 {
					if pct, ok := acfg.EffectivePercent(rules[i].StopLossPct, atr, entryPrice); ok {
						rules[i].StopLossPct = pct
						applied = true
					}
				}
			case store.ProtectionUnitStructural:
				if structOK {
					rules[i].StopLossPct = structPct
					applied = true
				} else if rules[i].StopLossPct > 0 {
					// Fallback: no structural boundary (breakout entry / no data) — treat
					// the configured value as an ATR multiple backstop.
					if pct, ok := acfg.EffectivePercent(rules[i].StopLossPct, atr, entryPrice); ok {
						rules[i].StopLossPct = pct
						applied = true
					}
				}
			}
			if rules[i].TakeProfitUnit == store.ProtectionUnitATR && rules[i].TakeProfitPct > 0 {
				if pct, ok := acfg.EffectivePercent(rules[i].TakeProfitPct, atr, entryPrice); ok {
					rules[i].TakeProfitPct = pct
					applied = true
				}
			}
		}
		adj.LadderTPSL.Rules = rules
	}

	// Break-even tiers: resolve trigger when its unit is "atr" (profit_pct mode).
	if len(base.BreakEvenStop.Rules) > 0 {
		beRules := make([]store.BreakEvenStopRule, len(base.BreakEvenStop.Rules))
		copy(beRules, base.BreakEvenStop.Rules)
		for i := range beRules {
			if beRules[i].TriggerUnit == store.ProtectionUnitATR &&
				beRules[i].TriggerMode == store.BreakEvenTriggerProfitPct && beRules[i].TriggerValue > 0 {
				if pct, ok := acfg.EffectivePercent(beRules[i].TriggerValue, atr, entryPrice); ok {
					beRules[i].TriggerValue = pct
					applied = true
				}
			}
			// Offset shares the rule's TriggerUnit (sign preserved for losing-side BE).
			if beRules[i].TriggerUnit == store.ProtectionUnitATR && beRules[i].OffsetPct != 0 {
				if pct, ok := atrOffsetEffectivePercent(acfg, beRules[i].OffsetPct, atr, entryPrice); ok {
					beRules[i].OffsetPct = pct
					applied = true
				}
			}
		}
		adj.BreakEvenStop.Rules = beRules
	}

	// Drawdown: resolve each rule's min-profit ARM threshold when its unit is
	// "atr". Max-drawdown give-back stays a % of peak (a ratio, not a distance).
	if len(base.DrawdownTakeProfit.Rules) > 0 {
		ddRules := make([]store.DrawdownTakeProfitRule, len(base.DrawdownTakeProfit.Rules))
		copy(ddRules, base.DrawdownTakeProfit.Rules)
		for i := range ddRules {
			if ddRules[i].MinProfitUnit == store.ProtectionUnitATR && ddRules[i].MinProfitPct > 0 {
				if pct, ok := acfg.EffectivePercent(ddRules[i].MinProfitPct, atr, entryPrice); ok {
					ddRules[i].MinProfitPct = pct
					applied = true
				}
			}
		}
		adj.DrawdownTakeProfit.Rules = ddRules
	}

	if applied {
		logger.Infof("  🎯 ATR-units resolved for %s: ATR(%s)=%.6f entry=%.6f → per-field ATR→%% (SL/TP/BE/DD)",
			symbol, acfg.WithDefaults().Timeframe, atr, entryPrice)
	}
	return adj, applied
}

// protectionUsesATR reports whether any TP/SL/BE/DD field opts into ATR units,
// so we skip the ATR fetch entirely for pure-percent strategies.
func protectionUsesATR(p store.ProtectionConfig) bool {
	for _, r := range p.LadderTPSL.Rules {
		if r.StopLossUnit == store.ProtectionUnitATR || r.TakeProfitUnit == store.ProtectionUnitATR ||
			r.StopLossUnit == store.ProtectionUnitStructural {
			return true
		}
	}
	for _, r := range p.BreakEvenStop.Rules {
		if r.TriggerUnit == store.ProtectionUnitATR {
			return true
		}
	}
	for _, r := range p.DrawdownTakeProfit.Rules {
		if r.MinProfitUnit == store.ProtectionUnitATR || r.MaxDrawdownUnit == store.ProtectionUnitATR {
			return true
		}
	}
	return false
}

// resolveDrawdownRulesATR resolves each drawdown rule's MinProfitPct to a
// percent when its MinProfitUnit is "atr", so the runtime DD arm threshold
// matches the open-time distance (fixes the prior bug where DD armed on the raw
// percent while open-time ATR-ized it). Percent-unit rules pass through.
func (at *AutoTrader) resolveDrawdownRulesATR(rules []store.DrawdownTakeProfitRule, symbol, side string, entryPrice float64) []store.DrawdownTakeProfitRule {
	if len(rules) == 0 || at.config.StrategyConfig == nil {
		return rules
	}
	acfg := at.config.StrategyConfig.ATRProtection
	if !acfg.Enabled || entryPrice <= 0 {
		return rules
	}
	anyATR := false
	for _, r := range rules {
		if r.MinProfitUnit == store.ProtectionUnitATR || r.MaxDrawdownUnit == store.ProtectionUnitATR {
			anyATR = true
			break
		}
	}
	if !anyATR {
		return rules
	}
	atr, ok := at.frozenATRForPosition(symbol, side, entryPrice, acfg)
	if !ok {
		return rules
	}
	out := make([]store.DrawdownTakeProfitRule, len(rules))
	copy(out, rules)
	for i := range out {
		if out[i].MinProfitUnit == store.ProtectionUnitATR && out[i].MinProfitPct > 0 {
			if pct, ok := acfg.EffectivePercent(out[i].MinProfitPct, atr, entryPrice); ok {
				out[i].MinProfitPct = pct
			}
		}
		if out[i].MaxDrawdownUnit == store.ProtectionUnitATR && out[i].MaxDrawdownPct > 0 {
			if pct, ok := acfg.EffectivePercent(out[i].MaxDrawdownPct, atr, entryPrice); ok {
				out[i].MaxDrawdownPct = pct
			}
		}
	}
	return out
}

// getActiveBreakEvenRulesATR returns the active break-even rules with each
// rule's trigger value resolved to a percent when its TriggerUnit is "atr", so
// the runtime BE monitor arms at the SAME distance the orders were placed with
// at open (no percent/ATR mismatch). Percent-unit rules pass through unchanged.
func (at *AutoTrader) getActiveBreakEvenRulesATR(symbol, side string, entryPrice float64) []store.BreakEvenStopRule {
	rules := at.getActiveBreakEvenRules()
	if len(rules) == 0 {
		return rules
	}
	if at.config.StrategyConfig == nil {
		return rules
	}
	acfg := at.config.StrategyConfig.ATRProtection
	if !acfg.Enabled || entryPrice <= 0 {
		return rules
	}
	// Skip the ATR fetch unless at least one rule uses ATR units.
	anyATR := false
	for _, r := range rules {
		if r.TriggerUnit == store.ProtectionUnitATR {
			anyATR = true
			break
		}
	}
	if !anyATR {
		return rules
	}
	atr, ok := at.frozenATRForPosition(symbol, side, entryPrice, acfg)
	if !ok {
		return rules
	}
	out := make([]store.BreakEvenStopRule, len(rules))
	copy(out, rules)
	for i := range out {
		if out[i].TriggerUnit == store.ProtectionUnitATR &&
			out[i].TriggerMode == store.BreakEvenTriggerProfitPct && out[i].TriggerValue > 0 {
			if pct, ok := acfg.EffectivePercent(out[i].TriggerValue, atr, entryPrice); ok {
				out[i].TriggerValue = pct
			}
		}
		// OffsetPct shares the rule's TriggerUnit: when the trigger is in ATR units
		// so is the offset (an ATR multiple of entry). Sign is preserved so a
		// negative offset (park the stop slightly losing-side) stays negative.
		if out[i].TriggerUnit == store.ProtectionUnitATR && out[i].OffsetPct != 0 {
			if pct, ok := atrOffsetEffectivePercent(acfg, out[i].OffsetPct, atr, entryPrice); ok {
				out[i].OffsetPct = pct
			}
		}
	}
	return out
}

// atrOffsetEffectivePercent resolves a signed ATR-multiple offset to an effective
// percent. EffectivePercent only accepts a positive multiple (and clamps to the
// configured min/max effective percent), so the sign is stripped, the magnitude
// resolved, and the sign reattached — preserving negative offsets (which park a
// break-even stop slightly on the losing side: cover fees + a noise buffer).
func atrOffsetEffectivePercent(acfg store.ATRProtectionConfig, atrMultiple, atrValue, entryPrice float64) (float64, bool) {
	sign := 1.0
	mag := atrMultiple
	if mag < 0 {
		sign = -1.0
		mag = -mag
	}
	pct, ok := acfg.EffectivePercent(mag, atrValue, entryPrice)
	if !ok {
		return 0, false
	}
	return sign * pct, true
}
