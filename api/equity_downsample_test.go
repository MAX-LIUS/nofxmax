package api

import (
	"math"
	"testing"
	"time"

	"nofx/store"
)

func mkSnaps(equities []float64) []*store.EquitySnapshot {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	out := make([]*store.EquitySnapshot, len(equities))
	for i, e := range equities {
		out[i] = &store.EquitySnapshot{
			Timestamp:   base.Add(time.Duration(i) * 3 * time.Minute),
			TotalEquity: e,
		}
	}
	return out
}

func TestDownsampleLeavesShortSeriesUntouched(t *testing.T) {
	snaps := mkSnaps([]float64{100, 101, 102})
	got := downsampleSnapshots(snaps, 1500)
	if len(got) != 3 {
		t.Fatalf("want 3 samples untouched, got %d", len(got))
	}
}

func TestDownsampleRespectsBudget(t *testing.T) {
	eq := make([]float64, 5000)
	for i := range eq {
		eq[i] = 100 + math.Sin(float64(i)/50)*10
	}
	got := downsampleSnapshots(mkSnaps(eq), 500)
	// Budget plus the two pinned endpoints.
	if len(got) > 502 {
		t.Fatalf("budget 500 exceeded: got %d", len(got))
	}
	if len(got) < 400 {
		t.Fatalf("downsampled too aggressively: got %d", len(got))
	}
}

// The reason for min/max bucketing rather than stride sampling: a stride can step
// over a spike and erase it, which would make the same curve report a different
// maximum drawdown at different zoom levels.
func TestDownsamplePreservesGlobalExtremes(t *testing.T) {
	eq := make([]float64, 4000)
	for i := range eq {
		eq[i] = 100
	}
	eq[1234] = 380 // lone peak
	eq[2777] = 12  // lone trough
	got := downsampleSnapshots(mkSnaps(eq), 200)

	var maxSeen, minSeen float64 = -1, math.MaxFloat64
	for _, s := range got {
		if s.TotalEquity > maxSeen {
			maxSeen = s.TotalEquity
		}
		if s.TotalEquity < minSeen {
			minSeen = s.TotalEquity
		}
	}
	if maxSeen != 380 {
		t.Errorf("peak lost by downsampling: want 380, got %v", maxSeen)
	}
	if minSeen != 12 {
		t.Errorf("trough lost by downsampling: want 12, got %v", minSeen)
	}
}

func TestDownsampleIsChronological(t *testing.T) {
	eq := make([]float64, 3000)
	for i := range eq {
		// Alternating saw so min and max within each bucket are out of index order.
		if i%2 == 0 {
			eq[i] = 100 + float64(i%97)
		} else {
			eq[i] = 100 - float64(i%89)
		}
	}
	got := downsampleSnapshots(mkSnaps(eq), 300)
	for i := 1; i < len(got); i++ {
		if !got[i].Timestamp.After(got[i-1].Timestamp) {
			t.Fatalf("sample %d is not after %d (%v vs %v)", i, i-1, got[i].Timestamp, got[i-1].Timestamp)
		}
	}
}

func TestDownsamplePinsEndpoints(t *testing.T) {
	eq := make([]float64, 2000)
	for i := range eq {
		eq[i] = 100 + float64(i)*0.01
	}
	snaps := mkSnaps(eq)
	got := downsampleSnapshots(snaps, 100)
	if got[0] != snaps[0] {
		t.Errorf("first sample not pinned: %v vs %v", got[0].Timestamp, snaps[0].Timestamp)
	}
	if got[len(got)-1] != snaps[len(snaps)-1] {
		t.Errorf("last sample not pinned: %v vs %v", got[len(got)-1].Timestamp, snaps[len(snaps)-1].Timestamp)
	}
}

func TestDownsampleCoversWholeSpanNotJustTail(t *testing.T) {
	// The bug this guards: a 45-day history reduced to a display budget must still
	// START 45 days ago. Returning the newest N rows instead is what made the "All"
	// range render as only the most recent few days.
	eq := make([]float64, 20000)
	for i := range eq {
		eq[i] = 100 + float64(i)*0.001
	}
	snaps := mkSnaps(eq)
	got := downsampleSnapshots(snaps, 1500)
	fullSpan := snaps[len(snaps)-1].Timestamp.Sub(snaps[0].Timestamp)
	gotSpan := got[len(got)-1].Timestamp.Sub(got[0].Timestamp)
	if gotSpan != fullSpan {
		t.Fatalf("span shrank: want %v, got %v", fullSpan, gotSpan)
	}
}
