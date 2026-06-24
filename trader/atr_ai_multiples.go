package trader

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"nofx/logger"
	"nofx/store"
)

// aiMultiples holds the per-dimension ATR multiples the AI returns for a coin.
type aiMultiples struct {
	StopLossATR    float64 `json:"stop_loss_atr"`
	TakeProfit1ATR float64 `json:"take_profit_1_atr"`
	TakeProfit2ATR float64 `json:"take_profit_2_atr"`
	BreakEven1ATR  float64 `json:"break_even_1_atr"`
	BreakEven2ATR  float64 `json:"break_even_2_atr"`
	Reasoning      string  `json:"reasoning,omitempty"`
}

// aiMultiplesCacheEntry caches one symbol's AI multiples with an expiry.
type aiMultiplesCacheEntry struct {
	mults  aiMultiples
	expiry time.Time
}

// aiMultiplesCache is a package-level per-(trader,symbol) cache so we don't call
// the AI on every position open. TTL keeps multiples fresh as volatility shifts.
var (
	aiMultiplesCacheMu sync.Mutex
	aiMultiplesCache   = map[string]aiMultiplesCacheEntry{}
)

// aiMultiplesTTL is how long AI multiples stay valid before re-querying.
const aiMultiplesTTL = 4 * time.Hour

// resolveAIMultiples returns the AI-computed ATR multiples for a symbol, using
// the cache when fresh. Returns (mults, true) on success; (zero, false) when the
// AI is unavailable or returns unusable output — caller then falls back to the
// configured fixed multiples.
func (at *AutoTrader) resolveAIMultiples(symbol string, acfg store.ATRProtectionConfig, atrValue, atrPct, price float64) (aiMultiples, bool) {
	if at.mcpClient == nil {
		return aiMultiples{}, false
	}
	cacheKey := at.id + "|" + symbol
	now := time.Now()

	aiMultiplesCacheMu.Lock()
	if ent, ok := aiMultiplesCache[cacheKey]; ok && now.Before(ent.expiry) {
		aiMultiplesCacheMu.Unlock()
		return ent.mults, true
	}
	aiMultiplesCacheMu.Unlock()

	mults, ok := at.queryAIMultiples(symbol, acfg, atrValue, atrPct, price)
	if !ok {
		return aiMultiples{}, false
	}

	aiMultiplesCacheMu.Lock()
	aiMultiplesCache[cacheKey] = aiMultiplesCacheEntry{mults: mults, expiry: now.Add(aiMultiplesTTL)}
	aiMultiplesCacheMu.Unlock()
	return mults, true
}

// queryAIMultiples makes the actual AI call and parses/clamps the response.
func (at *AutoTrader) queryAIMultiples(symbol string, acfg store.ATRProtectionConfig, atrValue, atrPct, price float64) (aiMultiples, bool) {
	c := acfg.WithDefaults()
	sys := "You are a crypto futures risk engine. Given a coin's volatility (ATR), " +
		"return ATR-multiple distances for protective orders, tailored to THIS coin's " +
		"volatility regime. Higher-volatility coins need wider multiples to avoid wick " +
		"stop-outs; calmer coins can use tighter multiples. Respond with ONLY a JSON object, " +
		"no prose, no code fence."
	usr := fmt.Sprintf(`Coin: %s
Current price: %.6f
ATR(%s,%d): %.6f  (= %.2f%% of price)

Return ATR multiples for these protection dimensions (numbers only, in units of ATR):
- stop_loss_atr: stop-loss distance (typical 2.0 - 5.0)
- take_profit_1_atr: first ladder take-profit distance (typical 2.0 - 4.0)
- take_profit_2_atr: second ladder take-profit distance (must be > tp1, typical 4.0 - 8.0)
- break_even_1_atr: profit (in ATR) that triggers move-to-break-even tier 1 (typical 1.0 - 2.5)
- break_even_2_atr: profit (in ATR) that triggers break-even tier 2 (must be > be1)

Constraints: all between %.1f and %.1f. tp2 > tp1, be2 > be1.
Respond as JSON: {"stop_loss_atr":..,"take_profit_1_atr":..,"take_profit_2_atr":..,"break_even_1_atr":..,"break_even_2_atr":..,"reasoning":"one short sentence"}`,
		symbol, price, c.Timeframe, c.ATRPeriod, atrValue, atrPct, c.AIMinMult, c.AIMaxMult)

	resp, err := at.mcpClient.CallWithMessages(sys, usr)
	if err != nil {
		logger.Warnf("  ⚠️ ATR-AI multiples: AI call failed for %s: %v", symbol, err)
		return aiMultiples{}, false
	}

	m, ok := parseAIMultiples(resp)
	if !ok {
		logger.Warnf("  ⚠️ ATR-AI multiples: unparseable AI output for %s", symbol)
		return aiMultiples{}, false
	}

	// Clamp to bounds and enforce ordering.
	clamp := func(v float64) float64 {
		if v < c.AIMinMult {
			return c.AIMinMult
		}
		if v > c.AIMaxMult {
			return c.AIMaxMult
		}
		return v
	}
	m.StopLossATR = clamp(m.StopLossATR)
	m.TakeProfit1ATR = clamp(m.TakeProfit1ATR)
	m.TakeProfit2ATR = clamp(m.TakeProfit2ATR)
	m.BreakEven1ATR = clamp(m.BreakEven1ATR)
	m.BreakEven2ATR = clamp(m.BreakEven2ATR)
	if m.TakeProfit2ATR <= m.TakeProfit1ATR {
		m.TakeProfit2ATR = m.TakeProfit1ATR + 1.0
	}
	if m.BreakEven2ATR <= m.BreakEven1ATR {
		m.BreakEven2ATR = m.BreakEven1ATR + 0.5
	}
	// Sanity: all core dims must be positive.
	if m.StopLossATR <= 0 || m.TakeProfit1ATR <= 0 || m.TakeProfit2ATR <= 0 {
		return aiMultiples{}, false
	}
	logger.Infof("  🤖 ATR-AI multiples %s: SL%.1f TP1 %.1f TP2 %.1f BE1 %.1f BE2 %.1f (%s)",
		symbol, m.StopLossATR, m.TakeProfit1ATR, m.TakeProfit2ATR, m.BreakEven1ATR, m.BreakEven2ATR, m.Reasoning)
	return m, true
}

// parseAIMultiples extracts the JSON object from the AI response (tolerating
// code fences / surrounding text).
func parseAIMultiples(resp string) (aiMultiples, bool) {
	s := strings.TrimSpace(resp)
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return aiMultiples{}, false
	}
	var m aiMultiples
	if err := json.Unmarshal([]byte(s[start:end+1]), &m); err != nil {
		return aiMultiples{}, false
	}
	return m, true
}
