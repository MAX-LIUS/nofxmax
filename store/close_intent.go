package store

import (
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// CloseIntent is a durable record of a system-initiated close decision, written
// at the moment the trader decides to close (AI exit, breadth breaker, managed
// drawdown, time stop, trailing take-profit, manual, replacement). It exists so
// the asynchronous OKX fill-sync can attribute the resulting close fill to the
// real mechanism instead of the bare close_long/close_short canonical action.
//
// Matching priority used by the sync path:
//  1. exchange order id (deterministic) when the close order result carried one
//  2. trader+symbol+side within a small time window around the fill (fallback)
type CloseIntent struct {
	ID              int64   `gorm:"primaryKey;autoIncrement" json:"id"`
	TraderID        string  `gorm:"column:trader_id;not null;index:idx_close_intents_match" json:"trader_id"`
	ExchangeID      string  `gorm:"column:exchange_id;default:''" json:"exchange_id"`
	Symbol          string  `gorm:"column:symbol;not null;index:idx_close_intents_match" json:"symbol"`
	Side            string  `gorm:"column:side;not null;index:idx_close_intents_match" json:"side"`
	Quantity        float64 `gorm:"column:quantity;default:0" json:"quantity"`
	Reason          string  `gorm:"column:reason;not null" json:"reason"`
	DecisionCycle   int     `gorm:"column:decision_cycle;default:0" json:"decision_cycle"`
	ExchangeOrderID string  `gorm:"column:exchange_order_id;default:'';index:idx_close_intents_order" json:"exchange_order_id"`
	IntentTime      int64   `gorm:"column:intent_time;not null;index:idx_close_intents_match,sort:desc" json:"intent_time"`
	Consumed        bool    `gorm:"column:consumed;default:false;index:idx_close_intents_match" json:"consumed"`
	ConsumedAt      int64   `gorm:"column:consumed_at;default:0" json:"consumed_at"`
	CreatedAt       int64   `gorm:"column:created_at" json:"created_at"`
}

func (CloseIntent) TableName() string { return "close_intents" }

type CloseIntentStore struct {
	db *gorm.DB
}

func NewCloseIntentStore(db *gorm.DB) *CloseIntentStore {
	return &CloseIntentStore{db: db}
}

func (s *CloseIntentStore) InitTables() error {
	if s.db.Dialector.Name() == "postgres" {
		var exists int64
		s.db.Raw(`SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'close_intents'`).Scan(&exists)
		if exists > 0 {
			return nil
		}
	}
	if err := s.db.AutoMigrate(&CloseIntent{}); err != nil {
		return fmt.Errorf("failed to migrate close_intents table: %w", err)
	}
	return nil
}

// Record persists a new close intent. side is normalized to upper-case LONG/SHORT.
// reason must be a canonical mechanism reason (e.g. ai_close_long, managed_drawdown,
// time_stop, trailing_take_profit, breadth_breaker, manual_close_long).
func (s *CloseIntentStore) Record(traderID, exchangeID, symbol, side, reason string, quantity float64, decisionCycle int, exchangeOrderID string) error {
	if s.db == nil || traderID == "" || symbol == "" || reason == "" {
		return nil
	}
	now := time.Now().UTC().UnixMilli()
	intent := &CloseIntent{
		TraderID:        traderID,
		ExchangeID:      exchangeID,
		Symbol:          symbol,
		Side:            strings.ToUpper(side),
		Quantity:        quantity,
		Reason:          reason,
		DecisionCycle:   decisionCycle,
		ExchangeOrderID: strings.TrimSpace(exchangeOrderID),
		IntentTime:      now,
		CreatedAt:       now,
	}
	return s.db.Create(intent).Error
}

// MatchByOrderIDAndConsume resolves the intent whose exchange order id matches
// exactly. This is the fully-deterministic path: a system market close records
// its order id, and the resulting fill(s) carry that same order id.
//
// A single close order can produce MANY fills (OKX splits a market close across
// price levels). Every one of those fills must resolve to the same reason, so
// the lookup is intentionally NOT gated on consumed: it matches the order id
// regardless of consumed state and returns the same reason for each fill. The
// consumed flag is still set (idempotently) so the time-window fallback never
// re-grabs this intent and pruning can reclaim it. Returns nil when no intent
// has that order id.
func (s *CloseIntentStore) MatchByOrderIDAndConsume(traderID, exchangeOrderID string) (*CloseIntent, error) {
	if s.db == nil || traderID == "" || strings.TrimSpace(exchangeOrderID) == "" {
		return nil, nil
	}
	var intent CloseIntent
	err := s.db.Where("trader_id = ? AND exchange_order_id = ?", traderID, strings.TrimSpace(exchangeOrderID)).
		Order("intent_time DESC").First(&intent).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	if !intent.Consumed {
		now := time.Now().UTC().UnixMilli()
		if err := s.db.Model(&CloseIntent{}).Where("id = ? AND consumed = ?", intent.ID, false).
			Updates(map[string]interface{}{"consumed": true, "consumed_at": now}).Error; err != nil {
			return nil, err
		}
		intent.Consumed = true
		intent.ConsumedAt = now
	}
	return &intent, nil
}

// MatchByWindowAndConsume consumes the newest unconsumed intent for
// trader+symbol+side whose intent_time is within +/- windowMs of fillTimeMs.
// This is the last-resort fallback used only when no order-id/tag/price signal
// resolved the mechanism. Returns nil when nothing matches.
func (s *CloseIntentStore) MatchByWindowAndConsume(traderID, symbol, side string, fillTimeMs, windowMs int64) (*CloseIntent, error) {
	if s.db == nil || traderID == "" {
		return nil, nil
	}
	if windowMs <= 0 {
		windowMs = 5 * 60 * 1000
	}
	var intent CloseIntent
	err := s.db.Where("trader_id = ? AND symbol = ? AND side = ? AND consumed = ? AND intent_time BETWEEN ? AND ?",
		traderID, symbol, strings.ToUpper(side), false, fillTimeMs-windowMs, fillTimeMs+windowMs).
		Order("intent_time DESC").First(&intent).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	now := time.Now().UTC().UnixMilli()
	if err := s.db.Model(&CloseIntent{}).Where("id = ? AND consumed = ?", intent.ID, false).
		Updates(map[string]interface{}{"consumed": true, "consumed_at": now}).Error; err != nil {
		return nil, err
	}
	intent.Consumed = true
	intent.ConsumedAt = now
	return &intent, nil
}

// PruneConsumed deletes consumed intents older than the cutoff to keep the table
// bounded. Returns rows deleted.
func (s *CloseIntentStore) PruneConsumed(olderThanMs int64) (int64, error) {
	if s.db == nil {
		return 0, nil
	}
	res := s.db.Where("consumed = ? AND consumed_at > 0 AND consumed_at < ?", true, olderThanMs).
		Delete(&CloseIntent{})
	return res.RowsAffected, res.Error
}
