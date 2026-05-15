package market

import (
	"testing"
)

func TestMergeIntoZones_Basic(t *testing.T) {
	levels := []StructuralLevel{
		{Price: 100.0, Type: "support", Timeframe: "1h", Source: "swing_point", TouchCount: 3, Confidence: 50},
		{Price: 100.2, Type: "support", Timeframe: "1h", Source: "volume_cluster", TouchCount: 2, Confidence: 45},
		{Price: 200.0, Type: "resistance", Timeframe: "4h", Source: "swing_point", TouchCount: 4, Confidence: 70},
	}

	// ATR = 1.0, tolerance = max(0.25, 100*0.003) = 0.3
	zones := MergeIntoZones(levels, 1.0, 100.0)
	if len(zones) != 2 {
		t.Fatalf("expected 2 zones, got %d", len(zones))
	}

	// First zone should merge the two close levels
	z0 := zones[0]
	if z0.Low != 100.0 || z0.High != 100.2 {
		t.Errorf("zone 0: expected low=100.0 high=100.2, got low=%.1f high=%.1f", z0.Low, z0.High)
	}
	if z0.TouchCount != 5 {
		t.Errorf("zone 0: expected touchCount=5, got %d", z0.TouchCount)
	}
	if len(z0.Sources) != 2 {
		t.Errorf("zone 0: expected 2 sources, got %d", len(z0.Sources))
	}
}

func TestMergeIntoZones_ATRScaled(t *testing.T) {
	// With large ATR, levels further apart should merge
	levels := []StructuralLevel{
		{Price: 100.0, Type: "support", Timeframe: "1h", Source: "swing_point", TouchCount: 2, Confidence: 40},
		{Price: 102.0, Type: "support", Timeframe: "1h", Source: "swing_point", TouchCount: 3, Confidence: 50},
	}

	// ATR = 10.0, tolerance = max(2.5, 100*0.003=0.3) = 2.5
	zones := MergeIntoZones(levels, 10.0, 100.0)
	if len(zones) != 1 {
		t.Fatalf("expected 1 zone (levels within 2.5 tolerance), got %d", len(zones))
	}
}

func TestMergeIntoZones_NoMergeWhenFarApart(t *testing.T) {
	levels := []StructuralLevel{
		{Price: 100.0, Type: "support", Timeframe: "1h", Source: "swing_point", TouchCount: 2, Confidence: 40},
		{Price: 110.0, Type: "resistance", Timeframe: "1h", Source: "swing_point", TouchCount: 3, Confidence: 50},
	}

	// ATR = 1.0, tolerance = 0.3 — levels are 10 apart, won't merge
	zones := MergeIntoZones(levels, 1.0, 105.0)
	if len(zones) != 2 {
		t.Fatalf("expected 2 zones, got %d", len(zones))
	}
}

func TestApplyFlipLogic_SupportBroken(t *testing.T) {
	zones := []StructuralZone{
		{Low: 99.0, High: 100.0, MidPrice: 99.5, Type: "support", Confidence: 50},
	}
	// Last 3 candles all close below zone.Low
	klines := []Kline{
		{Close: 99.5},
		{Close: 98.0},
		{Close: 97.5},
	}
	result := ApplyFlipLogic(zones, klines, 97.0)
	if result[0].Type != "resistance" {
		t.Errorf("expected flipped to resistance, got %s", result[0].Type)
	}
	if !result[0].Flipped {
		t.Error("expected Flipped=true")
	}
	if result[0].Confidence != 60 {
		t.Errorf("expected confidence 60 (50+10), got %.0f", result[0].Confidence)
	}
}

func TestApplyFlipLogic_ResistanceBroken(t *testing.T) {
	zones := []StructuralZone{
		{Low: 100.0, High: 101.0, MidPrice: 100.5, Type: "resistance", Confidence: 50},
	}
	klines := []Kline{
		{Close: 100.5},
		{Close: 102.0},
		{Close: 103.0},
	}
	result := ApplyFlipLogic(zones, klines, 103.0)
	if result[0].Type != "support" {
		t.Errorf("expected flipped to support, got %s", result[0].Type)
	}
	if !result[0].Flipped {
		t.Error("expected Flipped=true")
	}
}

func TestApplyFlipLogic_NoFlipWhenNotBroken(t *testing.T) {
	zones := []StructuralZone{
		{Low: 99.0, High: 100.0, MidPrice: 99.5, Type: "support", Confidence: 50},
	}
	klines := []Kline{
		{Close: 101.0},
		{Close: 100.5},
		{Close: 99.5}, // only 1 close below, not 2
	}
	result := ApplyFlipLogic(zones, klines, 99.5)
	if result[0].Flipped {
		t.Error("should not flip with only 1 close below")
	}
}

func TestAssignZoneQualityGrade(t *testing.T) {
	zones := []StructuralZone{
		{Confidence: 70, MultiTFCount: 2, TouchCount: 5}, // A
		{Confidence: 40, MultiTFCount: 1, TouchCount: 1}, // B
		{Confidence: 20, MultiTFCount: 0, TouchCount: 1}, // C
	}
	result := AssignZoneQualityGrade(zones)
	if result[0].QualityGrade != "A" {
		t.Errorf("expected A, got %s", result[0].QualityGrade)
	}
	if result[1].QualityGrade != "B" {
		t.Errorf("expected B, got %s", result[1].QualityGrade)
	}
	if result[2].QualityGrade != "C" {
		t.Errorf("expected C, got %s", result[2].QualityGrade)
	}
}

func TestFilterTopZonesForAI(t *testing.T) {
	zones := []StructuralZone{
		{MidPrice: 95, Type: "support", QualityGrade: "A", Confidence: 80},
		{MidPrice: 90, Type: "support", QualityGrade: "B", Confidence: 40},
		{MidPrice: 85, Type: "support", QualityGrade: "A", Confidence: 70},
		{MidPrice: 80, Type: "support", QualityGrade: "C", Confidence: 20}, // excluded
		{MidPrice: 75, Type: "support", QualityGrade: "B", Confidence: 35},
		{MidPrice: 110, Type: "resistance", QualityGrade: "A", Confidence: 75},
		{MidPrice: 120, Type: "resistance", QualityGrade: "B", Confidence: 45},
	}

	result := FilterTopZonesForAI(zones, 100.0, 3)

	supportCount := 0
	resistanceCount := 0
	for _, z := range result {
		if z.Type == "support" {
			supportCount++
		} else {
			resistanceCount++
		}
		if z.QualityGrade == "C" {
			t.Error("C-grade zone should not be in AI output")
		}
	}
	if supportCount > 3 {
		t.Errorf("expected max 3 support zones, got %d", supportCount)
	}
	if resistanceCount > 3 {
		t.Errorf("expected max 3 resistance zones, got %d", resistanceCount)
	}
}

func TestEnrichZoneMultiTF(t *testing.T) {
	zonesByTF := map[string][]StructuralZone{
		"1h": {
			{Low: 99, High: 101, MidPrice: 100, Type: "support"},
			{Low: 200, High: 202, MidPrice: 201, Type: "resistance"},
		},
		"4h": {
			{Low: 100, High: 102, MidPrice: 101, Type: "support"}, // overlaps with 1h zone
		},
	}

	EnrichZoneMultiTF(zonesByTF)

	// 1h zone at 99-101 should have 1 confirmation (from 4h 100-102)
	if zonesByTF["1h"][0].MultiTFCount != 1 {
		t.Errorf("expected MultiTFCount=1, got %d", zonesByTF["1h"][0].MultiTFCount)
	}
	// 1h zone at 200-202 should have 0 confirmations
	if zonesByTF["1h"][1].MultiTFCount != 0 {
		t.Errorf("expected MultiTFCount=0, got %d", zonesByTF["1h"][1].MultiTFCount)
	}
}

func TestApplyTimeframeBoost(t *testing.T) {
	zones := []StructuralZone{
		{Timeframes: []string{"5m"}, Confidence: 50},
		{Timeframes: []string{"4h"}, Confidence: 50},
		{Timeframes: []string{"1d"}, Confidence: 50},
		{Timeframes: []string{"1h", "4h"}, Confidence: 50},
	}
	result := ApplyTimeframeBoost(zones)
	if result[0].Confidence != 50 {
		t.Errorf("5m should get no boost, got %.0f", result[0].Confidence)
	}
	if result[1].Confidence != 65 {
		t.Errorf("4h should get +15, got %.0f", result[1].Confidence)
	}
	if result[2].Confidence != 75 {
		t.Errorf("1d should get +25, got %.0f", result[2].Confidence)
	}
	if result[3].Confidence != 65 {
		t.Errorf("1h+4h should get +15 (max), got %.0f", result[3].Confidence)
	}
}
