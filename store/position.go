package store

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"nofx/logger"

	"gorm.io/gorm"
)

// adaptivePriceRound rounds a price based on its magnitude to preserve meaningful precision.
// For small prices (like meme coins), it preserves more decimal places.
// It detects the number of decimal places needed from the reference price(s).
func adaptivePriceRound(price float64, referencePrices ...float64) float64 {
	if price == 0 {
		return 0
	}

	// Find the minimum magnitude among all prices (including the price itself)
	minMagnitude := math.Abs(price)
	for _, ref := range referencePrices {
		if ref > 0 && ref < minMagnitude {
			minMagnitude = ref
		}
	}

	// Determine decimal places needed based on price magnitude
	// For price 0.000000541, we need ~15 decimal places
	// For price 0.0001, we need ~8 decimal places
	// For price 1.0, we need ~4 decimal places
	var multiplier float64
	switch {
	case minMagnitude < 0.000001: // Ultra small (meme coins like CHEEMS, SHIB)
		multiplier = 1e15 // 15 decimal places
	case minMagnitude < 0.0001: // Very small (PEPE, FLOKI)
		multiplier = 1e12 // 12 decimal places
	case minMagnitude < 0.01: // Small
		multiplier = 1e10 // 10 decimal places
	case minMagnitude < 1: // Medium
		multiplier = 1e8 // 8 decimal places
	default: // Large
		multiplier = 1e6 // 6 decimal places
	}

	return math.Round(price*multiplier) / multiplier
}

// getPriceDecimalPlaces returns the number of decimal places in a price string
func getPriceDecimalPlaces(price float64) int {
	if price == 0 {
		return 0
	}
	s := strconv.FormatFloat(price, 'f', -1, 64)
	idx := strings.Index(s, ".")
	if idx == -1 {
		return 0
	}
	return len(s) - idx - 1
}

// formatDuration formats a duration
func formatDuration(d time.Duration) string {
	return formatDurationMs(d.Milliseconds())
}

// formatDurationMs formats a duration in milliseconds
func formatDurationMs(ms int64) string {
	seconds := ms / 1000
	minutes := seconds / 60
	hours := minutes / 60
	days := hours / 24

	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	if hours < 24 {
		remainingMins := minutes % 60
		if remainingMins == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		return fmt.Sprintf("%dh%dm", hours, remainingMins)
	}
	remainingHours := hours % 24
	if remainingHours == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd%dh", days, remainingHours)
}

// TraderPosition position record
// All time fields use int64 millisecond timestamps (UTC) to avoid timezone issues
type TraderPosition struct {
	ID                 int64   `gorm:"primaryKey;autoIncrement" json:"id"`
	TraderID           string  `gorm:"column:trader_id;not null;index:idx_positions_trader" json:"trader_id"`
	ExchangeID         string  `gorm:"column:exchange_id;not null;default:'';index:idx_positions_exchange" json:"exchange_id"`
	ExchangeType       string  `gorm:"column:exchange_type;not null;default:''" json:"exchange_type"`
	ExchangePositionID string  `gorm:"column:exchange_position_id;not null;default:''" json:"exchange_position_id"`
	Symbol             string  `gorm:"column:symbol;not null" json:"symbol"`
	Side               string  `gorm:"column:side;not null" json:"side"`
	EntryQuantity      float64 `gorm:"column:entry_quantity;default:0" json:"entry_quantity"`
	Quantity           float64 `gorm:"column:quantity;not null" json:"quantity"`
	EntryPrice         float64 `gorm:"column:entry_price;not null" json:"entry_price"`
	EntryOrderID       string  `gorm:"column:entry_order_id;default:''" json:"entry_order_id"`
	EntryDecisionCycle int     `gorm:"column:entry_decision_cycle;default:0" json:"entry_decision_cycle"`
	EntryTime          int64   `gorm:"column:entry_time;not null;index:idx_positions_entry" json:"entry_time"` // Unix milliseconds UTC

	ExitPrice         float64 `gorm:"column:exit_price;default:0" json:"exit_price"`
	ExitOrderID       string  `gorm:"column:exit_order_id;default:''" json:"exit_order_id"`
	ExitDecisionCycle int     `gorm:"column:exit_decision_cycle;default:0" json:"exit_decision_cycle"`
	ExitTime          int64   `gorm:"column:exit_time;index:idx_positions_exit" json:"exit_time"` // Unix milliseconds UTC, 0 means not set
	RealizedPnL       float64 `gorm:"column:realized_pnl;default:0" json:"realized_pnl"`
	Fee               float64 `gorm:"column:fee;default:0" json:"fee"`
	Leverage          int     `gorm:"column:leverage;default:1" json:"leverage"`
	Status            string  `gorm:"column:status;default:OPEN;index:idx_positions_status" json:"status"`
	CloseReason       string  `gorm:"column:close_reason;default:''" json:"close_reason"`
	Source            string  `gorm:"column:source;default:system" json:"source"`
	EntrySceneTags    string  `gorm:"column:entry_scene_tags;default:''" json:"entry_scene_tags"` // JSON: trend_phase, ema20_deviation, regime, chg4h at entry

	// Excursion tracking (MFE/MAE): running favorable peak and adverse trough of the
	// position's profit%, plus each extreme in frozen-open-time ATR multiples. Updated
	// live on the drawdown poll and frozen onto the row at close so closed positions
	// stay fully reconstructable for review/backtest reverse-lookup. PeakPnlPct is the
	// high-water profit%; TroughPnlPct the low-water (most adverse) profit%. Peak/Trough
	// AtrMult = |extreme profit% distance from entry| / ATR% at entry.
	PeakPnlPct    float64 `gorm:"column:peak_pnl_pct;default:0" json:"peak_pnl_pct"`
	TroughPnlPct  float64 `gorm:"column:trough_pnl_pct;default:0" json:"trough_pnl_pct"`
	PeakAtrMult   float64 `gorm:"column:peak_atr_mult;default:0" json:"peak_atr_mult"`
	TroughAtrMult float64 `gorm:"column:trough_atr_mult;default:0" json:"trough_atr_mult"`

	// RatchetHistory is the JSON-serialized []RatchetEvent (structural-stop tighten log)
	// frozen onto the row at close. The live log lives transiently in system_config
	// (ratchet_events.go) keyed by trader+symbol+tf+side and is deleted at close; this
	// column preserves the full walk-in sequence on the closed position for the history
	// panel. Empty ("") when the position used no structural trail or never tightened.
	RatchetHistory string `gorm:"column:ratchet_history;type:text;default:''" json:"ratchet_history,omitempty"`

	CreatedAt int64 `gorm:"column:created_at" json:"created_at"` // Unix milliseconds UTC
	UpdatedAt int64 `gorm:"column:updated_at" json:"updated_at"` // Unix milliseconds UTC
}

// TableName returns the table name
func (TraderPosition) TableName() string {
	return "trader_positions"
}

// PositionStore position storage
type PositionStore struct {
	db *gorm.DB
}

func (s *PositionStore) deriveCloseReason(pos *TraderPosition, exchangeOrderID string, requestedReason string, closeQty float64, executionPrice float64) (string, string, string) {
	reason := requestedReason
	source := requestedReason
	executionType := "MARKET"
	if exchangeOrderID == "" || s.db == nil {
		if reason == "" {
			reason = "unknown"
			source = "unknown"
		}
		return reason, source, executionType
	}

	lookupOrder := func(id string) (*TraderOrder, bool) {
		if id == "" {
			return nil, false
		}
		var ord TraderOrder
		if err := s.db.Where("exchange_id = ? AND exchange_order_id = ?", pos.ExchangeID, id).First(&ord).Error; err == nil {
			return &ord, true
		}
		return nil, false
	}

	ord, ok := lookupOrder(exchangeOrderID)
	if !ok {
		var fill TraderFill
		if err := s.db.Where("exchange_id = ? AND exchange_trade_id = ?", pos.ExchangeID, exchangeOrderID).First(&fill).Error; err == nil {
			if parent, found := lookupOrder(fill.ExchangeOrderID); found {
				ord = parent
				ok = true
			}
		}
	}
	if ok && ord != nil && ord.ParentOrderID != "" {
		if parent, found := lookupOrder(ord.ParentOrderID); found {
			ord = parent
		}
	}

	if ok && ord != nil {
		tagLower := strings.ToLower(ord.ClientOrderID)
		actionLower := strings.ToLower(ord.OrderAction)
		switch {
		case strings.Contains(tagLower, "break_even") || strings.Contains(actionLower, "break_even"):
			reason = "break_even_stop"
			source = "break_even_stop"
		case strings.Contains(tagLower, "native_trailing") || strings.Contains(actionLower, "native_trailing"):
			reason = "native_trailing"
			source = "native_trailing"
		case strings.Contains(tagLower, "managed_drawdown") || strings.Contains(actionLower, "managed_drawdown"):
			reason = "managed_drawdown"
			source = "managed_drawdown"
		case strings.Contains(tagLower, "ladder_tp") || strings.Contains(actionLower, "ladder_tp"):
			reason = "ladder_tp"
			source = "ladder_tp"
		case strings.Contains(tagLower, "ladder_sl") || strings.Contains(actionLower, "ladder_sl"):
			reason = "ladder_sl"
			source = "ladder_sl"
		case strings.Contains(tagLower, "full_tp") || strings.Contains(actionLower, "full_tp"):
			reason = "full_tp"
			source = "full_tp"
		case strings.Contains(tagLower, "full_sl") || strings.Contains(actionLower, "full_sl") || strings.Contains(tagLower, "fallback_maxloss") || strings.Contains(actionLower, "fallback_maxloss"):
			reason = "full_sl"
			source = "full_sl"
		}
		kind := strings.ToUpper(ord.Type)
		executionType = kind
		isPartial := closeQty > 0 && closeQty < pos.Quantity-0.0001
		switch {
		case strings.Contains(kind, "TRAILING"):
			reason = "native_trailing"
			source = "native_trailing"
		case strings.Contains(kind, "TAKE_PROFIT") || strings.Contains(kind, "TP"):
			if isPartial {
				reason = "ladder_tp"
				source = "ladder_tp"
			} else {
				reason = "full_tp"
				source = "full_tp"
			}
		case strings.Contains(kind, "STOP") || strings.Contains(kind, "SL"):
			breakEvenThreshold := 0.003
			nearEntry := pos.EntryPrice > 0 && math.Abs(executionPrice-pos.EntryPrice)/pos.EntryPrice <= breakEvenThreshold
			if nearEntry {
				reason = "break_even_stop"
				source = "break_even_stop"
			} else if isPartial {
				reason = "ladder_sl"
				source = "ladder_sl"
			} else {
				reason = "full_sl"
				source = "full_sl"
			}
		case strings.HasPrefix(actionLower, "close_"):
			if reason == "" || reason == "close_long" || reason == "close_short" {
				reason = ord.OrderAction
				source = ord.OrderAction
			}
		}
		// Final adoption: the synced order's OrderAction may already carry a fully
		// resolved mechanism that the switches above do not enumerate (e.g.
		// ai_close_long, time_stop, max_hold, trailing_take_profit, managed_drawdown_*,
		// breadth_breaker). When the requested reason is still bare/empty, prefer the
		// order's resolved action so the close event is attributed, not dumped into
		// sync_external. Never override an already-specific reason.
		if (reason == "" || reason == "close_long" || reason == "close_short" || reason == "unknown") &&
			ord.OrderAction != "" && ord.OrderAction != "close_long" && ord.OrderAction != "close_short" &&
			!strings.HasPrefix(strings.ToLower(ord.OrderAction), "open_") {
			reason = ord.OrderAction
			source = ord.OrderAction
		}
	}
	if reason == "" {
		reason = "unknown"
	}
	if source == "" {
		source = reason
	}
	return reason, source, executionType
}

// decisionCycleHasAICloseFor reports whether the decision_records row for this
// trader+cycle contains an AI close decision for the given symbol/side. It is the
// deterministic signal used to attribute a bare close_long/close_short fill to an
// AI proactive close when no close-intent was persisted (e.g. Binance). Matching
// on the recorded AI decision — not a guess — keeps attribution trustworthy: a
// cycle=0 or exchange-discovered close (no matching decision) is left as-is.
func (s *PositionStore) decisionCycleHasAICloseFor(traderID string, cycle int, symbol, side string) bool {
	if s.db == nil || cycle <= 0 {
		return false
	}
	var rec DecisionRecordDB
	if err := s.db.Where("trader_id = ? AND cycle_number = ?", traderID, cycle).
		First(&rec).Error; err != nil {
		return false
	}
	if rec.Decisions == "" {
		return false
	}
	var decisions []struct {
		Action string `json:"action"`
		Symbol string `json:"symbol"`
	}
	if err := json.Unmarshal([]byte(rec.Decisions), &decisions); err != nil {
		return false
	}
	wantAction := "close_long"
	if strings.EqualFold(side, "SHORT") {
		wantAction = "close_short"
	}
	normSym := normalizeSymbolForMatch(symbol)
	for _, d := range decisions {
		if strings.EqualFold(d.Action, wantAction) && normalizeSymbolForMatch(d.Symbol) == normSym {
			return true
		}
	}
	return false
}

// normalizeSymbolForMatch reduces a symbol to a comparable base form (upper-case,
// stripped of common quote suffixes and separators) so a decision-record symbol
// matches the position symbol regardless of USDT/USDC/PERP/dash formatting
// differences across exchanges.
func normalizeSymbolForMatch(sym string) string {
	s := strings.ToUpper(strings.TrimSpace(sym))
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, "_", "")
	s = strings.ReplaceAll(s, "/", "")
	for _, suffix := range []string{"USDTPERP", "USDCPERP", "USDPERP", "PERP", "USDT", "USDC", "USD"} {
		if strings.HasSuffix(s, suffix) {
			return strings.TrimSuffix(s, suffix)
		}
	}
	return s
}

// isUnresolvedMechanism reports whether an attribution mechanism is still
// "unresolved" — i.e. it carries no specific close cause and should trigger the
// recovery fallbacks (executionSource, close-intent ledger, decision-cycle).
// Both sync_external (exchange fill with no recorded origin) and the bare
// unknown_close literal qualify; either should be replaced when a more specific
// source is available.
func isUnresolvedMechanism(mech string) bool {
	return mech == MechSyncExternal || mech == MechUnknownClose
}

func (s *PositionStore) logCloseEvent(pos *TraderPosition, closeReason, executionSource, executionType, exchangeOrderID string, closeQty, executionPrice, feeDelta, realizedPnLDelta float64, eventTimeMs int64) error {
	if s.db == nil || pos == nil || closeQty <= 0 {
		return nil
	}
	// Canonical attribution: prefer the most specific reason available. closeReason
	// carries the resolved mechanism (e.g. managed_drawdown_runner_exit); fall back
	// to executionSource when closeReason is unresolved (sync_external OR the bare
	// literal "unknown_close"). Previously this only recovered from sync_external, so
	// a caller passing closeReason="unknown_close" with executionSource="ladder_tp"
	// was stranded as unknown_close even though the mechanism was recoverable.
	attrInput := closeReason
	if isUnresolvedMechanism(ClassifyClose(attrInput).Mechanism) && executionSource != "" {
		if alt := ClassifyClose(executionSource); !isUnresolvedMechanism(alt.Mechanism) {
			attrInput = executionSource
		}
	}
	attr := ClassifyClose(attrInput)
	closeRatioPct := 0.0
	baseQty := pos.EntryQuantity
	if baseQty <= 0 {
		baseQty = pos.Quantity
	}
	if baseQty > 0 {
		closeRatioPct = closeQty / baseQty * 100
	}
	decisionCycle := pos.ExitDecisionCycle
	if decisionCycle == 0 {
		decisionCycle = pos.EntryDecisionCycle
	}
	parentOrderID := ""
	if exchangeOrderID != "" && s.db != nil {
		var fill TraderFill
		if err := s.db.Where("exchange_id = ? AND exchange_trade_id = ?", pos.ExchangeID, exchangeOrderID).First(&fill).Error; err == nil {
			parentOrderID = fill.ExchangeOrderID
		}
		var ord TraderOrder
		if err := s.db.Where("exchange_id = ? AND exchange_order_id = ?", pos.ExchangeID, exchangeOrderID).First(&ord).Error; err == nil && ord.ParentOrderID != "" {
			parentOrderID = ord.ParentOrderID
		}
	}
	// Close-intent fallback: when attribution still resolves to sync_external, the
	// trader_order's OrderAction has not yet been stamped by the async fill-sync
	// (logCloseEvent can run 1-2s ahead of order_sync). The close-intent ledger is
	// written at decision time — well before the fill — so it is immune to that
	// race. Look it up by the close order id (parent, then the fill's own id) and
	// adopt its reason. Read-only: order_sync remains the sole intent consumer.
	if isUnresolvedMechanism(attr.Mechanism) && s.db != nil {
		intentStore := NewCloseIntentStore(s.db)
		for _, oid := range []string{parentOrderID, exchangeOrderID} {
			if oid == "" {
				continue
			}
			if intent, ierr := intentStore.LookupByOrderID(pos.TraderID, oid); ierr == nil && intent != nil && intent.Reason != "" {
				if alt := ClassifyClose(intent.Reason); !isUnresolvedMechanism(alt.Mechanism) {
					closeReason = intent.Reason
					executionSource = intent.Reason
					attr = alt
					break
				}
			}
		}
	}
	// Decision-cycle fallback (exchange-agnostic, deterministic): some exchanges
	// (notably Binance) do not reliably persist a close-intent for a bot-issued AI
	// market close — the intent write can be skipped and the synced fill loses the
	// broker client id, so the two intent lookups above miss and the close lands in
	// sync_external even though the AI truly initiated it. When the position's exit
	// decision cycle links to a REAL close decision for THIS symbol/side in
	// decision_records, the close is deterministically an AI proactive close. This
	// makes the stored attribution agree with what the review panel already infers
	// from the decision link, instead of leaving the header as 未归因.
	if isUnresolvedMechanism(attr.Mechanism) && decisionCycle > 0 {
		if s.decisionCycleHasAICloseFor(pos.TraderID, decisionCycle, pos.Symbol, pos.Side) {
			aiReason := "ai_close_long"
			if strings.EqualFold(pos.Side, "SHORT") {
				aiReason = "ai_close_short"
			}
			closeReason = aiReason
			executionSource = aiReason
			attr = ClassifyClose(aiReason)
		}
	}
	event := &PositionCloseEvent{
		PositionID:       pos.ID,
		TraderID:         pos.TraderID,
		ExchangeID:       pos.ExchangeID,
		Symbol:           pos.Symbol,
		Side:             pos.Side,
		CloseReason:      closeReason,
		ExecutionSource:  executionSource,
		ExecutionType:    executionType,
		Category:         attr.Category,
		Mechanism:        attr.Mechanism,
		DecisionCycle:    decisionCycle,
		ExchangeOrderID:  exchangeOrderID,
		ParentOrderID:    parentOrderID,
		CloseQuantity:    closeQty,
		CloseRatioPct:    closeRatioPct,
		ExecutionPrice:   executionPrice,
		CloseValueUSDT:   executionPrice * closeQty,
		RealizedPnLDelta: realizedPnLDelta,
		FeeDelta:         feeDelta,
		EventTime:        eventTimeMs,
		CreatedAt:        time.Now().UTC().UnixMilli(),
	}
	store := NewPositionCloseEventStore(s.db)
	if err := store.Create(event); err != nil {
		if strings.Contains(err.Error(), "no such table") || strings.Contains(err.Error(), "does not exist") {
			if initErr := store.InitTables(); initErr == nil {
				return store.Create(event)
			}
		}
		return err
	}
	return nil
}

// NewPositionStore creates position storage instance
func NewPositionStore(db *gorm.DB) *PositionStore {
	return &PositionStore{db: db}
}

// isPostgres checks if the database is PostgreSQL
func (s *PositionStore) isPostgres() bool {
	return s.db.Dialector.Name() == "postgres"
}

// InitTables initializes position tables
func (s *PositionStore) InitTables() error {
	// For PostgreSQL with existing table, skip AutoMigrate
	if s.isPostgres() {
		var tableExists int64
		s.db.Raw(`SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'trader_positions'`).Scan(&tableExists)
		if tableExists > 0 {
			// Migrate timestamp columns to bigint (Unix milliseconds UTC)
			// Check if column is still timestamp type before migrating
			timestampColumns := []string{"entry_time", "exit_time", "created_at", "updated_at"}
			for _, col := range timestampColumns {
				var dataType string
				s.db.Raw(`SELECT data_type FROM information_schema.columns WHERE table_name = 'trader_positions' AND column_name = ?`, col).Scan(&dataType)
				if dataType == "timestamp with time zone" || dataType == "timestamp without time zone" {
					// Convert timestamp to Unix milliseconds (bigint)
					s.db.Exec(fmt.Sprintf(`ALTER TABLE trader_positions ALTER COLUMN %s TYPE BIGINT USING EXTRACT(EPOCH FROM %s) * 1000`, col, col))
				}
			}

			s.db.Exec(`ALTER TABLE trader_positions ADD COLUMN IF NOT EXISTS entry_decision_cycle INTEGER DEFAULT 0`)
			s.db.Exec(`ALTER TABLE trader_positions ADD COLUMN IF NOT EXISTS exit_decision_cycle INTEGER DEFAULT 0`)

			// Just ensure index exists
			s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_positions_exchange_pos_unique ON trader_positions(exchange_id, exchange_position_id) WHERE exchange_position_id != ''`)
			return nil
		}
	}

	if err := s.db.AutoMigrate(&TraderPosition{}); err != nil {
		return fmt.Errorf("failed to migrate trader_positions table: %w", err)
	}

	// Create unique partial index for exchange position deduplication
	var indexSQL string
	if s.isPostgres() {
		indexSQL = `CREATE UNIQUE INDEX IF NOT EXISTS idx_positions_exchange_pos_unique ON trader_positions(exchange_id, exchange_position_id) WHERE exchange_position_id != ''`
	} else {
		indexSQL = `CREATE UNIQUE INDEX IF NOT EXISTS idx_positions_exchange_pos_unique ON trader_positions(exchange_id, exchange_position_id) WHERE exchange_position_id != ''`
	}
	if err := s.db.Exec(indexSQL).Error; err != nil {
		if !strings.Contains(err.Error(), "already exists") && !strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return fmt.Errorf("failed to create unique index: %w", err)
		}
	}

	return nil
}

// Create creates position record
func (s *PositionStore) Create(pos *TraderPosition) error {
	pos.Status = "OPEN"
	if pos.EntryQuantity == 0 {
		pos.EntryQuantity = pos.Quantity
	}
	return s.db.Create(pos).Error
}

func (s *PositionStore) GetLatestDecisionCycle(traderID string) int {
	return s.GetLatestSuccessfulDecisionCycle(traderID)
}

func (s *PositionStore) GetLatestSuccessfulDecisionCycle(traderID string) int {
	if s.db == nil || traderID == "" {
		return 0
	}
	var cycle int
	s.db.Model(&DecisionRecordDB{}).
		Where("trader_id = ? AND success = ?", traderID, true).
		Select("COALESCE(MAX(cycle_number), 0)").
		Scan(&cycle)
	return cycle
}

func (s *PositionStore) FindEntryDecisionCycleForPosition(traderID, symbol, side string, entryTimeMs int64) int {
	if s.db == nil || traderID == "" || symbol == "" {
		return 0
	}
	sideLower := strings.ToLower(strings.TrimSpace(side))
	action := ""
	switch sideLower {
	case "long":
		action = "open_long"
	case "short":
		action = "open_short"
	}
	if action == "" {
		return 0
	}

	entryTime := time.Time{}
	if entryTimeMs > 0 {
		entryTime = time.UnixMilli(entryTimeMs).UTC()
	}
	// Use combined patterns to ensure symbol+action are in the same JSON object.
	// Separate LIKE clauses would match different objects in the same array.
	combinedAS := fmt.Sprintf("%%\"action\":\"%s\",\"symbol\":\"%s\"%%", action, symbol)
	combinedSA := fmt.Sprintf("%%\"symbol\":\"%s\",\"action\":\"%s\"%%", symbol, action)

	// Strip all whitespace (spaces, newlines, tabs) for matching pretty-printed JSON.
	stripped := "REPLACE(REPLACE(REPLACE(decisions, ' ', ''), char(10), ''), char(13), '')"

	// IMPORTANT: do NOT compare the decision timestamp in SQL. The timestamp /
	// created_at columns are declared `datetime`, and the modernc sqlite driver
	// coerces both the column and the bound string parameter to a datetime value
	// for `<=` comparisons — which silently corrupts ordering (a 21:22 row can
	// test as `<= 12:24`). That bug caused entry_decision_cycle to be linked to
	// the wrong re-entry of the same symbol, or not backfilled at all. Instead we
	// fetch the symbol/action candidates and pick the nearest by absolute time in
	// Go, using time.Parse on the scanned string (plain text, no coercion).
	type cand struct {
		Cycle     int
		Timestamp string
		CreatedAt string
	}
	var candidates []cand
	s.db.Model(&DecisionRecordDB{}).
		Where("trader_id = ? AND success = ?", traderID, true).
		Where(fmt.Sprintf("(%s LIKE ? OR %s LIKE ?)", stripped, stripped), combinedAS, combinedSA).
		Order("cycle_number DESC").
		Select("cycle_number AS cycle, timestamp, created_at").
		Scan(&candidates)

	if len(candidates) == 0 {
		return 0
	}

	// No entry time known: fall back to the most recent matching cycle.
	if entryTime.IsZero() {
		return candidates[0].Cycle
	}

	parseTS := func(c cand) (time.Time, bool) {
		for _, v := range []string{c.CreatedAt, c.Timestamp} {
			if v == "" {
				continue
			}
			if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
				return t.UTC(), true
			}
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				return t.UTC(), true
			}
		}
		return time.Time{}, false
	}

	// The decision record is written AFTER the order executes (AI response →
	// parse → execute order → record decision), so the decision timestamp is
	// typically 5-30 s after entry_time. Pick the candidate whose time is closest
	// to entry_time in absolute terms, bounded to 6h so a stale historical
	// re-entry of the same symbol can never be linked by mistake.
	const maxSkewMs = int64(6 * 60 * 60 * 1000)
	bestCycle := 0
	bestDiff := int64(1<<62 - 1)
	for _, c := range candidates {
		t, ok := parseTS(c)
		if !ok {
			continue
		}
		diff := entryTimeMs - t.UnixMilli()
		if diff < 0 {
			diff = -diff
		}
		if diff < bestDiff {
			bestDiff = diff
			bestCycle = c.Cycle
		}
	}
	if bestCycle > 0 && bestDiff <= maxSkewMs {
		return bestCycle
	}
	return 0
}

func (s *PositionStore) BackfillEntryDecisionCycle(positionID int64, cycle int) error {
	if s.db == nil || positionID <= 0 || cycle <= 0 {
		return nil
	}
	return s.db.Model(&TraderPosition{}).
		Where("id = ?", positionID).
		Updates(map[string]interface{}{
			"entry_decision_cycle": cycle,
			"updated_at":           time.Now().UTC().UnixMilli(),
		}).Error
}

// ClosePosition closes position
func (s *PositionStore) ClosePosition(id int64, exitPrice float64, exitOrderID string, realizedPnL float64, fee float64, closeReason string) error {
	nowMs := time.Now().UTC().UnixMilli()
	var pos TraderPosition
	if err := s.db.First(&pos, id).Error; err != nil {
		return err
	}
	if err := s.db.Model(&TraderPosition{}).Where("id = ?", id).Updates(map[string]interface{}{
		"exit_price":          exitPrice,
		"exit_order_id":       exitOrderID,
		"exit_decision_cycle": s.GetLatestDecisionCycle(pos.TraderID),
		"exit_time":           nowMs,
		"realized_pnl":        realizedPnL,
		"fee":                 fee,
		"status":              "CLOSED",
		"close_reason":        closeReason,
		"updated_at":          nowMs,
	}).Error; err != nil {
		return err
	}
	// Drain remaining unconsumed protection intents so untriggered tiers from this
	// position don't linger as residue (see ClosePositionFully). Best-effort.
	if _, err := NewCloseIntentStore(s.db).ExpireUnconsumedForPosition(pos.TraderID, pos.Symbol, pos.Side, nowMs); err != nil {
		logger.Warnf("[%s] expire unconsumed intents on ClosePosition failed: %v", pos.Symbol, err)
	}
	return nil
}

// UpdatePositionQuantityAndPrice updates position quantity and recalculates entry price
func (s *PositionStore) UpdatePositionQuantityAndPrice(id int64, addQty float64, addPrice float64, addFee float64) error {
	var pos TraderPosition
	if err := s.db.First(&pos, id).Error; err != nil {
		return fmt.Errorf("failed to get current position: %w", err)
	}

	currentEntryQty := pos.EntryQuantity
	if currentEntryQty == 0 {
		currentEntryQty = pos.Quantity
	}

	newQty := math.Round((pos.Quantity+addQty)*10000) / 10000
	newEntryQty := math.Round((currentEntryQty+addQty)*10000) / 10000
	newEntryPrice := (pos.EntryPrice*pos.Quantity + addPrice*addQty) / newQty
	// Use adaptive precision based on price magnitude (for meme coins with very small prices)
	newEntryPrice = adaptivePriceRound(newEntryPrice, pos.EntryPrice, addPrice)
	newFee := pos.Fee + addFee
	nowMs := time.Now().UTC().UnixMilli()

	return s.db.Model(&TraderPosition{}).Where("id = ?", id).Updates(map[string]interface{}{
		"quantity":       newQty,
		"entry_quantity": newEntryQty,
		"entry_price":    newEntryPrice,
		"fee":            newFee,
		"updated_at":     nowMs,
	}).Error
}

// ReducePositionQuantity reduces position quantity for partial close
// If quantity reaches 0 (or near 0), automatically closes the position
func (s *PositionStore) ReducePositionQuantity(id int64, reduceQty float64, exitPrice float64, addFee float64, addPnL float64, closeReason string, executionSource string, executionType string, exchangeOrderID string, eventTimeMs int64) error {
	var pos TraderPosition
	if err := s.db.First(&pos, id).Error; err != nil {
		return fmt.Errorf("failed to get current position: %w", err)
	}

	newQty := math.Round((pos.Quantity-reduceQty)*10000) / 10000
	newFee := pos.Fee + addFee
	newPnL := pos.RealizedPnL + addPnL

	closedQty := pos.EntryQuantity - pos.Quantity
	newClosedQty := closedQty + reduceQty

	var newExitPrice float64
	if newClosedQty > 0 {
		newExitPrice = (pos.ExitPrice*closedQty + exitPrice*reduceQty) / newClosedQty
		// Use adaptive precision based on price magnitude (for meme coins with very small prices)
		newExitPrice = adaptivePriceRound(newExitPrice, pos.ExitPrice, exitPrice, pos.EntryPrice)
	}

	nowMs := time.Now().UTC().UnixMilli()
	if eventTimeMs == 0 {
		eventTimeMs = nowMs
	}
	reason, source, execType := s.deriveCloseReason(&pos, exchangeOrderID, closeReason, reduceQty, exitPrice)

	// Check if position should be fully closed (quantity reduced to ~0)
	const QUANTITY_TOLERANCE = 0.0001
	if newQty <= QUANTITY_TOLERANCE {
		if err := s.db.Model(&TraderPosition{}).Where("id = ?", id).Updates(map[string]interface{}{
			"quantity":            0,
			"fee":                 newFee,
			"exit_price":          newExitPrice,
			"exit_decision_cycle": s.GetLatestDecisionCycle(pos.TraderID),
			"realized_pnl":        newPnL,
			"status":              "CLOSED",
			"exit_time":           nowMs,
			"close_reason":        reason,
			"updated_at":          nowMs,
		}).Error; err != nil {
			return err
		}
		// Expire any remaining unconsumed protection intents for this
		// (trader, symbol, side) up to the close time, so untriggered ladder/BE
		// tiers from THIS position don't linger as residue. Mirrors
		// ClosePositionFully; this incremental reduce-to-zero path (used by the
		// OKX fill-sync) previously left them unconsumed. Bounded by close time so
		// a brand-new same-symbol position opened later is untouched. Best-effort.
		if n, err := NewCloseIntentStore(s.db).ExpireUnconsumedForPosition(pos.TraderID, pos.Symbol, pos.Side, nowMs); err != nil {
			logger.Warnf("[%s] expire unconsumed intents on incremental full-close failed: %v", pos.Symbol, err)
		} else if n > 0 {
			logger.Infof("[%s %s] expired %d unconsumed protection intent(s) on full close", pos.Symbol, pos.Side, n)
		}
		return s.logCloseEvent(&pos, reason, source, execType, exchangeOrderID, reduceQty, exitPrice, addFee, addPnL, eventTimeMs)
	}

	if err := s.db.Model(&TraderPosition{}).Where("id = ?", id).Updates(map[string]interface{}{
		"quantity":     newQty,
		"fee":          newFee,
		"exit_price":   newExitPrice,
		"realized_pnl": newPnL,
		"updated_at":   nowMs,
	}).Error; err != nil {
		return err
	}
	return s.logCloseEvent(&pos, reason, source, execType, exchangeOrderID, reduceQty, exitPrice, addFee, addPnL, eventTimeMs)
}

// UpdatePositionExchangeInfo updates exchange_id and exchange_type
func (s *PositionStore) UpdatePositionExchangeInfo(id int64, exchangeID, exchangeType string) error {
	nowMs := time.Now().UTC().UnixMilli()
	return s.db.Model(&TraderPosition{}).Where("id = ?", id).Updates(map[string]interface{}{
		"exchange_id":   exchangeID,
		"exchange_type": exchangeType,
		"updated_at":    nowMs,
	}).Error
}

// stampExcursionOntoUpdates freezes the position's final favorable-peak / adverse-
// trough profit% + ATR multiples onto the close Updates map, read from the live
// peak_pnl_states row. This preserves the full MFE/MAE trail on the closed history
// row before ClearPeakPnLCache deletes the transient state. Best-effort: a missing
// row (e.g. position never polled) leaves the columns at their existing values.
// pos_key matching is case-insensitive because the live cache uses exchange-reported
// side casing (OKX "long") while the DB row stores uppercase ("LONG").
func (s *PositionStore) stampExcursionOntoUpdates(pos *TraderPosition, updates map[string]interface{}) {
	if pos == nil || s.db == nil || pos.TraderID == "" {
		return
	}
	posKey := pos.Symbol + "_" + pos.Side
	var ex struct {
		PeakPnlPct    float64
		TroughPnlPct  float64
		PeakAtrMult   float64
		TroughAtrMult float64
	}
	row := s.db.Raw(`
		SELECT COALESCE(peak_pnl_pct,0)   AS peak_pnl_pct,
		       COALESCE(trough_pnl_pct,0) AS trough_pnl_pct,
		       COALESCE(peak_atr_mult,0)  AS peak_atr_mult,
		       COALESCE(trough_atr_mult,0) AS trough_atr_mult
		FROM peak_pnl_states
		WHERE trader_id = ? AND lower(pos_key) = lower(?)
		LIMIT 1`, pos.TraderID, posKey).Row()
	if row == nil {
		return
	}
	if err := row.Scan(&ex.PeakPnlPct, &ex.TroughPnlPct, &ex.PeakAtrMult, &ex.TroughAtrMult); err != nil {
		return // no live excursion row; leave columns unchanged
	}
	updates["peak_pnl_pct"] = ex.PeakPnlPct
	updates["trough_pnl_pct"] = ex.TroughPnlPct
	updates["peak_atr_mult"] = ex.PeakAtrMult
	updates["trough_atr_mult"] = ex.TroughAtrMult
}

// stampRatchetHistoryOntoUpdates freezes the structural-stop tighten log onto the
// close Updates map, then clears the transient log so a future position on the
// same symbol/side starts clean. The live log (ratchet_events.go) is keyed by
// trader+symbol+tf+side, but the store close path doesn't know the timeframe — so
// this matches on the event CONTENT (TraderID+Symbol+Side+EntryPrice) which every
// RatchetEvent carries, making it timeframe-agnostic and robust to the 1h/4h key
// split. Best-effort: any failure leaves ratchet_history at its existing value and
// never blocks the close. side matching is case-insensitive (live cache may use
// exchange-reported casing; DB row stores uppercase).
func (s *PositionStore) stampRatchetHistoryOntoUpdates(pos *TraderPosition, updates map[string]interface{}) {
	if pos == nil || s.db == nil || pos.TraderID == "" {
		return
	}
	// Reach the shared Store (system_config-backed) via the same *gorm.DB.
	// PositionStore.db IS the *gorm.DB, which maps to Store.gdb (the field the
	// system_config get/set helpers use).
	rs := &Store{gdb: s.db}
	state, err := rs.LoadRatchetEventsState()
	if err != nil || state == nil || len(state.Events) == 0 {
		return
	}
	sideUpper := strings.ToUpper(pos.Side)
	var matched []RatchetEvent
	var matchedKeys []string
	for key, evs := range state.Events {
		for _, ev := range evs {
			if ev.TraderID != pos.TraderID {
				break // whole key belongs to another trader
			}
			if !strings.EqualFold(ev.Symbol, pos.Symbol) || !strings.EqualFold(ev.Side, sideUpper) {
				break
			}
			// Guard against a stale log from a prior position on the same symbol/side:
			// require the entry price to match this position (same tolerance as the
			// frozen-ATR reuse guard uses elsewhere — exact for our own writes).
			if pos.EntryPrice > 0 && ev.EntryPrice > 0 &&
				math.Abs(ev.EntryPrice-pos.EntryPrice)/pos.EntryPrice > 0.005 {
				break
			}
			matched = append(matched, evs...)
			matchedKeys = append(matchedKeys, key)
			break // consumed this key's slice
		}
	}
	if len(matched) == 0 {
		return
	}
	raw, err := json.Marshal(matched)
	if err != nil {
		return
	}
	updates["ratchet_history"] = string(raw)
	// Clear the transient log for the matched keys so the next position is clean.
	for _, k := range matchedKeys {
		if err := rs.DeleteRatchetEvents(k); err != nil {
			logger.Warnf("⚠️ Failed to clear ratchet events for %s: %v", k, err)
		}
	}
}

// ClosePositionFully marks position as fully closed
// exitTimeMs is Unix milliseconds UTC
func (s *PositionStore) ClosePositionFully(id int64, exitPrice float64, exitOrderID string, exitTimeMs int64, totalRealizedPnL float64, totalFee float64, closeReason string, executionSource string, executionType string) error {
	var pos TraderPosition
	if err := s.db.First(&pos, id).Error; err != nil {
		return fmt.Errorf("failed to get position: %w", err)
	}

	quantity := pos.Quantity
	if pos.EntryQuantity > 0 {
		quantity = pos.EntryQuantity
	}
	reason, source, execType := s.deriveCloseReason(&pos, exitOrderID, closeReason, pos.Quantity, exitPrice)

	updates := map[string]interface{}{
		"quantity":            quantity,
		"exit_price":          exitPrice,
		"exit_order_id":       exitOrderID,
		"exit_decision_cycle": s.GetLatestDecisionCycle(pos.TraderID),
		"exit_time":           exitTimeMs,
		"realized_pnl":        totalRealizedPnL,
		"fee":                 totalFee,
		"status":              "CLOSED",
		"close_reason":        reason,
		"updated_at":          time.Now().UTC().UnixMilli(),
	}
	s.stampExcursionOntoUpdates(&pos, updates)
	s.stampRatchetHistoryOntoUpdates(&pos, updates)
	if err := s.db.Model(&TraderPosition{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return err
	}
	feeDelta := totalFee - pos.Fee
	realizedPnLDelta := totalRealizedPnL - pos.RealizedPnL
	// Drain any leftover unconsumed protection intents for this symbol/side: the
	// position is now closed, so its untriggered ladder tiers must not survive to
	// be borrowed by the NEXT position's close via the trigger-price matcher
	// (cross-position stringing). Best-effort; a failure here never blocks the
	// close. Bounded to intents recorded at/before this close time.
	if n, err := NewCloseIntentStore(s.db).ExpireUnconsumedForPosition(pos.TraderID, pos.Symbol, pos.Side, exitTimeMs); err != nil {
		logger.Warnf("⚠️ Failed to expire leftover close-intents for %s %s: %v", pos.Symbol, pos.Side, err)
	} else if n > 0 {
		logger.Infof("🧹 Expired %d leftover protection intents for closed %s %s", n, pos.Symbol, pos.Side)
	}
	return s.logCloseEvent(&pos, reason, source, execType, exitOrderID, pos.Quantity, exitPrice, feeDelta, realizedPnLDelta, exitTimeMs)
}

func (s *PositionStore) UpdateCloseReasonByExitOrderID(traderID, exitOrderID, closeReason string) error {
	if exitOrderID == "" || closeReason == "" {
		return nil
	}
	return s.db.Model(&TraderPosition{}).
		Where("trader_id = ? AND exit_order_id = ?", traderID, exitOrderID).
		Updates(map[string]interface{}{"close_reason": closeReason, "updated_at": time.Now().UTC().UnixMilli()}).Error
}

// DeleteAllOpenPositions deletes all OPEN positions for a trader
func (s *PositionStore) DeleteAllOpenPositions(traderID string) error {
	return s.db.Where("trader_id = ? AND status = ?", traderID, "OPEN").Delete(&TraderPosition{}).Error
}

// GetOpenPositions gets all open positions
func (s *PositionStore) GetOpenPositions(traderID string) ([]*TraderPosition, error) {
	var positions []*TraderPosition
	err := s.db.Where("trader_id = ? AND status = ?", traderID, "OPEN").
		Order("entry_time DESC").
		Find(&positions).Error
	if err != nil {
		return nil, fmt.Errorf("failed to query open positions: %w", err)
	}

	// Fix EntryQuantity if it's 0
	for _, pos := range positions {
		if pos.EntryQuantity == 0 {
			pos.EntryQuantity = pos.Quantity
		}
	}
	return positions, nil
}

// GetOpenPositionBySymbol gets open position for specified symbol and direction
func (s *PositionStore) GetOpenPositionBySymbol(traderID, symbol, side string) (*TraderPosition, error) {
	var pos TraderPosition
	err := s.db.Where("trader_id = ? AND symbol = ? AND side = ? AND status = ?", traderID, symbol, side, "OPEN").
		Order("entry_time DESC").
		First(&pos).Error

	if err == nil {
		if pos.EntryQuantity == 0 {
			pos.EntryQuantity = pos.Quantity
		}
		return &pos, nil
	}

	if err == gorm.ErrRecordNotFound {
		// Try without USDT suffix for backward compatibility
		if strings.HasSuffix(symbol, "USDT") {
			baseSymbol := strings.TrimSuffix(symbol, "USDT")
			err = s.db.Where("trader_id = ? AND symbol = ? AND side = ? AND status = ?", traderID, baseSymbol, side, "OPEN").
				Order("entry_time DESC").
				First(&pos).Error
			if err == nil {
				if pos.EntryQuantity == 0 {
					pos.EntryQuantity = pos.Quantity
				}
				return &pos, nil
			}
		}
		return nil, nil
	}
	return nil, err
}

// GetClosedPositions gets closed positions
func (s *PositionStore) GetClosedPositions(traderID string, limit int) ([]*TraderPosition, error) {
	var positions []*TraderPosition
	err := s.db.Where("trader_id = ? AND status = ?", traderID, "CLOSED").
		Order("exit_time DESC").
		Limit(limit).
		Find(&positions).Error
	if err != nil {
		return nil, fmt.Errorf("failed to query closed positions: %w", err)
	}

	for _, pos := range positions {
		if pos.EntryQuantity == 0 {
			pos.EntryQuantity = pos.Quantity
		}
	}
	return positions, nil
}

func (s *PositionStore) GetClosedPositionsWithOffset(traderID string, limit, offset int) ([]*TraderPosition, error) {
	var positions []*TraderPosition
	err := s.db.Where("trader_id = ? AND status = ?", traderID, "CLOSED").
		Order("exit_time DESC").
		Limit(limit).
		Offset(offset).
		Find(&positions).Error
	if err != nil {
		return nil, fmt.Errorf("failed to query closed positions: %w", err)
	}

	for _, pos := range positions {
		if pos.EntryQuantity == 0 {
			pos.EntryQuantity = pos.Quantity
		}
	}
	return positions, nil
}

func (s *PositionStore) UpdateSceneTags(positionID int64, tags string) error {
	return s.db.Model(&TraderPosition{}).Where("id = ?", positionID).
		Update("entry_scene_tags", tags).Error
}

// GetRecentlyClosedSyncAbsentPosition returns the most recent position closed via
// sync_absent_from_exchange for the same symbol/side within the specified delay window.
// The fill's trade time may be earlier than the sync_absent exit_time (exchange executes
// the stop, then our next position poll detects the absence), so we also match positions
// closed slightly AFTER the trade time.
func (s *PositionStore) GetRecentlyClosedSyncAbsentPosition(traderID, symbol, side string, tradeTimeMs int64, maxDelay time.Duration) (*TraderPosition, error) {
	if s.db == nil || traderID == "" || symbol == "" || side == "" || tradeTimeMs <= 0 {
		return nil, nil
	}
	if maxDelay <= 0 {
		maxDelay = 2 * time.Minute
	}
	minExitTime := tradeTimeMs - maxDelay.Milliseconds()
	maxExitTime := tradeTimeMs + maxDelay.Milliseconds()
	var pos TraderPosition
	err := s.db.Where("trader_id = ? AND symbol = ? AND side = ? AND status = ? AND close_reason = ? AND exit_time > 0 AND exit_time >= ? AND exit_time <= ?",
		traderID, symbol, side, "CLOSED", "sync_absent_from_exchange", minExitTime, maxExitTime).
		Order("exit_time DESC, id DESC").
		First(&pos).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query recently closed sync-absent position: %w", err)
	}
	if pos.EntryQuantity == 0 {
		pos.EntryQuantity = pos.Quantity
	}
	return &pos, nil
}

// GetRecentlyClosedUnderClosedPosition finds a position that was closed within the
// delay window AND is under-closed (the sum of its recorded close events is less
// than its entry quantity), regardless of the specific close_reason.
//
// Why broader than GetRecentlyClosedSyncAbsentPosition (fix 2026-07-17 SOL): when the
// exchange closes the tail of a position via a resting order (break-even/structural/
// trailing) and the /positions reconcile marks the local row CLOSED before those
// closing fills sync, MarkOpenPositionsAbsentFromExchangeClosed's attribution step
// rewrites close_reason to the specific protection tag (e.g. "ladder_tp") — which
// made the sync_absent-only matcher miss the late fills, dropping their PnL and
// leaving them unlinked (rpid=0). Matching on "recently closed AND under-closed"
// instead catches every such late fill. Under-closure is the safety gate: a fully
// closed position (recorded closes >= entry qty) never matches, so a genuinely
// unrelated later fill for a NEW same-symbol position can't attach here. Applying is
// still capped by ApplyLateCloseFillToClosedPosition's remaining-quantity clamp.
func (s *PositionStore) GetRecentlyClosedUnderClosedPosition(traderID, symbol, side string, tradeTimeMs int64, maxDelay time.Duration) (*TraderPosition, error) {
	if s.db == nil || traderID == "" || symbol == "" || side == "" || tradeTimeMs <= 0 {
		return nil, nil
	}
	if maxDelay <= 0 {
		maxDelay = 2 * time.Minute
	}
	minExitTime := tradeTimeMs - maxDelay.Milliseconds()
	maxExitTime := tradeTimeMs + maxDelay.Milliseconds()
	var candidates []TraderPosition
	err := s.db.Where("trader_id = ? AND symbol = ? AND side = ? AND status = ? AND exit_time > 0 AND exit_time >= ? AND exit_time <= ?",
		traderID, symbol, side, "CLOSED", minExitTime, maxExitTime).
		Order("exit_time DESC, id DESC").
		Find(&candidates).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query recently closed positions: %w", err)
	}
	eventStore := NewPositionCloseEventStore(s.db)
	const qtyTolerance = 0.0001
	for i := range candidates {
		pos := candidates[i]
		entryQty := pos.EntryQuantity
		if entryQty <= 0 {
			entryQty = pos.Quantity
		}
		if entryQty <= 0 {
			continue
		}
		events, err := eventStore.ListByPositionID(pos.ID)
		if err != nil {
			return nil, err
		}
		closedRecorded := 0.0
		for _, event := range events {
			if event != nil {
				closedRecorded += event.CloseQuantity
			}
		}
		if closedRecorded < entryQty-qtyTolerance {
			pos.EntryQuantity = entryQty
			return &pos, nil
		}
	}
	return nil, nil
}

// ApplyLateCloseFillToClosedPosition backfills a close fill that arrived after the
// local position had already been marked closed. This preserves close-event and PnL
// accounting for short sync gaps without reopening the position.
func (s *PositionStore) ApplyLateCloseFillToClosedPosition(id int64, closeQty float64, exitPrice float64, addFee float64, addPnL float64, closeReason string, executionSource string, executionType string, exchangeOrderID string, eventTimeMs int64) error {
	if s.db == nil || id <= 0 || closeQty <= 0 {
		return nil
	}
	var pos TraderPosition
	if err := s.db.First(&pos, id).Error; err != nil {
		return fmt.Errorf("failed to get closed position: %w", err)
	}
	if pos.Status != "CLOSED" {
		return fmt.Errorf("position %d is not closed", id)
	}
	entryQty := pos.EntryQuantity
	if entryQty <= 0 {
		entryQty = pos.Quantity
	}
	if entryQty <= 0 {
		return nil
	}

	events, err := NewPositionCloseEventStore(s.db).ListByPositionID(id)
	if err != nil {
		return err
	}
	closedBefore := 0.0
	recordedCloseValue := 0.0
	for _, event := range events {
		if event == nil {
			continue
		}
		closedBefore += event.CloseQuantity
		recordedCloseValue += event.ExecutionPrice * event.CloseQuantity
	}
	remaining := entryQty - closedBefore
	if remaining <= 0 {
		return nil
	}
	appliedQty := closeQty
	if appliedQty > remaining {
		appliedQty = remaining
	}
	if appliedQty <= 0 {
		return nil
	}

	recordedExitPrice := pos.ExitPrice
	if closedBefore > 0 {
		recordedExitPrice = recordedCloseValue / closedBefore
	} else if recordedExitPrice == 0 {
		recordedExitPrice = pos.EntryPrice
	}
	newClosedQty := closedBefore + appliedQty
	newExitPrice := exitPrice
	if newClosedQty > 0 {
		newExitPrice = adaptivePriceRound((recordedExitPrice*closedBefore+exitPrice*appliedQty)/newClosedQty, recordedExitPrice, exitPrice, pos.EntryPrice)
	}
	nowMs := time.Now().UTC().UnixMilli()
	if eventTimeMs == 0 {
		eventTimeMs = nowMs
	}
	reason, source, execType := s.deriveCloseReason(&pos, exchangeOrderID, closeReason, appliedQty, exitPrice)
	if closeReason != "" {
		reason = closeReason
	}
	if executionSource != "" {
		source = executionSource
	}
	if executionType != "" {
		execType = executionType
	}

	updates := map[string]interface{}{
		"realized_pnl": pos.RealizedPnL + addPnL,
		"fee":          pos.Fee + addFee,
		"exit_price":   newExitPrice,
		"updated_at":   nowMs,
	}
	// Attribution fix: when the original row reason is generic/sync, upgrade it to
	// the specific protection reason derived from the late fill's order tag.
	if reason != "" && reason != "unknown" && reason != "close_long" && reason != "close_short" {
		switch pos.CloseReason {
		case "", "unknown", "close_long", "close_short", "sync_absent_from_exchange", "sync_external":
			updates["close_reason"] = reason
		}
	}
	if pos.ExitTime == 0 || eventTimeMs > pos.ExitTime {
		updates["exit_time"] = eventTimeMs
	}
	if err := s.db.Model(&TraderPosition{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return err
	}
	return s.logCloseEvent(&pos, reason, source, execType, exchangeOrderID, appliedQty, exitPrice, addFee, addPnL, eventTimeMs)
}

// GetAllOpenPositions gets all traders' open positions
func (s *PositionStore) GetAllOpenPositions() ([]*TraderPosition, error) {
	var positions []*TraderPosition
	err := s.db.Where("status = ?", "OPEN").
		Order("trader_id, entry_time DESC").
		Find(&positions).Error
	if err != nil {
		return nil, fmt.Errorf("failed to query all open positions: %w", err)
	}

	for _, pos := range positions {
		if pos.EntryQuantity == 0 {
			pos.EntryQuantity = pos.Quantity
		}
	}
	return positions, nil
}

// ExistsWithExchangePositionID checks if a position exists
func (s *PositionStore) ExistsWithExchangePositionID(exchangeID, exchangePositionID string) (bool, error) {
	if exchangePositionID == "" {
		return false, nil
	}

	var count int64
	err := s.db.Model(&TraderPosition{}).
		Where("exchange_id = ? AND exchange_position_id = ?", exchangeID, exchangePositionID).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("failed to check position existence: %w", err)
	}
	return count > 0, nil
}

// GetOpenPositionByExchangePositionID gets an OPEN position by exchange_position_id
func (s *PositionStore) GetOpenPositionByExchangePositionID(exchangeID, exchangePositionID string) (*TraderPosition, error) {
	if exchangePositionID == "" {
		return nil, nil
	}

	var pos TraderPosition
	err := s.db.Where("exchange_id = ? AND exchange_position_id = ? AND status = ?", exchangeID, exchangePositionID, "OPEN").
		First(&pos).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}

	if pos.EntryQuantity == 0 {
		pos.EntryQuantity = pos.Quantity
	}
	return &pos, nil
}

// CreateOpenPosition creates an open position
func (s *PositionStore) CreateOpenPosition(pos *TraderPosition) error {
	if pos.ExchangePositionID != "" && pos.ExchangeID != "" {
		existingPos, err := s.GetOpenPositionByExchangePositionID(pos.ExchangeID, pos.ExchangePositionID)
		if err != nil {
			return err
		}
		if existingPos != nil {
			return s.UpdatePositionQuantityAndPrice(existingPos.ID, pos.Quantity, pos.EntryPrice, pos.Fee)
		}
		exists, err := s.ExistsWithExchangePositionID(pos.ExchangeID, pos.ExchangePositionID)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
	}

	// Net-position guard (fix 2026-06-22): the exchange runs one-way (net) mode, so
	// there must be at most ONE OPEN row per (trader, symbol, side). A sync race can
	// reach here with a fresh exchange_position_id even though an OPEN net row already
	// exists (e.g. the prior row was briefly marked sync-absent). Creating a second row
	// makes GetOpenPositionBySymbol return only the newest leg, which mis-sizes ladder
	// protection and makes the reconciler churn forever. Merge into the existing net
	// row instead of inserting a duplicate.
	if pos.TraderID != "" && pos.Symbol != "" && pos.Side != "" && pos.Quantity > 0 {
		netPos, err := s.GetOpenPositionBySymbol(pos.TraderID, pos.Symbol, pos.Side)
		if err != nil {
			return err
		}
		if netPos != nil {
			return s.UpdatePositionQuantityAndPrice(netPos.ID, pos.Quantity, pos.EntryPrice, pos.Fee)
		}
	}

	if pos.Status == "" {
		pos.Status = "OPEN"
	}
	if pos.Source == "" {
		pos.Source = "system"
	}
	if pos.EntryQuantity == 0 {
		pos.EntryQuantity = pos.Quantity
	}

	err := s.db.Create(pos).Error
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			existingPos, findErr := s.GetOpenPositionByExchangePositionID(pos.ExchangeID, pos.ExchangePositionID)
			if findErr != nil {
				return findErr
			}
			if existingPos != nil {
				return s.UpdatePositionQuantityAndPrice(existingPos.ID, pos.Quantity, pos.EntryPrice, pos.Fee)
			}
			return nil
		}
		return fmt.Errorf("failed to create open position: %w", err)
	}

	return nil
}

// ClosePositionWithAccurateData closes a position with accurate data from exchange
// exitTimeMs is Unix milliseconds UTC
func (s *PositionStore) ClosePositionWithAccurateData(id int64, exitPrice float64, exitOrderID string, exitTimeMs int64, realizedPnL float64, fee float64, closeReason string) error {
	updates := map[string]interface{}{
		"exit_price":    exitPrice,
		"exit_order_id": exitOrderID,
		"exit_time":     exitTimeMs,
		"realized_pnl":  realizedPnL,
		"fee":           fee,
		"status":        "CLOSED",
		"close_reason":  closeReason,
		"updated_at":    time.Now().UTC().UnixMilli(),
	}
	var pos TraderPosition
	if err := s.db.First(&pos, id).Error; err == nil {
		s.stampExcursionOntoUpdates(&pos, updates)
		s.stampRatchetHistoryOntoUpdates(&pos, updates)
	}
	if err := s.db.Model(&TraderPosition{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return err
	}
	// Drain remaining unconsumed protection intents (see ClosePositionFully).
	closeMs := exitTimeMs
	if closeMs <= 0 {
		closeMs = time.Now().UTC().UnixMilli()
	}
	if _, err := NewCloseIntentStore(s.db).ExpireUnconsumedForPosition(pos.TraderID, pos.Symbol, pos.Side, closeMs); err != nil {
		logger.Warnf("[%s] expire unconsumed intents on ClosePositionWithAccurateData failed: %v", pos.Symbol, err)
	}
	return nil
}

func (s *PositionStore) MarkOpenPositionsAbsentFromExchangeClosed(traderID string, livePositions map[string]float64, closeReason string) (int64, error) {
	if traderID == "" {
		return 0, nil
	}
	if closeReason == "" {
		closeReason = "sync_absent_from_exchange"
	}
	openPositions, err := s.GetOpenPositions(traderID)
	if err != nil {
		return 0, err
	}

	// Determine which local OPEN positions are absent from the exchange snapshot.
	absent := make([]*TraderPosition, 0, len(openPositions))
	for _, pos := range openPositions {
		key := positionPresenceKey(pos.Symbol, pos.Side)
		liveQty, ok := livePositions[key]
		// Present on the exchange => NOT absent, regardless of quantity.
		// A smaller live qty than our local row is a PARTIAL close (e.g. a drawdown
		// tier just reduced the position on-exchange) whose close fill has not yet
		// synced — force-closing the local row here would zero out a position that is
		// still live on the exchange, orphaning the remainder (fix 2026-07-17
		// SPCX partial-close race). Quantity divergence is reconciled by the fill-sync
		// path, not by this absence sweep. Only a key that is entirely missing from
		// the snapshot counts as absent.
		if ok && liveQty > 0 {
			continue
		}
		absent = append(absent, pos)
	}

	// Empty/incomplete-snapshot guard (fix 2026-06-24 mass false-close): a single
	// transient exchange fetch that returns zero (or a severely incomplete) position
	// set must not wipe every local OPEN row. When 2+ positions would all be closed
	// in one pass AND that accounts for every local open position, treat the snapshot
	// as untrustworthy (API hiccup) and skip — a genuine simultaneous close of every
	// position is far rarer than a flaky poll. A single absent position is still
	// reconciled, since closing the last position legitimately empties the snapshot.
	if len(absent) >= 2 && len(absent) == len(openPositions) {
		logger.Warnf("🛑 Position sync guard: exchange snapshot missing ALL %d open positions for trader %s; treating as transient fetch failure and skipping mass close", len(openPositions), traderID)
		return 0, nil
	}

	nowMs := time.Now().UTC().UnixMilli()
	var updated int64
	for _, pos := range absent {
		exitPrice := pos.EntryPrice
		realizedPnl := pos.RealizedPnL
		totalFee := pos.Fee

		// Try to compute real PnL from close fills/orders for the remaining quantity
		if computed, compFee, compExit, ok := s.computePnlFromFills(pos); ok {
			exitPrice = compExit
			realizedPnl = pos.RealizedPnL + computed
			totalFee = pos.Fee + compFee
		}

		// Attribution fix: if a linked closing order exists, derive the real close
		// reason from its protection tag instead of the generic sync_absent label.
		// This recovers protection attribution (SL/TP/BE/trailing) for positions the
		// exchange closed via a resting order that our next poll detected as absent.
		rowCloseReason := closeReason
		if closeOrderID := s.dominantCloseOrderID(pos); closeOrderID != "" {
			if derived, _, _ := s.deriveCloseReason(pos, closeOrderID, "", pos.Quantity, exitPrice); derived != "" &&
				derived != "close_long" && derived != "close_short" && derived != "unknown" {
				rowCloseReason = derived
			}
		}

		res := s.db.Model(&TraderPosition{}).Where("id = ? AND status = ?", pos.ID, "OPEN").Updates(map[string]interface{}{
			"quantity":     0,
			"exit_price":   exitPrice,
			"exit_time":    nowMs,
			"realized_pnl": realizedPnl,
			"fee":          totalFee,
			"status":       "CLOSED",
			"close_reason": rowCloseReason,
			"updated_at":   nowMs,
		})
		if res.Error != nil {
			return updated, res.Error
		}
		updated += res.RowsAffected
	}
	return updated, nil
}

// computePnlFromFills attempts to calculate realized PnL from filled close orders
// associated with a position. Returns (pnl, closeFee, avgExitPrice, success).
func (s *PositionStore) computePnlFromFills(pos *TraderPosition) (float64, float64, float64, bool) {
	if pos == nil || pos.ID == 0 {
		return 0, 0, 0, false
	}
	entryQty := pos.EntryQuantity
	if entryQty <= 0 {
		entryQty = pos.Quantity
	}
	if entryQty <= 0 {
		return 0, 0, 0, false
	}

	// Look for close orders linked to this position
	var orders []TraderOrder
	closeSide := "SELL"
	if strings.EqualFold(pos.Side, "SHORT") {
		closeSide = "BUY"
	}
	err := s.db.Where("related_position_id = ? AND side = ? AND status = ? AND filled_quantity > 0",
		pos.ID, closeSide, "FILLED").Find(&orders).Error
	if err != nil || len(orders) == 0 {
		// Fallback: search by symbol+side+time window
		windowStart := pos.EntryTime
		err = s.db.Where("trader_id = ? AND symbol = ? AND side = ? AND status = ? AND filled_quantity > 0 AND created_at > ?",
			pos.TraderID, pos.Symbol, closeSide, "FILLED", windowStart).
			Order("created_at ASC").Find(&orders).Error
		if err != nil || len(orders) == 0 {
			return 0, 0, 0, false
		}
	}

	var totalCloseQty, totalCloseValue, totalCloseFee float64
	for _, o := range orders {
		qty := o.FilledQuantity
		if totalCloseQty+qty > entryQty*1.01 {
			break
		}
		price := o.AvgFillPrice
		if price <= 0 {
			price = o.Price
		}
		if price <= 0 {
			continue
		}
		totalCloseQty += qty
		totalCloseValue += qty * price
		totalCloseFee += o.Commission
	}

	if totalCloseQty <= 0 || totalCloseValue <= 0 {
		return 0, 0, 0, false
	}

	avgExit := totalCloseValue / totalCloseQty
	var pnl float64
	if strings.EqualFold(pos.Side, "LONG") {
		pnl = (avgExit - pos.EntryPrice) * totalCloseQty
	} else {
		pnl = (pos.EntryPrice - avgExit) * totalCloseQty
	}

	return pnl, totalCloseFee, avgExit, true
}

// dominantCloseOrderID returns the exchange_order_id of the closing order that
// filled the largest quantity for this position. Used to attribute a real close
// reason (protection tag) when a position is detected absent via exchange sync.
// Returns "" if no linked closing order is found.
func (s *PositionStore) dominantCloseOrderID(pos *TraderPosition) string {
	if pos == nil || pos.ID == 0 {
		return ""
	}
	closeSide := "SELL"
	if strings.EqualFold(pos.Side, "SHORT") {
		closeSide = "BUY"
	}
	var orders []TraderOrder
	err := s.db.Where("related_position_id = ? AND side = ? AND status = ? AND filled_quantity > 0",
		pos.ID, closeSide, "FILLED").Find(&orders).Error
	if err != nil || len(orders) == 0 {
		windowStart := pos.EntryTime
		err = s.db.Where("trader_id = ? AND symbol = ? AND side = ? AND status = ? AND filled_quantity > 0 AND created_at > ?",
			pos.TraderID, pos.Symbol, closeSide, "FILLED", windowStart).
			Order("created_at ASC").Find(&orders).Error
		if err != nil || len(orders) == 0 {
			return ""
		}
	}
	bestID := ""
	bestQty := 0.0
	for _, o := range orders {
		if o.FilledQuantity > bestQty && o.ExchangeOrderID != "" {
			bestQty = o.FilledQuantity
			bestID = o.ExchangeOrderID
		}
	}
	return bestID
}

func positionPresenceKey(symbol, side string) string {
	return strings.ToUpper(strings.TrimSpace(symbol)) + "|" + normalizePositionSideForStore(side)
}

func normalizePositionSideForStore(side string) string {
	side = strings.ToUpper(strings.TrimSpace(side))
	switch side {
	case "LONG", "BUY":
		return "LONG"
	case "SHORT", "SELL":
		return "SHORT"
	default:
		return side
	}
}

func quantitiesEquivalent(a, b float64) bool {
	return math.Abs(a-b) <= math.Max(0.00000001, math.Max(math.Abs(a), math.Abs(b))*0.0001)
}

// GetClosedTradesForEvolution fetches recent closed trades for a specific symbol+side,
// used by the evolution engine to compute factor scores.
func (s *PositionStore) GetClosedTradesForEvolution(traderID, symbol, side string, limit int) ([]*TraderPosition, error) {
	var positions []*TraderPosition
	normalizedSide := strings.ToUpper(side)
	err := s.db.Where("trader_id = ? AND symbol = ? AND side = ? AND status = ?",
		traderID, symbol, normalizedSide, "CLOSED").
		Order("exit_time DESC").
		Limit(limit).
		Find(&positions).Error
	if err != nil {
		return nil, fmt.Errorf("failed to query evolution trades: %w", err)
	}
	return positions, nil
}

// SideExposureBucket is one hourly snapshot of long vs short notional exposure
// (entry_price * quantity in USDT) used for the dashboard long/short ratio curve.
type SideExposureBucket struct {
	BucketMs    int64   `json:"bucket_ms"`
	LongNotion  float64 `json:"long_notion"`
	ShortNotion float64 `json:"short_notion"`
}

// GetSideExposureSeries reconstructs hourly long/short notional exposure over
// [sinceMs, now]. A position contributes entry_price*entry_quantity to its side
// in every bucket where it was open (entry_time <= bucket_end AND (still open OR
// exit_time >= bucket_start)). Entry notional is used because historical mark
// prices are not stored; this yields a stable exposure-ratio curve without
// external kline fetches. Buckets with no open positions are emitted as zeros so
// the frontend can render a continuous line.
func (s *PositionStore) GetSideExposureSeries(traderID string, sinceMs, bucketMs int64) ([]SideExposureBucket, error) {
	if bucketMs <= 0 {
		bucketMs = 3600000
	}
	nowMs := time.Now().UTC().UnixMilli()
	// Fetch positions overlapping the window: entry before now, and either still
	// open or closed at/after sinceMs.
	var positions []TraderPosition
	err := s.db.Model(&TraderPosition{}).
		Where("trader_id = ? AND entry_time <= ?", traderID, nowMs).
		Where("status = 'OPEN' OR exit_time = 0 OR exit_time >= ?", sinceMs).
		Find(&positions).Error
	if err != nil {
		return nil, fmt.Errorf("failed to query positions for exposure series: %w", err)
	}

	startBucket := (sinceMs / bucketMs) * bucketMs
	endBucket := (nowMs / bucketMs) * bucketMs
	out := make([]SideExposureBucket, 0, (endBucket-startBucket)/bucketMs+1)
	for b := startBucket; b <= endBucket; b += bucketMs {
		bucketEnd := b + bucketMs
		bucket := SideExposureBucket{BucketMs: b}
		for i := range positions {
			p := &positions[i]
			openByThen := p.EntryTime < bucketEnd
			if !openByThen {
				continue
			}
			closedBeforeBucket := p.Status != "OPEN" && p.ExitTime > 0 && p.ExitTime < b
			if closedBeforeBucket {
				continue
			}
			qty := p.EntryQuantity
			if qty <= 0 {
				qty = p.Quantity
			}
			notion := p.EntryPrice * qty
			if notion <= 0 {
				continue
			}
			if strings.EqualFold(p.Side, "LONG") {
				bucket.LongNotion += notion
			} else {
				bucket.ShortNotion += notion
			}
		}
		out = append(out, bucket)
	}
	return out, nil
}
