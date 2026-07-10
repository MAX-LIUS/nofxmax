package shadoweval

import (
	"math"
	"math/rand"
	"sort"
)

// Scorecard is one virtual trader's result over the shared book.
type Scorecard struct {
	Name     string    `json:"name"`
	NTrades  int       `json:"n_trades"`
	NBlocked int       `json:"n_blocked"`
	RetR     float64   `json:"final_r"`
	MaxDD    float64   `json:"max_dd"`
	Ulcer    float64   `json:"ulcer"`
	CVaR95   float64   `json:"cvar95"`
	Sortino  float64   `json:"sortino"`
	WinRate  float64   `json:"win_rate"`
	ExpR     float64   `json:"exp_r"`
	ProfitF  float64   `json:"profit_factor"`
	VsRand   float64   `json:"vs_rand"`
	H1       float64   `json:"h1"`
	H2       float64   `json:"h2"`
	CILo     float64   `json:"ci_lo"`
	CIHi     float64   `json:"ci_hi"`
	Pass     bool      `json:"pass"`
	Equity   []float64 `json:"equity"` // cumulative R curve (chronological)
}

// Options controls a bench run.
type Options struct {
	Trials int     // random-control trials
	Cap    float64 // winsorize |R| to Cap (0 = off)
	Seed   int64
}

// Result is the full bench output for the API/CLI.
type Result struct {
	Book      int         `json:"book"`
	REligible int         `json:"r_eligible"`
	Forward   int         `json:"forward_closed"`
	RP5       float64     `json:"r_p5"`
	RP50      float64     `json:"r_p50"`
	RP95      float64     `json:"r_p95"`
	Baseline  Scorecard   `json:"baseline"`
	Traders   []Scorecard `json:"traders"`
}

// Run simulates the baseline + one virtual trader per rule and returns ranked
// scorecards with significance. tradesMap is from Load().
func Run(tradesMap map[string]*Trade, rules []string, opt Options) Result {
	tr := make([]*Trade, 0, len(tradesMap))
	for _, t := range tradesMap {
		tr = append(tr, t)
	}
	sort.Slice(tr, func(i, j int) bool { return tr[i].EntryMs < tr[j].EntryMs })

	if opt.Cap > 0 {
		for _, t := range tr {
			if t.RMult > opt.Cap {
				t.RMult = opt.Cap
			} else if t.RMult < -opt.Cap {
				t.RMult = -opt.Cap
			}
		}
	}
	if opt.Trials == 0 {
		opt.Trials = 5000
	}
	rng := rand.New(rand.NewSource(opt.Seed))

	var rElig, fwd int
	var rvals []float64
	for _, t := range tr {
		if t.RiskUSD > 0 {
			rElig++
			rvals = append(rvals, t.RMult)
		}
		if t.Forward {
			fwd++
		}
	}
	sort.Float64s(rvals)
	pct := func(q float64) float64 {
		if len(rvals) == 0 {
			return 0
		}
		return rvals[int(q*float64(len(rvals)-1))]
	}

	base, _ := simulate("BASELINE(take-all)", tr, nil)
	res := Result{Book: len(tr), REligible: rElig, Forward: fwd,
		RP5: pct(0.05), RP50: pct(0.5), RP95: pct(0.95), Baseline: base}

	for _, rule := range rules {
		drop := dropSetFor(tr, rule)
		sc, kept := simulate(rule, tr, drop)
		sc.VsRand = randControl(tr, drop, opt.Trials, rng)
		sc.H1 = halfVsRand(tr, rule, true, opt.Trials, rng)
		sc.H2 = halfVsRand(tr, rule, false, opt.Trials, rng)
		sc.CILo, sc.CIHi = bootstrapCI(kept, 2000, rng)
		sc.Pass = sc.RetR > base.RetR && sc.VsRand > 95 && sc.H1 > 95 && sc.H2 > 95 && sc.CILo > 0
		res.Traders = append(res.Traders, sc)
	}
	sort.Slice(res.Traders, func(i, j int) bool { return res.Traders[i].RetR > res.Traders[j].RetR })
	return res
}

func simulate(name string, tr []*Trade, drop map[string]bool) (Scorecard, []float64) {
	var kept, equityCurve []float64
	var equity, peak, sumDD, maxDD, wins, grossWin, grossLoss float64
	var blocked int
	for _, t := range tr {
		if t.RiskUSD <= 0 {
			continue
		}
		if drop != nil && drop[t.Pkey] {
			blocked++
			continue
		}
		r := t.RMult
		kept = append(kept, r)
		equity += r
		equityCurve = append(equityCurve, equity)
		if equity > peak {
			peak = equity
		}
		dd := peak - equity
		if dd > maxDD {
			maxDD = dd
		}
		sumDD += dd * dd
		if r > 0 {
			wins++
			grossWin += r
		} else {
			grossLoss += -r
		}
	}
	sc := Scorecard{Name: name, NTrades: len(kept), NBlocked: blocked, RetR: equity, MaxDD: maxDD, Equity: equityCurve}
	if len(kept) > 0 {
		sc.Ulcer = math.Sqrt(sumDD / float64(len(kept)))
		sc.WinRate = 100 * wins / float64(len(kept))
		sc.ExpR = equity / float64(len(kept))
		sc.CVaR95 = cvar95(kept)
		sc.Sortino = sortinoR(kept)
	}
	if grossLoss > 0 {
		sc.ProfitF = grossWin / grossLoss
	} else if grossWin > 0 {
		sc.ProfitF = math.Inf(1)
	}
	return sc, kept
}

func dropSetFor(tr []*Trade, rule string) map[string]bool {
	drop := map[string]bool{}
	for _, t := range tr {
		if t.BlockedBy[rule] {
			drop[t.Pkey] = true
		}
	}
	return drop
}

func cvar95(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	s := append([]float64(nil), x...)
	sort.Float64s(s)
	k := int(math.Ceil(0.05 * float64(len(s))))
	if k < 1 {
		k = 1
	}
	var sum float64
	for i := 0; i < k; i++ {
		sum += s[i]
	}
	return sum / float64(k)
}

func sortinoR(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	var mean float64
	for _, v := range x {
		mean += v
	}
	mean /= float64(len(x))
	var dsq float64
	var n int
	for _, v := range x {
		if v < 0 {
			dsq += v * v
			n++
		}
	}
	if n == 0 {
		return math.Inf(1)
	}
	dd := math.Sqrt(dsq / float64(n))
	if dd == 0 {
		return 0
	}
	return mean / dd
}

func randControl(tr []*Trade, drop map[string]bool, trials int, rng *rand.Rand) float64 {
	var elig []*Trade
	for _, t := range tr {
		if t.RiskUSD > 0 {
			elig = append(elig, t)
		}
	}
	nDrop := 0
	for _, t := range elig {
		if drop[t.Pkey] {
			nDrop++
		}
	}
	if nDrop == 0 || nDrop >= len(elig) {
		return 0
	}
	gExp := keptExpR(elig, drop)
	better := 0
	for i := 0; i < trials; i++ {
		if gExp > keptExpR(elig, randomDrop(elig, nDrop, rng)) {
			better++
		}
	}
	return 100 * float64(better) / float64(trials)
}

func keptExpR(elig []*Trade, drop map[string]bool) float64 {
	var sum float64
	var n int
	for _, t := range elig {
		if drop[t.Pkey] {
			continue
		}
		sum += t.RMult
		n++
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

func randomDrop(elig []*Trade, n int, rng *rand.Rand) map[string]bool {
	idx := rng.Perm(len(elig))
	d := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		d[elig[idx[i]].Pkey] = true
	}
	return d
}

func halfVsRand(tr []*Trade, rule string, early bool, trials int, rng *rand.Rand) float64 {
	var elig []*Trade
	for _, t := range tr {
		if t.RiskUSD > 0 {
			elig = append(elig, t)
		}
	}
	if len(elig) < 20 {
		return 0
	}
	mid := len(elig) / 2
	half := elig[mid:]
	if early {
		half = elig[:mid]
	}
	drop := map[string]bool{}
	for _, t := range half {
		if t.BlockedBy[rule] {
			drop[t.Pkey] = true
		}
	}
	return randControl(half, drop, trials, rng)
}

func bootstrapCI(kept []float64, iters int, rng *rand.Rand) (lo, hi float64) {
	if len(kept) == 0 {
		return 0, 0
	}
	means := make([]float64, iters)
	for i := 0; i < iters; i++ {
		var s float64
		for j := 0; j < len(kept); j++ {
			s += kept[rng.Intn(len(kept))]
		}
		means[i] = s / float64(len(kept))
	}
	sort.Float64s(means)
	return means[int(0.05*float64(iters))], means[int(0.95*float64(iters))]
}
