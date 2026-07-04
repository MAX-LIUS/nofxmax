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

	// CloseProxy models the non-price live close mechanisms the raw replay
	// ignores (time-stop, max-hold, and a heuristic AI/discretionary exit). Off
	// by default so existing sweeps are unchanged; enabled to raise fidelity.
	CloseProxy CloseProxyParams
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
