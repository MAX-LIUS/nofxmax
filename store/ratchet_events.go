package store

import (
	"encoding/json"
	"fmt"
)

// RatchetEventsConfigKey is the system_config key under which the per-position
// structural-stop ratchet-tighten event LOG is persisted. The frozen-ATR record
// (frozen_atr_state.go) only keeps the CURRENT trail boundary + a running count,
// overwriting on each tighten — it cannot answer "how did the stop walk in?".
// This log appends one row per genuine tighten so the position-history panel can
// show every ratchet step (distance %, ATR multiple, price at event). Keyed the
// same way as FrozenATRRecord (trader+symbol+tf+side) so the two stay aligned;
// the slice is frozen onto the closed position row at close, then deleted here.
const RatchetEventsConfigKey = "ratchet_events_v1"

// RatchetEvent is one genuine structural-stop tighten. Emitted from the single
// authoritative tighten site (structural_sl_guard.go), it is a pure side-record:
// writing it never influences the boundary/close logic.
type RatchetEvent struct {
	TraderID     string  `json:"trader_id"`
	Symbol       string  `json:"symbol"`
	Side         string  `json:"side"` // "LONG" / "SHORT"
	EntryPrice   float64 `json:"entry_price"`
	Seq          int     `json:"seq"`           // ratchet number (1-based, == TrailRatchets after this step)
	Boundary     float64 `json:"boundary"`      // new (tighter) close-confirm boundary
	PrevBoundary float64 `json:"prev_boundary"` // boundary before this tighten
	PriceAtEvent float64 `json:"price_at_event"` // just-closed bar close that triggered the recompute
	DistPct      float64 `json:"dist_pct"`      // |boundary-price|/price * 100 — how far the stop sits from price
	AtrMult      float64 `json:"atr_mult"`      // |boundary-price|/atr — same distance in frozen-ATR units
	Timestamp    int64   `json:"timestamp"`     // unix seconds
}

// RatchetEventsState is the persisted map: key -> ordered tighten log.
type RatchetEventsState struct {
	Events map[string][]RatchetEvent `json:"events"`
}

// LoadRatchetEventsState returns the full persisted map (never nil).
func (s *Store) LoadRatchetEventsState() (*RatchetEventsState, error) {
	raw, err := s.GetSystemConfig(RatchetEventsConfigKey)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return &RatchetEventsState{Events: map[string][]RatchetEvent{}}, nil
	}
	var state RatchetEventsState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, fmt.Errorf("failed to decode ratchet events state: %w", err)
	}
	if state.Events == nil {
		state.Events = map[string][]RatchetEvent{}
	}
	return &state, nil
}

// LoadRatchetEvents returns the ordered tighten log for one key (nil when none).
func (s *Store) LoadRatchetEvents(key string) ([]RatchetEvent, error) {
	state, err := s.LoadRatchetEventsState()
	if err != nil {
		return nil, err
	}
	return state.Events[key], nil
}

// AppendRatchetEvent appends one tighten to the key's log. Best-effort by design:
// callers on the trading hot path should log-and-continue on error, never block a
// stop update on the side-record write.
func (s *Store) AppendRatchetEvent(key string, ev RatchetEvent) error {
	state, err := s.LoadRatchetEventsState()
	if err != nil {
		return err
	}
	state.Events[key] = append(state.Events[key], ev)
	return s.saveRatchetEventsState(state)
}

// DeleteRatchetEvents removes a key's log (called at close, after the log has been
// frozen onto the closed position row).
func (s *Store) DeleteRatchetEvents(key string) error {
	state, err := s.LoadRatchetEventsState()
	if err != nil {
		return err
	}
	if _, ok := state.Events[key]; !ok {
		return nil
	}
	delete(state.Events, key)
	return s.saveRatchetEventsState(state)
}

func (s *Store) saveRatchetEventsState(state *RatchetEventsState) error {
	if state == nil {
		state = &RatchetEventsState{Events: map[string][]RatchetEvent{}}
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to encode ratchet events state: %w", err)
	}
	return s.SetSystemConfig(RatchetEventsConfigKey, string(raw))
}
