package backtest

import (
	"math/rand"
	"sort"
	"strings"

	"nofx/market"
)

// GuardParams configures the portfolio giveback guard overlay tested by the
// time-synchronized PortfolioSim. All distances are in quote currency unless a
// field name says Pct. Zero value = fully disabled (no-op overlay), so a guard
// run with GuardParams{} reproduces the pure-baseline run exactly.
type GuardParams struct {
	Enabled bool

	// L1: per-symbol giveback-velocity guard. Each position tracks its peak
	// unrealized profit (quote ccy). When, within L1WindowBars, the position
	// gives back >= L1GivebackPct of its peak AND the peak reached
	// >= L1MinPeakPct (position profit %), trim L1ClosePct of the position.
	L1Enabled     bool
	L1WindowBars  int
	L1GivebackPct float64
	L1MinPeakPct  float64
	L1ClosePct    float64

	// L2: portfolio circuit breaker. Tracks portfolio total unrealized profit
	// high-water mark. When portfolio gives back >= L2GivebackPct of its peak
	// (within L2WindowBars) AND peak reached >= L2MinPeakQuote, trim L2ClosePct
	// of EACH currently-winning position (de-risk the whole book).
	L2Enabled      bool
	L2WindowBars   int
	L2GivebackPct  float64
	L2MinPeakQuote float64
	L2ClosePct     float64

	// Directional concentration: when net same-side notional exposure ratio
	// >= ConcentrationPct (e.g. 0.7 = 70% one-sided) the L2 giveback threshold
	// is multiplied by ConcTightenMult (<1 = trigger earlier). 0 disables.
	ConcentrationPct float64
	ConcTightenMult  float64

	// Trend-adaptive close ratio: when enabled, the L2 close ratio is chosen by
	// the portfolio's trend strength (notional-weighted avg entry-ADX of open
	// winners). Strong trend (avg ADX >= TrendADXThreshold) => reversals tend to
	// be real => trim L2ClosePctTrend (lock more). Chop (below threshold) =>
	// pullbacks tend to recover => trim L2ClosePctChop (trim less). When
	// AdaptiveClose is false, the flat L2ClosePct is used.
	AdaptiveClose     bool
	TrendADXThreshold float64
	L2ClosePctTrend   float64
	L2ClosePctChop    float64

	// L3: account-level EQUITY drawdown circuit breaker. Tracks the high-water
	// mark of total equity (StartCapital + realized + unrealized). When equity
	// draws down from that peak by a tier's L3Tiers[i].DrawdownPct, it trims
	// L3Tiers[i].ClosePct of EVERY open position (winners AND losers — equity
	// drawdown signals a regime change, so de-risk the whole book, not just
	// winners like L2). Each tier fires once per episode and re-arms only when
	// equity sets a new high-water mark. StartCapital anchors the % drawdown
	// (in live this is account equity at guard start).
	L3Enabled    bool
	StartCapital float64
	L3Tiers      []L3Tier

	// Counter-trend-first cutting. When L3 fires, positions are classified by
	// pnl-velocity over L3VelWindow bars: velocity < -L3CounterVelEps =
	// counter-trend (cut at the tier's full ClosePct); otherwise trend-aligned
	// (cut at ClosePct * L3TrendKeepMult, so 0 = fully preserved as a 2nd-tier
	// runner, 1 = no distinction). Velocity sign is independent of PnL sign — a
	// profitable but reversing position is counter-trend and gets cut first.
	L3VelWindow     int     // bars of look-back for velocity (0 => default 6)
	L3TrendKeepMult float64 // 0..1 multiplier applied to ClosePct for trend-aligned positions
	L3CounterVelEps float64 // velocity threshold (profit%/bar); below -eps = counter-trend

	// Early (rate-based) trigger. If equity is falling faster than
	// L3EquityVelTrigger (% of peak per bar) AND drawdown already exceeds
	// L3EarlyFloorPct, fire the shallowest tier early instead of waiting for the
	// full tier threshold. 0 disables the early trigger.
	L3EquityVelTrigger float64 // equity drop %/bar that arms the early cut
	L3EarlyFloorPct    float64 // minimum drawdown% before the early trigger can fire

	// V-bounce protection for the early trigger. L3EarlyConfirmBars requires the
	// equity velocity to stay below -L3EquityVelTrigger for this many CONSECUTIVE
	// bars before the early cut fires — so a single down-wick (insert pin) that
	// immediately bounces does NOT trigger a cut at the spike low. 0/1 = react on
	// a single bar (no confirmation). L3FireCooldownBars enforces a minimum gap
	// (in bars) between consecutive L3 firings, preventing repeated cuts inside
	// one fast move. 0 disables the cooldown.
	L3EarlyConfirmBars int
	L3FireCooldownBars int

	// --- Rolling-baseline mode (simplified, no tiers/latch/re-arm) ---
	// When L3Mode == "rolling", the multi-tier ladder above is bypassed in favour
	// of a single rolling reference equity. Each time equity falls L3RollDropPct%
	// below the rolling reference, the guard trims positions and RESETS the
	// reference to current equity — so the next cut needs another full drop. No
	// latch, no re-arm, no high-water gate (that whole machinery is the source of
	// the lower-high protection gap). Two cut policies:
	//   - L3RollCounterOnly=true:  cut counter-trend positions in full (by velocity);
	//                              trend-aligned trimmed at L3TrendKeepMult.
	//   - L3RollCounterOnly=false: trim L3RollCutPct% of EVERY remaining position
	//                              (whole-book de-lever; cut-of-current-remaining,
	//                              so repeated drops compound into exponential
	//                              de-leveraging).
	L3Mode            string  // "" = legacy tier ladder; "rolling" = rolling baseline
	L3RollDropPct     float64 // equity drop (% of rolling reference) that arms a cut
	L3RollCutPct      float64 // fraction (%) of each remaining position to trim per cut
	L3RollCounterOnly bool    // true => cut counter-trend in full, keep trend (TrendKeepMult)
	// L3RollScaleCap scales the cut by how far the drop exceeds the threshold
	// (gap/flash-crash protection ②): effCut = cut * min(ScaleCap, drop/DropPct).
	// 1 = no scaling (fixed cut); 3 = a 3x-deep drop cuts up to 3x. 0 => treated as 1.
	L3RollScaleCap float64
	// L3RollReArmPct is the whipsaw guard ①: after a cut, the rolling reference is
	// re-based to current equity, but it will NOT rise again (and thus cannot arm a
	// fresh cut on a small bounce) until equity recovers at least this % above the
	// post-cut equity. 0 => reference rises on any new high (most reactive).
	L3RollReArmPct float64

	// --- Exposure gate (user spec: 仓位>2 或 保证金使用率达标 才允许熔断) ---
	// The rolling/tier breakers above fire on equity drawdown alone, which means
	// they can fire while the book is tiny (1 position, 5% margin) — where a
	// whole-book flatten is pure cost and no risk reduction. This gate requires
	// the book to actually be exposed before the breaker is allowed to act:
	//
	//	fire allowed  <=>  openCount >= L3GateMinPos  OR  marginPct >= L3GateMarginPct
	//
	// marginPct is LEVERAGE-AWARE: sum(remaining notional)/L3NominalLeverage is
	// the initial margin the exchange actually holds, and that over current
	// equity is the utilisation the user is describing. Without dividing by
	// leverage, "margin used" would be notional/equity, which at 10x reads 371%
	// on a normal book and makes the gate meaningless.
	//
	// Both zero => gate disabled (fire on drawdown alone, previous behaviour).
	L3GateMinPos      int     // minimum open positions that opens the gate (0 = ignore this leg)
	L3GateMarginPct   float64 // margin utilisation % that opens the gate (0 = ignore this leg)
	L3NominalLeverage float64 // nominal leverage for the margin calc (0 => 1, i.e. notional/equity)

	// --- Cycle breaker: equity freeze + new cycle (熔断=权益价值冻结，之后开仓算新周期) ---
	// This is the macro/portfolio formulation of the account circuit breaker, and
	// it is the one to prefer over the rolling/tier breakers plus L3Reentry.
	//
	// Semantics. When it fires, every open position is closed at the current mark.
	// That does not destroy value, it CRYSTALLISES it, so the account curve is
	// continuous through a firing. The realised equity at that instant becomes the
	// new cycle's baseline: the drawdown peak resets to it, and the positions the
	// trader actually opened after that instant constitute the next cycle.
	//
	// Why this is more rigorous than modelling re-entry. L3ReentryBars had to
	// invent a counterfactual — "the same position is re-established N bars later
	// at price P" — for which there is no evidence; the trader may never have
	// touched that symbol again. Invented exposure then compounds, which is what
	// produced severe non-monotonicity in the firing counts. Here nothing is
	// invented: the breaker only chooses WHERE TO TRUNCATE positions that were
	// genuinely open, and every entry in the run is a real observed decision. The
	// entry universe is therefore identical for every parameter value, which is
	// what makes two thresholds comparable at all.
	//
	// Residual assumption, stated plainly: the trader's post-firing entries were
	// actually taken in a world where the book had NOT been flattened. Whether
	// they would have opened the same positions with a clean book is unknowable.
	// Treating entry signals as exogenous is the standard assumption for an
	// overlay study, and it is a far smaller one than fabricating re-entries.
	L3CycleEnabled bool
	// L3CycleDDPct fires the breaker when equity falls this % below the CURRENT
	// cycle's peak. Measured on the continuous account curve.
	L3CycleDDPct float64
	// L3CycleArmGainPct is 「正收益达到一定比例才挂上回撤熔断」: the cycle must first
	// gain this % above its baseline before the drawdown breaker arms. Until then
	// the book runs on per-position control alone (「否则各自仓位控制」). 0 = always
	// armed from the cycle's start.
	L3CycleArmGainPct float64

	// --- Placebo control (falsification test for the freeze model) ---
	// Under the freeze model a firing is a PERMANENT early exit: the truncated
	// positions are never re-established, only later real entries arrive. For a
	// book whose baseline loses money, cutting positions early at ANY time tends to
	// improve PnL, so "breaker beats baseline" is not evidence that the drawdown
	// trigger works. It may just be evidence that trading less would have helped.
	//
	// L3CyclePlaceboProb replaces the drawdown trigger with a seeded coin flip of
	// this per-tick probability, keeping everything else identical (whole-book
	// freeze, gate, arm, fees, new-cycle re-anchor). Sweeping the probability
	// produces placebo runs at matched firing counts. The real trigger is only
	// worth anything if it beats the placebo AT THE SAME NUMBER OF FIRINGS.
	L3CyclePlaceboProb float64
	L3CyclePlaceboSeed int64

	// L3CycleMeasureOnly evaluates the trigger and counts firings but does NOT
	// close positions and does NOT re-anchor the cycle. It exists to separate two
	// causes of non-monotone firing counts that look identical in output:
	//
	//   (a) a measurement bug — the trigger is not reading the drawdown it claims;
	//   (b) closed-loop feedback — firing truncates the book, which changes later
	//       equity, which changes later triggers.
	//
	// With feedback removed, counts MUST be non-increasing in the threshold. If they
	// are, residual violations in the live sweep are (b), which is intrinsic to the
	// model rather than a defect: the cycle model re-anchors the drawdown peak at
	// every firing, so it has structurally STRONGER feedback than a de-lever model.
	L3CycleMeasureOnly bool

	// --- Breaker re-entry (user spec: 熔断将会降低损失并重新开仓，你需要仿真) ---
	// Without this, a flatten is a permanent early exit and the sim silently
	// credits the breaker with avoiding every later adverse move — it removes
	// exposure and never pays to restore it. That overstates the breaker badly,
	// because a live breaker re-enters and pays a full round trip each time.
	//
	// When L3ReentryBars > 0, each position the breaker flattens is re-opened
	// L3ReentryBars bars later at that bar's close, with the original quantity,
	// fresh SL/TP/BE levels computed from the new entry price, and full entry+exit
	// fees charged on both legs. L3ReentryMaxCycles bounds how many times one
	// position may be recycled, so a whipsawing market cannot manufacture
	// unlimited round trips.
	//
	// This models "flatten now, re-establish the same view shortly after". It does
	// NOT model the trader changing their mind about direction — that would need a
	// decision model, which no backtest can supply.
	L3ReentryBars      int
	L3ReentryMaxCycles int

	// --- Two-state arm (user spec: 熔断后要正收益达到一定比例才重新挂上熔断) ---
	// After a firing, the breaker DISARMS entirely: per-position control (SL/BE/
	// DD/TP) carries the book alone until equity has recovered L3ArmProfitPct%
	// above the equity level at that firing. Only then does the drawdown breaker
	// re-arm. This differs from L3RollReArmPct, which only pins the rolling
	// REFERENCE (the breaker can still fire below the floor); this suppresses the
	// trigger outright, which is what "达不到一定比例就各自仓位控制" means.
	//
	// 0 => always armed (no disarm phase).
	L3ArmProfitPct float64

	// --- ATR-normalized trigger (leverage-free, volatility-normalized) ---
	// When L3RollATRMult > 0, the rolling breaker fires on the portfolio-weighted
	// ADVERSE MOVE IN ATR UNITS instead of raw equity drawdown%. Rationale: raw
	// equity drop% = leverage × price-move%, so at 10x a 5% equity drop is only a
	// 0.5% price move — inside noise, tighter than a stop. Measuring the adverse
	// move as (adverse price-move% / atrPct) weighted by notional share is
	// leverage-free and volatility-normalized, and shares the stop's unit (k×ATR).
	// Fire when the weighted adverse-ATR >= L3RollATRMult (e.g. 1.5). The cut SIZE
	// still scales via L3RollScaleCap on the ATR overshoot.
	L3RollATRMult float64

	// --- Breadth breaker (per-symbol monitoring + correlation-reversal gate) ---
	// A redesign that replaces account-equity circuit breakers entirely. Instead
	// of one global equity-drawdown trigger (leverage-contaminated, fires on
	// 0.5% noise at 10x), it monitors EACH position and acts only when a MAJORITY
	// of held symbols retrace together — the signature of a correlated reversal,
	// not single-symbol noise. When BreadthEnabled is set this path runs INSTEAD
	// of the equity/rolling breakers.
	//
	// Trigger: count positions that are "retracing" (see BreadthUseATR / velocity
	// vs peak-giveback) and fire when retracingCount/total >= BreadthFrac AND
	// total >= BreadthMinPos (a "majority" needs a quorum).
	//
	// Action (surgical): cut LOSING positions (profitPct<0) that are retracing in
	// full (BreadthLoserCutPct, default 100); WINNING positions (profitPct>=0) are
	// left untouched — in live they are protected by their break-even stop. This
	// stops the bleeding side while letting BE lock in the profitable side.
	BreadthEnabled     bool
	BreadthMinPos      int     // minimum open positions before the gate can fire (quorum)
	BreadthFrac        float64 // fraction (0..1) of positions retracing that fires the gate
	BreadthLoserCutPct float64 // % of each losing+retracing position to cut (default 100)
	// Retracement definition. BreadthUseATR=false: a position is "retracing" when
	// its peak-to-current giveback exceeds BreadthGivebackPct OR its pnl-velocity
	// is negative. BreadthUseATR=true: retracing when the adverse move from peak
	// exceeds BreadthATRMult ATRs (volatility-normalized). Velocity uses L3VelWindow.
	BreadthUseATR      bool
	BreadthGivebackPct float64 // peak-to-current giveback% that counts as retracing (pnl% mode)
	BreadthATRMult     float64 // adverse-from-peak in ATR units that counts as retracing (ATR mode)
	BreadthVelEps      float64 // velocity threshold (profit%/bar); below -eps counts as retracing
}

// L3Tier is one staged-cut rung of the equity drawdown circuit breaker.
type L3Tier struct {
	DrawdownPct float64 // equity drawdown from peak (%) that arms this rung
	ClosePct    float64 // fraction (%) of EACH open position to trim when it fires
}

// SimResult is the outcome of a synchronized portfolio simulation.
type SimResult struct {
	TotalPnL       float64 // realized PnL summed across all positions at sim end
	MaxPortfolioDD float64 // max drawdown of realized+unrealized equity curve (quote)
	MaxGiveback    float64 // largest peak->trough drop of total UNREALIZED profit (quote)
	GuardTrims     int     // count of guard-triggered partial closes
	GuardClosedQty float64 // total fraction-equivalent closed by guard
	WinRatePct     float64
	Trades         int
	// L3Fires is the number of per-position TRIMS performed by L3, NOT the number
	// of breaker firings: one whole-book flatten of 4 positions adds 4. Kept with
	// its original meaning so existing sweeps/tests are unchanged.
	L3Fires int
	// L3Events is the number of distinct breaker FIRINGS (ticks on which L3
	// acted). This is the one to use for firing frequency, cost-per-fire and any
	// monotonicity reasoning; L3Fires conflates frequency with book size.
	L3Events int
	// L3Reentries is how many times a breaker-flattened position was re-opened.
	// Zero when L3ReentryBars is unset, in which case a flatten is a permanent
	// early exit and the breaker's benefit is overstated.
	L3Reentries int
	// L3Cycles is how many equity-freeze cycles the run was divided into
	// (1 = never fired). L3CycleFires = L3Cycles-1 when the cycle breaker is on.
	L3Cycles    int
	L3ClosedQty float64 // total fraction-equivalent closed by L3
	FeesPaid    float64 // total trading fees charged (quote ccy); 0 when FeeRatePct=0

	// Breaker occupancy diagnostics. A config with L3Fires=0 is ambiguous without
	// these: it could mean the market never triggered it, or that the arm/gate
	// suppressed every trigger it had. ArmedPct/GatePct/TriggerHits separate those.
	BreakerTicks  int // ticks the rolling breaker was consulted
	ArmedTicks    int // of those, ticks the two-state arm was armed
	GateOpenTicks int // of those, ticks the exposure gate was open
	TriggerHits   int // ticks the raw drawdown threshold was met (pre-suppression)
}

// tpLvl mirrors replay.go's tpLevel (kept local to avoid touching the tested engine).
type tpLvl struct {
	price float64
	frac  float64
	fired bool
}

// simPos is the per-position stateful protection machine, a bar-stepping mirror
// of ReplayEntry. It exposes step() for one bar and tracks guard state.
type simPos struct {
	e      Entry
	isLong bool

	slPrice     float64
	tps         []tpLvl
	beStop      float64
	beArmedTier int
	ddFired     []bool

	// --- Breaker re-entry state ---
	// bankedPnL holds realized PnL from PREVIOUS entry cycles of this position.
	// A breaker flatten followed by a re-entry restarts the position at a new
	// entry price, which resets the VWAP-exit accumulators; without banking, that
	// reset would erase the loss the breaker just took, making the breaker look
	// free. realizedPnL therefore returns banked + current-cycle.
	bankedPnL float64
	// pendingReentry is set when the breaker flattened this position and it is
	// scheduled to re-open. reentryAtMs is the bar OpenTime at/after which the
	// re-entry fills; cycles counts completed re-entries (bounded to stop a
	// pathological loop from generating unlimited round trips).
	pendingReentry bool
	reentryAtMs    int64
	reentryCycles  int
	// pendingRestoreFrac is how much of the ORIGINAL position size the breaker
	// cut and that is awaiting restore. Accumulated across trims within one
	// pending window, so a partial de-lever is rebuilt to its prior size.
	pendingRestoreFrac float64

	// feeQuote is cumulative trading fees in quote ccy (entry fill + every partial
	// exit). Subtracted inside realizedPnL so equity, MaxPortfolioDD and TotalPnL
	// are all fee-aware from one place. This matters specifically for the circuit
	// breaker: its only cost IS churn, so a zero-fee sim makes firing look free
	// and biases any sweep toward firing more often.
	feeQuote float64
	// feeRatePct is the per-side taker fee in percent, copied from
	// ProtectionParams at construction so addExit needs no extra plumbing.
	feeRatePct float64

	remaining    float64 // fraction of original position still open (0..1)
	peakPnlPct   float64 // peak position profit % (for BE/DD/L1 arming)
	exitNotional float64 // VWAP exit accounting
	exitFrac     float64
	closeReasons []string

	// guard state
	peakUnrealQuote float64 // peak unrealized profit in quote ccy (L1 high-water)
	l1FiredAtPeak   float64 // peakPnlPct value when L1 last fired (ratchet re-arm)
	entryADX        float64 // Wilder ADX at entry (trend strength of entry regime)
	armed           bool    // entry bar reached
	done            bool    // fully closed

	// velocity tracking: rolling window of recent profit% samples (favorable
	// sign). Used by L3 to classify counter-trend (deteriorating, velocity<0)
	// vs trend-aligned (improving, velocity>=0) positions — independent of PnL
	// sign, so a profitable-but-reversing position is treated as counter-trend.
	pnlHist []float64

	// atrPct is the position's ATR (at entry) as a percent of entry price. Used
	// by the ATR-normalized L3 trigger to measure adverse moves in ATR units
	// (leverage-free, volatility-normalized) instead of raw equity drawdown%.
	atrPct float64
}

// recordPnl appends the current profit% sample to the velocity window (bounded).
func (sp *simPos) recordPnl(price float64) {
	sp.pnlHist = append(sp.pnlHist, pnlPct(sp.e.Side, sp.e.EntryPrice, price))
	if len(sp.pnlHist) > velHistCap {
		sp.pnlHist = sp.pnlHist[len(sp.pnlHist)-velHistCap:]
	}
}

// pnlVelocity returns the profit%-change per bar over the last `window` bars.
// >0 = position improving (trend-aligned), <0 = deteriorating (counter-trend).
func (sp *simPos) pnlVelocity(window int) float64 {
	if window < 1 {
		window = 1
	}
	n := len(sp.pnlHist)
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
	return (sp.pnlHist[n-1] - sp.pnlHist[lo]) / float64(span)
}

// velHistCap bounds the per-position velocity sample ring.
const velHistCap = 64

// newSimPos resolves protective levels at entry (mirrors ReplayEntry setup).
func newSimPos(p ProtectionParams, e Entry, atr float64) *simPos {
	isLong := strings.EqualFold(e.Side, "long")
	sp := &simPos{
		e:           e,
		isLong:      isLong,
		beArmedTier: -1,
		remaining:   1.0,
		ddFired:     make([]bool, len(p.DDRules)),
		feeRatePct:  p.FeeRatePct,
	}
	// Entry fill fee, charged once on full notional. FeeRatePct defaults to 0, so
	// every pre-existing caller and test is unaffected until it opts in.
	sp.feeQuote = p.FeeRatePct / 100.0 * e.EntryPrice * e.Quantity
	if e.EntryPrice > 0 && atr > 0 {
		sp.atrPct = atr / e.EntryPrice * 100
	}
	slDist := p.slDistancePct(e.EntryPrice, atr)
	sp.slPrice = priceAtDistance(e.EntryPrice, slDist, isLong, false)
	for _, leg := range p.TPLegs {
		d := legDistancePct(leg, p.Unit, e.EntryPrice, atr)
		sp.tps = append(sp.tps, tpLvl{
			price: priceAtDistance(e.EntryPrice, d, isLong, true),
			frac:  leg.CloseRatioPct / 100.0,
		})
	}
	return sp
}

// addExit records a partial/full close at price for fraction frac of original.
func (sp *simPos) addExit(price, frac float64, reason string) {
	if frac <= 0 {
		return
	}
	if frac > sp.remaining {
		frac = sp.remaining
	}
	sp.exitNotional += price * frac
	sp.exitFrac += frac
	sp.remaining -= frac
	// Exit fee on the closed slice's notional at the fill price.
	sp.feeQuote += sp.feeRatePct / 100.0 * price * sp.e.Quantity * frac
	sp.closeReasons = append(sp.closeReasons, reason)
	if sp.remaining <= 1e-9 {
		sp.done = true
	}
}

// unrealizedQuote returns current open-fraction unrealized PnL in quote ccy.
func (sp *simPos) unrealizedQuote(price float64) float64 {
	if sp.remaining <= 1e-9 {
		return 0
	}
	return pnlPct(sp.e.Side, sp.e.EntryPrice, price) / 100.0 * sp.e.EntryPrice * sp.e.Quantity * sp.remaining
}

// notionalRemaining returns the open notional (entry-priced) for exposure calc.
func (sp *simPos) notionalRemaining() float64 {
	if sp.remaining <= 1e-9 {
		return 0
	}
	return sp.e.EntryPrice * sp.e.Quantity * sp.remaining
}

// stepBaseline applies one bar of baseline protection (SL/BE/TP/DD), mirroring
// ReplayEntry's intrabar convention (adverse extreme before favorable). Returns
// true if the position closed this bar. Guard logic is applied separately by
// the portfolio loop after all baseline steps, so baseline and guard never
// double-count within a bar.
func (sp *simPos) stepBaseline(p ProtectionParams, atr float64, bar market.Kline) {
	if sp.done {
		return
	}
	// Faithful mode: no modelled SL/TP/BE/DD. The position is carried until the
	// sim loop closes it at the trader's real exit, so the baseline equity path is
	// the one actually traded rather than one invented by a ladder the trader
	// never used. The guard/breaker still runs, so it remains the only overlay.
	if p.FaithfulExits {
		return
	}
	// --- adverse extreme first: SL / BE stop ---
	stopPrice := sp.slPrice
	stopClosesAll := true
	if sp.beStop > 0 {
		stopPrice = sp.beStop
		stopClosesAll = false
	}
	adverseHit := false
	if sp.isLong {
		adverseHit = bar.Low <= stopPrice
	} else {
		adverseHit = bar.High >= stopPrice
	}
	if adverseHit {
		if stopClosesAll {
			sp.addExit(stopPrice, sp.remaining, "stop_loss")
		} else {
			cum := cumulativeBERatio(p.BELegs, sp.beArmedTier)
			closeFrac := cum - (1.0 - sp.remaining)
			if closeFrac > sp.remaining {
				closeFrac = sp.remaining
			}
			if closeFrac > 0 {
				sp.addExit(stopPrice, closeFrac, "break_even")
			}
			sp.beStop = 0
			sp.beArmedTier = -1
		}
		return
	}

	// --- favorable extreme: take-profit legs ---
	for ti := range sp.tps {
		if sp.tps[ti].fired {
			continue
		}
		tpHit := false
		if sp.isLong {
			tpHit = bar.High >= sp.tps[ti].price
		} else {
			tpHit = bar.Low <= sp.tps[ti].price
		}
		if tpHit {
			sp.tps[ti].fired = true
			sp.addExit(sp.tps[ti].price, sp.tps[ti].frac, "take_profit")
		}
	}
	if sp.done {
		return
	}

	// --- end-of-bar: peak, BE arming, DD ---
	closePnL := pnlPct(sp.e.Side, sp.e.EntryPrice, bar.Close)
	if closePnL > sp.peakPnlPct {
		sp.peakPnlPct = closePnL
	}
	for ti := range p.BELegs {
		trig := beTriggerPct(p.BELegs[ti], p.Unit, sp.e.EntryPrice, atr)
		if closePnL >= trig && ti > sp.beArmedTier {
			off := beOffsetPct(p.BELegs[ti], p.Unit, sp.e.EntryPrice, atr)
			sp.beStop = priceAtDistance(sp.e.EntryPrice, off, sp.isLong, true)
			sp.beArmedTier = ti
		}
	}
	if sp.peakPnlPct > 0 {
		giveback := (sp.peakPnlPct - closePnL) / sp.peakPnlPct * 100
		for di := range p.DDRules {
			if sp.ddFired[di] {
				continue
			}
			minP := ddMinProfitPct(p.DDRules[di], p.Unit, sp.e.EntryPrice, atr)
			if closePnL >= minP && giveback >= p.DDRules[di].MaxDrawdownPct {
				sp.ddFired[di] = true
				sp.addExit(bar.Close, p.DDRules[di].CloseRatioPct/100.0, "drawdown")
			}
		}
	}
}

// finalize marks any unclosed remainder to the last seen close (open-ended exit).
func (sp *simPos) finalize(lastClose float64) {
	if sp.remaining > 1e-9 {
		sp.addExit(lastClose, sp.remaining, "mark_to_market")
	}
}

// realizedPnL returns the position's realized PnL given its VWAP exit.
func (sp *simPos) realizedPnL() float64 {
	// Fees are charged even before any exit (the entry fill is already paid), so
	// they are subtracted outside the exitFrac guard. bankedPnL carries PnL from
	// pre-re-entry cycles (see the field docs) and is always included.
	if sp.exitFrac <= 0 {
		return sp.bankedPnL - sp.feeQuote
	}
	// Gross is scaled by exitFrac: only the CLOSED fraction has realized PnL.
	//
	// This corrects a real defect. The original form omitted exitFrac and priced
	// the VWAP exit on full quantity, which is right only when the position is
	// completely closed (exitFrac==1). But realizedPnL is also read MID-FLIGHT to
	// build the portfolio equity curve, and there partial closes were booked at
	// full size — a 30% trim was credited as 3.33x its true value. Since the L3
	// equity breaker triggers on exactly that curve, it was reading an inflated
	// series, and any partial-close protection layer (TP/BE/DD ladders, guard
	// trims) inflated it further.
	//
	// Verified against the independent ReplayEntry path, which sums per-leg PnL
	// correctly, by TestPortfolioSimGuardDisabledMatchesBaseline.
	exitPrice := sp.exitNotional / sp.exitFrac
	gross := pnlPct(sp.e.Side, sp.e.EntryPrice, exitPrice) / 100.0 *
		sp.e.EntryPrice * sp.e.Quantity * sp.exitFrac
	return sp.bankedPnL + gross - sp.feeQuote
}

// simEntry bundles a prepared entry with its bars, entry index, and entry ATR.
type simEntry struct {
	loaded loadedEntry
	atr    float64
}

// GuardSweepRow is one evaluated guard config plus its DD-cut/PnL-cost score,
// exported so cmd tools can rank and print without naming the unexported
// loadedEntry type.
type GuardSweepRow struct {
	Guard   GuardParams
	Result  SimResult
	DDCut   float64 // baseline.MaxPortfolioDD - result.MaxPortfolioDD
	PnLCost float64 // baseline.TotalPnL - result.TotalPnL
	Score   float64 // DDCut - PnLCost
}

// SweepGuards runs the baseline (guard off) then every guard in grid over the
// prepared entries, returning the baseline result and rows sorted by Score desc.
func SweepGuards(grid []GuardParams, loaded []loadedEntry) (SimResult, []GuardSweepRow) {
	p := ClaudeBaselineParams()
	base := RunPortfolioSim(p, GuardParams{}, loaded)
	rows := make([]GuardSweepRow, 0, len(grid))
	for _, g := range grid {
		r := RunPortfolioSim(p, g, loaded)
		ddCut := base.MaxPortfolioDD - r.MaxPortfolioDD
		pnlCost := base.TotalPnL - r.TotalPnL
		rows = append(rows, GuardSweepRow{
			Guard: g, Result: r, DDCut: ddCut, PnLCost: pnlCost, Score: ddCut - pnlCost,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Score > rows[j].Score })
	return base, rows
}

// RunPortfolioSim runs a time-synchronized multi-position simulation over the
// prepared entries, applying baseline protection plus the giveback guard
// overlay. It steps a master clock (union of all bar open-times at 1h grid) and,
// at each tick, advances every active position one bar then applies guards.
//
// With GuardParams{} (disabled) the realized PnL equals the pure baseline
// (sum of ReplayEntry) up to intrabar-ordering identity, validated by tests.
func RunPortfolioSim(p ProtectionParams, g GuardParams, loaded []loadedEntry) SimResult {
	res, _ := runPortfolioSim(p, g, loaded, false)
	return res
}

// RunPortfolioSimTrace runs the sim and additionally returns the per-firing L3
// trace (empty unless L3 is enabled and fires). Used by the trace CLI to show
// the breaker's decisions on a real position sequence, not just final PnL/DD.
func RunPortfolioSimTrace(p ProtectionParams, g GuardParams, loaded []loadedEntry) (SimResult, []L3FireEvent) {
	return runPortfolioSim(p, g, loaded, true)
}

func runPortfolioSim(p ProtectionParams, g GuardParams, loaded []loadedEntry, withTrace bool) (SimResult, []L3FireEvent) {
	// indicatorWindow bounds how many pre-entry bars feed ATR/ADX. Period-14
	// Wilder indicators converge well within this, and bounding it keeps the
	// per-entry cost O(window) instead of O(entryIdx) — critical for sweeps over
	// long histories (late entries would otherwise scan thousands of bars each).
	const indicatorWindow = 60

	// Precompute entry ATR per loaded entry (mirrors ReplayEntry).
	sims := make([]simEntry, 0, len(loaded))
	for _, le := range loaded {
		atr := 0.0
		if le.entryIdx >= atrLookback {
			lo := le.entryIdx - indicatorWindow
			if lo < 0 {
				lo = 0
			}
			h, l, c := sliceOHLCRange(le.bars, lo, le.entryIdx-1)
			atr = wilderATR(h, l, c, atrLookback)
		}
		sims = append(sims, simEntry{loaded: le, atr: atr})
	}

	// Build master clock = sorted union of all bar OpenTimes across entries.
	tset := map[int64]struct{}{}
	for _, s := range sims {
		for _, b := range s.loaded.bars {
			tset[b.OpenTime] = struct{}{}
		}
	}
	clock := make([]int64, 0, len(tset))
	for t := range tset {
		clock = append(clock, t)
	}
	sort.Slice(clock, func(i, j int) bool { return clock[i] < clock[j] })

	// Per-entry: map OpenTime -> bar index for O(1) lookup, and the position machine.
	type live struct {
		se        simEntry
		barIdx    map[int64]int
		pos       *simPos
		started   bool
		lastClose float64
	}
	lives := make([]*live, 0, len(sims))
	for _, s := range sims {
		bi := make(map[int64]int, len(s.loaded.bars))
		for i, b := range s.loaded.bars {
			bi[b.OpenTime] = i
		}
		pos := newSimPos(p, s.loaded.entry, s.atr)
		// Entry-ADX: trend strength of the regime the position opened into.
		// Computed once from a bounded pre-entry window (no look-ahead). Used by
		// the trend-adaptive close ratio. atrLookback also serves as ADX period.
		// ADX needs 2*period+1 bars to seed; use a window of 4*period to be safe.
		if s.loaded.entryIdx >= 2*atrLookback+1 {
			lo := s.loaded.entryIdx - 4*atrLookback
			if lo < 0 {
				lo = 0
			}
			h, l, c := sliceOHLCRange(s.loaded.bars, lo, s.loaded.entryIdx-1)
			pos.entryADX = wilderADX(h, l, c, atrLookback)
		}
		lives = append(lives, &live{
			se:     s,
			barIdx: bi,
			pos:    pos,
		})
	}

	// Portfolio-level guard state.
	var portfolioPeakUnreal float64
	gstate := &guardState{}
	var l3trace []L3FireEvent
	if withTrace {
		gstate.trace = &l3trace
	}
	var maxGiveback float64
	var realizedEquity, peakEquity, maxDD float64
	var guardTrims int
	var guardClosedQty float64
	var l3Fires int
	var l3Events int
	var reentries int
	var l3ClosedQty float64

	res := SimResult{Trades: len(lives)}

	for _, t := range clock {
		gstate.traceTick = t

		// 0) fill any scheduled breaker re-entries due at this tick. Done before
		//    stepping so a re-entered position is marked and guarded this bar like
		//    any other open position. The re-entry is only filled while the
		//    original entry's exit window is still open: re-establishing a position
		//    past the point the trader had actually closed it would invent exposure
		//    the trader never had.
		if g.L3ReentryBars > 0 {
			for _, lv := range lives {
				if !lv.pos.pendingReentry {
					continue
				}
				bi, ok := lv.barIdx[t]
				if !ok {
					continue
				}
				if lv.pos.reentryAtMs == 0 {
					// Resolve the due bar the first time we see this symbol's clock
					// after the flatten: L3ReentryBars bars forward from here.
					due := bi + g.L3ReentryBars
					if due >= len(lv.se.loaded.bars) {
						lv.pos.pendingReentry = false
						continue
					}
					lv.pos.reentryAtMs = lv.se.loaded.bars[due].OpenTime
				}
				if lv.se.loaded.bars[bi].OpenTime < lv.pos.reentryAtMs {
					continue
				}
				e := lv.se.loaded.entry
				if e.ExitTime > 0 && lv.se.loaded.bars[bi].OpenTime > e.ExitTime {
					lv.pos.pendingReentry = false // window closed; no re-entry
					continue
				}
				lv.pos.reenter(p, lv.se.loaded.bars[bi].Close, lv.se.atr)
				reentries++
			}
		}

		// 1) advance each active position one baseline bar at this tick.
		for _, lv := range lives {
			if lv.pos.done {
				continue
			}
			bi, ok := lv.barIdx[t]
			if !ok {
				continue // this symbol has no bar at this tick
			}
			e := lv.se.loaded.entry
			// respect entry window: only start at/after entry bar, stop after exit.
			if bi < lv.se.loaded.entryIdx {
				continue
			}
			if e.ExitTime > 0 && lv.se.loaded.bars[bi].OpenTime > e.ExitTime {
				if !lv.pos.done {
					// Faithful mode closes at the trader's REAL fill price, so the
					// baseline reproduces realized PnL. Otherwise fall back to the
					// last bar close, which is the engine's original convention.
					px := lv.lastClose
					if p.FaithfulExits && e.ExitPrice > 0 {
						px = e.ExitPrice
					}
					lv.pos.finalize(px)
				}
				continue
			}
			lv.started = true
			bar := lv.se.loaded.bars[bi]
			lv.lastClose = bar.Close
			lv.pos.stepBaseline(p, lv.se.atr, bar)
			// update per-position peak unrealized (quote) for L1.
			u := lv.pos.unrealizedQuote(bar.Close)
			if u > lv.pos.peakUnrealQuote {
				lv.pos.peakUnrealQuote = u
			}
			// sample profit% for L3 velocity (trend-direction) classification.
			lv.pos.recordPnl(bar.Close)
		}

		// 2) measure portfolio unrealized + equity BEFORE guards, so both the
		//    L2 (unrealized giveback) and L3 (equity drawdown) breakers act on a
		//    consistent snapshot of this tick.
		var preUnreal, preRealized float64
		for _, lv := range lives {
			price := lv.lastClose
			if lv.started && !lv.pos.done && price > 0 {
				preUnreal += lv.pos.unrealizedQuote(price)
			}
			preRealized += lv.pos.realizedPnL()
		}
		if preUnreal > portfolioPeakUnreal {
			portfolioPeakUnreal = preUnreal
		}
		curEquity := g.StartCapital + preRealized + preUnreal
		if curEquity > gstate.equityPeak {
			gstate.equityPeak = curEquity
		}

		// 3) apply guards (after all baseline steps this tick). L3 (equity breaker)
		//    runs FIRST; if it fires this tick, L1/L2 are suppressed to avoid
		//    double-cutting the same positions on one bar (⑦).
		if g.Enabled {
			gps := make([]guardPos, 0, len(lives))
			for _, lv := range lives {
				if lv.started && !lv.pos.done && lv.lastClose > 0 {
					gps = append(gps, guardPos{pos: lv.pos, price: lv.lastClose})
				}
			}
			if g.L3CycleEnabled {
				// Cycle breaker REPLACES the other account-level breakers: it is the
				// same decision (act on account equity drawdown) expressed as a
				// freeze-and-restart rather than as a partial de-lever.
				nc, ncq := applyCycleBreaker(g, gps, curEquity, gstate)
				l3Fires += nc
				l3ClosedQty += ncq
				if nc > 0 {
					l3Events++
				}
				if gstate.cyclePendingAnchor {
					// Re-anchor from the full book: capital + all realised PnL.
					anchor := g.StartCapital
					for _, lv2 := range lives {
						anchor += lv2.pos.realizedPnL()
					}
					gstate.cycleBase, gstate.cyclePeak = anchor, anchor
					gstate.cyclePendingAnchor = false
				}
			} else if g.BreadthEnabled {
				// Breadth breaker REPLACES the equity/rolling circuit breakers:
				// per-symbol monitoring + majority-retrace gate + cut losers only.
				nb, ncqb := applyBreadthBreaker(g, gps, gstate)
				l3Fires += nb
				if nb > 0 {
					l3Events++
				}
				l3ClosedQty += ncqb
			} else {
				n3, ncq3 := applyEquityBreaker(g, gps, curEquity, gstate.equityPeak, gstate)
				l3Fires += n3
				l3ClosedQty += ncq3
				if n3 > 0 {
					l3Events++ // one tick on which the breaker acted, regardless of book size
				}
				if n3 == 0 {
					nt, ncq := applyGuards(g, gps, portfolioPeakUnreal, gstate)
					guardTrims += nt
					guardClosedQty += ncq
				}
			}
		}

		// 4) re-measure portfolio unrealized + equity for giveback / DD stats
		//    (post-guard, so reported DD reflects the protected curve).
		var totalUnreal, realizedSoFar float64
		for _, lv := range lives {
			price := lv.lastClose
			if lv.started && !lv.pos.done && price > 0 {
				totalUnreal += lv.pos.unrealizedQuote(price)
			}
			realizedSoFar += lv.pos.realizedPnL()
		}
		if gb := portfolioPeakUnreal - totalUnreal; gb > maxGiveback {
			maxGiveback = gb
		}
		equity := realizedSoFar + totalUnreal
		if equity > peakEquity {
			peakEquity = equity
		}
		if dd := peakEquity - equity; dd > maxDD {
			maxDD = dd
		}
		_ = realizedEquity
	}

	// finalize any still-open positions to their last close.
	for _, lv := range lives {
		if !lv.pos.done {
			lv.pos.finalize(lv.lastClose)
		}
	}

	var wins int
	for _, lv := range lives {
		pnl := lv.pos.realizedPnL()
		res.TotalPnL += pnl
		if pnl >= 0 {
			wins++
		}
	}
	if len(lives) > 0 {
		res.WinRatePct = float64(wins) / float64(len(lives)) * 100
	}
	res.MaxPortfolioDD = maxDD
	res.MaxGiveback = maxGiveback
	res.GuardTrims = guardTrims
	res.GuardClosedQty = guardClosedQty
	res.L3Fires = l3Fires
	res.L3Events = l3Events
	res.L3Reentries = reentries
	res.L3Cycles = gstate.cycleCount
	res.L3ClosedQty = l3ClosedQty
	for _, lv := range lives {
		res.FeesPaid += lv.pos.feeQuote
	}
	res.BreakerTicks = gstate.ticksTotal
	res.ArmedTicks = gstate.ticksArmed
	res.GateOpenTicks = gstate.ticksGateOpen
	res.TriggerHits = gstate.ticksTriggerHit
	return res, l3trace
}

// guardPos pairs a position machine with its current mark price for the guard.
type guardPos struct {
	pos   *simPos
	price float64
}

// guardState carries cross-tick ratchet state for the portfolio (L2) guard so
// it fires once per reversal episode and re-arms only on a new portfolio peak.
type guardState struct {
	l2FiredAtPeak float64 // portfolioPeak value when L2 last fired (0 = armed)

	// L3 equity circuit breaker ratchet.
	equityPeak    float64 // high-water mark of total equity (capital+realized+unreal)
	l3TierFiredDD []bool  // per-tier latch: true = that rung already fired this episode
	l3FiredAtPeak float64 // equityPeak when ANY L3 tier last fired (re-arm gate)
	prevEquity    float64 // last tick's equity (for equity-velocity early trigger)
	hasPrevEquity bool
	negVelStreak  int // consecutive bars with equity velocity below the early-trigger threshold
	barsSinceFire int // bars elapsed since the last L3 firing (for cooldown); large when never fired

	// Rolling-baseline mode state (L3Mode=="rolling"): a single reference equity
	// that resets to current equity after every cut. No latch/no re-arm.
	rollRef      float64 // rolling reference equity; a cut fires when equity drops L3RollDropPct% below it
	rollInit     bool    // rollRef seeded yet
	rollArmFloor float64 // whipsaw guard: rollRef won't rise until equity exceeds this post-cut floor

	// ATR-normalized trigger state (L3RollATRMult>0): the portfolio-weighted
	// adverse-move-in-ATR at the last fire. Re-fire only when the crash deepens
	// past this level by L3RollReArmPct% (whipsaw guard in ATR units, leverage-free).
	lastFireATR float64

	// Cycle breaker state. cycleBase is the account equity the current cycle
	// started from (the frozen value at the previous firing, or StartCapital for
	// cycle 0); cyclePeak is the high-water mark since then, which is what the
	// drawdown is measured against. cycleArmed tracks whether the cycle has met
	// L3CycleArmGainPct yet.
	cycleBase  float64
	cyclePeak  float64
	cycleArmed bool
	cycleInit  bool
	cycleCount int
	cycleFires int
	// cyclePendingAnchor: a firing happened this tick and the new cycle baseline
	// must be re-anchored by the sim loop from the full book's realised equity.
	cyclePendingAnchor bool
	// placeboRNG is seeded once per run so a given (seed, prob) is reproducible.
	placeboRNG *rand.Rand

	// Two-state arm (L3ArmProfitPct>0). armCycleBase is the equity at the last
	// firing; the breaker stays disarmed until equity reaches
	// armCycleBase*(1+L3ArmProfitPct/100). armed starts true (the first episode
	// needs no prior profit) and is flipped false by every firing.
	armCycleBase float64
	armed        bool
	armInit      bool
	// Diagnostics for the gate/arm study: how many ticks the breaker was
	// suppressed by each mechanism, and how many it was live. Without these a
	// zero-fire config is indistinguishable from a never-armed one.
	ticksTotal      int
	ticksArmed      int
	ticksGateOpen   int
	ticksTriggerHit int // drawdown threshold met (before gate/arm suppression)

	// Optional trace sink: when non-nil, each L3 firing appends a full snapshot.
	trace     *[]L3FireEvent
	traceTick int64 // OpenTime of the tick currently being evaluated (for the trace)
}

// L3FireEvent is a full snapshot of one L3 circuit-breaker firing, for tracing
// the logic on a real position sequence (not just final PnL/DD).
type L3FireEvent struct {
	Tick        int64       // bar OpenTime (ms) the breaker fired on
	Early       bool        // true = rate-based early trigger (DD threshold not yet reached)
	TierIdx     int         // index of the tier that fired
	TierDDPct   float64     // that tier's drawdown threshold
	TierClose   float64     // that tier's base close%
	EquityPeak  float64     // equity high-water at fire
	EquityCur   float64     // current equity at fire
	DDPct       float64     // drawdown from peak (%)
	EquityVel   float64     // equity velocity (% of peak per tick); <0 falling
	Positions   []L3FirePos // per-position snapshot + action
	TotalCutQty float64     // summed closed fraction this firing
}

// L3FirePos is one position's state + action at an L3 firing.
type L3FirePos struct {
	Symbol          string
	Side            string
	PnLPct          float64 // current profit% (favorable sign)
	Velocity        float64 // profit%-change per bar over the velocity window
	CounterTrend    bool    // classified as counter-trend (velocity < -eps)
	CutFrac         float64 // fraction of original position cut this firing
	RemainingBefore float64
	RemainingAfter  float64
}

// applyGuards runs L1 (per-symbol) then L2 (portfolio) guard trims on the
// currently-open positions. portfolioPeak is the running high-water mark of
// total unrealized profit (quote). Returns (#trims, summed closed fraction).
//
// Ratchet semantics (prevents per-tick churn): each guard fires at most once
// per reversal episode. L1 re-arms when its position makes a NEW profit-% peak
// above the peak at which it last fired. L2 re-arms when the portfolio unreal
// high-water makes a new high above the peak at which it last fired. This
// mirrors how a live trailing/give-back rule would act on discrete events
// rather than every monitoring tick.
//
// L1 uses profit-% (peakPnlPct) giveback, which is size-invariant so trimming
// the position does not distort the next measurement. L2 uses quote-unrealized
// giveback against the running portfolio high-water.
func applyGuards(g GuardParams, gps []guardPos, portfolioPeak float64, st *guardState) (int, float64) {
	trims := 0
	var closed float64

	// L1: per-symbol giveback-velocity trim (profit-% based, ratcheted).
	if g.L1Enabled {
		for _, gp := range gps {
			sp := gp.pos
			if sp.done || sp.remaining <= 1e-9 {
				continue
			}
			peakPct := sp.peakPnlPct
			if peakPct < g.L1MinPeakPct {
				continue
			}
			// re-arm: only consider firing if a new profit peak was set since
			// the last L1 fire on this position.
			if sp.l1FiredAtPeak > 0 && peakPct <= sp.l1FiredAtPeak {
				continue
			}
			curPct := pnlPct(sp.e.Side, sp.e.EntryPrice, gp.price)
			giveback := (peakPct - curPct) / peakPct * 100
			if giveback >= g.L1GivebackPct {
				frac := g.L1ClosePct / 100.0
				before := sp.remaining
				sp.addExit(gp.price, frac, "guard_l1")
				closed += before - sp.remaining
				sp.l1FiredAtPeak = peakPct
				trims++
			}
		}
	}

	// L2: portfolio circuit breaker — trim every currently-winning position,
	// once per episode (re-arm on new portfolio high-water).
	if g.L2Enabled && portfolioPeak >= g.L2MinPeakQuote && portfolioPeak > 0 {
		// re-arm: if portfolio set a new high-water above last fire, clear the latch.
		if st.l2FiredAtPeak > 0 && portfolioPeak > st.l2FiredAtPeak {
			st.l2FiredAtPeak = 0
		}
		armed := st.l2FiredAtPeak == 0
		var totalUnreal, longNotional, shortNotional float64
		for _, gp := range gps {
			if gp.pos.done {
				continue
			}
			totalUnreal += gp.pos.unrealizedQuote(gp.price)
			n := gp.pos.notionalRemaining()
			if gp.pos.isLong {
				longNotional += n
			} else {
				shortNotional += n
			}
		}
		threshold := g.L2GivebackPct
		// directional concentration tightening.
		if g.ConcentrationPct > 0 && g.ConcTightenMult > 0 {
			total := longNotional + shortNotional
			if total > 0 {
				netSame := longNotional
				if shortNotional > longNotional {
					netSame = shortNotional
				}
				if netSame/total >= g.ConcentrationPct {
					threshold *= g.ConcTightenMult
				}
			}
		}
		giveback := (portfolioPeak - totalUnreal) / portfolioPeak * 100
		if armed && giveback >= threshold {
			frac := g.L2ClosePct / 100.0
			// Trend-adaptive close ratio: pick based on the notional-weighted
			// avg entry-ADX of currently-open winners. Strong trend -> trim more.
			if g.AdaptiveClose {
				var wADX, wNotional float64
				for _, gp := range gps {
					if gp.pos.done || gp.pos.unrealizedQuote(gp.price) <= 0 {
						continue
					}
					n := gp.pos.notionalRemaining()
					wADX += gp.pos.entryADX * n
					wNotional += n
				}
				avgADX := 0.0
				if wNotional > 0 {
					avgADX = wADX / wNotional
				}
				if avgADX >= g.TrendADXThreshold {
					frac = g.L2ClosePctTrend / 100.0
				} else {
					frac = g.L2ClosePctChop / 100.0
				}
			}
			for _, gp := range gps {
				sp := gp.pos
				if sp.done || sp.remaining <= 1e-9 {
					continue
				}
				// only trim winning positions (de-risk profit, don't realize losers).
				if sp.unrealizedQuote(gp.price) <= 0 {
					continue
				}
				before := sp.remaining
				sp.addExit(gp.price, frac, "guard_l2")
				closed += before - sp.remaining
				trims++
			}
			st.l2FiredAtPeak = portfolioPeak // latch until a new high-water
		}
	}

	return trims, closed
}

// applyEquityBreaker is the L3 account-level circuit breaker. It watches TOTAL
// EQUITY drawdown from its high-water mark and de-risks the whole book when the
// book is bleeding (regime change). Two refinements over a flat tier-cut:
//
//  1. Counter-trend-first: when it fires, each open position is classified by
//     its pnl-VELOCITY (rate+direction of profit% change), NOT its PnL sign.
//     Deteriorating positions (velocity < -L3CounterVelEps) are counter-trend
//     and cut at the tier's full ClosePct; improving positions are trend-aligned
//     and cut only at ClosePct*L3TrendKeepMult (0 = preserved as a 2nd-tier
//     runner). So a profitable-but-reversing position is cut first, while a
//     losing-but-recovering position is kept — matching "砍逆势、留顺势".
//
//  2. Early (rate-based) trigger: if equity is plunging faster than
//     L3EquityVelTrigger (%/bar) once drawdown passes L3EarlyFloorPct, the
//     shallowest not-yet-fired tier fires early — cutting before the full
//     drawdown threshold is reached ("及时熔断，不要等回撤完了再砍").
//
// Ratchet: tiers fire deepest-first, latch, and re-arm only on a new equity
// high-water mark, preventing per-tick churn while equity oscillates.
func applyEquityBreaker(g GuardParams, gps []guardPos, curEquity, equityPeak float64, st *guardState) (int, float64) {
	if g.L3Mode == "rolling" {
		return applyRollingBreaker(g, gps, curEquity, st)
	}
	if !g.L3Enabled || len(g.L3Tiers) == 0 || equityPeak <= 0 {
		st.prevEquity, st.hasPrevEquity = curEquity, true
		return 0, 0
	}
	if len(st.l3TierFiredDD) != len(g.L3Tiers) {
		st.l3TierFiredDD = make([]bool, len(g.L3Tiers))
	}
	// Re-arm on a new equity high-water: clear all tier latches.
	if st.l3FiredAtPeak > 0 && equityPeak > st.l3FiredAtPeak {
		for i := range st.l3TierFiredDD {
			st.l3TierFiredDD[i] = false
		}
		st.l3FiredAtPeak = 0
	}

	// Equity velocity (% of peak per tick); negative = falling.
	equityVelPct := 0.0
	if st.hasPrevEquity && equityPeak > 0 {
		equityVelPct = (curEquity - st.prevEquity) / equityPeak * 100
	}
	st.prevEquity, st.hasPrevEquity = curEquity, true
	st.barsSinceFire++

	// Track consecutive bars where equity is falling faster than the trigger.
	// A single down-wick that bounces resets the streak, so the early trigger
	// only fires on a SUSTAINED plunge (V-bounce protection).
	if g.L3EquityVelTrigger > 0 && equityVelPct <= -g.L3EquityVelTrigger {
		st.negVelStreak++
	} else {
		st.negVelStreak = 0
	}

	ddPct := (equityPeak - curEquity) / equityPeak * 100
	if ddPct <= 0 {
		return 0, 0
	}

	// Deepest breached, not-yet-fired tier.
	chosen := -1
	chosenDD := -1.0
	for i, tier := range g.L3Tiers {
		if tier.DrawdownPct <= 0 || tier.ClosePct <= 0 {
			continue
		}
		if ddPct >= tier.DrawdownPct && !st.l3TierFiredDD[i] && tier.DrawdownPct > chosenDD {
			chosen = i
			chosenDD = tier.DrawdownPct
		}
	}

	// Early rate-based trigger: equity falling fast past the floor for enough
	// CONSECUTIVE bars -> fire the shallowest not-yet-fired tier even if its DD
	// threshold isn't reached. The confirm-bars requirement filters single-bar
	// wicks; the cooldown prevents repeated early cuts inside one fast move.
	early := false
	confirm := g.L3EarlyConfirmBars
	if confirm < 1 {
		confirm = 1
	}
	cooldownOK := g.L3FireCooldownBars <= 0 || st.barsSinceFire >= g.L3FireCooldownBars
	if chosen < 0 && g.L3EquityVelTrigger > 0 && st.negVelStreak >= confirm &&
		ddPct >= g.L3EarlyFloorPct && cooldownOK {
		for i, tier := range g.L3Tiers {
			if tier.DrawdownPct <= 0 || tier.ClosePct <= 0 || st.l3TierFiredDD[i] {
				continue
			}
			if chosen < 0 || tier.DrawdownPct < chosenDD {
				chosen = i
				chosenDD = tier.DrawdownPct
			}
		}
		if chosen >= 0 {
			early = true
		}
	}
	if chosen < 0 {
		return 0, 0
	}

	baseFrac := g.L3Tiers[chosen].ClosePct / 100.0
	window := g.L3VelWindow
	if window <= 0 {
		window = 6
	}
	keepMult := g.L3TrendKeepMult // 0 => fully preserve trend-aligned

	// Optional trace snapshot of this firing.
	var ev *L3FireEvent
	if st.trace != nil {
		ev = &L3FireEvent{
			Tick:       st.traceTick,
			Early:      early,
			TierIdx:    chosen,
			TierDDPct:  g.L3Tiers[chosen].DrawdownPct,
			TierClose:  g.L3Tiers[chosen].ClosePct,
			EquityPeak: equityPeak,
			EquityCur:  curEquity,
			DDPct:      ddPct,
			EquityVel:  equityVelPct,
		}
	}

	trims := 0
	var closed float64
	for _, gp := range gps {
		sp := gp.pos
		if sp.done || sp.remaining <= 1e-9 {
			continue
		}
		// Classify by velocity direction (independent of PnL sign).
		vel := sp.pnlVelocity(window)
		counter := vel < -g.L3CounterVelEps
		frac := baseFrac
		if !counter {
			// trend-aligned / stable -> preserve (or trim lightly).
			frac = baseFrac * keepMult
		}
		before := sp.remaining
		if frac > 0 {
			sp.addExit(gp.price, frac, "guard_l3")
			closed += before - sp.remaining
			trims++
		}
		if ev != nil {
			ev.Positions = append(ev.Positions, L3FirePos{
				Symbol:          sp.e.Symbol,
				Side:            sp.e.Side,
				PnLPct:          pnlPct(sp.e.Side, sp.e.EntryPrice, gp.price),
				Velocity:        vel,
				CounterTrend:    counter,
				CutFrac:         before - sp.remaining,
				RemainingBefore: before,
				RemainingAfter:  sp.remaining,
			})
		}
	}
	if ev != nil {
		ev.TotalCutQty = closed
		*st.trace = append(*st.trace, *ev)
	}
	st.l3TierFiredDD[chosen] = true
	st.l3FiredAtPeak = equityPeak // latch until a new equity high-water
	st.barsSinceFire = 0          // reset cooldown clock
	st.negVelStreak = 0           // require a fresh sustained plunge for the next early fire
	return trims, closed
}

// applyRollingBreaker is the simplified rolling-baseline equity circuit breaker
// (L3Mode=="rolling"). It keeps a single reference equity that resets after each
// cut, eliminating the tier ladder, per-tier latch, and high-water re-arm gate
// (the source of the lower-high protection gap). Each time equity falls
// L3RollDropPct% below the rolling reference, it cuts and re-bases the reference
// to current equity, so the next cut requires another full drop from there.
//
// Two cut policies (see GuardParams.L3RollCounterOnly):
//   - counter-only: trim counter-trend positions (velocity < -eps) at L3RollCutPct,
//     trend-aligned at L3RollCutPct*L3TrendKeepMult (0 => fully preserved).
//   - whole-book:   trim L3RollCutPct% of EVERY remaining position. Because it cuts
//     a fraction of CURRENT remaining each time, repeated drops compound into
//     exponential de-leveraging without any latch.
//
// gateOpen reports whether the book is exposed enough for a whole-book breaker
// to be worth firing: at least L3GateMinPos open positions OR at least
// L3GateMarginPct margin utilisation. Margin is leverage-aware (see the field
// docs). When both legs are zero the gate is disabled and always open, which
// preserves the pre-gate behaviour of every existing grid.
// maxReentryCycles resolves the re-entry cycle bound, defaulting to 3 so a
// misconfigured sweep cannot spin up unlimited round trips on a whipsawing path.
func maxReentryCycles(g GuardParams) int {
	if g.L3ReentryMaxCycles > 0 {
		return g.L3ReentryMaxCycles
	}
	return 3
}

// reenter restarts a flattened position at price, banking the completed cycle's
// PnL and resetting protective levels from the new entry. Fees for the new entry
// fill are charged here; the closing leg was already charged by addExit.
func (sp *simPos) reenter(p ProtectionParams, price, atr float64) {
	// Restore exactly what the breaker cut. Partial trims are restored too (see
	// the scheduling site): otherwise a cut50% policy would shed exposure for free
	// while cut100% paid a round trip, and the sweep would rank policies by
	// avoided cost rather than by risk management.
	restore := sp.pendingRestoreFrac
	if restore <= 0 {
		restore = 1 - sp.remaining
	}
	if restore > 1 {
		restore = 1
	}
	if restore <= 1e-9 {
		sp.pendingReentry, sp.pendingRestoreFrac = false, 0
		return
	}
	held := sp.remaining

	// Bank the finished cycle before clearing its accumulators, otherwise the
	// loss the breaker just realized would vanish and the breaker would look free.
	sp.bankedPnL = sp.realizedPnL() + sp.feeQuote // realizedPnL nets fees; re-add
	sp.feeQuote += p.FeeRatePct / 100.0 * price * sp.e.Quantity * restore

	// Blend the restored slice into any surviving slice at a size-weighted entry,
	// which is what adding to an existing position actually produces.
	if held > 1e-9 {
		sp.e.EntryPrice = (sp.e.EntryPrice*held + price*restore) / (held + restore)
	} else {
		sp.e.EntryPrice = price
	}
	sp.exitNotional, sp.exitFrac = 0, 0
	sp.remaining = held + restore
	if sp.remaining > 1 {
		sp.remaining = 1
	}
	sp.pendingRestoreFrac = 0
	sp.done = false
	sp.peakPnlPct = 0
	sp.peakUnrealQuote = 0
	sp.l1FiredAtPeak = 0
	sp.beStop = 0
	sp.beArmedTier = -1
	sp.pnlHist = sp.pnlHist[:0]
	sp.closeReasons = append(sp.closeReasons, "l3_reentry")
	sp.pendingReentry = false
	sp.reentryCycles++

	// Recompute protective levels from the new entry price, mirroring newSimPos.
	sp.ddFired = make([]bool, len(p.DDRules))
	if price > 0 && atr > 0 {
		sp.atrPct = atr / price * 100
	}
	slDist := p.slDistancePct(price, atr)
	sp.slPrice = priceAtDistance(price, slDist, sp.isLong, false)
	sp.tps = sp.tps[:0]
	for _, leg := range p.TPLegs {
		d := legDistancePct(leg, p.Unit, price, atr)
		sp.tps = append(sp.tps, tpLvl{
			price: priceAtDistance(price, d, sp.isLong, true),
			frac:  leg.CloseRatioPct / 100.0,
		})
	}
}

// applyCycleBreaker is the equity-freeze cycle breaker. It measures drawdown from
// the CURRENT cycle's peak and, when the threshold is breached with the gate open
// and the cycle armed, flattens the whole book and starts a new cycle anchored at
// the equity realised by that flatten.
//
// Returns (positionsClosed, closedFractionTotal).
func applyCycleBreaker(g GuardParams, gps []guardPos, curEquity float64, st *guardState) (int, float64) {
	if !g.L3CycleEnabled || g.L3CycleDDPct <= 0 || curEquity <= 0 {
		return 0, 0
	}
	// Seed cycle 0 at the account's starting equity.
	if !st.cycleInit {
		st.cycleBase, st.cyclePeak = curEquity, curEquity
		st.cycleInit, st.cycleCount = true, 1
		// Armed immediately only when no gain is required.
		st.cycleArmed = g.L3CycleArmGainPct <= 0
	}
	if curEquity > st.cyclePeak {
		st.cyclePeak = curEquity
	}

	st.ticksTotal++
	// Arm once the cycle has earned the required gain above its baseline. Latching
	// (never disarming within a cycle) is deliberate: 「正收益达到一定比例就开始挂上
	// 熔断」 arms the breaker for the cycle, and a subsequent dip is exactly the
	// event it exists to catch — disarming on the dip would switch it off at the
	// worst moment.
	if !st.cycleArmed && g.L3CycleArmGainPct > 0 && st.cycleBase > 0 &&
		curEquity >= st.cycleBase*(1+g.L3CycleArmGainPct/100) {
		st.cycleArmed = true
	}
	if st.cycleArmed {
		st.ticksArmed++
	}
	gate := gateOpen(g, gps, curEquity)
	if gate {
		st.ticksGateOpen++
	}

	if st.cyclePeak <= 0 {
		return 0, 0
	}
	triggered := false
	if g.L3CyclePlaceboProb > 0 {
		// Placebo: same machinery, no drawdown information.
		if st.placeboRNG == nil {
			st.placeboRNG = rand.New(rand.NewSource(g.L3CyclePlaceboSeed))
		}
		triggered = st.placeboRNG.Float64() < g.L3CyclePlaceboProb
	} else {
		ddPct := (st.cyclePeak - curEquity) / st.cyclePeak * 100
		triggered = ddPct >= g.L3CycleDDPct
	}
	if !triggered {
		return 0, 0
	}
	st.ticksTriggerHit++
	if !st.cycleArmed || !gate {
		// Suppressed. Do NOT advance the cycle or move its peak: a disarmed breaker
		// must not consume the drawdown signal, or the reference would ratchet down
		// through a decline and the breaker would find "no drawdown" once armed.
		return 0, 0
	}

	if g.L3CycleMeasureOnly {
		// Count the firing, touch nothing. Advance the arm state as usual so the
		// two-state machine is still exercised.
		st.cycleCount++
		st.cycleFires++
		st.cycleArmed = g.L3CycleArmGainPct <= 0
		return 0, 0
	}
	// Freeze: close every open position at its current mark.
	closedN := 0
	var closedFrac float64
	for _, gp := range gps {
		sp := gp.pos
		if sp.done || sp.remaining <= 1e-9 {
			continue
		}
		before := sp.remaining
		sp.addExit(gp.price, sp.remaining, "l3_cycle_freeze")
		closedFrac += before - sp.remaining
		closedN++
	}

	// Mark the cycle as needing a re-anchor. The new baseline must be the equity
	// realised across the WHOLE book, including positions closed in earlier cycles,
	// and only the sim loop can see that — gps holds just the currently-open
	// subset. Anchoring off gps alone would understate the frozen equity by every
	// prior cycle's realised PnL.
	st.cyclePendingAnchor = true
	st.cycleCount++
	st.cycleFires++
	st.cycleArmed = g.L3CycleArmGainPct <= 0
	return closedN, closedFrac
}

func gateOpen(g GuardParams, gps []guardPos, curEquity float64) bool {
	if g.L3GateMinPos <= 0 && g.L3GateMarginPct <= 0 {
		return true
	}
	openCount := 0
	var notional float64
	for _, gp := range gps {
		if gp.pos.done || gp.pos.remaining <= 1e-9 {
			continue
		}
		openCount++
		notional += gp.pos.notionalRemaining()
	}
	if g.L3GateMinPos > 0 && openCount >= g.L3GateMinPos {
		return true
	}
	if g.L3GateMarginPct > 0 && curEquity > 0 {
		lev := g.L3NominalLeverage
		if lev <= 0 {
			lev = 1
		}
		if notional/lev/curEquity*100 >= g.L3GateMarginPct {
			return true
		}
	}
	return false
}

// armCheck maintains the two-state arm. Returns whether the drawdown breaker is
// currently allowed to fire. The first episode is armed by definition; every
// firing disarms until equity recovers L3ArmProfitPct% above the firing level.
func armCheck(g GuardParams, curEquity float64, st *guardState) bool {
	if g.L3ArmProfitPct <= 0 {
		return true
	}
	if !st.armInit {
		st.armed, st.armInit, st.armCycleBase = true, true, curEquity
	}
	if !st.armed && st.armCycleBase > 0 &&
		curEquity >= st.armCycleBase*(1+g.L3ArmProfitPct/100) {
		st.armed = true
	}
	return st.armed
}

// disarmAfterFire records the post-fire equity and drops the arm, so the next
// firing needs a fresh L3ArmProfitPct recovery first.
func disarmAfterFire(g GuardParams, curEquity float64, st *guardState) {
	if g.L3ArmProfitPct <= 0 {
		return
	}
	st.armed, st.armInit, st.armCycleBase = false, true, curEquity
}

func applyRollingBreaker(g GuardParams, gps []guardPos, curEquity float64, st *guardState) (int, float64) {
	if g.L3RollCutPct <= 0 || curEquity <= 0 {
		return 0, 0
	}
	// ATR-normalized trigger branch: measure the portfolio's adverse move in ATR
	// units (leverage-free, volatility-normalized) instead of raw equity drawdown%.
	if g.L3RollATRMult > 0 {
		return applyRollingBreakerATR(g, gps, curEquity, st)
	}
	if g.L3RollDropPct <= 0 {
		return 0, 0
	}
	st.barsSinceFire++
	// Seed / raise the rolling reference. The reference tracks the running high so
	// a recovery raises the bar; a cut later re-bases it to current equity. The
	// whipsaw guard ①: after a cut, rollRef is pinned and only allowed to rise
	// again once equity recovers L3RollReArmPct above the post-cut floor — so a
	// small dead-cat bounce can't immediately re-arm another cut at the next dip.
	canRise := st.rollArmFloor <= 0 || curEquity >= st.rollArmFloor
	if !st.rollInit || (curEquity > st.rollRef && canRise) {
		st.rollRef = curEquity
		st.rollInit = true
		if canRise {
			st.rollArmFloor = 0 // recovery confirmed; clear the floor
		}
	}
	// Diagnostics: record arm/gate occupancy every tick the breaker is consulted,
	// so a zero-fire result can be attributed (never armed? gate never open?
	// trigger never hit?) instead of being reported as "no signal".
	st.ticksTotal++
	armedNow := armCheck(g, curEquity, st)
	if armedNow {
		st.ticksArmed++
	}
	gateNow := gateOpen(g, gps, curEquity)
	if gateNow {
		st.ticksGateOpen++
	}

	dropPct := (st.rollRef - curEquity) / st.rollRef * 100
	if dropPct < g.L3RollDropPct {
		return 0, 0
	}
	st.ticksTriggerHit++
	// Exposure gate + two-state arm. Both are evaluated AFTER the raw trigger so
	// ticksTriggerHit measures the unsuppressed signal rate: the difference
	// between it and the fire count is exactly what the gate and arm removed.
	//
	// NOTE the asymmetry: a suppressed trigger must NOT re-base rollRef. If it
	// did, a disarmed breaker would silently consume the drawdown signal and the
	// reference would ratchet down through a decline without a single cut.
	if !gateNow || !armedNow {
		return 0, 0
	}
	// Whipsaw cooldown ①: enforce a minimum bar gap between fires when configured.
	if g.L3FireCooldownBars > 0 && st.barsSinceFire < g.L3FireCooldownBars {
		return 0, 0
	}

	window := g.L3VelWindow
	if window <= 0 {
		window = 6
	}
	// Gap/flash-crash scaling ②: a drop that overshoots the threshold cuts more,
	// capped at L3RollScaleCap multiples. ScaleCap<=1 disables scaling (fixed cut).
	scale := 1.0
	if g.L3RollScaleCap > 1 {
		scale = dropPct / g.L3RollDropPct
		if scale < 1 {
			scale = 1
		}
		if scale > g.L3RollScaleCap {
			scale = g.L3RollScaleCap
		}
	}
	cutPct := g.L3RollCutPct / 100.0 * scale
	if cutPct > 1 {
		cutPct = 1
	}
	keepMult := g.L3TrendKeepMult

	var ev *L3FireEvent
	if st.trace != nil {
		ev = &L3FireEvent{
			Tick:       st.traceTick,
			EquityPeak: st.rollRef,
			EquityCur:  curEquity,
			DDPct:      dropPct,
			TierClose:  g.L3RollCutPct * scale,
		}
	}

	trims, closed := rollingCutLoop(g, gps, cutPct, keepMult, window, ev)
	if ev != nil {
		ev.TotalCutQty = closed
		*st.trace = append(*st.trace, *ev)
	}
	// Re-base the rolling reference to current equity and reset the whipsaw state:
	// the next cut needs another full L3RollDropPct drop from here, and rollRef is
	// pinned until equity recovers L3RollReArmPct above this post-cut level.
	st.rollRef = curEquity
	st.barsSinceFire = 0
	if g.L3RollReArmPct > 0 {
		st.rollArmFloor = curEquity * (1 + g.L3RollReArmPct/100)
	}
	disarmAfterFire(g, curEquity, st)
	return trims, closed
}

// rollingCutLoop performs the shared per-position de-lever: cut cutPct of each
// position's CURRENT remaining (compounding => always leaves a runner), trimming
// trend-aligned positions lightly (cutPct*keepMult) when counter-only is set.
// When ev != nil it records each position's action for the trace.
func rollingCutLoop(g GuardParams, gps []guardPos, cutPct, keepMult float64, window int, ev *L3FireEvent) (int, float64) {
	trims := 0
	var closed float64
	for _, gp := range gps {
		sp := gp.pos
		if sp.done || sp.remaining <= 1e-9 {
			continue
		}
		frac := cutPct
		counter := true
		if g.L3RollCounterOnly {
			counter = classifyCounter(sp, gp.price, window, g.L3CounterVelEps)
			if !counter {
				frac = cutPct * keepMult // trend-aligned: preserve / trim lightly
			}
		}
		before := sp.remaining
		cutAmt := frac * sp.remaining
		if cutAmt > 0 {
			sp.addExit(gp.price, cutAmt, "guard_l3roll")
			closed += before - sp.remaining
			trims++
			// Schedule a restore if re-entry is configured. This applies to PARTIAL
			// trims as well as full flattens, and that is deliberate: if only full
			// flattens were restored, a cut50% policy would shed exposure and never
			// pay to rebuild it, while cut100% paid a full round trip every time.
			// The sweep would then rank policies by how much re-entry cost they
			// managed to avoid rather than by how well they managed risk.
			//
			// pendingRestoreFrac accumulates how much of the original position size
			// must be rebuilt, so a de-lever is modelled as temporary, matching the
			// intent of a circuit breaker (reduce risk now, resume after).
			if g.L3ReentryBars > 0 && sp.reentryCycles < maxReentryCycles(g) {
				sp.pendingRestoreFrac += before - sp.remaining
				sp.pendingReentry = true
				sp.reentryAtMs = 0 // resolved by the sim loop against bar times
			}
		}
		if ev != nil {
			ev.Positions = append(ev.Positions, L3FirePos{
				Symbol:          sp.e.Symbol,
				Side:            sp.e.Side,
				PnLPct:          pnlPct(sp.e.Side, sp.e.EntryPrice, gp.price),
				CounterTrend:    counter,
				CutFrac:         before - sp.remaining,
				RemainingBefore: before,
				RemainingAfter:  sp.remaining,
			})
		}
	}
	return trims, closed
}

// portfolioAdverseATR returns the notional-weighted ADVERSE move in ATR units
// across all open positions. For each position the adverse move is
// max(0, -pnlPct) / atrPct (how many ATRs underwater it is, leverage-free), and
// it is weighted by the position's current notional share (remaining × entry ×
// qty). Positions with no ATR data are skipped. A return of 2.0 means the book
// is, on average, two ATRs underwater — the same unit a k×ATR stop is set in.
func portfolioAdverseATR(gps []guardPos) float64 {
	var wSum, awSum float64
	for _, gp := range gps {
		sp := gp.pos
		if sp.done || sp.remaining <= 1e-9 || sp.atrPct <= 0 {
			continue
		}
		notional := sp.remaining * sp.e.EntryPrice * sp.e.Quantity
		if notional <= 0 {
			continue
		}
		adverse := -pnlPct(sp.e.Side, sp.e.EntryPrice, gp.price) // >0 when underwater
		if adverse < 0 {
			adverse = 0
		}
		adverseATR := adverse / sp.atrPct
		wSum += notional
		awSum += adverseATR * notional
	}
	if wSum <= 0 {
		return 0
	}
	return awSum / wSum
}

// applyRollingBreakerATR is the ATR-normalized variant of the rolling breaker.
// The TRIGGER is leverage-free: it fires on the portfolio-weighted adverse move
// in ATR units (portfolioAdverseATR) crossing L3RollATRMult, not on equity
// drawdown% (which is leverage × underlying move and so fires on 0.5% noise at
// 10x). Decoupled from the trigger, the cut SIZE still scales with the ATR
// overshoot (capped by L3RollScaleCap). The whipsaw guard is also in ATR units:
// after a fire it requires the book to deepen another L3RollReArmPct% in ATR
// terms before re-firing, so a dead-cat bounce can't immediately re-trigger
// while a continuing crash always can.
func applyRollingBreakerATR(g GuardParams, gps []guardPos, curEquity float64, st *guardState) (int, float64) {
	st.barsSinceFire++
	advATR := portfolioAdverseATR(gps)
	if advATR < g.L3RollATRMult {
		// Book has recovered below the trigger: clear the whipsaw floor so the
		// next deepening re-arms from scratch.
		if advATR < g.L3RollATRMult*0.5 {
			st.lastFireATR = 0
		}
		return 0, 0
	}
	// Whipsaw guard (ATR units): after a fire, require the book to deepen another
	// L3RollReArmPct% beyond the last-fire level before re-firing.
	if st.lastFireATR > 0 && g.L3RollReArmPct > 0 {
		if advATR < st.lastFireATR*(1+g.L3RollReArmPct/100) {
			return 0, 0
		}
	}
	// Optional time cooldown (kept for parity; 0 = disabled, the default).
	if g.L3FireCooldownBars > 0 && st.barsSinceFire < g.L3FireCooldownBars {
		return 0, 0
	}

	window := g.L3VelWindow
	if window <= 0 {
		window = 6
	}
	// Overshoot scaling: deeper-than-threshold books cut more, capped by ScaleCap.
	scale := 1.0
	if g.L3RollScaleCap > 1 {
		scale = advATR / g.L3RollATRMult
		if scale < 1 {
			scale = 1
		}
		if scale > g.L3RollScaleCap {
			scale = g.L3RollScaleCap
		}
	}
	cutPct := g.L3RollCutPct / 100.0 * scale
	if cutPct > 1 {
		cutPct = 1
	}
	keepMult := g.L3TrendKeepMult

	var ev *L3FireEvent
	if st.trace != nil {
		ev = &L3FireEvent{
			Tick:      st.traceTick,
			EquityCur: curEquity,
			DDPct:     advATR, // in ATR mode this field carries adverse-ATR, not DD%
			TierClose: g.L3RollCutPct * scale,
		}
	}

	trims, closed := rollingCutLoop(g, gps, cutPct, keepMult, window, ev)
	if ev != nil {
		ev.TotalCutQty = closed
		*st.trace = append(*st.trace, *ev)
	}
	st.lastFireATR = advATR
	st.barsSinceFire = 0
	return trims, closed
}

// isRetracing reports whether a position is pulling back from its own peak —
// the per-symbol, leverage-free signal the breadth gate counts. In ATR mode the
// adverse move from peak profit is measured in ATR units; otherwise the
// peak-to-current giveback% or a negative pnl-velocity counts.
func isRetracing(g GuardParams, sp *simPos, price float64, window int) bool {
	cur := pnlPct(sp.e.Side, sp.e.EntryPrice, price)
	// Negative velocity (deteriorating) counts as retracing. Match live: when an
	// ATR is available the velocity is ATR-normalized (profit%/bar ÷ atrPct =
	// ATR-units/bar) so vel_eps is volatility-consistent across symbols. Without
	// an ATR fall back to raw profit%/bar (same as live's atrPct<=0 branch).
	if len(sp.pnlHist) >= 2 {
		vel := sp.pnlVelocity(window)
		if g.BreadthUseATR && sp.atrPct > 0 {
			vel = vel / sp.atrPct
		}
		if vel < -g.BreadthVelEps {
			return true
		}
	}
	if g.BreadthUseATR {
		if sp.atrPct <= 0 {
			return false
		}
		adverseFromPeak := (sp.peakPnlPct - cur) / sp.atrPct
		return adverseFromPeak >= g.BreadthATRMult
	}
	// pnl% mode: peak-to-current giveback in absolute profit% points.
	return (sp.peakPnlPct - cur) >= g.BreadthGivebackPct
}

// applyBreadthBreaker is the per-symbol breadth circuit breaker (redesign that
// replaces the account-equity breakers). It monitors every open position and
// fires only when a MAJORITY retrace together (a correlated reversal, not
// single-symbol noise): retracingCount/total >= BreadthFrac AND total >=
// BreadthMinPos. When it fires it cuts only the LOSING (profitPct<0) retracing
// positions in full (BreadthLoserCutPct); winning positions are left alone (in
// live they ride their break-even stop). This stops the bleeding side while the
// profitable side keeps running under BE protection.
func applyBreadthBreaker(g GuardParams, gps []guardPos, st *guardState) (int, float64) {
	if g.BreadthFrac <= 0 || g.BreadthMinPos <= 0 {
		return 0, 0
	}
	st.barsSinceFire++
	window := g.L3VelWindow
	if window <= 0 {
		window = 6
	}
	// Count open positions and how many are retracing.
	total := 0
	retracing := 0
	for _, gp := range gps {
		sp := gp.pos
		if sp.done || sp.remaining <= 1e-9 {
			continue
		}
		total++
		if isRetracing(g, sp, gp.price, window) {
			retracing++
		}
	}
	if total < g.BreadthMinPos {
		return 0, 0 // no quorum: "majority" is meaningless with too few positions
	}
	if float64(retracing)/float64(total) < g.BreadthFrac {
		return 0, 0 // not a majority reversal: leave each symbol to its own SL/BE
	}
	// Optional cooldown between fires (bars).
	if g.L3FireCooldownBars > 0 && st.barsSinceFire < g.L3FireCooldownBars {
		return 0, 0
	}

	cutPct := g.BreadthLoserCutPct / 100.0
	if cutPct <= 0 {
		cutPct = 1.0 // default: full cut of losers
	}
	if cutPct > 1 {
		cutPct = 1
	}

	var ev *L3FireEvent
	if st.trace != nil {
		ev = &L3FireEvent{
			Tick:      st.traceTick,
			DDPct:     float64(retracing) / float64(total) * 100, // breadth% in this field
			TierClose: g.BreadthLoserCutPct,
		}
	}

	trims := 0
	var closed float64
	for _, gp := range gps {
		sp := gp.pos
		if sp.done || sp.remaining <= 1e-9 {
			continue
		}
		cur := pnlPct(sp.e.Side, sp.e.EntryPrice, gp.price)
		loser := cur < 0
		retr := isRetracing(g, sp, gp.price, window)
		// Surgical: cut only LOSING positions that are retracing. Winners ride BE.
		if !loser || !retr {
			if ev != nil {
				ev.Positions = append(ev.Positions, L3FirePos{
					Symbol: sp.e.Symbol, Side: sp.e.Side, PnLPct: cur,
					CounterTrend: retr, RemainingBefore: sp.remaining, RemainingAfter: sp.remaining,
				})
			}
			continue
		}
		before := sp.remaining
		cutAmt := cutPct * sp.remaining
		if cutAmt > 0 {
			sp.addExit(gp.price, cutAmt, "guard_breadth")
			closed += before - sp.remaining
			trims++
		}
		if ev != nil {
			ev.Positions = append(ev.Positions, L3FirePos{
				Symbol: sp.e.Symbol, Side: sp.e.Side, PnLPct: cur, CounterTrend: retr,
				CutFrac: before - sp.remaining, RemainingBefore: before, RemainingAfter: sp.remaining,
			})
		}
	}
	if ev != nil {
		ev.TotalCutQty = closed
		*st.trace = append(*st.trace, *ev)
	}
	st.barsSinceFire = 0
	return trims, closed
}

// classifyCounter decides whether a position is counter-trend (deteriorating).
// Primary signal is pnl-velocity over the window; but a freshly opened position
// has too little history for a reliable velocity (returns ~0 => looks trend-
// aligned). Fallback ④: when history is insufficient, classify by current PnL
// sign — a losing position with no velocity data is treated as counter-trend so
// a brand-new position that opened straight into a loss is not given a free pass.
func classifyCounter(sp *simPos, price float64, window int, eps float64) bool {
	if len(sp.pnlHist) >= 2 {
		return sp.pnlVelocity(window) < -eps
	}
	// Insufficient velocity history: fall back to PnL sign.
	return pnlPct(sp.e.Side, sp.e.EntryPrice, price) < 0
}
