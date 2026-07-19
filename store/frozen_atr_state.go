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
	// StructuralBoundary is the pre-entry range boundary price frozen at open (swing
	// low for a long, swing high for a short) used by the structural stop-loss. 0 when
	// the position does not use structural SL. Frozen because it cannot be recomputed
	// after the fact — GetKlines returns the latest bars, not the pre-entry window.
	StructuralBoundary float64 `json:"structural_boundary,omitempty"`
	// TrailBoundary is the current ratcheted close-confirm boundary for the trailing
	// structural stop. It starts at StructuralBoundary and only tightens (never loosens)
	// as the guard recomputes nearest structure per closed bar. Persisted so the ratchet
	// survives a restart instead of resetting to the entry boundary. 0 when the trail
	// has not armed yet (falls back to StructuralBoundary).
	TrailBoundary float64 `json:"trail_boundary,omitempty"`
	// TrailRatchets counts how many times TrailBoundary has tightened, enforced against
	// TrailMaxRatchets. Persisted alongside the boundary.
	TrailRatchets int `json:"trail_ratchets,omitempty"`
	// BackupBoundary is the ONE-STEP-BEHIND structural level kept as a physical intrabar
	// backstop when the ratchet tightens (rolling 2-level design). TrailBoundary is the
	// newest (tightest) level enforced by the close-confirm software guard; BackupBoundary
	// is the previous ratchet level (or the entry boundary right after the first ratchet)
	// held as a resting exchange stop so a violent candle that blows past the close-confirm
	// level intrabar is still caught before the wide 4.5-ATR backstop. Only the two most
	// recent levels are ever kept: on each new ratchet the old TrailBoundary becomes the
	// BackupBoundary and any older level is dropped. 0 = no backup layer yet (pre-first-ratchet).
	BackupBoundary float64 `json:"backup_boundary,omitempty"`
	// BackupOrderID is the exchange order/algo ID of the resting intrabar stop currently
	// placed at BackupBoundary. The structural guard OWNS this order: on each ratchet roll
	// it cancels this ID (the superseded backup) before placing the new one, so exactly one
	// backup order exists at a time. Empty when no backup order is live.
	BackupOrderID string `json:"backup_order_id,omitempty"`
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
