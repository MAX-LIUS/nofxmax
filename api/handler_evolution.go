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
