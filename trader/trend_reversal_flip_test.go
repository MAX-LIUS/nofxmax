package trader

import (
	"nofx/kernel"
	"nofx/store"
	"testing"
	"time"
)

func newFlipTestTrader(cfg store.TrendReversalConfig) *AutoTrader {
	return &AutoTrader{
		id:                    "flip-test-trader",
		exchange:              "paper",
		config:                AutoTraderConfig{StrategyConfig: &store.StrategyConfig{Protection: store.ProtectionConfig{TrendReversal: cfg}}},
		positionFirstSeenTime: make(map[string]int64),
	}
}

func flipPos(symbol, side string, amt float64) map[string]interface{} {
	return map[string]interface{}{
		"symbol":      symbol,
		"side":        side,
		"positionAmt": amt,
	}
}

func decisionFor(symbol string, conf int) *kernel.Decision {
	return &kernel.Decision{Symbol: symbol, Confidence: conf}
}

// Fleet default is ON: with no strategy override the feature evaluates flips.
func TestEvaluateFlipFleetDefaultOn(t *testing.T) {
	at := newFlipTestTrader(store.TrendReversalConfig{}) // no override = fleet default
	now := time.Now().UnixMilli()
	at.positionFirstSeenTime["ETHUSDT_short"] = now - 10*60*60*1000 // 10h old
	fd := at.evaluateFlip(decisionFor("ETHUSDT", 90), "long", flipPos("ETHUSDT", "short", 1.0))
	if !fd.ShouldFlip {
		t.Fatalf("fleet default ON must flip aged high-conviction reversal, reason=%q", fd.Reason)
	}
}

// A strategy may hard-disable the feature for a specific trader.
func TestEvaluateFlipDisabledOverride(t *testing.T) {
	at := newFlipTestTrader(store.TrendReversalConfig{Disabled: true})
	now := time.Now().UnixMilli()
	at.positionFirstSeenTime["ETHUSDT_short"] = now - 10*60*60*1000 // 10h old
	fd := at.evaluateFlip(decisionFor("ETHUSDT", 90), "long", flipPos("ETHUSDT", "short", 1.0))
	if fd.ShouldFlip {
		t.Fatal("Disabled override must turn the feature off for this trader")
	}
}

// Same-direction signal is not a reversal.
func TestEvaluateFlipSameSide(t *testing.T) {
	at := newFlipTestTrader(store.TrendReversalConfig{})
	now := time.Now().UnixMilli()
	at.positionFirstSeenTime["ETHUSDT_long"] = now - 10*60*60*1000
	fd := at.evaluateFlip(decisionFor("ETHUSDT", 90), "long", flipPos("ETHUSDT", "long", 1.0))
	if fd.ShouldFlip {
		t.Fatal("same-side signal must not flip")
	}
}

// Confidence below floor blocks the flip (default floor 75).
func TestEvaluateFlipLowConfidence(t *testing.T) {
	at := newFlipTestTrader(store.TrendReversalConfig{})
	now := time.Now().UnixMilli()
	at.positionFirstSeenTime["ETHUSDT_short"] = now - 10*60*60*1000
	fd := at.evaluateFlip(decisionFor("ETHUSDT", 70), "long", flipPos("ETHUSDT", "short", 1.0))
	if fd.ShouldFlip {
		t.Fatalf("confidence 70 < floor 75 must block flip, got reason=%q", fd.Reason)
	}
}

// Position too young blocks the flip (default 6h).
func TestEvaluateFlipTooYoung(t *testing.T) {
	at := newFlipTestTrader(store.TrendReversalConfig{})
	now := time.Now().UnixMilli()
	at.positionFirstSeenTime["ETHUSDT_short"] = now - 2*60*60*1000 // 2h < 6h
	fd := at.evaluateFlip(decisionFor("ETHUSDT", 90), "long", flipPos("ETHUSDT", "short", 1.0))
	if fd.ShouldFlip {
		t.Fatalf("2h position must be too young to flip, got reason=%q", fd.Reason)
	}
}

// All conditions satisfied => flip.
func TestEvaluateFlipAllConditionsMet(t *testing.T) {
	at := newFlipTestTrader(store.TrendReversalConfig{})
	now := time.Now().UnixMilli()
	at.positionFirstSeenTime["ETHUSDT_short"] = now - 10*60*60*1000 // 10h
	fd := at.evaluateFlip(decisionFor("ETHUSDT", 80), "long", flipPos("ETHUSDT", "short", 2.5))
	if !fd.ShouldFlip {
		t.Fatalf("all conditions met must flip, got reason=%q", fd.Reason)
	}
	if fd.ExistingSide != "short" {
		t.Errorf("ExistingSide = %q, want short", fd.ExistingSide)
	}
	if fd.Quantity != 2.5 {
		t.Errorf("Quantity = %v, want 2.5", fd.Quantity)
	}
}

// Threshold overrides replace the fleet defaults.
func TestEvaluateFlipThresholdOverride(t *testing.T) {
	at := newFlipTestTrader(store.TrendReversalConfig{MinConfidence: 90, MinPositionAgeHours: 12})
	now := time.Now().UnixMilli()
	at.positionFirstSeenTime["ETHUSDT_short"] = now - 13*60*60*1000 // 13h > 12h
	if at.evaluateFlip(decisionFor("ETHUSDT", 85), "long", flipPos("ETHUSDT", "short", 1.0)).ShouldFlip {
		t.Fatal("override confidence floor 90 must block conf 85")
	}
	if !at.evaluateFlip(decisionFor("ETHUSDT", 90), "long", flipPos("ETHUSDT", "short", 1.0)).ShouldFlip {
		t.Fatal("conf 90 at override floor must flip")
	}
}

// Negative position amount is normalized to absolute quantity.
func TestEvaluateFlipNegativeAmt(t *testing.T) {
	at := newFlipTestTrader(store.TrendReversalConfig{})
	now := time.Now().UnixMilli()
	at.positionFirstSeenTime["ETHUSDT_long"] = now - 10*60*60*1000
	fd := at.evaluateFlip(decisionFor("ETHUSDT", 90), "short", flipPos("ETHUSDT", "long", -3.0))
	if !fd.ShouldFlip || fd.Quantity != 3.0 {
		t.Fatalf("negative amt must normalize to 3.0, got flip=%v qty=%v", fd.ShouldFlip, fd.Quantity)
	}
}

// A genuine opposite-on-held signal blocked by the confidence floor must still
// be flagged as a candidate (so it gets recorded) with the right outcome.
func TestEvaluateFlipBlockedConfidenceIsCandidate(t *testing.T) {
	at := newFlipTestTrader(store.TrendReversalConfig{})
	now := time.Now().UnixMilli()
	at.positionFirstSeenTime["ETHUSDT_short"] = now - 10*60*60*1000
	fd := at.evaluateFlip(decisionFor("ETHUSDT", 70), "long", flipPos("ETHUSDT", "short", 1.0))
	if fd.ShouldFlip {
		t.Fatal("conf 70 must not flip")
	}
	if !fd.IsCandidate {
		t.Fatal("blocked-by-confidence reversal must still be a candidate for recording")
	}
	if fd.Outcome != "blocked_confidence" {
		t.Fatalf("Outcome = %q, want blocked_confidence", fd.Outcome)
	}
	if fd.MinConf != 75 {
		t.Errorf("MinConf = %d, want 75", fd.MinConf)
	}
}

// Blocked by age => candidate + blocked_age, and AgeHours captured.
func TestEvaluateFlipBlockedAgeIsCandidate(t *testing.T) {
	at := newFlipTestTrader(store.TrendReversalConfig{})
	now := time.Now().UnixMilli()
	at.positionFirstSeenTime["ETHUSDT_short"] = now - 2*60*60*1000 // 2h < 6h
	fd := at.evaluateFlip(decisionFor("ETHUSDT", 90), "long", flipPos("ETHUSDT", "short", 1.0))
	if fd.ShouldFlip || !fd.IsCandidate {
		t.Fatalf("2h reversal must be a blocked candidate, flip=%v cand=%v", fd.ShouldFlip, fd.IsCandidate)
	}
	if fd.Outcome != "blocked_age" {
		t.Fatalf("Outcome = %q, want blocked_age", fd.Outcome)
	}
	if fd.AgeHours <= 0 || fd.AgeHours > 6 {
		t.Errorf("AgeHours = %v, want (0,6)", fd.AgeHours)
	}
}

// Disabled feature on a genuine reversal => candidate + blocked_disabled (so the
// near-miss is still recorded even when flipping is turned off for the trader).
func TestEvaluateFlipDisabledIsCandidate(t *testing.T) {
	at := newFlipTestTrader(store.TrendReversalConfig{Disabled: true})
	now := time.Now().UnixMilli()
	at.positionFirstSeenTime["ETHUSDT_short"] = now - 10*60*60*1000
	fd := at.evaluateFlip(decisionFor("ETHUSDT", 90), "long", flipPos("ETHUSDT", "short", 1.0))
	if fd.ShouldFlip {
		t.Fatal("disabled feature must not flip")
	}
	if !fd.IsCandidate || fd.Outcome != "blocked_disabled" {
		t.Fatalf("disabled reversal must be candidate/blocked_disabled, got cand=%v outcome=%q", fd.IsCandidate, fd.Outcome)
	}
}

// Same-side (not a reversal) is never a candidate.
func TestEvaluateFlipSameSideNotCandidate(t *testing.T) {
	at := newFlipTestTrader(store.TrendReversalConfig{})
	fd := at.evaluateFlip(decisionFor("ETHUSDT", 90), "long", flipPos("ETHUSDT", "long", 1.0))
	if fd.IsCandidate {
		t.Fatal("same-side signal must not be a flip candidate")
	}
}
