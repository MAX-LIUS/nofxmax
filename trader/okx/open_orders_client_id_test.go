package okx

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"nofx/store"
)

// newOpenOrdersProbeTrader serves the three endpoints GetOpenOrders walks: the
// regular order list (empty), the conditional-algo list, and the trailing-algo list.
func newOpenOrdersProbeTrader(conditionalJSON, trailingJSON string) *OKXTrader {
	tr := &OKXTrader{
		apiKey: "k", secretKey: "s", passphrase: "p",
		instrumentsCache: map[string]*OKXInstrument{
			"CL-USDT-SWAP": {InstID: "CL-USDT-SWAP", CtVal: 0.1, CtMult: 1, LotSz: 1, MinSz: 1, TickSz: 0.01, CtType: "linear"},
		},
		cachedOpenOrders: map[string]cachedOpenOrderEntry{},
	}
	tr.instrumentsCacheTime = time.Now()
	tr.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"code":"0","msg":"","data":[]}`
		if strings.HasPrefix(req.URL.Path, okxAlgoPendingPath) {
			if strings.Contains(req.URL.RawQuery, "move_order_stop") {
				body = trailingJSON
			} else {
				body = conditionalJSON
			}
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}
	return tr
}

// TestGetOpenOrdersSurfacesCodedClientID pins that GetOpenOrders reports the
// per-order client id, not the broker tag.
//
// Reading ClientOrderID from Tag made every bot order on a symbol carry the same
// 16-char constant. That value is non-empty but carries zero per-instance
// information, and non-empty is exactly what suppresses the plan-based fallback in
// enrichProtectionOrdersWithPlan (it only infers when the field is empty) — so a
// useless value was crowding out a computable one. The trailing branch had the same
// defect twice over: ProtectionRole came from protectionReasonFromTag(Tag), which
// can never match now that okxTag consumes the whole 16-char tag budget.
func TestGetOpenOrdersSurfacesCodedClientID(t *testing.T) {
	slID := encodeReasonClientID("ladder_sl")
	tpID := encodeReasonClientID("ladder_tp")
	trailID := encodeReasonClientID("managed_drawdown_stage1")
	for name, id := range map[string]string{"ladder_sl": slID, "ladder_tp": tpID, "trailing": trailID} {
		if id == "" {
			t.Fatalf("%s: codec produced no client id, test would prove nothing", name)
		}
	}

	conditional := fmt.Sprintf(`{"code":"0","msg":"","data":[
		{"algoId":"a-sl","instId":"CL-USDT-SWAP","side":"buy","posSide":"short","ordType":"conditional","slTriggerPx":"86.02","sz":"24","state":"live","tag":"%s","algoClOrdId":"%s"},
		{"algoId":"a-tp","instId":"CL-USDT-SWAP","side":"buy","posSide":"short","ordType":"conditional","tpTriggerPx":"82.72","sz":"6","state":"live","tag":"%s","algoClOrdId":"%s"}
	]}`, okxTag, slID, okxTag, tpID)
	trailing := fmt.Sprintf(`{"code":"0","msg":"","data":[
		{"algoId":"a-trail","instId":"CL-USDT-SWAP","side":"buy","posSide":"short","activePx":"81.06","callbackRatio":"0.55","moveTriggerPx":"0","sz":"28","tag":"%s","algoClOrdId":"%s"}
	]}`, okxTag, trailID)

	orders, err := newOpenOrdersProbeTrader(conditional, trailing).GetOpenOrders("CLUSDT")
	if err != nil {
		t.Fatalf("GetOpenOrders failed: %v", err)
	}
	if len(orders) != 3 {
		t.Fatalf("expected 3 orders (sl+tp+trailing), got %d", len(orders))
	}

	for _, o := range orders {
		if o.ClientOrderID == okxTag {
			t.Fatalf("%s order still reports the bare broker tag as its client id", o.Type)
		}
		if o.ClientOrderID == "" {
			t.Fatalf("%s order lost its client id entirely", o.Type)
		}
		if decodeReasonFromClientID(o.ClientOrderID) == "" {
			t.Fatalf("%s order client id %q does not decode to a mechanism", o.Type, o.ClientOrderID)
		}
	}

	foundTrailing := false
	for _, o := range orders {
		if !strings.Contains(o.Type, "TRAILING") {
			continue
		}
		foundTrailing = true
		// The codec folds dynamic stage variants onto one mechanism, so the round
		// trip of managed_drawdown_stage1 is managed_drawdown by design.
		want := store.NormalizeMechanism("managed_drawdown_stage1")
		if o.ProtectionRole != want {
			t.Fatalf("trailing ProtectionRole = %q, want %q (decoded from algoClOrdId)", o.ProtectionRole, want)
		}
	}
	if !foundTrailing {
		t.Fatal("no trailing order surfaced")
	}
}

// TestGetOpenOrdersLeavesLegacyClientIDEmpty covers orders placed before the reason
// moved into the client id. Reporting empty is the useful answer: it lets the
// plan-based fallback infer a role from price. Back-filling the tag would look
// authoritative while saying nothing.
func TestGetOpenOrdersLeavesLegacyClientIDEmpty(t *testing.T) {
	conditional := fmt.Sprintf(`{"code":"0","msg":"","data":[
		{"algoId":"a-legacy","instId":"CL-USDT-SWAP","side":"buy","posSide":"short","ordType":"conditional","slTriggerPx":"86.02","sz":"24","state":"live","tag":"%s","algoClOrdId":""}
	]}`, okxTag)

	orders, err := newOpenOrdersProbeTrader(conditional, `{"code":"0","msg":"","data":[]}`).GetOpenOrders("CLUSDT")
	if err != nil {
		t.Fatalf("GetOpenOrders failed: %v", err)
	}
	if len(orders) != 1 {
		t.Fatalf("expected 1 order, got %d", len(orders))
	}
	if orders[0].ClientOrderID != "" {
		t.Fatalf("legacy order client id = %q, want empty so plan-based inference can run", orders[0].ClientOrderID)
	}
}
