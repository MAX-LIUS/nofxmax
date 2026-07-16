package trader

import "testing"

// TestEvalAICloseGate guards the AI-close permission logic. Regression anchor for
// commit 53ffef7, which dropped the allowAIClose master check and let AI closes
// through even when the user had disabled AI closing.
func TestEvalAICloseGate(t *testing.T) {
	const minLoss = 1.0 // stopMinLossPct

	cases := []struct {
		name              string
		isStopLoss        bool
		pnlPct            float64
		allowClose        bool
		allowStopClose    bool
		allowTakeProfit   bool
		wantBlocked       bool
	}{
		// Master gate OFF blocks everything, regardless of sub-gates.
		{"master off blocks TP close", false, 0, false, true, true, true},
		{"master off blocks SL close", true, -5, false, true, true, true},
		{"master off blocks even with sub-gates on", true, -5, false, true, true, true},

		// BN's real config at incident time: close=1, stop_close=0, take_profit=0.
		// A discretionary (non-SL) close must be blocked by the take-profit gate.
		{"BN config: discretionary close blocked", false, 0, true, false, false, true},
		{"BN config: SL close blocked by stop-close gate", true, -5, true, false, false, true},

		// Master ON + sub-gates ON: allowed paths.
		{"TP close allowed", false, 0, true, false, true, false},
		{"SL close allowed when deep enough", true, -5, true, true, false, false},

		// SL close blocked when loss is shallower than threshold.
		{"SL close blocked shallow loss", true, -0.5, true, true, false, true},
		{"SL close allowed at threshold boundary", true, -1.5, true, true, false, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			blocked, tag := evalAICloseGate(c.isStopLoss, c.pnlPct, c.allowClose, c.allowStopClose, c.allowTakeProfit, minLoss)
			if blocked != c.wantBlocked {
				t.Fatalf("blocked=%v want=%v (tag=%q)", blocked, c.wantBlocked, tag)
			}
			if blocked && tag == "" {
				t.Fatal("blocked decision must carry a non-empty log tag")
			}
		})
	}
}
