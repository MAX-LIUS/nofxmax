package trader

import (
	"testing"
	"time"
)

// mustTime parses a wall-clock time in UTC for the tests (crypto klines are
// UTC-aligned, and Truncate works on absolute epoch time).
func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		t.Fatalf("bad time %q: %v", s, err)
	}
	return tm.UTC()
}

func TestAlignedFirstRunAt(t *testing.T) {
	cases := []struct {
		name     string
		now      string
		interval time.Duration
		slot     int
		want     string
	}{
		// 20m interval, start 15:05 → next boundary 15:20 + 60s settle, slot 0
		{"20m slot0", "2026-07-25 15:05:00", 20 * time.Minute, 0, "2026-07-25 15:21:00"},
		// same but slot 1 (+15s) and slot 2 (+30s)
		{"20m slot1", "2026-07-25 15:05:00", 20 * time.Minute, 1, "2026-07-25 15:21:15"},
		{"20m slot2", "2026-07-25 15:05:00", 20 * time.Minute, 2, "2026-07-25 15:21:30"},
		// 15m interval, start 15:05 → next boundary 15:15 + 60s, slot 0
		{"15m slot0", "2026-07-25 15:05:00", 15 * time.Minute, 0, "2026-07-25 15:16:00"},
		// exactly on a boundary → must roll to the NEXT one (not fire "now"),
		// then add the 60s settle delay.
		{"on boundary rolls fwd", "2026-07-25 15:20:00", 20 * time.Minute, 0, "2026-07-25 15:41:00"},
		// LCM overlap: at the top of the hour a 15m and 20m trader share the
		// boundary 16:00; global stagger slots keep them apart.
		{"lcm 15m slot0", "2026-07-25 15:50:00", 15 * time.Minute, 0, "2026-07-25 16:01:00"},
		{"lcm 20m slot1", "2026-07-25 15:50:00", 20 * time.Minute, 1, "2026-07-25 16:01:15"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := alignedFirstRunAt(mustTime(t, c.now), c.interval, c.slot)
			want := mustTime(t, c.want)
			if !got.Equal(want) {
				t.Errorf("alignedFirstRunAt(%s, %v, %d) = %s, want %s",
					c.now, c.interval, c.slot, got.Format("15:04:05"), want.Format("15:04:05"))
			}
		})
	}
}

// Intervals below the min-align threshold fire immediately (no alignment).
func TestAlignedFirstRunAt_TooSmall(t *testing.T) {
	now := mustTime(t, "2026-07-25 15:05:07")
	for _, iv := range []time.Duration{0, 30 * time.Second, time.Minute} {
		got := alignedFirstRunAt(now, iv, 3)
		if !got.Equal(now) {
			t.Errorf("interval %v: expected immediate (now=%s), got %s", iv, now, got)
		}
	}
}
