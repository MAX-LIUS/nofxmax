package market

import (
	"fmt"
	"math"
	"sort"
)

// StructuralQuality is an aggregate 0-100 assessment of how clean and tradeable
// a symbol's market structure is, derived from the structure detectors already
// computed on Data. It is EVIDENCE to help prioritize candidates (and inform
// coin screening), never a hard filter — a low score means "messy structure,
// trade with care", not "forbidden".
type StructuralQuality struct {
	Score        float64  `json:"score"`         // 0-100 composite
	Grade        string   `json:"grade"`         // A/B/C/D
	Reasons      []string `json:"reasons"`       // human-readable drivers (bilingual handled at render)
	ZoneScore    float64  `json:"zone_score"`    // quality of nearby S/R zones
	ReactionScore float64 `json:"reaction_score"`// how strongly levels have been respected
	ClarityScore float64  `json:"clarity_score"` // trend-structure clarity (BOS/CHoCH present & aligned)
	ProfileScore float64  `json:"profile_score"` // volume-profile definition (clear value area)
	ConfluenceScore float64 `json:"confluence_score"` // multiple structure types agreeing near price
}

// Sub-score weights (sum = 1.0).
const (
	sqZoneWeight       = 0.30
	sqReactionWeight   = 0.25
	sqClarityWeight    = 0.20
	sqProfileWeight    = 0.15
	sqConfluenceWeight = 0.10
)

// CalculateStructuralQuality builds the aggregate structural-quality signal from
// a fully-populated Data (zones, structure breaks, volume profile). Returns nil
// when there is not enough structure to assess (avoids fabricating a score).
func CalculateStructuralQuality(data *Data) *StructuralQuality {
	if data == nil || data.CurrentPrice <= 0 {
		return nil
	}
	atr14 := primaryATR14FromData(data)
	if atr14 <= 0 {
		return nil
	}

	sq := &StructuralQuality{}
	var reasons []string

	// 1. Zone quality: nearby A/B zones with confidence and multi-TF backing.
	sq.ZoneScore, _ = scoreZoneQuality(data.StructuralZones, data.CurrentPrice, atr14, &reasons)

	// 2. Reaction strength: have nearby levels produced strong, respected reactions?
	sq.ReactionScore = scoreReactionQuality(data.StructuralZones, data.CurrentPrice, atr14, &reasons)

	// 3. Clarity: recent BOS/CHoCH give a readable trend structure.
	sq.ClarityScore = scoreStructureClarity(data.StructureBreaks, &reasons)

	// 4. Profile definition: a clear value area (not flat / undefined).
	sq.ProfileScore = scoreProfileDefinition(data.VolumeProfile, data.CurrentPrice, atr14, &reasons)

	// 5. Confluence: multiple structure types clustering near price.
	sq.ConfluenceScore = scoreConfluence(data, atr14, &reasons)

	sq.Score = math.Round(
		(sq.ZoneScore*sqZoneWeight +
			sq.ReactionScore*sqReactionWeight +
			sq.ClarityScore*sqClarityWeight +
			sq.ProfileScore*sqProfileWeight +
			sq.ConfluenceScore*sqConfluenceWeight) * 100,
	)
	sq.Grade = qualityGrade(sq.Score)
	sq.Reasons = reasons
	return sq
}

// scoreZoneQuality rewards nearby high-grade, high-confidence, multi-TF zones.
func scoreZoneQuality(zones []StructuralZone, price, atr14 float64, reasons *[]string) (float64, int) {
	if len(zones) == 0 {
		return 0, 0
	}
	best := 0.0
	nearCount := 0
	for _, z := range zones {
		dist := math.Abs(z.MidPrice-price) / atr14
		if dist > 6 {
			continue // too far to matter for near-term trading
		}
		nearCount++
		s := z.Confidence / 100.0 // 0-1
		switch z.QualityGrade {
		case "A":
			s += 0.25
		case "B":
			s += 0.10
		}
		if z.MultiTFCount >= 2 {
			s += 0.15
		}
		s = clamp01(s)
		// Proximity weight: structure sitting AT price is worth full value; a zone
		// 4+ ATR away is heavily discounted (it can't anchor a near-term entry).
		s *= proximityWeight(dist)
		if s > best {
			best = s
		}
	}
	if nearCount == 0 {
		return 0, 0
	}
	if best >= 0.7 {
		*reasons = append(*reasons, "clean nearby S/R zone (high grade/confidence)")
	}
	return best, nearCount
}

// proximityWeight decays from 1.0 at 0 ATR to ~0.2 at 6 ATR, so structure near
// price dominates the quality score and distant structure contributes little.
func proximityWeight(distATR float64) float64 {
	if distATR <= 0.5 {
		return 1.0
	}
	w := 1.0 - (distATR-0.5)/6.0
	if w < 0.15 {
		w = 0.15
	}
	return w
}

// scoreReactionQuality rewards nearby zones that produced strong ATR reactions.
func scoreReactionQuality(zones []StructuralZone, price, atr14 float64, reasons *[]string) float64 {
	if len(zones) == 0 {
		return 0
	}
	best := 0.0
	for _, z := range zones {
		dist := math.Abs(z.MidPrice-price) / atr14
		if dist > 6 {
			continue
		}
		// normalize reaction: 3xATR reaction -> full marks, proximity-weighted.
		r := clamp01(z.MaxReactionATR/3.0) * proximityWeight(dist)
		if r > best {
			best = r
		}
	}
	best = clamp01(best)
	if best >= 0.6 {
		*reasons = append(*reasons, "levels strongly respected (large ATR reactions)")
	}
	return best
}

// scoreStructureClarity rewards a readable trend structure (recent BOS same
// direction) and flags recent CHoCH as reduced clarity (regime in transition).
func scoreStructureClarity(breaks []StructureBreak, reasons *[]string) float64 {
	if len(breaks) == 0 {
		return 0.3 // no clear structure signal -> neutral-low
	}
	var bosBull, bosBear, choch int
	for _, b := range breaks {
		if b.Type == "CHOCH" {
			choch++
		}
		if b.Direction == "bullish" {
			bosBull++
		} else {
			bosBear++
		}
	}
	score := 0.5
	// aligned BOS in one direction -> clear trend
	if bosBull > 0 && bosBear == 0 {
		score = 0.9
		*reasons = append(*reasons, "clear bullish structure (aligned BOS)")
	} else if bosBear > 0 && bosBull == 0 {
		score = 0.9
		*reasons = append(*reasons, "clear bearish structure (aligned BOS)")
	} else if bosBull > 0 && bosBear > 0 {
		score = 0.5 // mixed
	}
	// recent CHoCH reduces clarity (transition)
	if choch > 0 {
		score -= 0.2
		*reasons = append(*reasons, "recent CHoCH — structure in transition")
	}
	return clamp01(score)
}

// scoreProfileDefinition rewards a well-defined value area (POC + reasonable
// VAH-VAL width). A flat/undefined profile scores low.
func scoreProfileDefinition(vp *VolumeProfile, price, atr14 float64, reasons *[]string) float64 {
	if vp == nil || vp.POC <= 0 || vp.VAH <= vp.VAL {
		return 0.2
	}
	vaWidthATR := (vp.VAH - vp.VAL) / atr14
	// A healthy value area spans a few ATR; too tight = illiquid, too wide = no structure.
	score := 0.5
	if vaWidthATR >= 1.0 && vaWidthATR <= 8.0 {
		score = 0.85
		*reasons = append(*reasons, "well-defined volume value area")
	} else if vaWidthATR > 8.0 {
		score = 0.4 // sprawling, low definition
	}
	return clamp01(score)
}

// scoreConfluence rewards multiple structure types clustering within ~1 ATR of
// each other near price (zone + FVG + liquidity pool + period level agreement).
func scoreConfluence(data *Data, atr14 float64, reasons *[]string) float64 {
	price := data.CurrentPrice
	var levels []float64
	for _, z := range data.StructuralZones {
		if math.Abs(z.MidPrice-price)/atr14 <= 4 {
			levels = append(levels, z.MidPrice)
		}
	}
	for _, f := range data.FairValueGaps {
		if !f.Filled && math.Abs(f.Mid-price)/atr14 <= 4 {
			levels = append(levels, f.Mid)
		}
	}
	for _, l := range data.LiquidityPools {
		if math.Abs(l.Price-price)/atr14 <= 4 {
			levels = append(levels, l.Price)
		}
	}
	if data.PeriodLevels != nil {
		for _, v := range []float64{data.PeriodLevels.PrevDayHigh, data.PeriodLevels.PrevDayLow, data.PeriodLevels.PrevWeekHigh, data.PeriodLevels.PrevWeekLow} {
			if v > 0 && math.Abs(v-price)/atr14 <= 4 {
				levels = append(levels, v)
			}
		}
	}
	if len(levels) < 2 {
		return 0.2
	}
	// count the largest cluster within 1 ATR
	sort.Float64s(levels)
	maxCluster := 1
	for i := 0; i < len(levels); i++ {
		count := 1
		for j := i + 1; j < len(levels); j++ {
			if levels[j]-levels[i] <= atr14 {
				count++
			} else {
				break
			}
		}
		if count > maxCluster {
			maxCluster = count
		}
	}
	score := clamp01(float64(maxCluster-1) / 3.0) // 4+ agreeing = full marks
	if maxCluster >= 3 {
		*reasons = append(*reasons, fmt.Sprintf("%d structure types confluent near price", maxCluster))
	}
	return score
}

// qualityGrade maps a 0-100 score to a letter grade.
func qualityGrade(score float64) string {
	switch {
	case score >= 75:
		return "A"
	case score >= 55:
		return "B"
	case score >= 35:
		return "C"
	default:
		return "D"
	}
}

// primaryATR14FromData extracts a representative ATR14 from Data, preferring the
// mid timeframes used elsewhere as the primary structural reference (matches the
// kernel formatter's extractPrimaryATR14 ordering for consistency).
func primaryATR14FromData(data *Data) float64 {
	if data == nil || data.TimeframeData == nil {
		return 0
	}
	for _, tf := range []string{"1h", "15m", "4h", "30m", "5m"} {
		if sd, ok := data.TimeframeData[tf]; ok && sd != nil && sd.ATR14 > 0 {
			return sd.ATR14
		}
	}
	// fallback: any positive ATR
	for _, sd := range data.TimeframeData {
		if sd != nil && sd.ATR14 > 0 {
			return sd.ATR14
		}
	}
	return 0
}
