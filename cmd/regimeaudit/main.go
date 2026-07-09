// regimeaudit tests REGIME as an ENTRY GATE on the REAL book: for each closed
// position, judge the entry-time regime (no look-ahead) and decide pass/reject
// by direction alignment. Then drop rejected entries entirely and compare the
// resulting book PnL/CVaR to baseline and to a RANDOM control that drops the
// same number of entries at random.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"time"

	_ "modernc.org/sqlite"

	"nofx/market"
)

type rec struct {
	pnl     float64
	side    string
	regime  string
	sym     string
	tagged  bool
	entryMs int64
	conf    float64 // AI confidence 0-100, -1 unknown
}

func main() {
	dbPath := flag.String("db", "/app/data/data.db", "sqlite path")
	trader := flag.String("trader", "%", "trader_id LIKE pattern")
	trials := flag.Int("trials", 5000, "random-control trials")
	chopReject := flag.Bool("chopreject", false, "also reject ALL entries during CHOP")
	chopMinConf := flag.Float64("chopminconf", 0, "reject CHOP entries with confidence < this (0=off)")
	dnLongOnly := flag.Bool("dnlongonly", false, "reject ONLY TREND_DN LONG (isolate bleeding cell)")
	slopeWin := flag.Int("slopewin", 50, "slope window for trend direction")
	flag.Parse()
	setSlopeWindow(*slopeWin)

	db, err := sql.Open("sqlite", "file:"+*dbPath+"?mode=ro&immutable=1")
	must(err)
	defer db.Close()

	entries, err := loadPositionsWithConfidence(db, *trader)
	must(err)
	fmt.Printf("loaded %d closed positions\n", len(entries))
	withConf := 0
	for _, e := range entries {
		if e.confidence >= 0 {
			withConf++
		}
	}
	fmt.Printf("positions with recovered confidence: %d/%d\n", withConf, len(entries))

	bySym := map[string][]int{}
	for i, e := range entries {
		bySym[e.symbol] = append(bySym[e.symbol], i)
	}
	recs := make([]rec, len(entries))
	for i := range recs {
		recs[i] = rec{pnl: entries[i].pnl, side: entries[i].side, regime: "NA", sym: entries[i].symbol, entryMs: entries[i].entryTime, conf: entries[i].confidence}
	}

	symbols := make([]string, 0, len(bySym))
	for s := range bySym {
		symbols = append(symbols, s)
	}
	sort.Strings(symbols)

	tagged, naSym := 0, 0
	for _, sym := range symbols {
		idxs := bySym[sym]
		var minT, maxT int64 = 1 << 62, 0
		for _, ix := range idxs {
			if entries[ix].entryTime < minT {
				minT = entries[ix].entryTime
			}
			if entries[ix].entryTime > maxT {
				maxT = entries[ix].entryTime
			}
		}
		start := time.UnixMilli(minT).Add(-40 * 24 * time.Hour)
		end := time.UnixMilli(maxT).Add(2 * time.Hour)
		bars, err := market.GetKlinesRangeOKX(sym, "1h", start, end)
		if err != nil || len(bars) < 60 {
			naSym++
			continue
		}
		h := make([]float64, len(bars))
		l := make([]float64, len(bars))
		c := make([]float64, len(bars))
		ot := make([]int64, len(bars))
		for i, b := range bars {
			h[i], l[i], c[i], ot[i] = b.High, b.Low, b.Close, b.OpenTime
		}
		for _, ix := range idxs {
			bi := lastClosedBar(ot, entries[ix].entryTime)
			if bi < 50 {
				continue
			}
			recs[ix].regime = regimeAt(h, l, c, bi)
			recs[ix].tagged = true
			tagged++
		}
	}
	fmt.Printf("regime-tagged %d positions; %d symbols had no OKX data\n\n", tagged, naSym)

	// regime x side breakdown of REAL pnl
	fmt.Println("==== REAL PnL by (regime, side) ====")
	fmt.Printf("%-9s %-6s %5s %10s %8s %6s\n", "regime", "side", "n", "totalPnL", "avg", "win%")
	type key struct{ r, s string }
	agg := map[key][]float64{}
	for _, r := range recs {
		if !r.tagged {
			continue
		}
		agg[key{r.regime, r.side}] = append(agg[key{r.regime, r.side}], r.pnl)
	}
	regs := []string{"TREND_UP", "TREND_DN", "CHOP", "FLAT"}
	for _, rg := range regs {
		for _, sd := range []string{"LONG", "SHORT"} {
			p := agg[key{rg, sd}]
			if len(p) == 0 {
				continue
			}
			sum, w := 0.0, 0
			for _, x := range p {
				sum += x
				if x > 0 {
					w++
				}
			}
			mark := "  keep"
			if !entryAligns(sd, rg) {
				mark = "REJECT(counter-trend)"
			} else if rg == "CHOP" && *chopMinConf > 0 {
				mark = fmt.Sprintf("gate: CHOP conf<%.0f rejected", *chopMinConf)
			}
			fmt.Printf("%-9s %-6s %5d %+10.2f %+8.3f %5.1f  %s\n",
				rg, sd, len(p), sum, sum/float64(len(p)), 100*float64(w)/float64(len(p)), mark)
		}
	}

	// full sample
	evalGate(recs, allIdx(recs), *chopReject, *chopMinConf, *dnLongOnly, *trials, "FULL")

	// H1/H2 walk-forward split by median entry time of tagged recs
	var times []int64
	for _, r := range recs {
		if r.tagged {
			times = append(times, r.entryMs)
		}
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	if len(times) > 0 {
		med := times[len(times)/2]
		var h1, h2 []int
		for i, r := range recs {
			if r.entryMs < med {
				h1 = append(h1, i)
			} else {
				h2 = append(h2, i)
			}
		}
		evalGate(recs, h1, *chopReject, *chopMinConf, *dnLongOnly, *trials, "H1(early)")
		evalGate(recs, h2, *chopReject, *chopMinConf, *dnLongOnly, *trials, "H2(late)")
	}
}

func allIdx(recs []rec) []int {
	out := make([]int, len(recs))
	for i := range recs {
		out[i] = i
	}
	return out
}

// evalGate applies the counter-trend gate to the given subset of recs and
// compares to a random control that drops the same count within that subset.
func evalGate(recs []rec, subset []int, chopReject bool, chopMinConf float64, dnLongOnly bool, trials int, label string) {
	base := 0.0
	var kept []float64
	var rejectLocal []int // positions within subset
	rejPnL := 0.0
	for _, i := range subset {
		r := recs[i]
		base += r.pnl
		reject := false
		if r.tagged {
			if dnLongOnly {
				// isolate the single bleeding cell: reject ONLY downtrend-long
				if r.regime == "TREND_DN" && r.side == "LONG" {
					reject = true
				}
			} else {
				// rule 1+2: reject counter-trend (long in downtrend / short in uptrend)
				if !entryAligns(r.side, r.regime) {
					reject = true
				}
				// rule 3: reject low-confidence entries during CHOP
				if chopMinConf > 0 && r.regime == "CHOP" && r.conf >= 0 && r.conf < chopMinConf {
					reject = true
				}
				if chopReject && r.regime == "CHOP" {
					reject = true
				}
			}
		}
		if reject {
			rejectLocal = append(rejectLocal, i)
			rejPnL += r.pnl
		} else {
			kept = append(kept, r.pnl)
		}
	}
	keptSum := 0.0
	for _, x := range kept {
		keptSum += x
	}
	allp := make([]float64, 0, len(subset))
	for _, i := range subset {
		allp = append(allp, recs[i].pnl)
	}
	fmt.Printf("\n==== [%s] GATE (n=%d) ====\n", label, len(subset))
	fmt.Printf("baseline PnL=%+.2f (CVaR95 %.3f) | rejected %d (PnL %+.2f, avg %+.3f) | gated PnL=%+.2f (CVaR95 %.3f) delta %+.2f\n",
		base, cvar95(allp), len(rejectLocal), rejPnL, safeAvg(rejPnL, len(rejectLocal)), keptSum, cvar95(kept), keptSum-base)

	k := len(rejectLocal)
	if k == 0 {
		fmt.Println("  no rejects in subset")
		return
	}
	rng := rand.New(rand.NewSource(42))
	gatedCVaR := cvar95(kept)
	betterPnL, betterCVaR := 0, 0
	for t := 0; t < trials; t++ {
		perm := rng.Perm(len(subset))[:k]
		drop := make(map[int]bool, k)
		for _, p := range perm {
			drop[p] = true
		}
		rp := make([]float64, 0, len(subset)-k)
		sum := 0.0
		for pos, i := range subset {
			if drop[pos] {
				continue
			}
			rp = append(rp, recs[i].pnl)
			sum += recs[i].pnl
		}
		if keptSum > sum {
			betterPnL++
		}
		if gatedCVaR > cvar95(rp) {
			betterCVaR++
		}
	}
	fmt.Printf("  vs random (drop %d): PnL beats %.1f%% | CVaR95 beats %.1f%%  (need >95%%)\n",
		k, 100*float64(betterPnL)/float64(trials), 100*float64(betterCVaR)/float64(trials))
}

func condStr(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}
func safeAvg(s float64, n int) float64 {
	if n == 0 {
		return 0
	}
	return s / float64(n)
}
func report(p []float64, name string) {}
func cvar95(p []float64) float64 {
	if len(p) == 0 {
		return 0
	}
	s := append([]float64(nil), p...)
	sort.Float64s(s)
	n := int(math.Ceil(0.05 * float64(len(s))))
	if n < 1 {
		n = 1
	}
	sum := 0.0
	for i := 0; i < n; i++ {
		sum += s[i]
	}
	return sum / float64(n)
}
func lastClosedBar(ot []int64, entryMs int64) int {
	lo, hi, ans := 0, len(ot)-1, -1
	for lo <= hi {
		mid := (lo + hi) / 2
		if ot[mid] <= entryMs {
			ans = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return ans
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
