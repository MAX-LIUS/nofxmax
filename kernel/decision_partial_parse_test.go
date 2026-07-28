package kernel

import (
	"strings"
	"testing"
)

// 生产事故（周期 3416）：一个 alignment_notes 返回裸字符串,整批决策被丢。
// 实测 stdlib 行为比表面更糟——带自定义 Decision.UnmarshalJSON 时,解码在出错元素处
// 整体中止：该元素被清零(symbol/action 都没了),且排在它后面的元素根本不被解码。
// 所以一个说明性字段能连带丢掉后面的 close 决策。漏平比漏开危险,这才是"整数组原子性"
// 在这里是错误默认值的原因。
//
// 注意：本文件用的坏形状是 quality_score=[](数组),而不是 alignment_notes 裸字符串——
// 后者已由 AIStringList 容错(见 ai_string_list_test.go),不再触发失败。这里要测的是
// 逐元素隔离机制本身,所以需要一个当前仍然解不动的形状。
const badQualityScoreShape = `[]`

func TestPartialParseKeepsLaterCloseDecision(t *testing.T) {
	// 关键排布：坏元素在中间,close 排在它后面
	resp := "```json\n[" +
		`{"symbol":"AAAUSDT","action":"hold","reasoning":"first"},` +
		`{"symbol":"BBBUSDT","action":"open_long","quality_score":` + badQualityScoreShape + `,"reasoning":"malformed"},` +
		`{"symbol":"CCCUSDT","action":"close","reasoning":"must survive"}` +
		"]\n```"

	decisions, fallbackReason, err := extractDecisions(resp)
	if err != nil {
		t.Fatalf("一个坏元素不得让整批失败: %v", err)
	}
	if len(decisions) != 2 {
		t.Fatalf("应保住 2 条(坏的那条丢掉),实际 %d 条: %+v", len(decisions), decisions)
	}

	var sawClose, sawHold bool
	for _, d := range decisions {
		if d.Symbol == "CCCUSDT" && d.Action == "close" {
			sawClose = true
		}
		if d.Symbol == "AAAUSDT" && d.Action == "hold" {
			sawHold = true
		}
		if d.Symbol == "BBBUSDT" {
			t.Errorf("坏元素不得被放行执行: %+v", d)
		}
	}
	if !sawHold {
		t.Error("坏元素之前的决策丢失")
	}
	if !sawClose {
		t.Error("坏元素之后的 close 决策丢失 —— 这正是原缺陷最危险的部分")
	}

	// 必须可见地降级,而不是静默变少
	if fallbackReason == "" {
		t.Error("部分解析必须上报 fallbackReason,否则周期被静默削薄")
	}
	if !strings.Contains(fallbackReason, "partial_parse") {
		t.Errorf("fallbackReason 应标明 partial_parse,实际 %q", fallbackReason)
	}
	if !strings.Contains(fallbackReason, "BBBUSDT") {
		t.Errorf("fallbackReason 应指名被丢的标的,实际 %q", fallbackReason)
	}
}

func TestPartialParseAllGoodUnchanged(t *testing.T) {
	// 反向对照：全好时行为必须与从前一致(不报降级)
	resp := "```json\n[" +
		`{"symbol":"AAAUSDT","action":"hold","reasoning":"a"},` +
		`{"symbol":"BBBUSDT","action":"close","reasoning":"b"}` +
		"]\n```"

	decisions, fallbackReason, err := extractDecisions(resp)
	if err != nil {
		t.Fatalf("正常输入不得失败: %v", err)
	}
	if len(decisions) != 2 {
		t.Fatalf("应有 2 条,实际 %d", len(decisions))
	}
	if fallbackReason != "" {
		t.Errorf("全部正常时不得报降级,实际 %q", fallbackReason)
	}
}

func TestPartialParseAllBadStillFails(t *testing.T) {
	// 反向对照：一条都救不回来时,必须报错而不是返回空集当成"AI 说没事做"
	resp := "```json\n[" +
		`{"symbol":"AAAUSDT","action":"hold","quality_score":` + badQualityScoreShape + `},` +
		`{"symbol":"BBBUSDT","action":"close","quality_score":` + badQualityScoreShape + `}` +
		"]\n```"

	decisions, _, err := extractDecisions(resp)
	if err == nil {
		t.Fatalf("全坏时必须报错,否则会被误当作空决策集: %d 条 %+v", len(decisions), decisions)
	}
}

func TestSyntaxErrorStillFailsWhole(t *testing.T) {
	// 语法错误 ≠ 类型错误：结构都拆不开时没有可信数据,必须整批失败
	resp := "```json\n[" +
		`{"symbol":"AAAUSDT","action":"hold"},` +
		`{"symbol":` +
		"]\n```"

	if _, _, err := extractDecisions(resp); err == nil {
		t.Error("语法损坏的 JSON 必须整批失败")
	}
}
