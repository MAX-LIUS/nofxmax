package market

import (
	"math"
	"sort"
)

// StructuralZone represents a merged price zone from multiple structural levels.
type StructuralZone struct {
	Low           float64  `json:"low"`
	High          float64  `json:"high"`
	MidPrice      float64  `json:"mid_price"`
	Type          string   `json:"type"` // "support" | "resistance"
	Timeframes    []string `json:"timeframes"`
	Sources       []string `json:"sources"`
	TouchCount    int      `json:"touch_count"`
	Confidence    float64  `json:"confidence"`
	QualityGrade  string   `json:"quality_grade"` // "A" | "B" | "C"
	LastTouchBars int      `json:"last_touch_bars"`
	VolumeScore   float64  `json:"volume_score"`
	ATRWidth      float64  `json:"atr_width"` // zone width / ATR14
	Flipped       bool     `json:"flipped"`
	FlipCount     int      `json:"flip_count"`
	MultiTFCount  int      `json:"multi_tf_count"`
}

// MergeIntoZones merges individual structural levels into zones using ATR-scaled tolerance.
// tolerance = max(0.25 * atr14, currentPrice * 0.003)
func MergeIntoZones(levels []StructuralLevel, atr14, currentPrice float64) []StructuralZone {
	if len(levels) == 0 {
		return nil
	}
	if atr14 <= 0 {
		atr14 = currentPrice * 0.01
	}

	tolerance := math.Max(0.25*atr14, currentPrice*0.003)
	// Cap the total width of a merged zone. Without this, chain-merging (each level
	// only needs to be within `tolerance` of the running zone High) lets a zone grow
	// unbounded — e.g. WLD 0.49→0.56 (~14%) collapsing support+resistance into one
	// band so direction can't be told apart. A real S/R zone is tight; cap at the
	// larger of 1×ATR14 or 1.2% of price (fix 2026-06-10).
	maxZoneWidth := math.Max(atr14, currentPrice*0.012)

	sorted := make([]StructuralLevel, len(levels))
	copy(sorted, levels)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Price < sorted[j].Price
	})

	var zones []StructuralZone
	i := 0
	for i < len(sorted) {
		zone := newZoneFromLevel(sorted[i])
		j := i + 1
		for j < len(sorted) &&
			sorted[j].Price-zone.High <= tolerance &&
			sorted[j].Price-zone.Low <= maxZoneWidth {
			mergeLevel(&zone, sorted[j])
			j++
		}
		zone.ATRWidth = (zone.High - zone.Low) / atr14
		zones = append(zones, zone)
		i = j
	}

	return zones
}

func newZoneFromLevel(l StructuralLevel) StructuralZone {
	return StructuralZone{
		Low:           l.Price,
		High:          l.Price,
		MidPrice:      l.Price,
		Type:          l.Type,
		Timeframes:    []string{l.Timeframe},
		Sources:       []string{l.Source},
		TouchCount:    l.TouchCount,
		Confidence:    l.Confidence,
		LastTouchBars: l.LastTouchBars,
		VolumeScore:   l.VolumeScore,
		MultiTFCount:  l.MultiTFCount,
	}
}

func mergeLevel(zone *StructuralZone, l StructuralLevel) {
	if l.Price < zone.Low {
		zone.Low = l.Price
	}
	if l.Price > zone.High {
		zone.High = l.Price
	}
	// Volume-weighted mid price approximation
	zone.MidPrice = (zone.MidPrice*float64(zone.TouchCount) + l.Price*float64(max(l.TouchCount, 1))) /
		float64(zone.TouchCount+max(l.TouchCount, 1))

	zone.TouchCount += l.TouchCount
	if l.VolumeScore > zone.VolumeScore {
		zone.VolumeScore = l.VolumeScore
	}
	if l.LastTouchBars < zone.LastTouchBars || zone.LastTouchBars == 0 {
		zone.LastTouchBars = l.LastTouchBars
	}
	if l.MultiTFCount > zone.MultiTFCount {
		zone.MultiTFCount = l.MultiTFCount
	}

	// Merge timeframes (deduplicated)
	if !containsStr(zone.Timeframes, l.Timeframe) {
		zone.Timeframes = append(zone.Timeframes, l.Timeframe)
	}
	// Merge sources (deduplicated)
	if !containsStr(zone.Sources, l.Source) {
		zone.Sources = append(zone.Sources, l.Source)
	}

	// Take higher confidence
	if l.Confidence > zone.Confidence {
		zone.Confidence = l.Confidence
	}
}

// ApplyFlipLogic detects S/R flips: if price has broken through a zone
// (2 consecutive closes beyond the zone boundary), flip its type.
func ApplyFlipLogic(zones []StructuralZone, klines []Kline, currentPrice float64) []StructuralZone {
	if len(klines) < 3 {
		return zones
	}

	// Use last 3 candles for flip detection
	recent := klines[len(klines)-3:]

	for i := range zones {
		z := &zones[i]
		switch z.Type {
		case "support":
			// Support broken = last 2 closes below zone.Low
			if recent[1].Close < z.Low && recent[2].Close < z.Low {
				z.Type = "resistance"
				z.Flipped = true
				z.FlipCount++
				z.Confidence += 10
				if z.Confidence > 100 {
					z.Confidence = 100
				}
			}
		case "resistance":
			// Resistance broken = last 2 closes above zone.High
			if recent[1].Close > z.High && recent[2].Close > z.High {
				z.Type = "support"
				z.Flipped = true
				z.FlipCount++
				z.Confidence += 10
				if z.Confidence > 100 {
					z.Confidence = 100
				}
			}
		}
	}

	// Re-assign type based on current price for zones that didn't flip
	for i := range zones {
		if zones[i].Flipped {
			continue
		}
		if zones[i].MidPrice < currentPrice {
			zones[i].Type = "support"
		} else {
			zones[i].Type = "resistance"
		}
	}

	return zones
}

// AssignZoneQualityGrade assigns A/B/C quality grades to zones.
func AssignZoneQualityGrade(zones []StructuralZone) []StructuralZone {
	for i := range zones {
		zones[i].QualityGrade = computeZoneGrade(zones[i])
	}
	return zones
}

func computeZoneGrade(z StructuralZone) string {
	if z.Confidence >= 60 && (z.MultiTFCount >= 2 || z.TouchCount >= 4) {
		return "A"
	}
	if z.Confidence >= 35 && (z.MultiTFCount >= 1 || z.TouchCount >= 2) {
		return "B"
	}
	return "C"
}

// FilterTopZonesForAI returns the top N zones per direction (support/resistance),
// prioritizing A > B grade, then by ATR distance (nearest first).
// C-grade zones are excluded.
func FilterTopZonesForAI(zones []StructuralZone, currentPrice float64, maxPerDirection int) []StructuralZone {
	if maxPerDirection <= 0 {
		maxPerDirection = 3
	}

	var support, resistance []StructuralZone
	for _, z := range zones {
		if z.QualityGrade == "C" {
			continue
		}
		if z.Type == "support" {
			support = append(support, z)
		} else {
			resistance = append(resistance, z)
		}
	}

	sortZonesByPriority(support, currentPrice)
	sortZonesByPriority(resistance, currentPrice)

	if len(support) > maxPerDirection {
		support = support[:maxPerDirection]
	}
	if len(resistance) > maxPerDirection {
		resistance = resistance[:maxPerDirection]
	}

	result := make([]StructuralZone, 0, len(support)+len(resistance))
	result = append(result, support...)
	result = append(result, resistance...)
	return result
}

func sortZonesByPriority(zones []StructuralZone, currentPrice float64) {
	sort.Slice(zones, func(i, j int) bool {
		// A before B
		if zones[i].QualityGrade != zones[j].QualityGrade {
			return zones[i].QualityGrade < zones[j].QualityGrade // "A" < "B"
		}
		// Same grade: nearest first
		distI := math.Abs(zones[i].MidPrice - currentPrice)
		distJ := math.Abs(zones[j].MidPrice - currentPrice)
		return distI < distJ
	})
}

// ApplyTimeframeBoost adds confidence bonus for higher timeframe zones.
func ApplyTimeframeBoost(zones []StructuralZone) []StructuralZone {
	for i := range zones {
		boost := timeframeBoost(zones[i].Timeframes)
		zones[i].Confidence += boost
		if zones[i].Confidence > 100 {
			zones[i].Confidence = 100
		}
	}
	return zones
}

func timeframeBoost(timeframes []string) float64 {
	var boost float64
	for _, tf := range timeframes {
		switch tf {
		case "4h":
			if boost < 15 {
				boost = 15
			}
		case "1d", "1D":
			if boost < 25 {
				boost = 25
			}
		}
	}
	return boost
}

// EnrichZoneMultiTF matches zones across timeframes and increments MultiTFCount.
func EnrichZoneMultiTF(zonesByTF map[string][]StructuralZone) {
	tfs := make([]string, 0, len(zonesByTF))
	for tf := range zonesByTF {
		tfs = append(tfs, tf)
	}

	for _, tfA := range tfs {
		for i := range zonesByTF[tfA] {
			za := &zonesByTF[tfA][i]
			confirmations := 0
			for _, tfB := range tfs {
				if tfB == tfA {
					continue
				}
				for _, zb := range zonesByTF[tfB] {
					if zonesOverlap(*za, zb) {
						confirmations++
						break
					}
				}
			}
			za.MultiTFCount = confirmations
		}
	}
}

func zonesOverlap(a, b StructuralZone) bool {
	return a.Low <= b.High && b.Low <= a.High
}

func containsStr(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// collectAllZones merges zones from all timeframes into a consolidated list,
// deduplicating overlapping zones and returning the top zones for AI consumption.
func collectAllZones(timeframeData map[string]*TimeframeSeriesData, currentPrice float64) []StructuralZone {
	var all []StructuralZone
	for _, sd := range timeframeData {
		if sd == nil {
			continue
		}
		all = append(all, sd.StructuralZones...)
	}
	if len(all) == 0 {
		return nil
	}

	// Merge overlapping zones across timeframes
	sort.Slice(all, func(i, j int) bool {
		return all[i].Low < all[j].Low
	})

	// Cap merged zone width. Without this, overlap-based chain merging across
	// timeframes grows a zone unbounded (WLD: 0.492–0.556, ~13%, collapsing the
	// whole resistance area into one indistinguishable block). A cross-TF zone may
	// be slightly wider than a single-TF one, but must stay tight: cap at 1.5% of
	// price (fix 2026-06-10).
	maxZoneWidth := currentPrice * 0.015
	if maxZoneWidth <= 0 {
		maxZoneWidth = math.Inf(1)
	}

	var merged []StructuralZone
	merged = append(merged, all[0])
	for i := 1; i < len(all); i++ {
		last := &merged[len(merged)-1]
		mergedHigh := last.High
		if all[i].High > mergedHigh {
			mergedHigh = all[i].High
		}
		if zonesOverlap(*last, all[i]) && (mergedHigh-last.Low) <= maxZoneWidth {
			// Merge into existing zone
			if all[i].High > last.High {
				last.High = all[i].High
			}
			last.TouchCount += all[i].TouchCount
			if all[i].Confidence > last.Confidence {
				last.Confidence = all[i].Confidence
			}
			if all[i].VolumeScore > last.VolumeScore {
				last.VolumeScore = all[i].VolumeScore
			}
			if all[i].MultiTFCount > last.MultiTFCount {
				last.MultiTFCount = all[i].MultiTFCount
			}
			for _, tf := range all[i].Timeframes {
				if !containsStr(last.Timeframes, tf) {
					last.Timeframes = append(last.Timeframes, tf)
				}
			}
			for _, src := range all[i].Sources {
				if !containsStr(last.Sources, src) {
					last.Sources = append(last.Sources, src)
				}
			}
			if all[i].Flipped {
				last.Flipped = true
				last.FlipCount += all[i].FlipCount
			}
			// Recalculate mid price
			last.MidPrice = (last.Low + last.High) / 2
		} else {
			merged = append(merged, all[i])
		}
	}

	// Assign type based on current price and re-grade
	for i := range merged {
		if !merged[i].Flipped {
			if merged[i].MidPrice < currentPrice {
				merged[i].Type = "support"
			} else {
				merged[i].Type = "resistance"
			}
		}
		merged[i].QualityGrade = computeZoneGrade(merged[i])
	}

	return merged
}
