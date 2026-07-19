package trader

import (
	"time"

	"nofx/logger"
	"nofx/market"
	"nofx/store"
)

// runStructuralSLGuard is Phase 2 of the structural stop-loss: the close-confirm
// enforcement layer. It runs on the drawdown poll cadence and, for positions whose
// strategy uses a structural SL with CloseConfirm enabled, market-closes a position
// when the most recent CLOSED bar on the ATR timeframe closed BEYOND the frozen
// pre-entry range boundary (swing low for long / swing high for short).
//
// Why close-confirm + a separate layer: the resting exchange stop for these
// positions is parked at the wide backstop (see resolveATRProtection) so it never
// fires on an intrabar wick (stop-hunt / false break). This guard supplies the tight
// structural exit, but only on a confirmed bar close — which the extended-replay
// backtest showed cuts SL whipsaw from ~34% to ~27% and turns the ranging-regime
// net positive. The backstop resting order remains as a bot-downtime safety net.
func (at *AutoTrader) runStructuralSLGuard() {
	if at.config.StrategyConfig == nil {
		return
	}
	ladder := at.config.StrategyConfig.Protection.LadderTPSL
	if !ladder.StructuralSL.Enabled || !ladder.StructuralSL.WithDefaults().CloseConfirm {
		return
	}
	// Only meaningful if some SL rule actually opts into the structural unit.
	usesStructural := false
	for _, r := range ladder.Rules {
		if r.StopLossUnit == store.ProtectionUnitStructural {
			usesStructural = true
			break
		}
	}
	if !usesStructural {
		return
	}

	acfg := at.config.StrategyConfig.ATRProtection
	if !acfg.Enabled {
		return
	}

	positions, err := at.trader.GetPositions()
	if err != nil || len(positions) == 0 {
		return
	}

	for _, pos := range positions {
		symbol, _ := pos["symbol"].(string)
		side, _ := pos["side"].(string)
		entry, _ := pos["entryPrice"].(float64)
		qty, _ := pos["positionAmt"].(float64)
		if qty < 0 {
			qty = -qty
		}
		if symbol == "" || side == "" || entry <= 0 || qty <= 0 {
			continue
		}
		at.evaluateStructuralSLClose(symbol, side, entry, qty, ladder, acfg)
	}
}

// evaluateStructuralSLClose closes one position if the last CLOSED bar breached its
// frozen structural boundary. Dedups per closed-bar so a single breach fires once.
func (at *AutoTrader) evaluateStructuralSLClose(symbol, side string, entry, qty float64, ladder store.LadderTPSLConfig, acfg store.ATRProtectionConfig) {
	sscfg := ladder.StructuralSL
	isLong := actionFromPositionSide(side) == "open_long"
	// allowCompute=false: the guard only enforces a boundary that was frozen AT ENTRY.
	// A position with no frozen boundary (opened before enablement) rides the backstop.
	boundary, ok := at.frozenStructBoundaryForPosition(symbol, entry, isLong, false, sscfg, acfg)
	if !ok || boundary <= 0 {
		return // no structural edge for this position; resting backstop covers it
	}
	// Clamp the raw swing boundary to [floor, backstop] ATR — the SAME clamp the
	// resting backstop order uses (structuralSLPercent). Without this, a swing low
	// farther than BackstopATRMul would leave the guard's trigger BEYOND the wide
	// resting stop: the backstop fires first and the close-confirm guard could
	// never trigger, silently degrading the structural stop. Clamping keeps the
	// guard trigger at/inside the backstop so the tight close-confirm actually works.
	if frozenATR, aok := at.frozenATRForPosition(symbol, side, entry, acfg); aok && frozenATR > 0 {
		tpTargetPct := ladderMaxTPTargetPct(ladder.Rules, frozenATR, entry, acfg)
		if clamped, cok := clampStructuralBoundary(entry, boundary, frozenATR, tpTargetPct, isLong, sscfg); cok {
			boundary = clamped
		}
	}

	c := acfg.WithDefaults()
	ss := sscfg.WithDefaults()
	// When the ratchet is enabled we need the full lookback window (for swing
	// recompute); otherwise 3 bars suffice for the static close-confirm check.
	fetch := 3
	if ss.TrailEnabled {
		fetch = ss.LookbackBars + 4
		if fetch < 8 {
			fetch = 8
		}
	}
	bars, err := market.GetKlines(symbol, c.Timeframe, at.exchange, fetch)
	if err != nil || len(bars) < 2 {
		return
	}
	closedBar := bars[len(bars)-2]
	if closedBar.Close <= 0 {
		return
	}

	// Ratcheting trail: tighten the effective close-confirm boundary toward locking
	// profit. Uses the frozen entry boundary as the starting floor and only ever moves
	// tighter. Persisted so the ratchet survives a restart. Static behavior when off.
	if ss.TrailEnabled {
		boundary = at.applyStructuralTrail(symbol, side, entry, isLong, boundary, closedBar, bars, ss, acfg)
		// Roll the physical intrabar backup stop to sit one step behind the newest
		// close-confirm level (or clear it pre-first-ratchet). The 4.5-ATR resting
		// backstop is untouched; this is a separate, tighter, rolling safety layer.
		at.syncStructuralBackupStop(symbol, side, qty, entry, isLong, acfg)
	}

	breached := (isLong && closedBar.Close < boundary) || (!isLong && closedBar.Close > boundary)
	if !breached {
		return
	}

	// Dedup: only fire once per distinct closed bar.
	key := symbol + "_" + side
	at.structSLMutex.Lock()
	if at.structSLFiredBar == nil {
		at.structSLFiredBar = map[string]int64{}
	}
	if at.structSLFiredBar[key] == closedBar.OpenTime {
		at.structSLMutex.Unlock()
		return
	}
	at.structSLFiredBar[key] = closedBar.OpenTime
	at.structSLMutex.Unlock()

	logger.Infof("🛑 [StructuralSL] %s %s closed %.6f beyond boundary %.6f (close-confirm) → market close %.6f",
		symbol, side, closedBar.Close, boundary, qty)
	if err := at.closePositionByReason(symbol, side, qty, "structural_sl"); err != nil {
		logger.Infof("❌ [StructuralSL] close failed %s %s: %v", symbol, side, err)
	}
}

// applyStructuralTrail runs one ratchet step for the trailing structural stop and
// returns the EFFECTIVE close-confirm boundary to enforce this cycle: the tighter of
// the frozen entry boundary and the current ratcheted trail boundary. It loads the
// prior trail state (boundary + ratchet count) from the frozen-ATR record, recomputes
// nearest structure on the closed-bar window, persists any tighten, and clamps the
// result to the same [floor, backstop] band the static boundary uses so the ratchet
// can never push the trigger BEYOND the wide resting backstop. entryBoundary is the
// already-clamped frozen entry boundary; frozenBars includes the still-forming bar.
func (at *AutoTrader) applyStructuralTrail(symbol, side string, entry float64, isLong bool, entryBoundary float64, closedBar market.Kline, frozenBars []market.Kline, ss store.StructuralSLConfig, acfg store.ATRProtectionConfig) float64 {
	if len(frozenBars) < 4 {
		return entryBoundary
	}
	tf := acfg.WithDefaults().Timeframe
	sideUpper := "SHORT"
	if isLong {
		sideUpper = "LONG"
	}
	key := frozenATRKey(at.id, symbol, tf, sideUpper)

	// Frozen open-time ATR (price units) drives cushion/min-profit. No ATR → static.
	atr, ok := at.frozenATRForPosition(symbol, side, entry, acfg)
	if !ok || atr <= 0 {
		return entryBoundary
	}

	// Load prior trail state; seed from the entry boundary on first arm.
	prevBoundary := entryBoundary
	prevRatchets := 0
	if at.store != nil {
		if state, err := at.store.LoadFrozenATRState(); err == nil {
			if rec, found := state.Records[key]; found && entrySamePosition(rec.EntryPrice, entry) {
				if rec.TrailBoundary > 0 {
					prevBoundary = rec.TrailBoundary
				}
				prevRatchets = rec.TrailRatchets
			}
		}
	}

	// Closed-bar window (drop the still-forming last bar) for swing recompute.
	window := frozenBars[:len(frozenBars)-1]
	newBoundary, newRatchets := computeTrailBoundary(ss, trailRecomputeInput{
		window:   window,
		curClose: closedBar.Close,
		atr:      atr,
		entry:    entry,
		isLong:   isLong,
		curBound: prevBoundary,
		ratchets: prevRatchets,
	})

	// Clamp to the same [floor, backstop] band as the static boundary so a ratcheted
	// trigger can never sit beyond the wide resting backstop (which would fire first).
	tpTargetPct := ladderMaxTPTargetPct(at.config.StrategyConfig.Protection.LadderTPSL.Rules, atr, entry, acfg)
	if clamped, cok := clampStructuralBoundary(entry, newBoundary, atr, tpTargetPct, isLong, ss); cok {
		newBoundary = clamped
	}

	// Never let the ratchet LOOSEN below the frozen entry boundary; keep the tighter.
	effective := newBoundary
	if !isLong {
		if entryBoundary > 0 && entryBoundary < effective {
			effective = entryBoundary // short: lower = tighter
		}
	} else {
		if entryBoundary > 0 && entryBoundary > effective {
			effective = entryBoundary // long: higher = tighter
		}
	}

	// Persist the tighten (only when it actually moved) so it survives restart.
	// Rolling 2-level window: the level being SUPERSEDED (prevBoundary) becomes the new
	// BackupBoundary — a physical intrabar stop one step behind the newest close-confirm
	// level. This keeps exactly the two most recent structural levels; older levels are
	// implicitly dropped (BackupBoundary just holds whichever level was tight last cycle).
	// The wide 4.5-ATR backstop is a separate resting order and is never touched here.
	if newRatchets != prevRatchets && at.store != nil {
		if state, err := at.store.LoadFrozenATRState(); err == nil {
			rec := state.Records[key]
			rec.TraderID, rec.Symbol, rec.EntryPrice = at.id, symbol, entry
			rec.BackupBoundary = prevBoundary // the level we just superseded → intrabar backup
			rec.TrailBoundary = newBoundary    // newest tightest → close-confirm guard
			rec.TrailRatchets = newRatchets
			rec.UpdatedAt = time.Now().Unix()
			if err := at.store.SaveFrozenATRRecord(key, rec); err != nil {
				logger.Warnf("⚠️ [StructuralSL] trail persist failed %s: %v", key, err)
			} else {
				logger.Infof("🔧 [StructuralSL] %s %s trail ratchet #%d → tight %.6f, backup %.6f (entry-floor %.6f)",
					symbol, sideUpper, newRatchets, newBoundary, prevBoundary, entryBoundary)
			}
		}
	}
	return effective
}

// frozenBackupBoundaryForPosition returns the currently-persisted rolling backup
// boundary for a position (0 when none). Used by the reconciler to register the
// guard-owned backup stop price as tolerated (not churned, not double-placed).
func (at *AutoTrader) frozenBackupBoundaryForPosition(symbol, side string, entryPrice float64) float64 {
	if at.store == nil || entryPrice <= 0 || at.config.StrategyConfig == nil {
		return 0
	}
	tf := at.config.StrategyConfig.ATRProtection.WithDefaults().Timeframe
	sideUpper := "SHORT"
	if actionFromPositionSide(side) == "open_long" {
		sideUpper = "LONG"
	}
	key := frozenATRKey(at.id, symbol, tf, sideUpper)
	state, err := at.store.LoadFrozenATRState()
	if err != nil {
		return 0
	}
	if rec, found := state.Records[key]; found && entrySamePosition(rec.EntryPrice, entryPrice) {
		return rec.BackupBoundary
	}
	return 0
}

// syncStructuralBackupStop places / rolls / cancels the guard-owned physical intrabar
// backup stop so exactly ONE backup order sits at the current BackupBoundary. It is
// idempotent per cycle: it only acts when the desired backup price differs from the
// order already recorded. Called after a ratchet may have moved BackupBoundary.
//
//   - desiredBackup <= 0            → no backup layer yet (pre-first-ratchet): cancel any
//     stale backup order and clear the record.
//   - desiredBackup unchanged       → nothing to do (the resting order is already correct).
//   - desiredBackup moved (rolled)  → cancel the previous backup order by ID, place a new
//     tagged resting stop at the new price, persist the new order ID. This is the
//     "keep current + add tighter, drop farther" roll from the design.
func (at *AutoTrader) syncStructuralBackupStop(symbol, side string, qty, entry float64, isLong bool, acfg store.ATRProtectionConfig) {
	if at.store == nil {
		return
	}
	tf := acfg.WithDefaults().Timeframe
	sideUpper := "SHORT"
	if isLong {
		sideUpper = "LONG"
	}
	positionSide := sideUpper
	key := frozenATRKey(at.id, symbol, tf, sideUpper)

	state, err := at.store.LoadFrozenATRState()
	if err != nil {
		return
	}
	rec, found := state.Records[key]
	if !found || !entrySamePosition(rec.EntryPrice, entry) {
		return
	}
	desired := rec.BackupBoundary

	// Sanity: a valid backup must sit on the protective side of entry (below for a long,
	// above for a short). Anything else is treated as "no backup".
	if desired > 0 {
		if (isLong && desired >= entry) || (!isLong && desired <= entry) {
			desired = 0
		}
	}

	// The backup order is a physical resting stop keyed by its price. Detect whether one
	// already sits at the desired price to stay idempotent across cycles/restarts.
	openOrders, ooErr := at.trader.GetOpenOrders(symbol)
	if ooErr == nil && desired > 0 && hasMatchingProtectionOrder(openOrders, positionSide, false, desired) &&
		rec.BackupOrderID != "" {
		return // already placed and recorded at the right price
	}

	// Roll: cancel the previously-recorded backup order (the superseded/farther level).
	if rec.BackupOrderID != "" {
		if canceller, ok := at.trader.(interface {
			CancelOrder(symbol, orderID string) error
		}); ok {
			if cErr := canceller.CancelOrder(symbol, rec.BackupOrderID); cErr != nil {
				logger.Warnf("⚠️ [StructuralSL] %s %s cancel prior backup stop %s failed: %v",
					symbol, sideUpper, rec.BackupOrderID, cErr)
			} else {
				logger.Infof("🧹 [StructuralSL] %s %s canceled superseded backup stop %s",
					symbol, sideUpper, rec.BackupOrderID)
			}
		}
		rec.BackupOrderID = ""
	}

	// Place the new backup as a tagged resting intrabar stop at the desired price.
	if desired > 0 {
		if setter, ok := at.trader.(interface {
			SetStopLossTagged(symbol string, positionSide string, quantity, stopPrice float64, reasonTag string) (string, error)
		}); ok {
			algoID, sErr := setter.SetStopLossTagged(symbol, positionSide, qty, desired, "structural_backup_sl")
			if sErr != nil {
				logger.Warnf("⚠️ [StructuralSL] %s %s place backup stop @ %.6f failed: %v",
					symbol, sideUpper, desired, sErr)
			} else {
				rec.BackupOrderID = algoID
				at.recordProtectionIntent(symbol, positionSide, "structural_backup_sl", qty, desired, algoID)
				logger.Infof("🛡 [StructuralSL] %s %s backup stop placed @ %.6f (intrabar, one step behind close-confirm)",
					symbol, sideUpper, desired)
			}
		}
	}

	rec.UpdatedAt = time.Now().Unix()
	if pErr := at.store.SaveFrozenATRRecord(key, rec); pErr != nil {
		logger.Warnf("⚠️ [StructuralSL] %s %s persist backup order id failed: %v", symbol, sideUpper, pErr)
	}
}

