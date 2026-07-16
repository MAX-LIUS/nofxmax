package okx

import (
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"nofx/store"
)

// End-to-end proof of the exact attribution loop: a close fill whose originating
// order carries the coded algoClOrdId we set at placement must be persisted with
// the DECODED mechanism as its OrderAction — via the order-detail path, NOT any
// price guess. This exercises SyncOrders -> GetOrderLinkedReason -> order-detail
// algoClOrdId -> decode -> UpsertSyncedOrder.OrderAction.
func TestSyncCodedCloseAttributedExactly(t *testing.T) {
	codedID := encodeReasonClientID("break_even_stop") // okxTag + "BE" + nonce
	if codedID == "" {
		t.Fatal("codec produced empty id for break_even_stop")
	}
	fills := fmt.Sprintf(`[
		{"instId":"BTC-USDT-SWAP","tradeId":"t-open","ordId":"o-open","billId":"b1","side":"buy","posSide":"long","fillPx":"100.0","fillSz":"2","fee":"-0.01","feeCcy":"USDT","ts":"1714260000000","execType":"T","tag":"entry"},
		{"instId":"BTC-USDT-SWAP","tradeId":"t-close","ordId":"o-close","billId":"b2","side":"sell","posSide":"long","fillPx":"98.0","fillSz":"2","fee":"-0.01","feeCcy":"USDT","ts":"1714260060000","execType":"T","tag":""}
	]`)

	tr := &OKXTrader{
		apiKey: "k", secretKey: "s", passphrase: "p",
		httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body := `{"code":"0","msg":"","data":[]}`
			switch {
			case strings.HasPrefix(req.URL.Path, "/api/v5/account/positions"):
				body = `{"code":"0","msg":"","data":[]}`
			case strings.HasPrefix(req.URL.Path, "/api/v5/trade/fills-history"):
				body = fmt.Sprintf(`{"code":"0","msg":"","data":%s}`, fills)
			case strings.HasPrefix(req.URL.Path, "/api/v5/trade/order"):
				// Order-detail: the close order carries the coded algoClOrdId. The
				// open order returns no algo id (a plain entry fill).
				if strings.Contains(req.URL.RawQuery, "o-close") {
					body = fmt.Sprintf(`{"code":"0","msg":"","data":[{"ordId":"o-close","state":"filled","avgPx":"98.0","accFillSz":"2","fee":"-0.01","side":"sell","ordType":"market","cTime":"1","uTime":"2","algoId":"algo-1","algoClOrdId":"%s"}]}`, codedID)
				} else {
					body = `{"code":"0","msg":"","data":[{"ordId":"o-open","state":"filled","avgPx":"100.0","accFillSz":"2","side":"buy","ordType":"market","cTime":"1","uTime":"2"}]}`
				}
			case strings.HasPrefix(req.URL.Path, "/api/v5/public/instruments"):
				body = `{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","ctVal":"0.0001","ctMult":"1","lotSz":"1","minSz":"1","maxMktSz":"1000000","tickSz":"0.1","ctType":"linear"}]}`
			default:
				t.Fatalf("unexpected path: %s", req.URL.Path)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
		})},
		instrumentsCache: make(map[string]*OKXInstrument),
	}
	st, err := store.New(filepath.Join(t.TempDir(), "coded-attr.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := tr.SyncOrdersFromOKX("trader-1", "exchange-1", "okx", st); err != nil {
		t.Fatalf("sync: %v", err)
	}
	closeOrder, err := st.Order().GetOrderByExchangeID("exchange-1", "t-close")
	if err != nil || closeOrder == nil {
		t.Fatalf("get close order: %v (order=%v)", err, closeOrder)
	}
	if closeOrder.OrderAction != "break_even_stop" {
		t.Fatalf("close attributed to %q, want break_even_stop (exact via coded algoClOrdId)", closeOrder.OrderAction)
	}
}

// A close fill whose order carries NO coded id and matches NO intent must be
// marked EXPLICITLY as "unresolved_exchange_close" — never guessed to a real
// mechanism (no fabrication), but also never left as a silent bare close that
// would later be dumped into sync_external. The explicit marker preserves the
// no-guessing invariant while making the attribution failure visible for
// forensics (see Phase 2 attribution fix, 2026-07-16).
func TestSyncUnattributedCloseNotGuessed(t *testing.T) {
	fills := `[
		{"instId":"BTC-USDT-SWAP","tradeId":"t-open","ordId":"o-open","billId":"b1","side":"buy","posSide":"long","fillPx":"100.0","fillSz":"2","fee":"-0.01","feeCcy":"USDT","ts":"1714260000000","execType":"T","tag":"entry"},
		{"instId":"BTC-USDT-SWAP","tradeId":"t-close","ordId":"o-close","billId":"b2","side":"sell","posSide":"long","fillPx":"98.0","fillSz":"2","fee":"-0.01","feeCcy":"USDT","ts":"1714260060000","execType":"T","tag":""}
	]`
	tr := &OKXTrader{
		apiKey: "k", secretKey: "s", passphrase: "p",
		httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body := `{"code":"0","msg":"","data":[]}`
			switch {
			case strings.HasPrefix(req.URL.Path, "/api/v5/account/positions"):
				body = `{"code":"0","msg":"","data":[]}`
			case strings.HasPrefix(req.URL.Path, "/api/v5/trade/fills-history"):
				body = fmt.Sprintf(`{"code":"0","msg":"","data":%s}`, fills)
			case strings.HasPrefix(req.URL.Path, "/api/v5/trade/order"):
				// No algo id, no coded client id: a bare exchange order.
				body = `{"code":"0","msg":"","data":[{"ordId":"o-close","state":"filled","avgPx":"98.0","accFillSz":"2","side":"sell","ordType":"market","cTime":"1","uTime":"2"}]}`
			case strings.HasPrefix(req.URL.Path, "/api/v5/public/instruments"):
				body = `{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","ctVal":"0.0001","ctMult":"1","lotSz":"1","minSz":"1","maxMktSz":"1000000","tickSz":"0.1","ctType":"linear"}]}`
			default:
				t.Fatalf("unexpected path: %s", req.URL.Path)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
		})},
		instrumentsCache: make(map[string]*OKXInstrument),
	}
	st, err := store.New(filepath.Join(t.TempDir(), "unattr.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := tr.SyncOrdersFromOKX("trader-1", "exchange-1", "okx", st); err != nil {
		t.Fatalf("sync: %v", err)
	}
	closeOrder, err := st.Order().GetOrderByExchangeID("exchange-1", "t-close")
	if err != nil || closeOrder == nil {
		t.Fatalf("get close order: %v", err)
	}
	// Must be the explicit unresolved marker — NOT a fabricated protection reason
	// (no guessing) and NOT a silent bare close (which later becomes sync_external).
	if closeOrder.OrderAction != "unresolved_exchange_close" {
		t.Fatalf("unattributed close became %q, want unresolved_exchange_close (explicit, no guessing)", closeOrder.OrderAction)
	}
}
