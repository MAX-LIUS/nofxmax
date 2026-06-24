package trader

import (
	"testing"
	"time"
)

func TestRestoreCooldownFutureArmsCooldown(t *testing.T) {
	m := newEntryCooldownManager()
	m.duration = 90 * time.Minute

	ok := m.RestoreCooldown("BTCUSDT", time.Now().Add(30*time.Minute))
	if !ok {
		t.Fatalf("expected future cooldown to be armed")
	}
	cooling, remaining := m.IsCoolingDown("BTCUSDT")
	if !cooling {
		t.Fatalf("expected BTCUSDT to be cooling down")
	}
	if remaining <= 0 || remaining > 30*time.Minute {
		t.Fatalf("unexpected remaining: %v", remaining)
	}
}

func TestRestoreCooldownPastIsIgnored(t *testing.T) {
	m := newEntryCooldownManager()

	ok := m.RestoreCooldown("ETHUSDT", time.Now().Add(-5*time.Minute))
	if ok {
		t.Fatalf("expected past expiry to be ignored")
	}
	if cooling, _ := m.IsCoolingDown("ETHUSDT"); cooling {
		t.Fatalf("expired cooldown must not be armed")
	}
}
