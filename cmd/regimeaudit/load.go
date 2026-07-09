package main

import (
	"database/sql"
	"encoding/json"
	"strings"
)

type posrow struct {
	symbol     string
	side       string
	entryPrice float64
	entryTime  int64
	pnl        float64
	confidence float64 // -1 = unknown
}

// loadPositionsWithConfidence loads CLOSED positions and joins each to its entry
// decision record to recover the AI confidence for that symbol's open action.
func loadPositionsWithConfidence(db *sql.DB, traderLike string) ([]posrow, error) {
	rows, err := db.Query(`
		SELECT symbol, side, entry_price, entry_time, realized_pnl,
		       COALESCE(trader_id,''), COALESCE(entry_decision_cycle,0)
		FROM trader_positions
		WHERE trader_id LIKE ? AND status='CLOSED' AND entry_price>0 AND quantity>0
		ORDER BY entry_time ASC`, traderLike)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type pk struct {
		trader string
		cycle  int64
	}
	var out []posrow
	need := map[pk][]int{} // decision key -> indices needing confidence
	var traders []string
	var cycles []int64
	for rows.Next() {
		var p posrow
		var trader string
		var cycle int64
		if err := rows.Scan(&p.symbol, &p.side, &p.entryPrice, &p.entryTime, &p.pnl, &trader, &cycle); err != nil {
			return nil, err
		}
		p.confidence = -1
		out = append(out, p)
		if cycle > 0 && trader != "" {
			k := pk{trader, cycle}
			need[k] = append(need[k], len(out)-1)
		}
		traders = append(traders, trader)
		cycles = append(cycles, cycle)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// For each needed (trader,cycle), fetch decisions JSON and fill confidence by symbol.
	stmt, err := db.Prepare(`SELECT decisions FROM decision_records WHERE trader_id=? AND cycle_number=? LIMIT 1`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	for k, idxs := range need {
		var js sql.NullString
		if err := stmt.QueryRow(k.trader, k.cycle).Scan(&js); err != nil || !js.Valid {
			continue
		}
		var arr []map[string]interface{}
		if json.Unmarshal([]byte(js.String), &arr) != nil {
			continue
		}
		// symbol -> confidence for open actions
		conf := map[string]float64{}
		for _, d := range arr {
			act, _ := d["action"].(string)
			if !strings.HasPrefix(act, "open_") {
				continue
			}
			sym, _ := d["symbol"].(string)
			if cv, ok := d["confidence"].(float64); ok {
				conf[sym] = cv
			}
		}
		for _, ix := range idxs {
			if cv, ok := conf[out[ix].symbol]; ok {
				out[ix].confidence = cv
			}
		}
	}
	return out, nil
}
