package backtest

// ClaudeBaselineParams returns the percent-mode protection params matching
// Claude trader's real DB config (TP +3%/+6%, SL -5%, two-tier BE +2%/+4%,
// DD runner_exit at profit≥6% & 40% give-back). Used as the fidelity baseline
// and as the comparison point for the ATR sweep.
func ClaudeBaselineParams() ProtectionParams {
	return ProtectionParams{
		Unit:        UnitPercent,
		StopLossPct: 5,
		TPLegs: []LadderLeg{
			{DistPct: 3, CloseRatioPct: 35},
			{DistPct: 6, CloseRatioPct: 25},
		},
		BELegs: []BELeg{
			{TriggerPct: 2, OffsetPct: 0.4, CloseRatioPct: 50},
			{TriggerPct: 4, OffsetPct: 1.5, CloseRatioPct: 35},
		},
		DDRules: []DDRule{
			{MinProfitPct: 6, MaxDrawdownPct: 40, CloseRatioPct: 45},
		},
	}
}

type ValueUnit string

const (
	UnitPercent    ValueUnit = "percent"      // fixed % of entry price (Claude baseline)
	UnitATRMult    ValueUnit = "atr_multiple" // multiple of ATR (Claude-R)
	UnitStructural ValueUnit = "structural"   // per-entry AI structural levels (support/resistance/Fib)
)

// StructuralTPLeg is one tier of an AI structural take-profit ladder, expressed
// as an absolute price (the structural target) plus the close ratio.
type StructuralTPLeg struct {
	Price         float64
	CloseRatioPct float64
}

// StructuralPlan holds the AI-computed structural protection levels for one
// entry, extracted from the decision record's protection_plan. All prices are
// absolute. Two stop variants are supported:
//   - SLPrice: the AI's stop price with its OWN volatility buffer baked in
//     (Variant A — "AI 自带缓冲").
//   - SLAnchor: the bare structural level (no buffer), used to re-derive the
//     stop as anchor ± k×ATR during the config-buffer sweep (Variant B).
type StructuralPlan struct {
	SLPrice  float64           // AI structural stop (buffer included) — Variant A
	SLAnchor float64           // bare structural level (no buffer)     — Variant B base
	TPLegs   []StructuralTPLeg // AI structural TP ladder (absolute prices)
	// FirstTargetPrice is the AI's risk_reward.first_target (absolute price) — the
	// numerator of the AI's authoritative RR. Used as an alternative fallback RR-cap
	// anchor (RangeSLFallbackAnchor="first_target") so the structural fallback and the
	// AI RR speak the same language, instead of anchoring to the farthest TP leg.
	// 0 when the decision carried no risk_reward.first_target.
	FirstTargetPrice float64
}

// LadderLeg is one tier of the ladder TP/SL.
type LadderLeg struct {
	DistPct       float64 // distance from entry (percent mode), e.g. 3 = +3%
	ATRMult       float64 // distance from entry in ATR multiples (atr mode)
	CloseRatioPct float64 // fraction of original position closed at this leg
}

// BELeg is one break-even tier.
type BELeg struct {
	TriggerPct    float64 // profit % that arms this tier (percent mode)
	TriggerATR    float64 // profit in ATR multiples that arms (atr mode)
	OffsetPct     float64 // stop placed at entry ± offset (percent mode)
	OffsetATR     float64 // stop offset in ATR multiples (atr mode)
	CloseRatioPct float64 // fraction closed when stop hit (cumulative semantics)
}

// DDRule is one drawdown-take-profit tier.
type DDRule struct {
	MinProfitPct   float64 // arm only once profit ≥ this (percent mode)
	MinProfitATR   float64 // arm threshold in ATR multiples (atr mode)
	MaxDrawdownPct float64 // give-back of peak (% of peak) that triggers close
	CloseRatioPct  float64 // fraction of original position closed

	// MaxDrawdownATR, when > 0, switches the give-back trigger to LIVE ATR
	// semantics: close when the peak→current PRICE retrace ≥ MaxDrawdownATR×ATR
	// (matches drawdown_trailing_convert.go callback=(pct*atr)/refPrice), instead
	// of the "% of peak PnL" model. Set by the config converter for atr-unit DD.
	MaxDrawdownATR float64
}

// ProtectionParams is the full protection spec the backtest replays.
// Unit decides whether TP/SL/BE/DD distances are read as percent or ATR multiples.
type ProtectionParams struct {
	Unit ValueUnit

	// Ladder TP/SL
	StopLossPct float64 // SL distance (percent mode), positive number e.g. 5 = -5%
	StopLossATR float64 // SL distance in ATR multiples (atr mode)
	TPLegs      []LadderLeg

	// Break-even tiers
	BELegs []BELeg

	// Drawdown tiers
	DDRules []DDRule

	// Range-anchored structural SL (matches live computeStructuralBoundary +
	// structuralSLPercent). When RangeSLEnabled, the stop is the lookback range
	// low(long)/high(short) with its DISTANCE clamped to [RangeSLFloorATR,
	// RangeSLBackstopATR] ATR multiples — replacing the flat StopLossATR. This is
	// the faithful reconstruction of the live structural stop (the backtest has
	// the pre-entry bars), instead of approximating with the backstop multiple.
	RangeSLEnabled     bool
	RangeSLFloorATR    float64
	RangeSLBackstopATR float64
	RangeSLLookback    int
	// RangeSLPivotStrength: fractal strength for nearest-swing detection — the
	// boundary anchors to the NEAREST pre-entry swing beyond entry (closest overhead
	// swing high for a short / closest swing low below for a long), NOT the window's
	// absolute extreme. Mirrors live computeStructuralBoundary. Default 2.
	RangeSLPivotStrength int
	// RangeSLFallbackATR: the tighter cap used INSTEAD of RangeSLBackstopATR when the
	// nearest structure is still beyond the backstop (no near structure). Mirrors live
	// FallbackATRMul. Must be <= RangeSLBackstopATR. Default 3.0.
	RangeSLFallbackATR float64
	// RangeSLFallbackRRCapRatio: on the fallback path, the stop distance must stay
	// below this ratio × the max TP target (fallbackSL% <= ratio × TP%). Mirrors live
	// FallbackRRCapRatio. RangeSLFloorATR remains a hard minimum. 0 disables. Default 0.8.
	RangeSLFallbackRRCapRatio float64
	// RangeSLMaxTPTargetPct: the max TP target move (% of entry) across ladder tiers,
	// the reference the fallback RR cap tightens against. Resolved by LiveConfigParams.
	RangeSLMaxTPTargetPct float64
	// RangeSLFallbackAnchor selects which target the fallback RR cap tightens against:
	//   "" or "max_tp"      → live behavior: largest TP leg (RangeSLMaxTPTargetPct / TPLegs)
	//   "first_target"      → the AI's risk_reward.first_target (per-entry, from Structural)
	// The first_target anchor aligns the structural fallback with the AI's authoritative
	// RR (same numerator), instead of the farthest, lowest-probability ladder rung.
	RangeSLFallbackAnchor string
	// RRCapPrimary, when true, applies the RR cap (RangeSLFallbackRRCapRatio × anchor
	// TP) as a UNIVERSAL ceiling on EVERY structural stop distance — not just the rare
	// no-near-structure fallback branch. This models the "AI RR is the authoritative RR"
	// architecture: the structural stop is min(structural-clamped, ratio×first_target).
	// RangeSLFloorATR still applies as the hard minimum afterward, so lower the floor to
	// let a tight cap actually bind. Used only to sweep the RR-cap value; NOT live yet.
	RRCapPrimary bool
	// --- min-lot TP collapse modelling (mirrors validateProtectionPlanExecution) ---
	// CollapseK, when >0, forces the TP ladder to collapse to K tiers, simulating the
	// live case where per-leg qty < exchange min lot so only K tiers can be placed. The
	// position closes 100% across those K tiers (no runner — matches the live collapse,
	// which only triggers for positions too small to hold the configured ladder).
	CollapseK int
	// CollapseAnchor selects WHERE the collapsed TP(s) sit:
	//   "nearest"  → live behavior: the K tiers nearest entry (K=1 → +1.1×ATR, tiny profit)
	//   "aitarget" → re-anchor to the AI risk_reward.first_target (from Structural),
	//                pulled toward entry by CollapseTolPct so it fills easier. K>=2 spaces
	//                the extra tiers between entry and the target. Falls back to "nearest"
	//                when no first_target is available for the entry.
	CollapseAnchor string
	// CollapseTolPct is the tolerance (% of entry) the aitarget anchor is pulled toward
	// entry, so the TP is "easy to reach" rather than exactly at the target. Default 0.2
	// (mirrors clampDrawdownRulesToTarget's 0.2% buffer).
	CollapseTolPct float64
	// RangeSLCloseConfirm models the live Phase-2 close-confirm structural stop:
	// the tight structural boundary is NOT a resting intrabar stop — it fires
	// only when a bar CLOSES beyond the boundary, filling at that close. A wide
	// resting backstop (RangeSLBackstopATR ATR) still catches catastrophic
	// intrabar moves. This mirrors runStructuralSLGuard + the backstop resting
	// order. Without it the replay exits on an intrabar wick at the tight level,
	// which is FAVORABLE vs live and understates full_sl losses.
	RangeSLCloseConfirm bool

	// ConfirmStopATR, when > 0, REPLACES the swing-derived close-confirm boundary with
	// a FIXED adverse-excursion stop at ConfirmStopATR × ATR below(long)/above(short)
	// entry, enforced on bar CLOSE (fill at close) and active even while underwater —
	// unlike the live trail that only ratchets after profit. Models the proposed
	// "close-confirm adverse-excursion stop": cut a trade whose thesis has failed
	// (closed beyond N×ATR) without the intrabar-wick vulnerability of a resting stop.
	// The wide RangeSLBackstopATR resting stop still covers intrabar catastrophes.
	// Requires RangeSLCloseConfirm to take effect.
	ConfirmStopATR float64

	// Structural mode (UnitStructural): per-entry SL/TP come from Entry.Structural.
	// StructBufferATR, when > 0, re-derives the stop from the bare structural
	// anchor as anchor ± StructBufferATR×ATR (Variant B). When 0, the AI's own
	// buffered SLPrice is used as-is (Variant A).
	StructBufferATR float64
	// StructMinSLPct / StructMaxSLPct clamp the structural stop distance (percent
	// of entry) to guard against degenerate AI levels (0 = no clamp).
	StructMinSLPct float64
	StructMaxSLPct float64
	// StructUseATRSL, when true in structural mode, overrides the structural SL
	// with an ATR-wide stop (StopLossATR×ATR) while KEEPING the structural TP
	// ladder. This is the "structural TP + wide ATR SL" hybrid: capture profit at
	// AI structural targets, but stop wide enough to avoid wick-outs.
	StructUseATRSL bool

	// --- Trailing structural stop (ratchet) — models the proposed live feature ---
	// TrailStructEnabled turns on the ratcheting structural stop: once per CLOSED bar,
	// recompute the nearest post-entry swing beyond the current price and move the
	// close-confirm boundary TIGHTER toward locking profit (never looser). Requires
	// RangeSLCloseConfirm (the tight boundary is only ever a close-confirm trigger; the
	// wide backstop still guards intrabar catastrophes). No-op when RangeSLEnabled is off.
	TrailStructEnabled bool
	// TrailStructTolATR is the volatility tolerance buffer (in ATR multiples) added
	// BEYOND the recomputed swing before it becomes the new boundary — so the trailing
	// stop sits a cushion past structure, not exactly on it, avoiding whipsaw on a
	// marginal re-test. Also the minimum tightening step: the boundary only ratchets
	// when the new candidate is at least this far tighter than the current one (anti-jitter).
	TrailStructTolATR float64
	// TrailStructMode selects which timeframe's structure the trail follows:
	//   "current" / ""  → this-period swings only (tightest, locks most profit)
	//   "higher"        → higher-period swings only (widest, most whipsaw-resistant)
	//   "both"          → the LOOSER of the two (higher-tf floor): trails current-tf
	//                     structure but never tighter than the higher-tf swing — a
	//                     middle ground preserving anti-volatility while still ratcheting.
	TrailStructMode string
	// TrailStructHigherMult is the higher-timeframe aggregation factor (base bars per
	// higher bar), e.g. 4 → 4h structure when the replay runs on 1h. Used by "higher"
	// and "both" modes. Default 4 when unset and a higher mode is selected.
	TrailStructHigherMult int
	// TrailStructMinProfitATR gates activation: the trail only starts ratcheting once
	// the position's favorable excursion (peak) exceeds this many ATR from entry, so a
	// just-opened position isn't immediately trailed into a tight noise stop. Default 1.0.
	TrailStructMinProfitATR float64
	// TrailStructAfterBE, when true, keeps the trail RATCHETING through the break-even
	// phase and lets the ratcheted boundary act as the RUNNER's stop (tighter than the
	// wide entry-frozen structural level it would otherwise revert to). This is where a
	// trend-follow trail earns its keep: the runner rides the tightening structure
	// instead of a fixed BE offset. When false, the trail only operates before BE arms.
	TrailStructAfterBE bool
	// TrailStructOnlyAfterBE is the SPLIT mode: BEFORE break-even the position keeps its
	// tight current-period structural stop (the entry-frozen boundary, unchanged); once
	// break-even ARMS, the runner is handed to a (typically looser, higher-period) trail
	// that re-seeds from current structure and ratchets up. The break-even stop floors
	// the runner at entry, so the higher trail only binds once structure climbs above
	// breakeven. Pair with TrailStructMode="higher". Implies through-BE trailing.
	TrailStructOnlyAfterBE bool
	// TrailStructMaxRatchets caps how many times the boundary may tighten over the life
	// of the position; once reached the boundary locks. 0 = unlimited (back-compatible).
	// Mirrors live StructuralSLConfig.TrailMaxRatchets.
	TrailStructMaxRatchets int
	// TrailStructOnProfit / TrailStructOnLoss gate ratcheting by the position's current
	// state relative to entry: OnProfit allows tightening while price is favorable, OnLoss
	// while adverse. Both true = ratchet in every state (default). Mirrors live
	// TrailOnProfit / TrailOnLoss. Defaults applied in LiveConfigParams (both true when unset).
	TrailStructOnProfit bool
	TrailStructOnLoss   bool

	// CloseProxy models the non-price live close mechanisms the raw replay
	// ignores (time-stop, max-hold, and a heuristic AI/discretionary exit). Off
	// by default so existing sweeps are unchanged; enabled to raise fidelity.
	CloseProxy CloseProxyParams

	// RangeSLPreferProven mirrors live StructuralSLConfig.PreferProvenLevels: when
	// true the structural boundary prefers an order-block edge on the protective side
	// of entry over the raw fractal swing pivot, but only when it does not widen the
	// stop past the nearest pivot. Off = fractal-only (baseline), for A/B comparison.
	RangeSLPreferProven bool
}

// CloseProxyParams approximates live closes that are NOT fixed price levels.
// These mirror the code-enforced RiskControl stops plus a heuristic stand-in
// for AI/manual discretionary exits, so a replay can reproduce the mechanisms
// the per-mechanism fidelity table flagged as "unmodeled".
type CloseProxyParams struct {
	Enabled bool

	// Time-stop: force-close at bar close once held ≥ TimeStopHours AND the
	// close PnL% is worse than TimeStopLossPct (negative). 0 = disabled.
	TimeStopHours   float64
	TimeStopLossPct float64 // negative, e.g. -1.5

	// Max-hold: force-close at bar close once held ≥ MaxHoldHours UNLESS the
	// close PnL% ≥ MaxHoldProfitExemptPct (profitable-runner exemption). 0 = off.
	MaxHoldHours           float64
	MaxHoldProfitExemptPct float64 // positive, e.g. 2.0

	// AI/discretionary proxy: a deterministic stand-in for ai_close/manual_close.
	// Once profit has peaked ≥ AIPeakArmPct, close the remainder at bar close if
	// the give-back from peak ≥ AIGiveBackPct. Approximates "AI takes profit off
	// the table after a runup stalls". 0/0 = disabled.
	AIPeakArmPct   float64
	AIGiveBackPct  float64
	// TimeframeHours is the bar duration in hours, used to convert held-bars to
	// wall-clock hours for the time/hold thresholds. Set by the runner from tf.
	TimeframeHours float64
}

// Entry is one historical trade entry to replay protection over.
type Entry struct {
	Symbol     string
	Side       string // "LONG" or "SHORT"
	EntryPrice float64
	EntryTime  int64   // ms
	ExitTime   int64   // ms; 0 = open-ended (replay until data end)
	Quantity   float64 // original position size (contracts/coins)
	// RealizedPnL is Claude's actual realized P&L for this trade, used for
	// engine-fidelity validation (percent baseline should approximate it).
	RealizedPnL float64
	// CloseReason is the live close mechanism recorded for this trade
	// (full_sl / ai_close / managed_drawdown / max_hold / ...). Used to bucket
	// fidelity error by mechanism so we can see which live closes the replay
	// fails to model (the AI/time/breadth closes it currently ignores).
	CloseReason string
	// Structural carries the AI's per-entry structural SL/TP, populated only for
	// entries loaded with structural plans (UnitStructural mode). Nil otherwise.
	Structural *StructuralPlan

	// StructBoundaryOverride, when > 0, forces the close-confirm structural boundary
	// to this absolute price INSTEAD of computing it from the replay bars' pre-entry
	// swing. Used by the structural-timeframe isolation sweep (structTFSweep) to feed
	// a 15m/30m/1h-derived boundary into a replay whose ATR/TP/BE/DD/backstop are all
	// held on the SAME (1h) ATR — so the ONLY variable is the structural stop's
	// source timeframe. Zero (the default) preserves normal per-bar computation.
	StructBoundaryOverride float64

	// ATROverride, when > 0, forces ReplayEntry to use this ATR (price units) INSTEAD
	// of computing it from the replay bars' pre-entry window. This lets the structural-
	// TF isolation run each variant on its OWN bar granularity (15m/30m/1h — so the
	// close-confirm fires at that timeframe's natural resolution) while ALL ATR-derived
	// level distances (TP/BE/DD/backstop and the boundary clamp) stay pinned to the 1h
	// ATR. That removes the ATR-shrink confound of a whole-engine small-TF replay.
	ATROverride float64
}

// TradeResult is the outcome of replaying one entry under a ProtectionParams.
type TradeResult struct {
	Symbol       string
	Side         string
	EntryPrice   float64
	ExitPrice    float64 // volume-weighted average exit
	ClosedQty    float64
	RealizedPnL  float64 // sum over partial closes, in quote currency
	ReturnPct    float64 // realized PnL as % of notional at entry
	BarsHeld     int
	CloseReasons []string // ordered list of what fired (sl/tp1/be1/dd/...)
	FullyClosed  bool
	// TrailRatchets counts how many times the trailing structural stop moved
	// tighter over the trade's life (0 when trailing disabled or never armed).
	TrailRatchets int
	// TrailExit is true when the final close-confirm exit fired against a
	// RATCHETED boundary (i.e. the trail — not the entry-frozen level — closed it).
	TrailExit bool
}

// PortfolioResult aggregates many TradeResults.
type PortfolioResult struct {
	Trades       int
	Wins         int
	Losses       int
	TotalPnL     float64
	AvgReturnPct float64
	WinRatePct   float64
	ProfitFactor float64 // gross profit / gross loss
	MaxDrawdown  float64 // equity-curve max drawdown (quote currency)
	Results      []TradeResult
}

// TimeframeHours returns the bar duration in hours for a timeframe token,
// used to convert replay held-bars into wall-clock hours for the close proxy.
func TimeframeHours(tf string) float64 {
	switch tf {
	case "1m":
		return 1.0 / 60
	case "3m":
		return 3.0 / 60
	case "5m":
		return 5.0 / 60
	case "15m":
		return 15.0 / 60
	case "30m":
		return 30.0 / 60
	case "1h":
		return 1
	case "2h":
		return 2
	case "4h":
		return 4
	case "6h":
		return 6
	case "12h":
		return 12
	case "1d":
		return 24
	default:
		return 1
	}
}

// ClaudeCloseProxy returns a DETERMINISTIC close proxy matching claude's live
// code-enforced RiskControl (time_stop 24h/−1.5%, max_hold 18h/+2% exempt).
// The AI give-back stand-in is intentionally OFF: measured on real data it made
// fidelity worse, because claude's discretionary/AI closes are net-negative
// expectancy while a give-back rule is net-positive — the two are directionally
// opposed, so a naive rule cannot represent live AI closes. Use
// ClaudeCloseProxyWithAI for experiments. tfHours sizes held-bars→hours.
func ClaudeCloseProxy(tfHours float64) CloseProxyParams {
	return CloseProxyParams{
		Enabled:                true,
		TimeStopHours:          24,
		TimeStopLossPct:        -1.5,
		MaxHoldHours:           18,
		MaxHoldProfitExemptPct: 2.0,
		TimeframeHours:         tfHours,
	}
}

// ClaudeCloseProxyWithAI adds an experimental AI-discretionary give-back
// stand-in (arm at peakArm%, close on giveBack% retrace) on top of the
// deterministic rules. Kept separate because on claude's real data it degraded
// fidelity; useful only for counterfactual "what if AI took profit mechanically"
// exploration, not for fidelity.
func ClaudeCloseProxyWithAI(tfHours, peakArm, giveBack float64) CloseProxyParams {
	c := ClaudeCloseProxy(tfHours)
	c.AIPeakArmPct = peakArm
	c.AIGiveBackPct = giveBack
	return c
}
