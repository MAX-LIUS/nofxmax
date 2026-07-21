package market

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"nofx/httpx"
)

// RefreshAssetClassesOKX pulls the OKX SWAP instruments list and classifies every
// base asset by instCategory (1=crypto, 3=stock, 4=commodity). Safe to call on a
// timer; merges into the shared cache. Network/parse errors are returned (caller
// logs); the cache simply keeps its previous contents.
func RefreshAssetClassesOKX() (int, error) {
	body, err := okxHTTPGet(okxBaseURL + "/api/v5/public/instruments?instType=SWAP")
	if err != nil {
		return 0, fmt.Errorf("okx instruments fetch: %w", err)
	}
	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			InstID       string `json:"instId"`
			InstCategory string `json:"instCategory"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, fmt.Errorf("okx instruments parse: %w", err)
	}
	if resp.Code != "0" {
		return 0, fmt.Errorf("okx instruments error: code=%s msg=%s", resp.Code, resp.Msg)
	}
	out := make(map[string]AssetClass, len(resp.Data))
	for _, it := range resp.Data {
		b := baseAsset(it.InstID)
		switch it.InstCategory {
		case "3":
			out[b] = AssetStock
		case "4":
			out[b] = AssetCommodity
		default: // "1" and anything else → crypto
			out[b] = AssetCrypto
		}
	}
	setAssetClasses("okx", out)
	return len(out), nil
}

// RefreshAssetClassesBinance pulls the Binance USDⓈ-M exchangeInfo and classifies
// every base asset by contractType/underlyingType. TRADIFI_PERPETUAL splits into
// stock (KR_EQUITY/EQUITY) vs commodity (COMMODITY); everything else is crypto.
func RefreshAssetClassesBinance() (int, error) {
	body, err := binanceHTTPGet(baseURL + "/fapi/v1/exchangeInfo")
	if err != nil {
		return 0, fmt.Errorf("binance exchangeInfo fetch: %w", err)
	}
	var resp struct {
		Symbols []struct {
			Symbol         string `json:"symbol"`
			ContractType   string `json:"contractType"`
			UnderlyingType string `json:"underlyingType"`
			BaseAsset      string `json:"baseAsset"`
		} `json:"symbols"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, fmt.Errorf("binance exchangeInfo parse: %w", err)
	}
	out := make(map[string]AssetClass, len(resp.Symbols))
	for _, s := range resp.Symbols {
		b := strings.ToUpper(s.BaseAsset)
		if b == "" {
			b = baseAsset(s.Symbol)
		}
		switch strings.ToUpper(s.UnderlyingType) {
		case "KR_EQUITY", "EQUITY":
			out[b] = AssetStock
		case "COMMODITY":
			out[b] = AssetCommodity
		default: // COIN and anything else → crypto
			out[b] = AssetCrypto
		}
	}
	setAssetClasses("binance", out)
	return len(out), nil
}

// RefreshAssetClasses refreshes both exchanges (best-effort). Returns the total
// classified and the first error encountered (if any); a partial success still
// populates the cache from whichever exchange succeeded.
func RefreshAssetClasses() (int, error) {
	var firstErr error
	total := 0
	if n, err := RefreshAssetClassesOKX(); err != nil {
		firstErr = err
	} else {
		total += n
	}
	if n, err := RefreshAssetClassesBinance(); err != nil {
		if firstErr == nil {
			firstErr = err
		}
	} else {
		total += n
	}
	return total, firstErr
}

func okxHTTPGet(url string) ([]byte, error) {
	c := &http.Client{Timeout: 15 * time.Second}
	return httpGetBody(c, url)
}

func binanceHTTPGet(url string) ([]byte, error) {
	return httpGetBody(httpx.NewBinanceClient(15*time.Second), url)
}

func httpGetBody(c *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d from %s", resp.StatusCode, url)
	}
	return io.ReadAll(resp.Body)
}

// StartAssetClassRefresher does an immediate blocking-ish refresh (in a goroutine
// so startup is not delayed) and then refreshes on the given interval. Errors are
// logged, not fatal: an empty cache simply means every symbol is treated as crypto
// (24/7, no session restriction), i.e. current behaviour. Call once at startup.
func StartAssetClassRefresher(interval time.Duration, logf func(format string, args ...interface{})) {
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	go func() {
		n, err := RefreshAssetClasses()
		if err != nil {
			logf("⚠️ [AssetClass] initial refresh partial/failed: %v (classified=%d)", err, n)
		} else {
			logf("🏷️ [AssetClass] classified %d assets", n)
		}
		if interval <= 0 {
			return
		}
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			if n, err := RefreshAssetClasses(); err != nil {
				logf("⚠️ [AssetClass] refresh partial/failed: %v (classified=%d)", err, n)
			} else {
				logf("🏷️ [AssetClass] refreshed %d assets", n)
			}
		}
	}()
}
