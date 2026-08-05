package trader

import (
	"encoding/json"
	"fmt"
	"strings"
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

	// The breadth breaker is the portfolio guard. It has two PARALLEL judgment modes,
	// OR'd together inside gbApplyBreadth:
	//   1. quorum — per-symbol majority-retrace gate (leverage-free, the default)
	//   2. equity — account-value drawdown from the equity high-water mark
	// Either switch alone is enough to run the guard; the old L1/L2/L3 breakers
	// (equity-only, no per-symbol context) remain removed.
	if cfg.BreadthEnabled || cfg.BreadthEquityEnabled {
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
	if len(snaps) == 0 {
		return
	}
	// Which judgment modes are live this cycle. They are independent: the equity
	// path must still run when the quorum path is unconfigured (frac/minpos unset),
	// and vice versa, otherwise "parallel" would collapse to "quorum required".
	quorumOn := cfg.BreadthEnabled && cfg.BreadthFrac > 0 && cfg.BreadthMinPos > 0
	equityOn := cfg.BreadthEquityEnabled && (cfg.BreadthEquityDDPct > 0 || cfg.BreadthEquityDDAbs > 0)
	if !quorumOn && !equityOn {
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

	// Persist on EVERY exit path, not just the fire path. This function returns
	// early on the common outcomes (no_quorum / near_miss / cooldown), and those
	// returns used to discard persistNeeded entirely — so a bar advance or an
	// equity-peak ratchet was only ever written to disk on the rare cycle that
	// actually fired. The visible symptom was a restored velocity history stale by
	// hundreds of hours. For the account-value path this is a protection gap, not
	// just a cosmetic one: losing the ratcheted peak re-bases the breaker to the
	// current (possibly already drawn-down) equity on restart, silently moving the
	// trigger line down. Deferring the write also means it captures the post-fire
	// peak re-base, which happens after the last persistNeeded assignment.
	defer func() {
		if !persistNeeded || at.store == nil {
			return
		}
		// Snapshot under the lock, write after release so the DB write never
		// blocks the guard.
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
			EquityPeak:    at.gbEquityPeak,
			EquityPeakAt:  at.gbEquityPeakAt,
		}
		at.gbGuardMutex.Unlock()
		if err := at.store.SaveBreadthVelocityState(at.id, st); err != nil {
			logger.Warnf("⚠️ GivebackGuard Breadth: failed to persist velocity state: %v", err)
		}
	}()

	// Resolve ATR% per (symbol, timeframe) once. We use the FROZEN open-time ATR
	// (frozenATRForPosition) rather than a fresh live fetch: the live fetch could
	// fail transiently (kline gap / API hiccup) and silently drop a symbol from
	// the retracing set, weakening the breaker exactly when markets are disorderly.
	// The frozen value is computed once at open and persisted, so it survives
	// restarts and is always available after the first successful resolve.
	//
	// Two timeframes are resolved: 1h for the VELOCITY path (it samples per-1h-bar,
	// so the scale matches), and BreadthFromPeakTimeframe (default 4h) for the
	// FROM-PEAK path (cumulative peak-to-current giveback spans many bars, so a
	// timeframe-matched ATR avoids the systematic inflation of a single 1h ATR).
	// atrResolve returns (atrPct, frozen, ok); ok=false means ATR is genuinely
	// unavailable (recorded as a failure, never a silent drop — see retracing()).
	atrPctCache := make(map[string]float64) // key: symbol+"|"+timeframe
	atrOkCache := make(map[string]bool)
	atrFrozenCache := make(map[string]bool)
	acfg := at.config.StrategyConfig.ATRProtection
	fromPeakTF := cfg.BreadthFromPeakTimeframe
	if fromPeakTF == "" {
		fromPeakTF = "4h"
	}
	atrResolve := func(symbol, side string, entry float64, timeframe string) (float64, bool, bool) {
		ck := symbol + "|" + side + "|" + timeframe
		if v, ok := atrPctCache[ck]; ok {
			return v, atrFrozenCache[ck], atrOkCache[ck]
		}
		// Per-timeframe ATR config: copy the protection ATR config and override the
		// timeframe so frozenATRForPosition keys/persists per timeframe.
		tcfg := acfg
		tcfg.Timeframe = timeframe
		v := 0.0
		ok := false
		frozen := false
		if entry > 0 {
			if atr, fok := at.frozenATRForPosition(symbol, side, entry, tcfg); fok && atr > 0 {
				v = atr / entry * 100
				ok = true
				frozen = true
			} else if atr, lok := at.atrForProtection(symbol, tcfg); lok && atr > 0 {
				v = atr / entry * 100
				ok = true
			}
		}
		atrPctCache[ck] = v
		atrOkCache[ck] = ok
		atrFrozenCache[ck] = frozen
		return v, frozen, ok
	}

	// atrFailures counts symbols whose ATR could not be resolved this cycle (ATR
	// mode only). Recorded into the event for later diagnosis; classification
	// fail-safe treats an unresolved ATR symbol as NON-retracing (so a data gap
	// can never manufacture a false breaker fire), but the failure is always
	// logged so it is visible rather than silent.
	atrFailures := 0
	atrFailSyms := make(map[string]bool)

	// retracing classifies a position as pulling back from its own peak. The
	// velocity path is normalized by the 1h ATR (matches its 1h sampling); the
	// from-peak path is normalized by the timeframe-matched ATR (default 4h) so
	// BreadthATRMult means the same thing across low- and high-volatility symbols.
	//
	// Returns (retr, velHit, peakHit): retr = velHit || peakHit. Both flags are
	// evaluated independently (velocity does NOT short-circuit from-peak) so the
	// per-path breadth pressure indices can be computed separately.
	retracing := func(s gbPosition) (bool, bool, bool) {
		key := s.symbol + "_" + s.side
		hist := at.gbPnlHist[key]
		atrPct1h := 0.0   // velocity scale
		atrPctPeak := 0.0 // from-peak scale (timeframe-matched)
		if cfg.BreadthUseATR {
			var ok1, okp bool
			atrPct1h, _, ok1 = atrResolve(s.symbol, s.side, s.entry, "1h")
			atrPctPeak, _, okp = atrResolve(s.symbol, s.side, s.entry, fromPeakTF)
			if (!ok1 || !okp) && !atrFailSyms[s.symbol] {
				atrFailSyms[s.symbol] = true
				atrFailures++
				logger.Warnf("⚠️ [GivebackGuard Breadth] ATR unavailable for %s (1h_ok=%v %s_ok=%v) — treated as non-retracing this cycle (fail-safe)", s.symbol, ok1, fromPeakTF, okp)
			}
		}
		velHit := false
		// Velocity path: profit%/bar normalized by 1h ATR% => "ATR-units lost per bar".
		// A symbol giving back faster than BreadthVelEps ATRs/bar counts as retracing.
		//
		// Minimum-sample floor: require the FULL window of samples (not just 2).
		// The velocity slope is meant to be measured over `window` bars; computing
		// it on 2 thin samples (e.g. right after a restart/hot-reload that hasn't
		// re-accumulated history) degenerates into a single bar-to-bar delta and
		// makes the breaker hypersensitive — a single hour's pullback would fire it.
		// The window is the strategy-declared minimum, so the code must honor it.
		if len(hist) >= window {
			vel := gbPnlVelocity(hist, window)
			if atrPct1h > 0 {
				vel = vel / atrPct1h // raw %/bar -> ATR-units/bar
			}
			if vel < -cfg.BreadthVelEps {
				velHit = true
			}
		}
		peakHit := false
		if cfg.BreadthUseATR {
			if atrPctPeak > 0 {
				peakHit = (s.peakPct-s.profitPct)/atrPctPeak >= cfg.BreadthATRMult
			}
			// atrPctPeak<=0 => fail-safe: from-peak does not count
		} else {
			peakHit = (s.peakPct - s.profitPct) >= cfg.BreadthGivebackPct
		}
		return velHit || peakHit, velHit, peakHit
	}

	// Classify every position once and capture the full per-leg picture so the
	// decision is fully reconstructable from the recorded event (task: "把整个
	// 事件全貌细节都记录下来"). retracing() is evaluated exactly once per leg here.
	total := 0
	retr := 0
	velCount := 0  // positions retracing via the velocity path
	peakCount := 0 // positions retracing via the from-peak path
	legs := make([]store.BreadthEventLeg, 0, len(snaps))
	legRetr := make([]bool, len(snaps))
	for i, s := range snaps {
		total++
		isRetr, velHit, peakHit := retracing(s)
		legRetr[i] = isRetr
		if isRetr {
			retr++
		}
		if velHit {
			velCount++
		}
		if peakHit {
			peakCount++
		}
		atrPct := 0.0
		atrFailed := false
		frozen := false
		if cfg.BreadthUseATR {
			// Record the from-peak (timeframe-matched) ATR% since it drives the
			// from-peak decision; ok reflects whether that scale resolved.
			var ok bool
			atrPct, frozen, ok = atrResolve(s.symbol, s.side, s.entry, fromPeakTF)
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

	// --- Account-value path: read equity, ratchet the high-water mark ---------
	// Only touched when the equity path is enabled, so the default configuration
	// makes no extra exchange call. Equity is fetched fresh (not derived from the
	// position snapshots) because it must include realized balance, not just
	// unrealized PnL: a book that already realized its losses has really lost that
	// account value and the breaker must see it.
	//
	// Fail-safe: if equity cannot be read, the path is SKIPPED (never fires on a
	// zero/garbage reading) and the failure is recorded via EquityFetchOK=false.
	equityCur, equityPeak, equityDDPct, equityDDAbs := 0.0, 0.0, 0.0, 0.0
	equityFetchOK := false
	if equityOn {
		equityCur = at.fetchEquityForSizing()
		if equityCur > 0 {
			equityFetchOK = true
			at.gbGuardMutex.Lock()
			if equityCur > at.gbEquityPeak {
				at.gbEquityPeak = equityCur
				at.gbEquityPeakAt = time.Now().UnixMilli()
				persistNeeded = true // ratchet advanced; must survive restart
			}
			equityPeak = at.gbEquityPeak
			at.gbGuardMutex.Unlock()
			if equityPeak > 0 {
				equityDDAbs = equityPeak - equityCur
				equityDDPct = equityDDAbs / equityPeak * 100
				if equityDDAbs < 0 {
					equityDDAbs, equityDDPct = 0, 0
				}
			}
		} else {
			logger.Warnf("⚠️ [GivebackGuard Equity] equity unavailable (got %.4f) — account-value path skipped this cycle (fail-safe)", equityCur)
		}
	}
	equityScope := cfg.BreadthEquityScope
	if equityScope == "" {
		equityScope = "retracing"
	}
	// equityHit: the account-value trigger. Either threshold suffices; a threshold
	// left at 0 is OFF (not "fires at any drawdown").
	equityHit := false
	if equityOn && equityFetchOK && equityPeak > 0 {
		if cfg.BreadthEquityDDPct > 0 && equityDDPct >= cfg.BreadthEquityDDPct {
			equityHit = true
		}
		if cfg.BreadthEquityDDAbs > 0 && equityDDAbs >= cfg.BreadthEquityDDAbs {
			equityHit = true
		}
		// Optional quorum for the equity path (0 = none: an account-value stop is
		// legitimate even with a single oversized leg).
		if equityHit && cfg.BreadthEquityMinPos > 0 && total < cfg.BreadthEquityMinPos {
			equityHit = false
		}
	}

	// Per-path breadth pressure indices (0–100) for the dashboard gauges. Each
	// index is that path's retracing fraction relative to the BreadthFrac fire
	// threshold: index = (pathRetr/total) / BreadthFrac * 100, clamped to 100.
	// 0 = no position retracing on that path (no risk); 100 = that path alone has
	// reached the fraction that fires the breaker. The breaker fires on the COMBINED
	// fraction, so either index hitting 100 (or the two together) means a cut.
	// Only meaningful at quorum (total >= BreadthMinPos); below quorum the breaker
	// can't fire, so we report 0 to avoid a misleading "hot" gauge on 1–2 positions.
	velIdx, peakIdx := 0.0, 0.0
	if total > 0 && cfg.BreadthFrac > 0 && total >= cfg.BreadthMinPos {
		velIdx = float64(velCount) / float64(total) / cfg.BreadthFrac * 100
		peakIdx = float64(peakCount) / float64(total) / cfg.BreadthFrac * 100
		if velIdx > 100 {
			velIdx = 100
		}
		if peakIdx > 100 {
			peakIdx = 100
		}
	}
	at.gbGuardMutex.Lock()
	at.gbBreadthVelIndex = velIdx
	at.gbBreadthPeakIndex = peakIdx
	at.gbBreadthIndexAt = time.Now().UnixMilli()
	at.gbGuardMutex.Unlock()

	// recordEvent persists a full snapshot of this evaluation. Best-effort; never
	// blocks the guard. cut legs are marked by the caller before invoking on fire.
	recordEvent := func(outcome, firePath string, cutCount int) {
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
			// Account-value path snapshot: recorded on every persisted row when the
			// path is enabled, so a quorum near_miss also shows how close equity was.
			FirePath:      firePath,
			EquityPeak:    equityPeak,
			EquityCur:     equityCur,
			EquityDDPct:   equityDDPct,
			EquityDDAbs:   equityDDAbs,
			EquityThrPct:  cfg.BreadthEquityDDPct,
			EquityThrAbs:  cfg.BreadthEquityDDAbs,
			EquityScope:   equityScope,
			EquityFetchOK: equityFetchOK,
		}
		if !equityOn {
			evt.EquityScope = "" // path disabled: don't imply a scope was in effect
		}
		if err := at.store.BreadthEvent().Record(evt); err != nil {
			logger.Warnf("⚠️ [GivebackGuard Breadth] failed to record event: %v", err)
		}
	}

	// --- Combine the two parallel judgment modes -----------------------------
	// quorumHit: per-symbol majority retrace at quorum. equityHit (above): account
	// value fell far enough from its peak. Either fires the breaker. When both hit,
	// the quorum path wins the label — it is the more specific, surgical judgment
	// and its scope rules are the cross-validated ones.
	quorumHit := quorumOn && total >= cfg.BreadthMinPos && frac >= cfg.BreadthFrac
	if !quorumHit && !equityHit {
		// Nothing fired. Record the informative non-fire outcomes only, exactly as
		// before: below-quorum rows only when something was actually retracing or an
		// ATR lookup failed; at-quorum shortfalls always (so BreadthFrac stays
		// tunable). Quiet cycles are still not persisted.
		switch {
		case quorumOn && total < cfg.BreadthMinPos:
			if retr > 0 || atrFailures > 0 {
				recordEvent("no_quorum", "", 0)
			}
		case quorumOn:
			recordEvent("near_miss", "", 0)
		case retr > 0 || atrFailures > 0:
			// Equity-only configuration: keep a near-miss trail so the account-value
			// path is tunable from recorded data instead of guesswork.
			recordEvent("near_miss", "", 0)
		}
		return
	}

	// Cooldown applies to the QUORUM path only. The account-value breaker is an
	// absolute account stop; a bar-clock cooldown must not be able to hold it off
	// while equity keeps falling. Its own repeat-fire protection is the peak
	// re-base after a cut (see below), which requires a fresh full drawdown.
	if quorumHit && !equityHit && cfg.BreadthCooldownBars > 0 && barsSinceFire < cfg.BreadthCooldownBars {
		recordEvent("cooldown", "", 0)
		return
	}

	firePath := "quorum"
	if !quorumHit {
		firePath = "equity"
	}
	// cutAll: the account-value breaker's "all" scope closes the whole book —
	// unconditional go-to-cash, ignoring both the retracing classification and any
	// armed protection. Only reachable when the equity path is the firing path.
	cutAll := firePath == "equity" && equityScope == "all"

	// Cut fraction: each path has its own knob so tuning one cannot silently change
	// the other. Equity path falls back to the quorum path's value when unset, and
	// finally to a full cut.
	cutPct := cfg.BreadthLoserCutPct / 100.0
	if firePath == "equity" && cfg.BreadthEquityCutPct > 0 {
		cutPct = cfg.BreadthEquityCutPct / 100.0
	}
	if cutPct <= 0 {
		cutPct = 1.0 // default: full cut
	}
	if cutPct > 1 {
		cutPct = 1
	}

	scope := "losers only (winners ride BE)"
	if cfg.BreadthCutWinners {
		scope = "ALL retracing (winners too — full deleverage)"
	}
	if cutAll {
		scope = "EVERY open position (go-to-cash)"
	}
	if firePath == "equity" {
		logger.Infof("💰 [GivebackGuard Equity] account-value breaker FIRED: equity %.2f vs peak %.2f = -%.2f (-%.2f%%) [thr %.2f%% / %.2f USDT], cutting %s at %.0f%% (retracing %d/%d, atr_fail=%d)",
			equityCur, equityPeak, equityDDAbs, equityDDPct,
			cfg.BreadthEquityDDPct, cfg.BreadthEquityDDAbs, scope, cutPct*100, retr, total, atrFailures)
	} else {
		logger.Infof("🌐 [GivebackGuard Breadth] gate FIRED: %d/%d positions retracing (>=%.0f%%), cutting %s at %.0f%% (atr_fail=%d)",
			retr, total, cfg.BreadthFrac*100, scope, cutPct*100, atrFailures)
	}

	closeReason := "giveback_guard_breadth"
	if firePath == "equity" {
		closeReason = "giveback_guard_equity"
	}
	fired := 0
	for i, s := range snaps {
		if !cutAll {
			// Winner handling: a retracing WINNER is spared ONLY if it already has armed
			// downside protection (native trailing / managed drawdown / break-even stop)
			// — that protection will lock in its gain on the way down. A "naked" winner
			// (profit but NO armed protection) is exposed exactly like a loser: in a
			// correlated reversal it would give back the whole unprotected gain, so it
			// is cut alongside losers. BreadthCutWinners forces cutting ALL retracing
			// winners regardless of protection (full deleverage).
			if s.profitPct >= 0 && !cfg.BreadthCutWinners {
				if at.positionHasArmedProtection(s.symbol, s.side, s.entry) {
					continue // protected winner rides its own stop
				}
				// else: naked winner — fall through and cut like a loser
			}
			if !legRetr[i] {
				continue
			}
		}
		closeQty := s.quantity * cutPct
		if closeQty <= 0 {
			continue
		}
		detail := fmt.Sprintf("breadth %s %s pnl=%.2f%% peak=%.2f%% (%d/%d retracing)",
			s.symbol, s.side, s.profitPct, s.peakPct, retr, total)
		if firePath == "equity" {
			detail = fmt.Sprintf("equity-DD %s %s pnl=%.2f%% peak=%.2f%% (equity %.2f vs peak %.2f = -%.2f%%, scope=%s)",
				s.symbol, s.side, s.profitPct, s.peakPct, equityCur, equityPeak, equityDDPct, equityScope)
		}
		at.gbTrim(cfg, closeReason, s, closeQty, detail)
		legs[i].Cut = true
		fired++
	}
	recordEvent("fired", firePath, fired)

	// Re-base the equity peak after an account-value fire. This is the equity path's
	// repeat-fire protection: without it, every subsequent cycle would still see the
	// same peak-to-current gap and keep cutting whatever remains. Re-basing to the
	// post-cut equity means re-arming needs a fresh full BreadthEquityDDPct/Abs
	// drawdown from here. Done in DRY-RUN too, so dry-run trigger frequency matches
	// what live would produce instead of firing every cycle forever.
	if firePath == "equity" && equityFetchOK {
		at.gbGuardMutex.Lock()
		at.gbEquityPeak = equityCur
		at.gbEquityPeakAt = time.Now().UnixMilli()
		at.gbGuardMutex.Unlock()
		persistNeeded = true
		logger.Infof("💰 [GivebackGuard Equity] equity peak re-based to %.2f after fire (re-arm requires a fresh drawdown)", equityCur)
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
	// Persistence happens in the deferred writer installed near the top of this
	// function, so it also covers the early-return paths and the peak re-base above.
}

// positionHasArmedProtection reports whether a position already has armed
// downside protection that would lock in profit on a reversal — a native
// trailing stop, a managed drawdown stop, or a break-even stop resting on the
// exchange. Used by the breadth breaker to decide whether a retracing WINNER is
// safe to leave alone (protected) or must be cut like a loser (naked).
func (at *AutoTrader) positionHasArmedProtection(symbol, side string, entryPrice float64) bool {
	// Fast in-memory check: trailing / managed-drawdown arming states.
	state := at.getProtectionState(symbol, side)
	// 注意这里刻意**不**用 isDynamicDrawdownArmState:那个谓词含两种
	// *_exchange_failed_armed,而本函数下面几行专门对它们加了"必须有 tier alloc 才算
	// 有保护"的更严格判断(交易所上没单,只有本地监控)。这里只认交易所侧成立的状态。
	if isNativeTrailingProtectionState(state) ||
		state == "managed_drawdown_armed" || state == "managed_partial_drawdown_armed" ||
		state == "exchange_protection_verified" {
		return true
	}
	// The "exchange_failed" variants mean the exchange order could NOT be placed and the
	// in-process monitor is the only thing left. For a partial tier that monitor stages real
	// orders; for a close>=100 tier applyManagedDrawdownFallback places nothing and only
	// records state, so the state alone is not evidence of protection. Require a live tier
	// allocation — that is the managed executor (evaluateDrawdownTiers) actually watching
	// this position. Without it, treat the position as NAKED so the breadth breaker cuts a
	// retracing winner instead of leaving it to give the profit back.
	if state == "managed_drawdown_exchange_failed_armed" || state == "managed_partial_drawdown_exchange_failed_armed" {
		if len(at.getDrawdownTierAllocs(symbol, side)) > 0 {
			return true
		}
		logger.Warnf("⚠️ GivebackGuard: %s %s state=%s but no managed tier allocation exists — no executor is watching this tier, treating position as unprotected", symbol, side, state)
	}
	// Native drawdown trailing record armed for this position.
	if at.hasArmedNativeDrawdownForPosition(symbol, side, entryPrice) {
		return true
	}
	// Break-even stop armed (persisted as a break_even_stop "armed" record).
	if at.store != nil {
		if dyn, err := at.store.LoadDynamicProtectionState(); err == nil && dyn != nil {
			for _, rec := range dyn.Records {
				if rec.TraderID != "" && rec.TraderID != at.id {
					continue
				}
				if rec.ProtectionType == "break_even_stop" && rec.Status == "armed" &&
					strings.EqualFold(rec.Symbol, symbol) && strings.EqualFold(rec.Side, side) {
					return true
				}
			}
		}
	}
	return false
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
