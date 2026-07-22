package market

import "testing"

// TestProjectComposite_OverlaysByView verifies the structure-map overlays
// (BOS/CHoCH, order blocks, volume profile, anchored VWAP) are retained on the
// chart/full views and stripped on the token-sensitive ai/summary views.
func TestProjectComposite_OverlaysByView(t *testing.T) {
	base := &CompositeMarketSnapshot{
		Symbol:    "BTCUSDT",
		Price:     100,
		PrimaryTF: "1h",
		StructureBreaks: []StructureBreak{
			{Type: "BOS", Direction: "bullish", BreakLevel: 99, BarsAgo: 3},
		},
		OrderBlocks: []OrderBlock{
			{Low: 97, High: 98, Mid: 97.5, Direction: "demand", BarsAgo: 5},
		},
		VolumeProfile: &VolumeProfile{POC: 99.5, VAH: 101, VAL: 98},
		AnchoredVWAPs: []AnchoredVWAP{
			{Anchor: "swing_low", VWAP: 99.2, UpperBand: 100, LowerBand: 98.4},
		},
	}

	hasOverlays := func(s *CompositeMarketSnapshot) bool {
		return len(s.StructureBreaks) > 0 || len(s.OrderBlocks) > 0 ||
			s.VolumeProfile != nil || len(s.AnchoredVWAPs) > 0
	}

	// chart + full must KEEP overlays.
	for _, view := range []string{"chart", "full", ""} {
		got := ProjectCompositeMarketSnapshot(base, view)
		if !hasOverlays(got) {
			t.Errorf("view %q must retain structure-map overlays, got none", view)
		}
	}

	// ai + summary must STRIP overlays (prompt token budget).
	for _, view := range []string{"ai", "summary"} {
		got := ProjectCompositeMarketSnapshot(base, view)
		if hasOverlays(got) {
			t.Errorf("view %q must strip structure-map overlays, but some remained: breaks=%d blocks=%d vp=%v avwap=%d",
				view, len(got.StructureBreaks), len(got.OrderBlocks), got.VolumeProfile != nil, len(got.AnchoredVWAPs))
		}
	}
}
