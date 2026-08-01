package trader

// gate15m_sweep_manual_test.go 是 Claude-R(15m primary)开仓门禁的**离线参数扫描
// harness**,不是回归测试。它只在显式设置 GATE15M=1 时运行,默认 t.Skip ——
// 因为它要读 24MB 本地 K 线 CSV 和生产 DB 的只读副本,不能进 CI。
//
// 为什么写成 package trader 内的 test 而不是独立 Python:
// 判据必须是**生产代码本身**(chartTrendGate / swingSeqDir / counterTrend / sgADX …
// 全是包私有函数)。上一轮我在 Python 里复现过一遍,虽然逐位对齐了,但那是额外的
// 失真来源;直接调生产函数则零复现风险。
//
// 数据契约:
//   - K 线:Binance Vision 15m CSV,/root/.claude/jobs/5cbb3cf4/tmp/tf15/k15/<SYM>-<period>.csv
//   - 交易:sqlite3 只读导出的 CSV(见 gate15mTradesCSV),字段 symbol,side,entry_ms,exit_ms
//   - 因果性:只用 closeTime <= entry_ms 的**已收盘**K 线,最后一根即入场前最后一根。

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"nofx/market"
)

const (
	gate15mKlineDir  = "/root/.claude/jobs/5cbb3cf4/tmp/tf15/k15"
	gate15mTradesCSV = "/root/.claude/jobs/5cbb3cf4/tmp/tf15/trades.csv"
	// gate15mMinBars 是复算一条规则所需的最小历史。60 根是 shadow 回填自己的下限
	// (EvaluateShadowGatesForBackfill 对 <60 根直接返回 nil),沿用它保持一致。
	gate15mMinBars = 60

	// gate15mLiveWindow 复制线上 buildShadowGateCtx 的取数上限：
	// market.GetKlines(symbol, primaryTF, ex, 120)。所有闸门（含 enforce 版
	// chart_trend，它复用同一个 shadowGateCtx）只能看到这 120 根。
	gate15mLiveWindow = 120
	// gate15mHoldBars 是中性出场:15m × 24 = 6h,与 1h 研究的中性持有期等长,
	// 也贴近 Claude-R 实际均值 5.82h / 中位 3.72h。
	gate15mHoldBars = 24
)

type g15Trade struct {
	symbol  string
	side    string // LONG / SHORT
	entryMS int64
}

type g15Bar = market.Kline

// g15LoadKlines reads every CSV for a symbol and returns time-sorted, deduped bars.
func g15LoadKlines(symbol string) ([]g15Bar, error) {
	entries, err := os.ReadDir(gate15mKlineDir)
	if err != nil {
		return nil, err
	}
	seen := make(map[int64]struct{})
	var out []g15Bar
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".csv") || strings.SplitN(name, "-", 2)[0] != symbol {
			continue
		}
		f, err := os.Open(gate15mKlineDir + "/" + name)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if line == "" || strings.HasPrefix(line, "open_time") {
				continue
			}
			p := strings.Split(line, ",")
			if len(p) < 7 {
				continue
			}
			ot, e1 := strconv.ParseInt(p[0], 10, 64)
			o, e2 := strconv.ParseFloat(p[1], 64)
			h, e3 := strconv.ParseFloat(p[2], 64)
			l, e4 := strconv.ParseFloat(p[3], 64)
			c, e5 := strconv.ParseFloat(p[4], 64)
			v, _ := strconv.ParseFloat(p[5], 64)
			ct, e6 := strconv.ParseInt(p[6], 10, 64)
			if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || e6 != nil {
				continue
			}
			if _, dup := seen[ot]; dup {
				continue
			}
			seen[ot] = struct{}{}
			out = append(out, g15Bar{OpenTime: ot, Open: o, High: h, Low: l, Close: c, Volume: v, CloseTime: ct})
		}
		f.Close()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OpenTime < out[j].OpenTime })
	return out, nil
}

func g15LoadTrades(t *testing.T) []g15Trade {
	t.Helper()
	f, err := os.Open(gate15mTradesCSV)
	if err != nil {
		t.Fatalf("open trades csv: %v", err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatalf("read trades csv: %v", err)
	}
	var out []g15Trade
	for i, row := range rows {
		if i == 0 && strings.EqualFold(strings.TrimSpace(row[0]), "symbol") {
			continue
		}
		if len(row) < 3 {
			continue
		}
		ems, err := strconv.ParseInt(strings.TrimSpace(row[2]), 10, 64)
		if err != nil {
			continue
		}
		out = append(out, g15Trade{
			symbol:  strings.ToUpper(strings.TrimSpace(row[0])),
			side:    strings.ToUpper(strings.TrimSpace(row[1])),
			entryMS: ems,
		})
	}
	return out
}

// g15Sample is one trade with its causal bar window and neutral outcome already
// resolved, so every rule/parameter cell is scored on the SAME sample set.
type g15Sample struct {
	symbol string
	side   string
	bars   []g15Bar // causal: last bar closed at or before entry
	retPct float64  // neutral outcome: hold gate15mHoldBars, direction-signed
	month  int
	// futureBars 只给 TestG15GroundTruthOffset 用于诊断「差一根」假设,
	// 是入场后的 K 线。任何评分用途都**不得**读它 —— 那是未来函数。
	futureBars []g15Bar
}

func g15BuildSamples(t *testing.T) []g15Sample {
	t.Helper()
	trades := g15LoadTrades(t)
	bySym := map[string][]g15Bar{}
	for _, tr := range trades {
		if _, ok := bySym[tr.symbol]; ok {
			continue
		}
		kl, err := g15LoadKlines(tr.symbol)
		if err != nil || len(kl) == 0 {
			bySym[tr.symbol] = nil
			continue
		}
		bySym[tr.symbol] = kl
	}

	var out []g15Sample
	var noBars, thin, noFuture int
	for _, tr := range trades {
		kl := bySym[tr.symbol]
		if len(kl) == 0 {
			noBars++
			continue
		}
		// Causal cut: last usable bar must have CLOSED at or before entry.
		idx := -1
		for i := range kl {
			if kl[i].CloseTime <= tr.entryMS {
				idx = i
			} else {
				break
			}
		}
		if idx < gate15mMinBars-1 {
			thin++
			continue
		}
		if idx+gate15mHoldBars >= len(kl) {
			noFuture++
			continue
		}
		entry := kl[idx].Close
		exit := kl[idx+gate15mHoldBars].Close
		if entry <= 0 {
			continue
		}
		ret := (exit - entry) / entry * 100
		if strings.EqualFold(tr.side, "SHORT") {
			ret = -ret
		}
		mon := 0
		// 2026-06-01T00:00:00Z = 1780272000000 ; 2026-07-01 = 1782950400000
		if tr.entryMS >= 1782950400000 {
			mon = 7
		} else if tr.entryMS >= 1780272000000 {
			mon = 6
		}
		// 窗口必须与线上一致。buildShadowGateCtx 取 market.GetKlines(...,120)，
		// 即闸门只看最近 gate15mLiveWindow 根，而不是全部历史。传完整历史会让
		// chartSwingAlign 的分母涨到几百个摆动点，align 被大数定律压回 0.5 附近，
		// 于是任何 align_min>0.54 都恒不通过 —— 那是 harness 的假象，不是线上行为。
		lo := idx + 1 - gate15mLiveWindow
		if lo < 0 {
			lo = 0
		}
		fhi := idx + 1 + 4
		if fhi > len(kl) {
			fhi = len(kl)
		}
		out = append(out, g15Sample{
			symbol: tr.symbol, side: tr.side,
			bars:   kl[lo : idx+1],
			retPct: ret, month: mon,
			futureBars: kl[idx+1 : fhi],
		})
	}
	t.Logf("samples=%d skipped(noBars=%d thin=%d noFuture=%d) of %d trades",
		len(out), noBars, thin, noFuture, len(trades))
	return out
}

func (s g15Sample) ctx() shadowGateCtx {
	h := make([]float64, len(s.bars))
	l := make([]float64, len(s.bars))
	c := make([]float64, len(s.bars))
	for i, b := range s.bars {
		h[i], l[i], c[i] = b.High, b.Low, b.Close
	}
	return shadowGateCtx{highs: h, lows: l, closes: c, side: s.side, conf: 0}
}

func (s g15Sample) klineBars() []market.KlineBar {
	out := make([]market.KlineBar, len(s.bars))
	for i, b := range s.bars {
		out[i] = market.KlineBar{Time: b.OpenTime, Open: b.Open, High: b.High, Low: b.Low, Close: b.Close, Volume: b.Volume}
	}
	return out
}

// g15Stat is the scorecard for one gate configuration.
type g15Stat struct {
	pass   int
	total  int
	mean   float64
	win    float64
	t      float64
	mean6  float64
	mean7  float64
	meanL  float64
	meanS  float64
	passL  int
	passS  int
	topSym string
	topShr float64
}

// g15Score evaluates a predicate (allow=true) over the sample set.
func g15Score(samples []g15Sample, allow func(g15Sample) bool) g15Stat {
	var st g15Stat
	st.total = len(samples)
	var rs []float64
	var r6, r7, rl, rs2 []float64
	bySym := map[string]float64{}
	wins := 0
	for _, s := range samples {
		if !allow(s) {
			continue
		}
		st.pass++
		rs = append(rs, s.retPct)
		if s.retPct > 0 {
			wins++
		}
		switch s.month {
		case 6:
			r6 = append(r6, s.retPct)
		case 7:
			r7 = append(r7, s.retPct)
		}
		if s.side == "LONG" {
			rl = append(rl, s.retPct)
			st.passL++
		} else {
			rs2 = append(rs2, s.retPct)
			st.passS++
		}
		bySym[s.symbol] += s.retPct
	}
	if st.pass == 0 {
		return st
	}
	mean := func(v []float64) float64 {
		if len(v) == 0 {
			return math.NaN()
		}
		sum := 0.0
		for _, x := range v {
			sum += x
		}
		return sum / float64(len(v))
	}
	st.mean = mean(rs)
	st.win = float64(wins) / float64(st.pass) * 100
	st.mean6, st.mean7 = mean(r6), mean(r7)
	st.meanL, st.meanS = mean(rl), mean(rs2)
	// One-sample t against 0 (same statistic the 1h research reported).
	if len(rs) > 1 {
		var ss float64
		for _, x := range rs {
			ss += (x - st.mean) * (x - st.mean)
		}
		sd := math.Sqrt(ss / float64(len(rs)-1))
		if sd > 0 {
			st.t = st.mean / (sd / math.Sqrt(float64(len(rs))))
		}
	}
	// Concentration: largest single-coin share of total positive contribution.
	totAbs := 0.0
	for _, v := range bySym {
		totAbs += math.Abs(v)
	}
	for k, v := range bySym {
		if totAbs > 0 && math.Abs(v)/totAbs > st.topShr {
			st.topShr = math.Abs(v) / totAbs
			st.topSym = k
		}
	}
	return st
}

func (s g15Stat) line(label string) string {
	if s.pass == 0 {
		return fmt.Sprintf("%-34s pass=0", label)
	}
	return fmt.Sprintf("%-34s pass=%3d/%3d(%4.1f%%) mean=%+.3f win=%4.1f%% t=%+5.2f | 6m=%+.3f 7m=%+.3f | L=%+.3f(%d) S=%+.3f(%d) | top=%s %.0f%%",
		label, s.pass, s.total, float64(s.pass)/float64(s.total)*100, s.mean, s.win, s.t,
		s.mean6, s.mean7, s.meanL, s.passL, s.meanS, s.passS, s.topSym, s.topShr*100)
}

func g15Guard(t *testing.T) []g15Sample {
	t.Helper()
	if os.Getenv("GATE15M") != "1" {
		t.Skip("offline parameter sweep; set GATE15M=1 to run")
	}
	samples := g15BuildSamples(t)
	if len(samples) < 50 {
		t.Fatalf("too few samples: %d", len(samples))
	}
	return samples
}

// ── Baseline ──

func TestG15Baseline(t *testing.T) {
	samples := g15Guard(t)
	base := g15Score(samples, func(g15Sample) bool { return true })
	t.Log(base.line("NO GATE (baseline)"))
}

// ── 1) 既有影子规则registry:逐条按生产实现打分 ──
// 这批是 shadow_gate.go 里已实现的候选闸门,全部是 highs/lows/closes 的纯函数。
// 直接调 r.fn,所以判据和线上完全一致。
func TestG15ShadowRegistry(t *testing.T) {
	samples := g15Guard(t)
	t.Log(g15Score(samples, func(g15Sample) bool { return true }).line("baseline"))
	for _, r := range shadowRules {
		rule := r
		st := g15Score(samples, func(s g15Sample) bool {
			block, _, _ := rule.fn(s.ctx())
			return !block
		})
		t.Log(st.line(rule.name))
	}
}

// ── 2) chart_trend 参数扫描 ──
// 线上 Claude-R: slope_window=30, align_min=0.55, r2_min=0.60 (1h 标定值)。
// 注意 chartTrendGate 内部把 chartSwingAlign 的 lb 硬编码为 2。
func TestG15ChartTrendSweep(t *testing.T) {
	samples := g15Guard(t)
	t.Log(g15Score(samples, func(g15Sample) bool { return true }).line("baseline"))
	for _, w := range []int{20, 30, 50} {
		for _, r2 := range []float64{0.05, 0.10, 0.20, 0.30, 0.45, 0.60} {
			for _, am := range []float64{0.0, 0.40, 0.55, 0.70} {
				win, r2m, alm := w, r2, am
				st := g15Score(samples, func(s g15Sample) bool {
					block, _ := chartTrendGate(s.ctx(), win, alm, r2m, 2)
					return !block
				})
				t.Log(st.line(fmt.Sprintf("chart_trend w=%d r2>=%.2f align>=%.2f", win, r2m, alm)))
			}
		}
	}
}

// 拆解 chart_trend 的三个条件,看是哪一项在起作用/在拖累。
func TestG15ChartTrendDecompose(t *testing.T) {
	samples := g15Guard(t)
	t.Log(g15Score(samples, func(g15Sample) bool { return true }).line("baseline"))
	for _, w := range []int{20, 30, 50} {
		win := w
		t.Log(g15Score(samples, func(s g15Sample) bool {
			c := s.ctx()
			sl, _ := chartRegChannel(c.closes, win)
			if strings.EqualFold(c.side, "SHORT") {
				sl = -sl
			}
			return sl > 0
		}).line(fmt.Sprintf("dirSlope>0 only (w=%d)", win)))
	}
	for _, am := range []float64{0.40, 0.55, 0.70} {
		alm := am
		for _, lb := range []int{2, 3} {
			lbv := lb
			t.Log(g15Score(samples, func(s g15Sample) bool {
				c := s.ctx()
				return chartSwingAlign(c.highs, c.lows, c.side, lbv) >= alm
			}).line(fmt.Sprintf("align>=%.2f only (lb=%d)", alm, lbv)))
		}
	}
	for _, r2 := range []float64{0.10, 0.20, 0.30, 0.60} {
		r2m := r2
		t.Log(g15Score(samples, func(s g15Sample) bool {
			c := s.ctx()
			_, r2v := chartRegChannel(c.closes, 30)
			return r2v >= r2m
		}).line(fmt.Sprintf("r2>=%.2f only (w=30)", r2m)))
	}
}

// ── 3) HH/HL 结构闸门:最优参数与参数精度 ──
// 问题2。lb 是分形半宽,n 是要求单调的最近摆动点个数。
// 线上默认 lb=3/n=2(1h 标定)。这里把 lb×n 全格扫开,并附方向拆分与月度拆分,
// 用来判断「符号是否稳定」而不只看均值大小。
func TestG15StructuralSweep(t *testing.T) {
	samples := g15Guard(t)
	t.Log(g15Score(samples, func(g15Sample) bool { return true }).line("baseline"))
	for _, lb := range []int{1, 2, 3, 4, 5, 6, 8} {
		for _, n := range []int{2, 3, 4} {
			lbv, nv := lb, n
			st := g15Score(samples, func(s g15Sample) bool {
				want := 1
				if s.side == "SHORT" {
					want = -1
				}
				return swingSeqDir(s.klineBars(), lbv, nv) == want
			})
			t.Log(st.line(fmt.Sprintf("HH/HL lb=%d n=%d", lbv, nv)))
		}
	}
}

// 参数精度:lb 与 n 都是整数,没有小数精度可言 —— 真正要问的是
// 「相邻格之间结论是否翻转」。相邻格翻转 = 参数是调出来的,不可用。
// 这里把每个 lb 的三个 n 值并排打出,便于看单调性/稳定性。
func TestG15StructuralPrecision(t *testing.T) {
	samples := g15Guard(t)
	base := g15Score(samples, func(g15Sample) bool { return true })
	t.Logf("baseline mean=%+.3f", base.mean)
	t.Log("lb   n=2                      n=3                      n=4")
	for _, lb := range []int{1, 2, 3, 4, 5, 6, 8} {
		row := fmt.Sprintf("%-4d", lb)
		for _, n := range []int{2, 3, 4} {
			lbv, nv := lb, n
			st := g15Score(samples, func(s g15Sample) bool {
				want := 1
				if s.side == "SHORT" {
					want = -1
				}
				return swingSeqDir(s.klineBars(), lbv, nv) == want
			})
			if st.pass == 0 {
				row += "pass=0                   "
				continue
			}
			row += fmt.Sprintf("%+.3f(n=%3d,t=%+.2f)   ", st.mean, st.pass, st.t)
		}
		t.Log(row)
	}
}

// 反向:如果 HH/HL 在 15m 上符号为负,那么「反着用」(要求结构与开仓方向相反)
// 是否为正?这是判断「符号稳定的反向信号」还是「纯噪声」的关键 ——
// 纯噪声两侧都不显著,真反向则反着用显著为正。
func TestG15StructuralInverted(t *testing.T) {
	samples := g15Guard(t)
	t.Log(g15Score(samples, func(g15Sample) bool { return true }).line("baseline"))
	for _, lb := range []int{2, 3, 4} {
		for _, n := range []int{2, 3} {
			lbv, nv := lb, n
			t.Log(g15Score(samples, func(s g15Sample) bool {
				want := 1
				if s.side == "SHORT" {
					want = -1
				}
				return swingSeqDir(s.klineBars(), lbv, nv) == -want
			}).line(fmt.Sprintf("HH/HL INVERTED lb=%d n=%d", lbv, nv)))
			t.Log(g15Score(samples, func(s g15Sample) bool {
				return swingSeqDir(s.klineBars(), lbv, nv) == 0
			}).line(fmt.Sprintf("HH/HL NO-STRUCTURE lb=%d n=%d", lbv, nv)))
		}
	}
}

// ── 4) 其余可复算闸门的参数扫描 ──
func TestG15OtherGatesSweep(t *testing.T) {
	samples := g15Guard(t)
	t.Log(g15Score(samples, func(g15Sample) bool { return true }).line("baseline"))

	for _, p := range []int{10, 14, 20} {
		for _, thr := range []float64{15, 20, 25, 30} {
			pv, tv := p, thr
			t.Log(g15Score(samples, func(s g15Sample) bool {
				c := s.ctx()
				return sgADX(c.highs, c.lows, c.closes, pv) >= tv
			}).line(fmt.Sprintf("ADX(%d)>=%.0f", pv, tv)))
		}
	}
	for _, n := range []int{20, 48, 96} {
		nv := n
		t.Log(g15Score(samples, func(s g15Sample) bool {
			c := s.ctx()
			br := sgDonchianBreak(c.highs, c.lows, c.closes, nv)
			return !((br > 0 && c.side == "SHORT") || (br < 0 && c.side == "LONG"))
		}).line(fmt.Sprintf("donchian%d not-counter", nv)))
	}
	t.Log(g15Score(samples, func(s g15Sample) bool {
		c := s.ctx()
		return !sgConsensusChop(c.highs, c.lows, c.closes)
	}).line("consensus chop reject"))
	for _, w := range []int{30, 50, 72, 96} {
		wv := w
		t.Log(g15Score(samples, func(s g15Sample) bool {
			block, _, _ := counterTrend(s.ctx(), wv)
			return !block
		}).line(fmt.Sprintf("countertrend w=%d", wv)))
	}
}

// ── 5) 诊断:align 的实际取值分布 ──
// 起因:align>=0.55 在 369 笔上 pass=0,而 align>=0.40 放行 91.6%,且 lb=2/lb=3
// 结果逐位相同 —— 强烈提示取值退化(chartSwingAlign 在摆动点不足/正好半数时回 0.5)。
func TestG15AlignDistribution(t *testing.T) {
	samples := g15Guard(t)
	for _, lb := range []int{2, 3, 4} {
		hist := map[string]int{}
		var vals []float64
		for _, s := range samples {
			c := s.ctx()
			v := chartSwingAlign(c.highs, c.lows, c.side, lb)
			vals = append(vals, v)
			hist[fmt.Sprintf("%.3f", v)]++
		}
		sort.Float64s(vals)
		q := func(p float64) float64 { return vals[int(float64(len(vals)-1)*p)] }
		t.Logf("lb=%d  min=%.3f p10=%.3f p50=%.3f p90=%.3f max=%.3f  distinct=%d",
			lb, vals[0], q(0.10), q(0.50), q(0.90), vals[len(vals)-1], len(hist))
		type kv struct {
			k string
			v int
		}
		var top []kv
		for k, v := range hist {
			top = append(top, kv{k, v})
		}
		sort.Slice(top, func(i, j int) bool { return top[i].v > top[j].v })
		line := ""
		for i, e := range top {
			if i >= 6 {
				break
			}
			line += fmt.Sprintf("%s×%d  ", e.k, e.v)
		}
		t.Logf("       top values: %s", line)
	}
	// r2 分布(w=30,线上值)
	var r2s []float64
	for _, s := range samples {
		_, r2 := chartRegChannel(s.ctx().closes, 30)
		r2s = append(r2s, r2)
	}
	sort.Float64s(r2s)
	q := func(p float64) float64 { return r2s[int(float64(len(r2s)-1)*p)] }
	t.Logf("r2(w=30) p10=%.3f p25=%.3f p50=%.3f p75=%.3f p90=%.3f max=%.3f",
		q(0.10), q(0.25), q(0.50), q(0.75), q(0.90), r2s[len(r2s)-1])
	// 线上完整闸门:三条件与门
	st := g15Score(samples, func(s g15Sample) bool {
		block, _ := chartTrendGate(s.ctx(), 30, 0.55, 0.60, 2)
		return !block
	})
	t.Log(st.line("LIVE chart_trend (w=30,a=.55,r2=.60)"))
}

// g15HHHL 是线上 structural_alignment 的方向段（pct=0 时的全部生效逻辑）。
func g15HHHL(lb, n int) func(g15Sample) bool {
	return func(s g15Sample) bool {
		want := 1
		if s.side == "SHORT" {
			want = -1
		}
		return swingSeqDir(s.klineBars(), lb, n) == want
	}
}

// g15And 把多个闸门串成线上那样的「全部通过才放行」。
func g15And(fs ...func(g15Sample) bool) func(g15Sample) bool {
	return func(s g15Sample) bool {
		for _, f := range fs {
			if !f(s) {
				return false
			}
		}
		return true
	}
}

// TestG15StructuralPct 覆盖第二段:方向对了之后再要求离最近阻挡位 >= pct%。
// 线上部署默认 pct=0(该段关闭),这里看打开它是否还有增量。
func TestG15StructuralPct(t *testing.T) {
	samples := g15Guard(t)
	t.Log(g15Score(samples, func(g15Sample) bool { return true }).line("baseline"))
	for _, lb := range []int{3, 4, 5} {
		for _, pct := range []float64{0, 0.05, 0.15, 0.30, 0.50} {
			lbv, pv := lb, pct
			st := g15Score(samples, g15And(g15HHHL(lbv, 3), func(s g15Sample) bool {
				if pv <= 0 {
					return true
				}
				bars := s.klineBars()
				entry := bars[len(bars)-1].Close
				isLong := !strings.EqualFold(s.side, "SHORT")
				d, ok := nearestBlockingLevelPct(bars, entry, isLong, lbv)
				if !ok {
					return true // 无阻挡位 = 弃权放行,与线上一致
				}
				return d >= pv
			}))
			t.Log(st.line(fmt.Sprintf("HH/HL lb=%d n=3 pct>=%.2f", lbv, pv)))
		}
	}
}

// TestG15Combo 逐个叠加可复算的闸门,看组合是否优于单个。
// 关键约束:HH/HL n=3 单独就只剩 50~62 笔,任何叠加都会把样本压到 20~40 笔,
// 那时均值差异已经不可读 —— 所以这里同时打印 pass 数,pass<30 的结论不采信。
func TestG15Combo(t *testing.T) {
	samples := g15Guard(t)
	// counterTrend 返回 block；放行 = !block。
	ctr96 := func(s g15Sample) bool {
		block, _, _ := counterTrend(s.ctx(), 96)
		return !block
	}
	chop := func(s g15Sample) bool {
		c := s.ctx()
		return !sgConsensusChop(c.highs, c.lows, c.closes)
	}
	adx30 := func(s g15Sample) bool {
		c := s.ctx()
		return sgADX(c.highs, c.lows, c.closes, 10) >= 30
	}

	cases := []struct {
		label string
		f     func(g15Sample) bool
	}{
		{"baseline", func(g15Sample) bool { return true }},
		{"A: HH/HL lb=4 n=3", g15HHHL(4, 3)},
		{"B: HH/HL lb=5 n=3", g15HHHL(5, 3)},
		{"C: countertrend w=96", ctr96},
		{"D: not chop", chop},
		{"E: ADX(10)>=30", adx30},
		{"A+C", g15And(g15HHHL(4, 3), ctr96)},
		{"A+D", g15And(g15HHHL(4, 3), chop)},
		{"A+E", g15And(g15HHHL(4, 3), adx30)},
		{"B+C", g15And(g15HHHL(5, 3), ctr96)},
		{"B+D", g15And(g15HHHL(5, 3), chop)},
		{"C+D", g15And(ctr96, chop)},
		{"C+D+E", g15And(ctr96, chop, adx30)},
		{"A+C+D", g15And(g15HHHL(4, 3), ctr96, chop)},
		{"A+C+D+E", g15And(g15HHHL(4, 3), ctr96, chop, adx30)},
	}
	for _, c := range cases {
		t.Log(g15Score(samples, c.f).line(c.label))
	}
}

// TestG15WalkForward 把样本按时间切三折,只在前一折上「选最优」,
// 然后看这个选择在后一折上是否还成立。参数是否可用,只有这个检验说话。
func TestG15WalkForward(t *testing.T) {
	samples := g15Guard(t)
	sorted := make([]g15Sample, len(samples))
	copy(sorted, samples)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].bars[len(sorted[i].bars)-1].CloseTime < sorted[j].bars[len(sorted[j].bars)-1].CloseTime
	})
	nf := len(sorted) / 3
	folds := [][]g15Sample{sorted[:nf], sorted[nf : 2*nf], sorted[2*nf:]}

	type cand struct {
		label string
		f     func(g15Sample) bool
	}
	cands := []cand{}
	for _, lb := range []int{1, 2, 3, 4, 5, 6, 8} {
		for _, n := range []int{2, 3, 4} {
			cands = append(cands, cand{fmt.Sprintf("HH/HL lb=%d n=%d", lb, n), g15HHHL(lb, n)})
		}
	}
	for fi := 0; fi < 2; fi++ {
		best, bestMean, bestPass := "", -1e9, 0
		for _, c := range cands {
			st := g15Score(folds[fi], c.f)
			if st.pass < 10 {
				continue // 折内样本太少,不参与选优
			}
			if st.mean > bestMean {
				best, bestMean, bestPass = c.label, st.mean, st.pass
			}
		}
		var chosen func(g15Sample) bool
		for _, c := range cands {
			if c.label == best {
				chosen = c.f
			}
		}
		nextBase := g15Score(folds[fi+1], func(g15Sample) bool { return true })
		nextSt := g15Score(folds[fi+1], chosen)
		t.Logf("fold%d 选优 => %-18s (fold%d mean=%+.3f n=%d) | fold%d 实测 mean=%+.3f n=%d vs 该折基线 %+.3f n=%d",
			fi+1, best, fi+1, bestMean, bestPass, fi+2, nextSt.mean, nextSt.pass, nextBase.mean, nextBase.pass)
	}
	// 固定候选在三折上的逐折表现,看是否折折为正。
	for _, c := range []cand{
		{"HH/HL lb=4 n=3", g15HHHL(4, 3)},
		{"HH/HL lb=5 n=3", g15HHHL(5, 3)},
		{"HH/HL lb=3 n=3", g15HHHL(3, 3)},
		{"HH/HL lb=6 n=3", g15HHHL(6, 3)},
	} {
		row := fmt.Sprintf("%-16s", c.label)
		for fi := range folds {
			st := g15Score(folds[fi], c.f)
			bs := g15Score(folds[fi], func(g15Sample) bool { return true })
			row += fmt.Sprintf(" | f%d %+.3f(n=%2d, base %+.3f)", fi+1, st.mean, st.pass, bs.mean)
		}
		t.Log(row)
	}
}

// TestG15ComboWalkForward 只做一件事:把最终候选组合放到三折上逐折看。
// 组合是从同一份数据上挑出来的,所以「总体均值」已经没有证据力;
// 唯一还有意义的问题是「它是否折折都为正,且折折都优于该折基线」。
func TestG15ComboWalkForward(t *testing.T) {
	samples := g15Guard(t)
	sorted := make([]g15Sample, len(samples))
	copy(sorted, samples)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].bars[len(sorted[i].bars)-1].CloseTime < sorted[j].bars[len(sorted[j].bars)-1].CloseTime
	})
	nf := len(sorted) / 3
	folds := [][]g15Sample{sorted[:nf], sorted[nf : 2*nf], sorted[2*nf:]}

	ctr96 := func(s g15Sample) bool {
		block, _, _ := counterTrend(s.ctx(), 96)
		return !block
	}
	chop := func(s g15Sample) bool {
		c := s.ctx()
		return !sgConsensusChop(c.highs, c.lows, c.closes)
	}
	adx30 := func(s g15Sample) bool {
		c := s.ctx()
		return sgADX(c.highs, c.lows, c.closes, 10) >= 30
	}
	pctGate := func(lb int, minPct float64) func(g15Sample) bool {
		return func(s g15Sample) bool {
			bars := s.klineBars()
			entry := bars[len(bars)-1].Close
			isLong := !strings.EqualFold(s.side, "SHORT")
			d, ok := nearestBlockingLevelPct(bars, entry, isLong, lb)
			if !ok {
				return true
			}
			return d >= minPct
		}
	}

	cands := []struct {
		label string
		f     func(g15Sample) bool
	}{
		{"C+D (高通过率)", g15And(ctr96, chop)},
		{"C+D+E", g15And(ctr96, chop, adx30)},
		{"A=HH/HL lb4n3", g15HHHL(4, 3)},
		{"A+C", g15And(g15HHHL(4, 3), ctr96)},
		{"A+C+D", g15And(g15HHHL(4, 3), ctr96, chop)},
		{"A pct>=0.30", g15And(g15HHHL(4, 3), pctGate(4, 0.30))},
		{"A+C pct>=0.30", g15And(g15HHHL(4, 3), ctr96, pctGate(4, 0.30))},
	}
	for _, c := range cands {
		row := fmt.Sprintf("%-16s", c.label)
		neg := 0
		for fi := range folds {
			st := g15Score(folds[fi], c.f)
			bs := g15Score(folds[fi], func(g15Sample) bool { return true })
			flag := " "
			if st.mean <= bs.mean {
				flag = "!"
				neg++
			}
			row += fmt.Sprintf(" | f%d %+.3f(n=%2d)%s", fi+1, st.mean, st.pass, flag)
		}
		row += fmt.Sprintf("  折数劣于基线=%d/3", neg)
		t.Log(row)
	}
	t.Log("基线逐折: f1 -0.127  f2 -0.108  f3 +0.022  (! = 该折未跑赢基线)")
}

// gate15mChartTrendEnabledMS 是 chart_trend 以 enforce 模式上线的时刻
// (2026-07-16 00:00 UTC)。此后**已开仓**的单子,线上必然通过了该闸门。
const gate15mChartTrendEnabledMS int64 = 1784160000000

// TestG15GroundTruth 是整套 harness 唯一的正确性硬检验。
//
// 逻辑:chart_trend 自 2026-07-16 起是 enforce。那么该时刻之后**成功开仓**的每
// 一笔,线上都判过了 (w=30, align>=0.55, r2>=0.60)。如果我的复算判它们不过,
// 说明我的窗口/口径与线上不一致,前面所有扫描结论作废。
//
// 反过来,闸门上线**之前**的样本没有经过它,复算通过率应显著低于 100% ——
// 若也接近 100%,说明我的复算根本没在起作用。
func TestG15GroundTruth(t *testing.T) {
	samples := g15Guard(t)
	// 直接调生产函数,参数用线上现值。
	// 注意 chartTrendGate 返回的是 block 而非 allow(见 chart_trend_gate.go:107),
	// 放行 = !block。
	live := func(s g15Sample) bool {
		block, _ := chartTrendGate(s.ctx(), 30, 0.55, 0.60, 2)
		return !block
	}
	var postN, postPass, preN, prePass int
	for _, s := range samples {
		entryMS := s.bars[len(s.bars)-1].CloseTime
		if entryMS >= gate15mChartTrendEnabledMS {
			postN++
			if live(s) {
				postPass++
			}
		} else {
			preN++
			if live(s) {
				prePass++
			}
		}
	}
	t.Logf("闸门上线后 (线上真值=全部通过): 复算通过 %d/%d = %.1f%%", postPass, postN, 100*float64(postPass)/float64(postN))
	t.Logf("闸门上线前 (未经过该闸门):     复算通过 %d/%d = %.1f%%", prePass, preN, 100*float64(prePass)/float64(preN))
	if postN > 0 && float64(postPass)/float64(postN) < 0.80 {
		t.Errorf("口径不一致:闸门上线后的样本线上全部通过,复算却只过 %.1f%% —— 前面的扫描结论不可用",
			100*float64(postPass)/float64(postN))
	}
}

// TestG15GroundTruthOffset 诊断 TestG15GroundTruth 的残差来源。
//
// 已知:闸门上线后的样本线上 100% 通过,我的复算只有 61%。候选原因:
//  1. 差一根 K 线 —— 线上那 120 根含入场时刻正在形成的那根,我只用已收盘的;
//     且 entry_time 是**成交**时刻,可能比决策时刻晚若干分钟,跨了 15m 边界。
//  2. 交易所不同 —— 我用 Binance Vision,线上部分币可能取自别的所。
//
// 做法:把窗口右端在 [-2, +3] 根之间平移,看哪个偏移把「闸门后通过率」顶到最高。
// 若某个偏移显著更高,原因 1 成立,且该偏移就是正确口径。
func TestG15GroundTruthOffset(t *testing.T) {
	samples := g15Guard(t)
	for off := -2; off <= 3; off++ {
		var postN, postPass, preN, prePass int
		for _, s := range samples {
			// s.bars 右端是「入场前最后一根已收盘」。off>0 表示往未来多取几根。
			end := len(s.bars) + off
			if end < gate15mMinBars || end > len(s.bars) {
				// 往未来取需要原始序列,这里只能往回缩;off>0 用 futureBars 补。
				if off > 0 && len(s.futureBars) >= off {
					// 拼接:已收盘窗口 + 未来 off 根
					b := make([]g15Bar, 0, len(s.bars)+off)
					b = append(b, s.bars...)
					b = append(b, s.futureBars[:off]...)
					if len(b) > gate15mLiveWindow {
						b = b[len(b)-gate15mLiveWindow:]
					}
					sh := s
					sh.bars = b
					block, _ := chartTrendGate(sh.ctx(), 30, 0.55, 0.60, 2)
					if s.bars[len(s.bars)-1].CloseTime >= gate15mChartTrendEnabledMS {
						postN++
						if !block {
							postPass++
						}
					} else {
						preN++
						if !block {
							prePass++
						}
					}
				}
				continue
			}
			sh := s
			sh.bars = s.bars[:end]
			block, _ := chartTrendGate(sh.ctx(), 30, 0.55, 0.60, 2)
			if s.bars[len(s.bars)-1].CloseTime >= gate15mChartTrendEnabledMS {
				postN++
				if !block {
					postPass++
				}
			} else {
				preN++
				if !block {
					prePass++
				}
			}
		}
		pp, qq := 0.0, 0.0
		if postN > 0 {
			pp = 100 * float64(postPass) / float64(postN)
		}
		if preN > 0 {
			qq = 100 * float64(prePass) / float64(preN)
		}
		t.Logf("offset=%+d  闸门后 %3d/%3d=%5.1f%%   闸门前 %3d/%3d=%5.1f%%   分离度=%.1fx",
			off, postPass, postN, pp, prePass, preN, qq, pp/math.Max(qq, 0.01))
	}
}

// TestG15FinalDecision 回答一个具体问题:方案2(C+D+E)与方案3(HH/HL lb4n3)
// 该单开还是一起开。
//
// 判据三条,缺一不可:
//  1. 逐折都跑赢该折基线(不能只看总均值);
//  2. 每折样本 >= 30(否则均值不可读);
//  3. 开仓频率不能低到让策略事实上停摆。
func TestG15FinalDecision(t *testing.T) {
	samples := g15Guard(t)

	ctr96 := func(s g15Sample) bool { block, _, _ := counterTrend(s.ctx(), 96); return !block }
	chop := func(s g15Sample) bool { c := s.ctx(); return !sgConsensusChop(c.highs, c.lows, c.closes) }
	adx30 := func(s g15Sample) bool { c := s.ctx(); return sgADX(c.highs, c.lows, c.closes, 10) >= 30 }
	hhhl := g15HHHL(4, 3)

	plan2 := g15And(ctr96, chop, adx30)
	plan3 := hhhl
	both := g15And(ctr96, chop, adx30, hhhl)

	// 观测跨度(天),用于折算开仓频率。
	var minMS, maxMS int64 = math.MaxInt64, 0
	for _, s := range samples {
		ct := s.bars[len(s.bars)-1].CloseTime
		if ct < minMS {
			minMS = ct
		}
		if ct > maxMS {
			maxMS = ct
		}
	}
	days := float64(maxMS-minMS) / 86400000.0

	// 重叠度:方案3放行集有多少落在方案2放行集内。
	var n2, n3, nBoth int
	for _, s := range samples {
		a, b := plan2(s), plan3(s)
		if a {
			n2++
		}
		if b {
			n3++
		}
		if a && b {
			nBoth++
		}
	}
	t.Logf("观测跨度 %.1f 天,基线 %d 笔 = %.2f 笔/天", days, len(samples), float64(len(samples))/days)
	t.Logf("重叠度: 方案2放行=%d 方案3放行=%d 交集=%d | 方案3中已被方案2覆盖的比例=%.1f%% | Jaccard=%.2f",
		n2, n3, nBoth, 100*float64(nBoth)/float64(n3),
		float64(nBoth)/float64(n2+n3-nBoth))

	sorted := make([]g15Sample, len(samples))
	copy(sorted, samples)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].bars[len(sorted[i].bars)-1].CloseTime < sorted[j].bars[len(sorted[j].bars)-1].CloseTime
	})
	nf := len(sorted) / 3
	folds := [][]g15Sample{sorted[:nf], sorted[nf : 2*nf], sorted[2*nf:]}

	for _, c := range []struct {
		label string
		f     func(g15Sample) bool
	}{
		{"方案2 C+D+E", plan2},
		{"方案3 HH/HL lb4n3", plan3},
		{"2+3 一起开", both},
	} {
		st := g15Score(samples, c.f)
		t.Log(st.line(c.label))
		row := fmt.Sprintf("   逐折 %-18s", "")
		thin := 0
		for fi := range folds {
			fs := g15Score(folds[fi], c.f)
			bs := g15Score(folds[fi], func(g15Sample) bool { return true })
			flag := " "
			if fs.mean <= bs.mean {
				flag = "✗"
			}
			if fs.pass < 30 {
				thin++
			}
			row += fmt.Sprintf(" | f%d %+.3f(n=%2d)%s", fi+1, fs.mean, fs.pass, flag)
		}
		row += fmt.Sprintf("  样本不足(<30)的折=%d/3  开仓=%.2f 笔/天", thin, float64(st.pass)/days)
		t.Log(row)
	}
	t.Log("基线逐折: f1 -0.127  f2 -0.108  f3 +0.022")
}

// TestG15Deployable 只评估**当前配置层能原样表达**的组合。
//
// 卡点:方案2 里的 E 项我测的是 ADX(10)>=30,但生产 adx_weak 分支把周期硬编码为
// 14(regime_gate.go: sgADX(ctx..., 14)),只暴露 threshold。而 ADX(14) 各档全为负
// (>=25 → -0.064, >=30 → -0.120)。所以不改代码时,可落地的是 C+D。
func TestG15Deployable(t *testing.T) {
	samples := g15Guard(t)
	ctr96 := func(s g15Sample) bool { block, _, _ := counterTrend(s.ctx(), 96); return !block }
	chop := func(s g15Sample) bool { c := s.ctx(); return !sgConsensusChop(c.highs, c.lows, c.closes) }
	adx14 := func(thr float64) func(g15Sample) bool {
		return func(s g15Sample) bool { c := s.ctx(); return sgADX(c.highs, c.lows, c.closes, 14) >= thr }
	}
	adx10 := func(s g15Sample) bool { c := s.ctx(); return sgADX(c.highs, c.lows, c.closes, 10) >= 30 }
	hhhl := g15HHHL(4, 3)

	var minMS, maxMS int64 = math.MaxInt64, 0
	for _, s := range samples {
		ct := s.bars[len(s.bars)-1].CloseTime
		if ct < minMS {
			minMS = ct
		}
		if ct > maxMS {
			maxMS = ct
		}
	}
	days := float64(maxMS-minMS) / 86400000.0

	sorted := make([]g15Sample, len(samples))
	copy(sorted, samples)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].bars[len(sorted[i].bars)-1].CloseTime < sorted[j].bars[len(sorted[j].bars)-1].CloseTime
	})
	nf := len(sorted) / 3
	folds := [][]g15Sample{sorted[:nf], sorted[nf : 2*nf], sorted[2*nf:]}

	for _, c := range []struct {
		label string
		f     func(g15Sample) bool
	}{
		{"C+D  (配置即可)", g15And(ctr96, chop)},
		{"C+D+ADX14>=20", g15And(ctr96, chop, adx14(20))},
		{"C+D+ADX14>=25", g15And(ctr96, chop, adx14(25))},
		{"C+D+ADX10>=30 (需改代码)", g15And(ctr96, chop, adx10)},
		{"C+D + HH/HL", g15And(ctr96, chop, hhhl)},
	} {
		st := g15Score(samples, c.f)
		t.Log(st.line(c.label))
		row := fmt.Sprintf("   逐折 %-14s", "")
		thin := 0
		for fi := range folds {
			fs := g15Score(folds[fi], c.f)
			bs := g15Score(folds[fi], func(g15Sample) bool { return true })
			flag := " "
			if fs.mean <= bs.mean {
				flag = "✗"
			}
			if fs.pass < 30 {
				thin++
			}
			row += fmt.Sprintf(" | f%d %+.3f(n=%2d)%s", fi+1, fs.mean, fs.pass, flag)
		}
		row += fmt.Sprintf("  折样本<30: %d/3  %.2f 笔/天", thin, float64(st.pass)/days)
		t.Log(row)
	}
	t.Log("基线: mean=-0.071  8.96 笔/天  逐折 f1 -0.127 f2 -0.108 f3 +0.022")
}
