package api

import (
	"encoding/json"
	"net/http"
	"nofx/logger"
	"nofx/store"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// handleEvolutionProfiles returns all evolution profiles for a trader.
func (s *Server) handleEvolutionProfiles(c *gin.Context) {
	traderID := c.Query("trader_id")
	if traderID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "trader_id is required"})
		return
	}

	profiles, err := s.store.Evolution().GetAllProfiles(traderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch evolution profiles"})
		return
	}

	// Convert to response format with parsed factors and adaptations
	type ProfileResponse struct {
		Symbol      string                 `json:"symbol"`
		Side        string                 `json:"side"`
		SampleSize  int                    `json:"sample_size"`
		Version     int                    `json:"version"`
		UpdatedAt   int64                  `json:"updated_at"`
		Factors     []store.EvolutionFactor `json:"factors"`
		Adaptations []store.Adaptation     `json:"adaptations"`
	}

	response := make([]ProfileResponse, 0, len(profiles))
	for _, p := range profiles {
		response = append(response, ProfileResponse{
			Symbol:      p.Symbol,
			Side:        p.Side,
			SampleSize:  p.SampleSize,
			Version:     p.Version,
			UpdatedAt:   p.UpdatedAt,
			Factors:     p.GetFactors(),
			Adaptations: p.GetAdaptations(),
		})
	}

	c.JSON(http.StatusOK, response)
}

// handleResetEvolutionProfile deletes a specific evolution profile, forcing re-learning.
func (s *Server) handleResetEvolutionProfile(c *gin.Context) {
	var req struct {
		TraderID string `json:"trader_id"`
		Symbol   string `json:"symbol"`
		Side     string `json:"side"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.TraderID == "" || req.Symbol == "" || req.Side == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "trader_id, symbol, and side are required"})
		return
	}

	if err := s.store.Evolution().DeleteProfile(req.TraderID, req.Symbol, req.Side); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reset profile"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "profile reset successfully", "symbol": req.Symbol, "side": req.Side})
}

// handleRebuildEvolutionProfiles recalculates evolution profiles for all coin+side
// combinations that have enough closed trades. Used for initial backfill.
func (s *Server) handleRebuildEvolutionProfiles(c *gin.Context) {
	traderID := c.Param("id")
	if traderID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "trader_id is required"})
		return
	}

	// Get all distinct symbol+side combinations with enough trades
	type symbolSide struct {
		Symbol string
		Side   string
	}
	var pairs []symbolSide
	s.store.GormDB().Raw(`
		SELECT symbol, side FROM trader_positions
		WHERE trader_id = ? AND status = 'CLOSED'
		GROUP BY symbol, side
		HAVING COUNT(*) >= 3
	`, traderID).Scan(&pairs)

	rebuilt := 0
	for _, pair := range pairs {
		normalizedSide := strings.ToLower(pair.Side)
		if normalizedSide != "long" && normalizedSide != "short" {
			continue
		}

		trades, err := s.store.Position().GetClosedTradesForEvolution(traderID, pair.Symbol, normalizedSide, 30)
		if err != nil || len(trades) < 3 {
			continue
		}

		outcomes := make([]store.TradeOutcome, 0, len(trades))
		for _, t := range trades {
			outcome := store.TradeOutcome{
				Symbol:      t.Symbol,
				Side:        normalizedSide,
				IsWin:       t.RealizedPnL > 0,
				CloseTime:   time.UnixMilli(t.ExitTime),
				EntryTime:   time.UnixMilli(t.EntryTime),
				CloseReason: t.CloseReason,
			}
			if t.EntryPrice > 0 && t.ExitPrice > 0 {
				if normalizedSide == "long" {
					outcome.PnLPct = (t.ExitPrice - t.EntryPrice) / t.EntryPrice * 100
				} else {
					outcome.PnLPct = (t.EntryPrice - t.ExitPrice) / t.EntryPrice * 100
				}
			}
			if t.EntrySceneTags != "" {
				var tags store.SceneTagsData
				if err := json.Unmarshal([]byte(t.EntrySceneTags), &tags); err == nil {
					outcome.SceneTags = tags
				}
			}
			outcomes = append(outcomes, outcome)
		}

		factors := store.ComputeFactors(outcomes)
		if len(factors) == 0 {
			continue
		}
		adaptations := store.GenerateAdaptations(factors)

		profile, _ := s.store.Evolution().GetProfile(traderID, pair.Symbol, normalizedSide)
		if profile == nil {
			profile = &store.CoinEvolutionProfile{
				TraderID: traderID,
				Symbol:   pair.Symbol,
				Side:     normalizedSide,
			}
		}
		profile.SetFactors(factors)
		profile.SetAdaptations(adaptations)
		profile.SampleSize = len(trades)

		if err := s.store.Evolution().SaveProfile(profile); err != nil {
			logger.Infof("⚠️ Evolution rebuild failed for %s %s: %v", pair.Symbol, normalizedSide, err)
			continue
		}
		rebuilt++
	}

	logger.Infof("🧬 Evolution profiles rebuilt for trader %s: %d profiles from %d pairs", traderID, rebuilt, len(pairs))
	c.JSON(http.StatusOK, gin.H{
		"message":  "evolution profiles rebuilt",
		"rebuilt":  rebuilt,
		"total":    len(pairs),
	})
}
