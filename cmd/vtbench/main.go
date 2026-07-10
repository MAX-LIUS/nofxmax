// Command vtbench — Virtual Trader Bench.
//
// Simulates "N traders, each with a different entry-gate rulebook, trading the
// SAME account" over the real executed book. Ground truth = every CLOSED
// position (real realized PnL under the real protection/exit system). Each
// virtual trader FILTERS that book: it takes every real trade except the ones
// its gate would have blocked, then runs the survivors chronologically on a
// shared starting capital with fixed-fractional risk sizing. Output = per-trader
// equity curve + scorecard (return, maxDD, CVaR95, Ulcer, Sortino, winRate,
// expectancy in R, profit factor), plus random-control / H1-H2 / bootstrap CI
// significance so we can tell a real edge from luck.
//
// Per-trade unit is REALIZED R = realized_pnl / initial_risk_usd, where
// initial_risk_usd = |entry_price - stop_loss| * entry_quantity, and stop_loss
// is recovered from the opening decision JSON. R normalizes out position-size
// noise so we measure ENTRY-gate quality, not sizing.
//
// LIMITATIONS (honest, by construction):
//   - Virtual traders can only be MORE restrictive than reality (a subset of
//     executed trades). A gate that would OPEN trades the live system rejected
//     has no ground truth — we never traded them, so we cannot score them. This
//     is correct for an entry gate: it only ever removes trades.
//   - Exits are held at the real protection outcome, so we isolate entry quality.
//
// Usage: vtbench -db /opt/webstack/nofx/data/data.db [-risk 0.01] [-trials 5000]
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

// trade is one CLOSED position enriched with R and gate-block flags.
type trade struct {
	pkey     string
	trader   string
	symbol   string
	side     string
	entryMs  int64
	pnlUSD   float64 // realized_pnl (real, under real protection)
	riskUSD  float64 // initial risk = |entry-stop|*qty; 0 if unrecoverable
	rMult    float64 // realized R = pnlUSD/riskUSD; 0 if riskUSD==0
	conf     float64 // AI confidence at entry (0 if unknown)
	estRR    float64 // AI net_estimated_rr at entry
	blockedBy map[string]bool // rule_name -> would_block
}

// dkey identifies a decision for R enrichment.
type dkey struct {
	trader string
	cycle  int64
	symbol string
}

type dinfo struct {
	stop  float64
	conf  float64
	estRR float64
}

func main() {
	dbPath := flag.String("db", "/opt/webstack/nofx/data/data.db", "sqlite db path")
	riskFrac := flag.Float64("risk", 0.01, "fixed fraction of equity risked per trade")
	trials := flag.Int("trials", 5000, "random-control trials")
	seed := flag.Int64("seed", 42, "rng seed")
	cap := flag.Float64("cap", 0, "winsorize realized R to +/-cap (0=off) — robustness vs fat-tail outliers")
	flag.Parse()

	db, err := sql.Open("sqlite", "file:"+*dbPath+"?mode=ro&immutable=1")
	must(err)
	defer db.Close()

	dmap := loadDecisionR(db)       // (trader,cycle,symbol) -> stop/conf/estRR
	trades := loadTrades(db, dmap)  // enriched closed book
	loadBlocks(db, trades)          // attach per-rule would_block
	if *cap > 0 {
		for _, t := range trades {
			if t.rMult > *cap {
				t.rMult = *cap
			} else if t.rMult < -*cap {
				t.rMult = -*cap
			}
		}
		fmt.Printf("[robustness] realized R winsorized to +/-%.1f\n", *cap)
	}
	rules := ruleNames(trades)

	rng := rand.New(rand.NewSource(*seed))
	runBench(trades, rules, *riskFrac, *trials, rng)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

// loadDecisionR scans decision_records once and extracts, per opening decision,
// the stop_loss / confidence / net_estimated_rr keyed by (trader,cycle,symbol).
func loadDecisionR(db *sql.DB) map[dkey]dinfo {
	rows, err := db.Query(`SELECT trader_id, cycle_number, decisions
		FROM decision_records WHERE decisions LIKE '%stop_loss%'`)
	must(err)
	defer rows.Close()

	out := map[dkey]dinfo{}
	type rawDec struct {
		Action     string  `json:"action"`
		Symbol     string  `json:"symbol"`
		StopLoss   float64 `json:"stop_loss"`
		Confidence float64 `json:"confidence"`
		RiskReward struct {
			NetRR float64 `json:"net_estimated_rr"`
		} `json:"risk_reward"`
	}
	for rows.Next() {
		var trader, decs string
		var cycle int64
		if err := rows.Scan(&trader, &cycle, &decs); err != nil {
			continue
		}
		var arr []rawDec
		if json.Unmarshal([]byte(decs), &arr) != nil {
			continue
		}
		for _, d := range arr {
			if !strings.Contains(d.Action, "open") || d.StopLoss <= 0 {
				continue
			}
			out[dkey{trader, cycle, d.Symbol}] = dinfo{d.StopLoss, d.Confidence, d.RiskReward.NetRR}
		}
	}
	return out
}

// loadTrades loads CLOSED positions and enriches R from the decision map.
func loadTrades(db *sql.DB, dmap map[dkey]dinfo) map[string]*trade {
	rows, err := db.Query(`SELECT id, trader_id, symbol, side, entry_price,
		entry_quantity, realized_pnl, entry_time, COALESCE(entry_decision_cycle,0)
		FROM trader_positions
		WHERE status='CLOSED' AND entry_price>0 AND entry_quantity>0`)
	must(err)
	defer rows.Close()

	out := map[string]*trade{}
	for rows.Next() {
		var id, entryMs, cycle int64
		var trader, symbol, side string
		var entryPx, qty, pnl float64
		if err := rows.Scan(&id, &trader, &symbol, &side, &entryPx, &qty, &pnl, &entryMs, &cycle); err != nil {
			continue
		}
		pkey := fmt.Sprintf("%d", id)
		t := &trade{pkey: pkey, trader: trader, symbol: symbol, side: strings.ToUpper(side),
			entryMs: entryMs, pnlUSD: pnl, blockedBy: map[string]bool{}}
		if di, ok := dmap[dkey{trader, cycle, symbol}]; ok {
			t.riskUSD = math.Abs(entryPx-di.stop) * qty
			t.conf, t.estRR = di.conf, di.estRR
			if t.riskUSD > 0 {
				t.rMult = pnl / t.riskUSD
			}
		}
		out[pkey] = t
	}
	return out
}

// loadBlocks attaches each rule's would_block verdict to its trade by position_id.
func loadBlocks(db *sql.DB, trades map[string]*trade) {
	rows, err := db.Query(`SELECT position_id, rule_name, would_block
		FROM shadow_gate_verdicts WHERE position_id>0`)
	must(err)
	defer rows.Close()
	for rows.Next() {
		var pid int64
		var rule string
		var blk int
		if err := rows.Scan(&pid, &rule, &blk); err != nil {
			continue
		}
		if t, ok := trades[fmt.Sprintf("%d", pid)]; ok {
			t.blockedBy[rule] = blk != 0
		}
	}
}

func ruleNames(trades map[string]*trade) []string {
	set := map[string]bool{}
	for _, t := range trades {
		for r := range t.blockedBy {
			set[r] = true
		}
	}
	out := make([]string, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// scorecard is one virtual trader's result.
type scorecard struct {
	name    string
	nTrades int
	nBlocked int
	retR    float64 // final equity in R (fixed 1R risk per trade => sum of rMult)
	maxDD   float64 // max drawdown in R
	ulcer   float64
	cvar95  float64 // CVaR95 of per-trade R
	sortino float64
	winRate float64
	expR    float64 // expectancy per trade (mean R)
	profitF float64
}

// simulate runs one virtual trader over the R-eligible trades in time order,
// skipping trades its `drop` set blocks. Equity is a running sum of realized R
// (fixed 1R risk per trade). Returns the scorecard and per-trade R series kept.
func simulate(name string, tr []*trade, drop map[string]bool) (scorecard, []float64) {
	var kept []float64
	var equity, peak, sumDD, maxDD float64
	var wins, grossWin, grossLoss float64
	var blocked int
	for _, t := range tr {
		if t.riskUSD <= 0 {
			continue // not R-eligible
		}
		if drop != nil && drop[t.pkey] {
			blocked++
			continue
		}
		r := t.rMult
		kept = append(kept, r)
		equity += r
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
	sc := scorecard{name: name, nTrades: len(kept), nBlocked: blocked, retR: equity, maxDD: maxDD}
	if len(kept) > 0 {
		sc.ulcer = math.Sqrt(sumDD / float64(len(kept)))
		sc.winRate = 100 * wins / float64(len(kept))
		sc.expR = equity / float64(len(kept))
		sc.cvar95 = cvar95(kept)
		sc.sortino = sortinoR(kept)
	}
	if grossLoss > 0 {
		sc.profitF = grossWin / grossLoss
	} else if grossWin > 0 {
		sc.profitF = math.Inf(1)
	}
	return sc, kept
}

// dropSetFor builds the set of position keys a rule (or rule combo) blocks.
func dropSetFor(tr []*trade, rules []string, all bool) map[string]bool {
	drop := map[string]bool{}
	for _, t := range tr {
		hit := 0
		for _, r := range rules {
			if t.blockedBy[r] {
				hit++
			}
		}
		block := hit > 0 // OR combo
		if all {
			block = hit == len(rules) // AND combo
		}
		if block {
			drop[t.pkey] = true
		}
	}
	return drop
}

// cvar95 = mean of the worst 5% outcomes (negative = loss). Lower is worse.
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
	sum := 0.0
	for i := 0; i < k; i++ {
		sum += s[i]
	}
	return sum / float64(k)
}

// sortinoR = mean / downside-deviation of the per-trade R series.
func sortinoR(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	mean := 0.0
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

// randControlExpR: fraction of trials where the gate's kept-book expectancy beats
// a random trader that drops the SAME number of trades. >95% => real edge.
func randControlExpR(tr []*trade, drop map[string]bool, trials int, rng *rand.Rand) float64 {
	var elig []*trade
	for _, t := range tr {
		if t.riskUSD > 0 {
			elig = append(elig, t)
		}
	}
	nDrop := 0
	for _, t := range elig {
		if drop[t.pkey] {
			nDrop++
		}
	}
	if nDrop == 0 || nDrop >= len(elig) {
		return 0
	}
	gExp := keptExpR(elig, drop)
	better := 0
	for i := 0; i < trials; i++ {
		rd := randomDrop(elig, nDrop, rng)
		if gExp > keptExpR(elig, rd) {
			better++
		}
	}
	return 100 * float64(better) / float64(trials)
}

func keptExpR(elig []*trade, drop map[string]bool) float64 {
	var sum float64
	var n int
	for _, t := range elig {
		if drop[t.pkey] {
			continue
		}
		sum += t.rMult
		n++
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

func randomDrop(elig []*trade, n int, rng *rand.Rand) map[string]bool {
	idx := rng.Perm(len(elig))
	d := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		d[elig[idx[i]].pkey] = true
	}
	return d
}

// halfVsRand runs the vs-random(expR) control on ONE time half of the book
// (early=true -> first half by entry time). The overfit detector: a real edge
// beats random in BOTH halves; an overfit rule wins full-sample but collapses in
// one half. `tr` must be time-sorted.
func halfVsRand(tr []*trade, rule string, early bool, trials int, rng *rand.Rand) float64 {
	var elig []*trade
	for _, t := range tr {
		if t.riskUSD > 0 {
			elig = append(elig, t)
		}
	}
	if len(elig) < 20 {
		return 0
	}
	mid := len(elig) / 2
	var half []*trade
	if early {
		half = elig[:mid]
	} else {
		half = elig[mid:]
	}
	drop := map[string]bool{}
	for _, t := range half {
		if t.blockedBy[rule] {
			drop[t.pkey] = true
		}
	}
	return randControlExpR(half, drop, trials, rng)
}

// bootstrapCI returns the 5th/95th percentile of kept-book expectancy under
// resampling — a confidence band on the gate trader's edge.
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

// runBench sorts trades by time, simulates the baseline + one virtual trader per
// gate, and prints the comparison scorecard with significance.
func runBench(tradesMap map[string]*trade, rules []string, riskFrac float64, trials int, rng *rand.Rand) {
	tr := make([]*trade, 0, len(tradesMap))
	for _, t := range tradesMap {
		tr = append(tr, t)
	}
	sort.Slice(tr, func(i, j int) bool { return tr[i].entryMs < tr[j].entryMs })

	// coverage: how many trades are R-eligible (have recoverable stop)
	var rElig, withBlocks int
	for _, t := range tr {
		if t.riskUSD > 0 {
			rElig++
		}
		if len(t.blockedBy) > 0 {
			withBlocks++
		}
	}
	fmt.Printf("book: %d closed trades | R-eligible (stop recovered): %d | with gate verdicts: %d\n",
		len(tr), rElig, withBlocks)
	// R distribution — flags fat tails that widen bootstrap CIs (edge may hinge
	// on a few outliers rather than a broad, stable advantage).
	var rs2 []float64
	for _, t := range tr {
		if t.riskUSD > 0 {
			rs2 = append(rs2, t.rMult)
		}
	}
	sort.Float64s(rs2)
	if len(rs2) > 0 {
		p := func(q float64) float64 { return rs2[int(q*float64(len(rs2)-1))] }
		fmt.Printf("realized R dist: min %.1f | p5 %.2f | p50 %.2f | p95 %.2f | max %.1f (fat tails widen CI)\n",
			rs2[0], p(0.05), p(0.5), p(0.95), rs2[len(rs2)-1])
	}
	fmt.Println("unit = realized R (pnl / initial risk). fixed 1R per trade => equity is cumulative R.")
	fmt.Println("each row = a virtual trader running the SAME book minus the trades its gate blocks.")
	fmt.Println()

	base, _ := simulate("BASELINE(take-all)", tr, nil)
	printHeader()
	printRow(base, 0, 0, 0, 0, 0, false)

	type ranked struct {
		sc         scorecard
		vsRand     float64
		h1, h2     float64
		ciLo, ciHi float64
		pass       bool
	}
	var rs []ranked
	for _, rule := range rules {
		drop := dropSetFor(tr, []string{rule}, false)
		sc, kept := simulate(rule, tr, drop)
		vs := randControlExpR(tr, drop, trials, rng)
		h1 := halfVsRand(tr, rule, true, trials, rng)
		h2 := halfVsRand(tr, rule, false, trials, rng)
		lo, hi := bootstrapCI(kept, 2000, rng)
		// PASS = lifts final R over baseline AND full-sample & BOTH halves beat
		// random >95% AND bootstrap lower bound stays positive (edge not luck).
		pass := sc.retR > base.retR && vs > 95 && h1 > 95 && h2 > 95 && lo > 0
		rs = append(rs, ranked{sc, vs, h1, h2, lo, hi, pass})
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].sc.retR > rs[j].sc.retR })
	for _, r := range rs {
		printRow(r.sc, r.vsRand, r.h1, r.h2, r.ciLo, r.ciHi, r.pass)
	}
	fmt.Printf("\nbaseline expectancy = %+.3fR over %d trades.\n", base.expR, base.nTrades)
	fmt.Println("PASS (✓) = final R > baseline AND vs-random(expR) full-sample & H1 & H2 all >95%")
	fmt.Println("          AND bootstrap 5%% expR > 0. Full-sample-only winners that fail a half are")
	fmt.Println("          OVERFIT (DEC-0110..0116) — do not enforce.")
	fmt.Println("NOTE: virtual traders only REMOVE trades (subset of executed book); gates that would")
	fmt.Println("OPEN untraded ideas cannot be scored — no ground truth. Backfill chop rules see conf=0.")
}

func printHeader() {
	fmt.Printf("%-22s %6s %5s %8s %7s %7s %7s %8s %7s %7s %6s %s\n",
		"virtual_trader", "trades", "blkd", "finalR", "expR", "maxDD", "CVaR95", "vsRand", "H1", "H2", "ci5%", "")
}

func printRow(s scorecard, vsRand, h1, h2, ciLo, ciHi float64, pass bool) {
	col := func(v float64, on bool) string {
		if !on {
			return "  -  "
		}
		return fmt.Sprintf("%.0f", v)
	}
	tag := ""
	if pass {
		tag = "✓ PASS"
	} else if s.nBlocked > 0 {
		tag = "overfit/weak"
	}
	fmt.Printf("%-22s %6d %5d %+8.1f %+7.3f %7.1f %7.3f %8s %7s %7s %+6.2f %s\n",
		trunc(s.name, 22), s.nTrades, s.nBlocked, s.retR, s.expR, s.maxDD, s.cvar95,
		col(vsRand, s.nBlocked > 0), col(h1, s.nBlocked > 0), col(h2, s.nBlocked > 0), ciLo, tag)
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
