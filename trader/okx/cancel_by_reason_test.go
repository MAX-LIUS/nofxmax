package okx

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// newCancelProbeTrader returns a trader whose HTTP layer serves a fixed pending-algo
// list and records every algoId the code under test asks to cancel.
func newCancelProbeTrader(pendingJSON string, cancelled *[]string) *OKXTrader {
	return &OKXTrader{
		apiKey: "k", secretKey: "s", passphrase: "p",
		httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body := `{"code":"0","msg":"","data":[]}`
			switch {
			case strings.HasPrefix(req.URL.Path, okxAlgoPendingPath):
				body = pendingJSON
			case strings.HasPrefix(req.URL.Path, okxCancelAlgoPath),
				strings.HasPrefix(req.URL.Path, okxCancelAdvanceAlgoPath):
				var reqBody []map[string]interface{}
				if req.Body != nil {
					_ = json.NewDecoder(req.Body).Decode(&reqBody)
				}
				for _, b := range reqBody {
					if id, ok := b["algoId"].(string); ok {
						*cancelled = append(*cancelled, id)
					}
				}
				body = `{"code":"0","msg":"","data":[{"sCode":"0"}]}`
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		})},
	}
}

// pendingWithCodedIDs builds a realistic conditional-algo list: one ladder_sl, one
// break_even_stop, two ladder_tp, plus one order carrying a foreign client id.
// Every one of them has tag == okxTag, which is exactly why tag-based filtering
// could not tell them apart.
func pendingWithCodedIDs() string {
	mk := func(algoID, clOrdID string) string {
		return fmt.Sprintf(`{"algoId":"%s","instId":"ETH-USDT-SWAP","ordType":"conditional","tag":"%s","algoClOrdId":"%s"}`,
			algoID, okxTag, clOrdID)
	}
	return `{"code":"0","msg":"","data":[` + strings.Join([]string{
		mk("algo-ladder-sl", okxTag+"LS"+"aaaaaaaaaaaa"),
		mk("algo-break-even", okxTag+"BE"+"bbbbbbbbbbbb"),
		mk("algo-ladder-tp-1", okxTag+"LT"+"cccccccccccc"),
		mk("algo-ladder-tp-2", okxTag+"LT"+"dddddddddddd"),
		mk("algo-foreign", "someone-elses-id"),
	}, ",") + `]}`
}

// 旧实现按 tag 过滤,而 okxReasonTag(reason) 恒等于 okxTag,所以"只撤 ladder_sl"
// 实际会撤掉该 symbol 上所有 bot conditional 单(止损档和止盈档都是 conditional)。
// 这里钉住:只有 mechanism 真正匹配的那一张会被撤。
func TestCancelByReasonOnlyCancelsMatchingMechanism(t *testing.T) {
	var cancelled []string
	tr := newCancelProbeTrader(pendingWithCodedIDs(), &cancelled)

	if err := tr.CancelStopLossOrdersTagged("ETHUSDT", "ladder_sl"); err != nil {
		t.Fatalf("cancel ladder_sl: %v", err)
	}

	if len(cancelled) != 1 || cancelled[0] != "algo-ladder-sl" {
		t.Fatalf("只应撤 ladder_sl 那一张,got %v", cancelled)
	}

	// 反向验证:旧语义(按 tag 相等)会命中全部 4 张 bot 单 —— 证明这个用例
	// 确实覆盖了广撤 bug,而不是构造了一个天然只有一张的场景。
	var botTagged int
	var orders []struct {
		AlgoID string `json:"algoId"`
		Tag    string `json:"tag"`
	}
	var payload struct{ Data json.RawMessage }
	_ = json.Unmarshal([]byte(pendingWithCodedIDs()), &payload)
	_ = json.Unmarshal(payload.Data, &orders)
	for _, o := range orders {
		if o.Tag == okxTag {
			botTagged++
		}
	}
	if botTagged < 4 {
		t.Fatalf("测试构造无效:tag==okxTag 的单只有 %d 张,钉不住广撤 bug", botTagged)
	}
}

// 无法解码 client id 的单必须跳过而不是撤掉:少撤可恢复,误撤活着的止损不可恢复。
func TestCancelByReasonSkipsUndecodableClientIDs(t *testing.T) {
	pending := `{"code":"0","msg":"","data":[` +
		fmt.Sprintf(`{"algoId":"algo-no-clid","instId":"ETH-USDT-SWAP","ordType":"conditional","tag":"%s","algoClOrdId":""},`, okxTag) +
		fmt.Sprintf(`{"algoId":"algo-bad-code","instId":"ETH-USDT-SWAP","ordType":"conditional","tag":"%s","algoClOrdId":"%sZZ0011223344"}`, okxTag, okxTag) +
		`]}`

	var cancelled []string
	tr := newCancelProbeTrader(pending, &cancelled)
	if err := tr.CancelStopLossOrdersTagged("ETHUSDT", "ladder_sl"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if len(cancelled) != 0 {
		t.Fatalf("不可解码的单必须跳过,got cancelled=%v", cancelled)
	}
}

// reason 没有注册 code 时必须整体拒绝,而不是退化成"撤全部"。
func TestCancelByReasonRefusesUnregisteredReason(t *testing.T) {
	var cancelled []string
	tr := newCancelProbeTrader(pendingWithCodedIDs(), &cancelled)
	if err := tr.CancelStopLossOrdersTagged("ETHUSDT", "totally_unknown_reason"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if len(cancelled) != 0 {
		t.Fatalf("未注册 reason 必须一张都不撤,got %v", cancelled)
	}
}

// managed_drawdown_<stage> 这类动态变体必须能折叠到基础 mechanism 并命中。
func TestCancelByReasonFoldsDynamicVariants(t *testing.T) {
	pending := `{"code":"0","msg":"","data":[` +
		fmt.Sprintf(`{"algoId":"algo-dd","instId":"ETH-USDT-SWAP","ordType":"conditional","tag":"%s","algoClOrdId":"%sDD0011223344"}`, okxTag, okxTag) +
		`]}`

	var cancelled []string
	tr := newCancelProbeTrader(pending, &cancelled)
	if err := tr.CancelStopLossOrdersTagged("ETHUSDT", "managed_drawdown_stage2"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if len(cancelled) != 1 || cancelled[0] != "algo-dd" {
		t.Fatalf("managed_drawdown_stage2 应折叠到 managed_drawdown 并命中,got %v", cancelled)
	}
}
