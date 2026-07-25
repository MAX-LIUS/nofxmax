package store

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"
)

// DecisionStore decision log storage
type DecisionStore struct {
	db *gorm.DB
}

// DecisionRecordDB internal GORM model for decision_records table
type DecisionRecordDB struct {
	ID                  int64     `gorm:"primaryKey;autoIncrement"`
	TraderID            string    `gorm:"column:trader_id;not null;index:idx_decision_records_trader_time;index:idx_decision_records_trader_cycle,priority:1"`
	CycleNumber         int       `gorm:"column:cycle_number;not null;index:idx_decision_records_trader_cycle,priority:2"`
	Timestamp           time.Time `gorm:"not null;index:idx_decision_records_trader_time,sort:desc;index:idx_decision_records_timestamp,sort:desc"`
	SystemPrompt        string    `gorm:"column:system_prompt;default:''"`
	InputPrompt         string    `gorm:"column:input_prompt;default:''"`
	CoTTrace            string    `gorm:"column:cot_trace;default:''"`
	DecisionJSON        string    `gorm:"column:decision_json;default:''"`
	RawResponse         string    `gorm:"column:raw_response;default:''"`
	CandidateCoins      string    `gorm:"column:candidate_coins;default:''"`
	ExecutionLog        string    `gorm:"column:execution_log;default:''"`
	Decisions           string    `gorm:"column:decisions;default:'[]'"`
	ProtectionSnapshot  string    `gorm:"column:protection_snapshot;default:''"`
	ReviewContext       string    `gorm:"column:review_context;default:''"`
	AllowAIClose        bool      `gorm:"column:allow_ai_close;default:true"`
	AllowAIOpen         bool      `gorm:"column:allow_ai_open;default:true"`
	AIDecisionMode      string    `gorm:"column:ai_decision_mode;default:'balanced'"`
	Success             bool      `gorm:"default:false"`
	ErrorMessage        string    `gorm:"column:error_message;default:''"`
	AIRequestDurationMs int64     `gorm:"column:ai_request_duration_ms;default:0"`
	CreatedAt           time.Time `json:"created_at"`
}

func (DecisionRecordDB) TableName() string { return "decision_records" }

// DecisionRecord decision record (external API struct)
type DecisionRecord struct {
	ID                  int64                  `json:"id"`
	TraderID            string                 `json:"trader_id"`
	CycleNumber         int                    `json:"cycle_number"`
	Timestamp           time.Time              `json:"timestamp"`
	SystemPrompt        string                 `json:"system_prompt"`
	InputPrompt         string                 `json:"input_prompt"`
	CoTTrace            string                 `json:"cot_trace"`
	DecisionJSON        string                 `json:"decision_json"`
	RawResponse         string                 `json:"raw_response"` // Raw AI response for debugging
	CandidateCoins      []string               `json:"candidate_coins"`
	ExecutionLog        []string               `json:"execution_log"`
	Success             bool                   `json:"success"`
	ErrorMessage        string                 `json:"error_message"`
	AIRequestDurationMs int64                  `json:"ai_request_duration_ms"`
	AccountState        AccountSnapshot        `json:"account_state"`
	Positions           []PositionSnapshot     `json:"positions"`
	Decisions           []DecisionAction       `json:"decisions"`
	ProtectionSnapshot  *ProtectionSnapshot    `json:"protection_snapshot,omitempty"`
	ReviewContext       map[string]interface{} `json:"review_context,omitempty"`
	AllowAIClose        bool                   `json:"allow_ai_close"`
	AllowAIOpen         bool                   `json:"allow_ai_open"`
	AllowAIStopClose    bool                   `json:"allow_ai_stop_close"`
	AllowAITakeProfit   bool                   `json:"allow_ai_take_profit"`
	AIDecisionMode      string                 `json:"ai_decision_mode"`
}

// AccountSnapshot account state snapshot
type AccountSnapshot struct {
	TotalBalance          float64 `json:"total_balance"`
	AvailableBalance      float64 `json:"available_balance"`
	TotalUnrealizedProfit float64 `json:"total_unrealized_profit"`
	PositionCount         int     `json:"position_count"`
	MarginUsedPct         float64 `json:"margin_used_pct"`
	InitialBalance        float64 `json:"initial_balance"`
}

// PositionSnapshot position snapshot
type PositionSnapshot struct {
	Symbol           string  `json:"symbol"`
	Side             string  `json:"side"`
	PositionAmt      float64 `json:"position_amt"`
	EntryPrice       float64 `json:"entry_price"`
	MarkPrice        float64 `json:"mark_price"`
	UnrealizedProfit float64 `json:"unrealized_profit"`
	Leverage         float64 `json:"leverage"`
	LiquidationPrice float64 `json:"liquidation_price"`
}

// DecisionAction decision action
type DecisionAction struct {
	Action        string                       `json:"action"`
	Symbol        string                       `json:"symbol"`
	Quantity      float64                      `json:"quantity"`
	Leverage      int                          `json:"leverage"`
	Price         float64                      `json:"price"`
	StopLoss      float64                      `json:"stop_loss,omitempty"`
	TakeProfit    float64                      `json:"take_profit,omitempty"`
	Confidence    int                          `json:"confidence,omitempty"`
	Reasoning     string                       `json:"reasoning,omitempty"`
	TriggerType   string                       `json:"trigger_type,omitempty"`
	ReviewContext *DecisionActionReviewContext `json:"review_context,omitempty"`
	OrderID       int64                        `json:"order_id"`
	Timestamp     time.Time                    `json:"timestamp"`
	Success       bool                         `json:"success"`
	Error         string                       `json:"error"`
}

// DecisionActionReviewContext captures compact, structured rationale for a single action.
type DecisionActionReviewContext struct {
	PrimaryTimeframe     string                              `json:"primary_timeframe,omitempty"`
	TimeframeContext     *DecisionActionTimeframeContext     `json:"timeframe_context,omitempty"`
	MinRiskReward        float64                             `json:"min_risk_reward,omitempty"`
	RiskReward           *DecisionActionRiskRewardSummary    `json:"risk_reward,omitempty"`
	KeyLevels            *DecisionActionKeyLevels            `json:"key_levels,omitempty"`
	SelectedLevels       []DecisionActionSelectedLevel       `json:"selected_levels,omitempty"`
	Anchors              []DecisionActionReasonAnchor        `json:"anchors,omitempty"`
	HigherAnchors        []DecisionActionReasonAnchor        `json:"higher_timeframe_anchors,omitempty"`
	TimeframeStructures  []DecisionActionTimeframeStructure  `json:"timeframe_structures,omitempty"`
	Protection           *DecisionActionProtectionAlignment  `json:"protection,omitempty"`
	Control              *DecisionActionControlOutcome       `json:"control,omitempty"`
	ExecutionConstraints *DecisionActionExecutionConstraints `json:"execution_constraints,omitempty"`
	QualityGate          *DecisionActionQualityGate          `json:"quality_gate,omitempty"`
	Extra                map[string]interface{}              `json:"extra,omitempty"`
}

// DecisionActionSelectedLevel represents a structural level AI explicitly chose
type DecisionActionSelectedLevel struct {
	Price      float64 `json:"price"`
	Type       string  `json:"type"`
	Timeframe  string  `json:"timeframe"`
	Source     string  `json:"source"`
	UsedFor    string  `json:"used_for"`
	BasisType  string  `json:"basis_type"`
	Reason     string  `json:"reason,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

// DecisionActionQualityGate stores record-only v2 trade-quality checks. During
// shadow rollout this does not block orders; it lets us measure how often AI
// decisions would fail stricter reliability rules before hard enforcement.
type DecisionActionQualityGate struct {
	ShadowMode   bool                   `json:"shadow_mode,omitempty"`
	Decision     string                 `json:"decision,omitempty"`
	Passed       bool                   `json:"passed"`
	FailedChecks []string               `json:"failed_checks,omitempty"`
	Regime       string                 `json:"regime,omitempty"`
	SetupType    string                 `json:"setup_type,omitempty"`
	Confidence   int                    `json:"confidence,omitempty"`
	QualityTotal int                    `json:"quality_total,omitempty"`
	NetRR        float64                `json:"net_rr,omitempty"`
	BlockedStage string                 `json:"blocked_stage,omitempty"`
	GateChecks   []EntryGateCheckRecord `json:"gate_checks,omitempty"`
}

// EntryGateCheckRecord stores one check result with full attribution detail.
type EntryGateCheckRecord struct {
	Code     string `json:"code"`
	Stage    string `json:"stage"`
	Passed   bool   `json:"passed"`
	Detail   string `json:"detail"`
	Values   string `json:"values,omitempty"`
	Enforced bool   `json:"enforced"`
}

// DecisionActionTimeframeContext stores compact structural timeframe evidence for entry audit.
type DecisionActionTimeframeContext struct {
	Primary string   `json:"primary,omitempty"`
	Lower   []string `json:"lower,omitempty"`
	Higher  []string `json:"higher,omitempty"`
}

// DecisionActionControlOutcome stores compact system policy outcome metadata.
type DecisionActionControlOutcome struct {
	Decision                   string   `json:"decision,omitempty"`
	OriginalAction             string   `json:"original_action,omitempty"`
	FinalAction                string   `json:"final_action,omitempty"`
	Reasons                    []string `json:"reasons,omitempty"`
	FailedChecks               []string `json:"failed_checks,omitempty"`
	ConstraintsMerged          bool     `json:"constraints_merged,omitempty"`
	RuntimeRRRecomputed        bool     `json:"runtime_rr_recomputed,omitempty"`
	AIGrossRR                  float64  `json:"ai_gross_rr,omitempty"`
	AINetRR                    float64  `json:"ai_net_rr,omitempty"`
	RuntimeGrossRR             float64  `json:"runtime_gross_rr,omitempty"`
	RuntimeNetRR               float64  `json:"runtime_net_rr,omitempty"`
	EffectiveRR                float64  `json:"effective_rr,omitempty"`
	EffectiveRRSource          string   `json:"effective_rr_source,omitempty"`
	ExecutionConstraintSources []string `json:"execution_constraint_sources,omitempty"`
	RegimeCurrent              string   `json:"regime_current,omitempty"`
	RegimeAllowed              []string `json:"regime_allowed,omitempty"`
	RegimePrimaryTimeframe     string   `json:"regime_primary_timeframe,omitempty"`
	RegimeATR14Pct             float64  `json:"regime_atr14_pct,omitempty"`
	RegimeFundingRate          float64  `json:"regime_funding_rate,omitempty"`
	RegimeTrendAligned         *bool    `json:"regime_trend_aligned,omitempty"`
	RegimeStructureCurrent     string   `json:"regime_structure_current,omitempty"`
	RegimeStructureAllowed     []string `json:"regime_structure_allowed,omitempty"`
	NoOrderPlaced              bool     `json:"no_order_placed,omitempty"`
}

// DecisionActionExecutionConstraints stores compact execution-relevant venue constraints.
type DecisionActionExecutionConstraints struct {
	TickSize             float64 `json:"tick_size,omitempty"`
	PricePrecision       int     `json:"price_precision,omitempty"`
	QtyStepSize          float64 `json:"qty_step_size,omitempty"`
	QtyPrecision         int     `json:"qty_precision,omitempty"`
	MinQty               float64 `json:"min_qty,omitempty"`
	MinNotional          float64 `json:"min_notional,omitempty"`
	ContractValue        float64 `json:"contract_value,omitempty"`
	MarkPrice            float64 `json:"mark_price,omitempty"`
	LastPrice            float64 `json:"last_price,omitempty"`
	BestBid              float64 `json:"best_bid,omitempty"`
	BestAsk              float64 `json:"best_ask,omitempty"`
	SpreadBps            float64 `json:"spread_bps,omitempty"`
	TakerFeeRate         float64 `json:"taker_fee_rate,omitempty"`
	MakerFeeRate         float64 `json:"maker_fee_rate,omitempty"`
	EstimatedSlippageBps float64 `json:"estimated_slippage_bps,omitempty"`
}

// DecisionActionRiskRewardSummary stores gross/net RR and pass/fail metadata.
type DecisionActionRiskRewardSummary struct {
	Entry            float64 `json:"entry,omitempty"`
	Invalidation     float64 `json:"invalidation,omitempty"`
	FirstTarget      float64 `json:"first_target,omitempty"`
	GrossEstimatedRR float64 `json:"gross_estimated_rr,omitempty"`
	NetEstimatedRR   float64 `json:"net_estimated_rr,omitempty"`
	Passed           bool    `json:"passed"`
}

// DecisionActionKeyLevels stores compact key support/resistance levels for audit UI.
type DecisionActionKeyLevels struct {
	Support    []float64                       `json:"support,omitempty"`
	Resistance []float64                       `json:"resistance,omitempty"`
	SwingHighs []float64                       `json:"swing_highs,omitempty"`
	SwingLows  []float64                       `json:"swing_lows,omitempty"`
	Fibonacci  *DecisionActionFibonacciSummary `json:"fibonacci,omitempty"`
}

type DecisionActionTimeframeStructure struct {
	Timeframe  string                          `json:"timeframe,omitempty"`
	Role       string                          `json:"role,omitempty"`
	Support    []float64                       `json:"support,omitempty"`
	Resistance []float64                       `json:"resistance,omitempty"`
	Fibonacci  *DecisionActionFibonacciSummary `json:"fibonacci,omitempty"`
	Anchors    []DecisionActionReasonAnchor    `json:"anchors,omitempty"`
	ATR14Pct   float64                         `json:"atr14_pct,omitempty"`
	Trend      string                          `json:"trend,omitempty"`
	UsedFor    string                          `json:"used_for,omitempty"`
}

// DecisionActionFibonacciSummary stores compact fibonacci structural anchors when configured.
type DecisionActionFibonacciSummary struct {
	SwingHigh float64   `json:"swing_high,omitempty"`
	SwingLow  float64   `json:"swing_low,omitempty"`
	Levels    []float64 `json:"levels,omitempty"`
}

// DecisionActionReasonAnchor stores a compact rationale anchor.
type DecisionActionReasonAnchor struct {
	Type      string  `json:"type,omitempty"`
	Timeframe string  `json:"timeframe,omitempty"`
	Price     float64 `json:"price,omitempty"`
	Reason    string  `json:"reason,omitempty"`
}

// DecisionActionProtectionAlignment stores compact protection alignment audit notes.
type DecisionActionProtectionAlignment struct {
	StopBeyondInvalidation bool     `json:"stop_beyond_invalidation,omitempty"`
	TargetAligned          bool     `json:"target_aligned,omitempty"`
	BreakEvenBeforeTarget  bool     `json:"break_even_before_target,omitempty"`
	FallbackWithinEnvelope bool     `json:"fallback_within_envelope,omitempty"`
	PolicyStatus           string   `json:"policy_status,omitempty"`
	PolicyOverride         bool     `json:"policy_override,omitempty"`
	PolicyRejected         bool     `json:"policy_rejected,omitempty"`
	PolicyReasons          []string `json:"policy_reasons,omitempty"`
	Notes                  []string `json:"notes,omitempty"`
}

// ProtectionSnapshot captures the active protection configuration at decision time
type ProtectionSnapshot struct {
	FullTPSL   *ProtectionSnapshotFullTPSL  `json:"full_tp_sl,omitempty"`
	LadderTPSL *ProtectionSnapshotLadder    `json:"ladder_tp_sl,omitempty"`
	Drawdown   []ProtectionSnapshotDrawdown `json:"drawdown,omitempty"`
	BreakEven  *ProtectionSnapshotBreakEven `json:"break_even,omitempty"`
}

type ProtectionSnapshotValueSource struct {
	Mode  string  `json:"mode,omitempty"`
	Value float64 `json:"value,omitempty"`
}

// ProtectionSnapshotFullTPSL full take-profit / stop-loss snapshot
type ProtectionSnapshotFullTPSL struct {
	Enabled         bool                          `json:"enabled"`
	Mode            string                        `json:"mode"`
	TakeProfit      ProtectionSnapshotValueSource `json:"take_profit,omitempty"`
	StopLoss        ProtectionSnapshotValueSource `json:"stop_loss,omitempty"`
	FallbackMaxLoss ProtectionSnapshotValueSource `json:"fallback_max_loss,omitempty"`
}

// ProtectionSnapshotLadder ladder TP/SL snapshot with concrete rules
type ProtectionSnapshotLadder struct {
	Enabled           bool                           `json:"enabled"`
	Mode              string                         `json:"mode"`
	TakeProfitEnabled bool                           `json:"take_profit_enabled"`
	StopLossEnabled   bool                           `json:"stop_loss_enabled"`
	TakeProfitPrice   ProtectionSnapshotValueSource  `json:"take_profit_price,omitempty"`
	TakeProfitSize    ProtectionSnapshotValueSource  `json:"take_profit_size,omitempty"`
	StopLossPrice     ProtectionSnapshotValueSource  `json:"stop_loss_price,omitempty"`
	StopLossSize      ProtectionSnapshotValueSource  `json:"stop_loss_size,omitempty"`
	FallbackMaxLoss   ProtectionSnapshotValueSource  `json:"fallback_max_loss,omitempty"`
	Rules             []ProtectionSnapshotLadderRule `json:"rules,omitempty"`
}

// ProtectionSnapshotLadderRule a single ladder rule with concrete values
type ProtectionSnapshotLadderRule struct {
	TakeProfitPct           float64 `json:"take_profit_pct,omitempty"`
	TakeProfitPrice         float64 `json:"take_profit_price,omitempty"`
	TakeProfitCloseRatioPct float64 `json:"take_profit_close_ratio_pct,omitempty"`
	StopLossPct             float64 `json:"stop_loss_pct,omitempty"`
	StopLossPrice           float64 `json:"stop_loss_price,omitempty"`
	StopLossCloseRatioPct   float64 `json:"stop_loss_close_ratio_pct,omitempty"`
	StructuralAnchor        string  `json:"structural_anchor,omitempty"`
	TakeProfitAnchor        string  `json:"take_profit_anchor,omitempty"`
	StopLossAnchor          string  `json:"stop_loss_anchor,omitempty"`
	VolatilityBufferPct     float64 `json:"volatility_buffer_pct,omitempty"`
}

// ProtectionSnapshotDrawdown drawdown take-profit rule snapshot
type ProtectionSnapshotDrawdown struct {
	Mode              string  `json:"mode,omitempty"`
	Source            string  `json:"source,omitempty"`
	MinProfitPct      float64 `json:"min_profit_pct"`
	MaxDrawdownPct    float64 `json:"max_drawdown_pct"`
	MaxDrawdownAbsPct float64 `json:"max_drawdown_abs_profit_pct,omitempty"`
	CloseRatioPct     float64 `json:"close_ratio_pct"`
	PollIntervalS     int     `json:"poll_interval_s"`
}

// ProtectionSnapshotBreakEven break-even stop snapshot
type ProtectionSnapshotBreakEven struct {
	Enabled      bool    `json:"enabled"`
	Source       string  `json:"source,omitempty"`
	TriggerMode  string  `json:"trigger_mode"`
	TriggerValue float64 `json:"trigger_value"`
	OffsetPct    float64 `json:"offset_pct"`
}

// Statistics statistics information
type Statistics struct {
	TotalCycles         int `json:"total_cycles"`
	SuccessfulCycles    int `json:"successful_cycles"`
	FailedCycles        int `json:"failed_cycles"`
	TotalOpenPositions  int `json:"total_open_positions"`
	TotalClosePositions int `json:"total_close_positions"`

	// Expectancy metrics (computed from closed trader_positions). These quantify whether the
	// strategy has a positive edge. ExpectancyUSD < 0 means the strategy loses money per trade
	// on average — the single most important health signal.
	ClosedTrades  int     `json:"closed_trades"`  // closed positions with a recorded exit
	NetWins       int     `json:"net_wins"`       // closed trades with (realized_pnl - fee) > 0
	NetWinRate    float64 `json:"net_win_rate"`   // net_wins / closed_trades (0..1)
	AvgWinUSD     float64 `json:"avg_win_usd"`    // average net profit on winning trades
	AvgLossUSD    float64 `json:"avg_loss_usd"`   // average net loss on losing trades (negative)
	PayoffRatio   float64 `json:"payoff_ratio"`   // avg_win / |avg_loss|; >1 means winners bigger
	ExpectancyUSD float64 `json:"expectancy_usd"` // net pnl per trade = (grossPnl - fees) / closedTrades
	GrossPnLUSD   float64 `json:"gross_pnl_usd"`  // sum of realized_pnl (no fees)
	TotalFeesUSD  float64 `json:"total_fees_usd"` // sum of fees
	NetPnLUSD     float64 `json:"net_pnl_usd"`    // grossPnl - fees
	FeeDragRatio  float64 `json:"fee_drag_ratio"` // fees / |grossPnl|; >1 means fees exceed gross result
	HealthFlag    string  `json:"health_flag"`    // "positive" | "marginal" | "negative"
}

// NewDecisionStore creates a new DecisionStore
func NewDecisionStore(db *gorm.DB) *DecisionStore {
	return &DecisionStore{db: db}
}

// initTables initializes AI decision log tables
func (s *DecisionStore) initTables() error {
	// For PostgreSQL with existing table, add missing columns instead of full AutoMigrate
	if s.db.Dialector.Name() == "postgres" {
		var tableExists int64
		s.db.Raw(`SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'decision_records'`).Scan(&tableExists)
		if tableExists > 0 {
			// Add protection_snapshot column if missing (safe: ADD COLUMN IF NOT EXISTS)
			s.db.Exec(`ALTER TABLE decision_records ADD COLUMN IF NOT EXISTS protection_snapshot TEXT DEFAULT ''`)
			s.db.Exec(`ALTER TABLE decision_records ADD COLUMN IF NOT EXISTS review_context TEXT DEFAULT ''`)
			s.db.Exec(`ALTER TABLE decision_records ADD COLUMN IF NOT EXISTS allow_ai_close BOOLEAN DEFAULT true`)
			s.db.Exec(`ALTER TABLE decision_records ADD COLUMN IF NOT EXISTS allow_ai_open BOOLEAN DEFAULT true`)
			s.db.Exec(`ALTER TABLE decision_records ADD COLUMN IF NOT EXISTS ai_decision_mode TEXT DEFAULT 'balanced'`)
			return nil
		}
	}
	return s.db.AutoMigrate(&DecisionRecordDB{})
}

// toRecord converts DB model to API struct
func (db *DecisionRecordDB) toRecord() *DecisionRecord {
	record := &DecisionRecord{
		ID:                  db.ID,
		TraderID:            db.TraderID,
		CycleNumber:         db.CycleNumber,
		Timestamp:           db.Timestamp,
		SystemPrompt:        db.SystemPrompt,
		InputPrompt:         db.InputPrompt,
		CoTTrace:            db.CoTTrace,
		DecisionJSON:        db.DecisionJSON,
		RawResponse:         db.RawResponse,
		Success:             db.Success,
		ErrorMessage:        db.ErrorMessage,
		AIRequestDurationMs: db.AIRequestDurationMs,
		AllowAIClose:        db.AllowAIClose,
		AllowAIOpen:         db.AllowAIOpen,
		AIDecisionMode:      db.AIDecisionMode,
	}
	json.Unmarshal([]byte(db.CandidateCoins), &record.CandidateCoins)
	json.Unmarshal([]byte(db.ExecutionLog), &record.ExecutionLog)
	json.Unmarshal([]byte(db.Decisions), &record.Decisions)
	if db.ProtectionSnapshot != "" {
		var ps ProtectionSnapshot
		if err := json.Unmarshal([]byte(db.ProtectionSnapshot), &ps); err == nil {
			record.ProtectionSnapshot = &ps
		}
	}
	if db.ReviewContext != "" {
		var rc map[string]interface{}
		if err := json.Unmarshal([]byte(db.ReviewContext), &rc); err == nil {
			record.ReviewContext = rc
			if v, ok := rc["allow_ai_open"].(bool); ok {
				record.AllowAIOpen = v
			}
			if v, ok := rc["allow_ai_close"].(bool); ok {
				record.AllowAIClose = v
			}
			if v, ok := rc["allow_ai_stop_close"].(bool); ok {
				record.AllowAIStopClose = v
			}
			if v, ok := rc["allow_ai_take_profit"].(bool); ok {
				record.AllowAITakeProfit = v
			}
		}
	}
	return record
}

// LogDecision logs decision
func (s *DecisionStore) LogDecision(record *DecisionRecord) error {
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now().UTC()
	} else {
		record.Timestamp = record.Timestamp.UTC()
	}

	// Serialize arrays to JSON
	candidateCoinsJSON, _ := json.Marshal(record.CandidateCoins)
	executionLogJSON, _ := json.Marshal(record.ExecutionLog)
	decisionsJSON, _ := json.Marshal(record.Decisions)
	protectionSnapshotJSON := ""
	if record.ProtectionSnapshot != nil {
		if ps, err := json.Marshal(record.ProtectionSnapshot); err == nil {
			protectionSnapshotJSON = string(ps)
		}
	}
	reviewContextJSON := ""
	if record.ReviewContext != nil {
		if rc, err := json.Marshal(record.ReviewContext); err == nil {
			reviewContextJSON = string(rc)
		}
	}

	dbRecord := &DecisionRecordDB{
		TraderID:            record.TraderID,
		CycleNumber:         record.CycleNumber,
		Timestamp:           record.Timestamp,
		SystemPrompt:        record.SystemPrompt,
		InputPrompt:         record.InputPrompt,
		CoTTrace:            record.CoTTrace,
		DecisionJSON:        record.DecisionJSON,
		RawResponse:         record.RawResponse,
		CandidateCoins:      string(candidateCoinsJSON),
		ExecutionLog:        string(executionLogJSON),
		Decisions:           string(decisionsJSON),
		ProtectionSnapshot:  protectionSnapshotJSON,
		ReviewContext:       reviewContextJSON,
		AllowAIClose:        record.AllowAIClose,
		AllowAIOpen:         record.AllowAIOpen,
		AIDecisionMode:      record.AIDecisionMode,
		Success:             record.Success,
		ErrorMessage:        record.ErrorMessage,
		AIRequestDurationMs: record.AIRequestDurationMs,
	}

	if err := s.db.Create(dbRecord).Error; err != nil {
		return fmt.Errorf("failed to insert decision record: %w", err)
	}
	record.ID = dbRecord.ID
	return nil
}

// GetLatestRecords gets the latest N records for specified trader (sorted by time in ascending order: old to new)
func (s *DecisionStore) GetLatestRecords(traderID string, n int) ([]*DecisionRecord, error) {
	var dbRecords []*DecisionRecordDB
	err := s.db.Where("trader_id = ?", traderID).
		Order("timestamp DESC").
		Limit(n).
		Find(&dbRecords).Error
	if err != nil {
		return nil, fmt.Errorf("failed to query decision records: %w", err)
	}

	records := make([]*DecisionRecord, len(dbRecords))
	for i, db := range dbRecords {
		records[i] = db.toRecord()
	}

	// Reverse array to sort time from old to new
	for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
		records[i], records[j] = records[j], records[i]
	}

	return records, nil
}

// slimDecisionColumns is every decision_records column EXCEPT the four fat prompt/
// response blobs (system_prompt, input_prompt, cot_trace, raw_response), which
// together are ~94% of a row (~110KB of ~117KB). The dashboard list never renders
// them — they are lazy-loaded per cycle via GetDecisionPrompts. Selecting only these
// columns shrinks a row from ~117KB to ~6KB (~20x less transfer + JSON work).
var slimDecisionColumns = []string{
	"id", "trader_id", "cycle_number", "timestamp",
	"decision_json", "candidate_coins", "execution_log", "decisions",
	"protection_snapshot", "review_context",
	"allow_ai_close", "allow_ai_open", "ai_decision_mode",
	"success", "error_message", "ai_request_duration_ms", "created_at",
}

// GetLatestRecordsSlim is GetLatestRecords WITHOUT the heavy prompt/response blobs.
// Returns oldest→newest (same ordering contract as GetLatestRecords). Use this for
// list/dashboard views; call GetDecisionPrompts to lazy-load a single cycle's prompts.
func (s *DecisionStore) GetLatestRecordsSlim(traderID string, n int) ([]*DecisionRecord, error) {
	var dbRecords []*DecisionRecordDB
	err := s.db.Select(slimDecisionColumns).
		Where("trader_id = ?", traderID).
		Order("timestamp DESC").
		Limit(n).
		Find(&dbRecords).Error
	if err != nil {
		return nil, fmt.Errorf("failed to query decision records (slim): %w", err)
	}
	records := make([]*DecisionRecord, len(dbRecords))
	for i, db := range dbRecords {
		records[i] = db.toRecord() // prompt fields are empty strings — omitted by Select
	}
	// Reverse to oldest→newest.
	for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
		records[i], records[j] = records[j], records[i]
	}
	return records, nil
}

// GetDecisionPrompts lazy-loads ONLY the heavy prompt/response blobs for one cycle,
// keyed by trader+cycle. Returned separately so the list endpoint stays lightweight.
func (s *DecisionStore) GetDecisionPrompts(traderID string, cycleNumber int) (systemPrompt, inputPrompt, coTTrace, rawResponse string, err error) {
	var db DecisionRecordDB
	qerr := s.db.Select("system_prompt", "input_prompt", "cot_trace", "raw_response").
		Where("trader_id = ? AND cycle_number = ?", traderID, cycleNumber).
		Order("timestamp DESC").
		First(&db).Error
	if qerr != nil {
		if qerr == gorm.ErrRecordNotFound {
			return "", "", "", "", nil
		}
		return "", "", "", "", fmt.Errorf("failed to query decision prompts: %w", qerr)
	}
	return db.SystemPrompt, db.InputPrompt, db.CoTTrace, db.RawResponse, nil
}

// GetAllLatestRecords gets the latest N records for all traders
func (s *DecisionStore) GetAllLatestRecords(n int) ([]*DecisionRecord, error) {
	var dbRecords []*DecisionRecordDB
	err := s.db.Order("timestamp DESC").Limit(n).Find(&dbRecords).Error
	if err != nil {
		return nil, fmt.Errorf("failed to query decision records: %w", err)
	}

	records := make([]*DecisionRecord, len(dbRecords))
	for i, db := range dbRecords {
		records[i] = db.toRecord()
	}

	// Reverse array
	for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
		records[i], records[j] = records[j], records[i]
	}

	return records, nil
}

func (s *DecisionStore) GetRecordByCycle(traderID string, cycleNumber int) (*DecisionRecord, error) {
	if traderID == "" || cycleNumber <= 0 {
		return nil, nil
	}
	var dbRecord DecisionRecordDB
	err := s.db.Where("trader_id = ? AND cycle_number = ?", traderID, cycleNumber).First(&dbRecord).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query decision record by cycle: %w", err)
	}
	return dbRecord.toRecord(), nil
}

// GetRecordsByDate gets all records for a specified trader on a specified date
func (s *DecisionStore) GetRecordsByDate(traderID string, date time.Time) ([]*DecisionRecord, error) {
	dateStr := date.Format("2006-01-02")

	var dbRecords []*DecisionRecordDB
	err := s.db.Where("trader_id = ? AND DATE(timestamp) = ?", traderID, dateStr).
		Order("timestamp ASC").
		Find(&dbRecords).Error
	if err != nil {
		return nil, fmt.Errorf("failed to query decision records: %w", err)
	}

	records := make([]*DecisionRecord, len(dbRecords))
	for i, db := range dbRecords {
		records[i] = db.toRecord()
	}

	return records, nil
}

// CleanOldRecords cleans old records from N days ago
func (s *DecisionStore) CleanOldRecords(traderID string, days int) (int64, error) {
	cutoffTime := time.Now().AddDate(0, 0, -days)

	result := s.db.Where("trader_id = ? AND timestamp < ?", traderID, cutoffTime).
		Delete(&DecisionRecordDB{})
	if result.Error != nil {
		return 0, fmt.Errorf("failed to clean old records: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// GetStatistics gets statistics information for specified trader
func (s *DecisionStore) GetStatistics(traderID string) (*Statistics, error) {
	stats := &Statistics{}

	var totalCount, successCount int64
	s.db.Model(&DecisionRecordDB{}).Where("trader_id = ?", traderID).Count(&totalCount)
	s.db.Model(&DecisionRecordDB{}).Where("trader_id = ? AND success = ?", traderID, true).Count(&successCount)

	stats.TotalCycles = int(totalCount)
	stats.SuccessfulCycles = int(successCount)
	stats.FailedCycles = stats.TotalCycles - stats.SuccessfulCycles

	// Count from trader_positions table using raw query for cross-table
	s.db.Raw("SELECT COUNT(*) FROM trader_positions WHERE trader_id = ?", traderID).Scan(&stats.TotalOpenPositions)
	s.db.Raw("SELECT COUNT(*) FROM trader_positions WHERE trader_id = ? AND status = 'CLOSED'", traderID).Scan(&stats.TotalClosePositions)

	s.fillExpectancyMetrics(stats, "trader_id = ?", traderID)

	return stats, nil
}

// fillExpectancyMetrics computes net win-rate, payoff ratio, expectancy and fee-drag from
// CLOSED trader_positions matching the given where clause. Net = realized_pnl - fee per trade.
func (s *DecisionStore) fillExpectancyMetrics(stats *Statistics, where string, args ...interface{}) {
	type row struct {
		RealizedPnl float64
		Fee         float64
	}
	var rows []row
	q := "SELECT realized_pnl AS realized_pnl, fee AS fee FROM trader_positions WHERE status = 'CLOSED'"
	if where != "" {
		q += " AND " + where
	}
	if err := s.db.Raw(q, args...).Scan(&rows).Error; err != nil || len(rows) == 0 {
		return
	}

	var grossSum, feeSum, winSum, lossSum float64
	var winCount, lossCount int
	for _, r := range rows {
		grossSum += r.RealizedPnl
		feeSum += r.Fee
		net := r.RealizedPnl - r.Fee
		if net > 0 {
			winCount++
			winSum += net
		} else if net < 0 {
			lossCount++
			lossSum += net // negative
		}
	}

	n := len(rows)
	stats.ClosedTrades = n
	stats.NetWins = winCount
	stats.GrossPnLUSD = grossSum
	stats.TotalFeesUSD = feeSum
	stats.NetPnLUSD = grossSum - feeSum
	if n > 0 {
		stats.NetWinRate = float64(winCount) / float64(n)
		stats.ExpectancyUSD = stats.NetPnLUSD / float64(n)
	}
	if winCount > 0 {
		stats.AvgWinUSD = winSum / float64(winCount)
	}
	if lossCount > 0 {
		stats.AvgLossUSD = lossSum / float64(lossCount)
	}
	if stats.AvgLossUSD < 0 {
		stats.PayoffRatio = stats.AvgWinUSD / (-stats.AvgLossUSD)
	}
	if grossSum != 0 {
		stats.FeeDragRatio = feeSum / math.Abs(grossSum)
	}

	switch {
	case stats.ExpectancyUSD > 0:
		stats.HealthFlag = "positive"
	case stats.ExpectancyUSD == 0:
		stats.HealthFlag = "marginal"
	default:
		stats.HealthFlag = "negative"
	}
}

// GetAllStatistics gets statistics information for all traders
func (s *DecisionStore) GetAllStatistics() (*Statistics, error) {
	stats := &Statistics{}

	var totalCount, successCount int64
	s.db.Model(&DecisionRecordDB{}).Count(&totalCount)
	s.db.Model(&DecisionRecordDB{}).Where("success = ?", true).Count(&successCount)

	stats.TotalCycles = int(totalCount)
	stats.SuccessfulCycles = int(successCount)
	stats.FailedCycles = stats.TotalCycles - stats.SuccessfulCycles

	// Count from trader_positions table
	s.db.Raw("SELECT COUNT(*) FROM trader_positions").Scan(&stats.TotalOpenPositions)
	s.db.Raw("SELECT COUNT(*) FROM trader_positions WHERE status = 'CLOSED'").Scan(&stats.TotalClosePositions)

	return stats, nil
}

// GetLastCycleNumber gets the last cycle number for specified trader
func (s *DecisionStore) GetLastCycleNumber(traderID string) (int, error) {
	var cycleNumber *int
	err := s.db.Model(&DecisionRecordDB{}).
		Where("trader_id = ?", traderID).
		Select("MAX(cycle_number)").
		Scan(&cycleNumber).Error
	if err != nil {
		return 0, err
	}
	if cycleNumber == nil {
		return 0, nil
	}
	return *cycleNumber, nil
}
