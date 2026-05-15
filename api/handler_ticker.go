package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// handleTicker GET /api/ticker?symbol=BTCUSDT&exchange=okx
// Returns real-time price from exchange ticker API (no auth needed)
func (s *Server) handleTicker(c *gin.Context) {
	symbol := c.Query("symbol")
	if symbol == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "symbol parameter is required"})
		return
	}
	exchange := c.DefaultQuery("exchange", "okx")

	var price float64
	var err error

	switch strings.ToLower(exchange) {
	case "okx":
		price, err = getOKXTickerPrice(symbol)
	case "binance":
		price, err = getBinanceTickerPrice(symbol)
	default:
		price, err = getOKXTickerPrice(symbol)
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"symbol": symbol,
		"price":  price,
		"ts":     time.Now().UnixMilli(),
	})
}

func getOKXTickerPrice(symbol string) (float64, error) {
	// Convert BTCUSDT -> BTC-USDT-SWAP
	instId := convertToOKXInstId(symbol)
	url := fmt.Sprintf("https://www.okx.com/api/v5/market/ticker?instId=%s", instId)

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var result struct {
		Data []struct {
			Last string `json:"last"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, err
	}
	if len(result.Data) == 0 {
		return 0, fmt.Errorf("no ticker data for %s", symbol)
	}
	return strconv.ParseFloat(result.Data[0].Last, 64)
}

func getBinanceTickerPrice(symbol string) (float64, error) {
	url := fmt.Sprintf("https://fapi.binance.com/fapi/v1/ticker/price?symbol=%s", symbol)

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var result struct {
		Price string `json:"price"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, err
	}
	if result.Price == "" {
		return 0, fmt.Errorf("no ticker data for %s", symbol)
	}
	return strconv.ParseFloat(result.Price, 64)
}

func convertToOKXInstId(symbol string) string {
	// BTCUSDT -> BTC-USDT-SWAP
	symbol = strings.ToUpper(symbol)
	if strings.HasSuffix(symbol, "USDT") {
		base := strings.TrimSuffix(symbol, "USDT")
		return base + "-USDT-SWAP"
	}
	return symbol + "-USDT-SWAP"
}
