package api

import (
	"net/http"
	"nofx/logger"
	"nofx/store"
	"time"

	"github.com/gin-gonic/gin"
)

// handleCreateEquityAdjustment records a deposit or withdrawal
func (s *Server) handleCreateEquityAdjustment(c *gin.Context) {
	userID := c.GetString("user_id")
	traderID := c.Param("id")

	var req struct {
		Amount      float64 `json:"amount" binding:"required"`
		Type        string  `json:"type" binding:"required,oneof=deposit withdrawal"`
		Description string  `json:"description"`
		Timestamp   string  `json:"timestamp"` // Optional, defaults to now
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
		return
	}

	// Verify trader ownership
	traderConfig, err := s.store.Trader().GetFullConfig(userID, traderID)
	if err != nil {
		logger.Infof("❌ Trader not found: %v", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Trader not found"})
		return
	}

	// Parse timestamp
	timestamp := time.Now()
	if req.Timestamp != "" {
		parsedTime, err := time.Parse(time.RFC3339, req.Timestamp)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid timestamp format, use RFC3339"})
			return
		}
		timestamp = parsedTime
	}

	// Normalize amount sign based on type
	amount := req.Amount
	if req.Type == "withdrawal" && amount > 0 {
		amount = -amount
	} else if req.Type == "deposit" && amount < 0 {
		amount = -amount
	}

	adjustment := &store.EquityAdjustment{
		TraderID:    traderID,
		Timestamp:   timestamp,
		Amount:      amount,
		Type:        req.Type,
		Description: req.Description,
	}

	if err := s.store.EquityAdjustment().Create(adjustment); err != nil {
		logger.Infof("❌ Failed to create equity adjustment: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to record adjustment"})
		return
	}

	logger.Infof("✅ Recorded equity adjustment for trader %s (%s): %s %.2f USDT",
		traderConfig.Trader.Name, traderID, req.Type, req.Amount)

	c.JSON(http.StatusOK, gin.H{
		"message":    "Equity adjustment recorded",
		"adjustment": adjustment,
	})
}

// handleGetEquityAdjustments gets all adjustments for a trader
func (s *Server) handleGetEquityAdjustments(c *gin.Context) {
	userID := c.GetString("user_id")
	traderID := c.Param("id")

	// Verify trader ownership
	_, err := s.store.Trader().GetFullConfig(userID, traderID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Trader not found"})
		return
	}

	adjustments, err := s.store.EquityAdjustment().GetByTrader(traderID)
	if err != nil {
		logger.Infof("❌ Failed to get equity adjustments: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get adjustments"})
		return
	}

	// Calculate totals
	totalDeposits := 0.0
	totalWithdrawals := 0.0
	for _, adj := range adjustments {
		if adj.Amount > 0 {
			totalDeposits += adj.Amount
		} else {
			totalWithdrawals += -adj.Amount
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"adjustments":        adjustments,
		"total_deposits":     totalDeposits,
		"total_withdrawals":  totalWithdrawals,
		"net_adjustments":    totalDeposits - totalWithdrawals,
	})
}

// handleDeleteEquityAdjustment deletes an adjustment record
func (s *Server) handleDeleteEquityAdjustment(c *gin.Context) {
	userID := c.GetString("user_id")
	traderID := c.Param("id")

	var req struct {
		AdjustmentID int64 `json:"adjustment_id" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	// Verify trader ownership
	_, err := s.store.Trader().GetFullConfig(userID, traderID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Trader not found"})
		return
	}

	if err := s.store.EquityAdjustment().Delete(req.AdjustmentID); err != nil {
		logger.Infof("❌ Failed to delete equity adjustment: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete adjustment"})
		return
	}

	logger.Infof("✅ Deleted equity adjustment %d for trader %s", req.AdjustmentID, traderID)

	c.JSON(http.StatusOK, gin.H{"message": "Adjustment deleted"})
}
