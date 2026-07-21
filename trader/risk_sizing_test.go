package trader

import (
	"math"
	"testing"

	"nofx/kernel"
	"nofx/store"
)

func mkCfg(enabled bool, pct float64, ladderEnabled bool, slPct float64, slUnit store.ProtectionDistanceUnit) *store.StrategyConfig {
	c := &store.StrategyConfig{}
	c.RiskControl.RiskSizingEnabled = enabled
	c.RiskControl.RiskPerTradePctOfEquity = pct
	if ladderEnabled {
		c.Protection.LadderTPSL.Enabled = true
		c.Protection.LadderTPSL.Rules = []store.LadderTPSLRule{
			{StopLossPct: slPct, StopLossUnit: slUnit},
		}
	}
	return c
}

func TestRiskBasedPositionSize(t *testing.T) {
	cases := []struct {
		name      string
		cfg       *store.StrategyConfig
		entry     float64
		stop      float64
		atr       float64
		equity    float64
		wantApply bool
		wantSize  float64
	}{
		{
			// AI stop only (no ladder): equity 160, risk 3% = 4.8; stopDist 2% → 240
			name: "ai stop only", cfg: mkCfg(true, 3, false, 0, ""),
			entry: 100, stop: 98, atr: 0, equity: 160, wantApply: true, wantSize: 240,
		},
		{
			// config stop WIDER than AI: AI stop 2% (98), but ladder SL 2.5×ATR, ATR=2 →
			// config stop = 2.5*2/100*100 = 5%. eff = max(2%,5%) = 5%. size = 4.8/0.05 = 96
			// (uses the wider config stop → smaller, safer size)
			name: "config stop wider wins", cfg: mkCfg(true, 3, true, 2.5, store.ProtectionUnitATR),
			entry: 100, stop: 98, atr: 2, equity: 160, wantApply: true, wantSize: 96,
		},
		{
			// AI stop WIDER than config: AI stop 6% (94), ladder 2.5×ATR ATR=1 → config 2.5%.
			// eff = max(6%,2.5%) = 6%. size = 4.8/0.06 = 80
			name: "ai stop wider wins", cfg: mkCfg(true, 3, true, 2.5, store.ProtectionUnitATR),
			entry: 100, stop: 94, atr: 1, equity: 160, wantApply: true, wantSize: 80,
		},
		{name: "disabled", cfg: mkCfg(false, 3, false, 0, ""), entry: 100, stop: 98, atr: 0, equity: 160, wantApply: false},
		{name: "no stop", cfg: mkCfg(true, 3, false, 0, ""), entry: 100, stop: 0, atr: 0, equity: 160, wantApply: false},
		{name: "zero equity", cfg: mkCfg(true, 3, false, 0, ""), entry: 100, stop: 98, atr: 0, equity: 0, wantApply: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &kernel.Decision{Symbol: "TESTUSDT", StopLoss: c.stop}
			size, applied, reason := riskBasedPositionSize(c.cfg, d, c.entry, c.atr, c.equity)
			if applied != c.wantApply {
				t.Fatalf("applied=%v want %v (reason=%s)", applied, c.wantApply, reason)
			}
			if c.wantApply && math.Abs(size-c.wantSize) > 0.01 {
				t.Fatalf("size=%.4f want %.4f (reason=%s)", size, c.wantSize, reason)
			}
		})
	}
}
