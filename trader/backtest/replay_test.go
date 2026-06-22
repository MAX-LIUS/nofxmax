package backtest

import (
	"testing"

	"nofx/market"
)

// claudeBaseline returns the percent-mode params matching Claude's real config.
func claudeBaseline() ProtectionParams {
	return ProtectionParams{
		Unit:        UnitPercent,
		StopLossPct: 5,
		TPLegs: []LadderLeg{
			{DistPct: 3, CloseRatioPct: 35},
			{DistPct: 6, CloseRatioPct: 25},
		},
		BELegs: []BELeg{
			{TriggerPct: 2, OffsetPct: 0.4, CloseRatioPct: 50},
			{TriggerPct: 4, OffsetPct: 1.5, CloseRatioPct: 35},
		},
		DDRules: []DDRule{
			{MinProfitPct: 6, MaxDrawdownPct: 40, CloseRatioPct: 45},
		},
	}
}

// bar builds a Kline with the given OHLC at sequential open times.
func bar(o, h, l, c float64, t int64) market.Kline {
	return market.Kline{Open: o, High: h, Low: l, Close: c, OpenTime: t}
}

// flatPreHistory returns atrLookback+1 flat bars so ATR is well-defined and
// entryIdx points just past them.
func flatPreHistory(price float64) []market.Kline {
	var bars []market.Kline
	for i := 0; i <= atrLookback; i++ {
		bars = append(bars, bar(price, price*1.001, price*0.999, price, int64(i)*3600000))
	}
	return bars
}

func TestReplay_StopLossLong(t *testing.T) {
	bars := flatPreHistory(100)
	entryIdx := len(bars)
	// One bar that dives below -5% (to 94) → SL at 95 fills, closes all.
	bars = append(bars, bar(100, 100.5, 94, 94.5, int64(entryIdx)*3600000))
	e := Entry{Symbol: "BTCUSDT", Side: "LONG", EntryPrice: 100, Quantity: 1}
	r := ReplayEntry(claudeBaseline(), e, bars, entryIdx)
	if r.ExitPrice != 95 {
		t.Fatalf("SL should fill at 95, got %.4f", r.ExitPrice)
	}
	if len(r.CloseReasons) == 0 || r.CloseReasons[0] != "stop_loss" {
		t.Fatalf("expected stop_loss first, got %v", r.CloseReasons)
	}
	if r.ReturnPct > -4.9 || r.ReturnPct < -5.1 {
		t.Fatalf("SL return should be ~-5%%, got %.2f", r.ReturnPct)
	}
}

func TestReplay_TakeProfitLadderLong(t *testing.T) {
	bars := flatPreHistory(100)
	entryIdx := len(bars)
	// Rise to +3% then +6%, never breaching stop.
	bars = append(bars, bar(100, 103.2, 100, 103, int64(entryIdx)*3600000))
	bars = append(bars, bar(103, 106.5, 103, 106, int64(entryIdx+1)*3600000))
	e := Entry{Symbol: "BTCUSDT", Side: "LONG", EntryPrice: 100, Quantity: 1}
	r := ReplayEntry(claudeBaseline(), e, bars, entryIdx)
	// TP1 (35% @103) + TP2 (25% @106) = 60% closed; rest marked at last close.
	hasTP := false
	for _, cr := range r.CloseReasons {
		if cr == "take_profit" {
			hasTP = true
		}
	}
	if !hasTP {
		t.Fatalf("expected take_profit fills, got %v", r.CloseReasons)
	}
	if r.ReturnPct <= 0 {
		t.Fatalf("ladder up move should be profitable, got %.2f", r.ReturnPct)
	}
}

func TestReplay_ATRModeScalesWithVolatility(t *testing.T) {
	// Higher ATR → wider SL distance in price terms.
	p := ProtectionParams{Unit: UnitATRMult, StopLossATR: 1.5}
	// Build pre-history with ~2-unit true range → ATR ~2 on price 100.
	var bars []market.Kline
	for i := 0; i <= atrLookback; i++ {
		bars = append(bars, bar(100, 101, 99, 100, int64(i)*3600000))
	}
	entryIdx := len(bars)
	atrH, atrL, atrC := sliceOHLC(bars[:entryIdx], entryIdx-1)
	atr := wilderATR(atrH, atrL, atrC, atrLookback)
	if atr <= 0 {
		t.Fatalf("ATR should be positive")
	}
	slDist := p.slDistancePct(100, atr)
	wantDist := 1.5 * atr / 100 * 100
	if slDist < wantDist-1e-9 || slDist > wantDist+1e-9 {
		t.Fatalf("ATR SL dist=%.4f want %.4f", slDist, wantDist)
	}
}

func TestAggregate_Metrics(t *testing.T) {
	results := []TradeResult{
		{RealizedPnL: 100, ReturnPct: 10},
		{RealizedPnL: -50, ReturnPct: -5},
		{RealizedPnL: 30, ReturnPct: 3},
	}
	pr := Aggregate(results)
	if pr.Trades != 3 || pr.Wins != 2 || pr.Losses != 1 {
		t.Fatalf("counts wrong: %+v", pr)
	}
	if pr.TotalPnL != 80 {
		t.Fatalf("total pnl=%.2f want 80", pr.TotalPnL)
	}
	// gross profit 130 / gross loss 50 = 2.6
	if pr.ProfitFactor < 2.59 || pr.ProfitFactor > 2.61 {
		t.Fatalf("profit factor=%.3f want ~2.6", pr.ProfitFactor)
	}
}
