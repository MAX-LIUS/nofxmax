package store

import "testing"

func TestDimMode_PerDimensionOverridesPanel(t *testing.T) {
	c := ATRProtectionConfig{
		Enabled:      true,
		MultipleMode: "fixed",
		SLMode:       "ai",
		TP1Mode:      "percent",
		// TP2/BE1/BE2/DD unset → fall back to panel "fixed"
	}
	if c.DimMode(ATRDimSL) != "ai" {
		t.Fatalf("SL should be ai, got %s", c.DimMode(ATRDimSL))
	}
	if c.DimMode(ATRDimTP1) != "percent" {
		t.Fatalf("TP1 should be percent, got %s", c.DimMode(ATRDimTP1))
	}
	if c.DimMode(ATRDimTP2) != "fixed" {
		t.Fatalf("TP2 should fall back to fixed, got %s", c.DimMode(ATRDimTP2))
	}
}

func TestDimMode_PanelAIFallback(t *testing.T) {
	c := ATRProtectionConfig{Enabled: true, MultipleMode: "ai"}
	if c.DimMode(ATRDimBE1) != "ai" {
		t.Fatalf("unset dim should follow panel ai, got %s", c.DimMode(ATRDimBE1))
	}
}

func TestAnyATRDimActive(t *testing.T) {
	allPct := ATRProtectionConfig{
		Enabled: true, MultipleMode: "fixed",
		SLMode: "percent", TP1Mode: "percent", TP2Mode: "percent",
		BE1Mode: "percent", BE2Mode: "percent", DDMode: "percent",
	}
	if allPct.AnyATRDimActive() {
		t.Fatalf("all-percent should be inactive")
	}
	oneAI := allPct
	oneAI.TP2Mode = "ai"
	if !oneAI.AnyATRDimActive() {
		t.Fatalf("one ai dim should be active")
	}
}
