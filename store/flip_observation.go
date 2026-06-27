package store

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// FlipObservation is a durable record of every trend-reversal flip decision the
// system evaluated to TRUE — whether dry-run (observed only) or live (executed).
// It exists so the dashboard can review live AI reversal-signal quality before
// real execution is enabled fleet-wide. One row per flip decision.
type FlipObservation struct {
	ID            int64   `gorm:"primaryKey;autoIncrement" json:"id"`
	TraderID      string  `gorm:"column:trader_id;not null;index:idx_flip_obs_trader,sort:desc" json:"trader_id"`
	ExchangeID    string  `gorm:"column:exchange_id;default:''" json:"exchange_id"`
	Symbol        string  `gorm:"column:symbol;not null" json:"symbol"`
	FromSide      string  `gorm:"column:from_side;not null" json:"from_side"`   // side being closed (long/short)
	ToSide        string  `gorm:"column:to_side;not null" json:"to_side"`       // reverse side opened (long/short)
	Confidence    int     `gorm:"column:confidence;default:0" json:"confidence"` // AI confidence on the reversal
	AgeHours      float64 `gorm:"column:age_hours;default:0" json:"age_hours"`   // hold age at flip time
	Quantity      float64 `gorm:"column:quantity;default:0" json:"quantity"`
	DecisionCycle int     `gorm:"column:decision_cycle;default:0" json:"decision_cycle"`
	Executed      bool    `gorm:"column:executed;default:false" json:"executed"` // false = dry_run observation
	Reasoning     string  `gorm:"column:reasoning;default:''" json:"reasoning"`  // AI reasoning snapshot
	// Outcome backfill (filled later by review tooling): PnL of the reverse position
	// vs what holding the original would have produced. 0 until evaluated.
	ReverseRealizedPnL float64 `gorm:"column:reverse_realized_pnl;default:0" json:"reverse_realized_pnl"`
	ObservedAt         int64   `gorm:"column:observed_at;not null;index:idx_flip_obs_trader,sort:desc" json:"observed_at"`
	CreatedAt          int64   `gorm:"column:created_at" json:"created_at"`
}

func (FlipObservation) TableName() string { return "flip_observations" }

type FlipObservationStore struct {
	db *gorm.DB
}

func NewFlipObservationStore(db *gorm.DB) *FlipObservationStore {
	return &FlipObservationStore{db: db}
}

func (s *FlipObservationStore) InitTables() error {
	if s.db.Dialector.Name() == "postgres" {
		var exists int64
		s.db.Raw(`SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'flip_observations'`).Scan(&exists)
		if exists > 0 {
			return nil
		}
	}
	if err := s.db.AutoMigrate(&FlipObservation{}); err != nil {
		return fmt.Errorf("failed to migrate flip_observations table: %w", err)
	}
	return nil
}

// Record persists a flip observation. Best-effort: returns error but callers
// typically log-and-continue so the trade path is never blocked.
func (s *FlipObservationStore) Record(obs *FlipObservation) error {
	if obs == nil {
		return nil
	}
	return s.db.Create(obs).Error
}

// ListByTrader returns the most recent flip observations for a trader.
func (s *FlipObservationStore) ListByTrader(traderID string, limit int) ([]*FlipObservation, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []*FlipObservation
	err := s.db.Where("trader_id = ?", traderID).
		Order("observed_at DESC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list flip observations: %w", err)
	}
	return rows, nil
}

// BackfillOutcomes fills reverse_realized_pnl for executed flips whose reverse
// position has since closed. For each executed flip with a zero outcome, it
// locates the reverse position — the CLOSED position on the same symbol/to_side
// opened at/after the flip time — and copies its realized PnL. Idempotent:
// re-running only touches rows still at zero, so it is safe to call on a timer.
// Returns the number of rows updated.
func (s *FlipObservationStore) BackfillOutcomes(traderID string) (int64, error) {
	var pending []*FlipObservation
	err := s.db.Where("trader_id = ? AND executed = ? AND reverse_realized_pnl = 0",
		traderID, true).Find(&pending).Error
	if err != nil {
		return 0, fmt.Errorf("failed to query pending flip outcomes: %w", err)
	}
	var updated int64
	for _, obs := range pending {
		var rev struct {
			RealizedPnL float64
			Status      string
		}
		row := s.db.Table("trader_positions").
			Select("realized_pnl, status").
			Where("trader_id = ? AND symbol = ? AND UPPER(side) = ? AND status = ? AND entry_time >= ?",
				traderID, obs.Symbol, strings.ToUpper(obs.ToSide), "CLOSED", obs.ObservedAt).
			Order("entry_time ASC").Limit(1).Scan(&rev)
		if row.Error != nil || row.RowsAffected == 0 {
			continue // reverse position not closed yet (or not found)
		}
		if err := s.db.Model(&FlipObservation{}).Where("id = ?", obs.ID).
			Update("reverse_realized_pnl", rev.RealizedPnL).Error; err != nil {
			continue
		}
		updated++
	}
	return updated, nil
}
