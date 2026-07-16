package api

import (
	"math"
	"time"

	"nofx/logger"
	"nofx/trader/backtest"

	"github.com/gin-gonic/gin"
	"net/http"
)

// blocksim replayer: turns enforce-blocked open intents into counterfactual R by
// paper-trading each through the trader's REAL protection ladder using the
// backtest engine (validated ~2% PnL fidelity vs actual). Read-only over live
// trading; runs in the background so it never touches the decision path.

// startBlockSimReplayer periodically simulates newly-captured blocked intents.
func (s *Server) startBlockSimReplayer() {
	go func() {
		time.Sleep(30 * time.Second)
		s.replayPendingBlockedSims()
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for range t.C {
			s.replayPendingBlockedSims()
		}
	}()
}

func (s *Server) replayPendingBlockedSims() {
	defer func() {
		if r := recover(); r != nil {
			logger.Infof("⚠ blocksim replay panicked (non-blocking): %v", r)
		}
	}()
	db := s.store.DB()
	if db == nil {
		return
	}
	pending, err := s.store.BlockedSim().PendingForReplay(100)
	if err != nil || len(pending) == 0 {
		return
	}

	// Cache per-trader live protection params (one config load per trader).
	type cfgEntry struct {
		params backtest.ProtectionParams
		tf     string
		ok     bool
	}
	cfgCache := map[string]cfgEntry{}
	done, skipped := 0, 0

	for _, o := range pending {
		ce, seen := cfgCache[o.TraderID]
		if !seen {
			cfg, ctf, cerr := backtest.LoadTraderStrategyConfig(db, o.TraderID)
			if cerr != nil || cfg == nil {
				cfgCache[o.TraderID] = cfgEntry{ok: false}
				ce = cfgCache[o.TraderID]
			} else {
				ce = cfgEntry{params: backtest.LiveConfigParams(cfg, backtest.TimeframeHours(ctf)), tf: ctf, ok: true}
				cfgCache[o.TraderID] = ce
			}
		}
		if !ce.ok {
			_ = s.store.BlockedSim().UpdateSim(o.ID, map[string]interface{}{"sim_status": "skipped", "sim_reason": "no_live_config", "sim_at": time.Now().UTC().UnixMilli()})
			skipped++
			continue
		}

		e := backtest.Entry{
			Symbol:     o.Symbol,
			Side:       o.Side,
			EntryPrice: o.EntryPrice,
			EntryTime:  o.ObservedAt,
			ExitTime:   0, // open-ended: replay forward through protection until resolved / horizon
			Quantity:   o.Quantity,
		}
		loaded, skip := backtest.PrepareEntries([]backtest.Entry{e}, ce.tf, backtest.OKXBars)
		if skip > 0 || len(loaded) == 0 {
			_ = s.store.BlockedSim().UpdateSim(o.ID, map[string]interface{}{"sim_status": "skipped", "sim_reason": "no_bars", "sim_at": time.Now().UTC().UnixMilli()})
			skipped++
			continue
		}
		res := backtest.RunParams(ce.params, loaded)
		// RunParams aggregates; for a single entry the per-trade fields live on the
		// aggregate PnL. Recompute R from PnL and the intent's own risk.
		risk := math.Abs(o.EntryPrice-o.StopLoss) * o.Quantity
		simR := 0.0
		if risk > 0 {
			simR = res.TotalPnL / risk
		}
		_ = s.store.BlockedSim().UpdateSim(o.ID, map[string]interface{}{
			"sim_status": "done",
			"sim_pnl":    res.TotalPnL,
			"sim_r":      simR,
			"sim_at":     time.Now().UTC().UnixMilli(),
		})
		done++
	}
	if done > 0 || skipped > 0 {
		logger.Infof("🧪 blocksim replay: %d simulated, %d skipped (%d pending scanned)", done, skipped, len(pending))
	}
}

// handleBlockSim serves the blocked-open counterfactual outcomes: for each
// enforce-blocked open, whether the trade we skipped would have won or lost.
func (s *Server) handleBlockSim(c *gin.Context) {
	db := s.store.DB()
	if db == nil {
		c.JSON(http.StatusOK, gin.H{"ready": false})
		return
	}
	rows, err := db.Query(`SELECT symbol, side, blocked_by, sim_status, sim_r, sim_pnl, observed_at
		FROM blocked_sim_outcomes ORDER BY observed_at DESC LIMIT 300`)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ready": true, "rows": []interface{}{}, "error": err.Error()})
		return
	}
	defer rows.Close()
	type row struct {
		Symbol     string  `json:"symbol"`
		Side       string  `json:"side"`
		BlockedBy  string  `json:"blocked_by"`
		SimStatus  string  `json:"sim_status"`
		SimR       float64 `json:"sim_r"`
		SimPnL     float64 `json:"sim_pnl"`
		ObservedAt int64   `json:"observed_at"`
	}
	var out []row
	var doneN, goodBlock int
	var sumR float64
	for rows.Next() {
		var r row
		if rows.Scan(&r.Symbol, &r.Side, &r.BlockedBy, &r.SimStatus, &r.SimR, &r.SimPnL, &r.ObservedAt) != nil {
			continue
		}
		if r.SimStatus == "done" {
			doneN++
			sumR += r.SimR
			if r.SimR < 0 { // sim loss = the block correctly avoided a loser
				goodBlock++
			}
		}
		out = append(out, r)
	}
	avgR := 0.0
	goodPct := 0.0
	if doneN > 0 {
		avgR = sumR / float64(doneN)
		goodPct = 100 * float64(goodBlock) / float64(doneN)
	}
	c.JSON(http.StatusOK, gin.H{
		"ready": true, "rows": out,
		"summary": gin.H{"simulated": doneN, "avg_sim_r": avgR, "good_block_pct": goodPct,
			"note": "sim_r<0 => blocked a loser (good block); avg_sim_r = mean R of trades we skipped. Under live protection ladder, ~2% PnL fidelity; excludes portfolio-breadth and AI-discretionary closes."},
	})
}
