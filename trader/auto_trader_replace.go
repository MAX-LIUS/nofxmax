package trader

import (
	"fmt"
	"nofx/kernel"
	"nofx/logger"
	"nofx/market"
	"nofx/store"
	"strings"
	"time"
)

// replaceWeakestConfig holds the resolved (defaulted) strong-signal replacement parameters.
type replaceWeakestConfig struct {
	Enabled             bool
	MinConfidence       int     // new-signal absolute confidence floor
	MinHoldMinutes      int     // victim must be held at least this long
	MaxVictimProfitPct  float64 // never cut a winner above this pnl%
	MinConfidenceMargin int     // new conf must beat victim entry conf by this many points
}

// getReplaceWeakestConfig resolves replacement config with safe defaults. Returns Enabled=false
// when the feature is off so callers can cheaply skip the whole path.
func (at *AutoTrader) getReplaceWeakestConfig() replaceWeakestConfig {
	cfg := replaceWeakestConfig{}
	if at == nil || at.config.StrategyConfig == nil {
		return cfg
	}
	rc := at.config.StrategyConfig.RiskControl
	if !rc.ReplaceWeakestEnabled {
		return cfg
	}
	cfg.Enabled = true

	cfg.MinConfidence = rc.ReplaceMinConfidence
	if cfg.MinConfidence <= 0 {
		cfg.MinConfidence = 80 // default: only very high-conviction signals may evict
	}
	cfg.MinHoldMinutes = rc.ReplaceMinHoldMinutes
	if cfg.MinHoldMinutes <= 0 {
		cfg.MinHoldMinutes = 30 // default: never churn a position younger than 30m
	}
	cfg.MaxVictimProfitPct = rc.ReplaceMaxVictimProfitPct
	if cfg.MaxVictimProfitPct <= 0 {
		cfg.MaxVictimProfitPct = 1.0 // default: protect any position up >= +1%
	}
	cfg.MinConfidenceMargin = rc.ReplaceMinConfidenceMargin
	if cfg.MinConfidenceMargin < 0 {
		cfg.MinConfidenceMargin = 0
	}
	return cfg
}

// replaceVictim describes a position eligible to be cut to free a slot.
type replaceVictim struct {
	Symbol      string
	Side        string // "long" / "short"
	NotionalUSD float64
	HoldMinutes float64
	PnLPct      float64
	EntryConf   int
	Score       float64
}

// selectReplaceVictim ranks the current positions and returns the weakest one eligible for
// eviction, or nil when none qualify. Ranking favors (smaller notional + longer hold + weaker
// pnl). Positions in the incoming symbol, profitable runners, and freshly-opened positions are
// excluded. Pure-ish: reads at.positionFirstSeenTime but performs no side effects.
func (at *AutoTrader) selectReplaceVictim(positions []map[string]interface{}, newSymbol string, cfg replaceWeakestConfig) *replaceVictim {
	newSymbolNorm := market.Normalize(newSymbol)
	candidates := make([]replaceVictim, 0, len(positions))

	for _, pos := range positions {
		symbol, _ := pos["symbol"].(string)
		side, _ := pos["side"].(string)
		if symbol == "" || side == "" {
			continue
		}
		// Never evict a position in the same symbol the new signal targets.
		if market.Normalize(symbol) == newSymbolNorm {
			continue
		}

		markPrice, _ := pos["markPrice"].(float64)
		amt, _ := pos["positionAmt"].(float64)
		if amt < 0 {
			amt = -amt
		}
		notional := amt * markPrice

		unrealized, _ := pos["unRealizedProfit"].(float64)
		leverage := 10.0
		if lev, ok := pos["leverage"].(float64); ok && lev > 0 {
			leverage = lev
		}
		marginUsed := 0.0
		if leverage > 0 {
			marginUsed = notional / leverage
		}
		pnlPct := 0.0
		if marginUsed > 0 {
			pnlPct = unrealized / marginUsed * 100
		}

		// Protect profitable runners — let winners run.
		if pnlPct >= cfg.MaxVictimProfitPct {
			continue
		}

		holdMinutes := at.positionHoldMinutes(symbol, side)
		// Protect freshly-opened positions from churn.
		if holdMinutes < float64(cfg.MinHoldMinutes) {
			continue
		}

		candidates = append(candidates, replaceVictim{
			Symbol:      symbol,
			Side:        side,
			NotionalUSD: notional,
			HoldMinutes: holdMinutes,
			PnLPct:      pnlPct,
			EntryConf:   at.lookupEntryConfidence(symbol, side),
		})
	}

	if len(candidates) == 0 {
		return nil
	}

	// Normalize each dimension across the candidate set, then weight. Smaller notional, longer
	// hold, and weaker (more negative) pnl all increase the eviction score.
	minNotional, maxNotional := candidates[0].NotionalUSD, candidates[0].NotionalUSD
	minHold, maxHold := candidates[0].HoldMinutes, candidates[0].HoldMinutes
	minPnL, maxPnL := candidates[0].PnLPct, candidates[0].PnLPct
	for _, c := range candidates {
		minNotional, maxNotional = minf(minNotional, c.NotionalUSD), maxf(maxNotional, c.NotionalUSD)
		minHold, maxHold = minf(minHold, c.HoldMinutes), maxf(maxHold, c.HoldMinutes)
		minPnL, maxPnL = minf(minPnL, c.PnLPct), maxf(maxPnL, c.PnLPct)
	}

	norm := func(v, lo, hi float64, invert bool) float64 {
		if hi <= lo {
			return 0.5 // single value or degenerate range — neutral
		}
		x := (v - lo) / (hi - lo)
		if invert {
			x = 1 - x
		}
		return x
	}

	const (
		wSize = 0.4 // prefer smaller positions
		wHold = 0.3 // prefer longer-held positions
		wPnL  = 0.3 // prefer weaker pnl
	)

	best := -1.0
	var victim *replaceVictim
	for i := range candidates {
		c := &candidates[i]
		sizeScore := norm(c.NotionalUSD, minNotional, maxNotional, true) // smaller = higher
		holdScore := norm(c.HoldMinutes, minHold, maxHold, false)        // longer = higher
		pnlScore := norm(c.PnLPct, minPnL, maxPnL, true)                 // weaker = higher
		c.Score = wSize*sizeScore + wHold*holdScore + wPnL*pnlScore
		if c.Score > best {
			best = c.Score
			victim = c
		}
	}
	return victim
}

// positionHoldMinutes returns how long the symbol/side position has been held, in minutes.
// Falls back to 0 when the open time is unknown (treated as fresh → protected by min-hold guard).
func (at *AutoTrader) positionHoldMinutes(symbol, side string) float64 {
	key := positionKey(symbol, side)
	openedMs, ok := at.positionFirstSeenTime[key]
	if !ok || openedMs <= 0 {
		// Best-effort fallback to the persisted entry time.
		if at.store != nil {
			if openPos, err := at.store.Position().GetOpenPositionBySymbol(at.id, market.Normalize(symbol), strings.ToUpper(side)); err == nil && openPos != nil && openPos.EntryTime > 0 {
				openedMs = openPos.EntryTime
			}
		}
	}
	if openedMs <= 0 {
		return 0
	}
	return float64(time.Now().UnixMilli()-openedMs) / 60000.0
}

// lookupEntryConfidence best-effort reads the AI confidence recorded when the position was opened.
// Returns 0 when unavailable (so the confidence-margin guard degrades to "no extra requirement").
func (at *AutoTrader) lookupEntryConfidence(symbol, side string) int {
	if at.store == nil {
		return 0
	}
	openPos, err := at.store.Position().GetOpenPositionBySymbol(at.id, market.Normalize(symbol), strings.ToUpper(side))
	if err != nil || openPos == nil {
		return 0
	}
	decisionStore := at.store.Decision()
	if decisionStore == nil {
		return 0
	}
	record, err := decisionStore.GetRecordByCycle(at.id, openPos.EntryDecisionCycle)
	if err != nil || record == nil {
		return 0
	}
	action := findMatchedDecisionAction(record, symbol, sideToOpenAction(strings.ToUpper(side)))
	if action == nil {
		return 0
	}
	return action.Confidence
}

// tryFreeSlotForEntry attempts to free one position slot for an incoming high-conviction entry by
// closing the weakest eligible position. It returns true only when a victim was successfully
// closed (slot freed). It performs the cheap, balance-independent prechecks itself (the
// signal-strength floor) but the caller is responsible for entry-price-deviation precheck before
// calling, so a victim is never cut for an order that would be rejected anyway.
//
// Coordination guarantee: the cut runs synchronously and the position is confirmed gone (re-fetch
// in the caller + re-enforce max positions) before the open proceeds. If the cut fails, false is
// returned and the caller surfaces the original max-positions error — no open is attempted.
func (at *AutoTrader) tryFreeSlotForEntry(decision *kernel.Decision, side string, positions []map[string]interface{}) bool {
	cfg := at.getReplaceWeakestConfig()
	if !cfg.Enabled {
		return false
	}

	// Signal-strength floor: only very high-conviction entries may evict an existing position.
	if decision.Confidence < cfg.MinConfidence {
		logger.Infof("  🔁 [REPLACE] %s %s confidence %d < floor %d — not eligible to evict",
			decision.Symbol, side, decision.Confidence, cfg.MinConfidence)
		return false
	}

	victim := at.selectReplaceVictim(positions, decision.Symbol, cfg)
	if victim == nil {
		logger.Infof("  🔁 [REPLACE] no eligible victim to free a slot for %s %s (all positions protected: winners / too-new / same-symbol)",
			decision.Symbol, side)
		return false
	}

	// Confidence-margin guard: new signal must clearly beat the victim's entry conviction.
	if victim.EntryConf > 0 && decision.Confidence < victim.EntryConf+cfg.MinConfidenceMargin {
		logger.Infof("  🔁 [REPLACE] %s conf %d does not beat victim %s entry conf %d by margin %d — keeping victim",
			decision.Symbol, decision.Confidence, victim.Symbol, victim.EntryConf, cfg.MinConfidenceMargin)
		return false
	}

	logger.Infof("  🔁 [REPLACE] freeing slot: cutting weakest %s %s (notional=%.2f, held=%.0fm, pnl=%.2f%%, entryConf=%d, score=%.3f) for %s %s (conf=%d)",
		victim.Symbol, victim.Side, victim.NotionalUSD, victim.HoldMinutes, victim.PnLPct, victim.EntryConf, victim.Score,
		decision.Symbol, side, decision.Confidence)

	if err := at.closePositionForReplacement(victim); err != nil {
		logger.Warnf("  🔁 [REPLACE] failed to cut victim %s %s: %v — aborting replacement, slot NOT freed",
			victim.Symbol, victim.Side, err)
		return false
	}
	return true
}

// closePositionForReplacement closes a victim position via the proven close-with-record path so
// fills, fees, and position bookkeeping are recorded consistently. Tagged with a replacement
// close reason for attribution.
func (at *AutoTrader) closePositionForReplacement(victim *replaceVictim) error {
	action := "close_long"
	if strings.EqualFold(victim.Side, "short") {
		action = "close_short"
	}

	closeDecision := &kernel.Decision{
		Symbol:      victim.Symbol,
		Action:      action,
		CloseReason: "replaced_by_stronger_signal",
		Reasoning:   fmt.Sprintf("strong-signal replacement: evicted weakest slot (notional=%.2f, held=%.0fm, pnl=%.2f%%)", victim.NotionalUSD, victim.HoldMinutes, victim.PnLPct),
	}
	rec := &store.DecisionAction{
		Action:    action,
		Symbol:    victim.Symbol,
		Timestamp: time.Now().UTC(),
	}

	if action == "close_long" {
		return at.executeCloseLongWithRecord(closeDecision, rec)
	}
	return at.executeCloseShortWithRecord(closeDecision, rec)
}

// enforceCapacityWithReplacement is the single capacity gate for entries. When below MaxPositions
// it is a no-op (returns the positions unchanged). When at capacity it tries to free a slot via
// strong-signal replacement, then re-fetches positions and re-enforces the limit:
//
//  1. If under capacity            → pass through (no cut).
//  2. If at capacity, replacement OFF or signal too weak / no victim → return max-positions error.
//  3. If at capacity, a victim is cut → re-fetch positions, re-enforce. Only proceed when a slot
//     is genuinely free. This guarantees the open never proceeds on a stale "freed" assumption.
//
// The entry-price deviation precheck runs BEFORE any cut so a victim is never sacrificed for an
// order that would be rejected on price grounds anyway. currentPrice is the live execution price.
func (at *AutoTrader) enforceCapacityWithReplacement(decision *kernel.Decision, side string, positions []map[string]interface{}, currentPrice float64) ([]map[string]interface{}, error) {
	// Under capacity: nothing to do.
	capacityErr := at.enforceMaxPositions(len(positions))
	if capacityErr == nil {
		return positions, nil
	}

	cfg := at.getReplaceWeakestConfig()
	if !cfg.Enabled {
		return positions, capacityErr
	}

	// Cheap, balance-independent precheck: never cut a victim for an order that price-deviation
	// would reject. Mirrors the deviation guard applied later in the open path.
	if err := enforceEntryPriceDeviationWithMax(decision, currentPrice, side, at.getMaxEntryDeviationPct()); err != nil {
		logger.Infof("  🔁 [REPLACE] skipping replacement for %s %s: entry deviation precheck failed (%v)", decision.Symbol, side, err)
		return positions, capacityErr
	}

	if !at.tryFreeSlotForEntry(decision, side, positions) {
		return positions, capacityErr
	}

	// Re-fetch positions and re-enforce: only proceed when a slot is genuinely free now.
	refreshed, err := at.trader.GetPositions()
	if err != nil {
		logger.Warnf("  🔁 [REPLACE] cut succeeded but re-fetch failed for %s %s: %v — refusing to open on stale state", decision.Symbol, side, err)
		return positions, fmt.Errorf("replacement cut done but position re-fetch failed: %w", err)
	}
	if err := at.enforceMaxPositions(len(refreshed)); err != nil {
		logger.Warnf("  🔁 [REPLACE] still at capacity after cut for %s %s (%d positions) — close may not have settled yet, refusing open", decision.Symbol, side, len(refreshed))
		return refreshed, err
	}

	logger.Infof("  🔁 [REPLACE] slot freed, proceeding to open %s %s (%d positions remain)", decision.Symbol, side, len(refreshed))
	return refreshed, nil
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
