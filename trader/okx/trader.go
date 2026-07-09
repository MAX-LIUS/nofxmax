package okx

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"nofx/logger"
	"nofx/trader/types"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// OKX API endpoints
const (
	okxBaseURL               = "https://www.okx.com"
	okxAccountPath           = "/api/v5/account/balance"
	okxPositionPath          = "/api/v5/account/positions"
	okxOrderPath             = "/api/v5/trade/order"
	okxLeveragePath          = "/api/v5/account/set-leverage"
	okxTickerPath            = "/api/v5/market/ticker"
	okxInstrumentsPath       = "/api/v5/public/instruments"
	okxCancelOrderPath       = "/api/v5/trade/cancel-order"
	okxPendingOrdersPath     = "/api/v5/trade/orders-pending"
	okxAlgoOrderPath         = "/api/v5/trade/order-algo"
	okxCancelAlgoPath        = "/api/v5/trade/cancel-algos"
	okxAlgoPendingPath       = "/api/v5/trade/orders-algo-pending"
	okxAdvanceAlgoPath       = "/api/v5/trade/order-algo"
	okxCancelAdvanceAlgoPath = "/api/v5/trade/cancel-advance-algos"
	okxPositionModePath      = "/api/v5/account/set-position-mode"
	okxAccountConfigPath     = "/api/v5/account/config"
)

// OKXTrader OKX futures trader
type OKXTrader struct {
	apiKey     string
	secretKey  string
	passphrase string

	// Margin mode setting
	isCrossMargin bool

	// Position mode: "long_short_mode" (hedge) or "net_mode" (one-way)
	positionMode string

	// HTTP client (proxy disabled)
	httpClient *http.Client

	// Balance cache
	cachedBalance     map[string]interface{}
	balanceCacheTime  time.Time
	balanceCacheMutex sync.RWMutex

	// Positions cache
	cachedPositions     []map[string]interface{}
	positionsCacheTime  time.Time
	positionsCacheMutex sync.RWMutex

	// Open-orders cache (per-symbol). Each GetOpenOrders does 3 serial OKX GETs
	// (limit + conditional algo + trailing); the dashboard loads protection orders
	// per position, so without a short cache N positions = N*3 serial round-trips.
	// TTL is short so the reconciler still sees fresh order state; placement/cancel
	// invalidate the symbol entry (fix 2026-06-10).
	cachedOpenOrders     map[string]cachedOpenOrderEntry
	openOrdersCacheMutex sync.RWMutex
	// Collapses concurrent GetOpenOrders calls for the same symbol into one OKX
	// fetch. Multiple monitor subsystems (protection reconciler, drawdown monitor,
	// risk arming) poll the same symbol within the same tick; without this they all
	// miss the short cache simultaneously and each fans out 3-4 algo-pending GETs,
	// saturating the per-key OKX rate limit (fix 2026-06-23).
	openOrdersSF singleflight.Group

	// Instrument info cache
	instrumentsCache      map[string]*OKXInstrument
	instrumentsCacheTime  time.Time
	instrumentsCacheMutex sync.RWMutex

	// Rate-limit backoff state
	rateLimitMutex sync.Mutex
	rateLimitUntil time.Time

	// Cache duration
	cacheDuration time.Duration
}

// cachedOpenOrderEntry holds a short-lived per-symbol GetOpenOrders result.
type cachedOpenOrderEntry struct {
	orders []types.OpenOrder
	at     time.Time
}

// OKXInstrument OKX instrument info
type OKXInstrument struct {
	InstID   string  // Instrument ID
	CtVal    float64 // Contract value
	CtMult   float64 // Contract multiplier
	LotSz    float64 // Minimum order size
	MinSz    float64 // Minimum order size
	MaxMktSz float64 // Maximum market order size
	TickSz   float64 // Minimum price increment
	CtType   string  // Contract type
}

// OKXResponse OKX API response
type OKXResponse struct {
	Code string          `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// OKX order tag
var okxTag = func() string {
	b, _ := base64.StdEncoding.DecodeString("NGMzNjNjODFlZGM1QkNERQ==")
	return string(b)
}()

func okxReasonTag(reason string) string {
	reason = strings.TrimSpace(strings.ToLower(reason))
	if reason == "" {
		return okxTag
	}
	tag := fmt.Sprintf("%s_%s", okxTag, reason)
	if len(tag) > 16 {
		tag = tag[:16]
	}
	return tag
}

// clOrdIDForReason returns a client order id that encodes the close mechanism
// when the reason has a registered code (see reason_codec.go), so the resulting
// close fill decodes its own reason exactly. Falls back to a plain random id when
// the reason is unknown/empty, preserving prior behaviour.
func clOrdIDForReason(reason string) string {
	if coded := encodeReasonClientID(reason); coded != "" {
		return coded
	}
	return genOkxClOrdID()
}

// genOkxClOrdID generates OKX order ID
func genOkxClOrdID() string {
	timestamp := time.Now().UnixNano() % 10000000000000
	randomBytes := make([]byte, 4)
	rand.Read(randomBytes)
	randomHex := hex.EncodeToString(randomBytes)
	// OKX clOrdId max 32 characters
	orderID := fmt.Sprintf("%s%d%s", okxTag, timestamp, randomHex)
	if len(orderID) > 32 {
		orderID = orderID[:32]
	}
	return orderID
}

// NewOKXTrader creates OKX trader
func NewOKXTrader(apiKey, secretKey, passphrase string) *OKXTrader {
	// Use a dedicated transport instead of http.DefaultTransport so we can tolerate
	// transient upstream EOF / reset issues without polluting global client behavior.
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	httpClient := &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}

	trader := &OKXTrader{
		apiKey:           apiKey,
		secretKey:        secretKey,
		passphrase:       passphrase,
		httpClient:       httpClient,
		cacheDuration:    15 * time.Second,
		instrumentsCache: make(map[string]*OKXInstrument),
		cachedOpenOrders: make(map[string]cachedOpenOrderEntry),
	}

	// Get current position mode first
	if err := trader.detectPositionMode(); err != nil {
		logger.Infof("⚠️ Failed to detect OKX position mode: %v, assuming dual mode", err)
		trader.positionMode = "long_short_mode"
	}

	// Try to set dual position mode (only if not already)
	if trader.positionMode != "long_short_mode" {
		if err := trader.setPositionMode(); err != nil {
			logger.Infof("⚠️ Failed to set OKX position mode: %v (current mode: %s)", err, trader.positionMode)
		}
	}

	logger.Infof("✓ OKX trader initialized with position mode: %s", trader.positionMode)
	return trader
}

// detectPositionMode gets current position mode from account config
func (t *OKXTrader) detectPositionMode() error {
	data, err := t.doRequest("GET", okxAccountConfigPath, nil)
	if err != nil {
		return fmt.Errorf("failed to get account config: %w", err)
	}

	var configs []struct {
		PosMode string `json:"posMode"`
	}

	if err := json.Unmarshal(data, &configs); err != nil {
		return fmt.Errorf("failed to parse account config: %w", err)
	}

	if len(configs) > 0 {
		t.positionMode = configs[0].PosMode
		logger.Infof("✓ Detected OKX position mode: %s", t.positionMode)
	}

	return nil
}

// setPositionMode sets dual position mode
func (t *OKXTrader) setPositionMode() error {
	body := map[string]string{
		"posMode": "long_short_mode", // Dual position mode
	}

	_, err := t.doRequest("POST", okxPositionModePath, body)
	if err != nil {
		// Ignore error if already in dual position mode
		if strings.Contains(err.Error(), "already") || strings.Contains(err.Error(), "Position mode is not modified") {
			logger.Infof("  ✓ OKX account is already in dual position mode")
			return nil
		}
		return err
	}

	logger.Infof("  ✓ OKX account switched to dual position mode")
	return nil
}

// sign generates OKX API signature
func (t *OKXTrader) sign(timestamp, method, requestPath, body string) string {
	preHash := timestamp + method + requestPath + body
	h := hmac.New(sha256.New, []byte(t.secretKey))
	h.Write([]byte(preHash))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// doRequest executes HTTP request
func (t *OKXTrader) doRequest(method, path string, body interface{}) ([]byte, error) {
	var bodyBytes []byte
	var err error

	if body != nil {
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to serialize request body: %w", err)
		}
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		t.waitForRateLimitBackoff()
		timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		signature := t.sign(timestamp, method, path, string(bodyBytes))

		req, err := http.NewRequest(method, okxBaseURL+path, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		req.Header.Set("OK-ACCESS-KEY", t.apiKey)
		req.Header.Set("OK-ACCESS-SIGN", signature)
		req.Header.Set("OK-ACCESS-TIMESTAMP", timestamp)
		req.Header.Set("OK-ACCESS-PASSPHRASE", t.passphrase)
		req.Header.Set("Content-Type", "application/json")
		// Set request header
		req.Header.Set("x-simulated-trading", "0")
		req.Header.Set("Connection", "keep-alive")

		resp, err := t.httpClient.Do(req)
		if err != nil {
			lastErr = err
			if shouldRetryOKXError(err) && attempt < 3 {
				logger.Infof("⚠️ OKX request retry %d/3 for %s %s after error: %v", attempt, method, path, err)
				time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
				continue
			}
			return nil, fmt.Errorf("request failed: %w", err)
		}

		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if attempt < 3 {
				logger.Infof("⚠️ OKX response read retry %d/3 for %s %s after error: %v", attempt, method, path, readErr)
				time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
				continue
			}
			return nil, fmt.Errorf("failed to read response: %w", readErr)
		}

		var okxResp OKXResponse
		if err := json.Unmarshal(respBody, &okxResp); err != nil {
			return nil, fmt.Errorf("failed to parse response: %w", err)
		}

		// code=1 indicates partial success, need to check specific results in data
		// code=2 indicates complete failure
		if okxResp.Code != "0" && okxResp.Code != "1" {
			if okxResp.Code == "50011" && attempt < 3 {
				delay := time.Duration(attempt) * 2 * time.Second
				t.noteRateLimitBackoff(delay)
				logger.Infof("⚠️ OKX rate limit retry %d/3 for %s %s after %s", attempt, method, path, delay)
				time.Sleep(delay)
				continue
			}
			return nil, fmt.Errorf("OKX API error: code=%s, msg=%s", okxResp.Code, okxResp.Msg)
		}

		return okxResp.Data, nil
	}

	return nil, fmt.Errorf("request failed after retries: %w", lastErr)
}

func (t *OKXTrader) waitForRateLimitBackoff() {
	t.rateLimitMutex.Lock()
	until := t.rateLimitUntil
	t.rateLimitMutex.Unlock()
	if until.IsZero() {
		return
	}
	if delay := time.Until(until); delay > 0 {
		time.Sleep(delay)
	}
}

func (t *OKXTrader) noteRateLimitBackoff(delay time.Duration) {
	if delay <= 0 {
		return
	}
	t.rateLimitMutex.Lock()
	until := time.Now().Add(delay)
	if until.After(t.rateLimitUntil) {
		t.rateLimitUntil = until
	}
	t.rateLimitMutex.Unlock()
}

func shouldRetryOKXError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "eof") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "tls handshake timeout")
}

// convertSymbol converts generic symbol to OKX format
// e.g. BTCUSDT -> BTC-USDT-SWAP
func (t *OKXTrader) convertSymbol(symbol string) string {
	// Remove USDT suffix and build OKX format
	base := strings.TrimSuffix(symbol, "USDT")
	return fmt.Sprintf("%s-USDT-SWAP", base)
}

// convertSymbolBack converts OKX format back to generic symbol
// e.g. BTC-USDT-SWAP -> BTCUSDT
func (t *OKXTrader) convertSymbolBack(instId string) string {
	parts := strings.Split(instId, "-")
	if len(parts) >= 2 {
		return parts[0] + parts[1]
	}
	return instId
}

// FormatQuantity formats quantity (converts base asset quantity to contract count)
func (t *OKXTrader) FormatQuantity(symbol string, quantity float64) (string, error) {
	inst, err := t.getInstrument(symbol)
	if err != nil {
		return fmt.Sprintf("%.3f", quantity), nil
	}

	// OKX uses contract count: quantity (in base asset) / ctVal (asset per contract)
	sz := quantity / inst.CtVal
	return t.formatSize(sz, inst), nil
}

// formatPrice rounds a price to the instrument's tick size so that the value
// sent to the OKX API matches what OKX will actually store. Without this
// rounding OKX silently truncates the price, which can cause verification
// mismatches when the plan target differs from the exchange order.
func (t *OKXTrader) formatPrice(price float64, inst *OKXInstrument) string {
	if inst.TickSz > 0 {
		// Round to the nearest multiple of tick size
		steps := math.Round(price / inst.TickSz)
		price = steps * inst.TickSz
	}
	// Determine decimal places from tick size string representation
	precision := tickSzPrecision(inst.TickSz)
	format := fmt.Sprintf("%%.%df", precision)
	return fmt.Sprintf(format, price)
}

// tickSzPrecision returns the number of decimal places implied by a tick size.
func tickSzPrecision(tickSz float64) int {
	if tickSz <= 0 || tickSz >= 1 {
		return 0
	}
	s := fmt.Sprintf("%f", tickSz)
	s = strings.TrimRight(s, "0")
	dot := strings.Index(s, ".")
	if dot == -1 {
		return 0
	}
	return len(s) - dot - 1
}

// formatSize formats contract size
func (t *OKXTrader) formatSize(sz float64, inst *OKXInstrument) string {
	// Determine precision based on lotSz
	if inst.LotSz >= 1 {
		return fmt.Sprintf("%.0f", sz)
	}

	// Calculate decimal places
	lotSzStr := fmt.Sprintf("%f", inst.LotSz)
	dotIndex := strings.Index(lotSzStr, ".")
	if dotIndex == -1 {
		return fmt.Sprintf("%.0f", sz)
	}

	// Remove trailing zeros
	lotSzStr = strings.TrimRight(lotSzStr, "0")
	precision := len(lotSzStr) - dotIndex - 1

	format := fmt.Sprintf("%%.%df", precision)
	return fmt.Sprintf(format, sz)
}

// closeSizeDecision is the outcome of resolving how many contracts to actually
// send on a (possibly partial) close, given exchange lot-size constraints.
type closeSizeDecision struct {
	SzStr  string // formatted sz to send (empty when Dust)
	Skip   bool   // true = do not send any order
	Dust   bool   // true = the FULL remaining position is below lotSz (uncloseable)
	Bumped bool   // true = requested partial rounded below lotSz, bumped up
	Reason string // human-readable explanation for logs
}

// resolveCloseSize decides the sz to send when closing `wantContracts` out of a
// position whose full remaining size is `fullContracts`. It fixes the sz=0 bug:
// a sub-lot partial close is bumped up to the minimum lot (capped at the full
// remaining), and a full remaining position that is itself below lotSz is flagged
// as Dust so callers stop retrying instead of spamming sz=0 rejects.
func (t *OKXTrader) resolveCloseSize(wantContracts, fullContracts float64, inst *OKXInstrument) closeSizeDecision {
	lot := inst.LotSz
	if lot <= 0 {
		lot = inst.MinSz
	}
	// Whole remaining position is below one lot -> genuine dust, can't be closed
	// by a standard lot-aligned order. Tell the caller to stop retrying.
	if lot > 0 && fullContracts > 0 && fullContracts < lot {
		return closeSizeDecision{Skip: true, Dust: true,
			Reason: fmt.Sprintf("full remaining %.8f < lotSz %.8f (sub-lot dust, uncloseable)", fullContracts, lot)}
	}
	if wantContracts <= 0 {
		return closeSizeDecision{Skip: true, Reason: "want<=0"}
	}
	// Cap the requested close at the full remaining.
	if wantContracts > fullContracts {
		wantContracts = fullContracts
	}
	send := wantContracts
	bumped := false
	// Requested partial rounds below one lot -> bump up to one lot (still <= full).
	if lot > 0 && send < lot {
		send = lot
		bumped = true
		if send > fullContracts {
			send = fullContracts
		}
	}
	szStr := t.formatSize(send, inst)
	// Final guard: never emit a non-positive sz.
	if v, err := strconv.ParseFloat(szStr, 64); err != nil || v <= 0 {
		return closeSizeDecision{Skip: true, Dust: true,
			Reason: fmt.Sprintf("formatted sz=%q non-positive (contracts=%.8f lotSz=%.8f)", szStr, send, lot)}
	}
	return closeSizeDecision{SzStr: szStr, Bumped: bumped,
		Reason: fmt.Sprintf("send %.8f contracts (lotSz=%.8f bumped=%v)", send, lot, bumped)}
}
