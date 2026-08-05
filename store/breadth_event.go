package store

import (
	"fmt"

	"gorm.io/gorm"
)

// BreadthEvent is a durable, per-cycle snapshot of the breadth circuit breaker's
// evaluation. Unlike the velocity-state table (which only persists the rolling
// pnl history), this records the FULL decision picture every time the breaker
// reaches quorum — whether it fired, was a near-miss, or was blocked — so the
// breaker's behaviour can be reconstructed and tuned after the fact without
// relying on container logs that are lost on restart.
//
// Outcome values:
//
//	"fired"        — gate fired and positions were cut
//	"near_miss"    — quorum met but retr/total fell short of BreadthFrac
//	"cooldown"     — would have fired but BreadthCooldownBars blocked it
//	"no_quorum"    — total < BreadthMinPos (only recorded when retracing>0)
//
// FirePath records WHICH of the two parallel judgment modes caused a fire:
// "quorum" (the per-symbol breadth majority) or "equity" (the account-value
// drawdown breaker). Empty on non-fire outcomes.
//
// One row per evaluation cycle that is worth recording (fire / near-miss /
// cooldown-blocked). Routine "nothing happening" cycles are not persisted.
type BreadthEvent struct {
	ID         int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	TraderID   string `gorm:"column:trader_id;not null;index:idx_breadth_evt_trader,sort:desc" json:"trader_id"`
	ExchangeID string `gorm:"column:exchange_id;default:''" json:"exchange_id"`
	Outcome    string `gorm:"column:outcome;not null" json:"outcome"`

	Total         int     `gorm:"column:total;default:0" json:"total"`                     // open positions evaluated
	Retracing     int     `gorm:"column:retracing;default:0" json:"retracing"`             // classified as retracing
	RetracingFrac float64 `gorm:"column:retracing_frac;default:0" json:"retracing_frac"`   // retr/total
	ThresholdFrac float64 `gorm:"column:threshold_frac;default:0" json:"threshold_frac"`   // BreadthFrac
	MinPos        int     `gorm:"column:min_pos;default:0" json:"min_pos"`                 // BreadthMinPos
	CutCount      int     `gorm:"column:cut_count;default:0" json:"cut_count"`             // positions actually cut
	ATRFailCount  int     `gorm:"column:atr_fail_count;default:0" json:"atr_fail_count"`   // symbols whose ATR could not be resolved
	BarsSinceFire int     `gorm:"column:bars_since_fire;default:0" json:"bars_since_fire"` // cooldown counter at eval time
	CooldownBars  int     `gorm:"column:cooldown_bars;default:0" json:"cooldown_bars"`     // BreadthCooldownBars
	CutWinners    bool    `gorm:"column:cut_winners;default:false" json:"cut_winners"`     // BreadthCutWinners
	UseATR        bool    `gorm:"column:use_atr;default:false" json:"use_atr"`             // BreadthUseATR

	// --- Account-value breaker (parallel judgment mode) ---
	// Recorded on EVERY persisted evaluation when the equity path is enabled, not
	// just on its fires, so a near-miss row shows how close the account-value path
	// was at the same instant the quorum path fell short.
	FirePath      string  `gorm:"column:fire_path;default:''" json:"fire_path"`             // "quorum" | "equity" | "" (no fire)
	EquityPeak    float64 `gorm:"column:equity_peak;default:0" json:"equity_peak"`          // equity high-water mark at eval time
	EquityCur     float64 `gorm:"column:equity_cur;default:0" json:"equity_cur"`            // current total equity (balance + unrealized)
	EquityDDPct   float64 `gorm:"column:equity_dd_pct;default:0" json:"equity_dd_pct"`      // (peak-cur)/peak*100
	EquityDDAbs   float64 `gorm:"column:equity_dd_abs;default:0" json:"equity_dd_abs"`      // peak-cur in USDT
	EquityThrPct  float64 `gorm:"column:equity_thr_pct;default:0" json:"equity_thr_pct"`    // BreadthEquityDDPct
	EquityThrAbs  float64 `gorm:"column:equity_thr_abs;default:0" json:"equity_thr_abs"`    // BreadthEquityDDAbs
	EquityScope   string  `gorm:"column:equity_scope;default:''" json:"equity_scope"`       // BreadthEquityScope in effect
	EquityFetchOK bool    `gorm:"column:equity_fetch_ok;default:false" json:"equity_fetch_ok"` // false => equity unavailable, path skipped (fail-safe)
	// LegsJSON is the per-position detail at eval time: symbol, side, entry, mark,
	// profit%, peak%, whether classified retracing, the resolved ATR% (0 = ATR
	// resolution failed for that symbol), and whether it was cut. JSON array.
	LegsJSON   string `gorm:"column:legs_json;type:text;default:'[]'" json:"legs_json"`
	ObservedAt int64  `gorm:"column:observed_at;not null;index:idx_breadth_evt_trader,sort:desc" json:"observed_at"`
	CreatedAt  int64  `gorm:"column:created_at" json:"created_at"`
}

func (BreadthEvent) TableName() string { return "breadth_events" }

// BreadthEventLeg is the per-position snapshot embedded in BreadthEvent.LegsJSON.
type BreadthEventLeg struct {
	Symbol    string  `json:"symbol"`
	Side      string  `json:"side"`
	Entry     float64 `json:"entry"`
	Mark      float64 `json:"mark"`
	ProfitPct float64 `json:"profit_pct"`
	PeakPct   float64 `json:"peak_pct"`
	Retracing bool    `json:"retracing"`
	ATRPct    float64 `json:"atr_pct"`    // resolved ATR% (0 => ATR unavailable)
	ATRFailed bool    `json:"atr_failed"` // true => ATR could not be resolved this cycle
	Frozen    bool    `json:"frozen"`     // true => ATR came from the frozen open-time value
	Cut       bool    `json:"cut"`        // true => this leg was trimmed
}

type BreadthEventStore struct {
	db *gorm.DB
}

func NewBreadthEventStore(db *gorm.DB) *BreadthEventStore {
	return &BreadthEventStore{db: db}
}

func (s *BreadthEventStore) InitTables() error {
	if s.db.Dialector.Name() == "postgres" {
		var exists int64
		s.db.Raw(`SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'breadth_events'`).Scan(&exists)
		if exists > 0 {
			return nil
		}
	}
	if err := s.db.AutoMigrate(&BreadthEvent{}); err != nil {
		return fmt.Errorf("failed to migrate breadth_events table: %w", err)
	}
	return nil
}

// Record persists a breadth event. Best-effort: callers log-and-continue so the
// guard path is never blocked by a DB write.
func (s *BreadthEventStore) Record(evt *BreadthEvent) error {
	if evt == nil {
		return nil
	}
	return s.db.Create(evt).Error
}

// ListByTrader returns the most recent breadth events for a trader.
func (s *BreadthEventStore) ListByTrader(traderID string, limit int) ([]*BreadthEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []*BreadthEvent
	err := s.db.Where("trader_id = ?", traderID).
		Order("observed_at DESC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list breadth events: %w", err)
	}
	return rows, nil
}
