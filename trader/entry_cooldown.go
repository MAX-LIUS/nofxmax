package trader

import (
	"sync"
	"time"
)

const defaultEntryCooldownDuration = 90 * time.Minute

type entryCooldownManager struct {
	mu        sync.Mutex
	cooldowns map[string]time.Time
	duration  time.Duration
}

func newEntryCooldownManager() *entryCooldownManager {
	return &entryCooldownManager{
		cooldowns: make(map[string]time.Time),
		duration:  defaultEntryCooldownDuration,
	}
}

func newEntryCooldownManagerFromConfig(config AutoTraderConfig) *entryCooldownManager {
	m := newEntryCooldownManager()
	if config.StrategyConfig != nil && config.StrategyConfig.RiskControl.EntryCooldownMinutes > 0 {
		m.duration = time.Duration(config.StrategyConfig.RiskControl.EntryCooldownMinutes) * time.Minute
	}
	return m
}

func (m *entryCooldownManager) SetDuration(minutes int) {
	if minutes > 0 {
		m.duration = time.Duration(minutes) * time.Minute
	}
}

func (m *entryCooldownManager) SetCooldown(symbol string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cooldowns[symbol] = time.Now().Add(m.duration)
}

// SetCooldownWithMultiplier sets cooldown with a duration multiplier for consecutive losses.
func (m *entryCooldownManager) SetCooldownWithMultiplier(symbol string, multiplier int) {
	if multiplier < 1 {
		multiplier = 1
	}
	if multiplier > 4 {
		multiplier = 4
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cooldowns[symbol] = time.Now().Add(m.duration * time.Duration(multiplier))
}

func (m *entryCooldownManager) IsCoolingDown(symbol string) (bool, time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	until, ok := m.cooldowns[symbol]
	if !ok {
		return false, 0
	}
	remaining := time.Until(until)
	if remaining <= 0 {
		delete(m.cooldowns, symbol)
		return false, 0
	}
	return true, remaining
}

func (m *entryCooldownManager) Clear(symbol string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cooldowns, symbol)
}

func (m *entryCooldownManager) Cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for symbol, until := range m.cooldowns {
		if now.After(until) {
			delete(m.cooldowns, symbol)
		}
	}
}
