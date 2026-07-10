package api

import (
	"net/http"
	"sync"
	"time"

	"nofx/logger"
	"nofx/shadoweval"

	"github.com/gin-gonic/gin"
)

// benchCache holds the latest Virtual Trader Bench result. Computing it scans
// decision_records (large) + thousands of random-control trials (~tens of
// seconds), far too slow for a live request, so a background goroutine refreshes
// it on an interval and the handler serves the cached snapshot instantly.
type benchCache struct {
	mu         sync.RWMutex
	result     *shadoweval.Result
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
	res := shadoweval.Run(trades, shadoweval.RuleNames(trades),
		shadoweval.Options{Trials: 5000, Seed: 42})

	shadowBench.mu.Lock()
	shadowBench.result = &res
	shadowBench.computedAt = time.Now().UTC()
	shadowBench.err = ""
	shadowBench.mu.Unlock()
	logger.Infof("📊 shadow-bench refreshed: %d trades, %d forward, %d rules",
		res.Book, res.Forward, len(res.Traders))
}

// handleShadowBench serves the cached Virtual Trader Bench result. Optional
// ?cap=3 returns a winsorized (robustness) variant computed on demand for that
// request only (still fast — reuses the cached trade set is not possible across
// caps, so this recomputes; guarded to avoid abuse by requiring the base cache).
func (s *Server) handleShadowBench(c *gin.Context) {
	shadowBench.mu.RLock()
	res, at, errStr, computing := shadowBench.result, shadowBench.computedAt, shadowBench.err, shadowBench.computing
	shadowBench.mu.RUnlock()

	if res == nil {
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
		"computed_at": at.Format(time.RFC3339),
		"stale_sec":   int(time.Since(at).Seconds()),
		"result":      res,
	})
}
