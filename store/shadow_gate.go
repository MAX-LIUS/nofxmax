package store

import (
	"fmt"

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
	// Join each verdict to EXACTLY ONE closed position:
	//   - backfill rows carry position_id -> join by id (always 1:1, robust to
	//     non-unique (trader,cycle,symbol) keys);
	//   - forward-live rows have position_id=0 -> join by the per-decision unique
	//     (trader,cycle,symbol) with cycle>0, excluding any ambiguous key that
	//     maps to >1 closed position.
	// LEFT JOIN so unmatched verdicts still surface as pending.
	q := `
		SELECT v.rule_name, v.would_block,
		       p.realized_pnl,
		       CASE WHEN p.id IS NULL THEN 0 ELSE 1 END AS matched
		FROM shadow_gate_verdicts v
		LEFT JOIN trader_positions p
		  ON p.status = 'CLOSED'
		 AND (
		       (v.position_id > 0 AND p.id = v.position_id)
		    OR (v.position_id = 0 AND v.cycle > 0
		        AND p.trader_id = v.trader_id
		        AND p.entry_decision_cycle = v.cycle
		        AND p.symbol = v.symbol
		        AND p.entry_decision_cycle NOT IN (
		          SELECT entry_decision_cycle FROM trader_positions
		          WHERE status='CLOSED' AND entry_price>0
		          GROUP BY trader_id, entry_decision_cycle, symbol HAVING COUNT(*)>1
		        ))
		     )
		WHERE (v.position_id > 0 OR v.cycle > 0) AND ` + where
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
