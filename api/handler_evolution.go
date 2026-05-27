package api

import (
	"net/http"
	"nofx/store"

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
