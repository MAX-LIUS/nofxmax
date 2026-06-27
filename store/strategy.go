package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// StrategyStore strategy storage
type StrategyStore struct {
	db *gorm.DB
}

// Strategy strategy configuration
type Strategy struct {
	ID            string    `gorm:"primaryKey" json:"id"`
	UserID        string    `gorm:"column:user_id;not null;default:'';index" json:"user_id"`
	Name          string    `gorm:"not null" json:"name"`
	Description   string    `gorm:"default:''" json:"description"`
	IsActive      bool      `gorm:"column:is_active;default:false;index" json:"is_active"`
	IsDefault     bool      `gorm:"column:is_default;default:false" json:"is_default"`
	IsPublic      bool      `gorm:"column:is_public;default:false;index" json:"is_public"`    // whether visible in strategy market
	ConfigVisible bool      `gorm:"column:config_visible;default:true" json:"config_visible"` // whether config details are visible
	Config        string    `gorm:"not null;default:'{}'" json:"config"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (Strategy) TableName() string { return "strategies" }

// Strategy type identifiers.
const (
	StrategyTypeAI       = "ai_trading"
	StrategyTypeGrid     = "grid_trading"
	StrategyTypeBreakout = "breakout_trading"
)

// StrategyConfig strategy configuration details (JSON structure)
type StrategyConfig struct {
	// Strategy type: "ai_trading" (default), "grid_trading", or "breakout_trading"
	StrategyType string `json:"strategy_type,omitempty"`

	// language setting: "zh" for Chinese, "en" for English
	// This determines the language used for data formatting and prompt generation
	Language string `json:"language,omitempty"`
	// coin source configuration
	CoinSource CoinSourceConfig `json:"coin_source"`
	// quantitative data configuration
	Indicators IndicatorConfig `json:"indicators"`
	// custom prompt (appended at the end)
	CustomPrompt string `json:"custom_prompt,omitempty"`
	// risk control configuration
	RiskControl RiskControlConfig `json:"risk_control"`
	// unified protection / profit-control configuration
	Protection ProtectionConfig `json:"protection,omitempty"`
	// OPT-IN ATR-driven protection distances (off by default = no-op)
	ATRProtection ATRProtectionConfig `json:"atr_protection,omitempty"`
	// structural entry contract configuration
	EntryStructure EntryStructureConfig `json:"entry_structure,omitempty"`
	// editable sections of System Prompt
	PromptSections PromptSectionsConfig `json:"prompt_sections,omitempty"`

	// Strategy-control policy for system-governed entry/protection decisions.
	// Omitted or unknown modes default to strict to preserve current runtime blocks.
	StrategyControlPolicy StrategyControlPolicyConfig `json:"strategy_control_policy,omitempty"`

	// Evolution engine configuration
	Evolution EvolutionConfig `json:"evolution,omitempty"`

	// Breakout entry engine (CODE ENFORCED, data-validated 2026-06-13). When enabled,
	// the decision cycle also opens on a pure breakout rule (close breaks prior-N-bar
	// high/low), independent of the AI. Inherits all existing risk controls (cooldown,
	// max positions, position-value cap, configured ladder/runner/time-stop protection).
	BreakoutEntry BreakoutEntryConfig `json:"breakout_entry,omitempty"`

	// Grid trading configuration (only used when StrategyType == "grid_trading")
	GridConfig *GridStrategyConfig `json:"grid_config,omitempty"`
}

// BreakoutEntryConfig controls the code-level breakout entry engine.
type BreakoutEntryConfig struct {
	Enabled   bool   `json:"enabled,omitempty"`
	Timeframe string `json:"timeframe,omitempty"` // which timeframe series to use, e.g. "1h","15m". Default primary.
	Lookback  int    `json:"lookback,omitempty"`  // prior-bar window for high/low break. Default 20.
	Leverage  int    `json:"leverage,omitempty"`  // leverage for breakout entries. Default altcoin leverage.
	// Position size as a fraction of equity (e.g. 0.5 = 50% of equity notional).
	// 0/unset → fall back to a conservative default.
	SizeEquityFrac float64 `json:"size_equity_frac,omitempty"`
}

type EntryStructureConfig struct {
	Enabled                          bool            `json:"enabled"`
	RequirePrimaryTimeframe          bool            `json:"require_primary_timeframe"`
	RequireAdjacentTimeframes        bool            `json:"require_adjacent_timeframes"`
	RequireSupportResistance         bool            `json:"require_support_resistance"`
	RequireStructuralAnchors         bool            `json:"require_structural_anchors"`
	RequireFibonacci                 bool            `json:"require_fibonacci"`
	MaxSupportLevels                 int             `json:"max_support_levels,omitempty"`
	MaxResistanceLevels              int             `json:"max_resistance_levels,omitempty"`
	MaxAnchorCount                   int             `json:"max_anchor_count,omitempty"`
	AuditPrimaryTimeframe            bool            `json:"audit_primary_timeframe,omitempty"`
	AuditAdjacentTimeframes          bool            `json:"audit_adjacent_timeframes,omitempty"`
	AuditSupportResistance           bool            `json:"audit_support_resistance,omitempty"`
	AuditStructuralAnchors           bool            `json:"audit_structural_anchors,omitempty"`
	AuditFibonacci                   bool            `json:"audit_fibonacci,omitempty"`
	RequireInvalidationTargetLinkage bool            `json:"require_invalidation_target_linkage,omitempty"`
	EntryGate                        EntryGateConfig `json:"entry_gate,omitempty"`
}

// EntryGateConfig controls executable entry-quality gates that validate AI open proposals.
// It is nested under EntryStructure so it composes with existing entry/protection gates.
type EntryGateConfig struct {
	Enabled                     bool    `json:"enabled,omitempty"`
	MinATR14Pct                 float64 `json:"min_atr14_pct,omitempty"`
	MinRiskDistancePct          float64 `json:"min_risk_distance_pct,omitempty"`
	MinSLDistanceATRMul         float64 `json:"min_sl_distance_atr_mul,omitempty"`
	MinRewardATRMul             float64 `json:"min_reward_atr_mul,omitempty"`
	EntryProximityATRMul        float64 `json:"entry_proximity_atr_mul,omitempty"`
	EntryProximityMinPct        float64 `json:"entry_proximity_min_pct,omitempty"`
	EntryProximityMaxPct        float64 `json:"entry_proximity_max_pct,omitempty"`
	InvalidationStructureATRMul float64 `json:"invalidation_structure_atr_mul,omitempty"`
	InvalidationStructureMinPct float64 `json:"invalidation_structure_min_pct,omitempty"`
	MaxBlockingLevels           int     `json:"max_blocking_levels,omitempty"`
	MaxTargetTimeframeRankGap   int     `json:"max_target_timeframe_rank_gap,omitempty"`

	// Group toggles (pointer to distinguish false from unset)
	StopQualityEnabled    *bool `json:"stop_quality_enabled,omitempty"`
	VolatilityGateEnabled *bool `json:"volatility_gate_enabled,omitempty"`
	EntryPrecisionEnabled *bool `json:"entry_precision_enabled,omitempty"`
	PathClarityEnabled    *bool `json:"path_clarity_enabled,omitempty"`

	// Squeeze/crowded regime extra requirements (G2b)
	SqueezeMinConfidence int     `json:"squeeze_min_confidence,omitempty"`
	SqueezeMinRR         float64 `json:"squeeze_min_rr,omitempty"`

	// Fallback minimum RR when strategy MinRiskRewardRatio is not set (G3b)
	FallbackMinRR float64 `json:"fallback_min_rr,omitempty"`

	// Short position minimum confidence when regime is not trend_down (G3e)
	ShortNonDowntrendMinConfidence int `json:"short_non_downtrend_min_confidence,omitempty"`

	// VolatilityBufferATRMul adds extra ATR-based buffer to SL/TP distances beyond
	// the structural placement. Range [0, 1.5]: 0 = tightest (no extra buffer),
	// 1.5 = widest (1.5× ATR extra padding). Default 0.3.
	// When SL distance < (MinSLDistanceATRMul + VolatilityBufferATRMul) × ATR and
	// RR still holds after widening, backend auto-widens SL to meet the threshold.
	VolatilityBufferATRMul float64 `json:"volatility_buffer_atr_mul,omitempty"`
}

func (c EntryGateConfig) WithDefaults() EntryGateConfig {
	// Default enabled so existing EntryStructure strict mode keeps enforcing executable entry gates.
	c.Enabled = true
	if c.MinATR14Pct <= 0 {
		c.MinATR14Pct = 1.2
	}
	if c.MinRiskDistancePct <= 0 {
		c.MinRiskDistancePct = 0.4
	}
	if c.MinSLDistanceATRMul <= 0 {
		c.MinSLDistanceATRMul = 1.2
	}
	if c.MinRewardATRMul <= 0 {
		c.MinRewardATRMul = 1.8
	}
	if c.EntryProximityATRMul <= 0 {
		c.EntryProximityATRMul = 0.6
	}
	if c.EntryProximityMinPct <= 0 {
		c.EntryProximityMinPct = 0.2
	}
	if c.EntryProximityMaxPct <= 0 {
		c.EntryProximityMaxPct = 1.5
	}
	if c.InvalidationStructureATRMul <= 0 {
		c.InvalidationStructureATRMul = 0.5
	}
	if c.InvalidationStructureMinPct <= 0 {
		c.InvalidationStructureMinPct = 0.3
	}
	if c.MaxBlockingLevels <= 0 {
		c.MaxBlockingLevels = 4
	}
	if c.MaxTargetTimeframeRankGap <= 0 {
		c.MaxTargetTimeframeRankGap = 3
	}
	if c.SqueezeMinConfidence <= 0 {
		c.SqueezeMinConfidence = 80
	}
	if c.SqueezeMinRR <= 0 {
		c.SqueezeMinRR = 2.5
	}
	if c.FallbackMinRR <= 0 {
		c.FallbackMinRR = 1.5
	}
	if c.ShortNonDowntrendMinConfidence <= 0 {
		c.ShortNonDowntrendMinConfidence = 85
	}
	if c.VolatilityBufferATRMul == 0 {
		c.VolatilityBufferATRMul = 0.3
	} else if c.VolatilityBufferATRMul < 0 {
		c.VolatilityBufferATRMul = 0
	}
	if c.VolatilityBufferATRMul > 1.5 {
		c.VolatilityBufferATRMul = 1.5
	}
	return c
}

type StrategyControlPolicyMode string

const (
	StrategyControlPolicyModeStrict        StrategyControlPolicyMode = "strict"
	StrategyControlPolicyModeAuditOnly     StrategyControlPolicyMode = "audit_only"
	StrategyControlPolicyModeRecommendOnly StrategyControlPolicyMode = "recommend_only"
)

// StrategyControlPolicyConfig is the first narrow config surface for
// system-governed strategy-control behavior. Mode defaults to strict so legacy
// configs keep the current reject/block behavior.
type StrategyControlPolicyConfig struct {
	Mode StrategyControlPolicyMode `json:"mode,omitempty"`
}

// EffectiveMode returns the safe default for omitted or unknown policy modes.
func (c StrategyControlPolicyConfig) EffectiveMode() StrategyControlPolicyMode {
	switch c.Mode {
	case StrategyControlPolicyModeAuditOnly, StrategyControlPolicyModeRecommendOnly:
		return c.Mode
	default:
		return StrategyControlPolicyModeStrict
	}
}

// ProtectionConfig unified trade protection / profit-control configuration.
// Phase 1 focuses on manual full TP/SL and execution-side closure verification.
type ProtectionConfig struct {
	FullTPSL           FullTPSLConfig           `json:"full_tp_sl,omitempty"`
	LadderTPSL         LadderTPSLConfig         `json:"ladder_tp_sl,omitempty"`
	DrawdownTakeProfit DrawdownTakeProfitConfig `json:"drawdown_take_profit,omitempty"`
	BreakEvenStop      BreakEvenStopConfig      `json:"break_even_stop,omitempty"`
	RegimeFilter       RegimeFilterConfig       `json:"regime_filter,omitempty"`
	GivebackGuard      GivebackGuardConfig      `json:"giveback_guard,omitempty"`
	TrendReversal      TrendReversalConfig      `json:"trend_reversal,omitempty"`
}

// TrendReversalConfig configures the trend-reversal position-flip feature. When
// the AI signals a HIGH-CONVICTION opposite-direction entry on a symbol that
// already has an aged open position, the system closes the original and opens
// the reverse (instead of rejecting the same-symbol entry outright).
//
// The rule set was distilled from a gbsim backtest over all three live traders'
// real entries (validated 2026-06): the winning configuration is intentionally
// minimal — opposite-conviction signal + a minimum hold age + reverse-open. The
// originally-considered breadth gate and ATR/EMA exhaustion gates were DROPPED
// because on real (non-mechanical) entries they either blocked every flip
// (breadth: real positions don't cluster same-side) or filtered out profitable
// flips (exhaustion gates were net-negative). Flip win rate was >=55% across all
// three traders; claude (212 entries) turned -7.4 USDT into +72.9 USDT.
//
// The architecture flips each position AT MOST ONCE (the reverse is then held to
// its own exit), which is inherently anti-whipsaw — no cooldown needed.
//
// FLEET DEFAULT: the feature is ON for every trader (existing and newly created)
// and starts in DryRun (records intended flips without placing orders) so live
// AI reversal-signal quality can be observed before real execution. The backtest
// proxied AI conviction with a mechanical EMA cross and cannot vouch for the live
// AI signal itself, hence the dry-run-first rollout. A strategy may override per
// trader: LiveExecution=true turns on real flips for that trader; Disabled=true
// turns the feature off entirely; MinConfidence / MinPositionAgeHours override
// the thresholds. The default thresholds are conviction 75, hold age 6h.
type TrendReversalConfig struct {
	// Disabled hard-disables the fleet-default feature for THIS trader.
	Disabled bool `json:"disabled,omitempty"`
	// LiveExecution opts THIS trader into real flip execution (turns off dry-run).
	// Retained for forward-compat; the fleet default is already LIVE.
	LiveExecution bool `json:"live_execution,omitempty"`
	// ForceDryRun pins THIS trader back to dry-run (observe only) even though the
	// fleet default is live — used to quarantine a single trader without disabling.
	ForceDryRun bool `json:"force_dry_run,omitempty"`

	// MinConfidence is the AI-decision confidence floor (0-100 scale) the opposite
	// signal must clear to justify flipping an existing position. Default 75.
	MinConfidence int `json:"min_confidence,omitempty"`

	// MinPositionAgeHours is the minimum hold age before a position may be flipped
	// (prevents noise flips inside the first trend leg). Backtest optimum: 6h.
	MinPositionAgeHours float64 `json:"min_position_age_hours,omitempty"`
}

// GivebackGuardConfig configures the portfolio giveback guard. The sole portfolio
// breaker is the breadth circuit breaker: per-symbol monitoring + a
// correlation-reversal gate (cut losers, let winners ride break-even). It was
// validated by the gbsim backtest and replaced the old L1/L2/L3 account-equity
// breakers, which measured leverage-contaminated equity drawdown (at 10x a 5%
// equity drop is only a 0.5% price move, inside noise) and knocked the whole
// book out on routine wiggles.
//
// Zero value = disabled = complete no-op (existing traders unaffected). When
// DryRun is true the guard only logs the intended trim ("would close X%")
// without placing any order — used to observe trigger timing before going live.
type GivebackGuardConfig struct {
	Enabled bool `json:"enabled,omitempty"`
	DryRun  bool `json:"dry_run,omitempty"`

	// --- Breadth breaker (per-symbol monitoring + correlation-reversal gate) ---
	// Instead of one global equity-drawdown trigger, it monitors EACH position and
	// acts only when a MAJORITY of held symbols retrace together (a correlated
	// reversal, not single-symbol noise).
	//
	// Trigger: fire when retracingCount/total >= BreadthFrac AND total >=
	// BreadthMinPos (a "majority" needs a quorum). A position counts as retracing
	// when its adverse move from peak exceeds BreadthATRMult ATRs (BreadthUseATR,
	// volatility-normalized, the cross-validated default) or, in pnl% mode, when
	// its peak-to-current giveback exceeds BreadthGivebackPct, or its pnl-velocity
	// is negative.
	//
	// Action (surgical): cut LOSING positions (profit% < 0) that are retracing by
	// BreadthLoserCutPct (default 100 = full). WINNING positions are left alone —
	// they ride their break-even stop. Cross-validated production preset:
	// min4 / f70% / ATR0.9 / cut100 / no cooldown.
	BreadthEnabled     bool    `json:"breadth_enabled,omitempty"`
	BreadthMinPos      int     `json:"breadth_min_pos,omitempty"`      // quorum: min open positions before the gate can fire
	BreadthFrac        float64 `json:"breadth_frac,omitempty"`         // fraction (0..1) of positions retracing that fires the gate
	BreadthLoserCutPct float64 `json:"breadth_loser_cut_pct,omitempty"` // % of each losing+retracing position to cut (default 100)
	BreadthUseATR      bool    `json:"breadth_use_atr,omitempty"`      // true => retrace measured in ATR-from-peak units
	BreadthATRMult     float64 `json:"breadth_atr_mult,omitempty"`     // adverse-from-peak in ATR units that counts as retracing
	BreadthGivebackPct float64 `json:"breadth_giveback_pct,omitempty"` // peak-to-current giveback% that counts as retracing (pnl% mode)
	BreadthVelEps      float64 `json:"breadth_vel_eps,omitempty"`      // velocity threshold in ATR-units/bar (giveback% per ATR per bar); below -eps counts as retracing. ATR-normalized so the same value behaves consistently across low- and high-volatility symbols.
	BreadthVelWindow   int     `json:"breadth_vel_window,omitempty"`   // bars of look-back for pnl-velocity (0 => default 6)
	BreadthCooldownBars int    `json:"breadth_cooldown_bars,omitempty"` // min bars between fires (0 => disabled, the validated default)

	// BreadthCutWinners turns the breadth breaker into a full deleveraging circuit
	// breaker: when the gate fires, retracing WINNING positions are cut too (not
	// just losers). Default false = surgical (losers only, winners ride break-even).
	// Enable for a system-wide "go to cash on a correlated crash" policy.
	BreadthCutWinners bool `json:"breadth_cut_winners,omitempty"`

	// Monitor cadence floor (seconds); 0 => reuse drawdown monitor cadence.
	PollIntervalSeconds int `json:"poll_interval_seconds,omitempty"`
}

type ProtectionMode string

const (
	ProtectionModeDisabled ProtectionMode = "disabled"
	ProtectionModeManual   ProtectionMode = "manual"
	ProtectionModeAI       ProtectionMode = "ai"
)

type ProtectionValueMode string

const (
	ProtectionValueModeDisabled ProtectionValueMode = "disabled"
	ProtectionValueModeManual   ProtectionValueMode = "manual"
	ProtectionValueModeAI       ProtectionValueMode = "ai"
)

// ProtectionDistanceUnit selects how a protection distance/trigger VALUE is
// interpreted at placement and runtime. This replaces the former standalone ATR
// overlay: instead of a separate panel rewriting every distance, each field
// chooses its own unit. "percent" = the value is a percent-of-entry distance
// (the legacy behaviour); "atr" = the value is an ATR MULTIPLE, converted to an
// effective percent at use time via value * ATR(period) / entryPrice * 100
// (clamped to the ATRProtection min/max bounds). Empty defaults to "percent".
type ProtectionDistanceUnit string

const (
	ProtectionUnitPercent ProtectionDistanceUnit = "percent"
	ProtectionUnitATR     ProtectionDistanceUnit = "atr"
)

type ProtectionValueSource struct {
	Mode  ProtectionValueMode `json:"mode,omitempty"`
	Value float64             `json:"value,omitempty"`
}

func (p *ProtectionValueSource) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*p = ProtectionValueSource{}
		return nil
	}

	var probe map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &probe); err == nil {
		if _, hasEnabled := probe["enabled"]; hasEnabled {
			var legacy struct {
				Enabled bool    `json:"enabled"`
				Value   float64 `json:"value,omitempty"`
			}
			if err := json.Unmarshal(trimmed, &legacy); err != nil {
				return err
			}
			if legacy.Enabled {
				p.Mode = ProtectionValueModeManual
				p.Value = legacy.Value
			} else {
				p.Mode = ProtectionValueModeDisabled
				p.Value = 0
			}
			return nil
		}
	}

	type alias ProtectionValueSource
	var decoded alias
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return err
	}
	p.Mode = decoded.Mode
	p.Value = decoded.Value
	return nil
}

type FullTPSLConfig struct {
	Enabled                bool                  `json:"enabled"`
	Mode                   ProtectionMode        `json:"mode,omitempty"`
	TakeProfitEnabled      bool                  `json:"take_profit_enabled,omitempty"`
	StopLossEnabled        bool                  `json:"stop_loss_enabled,omitempty"`
	FallbackMaxLossEnabled bool                  `json:"fallback_max_loss_enabled,omitempty"`
	TakeProfit             ProtectionValueSource `json:"take_profit,omitempty"`
	StopLoss               ProtectionValueSource `json:"stop_loss,omitempty"`
	FallbackMaxLoss        ProtectionValueSource `json:"fallback_max_loss,omitempty"`
}

type ProtectionThresholdRule struct {
	Enabled      bool    `json:"enabled"`
	PriceMovePct float64 `json:"price_move_pct,omitempty"`
}

type LadderTPSLConfig struct {
	Enabled           bool                  `json:"enabled"`
	Mode              ProtectionMode        `json:"mode,omitempty"`
	TakeProfitEnabled bool                  `json:"take_profit_enabled"`
	StopLossEnabled   bool                  `json:"stop_loss_enabled"`
	TakeProfitPrice   ProtectionValueSource `json:"take_profit_price,omitempty"`
	TakeProfitSize    ProtectionValueSource `json:"take_profit_size,omitempty"`
	StopLossPrice     ProtectionValueSource `json:"stop_loss_price,omitempty"`
	StopLossSize      ProtectionValueSource `json:"stop_loss_size,omitempty"`
	Rules             []LadderTPSLRule      `json:"rules,omitempty"`
	FallbackMaxLoss   ProtectionValueSource `json:"fallback_max_loss,omitempty"`
}

type LadderTPSLRule struct {
	TakeProfitPct           float64 `json:"take_profit_pct,omitempty"`
	TakeProfitCloseRatioPct float64 `json:"take_profit_close_ratio_pct,omitempty"`
	StopLossPct             float64 `json:"stop_loss_pct,omitempty"`
	StopLossCloseRatioPct   float64 `json:"stop_loss_close_ratio_pct,omitempty"`

	// Per-field distance unit (percent|atr). When "atr", the corresponding *Pct
	// value is an ATR MULTIPLE resolved to an effective percent at use time.
	// Empty => percent (legacy). Replaces the standalone ATR overlay.
	TakeProfitUnit ProtectionDistanceUnit `json:"take_profit_unit,omitempty"`
	StopLossUnit   ProtectionDistanceUnit `json:"stop_loss_unit,omitempty"`
}

type DrawdownEngineMode string

const (
	DrawdownEngineModeManual DrawdownEngineMode = "manual"
	DrawdownEngineModeAI     DrawdownEngineMode = "ai"
)

const (
	DrawdownBreakEvenRunnerPrimary      = "primary"
	DrawdownBreakEvenRunnerFallbackOnly = "fallback_only"
	DrawdownBreakEvenRunnerDisabled     = "disabled_for_runner"
)

type DrawdownTakeProfitConfig struct {
	Enabled               bool                     `json:"enabled"`
	Mode                  ProtectionMode           `json:"mode,omitempty"`
	EngineMode            DrawdownEngineMode       `json:"engine_mode,omitempty"`
	RunnerEnabled         bool                     `json:"runner_enabled,omitempty"`
	MinRunnerKeepPct      float64                  `json:"min_runner_keep_pct,omitempty"`
	MaxFirstReducePct     float64                  `json:"max_first_reduce_pct,omitempty"`
	BreakEvenRunnerPolicy string                   `json:"break_even_runner_policy,omitempty"`
	Rules                 []DrawdownTakeProfitRule `json:"rules,omitempty"`
}

func (c *DrawdownTakeProfitConfig) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*c = DrawdownTakeProfitConfig{}
		return nil
	}

	type alias DrawdownTakeProfitConfig
	var decoded alias
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return err
	}

	*c = DrawdownTakeProfitConfig(decoded)

	if c.Mode == "" {
		if c.Enabled {
			c.Mode = ProtectionModeManual
		} else {
			c.Mode = ProtectionModeDisabled
		}
	}
	if c.EngineMode == "" {
		if c.Mode == ProtectionModeAI {
			c.EngineMode = DrawdownEngineModeAI
		} else {
			c.EngineMode = DrawdownEngineModeManual
		}
	}
	if c.MinRunnerKeepPct <= 0 {
		c.MinRunnerKeepPct = 20
	}
	if c.MaxFirstReducePct <= 0 {
		c.MaxFirstReducePct = 60
	}
	if c.BreakEvenRunnerPolicy == "" {
		c.BreakEvenRunnerPolicy = DrawdownBreakEvenRunnerFallbackOnly
	}

	return nil
}

type DrawdownTakeProfitRule struct {
	Timeframe           string  `json:"timeframe,omitempty"`
	MinProfitPct        float64 `json:"min_profit_pct,omitempty"`
	MaxDrawdownPct      float64 `json:"max_drawdown_pct,omitempty"`
	MaxDrawdownAbsPct   float64 `json:"max_drawdown_abs_profit_pct,omitempty"`
	CloseRatioPct       float64 `json:"close_ratio_pct,omitempty"`
	PollIntervalSeconds int     `json:"poll_interval_seconds,omitempty"`
	ReasonAnchor        string  `json:"reason_anchor,omitempty"`
	StageName           string  `json:"stage_name,omitempty"`
	RunnerKeepPct       float64 `json:"runner_keep_pct,omitempty"`
	RunnerStopMode      string  `json:"runner_stop_mode,omitempty"`
	RunnerStopSource    string  `json:"runner_stop_source,omitempty"`
	RunnerTargetMode    string  `json:"runner_target_mode,omitempty"`
	RunnerTargetSource  string  `json:"runner_target_source,omitempty"`

	// Per-field AI/manual control: each dimension can independently choose AI or manual value.
	// When mode is "ai", AI decides the value; when "manual", the configured value is used.
	CloseRatioMode  ProtectionValueMode `json:"close_ratio_mode,omitempty"`
	MinProfitMode   ProtectionValueMode `json:"min_profit_mode,omitempty"`
	MaxDrawdownMode ProtectionValueMode `json:"max_drawdown_mode,omitempty"`

	// MinProfitUnit selects how MinProfitPct (the arm threshold) is interpreted:
	// percent-of-entry (legacy) or ATR multiple. "atr" resolves to an effective
	// percent at runtime so the open-time and arm-time thresholds stay consistent
	// (fixes the prior overlay mismatch where DD armed on the raw percent).
	MinProfitUnit ProtectionDistanceUnit `json:"min_profit_unit,omitempty"`

	// MaxDrawdownUnit selects how MaxDrawdownPct (the giveback-from-peak trigger)
	// is interpreted: percent (legacy) or ATR multiple. "atr" makes the giveback
	// distance volatility-adaptive, matching MinProfitUnit so both the arm and the
	// trigger scale with ATR instead of one tracking volatility and the other not.
	MaxDrawdownUnit ProtectionDistanceUnit `json:"max_drawdown_unit,omitempty"`
}

type BreakEvenTriggerMode string

const (
	BreakEvenTriggerProfitPct BreakEvenTriggerMode = "profit_pct"
	BreakEvenTriggerRMultiple BreakEvenTriggerMode = "r_multiple"
)

type BreakEvenStopConfig struct {
	Enabled      bool                 `json:"enabled"`
	Mode         ProtectionMode       `json:"mode,omitempty"`
	TriggerMode  BreakEvenTriggerMode `json:"trigger_mode,omitempty"`
	TriggerValue float64              `json:"trigger_value,omitempty"`
	OffsetPct    float64              `json:"offset_pct,omitempty"`
	Rules        []BreakEvenStopRule  `json:"rules,omitempty"`
}

type BreakEvenStopRule struct {
	TriggerMode   BreakEvenTriggerMode `json:"trigger_mode,omitempty"`
	TriggerValue  float64              `json:"trigger_value,omitempty"`
	OffsetPct     float64              `json:"offset_pct,omitempty"`
	CloseRatioPct float64              `json:"close_ratio_pct,omitempty"`
	StageName     string               `json:"stage_name,omitempty"`

	// TriggerUnit selects how TriggerValue is interpreted when TriggerMode is
	// profit_pct: percent-of-entry (legacy) or ATR multiple. "atr" resolves to an
	// effective percent at both open and arm time so they stay consistent.
	TriggerUnit ProtectionDistanceUnit `json:"trigger_unit,omitempty"`
}

// DrawdownTierAllocation records the fixed position allocation for each drawdown tier,
// computed once at position open and never changed afterwards.
type DrawdownTierAllocation struct {
	TierIndex      int     `json:"tier_index"`
	StageName      string  `json:"stage_name"`
	Quantity       float64 `json:"quantity"`
	CloseRatioPct  float64 `json:"close_ratio_pct"`
	MinProfitPct   float64 `json:"min_profit_pct"`
	MaxDrawdownPct float64 `json:"max_drawdown_pct"`
	PeakPnLPct     float64 `json:"peak_pnl_pct"`
	Status         string  `json:"status"` // "pending", "tracking", "executed", "be_covered"
}

type RegimeTrendAlignmentMode string

const (
	RegimeTrendAlignmentStrict                 RegimeTrendAlignmentMode = "strict"
	RegimeTrendAlignmentAllowRangeEdgeReversal RegimeTrendAlignmentMode = "allow_range_edge_reversal"
)

type RegimeFilterConfig struct {
	Enabled               bool                     `json:"enabled"`
	AuditOnly             bool                     `json:"audit_only,omitempty"` // Log rejections but don't block (observation mode)
	AllowedRegimes        []string                 `json:"allowed_regimes,omitempty"`
	BlockHighFunding      bool                     `json:"block_high_funding"`
	MaxFundingRateAbs     float64                  `json:"max_funding_rate_abs,omitempty"`
	BlockHighVolatility   bool                     `json:"block_high_volatility"`
	MaxATR14Pct           float64                  `json:"max_atr14_pct,omitempty"`
	RequireTrendAlignment bool                     `json:"require_trend_alignment"`
	TrendAlignmentMode    RegimeTrendAlignmentMode `json:"trend_alignment_mode,omitempty"`
	// Coin momentum gate — blocks entries on coins with insufficient or excessive momentum
	MomentumGateEnabled    bool    `json:"momentum_gate_enabled"`
	MomentumStaleChg1h     float64 `json:"momentum_stale_chg1h,omitempty"`     // abs(chg1h) below this = stale (default 0.15)
	MomentumStaleChg4h     float64 `json:"momentum_stale_chg4h,omitempty"`     // abs(chg4h) below this AND chg1h stale = stale (default 0.4)
	MomentumExhaustedChg4h float64 `json:"momentum_exhausted_chg4h,omitempty"` // abs(chg4h) above this = exhausted (default 4.5 major, 3.5 alt)
	MomentumCounterChg1h   float64 `json:"momentum_counter_chg1h,omitempty"`   // chg1h opposing direction above this = counter (default 0.3)
	MomentumFadingChg4h    float64 `json:"momentum_fading_chg4h,omitempty"`    // chg4h above this triggers fading check (default 2.5)
	MaxSLDistancePct       float64 `json:"max_sl_distance_pct,omitempty"`      // max SL distance as % of entry price (default 2.0)

	// Asymmetric trend alignment — based on regime-classifier accuracy validation
	// (2026-06-04): trending_up direction accuracy ~45% (counter-indicator),
	// trending_down ~60% (reliable). EMA20-deviation-driven extension over-triggers
	// in ranging markets. When enabled, counter-trend shorts in uptrends and
	// EMA-driven extensions become soft (penalty + size reduction) instead of hard blocks,
	// while reliable downtrend protection (block counter-trend longs) stays hard.
	AsymmetricTrendAlignment  bool `json:"asymmetric_trend_alignment,omitempty"`   // master switch; false = legacy hard-block behavior
	CounterTrendShortPenalty  int  `json:"counter_trend_short_penalty,omitempty"`  // soft penalty for short in trending_up (default 25)
	ExtensionEMADrivenPenalty int  `json:"extension_ema_driven_penalty,omitempty"` // soft penalty for EMA-deviation-driven extension (default 20)
}

// GridStrategyConfig grid trading specific configuration
type GridStrategyConfig struct {
	// Trading pair (e.g., "BTCUSDT")
	Symbol string `json:"symbol"`
	// Number of grid levels (5-50)
	GridCount int `json:"grid_count"`
	// Total investment in USDT
	TotalInvestment float64 `json:"total_investment"`
	// Leverage (1-20)
	Leverage int `json:"leverage"`
	// Upper price boundary (0 = auto-calculate from ATR)
	UpperPrice float64 `json:"upper_price"`
	// Lower price boundary (0 = auto-calculate from ATR)
	LowerPrice float64 `json:"lower_price"`
	// Use ATR to auto-calculate bounds
	UseATRBounds bool `json:"use_atr_bounds"`
	// ATR multiplier for bound calculation (default 2.0)
	ATRMultiplier float64 `json:"atr_multiplier"`
	// Position distribution: "uniform" | "gaussian" | "pyramid"
	Distribution string `json:"distribution"`
	// Maximum drawdown percentage before emergency exit
	MaxDrawdownPct float64 `json:"max_drawdown_pct"`
	// Stop loss percentage per position
	StopLossPct float64 `json:"stop_loss_pct"`
	// Daily loss limit percentage
	DailyLossLimitPct float64 `json:"daily_loss_limit_pct"`
	// Use maker-only orders for lower fees
	UseMakerOnly bool `json:"use_maker_only"`
	// Enable automatic grid direction adjustment based on box breakouts
	EnableDirectionAdjust bool `json:"enable_direction_adjust"`
	// Direction bias ratio for long_bias/short_bias modes (default 0.7 = 70%/30%)
	DirectionBiasRatio float64 `json:"direction_bias_ratio"`
}

// PromptSectionsConfig editable sections of System Prompt
type PromptSectionsConfig struct {
	// role definition (title + description)
	RoleDefinition string `json:"role_definition,omitempty"`
	// trading frequency awareness
	TradingFrequency string `json:"trading_frequency,omitempty"`
	// entry standards
	EntryStandards string `json:"entry_standards,omitempty"`
	// decision process
	DecisionProcess string `json:"decision_process,omitempty"`
}

// EvolutionConfig controls the self-evolution engine behavior.
type EvolutionConfig struct {
	Enabled            bool    `json:"enabled"`                        // Master switch
	HalfLifeDays       int     `json:"half_life_days,omitempty"`       // Time decay half-life (default 14)
	MinSampleSize      int     `json:"min_sample_size,omitempty"`      // Min trades before generating adaptations (default 5)
	AdaptationTTLDays  int     `json:"adaptation_ttl_days,omitempty"`  // Adaptation expiry (default 30)
	ScoreThresholdLow  float64 `json:"score_threshold_low,omitempty"`  // Below this triggers adaptation (default 35)
	ScoreThresholdHigh float64 `json:"score_threshold_high,omitempty"` // Above this is positive signal (default 65)
	InjectToPrompt     bool    `json:"inject_to_prompt,omitempty"`     // Whether to inject profiles into AI prompt
}

// CoinSourceConfig coin source configuration
type CoinSourceConfig struct {
	// source type: "static" | "ai500" | "oi_top" | "oi_low" | "mixed" | "market"
	SourceType string `json:"source_type"`
	// static coin list (used when source_type = "static")
	StaticCoins []string `json:"static_coins,omitempty"`
	// excluded coins list (filtered out from all sources)
	ExcludedCoins []string `json:"excluded_coins,omitempty"`
	// whether to use AI500 coin pool
	UseAI500 bool `json:"use_ai500"`
	// AI500 coin pool maximum count
	AI500Limit int `json:"ai500_limit,omitempty"`
	// whether to use OI Top (OI increase ranking, suitable for long positions)
	UseOITop bool `json:"use_oi_top"`
	// OI Top maximum count
	OITopLimit int `json:"oi_top_limit,omitempty"`
	// whether to use OI Low (OI decrease ranking, suitable for short positions)
	UseOILow bool `json:"use_oi_low"`
	// OI Low maximum count
	OILowLimit int `json:"oi_low_limit,omitempty"`
	// whether to use Hyperliquid All coins (all available perp pairs)
	UseHyperAll bool `json:"use_hyper_all"`
	// whether to use Hyperliquid Main coins (top N by 24h volume)
	UseHyperMain bool `json:"use_hyper_main"`
	// Hyperliquid Main maximum count (default 20)
	HyperMainLimit int `json:"hyper_main_limit,omitempty"`
	// Market source config (used when source_type = "market")
	MarketList  string   `json:"market_list,omitempty"`  // deprecated: single list, kept for backward compat
	MarketLists []string `json:"market_lists,omitempty"` // multi-select: ["hot", "oi_top", "oi_low"]
	MarketLimit int      `json:"market_limit,omitempty"` // top N coins per list
	// Exchange for market data source (default: "okx")
	ExchangeSource string `json:"exchange_source,omitempty"` // "binance" | "okx"
	// Note: API URLs are now built automatically using NofxOSAPIKey from IndicatorConfig
}

// IndicatorConfig indicator configuration
type IndicatorConfig struct {
	// K-line configuration
	Klines KlineConfig `json:"klines"`
	// raw kline data (OHLCV) - always enabled, required for AI analysis
	EnableRawKlines bool `json:"enable_raw_klines"`
	// technical indicator switches
	EnableEMA         bool `json:"enable_ema"`
	EnableMACD        bool `json:"enable_macd"`
	EnableRSI         bool `json:"enable_rsi"`
	EnableATR         bool `json:"enable_atr"`
	EnableBOLL        bool `json:"enable_boll"` // Bollinger Bands
	EnableVolume      bool `json:"enable_volume"`
	EnableOI          bool `json:"enable_oi"`           // open interest
	EnableFundingRate bool `json:"enable_funding_rate"` // funding rate
	// Exchange sentiment data toggles
	EnableLongShortRatio    bool `json:"enable_long_short_ratio"`     // long/short account ratio
	EnableTopTraderRatio    bool `json:"enable_top_trader_ratio"`     // top trader long/short ratio (Binance only)
	EnableTakerBuySellRatio bool `json:"enable_taker_buy_sell_ratio"` // taker buy/sell volume ratio
	EnableOrderBookDepth    bool `json:"enable_order_book_depth"`     // order book depth imbalance
	// EMA period configuration
	EMAPeriods []int `json:"ema_periods,omitempty"` // default [20, 50]
	// RSI period configuration
	RSIPeriods []int `json:"rsi_periods,omitempty"` // default [7, 14]
	// ATR period configuration
	ATRPeriods []int `json:"atr_periods,omitempty"` // default [14]
	// BOLL period configuration (period, standard deviation multiplier is fixed at 2)
	BOLLPeriods []int `json:"boll_periods,omitempty"` // default [20] - can select multiple timeframes
	// external data sources
	ExternalDataSources []ExternalDataSource `json:"external_data_sources,omitempty"`

	// ========== NofxOS Unified API Configuration ==========
	// Unified API Key for all NofxOS data sources
	NofxOSAPIKey string `json:"nofxos_api_key,omitempty"`

	// quantitative data sources (capital flow, position changes, price changes)
	EnableQuantData    bool `json:"enable_quant_data"`    // whether to enable quantitative data
	EnableQuantOI      bool `json:"enable_quant_oi"`      // whether to show OI data
	EnableQuantNetflow bool `json:"enable_quant_netflow"` // whether to show Netflow data

	// OI ranking data (market-wide open interest increase/decrease rankings)
	EnableOIRanking   bool   `json:"enable_oi_ranking"`             // whether to enable OI ranking data
	OIRankingDuration string `json:"oi_ranking_duration,omitempty"` // duration: 1h, 4h, 24h
	OIRankingLimit    int    `json:"oi_ranking_limit,omitempty"`    // number of entries (default 10)

	// NetFlow ranking data (market-wide fund flow rankings - institution/personal)
	EnableNetFlowRanking   bool   `json:"enable_netflow_ranking"`             // whether to enable NetFlow ranking data
	NetFlowRankingDuration string `json:"netflow_ranking_duration,omitempty"` // duration: 1h, 4h, 24h
	NetFlowRankingLimit    int    `json:"netflow_ranking_limit,omitempty"`    // number of entries (default 10)

	// Price ranking data (market-wide gainers/losers)
	EnablePriceRanking   bool   `json:"enable_price_ranking"`             // whether to enable price ranking data
	PriceRankingDuration string `json:"price_ranking_duration,omitempty"` // durations: "1h" or "1h,4h,24h"
	PriceRankingLimit    int    `json:"price_ranking_limit,omitempty"`    // number of entries per ranking (default 10)

	// Derivatives enhancement indicators (Phase B)
	EnableCVD             bool `json:"enable_cvd"`
	EnableOIGrowthRate    bool `json:"enable_oi_growth_rate"`
	EnableFundingHistory  bool `json:"enable_funding_history"`
	EnableVWAP            bool `json:"enable_vwap"`
	EnableTakerDelta      bool `json:"enable_taker_delta"`
	EnableDepthChangeRate bool `json:"enable_depth_change_rate"`
}

// KlineConfig K-line configuration
type KlineConfig struct {
	// primary timeframe: "1m", "3m", "5m", "15m", "1h", "4h"
	PrimaryTimeframe string `json:"primary_timeframe"`
	// primary timeframe K-line count
	PrimaryCount int `json:"primary_count"`
	// longer timeframe
	LongerTimeframe string `json:"longer_timeframe,omitempty"`
	// longer timeframe K-line count
	LongerCount int `json:"longer_count,omitempty"`
	// whether to enable multi-timeframe analysis
	EnableMultiTimeframe bool `json:"enable_multi_timeframe"`
	// selected timeframe list (new: supports multi-timeframe selection)
	SelectedTimeframes []string `json:"selected_timeframes,omitempty"`
}

// ExternalDataSource external data source configuration
type ExternalDataSource struct {
	Name        string            `json:"name"`   // data source name
	Type        string            `json:"type"`   // type: "api" | "webhook"
	URL         string            `json:"url"`    // API URL
	Method      string            `json:"method"` // HTTP method
	Headers     map[string]string `json:"headers,omitempty"`
	DataPath    string            `json:"data_path,omitempty"`    // JSON data path
	RefreshSecs int               `json:"refresh_secs,omitempty"` // refresh interval (seconds)
}

// RiskControlConfig risk control configuration
type RiskControlConfig struct {
	// Max number of coins held simultaneously (CODE ENFORCED)
	MaxPositions int `json:"max_positions"`

	// BTC/ETH exchange leverage for opening positions (AI guided)
	BTCETHMaxLeverage int `json:"btc_eth_max_leverage"`
	// Altcoin exchange leverage for opening positions (AI guided)
	AltcoinMaxLeverage int `json:"altcoin_max_leverage"`

	// BTC/ETH single position max value = equity × this ratio (CODE ENFORCED, default: 5)
	BTCETHMaxPositionValueRatio float64 `json:"btc_eth_max_position_value_ratio"`
	// Altcoin single position max value = equity × this ratio (CODE ENFORCED, default: 1)
	AltcoinMaxPositionValueRatio float64 `json:"altcoin_max_position_value_ratio"`

	// Max margin utilization (e.g. 0.9 = 90%) (CODE ENFORCED)
	MaxMarginUsage float64 `json:"max_margin_usage"`
	// Min position size in USDT (CODE ENFORCED)
	MinPositionSize float64 `json:"min_position_size"`

	// Min take_profit / stop_loss ratio (AI guided)
	MinRiskRewardRatio float64 `json:"min_risk_reward_ratio"`
	// Min AI confidence to open position (AI guided)
	MinConfidence int `json:"min_confidence"`

	// Post-loss entry cooldown per symbol in minutes (CODE ENFORCED, default: 90)
	EntryCooldownMinutes int `json:"entry_cooldown_minutes,omitempty"`
	// Max allowed entry price deviation % between AI plan and execution (CODE ENFORCED, default: 1.5)
	MaxEntryDeviationPct float64 `json:"max_entry_deviation_pct,omitempty"`

	// Time-stop (CODE ENFORCED): force-close a position that has been held longer than
	// TimeStopHours AND is still in loss worse than TimeStopLossPct. Cuts the
	// "directionally-wrong trade bled slowly over 20-37h" loss pattern without touching
	// winners (loss condition is required). 0/unset = disabled.
	TimeStopHours   float64 `json:"time_stop_hours,omitempty"`    // e.g. 24
	TimeStopLossPct float64 `json:"time_stop_loss_pct,omitempty"` // negative, e.g. -1.5

	// Max-hold stop (CODE ENFORCED): force-close any position held longer than
	// MaxHoldHours UNLESS it is a profitable runner (unrealized pnl >= MaxHoldProfitExemptPct).
	// Unlike TimeStop (which only triggers on a loss worse than a threshold), this also clears
	// break-even / dust tails that grind sideways and occupy a position slot. Profitable runners
	// are explicitly spared so trends are not cut short. 0/unset = disabled.
	// Backtest (claude, 2026-05/06): MaxHoldHours=18, ProfitExemptPct=2.0 cut net loss by ~1/3.
	MaxHoldHours           float64 `json:"max_hold_hours,omitempty"`             // e.g. 18
	MaxHoldProfitExemptPct float64 `json:"max_hold_profit_exempt_pct,omitempty"` // positive, e.g. 2.0

	// Trailing take-profit (CODE ENFORCED): once unrealized pnl reaches TrailingActivatePct,
	// track the peak; if pnl gives back TrailingGivebackPct of that peak, close the remaining
	// position. This converts the "cut winners short / let losers run" payoff (claude 0.67) into
	// a let-winners-run structure. A separate, simpler mechanism from the multi-tier
	// DrawdownTakeProfit (which requires AI rules); this one is pure config and always available.
	// Disabled when TrailingTakeProfitEnabled is false or TrailingActivatePct <= 0.
	TrailingTakeProfitEnabled bool    `json:"trailing_take_profit_enabled,omitempty"`
	TrailingActivatePct       float64 `json:"trailing_activate_pct,omitempty"` // e.g. 3.0 (arm once pnl >= +3%)
	TrailingGivebackPct       float64 `json:"trailing_giveback_pct,omitempty"` // e.g. 35 (close if pnl falls 35% below peak)
	TrailingMinLockPct        float64 `json:"trailing_min_lock_pct,omitempty"` // e.g. 0.5 (only fire if locked pnl still >= this)

	// Volatility-targeted position sizing (CODE ENFORCED): scale the AI-proposed position size
	// inversely to the symbol's current ATR14 as a % of price. size *= clamp(VolTargetPct / atr14Pct,
	// VolSizeMinMult, VolSizeMaxMult). High-volatility coins (e.g. HYPE, ZEC) get smaller size;
	// low-volatility coins get up to VolSizeMaxMult. Defends against single-symbol blowups.
	// Disabled when VolSizingEnabled is false or VolTargetPct <= 0.
	VolSizingEnabled bool    `json:"vol_sizing_enabled,omitempty"`
	VolTargetPct     float64 `json:"vol_target_pct,omitempty"`    // target ATR14% reference, e.g. 1.5
	VolSizeMinMult   float64 `json:"vol_size_min_mult,omitempty"` // floor multiplier, e.g. 0.4
	VolSizeMaxMult   float64 `json:"vol_size_max_mult,omitempty"` // cap multiplier, e.g. 1.5

	// Maker-only entry (CODE ENFORCED): place entries as post-only limit orders at the near touch
	// to earn the maker fee (OKX taker 0.05% -> maker 0.02%) instead of crossing the spread.
	// Poll up to MakerEntryTimeoutSec for a fill; if unfilled (or post-only rejected), optionally
	// fall back to a market order when MakerEntryFallbackMarket is true. For high-churn traders
	// (OKX91: 301 trades, fees = 1.5x gross loss) this is the most certain cost saving.
	// Disabled when MakerEntryEnabled is false.
	MakerEntryEnabled        bool `json:"maker_entry_enabled,omitempty"`
	MakerEntryTimeoutSec     int  `json:"maker_entry_timeout_sec,omitempty"`     // e.g. 15
	MakerEntryOffsetTicks    int  `json:"maker_entry_offset_ticks,omitempty"`    // ticks inside best bid/ask, default 0 (at touch)
	MakerEntryFallbackMarket bool `json:"maker_entry_fallback_market,omitempty"` // if unfilled, cross with market

	// Strong-signal position replacement (CODE ENFORCED): when at MaxPositions and a new
	// high-conviction entry arrives, close the weakest existing position to free a slot instead
	// of dropping the signal. Victims are ranked by (smaller notional + longer hold + weaker pnl);
	// strong winners and freshly-opened positions are protected. The cut runs only AFTER cheap,
	// balance-independent prechecks (signal-strength floor + entry price deviation) pass, so the
	// "cut a position but then fail to open" window is minimized. After the cut the position list
	// is re-fetched and the max-positions guard is re-enforced before the open proceeds.
	// Disabled when ReplaceWeakestEnabled is false.
	ReplaceWeakestEnabled      bool    `json:"replace_weakest_enabled,omitempty"`
	ReplaceMinConfidence       int     `json:"replace_min_confidence,omitempty"`        // new-signal absolute confidence floor, e.g. 80
	ReplaceMinHoldMinutes      int     `json:"replace_min_hold_minutes,omitempty"`      // victim must be held at least this long, e.g. 30
	ReplaceMaxVictimProfitPct  float64 `json:"replace_max_victim_profit_pct,omitempty"` // never cut a winner above this pnl%, e.g. 1.0
	ReplaceMinConfidenceMargin int     `json:"replace_min_confidence_margin,omitempty"` // new conf must beat victim entry conf by this, e.g. 5
}

// NewStrategyStore creates a new StrategyStore
func NewStrategyStore(db *gorm.DB) *StrategyStore {
	return &StrategyStore{db: db}
}

func (s *StrategyStore) initTables() error {
	// AutoMigrate will add missing columns without dropping existing data
	return s.db.AutoMigrate(&Strategy{})
}

func (s *StrategyStore) initDefaultData() error {
	// No longer pre-populate strategies - create on demand when user configures
	return nil
}

// GetDefaultStrategyConfig returns the default strategy configuration for the given language
func GetDefaultStrategyConfig(lang string) StrategyConfig {
	// Normalize language to "zh" or "en"
	normalizedLang := "en"
	if lang == "zh" {
		normalizedLang = "zh"
	}

	config := StrategyConfig{
		Language: normalizedLang,
		CoinSource: CoinSourceConfig{
			SourceType: "ai500",
			UseAI500:   true,
			AI500Limit: 10,
			UseOITop:   false,
			OITopLimit: 10,
			UseOILow:   false,
			OILowLimit: 10,
		},
		Indicators: IndicatorConfig{
			Klines: KlineConfig{
				PrimaryTimeframe:     "1h",
				PrimaryCount:         30,
				LongerTimeframe:      "4h",
				LongerCount:          10,
				EnableMultiTimeframe: true,
				SelectedTimeframes:   []string{"1h", "15m", "4h"},
			},
			EnableRawKlines:   true, // Required - raw OHLCV data for AI analysis
			EnableEMA:         false,
			EnableMACD:        false,
			EnableRSI:         false,
			EnableATR:         false,
			EnableBOLL:        false,
			EnableVolume:      true,
			EnableOI:          true,
			EnableFundingRate: true,
			// Exchange sentiment data
			EnableLongShortRatio:    true,
			EnableTopTraderRatio:    true, // Binance only
			EnableTakerBuySellRatio: true,
			EnableOrderBookDepth:    true,
			EMAPeriods:              []int{20, 50},
			RSIPeriods:              []int{7, 14},
			ATRPeriods:              []int{14},
			BOLLPeriods:             []int{20},
			// NofxOS unified API key
			NofxOSAPIKey: "cm_568c67eae410d912c54c",
			// Quant data
			EnableQuantData:    false,
			EnableQuantOI:      false,
			EnableQuantNetflow: false,
			// OI ranking data
			EnableOIRanking:   false,
			OIRankingDuration: "1h",
			OIRankingLimit:    10,
			// NetFlow ranking data
			EnableNetFlowRanking:   false,
			NetFlowRankingDuration: "1h",
			NetFlowRankingLimit:    10,
			// Price ranking data
			EnablePriceRanking:   true,
			PriceRankingDuration: "1h,4h,24h",
			PriceRankingLimit:    10,
			// Derivatives enhancement (Phase B)
			EnableCVD:             true,
			EnableOIGrowthRate:    true,
			EnableFundingHistory:  true,
			EnableVWAP:            true,
			EnableTakerDelta:      true,
			EnableDepthChangeRate: true,
		},
		RiskControl: RiskControlConfig{
			MaxPositions:                 3,   // Max 3 coins simultaneously (CODE ENFORCED)
			BTCETHMaxLeverage:            5,   // BTC/ETH exchange leverage (AI guided)
			AltcoinMaxLeverage:           5,   // Altcoin exchange leverage (AI guided)
			BTCETHMaxPositionValueRatio:  5.0, // BTC/ETH: max position = 5x equity (CODE ENFORCED)
			AltcoinMaxPositionValueRatio: 1.0, // Altcoin: max position = 1x equity (CODE ENFORCED)
			MaxMarginUsage:               0.9, // Max 90% margin usage (CODE ENFORCED)
			MinPositionSize:              12,  // Min 12 USDT per position (CODE ENFORCED)
			MinRiskRewardRatio:           3.0, // Min 3:1 profit/loss ratio (AI guided)
			MinConfidence:                75,  // Min 75% confidence (AI guided)
		},
		StrategyControlPolicy: StrategyControlPolicyConfig{Mode: StrategyControlPolicyModeStrict},
		EntryStructure: EntryStructureConfig{
			Enabled:                          true,
			RequirePrimaryTimeframe:          true,
			RequireAdjacentTimeframes:        true,
			RequireSupportResistance:         true,
			RequireStructuralAnchors:         true,
			RequireFibonacci:                 false,
			MaxSupportLevels:                 3,
			MaxResistanceLevels:              3,
			MaxAnchorCount:                   4,
			AuditPrimaryTimeframe:            true,
			AuditAdjacentTimeframes:          true,
			AuditSupportResistance:           true,
			AuditStructuralAnchors:           true,
			AuditFibonacci:                   true,
			RequireInvalidationTargetLinkage: true,
		},
		Protection: ProtectionConfig{
			FullTPSL: FullTPSLConfig{
				Enabled:         false,
				Mode:            ProtectionModeManual,
				TakeProfit:      ProtectionValueSource{Mode: ProtectionValueModeManual, Value: 0},
				StopLoss:        ProtectionValueSource{Mode: ProtectionValueModeManual, Value: 0},
				FallbackMaxLoss: ProtectionValueSource{Mode: ProtectionValueModeDisabled, Value: 0},
			},
			LadderTPSL: LadderTPSLConfig{
				Enabled:           false,
				Mode:              ProtectionModeManual,
				TakeProfitEnabled: false,
				StopLossEnabled:   false,
				TakeProfitPrice:   ProtectionValueSource{Mode: ProtectionValueModeManual, Value: 0},
				TakeProfitSize:    ProtectionValueSource{Mode: ProtectionValueModeManual, Value: 0},
				StopLossPrice:     ProtectionValueSource{Mode: ProtectionValueModeManual, Value: 0},
				StopLossSize:      ProtectionValueSource{Mode: ProtectionValueModeManual, Value: 0},
				Rules:             []LadderTPSLRule{},
				FallbackMaxLoss:   ProtectionValueSource{Mode: ProtectionValueModeDisabled, Value: 0},
			},
			DrawdownTakeProfit: DrawdownTakeProfitConfig{
				Enabled:               false,
				Mode:                  ProtectionModeManual,
				EngineMode:            DrawdownEngineModeManual,
				RunnerEnabled:         true,
				MinRunnerKeepPct:      20,
				MaxFirstReducePct:     60,
				BreakEvenRunnerPolicy: DrawdownBreakEvenRunnerFallbackOnly,
				Rules: []DrawdownTakeProfitRule{
					{MinProfitPct: 5, MaxDrawdownPct: 40, CloseRatioPct: 100, PollIntervalSeconds: 60},
				},
			},
			BreakEvenStop: BreakEvenStopConfig{
				Enabled:      false,
				Mode:         ProtectionModeManual,
				TriggerMode:  BreakEvenTriggerProfitPct,
				TriggerValue: 3,
				OffsetPct:    0.1,
			},
			RegimeFilter: RegimeFilterConfig{
				Enabled:               false,
				AllowedRegimes:        []string{"narrow", "standard", "wide"},
				BlockHighFunding:      false,
				MaxFundingRateAbs:     0.01,
				BlockHighVolatility:   false,
				MaxATR14Pct:           3.0,
				RequireTrendAlignment: false,
			},
		},
	}

	if lang == "zh" {
		config.PromptSections = PromptSectionsConfig{
			RoleDefinition: `# 你是一个专业的加密货币交易AI

你的任务是根据提供的市场数据做出交易决策。你是一个经验丰富的量化交易员，擅长技术分析和风险管理。`,
			TradingFrequency: `# ⏱️ 交易频率意识

- 优秀交易员：每天2-4笔 ≈ 每小时0.1-0.2笔
- 每小时超过2笔 = 过度交易
- 单笔持仓时间 ≥ 30-60分钟
如果你发现自己每个周期都在交易 → 标准太低；如果持仓不到30分钟就平仓 → 太冲动。`,
			EntryStandards: `# 🎯 入场标准（严格）

只在多个信号共振时入场。自由使用任何有效的分析方法，避免单一指标、信号矛盾、横盘震荡、或平仓后立即重新开仓等低质量行为。`,
			DecisionProcess: `# 📋 决策流程

1. 检查持仓 → 是否止盈/止损
2. 扫描候选币种 + 多时间框架 → 是否存在强信号
3. 先写思维链，再输出结构化JSON`,
		}
	} else {
		config.PromptSections = PromptSectionsConfig{
			RoleDefinition: `# You are a professional cryptocurrency trading AI

Your task is to make trading decisions based on the provided market data. You are an experienced quantitative trader skilled in technical analysis and risk management.`,
			TradingFrequency: `# ⏱️ Trading Frequency Awareness

- Excellent trader: 2-4 trades per day ≈ 0.1-0.2 trades per hour
- >2 trades per hour = overtrading
- Single position holding time ≥ 30-60 minutes
If you find yourself trading every cycle → standards are too low; if closing positions in <30 minutes → too impulsive.`,
			EntryStandards: `# 🎯 Entry Standards (Strict)

Only enter positions when multiple signals resonate. Freely use any effective analysis methods, avoid low-quality behaviors such as single indicators, contradictory signals, sideways oscillation, or immediately restarting after closing positions.`,
			DecisionProcess: `# 📋 Decision Process

1. Check positions → whether to take profit/stop loss
2. Scan candidate coins + multi-timeframe → whether strong signals exist
3. Write chain of thought first, then output structured JSON`,
		}
	}

	return config
}

// Create create a strategy
func (s *StrategyStore) Create(strategy *Strategy) error {
	return s.db.Create(strategy).Error
}

// Update update a strategy
func (s *StrategyStore) Update(strategy *Strategy) error {
	return s.db.Model(&Strategy{}).
		Where("id = ? AND user_id = ?", strategy.ID, strategy.UserID).
		Updates(map[string]interface{}{
			"name":           strategy.Name,
			"description":    strategy.Description,
			"config":         strategy.Config,
			"is_public":      strategy.IsPublic,
			"config_visible": strategy.ConfigVisible,
			"updated_at":     time.Now().UTC(),
		}).Error
}

// Delete delete a strategy
func (s *StrategyStore) Delete(userID, id string) error {
	// do not allow deleting system default strategy
	var st Strategy
	if err := s.db.Where("id = ?", id).First(&st).Error; err == nil && st.IsDefault {
		return fmt.Errorf("cannot delete system default strategy")
	}

	return s.db.Where("id = ? AND user_id = ?", id, userID).Delete(&Strategy{}).Error
}

// List get user's strategy list
func (s *StrategyStore) List(userID string) ([]*Strategy, error) {
	var strategies []*Strategy
	err := s.db.Where("user_id = ? OR is_default = ?", userID, true).
		Order("is_default DESC, created_at DESC").
		Find(&strategies).Error
	if err != nil {
		return nil, err
	}
	return strategies, nil
}

// ListPublic get all public strategies for the strategy market
func (s *StrategyStore) ListPublic() ([]*Strategy, error) {
	var strategies []*Strategy
	err := s.db.Where("is_public = ?", true).
		Order("created_at DESC").
		Find(&strategies).Error
	if err != nil {
		return nil, err
	}
	return strategies, nil
}

// Get get a single strategy
func (s *StrategyStore) Get(userID, id string) (*Strategy, error) {
	var st Strategy
	err := s.db.Where("id = ? AND (user_id = ? OR is_default = ?)", id, userID, true).
		First(&st).Error
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// GetActive get user's currently active strategy
func (s *StrategyStore) GetActive(userID string) (*Strategy, error) {
	var st Strategy
	err := s.db.Where("user_id = ? AND is_active = ?", userID, true).First(&st).Error
	if err == gorm.ErrRecordNotFound {
		// no active strategy, return system default strategy
		return s.GetDefault()
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// GetDefault get system default strategy
func (s *StrategyStore) GetDefault() (*Strategy, error) {
	var st Strategy
	err := s.db.Where("is_default = ?", true).First(&st).Error
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// SetActive set active strategy (will first deactivate other strategies)
func (s *StrategyStore) SetActive(userID, strategyID string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// first deactivate all strategies for the user
		if err := tx.Model(&Strategy{}).Where("user_id = ?", userID).
			Update("is_active", false).Error; err != nil {
			return err
		}

		// activate specified strategy
		return tx.Model(&Strategy{}).
			Where("id = ? AND (user_id = ? OR is_default = ?)", strategyID, userID, true).
			Update("is_active", true).Error
	})
}

// Duplicate duplicate a strategy (used to create custom strategy based on default strategy)
func (s *StrategyStore) Duplicate(userID, sourceID, newID, newName string) error {
	// get source strategy
	source, err := s.Get(userID, sourceID)
	if err != nil {
		return fmt.Errorf("failed to get source strategy: %w", err)
	}

	// create new strategy
	newStrategy := &Strategy{
		ID:          newID,
		UserID:      userID,
		Name:        newName,
		Description: "Created based on [" + source.Name + "]",
		IsActive:    false,
		IsDefault:   false,
		Config:      source.Config,
	}

	return s.Create(newStrategy)
}

// ParseConfig parse strategy configuration JSON
func (s *Strategy) ParseConfig() (*StrategyConfig, error) {
	var config StrategyConfig
	if err := json.Unmarshal([]byte(s.Config), &config); err != nil {
		return nil, fmt.Errorf("failed to parse strategy configuration: %w", err)
	}
	config.Indicators.FillSentimentDefaults()
	return &config, nil
}

// FillSentimentDefaults ensures new sentiment toggle fields default to true
// for strategies created before these fields existed (where all 4 would be false).
func (ind *IndicatorConfig) FillSentimentDefaults() {
	// If all 4 new sentiment fields are false but OI or funding rate is enabled,
	// this is likely a pre-existing strategy — default them all to true.
	if !ind.EnableLongShortRatio && !ind.EnableTopTraderRatio &&
		!ind.EnableTakerBuySellRatio && !ind.EnableOrderBookDepth {
		if ind.EnableOI || ind.EnableFundingRate {
			ind.EnableLongShortRatio = true
			ind.EnableTopTraderRatio = true
			ind.EnableTakerBuySellRatio = true
			ind.EnableOrderBookDepth = true
		}
	}
}

// SetConfig set strategy configuration
func (s *Strategy) SetConfig(config *StrategyConfig) error {
	data, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to serialize strategy configuration: %w", err)
	}
	s.Config = string(data)
	return nil
}
