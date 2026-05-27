package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// GateBlockEntry represents a single gate-blocked trade for the API response.
type GateBlockEntry struct {
	Timestamp    string   `json:"timestamp"`
	CycleNumber  int      `json:"cycle_number"`
	Symbol       string   `json:"symbol"`
	Action       string   `json:"action"`
	Confidence   int      `json:"confidence,omitempty"`
	BlockedBy    string   `json:"blocked_by"`
	BlockReason  string   `json:"block_reason"`
	FailedChecks []string `json:"failed_checks,omitempty"`
	Regime       string   `json:"regime,omitempty"`
	GateScore    int      `json:"gate_score,omitempty"`
}

// handleGateBlocks returns recent gate-blocked trades for a trader.
func (s *Server) handleGateBlocks(c *gin.Context) {
	traderID := c.Query("trader_id")
	if traderID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "trader_id is required"})
		return
	}

	limitStr := c.DefaultQuery("limit", "50")
	limit, _ := strconv.Atoi(limitStr)
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	// Fetch recent decisions (more than limit since not all have blocks)
	decisions, err := s.store.Decision().GetLatestRecords(traderID, limit*3)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch decisions"})
		return
	}

	blocks := make([]GateBlockEntry, 0)
	for _, d := range decisions {
		for _, a := range d.Decisions {
			if a.ReviewContext == nil || a.ReviewContext.Control == nil {
				continue
			}
			ctrl := a.ReviewContext.Control
			if ctrl.Decision != "rejected" {
				continue
			}

			entry := GateBlockEntry{
				Timestamp:   d.Timestamp.Format(time.RFC3339),
				CycleNumber: d.CycleNumber,
				Symbol:      a.Symbol,
				Action:      a.Action,
				Confidence:  a.Confidence,
				BlockReason: a.Error,
			}

			if len(ctrl.FailedChecks) > 0 {
				entry.BlockedBy = ctrl.FailedChecks[0]
				entry.FailedChecks = ctrl.FailedChecks
			}
			if ctrl.RegimeCurrent != "" {
				entry.Regime = ctrl.RegimeCurrent
			}

			blocks = append(blocks, entry)
			if len(blocks) >= limit {
				break
			}
		}
		if len(blocks) >= limit {
			break
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"blocks": blocks,
		"total":  len(blocks),
	})
}
