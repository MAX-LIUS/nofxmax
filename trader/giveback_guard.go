package trader

import (
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

	// Resolve ATR% per symbol once (ATR mode only). atrForProtection fetches
	// klines, so cache per call to avoid duplicate fetches across same-symbol legs.
	atrPctCache := make(map[string]float64)
	acfg := at.config.StrategyConfig.ATRProtection
	atrPctOf := func(symbol string, entry float64) float64 {
		if v, ok := atrPctCache[symbol]; ok {
			return v
		}
		v := 0.0
		if entry > 0 {
			if atr, ok := at.atrForProtection(symbol, acfg); ok && atr > 0 {
				v = atr / entry * 100
			}
		}
		atrPctCache[symbol] = v
		return v
	}

	// retracing classifies a position as pulling back from its own peak. Both the
	// velocity path and the from-peak path are ATR-normalized, so BreadthVelEps and
	// BreadthATRMult mean the same thing across low- and high-volatility symbols.
	retracing := func(s gbPosition) bool {
		key := s.symbol + "_" + s.side
		hist := at.gbPnlHist[key]
		atrPct := 0.0
		if cfg.BreadthUseATR {
			atrPct = atrPctOf(s.symbol, s.entry)
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
				return false
			}
			return (s.peakPct-s.profitPct)/atrPct >= cfg.BreadthATRMult
		}
		return (s.peakPct - s.profitPct) >= cfg.BreadthGivebackPct
	}

	total := 0
	retr := 0
	for _, s := range snaps {
		total++
		if retracing(s) {
			retr++
		}
	}
	barsSinceFire := at.gbBreadthBarsSinceFire
	at.gbGuardMutex.Unlock()

	if total < cfg.BreadthMinPos {
		return // no quorum: "majority" is meaningless with too few positions
	}
	if float64(retr)/float64(total) < cfg.BreadthFrac {
		return // not a majority reversal: leave each symbol to its own SL/BE
	}
	if cfg.BreadthCooldownBars > 0 && barsSinceFire < cfg.BreadthCooldownBars {
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
	logger.Infof("🌐 [GivebackGuard Breadth] gate FIRED: %d/%d positions retracing (>=%.0f%%), cutting %s at %.0f%%",
		retr, total, cfg.BreadthFrac*100, scope, cutPct*100)

	fired := 0
	for _, s := range snaps {
		// Default surgical mode: cut only LOSING positions that are retracing;
		// winners ride their break-even stop. When BreadthCutWinners is set this
		// becomes a full deleveraging breaker: retracing winners are cut too.
		if s.profitPct >= 0 && !cfg.BreadthCutWinners {
			continue
		}
		at.gbGuardMutex.Lock()
		isRetr := retracing(s)
		at.gbGuardMutex.Unlock()
		if !isRetr {
			continue
		}
		closeQty := s.quantity * cutPct
		if closeQty <= 0 {
			continue
		}
		at.gbTrim(cfg, "giveback_guard_breadth", s, closeQty,
			fmt.Sprintf("breadth %s %s pnl=%.2f%% peak=%.2f%% (%d/%d retracing)",
				s.symbol, s.side, s.profitPct, s.peakPct, retr, total))
		fired++
	}
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
