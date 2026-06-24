package trader

import (
	"sync"

	"nofx/logger"
)

// Account exclusivity registry (fix 2026-06-22).
//
// The exchange runs one-way (net) mode: a single OKX account holds ONE net
// position per (symbol, side). If two AutoTrader instances are bound to the SAME
// exchange account (same exchangeID) and both run their protection reconciler /
// drawdown monitor, they each see the shared net position and fight over its
// protection orders — repeatedly placing and cancelling each other's TP/SL legs
// (the churn pattern), and potentially mis-closing positions they did not open.
//
// This registry enforces that at most ONE active instance owns an exchangeID's
// protection lifecycle. The first instance to claim wins; later instances for the
// same account are denied protection ownership (they can still observe, but must
// not run reconciler/drawdown writes against the shared account).
var (
	accountProtectionOwners   = make(map[string]string) // exchangeID -> owning trader id
	accountProtectionOwnersMu sync.Mutex
)

// claimAccountProtectionOwnership attempts to make this trader the sole protection
// owner of its exchange account. Returns true if granted (or already held by this
// same trader), false if another active trader already owns the account.
func claimAccountProtectionOwnership(exchangeID, traderID string) bool {
	if exchangeID == "" {
		// No account scoping available; do not block (single-account/legacy path).
		return true
	}
	accountProtectionOwnersMu.Lock()
	defer accountProtectionOwnersMu.Unlock()
	owner, exists := accountProtectionOwners[exchangeID]
	if !exists {
		accountProtectionOwners[exchangeID] = traderID
		return true
	}
	if owner == traderID {
		return true
	}
	logger.Warnf("🛑 Account exclusivity: exchange %s already owned by trader %s; trader %s is DENIED protection ownership (shared-account conflict prevented)", exchangeID, owner, traderID)
	return false
}

// releaseAccountProtectionOwnership relinquishes ownership if this trader holds it,
// allowing a future instance on the same account to take over (e.g. after restart).
func releaseAccountProtectionOwnership(exchangeID, traderID string) {
	if exchangeID == "" {
		return
	}
	accountProtectionOwnersMu.Lock()
	defer accountProtectionOwnersMu.Unlock()
	if accountProtectionOwners[exchangeID] == traderID {
		delete(accountProtectionOwners, exchangeID)
	}
}
