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
)

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

type dinfo struct {
	stop  float64
	conf  float64
	estRR float64
}

// Load builds the enriched book: closed positions + R (from decision JSON) +
// per-rule block flags (from shadow_gate_verdicts by position_id).
func Load(db *sql.DB) (map[string]*Trade, error) {
	dmap, err := loadDecisionR(db)
	if err != nil {
		return nil, err
	}
	trades, err := loadTrades(db, dmap)
	if err != nil {
		return nil, err
	}
	if err := loadBlocks(db, trades); err != nil {
		return nil, err
	}
	return trades, nil
}

func loadDecisionR(db *sql.DB) (map[dkey]dinfo, error) {
	rows, err := db.Query(`SELECT trader_id, cycle_number, decisions
		FROM decision_records WHERE decisions LIKE '%stop_loss%'`)
	if err != nil {
		return nil, err
	}
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
		if rows.Scan(&trader, &cycle, &decs) != nil {
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
	return out, nil
}

func loadTrades(db *sql.DB, dmap map[dkey]dinfo) (map[string]*Trade, error) {
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
		if di, ok := dmap[dkey{trader, cycle, symbol}]; ok {
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
