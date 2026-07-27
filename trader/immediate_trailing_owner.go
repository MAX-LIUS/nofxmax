package trader

import (
	"fmt"
	"strings"

	"nofx/logger"
)

// ---------------------------------------------------------------------------
// Immediate-trailing ownership registry
// ---------------------------------------------------------------------------
//
// THE RULE THIS FILE EXISTS TO ENFORCE: every trailing order resting on an
// exchange must have exactly one PERSISTED owner, and a matcher may only claim
// an order it can prove is its own.
//
// Before this file, that rule held for drawdown tiers (their owner is a
// dynamic-protection record in the DB) but NOT for the immediate trailing order
// placed at open by placeImmediateTrailing. Its order ID lived only in the
// in-memory map immediateTrailingIDs, which produced two defects:
//
//  1. LEAK — the ID is lost on restart, so cancelImmediateTrailing returns
//     early ("" ID) and the 50% order rests on the exchange forever. Nothing
//     else knows it exists, so nothing ever cancels it.
//
//  2. MIS-ADOPTION (the dangerous one) — because the order has no persisted
//     owner it never appears in claimedTrailingOrderIDsForPosition, so the
//     full-close tier's positional fallback ("the first unclaimed TRAILING
//     order on this side is mine") can adopt it. The tier then reports itself
//     covered while the resting order closes only 50% of the position. Same
//     failure shape as v1.16.8/v1.16.10, one more unowned order away.
//
// Observed 2026-07-27 on Binance BN CLUSDT: three trailing orders on one short
// — 2.43 (dd1, owned), 0.73 (30% partial, owned), 1.22 (=50%, order
// 2000001313247150, UNOWNED). Same shape on OKX ETHUSDT order
// 3779472770300542976 (0.120 of 0.239), which appears zero times in the
// protection blob.
//
// Tag-based identification is not an option: okxReasonTag builds
// "<brokerTag>_<reason>" and truncates to 16 chars, but okxTag is already 16
// chars, so the reason is always sliced off — every OKX protection order
// carries an identical tag. Ownership therefore has to be persisted by us.
const immediateTrailingProtectionType = "immediate_trailing"

// fullTrailingAdoptionMinCoverage is the minimum fraction of the current position an
// UNOWNED trailing order must cover before a full-close (100%) tier may adopt it as
// its own protection. Below this the tier reports itself missing and arms its own
// order instead of inheriting partial coverage.
//
// 0.95 rather than 1.0 leaves room for exchange lot-size rounding (a 380-unit order
// against a 380.0001-unit position must still count as full). The leaked immediate
// trailing order sits at 0.50, far below the threshold.
const fullTrailingAdoptionMinCoverage = 0.95

// immediateTrailingRuleFingerprint builds the pseudo rule fingerprint used for the
// immediate trailing owner record. Only the FIRST field is load-bearing:
// persistDynamicProtectionRecordWithDetails splits on "|" and uses parts[0] as the
// entry price when composing the record's PositionFingerprint.
func immediateTrailingRuleFingerprint(entryPrice float64) string {
	return fmt.Sprintf("%.8f|0.00000000|%s", entryPrice, immediateTrailingProtectionType)
}

// persistImmediateTrailingRecord records the immediate trailing order as an owned
// protection so it survives a restart and is visible to every matcher's claimed set.
func (at *AutoTrader) persistImmediateTrailingRecord(symbol, side string, entryPrice, activationPrice, callbackRatio, quantity float64, orderID string) {
	if orderID == "" {
		return
	}
	at.persistDynamicProtectionRecordWithDetails(
		symbol, side,
		immediateTrailingProtectionType,
		immediateTrailingRuleFingerprint(entryPrice),
		50, // the order closes 50% of the position by construction
		"armed",
		orderID,
		activationPrice, callbackRatio, quantity,
	)
}

// latestArmedImmediateTrailingRecord returns the most recent armed immediate-trailing
// owner record for this position, or nil.
//
// Position identity is deliberately NOT checked. A record left over from a previous
// position points at an order ID that is already dead, so the worst case is a cancel
// call that the exchange rejects (logged, harmless) or a dead ID sitting in a claimed
// set (matches no live order). Requiring identity would instead re-open the leak for
// exactly the case that matters — a restart, where the in-memory hint is gone.
func (at *AutoTrader) latestArmedImmediateTrailingRecord(symbol, side string) *struct {
	OrderID   string
	Key       string
	UpdatedAt int64
} {
	if at.store == nil {
		return nil
	}
	state, err := at.store.LoadDynamicProtectionState()
	if err != nil || state == nil {
		return nil
	}
	var best *struct {
		OrderID   string
		Key       string
		UpdatedAt int64
	}
	for key, record := range state.Records {
		if record.TraderID != "" && record.TraderID != at.id {
			continue
		}
		if record.ProtectionType != immediateTrailingProtectionType || record.Status != "armed" {
			continue
		}
		if !strings.EqualFold(record.Symbol, symbol) || !strings.EqualFold(record.Side, side) {
			continue
		}
		if record.ExchangeOrderID == "" {
			continue
		}
		if best == nil || record.UpdatedAt > best.UpdatedAt {
			best = &struct {
				OrderID   string
				Key       string
				UpdatedAt int64
			}{OrderID: record.ExchangeOrderID, Key: key, UpdatedAt: record.UpdatedAt}
		}
	}
	return best
}

// persistedImmediateTrailingOrderID returns the order ID of the immediate trailing
// order according to the persisted registry. Used as the restart-safe fallback for
// the in-memory hint.
func (at *AutoTrader) persistedImmediateTrailingOrderID(symbol, side string) string {
	if rec := at.latestArmedImmediateTrailingRecord(symbol, side); rec != nil {
		return rec.OrderID
	}
	return ""
}

// markImmediateTrailingRecordCleared retires the owner record once the order is gone,
// so a later poll does not keep trying to cancel an order that no longer exists.
func (at *AutoTrader) markImmediateTrailingRecordCleared(symbol, side, orderID string) {
	if at.store == nil || orderID == "" {
		return
	}
	state, err := at.store.LoadDynamicProtectionState()
	if err != nil || state == nil {
		return
	}
	for _, record := range state.Records {
		if record.TraderID != "" && record.TraderID != at.id {
			continue
		}
		if record.ProtectionType != immediateTrailingProtectionType || record.Status != "armed" {
			continue
		}
		if record.ExchangeOrderID != orderID {
			continue
		}
		record.Status = "cleared"
		if err := at.store.SaveDynamicProtectionRecord(record); err != nil {
			logger.Warnf("⚠️ Immediate trailing: failed to clear owner record for %s %s (orderID=%s): %v", symbol, side, orderID, err)
		}
		return
	}
}

// immediateTrailingClaimedIDs returns the immediate-trailing order IDs owned for this
// position — both the in-memory hint and the persisted registry, because a restart
// leaves only the latter and a fresh placement briefly only the former.
//
// This is unioned into claimedTrailingOrderIDsForPosition so that EVERY matcher that
// consults the claimed set (findExistingFullTrailingOrder,
// hasMatchingNativeTrailingOrderForRule, findPartialTrailingReplacementCandidate, and
// the collapse/cancel claim logic) is fixed by one change rather than four.
func (at *AutoTrader) immediateTrailingClaimedIDs(symbol, side string) []string {
	ids := make([]string, 0, 2)
	if id := at.getImmediateTrailingOrderID(symbol, side); id != "" {
		ids = append(ids, id)
	}
	if id := at.persistedImmediateTrailingOrderID(symbol, side); id != "" && (len(ids) == 0 || ids[0] != id) {
		ids = append(ids, id)
	}
	return ids
}

// immediateTrailingOrderAbsent reports true ONLY on positive evidence that the order is
// no longer on the exchange. A failed order query returns false ("unknown"), never true:
// treating an API error as absence would retire the owner record for an order that is
// still resting, which is precisely the unowned-order state this file exists to prevent.
func (at *AutoTrader) immediateTrailingOrderAbsent(symbol, orderID string) bool {
	if orderID == "" {
		return true
	}
	if at.trader == nil {
		return false
	}
	orders, err := at.trader.GetOpenOrders(symbol)
	if err != nil {
		return false
	}
	for _, o := range orders {
		if o.OrderID == orderID {
			return false
		}
	}
	return true
}
