package backtest

import (
	"sort"
)

// SweepPoint is one parameter combination and its portfolio outcome.
type SweepPoint struct {
	SLATR    float64
	TP1ATR   float64
	TP2ATR   float64
	BE1ATR   float64
	BE2ATR   float64
	Result   PortfolioResult
}

// ATRGrid defines the candidate ATR multiples to sweep over each dimension.
// Keeps close ratios and DD give-back fixed at Claude's baseline (the question
// is "what ATR distances", not "what ratios").
type ATRGrid struct {
	SL  []float64 // stop-loss ATR multiples
	TP1 []float64 // first TP ATR multiples
	TP2 []float64 // second TP ATR multiples
	BE1 []float64 // BE tier-1 trigger ATR multiples
	BE2 []float64 // BE tier-2 trigger ATR multiples
}

// DefaultATRGrid is the wide search grid. Ranges are extended beyond the prior
// optimum (SL 4.0 / TP2 6.0 were at the old boundary) so the best point is an
// interior optimum, not a grid-edge artifact.
func DefaultATRGrid() ATRGrid {
	return ATRGrid{
		SL:  []float64{2.0, 2.5, 3.0, 3.5, 4.0, 4.5, 5.0, 6.0},
		TP1: []float64{1.0, 1.5, 2.0, 2.5, 3.0, 3.5},
		TP2: []float64{3.0, 4.0, 5.0, 6.0, 7.0, 8.0, 10.0},
		BE1: []float64{0.5, 1.0, 1.5, 2.0, 2.5},
		BE2: []float64{2.0, 2.5, 3.0, 3.5, 4.0},
	}
}

// buildATRParams builds an ATR-mode ProtectionParams from a grid point, keeping
// Claude's close ratios / DD give-back / BE offsets fixed.
func buildATRParams(sl, tp1, tp2, be1, be2 float64) ProtectionParams {
	return ProtectionParams{
		Unit:        UnitATRMult,
		StopLossATR: sl,
		TPLegs: []LadderLeg{
			{ATRMult: tp1, CloseRatioPct: 35},
			{ATRMult: tp2, CloseRatioPct: 25},
		},
		BELegs: []BELeg{
			{TriggerATR: be1, OffsetATR: 0.1, CloseRatioPct: 50},
			{TriggerATR: be2, OffsetATR: 0.3, CloseRatioPct: 35},
		},
		DDRules: []DDRule{
			// DD still arms on profit in ATR terms; give-back stays 40% of peak.
			{MinProfitATR: tp2, MaxDrawdownPct: 40, CloseRatioPct: 45},
		},
	}
}

// Sweep runs the full ATR grid over the pre-loaded entries and returns points
// sorted best-first by TotalPnL. Skips combos where tp2<=tp1 or be2<=be1.
func Sweep(grid ATRGrid, loaded []loadedEntry) []SweepPoint {
	var points []SweepPoint
	for _, sl := range grid.SL {
		for _, tp1 := range grid.TP1 {
			for _, tp2 := range grid.TP2 {
				if tp2 <= tp1 {
					continue
				}
				for _, be1 := range grid.BE1 {
					for _, be2 := range grid.BE2 {
						if be2 <= be1 {
							continue
						}
						p := buildATRParams(sl, tp1, tp2, be1, be2)
						res := RunParams(p, loaded)
						// Drop per-trade detail to keep the sweep memory-light: only
						// aggregate metrics are needed for ranking. Retaining Results
						// for every grid point × hundreds of trades blows up RAM and
						// OOM-kills the process (SIGKILL/137).
						res.Results = nil
						points = append(points, SweepPoint{
							SLATR: sl, TP1ATR: tp1, TP2ATR: tp2, BE1ATR: be1, BE2ATR: be2,
							Result: res,
						})
					}
				}
			}
		}
	}
	sort.Slice(points, func(i, j int) bool {
		return points[i].Result.TotalPnL > points[j].Result.TotalPnL
	})
	return points
}
