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

	return nil
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

// LogTrendTransition 记录趋势转换
func (s *Store) LogTrendTransition(traderID, symbol string, fromTrend, toTrend, transitionLevel int, actionTaken string) error {
	_, err := s.db.Exec(`
		INSERT INTO trend_transition_logs (trader_id, symbol, from_trend, to_trend, transition_level, action_taken, created_at)
		VALUES (?, ?, ?, ?, ?, ?, datetime('now'))
	`, traderID, symbol, fromTrend, toTrend, transitionLevel, actionTaken)

	return err
}
