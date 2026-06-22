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
	UnitPercent  ValueUnit = "percent"     // fixed % of entry price (Claude baseline)
	UnitATRMult  ValueUnit = "atr_multiple" // multiple of ATR (Claude-R)
)

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
}

// Entry is one historical trade entry to replay protection over.
type Entry struct {
	Symbol     string
	Side       string  // "LONG" or "SHORT"
	EntryPrice float64
	EntryTime  int64   // ms
	ExitTime   int64   // ms; 0 = open-ended (replay until data end)
	Quantity   float64 // original position size (contracts/coins)
	// RealizedPnL is Claude's actual realized P&L for this trade, used for
	// engine-fidelity validation (percent baseline should approximate it).
	RealizedPnL float64
}

// TradeResult is the outcome of replaying one entry under a ProtectionParams.
type TradeResult struct {
	Symbol         string
	Side           string
	EntryPrice     float64
	ExitPrice      float64 // volume-weighted average exit
	ClosedQty      float64
	RealizedPnL    float64 // sum over partial closes, in quote currency
	ReturnPct      float64 // realized PnL as % of notional at entry
	BarsHeld       int
	CloseReasons   []string // ordered list of what fired (sl/tp1/be1/dd/...)
	FullyClosed    bool
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
