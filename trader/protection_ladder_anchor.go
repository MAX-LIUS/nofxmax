package trader

import (
	"sort"

	"nofx/logger"
)

// anchorLadderTakeProfitToEntry rewrites a ladder take-profit plan so that each
// tier's close size is measured against the ORIGINAL entry quantity, and tiers
// that have already been filled (the position was reduced past their cumulative
// size) are dropped instead of being re-placed.
//
// Background (2026-06-07): ladder TP order quantity was computed as
// currentQuantity * closeRatioPct, and the reconciler re-applied the plan every
// cycle once a filled TP order disappeared. On a position that touched TP1 this
// re-fired TP1 against the shrinking remainder every cycle (e.g. WLD short
// 51→31→19→11→7→3→2), grinding the position into a dust tail instead of closing
// 40% once then 35% at the next tier.
//
// This function leaves the StopLossOrders untouched: a single 100% ladder stop
// must always cover the full remaining position and must never be treated as
// "already executed" just because TP reduced the size.
func anchorLadderTakeProfitToEntry(plan *ProtectionPlan, action string, entryQuantity, currentQuantity float64) {
	if plan == nil || len(plan.TakeProfitOrders) == 0 {
		return
	}
	if entryQuantity <= 0 || currentQuantity <= 0 {
		return
	}
	// No reduction has happened (or the DB entry size is stale/smaller than live):
	// keep the open-time ladder shape and let the normal path size it.
	if entryQuantity <= currentQuantity {
		return
	}

	isLong := action == "open_long"
	isShort := action == "open_short"
	if !isLong && !isShort {
		return
	}

	// Order tiers by fill order: the tier closest to entry fills first.
	// Long TPs sit above entry (lowest price first); short TPs sit below entry
	// (highest price first).
	tiers := make([]ProtectionOrder, len(plan.TakeProfitOrders))
	copy(tiers, plan.TakeProfitOrders)
	sort.SliceStable(tiers, func(i, j int) bool {
		if isLong {
			return tiers[i].Price < tiers[j].Price
		}
		return tiers[i].Price > tiers[j].Price
	})

	closedQty := entryQuantity - currentQuantity
	remainingClosed := closedQty
	kept := make([]ProtectionOrder, 0, len(tiers))

	for _, tier := range tiers {
		if tier.CloseRatioPct <= 0 {
			continue
		}
		tierQty := entryQuantity * tier.CloseRatioPct / 100.0
		filled := remainingClosed
		if filled > tierQty {
			filled = tierQty
		}
		if filled < 0 {
			filled = 0
		}
		remainingClosed -= filled
		unfilled := tierQty - filled

		// Treat the tier as already executed when at least half of it has filled.
		// Mirrors the drawdown native-fill detection tolerance.
		if unfilled <= tierQty*0.5 {
			logger.Infof("  🪜 Ladder TP tier already executed (anchored to entry): price=%.6f ratio=%.1f%% tierQty=%.6f filled=%.6f — not re-placing",
				tier.Price, tier.CloseRatioPct, tierQty, filled)
			continue
		}

		// Re-express the unfilled remainder as a ratio of the CURRENT position so the
		// downstream executor (quantity * ratio) places exactly the unfilled size.
		anchoredRatio := unfilled / currentQuantity * 100.0
		if anchoredRatio > 100 {
			anchoredRatio = 100
		}
		tier.CloseRatioPct = anchoredRatio
		kept = append(kept, tier)
	}

	plan.TakeProfitOrders = kept
	plan.NeedsTakeProfit = len(kept) > 0
	if len(kept) == 0 {
		plan.TakeProfitPrice = 0
		logger.Infof("  🪜 All ladder TP tiers already executed; no take-profit re-applied (entry=%.6f current=%.6f closed=%.6f)",
			entryQuantity, currentQuantity, closedQty)
	} else if len(kept) == 1 {
		plan.TakeProfitPrice = kept[0].Price
	}
}

// nearestLadderTakeProfitPrice returns the take-profit tier that fills first
// (closest to entry): the lowest price for longs, the highest for shorts.
// Used to collapse an all-below-minimum ladder into a single full-position TP.
func nearestLadderTakeProfitPrice(orders []ProtectionOrder, side string) float64 {
	best := 0.0
	for _, o := range orders {
		if o.Price <= 0 {
			continue
		}
		if best == 0 {
			best = o.Price
			continue
		}
		if side == "long" {
			if o.Price < best {
				best = o.Price
			}
		} else {
			if o.Price > best {
				best = o.Price
			}
		}
	}
	return best
}

// tightestLadderStopPrice returns the stop tier closest to current price (the
// tightest protection): the highest stop for longs, the lowest for shorts.
// Used to collapse an all-below-minimum ladder into a single full-position stop.
func tightestLadderStopPrice(orders []ProtectionOrder, side string) float64 {
	best := 0.0
	for _, o := range orders {
		if o.Price <= 0 {
			continue
		}
		if best == 0 {
			best = o.Price
			continue
		}
		if side == "long" {
			if o.Price > best {
				best = o.Price
			}
		} else {
			if o.Price < best {
				best = o.Price
			}
		}
	}
	return best
}
