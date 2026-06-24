package store

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// EquityAdjustmentStore manages equity adjustments (deposits/withdrawals)
type EquityAdjustmentStore struct {
	db *gorm.DB
}

// EquityAdjustment records fund transfers (deposits/withdrawals) that should not affect PnL calculation
type EquityAdjustment struct {
	ID          int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	TraderID    string    `gorm:"column:trader_id;not null;index:idx_adjustment_trader_time" json:"trader_id"`
	Timestamp   time.Time `gorm:"not null;index:idx_adjustment_trader_time,sort:desc" json:"timestamp"`
	Amount      float64   `gorm:"not null" json:"amount"` // Positive for deposit, negative for withdrawal
	Type        string    `gorm:"not null" json:"type"`   // "deposit" or "withdrawal"
	Description string    `gorm:"column:description" json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

func (EquityAdjustment) TableName() string { return "equity_adjustments" }

// NewEquityAdjustmentStore creates a new EquityAdjustmentStore
func NewEquityAdjustmentStore(db *gorm.DB) *EquityAdjustmentStore {
	return &EquityAdjustmentStore{db: db}
}

// initTables initializes equity adjustment table
func (s *EquityAdjustmentStore) initTables() error {
	return s.db.AutoMigrate(&EquityAdjustment{})
}

// Create records a new equity adjustment
func (s *EquityAdjustmentStore) Create(adjustment *EquityAdjustment) error {
	if adjustment.Timestamp.IsZero() {
		adjustment.Timestamp = time.Now().UTC()
	} else {
		adjustment.Timestamp = adjustment.Timestamp.UTC()
	}

	if err := s.db.Omit("ID").Create(adjustment).Error; err != nil {
		return fmt.Errorf("failed to create equity adjustment: %w", err)
	}
	return nil
}

// GetTotalAdjustments returns the sum of all adjustments for a trader
func (s *EquityAdjustmentStore) GetTotalAdjustments(traderID string) (float64, error) {
	var total float64
	err := s.db.Model(&EquityAdjustment{}).
		Where("trader_id = ?", traderID).
		Select("COALESCE(SUM(amount), 0)").
		Scan(&total).Error
	if err != nil {
		return 0, fmt.Errorf("failed to calculate total adjustments: %w", err)
	}
	return total, nil
}

// GetByTrader returns all adjustments for a trader
func (s *EquityAdjustmentStore) GetByTrader(traderID string) ([]*EquityAdjustment, error) {
	var adjustments []*EquityAdjustment
	err := s.db.Where("trader_id = ?", traderID).
		Order("timestamp DESC").
		Find(&adjustments).Error
	if err != nil {
		return nil, fmt.Errorf("failed to query adjustments: %w", err)
	}
	return adjustments, nil
}

// GetByTimeRange returns adjustments within a time range
func (s *EquityAdjustmentStore) GetByTimeRange(traderID string, start, end time.Time) ([]*EquityAdjustment, error) {
	var adjustments []*EquityAdjustment
	err := s.db.Where("trader_id = ? AND timestamp >= ? AND timestamp <= ?", traderID, start, end).
		Order("timestamp ASC").
		Find(&adjustments).Error
	if err != nil {
		return nil, fmt.Errorf("failed to query adjustments by time range: %w", err)
	}
	return adjustments, nil
}

// Delete removes an adjustment record
func (s *EquityAdjustmentStore) Delete(id int64) error {
	result := s.db.Delete(&EquityAdjustment{}, id)
	if result.Error != nil {
		return fmt.Errorf("failed to delete adjustment: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("adjustment not found")
	}
	return nil
}
