package trader

import (
	"encoding/json"
	"fmt"
	"time"

	"nofx/logger"
	"nofx/store"
)

// gbPosition is a per-position snapshot the giveback guard works on.
type gbPosition struct {
	symbol    string
	side      string
	entry     float64
	mark      float64
	quantity  float64 // absolute
	profitPct float64 // calculatePositionPnLPct
	unreal    float64 // unrealized PnL in quote ccy (profitPct/100 * entry * qty)
	peakPct   float64 // peak profit% from peakPnLCache (high-water)
}

// runGivebackGuard is the live portfolio giveback guard. The sole portfolio
// breaker is the breadth circuit breaker (gbApplyBreadth): it monitors every
// open position per-symbol and fires only when a MAJORITY retrace together (a
// correlated reversal), cutting only the losing positions while winners ride
// their break-even stop. The old L1/L2/L3 account-equity breakers were removed
// because they measured leverage-contaminated equity drawdown — a 0.5% wiggle
// at 10x looked like a 5% account hit and knocked the whole book out.
//
// Safety: disabled config => immediate no-op. DryRun => logs the intended trim
// without placing any order. Called at the top of checkPositionDrawdown so it
// shares the existing drawdown-monitor cadence (default 60s).
func (at *AutoTrader) runGivebackGuard() {
	if at.config.StrategyConfig == nil {
		return
	}
	cfg := at.config.StrategyConfig.Protection.GivebackGuard
	if !cfg.Enabled {
		return
	}

	positions, err := at.trader.GetPositions()
	if err != nil || len(positions) == 0 {
		return
	}

	// Build per-position snapshots (only positions with a usable mark).
	snaps := make([]gbPosition, 0, len(positions))
	for _, pos := range positions {
		symbol, _ := pos["symbol"].(string)
		side, _ := pos["side"].(string)
		entry, _ := pos["entryPrice"].(float64)
		mark, _ := pos["markPrice"].(float64)
		qty, _ := pos["positionAmt"].(float64)
		if qty < 0 {
			qty = -qty
		}
		if symbol == "" || side == "" || entry <= 0 || mark <= 0 || qty <= 0 {
			continue
		}
		profitPct := calculatePositionPnLPct(side, entry, mark)
		posKey := symbol + "_" + side
		at.peakPnLCacheMutex.RLock()
		peakPct, ok := at.peakPnLCache[posKey]
		at.peakPnLCacheMutex.RUnlock()
		if !ok {
			peakPct = profitPct
		}
		snaps = append(snaps, gbPosition{
			symbol: symbol, side: side, entry: entry, mark: mark, quantity: qty,
			profitPct: profitPct,
			unreal:    profitPct / 100.0 * entry * qty,
			peakPct:   peakPct,
		})
	}
	if len(snaps) == 0 {
		return
	}

	// Breadth breaker is the sole portfolio guard: per-symbol monitoring +
	// majority-retrace gate + cut losers only (winners ride break-even). The old
	// L1/L2/L3 account-equity breakers were removed (leverage-contaminated).
	if cfg.BreadthEnabled {
		at.gbApplyBreadth(cfg, snaps)
	}
}

// gbApplyBreadth is the live breadth circuit breaker — the redesign that
// supersedes the L1/L2/L3 account-equity breakers. It monitors every open
// position and fires only when a MAJORITY retrace together (a correlated
// reversal, not single-symbol noise): retracingCount/total >= BreadthFrac AND
// total >= BreadthMinPos. When it fires it cuts only the LOSING (profit% < 0)
// retracing positions in full (BreadthLoserCutPct); winning positions are left
// alone to ride their break-even stop. This mirrors the backtested
// applyBreadthBreaker() in trader/backtest/portfolio_sim.go.
//
// Leverage-free trigger: retracement is measured per-symbol (ATR-from-peak or
// peak-giveback%), never on account equity — so a 0.5% wiggle at 10x leverage
// can't knock the book out the way the equity-5% breaker did.
func (at *AutoTrader) gbApplyBreadth(cfg store.GivebackGuardConfig, snaps []gbPosition) {
	if cfg.BreadthFrac <= 0 || cfg.BreadthMinPos <= 0 || len(snaps) == 0 {
		return
	}
	window := cfg.BreadthVelWindow
	if window <= 0 {
		window = 6
	}
	const gbVelHistCap = 64
	const gbBreadthBarMs int64 = 3600_000

	at.gbGuardMutex.Lock()
	nowMs := time.Now().UnixMilli()
	newBar := nowMs-at.gbLastBreadthBarMs >= gbBreadthBarMs
	if at.gbPnlHist == nil {
		at.gbPnlHist = make(map[string][]float64)
	}
	// Sample per-position pnl velocity once per 1h bar; prune closed positions;
	// advance the breadth cooldown counter on the bar-clock.
	if newBar {
		liveKeys := make(map[string]bool, len(snaps))
		for _, s := range snaps {
			key := s.symbol + "_" + s.side
			liveKeys[key] = true
			h := append(at.gbPnlHist[key], s.profitPct)
			if len(h) > gbVelHistCap {
				h = h[len(h)-gbVelHistCap:]
			}
			at.gbPnlHist[key] = h
		}
		for key := range at.gbPnlHist {
			if !liveKeys[key] {
				delete(at.gbPnlHist, key)
			}
		}
		at.gbBreadthBarsSinceFire++
		at.gbLastBreadthBarMs = nowMs
	}
	persistNeeded := newBar // bar advance changed hist + bar clock; persist below

	// Resolve ATR% per symbol once (ATR mode only). We use the FROZEN open-time
	// ATR (frozenATRForPosition) rather than a fresh live fetch: the live fetch
	// could fail transiently (kline gap / API hiccup) and silently drop a symbol
	// from the retracing set, weakening the breaker exactly when markets are
	// disorderly. The frozen value is computed once at open and persisted, so it
	// survives restarts and is always available after the first successful resolve.
	// atrResolve returns (atrPct, frozen, ok): ok=false means ATR is genuinely
	// unavailable for this symbol this cycle (recorded as an ATR failure, never a
	// silent drop — see fail-safe handling in retracing()).
	atrPctCache := make(map[string]float64)
	atrOkCache := make(map[string]bool)
	atrFrozenCache := make(map[string]bool)
	acfg := at.config.StrategyConfig.ATRProtection
	atrResolve := func(symbol string, entry float64) (float64, bool, bool) {
		if v, ok := atrPctCache[symbol]; ok {
			return v, atrFrozenCache[symbol], atrOkCache[symbol]
		}
		v := 0.0
		ok := false
		frozen := false
		if entry > 0 {
			// Frozen open-time ATR first (stable, restart-safe, never silently 0).
			if atr, fok := at.frozenATRForPosition(symbol, entry, acfg); fok && atr > 0 {
				v = atr / entry * 100
				ok = true
				frozen = true
			} else if atr, lok := at.atrForProtection(symbol, acfg); lok && atr > 0 {
				// Fallback to a live fetch only if the frozen path is unavailable
				// (e.g. position predates frozen-ATR persistence).
				v = atr / entry * 100
				ok = true
			}
		}
		atrPctCache[symbol] = v
		atrOkCache[symbol] = ok
		atrFrozenCache[symbol] = frozen
		return v, frozen, ok
	}

	// atrFailures counts symbols whose ATR could not be resolved this cycle (ATR
	// mode only). Recorded into the event for later diagnosis; classification
	// fail-safe treats an unresolved ATR symbol as NON-retracing (so a data gap
	// can never manufacture a false breaker fire), but the failure is always
	// logged so it is visible rather than silent.
	atrFailures := 0
	atrFailSyms := make(map[string]bool)

	// retracing classifies a position as pulling back from its own peak. Both the
	// velocity path and the from-peak path are ATR-normalized, so BreadthVelEps and
	// BreadthATRMult mean the same thing across low- and high-volatility symbols.
	retracing := func(s gbPosition) bool {
		key := s.symbol + "_" + s.side
		hist := at.gbPnlHist[key]
		atrPct := 0.0
		if cfg.BreadthUseATR {
			var ok bool
			atrPct, _, ok = atrResolve(s.symbol, s.entry)
			if !ok && !atrFailSyms[s.symbol] {
				atrFailSyms[s.symbol] = true
				atrFailures++
				logger.Warnf("⚠️ [GivebackGuard Breadth] ATR unavailable for %s (frozen+live both failed) — treated as non-retracing this cycle (fail-safe)", s.symbol)
			}
		}
		// Velocity path: profit%/bar normalized by ATR% => "ATR-units lost per bar".
		// A symbol giving back faster than BreadthVelEps ATRs/bar counts as retracing.
		if len(hist) >= 2 {
			vel := gbPnlVelocity(hist, window)
			if atrPct > 0 {
				vel = vel / atrPct // raw %/bar -> ATR-units/bar
			}
			if vel < -cfg.BreadthVelEps {
				return true
			}
		}
		if cfg.BreadthUseATR {
			if atrPct <= 0 {
				return false // fail-safe: unresolved ATR never counts as retracing
			}
			return (s.peakPct-s.profitPct)/atrPct >= cfg.BreadthATRMult
		}
		return (s.peakPct - s.profitPct) >= cfg.BreadthGivebackPct
	}

	// Classify every position once and capture the full per-leg picture so the
	// decision is fully reconstructable from the recorded event (task: "把整个
	// 事件全貌细节都记录下来"). retracing() is evaluated exactly once per leg here.
	total := 0
	retr := 0
	legs := make([]store.BreadthEventLeg, 0, len(snaps))
	legRetr := make([]bool, len(snaps))
	for i, s := range snaps {
		total++
		isRetr := retracing(s)
		legRetr[i] = isRetr
		if isRetr {
			retr++
		}
		atrPct := 0.0
		atrFailed := false
		frozen := false
		if cfg.BreadthUseATR {
			var ok bool
			atrPct, frozen, ok = atrResolve(s.symbol, s.entry)
			atrFailed = !ok
		}
		legs = append(legs, store.BreadthEventLeg{
			Symbol:    s.symbol,
			Side:      s.side,
			Entry:     s.entry,
			Mark:      s.mark,
			ProfitPct: s.profitPct,
			PeakPct:   s.peakPct,
			Retracing: isRetr,
			ATRPct:    atrPct,
			ATRFailed: atrFailed,
			Frozen:    frozen,
		})
	}
	barsSinceFire := at.gbBreadthBarsSinceFire
	at.gbGuardMutex.Unlock()

	frac := 0.0
	if total > 0 {
		frac = float64(retr) / float64(total)
	}

	// recordEvent persists a full snapshot of this evaluation. Best-effort; never
	// blocks the guard. cut legs are marked by the caller before invoking on fire.
	recordEvent := func(outcome string, cutCount int) {
		if at.store == nil {
			return
		}
		legsJSON := "[]"
		if b, err := json.Marshal(legs); err == nil {
			legsJSON = string(b)
		}
		nowMs := time.Now().UnixMilli()
		evt := &store.BreadthEvent{
			TraderID:      at.id,
			ExchangeID:    at.exchangeID,
			Outcome:       outcome,
			Total:         total,
			Retracing:     retr,
			RetracingFrac: frac,
			ThresholdFrac: cfg.BreadthFrac,
			MinPos:        cfg.BreadthMinPos,
			CutCount:      cutCount,
			ATRFailCount:  atrFailures,
			BarsSinceFire: barsSinceFire,
			CooldownBars:  cfg.BreadthCooldownBars,
			CutWinners:    cfg.BreadthCutWinners,
			UseATR:        cfg.BreadthUseATR,
			LegsJSON:      legsJSON,
			ObservedAt:    nowMs,
			CreatedAt:     nowMs,
		}
		if err := at.store.BreadthEvent().Record(evt); err != nil {
			logger.Warnf("⚠️ [GivebackGuard Breadth] failed to record event: %v", err)
		}
	}

	if total < cfg.BreadthMinPos {
		// Below quorum. Only worth recording when something was retracing (so we
		// capture "near-misses that never reached quorum") or an ATR fetch failed
		// (so silent data gaps are visible). Quiet cycles are not persisted.
		if retr > 0 || atrFailures > 0 {
			recordEvent("no_quorum", 0)
		}
		return // no quorum: "majority" is meaningless with too few positions
	}
	if frac < cfg.BreadthFrac {
		// Quorum reached but the majority threshold was not — a genuine near-miss.
		// Always recorded so over/under-sensitivity of BreadthFrac is tunable.
		recordEvent("near_miss", 0)
		return // not a majority reversal: leave each symbol to its own SL/BE
	}
	if cfg.BreadthCooldownBars > 0 && barsSinceFire < cfg.BreadthCooldownBars {
		recordEvent("cooldown", 0)
		return
	}

	cutPct := cfg.BreadthLoserCutPct / 100.0
	if cutPct <= 0 {
		cutPct = 1.0 // default: full cut of losers
	}
	if cutPct > 1 {
		cutPct = 1
	}

	scope := "losers only (winners ride BE)"
	if cfg.BreadthCutWinners {
		scope = "ALL retracing (winners too — full deleverage)"
	}
	logger.Infof("🌐 [GivebackGuard Breadth] gate FIRED: %d/%d positions retracing (>=%.0f%%), cutting %s at %.0f%% (atr_fail=%d)",
		retr, total, cfg.BreadthFrac*100, scope, cutPct*100, atrFailures)

	fired := 0
	for i, s := range snaps {
		// Default surgical mode: cut only LOSING positions that are retracing;
		// winners ride their break-even stop. When BreadthCutWinners is set this
		// becomes a full deleveraging breaker: retracing winners are cut too.
		if s.profitPct >= 0 && !cfg.BreadthCutWinners {
			continue
		}
		if !legRetr[i] {
			continue
		}
		closeQty := s.quantity * cutPct
		if closeQty <= 0 {
			continue
		}
		at.gbTrim(cfg, "giveback_guard_breadth", s, closeQty,
			fmt.Sprintf("breadth %s %s pnl=%.2f%% peak=%.2f%% (%d/%d retracing)",
				s.symbol, s.side, s.profitPct, s.peakPct, retr, total))
		legs[i].Cut = true
		fired++
	}
	recordEvent("fired", fired)
	if fired > 0 {
		at.gbGuardMutex.Lock()
		at.gbBreadthBarsSinceFire = 0
		at.gbGuardMutex.Unlock()
		persistNeeded = true // fire reset the cooldown counter
	}

	// Persist velocity history + bar clock outside the hot path, but only on a
	// meaningful change (bar advance or fire). Snapshot under the lock, write
	// after release so the DB write never blocks the guard.
	if persistNeeded && at.store != nil {
		at.gbGuardMutex.Lock()
		histCopy := make(map[string][]float64, len(at.gbPnlHist))
		for k, v := range at.gbPnlHist {
			cp := make([]float64, len(v))
			copy(cp, v)
			histCopy[k] = cp
		}
		st := store.BreadthVelocityState{
			PnlHist:       histCopy,
			LastBarMs:     at.gbLastBreadthBarMs,
			BarsSinceFire: at.gbBreadthBarsSinceFire,
		}
		at.gbGuardMutex.Unlock()
		if err := at.store.SaveBreadthVelocityState(at.id, st); err != nil {
			logger.Warnf("⚠️ GivebackGuard Breadth: failed to persist velocity state: %v", err)
		}
	}
}

// gbPnlVelocity returns the profit%-change per bar over the last `window`
// samples of a position's pnl history. >0 = improving (trend-aligned), <0 =
// deteriorating (counter-trend). Mirrors simPos.pnlVelocity.
func gbPnlVelocity(hist []float64, window int) float64 {
	if window < 1 {
		window = 1
	}
	n := len(hist)
	if n < 2 {
		return 0
	}
	lo := n - 1 - window
	if lo < 0 {
		lo = 0
	}
	span := (n - 1) - lo
	if span <= 0 {
		return 0
	}
	return (hist[n-1] - hist[lo]) / float64(span)
}

// gbTrim performs (or, in DryRun, only logs) a partial close of one position.
func (at *AutoTrader) gbTrim(cfg store.GivebackGuardConfig, reason string, s gbPosition, closeQty float64, detail string) {
	if cfg.DryRun {
		logger.Infof("🟡 [GivebackGuard DRY-RUN] would close %.6f of %s %s (%s) reason=%s",
			closeQty, s.symbol, s.side, detail, reason)
		return
	}
	logger.Infof("✂️ [GivebackGuard] closing %.6f of %s %s (%s) reason=%s",
		closeQty, s.symbol, s.side, detail, reason)
	if err := at.closePositionByReason(s.symbol, s.side, closeQty, reason); err != nil {
		logger.Infof("❌ [GivebackGuard] close failed %s %s: %v", s.symbol, s.side, err)
	}
}
