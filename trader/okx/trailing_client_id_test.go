package okx

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"nofx/store"
)

// newTrailingProbeTrader serves a fixed instrument and a successful algo response,
// capturing the POST body sent to the advance-algo endpoint. The instrument cache is
// pre-seeded so the test never depends on the instruments endpoint shape.
func newTrailingProbeTrader(captured *map[string]interface{}) *OKXTrader {
	var mu sync.Mutex
	t := &OKXTrader{
		apiKey: "k", secretKey: "s", passphrase: "p",
		instrumentsCache: map[string]*OKXInstrument{
			"ETH-USDT-SWAP": {InstID: "ETH-USDT-SWAP", CtVal: 0.01, CtMult: 1, LotSz: 0.1, MinSz: 0.1, TickSz: 0.01, CtType: "linear"},
		},
		cachedOpenOrders: map[string]cachedOpenOrderEntry{},
	}
	t.instrumentsCacheTime = time.Now()
	t.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"code":"0","msg":"","data":[]}`
		if strings.HasPrefix(req.URL.Path, okxAdvanceAlgoPath) && req.Method == http.MethodPost {
			var parsed map[string]interface{}
			if req.Body != nil {
				_ = json.NewDecoder(req.Body).Decode(&parsed)
			}
			mu.Lock()
			*captured = parsed
			mu.Unlock()
			body = `{"code":"0","msg":"","data":[{"algoId":"algo-trail-1","sCode":"0","sMsg":""}]}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}
	return t
}

// TestTrailingPlacementCarriesCodedClientID pins the placement half of the fix: the
// OKX trailing order must carry a decodable algoClOrdId. The tag field is fully
// consumed by okxTag (16 chars, no room for a reason), so without this id the
// mechanism behind a resting trailing order is unrecoverable and targeted cleanup
// cannot tell it apart from any other algo on the same symbol.
func TestTrailingPlacementCarriesCodedClientID(t *testing.T) {
	for _, reason := range []string{"ladder_sl", "break_even_stop", "managed_drawdown_stage2"} {
		var body map[string]interface{}
		tr := newTrailingProbeTrader(&body)

		// quantity > 0 keeps the call off the position-lookup path.
		algoID, err := tr.SetTrailingStopLossTaggedWithID("ETHUSDT", "SHORT", 1900, 0.8, 0.5, reason)
		if err != nil {
			t.Fatalf("reason %s: placement failed: %v", reason, err)
		}
		if algoID != "algo-trail-1" {
			t.Fatalf("reason %s: expected algoId from response, got %q", reason, algoID)
		}
		if body == nil {
			t.Fatalf("reason %s: advance-algo endpoint was never called", reason)
		}

		// The tag stays the bare broker tag — that is exactly why it cannot carry a reason.
		if got, _ := body["tag"].(string); got != okxTag {
			t.Fatalf("reason %s: tag should be the bare broker tag %q, got %q", reason, okxTag, got)
		}

		clID, _ := body["algoClOrdId"].(string)
		if clID == "" {
			t.Fatalf("reason %s: algoClOrdId missing — the mechanism would be unrecoverable", reason)
		}
		want := store.NormalizeMechanism(reason)
		if decoded := decodeReasonFromClientID(clID); decoded != want {
			t.Fatalf("reason %s: algoClOrdId %q decodes to %q, want %q", reason, clID, decoded, want)
		}
	}
}

// TestTrailingPlacementClientIDsAreUnique guards the nonce: two trailing tiers armed
// for the same mechanism must not collide on algoClOrdId, or OKX would reject the
// second placement as a duplicate client id.
func TestTrailingPlacementClientIDsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		var body map[string]interface{}
		tr := newTrailingProbeTrader(&body)
		if err := tr.SetTrailingStopLossTagged("ETHUSDT", "SHORT", 1900, 0.8, 0.5, "ladder_sl"); err != nil {
			t.Fatalf("placement %d failed: %v", i, err)
		}
		clID, _ := body["algoClOrdId"].(string)
		if clID == "" {
			t.Fatalf("placement %d produced no algoClOrdId", i)
		}
		if seen[clID] {
			t.Fatalf("duplicate algoClOrdId %q on placement %d — OKX would reject it", clID, i)
		}
		seen[clID] = true
	}
}
