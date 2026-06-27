package backtest

import (
	"database/sql"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"nofx/kernel"
)

// anchorNumRe matches a plausible price token in a free-text structural anchor
// such as "1h支撑0.7833+ATR缓冲" or "4h阻力61.98外侧".
var anchorNumRe = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?`)

// anchorNum extracts the longest decimal/integer token from an anchor string
// (price levels usually carry more digits than timeframe prefixes like "4h").
func anchorNum(s string) (float64, bool) {
	m := anchorNumRe.FindAllString(s, -1)
	if len(m) == 0 {
		return 0, false
	}
	best := ""
	for _, t := range m {
		if len(t) > len(best) {
			best = t
		}
	}
	v, err := strconv.ParseFloat(best, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}
// PLACEHOLDER_EXTRACT

// extractStructuralPlan converts an AI protection_plan into a backtest
// StructuralPlan (absolute SL/TP). Returns nil when no usable structural SL is
// present. action contains "long"/"short"; entryPrice anchors sanity checks.
func extractStructuralPlan(action string, pp *kernel.AIProtectionPlan, entryPrice float64) *StructuralPlan {
	if pp == nil || entryPrice <= 0 {
		return nil
	}
	isLong := strings.Contains(strings.ToLower(action), "long")
	sp := &StructuralPlan{}

	// Stop loss: prefer ladder rule SL (carries structural_anchor); else
	// fall back to plan-level StopLossPrice.
	var slPrice, slAnchor float64
	for i := range pp.LadderRules {
		r := pp.LadderRules[i]
		if r.StopLossPrice > 0 && slPrice == 0 {
			slPrice = r.StopLossPrice
		}
		if slAnchor == 0 {
			if v, ok := anchorNum(r.StructuralAnchor); ok {
				slAnchor = v
			} else if v, ok := anchorNum(r.StopLossAnchor); ok {
				slAnchor = v
			}
		}
	}
	if slPrice == 0 {
		slPrice = pp.StopLossPrice
	}
	if slAnchor == 0 {
		if v, ok := anchorNum(pp.StopLossAnchor); ok {
			slAnchor = v
		}
	}
	// Sanity: SL must be on the adverse side of entry.
	if slPrice > 0 {
		if (isLong && slPrice >= entryPrice) || (!isLong && slPrice <= entryPrice) {
			slPrice = 0
		}
	}
	// Anchor sanity: within ±25% of entry and on the adverse side.
	if slAnchor > 0 {
		dev := (slAnchor - entryPrice) / entryPrice
		if dev < -0.25 || dev > 0.25 {
			slAnchor = 0
		} else if (isLong && slAnchor >= entryPrice) || (!isLong && slAnchor <= entryPrice) {
			slAnchor = 0
		}
	}
	if slPrice <= 0 && slAnchor <= 0 {
		return nil
	}
	sp.SLPrice = slPrice
	sp.SLAnchor = slAnchor

	// Take-profit ladder: collect absolute TP prices on the favorable side.
	for i := range pp.LadderRules {
		r := pp.LadderRules[i]
		if r.TakeProfitPrice <= 0 {
			continue
		}
		tp := r.TakeProfitPrice
		if (isLong && tp <= entryPrice) || (!isLong && tp >= entryPrice) {
			continue
		}
		ratio := r.TakeProfitCloseRatioPct
		if ratio <= 0 {
			ratio = 30
		}
		sp.TPLegs = append(sp.TPLegs, StructuralTPLeg{Price: tp, CloseRatioPct: ratio})
	}
	if len(sp.TPLegs) == 0 && pp.TakeProfitPrice > 0 {
		tp := pp.TakeProfitPrice
		if (isLong && tp > entryPrice) || (!isLong && tp < entryPrice) {
			sp.TPLegs = append(sp.TPLegs, StructuralTPLeg{Price: tp, CloseRatioPct: 60})
		}
	}
	return sp
}
// PLACEHOLDER_LOADER

// LoadStructuralEntries reads CLOSED positions and, for each, finds the nearest
// preceding open decision (same symbol, within matchWindowMs) carrying a
// structural protection_plan, attaching it as Entry.Structural. Entries without
// a usable structural plan keep Structural=nil. Returns (entries, matchedCount).
func LoadStructuralEntries(db *sql.DB, traderIDLike string, matchWindowMs int64) ([]Entry, int, error) {
	entries, err := LoadClaudeEntries(db, traderIDLike)
	if err != nil {
		return nil, 0, err
	}

	rows, err := db.Query(`
		SELECT timestamp, decision_json
		FROM decision_records
		WHERE trader_id LIKE ? AND decision_json LIKE '%protection_plan%'
		ORDER BY timestamp ASC`, traderIDLike)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	type decRow struct {
		tsMs int64
		decs []kernel.Decision
	}
	var decisions []decRow
	for rows.Next() {
		var tsStr, dj string
		if err := rows.Scan(&tsStr, &dj); err != nil {
			return nil, 0, err
		}
		var ds []kernel.Decision
		if err := json.Unmarshal([]byte(dj), &ds); err != nil {
			continue
		}
		decisions = append(decisions, decRow{tsMs: parseTSms(tsStr), decs: ds})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	matched := 0
	for i := range entries {
		e := &entries[i]
		var best *kernel.Decision
		var bestDelta int64 = 1<<62 - 1
		for di := range decisions {
			d := &decisions[di]
			delta := e.EntryTime - d.tsMs
			if delta < 0 || delta > matchWindowMs {
				continue
			}
			for k := range d.decs {
				dec := &d.decs[k]
				if !strings.EqualFold(dec.Symbol, e.Symbol) {
					continue
				}
				if !strings.Contains(strings.ToLower(dec.Action), "open") {
					continue
				}
				if dec.ProtectionPlan == nil {
					continue
				}
				if delta < bestDelta {
					bestDelta = delta
					best = dec
				}
			}
		}
		if best != nil {
			if sp := extractStructuralPlan(best.Action, best.ProtectionPlan, e.EntryPrice); sp != nil {
				e.Structural = sp
				matched++
			}
		}
	}
	return entries, matched, nil
}

// parseTSms parses the decision_records timestamp to epoch ms (0 on failure).
func parseTSms(s string) int64 {
	s = strings.TrimSpace(s)
	layouts := []string{
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}



