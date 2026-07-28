package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const DynamicProtectionStateConfigKey = "dynamic_protection_state_v1"

type DynamicProtectionRecord struct {
	Key                 string  `json:"key"`
	TraderID            string  `json:"trader_id"`
	ExchangeID          string  `json:"exchange_id"`
	Symbol              string  `json:"symbol"`
	Side                string  `json:"side"`
	PositionFingerprint string  `json:"position_fingerprint"`
	PositionCreatedTime int64   `json:"position_created_time,omitempty"`
	ProtectionType      string  `json:"protection_type"`
	RuleFingerprint     string  `json:"rule_fingerprint"`
	CloseRatioPct       float64 `json:"close_ratio_pct"`
	Status              string  `json:"status"`
	ExchangeOrderID     string  `json:"exchange_order_id,omitempty"`
	ActivationPrice     float64 `json:"activation_price,omitempty"`
	TriggerPrice        float64 `json:"trigger_price,omitempty"`
	StopPrice           float64 `json:"stop_price,omitempty"`
	CallbackRatio       float64 `json:"callback_ratio,omitempty"`
	Quantity            float64 `json:"quantity,omitempty"`
	UpdatedAt           int64   `json:"updated_at"`
}

type DynamicProtectionState struct {
	Records map[string]DynamicProtectionRecord `json:"records"`
}

func BuildDynamicProtectionKey(traderID, exchangeID, symbol, side, positionFingerprint, protectionType, ruleFingerprint string, closeRatioPct float64) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%.4f", traderID, exchangeID, symbol, side, positionFingerprint, protectionType, ruleFingerprint, closeRatioPct)
}

func (s *Store) LoadDynamicProtectionState() (*DynamicProtectionState, error) {
	raw, err := s.GetSystemConfig(DynamicProtectionStateConfigKey)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return &DynamicProtectionState{Records: map[string]DynamicProtectionRecord{}}, nil
	}
	var state DynamicProtectionState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, fmt.Errorf("failed to decode dynamic protection state: %w", err)
	}
	if state.Records == nil {
		state.Records = map[string]DynamicProtectionRecord{}
	}
	return &state, nil
}

func (s *Store) SaveDynamicProtectionRecord(record DynamicProtectionRecord) error {
	state, err := s.LoadDynamicProtectionState()
	if err != nil {
		return err
	}
	if record.UpdatedAt == 0 {
		record.UpdatedAt = time.Now().UTC().UnixMilli()
	}
	if record.Key == "" {
		record.Key = BuildDynamicProtectionKey(record.TraderID, record.ExchangeID, record.Symbol, record.Side, record.PositionFingerprint, record.ProtectionType, record.RuleFingerprint, record.CloseRatioPct)
	}
	for key, existing := range state.Records {
		if key == record.Key {
			continue
		}
		if existing.TraderID != record.TraderID || existing.ExchangeID != record.ExchangeID || existing.Symbol != record.Symbol || existing.Side != record.Side || existing.Status != "armed" {
			continue
		}
		// Dynamic native protections are singleton owners per active symbol/side.
		// When a newer arm succeeds, older persisted owners in the same singleton group
		// must not be restored on restart.
		if record.Status == "armed" && singletonDynamicProtectionGroupForRecord(record) != "" && singletonDynamicProtectionGroupForRecord(record) == singletonDynamicProtectionGroupForRecord(existing) {
			existing.Status = "replaced"
			existing.UpdatedAt = record.UpdatedAt
			state.Records[key] = existing
		}
	}
	state.Records[record.Key] = record
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to encode dynamic protection state: %w", err)
	}
	return s.SetSystemConfig(DynamicProtectionStateConfigKey, string(data))
}

// SaveDynamicProtectionRecordByKey 原地更新一条已存在的记录,不走 SaveDynamicProtectionRecord
// 的独占降级逻辑。用于状态迁移(例如逐档替换把旧档记录标成 replaced):那里已经知道
// 要改哪一条,再跑一遍独占降级只会牵连无关记录。key 不存在时不创建,返回 nil。
func (s *Store) SaveDynamicProtectionRecordByKey(key string, record DynamicProtectionRecord) error {
	if key == "" {
		return nil
	}
	state, err := s.LoadDynamicProtectionState()
	if err != nil {
		return err
	}
	if _, ok := state.Records[key]; !ok {
		return nil
	}
	record.Key = key
	record.UpdatedAt = time.Now().UTC().UnixMilli()
	state.Records[key] = record
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to encode dynamic protection state: %w", err)
	}
	return s.SetSystemConfig(DynamicProtectionStateConfigKey, string(data))
}

func singletonDynamicProtectionGroup(protectionType string) string {
	switch protectionType {
	case "break_even_stop":
		return "break_even_stop"
	default:
		return ""
	}
}

// singletonDynamicProtectionGroupForRecord 把"独占组"细化到档位。
//
// 原来独占组只按 protectionType 分,即"一个 symbol/side 上只能有一条 armed 的
// break_even_stop 记录"。这是 BE 只有一档时代的假设。配成两档后,BE2 一 arm 就把
// BE1 降级成 replaced —— 而 BE1 的单在交易所上还活着。后果:
//   - 认领集合(只收 armed)永远只认到一张,分类器把另一张判成 stale 重复单;
//   - 档位数额度(按 armed 记录里的 stage 去重)永远只数到 1,兜底也救不回来;
//   - 重启后只恢复一档的所有权,另一档变成无主单。
//
// 线上实证:22 条 BE 记录里 20 条 replaced、只有 2 条 armed(ETHUSDT long 与
// SKHYNIXUSDT short 各一条),而这两个仓位当时 BE1/BE2 都已挂上并在场。
//
// 档位不是彼此的替代品,是并存的两张保护单,各自独占自己那一档。所以独占键要带 stage。
// stage 取不到时退回按 protectionType 分组(即改造前语义),绝不猜 —— 猜错会把两档
// 并成一组,重新引发上面的降级。
func singletonDynamicProtectionGroupForRecord(record DynamicProtectionRecord) string {
	group := singletonDynamicProtectionGroup(record.ProtectionType)
	if group == "" {
		return ""
	}
	if stage := dynamicProtectionStageFromFingerprint(record.RuleFingerprint); stage != "" {
		return group + "|" + stage
	}
	return group
}

// dynamicProtectionStageFromFingerprint 取 BE fingerprint 的档位名。
// 格式(见 applyBreakEvenStop 的持久化):entry|qty|trigger|offset|stage。
func dynamicProtectionStageFromFingerprint(fingerprint string) string {
	parts := strings.Split(fingerprint, "|")
	if len(parts) < 5 {
		return ""
	}
	return strings.TrimSpace(parts[4])
}

func (s *Store) DeleteDynamicProtectionRecordsForInactive(traderID string, activeKeys map[string]struct{}) error {
	state, err := s.LoadDynamicProtectionState()
	if err != nil {
		return err
	}
	changed := false
	for key, record := range state.Records {
		if traderID != "" && record.TraderID != traderID {
			continue
		}
		positionKey := record.Symbol + "_" + record.Side
		if _, ok := activeKeys[positionKey]; ok {
			continue
		}
		delete(state.Records, key)
		changed = true
	}
	if !changed {
		return nil
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to encode dynamic protection state: %w", err)
	}
	return s.SetSystemConfig(DynamicProtectionStateConfigKey, string(data))
}
