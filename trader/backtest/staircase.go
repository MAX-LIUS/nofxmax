package backtest

import "math"

// This file scores an equity path by how much it resembles a STAIRCASE — steady
// climbs punctuated by short, shallow pauses — rather than by final PnL alone.
//
// Ranking by PnL answers a different question. A path that doubles and then halves
// can beat a path that grinds steadily upward on final PnL while being far worse to
// hold. The objective「稳定爬楼梯」is explicitly about the shape of the path, so the
// objective function has to price the shape.

// CurveQuality is the shape report for one equity path.
type CurveQuality struct {
	Start float64
	End   float64
	// ReturnPct is the total return over the path.
	ReturnPct float64
	// MaxDDPct is the deepest peak-to-trough drawdown, in percent.
	MaxDDPct float64
	// UlcerIndex is the root-mean-square drawdown across every sample, in percent.
	//
	// This is the core staircase metric: unlike MaxDD it penalises drawdowns for
	// their DURATION as well as their depth, so a path that spends weeks 10% under
	// water scores far worse than one that dips 10% for an hour. A true staircase
	// has a very low Ulcer Index because it is almost always at a new high.
	UlcerIndex float64
	// MartinRatio is ReturnPct / UlcerIndex: return earned per unit of time spent
	// under water. This is the ranking metric for「稳定爬楼梯」.
	MartinRatio float64
	// TimeAtHighPct is the share of samples within 0.5% of the running peak — the
	// literal "standing on a step" fraction.
	TimeAtHighPct float64
	// UpFrac is the share of steps that were non-negative.
	UpFrac float64
	// WorstStepPct is the single worst sample-to-sample move.
	WorstStepPct float64
}

// TimeInMarketPct is the share of samples during which the account was actually
// tracking the curve rather than sitting frozen.
//
// This must be reported alongside any staircase score. A rule that sits out most of
// the record can show a beautiful Ulcer Index while barely participating, and the
// flat frozen stretches themselves count as "at the high", which flatters every
// smoothness metric. Without this column an optimiser silently converges on "stop
// trading" and presents it as a well-tuned breaker.
func TimeInMarketPct(active int, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(active) / float64(total) * 100
}

// AnalyzeCurve computes the shape report for an equity path.
func AnalyzeCurve(curve []float64) CurveQuality {
	q := CurveQuality{}
	if len(curve) < 2 {
		return q
	}
	q.Start, q.End = curve[0], curve[len(curve)-1]
	if q.Start > 0 {
		q.ReturnPct = (q.End/q.Start - 1) * 100
	}
	peak := curve[0]
	var sumSqDD float64
	var atHigh, ups int
	worst := 0.0
	for i, v := range curve {
		if v > peak {
			peak = v
		}
		dd := 0.0
		if peak > 0 {
			dd = (peak - v) / peak * 100
		}
		sumSqDD += dd * dd
		if dd > q.MaxDDPct {
			q.MaxDDPct = dd
		}
		if dd <= 0.5 {
			atHigh++
		}
		if i > 0 && curve[i-1] > 0 {
			step := (v/curve[i-1] - 1) * 100
			if step >= 0 {
				ups++
			}
			if step < worst {
				worst = step
			}
		}
	}
	n := float64(len(curve))
	q.UlcerIndex = math.Sqrt(sumSqDD / n)
	q.TimeAtHighPct = float64(atHigh) / n * 100
	q.UpFrac = float64(ups) / (n - 1) * 100
	q.WorstStepPct = worst
	switch {
	case q.UlcerIndex > 1e-9:
		q.MartinRatio = q.ReturnPct / q.UlcerIndex
	case q.ReturnPct > 0:
		// Zero drawdown with a positive return is the ideal staircase and must rank
		// ABOVE everything else. Guarding this to 0 (the obvious way to avoid the
		// division) inverted the ranking: a flawless monotone climb scored 0 and lost
		// to a path that doubled and halved. No real curve has zero Ulcer, so this
		// never affected a sweep result, but the metric was wrong at its own optimum.
		q.MartinRatio = math.Inf(1)
	case q.ReturnPct < 0:
		q.MartinRatio = math.Inf(-1)
	}
	return q
}

// FreezePath applies the user's rule to an equity curve and returns the resulting
// equity path, so its SHAPE can be scored rather than just its endpoint.
//
// The rule, exactly as specified:「仓位涨到 +armPct% 才挂上熔断，之后从峰值回撤
// ddPct% 就全仓熔断（权益价值冻结），冻结点后重新开始新一周期」.
//
//   - The breaker arms only once the current cycle has gained armPct% over its
//     baseline. Before that the account rides unprotected, which is the intended
//     「达不到一定比例就各自仓位控制」branch.
//   - On firing, the account stops tracking the curve for cooldownBars samples
//     (the value is crystallised), then resumes with both the drawdown reference
//     and the cycle baseline reset to the frozen equity — a new cycle.
//
// The re-entry delay is an explicit parameter because it must be: without one, a
// freeze that never resumes is not a breaker but a permanent exit, and on a
// declining curve that trivially "wins" for reasons unrelated to the trigger.
func FreezePath(curve []float64, armPct, ddPct float64, cooldownBars int) ([]float64, int) {
	p, n, _ := FreezePathActive(curve, armPct, ddPct, cooldownBars)
	return p, n
}

// FreezePathActive is FreezePath plus the number of samples the account was active
// (not frozen), so time-in-market can be reported.
func FreezePathActive(curve []float64, armPct, ddPct float64, cooldownBars int) ([]float64, int, int) {
	if len(curve) == 0 {
		return nil, 0, 0
	}
	path := make([]float64, 0, len(curve))
	equity := curve[0]
	base := equity // current cycle baseline
	peak := equity
	armed := armPct <= 0
	frozenFor := -1
	freezes := 0
	active := 0
	path = append(path, equity)
	for i := 1; i < len(curve); i++ {
		if curve[i-1] <= 0 {
			path = append(path, equity)
			continue
		}
		if frozenFor >= 0 {
			frozenFor++
			if frozenFor >= cooldownBars {
				frozenFor = -1
				base, peak = equity, equity
				armed = armPct <= 0
			}
			path = append(path, equity)
			continue
		}
		active++
		equity *= curve[i] / curve[i-1]
		if equity > peak {
			peak = equity
		}
		if !armed && armPct > 0 && base > 0 && equity >= base*(1+armPct/100) {
			armed = true
		}
		if armed && peak > 0 && (peak-equity)/peak*100 >= ddPct {
			frozenFor = 0
			freezes++
		}
		path = append(path, equity)
	}
	return path, freezes, active
}

// StaircaseRow is one (arm, dd) cell scored on both halves of the record.
type StaircaseRow struct {
	ArmPct   float64
	DDPct    float64
	Cooldown int
	Train    CurveQuality
	Test     CurveQuality
	Full     CurveQuality
	Freezes  int
	// TimeInMktPct is the share of the FULL record the account was active. A high
	// staircase score with a low value here means "barely traded", not "well tuned".
	TimeInMktPct float64
}

// SweepStaircase scores the arm x drawdown grid by staircase quality, on a
// chronological train/test split of the equity curve.
//
// Both halves are always reported. A parameter that only looks good on the half it
// was chosen from is not a setting, it is a coincidence, and for this objective
// that distinction matters more than usual: smoothness is exactly the property that
// is easiest to fit by accident on a short record.
func SweepStaircase(curve []float64, arms, dds []float64, cooldowns []int) []StaircaseRow {
	if len(curve) < 8 {
		return nil
	}
	mid := len(curve) / 2
	first, second := curve[:mid], curve[mid:]
	var out []StaircaseRow
	for _, a := range arms {
		for _, d := range dds {
			for _, cd := range cooldowns {
				pf, nf := FreezePath(first, a, d, cd)
				ps, _ := FreezePath(second, a, d, cd)
				pfull, nAll, act := FreezePathActive(curve, a, d, cd)
				out = append(out, StaircaseRow{
					ArmPct: a, DDPct: d, Cooldown: cd,
					Train: AnalyzeCurve(pf), Test: AnalyzeCurve(ps),
					Full: AnalyzeCurve(pfull), Freezes: nAll,
					TimeInMktPct: TimeInMarketPct(act, len(curve)-1),
				})
				_ = nf
			}
		}
	}
	return out
}
