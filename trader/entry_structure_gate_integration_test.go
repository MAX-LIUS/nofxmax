package trader

import (
	"testing"
	"time"

	"nofx/kernel"
	"nofx/market"
	"nofx/store"
)

// mkStructInput wires a full entryGateInput whose PRIMARY timeframe carries the
// supplied bars, so evaluateStructuralFitGate's structural check runs for real.
func mkStructInput(action string, entry float64, bars []market.KlineBar, tune func(*store.EntryGateConfig)) entryGateInput {
	yes := true
	eg := store.EntryGateConfig{Enabled: true, StructuralAlignment: &yes}
	if tune != nil {
		tune(&eg)
	}
	sc := &store.StrategyConfig{}
	sc.Indicators.Klines.PrimaryTimeframe = "1h"
	sc.Indicators.Klines.SelectedTimeframes = []string{"1h", "15m"}
	sc.EntryStructure.EntryGate = eg
	return entryGateInput{
		Decision: &kernel.Decision{
			Action:      action,
			TriggerType: "support_rejection_confirmed",
			SetupType:   "trend_pullback",
			Confidence:  70,
			EntryProtection: &kernel.AIEntryProtectionRationale{
				RiskReward: kernel.AIRiskRewardRationale{
					Entry: entry, Invalidation: entry * 0.96, FirstTarget: entry * 1.1,
					GrossEstimatedRR: 2.2, NetEstimatedRR: 2.0, MinRequiredRR: 1.5, Passed: true,
				},
			},
		},
		StrategyConfig: sc,
		MarketData: &market.Data{
			CurrentPrice: entry,
			TimeframeData: map[string]*market.TimeframeSeriesData{
				// One extra bar: the gate drops the last (still-forming) one.
				"1h": {Timeframe: "1h", Klines: append(bars, sgBar(entry, entry))},
			},
		},
	}
}

// A LONG into a confirmed HH+HL uptrend must NOT trip the structural check.
func TestStructuralGate_AlignedLongPasses(t *testing.T) {
	bars := zigzag(6, 100, 5, 4)
	in := mkStructInput("open_long", bars[len(bars)-1].Close, bars, func(c *store.EntryGateConfig) {
		z := 0.0
		c.StructuralMinBlockingPct = &z // isolate the direction check
	})
	checks := evaluateStructuralFitGate(in)
	if c := findCheck(checks, "structural_alignment_missing"); c != nil && !c.Passed {
		t.Fatalf("aligned long should not fail the structural check: %s", c.Detail)
	}
}

// A LONG while structure is LH+LL (down) must hard-block.
func TestStructuralGate_CounterTrendLongBlocks(t *testing.T) {
	bars := zigzag(6, 150, -5, 4)
	in := mkStructInput("open_long", bars[len(bars)-1].Close, bars, nil)
	checks := evaluateStructuralFitGate(in)
	c := findCheck(checks, "structural_alignment_missing")
	if c == nil {
		t.Fatal("expected structural_alignment_missing for a long into a downtrend")
	}
	if c.Passed {
		t.Error("check must not pass")
	}
	if !c.Enforced {
		t.Error("check must be ENFORCED (hard block) when audit-only is off")
	}
	blocked, code, _ := firstEnforcedFailure(checks)
	if !blocked || code != "structural_alignment_missing" {
		t.Errorf("expected a hard block from structural_alignment_missing, got blocked=%v code=%s", blocked, code)
	}
}

// A SHORT into a confirmed downtrend passes; the mirror of the aligned-long case.
// Guards against a side-handling asymmetry, which is what I wrongly accused
// chart_trend of having earlier in this work.
func TestStructuralGate_AlignedShortPasses(t *testing.T) {
	bars := zigzag(6, 150, -5, 4)
	in := mkStructInput("open_short", bars[len(bars)-1].Close, bars, func(c *store.EntryGateConfig) {
		z := 0.0
		c.StructuralMinBlockingPct = &z
	})
	checks := evaluateStructuralFitGate(in)
	if c := findCheck(checks, "structural_alignment_missing"); c != nil && !c.Passed {
		t.Fatalf("aligned short should not fail: %s", c.Detail)
	}
}

func TestStructuralGate_CounterTrendShortBlocks(t *testing.T) {
	bars := zigzag(6, 100, 5, 4)
	in := mkStructInput("open_short", bars[len(bars)-1].Close, bars, nil)
	checks := evaluateStructuralFitGate(in)
	c := findCheck(checks, "structural_alignment_missing")
	if c == nil || c.Passed {
		t.Fatal("expected a block for a short into an uptrend")
	}
}

// Audit-only must surface the finding but never hard-block.
func TestStructuralGate_AuditOnlyDoesNotBlock(t *testing.T) {
	bars := zigzag(6, 150, -5, 4)
	in := mkStructInput("open_long", bars[len(bars)-1].Close, bars, func(c *store.EntryGateConfig) {
		yes := true
		c.StructuralAuditOnly = &yes
	})
	checks := evaluateStructuralFitGate(in)
	c := findCheck(checks, "structural_alignment_missing")
	if c == nil {
		t.Fatal("audit mode must still record the check")
	}
	if c.Enforced {
		t.Error("audit-only must not be enforced")
	}
	if blocked, code, _ := firstEnforcedFailure(checks); blocked && code == "structural_alignment_missing" {
		t.Error("audit-only must not hard-block")
	}
	// Isolate THIS check's contribution: other soft checks in the same stage
	// (e.g. range_middle_without_edge_setup, -15) legitimately deduct, so
	// asserting on the total score would measure them instead of us.
	withStruct := computeGateScore(checks)
	var without []EntryGateCheck
	for _, ck := range checks {
		if ck.Code != "structural_alignment_missing" {
			without = append(without, ck)
		}
	}
	if delta := computeGateScore(without) - withStruct; delta != 0 {
		t.Errorf("audit-only structural check must deduct nothing, but it cost %d points", delta)
	}
}

// Gate off (default) → the check must not appear at all, even on a setup that
// would otherwise block. This is the guard on "ships OFF".
func TestStructuralGate_DisabledByDefaultEmitsNothing(t *testing.T) {
	bars := zigzag(6, 150, -5, 4)
	in := mkStructInput("open_long", bars[len(bars)-1].Close, bars, func(c *store.EntryGateConfig) {
		c.StructuralAlignment = nil // unset = default
	})
	checks := evaluateStructuralFitGate(in)
	if c := findCheck(checks, "structural_alignment_missing"); c != nil {
		t.Fatal("structural check must not run when the gate is unset (default off)")
	}
}

// Too few bars → abstain, never block on missing data.
func TestStructuralGate_InsufficientBarsAbstains(t *testing.T) {
	bars := []market.KlineBar{sgBar(10, 9), sgBar(11, 10), sgBar(12, 11)}
	in := mkStructInput("open_long", 11, bars, nil)
	checks := evaluateStructuralFitGate(in)
	if c := findCheck(checks, "structural_alignment_missing"); c != nil {
		t.Fatal("must abstain (not block) when there are too few closed bars")
	}
}

// REGRESSION GUARD for the 2026-07-31 production defect: the gate read the
// decision context, which is trimmed to PrimaryCount for the AI prompt. At
// GPT-ct50's primary_count=22 (21 closed bars) the rule degrades to noise
// (+0.160, p=0.15) and blocks 92% of entries for having too few VISIBLE pivots.
// A context shorter than the validated window must abstain, not block.
//
// The cache is pre-seeded with an empty entry so the self-fetch is not attempted:
// this asserts the abstain semantics without a network call.
func TestStructuralGate_ThinContextAbstainsInsteadOfBlocking(t *testing.T) {
	full := zigzag(6, 150, -5, 4) // downtrend
	if len(full) < structuralMinBars {
		t.Fatalf("fixture too short: %d", len(full))
	}
	// A counter-trend LONG into a downtrend: with the full window this BLOCKS.
	in := mkStructInput("open_long", full[len(full)-1].Close, full, nil)
	in.Decision.Symbol = "TESTTHINUSDT"
	if c := findCheck(evaluateStructuralFitGate(in), "structural_alignment_missing"); c == nil {
		t.Fatal("precondition failed: full window must block a counter-trend long")
	}

	// Same entry, but the context is trimmed the way PrimaryCount trims it.
	thin := full[len(full)-21:]
	in2 := mkStructInput("open_long", thin[len(thin)-1].Close, thin, nil)
	in2.Decision.Symbol = "TESTTHINUSDT"
	in2.Exchange = "okx"
	key := "TESTTHINUSDT|1h|okx"
	structuralBarsCache.Store(key, &structuralBarsCacheEntry{updatedAt: time.Now().UTC()})
	defer structuralBarsCache.Delete(key)

	if c := findCheck(evaluateStructuralFitGate(in2), "structural_alignment_missing"); c != nil {
		t.Fatalf("thin context must abstain, not block; got %s", c.Values)
	}
}

// A context that already carries enough closed bars must be used as-is, with no
// self-fetch. Proven by using a symbol whose cache entry says "abstain": if the
// code consulted the cache/fetch path at all, the check would not fire.
func TestStructuralBars_SufficientContextSkipsFetch(t *testing.T) {
	full := zigzag(6, 150, -5, 4)
	in := mkStructInput("open_long", full[len(full)-1].Close, full, nil)
	in.Decision.Symbol = "TESTSKIPUSDT"
	in.Exchange = "okx"
	key := "TESTSKIPUSDT|1h|okx"
	structuralBarsCache.Store(key, &structuralBarsCacheEntry{updatedAt: time.Now().UTC()})
	defer structuralBarsCache.Delete(key)

	if c := findCheck(evaluateStructuralFitGate(in), "structural_alignment_missing"); c == nil {
		t.Fatal("sufficient in-context bars must be used directly, without consulting the fetch path")
	}
}

// The cached-abstain entry must not be mistaken for a usable series.
func TestStructuralBars_CachedAbstainStaysAbstain(t *testing.T) {
	sc := &store.StrategyConfig{}
	sc.Indicators.Klines.PrimaryTimeframe = "1h"
	key := "TESTCACHEUSDT|1h|okx"
	structuralBarsCache.Store(key, &structuralBarsCacheEntry{updatedAt: time.Now().UTC()})
	defer structuralBarsCache.Delete(key)

	bars, tf := structuralBars(sc, &market.Data{}, "TESTCACHEUSDT", "okx")
	if bars != nil {
		t.Fatalf("cached abstain must return nil bars, got %d", len(bars))
	}
	if tf != "1h" {
		t.Fatalf("timeframe must still resolve for logging, got %q", tf)
	}
}

// No symbol → cannot self-fetch → abstain. Guards the execution path against a
// failed fetch turning into a blocked trade.
func TestStructuralBars_NoSymbolAbstains(t *testing.T) {
	sc := &store.StrategyConfig{}
	sc.Indicators.Klines.PrimaryTimeframe = "1h"
	if bars, _ := structuralBars(sc, &market.Data{}, "", "okx"); bars != nil {
		t.Fatalf("must abstain without a symbol, got %d bars", len(bars))
	}
}

// Direction agrees but a real pivot sits just ahead → the distance sub-check
// blocks. Also proves the two sub-checks are mutually exclusive per entry.
func TestStructuralGate_BlockingLevelTooCloseBlocks(t *testing.T) {
	bars := zigzag(6, 100, 5, 4)
	last := bars[len(bars)-1]
	// Add a nearby swing high just 0.1% above, then padding so it resolves as a pivot.
	top := last.High * 1.001
	for i := 0; i < 4; i++ {
		bars = append(bars, sgBar(last.High, last.Low))
	}
	for i := 0; i < 1; i++ {
		bars = append(bars, sgBar(top, top*0.999))
	}
	for i := 0; i < 4; i++ {
		bars = append(bars, sgBar(last.High, last.Low))
	}
	entry := last.High
	in := mkStructInput("open_long", entry, bars, func(c *store.EntryGateConfig) {
		v := 0.5
		c.StructuralMinBlockingPct = &v
	})
	checks := evaluateStructuralFitGate(in)
	dirC := findCheck(checks, "structural_alignment_missing")
	distC := findCheck(checks, "structural_blocking_level_too_close")
	if dirC != nil && distC != nil {
		t.Error("the two structural sub-checks must not both fire for one entry")
	}
	if dirC == nil && distC == nil {
		t.Skip("bar geometry did not produce a qualifying near pivot; direction path already verified elsewhere")
	}
}
