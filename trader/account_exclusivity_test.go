package trader

import "testing"

// First claimer of an exchange account wins; a second distinct trader on the same
// account is denied; releasing lets a new instance take over.
func TestAccountProtectionExclusivity(t *testing.T) {
	ex := "acct-test-EX1"
	a := "trader-A"
	b := "trader-B"

	// Clean any leftover state for this exchange key.
	releaseAccountProtectionOwnership(ex, a)
	releaseAccountProtectionOwnership(ex, b)

	if !claimAccountProtectionOwnership(ex, a) {
		t.Fatal("first claimer A should be granted ownership")
	}
	// Idempotent re-claim by the same owner.
	if !claimAccountProtectionOwnership(ex, a) {
		t.Fatal("re-claim by same owner A should still be granted")
	}
	// Different trader on the SAME account must be denied.
	if claimAccountProtectionOwnership(ex, b) {
		t.Fatal("second trader B on the same account must be denied")
	}
	// After A releases, B can take over.
	releaseAccountProtectionOwnership(ex, a)
	if !claimAccountProtectionOwnership(ex, b) {
		t.Fatal("after A released, B should be granted ownership")
	}
	releaseAccountProtectionOwnership(ex, b)
}

// Empty exchangeID must never block (legacy/single-account path).
func TestAccountProtectionExclusivity_EmptyExchangeNeverBlocks(t *testing.T) {
	if !claimAccountProtectionOwnership("", "trader-X") {
		t.Fatal("empty exchangeID must not block ownership")
	}
	if !claimAccountProtectionOwnership("", "trader-Y") {
		t.Fatal("empty exchangeID must not block a second trader either")
	}
}

// A non-owner release must not steal ownership from the real owner.
func TestAccountProtectionExclusivity_ReleaseByNonOwnerNoop(t *testing.T) {
	ex := "acct-test-EX2"
	owner := "owner"
	other := "other"
	releaseAccountProtectionOwnership(ex, owner)

	if !claimAccountProtectionOwnership(ex, owner) {
		t.Fatal("owner should be granted")
	}
	// Non-owner release is a no-op.
	releaseAccountProtectionOwnership(ex, other)
	// Owner still holds it, so a third party is still denied.
	if claimAccountProtectionOwnership(ex, other) {
		t.Fatal("non-owner release must not free the account; other should still be denied")
	}
	releaseAccountProtectionOwnership(ex, owner)
}
