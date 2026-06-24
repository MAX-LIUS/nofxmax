package backtest

// Candidate is a named ATR-multiple parameter set to evaluate.
type Candidate struct {
	Name string
	SL   float64
	TP1  float64
	TP2  float64
	BE1  float64
	BE2  float64
}

// ToParams builds ATR-mode ProtectionParams from the candidate, keeping Claude's
// close ratios / BE offsets / DD give-back fixed (same as buildATRParams).
func (c Candidate) ToParams() ProtectionParams {
	return buildATRParams(c.SL, c.TP1, c.TP2, c.BE1, c.BE2)
}

// CandidateEval reports how a candidate performs on a sample and where it ranks
// within the full grid sweep of that same sample.
type CandidateEval struct {
	Name          string
	Result        PortfolioResult
	RankByPnL     int     // 1-based rank among all grid points (by TotalPnL)
	GridSize      int     // total grid points
	PercentileTop float64 // 0..100, lower = better (top X%)
	BestPnL       float64 // best grid point's PnL (for gap reference)
}

// EvaluateCandidate runs the candidate on the loaded entries and computes its
// rank within the full DefaultATRGrid sweep of the same entries.
func EvaluateCandidate(c Candidate, loaded []loadedEntry) CandidateEval {
	res := RunParams(c.ToParams(), loaded)
	sweep := Sweep(DefaultATRGrid(), loaded)
	rank := len(sweep) + 1
	best := 0.0
	if len(sweep) > 0 {
		best = sweep[0].Result.TotalPnL
	}
	for i, p := range sweep {
		if res.TotalPnL >= p.Result.TotalPnL {
			rank = i + 1
			break
		}
	}
	pct := 100.0
	if len(sweep) > 0 {
		pct = float64(rank) / float64(len(sweep)) * 100
	}
	return CandidateEval{
		Name:          c.Name,
		Result:        res,
		RankByPnL:     rank,
		GridSize:      len(sweep),
		PercentileTop: pct,
		BestPnL:       best,
	}
}
