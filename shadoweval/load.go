// Package shadoweval is the shared core of the Virtual Trader Bench: it loads
// the executed book (closed positions enriched with realized R and per-rule
// gate verdicts) and simulates one virtual trader per gate policy — each takes
// every real trade EXCEPT the ones its gate blocks, on a shared account, so we
// can compare gate rulebooks as competing traders. Used by both cmd/vtbench
// (CLI) and the API monitor handler so the numbers are identical everywhere.
package shadoweval

import (
	"database/sql"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"
)

// decMatchWindowMs bounds the time-fallback match between a position's entry_time
// and a decision's write time when the (trader,cycle,symbol) key misses (which it
// always does for exchange-synced positions whose entry_decision_cycle=0). Real
// matches land within ~20s; 5min is a safe ceiling that still avoids cross-decision
// bleed since a trader rarely opens the same symbol twice within it.
const decMatchWindowMs = 300000

// Trade is one CLOSED position enriched for evaluation.
type Trade struct {
	Pkey      string
	Trader    string
	Symbol    string
	Side      string
	EntryMs   int64
	PnlUSD    float64
	RiskUSD   float64 // |entry-stop|*qty; 0 if unrecoverable
	RMult     float64 // realized R = PnlUSD/RiskUSD
	Conf      float64
	EstRR     float64
	Forward   bool            // true if a forward-live verdict (out-of-sample)
	BlockedBy map[string]bool // rule -> would_block
}

type dkey struct {
	trader string
	cycle  int64
	symbol string
}

type tkey struct {
	trader string
	symbol string
}

type dinfo struct {
	stop  float64
	conf  float64
	estRR float64
}

type timedInfo struct {
	ms   int64
	info dinfo
}

// decIndex holds decisions indexed two ways: by (trader,cycle,symbol) for the
// exact AI-cycle match (historical/backfill positions) and by (trader,symbol)
// time-sorted list for the fallback used by cycle=0 synced positions.
type decIndex struct {
	byCycle map[dkey]dinfo
	byTime  map[tkey][]timedInfo
}

// lookup resolves a position's decision info: exact cycle match first, then the
// nearest same-symbol open decision within decMatchWindowMs of entryMs.
func (d *decIndex) lookup(trader, symbol string, cycle, entryMs int64) (dinfo, bool) {
	if cycle != 0 {
		if di, ok := d.byCycle[dkey{trader, cycle, symbol}]; ok {
			return di, true
		}
	}
	cands := d.byTime[tkey{trader, symbol}]
	var best dinfo
	var bestDiff int64 = 1 << 62
	found := false
	for _, ti := range cands {
		diff := ti.ms - entryMs
		if diff < 0 {
			diff = -diff
		}
		if diff < bestDiff && diff <= decMatchWindowMs {
			bestDiff, best, found = diff, ti.info, true
		}
	}
	return best, found
}

// parseDecMs parses decision_records.created_at. The value's textual form varies
// by driver: the modernc.org/sqlite driver returns RFC3339Nano
// ("2026-07-10T19:46:27.186480506Z") while the sqlite3 CLI shows a space-separated
// form ("2026-07-10 19:46:27.186480506+00:00"). Try the layouts we've observed.
func parseDecMs(s string) (int64, bool) {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05-07:00",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixMilli(), true
		}
	}
	return 0, false
}

// Load builds the enriched book: closed positions + R (from decision JSON) +
// per-rule block flags (from shadow_gate_verdicts by position_id).
func Load(db *sql.DB) (map[string]*Trade, error) {
	idx, err := loadDecisionR(db)
	if err != nil {
		return nil, err
	}
	trades, err := loadTrades(db, idx)
	if err != nil {
		return nil, err
	}
	if err := loadBlocks(db, trades); err != nil {
		return nil, err
	}
	return trades, nil
}

func loadDecisionR(db *sql.DB) (*decIndex, error) {
	rows, err := db.Query(`SELECT trader_id, cycle_number, created_at, decisions
		FROM decision_records WHERE decisions LIKE '%stop_loss%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	idx := &decIndex{byCycle: map[dkey]dinfo{}, byTime: map[tkey][]timedInfo{}}
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
		var trader, createdAt, decs string
		var cycle int64
		if rows.Scan(&trader, &cycle, &createdAt, &decs) != nil {
			continue
		}
		var arr []rawDec
		if json.Unmarshal([]byte(decs), &arr) != nil {
			continue
		}
		ms, haveMs := parseDecMs(createdAt)
		for _, d := range arr {
			if !strings.Contains(d.Action, "open") || d.StopLoss <= 0 {
				continue
			}
			di := dinfo{d.StopLoss, d.Confidence, d.RiskReward.NetRR}
			idx.byCycle[dkey{trader, cycle, d.Symbol}] = di
			if haveMs {
				k := tkey{trader, d.Symbol}
				idx.byTime[k] = append(idx.byTime[k], timedInfo{ms, di})
			}
		}
	}
	return idx, nil
}

func loadTrades(db *sql.DB, idx *decIndex) (map[string]*Trade, error) {
	rows, err := db.Query(`SELECT id, trader_id, symbol, side, entry_price,
		entry_quantity, realized_pnl, entry_time, COALESCE(entry_decision_cycle,0)
		FROM trader_positions
		WHERE status='CLOSED' AND entry_price>0 AND entry_quantity>0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*Trade{}
	for rows.Next() {
		var id, entryMs, cycle int64
		var trader, symbol, side string
		var entryPx, qty, pnl float64
		if rows.Scan(&id, &trader, &symbol, &side, &entryPx, &qty, &pnl, &entryMs, &cycle) != nil {
			continue
		}
		pkey := itoa(id)
		t := &Trade{Pkey: pkey, Trader: trader, Symbol: symbol, Side: strings.ToUpper(side),
			EntryMs: entryMs, PnlUSD: pnl, BlockedBy: map[string]bool{}}
		if di, ok := idx.lookup(trader, symbol, cycle, entryMs); ok {
			t.RiskUSD = math.Abs(entryPx-di.stop) * qty
			t.Conf, t.EstRR = di.conf, di.estRR
			if t.RiskUSD > 0 {
				t.RMult = pnl / t.RiskUSD
			}
		}
		out[pkey] = t
	}
	return out, nil
}

// loadBlocks attaches per-rule would_block by position_id, and flags forward
// rows (observed_at within 10min of created_at = recorded live, not backfill).
func loadBlocks(db *sql.DB, trades map[string]*Trade) error {
	rows, err := db.Query(`SELECT position_id, rule_name, would_block,
		CASE WHEN (created_at-observed_at)<600000 THEN 1 ELSE 0 END AS fwd
		FROM shadow_gate_verdicts WHERE position_id>0`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var pid int64
		var rule string
		var blk, fwd int
		if rows.Scan(&pid, &rule, &blk, &fwd) != nil {
			continue
		}
		if t, ok := trades[itoa(pid)]; ok {
			t.BlockedBy[rule] = blk != 0
			if fwd == 1 {
				t.Forward = true
			}
		}
	}
	return nil
}

// RuleNames returns the sorted set of all rules seen across trades.
func RuleNames(trades map[string]*Trade) []string {
	set := map[string]bool{}
	for _, t := range trades {
		for r := range t.BlockedBy {
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

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
