package backtest

import (
	"testing"

	"nofx/market"
)

// proxyFlatBars builds `n` ascending 1h bars starting at t0(ms), each flat at price
// `px` (high=low=close=open=px) so no price-level protection fires — isolating
// the non-price close proxy.
func proxyFlatBars(n int, t0, stepMs int64, px float64) []market.Kline {
	bars := make([]market.Kline, n)
	for i := 0; i < n; i++ {
		bars[i] = market.Kline{
			OpenTime: t0 + int64(i)*stepMs,
			Open:     px, High: px, Low: px, Close: px,
		}
	}
	return bars
}

// A losing position held past TimeStopHours must close via time_stop.
func TestCloseProxy_TimeStop(t *testing.T) {
	const hourMs = 3600_000
	// entry at 100, price sits at 98.5 (LONG → -1.5%), 30 flat 1h bars.
	entry := Entry{Symbol: "X", Side: "long", EntryPrice: 100, Quantity: 1, EntryTime: 0}
	bars := proxyFlatBars(30, 0, hourMs, 98.5)
	p := ProtectionParams{Unit: UnitPercent, StopLossPct: 10} // wide SL so it won't fire
	p.CloseProxy = CloseProxyParams{Enabled: true, TimeStopHours: 24, TimeStopLossPct: -1.5, TimeframeHours: 1}
	res := ReplayEntry(p, entry, bars, 0)
	if !hasReason(res.CloseReasons, "time_stop") {
		t.Fatalf("expected time_stop, got %v", res.CloseReasons)
	}
	// must fire at ~24 bars (held 24h), not run to 30.
	if res.BarsHeld > 25 {
		t.Errorf("time_stop fired too late: barsHeld=%d", res.BarsHeld)
	}
}

// A near-flat (non-runner) position held past MaxHoldHours closes via max_hold.
func TestCloseProxy_MaxHold(t *testing.T) {
	const hourMs = 3600_000
	entry := Entry{Symbol: "X", Side: "long", EntryPrice: 100, Quantity: 1, EntryTime: 0}
	bars := proxyFlatBars(30, 0, hourMs, 100.5) // +0.5% < 2% exempt
	p := ProtectionParams{Unit: UnitPercent, StopLossPct: 10}
	p.CloseProxy = CloseProxyParams{Enabled: true, MaxHoldHours: 18, MaxHoldProfitExemptPct: 2, TimeframeHours: 1}
	res := ReplayEntry(p, entry, bars, 0)
	if !hasReason(res.CloseReasons, "max_hold") {
		t.Fatalf("expected max_hold, got %v", res.CloseReasons)
	}
	if res.BarsHeld > 19 {
		t.Errorf("max_hold fired too late: barsHeld=%d", res.BarsHeld)
	}
}

// A profitable runner (>= exempt) must NOT be cut by max_hold.
func TestCloseProxy_MaxHoldRunnerExempt(t *testing.T) {
	const hourMs = 3600_000
	entry := Entry{Symbol: "X", Side: "long", EntryPrice: 100, Quantity: 1, EntryTime: 0}
	bars := proxyFlatBars(30, 0, hourMs, 103) // +3% >= 2% exempt → spared
	p := ProtectionParams{Unit: UnitPercent, StopLossPct: 10}
	p.CloseProxy = CloseProxyParams{Enabled: true, MaxHoldHours: 18, MaxHoldProfitExemptPct: 2, TimeframeHours: 1}
	res := ReplayEntry(p, entry, bars, 0)
	if hasReason(res.CloseReasons, "max_hold") {
		t.Errorf("profitable runner should be exempt from max_hold, got %v", res.CloseReasons)
	}
}

// Timeframe scaling: same 18-bar hold on 15m = 4.5h, so an 18h max-hold must NOT
// fire (proves held-bars→hours conversion uses TimeframeHours).
func TestCloseProxy_TimeframeScaling(t *testing.T) {
	const stepMs = 15 * 60_000
	entry := Entry{Symbol: "X", Side: "long", EntryPrice: 100, Quantity: 1, EntryTime: 0}
	bars := proxyFlatBars(30, 0, stepMs, 100.5)
	p := ProtectionParams{Unit: UnitPercent, StopLossPct: 10}
	p.CloseProxy = CloseProxyParams{Enabled: true, MaxHoldHours: 18, MaxHoldProfitExemptPct: 2, TimeframeHours: 0.25}
	res := ReplayEntry(p, entry, bars, 0)
	// 30 bars * 0.25h = 7.5h < 18h → never fires; ends mark_to_market.
	if hasReason(res.CloseReasons, "max_hold") {
		t.Errorf("15m/30bars=7.5h should not hit 18h max_hold, got %v", res.CloseReasons)
	}
}

func hasReason(rs []string, want string) bool {
	for _, r := range rs {
		if r == want {
			return true
		}
	}
	return false
}

// ATR-give-back DD: a LONG that peaks then retraces ≥ MaxDrawdownATR×ATR in
// PRICE terms (not % of peak PnL) must close via drawdown. Bars: rise to a peak,
// then pull back exactly one ATR-multiple's worth.
func TestDDRule_ATRGiveback(t *testing.T) {
	const hourMs = 3600_000
	entry := Entry{Symbol: "X", Side: "long", EntryPrice: 100, Quantity: 1, EntryTime: 0}
	// Build bars: 20 pre-entry flat @100 (ATR≈0 guard), then entry, rise to 106,
	// then fall to 104. With ATR≈~1 (from a small wiggle) a 1.5×ATR retrace fires.
	bars := make([]market.Kline, 0, 40)
	for i := 0; i < 20; i++ { // pre-entry, small range to seed ATR≈1
		px := 100.0
		bars = append(bars, market.Kline{OpenTime: int64(i-20) * hourMs, Open: px, High: px + 0.5, Low: px - 0.5, Close: px})
	}
	// forward: climb 100→106, then retrace to 104 (=-2 from peak).
	fwd := []float64{101, 103, 106, 104, 104}
	for i, px := range fwd {
		bars = append(bars, market.Kline{OpenTime: int64(i) * hourMs, Open: px, High: px, Low: px, Close: px})
	}
	entryIdx := 20
	p := ProtectionParams{Unit: UnitATRMult, StopLossATR: 20} // SL far away
	// arm at +2% (min profit), give-back 1.5×ATR. ATR≈1 → retrace≥1.5 triggers at 104 (peak106, -2).
	p.DDRules = []DDRule{{MinProfitATR: 0.5, MaxDrawdownATR: 1.5, CloseRatioPct: 100}}
	res := ReplayEntry(p, entry, bars, entryIdx)
	if !hasReason(res.CloseReasons, "drawdown") {
		t.Fatalf("expected ATR-giveback drawdown close, got %v (exit=%.2f)", res.CloseReasons, res.ExitPrice)
	}
}
