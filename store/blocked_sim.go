package store

import (
	"fmt"

	"gorm.io/gorm"
)

// BlockedSimOutcome records an open that an ENFORCE-mode regime gate blocked, plus
// the simulated lifecycle outcome of the trade we DIDN'T take. This restores the
// counterfactual that enforcing would otherwise destroy: the blocked signal is
// paper-traded through the trader's real protection ladder (via the backtest
// engine, validated to ~2% PnL fidelity), so we can measure whether the block was
// right (sim loss = good block) or wrong (sim profit = missed winner).
//
// SimStatus lifecycle: "pending" (captured, not yet replayed) -> "done" (sim_r
// set) | "skipped" (no bars / no stop / data gap).
type BlockedSimOutcome struct {
	ID       int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	TraderID string `gorm:"column:trader_id;not null;index:idx_blocked_trader" json:"trader_id"`
	Cycle    int64  `gorm:"column:cycle;default:0" json:"cycle"`
	Symbol   string `gorm:"column:symbol;not null" json:"symbol"`
	Side     string `gorm:"column:side;not null" json:"side"` // LONG / SHORT

	// Intent snapshot — the open the gate blocked, captured at decision time.
	EntryPrice float64 `gorm:"column:entry_price;default:0" json:"entry_price"`
	StopLoss   float64 `gorm:"column:stop_loss;default:0" json:"stop_loss"`
	TakeProfit float64 `gorm:"column:take_profit;default:0" json:"take_profit"`
	Quantity   float64 `gorm:"column:quantity;default:0" json:"quantity"`
	Confidence float64 `gorm:"column:confidence;default:0" json:"confidence"`
	Leverage   int     `gorm:"column:leverage;default:0" json:"leverage"`

	// BlockedBy is the comma-joined gate categories that enforced the block.
	BlockedBy string `gorm:"column:blocked_by;default:''" json:"blocked_by"`

	// Simulated outcome (filled by the replay pass).
	SimStatus   string  `gorm:"column:sim_status;default:'pending';index:idx_blocked_status" json:"sim_status"`
	SimExit     float64 `gorm:"column:sim_exit;default:0" json:"sim_exit"`
	SimPnL      float64 `gorm:"column:sim_pnl;default:0" json:"sim_pnl"`
	SimR        float64 `gorm:"column:sim_r;default:0" json:"sim_r"`
	SimReason   string  `gorm:"column:sim_reason;default:''" json:"sim_reason"`
	SimBarsHeld int     `gorm:"column:sim_bars_held;default:0" json:"sim_bars_held"`

	ObservedAt int64 `gorm:"column:observed_at;not null;index:idx_blocked_trader" json:"observed_at"`
	SimAt      int64 `gorm:"column:sim_at;default:0" json:"sim_at"`
	CreatedAt  int64 `gorm:"column:created_at" json:"created_at"`
}

func (BlockedSimOutcome) TableName() string { return "blocked_sim_outcomes" }

type BlockedSimStore struct {
	db *gorm.DB
}

func NewBlockedSimStore(db *gorm.DB) *BlockedSimStore { return &BlockedSimStore{db: db} }

func (s *BlockedSimStore) InitTables() error {
	if s.db.Dialector.Name() == "postgres" {
		var exists int64
		s.db.Raw(`SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'blocked_sim_outcomes'`).Scan(&exists)
		if exists > 0 {
			return nil
		}
	}
	if err := s.db.AutoMigrate(&BlockedSimOutcome{}); err != nil {
		return fmt.Errorf("failed to migrate blocked_sim_outcomes table: %w", err)
	}
	return nil
}

// Record persists one blocked-open intent (best-effort; never blocks live path).
func (s *BlockedSimStore) Record(o *BlockedSimOutcome) error {
	return s.db.Create(o).Error
}

// PendingForReplay returns captured intents not yet simulated, oldest first.
func (s *BlockedSimStore) PendingForReplay(limit int) ([]BlockedSimOutcome, error) {
	var out []BlockedSimOutcome
	q := s.db.Where("sim_status = ?", "pending").Order("observed_at ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	return out, q.Find(&out).Error
}

// UpdateSim writes the simulated outcome back onto a captured intent.
func (s *BlockedSimStore) UpdateSim(id int64, fields map[string]interface{}) error {
	return s.db.Model(&BlockedSimOutcome{}).Where("id = ?", id).Updates(fields).Error
}
