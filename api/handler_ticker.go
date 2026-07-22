package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"nofx/httpx"
	"nofx/market"
	"nofx/provider/hyperliquid"
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

	// Stock/forex/commodity perps (PLTR, MU, TSLA, GOLD, ...) have no ticker on the
	// CEX price APIs — their price lives on Hyperliquid. Route by BASE-ASSET identity
	// (not just the "xyz:" prefix) because the frontend passes the exchange-native
	// symbol ("PLTRUSDT"). Without this the CEX ticker call 500s and the panel errors.
	if strings.HasPrefix(strings.ToLower(symbol), "xyz:") || market.IsXyzDexAsset(symbol) {
		price, err = getHyperliquidTickerPrice(symbol)
	} else {
		switch strings.ToLower(exchange) {
		case "okx":
			price, err = getOKXTickerPrice(symbol)
		case "binance":
			price, err = getBinanceTickerPrice(symbol)
		default:
			price, err = getOKXTickerPrice(symbol)
		}
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

// getHyperliquidTickerPrice returns the current mid price for an xyz DEX asset
// (stock/forex/commodity perp) from Hyperliquid. Accepts either the internal
// "xyz:PLTR" form or the exchange-native "PLTRUSDT" form — FormatCoinForAPI maps
// both to the Hyperliquid key ("xyz:PLTR"), which is how the allMids/xyz map is keyed.
func getHyperliquidTickerPrice(symbol string) (float64, error) {
	key := hyperliquid.FormatCoinForAPI(symbol)
	client := hyperliquid.NewClient()
	mids, err := client.GetAllMidsXYZ(context.Background())
	if err != nil {
		return 0, err
	}
	priceStr, ok := mids[key]
	if !ok {
		return 0, fmt.Errorf("no ticker data for %s", key)
	}
	return strconv.ParseFloat(priceStr, 64)
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
	// The dashboard may pass a bare base ("BTC"); Binance needs the full pair
	// ("BTCUSDT"). Normalize to match the OKX path (which maps via instId).
	symbol = market.Normalize(symbol)
	url := fmt.Sprintf("https://fapi.binance.com/fapi/v1/ticker/price?symbol=%s", symbol)

	client := httpx.NewBinanceClient(3 * time.Second)
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
