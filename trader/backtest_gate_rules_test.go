package trader

import (
	"fmt"
	"nofx/market"
	"testing"
)

// TestBacktestNewGateRules simulates the new entry gate rules against historical
// trade scenarios to estimate how many losing trades would be blocked and how many
// winning trades would be incorrectly blocked.
func TestBacktestNewGateRules(t *testing.T) {
	// Historical trade scenarios from OKX91 analysis (simplified)
	// Each entry: symbol, side, pnl, chg4h, chg1h, ema20_dev, regime
	type scenario struct {
		id       int
		symbol   string
		side     string
		pnl      float64
		chg4h    float64
		chg1h    float64
		ema20Dev float64 // (price-ema20)/ema20 * 100
	}

	// Top losses
	losses := []scenario{
		{110, "SUI", "long", -2.89, 3.09, 0.8, 1.5},
		{112, "ZEC", "long", -2.76, 0.73, 0.3, 0.5},
		{147, "HYPE", "short", -2.15, -1.94, 0.5, -0.5},
		{108, "TON", "long", -1.96, 1.52, 0.6, 0.8},
		{255, "ZEC", "long", -1.82, 0.33, 0.1, -0.9},
		{253, "OKB", "short", -1.80, -0.80, -0.3, 6.3},
		{152, "HYPE", "short", -1.31, -3.95, 1.02, -1.8},
		{231, "HYPE", "long", -1.28, 2.87, 0.9, 2.6},
		{250, "WLD", "short", -1.10, -0.97, 0.2, 2.8},
		{107, "ETH", "long", -1.02, 0.05, 0.02, 0.1},
		{235, "CL", "long", -0.77, -0.74, -0.3, 0.3},
	}

	// Top wins
	wins := []scenario{
		{168, "ZEC", "long", 4.58, 1.64, 0.5, 1.0},
		{154, "ETH", "short", 3.48, 0.23, 0.1, 0.1},
		{240, "CL", "long", 3.25, 0.19, 0.1, -0.9},
		{238, "HYPE", "long", 2.93, 1.34, 0.5, 0.7},
		{136, "ZEC", "long", 2.17, 0.63, 0.3, 0.9},
		{234, "WLD", "short", 1.90, -0.83, -0.3, -1.5},
		{237, "ZEC", "short", 1.74, -0.54, -0.2, -0.4},
		{109, "TON", "long", 1.66, 2.30, 0.8, 1.2},
		{212, "SOL", "long", 1.48, 1.16, 0.4, 0.8},
	}

	// Simulate gate checks
	checkBlocked := func(s scenario) (bool, string) {
		data := &market.Data{
			CurrentPrice:  100, // normalized
			CurrentEMA20:  100 / (1 + s.ema20Dev/100),
			PriceChange4h: s.chg4h,
			PriceChange1h: s.chg1h,
		}

		// Check 1: EMA20 direction conflict
		if s.side == "long" && s.ema20Dev < -0.5 {
			return true, "ema20_direction_conflict"
		}
		if s.side == "short" && s.ema20Dev > 0.5 {
			return true, "ema20_direction_conflict"
		}

		// Check 2: Trend phase
		phase := market.ClassifyTrendPhase(data)
		if phase.Phase == "exhaustion" {
			return true, "trend_phase_exhaustion"
		}
		if phase.Phase == "extension" {
			isTrendFollowing := (s.side == "long" && s.chg4h > 0) || (s.side == "short" && s.chg4h < 0)
			if isTrendFollowing {
				return true, "trend_phase_extension"
			}
		}

		// Check 3: Momentum exhausted (new thresholds)
		absChg4h := s.chg4h
		if absChg4h < 0 {
			absChg4h = -absChg4h
		}
		exhaustedThreshold := 3.5
		if s.symbol != "BTC" && s.symbol != "ETH" {
			exhaustedThreshold = 2.8
		}
		if absChg4h > exhaustedThreshold {
			return true, "momentum_exhausted"
		}

		// Check 4: Momentum fading
		absChg1h := s.chg1h
		if absChg1h < 0 {
			absChg1h = -absChg1h
		}
		if absChg4h > 2.0 {
			ratio := absChg1h / absChg4h
			if ratio < 0.2 {
				return true, "momentum_fading"
			}
			if s.side == "long" && s.chg4h > 2.0 && s.chg1h < 0 {
				return true, "momentum_fading"
			}
			if s.side == "short" && s.chg4h < -2.0 && s.chg1h > 0 {
				return true, "momentum_fading"
			}
		}

		return false, ""
	}

	// Run backtest
	fmt.Println("\n═══════════════════════════════════════════════════")
	fmt.Println("  BACKTEST: New Gate Rules vs Historical Trades")
	fmt.Println("═══════════════════════════════════════════════════")

	blockedLosses := 0
	totalLossPnL := 0.0
	savedPnL := 0.0
	fmt.Println("\n📉 LOSSES (would be blocked?):")
	for _, s := range losses {
		blocked, reason := checkBlocked(s)
		status := "  PASS"
		if blocked {
			status = "🚫 BLOCK"
			blockedLosses++
			savedPnL += -s.pnl // positive = saved money
		}
		totalLossPnL += s.pnl
		fmt.Printf("  #%d %s %s PnL=%.2f | 4h=%.2f%% EMA20=%.1f%% | %s %s\n",
			s.id, s.symbol, s.side, s.pnl, s.chg4h, s.ema20Dev, status, reason)
	}

	blockedWins := 0
	missedPnL := 0.0
	fmt.Println("\n📈 WINS (would be incorrectly blocked?):")
	for _, s := range wins {
		blocked, reason := checkBlocked(s)
		status := "  PASS"
		if blocked {
			status = "⚠️ FALSE BLOCK"
			blockedWins++
			missedPnL += s.pnl
		}
		fmt.Printf("  #%d %s %s PnL=+%.2f | 4h=%.2f%% EMA20=%.1f%% | %s %s\n",
			s.id, s.symbol, s.side, s.pnl, s.chg4h, s.ema20Dev, status, reason)
	}

	fmt.Println("\n═══════════════════════════════════════════════════")
	fmt.Printf("  RESULTS:\n")
	fmt.Printf("  Losses blocked: %d/%d (saved %.2f USDT)\n", blockedLosses, len(losses), savedPnL)
	fmt.Printf("  Wins incorrectly blocked: %d/%d (missed %.2f USDT)\n", blockedWins, len(wins), missedPnL)
	fmt.Printf("  Net PnL improvement: +%.2f USDT\n", savedPnL-missedPnL)
	fmt.Printf("  False positive rate: %.1f%%\n", float64(blockedWins)/float64(len(wins))*100)
	fmt.Println("═══════════════════════════════════════════════════")

	// Assertions
	if savedPnL-missedPnL < 0 {
		t.Errorf("Net PnL improvement is negative (%.2f), new rules make things worse", savedPnL-missedPnL)
	}
	if float64(blockedWins)/float64(len(wins)) > 0.2 {
		t.Errorf("False positive rate too high: %d/%d = %.1f%%", blockedWins, len(wins), float64(blockedWins)/float64(len(wins))*100)
	}
}
