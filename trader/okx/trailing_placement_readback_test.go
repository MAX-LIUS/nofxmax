package okx

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// newTrailingPlacementTrader serves a POST order-algo that fails the way OKX 51149
// fails, plus a pending-algo read-back whose payload the caller controls.
// readbackFn builds the pending-algo response, receiving the algoClOrdId the
// placement actually sent (it carries a random nonce, so it cannot be precomputed).
type readbackFn func(sentClientID string) string

func newTrailingPlacementTrader(readback readbackFn, postCalls, getCalls *int) *OKXTrader {
	tr := &OKXTrader{
		apiKey: "k", secretKey: "s", passphrase: "p",
		instrumentsCache: map[string]*OKXInstrument{
			"ETH-USDT-SWAP": {InstID: "ETH-USDT-SWAP", CtVal: 0.1, CtMult: 1, LotSz: 0.01, MinSz: 0.01, TickSz: 0.01, CtType: "linear"},
		},
		cachedOpenOrders: map[string]cachedOpenOrderEntry{},
	}
	tr.instrumentsCacheTime = time.Now()
	sentClientID := ""
	tr.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"code":"0","msg":"","data":[]}`
		switch {
		case req.Method == "POST" && strings.HasPrefix(req.URL.Path, okxAdvanceAlgoPath):
			*postCalls++
			if req.Body != nil {
				raw, _ := io.ReadAll(req.Body)
				var sent struct {
					AlgoClOrdID string `json:"algoClOrdId"`
				}
				_ = json.Unmarshal(raw, &sent)
				sentClientID = sent.AlgoClOrdID
			}
			// The production shape: envelope-level failure, no data payload.
			body = `{"code":"51149","msg":"Order timed out. Please try again.","data":[]}`
		case req.Method == "GET" && strings.HasPrefix(req.URL.Path, okxAlgoPendingPath):
			*getCalls++
			body = readback(sentClientID)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}
	return tr
}

// TestTrailingPlacementAdoptsOrderThatLandedDespiteError pins that an ambiguous
// placement error is reconciled by reading back the client id before being reported
// as a failure.
//
// Production incident (2026-07-31 22:23:33 ETHUSDT short): OKX returned
// code=51149 "Order timed out" and the code treated it as a hard failure, discarding
// the placement. But the algo HAD landed — 3791832449865392128 showed up in OKX's
// pending list one second later and order_sync recorded it. With no algoId captured,
// the ownership ledger could not claim it, so it went unmaintained, OKX withdrew it,
// and the arm loop re-placed a duplicate for the same tier 10s later. All 6 trailing
// placement failures in 4 days of production were this one error code.
func TestTrailingPlacementAdoptsOrderThatLandedDespiteError(t *testing.T) {
	var postCalls, getCalls int
	// The exchange echoes back the client id the placement sent — that is precisely
	// what makes it usable as an idempotency key.
	readback := func(sentClientID string) string {
		if sentClientID == "" {
			t.Error("placement sent no algoClOrdId, so no read-back key exists")
		}
		return `{"code":"0","msg":"","data":[
			{"algoId":"3791832449865392128","algoClOrdId":"` + sentClientID + `"}
		]}`
	}

	tr := newTrailingPlacementTrader(readback, &postCalls, &getCalls)
	algoID, err := tr.SetTrailingStopLossTaggedWithID("ETHUSDT", "SHORT", 1833.77, 0.010917, 0.156, "native_trailing#1")
	if err != nil {
		t.Fatalf("placement reported failure for an order that landed: %v", err)
	}
	if algoID != "3791832449865392128" {
		t.Fatalf("adopted algoId = %q, want the id that actually landed", algoID)
	}
	if getCalls == 0 {
		t.Fatal("no read-back was attempted; the timeout was taken at face value")
	}
}

// TestTrailingPlacementStillFailsWhenNothingLanded pins the other half: a genuine
// failure must stay a failure. Adopting on an empty read-back would report phantom
// coverage and leave the tier with no executor at all.
func TestTrailingPlacementStillFailsWhenNothingLanded(t *testing.T) {
	var postCalls, getCalls int
	tr := newTrailingPlacementTrader(func(string) string { return `{"code":"0","msg":"","data":[]}` }, &postCalls, &getCalls)
	if _, err := tr.SetTrailingStopLossTaggedWithID("ETHUSDT", "SHORT", 1833.77, 0.010917, 0.156, "native_trailing#1"); err == nil {
		t.Fatal("placement reported success though nothing is on the exchange")
	}
	if getCalls == 0 {
		t.Fatal("no read-back was attempted")
	}
}

// TestTrailingReadBackRejectsForeignClientID pins that the read-back matches the
// client id explicitly. OKX endpoints that ignore an unknown filter return the full
// list; adopting whatever came back would bind this tier to another tier's order —
// one tier double-claimed, the other silently unprotected.
func TestTrailingReadBackRejectsForeignClientID(t *testing.T) {
	var postCalls, getCalls int
	readback := func(string) string {
		return `{"code":"0","msg":"","data":[
			{"algoId":"9999999999999999999","algoClOrdId":"someoneElsesClientId"}
		]}`
	}
	tr := newTrailingPlacementTrader(readback, &postCalls, &getCalls)
	if _, err := tr.SetTrailingStopLossTaggedWithID("ETHUSDT", "SHORT", 1833.77, 0.010917, 0.156, "native_trailing#1"); err == nil {
		t.Fatal("adopted an order belonging to a different client id")
	}
}
