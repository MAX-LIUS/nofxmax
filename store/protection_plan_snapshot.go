package store

import (
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// ProtectionPlanSnapshot is the canonical, duplicate-free record of the protection
// plan the bot RESOLVED at open time (concrete prices + close ratios per tier),
// captured exactly ONCE per position open — unlike close_intents, which is an
// append-only placement ledger that re-records the same tier on every reconcile /
// trailing re-arm. The position-history panel reads this as the authoritative
// "entry protection plan" instead of reconstructing it from the noisy ledger.
//
// Positions are created asynchronously by order-sync, so (like close_intents) the
// snapshot is keyed by (trader, symbol, side) + snapshot_time ≈ entry_time rather
// than by position id. The panel matches the snapshot whose time is closest to a
// position's entry within a small tolerance.
type ProtectionPlanSnapshot struct {
	ID           int64   `gorm:"primaryKey;autoIncrement" json:"id"`
	TraderID     string  `gorm:"column:trader_id;not null;index:idx_pps_match" json:"trader_id"`
	ExchangeID   string  `gorm:"column:exchange_id;default:''" json:"exchange_id"`
	Symbol       string  `gorm:"column:symbol;not null;index:idx_pps_match" json:"symbol"`
	Side         string  `gorm:"column:side;not null;index:idx_pps_match" json:"side"`
	EntryPrice   float64 `gorm:"column:entry_price;default:0" json:"entry_price"`
	Mode         string  `gorm:"column:mode;default:''" json:"mode"`
	DecisionCycle int    `gorm:"column:decision_cycle;default:0" json:"decision_cycle"`
	// TiersJSON is a JSON array of ProtectionPlanTier in the exact shape the
	// frontend plan renderer consumes (mechanism/kind/label/triggerPct/
	// triggerPrice/closeRatioPct/note).
	TiersJSON    string `gorm:"column:tiers_json;type:text;default:''" json:"tiers_json"`
	SnapshotTime int64  `gorm:"column:snapshot_time;not null;index:idx_pps_match,sort:desc" json:"snapshot_time"`
	CreatedAt    int64  `gorm:"column:created_at" json:"created_at"`
}

func (ProtectionPlanSnapshot) TableName() string { return "protection_plan_snapshots" }

// ProtectionPlanTier is one resolved protection level. Mirrors the frontend
// PlanItem so the API can pass it through without re-derivation.
type ProtectionPlanTier struct {
	Mechanism     string   `json:"mechanism"`
	Kind          string   `json:"kind"` // tp | sl | be | drawdown | trailing
	Label         string   `json:"label"`
	TriggerPct    float64  `json:"triggerPct"`
	TriggerPrice  *float64 `json:"triggerPrice,omitempty"`
	CloseRatioPct *float64 `json:"closeRatioPct,omitempty"`
	Note          string   `json:"note,omitempty"`
}

type ProtectionPlanSnapshotStore struct {
	db *gorm.DB
}

func NewProtectionPlanSnapshotStore(db *gorm.DB) *ProtectionPlanSnapshotStore {
	return &ProtectionPlanSnapshotStore{db: db}
}

func (s *ProtectionPlanSnapshotStore) InitTables() error {
	if s.db.Dialector.Name() == "postgres" {
		var exists int64
		s.db.Raw(`SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'protection_plan_snapshots'`).Scan(&exists)
		if exists > 0 {
			return nil
		}
	}
	if err := s.db.AutoMigrate(&ProtectionPlanSnapshot{}); err != nil {
		return fmt.Errorf("failed to migrate protection_plan_snapshots table: %w", err)
	}
	return nil
}

// Save persists one snapshot for a position open. tiers is serialized to JSON.
// side is normalized to upper-case. Best-effort: a marshal error is returned so
// the caller can log it without failing the open.
func (s *ProtectionPlanSnapshotStore) Save(traderID, exchangeID, symbol, side, mode string, entryPrice float64, decisionCycle int, tiers []ProtectionPlanTier, snapshotTimeMs int64) error {
	if s.db == nil || traderID == "" || len(tiers) == 0 {
		return nil
	}
	blob, err := json.Marshal(tiers)
	if err != nil {
		return fmt.Errorf("marshal protection plan tiers: %w", err)
	}
	row := &ProtectionPlanSnapshot{
		TraderID:      traderID,
		ExchangeID:    exchangeID,
		Symbol:        symbol,
		Side:          strings.ToUpper(side),
		EntryPrice:    entryPrice,
		Mode:          mode,
		DecisionCycle: decisionCycle,
		TiersJSON:     string(blob),
		SnapshotTime:  snapshotTimeMs,
		CreatedAt:     snapshotTimeMs,
	}
	return s.db.Create(row).Error
}

// FindForPosition returns the snapshot whose SnapshotTime is closest to entryTimeMs
// for the given (trader, symbol, side), within toleranceMs. Returns (nil,nil) when
// none matches (legacy positions with no snapshot). This mirrors how close_intents
// are matched to positions by time.
func (s *ProtectionPlanSnapshotStore) FindForPosition(traderID, symbol, side string, entryTimeMs, toleranceMs int64) (*ProtectionPlanSnapshot, error) {
	if s.db == nil || traderID == "" {
		return nil, nil
	}
	lo := entryTimeMs - toleranceMs
	hi := entryTimeMs + toleranceMs
	var rows []ProtectionPlanSnapshot
	if err := s.db.Where("trader_id = ? AND symbol = ? AND side = ? AND snapshot_time BETWEEN ? AND ?",
		traderID, symbol, strings.ToUpper(side), lo, hi).
		Order("snapshot_time asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	best := &rows[0]
	bestDist := absInt64(rows[0].SnapshotTime - entryTimeMs)
	for i := 1; i < len(rows); i++ {
		d := absInt64(rows[i].SnapshotTime - entryTimeMs)
		if d < bestDist {
			best, bestDist = &rows[i], d
		}
	}
	return best, nil
}

// Tiers decodes the stored JSON tier list.
func (r *ProtectionPlanSnapshot) Tiers() ([]ProtectionPlanTier, error) {
	if strings.TrimSpace(r.TiersJSON) == "" {
		return nil, nil
	}
	var tiers []ProtectionPlanTier
	if err := json.Unmarshal([]byte(r.TiersJSON), &tiers); err != nil {
		return nil, err
	}
	return tiers, nil
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
