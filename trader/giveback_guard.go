package trader

import (
	"fmt"

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

// runGivebackGuard is the live portfolio giveback guard. It mirrors the
// backtested applyGuards() (trader/backtest/portfolio_sim.go): an L1 per-symbol
// velocity trim plus an L2 portfolio circuit breaker, both ratcheted to fire
// once per reversal episode. Validated config (L1+L2) was strongest across
// short-sample, 12mo, walk-forward, and 18mo OKX backtests.
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

	if cfg.L1Enabled {
		at.gbApplyL1(cfg, snaps)
	}
	if cfg.L2Enabled {
		at.gbApplyL2(cfg, snaps)
	}
}

// gbApplyL1 trims a single position when it gives back >= L1GivebackPct of its
// own peak profit% (after peaking >= L1MinPeakPct). Ratcheted per symbol_side:
// re-arms only when a new profit peak is set above the peak at last fire.
func (at *AutoTrader) gbApplyL1(cfg store.GivebackGuardConfig, snaps []gbPosition) {
	for _, s := range snaps {
		if s.peakPct < cfg.L1MinPeakPct || s.peakPct <= 0 {
			continue
		}
		key := s.symbol + "_" + s.side
		at.gbGuardMutex.Lock()
		firedAt := at.gbL1FiredAtPeak[key]
		at.gbGuardMutex.Unlock()
		// Re-arm gate: skip if no new profit peak since last fire on this position.
		if firedAt > 0 && s.peakPct <= firedAt {
			continue
		}
		giveback := (s.peakPct - s.profitPct) / s.peakPct * 100
		if giveback < cfg.L1GivebackPct {
			continue
		}
		closeQty := s.quantity * cfg.L1ClosePct / 100.0
		if closeQty <= 0 {
			continue
		}
		at.gbTrim(cfg, "giveback_guard_l1", s, closeQty,
			fmt.Sprintf("L1 %s %s peak=%.2f%% cur=%.2f%% giveback=%.0f%%>=%.0f%%",
				s.symbol, s.side, s.peakPct, s.profitPct, giveback, cfg.L1GivebackPct))
		at.gbGuardMutex.Lock()
		at.gbL1FiredAtPeak[key] = s.peakPct
		at.gbGuardMutex.Unlock()
	}
}

// gbApplyL2 is the portfolio circuit breaker: track total-unrealized high-water;
// when the book gives back >= L2GivebackPct of that peak AND peak >=
// L2MinPeakEquityPct of account equity, trim L2ClosePct of EACH winning
// position. Ratcheted: fires once per episode, re-arms on a new portfolio high.
func (at *AutoTrader) gbApplyL2(cfg store.GivebackGuardConfig, snaps []gbPosition) {
	var totalUnreal float64
	for _, s := range snaps {
		totalUnreal += s.unreal
	}

	at.gbGuardMutex.Lock()
	if totalUnreal > at.gbPortfolioPeakUnreal {
		at.gbPortfolioPeakUnreal = totalUnreal
	}
	peak := at.gbPortfolioPeakUnreal
	// Re-arm: portfolio set a new high-water above last fire -> clear latch.
	if at.gbL2FiredAtPeak > 0 && peak > at.gbL2FiredAtPeak {
		at.gbL2FiredAtPeak = 0
	}
	armed := at.gbL2FiredAtPeak == 0
	at.gbGuardMutex.Unlock()

	if peak <= 0 || !armed {
		return
	}
	// Min-peak gate scaled to account equity (backtest used absolute USD).
	equity := at.fetchEquityForSizing()
	minPeak := 0.0
	if equity > 0 && cfg.L2MinPeakEquityPct > 0 {
		minPeak = equity * cfg.L2MinPeakEquityPct / 100.0
	}
	if peak < minPeak {
		return
	}
	giveback := (peak - totalUnreal) / peak * 100
	if giveback < cfg.L2GivebackPct {
		return
	}

	logger.Infof("🟠 [GivebackGuard L2] portfolio giveback %.0f%%>=%.0f%% (peakUnreal=%.2f cur=%.2f equity=%.0f) — trimming winners %.0f%%",
		giveback, cfg.L2GivebackPct, peak, totalUnreal, equity, cfg.L2ClosePct)

	for _, s := range snaps {
		if s.unreal <= 0 { // only de-risk winners; never realize losers
			continue
		}
		closeQty := s.quantity * cfg.L2ClosePct / 100.0
		if closeQty <= 0 {
			continue
		}
		at.gbTrim(cfg, "giveback_guard_l2", s, closeQty,
			fmt.Sprintf("L2 %s %s unreal=%.2f", s.symbol, s.side, s.unreal))
	}

	at.gbGuardMutex.Lock()
	at.gbL2FiredAtPeak = peak // latch until a new portfolio high-water
	at.gbGuardMutex.Unlock()
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
