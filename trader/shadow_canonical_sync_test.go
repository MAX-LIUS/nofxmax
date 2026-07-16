package trader

import (
	"testing"

	"nofx/store"
)

// The store's fixed UI ordering (CanonicalShadowRuleOrder) must exactly match the
// trader package's live rule registry (same names, same order). If someone adds a
// rule to shadowRules but forgets the canonical list, the monitor page would drop
// or misorder it — this test fails loudly instead.
func TestCanonicalOrderMatchesRegistry(t *testing.T) {
	reg := ShadowRuleNames()
	canon := store.CanonicalShadowRuleOrder
	if len(reg) != len(canon) {
		t.Fatalf("rule count mismatch: registry=%d canonical=%d\nregistry=%v\ncanonical=%v", len(reg), len(canon), reg, canon)
	}
	for i := range reg {
		if reg[i] != canon[i] {
			t.Fatalf("order mismatch at %d: registry=%q canonical=%q", i, reg[i], canon[i])
		}
	}
}
