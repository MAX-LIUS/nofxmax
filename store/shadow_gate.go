package store

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// ShadowGateVerdict records, for a single AI open decision, what ONE candidate
// entry-gate rule WOULD have decided — without affecting live execution. Every
// candidate rule writes one row per open decision it evaluates. Later these are
// joined to trader_positions (by trader_id + symbol + entry_decision_cycle) to
// measure each rule's realized-PnL edge on FORWARD live data (true out-of-sample,
// not a backtest counterfactual). This is the dry-run / shadow-mode substrate for
// evaluating ALL regime/trend gate methods in parallel before any of them is
// allowed to enforce.
type ShadowGateVerdict struct {
	ID       int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	TraderID string `gorm:"column:trader_id;not null;index:idx_shadow_trader_cycle" json:"trader_id"`
	Cycle    int64  `gorm:"column:cycle;default:0;index:idx_shadow_trader_cycle" json:"cycle"`
	Symbol   string `gorm:"column:symbol;not null" json:"symbol"`
	Action   string `gorm:"column:action;not null" json:"action"` // open_long / open_short
	Side     string `gorm:"column:side;default:''" json:"side"`   // LONG / SHORT

	// PositionID is the exact position this verdict maps to, when known. Backfill
	// fills it (it iterates real positions) so scoring joins 1:1 even when
	// (trader,cycle,symbol) is not unique. Forward-live rows leave it 0 (the
	// position is not open yet at decision time) and rely on the unique per-cycle
	// (trader,cycle,symbol) join instead.
	PositionID int64   `gorm:"column:position_id;default:0;index:idx_shadow_posid" json:"position_id"`
	RuleName   string  `gorm:"column:rule_name;not null;index:idx_shadow_rule" json:"rule_name"`
	WouldBlock bool    `gorm:"column:would_block;default:false" json:"would_block"`
	Regime     string  `gorm:"column:regime;default:''" json:"regime"`
	Confidence float64 `gorm:"column:confidence;default:0" json:"confidence"`
	Detail     string  `gorm:"column:detail;type:text;default:''" json:"detail"`

	// LiveAllowed mirrors whether the live gate allowed this entry, so shadow
	// rules can be compared against what the production gate actually did.
	// default:false (NOT true): with default:true, GORM omits the Go zero value
	// (false) from INSERT and the column default (true) overwrote every live
	// block, making it impossible to persist live_allowed=false. With default
	// false, a false value falls through to the same false default while true is
	// written explicitly.
	LiveAllowed bool  `gorm:"column:live_allowed;default:false" json:"live_allowed"`
	ObservedAt  int64 `gorm:"column:observed_at;not null;index:idx_shadow_trader_cycle" json:"observed_at"`
	CreatedAt   int64 `gorm:"column:created_at" json:"created_at"`
}

func (ShadowGateVerdict) TableName() string { return "shadow_gate_verdicts" }

// CanonicalShadowRuleOrder is the FIXED display order for the nine+ shadow gates.
// The UI must render gates in this order (never re-sort on click). It mirrors the
// trader package's shadowRules registry; trader/shadow_gate_test.go asserts the two
// stay in sync so a new rule can't silently drift the order.
var CanonicalShadowRuleOrder = []string{
	"countertrend_slope30",
	"countertrend_slope50",
	"countertrend_slope72",
	"downtrend_long_only_s50",
	"uptrend_short_only_s50",
	"chop_reject_all",
	"chop_lowconf_lt70",
	"chop_lowconf_lt80",
	"adx_weak_lt20",
	"donchian48_counter",
	"builtin_regime_filter",
}

type ShadowGateStore struct {
	db *gorm.DB
}

func NewShadowGateStore(db *gorm.DB) *ShadowGateStore {
	return &ShadowGateStore{db: db}
}

func (s *ShadowGateStore) InitTables() error {
	if s.db.Dialector.Name() == "postgres" {
		var exists int64
		s.db.Raw(`SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'shadow_gate_verdicts'`).Scan(&exists)
		if exists > 0 {
			return nil
		}
	}
	if err := s.db.AutoMigrate(&ShadowGateVerdict{}); err != nil {
		return fmt.Errorf("failed to migrate shadow_gate_verdicts table: %w", err)
	}
	return nil
}

// RecordBatch persists a batch of verdicts (one decision, many rules). Best-effort:
// the live decision path must never be blocked by a shadow-logging failure.
func (s *ShadowGateStore) RecordBatch(verdicts []*ShadowGateVerdict) error {
	if len(verdicts) == 0 {
		return nil
	}
	// Explicitly Select the boolean columns so GORM writes their real value even
	// when it is the Go zero value (false). Without this, a false WouldBlock or
	// LiveAllowed is omitted from the INSERT and the column DEFAULT is applied —
	// for LiveAllowed (default:true) that silently turned every blocked entry into
	// live_allowed=true, making it physically impossible to record a live block.
	return s.db.Select(
		"trader_id", "cycle", "symbol", "action", "side", "position_id",
		"rule_name", "would_block", "regime", "confidence", "detail",
		"live_allowed", "observed_at", "created_at",
	).Create(verdicts).Error
}

// LinkForwardVerdicts assigns position_id to still-unlinked verdicts
// (position_id=0) by matching each open decision to the position whose
// entry_time is nearest its observed_at, within windowMs, per
// (trader_id,symbol,side). Ambiguous windows (>1 candidate) are skipped so a
// verdict is never mis-attributed. Idempotent and observation-only: it only
// fills position_id on shadow rows and never touches positions or trading.
// Returns the number of verdict rows updated. Safe to call every cycle.
func (s *ShadowGateStore) LinkForwardVerdicts(windowMs int64) (int, error) {
	type vrow struct {
		ID         int64
		TraderID   string
		Symbol     string
		Side       string
		ObservedAt int64
	}
	var vs []vrow
	if err := s.db.Raw(`SELECT id, trader_id, symbol, side, observed_at
		FROM shadow_gate_verdicts WHERE position_id = 0`).Scan(&vs).Error; err != nil {
		return 0, err
	}
	if len(vs) == 0 {
		return 0, nil
	}

	// group verdict rows into decisions (same trader/symbol/side/observed_at)
	type decKey struct {
		trader, symbol, side string
		obs                  int64
	}
	decs := map[decKey][]int64{}
	for _, v := range vs {
		k := decKey{v.TraderID, v.Symbol, strings.ToUpper(v.Side), v.ObservedAt}
		decs[k] = append(decs[k], v.ID)
	}

	type prow struct {
		ID       int64
		TraderID string
		Symbol   string
		Side     string
		EntryTime int64
	}
	var ps []prow
	if err := s.db.Raw(`SELECT id, trader_id, symbol, side, entry_time
		FROM trader_positions WHERE entry_price > 0 AND entry_quantity > 0`).Scan(&ps).Error; err != nil {
		return 0, err
	}
	posByKey := map[string][]prow{}
	for _, p := range ps {
		k := p.TraderID + "|" + p.Symbol + "|" + strings.ToUpper(p.Side)
		posByKey[k] = append(posByKey[k], p)
	}

	used := map[int64]bool{}
	updated := 0
	for k, vids := range decs {
		cands := posByKey[k.trader+"|"+k.symbol+"|"+k.side]
		var bestID int64
		var bestDiff int64 = 1 << 62
		within := 0
		for _, c := range cands {
			if used[c.ID] {
				continue
			}
			diff := c.EntryTime - k.obs
			if diff < 0 {
				diff = -diff
			}
			if diff <= windowMs {
				within++
				if diff < bestDiff {
					bestDiff, bestID = diff, c.ID
				}
			}
		}
		if within != 1 {
			continue // no match, or ambiguous -> leave unlinked
		}
		used[bestID] = true
		if err := s.db.Exec(`UPDATE shadow_gate_verdicts SET position_id = ? WHERE id IN ?`,
			bestID, vids).Error; err != nil {
			return updated, err
		}
		updated += len(vids)
	}
	return updated, nil
}

// ShadowRuleStat is the forward-data scorecard for one candidate rule: for the
// entries it WOULD block vs those it would keep, the realized-PnL profile of the
// positions that actually opened. Joined by (trader_id, cycle, symbol).
type ShadowRuleStat struct {
	RuleName  string  `json:"rule_name"`
	BlockN    int     `json:"block_n"`
	BlockPnL  float64 `json:"block_pnl"`
	BlockWin  int     `json:"block_win"`
	KeepN     int     `json:"keep_n"`
	KeepPnL   float64 `json:"keep_pnl"`
	KeepWin   int     `json:"keep_win"`
	PendingN  int     `json:"pending_n"` // verdicts with no matched closed position yet
}

// RuleStats computes per-rule forward scorecards. If traderID is empty, all
// traders are aggregated. Only CLOSED positions contribute to PnL buckets;
// verdicts without a matched closed position count toward PendingN.
func (s *ShadowGateStore) RuleStats(traderID string) ([]ShadowRuleStat, error) {
	where := "1=1"
	args := []interface{}{}
	if traderID != "" {
		where = "v.trader_id = ?"
		args = append(args, traderID)
	}
	// Join each verdict to EXACTLY ONE closed position by position_id — one
	// uniform, 1:1 path for both backfill and forward rows. Backfill sets
	// position_id at replay time; forward rows are recorded before the position
	// exists (position_id=0) and get it filled in afterward by the shadow-link
	// reconciler (time-nearest match on trader/symbol/side). The old
	// (trader,cycle,symbol) fallback was unreliable forward: live positions
	// arrive via exchange sync with entry_decision_cycle=0, which never matched
	// the AI cycleNumber the verdict stored. LEFT JOIN so still-unlinked verdicts
	// surface as pending rather than vanishing.
	q := `
		SELECT v.rule_name, v.would_block,
		       p.realized_pnl,
		       CASE WHEN p.id IS NULL THEN 0 ELSE 1 END AS matched
		FROM shadow_gate_verdicts v
		LEFT JOIN trader_positions p
		  ON p.status = 'CLOSED' AND v.position_id > 0 AND p.id = v.position_id
		WHERE v.position_id > 0 AND ` + where
	rows, err := s.db.Raw(q, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	acc := map[string]*ShadowRuleStat{}
	for rows.Next() {
		var name string
		var block bool
		var pnl *float64
		var matched int
		if err := rows.Scan(&name, &block, &pnl, &matched); err != nil {
			return nil, err
		}
		st := acc[name]
		if st == nil {
			st = &ShadowRuleStat{RuleName: name}
			acc[name] = st
		}
		if matched == 0 || pnl == nil {
			st.PendingN++
			continue
		}
		if block {
			st.BlockN++
			st.BlockPnL += *pnl
			if *pnl > 0 {
				st.BlockWin++
			}
		} else {
			st.KeepN++
			st.KeepPnL += *pnl
			if *pnl > 0 {
				st.KeepWin++
			}
		}
	}
	out := make([]ShadowRuleStat, 0, len(acc))
	for _, st := range acc {
		out = append(out, *st)
	}
	return out, rows.Err()
}

// TimeSeriesPoint is one time-bucket for one rule's four-category PnL breakdown.
// The four categories partition every matched verdict into block/keep × win/loss,
// each with count, total value, and average. This drives the monitor page's
// four-curve historical chart.
type TimeSeriesPoint struct {
	BucketStart int64 `json:"bucket_start"` // unix ms, aligned to the bucket size

	// block×loss: the gate WOULD block AND the trade lost — a correct block (saved money)
	BlockLossN   int     `json:"block_loss_n"`
	BlockLossPnL float64 `json:"block_loss_pnl"`
	// block×win: the gate WOULD block AND the trade won — a false block (missed profit)
	BlockWinN   int     `json:"block_win_n"`
	BlockWinPnL float64 `json:"block_win_pnl"`
	// keep×win: the gate would keep AND the trade won — a correct keep
	KeepWinN   int     `json:"keep_win_n"`
	KeepWinPnL float64 `json:"keep_win_pnl"`
	// keep×loss: the gate would keep AND the trade lost — a bad keep
	KeepLossN   int     `json:"keep_loss_n"`
	KeepLossPnL float64 `json:"keep_loss_pnl"`
}

// bucketSizeMs maps a period label to its millisecond width.
func bucketSizeMs(period string) int64 {
	const day = 24 * 60 * 60 * 1000
	switch period {
	case "10m":
		return 10 * 60 * 1000
	case "1h":
		return 60 * 60 * 1000
	case "12h":
		return 12 * 60 * 60 * 1000
	case "1d":
		return day
	case "1w", "1week":
		return 7 * day
	case "1mo", "1month":
		return 30 * day
	case "1y", "1year":
		return 365 * day
	default:
		return 60 * 60 * 1000 // default 1h
	}
}

// RuleTimeSeries returns the four-category PnL breakdown for ONE rule, bucketed by
// the given period (10m/1h/12h/1d/1w/1mo/1y), ordered oldest→newest. source selects
// the opening data set (backfill|forward|""=all). PnL for a matched
// verdict is the real closed-position realized_pnl; for verdicts that were
// actually live-blocked (no position exists), it falls back to the blocksim
// counterfactual sim_pnl so blocked entries still contribute a fully-simulated
// (SL/TP/DD/BE) outcome. Bucketing is by the position's exit time when known, else
// the verdict observation time.
func (s *ShadowGateStore) RuleTimeSeries(traderID, ruleName, period, source string) ([]TimeSeriesPoint, error) {
	size := bucketSizeMs(period)
	where := "v.rule_name = ?"
	args := []interface{}{ruleName}
	if traderID != "" {
		where += " AND v.trader_id = ?"
		args = append(args, traderID)
	}
	if sf := sourceFilterSQL(source); sf != "" {
		where += " AND " + sf
	}
	// COALESCE real closed-position pnl with the blocksim counterfactual; bucket by
	// the resolution time (position exit_time, else blocksim sim_at, else observed_at).
	q := `
		SELECT
			CAST(COALESCE(p.exit_time, b.sim_at, v.observed_at) / ? AS INTEGER) * ? AS bucket,
			v.would_block,
			COALESCE(p.realized_pnl, b.sim_pnl) AS pnl,
			CASE WHEN p.id IS NOT NULL OR (b.id IS NOT NULL AND b.sim_status = 'done') THEN 1 ELSE 0 END AS matched
		FROM shadow_gate_verdicts v
		LEFT JOIN trader_positions p
		  ON p.status = 'CLOSED' AND v.position_id > 0 AND p.id = v.position_id
		LEFT JOIN blocked_sim_outcomes b
		  ON b.trader_id = v.trader_id AND b.symbol = v.symbol
		     AND b.side = v.side AND b.cycle = v.cycle AND b.sim_status = 'done'
		WHERE ` + where
	rows, err := s.db.Raw(q, append([]interface{}{size, size}, args...)...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	acc := map[int64]*TimeSeriesPoint{}
	for rows.Next() {
		var bucket int64
		var block bool
		var pnl *float64
		var matched int
		if err := rows.Scan(&bucket, &block, &pnl, &matched); err != nil {
			return nil, err
		}
		if matched == 0 || pnl == nil {
			continue // unresolved verdict: no PnL to attribute yet
		}
		pt := acc[bucket]
		if pt == nil {
			pt = &TimeSeriesPoint{BucketStart: bucket}
			acc[bucket] = pt
		}
		win := *pnl > 0
		switch {
		case block && !win:
			pt.BlockLossN++
			pt.BlockLossPnL += *pnl
		case block && win:
			pt.BlockWinN++
			pt.BlockWinPnL += *pnl
		case !block && win:
			pt.KeepWinN++
			pt.KeepWinPnL += *pnl
		default: // !block && !win
			pt.KeepLossN++
			pt.KeepLossPnL += *pnl
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Sort buckets ascending.
	buckets := make([]int64, 0, len(acc))
	for b := range acc {
		buckets = append(buckets, b)
	}
	sortInt64Asc(buckets)
	out := make([]TimeSeriesPoint, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, *acc[b])
	}
	return out, nil
}

func sortInt64Asc(a []int64) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}

// sourceFilterSQL returns a WHERE fragment (on alias v) selecting the data source:
//   - "backfill": historical replay rows. The backfill tool sets observed_at to the
//     position's real entry_time but created_at to the replay moment, so the two
//     differ by far more than a decision cycle (>1h). These are local historical
//     opening records.
//   - "forward": live production rows. Recorded at decision time, so observed_at and
//     created_at are written together (≈equal). These are real live-trading records.
//   - "" or "all": no filter (mixed — avoid for clean per-source comparison).
func sourceFilterSQL(source string) string {
	switch source {
	case "backfill":
		return "v.created_at - v.observed_at > 3600000"
	case "forward":
		return "v.created_at - v.observed_at <= 3600000"
	default:
		return ""
	}
}

// FootprintTrade is one resolved open decision as a single scatter point: the gate's
// verdict (block/keep) on a trade whose real (or fully-simulated) PnL is fixed. The
// scatter of (Time, PnL) is IDENTICAL across all gates for the same source — a gate
// only recolors which points are "blocked", so gates are directly comparable on one
// canvas.
type FootprintTrade struct {
	Time    int64   `json:"time"`     // resolution time (exit_time, else sim_at, else observed_at)
	Symbol  string  `json:"symbol"`
	Side    string  `json:"side"`
	PnL     float64 `json:"pnl"`      // net realized PnL (real close) or simulated PnL (blocksim)
	Block   bool    `json:"block"`    // would this gate have blocked the open?
	Sim     bool    `json:"sim"`      // true = PnL is a blocksim counterfactual, false = real close
	Quadrant string `json:"quadrant"` // block_loss|block_win|keep_win|keep_loss
}

// RuleFootprint returns every resolved trade for one gate as a raw per-trade point
// (no bucketing) so the UI can plot the actual historical footprint and let the user
// zoom/pan tick-by-tick. Ordered by resolution time ascending.
func (s *ShadowGateStore) RuleFootprint(traderID, ruleName, source string) ([]FootprintTrade, error) {
	where := "v.rule_name = ?"
	args := []interface{}{ruleName}
	if traderID != "" {
		where += " AND v.trader_id = ?"
		args = append(args, traderID)
	}
	if sf := sourceFilterSQL(source); sf != "" {
		where += " AND " + sf
	}
	q := `
		SELECT
			COALESCE(p.exit_time, b.sim_at, v.observed_at) AS t,
			v.symbol, v.side, v.would_block,
			COALESCE(p.realized_pnl, b.sim_pnl) AS pnl,
			CASE WHEN p.id IS NOT NULL THEN 0 ELSE 1 END AS is_sim,
			CASE WHEN p.id IS NOT NULL OR (b.id IS NOT NULL AND b.sim_status = 'done') THEN 1 ELSE 0 END AS matched
		FROM shadow_gate_verdicts v
		LEFT JOIN trader_positions p
		  ON p.status = 'CLOSED' AND v.position_id > 0 AND p.id = v.position_id
		LEFT JOIN blocked_sim_outcomes b
		  ON b.trader_id = v.trader_id AND b.symbol = v.symbol
		     AND b.side = v.side AND b.cycle = v.cycle AND b.sim_status = 'done'
		WHERE ` + where + `
		ORDER BY t ASC`
	rows, err := s.db.Raw(q, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]FootprintTrade, 0, 256)
	for rows.Next() {
		var t int64
		var symbol, side string
		var block bool
		var pnl *float64
		var isSim, matched int
		if err := rows.Scan(&t, &symbol, &side, &block, &pnl, &isSim, &matched); err != nil {
			return nil, err
		}
		if matched == 0 || pnl == nil {
			continue
		}
		win := *pnl > 0
		ft := FootprintTrade{Time: t, Symbol: symbol, Side: side, PnL: *pnl, Block: block, Sim: isSim == 1}
		switch {
		case block && !win:
			ft.Quadrant = "block_loss"
		case block && win:
			ft.Quadrant = "block_win"
		case !block && win:
			ft.Quadrant = "keep_win"
		default:
			ft.Quadrant = "keep_loss"
		}
		out = append(out, ft)
	}
	return out, rows.Err()
}

// GateMatrixRow is one gate's four-quadrant confusion tally over the SAME set of
// resolved trades: how many losers it correctly blocked, winners it wrongly blocked,
// winners it correctly kept, losers it wrongly kept — plus the PnL of each quadrant.
type GateMatrixRow struct {
	RuleName string `json:"rule_name"`
	// block×loss = blocked a losing trade = CORRECT BLOCK (avoided a loss)
	BlockLossN   int     `json:"block_loss_n"`
	BlockLossPnL float64 `json:"block_loss_pnl"`
	// block×win = blocked a winning trade = WRONG BLOCK (missed a profit)
	BlockWinN   int     `json:"block_win_n"`
	BlockWinPnL float64 `json:"block_win_pnl"`
	// keep×win = kept a winning trade = CORRECT KEEP
	KeepWinN   int     `json:"keep_win_n"`
	KeepWinPnL float64 `json:"keep_win_pnl"`
	// keep×loss = kept a losing trade = WRONG KEEP
	KeepLossN   int     `json:"keep_loss_n"`
	KeepLossPnL float64 `json:"keep_loss_pnl"`
}

// GateMatrix computes the correct/wrong block/keep confusion for EVERY gate over the
// exact same set of resolved trades from one data source, so the nine gates are
// compared on identical opening data (never a mixed/uneven sample).
func (s *ShadowGateStore) GateMatrix(traderID, source string) ([]GateMatrixRow, error) {
	where := "1=1"
	args := []interface{}{}
	if traderID != "" {
		where += " AND v.trader_id = ?"
		args = append(args, traderID)
	}
	if sf := sourceFilterSQL(source); sf != "" {
		where += " AND " + sf
	}
	q := `
		SELECT v.rule_name, v.would_block,
			COALESCE(p.realized_pnl, b.sim_pnl) AS pnl,
			CASE WHEN p.id IS NOT NULL OR (b.id IS NOT NULL AND b.sim_status = 'done') THEN 1 ELSE 0 END AS matched
		FROM shadow_gate_verdicts v
		LEFT JOIN trader_positions p
		  ON p.status = 'CLOSED' AND v.position_id > 0 AND p.id = v.position_id
		LEFT JOIN blocked_sim_outcomes b
		  ON b.trader_id = v.trader_id AND b.symbol = v.symbol
		     AND b.side = v.side AND b.cycle = v.cycle AND b.sim_status = 'done'
		WHERE ` + where
	rows, err := s.db.Raw(q, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	acc := map[string]*GateMatrixRow{}
	for rows.Next() {
		var name string
		var block bool
		var pnl *float64
		var matched int
		if err := rows.Scan(&name, &block, &pnl, &matched); err != nil {
			return nil, err
		}
		if matched == 0 || pnl == nil {
			continue
		}
		row := acc[name]
		if row == nil {
			row = &GateMatrixRow{RuleName: name}
			acc[name] = row
		}
		win := *pnl > 0
		switch {
		case block && !win:
			row.BlockLossN++
			row.BlockLossPnL += *pnl
		case block && win:
			row.BlockWinN++
			row.BlockWinPnL += *pnl
		case !block && win:
			row.KeepWinN++
			row.KeepWinPnL += *pnl
		default:
			row.KeepLossN++
			row.KeepLossPnL += *pnl
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Emit in the fixed canonical rule order (req: stable ordering, no re-sort).
	out := make([]GateMatrixRow, 0, len(acc))
	for _, name := range CanonicalShadowRuleOrder {
		if row, ok := acc[name]; ok {
			out = append(out, *row)
		}
	}
	return out, nil
}

// RecentVerdicts returns the most recent shadow verdicts (optionally filtered by
// trader), newest first. Used by the monitoring page's live feed.
func (s *ShadowGateStore) RecentVerdicts(traderID string, limit int) ([]ShadowGateVerdict, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := s.db.Model(&ShadowGateVerdict{}).Order("observed_at DESC").Limit(limit)
	if traderID != "" {
		q = q.Where("trader_id = ?", traderID)
	}
	var out []ShadowGateVerdict
	err := q.Find(&out).Error
	return out, err
}
