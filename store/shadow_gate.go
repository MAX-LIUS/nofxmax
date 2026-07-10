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
	LiveAllowed bool  `gorm:"column:live_allowed;default:true" json:"live_allowed"`
	ObservedAt  int64 `gorm:"column:observed_at;not null;index:idx_shadow_trader_cycle" json:"observed_at"`
	CreatedAt   int64 `gorm:"column:created_at" json:"created_at"`
}

func (ShadowGateVerdict) TableName() string { return "shadow_gate_verdicts" }

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
	return s.db.Create(verdicts).Error
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
