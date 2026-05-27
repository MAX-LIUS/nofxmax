package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"nofx/logger"
	"nofx/market"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// handleBackfillSceneTags retroactively fills entry_scene_tags for historical positions
// that don't have them. Parses the decision record's input prompt for market data.
func (s *Server) handleBackfillSceneTags(c *gin.Context) {
	traderID := c.Param("id")
	if traderID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "trader_id is required"})
		return
	}

	positions, err := s.store.Position().GetClosedPositions(traderID, 500)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch positions"})
		return
	}

	updated := 0
	skipped := 0
	errors := 0
	for _, pos := range positions {
		if pos.EntrySceneTags != "" {
			skipped++
			continue
		}

		if pos.EntryDecisionCycle <= 0 {
			skipped++
			continue
		}

		decision, err := s.store.Decision().GetRecordByCycle(traderID, pos.EntryDecisionCycle)
		if err != nil || decision == nil {
			skipped++
			continue
		}

		tags := reconstructSceneTagsFromInputPrompt(decision.InputPrompt, decision.DecisionJSON, pos.Symbol, pos.Side)
		if tags == "" {
			skipped++
			continue
		}

		if err := s.store.Position().UpdateSceneTags(pos.ID, tags); err != nil {
			logger.Infof("⚠️ Failed to update scene tags for position %d: %v", pos.ID, err)
			errors++
			continue
		}
		updated++
	}

	c.JSON(http.StatusOK, gin.H{
		"updated": updated,
		"skipped": skipped,
		"errors":  errors,
		"total":   len(positions),
	})
}

// reconstructSceneTagsFromInputPrompt parses the AI input prompt to extract market state
// at the time of the decision. The prompt contains formatted market data including
// price, EMA20, price changes, regime, and trend phase.
func reconstructSceneTagsFromInputPrompt(inputPrompt, decisionJSON, symbol, side string) string {
	if inputPrompt == "" {
		return ""
	}

	// Find the section for this symbol's market data
	symbolSection := extractSymbolSection(inputPrompt, symbol)
	if symbolSection == "" {
		return ""
	}

	var chg4h, chg1h, ema20Dev float64
	var trendPhase, regime, direction string

	// Extract price changes from patterns like "4h_change=+1.23%" or "chg_4h: +1.23%"
	chg4h = extractFloat(symbolSection, `(?:4h_change|chg_4h|price_change_4h)[=:\s]+([+-]?\d+\.?\d*)`)
	chg1h = extractFloat(symbolSection, `(?:1h_change|chg_1h|price_change_1h)[=:\s]+([+-]?\d+\.?\d*)`)

	// Also try composite snapshot format: "1h=1.23% 4h=-0.45%"
	if chg4h == 0 {
		chg4h = extractFloat(symbolSection, `4h=([+-]?\d+\.?\d*)%`)
	}
	if chg1h == 0 {
		chg1h = extractFloat(symbolSection, `1h=([+-]?\d+\.?\d*)%`)
	}

	// Extract current price and EMA20 for deviation calculation
	price := extractFloat(symbolSection, `current_price\s*=\s*(\d+\.?\d*)`)
	if price == 0 {
		price = extractFloat(symbolSection, `price=(\d+\.?\d*)`)
	}
	ema20 := extractFloat(symbolSection, `current_ema20\s*=\s*(\d+\.?\d*)`)
	if price > 0 && ema20 > 0 {
		ema20Dev = (price - ema20) / ema20 * 100
	}

	// Extract trend phase from "trend_phase=extension" pattern
	trendPhase = extractString(symbolSection, `trend_phase=(\w+)`)
	direction = extractString(symbolSection, `direction=(\w+)`)
	regime = extractString(symbolSection, `regime=(\w+)`)

	// If we couldn't find trend_phase from the prompt, compute it from the data we have
	if trendPhase == "" && (chg4h != 0 || ema20Dev != 0) {
		data := &market.Data{
			CurrentPrice:  price,
			CurrentEMA20:  ema20,
			PriceChange4h: chg4h,
			PriceChange1h: chg1h,
		}
		if price == 0 {
			data.CurrentPrice = 100
			data.CurrentEMA20 = 100 / (1 + ema20Dev/100)
		}
		phase := market.ClassifyTrendPhase(data)
		if phase != nil {
			trendPhase = phase.Phase
			direction = phase.Direction
		}
	}

	// If we still have nothing useful, return empty
	if trendPhase == "" && regime == "" && chg4h == 0 && ema20Dev == 0 {
		return ""
	}

	// Default direction from side if not found
	if direction == "" {
		if side == "LONG" || side == "long" {
			direction = "up"
		} else {
			direction = "down"
		}
	}

	tags := map[string]interface{}{
		"trend_phase": trendPhase,
		"regime":      regime,
		"chg4h":       roundTo2(chg4h),
		"chg1h":       roundTo2(chg1h),
		"ema20_dev":   roundTo2(ema20Dev),
		"direction":   direction,
	}

	// Try to extract trigger_type from the decision JSON output
	triggerType := extractString(decisionJSON, `"trigger_type"\s*:\s*"([^"]+)"`)
	if triggerType != "" {
		tags["trigger_type"] = triggerType
	}

	data, err := json.Marshal(tags)
	if err != nil {
		return ""
	}
	return string(data)
}

// extractSymbolSection finds the market data section for a specific symbol in the prompt.
func extractSymbolSection(prompt, symbol string) string {
	// Normalize symbol: remove USDT suffix for matching
	baseSymbol := strings.TrimSuffix(strings.TrimSuffix(symbol, "USDT"), "-USDT")

	// Try multiple patterns used in different prompt versions
	patterns := []string{
		fmt.Sprintf(`=== %s Market Data ===`, symbol),
		fmt.Sprintf(`=== %s Market Data ===`, baseSymbol),
		fmt.Sprintf(`=== %sUSDT Market Data ===`, baseSymbol),
		fmt.Sprintf(`\[%s\]`, symbol),
		fmt.Sprintf(`\[%s\]`, baseSymbol),
		fmt.Sprintf(`Symbol: %s`, symbol),
		fmt.Sprintf(`Symbol: %s`, baseSymbol),
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(`(?i)` + pattern)
		loc := re.FindStringIndex(prompt)
		if loc != nil {
			// Extract up to 2000 chars after the match (enough for one symbol's data)
			end := loc[1] + 2000
			if end > len(prompt) {
				end = len(prompt)
			}
			section := prompt[loc[0]:end]
			// Trim at next symbol section if present
			nextSection := regexp.MustCompile(`(?i)=== \w+ Market Data ===`)
			if nextLoc := nextSection.FindStringIndex(section[loc[1]-loc[0]:]); nextLoc != nil {
				section = section[:loc[1]-loc[0]+nextLoc[0]]
			}
			return section
		}
	}

	// Fallback: if prompt is short enough, search the whole thing
	if len(prompt) < 5000 {
		return prompt
	}
	return ""
}

func extractFloat(text, pattern string) float64 {
	re := regexp.MustCompile(pattern)
	matches := re.FindStringSubmatch(text)
	if len(matches) < 2 {
		return 0
	}
	val, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0
	}
	return val
}

func extractString(text, pattern string) string {
	re := regexp.MustCompile(pattern)
	matches := re.FindStringSubmatch(text)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}

func roundTo2(v float64) float64 {
	return float64(int(v*100)) / 100
}
