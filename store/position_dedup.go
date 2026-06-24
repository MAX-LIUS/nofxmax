package store

import (
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"

	"nofx/logger"
)

// MergeResult summarizes a single (trader, symbol, side) net-position merge.
type MergeResult struct {
	TraderID      string
	Symbol        string
	Side          string
	KeptID        int64
	MergedIDs     []int64
	NetQuantity   float64
	NetEntryQty   float64
	WeightedEntry float64
}

// MergeDuplicateOpenPositionsForKey collapses every OPEN row for a single
// (traderID, symbol, side) into one net-position row, matching the exchange's
// one-way (net) position model.
//
// Background (2026-06-22): a sync race could let PositionBuilder.handleOpen miss
// an existing OPEN row (e.g. it was briefly marked sync-absent) and create a
// SECOND OPEN row for the same symbol/side. The protection reconciler then sized
// ladder tiers from GetOpenPositionBySymbol (which returns only the newest row's
// entry_quantity) while the exchange held the merged net position — so plan TP
// prices never matched the exchange orders and the reconciler churned forever.
//
// Merge rules (deterministic):
//   - Primary row = earliest entry_time (the original net position).
//   - Quantity / EntryQuantity / Fee / RealizedPnL are summed.
//   - EntryPrice becomes the quantity-weighted average across rows.
//   - EntryTime/EntryDecisionCycle/EntryOrderID stay from the primary row.
//   - Secondary rows are marked CLOSED with reason "merged_into_net_position"
//     and zeroed quantity so they never reappear as open net legs.
//
// When dryRun is true no writes happen; the planned MergeResult is still returned.
func (s *PositionStore) MergeDuplicateOpenPositionsForKey(traderID, symbol, side string, dryRun bool) (*MergeResult, error) {
	var rows []TraderPosition
	if err := s.db.Where("trader_id = ? AND symbol = ? AND side = ? AND status = ?", traderID, symbol, side, "OPEN").
		Order("entry_time ASC, id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("query open rows: %w", err)
	}
	if len(rows) <= 1 {
		return nil, nil // nothing to merge
	}

	primary := rows[0]
	var (
		sumQty      float64
		sumEntryQty float64
		sumFee      float64
		sumPnL      float64
		weightedNum float64 // sum(entryPrice * quantity)
		weightedDen float64 // sum(quantity)
		mergedIDs   []int64
	)
	for _, r := range rows {
		q := r.Quantity
		eq := r.EntryQuantity
		if eq == 0 {
			eq = r.Quantity
		}
		sumQty += q
		sumEntryQty += eq
		sumFee += r.Fee
		sumPnL += r.RealizedPnL
		if r.EntryPrice > 0 && q > 0 {
			weightedNum += r.EntryPrice * q
			weightedDen += q
		}
		if r.ID != primary.ID {
			mergedIDs = append(mergedIDs, r.ID)
		}
	}

	weightedEntry := primary.EntryPrice
	if weightedDen > 0 {
		weightedEntry = adaptivePriceRound(weightedNum/weightedDen, primary.EntryPrice)
	}
	sumQty = math.Round(sumQty*100000000) / 100000000
	sumEntryQty = math.Round(sumEntryQty*100000000) / 100000000

	result := &MergeResult{
		TraderID:      traderID,
		Symbol:        symbol,
		Side:          side,
		KeptID:        primary.ID,
		MergedIDs:     mergedIDs,
		NetQuantity:   sumQty,
		NetEntryQty:   sumEntryQty,
		WeightedEntry: weightedEntry,
	}

	if dryRun {
		logger.Infof("  [dry-run] merge %s %s %s: keep id=%d, fold %v -> netQty=%.8f entryQty=%.8f wEntry=%.8f",
			traderID, symbol, side, primary.ID, mergedIDs, sumQty, sumEntryQty, weightedEntry)
		return result, nil
	}

	nowMs := time.Now().UTC().UnixMilli()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&TraderPosition{}).Where("id = ?", primary.ID).Updates(map[string]interface{}{
			"quantity":       sumQty,
			"entry_quantity": sumEntryQty,
			"entry_price":    weightedEntry,
			"fee":            sumFee,
			"realized_pnl":   sumPnL,
			"updated_at":     nowMs,
		}).Error; err != nil {
			return fmt.Errorf("update primary row %d: %w", primary.ID, err)
		}
		for _, id := range mergedIDs {
			if err := tx.Model(&TraderPosition{}).Where("id = ?", id).Updates(map[string]interface{}{
				"status":       "CLOSED",
				"quantity":     0,
				"close_reason": "merged_into_net_position",
				"exit_time":    nowMs,
				"updated_at":   nowMs,
			}).Error; err != nil {
				return fmt.Errorf("retire merged row %d: %w", id, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	logger.Infof("  Merged %s %s %s into net position id=%d (folded %v): netQty=%.8f entryQty=%.8f wEntry=%.8f",
		traderID, symbol, side, primary.ID, mergedIDs, sumQty, sumEntryQty, weightedEntry)
	return result, nil
}

// FindDuplicateOpenPositionKeys returns every (traderID, symbol, side) that
// currently has more than one OPEN row — the candidates for net-position merge.
func (s *PositionStore) FindDuplicateOpenPositionKeys() ([]MergeResult, error) {
	type keyRow struct {
		TraderID string
		Symbol   string
		Side     string
		N        int
	}
	var keys []keyRow
	if err := s.db.Model(&TraderPosition{}).
		Select("trader_id, symbol, side, COUNT(*) AS n").
		Where("status = ?", "OPEN").
		Group("trader_id, symbol, side").
		Having("COUNT(*) > 1").
		Scan(&keys).Error; err != nil {
		return nil, fmt.Errorf("scan duplicate keys: %w", err)
	}
	out := make([]MergeResult, 0, len(keys))
	for _, k := range keys {
		out = append(out, MergeResult{TraderID: k.TraderID, Symbol: k.Symbol, Side: k.Side})
	}
	return out, nil
}
