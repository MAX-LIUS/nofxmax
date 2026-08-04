package backtest

import (
	"database/sql"
	"encoding/json"
	"strings"
)

// AttachLiveATR pins each entry's ATR to the value the LIVE system actually used
// at decision time, read from decision_records' review_context.control.regime_atr14_pct.
//
// Why this is required for any ATR-unit parameter sweep: ReplayEntry derives ATR
// from the replay bars unless Entry.ATROverride is set, so a "0.3 ATR" threshold in
// the replay is a DIFFERENT price distance than the "0.3 ATR" the live engine
// applied. That mismatch makes the replay arm break-even on trades whose live peak
// never reached the threshold (and miss ones that did), which shows up as a phantom
// winner-clipping band. Recovering regime_atr14_pct and converting it to price
// units (pct/100 * entryPrice) puts replay and live on the same ruler, so the
// measured effect is attributable to the parameter rather than to the unit.
//
// Verified against live telemetry: peak_pct / peak_atr_mult reproduces
// regime_atr14_pct with median ratio 1.000 (p10 0.997 / p90 1.006) over the 194
// July positions that carry both fields, confirming regime_atr14_pct is the same
// ATR the live engine used to compute peak_atr_mult.
//
// Returns the number of entries pinned. Entries without a usable decision keep
// their replay-derived ATR (ATROverride stays 0).
func AttachLiveATR(db *sql.DB, traderIDLike string, matchWindowMs int64, entries []Entry) (int, error) {
	rows, err := db.Query(`
		-- regime_atr14_pct lives in the "decisions" column (the executed decision
		-- list), NOT in decision_json or review_context: for GPT 3491/11498 rows
		-- carry it in decisions and 0 rows carry it in either of the other two.
		SELECT timestamp, decisions
		FROM decision_records
		WHERE trader_id LIKE ? AND decisions LIKE '%regime_atr14_pct%'
		ORDER BY timestamp ASC`, traderIDLike)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	// atrRec is one (ts, symbol, action) → live ATR% observation.
	type atrRec struct {
		tsMs   int64
		symbol string
		action string
		atrPct float64
	}
	var recs []atrRec
	for rows.Next() {
		var tsStr, dj string
		if err := rows.Scan(&tsStr, &dj); err != nil {
			return 0, err
		}
		// decision_json is a LIST and 469/2523 rows are JSON null, so decode into a
		// slice of loosely-typed shapes and skip anything that isn't an object list.
		var raw []struct {
			Symbol        string `json:"symbol"`
			Action        string `json:"action"`
			ReviewContext *struct {
				Control *struct {
					RegimeATR14Pct float64 `json:"regime_atr14_pct"`
				} `json:"control"`
			} `json:"review_context"`
		}
		if err := json.Unmarshal([]byte(dj), &raw); err != nil {
			continue
		}
		ts := parseTSms(tsStr)
		for _, d := range raw {
			if d.ReviewContext == nil || d.ReviewContext.Control == nil {
				continue
			}
			if d.ReviewContext.Control.RegimeATR14Pct <= 0 {
				continue
			}
			recs = append(recs, atrRec{tsMs: ts, symbol: d.Symbol, action: d.Action,
				atrPct: d.ReviewContext.Control.RegimeATR14Pct})
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	pinned := 0
	for i := range entries {
		e := &entries[i]
		want := "open_long"
		if strings.EqualFold(e.Side, "SHORT") {
			want = "open_short"
		}
		// Nearest preceding open decision for the same symbol+side. Decisions land
		// 10-12s AFTER the position row's entry_time in some rows, so allow a small
		// negative slack the same way the structural matcher's window does.
		var best float64
		var bestDelta int64 = 1<<62 - 1
		for _, r := range recs {
			if !strings.EqualFold(r.symbol, e.Symbol) || !strings.EqualFold(r.action, want) {
				continue
			}
			delta := e.EntryTime - r.tsMs
			if delta < 0 {
				delta = -delta
			}
			if delta > matchWindowMs {
				continue
			}
			if delta < bestDelta {
				bestDelta = delta
				best = r.atrPct
			}
		}
		if best > 0 && e.EntryPrice > 0 {
			e.ATROverride = best / 100 * e.EntryPrice
			pinned++
		}
	}
	return pinned, nil
}
