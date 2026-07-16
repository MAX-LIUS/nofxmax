package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// ShadowRuleView is the per-rule scorecard enriched with derived metrics for the
// monitoring page (avg PnL and win% per bucket + a simple "edge" = keepAvg-blockAvg).
type ShadowRuleView struct {
	RuleName  string  `json:"rule_name"`
	BlockN    int     `json:"block_n"`
	BlockPnL  float64 `json:"block_pnl"`
	BlockAvg  float64 `json:"block_avg"`
	BlockWin  float64 `json:"block_win_pct"`
	KeepN     int     `json:"keep_n"`
	KeepPnL   float64 `json:"keep_pnl"`
	KeepAvg   float64 `json:"keep_avg"`
	KeepWin   float64 `json:"keep_win_pct"`
	PendingN  int     `json:"pending_n"`
	// Edge>0 means the rule blocks trades that are worse (lower avg PnL) than the
	// ones it keeps — i.e. it is separating losers from the rest.
	Edge float64 `json:"edge"`
}

// handleShadowGateStats returns the forward-data scorecard for every candidate
// shadow rule (observation-only gates), optionally scoped to one trader.
func (s *Server) handleShadowGateStats(c *gin.Context) {
	traderID := c.Query("trader_id")
	stats, err := s.store.ShadowGate().RuleStats(traderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to compute shadow stats"})
		return
	}
	views := make([]ShadowRuleView, 0, len(stats))
	for _, st := range stats {
		v := ShadowRuleView{
			RuleName: st.RuleName,
			BlockN:   st.BlockN, BlockPnL: round2(st.BlockPnL),
			KeepN: st.KeepN, KeepPnL: round2(st.KeepPnL),
			PendingN: st.PendingN,
		}
		if st.BlockN > 0 {
			v.BlockAvg = round4(st.BlockPnL / float64(st.BlockN))
			v.BlockWin = round1(100 * float64(st.BlockWin) / float64(st.BlockN))
		}
		if st.KeepN > 0 {
			v.KeepAvg = round4(st.KeepPnL / float64(st.KeepN))
			v.KeepWin = round1(100 * float64(st.KeepWin) / float64(st.KeepN))
		}
		v.Edge = round4(v.KeepAvg - v.BlockAvg)
		views = append(views, v)
	}
	c.JSON(http.StatusOK, gin.H{"rules": views, "total": len(views)})
}

// ShadowVerdictView is one recent verdict for the live feed.
type ShadowVerdictView struct {
	Time       string  `json:"time"`
	TraderID   string  `json:"trader_id"`
	Cycle      int64   `json:"cycle"`
	Symbol     string  `json:"symbol"`
	Side       string  `json:"side"`
	Rule       string  `json:"rule"`
	WouldBlock bool    `json:"would_block"`
	Regime     string  `json:"regime"`
	Confidence float64 `json:"confidence"`
	Detail     string  `json:"detail"`
	LiveAllowed bool   `json:"live_allowed"`
}

// handleShadowGateFeed returns the most recent shadow verdicts (live feed).
func (s *Server) handleShadowGateFeed(c *gin.Context) {
	traderID := c.Query("trader_id")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	rows, err := s.store.ShadowGate().RecentVerdicts(traderID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch verdicts"})
		return
	}
	out := make([]ShadowVerdictView, 0, len(rows))
	for _, r := range rows {
		out = append(out, ShadowVerdictView{
			Time:        time.UnixMilli(r.ObservedAt).UTC().Format(time.RFC3339),
			TraderID:    r.TraderID,
			Cycle:       r.Cycle,
			Symbol:      r.Symbol,
			Side:        r.Side,
			Rule:        r.RuleName,
			WouldBlock:  r.WouldBlock,
			Regime:      r.Regime,
			Confidence:  r.Confidence,
			Detail:      r.Detail,
			LiveAllowed: r.LiveAllowed,
		})
	}
	c.JSON(http.StatusOK, gin.H{"verdicts": out, "total": len(out)})
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }
func round4(f float64) float64 {
	if f < 0 {
		return -float64(int(-f*10000+0.5)) / 10000
	}
	return float64(int(f*10000+0.5)) / 10000
}
