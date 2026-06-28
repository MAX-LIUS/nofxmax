package main

// protectsim replays historical closed positions through configurable
// TP/BE/DD/SL protection ladders to compare parameter sets on REAL price paths.
//
// Method: for each closed position we fetch the 15m price path between entry and
// exit, compute the ATR(14,1h) frozen at entry, then walk bars applying a ladder.
// PnL = sum(closedQty * (fillPrice-entry) * dir) - fees. Quantity not closed by
// the ladder by the actual exit time is closed at the actual exit price (residual
// follows the real-world non-price outcome: ai_close/max_hold/manual). This
// isolates the effect of the price-driven protection ladder. The breaker is
// portfolio-breadth driven, analyzed separately from close_events.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const (
	dbPath    = "/opt/webstack/nofx/data/data.db"
	cacheDir  = "/tmp/protectsim_cache"
	feeRate   = 0.0005 // 0.05% taker per close side
	atrPeriod = 14
)

// Track-2 faithfulness controls. When simExtendMs>0, replay does NOT stop at the
// position's original (AI/reversal) exit time. Instead the protection ladder runs
// forward up to ExitTime+simExtendMs (capped), and whatever it doesn't close is
// settled at the LAST observed bar close — exposing the reversal/tail losses that
// the truncated window hid. simExitBoundFor() centralizes the per-position bound.
var (
	simExtendMs  int64 = 0 // 0 = truncated (legacy); >0 = extended replay window
	simMaxHoldMs int64 = 30 * 24 * 60 * 60 * 1000 // hard cap on total hold (30 days)
)

func simExitBoundFor(p Position) int64 {
	if simExtendMs <= 0 {
		return p.ExitTime
	}
	bound := p.ExitTime + simExtendMs
	cap := p.EntryTime + simMaxHoldMs
	if bound > cap {
		bound = cap
	}
	return bound
}

type Position struct {
	ID         int64
	Symbol     string
	Side       string // LONG/SHORT
	Qty        float64
	Entry      float64
	EntryTime  int64 // ms
	Exit       float64
	ExitTime   int64 // ms
	Realized   float64
	Fee        float64
	CloseReason string
}

type Bar struct {
	T                   int64
	Open, High, Low, Close float64
}

func dirMul(side string) float64 {
	if strings.EqualFold(side, "LONG") {
		return 1
	}
	return -1
}

func instID(symbol string) string {
	s := strings.ToUpper(symbol)
	if !strings.HasSuffix(s, "USDT") {
		return ""
	}
	base := strings.TrimSuffix(s, "USDT")
	if base == "" {
		return ""
	}
	return base + "-USDT-SWAP"
}

func loadPositions(db *sql.DB) ([]Position, error) {
	rows, err := db.Query(`SELECT id,symbol,side,entry_quantity,entry_price,entry_time,
		exit_price,exit_time,realized_pnl,fee,close_reason
		FROM trader_positions WHERE status='CLOSED' AND entry_quantity>0
		AND exit_price>0 AND exit_time>entry_time ORDER BY entry_time`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Position
	for rows.Next() {
		var p Position
		if err := rows.Scan(&p.ID, &p.Symbol, &p.Side, &p.Qty, &p.Entry, &p.EntryTime,
			&p.Exit, &p.ExitTime, &p.Realized, &p.Fee, &p.CloseReason); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// fetchOKX pages history-candles backward covering [startMs,endMs]. Returns bars
// ascending by time. Caches per symbol+bar to disk as JSON.
func fetchOKX(symbol, bar string, startMs, endMs int64) ([]Bar, error) {
	inst := instID(symbol)
	if inst == "" {
		return nil, fmt.Errorf("unmappable symbol %s", symbol)
	}
	cacheFile := filepath.Join(cacheDir, fmt.Sprintf("%s_%s.json", inst, bar))
	if b, err := os.ReadFile(cacheFile); err == nil {
		var cached []Bar
		if json.Unmarshal(b, &cached) == nil && len(cached) > 0 {
			if cached[0].T <= startMs && cached[len(cached)-1].T >= endMs-barMs(bar) {
				return sliceBars(cached, startMs, endMs), nil
			}
		}
	}
	client := &http.Client{Timeout: 15 * time.Second}
	var all []Bar
	cursor := endMs + barMs(bar)
	for cursor > startMs {
		url := fmt.Sprintf("https://www.okx.com/api/v5/market/history-candles?instId=%s&bar=%s&after=%d&limit=100",
			inst, bar, cursor)
		req, _ := http.NewRequest("GET", url, nil)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var parsed struct {
			Code string     `json:"code"`
			Msg  string     `json:"msg"`
			Data [][]string `json:"data"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, err
		}
		if parsed.Code != "0" {
			return nil, fmt.Errorf("okx %s: %s", parsed.Code, parsed.Msg)
		}
		if len(parsed.Data) == 0 {
			break
		}
		for _, d := range parsed.Data {
			t, _ := strconv.ParseInt(d[0], 10, 64)
			o, _ := strconv.ParseFloat(d[1], 64)
			h, _ := strconv.ParseFloat(d[2], 64)
			l, _ := strconv.ParseFloat(d[3], 64)
			c, _ := strconv.ParseFloat(d[4], 64)
			all = append(all, Bar{T: t, Open: o, High: h, Low: l, Close: c})
		}
		oldest := all[len(all)-1].T
		if oldest <= startMs {
			break
		}
		cursor = oldest
		time.Sleep(120 * time.Millisecond)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].T < all[j].T })
	dedup := all[:0]
	var last int64 = -1
	for _, b := range all {
		if b.T != last {
			dedup = append(dedup, b)
			last = b.T
		}
	}
	_ = os.MkdirAll(cacheDir, 0o755)
	if b, err := json.Marshal(dedup); err == nil {
		_ = os.WriteFile(cacheFile, b, 0o644)
	}
	return sliceBars(dedup, startMs, endMs), nil
}

func barMs(bar string) int64 {
	switch bar {
	case "15m":
		return 15 * 60 * 1000
	case "5m":
		return 5 * 60 * 1000
	case "1H":
		return 60 * 60 * 1000
	}
	return 60 * 1000
}

func sliceBars(bars []Bar, startMs, endMs int64) []Bar {
	var out []Bar
	for _, b := range bars {
		if b.T >= startMs-barMs("1H") && b.T <= endMs+barMs("15m") {
			out = append(out, b)
		}
	}
	return out
}

// atrAtEntry computes Wilder ATR(14) on 1h bars using bars strictly before entry.
func atrAtEntry(bars1h []Bar, entryTime int64) float64 {
	var hist []Bar
	for _, b := range bars1h {
		if b.T < entryTime {
			hist = append(hist, b)
		}
	}
	if len(hist) < atrPeriod+1 {
		return 0
	}
	trs := make([]float64, 0, len(hist)-1)
	for i := 1; i < len(hist); i++ {
		hl := hist[i].High - hist[i].Low
		hc := math.Abs(hist[i].High - hist[i-1].Close)
		lc := math.Abs(hist[i].Low - hist[i-1].Close)
		tr := hl
		if hc > tr {
			tr = hc
		}
		if lc > tr {
			tr = lc
		}
		trs = append(trs, tr)
	}
	if len(trs) < atrPeriod {
		return 0
	}
	var sum float64
	for i := 0; i < atrPeriod; i++ {
		sum += trs[i]
	}
	atr := sum / float64(atrPeriod)
	for i := atrPeriod; i < len(trs); i++ {
		atr = (atr*float64(atrPeriod-1) + trs[i]) / float64(atrPeriod)
	}
	return atr
}

// --- Ladder config (all distances in ATR multiples) ---

type TPTier struct {
	ATR      float64 // arm/trigger distance in ATR multiples (profit side)
	ClosePct float64 // % of ORIGINAL qty
}

// Floor = arm-high-then-stop-low. Once peak profit reaches ArmATR, a stop is set
// at LockATR (profit side, can be negative for loss-side protection). If price
// later retraces to the stop, ClosePct of original qty is closed.
// For DD-style giveback, set Giveback>0: lock = ArmATR*(1-Giveback) instead of LockATR.
type Floor struct {
	ArmATR   float64
	LockATR  float64 // used when Giveback<=0
	Giveback float64 // 0..1; when >0, lock = ArmATR*(1-Giveback)
	ClosePct float64 // % of ORIGINAL qty (capped to remaining)
	Loss     bool    // true => loss-side (arm on adverse excursion, stop tighter toward entry)
}

type Ladder struct {
	Name        string
	MinEffPct   float64 // floor on effective %, mirrors min_eff_pct
	TP          []TPTier
	Floors      []Floor
	PartialSL   float64 // partial stop in ATR multiples (loss side), 0 = none
	PartialSLPct float64 // % of original qty closed at PartialSL
	SLATR       float64 // full stop in ATR multiples (loss side), 0 = none
	SLClosePct  float64

	// Engine-semantics DD: when EngineDD is true, replay() routes to replayEngine,
	// which models the REAL trader: DD tiers are pre-sliced at open (cumulative cap
	// 100% of original qty), higher tiers supersede lower ones when armed, the active
	// tracking tier fires its fixed slice on its own giveback, and everything left
	// (superseded + unreached slices) is handed to a break-even stop near entry.
	EngineDD bool
	DDTiers  []DDTier
	// BE break-even floors used alongside engine DD (arm high → lock near entry).
	BEArm  []float64 // arm ATR multiples (profit reached before BE stop is set)
	BELock []float64 // lock ATR multiples (stop level, near entry)
}

// DDTier mirrors store.DrawdownTakeProfitRule for engine-semantics replay.
type DDTier struct {
	ArmATR   float64 // min_profit_pct in ATR units: tier arms/tracks when fav >= this
	Giveback float64 // max_drawdown_pct/100: fraction of peak profit given back to fire
	SlicePct float64 // close_ratio_pct: % of ORIGINAL qty, cumulative-capped at alloc
}

// effPct clamps an ATR-multiple distance to a min effective percent, returning
// the price fraction (e.g. 0.0094 for 0.94%).
func (l Ladder) effFrac(atrMult, atrPct float64) float64 {
	p := atrMult * atrPct
	if p < l.MinEffPct {
		p = l.MinEffPct
	}
	return p / 100.0
}

type SimResult struct {
	NetPnL    float64
	GrossPnL  float64
	Fees      float64
	Closes    int
	TierFires map[string]int
	TierPnL   map[string]float64
	ExitedFlat bool // true if ladder fully closed before actual exit
}

// PathMetrics characterizes a position's realized price path in ATR units.
type PathMetrics struct {
	MFEAtr float64 // max favorable excursion (profit side) in ATR multiples
	MAEAtr float64 // max adverse excursion (loss side) in ATR multiples (>=0)
	EffRatio float64 // |net move| / total path travel; low => oscillation
	Bars   int
}

func pathMetrics(p Position, bars []Bar, atrAbs float64) PathMetrics {
	var pm PathMetrics
	if atrAbs <= 0 || p.Entry <= 0 {
		return pm
	}
	dir := dirMul(p.Side)
	var maxFav, maxAdv, travel float64
	var prevClose float64
	first := true
	for _, b := range bars {
		if b.T < p.EntryTime || b.T > p.ExitTime {
			continue
		}
		// favorable excursion = best (most positive) of high/low in trade direction
		fHigh := (b.High - p.Entry) * dir
		fLow := (b.Low - p.Entry) * dir
		hiFav := math.Max(fHigh, fLow)
		loFav := math.Min(fHigh, fLow)
		if hiFav > maxFav {
			maxFav = hiFav
		}
		if -loFav > maxAdv {
			maxAdv = -loFav
		}
		if !first {
			travel += math.Abs(b.Close - prevClose)
		}
		prevClose = b.Close
		first = false
		pm.Bars++
	}
	pm.MFEAtr = maxFav / atrAbs
	pm.MAEAtr = maxAdv / atrAbs
	net := math.Abs((p.Exit - p.Entry))
	if travel > 0 {
		pm.EffRatio = net / travel
	}
	return pm
}

// replay walks 15m bars applying the ladder. atrPct = atr/entry*100.
func replay(p Position, bars []Bar, atrAbs float64, l Ladder) SimResult {
	if l.EngineDD {
		return replayEngine(p, bars, atrAbs, l)
	}
	res := SimResult{TierFires: map[string]int{}, TierPnL: map[string]float64{}}
	if atrAbs <= 0 || p.Entry <= 0 || len(bars) == 0 {
		return res
	}
	atrPct := atrAbs / p.Entry * 100
	dir := dirMul(p.Side)
	remaining := p.Qty
	addClose := func(tag string, qty, price float64) {
		if qty <= 0 {
			return
		}
		pnl := qty * (price - p.Entry) * dir
		fee := qty * price * feeRate
		res.GrossPnL += pnl
		res.Fees += fee
		res.NetPnL += pnl - fee
		res.Closes++
		res.TierFires[tag]++
		res.TierPnL[tag] += pnl - fee
		remaining -= qty
	}

	// Precompute trigger prices (profit side positive frac in dir).
	tpPrice := make([]float64, len(l.TP))
	tpDone := make([]bool, len(l.TP))
	for i, t := range l.TP {
		tpPrice[i] = p.Entry * (1 + dir*l.effFrac(t.ATR, atrPct))
	}
	floorArm := make([]float64, len(l.Floors))
	floorStop := make([]float64, len(l.Floors))
	floorArmed := make([]bool, len(l.Floors))
	floorDone := make([]bool, len(l.Floors))
	for i, f := range l.Floors {
		if f.Loss {
			floorArm[i] = p.Entry * (1 - dir*l.effFrac(f.ArmATR, atrPct))   // adverse arm
			floorStop[i] = p.Entry * (1 - dir*l.effFrac(f.LockATR, atrPct)) // tighter adverse stop
		} else {
			floorArm[i] = p.Entry * (1 + dir*l.effFrac(f.ArmATR, atrPct))
			lockATR := f.LockATR
			if f.Giveback > 0 {
				lockATR = f.ArmATR * (1 - f.Giveback)
			}
			floorStop[i] = p.Entry * (1 + dir*l.effFrac(lockATR, atrPct))
		}
	}
	var slPrice float64
	if l.SLATR > 0 {
		slPrice = p.Entry * (1 - dir*l.effFrac(l.SLATR, atrPct))
	}
	var partialSLPrice float64
	partialSLDone := false
	if l.PartialSL > 0 && l.PartialSLPct > 0 {
		partialSLPrice = p.Entry * (1 - dir*l.effFrac(l.PartialSL, atrPct))
	}

	favReached := func(price float64) float64 { return (price - p.Entry) * dir } // >0 profit side

	exitBound := simExitBoundFor(p)
	var lastClose float64
	for _, b := range bars {
		if b.T < p.EntryTime || b.T > exitBound {
			continue
		}
		lastClose = b.Close
		if remaining <= 1e-9 {
			break
		}
		// Pessimistic: within a bar, adverse extreme is hit before favorable.
		adverse := b.Low
		favorable := b.High
		if dir < 0 {
			adverse = b.High
			favorable = b.Low
		}

		// 1) Partial SL (fires before full SL since it's the nearer level)
		if partialSLPrice > 0 && !partialSLDone && remaining > 1e-9 {
			hit := (dir > 0 && adverse <= partialSLPrice) || (dir < 0 && adverse >= partialSLPrice)
			if hit {
				qty := p.Qty * l.PartialSLPct / 100
				if qty > remaining {
					qty = remaining
				}
				addClose("PartialSL", qty, partialSLPrice)
				partialSLDone = true
			}
		}
		// 2) Full SL (worst case)
		if slPrice > 0 && remaining > 1e-9 {
			hit := (dir > 0 && adverse <= slPrice) || (dir < 0 && adverse >= slPrice)
			if hit {
				qty := p.Qty * l.SLClosePct / 100
				if qty > remaining {
					qty = remaining
				}
				addClose("SL", qty, slPrice)
			}
		}
		// 2) Loss-side floors (arm on adverse, stop tighter toward worse)
		for i, f := range l.Floors {
			if !f.Loss || floorDone[i] || remaining <= 1e-9 {
				continue
			}
			if !floorArmed[i] {
				armed := (dir > 0 && adverse <= floorArm[i]) || (dir < 0 && adverse >= floorArm[i])
				if armed {
					floorArmed[i] = true
				}
			}
			if floorArmed[i] {
				hit := (dir > 0 && adverse <= floorStop[i]) || (dir < 0 && adverse >= floorStop[i])
				if hit {
					qty := p.Qty * f.ClosePct / 100
					if qty > remaining {
						qty = remaining
					}
					addClose(fmt.Sprintf("LossDD%d", i+1), qty, floorStop[i])
					floorDone[i] = true
				}
			}
		}
		// 3) Profit floors: check retrace-to-stop for already-armed floors using adverse
		for i, f := range l.Floors {
			if f.Loss || floorDone[i] || remaining <= 1e-9 {
				continue
			}
			if floorArmed[i] {
				hit := (dir > 0 && adverse <= floorStop[i]) || (dir < 0 && adverse >= floorStop[i])
				if hit {
					qty := p.Qty * f.ClosePct / 100
					if qty > remaining {
						qty = remaining
					}
					tag := "BE"
					if f.Giveback > 0 {
						tag = "DD"
					}
					addClose(fmt.Sprintf("%s%d", tag, i+1), qty, floorStop[i])
					floorDone[i] = true
				}
			}
		}
		// 4) TP fills on favorable extreme
		for i := range l.TP {
			if tpDone[i] || remaining <= 1e-9 {
				continue
			}
			hit := (dir > 0 && favorable >= tpPrice[i]) || (dir < 0 && favorable <= tpPrice[i])
			if hit {
				qty := p.Qty * l.TP[i].ClosePct / 100
				if qty > remaining {
					qty = remaining
				}
				addClose(fmt.Sprintf("TP%d", i+1), qty, tpPrice[i])
				tpDone[i] = true
			}
		}
		// 5) Arm profit floors when favorable reaches arm level (after TP, so same-bar
		//    arm doesn't immediately stop out on the same candle).
		for i, f := range l.Floors {
			if f.Loss || floorDone[i] || floorArmed[i] {
				continue
			}
			if favReached(favorable) >= l.effFrac(f.ArmATR, atrPct)*p.Entry {
				floorArmed[i] = true
			}
		}
	}

	// Residual settlement. Truncated mode: close at the real (AI/reversal) exit
	// price. Extended mode: the protection ladder never closed within the window,
	// so settle at the LAST observed bar close (the honest mark, including any
	// reversal-driven adverse drift past the original exit).
	if remaining > 1e-9 {
		px := p.Exit
		if simExtendMs > 0 && lastClose > 0 {
			px = lastClose
		}
		addClose("residual", remaining, px)
		res.ExitedFlat = false
	} else {
		res.ExitedFlat = true
	}
	return res
}

// replayEngine models the REAL trader's protection stack faithfully:
//   - TP ladder: fixed slices of original qty, fired on favorable extreme.
//   - DD tiers: pre-sliced at open (cumulative cap 100% of original qty, mirrors
//     computeDrawdownTierAllocations). A tier "tracks" once fav >= its ArmATR, and
//     arming a higher tier marks all lower pending/tracking tiers "superseded"
//     (mirrors evaluateDrawdownTiers single-direction upgrade). The active tracking
//     tier fires its OWN fixed slice when price gives back Giveback of its peak.
//   - BE: superseded + unreached DD slices are protected by a break-even stop that
//     arms at BEArm and stops near entry (BELock). This is where lost-runner profit
//     leaks: those slices exit near entry, not at the giveback level.
//   - Loss side: PartialSL then full SL, identical to floor replay.
func replayEngine(p Position, bars []Bar, atrAbs float64, l Ladder) SimResult {
	res := SimResult{TierFires: map[string]int{}, TierPnL: map[string]float64{}}
	if atrAbs <= 0 || p.Entry <= 0 || len(bars) == 0 {
		return res
	}
	atrPct := atrAbs / p.Entry * 100
	dir := dirMul(p.Side)
	remaining := p.Qty
	addClose := func(tag string, qty, price float64) {
		if qty <= 1e-12 {
			return
		}
		if qty > remaining {
			qty = remaining
		}
		pnl := qty * (price - p.Entry) * dir
		fee := qty * price * feeRate
		res.GrossPnL += pnl
		res.Fees += fee
		res.NetPnL += pnl - fee
		res.Closes++
		res.TierFires[tag]++
		res.TierPnL[tag] += pnl - fee
		remaining -= qty
	}

	// --- Pre-slice DD tiers at open (cumulative cap 100%), like the engine. ---
	type tierState struct {
		armATR   float64
		giveback float64
		qty      float64 // fixed allocated slice (absolute qty)
		status   int     // 0 pending, 1 tracking, 2 executed, 3 superseded, 4 be_covered
		peak     float64 // peak favorable fraction-in-dir seen while tracking
	}
	tiers := make([]tierState, 0, len(l.DDTiers))
	allocated := 0.0
	for _, d := range l.DDTiers {
		if d.ArmATR <= 0 || d.Giveback <= 0 || d.SlicePct <= 0 {
			continue
		}
		slice := d.SlicePct
		if allocated+slice > 100 {
			slice = 100 - allocated
		}
		if slice <= 0 {
			continue
		}
		tiers = append(tiers, tierState{
			armATR:   d.ArmATR,
			giveback: d.Giveback,
			qty:      p.Qty * slice / 100.0,
		})
		allocated += slice
	}

	// TP trigger prices.
	tpPrice := make([]float64, len(l.TP))
	tpDone := make([]bool, len(l.TP))
	for i, t := range l.TP {
		tpPrice[i] = p.Entry * (1 + dir*l.effFrac(t.ATR, atrPct))
	}
	// BE floors: arm levels (price) and lock levels (price), armed flags.
	beArmPrice := make([]float64, len(l.BEArm))
	beLockPrice := make([]float64, len(l.BEArm))
	beArmed := make([]bool, len(l.BEArm))
	beDone := make([]bool, len(l.BEArm))
	for i := range l.BEArm {
		beArmPrice[i] = p.Entry * (1 + dir*l.effFrac(l.BEArm[i], atrPct))
		lk := 0.0
		if i < len(l.BELock) {
			lk = l.BELock[i]
		}
		beLockPrice[i] = p.Entry * (1 + dir*l.effFrac(lk, atrPct))
	}
	var slPrice float64
	if l.SLATR > 0 {
		slPrice = p.Entry * (1 - dir*l.effFrac(l.SLATR, atrPct))
	}
	var partialSLPrice float64
	partialSLDone := false
	if l.PartialSL > 0 && l.PartialSLPct > 0 {
		partialSLPrice = p.Entry * (1 - dir*l.effFrac(l.PartialSL, atrPct))
	}

	favFrac := func(price float64) float64 { return (price - p.Entry) * dir / p.Entry } // profit frac

	exitBound := simExitBoundFor(p)
	var lastClose float64
	for _, b := range bars {
		if b.T < p.EntryTime || b.T > exitBound {
			continue
		}
		lastClose = b.Close
		if remaining <= 1e-9 {
			break
		}
		// Pessimistic: adverse extreme before favorable within the bar.
		adverse := b.Low
		favorable := b.High
		if dir < 0 {
			adverse = b.High
			favorable = b.Low
		}
		advFrac := favFrac(adverse)
		favHi := favFrac(favorable)

		// 1) Partial SL, then full SL (loss side, nearest first).
		if partialSLPrice > 0 && !partialSLDone && remaining > 1e-9 {
			if (dir > 0 && adverse <= partialSLPrice) || (dir < 0 && adverse >= partialSLPrice) {
				addClose("PartialSL", p.Qty*l.PartialSLPct/100, partialSLPrice)
				partialSLDone = true
			}
		}
		if slPrice > 0 && remaining > 1e-9 {
			if (dir > 0 && adverse <= slPrice) || (dir < 0 && adverse >= slPrice) {
				addClose("SL", remaining, slPrice)
				break
			}
		}

		// 2) DD giveback check on adverse extreme for ALREADY-tracking tiers
		//    (armed on a PRIOR bar). The active tracking tier fires its slice.
		for i := range tiers {
			t := &tiers[i]
			if t.status != 1 || remaining <= 1e-9 || t.peak <= 0 {
				continue
			}
			ddFromPeak := (t.peak - advFrac) / t.peak // fraction of peak profit given back
			if ddFromPeak >= t.giveback {
				lockFrac := t.peak * (1 - t.giveback)
				price := p.Entry * (1 + dir*lockFrac)
				addClose(fmt.Sprintf("DD%d", i+1), t.qty, price)
				t.status = 2
			}
		}

		// 3) BE stop check on adverse extreme for ALREADY-armed floors (prior bar).
		//    BE protects whatever the DD tiers don't still own (superseded/unreached
		//    slices + any pre-DD remainder).
		for i := range l.BEArm {
			if beDone[i] || !beArmed[i] || remaining <= 1e-9 {
				continue
			}
			if (dir > 0 && adverse <= beLockPrice[i]) || (dir < 0 && adverse >= beLockPrice[i]) {
				activeDDQty := 0.0
				for k := range tiers {
					if tiers[k].status == 1 {
						activeDDQty += tiers[k].qty
					}
				}
				beQty := remaining - activeDDQty
				if beQty > 0 {
					addClose(fmt.Sprintf("BE%d", i+1), beQty, beLockPrice[i])
				}
				beDone[i] = true
			}
		}

		// 4) TP fills on favorable extreme.
		for i := range l.TP {
			if tpDone[i] || remaining <= 1e-9 {
				continue
			}
			if (dir > 0 && favorable >= tpPrice[i]) || (dir < 0 && favorable <= tpPrice[i]) {
				addClose(fmt.Sprintf("TP%d", i+1), p.Qty*l.TP[i].ClosePct/100, tpPrice[i])
				tpDone[i] = true
			}
		}

		// 5) End-of-bar: arm/track DD tiers + supersede, and arm BE floors on the
		//    favorable extreme. Armed THIS bar => can only stop out on a LATER bar
		//    (mirrors floor-replay fairness; avoids same-bar arm+stop).
		for i := range tiers {
			t := &tiers[i]
			if t.status == 2 || t.status == 3 || t.status == 4 {
				continue
			}
			if t.status == 0 && favHi >= l.effFrac(t.armATR, atrPct) {
				t.status = 1
				if favHi > t.peak {
					t.peak = favHi
				}
				for j := 0; j < i; j++ {
					if tiers[j].status == 0 || tiers[j].status == 1 {
						tiers[j].status = 3 // superseded
					}
				}
			}
			if t.status == 1 && favHi > t.peak {
				t.peak = favHi
			}
		}
		for i := range l.BEArm {
			if !beDone[i] && !beArmed[i] && favHi >= l.effFrac(l.BEArm[i], atrPct) {
				beArmed[i] = true
			}
		}
	}

	if remaining > 1e-9 {
		px := p.Exit
		if simExtendMs > 0 && lastClose > 0 {
			px = lastClose
		}
		addClose("residual", remaining, px)
		res.ExitedFlat = false
	} else {
		res.ExitedFlat = true
	}
	return res
}

func ladders() []Ladder {
	return []Ladder{
		// Baseline = current live config (claude strategy).
		{
			Name:      "BASELINE",
			MinEffPct: 0.5,
			TP: []TPTier{
				{ATR: 3.0, ClosePct: 35},
				{ATR: 5.5, ClosePct: 25},
			},
			Floors: []Floor{
				{ArmATR: 1.6, LockATR: 0.3, ClosePct: 50}, // BE1 (offset clamps to 0.5%)
				{ArmATR: 3.0, LockATR: 0.9, ClosePct: 50}, // BE2
				{ArmATR: 4.5, LockATR: 2.0, ClosePct: 50}, // BE3
				{ArmATR: 3.5, Giveback: 0.45, ClosePct: 50}, // DD1
			},
			SLATR: 4.5, SLClosePct: 100,
		},
		// Proposal A = dense TP + rising floor (Part A design).
		{
			Name:      "PROP_A",
			MinEffPct: 0.5,
			TP: []TPTier{
				{ATR: 1.2, ClosePct: 20},
				{ATR: 1.8, ClosePct: 18},
				{ATR: 2.6, ClosePct: 15},
				{ATR: 3.6, ClosePct: 12},
			},
			Floors: []Floor{
				{ArmATR: 1.0, LockATR: 0.1, ClosePct: 100}, // Floor0 breakeven
				{ArmATR: 2.0, LockATR: 1.0, ClosePct: 100}, // Floor1
				{ArmATR: 3.0, Giveback: 0.40, ClosePct: 100}, // Floor2 DD
				{ArmATR: 4.5, Giveback: 0.30, ClosePct: 100}, // Floor3 DD runner
			},
			SLATR: 4.5, SLClosePct: 100,
		},
		// Proposal B = A + extra high DD tiers (user: lock higher profit on big runs).
		{
			Name:      "PROP_B",
			MinEffPct: 0.5,
			TP: []TPTier{
				{ATR: 1.2, ClosePct: 20},
				{ATR: 1.8, ClosePct: 18},
				{ATR: 2.6, ClosePct: 15},
				{ATR: 3.6, ClosePct: 12},
			},
			Floors: []Floor{
				{ArmATR: 1.0, LockATR: 0.1, ClosePct: 100},
				{ArmATR: 2.0, LockATR: 1.0, ClosePct: 100},
				{ArmATR: 3.0, Giveback: 0.40, ClosePct: 100},
				{ArmATR: 4.5, Giveback: 0.30, ClosePct: 100},
				{ArmATR: 6.5, Giveback: 0.22, ClosePct: 100}, // high tier, tighter giveback
				{ArmATR: 9.0, Giveback: 0.15, ClosePct: 100}, // very high, lock most
				// loss-side protection (user: DD1 style to loss side)
				{ArmATR: 2.0, LockATR: 2.8, ClosePct: 30, Loss: true},
			},
			SLATR: 4.5, SLClosePct: 70, // SL keeps last 70% after loss-DD took 30%
		},
		// Proposal C = TP1 pulled to OSC MFE reality (0.8 ATR, reach ~45%).
		// Denser low tiers, keep high DD tiers for runners. No loss-side DD.
		{
			Name:      "PROP_C",
			MinEffPct: 0.5,
			TP: []TPTier{
				{ATR: 0.8, ClosePct: 18},
				{ATR: 1.3, ClosePct: 16},
				{ATR: 1.9, ClosePct: 14},
				{ATR: 2.8, ClosePct: 12},
			},
			Floors: []Floor{
				{ArmATR: 0.8, LockATR: 0.1, ClosePct: 100}, // breakeven floor early
				{ArmATR: 1.8, LockATR: 0.9, ClosePct: 100},
				{ArmATR: 2.8, Giveback: 0.38, ClosePct: 100},
				{ArmATR: 4.5, Giveback: 0.28, ClosePct: 100},
				{ArmATR: 6.5, Giveback: 0.22, ClosePct: 100},
				{ArmATR: 9.0, Giveback: 0.15, ClosePct: 100},
			},
			SLATR: 4.5, SLClosePct: 100,
		},
		// Proposal D = C + tail-only loss-side DD armed just before SL.
		{
			Name:      "PROP_D",
			MinEffPct: 0.5,
			TP: []TPTier{
				{ATR: 0.8, ClosePct: 18},
				{ATR: 1.3, ClosePct: 16},
				{ATR: 1.9, ClosePct: 14},
				{ATR: 2.8, ClosePct: 12},
			},
			Floors: []Floor{
				{ArmATR: 0.8, LockATR: 0.1, ClosePct: 100},
				{ArmATR: 1.8, LockATR: 0.9, ClosePct: 100},
				{ArmATR: 2.8, Giveback: 0.38, ClosePct: 100},
				{ArmATR: 4.5, Giveback: 0.28, ClosePct: 100},
				{ArmATR: 6.5, Giveback: 0.22, ClosePct: 100},
				{ArmATR: 9.0, Giveback: 0.15, ClosePct: 100},
				// tail-only: arm at -3.5 ATR, cut at -4.0 ATR (just inside SL)
				{ArmATR: 3.5, LockATR: 4.0, ClosePct: 40, Loss: true},
			},
			SLATR: 4.5, SLClosePct: 60,
		},
		// Proposal E = best-of: B's TP (1.2 start) + B's high DD tiers + D's
		// tail-only loss-side DD (arm just before SL). Expected OSC ~ -13.
		{
			Name:      "PROP_E",
			MinEffPct: 0.5,
			TP: []TPTier{
				{ATR: 1.1, ClosePct: 20},
				{ATR: 1.7, ClosePct: 18},
				{ATR: 2.5, ClosePct: 15},
				{ATR: 3.6, ClosePct: 12},
			},
			Floors: []Floor{
				{ArmATR: 1.0, LockATR: 0.1, ClosePct: 100}, // breakeven floor
				{ArmATR: 2.0, LockATR: 1.0, ClosePct: 100},
				{ArmATR: 3.0, Giveback: 0.40, ClosePct: 100},
				{ArmATR: 4.5, Giveback: 0.28, ClosePct: 100},
				{ArmATR: 6.5, Giveback: 0.20, ClosePct: 100}, // high: lock 5.2 ATR
				{ArmATR: 9.0, Giveback: 0.13, ClosePct: 100}, // very high: lock 7.8 ATR
			},
			// Loss side via STATIC partial SL (config-expressible, no DD-engine change):
			// partial 40% at 4.0 ATR, then full 60% at 4.5 ATR.
			PartialSL: 4.0, PartialSLPct: 40,
			SLATR: 4.5, SLClosePct: 60,
		},

		// ===== ENGINE-SEMANTICS DD candidates (real trader behavior) =====
		// All share PROP_E's TP ladder + BE floors + loss-side static SL.
		// They differ ONLY in DD slice distribution. The "floor replay" PROP_E above
		// is the IDEALIZED upper bound (each DD floor closes all remaining); these
		// show what the engine ACTUALLY does with pre-sliced tiers + supersede + BE.

		// ENG_BROKEN = exactly what is live now: close=100 on all 4 tiers.
		// Collapses to T1-only (T2..4 allocate 0). Proves the defect.
		engPropE("ENG_BROKEN", []DDTier{
			{ArmATR: 3.0, Giveback: 0.40, SlicePct: 100},
			{ArmATR: 4.5, Giveback: 0.28, SlicePct: 100},
			{ArmATR: 6.5, Giveback: 0.20, SlicePct: 100},
			{ArmATR: 9.0, Giveback: 0.13, SlicePct: 100},
		}),
		// ENG_BALANCED = thick ends (OSC needs T1, big trends need T4), thin middle.
		engPropE("ENG_BAL", []DDTier{
			{ArmATR: 3.0, Giveback: 0.40, SlicePct: 40},
			{ArmATR: 4.5, Giveback: 0.28, SlicePct: 15},
			{ArmATR: 6.5, Giveback: 0.20, SlicePct: 15},
			{ArmATR: 9.0, Giveback: 0.13, SlicePct: 30},
		}),
		// ENG_T1HEAVY = conservative: most weight on the tier that fires in 94% OSC.
		engPropE("ENG_T1H", []DDTier{
			{ArmATR: 3.0, Giveback: 0.40, SlicePct: 55},
			{ArmATR: 4.5, Giveback: 0.28, SlicePct: 15},
			{ArmATR: 6.5, Giveback: 0.20, SlicePct: 10},
			{ArmATR: 9.0, Giveback: 0.13, SlicePct: 20},
		}),
		// ENG_EVEN = flat 25/25/25/25 for reference.
		engPropE("ENG_EVEN", []DDTier{
			{ArmATR: 3.0, Giveback: 0.40, SlicePct: 25},
			{ArmATR: 4.5, Giveback: 0.28, SlicePct: 25},
			{ArmATR: 6.5, Giveback: 0.20, SlicePct: 25},
			{ArmATR: 9.0, Giveback: 0.13, SlicePct: 25},
		}),
		// ENG_3TIER = drop the rarely-used 6.5 tier, widen T1 + keep high lock.
		engPropE("ENG_3T", []DDTier{
			{ArmATR: 3.0, Giveback: 0.40, SlicePct: 50},
			{ArmATR: 4.5, Giveback: 0.28, SlicePct: 20},
			{ArmATR: 9.0, Giveback: 0.13, SlicePct: 30},
		}),

		// ===== TREND-BALANCE candidates (osc microprofit ↔ trend capture) =====
		// Principle: TP1/TP2 (1.1/1.7 ATR) bank the OSC microprofit (41%/24% of OSC
		// reach them); TP3/TP4 mostly just shrink the runner BEFORE a trend develops
		// (only 14%/8% of OSC reach 2.6/3.6). So keep TP1/TP2, drop/shrink TP3/TP4 to
		// leave a bigger RUNNER, and protect that runner with a DD giveback tuned to
		// trend pullback depth (looser giveback = rides trend longer, locks later).
		// DEPLOYED reference (65% out, 35% runner, DD 40% giveback) = ENG_BROKEN above.

		// T_KEEP55: bank 45% on TP1-3, 55% runner @ DD 3.0/45%.
		engTrend("T_KEEP55",
			[]TPTier{{ATR: 1.1, ClosePct: 18}, {ATR: 1.7, ClosePct: 15}, {ATR: 2.5, ClosePct: 12}},
			[]DDTier{{ArmATR: 3.0, Giveback: 0.45, SlicePct: 100}}),
		// T_KEEP67: bank 33% on TP1-2, 67% runner @ DD 3.0/50% (looser → rides further).
		engTrend("T_KEEP67",
			[]TPTier{{ATR: 1.1, ClosePct: 18}, {ATR: 1.7, ClosePct: 15}},
			[]DDTier{{ArmATR: 3.0, Giveback: 0.50, SlicePct: 100}}),
		// T_KEEP67_2T: 33% banked; runner split — lock half loosely early, half hard-high.
		engTrend("T_KEEP67_2T",
			[]TPTier{{ATR: 1.1, ClosePct: 18}, {ATR: 1.7, ClosePct: 15}},
			[]DDTier{{ArmATR: 3.0, Giveback: 0.50, SlicePct: 50}, {ArmATR: 6.0, Giveback: 0.25, SlicePct: 50}}),
		// T_RUN80: minimal early bank (20%), 80% runner @ DD 3.0/50%. Max trend tilt.
		engTrend("T_RUN80",
			[]TPTier{{ATR: 1.1, ClosePct: 20}},
			[]DDTier{{ArmATR: 3.0, Giveback: 0.50, SlicePct: 100}}),
		// T_RUN80_2T: 20% banked; runner split loose-early + hard-high lock.
		engTrend("T_RUN80_2T",
			[]TPTier{{ATR: 1.1, ClosePct: 20}},
			[]DDTier{{ArmATR: 3.0, Giveback: 0.50, SlicePct: 45}, {ArmATR: 6.0, Giveback: 0.25, SlicePct: 55}}),

		// ===== CHOP-PROFIT candidates: pull TP1 to the OSC median MFE (0.75 ATR) =====
		// Goal: bank MORE of the 718 chop positions in profit to beat the ~48 fee wall.
		// Half of chop never reaches 1.1 ATR (median MFE=0.75), so TP1 there exits near
		// breakeven. Earlier TP1 catches those — at the cost of a smaller trend runner.
		// C_E07: TP1 0.7 (median), then 1.2/2.0; 50% banked, 50% runner @ DD 3.0/45%.
		engTrend("C_E07",
			[]TPTier{{ATR: 0.7, ClosePct: 22}, {ATR: 1.2, ClosePct: 16}, {ATR: 2.0, ClosePct: 12}},
			[]DDTier{{ArmATR: 3.0, Giveback: 0.45, SlicePct: 100}}),
		// C_E06: even earlier TP1 0.6, dense low banking 0.6/1.0/1.6; 52% banked.
		engTrend("C_E06",
			[]TPTier{{ATR: 0.6, ClosePct: 24}, {ATR: 1.0, ClosePct: 16}, {ATR: 1.6, ClosePct: 12}},
			[]DDTier{{ArmATR: 3.0, Giveback: 0.45, SlicePct: 100}}),
		// C_E08_run: TP1 0.8 light (chop bank) but keep a big 60% runner for trend.
		engTrend("C_E08_run",
			[]TPTier{{ATR: 0.8, ClosePct: 25}, {ATR: 1.5, ClosePct: 15}},
			[]DDTier{{ArmATR: 3.0, Giveback: 0.50, SlicePct: 100}}),
		// C_DENSE: 4 dense low tiers 0.6/0.9/1.3/1.8 to maximize chop banking.
		engTrend("C_DENSE",
			[]TPTier{{ATR: 0.6, ClosePct: 18}, {ATR: 0.9, ClosePct: 16}, {ATR: 1.3, ClosePct: 14}, {ATR: 1.8, ClosePct: 12}},
			[]DDTier{{ArmATR: 3.0, Giveback: 0.45, SlicePct: 100}}),
	}
}

// engTrend builds an engine-semantics ladder with a PARAMETERIZED TP ladder and DD
// tiers (BE + loss-side SL identical to the deployed stack). Used to sweep the
// osc-microprofit ↔ trend-capture tradeoff: lighter TP => bigger runner => more
// trend captured, at the cost of less banked in chop.
func engTrend(name string, tp []TPTier, dd []DDTier) Ladder {
	return Ladder{
		Name:      name,
		MinEffPct: 0.5,
		EngineDD:  true,
		TP:        tp,
		BEArm:     []float64{1.0, 2.0},
		BELock:    []float64{0.1, 1.0},
		DDTiers:   dd,
		PartialSL: 4.0, PartialSLPct: 40,
		SLATR: 4.5, SLClosePct: 60,
	}
}

// engPropE builds an engine-semantics ladder sharing PROP_E's TP/BE/SL stack,
// parameterized only by its DD slice distribution.
func engPropE(name string, dd []DDTier) Ladder {
	return Ladder{
		Name:      name,
		MinEffPct: 0.5,
		EngineDD:  true,
		TP: []TPTier{
			{ATR: 1.1, ClosePct: 20},
			{ATR: 1.7, ClosePct: 18},
			{ATR: 2.5, ClosePct: 15},
			{ATR: 3.6, ClosePct: 12},
		},
		// BE floors = live break_even_stop (BE1 @1 ATR lock +0.1%, BE2 @2 ATR lock +1%).
		BEArm:   []float64{1.0, 2.0},
		BELock:  []float64{0.1, 1.0},
		DDTiers: dd,
		PartialSL: 4.0, PartialSLPct: 40,
		SLATR: 4.5, SLClosePct: 60,
	}
}

func main() {
	limit := 0
	if len(os.Args) > 1 {
		limit, _ = strconv.Atoi(os.Args[1])
	}
	// SIM_EXTEND_DAYS>0 enables extended replay: the protection ladder runs past the
	// original AI/reversal exit (up to +N days, capped by simMaxHoldMs), settling any
	// open remainder at the last observed close. This exposes reversal/tail losses.
	extendDays := 0.0
	if v := os.Getenv("SIM_EXTEND_DAYS"); v != "" {
		extendDays, _ = strconv.ParseFloat(v, 64)
	}
	simExtendMs = int64(extendDays * 24 * 60 * 60 * 1000)
	modeLabel := "TRUNCATED (stop at AI exit)"
	if simExtendMs > 0 {
		modeLabel = fmt.Sprintf("EXTENDED (+%.1fd, cap %.0fd)", extendDays, float64(simMaxHoldMs)/86400000)
	}
	fmt.Printf("REPLAY MODE: %s\n", modeLabel)
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?mode=ro")
	if err != nil {
		panic(err)
	}
	defer db.Close()
	positions, err := loadPositions(db)
	if err != nil {
		panic(err)
	}
	if limit > 0 && limit < len(positions) {
		positions = positions[len(positions)-limit:]
	}
	fmt.Printf("Loaded %d closed positions\n", len(positions))

	// Prewarm: fetch each symbol once over its full [minEntry, maxExit+extend] range
	// so per-position calls hit cache instead of re-paging widening windows.
	symRange := map[string][2]int64{}
	for _, p := range positions {
		r, ok := symRange[p.Symbol]
		if !ok {
			r = [2]int64{p.EntryTime, p.ExitTime}
		}
		if p.EntryTime < r[0] {
			r[0] = p.EntryTime
		}
		eb := simExitBoundFor(p)
		if eb > r[1] {
			r[1] = eb
		}
		symRange[p.Symbol] = r
	}
	warmed := 0
	for sym, r := range symRange {
		if instID(sym) == "" {
			continue
		}
		_, e1 := fetchOKX(sym, "15m", r[0], r[1])
		_, e2 := fetchOKX(sym, "1H", r[0]-atrPeriod*4*barMs("1H"), r[1])
		warmed++
		if e1 != nil || e2 != nil {
			fmt.Fprintf(os.Stderr, "  warm %s err: %v / %v\n", sym, e1, e2)
		} else {
			fmt.Fprintf(os.Stderr, "  warmed %s (%d/%d)\n", sym, warmed, len(symRange))
		}
	}

	configs := ladders()
	type agg struct {
		net, gross, fees float64
		wins, losses, n  int
		worst            float64
		tierFires        map[string]int
		tierPnL          map[string]float64
		flat             int
		capAtr           float64 // sum of captured ATR per position (NetPnL/(qty*atr))
		availAtr         float64 // sum of available ATR (MFE) — same across configs in a bucket
	}
	newAggs := func() []agg {
		a := make([]agg, len(configs))
		for i := range a {
			a[i].tierFires = map[string]int{}
			a[i].tierPnL = map[string]float64{}
		}
		return a
	}
	// buckets: ALL, OSC (oscillation), TREND, BREAKOUT (strong one-directional run)
	bucket := map[string][]agg{"ALL": newAggs(), "OSC": newAggs(), "TREND": newAggs(), "BREAKOUT": newAggs()}
	var actualNet, actualOsc, actualTrend, actualBreakout float64
	skipped, noATR := 0, 0
	var oscN, trendN, breakoutN int
	// MFE distribution (ATR multiples) over oscillation positions
	mfeOsc := []float64{}
	// MFE distribution over ALL positions — sizes the favorable-trend opportunity.
	mfeAll := []float64{}

	// Validation diagnostic: per-close-reason sim(BASELINE) vs actual realized.
	type diagRow struct {
		n             int
		actual, sim   float64
		simSL, simFlat int
	}
	diag := map[string]diagRow{}

	accumulate := func(b string, ci int, r SimResult, capAtr, availAtr float64) {
		a := &bucket[b][ci]
		a.net += r.NetPnL
		a.gross += r.GrossPnL
		a.fees += r.Fees
		a.n++
		if r.NetPnL > 0 {
			a.wins++
		} else {
			a.losses++
		}
		if r.NetPnL < a.worst {
			a.worst = r.NetPnL
		}
		if r.ExitedFlat {
			a.flat++
		}
		for k, v := range r.TierFires {
			a.tierFires[k] += v
		}
		for k, v := range r.TierPnL {
			a.tierPnL[k] += v
		}
		a.capAtr += capAtr
		a.availAtr += availAtr
	}

	for pi, p := range positions {
		eb := simExitBoundFor(p)
		bars15, err := fetchOKX(p.Symbol, "15m", p.EntryTime, eb)
		if err != nil || len(bars15) == 0 {
			skipped++
			continue
		}
		bars1h, err := fetchOKX(p.Symbol, "1H", p.EntryTime-atrPeriod*4*barMs("1H"), eb)
		if err != nil {
			skipped++
			continue
		}
		atr := atrAtEntry(bars1h, p.EntryTime)
		if atr <= 0 {
			noATR++
			continue
		}
		pm := pathMetrics(p, bars15, atr)
		// Oscillation = price never ran far in one direction AND path inefficient.
		// Objective, path-based: MFE<2 ATR and MAE<2 ATR (stayed in a band), OR
		// efficiency ratio < 0.35 (lots of back-and-forth relative to net move).
		isOsc := (pm.MFEAtr < 2.0 && pm.MAEAtr < 2.0) || pm.EffRatio < 0.35
		// Breakout = a LARGE FAVORABLE move was on the table: MFE >= 5 ATR, regardless
		// of path efficiency. This is the "trend main battlefield in OUR favor" — the
		// set where a bigger runner could have captured more. (We drop the efficiency
		// gate: a trend with a deep mid-pullback still offered the move.)
		isBreakout := pm.MFEAtr >= 5.0
		mfeAll = append(mfeAll, pm.MFEAtr)
		actualNet += p.Realized
		if isOsc {
			oscN++
			actualOsc += p.Realized
			mfeOsc = append(mfeOsc, pm.MFEAtr)
		} else {
			trendN++
			actualTrend += p.Realized
		}
		if isBreakout {
			breakoutN++
			actualBreakout += p.Realized
		}
		// availAtr = the favorable ATR that was on the table (MFE). capAtr per config
		// is computed from its NetPnL below. Capture% = capAtr/availAtr.
		availAtr := pm.MFEAtr
		for ci, l := range configs {
			r := replay(p, bars15, atr, l)
			capAtr := 0.0
			if p.Qty > 0 && atr > 0 {
				capAtr = r.NetPnL / (p.Qty * atr) // realized move in ATR units
			}
			accumulate("ALL", ci, r, capAtr, availAtr)
			if isOsc {
				accumulate("OSC", ci, r, capAtr, availAtr)
			} else {
				accumulate("TREND", ci, r, capAtr, availAtr)
			}
			if isBreakout {
				accumulate("BREAKOUT", ci, r, capAtr, availAtr)
			}
			// Validation diagnostic: compare BASELINE (ci==0) sim vs ACTUAL realized,
			// bucketed by the original close reason. This tests whether the replay
			// faithfully reproduces what the live system actually did. A large gap on
			// reversal/AI-exit reasons means the fixed-window + ladder model is NOT a
			// reliable proxy for those positions (user's concern).
			if ci == 0 {
				reason := p.CloseReason
				if reason == "" {
					reason = "(none)"
				}
				d := diag[reason]
				d.n++
				d.actual += p.Realized
				d.sim += r.NetPnL
				// Did the sim hit its synthetic SL (worse-than-actual tail risk)?
				if r.TierFires["SL"] > 0 || r.TierFires["PartialSL"] > 0 {
					d.simSL++
				}
				if r.ExitedFlat {
					d.simFlat++
				}
				diag[reason] = d
			}
		}
		if (pi+1)%100 == 0 {
			fmt.Fprintf(os.Stderr, "  processed %d/%d\n", pi+1, len(positions))
		}
	}

	fmt.Printf("\nSkipped(no klines)=%d  no-ATR=%d\n", skipped, noATR)
	fmt.Printf("Regime split: OSC=%d  TREND=%d  BREAKOUT=%d (BREAKOUT=MFE>=5ATR favorable, any efficiency)\n", oscN, trendN, breakoutN)
	fmt.Printf("ACTUAL realized net: ALL=%.2f  OSC=%.2f  TREND=%.2f  BREAKOUT=%.2f\n", actualNet, actualOsc, actualTrend, actualBreakout)

	// All-position MFE histogram — sizes how often a big favorable move was on the
	// table at all (the precondition for any trend-capture benefit to exist).
	if len(mfeAll) > 0 {
		reach := func(m float64) int {
			c := 0
			for _, v := range mfeAll {
				if v >= m {
					c++
				}
			}
			return c
		}
		n := len(mfeAll)
		fmt.Printf("ALL-position MFE reach (favorable move available), n=%d:\n", n)
		for _, m := range []float64{2, 3, 4, 5, 7, 10} {
			c := reach(m)
			fmt.Printf("   MFE>=%4.1f ATR : %4d positions (%4.1f%%)\n", m, c, float64(c)/float64(n)*100)
		}
	}

	printBucket := func(name string) {
		aggs := bucket[name]
		fmt.Printf("\n========== %s ==========\n", name)
		fmt.Printf("%-10s %10s %10s %8s %8s %8s %10s %8s %8s\n",
			"CONFIG", "NET", "GROSS", "WIN%", "WINS", "LOSS", "WORST", "FLAT", "CAP%")
		for ci, l := range configs {
			r := aggs[ci]
			winRate := 0.0
			if r.n > 0 {
				winRate = float64(r.wins) / float64(r.n) * 100
			}
			// Capture% = realized ATR / available ATR (MFE). How much of the move
			// on the table this config actually banked. Key trend-capture metric.
			capPct := 0.0
			if r.availAtr > 0 {
				capPct = r.capAtr / r.availAtr * 100
			}
			fmt.Printf("%-10s %10.2f %10.2f %7.1f%% %8d %8d %10.2f %8d %7.1f%%\n",
				l.Name, r.net, r.gross, winRate, r.wins, r.losses, r.worst, r.flat, capPct)
		}
	}
	printBucket("ALL")
	printBucket("OSC")
	printBucket("TREND")
	printBucket("BREAKOUT")

	// MFE distribution over oscillation set — where to place TP tiers.
	if len(mfeOsc) > 0 {
		sort.Float64s(mfeOsc)
		q := func(f float64) float64 {
			idx := int(f * float64(len(mfeOsc)-1))
			return mfeOsc[idx]
		}
		reach := func(atrMult float64) float64 {
			c := 0
			for _, v := range mfeOsc {
				if v >= atrMult {
					c++
				}
			}
			return float64(c) / float64(len(mfeOsc)) * 100
		}
		fmt.Printf("\n=== OSC MFE distribution (ATR multiples), n=%d ===\n", len(mfeOsc))
		fmt.Printf("  p25=%.2f  p50=%.2f  p75=%.2f  p90=%.2f  max=%.2f\n",
			q(0.25), q(0.50), q(0.75), q(0.90), mfeOsc[len(mfeOsc)-1])
		fmt.Printf("  reach-rate (%% of OSC positions whose MFE >= X ATR):\n")
		for _, m := range []float64{1.0, 1.2, 1.5, 1.8, 2.0, 2.6, 3.0, 3.6} {
			fmt.Printf("    %.1f ATR : %5.1f%%\n", m, reach(m))
		}
	}

	fmt.Printf("\n=== Per-tier fire counts & PnL (ALL) ===\n")
	for ci, l := range configs {
		r := bucket["ALL"][ci]
		fmt.Printf("\n[%s] flat-exit=%d/%d\n", l.Name, r.flat, r.n)
		keys := make([]string, 0, len(r.tierFires))
		for k := range r.tierFires {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("   %-10s fires=%4d  pnl=%9.2f\n", k, r.tierFires[k], r.tierPnL[k])
		}
	}

	// === VALIDATION: BASELINE sim vs ACTUAL realized, per close reason ===
	// If the replay were faithful, sim≈actual per bucket. Large gaps reveal where
	// the fixed-window + passive-ladder model fails (esp. reversal/AI exits, where
	// the live system closed at a flip price the replay never sees).
	fmt.Printf("\n=== VALIDATION: BASELINE sim vs ACTUAL realized (per close_reason) ===\n")
	fmt.Printf("%-26s %5s %10s %10s %10s %7s %7s\n",
		"close_reason", "N", "ACTUAL", "SIM", "GAP", "simSL", "simFlat")
	rkeys := make([]string, 0, len(diag))
	for k := range diag {
		rkeys = append(rkeys, k)
	}
	sort.Slice(rkeys, func(i, j int) bool { return diag[rkeys[i]].n > diag[rkeys[j]].n })
	var totA, totS float64
	for _, k := range rkeys {
		d := diag[k]
		totA += d.actual
		totS += d.sim
		fmt.Printf("%-26s %5d %10.2f %10.2f %10.2f %7d %7d\n",
			trunc(k, 26), d.n, d.actual, d.sim, d.sim-d.actual, d.simSL, d.simFlat)
	}
	fmt.Printf("%-26s %5s %10.2f %10.2f %10.2f\n", "TOTAL", "", totA, totS, totS-totA)
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}


