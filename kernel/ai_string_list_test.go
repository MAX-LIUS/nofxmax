package kernel

import (
	"encoding/json"
	"testing"
)

// 钉住 2026-07-28 17:32 生产实况:claude trader 把 alignment_notes 返回成裸字符串,
// 整个 cycle 3416 的决策全被丢弃(含同批那笔 ZECUSDT open_long)。
// 一个纯说明性字段的形状,代价是一轮交易。
func TestAlignmentNotesAcceptsBareString(t *testing.T) {
	// 生产原文(截断保留首尾),裸字符串形。
	raw := `{
      "timeframe_context": {"primary": "15m", "lower": ["5m"], "higher": ["1h"]},
      "alignment_notes": "15m 与 5m 在低位支撑出现拒绝，买方 CVD 占优，OI 小幅增加。"
    }`
	var r AIEntryProtectionRationale
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("裸字符串形的 alignment_notes 不得让整批决策解析失败: %v", err)
	}
	if len(r.AlignmentNotes) != 1 {
		t.Fatalf("裸字符串应收成单元素,得到 %d 个: %v", len(r.AlignmentNotes), r.AlignmentNotes)
	}
	if r.TimeframeContext.Primary != "15m" || len(r.TimeframeContext.Lower) != 1 {
		t.Fatalf("同一对象里其它字段必须照常解析: %+v", r.TimeframeContext)
	}
}

// 数组形(schema 正例)必须保持原样 —— 宽容不能改变正常路径。
func TestAlignmentNotesArrayFormUnchanged(t *testing.T) {
	raw := `{"alignment_notes": ["a", "b", "c"]}`
	var r AIEntryProtectionRationale
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("数组形必须照常解析: %v", err)
	}
	if len(r.AlignmentNotes) != 3 || r.AlignmentNotes[0] != "a" || r.AlignmentNotes[2] != "c" {
		t.Fatalf("数组形内容或顺序被改动: %v", r.AlignmentNotes)
	}
}

// timeframe_context 的 lower/higher 同样是 AI 填的 []string —— 只修
// alignment_notes 一处的话,下次轮到它们炸。这条钉住"一次覆盖三处"。
func TestTimeframeContextAcceptsBareStrings(t *testing.T) {
	raw := `{"primary": "1h", "lower": "15m", "higher": "4h"}`
	var c AIEntryTimeframeContext
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("lower/higher 裸字符串形不得解析失败: %v", err)
	}
	if len(c.Lower) != 1 || c.Lower[0] != "15m" {
		t.Fatalf("lower 裸字符串应收成 [15m], 得到 %v", c.Lower)
	}
	if len(c.Higher) != 1 || c.Higher[0] != "4h" {
		t.Fatalf("higher 裸字符串应收成 [4h], 得到 %v", c.Higher)
	}
}

func TestAIStringListEdgeCases(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int
	}{
		{"null 归一为空", `null`, 0},
		{"空数组", `[]`, 0},
		{"空串归一为空(不是 [\"\"])", `""`, 0},
		{"纯空白串归一为空", `"   "`, 0},
		{"数字标量也不炸", `42`, 1},
	}
	for _, tc := range cases {
		var l AIStringList
		if err := json.Unmarshal([]byte(tc.raw), &l); err != nil {
			t.Fatalf("%s: 不得报错: %v", tc.name, err)
		}
		if len(l) != tc.want {
			t.Fatalf("%s: 期望 %d 个元素, 得到 %d (%v)", tc.name, tc.want, len(l), l)
		}
	}
}

// 整批决策数组的端到端:生产上炸掉的正是这一层(extract decisions)。
// 第二笔带裸字符串 alignment_notes,两笔都必须活下来。
func TestDecisionBatchSurvivesBareStringRationale(t *testing.T) {
	raw := `[
      {"symbol": "WLDUSDT", "action": "hold"},
      {"symbol": "ZECUSDT", "action": "open_long", "leverage": 8,
       "entry_protection_rationale": {"alignment_notes": "低位支撑拒绝"}}
    ]`
	var decisions []Decision
	if err := json.Unmarshal([]byte(raw), &decisions); err != nil {
		t.Fatalf("整批决策不得因一个说明性字段的形状被丢弃: %v", err)
	}
	if len(decisions) != 2 {
		t.Fatalf("两笔决策都应保留, 得到 %d", len(decisions))
	}
	if decisions[1].Symbol != "ZECUSDT" || decisions[1].Action != "open_long" {
		t.Fatalf("第二笔决策内容不对: %+v", decisions[1])
	}
}
