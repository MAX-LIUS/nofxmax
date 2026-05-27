package store

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"
)

// EvolutionFactor represents a single scoring dimension for a coin+direction.
type EvolutionFactor struct {
	Name       string  `json:"name"`
	Score      float64 `json:"score"`       // 0-100, 50=neutral
	SampleSize int     `json:"sample_size"` // number of trades contributing
	Confidence float64 `json:"confidence"`  // 0-1, based on sample size
	Insight    string  `json:"insight"`
	UpdatedAt  int64   `json:"updated_at"` // unix ms
}

// Adaptation represents a personalized trading adjustment generated from factor analysis.
type Adaptation struct {
	Condition     string  `json:"condition"`      // e.g. "phase=extension", "trigger_tf=15m"
	Action        string  `json:"action"`         // e.g. "require_confidence_85,reduce_size_50%"
	Reason        string  `json:"reason"`
	Effectiveness float64 `json:"effectiveness"`  // 0-1, how well this adaptation works
	CreatedAt     int64   `json:"created_at"`     // unix ms
	ExpiresAt     int64   `json:"expires_at"`     // unix ms, 0=no expiry
	Contradictions int    `json:"contradictions"` // consecutive contradicting trades
}

// CoinEvolutionProfile is the stored profile for a coin+direction combination.
type CoinEvolutionProfile struct {
	ID          int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	TraderID    string `gorm:"column:trader_id;not null;uniqueIndex:idx_evo_trader_symbol_side" json:"trader_id"`
	Symbol      string `gorm:"column:symbol;not null;uniqueIndex:idx_evo_trader_symbol_side" json:"symbol"`
	Side        string `gorm:"column:side;not null;uniqueIndex:idx_evo_trader_symbol_side" json:"side"` // "long" or "short"
	FactorsJSON string `gorm:"column:factors_json;type:text" json:"-"`
	AdaptJSON   string `gorm:"column:adaptations_json;type:text" json:"-"`
	SampleSize  int    `gorm:"column:sample_size;default:0" json:"sample_size"`
	Version     int    `gorm:"column:version;default:1" json:"version"`
	UpdatedAt   int64  `gorm:"column:updated_at" json:"updated_at"`
	CreatedAt   int64  `gorm:"column:created_at" json:"created_at"`
}

func (CoinEvolutionProfile) TableName() string {
	return "coin_evolution_profiles"
}

func (p *CoinEvolutionProfile) GetFactors() []EvolutionFactor {
	if p.FactorsJSON == "" {
		return nil
	}
	var factors []EvolutionFactor
	_ = json.Unmarshal([]byte(p.FactorsJSON), &factors)
	return factors
}

func (p *CoinEvolutionProfile) SetFactors(factors []EvolutionFactor) {
	data, _ := json.Marshal(factors)
	p.FactorsJSON = string(data)
}

func (p *CoinEvolutionProfile) GetAdaptations() []Adaptation {
	if p.AdaptJSON == "" {
		return nil
	}
	var adaptations []Adaptation
	_ = json.Unmarshal([]byte(p.AdaptJSON), &adaptations)
	return adaptations
}

func (p *CoinEvolutionProfile) SetAdaptations(adaptations []Adaptation) {
	data, _ := json.Marshal(adaptations)
	p.AdaptJSON = string(data)
}

// EvolutionStore handles persistence of evolution profiles.
type EvolutionStore struct {
	db *gorm.DB
}

func NewEvolutionStore(db *gorm.DB) *EvolutionStore {
	return &EvolutionStore{db: db}
}

func (s *EvolutionStore) AutoMigrate() error {
	return s.db.AutoMigrate(&CoinEvolutionProfile{})
}

func (s *EvolutionStore) GetProfile(traderID, symbol, side string) (*CoinEvolutionProfile, error) {
	var profile CoinEvolutionProfile
	err := s.db.Where("trader_id = ? AND symbol = ? AND side = ?", traderID, symbol, side).First(&profile).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	return &profile, err
}

func (s *EvolutionStore) GetAllProfiles(traderID string) ([]CoinEvolutionProfile, error) {
	var profiles []CoinEvolutionProfile
	err := s.db.Where("trader_id = ?", traderID).Find(&profiles).Error
	return profiles, err
}

func (s *EvolutionStore) SaveProfile(profile *CoinEvolutionProfile) error {
	if profile.ID == 0 {
		profile.CreatedAt = time.Now().UTC().UnixMilli()
		profile.UpdatedAt = profile.CreatedAt
		return s.db.Create(profile).Error
	}
	profile.UpdatedAt = time.Now().UTC().UnixMilli()
	profile.Version++
	return s.db.Save(profile).Error
}

func (s *EvolutionStore) DeleteProfile(traderID, symbol, side string) error {
	return s.db.Where("trader_id = ? AND symbol = ? AND side = ?", traderID, symbol, side).Delete(&CoinEvolutionProfile{}).Error
}

// ══════════════════════════════════════════════════════════════════════
// Factor Scoring Engine
// ══════════════════════════════════════════════════════════════════════

const (
	FactorTrendPhaseFit          = "trend_phase_fit"
	FactorEMA20Alignment         = "ema20_alignment"
	FactorTriggerQuality         = "trigger_quality"
	FactorMomentumSweetSpot      = "momentum_sweet_spot"
	FactorHoldDurationFit        = "hold_duration_fit"
	FactorProtectionEffectiveness = "protection_effectiveness"
	FactorTimeOfDay              = "time_of_day"
	FactorVolatilityRegime       = "volatility_regime"
)

// SceneTagsData is the parsed version of EntrySceneTags JSON.
type SceneTagsData struct {
	TrendPhase  string  `json:"trend_phase"`
	Regime      string  `json:"regime"`
	Chg4h       float64 `json:"chg4h"`
	Chg1h       float64 `json:"chg1h"`
	EMA20Dev    float64 `json:"ema20_dev"`
	Direction   string  `json:"direction"`
	TriggerType string  `json:"trigger_type,omitempty"`
}

// TradeOutcome is a simplified trade record for factor computation.
type TradeOutcome struct {
	Symbol      string
	Side        string
	PnLPct      float64
	IsWin       bool
	CloseTime   time.Time
	EntryTime   time.Time
	SceneTags   SceneTagsData
	CloseReason string
}

// halfLifeDays controls how quickly old trades lose weight.
const halfLifeDays = 14.0

// adaptationTTLDays is how long an adaptation stays active without validation.
const adaptationTTLDays = 30

// minSampleForAdaptation is the minimum trades needed to generate an adaptation.
const minSampleForAdaptation = 5

// ComputeFactors calculates all factor scores from a set of trade outcomes.
func ComputeFactors(trades []TradeOutcome) []EvolutionFactor {
	if len(trades) == 0 {
		return nil
	}

	now := time.Now()
	factors := make([]EvolutionFactor, 0, 8)

	// Factor 1: Trend Phase Fit
	factors = append(factors, computeTrendPhaseFit(trades, now))

	// Factor 2: EMA20 Alignment
	factors = append(factors, computeEMA20Alignment(trades, now))

	// Factor 3: Momentum Sweet Spot
	factors = append(factors, computeMomentumSweetSpot(trades, now))

	// Factor 4: Hold Duration Fit
	factors = append(factors, computeHoldDurationFit(trades, now))

	// Factor 5: Protection Effectiveness
	factors = append(factors, computeProtectionEffectiveness(trades, now))

	// Factor 6: Time of Day
	factors = append(factors, computeTimeOfDay(trades, now))

	// Factor 7: Volatility Regime
	factors = append(factors, computeVolatilityRegime(trades, now))

	// Factor 8: Trigger Quality
	factors = append(factors, computeTriggerQuality(trades, now))

	return factors
}

func computeTrendPhaseFit(trades []TradeOutcome, now time.Time) EvolutionFactor {
	// Score based on win rate in establishment vs extension phases
	var estWins, estTotal, extWins, extTotal float64
	for _, t := range trades {
		w := decayWeight(t.CloseTime, now)
		switch t.SceneTags.TrendPhase {
		case "establishment":
			estTotal += w
			if t.IsWin {
				estWins += w
			}
		case "extension", "exhaustion":
			extTotal += w
			if t.IsWin {
				extWins += w
			}
		}
	}

	// Score: high if establishment works well, low if extension causes losses
	score := 50.0
	insight := ""
	sampleSize := 0
	for _, t := range trades {
		if t.SceneTags.TrendPhase != "" {
			sampleSize++
		}
	}

	if estTotal > 0 {
		estWinRate := estWins / estTotal * 100
		score = estWinRate // establishment win rate IS the score
		insight = fmt.Sprintf("establishment胜率%.0f%%", estWinRate)
	}
	if extTotal > 1 {
		extWinRate := extWins / extTotal * 100
		if extWinRate < 35 {
			insight += fmt.Sprintf(", extension胜率仅%.0f%%", extWinRate)
		}
	}

	return EvolutionFactor{
		Name:       FactorTrendPhaseFit,
		Score:      clampScore(score),
		SampleSize: sampleSize,
		Confidence: sampleConfidence(sampleSize),
		Insight:    insight,
		UpdatedAt:  now.UnixMilli(),
	}
}

func computeEMA20Alignment(trades []TradeOutcome, now time.Time) EvolutionFactor {
	var alignedWins, alignedTotal, conflictWins, conflictTotal float64
	for _, t := range trades {
		w := decayWeight(t.CloseTime, now)
		// Aligned: long with positive EMA20 dev, short with negative
		aligned := (t.Side == "long" && t.SceneTags.EMA20Dev >= -0.5) ||
			(t.Side == "short" && t.SceneTags.EMA20Dev <= 0.5)
		if aligned {
			alignedTotal += w
			if t.IsWin {
				alignedWins += w
			}
		} else {
			conflictTotal += w
			if t.IsWin {
				conflictWins += w
			}
		}
	}

	score := 50.0
	insight := ""
	if alignedTotal > 0 && conflictTotal > 0 {
		alignedWR := alignedWins / alignedTotal * 100
		conflictWR := conflictWins / conflictTotal * 100
		score = alignedWR - conflictWR + 50 // positive = alignment matters
		insight = fmt.Sprintf("EMA20一致胜率%.0f%%, 矛盾胜率%.0f%%", alignedWR, conflictWR)
	} else if alignedTotal > 0 {
		score = alignedWins / alignedTotal * 100
		insight = fmt.Sprintf("EMA20一致胜率%.0f%%", score)
	}

	return EvolutionFactor{
		Name:       FactorEMA20Alignment,
		Score:      clampScore(score),
		SampleSize: len(trades),
		Confidence: sampleConfidence(len(trades)),
		Insight:    insight,
		UpdatedAt:  now.UnixMilli(),
	}
}

func computeMomentumSweetSpot(trades []TradeOutcome, now time.Time) EvolutionFactor {
	// Bucket by |4h change|: 0-1%, 1-2%, 2-3%, 3%+
	type bucket struct{ wins, total float64 }
	buckets := [4]bucket{}
	for _, t := range trades {
		w := decayWeight(t.CloseTime, now)
		abs4h := t.SceneTags.Chg4h
		if abs4h < 0 {
			abs4h = -abs4h
		}
		idx := 0
		switch {
		case abs4h >= 3:
			idx = 3
		case abs4h >= 2:
			idx = 2
		case abs4h >= 1:
			idx = 1
		}
		buckets[idx].total += w
		if t.IsWin {
			buckets[idx].wins += w
		}
	}

	// Find best bucket
	bestIdx := 0
	bestWR := 0.0
	for i, b := range buckets {
		if b.total > 0 {
			wr := b.wins / b.total
			if wr > bestWR {
				bestWR = wr
				bestIdx = i
			}
		}
	}

	labels := []string{"0-1%", "1-2%", "2-3%", "3%+"}
	score := bestWR * 100
	insight := fmt.Sprintf("最佳4h动量区间: %s (胜率%.0f%%)", labels[bestIdx], bestWR*100)

	// Penalize if 3%+ bucket has very low win rate
	if buckets[3].total > 1 {
		wr3 := buckets[3].wins / buckets[3].total * 100
		if wr3 < 30 {
			insight += fmt.Sprintf(", 4h>3%%胜率仅%.0f%%", wr3)
		}
	}

	return EvolutionFactor{
		Name:       FactorMomentumSweetSpot,
		Score:      clampScore(score),
		SampleSize: len(trades),
		Confidence: sampleConfidence(len(trades)),
		Insight:    insight,
		UpdatedAt:  now.UnixMilli(),
	}
}

func computeHoldDurationFit(trades []TradeOutcome, now time.Time) EvolutionFactor {
	// Bucket by hold duration: <1h, 1-4h, 4-12h, 12h+
	type bucket struct{ wins, total float64 }
	buckets := [4]bucket{}
	for _, t := range trades {
		w := decayWeight(t.CloseTime, now)
		dur := t.CloseTime.Sub(t.EntryTime).Hours()
		idx := 0
		switch {
		case dur >= 12:
			idx = 3
		case dur >= 4:
			idx = 2
		case dur >= 1:
			idx = 1
		}
		buckets[idx].total += w
		if t.IsWin {
			buckets[idx].wins += w
		}
	}

	bestIdx := 0
	bestWR := 0.0
	for i, b := range buckets {
		if b.total > 0 {
			wr := b.wins / b.total
			if wr > bestWR {
				bestWR = wr
				bestIdx = i
			}
		}
	}

	labels := []string{"<1h", "1-4h", "4-12h", "12h+"}
	score := bestWR * 100
	insight := fmt.Sprintf("最佳持仓时长: %s (胜率%.0f%%)", labels[bestIdx], bestWR*100)

	return EvolutionFactor{
		Name:       FactorHoldDurationFit,
		Score:      clampScore(score),
		SampleSize: len(trades),
		Confidence: sampleConfidence(len(trades)),
		Insight:    insight,
		UpdatedAt:  now.UnixMilli(),
	}
}

func computeProtectionEffectiveness(trades []TradeOutcome, now time.Time) EvolutionFactor {
	// Analyze which close reasons correlate with better outcomes
	var slCount, tpCount, drawdownCount float64
	var slPnL, tpPnL, drawdownPnL float64
	for _, t := range trades {
		w := decayWeight(t.CloseTime, now)
		switch {
		case contains(t.CloseReason, "sl") || contains(t.CloseReason, "stop"):
			slCount += w
			slPnL += t.PnLPct * w
		case contains(t.CloseReason, "tp") || contains(t.CloseReason, "profit"):
			tpCount += w
			tpPnL += t.PnLPct * w
		case contains(t.CloseReason, "drawdown") || contains(t.CloseReason, "trailing"):
			drawdownCount += w
			drawdownPnL += t.PnLPct * w
		}
	}

	// Score based on TP/SL ratio effectiveness
	score := 50.0
	insight := ""
	if tpCount > 0 && slCount > 0 {
		avgTP := tpPnL / tpCount
		avgSL := slPnL / slCount
		if avgSL < 0 {
			ratio := avgTP / (-avgSL)
			score = math.Min(ratio*25, 100) // ratio of 2 = score 50, ratio of 4 = score 100
			insight = fmt.Sprintf("TP均值+%.1f%%, SL均值%.1f%%, 比率%.1f", avgTP, avgSL, ratio)
		}
	}

	return EvolutionFactor{
		Name:       FactorProtectionEffectiveness,
		Score:      clampScore(score),
		SampleSize: len(trades),
		Confidence: sampleConfidence(len(trades)),
		Insight:    insight,
		UpdatedAt:  now.UnixMilli(),
	}
}

func computeTimeOfDay(trades []TradeOutcome, now time.Time) EvolutionFactor {
	// Bucket by entry hour (UTC): 0-6, 6-12, 12-18, 18-24
	type bucket struct{ wins, total float64 }
	buckets := [4]bucket{}
	for _, t := range trades {
		w := decayWeight(t.CloseTime, now)
		hour := t.EntryTime.UTC().Hour()
		idx := hour / 6
		if idx > 3 {
			idx = 3
		}
		buckets[idx].total += w
		if t.IsWin {
			buckets[idx].wins += w
		}
	}

	bestIdx := 0
	bestWR := 0.0
	for i, b := range buckets {
		if b.total > 0 {
			wr := b.wins / b.total
			if wr > bestWR {
				bestWR = wr
				bestIdx = i
			}
		}
	}

	labels := []string{"00-06 UTC", "06-12 UTC", "12-18 UTC", "18-24 UTC"}
	score := bestWR * 100
	insight := fmt.Sprintf("最佳时段: %s (胜率%.0f%%)", labels[bestIdx], bestWR*100)

	return EvolutionFactor{
		Name:       FactorTimeOfDay,
		Score:      clampScore(score),
		SampleSize: len(trades),
		Confidence: sampleConfidence(len(trades)),
		Insight:    insight,
		UpdatedAt:  now.UnixMilli(),
	}
}

func computeVolatilityRegime(trades []TradeOutcome, now time.Time) EvolutionFactor {
	// Use |chg4h| as volatility proxy. Bucket: low(<1%), medium(1-2.5%), high(>2.5%)
	type bucket struct{ wins, total float64 }
	buckets := [3]bucket{}
	for _, t := range trades {
		w := decayWeight(t.CloseTime, now)
		absChg := t.SceneTags.Chg4h
		if absChg < 0 {
			absChg = -absChg
		}
		idx := 0
		switch {
		case absChg >= 2.5:
			idx = 2
		case absChg >= 1.0:
			idx = 1
		}
		buckets[idx].total += w
		if t.IsWin {
			buckets[idx].wins += w
		}
	}

	bestIdx := 0
	bestWR := 0.0
	for i, b := range buckets {
		if b.total > 0 {
			wr := b.wins / b.total
			if wr > bestWR {
				bestWR = wr
				bestIdx = i
			}
		}
	}

	labels := []string{"低波动(<1%)", "中波动(1-2.5%)", "高波动(>2.5%)"}
	score := bestWR * 100
	insight := fmt.Sprintf("最佳波动率: %s (胜率%.0f%%)", labels[bestIdx], bestWR*100)

	// Penalize high volatility if win rate is very low
	if buckets[2].total > 1 {
		wr := buckets[2].wins / buckets[2].total * 100
		if wr < 30 {
			insight += fmt.Sprintf(", 高波动胜率仅%.0f%%", wr)
		}
	}

	return EvolutionFactor{
		Name:       FactorVolatilityRegime,
		Score:      clampScore(score),
		SampleSize: len(trades),
		Confidence: sampleConfidence(len(trades)),
		Insight:    insight,
		UpdatedAt:  now.UnixMilli(),
	}
}

func computeTriggerQuality(trades []TradeOutcome, now time.Time) EvolutionFactor {
	// Group by trigger type category: rejection (support/resistance), breakout (retest/confirmed), none
	type bucket struct{ wins, total float64 }
	buckets := map[string]*bucket{}
	for _, t := range trades {
		w := decayWeight(t.CloseTime, now)
		triggerCat := categorizeTrigger(t.SceneTags.TriggerType)
		if _, ok := buckets[triggerCat]; !ok {
			buckets[triggerCat] = &bucket{}
		}
		buckets[triggerCat].total += w
		if t.IsWin {
			buckets[triggerCat].wins += w
		}
	}

	// Find best and worst trigger categories
	bestCat := ""
	bestWR := 0.0
	worstCat := ""
	worstWR := 1.0
	sampleSize := 0
	for cat, b := range buckets {
		if b.total < 1 {
			continue
		}
		wr := b.wins / b.total
		if wr > bestWR {
			bestWR = wr
			bestCat = cat
		}
		if wr < worstWR {
			worstWR = wr
			worstCat = cat
		}
		sampleSize += int(b.total)
	}

	score := bestWR * 100
	insight := ""
	if bestCat != "" {
		insight = fmt.Sprintf("最佳trigger: %s (胜率%.0f%%)", bestCat, bestWR*100)
	}
	if worstCat != "" && worstWR < 0.35 && worstCat != bestCat {
		insight += fmt.Sprintf(", %s胜率仅%.0f%%", worstCat, worstWR*100)
	}

	return EvolutionFactor{
		Name:       FactorTriggerQuality,
		Score:      clampScore(score),
		SampleSize: sampleSize,
		Confidence: sampleConfidence(sampleSize),
		Insight:    insight,
		UpdatedAt:  now.UnixMilli(),
	}
}

func categorizeTrigger(triggerType string) string {
	switch triggerType {
	case "support_rejection_confirmed", "resistance_rejection_confirmed":
		return "rejection"
	case "resistance_breakout_retest_successful", "support_breakdown_retest_failed":
		return "breakout_retest"
	case "higher_low_breakout_confirmed", "lower_high_breakdown_confirmed":
		return "structure_break"
	default:
		return "unknown"
	}
}

// ══════════════════════════════════════════════════════════════════════
// Adaptation Generation
// ══════════════════════════════════════════════════════════════════════

// GenerateAdaptations creates personalized trading adjustments from factor scores.
func GenerateAdaptations(factors []EvolutionFactor) []Adaptation {
	now := time.Now().UTC().UnixMilli()
	ttl := int64(adaptationTTLDays * 24 * 60 * 60 * 1000)
	var adaptations []Adaptation

	for _, f := range factors {
		if f.SampleSize < minSampleForAdaptation {
			continue
		}
		if f.Score > 35 && f.Score < 65 {
			continue // neutral range, no adaptation needed
		}

		switch f.Name {
		case FactorTrendPhaseFit:
			if f.Score < 35 {
				adaptations = append(adaptations, Adaptation{
					Condition:     "phase=extension",
					Action:        "require_confidence_85,reduce_size_50%",
					Reason:        f.Insight,
					Effectiveness: 0.7,
					CreatedAt:     now,
					ExpiresAt:     now + ttl,
				})
			}
		case FactorEMA20Alignment:
			if f.Score < 35 {
				adaptations = append(adaptations, Adaptation{
					Condition:     "ema20_contradicted",
					Action:        "require_confidence_90,reduce_size_30%",
					Reason:        f.Insight,
					Effectiveness: 0.8,
					CreatedAt:     now,
					ExpiresAt:     now + ttl,
				})
			}
		case FactorMomentumSweetSpot:
			if f.Score < 35 {
				adaptations = append(adaptations, Adaptation{
					Condition:     "chg4h_gt_2.5",
					Action:        "require_confidence_85,reduce_size_50%",
					Reason:        f.Insight,
					Effectiveness: 0.6,
					CreatedAt:     now,
					ExpiresAt:     now + ttl,
				})
			}
		case FactorTriggerQuality:
			if f.Score < 35 {
				adaptations = append(adaptations, Adaptation{
					Condition:     "trigger_low_quality",
					Action:        "require_confidence_85,reduce_size_50%",
					Reason:        f.Insight,
					Effectiveness: 0.65,
					CreatedAt:     now,
					ExpiresAt:     now + ttl,
				})
			}
		}
	}

	return adaptations
}

// PruneExpiredAdaptations removes adaptations that have expired or been contradicted too many times.
func PruneExpiredAdaptations(adaptations []Adaptation) []Adaptation {
	now := time.Now().UTC().UnixMilli()
	result := make([]Adaptation, 0, len(adaptations))
	for _, a := range adaptations {
		if a.ExpiresAt > 0 && now > a.ExpiresAt {
			continue // expired
		}
		if a.Contradictions >= 3 {
			continue // contradicted too many times
		}
		result = append(result, a)
	}
	return result
}

// ══════════════════════════════════════════════════════════════════════
// Helpers
// ══════════════════════════════════════════════════════════════════════

func decayWeight(tradeTime time.Time, now time.Time) float64 {
	daysAgo := now.Sub(tradeTime).Hours() / 24
	return math.Exp(-daysAgo / halfLifeDays * math.Ln2)
}

func sampleConfidence(n int) float64 {
	if n <= 0 {
		return 0
	}
	// Asymptotic: 5 trades = 0.5, 10 = 0.7, 20 = 0.85, 50 = 0.95
	return 1 - math.Exp(-float64(n)/10.0)
}

func clampScore(s float64) float64 {
	if s < 0 {
		return 0
	}
	if s > 100 {
		return 100
	}
	return s
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && findSubstring(s, substr))
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// BuildEvolutionContext generates a compact prompt snippet for AI consumption.
func BuildEvolutionContext(profile *CoinEvolutionProfile) string {
	if profile == nil || profile.SampleSize < 3 {
		return ""
	}

	factors := profile.GetFactors()
	adaptations := profile.GetAdaptations()
	if len(factors) == 0 {
		return ""
	}

	// Compute overall fitness score (average of all factors)
	totalScore := 0.0
	count := 0
	for _, f := range factors {
		if f.SampleSize >= 3 {
			totalScore += f.Score
			count++
		}
	}
	if count == 0 {
		return ""
	}
	avgScore := totalScore / float64(count)

	result := fmt.Sprintf("适配度: %.0f/100 (样本%d笔)", avgScore, profile.SampleSize)

	// Add top insights — only the most actionable ones
	insightCount := 0
	for _, f := range factors {
		if insightCount >= 3 {
			break
		}
		if f.Insight != "" && f.SampleSize >= minSampleForAdaptation && (f.Score < 35 || f.Score > 65) {
			result += " | " + f.Insight
			insightCount++
		}
	}

	// Add active adaptations as explicit constraints
	pruned := PruneExpiredAdaptations(adaptations)
	if len(pruned) > 0 {
		result += " | 约束: "
		for i, a := range pruned {
			if i > 0 {
				result += "; "
			}
			result += a.Condition + "→" + a.Action
			if a.Reason != "" && len(a.Reason) < 40 {
				result += "(" + a.Reason + ")"
			}
		}
	}

	return result
}
