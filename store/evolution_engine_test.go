package store

import (
	"testing"
	"time"
)

func TestComputeFactorsBasic(t *testing.T) {
	now := time.Now()
	trades := []TradeOutcome{
		{Symbol: "BTC", Side: "long", PnLPct: 2.5, IsWin: true, CloseTime: now.Add(-1 * 24 * time.Hour), EntryTime: now.Add(-25 * time.Hour), SceneTags: SceneTagsData{TrendPhase: "establishment", Chg4h: 1.2, Chg1h: 0.5, EMA20Dev: 0.3, TriggerType: "support_rejection_confirmed"}, CloseReason: "tp_hit"},
		{Symbol: "BTC", Side: "long", PnLPct: 1.8, IsWin: true, CloseTime: now.Add(-2 * 24 * time.Hour), EntryTime: now.Add(-50 * time.Hour), SceneTags: SceneTagsData{TrendPhase: "establishment", Chg4h: 0.8, Chg1h: 0.3, EMA20Dev: 0.5, TriggerType: "higher_low_breakout_confirmed"}, CloseReason: "tp_hit"},
		{Symbol: "BTC", Side: "long", PnLPct: -1.2, IsWin: false, CloseTime: now.Add(-3 * 24 * time.Hour), EntryTime: now.Add(-75 * time.Hour), SceneTags: SceneTagsData{TrendPhase: "extension", Chg4h: 3.1, Chg1h: 0.2, EMA20Dev: 2.1, TriggerType: "support_rejection_confirmed"}, CloseReason: "sl_hit"},
		{Symbol: "BTC", Side: "long", PnLPct: -0.8, IsWin: false, CloseTime: now.Add(-4 * 24 * time.Hour), EntryTime: now.Add(-100 * time.Hour), SceneTags: SceneTagsData{TrendPhase: "extension", Chg4h: 2.8, Chg1h: -0.1, EMA20Dev: 1.9, TriggerType: "resistance_breakout_retest_successful"}, CloseReason: "sl_hit"},
		{Symbol: "BTC", Side: "long", PnLPct: 3.2, IsWin: true, CloseTime: now.Add(-5 * 24 * time.Hour), EntryTime: now.Add(-126 * time.Hour), SceneTags: SceneTagsData{TrendPhase: "establishment", Chg4h: 1.5, Chg1h: 0.6, EMA20Dev: 0.4, TriggerType: "higher_low_breakout_confirmed"}, CloseReason: "tp_hit"},
		{Symbol: "BTC", Side: "long", PnLPct: 0.5, IsWin: true, CloseTime: now.Add(-6 * 24 * time.Hour), EntryTime: now.Add(-148 * time.Hour), SceneTags: SceneTagsData{TrendPhase: "continuation", Chg4h: 1.8, Chg1h: 0.4, EMA20Dev: 1.0, TriggerType: "support_rejection_confirmed"}, CloseReason: "drawdown"},
	}

	factors := ComputeFactors(trades)
	if len(factors) != 8 {
		t.Fatalf("expected 8 factors, got %d", len(factors))
	}

	// Verify factor names
	expectedNames := []string{
		FactorTrendPhaseFit,
		FactorEMA20Alignment,
		FactorMomentumSweetSpot,
		FactorHoldDurationFit,
		FactorProtectionEffectiveness,
		FactorTimeOfDay,
		FactorVolatilityRegime,
		FactorTriggerQuality,
	}
	for i, f := range factors {
		if f.Name != expectedNames[i] {
			t.Errorf("factor %d: expected name %q, got %q", i, expectedNames[i], f.Name)
		}
		if f.Score < 0 || f.Score > 100 {
			t.Errorf("factor %s: score %.2f out of range [0,100]", f.Name, f.Score)
		}
		if f.SampleSize <= 0 {
			t.Errorf("factor %s: sample_size should be > 0", f.Name)
		}
	}

	// Trend phase fit: establishment wins 3/3, extension loses 2/2 → high score
	tpf := factors[0]
	if tpf.Score < 50 {
		t.Errorf("trend_phase_fit score %.0f should be high (establishment wins dominate)", tpf.Score)
	}

	// Trigger quality: rejection has 2W/1L, structure_break has 2W/0L, breakout_retest has 0W/1L
	tq := factors[7]
	if tq.Score < 40 {
		t.Errorf("trigger_quality score %.0f should be moderate-high", tq.Score)
	}
	if tq.Insight == "" {
		t.Error("trigger_quality should have an insight")
	}
}

func TestGenerateAdaptations(t *testing.T) {
	factors := []EvolutionFactor{
		{Name: FactorTrendPhaseFit, Score: 25, SampleSize: 8, Confidence: 0.7},
		{Name: FactorEMA20Alignment, Score: 30, SampleSize: 10, Confidence: 0.8},
		{Name: FactorMomentumSweetSpot, Score: 60, SampleSize: 7, Confidence: 0.6}, // neutral
		{Name: FactorTriggerQuality, Score: 20, SampleSize: 6, Confidence: 0.5},
	}

	adaptations := GenerateAdaptations(factors)

	// Should generate adaptations for trend_phase_fit, ema20_alignment, and trigger_quality (all < 35)
	// Should NOT generate for momentum_sweet_spot (score 60, in neutral range)
	if len(adaptations) != 3 {
		t.Fatalf("expected 3 adaptations, got %d", len(adaptations))
	}

	conditions := map[string]bool{}
	for _, a := range adaptations {
		conditions[a.Condition] = true
		if a.ExpiresAt <= 0 {
			t.Errorf("adaptation %s should have an expiry", a.Condition)
		}
	}

	if !conditions["phase=extension"] {
		t.Error("expected adaptation for phase=extension")
	}
	if !conditions["ema20_contradicted"] {
		t.Error("expected adaptation for ema20_contradicted")
	}
	if !conditions["trigger_low_quality"] {
		t.Error("expected adaptation for trigger_low_quality")
	}
}

func TestPruneExpiredAdaptations(t *testing.T) {
	now := time.Now().UTC().UnixMilli()
	adaptations := []Adaptation{
		{Condition: "phase=extension", ExpiresAt: now + 86400000, Contradictions: 0},  // valid
		{Condition: "ema20_contradicted", ExpiresAt: now - 1000, Contradictions: 0},   // expired
		{Condition: "chg4h_gt_2.5", ExpiresAt: now + 86400000, Contradictions: 5},     // too many contradictions
		{Condition: "trigger_low_quality", ExpiresAt: 0, Contradictions: 2},            // no expiry, valid
	}

	pruned := PruneExpiredAdaptations(adaptations)
	if len(pruned) != 2 {
		t.Fatalf("expected 2 valid adaptations after pruning, got %d", len(pruned))
	}
	if pruned[0].Condition != "phase=extension" && pruned[1].Condition != "phase=extension" {
		t.Error("phase=extension should survive pruning")
	}
}

func TestBuildEvolutionContext(t *testing.T) {
	profile := &CoinEvolutionProfile{
		SampleSize: 10,
	}

	factors := []EvolutionFactor{
		{Name: FactorTrendPhaseFit, Score: 80, SampleSize: 8, Insight: "establishment胜率80%"},
		{Name: FactorEMA20Alignment, Score: 25, SampleSize: 7, Insight: "EMA20一致胜率30%, 矛盾胜率70%"},
		{Name: FactorMomentumSweetSpot, Score: 55, SampleSize: 6, Insight: ""},
	}
	profile.SetFactors(factors)

	adaptations := []Adaptation{
		{Condition: "ema20_contradicted", Action: "reduce_size_30%", Reason: "EMA20矛盾"},
	}
	profile.SetAdaptations(adaptations)

	ctx := BuildEvolutionContext(profile)
	if ctx == "" {
		t.Fatal("expected non-empty evolution context")
	}
	if !findSubstring(ctx, "适配度") {
		t.Error("context should contain fitness score")
	}
	if !findSubstring(ctx, "establishment") {
		t.Error("context should contain top insight")
	}
	if !findSubstring(ctx, "ema20_contradicted") {
		t.Error("context should contain adaptation")
	}
}

func TestDecayWeight(t *testing.T) {
	now := time.Now()

	// Today's trade should have weight ~1.0
	w0 := decayWeight(now, now)
	if w0 < 0.99 || w0 > 1.01 {
		t.Errorf("weight for today should be ~1.0, got %.4f", w0)
	}

	// 14 days ago (half-life) should have weight ~0.5
	w14 := decayWeight(now.Add(-14*24*time.Hour), now)
	if w14 < 0.45 || w14 > 0.55 {
		t.Errorf("weight for 14 days ago should be ~0.5, got %.4f", w14)
	}

	// 28 days ago should have weight ~0.25
	w28 := decayWeight(now.Add(-28*24*time.Hour), now)
	if w28 < 0.20 || w28 > 0.30 {
		t.Errorf("weight for 28 days ago should be ~0.25, got %.4f", w28)
	}
}
