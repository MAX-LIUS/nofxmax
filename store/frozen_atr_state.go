package store

import (
	"encoding/json"
	"fmt"
)

// FrozenATRStateConfigKey is the system_config key under which per-position
// frozen ATR values are persisted. ATR-unit protection rules resolve their
// distances using the ATR captured at position open; persisting that value lets
// the resolved activation/callback survive a process restart instead of being
// recomputed against a later (drifted) ATR. Percent-unit rules need no freezing
// (panel percent maps directly to the exchange callback), so only ATR-mode
// positions are recorded here.
const FrozenATRStateConfigKey = "frozen_atr_state_v1"

// FrozenATRRecord is the open-time ATR frozen for one position, keyed by
// trader+symbol with the entry price guarding against stale reuse on a new
// position for the same symbol.
type FrozenATRRecord struct {
	TraderID   string  `json:"trader_id"`
	Symbol     string  `json:"symbol"`
	EntryPrice float64 `json:"entry_price"`
	ATR        float64 `json:"atr"`
	UpdatedAt  int64   `json:"updated_at"`
}

type FrozenATRState struct {
	Records map[string]FrozenATRRecord `json:"records"`
}

// LoadFrozenATRState returns the persisted frozen-ATR map (never nil).
func (s *Store) LoadFrozenATRState() (*FrozenATRState, error) {
	raw, err := s.GetSystemConfig(FrozenATRStateConfigKey)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return &FrozenATRState{Records: map[string]FrozenATRRecord{}}, nil
	}
	var state FrozenATRState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, fmt.Errorf("failed to decode frozen ATR state: %w", err)
	}
	if state.Records == nil {
		state.Records = map[string]FrozenATRRecord{}
	}
	return &state, nil
}

// SaveFrozenATRRecord upserts a single frozen-ATR record by key.
func (s *Store) SaveFrozenATRRecord(key string, record FrozenATRRecord) error {
	state, err := s.LoadFrozenATRState()
	if err != nil {
		return err
	}
	state.Records[key] = record
	return s.saveFrozenATRState(state)
}

// DeleteFrozenATRRecord removes a frozen-ATR record (called when a position
// closes so a future position on the same symbol re-freezes cleanly).
func (s *Store) DeleteFrozenATRRecord(key string) error {
	state, err := s.LoadFrozenATRState()
	if err != nil {
		return err
	}
	if _, ok := state.Records[key]; !ok {
		return nil
	}
	delete(state.Records, key)
	return s.saveFrozenATRState(state)
}

func (s *Store) saveFrozenATRState(state *FrozenATRState) error {
	if state == nil {
		state = &FrozenATRState{Records: map[string]FrozenATRRecord{}}
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to encode frozen ATR state: %w", err)
	}
	return s.SetSystemConfig(FrozenATRStateConfigKey, string(raw))
}
