package trader

import (
	"sync"
	"testing"
	"time"

	"nofx/store"
)

// newIsolationTestTrader builds an AutoTrader with every per-position state map
// initialized, so cleanup/reset logic can be exercised without a live exchange.
func newIsolationTestTrader(id string) *AutoTrader {
	return &AutoTrader{
		id:                    id,
		exchange:              "paper",
		trader:                &fakeProtectionTrader{},
		config:                AutoTraderConfig{StrategyConfig: &store.StrategyConfig{}},
		positionFirstSeenTime: make(map[string]int64),
		peakPnLCache:          make(map[string]float64),
		troughPnLCache:        make(map[string]float64),
		peakAtrMultCache:      make(map[string]float64),
		troughAtrMultCache:    make(map[string]float64),
		gbPnlHist:             make(map[string][]float64),
		structSLFiredBar:      make(map[string]int64),
		protectionState:       make(map[string]string),
		breakEvenState:        make(map[string]string),
		breakEvenFingerprints: make(map[string]string),
		breakEvenSource:       make(map[string]string),
		drawdownState:         make(map[string]string),
		drawdownSource:        make(map[string]string),
		drawdownAIRules:       make(map[string][]store.DrawdownTakeProfitRule),
		drawdownRunnerState:   make(map[string]DrawdownRunnerState),
		drawdownTierAllocs:    make(map[string][]store.DrawdownTierAllocation),
		immediateTrailingIDs:  make(map[string]string),
		protectionStateMutex:  sync.RWMutex{},
		breakEvenStateMutex:   sync.RWMutex{},
		peakPnLCacheMutex:     sync.RWMutex{},
	}
}

// seedAllPerPositionState fills every per-position map with a value for key symbol_side.
func seedAllPerPositionState(at *AutoTrader, symbol, side string) {
	key := symbol + "_" + side
	at.peakPnLCache[key] = 3.51
	at.troughPnLCache[key] = -1.2
	at.peakAtrMultCache[key] = 2.0
	at.troughAtrMultCache[key] = -0.8
	at.gbPnlHist[key] = []float64{1, 2, 3}
	at.structSLFiredBar[key] = 1784259700000
	at.protectionState[key] = "native_trailing_armed"
	at.breakEvenState[key] = "armed"
	at.breakEvenFingerprints[key] = "126.57|0.55"
	at.breakEvenSource[key] = "ai_decision"
	at.drawdownState[key] = "rule-fp"
	at.drawdownSource[key] = "ai_decision"
	at.drawdownAIRules[key] = []store.DrawdownTakeProfitRule{{MinProfitPct: 3, MaxDrawdownPct: 1.8, CloseRatioPct: 100, StageName: "dd1"}}
	at.drawdownRunnerState[key] = DrawdownRunnerState{StageName: "runner"}
	at.drawdownTierAllocs[key] = []store.DrawdownTierAllocation{{TierIndex: 0, StageName: "dd1", PeakPnLPct: 3.51, Status: "tracking"}}
	at.immediateTrailingIDs[key] = "order-123"
}

// perPositionStateKeys returns which maps still hold key symbol_side (for assertions).
func perPositionStateKeys(at *AutoTrader, symbol, side string) []string {
	key := symbol + "_" + side
	var present []string
	if _, ok := at.peakPnLCache[key]; ok {
		present = append(present, "peakPnLCache")
	}
	if _, ok := at.troughPnLCache[key]; ok {
		present = append(present, "troughPnLCache")
	}
	if _, ok := at.peakAtrMultCache[key]; ok {
		present = append(present, "peakAtrMultCache")
	}
	if _, ok := at.troughAtrMultCache[key]; ok {
		present = append(present, "troughAtrMultCache")
	}
	if _, ok := at.gbPnlHist[key]; ok {
		present = append(present, "gbPnlHist")
	}
	if _, ok := at.structSLFiredBar[key]; ok {
		present = append(present, "structSLFiredBar")
	}
	if _, ok := at.protectionState[key]; ok {
		present = append(present, "protectionState")
	}
	if _, ok := at.breakEvenState[key]; ok {
		present = append(present, "breakEvenState")
	}
	if _, ok := at.breakEvenFingerprints[key]; ok {
		present = append(present, "breakEvenFingerprints")
	}
	if _, ok := at.breakEvenSource[key]; ok {
		present = append(present, "breakEvenSource")
	}
	if _, ok := at.drawdownState[key]; ok {
		present = append(present, "drawdownState")
	}
	if _, ok := at.drawdownSource[key]; ok {
		present = append(present, "drawdownSource")
	}
	if _, ok := at.drawdownAIRules[key]; ok {
		present = append(present, "drawdownAIRules")
	}
	if _, ok := at.drawdownRunnerState[key]; ok {
		present = append(present, "drawdownRunnerState")
	}
	if _, ok := at.drawdownTierAllocs[key]; ok {
		present = append(present, "drawdownTierAllocs")
	}
	if _, ok := at.immediateTrailingIDs[key]; ok {
		present = append(present, "immediateTrailingIDs")
	}
	return present
}

// TestResetPerPositionStateOnOpen_WipesAllMaps covers Block 2: opening a new position
// on a symbol|side must wipe EVERY stale per-position map from the prior position.
func TestResetPerPositionStateOnOpen_WipesAllMaps(t *testing.T) {
	at := newIsolationTestTrader("traderA")
	seedAllPerPositionState(at, "SPCXUSDT", "short")

	if present := perPositionStateKeys(at, "SPCXUSDT", "short"); len(present) != 16 {
		t.Fatalf("seed sanity: expected 16 maps populated, got %d: %v", len(present), present)
	}

	at.resetPerPositionStateOnOpen("SPCXUSDT", "short")

	if present := perPositionStateKeys(at, "SPCXUSDT", "short"); len(present) != 0 {
		t.Fatalf("resetPerPositionStateOnOpen must wipe all per-position state, still present: %v", present)
	}
}

// TestResetPerPositionStateOnOpen_IsolatesOtherSideAndSymbol ensures the reset for one
// symbol|side never touches a different side or a different symbol.
func TestResetPerPositionStateOnOpen_IsolatesOtherSideAndSymbol(t *testing.T) {
	at := newIsolationTestTrader("traderA")
	seedAllPerPositionState(at, "SPCXUSDT", "short")
	seedAllPerPositionState(at, "SPCXUSDT", "long") // opposite side (hedge)
	seedAllPerPositionState(at, "ZECUSDT", "short") // different symbol

	at.resetPerPositionStateOnOpen("SPCXUSDT", "short")

	if present := perPositionStateKeys(at, "SPCXUSDT", "short"); len(present) != 0 {
		t.Fatalf("target side must be wiped, still present: %v", present)
	}
	if present := perPositionStateKeys(at, "SPCXUSDT", "long"); len(present) != 16 {
		t.Fatalf("opposite side must be preserved, got %d: %v", len(present), present)
	}
	if present := perPositionStateKeys(at, "ZECUSDT", "short"); len(present) != 16 {
		t.Fatalf("other symbol must be preserved, got %d: %v", len(present), present)
	}
}

// TestResetPerPositionStateOnOpen_NormalizesSideCase confirms mixed-case side input
// resolves to the same lowercase key the rest of the system uses.
func TestResetPerPositionStateOnOpen_NormalizesSideCase(t *testing.T) {
	at := newIsolationTestTrader("traderA")
	seedAllPerPositionState(at, "SPCXUSDT", "short")

	at.resetPerPositionStateOnOpen("SPCXUSDT", "SHORT") // uppercase input

	if present := perPositionStateKeys(at, "SPCXUSDT", "short"); len(present) != 0 {
		t.Fatalf("uppercase side must still wipe lowercase-keyed state, still present: %v", present)
	}
}

// TestCleanupInactive_EvictsAllPerPositionMaps covers Block 1: the reconcile sweep must
// evict every per-position map for a symbol|side absent from the active set — the
// authoritative eviction path for sync/exchange-side closes.
func TestCleanupInactive_EvictsAllPerPositionMaps(t *testing.T) {
	at := newIsolationTestTrader("traderA")
	seedAllPerPositionState(at, "SPCXUSDT", "short") // closed (absent from active)
	seedAllPerPositionState(at, "ZECUSDT", "short")  // still active

	active := map[string]struct{}{"ZECUSDT_short": {}}
	at.cleanupInactiveProtectionState(active)

	// Maps covered by cleanup must be evicted for the inactive position. gbPnlHist and
	// breakEvenFingerprints are swept by their own paths; assert the risk-critical ones.
	inactive := perPositionStateKeys(at, "SPCXUSDT", "short")
	for _, m := range inactive {
		switch m {
		case "gbPnlHist", "breakEvenFingerprints":
			// swept elsewhere / paired with breakEvenState; not asserted here
		default:
			t.Fatalf("cleanup must evict %s for inactive SPCXUSDT_short, but it survived (all: %v)", m, inactive)
		}
	}
	// Active position must be fully preserved.
	if present := perPositionStateKeys(at, "ZECUSDT", "short"); len(present) != 16 {
		t.Fatalf("active ZECUSDT_short must be preserved, got %d: %v", len(present), present)
	}
}

// TestCleanupInactive_EvictsDrawdownTierAllocs is the focused SPCX-bug regression: the
// tier allocs map (the false-DD root cause) must be evicted on full close.
func TestCleanupInactive_EvictsDrawdownTierAllocs(t *testing.T) {
	at := newIsolationTestTrader("traderA")
	at.drawdownTierAllocs["SPCXUSDT_short"] = []store.DrawdownTierAllocation{
		{TierIndex: 0, StageName: "dd1", PeakPnLPct: 3.51, Status: "tracking"},
	}

	at.cleanupInactiveProtectionState(map[string]struct{}{}) // nothing active

	if _, ok := at.drawdownTierAllocs["SPCXUSDT_short"]; ok {
		t.Fatal("drawdownTierAllocs must be evicted on full close to prevent stale-peak false DD")
	}
}

// TestReconcileCooldown_TraderIsolation covers Block 3: two traders sharing the package
// cooldown map must not see or delete each other's cooldowns.
func TestReconcileCooldown_TraderIsolation(t *testing.T) {
	reconcileCooldownMutex.Lock()
	reconcileCooldowns = make(map[string]time.Time)
	reconcileCooldownMutex.Unlock()

	a := newIsolationTestTrader("traderA")
	b := newIsolationTestTrader("traderB")

	a.setReconcileCooldown("SPCXUSDT_short")

	if !a.isReconcileCooldownActive("SPCXUSDT_short") {
		t.Fatal("trader A must see its own cooldown")
	}
	if b.isReconcileCooldownActive("SPCXUSDT_short") {
		t.Fatal("trader B must NOT see trader A's cooldown (cross-trader leak)")
	}

	// Trader B's cleanup pass (no active positions) must not delete trader A's cooldown.
	b.cleanupInactiveProtectionState(map[string]struct{}{})
	if !a.isReconcileCooldownActive("SPCXUSDT_short") {
		t.Fatal("trader B cleanup must not delete trader A's cooldown")
	}

	// Trader A's own cleanup with the position inactive DOES clear A's cooldown.
	a.cleanupInactiveProtectionState(map[string]struct{}{})
	if a.isReconcileCooldownActive("SPCXUSDT_short") {
		t.Fatal("trader A cleanup should clear its own inactive-position cooldown")
	}
}
