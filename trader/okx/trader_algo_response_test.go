package okx

import (
	"errors"
	"strings"
	"testing"
)

func TestParseOKXAlgoOrderResponseSuccess(t *testing.T) {
	algoID, err := parseOKXAlgoOrderResponse([]byte(`[{"algoId":"123","sCode":"0","sMsg":""}]`), "stop loss")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if algoID != "123" {
		t.Fatalf("expected algo id 123, got %q", algoID)
	}
}

func TestParseOKXAlgoOrderResponseRejectsBusinessFailure(t *testing.T) {
	_, err := parseOKXAlgoOrderResponse([]byte(`[{"algoId":"","sCode":"51280","sMsg":"trigger price invalid"}]`), "stop loss")
	if err == nil {
		t.Fatal("expected rejection error")
	}
	if got := err.Error(); got != "OKX stop loss rejected: code=51280 msg=trigger price invalid" {
		t.Fatalf("unexpected error: %s", got)
	}
	if errors.Is(err, ErrTriggerPriceAlreadyPassed) {
		t.Fatal("51280 不是'触发价已被越过',不得被判为可跳过")
	}
}

// 51277/51278 是"触发价已被市价越过":这一档要么正在成交、要么已无意义,
// 不是挂单失败。必须能被 errors.Is 识别,否则(2026-07-31 线上 BTCUSDT SHORT)
// 一个已越过的 TP 档会让整份保护计划连坐:重试 2 次 → 标不可重试 → ❌ reconcile failed,
// 而实际上另外 3 个 TP 档和 SL 都已挂上。
func TestParseOKXAlgoOrderResponseFlagsTriggerAlreadyPassed(t *testing.T) {
	cases := []struct {
		code string
		msg  string
	}{
		{"51277", "TP trigger price cannot be higher than the last price"},
		{"51278", "TP trigger price cannot be lower than the last price"},
	}
	for _, c := range cases {
		body := []byte(`[{"algoId":"","sCode":"` + c.code + `","sMsg":"` + c.msg + `"}]`)
		_, err := parseOKXAlgoOrderResponse(body, "take profit")
		if err == nil {
			t.Fatalf("%s: expected rejection error", c.code)
		}
		if !errors.Is(err, ErrTriggerPriceAlreadyPassed) {
			t.Fatalf("%s 必须可被识别为触发价已越过,got %v", c.code, err)
		}
		// 原始 code/msg 必须仍留在错误串里,否则线上无从判断是哪一档被跳过。
		if !strings.Contains(err.Error(), c.code) || !strings.Contains(err.Error(), c.msg) {
			t.Fatalf("%s: 错误串必须保留原始 code 与 msg,got %s", c.code, err.Error())
		}
	}
}

// 反向锁:白名单之外的拒因一律不得被判成可跳过 —— 静默吞掉挂单失败比连坐危险得多。
// 这里特意覆盖形近码(5127x 段)与参数类错误。
func TestParseOKXAlgoOrderResponseNeverSwallowsRealRejections(t *testing.T) {
	for _, code := range []string{"51000", "51004", "51008", "51276", "51279", "51280", "51400", "1"} {
		body := []byte(`[{"algoId":"","sCode":"` + code + `","sMsg":"some rejection"}]`)
		_, err := parseOKXAlgoOrderResponse(body, "take profit")
		if err == nil {
			t.Fatalf("%s: expected rejection error", code)
		}
		if errors.Is(err, ErrTriggerPriceAlreadyPassed) {
			t.Fatalf("code=%s 不在白名单内,不得被判为可跳过(会静默吞掉挂单失败)", code)
		}
	}
}

// 成功响应不得携带 sentinel。
func TestParseOKXAlgoOrderResponseSuccessCarriesNoSentinel(t *testing.T) {
	if _, err := parseOKXAlgoOrderResponse([]byte(`[{"algoId":"9","sCode":"0","sMsg":""}]`), "take profit"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

// 畸形/空响应仍必须是普通错误,不能因为解析不出 code 就退化成"可跳过"。
func TestParseOKXAlgoOrderResponseMalformedIsNotSkippable(t *testing.T) {
	for _, body := range []string{`[]`, `not json`, `[{}]`} {
		_, err := parseOKXAlgoOrderResponse([]byte(body), "take profit")
		if err == nil {
			t.Fatalf("%s: expected error", body)
		}
		if errors.Is(err, ErrTriggerPriceAlreadyPassed) {
			t.Fatalf("%s: 畸形响应不得被判为可跳过", body)
		}
	}
}
