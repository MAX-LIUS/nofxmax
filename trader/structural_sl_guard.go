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
	// Method 4: asset-adaptive confirmation timeframe. When enabled, crypto confirms
	// the close-confirm breach on a ~2×-finer TF (backtested optimum) while stocks/
	// commodities keep native (fine TFs whipsaw worst on their session microstructure).
	// Off (default) → native TF, current behaviour. The tighter confirm TF only changes
	// WHICH bar-close we test; boundary/backstop math is unchanged.
	confirmTF := c.Timeframe
	if ss.AssetAdaptiveConfirmTF {
		if adaptive := market.ConfirmTimeframe(c.Timeframe, symbol); adaptive != "" {
			confirmTF = adaptive
		}
	}
	bars, err := market.GetKlines(symbol, confirmTF, at.exchange, fetch)
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

	// ATR (price units) drives cushion/min-profit. No ATR → static. By default this
	// resolves to the open-time FROZEN ATR (unchanged behaviour); when StructuralSL.
	// DynamicATR is opted in, it resolves to a per-main-cycle recomputed candidate so
	// the trail cushion adapts to changing volatility. The monotonic never-loosen gate
	// below guarantees a changing ATR can only tighten the boundary, never widen it.
	atr, ok := at.candidateATRForRatchet(symbol, side, entry, acfg, ss)
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

	// Genuine-tighten gate: computeTrailBoundary decides the ratchet from the PRE-clamp
	// swing, but the clamp above can pull newBoundary back to the entry-floor. When that
	// happens the "tightened" level collapses onto prevBoundary — arming a ratchet that
	// didn't actually move and (worse) parking a physical backup order right on top of the
	// close-confirm level, which destroys its wick-immunity. Only count the ratchet when
	// the clamped level is strictly tighter than prevBoundary; otherwise it is a no-op and
	// the software close-confirm + wide backstop keep covering the position unchanged.
	if newRatchets != prevRatchets {
		genuinelyTighter := (isLong && newBoundary > prevBoundary) || (!isLong && newBoundary < prevBoundary)
		if !genuinelyTighter {
			newBoundary = prevBoundary
			newRatchets = prevRatchets
		}
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
	// Wick-immunity design (2026-07-20): ALL rolling structural levels — the newest tight
	// AND every superseded one — are close-confirm ONLY (software, fire on bar close). We do
	// NOT park a near-price physical intrabar backup at the vacated level: that level sits
	// close to price and a shallow wick would trigger it, re-introducing exactly the wick
	// vulnerability close-confirm eliminates (observed: SOL long wick-closed at 75.32 by the
	// backup @75.30 while the tight close-confirm @75.53 would have ignored the same wick).
	// The ONLY physical resting stop is the wide 4.5-ATR backstop — far from price (a wick
	// reaching it = genuine damage) and doubling as the bot-downtime / gap safety net.
	// BackupBoundary is forced to 0 so syncStructuralBackupStop cancels any legacy backup
	// order and never places a new one.
	if newRatchets != prevRatchets && at.store != nil {
		if state, err := at.store.LoadFrozenATRState(); err == nil {
			rec := state.Records[key]
			rec.TraderID, rec.Symbol, rec.EntryPrice = at.id, symbol, entry
			rec.BackupBoundary = 0             // no near-price physical backup (close-confirm only)
			rec.TrailBoundary = newBoundary    // newest tightest → close-confirm guard
			rec.TrailRatchets = newRatchets
			rec.UpdatedAt = time.Now().Unix()
			if err := at.store.SaveFrozenATRRecord(key, rec); err != nil {
				logger.Warnf("⚠️ [StructuralSL] trail persist failed %s: %v", key, err)
			} else {
				logger.Infof("🔧 [StructuralSL] %s %s trail ratchet #%d → tight %.6f (close-confirm only, no physical backup; prev %.6f, entry-floor %.6f)",
					symbol, sideUpper, newRatchets, newBoundary, prevBoundary, entryBoundary)
			}
			// Side-record this genuine tighten to the ratchet event log so the
			// position-history panel can replay every step (distance %, ATR multiple,
			// price at event). PURE side-effect: it reads no state back into the
			// boundary/close logic, and a write failure is logged-and-ignored so the
			// stop update is never blocked on the audit trail. Keyed identically to the
			// frozen-ATR record; frozen onto the closed row + deleted at position close.
			dist := newBoundary - closedBar.Close
			if dist < 0 {
				dist = -dist
			}
			distPct := 0.0
			if closedBar.Close > 0 {
				distPct = dist / closedBar.Close * 100
			}
			atrMult := 0.0
			if atr > 0 {
				atrMult = dist / atr
			}
			if err := at.store.AppendRatchetEvent(key, store.RatchetEvent{
				TraderID:     at.id,
				Symbol:       symbol,
				Side:         sideUpper,
				EntryPrice:   entry,
				Seq:          newRatchets,
				Boundary:     newBoundary,
				PrevBoundary: prevBoundary,
				PriceAtEvent: closedBar.Close,
				DistPct:      distPct,
				AtrMult:      atrMult,
				Timestamp:    time.Now().Unix(),
			}); err != nil {
				logger.Warnf("⚠️ [StructuralSL] ratchet event log append failed %s: %v", key, err)
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
	key := frozenATRKey(at.id, symbol, tf, sideUpper)

	state, err := at.store.LoadFrozenATRState()
	if err != nil {
		return
	}
	rec, found := state.Records[key]
	if !found || !entrySamePosition(rec.EntryPrice, entry) {
		return
	}
	// Wick-immunity design (2026-07-20, Option 1): NO near-price physical backup stop.
	// All rolling structural levels are close-confirm only (software, bar-close). The only
	// physical resting stop is the wide 4.5-ATR backstop (owned elsewhere). This function is
	// therefore CANCEL-ONLY: it tears down any legacy backup order left over from the old
	// rolling-backup scheme and never places a new one. Because there is no placement path,
	// the prior race (re-placing a backup onto an already-closing position, seen on SOL) is
	// structurally eliminated — a stale qty/entry snapshot can no longer arm anything.
	_ = qty // retained in signature; no longer used to place orders

	if rec.BackupOrderID != "" {
		// CRITICAL: the backup is an ALGO order (SetStopLossTagged → algoId on BOTH OKX and
		// Binance). It MUST be cancelled via the algo endpoint (CancelAlgoOrderByID), NOT the
		// regular CancelOrder path — cancel-order/ordId (OKX) and fapi/v1/order (Binance) do
		// not know about algo orders and silently no-op while logging a fake success. That bug
		// left SKHYNIX @1145.94 / TRUMP @1.602 backups resting on OKX after we "cancelled" them.
		if canceller, ok := at.trader.(interface {
			CancelAlgoOrderByID(symbol, orderID string) error
		}); ok {
			if cErr := canceller.CancelAlgoOrderByID(symbol, rec.BackupOrderID); cErr != nil {
				// Do NOT clear the tracked id on failure — clearing it would strand the order
				// as an untrackable orphan (the exact bug that left SKHYNIX/TRUMP backups live
				// after a fake-success cancel). Keep the id so the next cycle retries the cancel.
				logger.Warnf("⚠️ [StructuralSL] %s %s cancel legacy backup stop %s FAILED (will retry): %v",
					symbol, sideUpper, rec.BackupOrderID, cErr)
			} else {
				logger.Infof("🧹 [StructuralSL] %s %s canceled legacy backup stop %s (close-confirm only now)",
					symbol, sideUpper, rec.BackupOrderID)
				rec.BackupOrderID = "" // clear ONLY after the exchange confirmed the cancel
			}
		} else {
			// No algo-canceller available (shouldn't happen for OKX/Binance) — keep the id.
			logger.Warnf("⚠️ [StructuralSL] %s %s no CancelAlgoOrderByID; leaving backup id %s tracked",
				symbol, sideUpper, rec.BackupOrderID)
		}
	}

	// Keep BackupBoundary cleared so the panel/reconciler never resurface a near-price level.
	if rec.BackupBoundary != 0 {
		rec.BackupBoundary = 0
	}

	rec.UpdatedAt = time.Now().Unix()
	if pErr := at.store.SaveFrozenATRRecord(key, rec); pErr != nil {
		logger.Warnf("⚠️ [StructuralSL] %s %s persist backup teardown failed: %v", symbol, sideUpper, pErr)
	}
}

