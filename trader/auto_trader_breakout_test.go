package trader

import "testing"

func TestParseFirstInt(t *testing.T) {
	cases := map[string]int{
		"75": 75, "score: 82": 82, "  40  ": 40, "100": 100, "0": 0,
		"abc": -1, "": -1, "150": -1, "-5": 5, // "-5"→"5"=5 (leading minus ignored)
	}
	for in, want := range cases {
		if g := parseFirstInt(in); g != want {
			t.Errorf("parseFirstInt(%q)=%d want %d", in, g, want)
		}
	}
}

func TestPctOf(t *testing.T) {
	if pctOf(2, 100) != 2 {
		t.Fatal("pctOf(2,100) should be 2")
	}
	if pctOf(1, 0) != 0 {
		t.Fatal("pctOf div0 should be 0")
	}
}

func TestBreakoutSizeMultiplierForScore(t *testing.T) {
	cases := []struct {
		score int
		mult  float64
		label string
	}{
		{100, 1.0, "genuine"},
		{70, 1.0, "genuine"}, // boundary: >=70 genuine
		{69, 0.6, "suspicious"},
		{40, 0.6, "suspicious"}, // boundary: >=40 suspicious
		{39, 0.3, "weak"},
		{0, 0.3, "weak"},
	}
	for _, c := range cases {
		mult, label := breakoutSizeMultiplierForScore(c.score)
		if mult != c.mult || label != c.label {
			t.Errorf("score %d → (%.1f,%q), want (%.1f,%q)", c.score, mult, label, c.mult, c.label)
		}
	}
}
