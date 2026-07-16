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
	// ConfInvalid marks a conf-gated rule whose would_block is meaningless in the
	// current segment (backfill sees conf=0 so it never fires). UIs should show
	// its numbers as "n/a for this segment", not a real zero-effect verdict.
	ConfInvalid bool `json:"conf_invalid"`
}

// Options controls a bench run.
type Options struct {
	Trials  int     // random-control trials
	Cap     float64 // winsorize |R| to Cap (0 = off)
	Seed    int64
	Segment string // "" or "all" = both; "backfill" = pre-deploy only; "forward" = live OOS only
}

// confDependentRules never fire in the backfill segment: the backfill reconstructs
// verdicts from price alone with conf=0, and these rules gate on conf>0 && conf<T.
// Their would_block is therefore always false in backfill and only meaningful in
// the forward segment. Flagged in Result so analyses don't misread the zeros.
var confDependentRules = map[string]bool{
	"chop_lowconf_lt70": true,
	"chop_lowconf_lt80": true,
}

// segmentFilter returns the subset of trades belonging to the requested segment.
// forward = recorded live (Trade.Forward); backfill = the rest.
func segmentFilter(tr []*Trade, seg string) []*Trade {
	switch seg {
	case "forward":
		out := tr[:0:0]
		for _, t := range tr {
			if t.Forward {
				out = append(out, t)
			}
		}
		return out
	case "backfill":
		out := tr[:0:0]
		for _, t := range tr {
			if !t.Forward {
				out = append(out, t)
			}
		}
		return out
	default:
		return tr
	}
}

// ConfLayer is one AI-confidence bucket's global calibration: does higher AI
// confidence actually earn higher R? (Empirically, not always.)
type ConfLayer struct {
	Name    string  `json:"name"` // "80~89"
	NTrades int     `json:"n_trades"`
	ExpR    float64 `json:"exp_r"`
	WinRate float64 `json:"win_rate"`
	SumR    float64 `json:"sum_r"`
}

// GateConfCell is one (rule × confidence layer) cell. It answers both:
//
//	(A) does this gate block the RIGHT trades in this layer? -> BlockExpR/KeepExpR/Edge
//	(B) if this gate ran ONLY within this layer, does it beat taking the whole
//	    layer? -> LayerBaseExpR/KeptExpR/Lift
type GateConfCell struct {
	Layer         string  `json:"layer"`
	BlockN        int     `json:"block_n"`
	BlockExpR     float64 `json:"block_exp_r"`
	KeepN         int     `json:"keep_n"`
	KeepExpR      float64 `json:"keep_exp_r"`
	Edge          float64 `json:"edge"`             // KeepExpR - BlockExpR (>0 = blocked the worse trades)
	LayerBaseExpR float64 `json:"layer_base_exp_r"` // take-all in this layer
	Lift          float64 `json:"lift"`             // KeepExpR - LayerBaseExpR (>0 = gate helps in this layer)
}

// GateConf is one gate's confidence breakdown across layers.
type GateConf struct {
	Name  string         `json:"name"`
	Cells []GateConfCell `json:"cells"`
}

// Result is the full bench output for the API/CLI.
type Result struct {
	Segment    string      `json:"segment"`    // "all" | "backfill" | "forward"
	Book       int         `json:"book"`       // trades in this segment
	REligible  int         `json:"r_eligible"` // with recoverable R in this segment
	Forward    int         `json:"forward_closed"`
	Backfill   int         `json:"backfill_closed"`
	RP5        float64     `json:"r_p5"`
	RP50       float64     `json:"r_p50"`
	RP95       float64     `json:"r_p95"`
	Baseline   Scorecard   `json:"baseline"`
	Traders    []Scorecard `json:"traders"`
	ConfLayers []ConfLayer `json:"conf_layers"` // global AI-confidence calibration
	GateConf   []GateConf  `json:"gate_conf"`   // per-gate × confidence-layer effect
}

// Run simulates the baseline + one virtual trader per rule and returns ranked
// scorecards with significance. tradesMap is from Load().
func Run(tradesMap map[string]*Trade, rules []string, opt Options) Result {
	tr := make([]*Trade, 0, len(tradesMap))
	for _, t := range tradesMap {
		tr = append(tr, t)
	}
	sort.Slice(tr, func(i, j int) bool { return tr[i].EntryMs < tr[j].EntryMs })

	seg := opt.Segment
	if seg == "" {
		seg = "all"
	}
	tr = segmentFilter(tr, seg)
	backfillEval := seg != "forward" // conf gates are only valid when forward is included exclusively

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

	var rElig, fwd, bkf int
	var rvals []float64
	for _, t := range tr {
		if t.RiskUSD > 0 {
			rElig++
			rvals = append(rvals, t.RMult)
		}
		if t.Forward {
			fwd++
		} else {
			bkf++
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
	res := Result{Segment: seg, Book: len(tr), REligible: rElig, Forward: fwd, Backfill: bkf,
		RP5: pct(0.05), RP50: pct(0.5), RP95: pct(0.95), Baseline: base}

	for _, rule := range rules {
		drop := dropSetFor(tr, rule)
		sc, kept := simulate(rule, tr, drop)
		sc.VsRand = randControl(tr, drop, opt.Trials, rng)
		sc.H1 = halfVsRand(tr, rule, true, opt.Trials, rng)
		sc.H2 = halfVsRand(tr, rule, false, opt.Trials, rng)
		sc.CILo, sc.CIHi = bootstrapCI(kept, 2000, rng)
		sc.Pass = sc.RetR > base.RetR && sc.VsRand > 95 && sc.H1 > 95 && sc.H2 > 95 && sc.CILo > 0
		// A conf-gated rule can't fire when backfill is in the mix (conf=0 there),
		// so its verdict is only valid in a forward-only run.
		sc.ConfInvalid = confDependentRules[rule] && backfillEval
		res.Traders = append(res.Traders, sc)
	}
	sort.Slice(res.Traders, func(i, j int) bool { return res.Traders[i].RetR > res.Traders[j].RetR })

	res.ConfLayers = confLayers(tr)
	res.GateConf = gateConf(tr, rules)
	return res
}

// confBucket maps an AI confidence to a fixed layer label. Layers are coarse on
// purpose so per-cell samples stay meaningful.
func confBucket(c float64) string {
	switch {
	case c <= 0:
		return "缺失"
	case c < 60:
		return "<60"
	case c < 70:
		return "60~69"
	case c < 80:
		return "70~79"
	case c < 90:
		return "80~89"
	default:
		return ">=90"
	}
}

// confLayerOrder is the display order; only layers with trades are emitted.
var confLayerOrder = []string{"缺失", "<60", "60~69", "70~79", "80~89", ">=90"}

func confLayers(tr []*Trade) []ConfLayer {
	sumR := map[string]float64{}
	n := map[string]int{}
	wins := map[string]int{}
	for _, t := range tr {
		if t.RiskUSD <= 0 {
			continue
		}
		b := confBucket(t.Conf)
		sumR[b] += t.RMult
		n[b]++
		if t.RMult > 0 {
			wins[b]++
		}
	}
	var out []ConfLayer
	for _, name := range confLayerOrder {
		if n[name] == 0 {
			continue
		}
		out = append(out, ConfLayer{Name: name, NTrades: n[name],
			ExpR: sumR[name] / float64(n[name]), SumR: sumR[name],
			WinRate: 100 * float64(wins[name]) / float64(n[name])})
	}
	return out
}

func gateConf(tr []*Trade, rules []string) []GateConf {
	// pre-bucket layer baselines (take-all expR per layer)
	layerSum := map[string]float64{}
	layerN := map[string]int{}
	for _, t := range tr {
		if t.RiskUSD <= 0 {
			continue
		}
		b := confBucket(t.Conf)
		layerSum[b] += t.RMult
		layerN[b]++
	}
	var out []GateConf
	for _, rule := range rules {
		gc := GateConf{Name: rule}
		type acc struct {
			bSum, kSum float64
			bN, kN     int
		}
		m := map[string]*acc{}
		for _, t := range tr {
			if t.RiskUSD <= 0 {
				continue
			}
			b := confBucket(t.Conf)
			a := m[b]
			if a == nil {
				a = &acc{}
				m[b] = a
			}
			if t.BlockedBy[rule] {
				a.bSum += t.RMult
				a.bN++
			} else {
				a.kSum += t.RMult
				a.kN++
			}
		}
		for _, name := range confLayerOrder {
			a := m[name]
			if a == nil || a.bN == 0 { // only layers where this gate actually blocks
				continue
			}
			cell := GateConfCell{Layer: name, BlockN: a.bN, KeepN: a.kN}
			cell.BlockExpR = a.bSum / float64(a.bN)
			if a.kN > 0 {
				cell.KeepExpR = a.kSum / float64(a.kN)
			}
			cell.Edge = cell.KeepExpR - cell.BlockExpR
			if layerN[name] > 0 {
				cell.LayerBaseExpR = layerSum[name] / float64(layerN[name])
			}
			cell.Lift = cell.KeepExpR - cell.LayerBaseExpR
			gc.Cells = append(gc.Cells, cell)
		}
		if len(gc.Cells) > 0 {
			out = append(out, gc)
		}
	}
	return out
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
