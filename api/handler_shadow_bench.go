package api

import (
	"net/http"
	"sync"
	"time"

	"nofx/logger"
	"nofx/shadoweval"

	"github.com/gin-gonic/gin"
)

// benchCache holds the latest Virtual Trader Bench results, one per data segment
// (all / backfill / forward). Computing scans decision_records (large) + thousands
// of random-control trials (~tens of seconds), far too slow for a live request, so
// a background goroutine refreshes on an interval and the handler serves the cached
// snapshot instantly. All three segments come from a single Load per refresh.
type benchCache struct {
	mu         sync.RWMutex
	results    map[string]*shadoweval.Result // "all" | "backfill" | "forward"
	computedAt time.Time
	computing  bool
	err        string
}

var shadowBench = &benchCache{}

// startShadowBenchRefresher kicks off periodic recomputation. Read-only over the
// DB; never touches trading. First run fires shortly after boot so the monitor
// has data without waiting a full interval.
func (s *Server) startShadowBenchRefresher() {
	go func() {
		time.Sleep(20 * time.Second) // let startup settle
		s.refreshShadowBench()
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for range t.C {
			s.refreshShadowBench()
		}
	}()
}

func (s *Server) refreshShadowBench() {
	shadowBench.mu.Lock()
	if shadowBench.computing {
		shadowBench.mu.Unlock()
		return
	}
	shadowBench.computing = true
	shadowBench.mu.Unlock()

	defer func() {
		shadowBench.mu.Lock()
		shadowBench.computing = false
		shadowBench.mu.Unlock()
		if r := recover(); r != nil {
			logger.Infof("⚠ shadow-bench recompute panicked (non-blocking): %v", r)
		}
	}()

	db := s.store.DB()
	if db == nil {
		return
	}
	trades, err := shadoweval.Load(db)
	if err != nil {
		shadowBench.mu.Lock()
		shadowBench.err = err.Error()
		shadowBench.mu.Unlock()
		logger.Infof("⚠ shadow-bench load failed: %v", err)
		return
	}
	rules := shadoweval.RuleNames(trades)
	out := map[string]*shadoweval.Result{}
	for _, seg := range []string{"all", "backfill", "forward"} {
		r := shadoweval.Run(trades, rules, shadoweval.Options{Trials: 5000, Seed: 42, Segment: seg})
		out[seg] = &r
	}

	shadowBench.mu.Lock()
	shadowBench.results = out
	shadowBench.computedAt = time.Now().UTC()
	shadowBench.err = ""
	shadowBench.mu.Unlock()
	logger.Infof("📊 shadow-bench refreshed: all=%d bkf=%d fwd=%d, %d rules",
		out["all"].Book, out["backfill"].Book, out["forward"].Book, len(rules))
}

// handleShadowBench serves a cached Virtual Trader Bench result for the requested
// data segment (?segment=all|backfill|forward, default all). Backfill = pre-deploy
// in-sample history (price/trend gates only; conf gates n/a). Forward = live OOS.
func (s *Server) handleShadowBench(c *gin.Context) {
	seg := c.DefaultQuery("segment", "all")
	if seg != "all" && seg != "backfill" && seg != "forward" {
		seg = "all"
	}
	shadowBench.mu.RLock()
	results, at, errStr, computing := shadowBench.results, shadowBench.computedAt, shadowBench.err, shadowBench.computing
	shadowBench.mu.RUnlock()

	if results == nil || results[seg] == nil {
		c.JSON(http.StatusOK, gin.H{
			"ready":     false,
			"computing": computing,
			"error":     errStr,
			"message":   "bench is computing; retry shortly",
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ready":       true,
		"segment":     seg,
		"computed_at": at.Format(time.RFC3339),
		"stale_sec":   int(time.Since(at).Seconds()),
		"result":      results[seg],
	})
}
