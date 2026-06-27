package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// MigrateUnifiedProtection 统一保护系统数据库迁移
func MigrateUnifiedProtection(db *sql.DB) error {
	migrations := []string{
		// 创建统一保护配置表
		`CREATE TABLE IF NOT EXISTS trader_protection_configs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			trader_id TEXT NOT NULL UNIQUE,
			config TEXT NOT NULL,
			version INTEGER DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		// 创建配置历史表（用于回滚）
		`CREATE TABLE IF NOT EXISTS trader_protection_config_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			trader_id TEXT NOT NULL,
			config TEXT NOT NULL,
			version INTEGER NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			reason TEXT DEFAULT ''
		)`,

		// 创建追踪止盈状态表
		`CREATE TABLE IF NOT EXISTS trailing_tp_states (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			trader_id TEXT NOT NULL,
			position_id TEXT NOT NULL,
			is_active INTEGER DEFAULT 0,
			highest_profit_r REAL DEFAULT 0,
			trailing_stop_r REAL DEFAULT 0,
			activated_at DATETIME,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(trader_id, position_id)
		)`,

		// 创建趋势转换日志表
		`CREATE TABLE IF NOT EXISTS trend_transition_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			trader_id TEXT NOT NULL,
			symbol TEXT NOT NULL,
			from_trend INTEGER NOT NULL,
			to_trend INTEGER NOT NULL,
			transition_level INTEGER NOT NULL,
			action_taken TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		// 创建峰值盈亏状态表（持久化 peakPnLCache，使其在重启后可恢复）
		// key 为 trader_id + pos_key(symbol_side)，与内存缓存键一致。
		`CREATE TABLE IF NOT EXISTS peak_pnl_states (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			trader_id TEXT NOT NULL,
			pos_key TEXT NOT NULL,
			peak_pnl_pct REAL DEFAULT 0,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(trader_id, pos_key)
		)`,

		// 创建回吐保护(GivebackGuard)状态表（持久化 L1/L2 ratchet 状态，使其在重启后可恢复）
		// 每个 trader 一行：组合浮盈高水位、L2 触发高水位、L1 各仓位触发高水位(JSON)。
		`CREATE TABLE IF NOT EXISTS giveback_guard_states (
			trader_id TEXT PRIMARY KEY,
			portfolio_peak_unreal REAL DEFAULT 0,
			l2_fired_at_peak REAL DEFAULT 0,
			l1_fired_at_peak_json TEXT DEFAULT '{}',
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		// 广度熔断速度历史：持久化 gbPnlHist 与 bar 时钟，使重启后速度路不必再
		// 经历 2~6h 的 warm-up。加载时带过期保护：超过 N 根 bar 的快照直接丢弃，
		// 回到空历史(fail-safe)，避免跨越宕机时间空洞计算出错误速度。
		`CREATE TABLE IF NOT EXISTS breadth_velocity_states (
			trader_id TEXT PRIMARY KEY,
			pnl_hist_json TEXT DEFAULT '{}',
			last_bar_ms INTEGER DEFAULT 0,
			bars_since_fire INTEGER DEFAULT 0,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		// 添加索引
		`CREATE INDEX IF NOT EXISTS idx_protection_configs_trader
		 ON trader_protection_configs(trader_id)`,

		`CREATE INDEX IF NOT EXISTS idx_protection_history_trader_version
		 ON trader_protection_config_history(trader_id, version)`,

		`CREATE INDEX IF NOT EXISTS idx_trailing_states_trader_position
		 ON trailing_tp_states(trader_id, position_id)`,

		`CREATE INDEX IF NOT EXISTS idx_trend_logs_trader_symbol
		 ON trend_transition_logs(trader_id, symbol, created_at)`,

		`CREATE INDEX IF NOT EXISTS idx_peak_pnl_states_trader
		 ON peak_pnl_states(trader_id)`,
	}

	for i, migration := range migrations {
		if _, err := db.Exec(migration); err != nil {
			return fmt.Errorf("migration %d failed: %w", i+1, err)
		}
	}

	// L3 equity circuit-breaker ratchet columns. SQLite has no ADD COLUMN IF NOT
	// EXISTS, so add each column only when PRAGMA table_info shows it missing —
	// idempotent across restarts and safe on older DBs that predate L3.
	for _, col := range []struct{ name, ddl string }{
		{"equity_peak", "ALTER TABLE giveback_guard_states ADD COLUMN equity_peak REAL DEFAULT 0"},
		{"l3_fired_at_peak", "ALTER TABLE giveback_guard_states ADD COLUMN l3_fired_at_peak REAL DEFAULT 0"},
		{"l3_tier_fired_json", "ALTER TABLE giveback_guard_states ADD COLUMN l3_tier_fired_json TEXT DEFAULT '[]'"},
		// Rolling-baseline mode persistence: the rolling reference equity and the
		// post-cut re-arm floor must survive restarts, or a restart mid-drawdown
		// would reset protection to the (already-depressed) current equity.
		{"roll_ref", "ALTER TABLE giveback_guard_states ADD COLUMN roll_ref REAL DEFAULT 0"},
		{"roll_arm_floor", "ALTER TABLE giveback_guard_states ADD COLUMN roll_arm_floor REAL DEFAULT 0"},
	} {
		if !columnExists(db, "giveback_guard_states", col.name) {
			if _, err := db.Exec(col.ddl); err != nil {
				return fmt.Errorf("add column %s failed: %w", col.name, err)
			}
		}
	}

	return nil
}

// columnExists reports whether table has a column with the given name, via
// PRAGMA table_info. Used to make ALTER TABLE ADD COLUMN idempotent on SQLite.
func columnExists(db *sql.DB, table, column string) bool {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid       int
			name, typ string
			notnull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dfltValue, &pk); err != nil {
			return false
		}
		if name == column {
			return true
		}
	}
	return false
}

// SaveUnifiedProtectionConfig 保存统一保护配置
func (s *Store) SaveUnifiedProtectionConfig(traderID string, config *UnifiedProtectionConfig) error {
	configJSON, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("marshal config failed: %w", err)
	}

	// 获取当前版本
	var currentVersion int
	err = s.db.QueryRow(`SELECT version FROM trader_protection_configs WHERE trader_id = ?`, traderID).Scan(&currentVersion)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	newVersion := currentVersion + 1

	// 如果存在旧配置，先保存到历史
	if currentVersion > 0 {
		var oldConfig string
		err = s.db.QueryRow(`SELECT config FROM trader_protection_configs WHERE trader_id = ?`, traderID).Scan(&oldConfig)
		if err == nil {
			_, err = s.db.Exec(`INSERT INTO trader_protection_config_history (trader_id, config, version) VALUES (?, ?, ?)`,
				traderID, oldConfig, currentVersion)
			if err != nil {
				return fmt.Errorf("save history failed: %w", err)
			}
		}
	}

	// 保存新配置
	_, err = s.db.Exec(`
		INSERT INTO trader_protection_configs (trader_id, config, version, updated_at)
		VALUES (?, ?, ?, datetime('now'))
		ON CONFLICT(trader_id) DO UPDATE SET
			config = excluded.config,
			version = excluded.version,
			updated_at = datetime('now')
	`, traderID, string(configJSON), newVersion)

	return err
}

// GetUnifiedProtectionConfig 获取统一保护配置
func (s *Store) GetUnifiedProtectionConfig(traderID string) (*UnifiedProtectionConfig, error) {
	var configJSON string
	err := s.db.QueryRow(`SELECT config FROM trader_protection_configs WHERE trader_id = ?`, traderID).Scan(&configJSON)
	if err == sql.ErrNoRows {
		// 返回默认配置
		defaultConfig := DefaultUnifiedProtectionConfig()
		return &defaultConfig, nil
	}
	if err != nil {
		return nil, err
	}

	var config UnifiedProtectionConfig
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return nil, fmt.Errorf("unmarshal config failed: %w", err)
	}

	return &config, nil
}

// GetExplicitUnifiedProtectionConfig 仅在数据库中存在显式配置行时返回配置。
// 返回 (config, exists, error)。exists=false 表示该交易员从未保存过统一保护配置，
// 调用方应保持原有行为不变（绝不能用默认 R 模式覆盖未配置的交易员）。
func (s *Store) GetExplicitUnifiedProtectionConfig(traderID string) (*UnifiedProtectionConfig, bool, error) {
	var configJSON string
	err := s.db.QueryRow(`SELECT config FROM trader_protection_configs WHERE trader_id = ?`, traderID).Scan(&configJSON)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	var config UnifiedProtectionConfig
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return nil, false, fmt.Errorf("unmarshal config failed: %w", err)
	}

	return &config, true, nil
}

// GetTrailingTPState 读取某仓位的追踪止盈状态。返回 (state, exists, error)。
func (s *Store) GetTrailingTPState(traderID, positionID string) (isActive bool, highestProfitR, trailingStopR float64, exists bool, err error) {
	row := s.db.QueryRow(`SELECT is_active, highest_profit_r, trailing_stop_r FROM trailing_tp_states WHERE trader_id = ? AND position_id = ?`, traderID, positionID)
	err = row.Scan(&isActive, &highestProfitR, &trailingStopR)
	if err == sql.ErrNoRows {
		return false, 0, 0, false, nil
	}
	if err != nil {
		return false, 0, 0, false, err
	}
	return isActive, highestProfitR, trailingStopR, true, nil
}

// SaveTrailingTPState 保存追踪止盈状态
func (s *Store) SaveTrailingTPState(traderID, positionID string, isActive bool, highestProfitR, trailingStopR float64) error {
	_, err := s.db.Exec(`
		INSERT INTO trailing_tp_states (trader_id, position_id, is_active, highest_profit_r, trailing_stop_r, activated_at, updated_at)
		VALUES (?, ?, ?, ?, ?, datetime('now'), datetime('now'))
		ON CONFLICT(trader_id, position_id) DO UPDATE SET
			is_active = excluded.is_active,
			highest_profit_r = excluded.highest_profit_r,
			trailing_stop_r = excluded.trailing_stop_r,
			updated_at = datetime('now')
	`, traderID, positionID, isActive, highestProfitR, trailingStopR)

	return err
}

// SavePeakPnL 持久化某仓位的峰值盈亏百分比（high-water）。
// posKey 与内存缓存键一致（symbol_side）。
func (s *Store) SavePeakPnL(traderID, posKey string, peakPnLPct float64) error {
	_, err := s.db.Exec(`
		INSERT INTO peak_pnl_states (trader_id, pos_key, peak_pnl_pct, updated_at)
		VALUES (?, ?, ?, datetime('now'))
		ON CONFLICT(trader_id, pos_key) DO UPDATE SET
			peak_pnl_pct = excluded.peak_pnl_pct,
			updated_at = datetime('now')
	`, traderID, posKey, peakPnLPct)

	return err
}

// DeletePeakPnL 删除某仓位的峰值盈亏记录（平仓后清理）。
func (s *Store) DeletePeakPnL(traderID, posKey string) error {
	_, err := s.db.Exec(`DELETE FROM peak_pnl_states WHERE trader_id = ? AND pos_key = ?`, traderID, posKey)
	return err
}

// LoadPeakPnLForTrader 加载某 trader 的全部峰值盈亏记录，用于启动时恢复内存缓存。
func (s *Store) LoadPeakPnLForTrader(traderID string) (map[string]float64, error) {
	rows, err := s.db.Query(`SELECT pos_key, peak_pnl_pct FROM peak_pnl_states WHERE trader_id = ?`, traderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]float64)
	for rows.Next() {
		var posKey string
		var peak float64
		if scanErr := rows.Scan(&posKey, &peak); scanErr != nil {
			return nil, scanErr
		}
		result[posKey] = peak
	}
	return result, rows.Err()
}

// BreadthVelocityState 是广度熔断速度路的可持久化快照。
type BreadthVelocityState struct {
	PnlHist       map[string][]float64 // symbol_side -> 最近的 profit% 采样序列
	LastBarMs     int64                // 上次推进速度 bar 的毫秒时间戳
	BarsSinceFire int                  // 距上次熔断触发的 bar 数（冷却计数）
}

// SaveBreadthVelocityState 持久化广度熔断的速度历史与 bar 时钟。每次 bar 推进后
// 调用，使速度路在重启后无需重新经历 warm-up。
func (s *Store) SaveBreadthVelocityState(traderID string, state BreadthVelocityState) error {
	histJSON := "{}"
	if len(state.PnlHist) > 0 {
		b, err := json.Marshal(state.PnlHist)
		if err != nil {
			return fmt.Errorf("marshal breadth pnl-hist failed: %w", err)
		}
		histJSON = string(b)
	}
	_, err := s.db.Exec(`
		INSERT INTO breadth_velocity_states (trader_id, pnl_hist_json, last_bar_ms, bars_since_fire, updated_at)
		VALUES (?, ?, ?, ?, datetime('now'))
		ON CONFLICT(trader_id) DO UPDATE SET
			pnl_hist_json = excluded.pnl_hist_json,
			last_bar_ms = excluded.last_bar_ms,
			bars_since_fire = excluded.bars_since_fire,
			updated_at = datetime('now')
	`, traderID, histJSON, state.LastBarMs, state.BarsSinceFire)
	return err
}

// LoadBreadthVelocityState 加载广度熔断速度状态。无记录时返回空状态（空 hist），
// 等价于全新 warm-up。过期判定由调用方按 last_bar_ms 决定（见 trader 层）。
func (s *Store) LoadBreadthVelocityState(traderID string) (BreadthVelocityState, error) {
	state := BreadthVelocityState{PnlHist: make(map[string][]float64)}
	var histJSON string
	err := s.db.QueryRow(`
		SELECT pnl_hist_json, last_bar_ms, bars_since_fire
		FROM breadth_velocity_states WHERE trader_id = ?
	`, traderID).Scan(&histJSON, &state.LastBarMs, &state.BarsSinceFire)
	if err == sql.ErrNoRows {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if histJSON != "" && histJSON != "{}" {
		if uErr := json.Unmarshal([]byte(histJSON), &state.PnlHist); uErr != nil {
			return state, fmt.Errorf("unmarshal breadth pnl-hist failed: %w", uErr)
		}
	}
	return state, nil
}

// LogTrendTransition 记录趋势转换
func (s *Store) LogTrendTransition(traderID, symbol string, fromTrend, toTrend, transitionLevel int, actionTaken string) error {
	_, err := s.db.Exec(`
		INSERT INTO trend_transition_logs (trader_id, symbol, from_trend, to_trend, transition_level, action_taken, created_at)
		VALUES (?, ?, ?, ?, ?, ?, datetime('now'))
	`, traderID, symbol, fromTrend, toTrend, transitionLevel, actionTaken)

	return err
}
