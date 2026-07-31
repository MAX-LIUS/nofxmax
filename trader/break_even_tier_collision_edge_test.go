package trader

import (
	"path/filepath"
	"sync"
	"testing"

	"nofx/store"
)

// 本文件是「一张单两个主人」修复的破坏性/边界锁。
// 目标不是覆盖率,而是钉住:异常输入下这两个函数**宁可不动手**,绝不误退役活单的主人。

func newEdgeTrader(t *testing.T, name string) (*AutoTrader, *store.Store) {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	return &AutoTrader{id: "t1", store: st, protectionStateMutex: sync.RWMutex{}}, st
}

func edgeBERecord(stage, fingerprint, orderID string) store.DynamicProtectionRecord {
	return store.DynamicProtectionRecord{
		TraderID: "t1", Symbol: "ETHUSDT", Side: "short",
		ProtectionType:  "break_even_stop",
		RuleFingerprint: fingerprint,
		Status:          "armed", ExchangeOrderID: orderID,
	}
}

func armedIdentities(t *testing.T, st *store.Store, orderID string) []string {
	t.Helper()
	state, err := st.LoadDynamicProtectionState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	out := []string{}
	for _, r := range state.Records {
		if r.Status == "armed" && r.ExchangeOrderID == orderID {
			out = append(out, drawdownRuleIdentity(r.RuleFingerprint))
		}
	}
	return out
}

// 边界①:store 为 nil / 接收者为 nil / stage 为空 —— 必须静默返回,不 panic。
func TestRetireSuppressedTierIsNilAndEmptySafe(t *testing.T) {
	var nilTrader *AutoTrader
	nilTrader.retireSuppressedBreakEvenTierRecord("ETHUSDT", "short", "BE2", "BE1")

	(&AutoTrader{id: "t1"}).retireSuppressedBreakEvenTierRecord("ETHUSDT", "short", "BE2", "BE1")

	at, st := newEdgeTrader(t, "retire-empty.db")
	if err := st.SaveDynamicProtectionRecord(edgeBERecord("BE1", "1867.75000000|0.08320000|0.9638|0.3000|BE1", "sl_1")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// stage 为空:无法判断要退役谁,必须不动手。
	at.retireSuppressedBreakEvenTierRecord("ETHUSDT", "short", "", "BE1")
	if got := armedIdentities(t, st, "sl_1"); len(got) != 1 {
		t.Fatalf("stage 为空时不得改动任何记录,armed=%d", len(got))
	}
}

// 边界②:被抑制档在库里根本没有记录(首轮就被抑制,从未挂单)——必须是无操作,
// 且不得顺手碰到别人的记录。
func TestRetireSuppressedTierWithNoRecordIsNoop(t *testing.T) {
	at, st := newEdgeTrader(t, "retire-missing.db")
	if err := st.SaveDynamicProtectionRecord(edgeBERecord("BE1", "1867.75000000|0.08320000|0.9638|0.3000|BE1", "sl_1")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	at.retireSuppressedBreakEvenTierRecord("ETHUSDT", "short", "BE2", "BE1")
	if got := armedIdentities(t, st, "sl_1"); len(got) != 1 {
		t.Fatalf("BE2 无记录时 BE1 必须原样留着,armed=%v", got)
	}
}

// 边界③:跨 trader / 跨币种 / 跨方向的同名档位不得被误退役。
// 这是"别哪漏补哪"的反向锁:退役必须严格限定在本仓位本档。
func TestRetireSuppressedTierNeverCrossesPositionBoundary(t *testing.T) {
	at, st := newEdgeTrader(t, "retire-cross.db")
	others := []store.DynamicProtectionRecord{
		{TraderID: "t2", Symbol: "ETHUSDT", Side: "short", ProtectionType: "break_even_stop",
			RuleFingerprint: "1867.75000000|0.08320000|1.6064|0.4498|BE2", Status: "armed", ExchangeOrderID: "sl_other_trader"},
		{TraderID: "t1", Symbol: "BTCUSDT", Side: "short", ProtectionType: "break_even_stop",
			RuleFingerprint: "1867.75000000|0.08320000|1.6064|0.4498|BE2", Status: "armed", ExchangeOrderID: "sl_other_symbol"},
		{TraderID: "t1", Symbol: "ETHUSDT", Side: "long", ProtectionType: "break_even_stop",
			RuleFingerprint: "1867.75000000|0.08320000|1.6064|0.4498|BE2", Status: "armed", ExchangeOrderID: "sl_other_side"},
		{TraderID: "t1", Symbol: "ETHUSDT", Side: "short", ProtectionType: "native_trailing",
			RuleFingerprint: "1867.75000000|0.08320000|1.6064|0.4498|BE2", Status: "armed", ExchangeOrderID: "sl_other_type"},
	}
	for i, r := range others {
		if err := st.SaveDynamicProtectionRecord(r); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	at.retireSuppressedBreakEvenTierRecord("ETHUSDT", "short", "BE2", "BE1")
	for _, oid := range []string{"sl_other_trader", "sl_other_symbol", "sl_other_side", "sl_other_type"} {
		if got := armedIdentities(t, st, oid); len(got) != 1 {
			t.Fatalf("%s 不属于本仓位本档,不得被退役,armed=%v", oid, got)
		}
	}
}

// 边界④:已经是 superseded/executed 的记录不得被重复改写(幂等)。
func TestRetireSuppressedTierSkipsNonArmedRecords(t *testing.T) {
	at, st := newEdgeTrader(t, "retire-idempotent.db")
	rec := edgeBERecord("BE2", "1867.75000000|0.08320000|1.6064|0.4498|BE2", "sl_1")
	rec.Status = "executed"
	if err := st.SaveDynamicProtectionRecord(rec); err != nil {
		t.Fatalf("seed: %v", err)
	}
	at.retireSuppressedBreakEvenTierRecord("ETHUSDT", "short", "BE2", "BE1")
	state, err := st.LoadDynamicProtectionState()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, r := range state.Records {
		if r.Status != "executed" {
			t.Fatalf("已执行的记录不得被改成 %s —— 那会让成交事实丢失", r.Status)
		}
	}
}

// 边界⑤:畸形/空 fingerprint 落进裁决 —— 不得 panic,且必须退回"current 胜"这一保守语义
// (旧行为)。绝不能因为解析不出 stage 就用字典序留下陈旧记录。
func TestSupersedeConflictHandlesMalformedFingerprints(t *testing.T) {
	cases := []struct {
		name        string
		recordFP    string
		currentFP   string
		wantCurrent bool
	}{
		{"both empty", "", "", true},
		{"record malformed", "garbage", "1867.75|0.083|0.9638|0.3000|BE1", true},
		{"current malformed", "1867.75|0.083|0.9638|0.3000|BE1", "garbage", true},
		{"too few segments", "1867.75|0.083", "1867.75|0.083|0.9638|0.3000|BE1", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			at, st := newEdgeTrader(t, "malformed.db")
			record := edgeBERecord("", c.recordFP, "sl_1")
			current := edgeBERecord("", c.currentFP, "sl_1")
			if err := st.SaveDynamicProtectionRecord(record); err != nil {
				t.Fatalf("seed record: %v", err)
			}
			if err := st.SaveDynamicProtectionRecord(current); err != nil {
				t.Fatalf("seed current: %v", err)
			}
			at.supersedeConflictingOrderClaim(record, current)

			got := armedIdentities(t, st, "sl_1")
			if len(got) != 1 {
				t.Fatalf("一张活单只能有一个 armed 主人,got %d: %v", len(got), got)
			}
			if c.wantCurrent && got[0] != drawdownRuleIdentity(current.RuleFingerprint) {
				t.Fatalf("畸形指纹必须退回 current 胜,got %q want %q", got[0], drawdownRuleIdentity(current.RuleFingerprint))
			}
		})
	}
}

// 边界⑥:两档 offset 完全相同(身份串相等)——早退,一条都不许改。
// 若这里动手,唯一那张单的主人会被退役,保护在账本上凭空消失。
func TestSupersedeConflictIdenticalIdentityIsNoop(t *testing.T) {
	at, st := newEdgeTrader(t, "identical.db")
	fp := "1867.75000000|0.08320000|0.9638|0.3000|BE1"
	record := edgeBERecord("BE1", fp, "sl_1")
	if err := st.SaveDynamicProtectionRecord(record); err != nil {
		t.Fatalf("seed: %v", err)
	}
	at.supersedeConflictingOrderClaim(record, record)
	if got := armedIdentities(t, st, "sl_1"); len(got) != 1 {
		t.Fatalf("身份串相等必须早退,armed=%v", got)
	}
}

// 边界⑦:负 offset(保本档被配成"倒挂")仍必须收敛到唯一主人。
// 档位配置是人填的,不能假设它一定为正;字典序判据只依赖字符串,不依赖数值正负。
func TestSupersedeConflictConvergesWithNegativeOffsets(t *testing.T) {
	at, st := newEdgeTrader(t, "negative.db")
	be1 := edgeBERecord("BE1", "1867.75000000|0.08320000|0.9638|-0.3000|BE1", "sl_1")
	be2 := edgeBERecord("BE2", "1867.75000000|0.08320000|1.6064|-0.4498|BE2", "sl_1")
	if err := st.SaveDynamicProtectionRecord(be1); err != nil {
		t.Fatalf("seed be1: %v", err)
	}
	if err := st.SaveDynamicProtectionRecord(be2); err != nil {
		t.Fatalf("seed be2: %v", err)
	}
	// 两种写入顺序都必须收敛到同一个赢家。
	at.supersedeConflictingOrderClaim(be1, be2)
	first := armedIdentities(t, st, "sl_1")
	if len(first) != 1 {
		t.Fatalf("负 offset 下仍须唯一主人,armed=%v", first)
	}

	at2, st2 := newEdgeTrader(t, "negative-rev.db")
	if err := st2.SaveDynamicProtectionRecord(be2); err != nil {
		t.Fatalf("seed be2: %v", err)
	}
	if err := st2.SaveDynamicProtectionRecord(be1); err != nil {
		t.Fatalf("seed be1: %v", err)
	}
	at2.supersedeConflictingOrderClaim(be2, be1)
	second := armedIdentities(t, st2, "sl_1")
	if len(second) != 1 {
		t.Fatalf("反序写入仍须唯一主人,armed=%v", second)
	}
	if first[0] != second[0] {
		t.Fatalf("裁决必须与写入顺序无关,正序留 %s 反序留 %s", first[0], second[0])
	}
}

// 边界⑧:orderID 为空 / 不同 —— 不是同一张单,不构成冲突,必须早退。
func TestSupersedeConflictRequiresSameLiveOrder(t *testing.T) {
	at, st := newEdgeTrader(t, "distinct-orders.db")
	be1 := edgeBERecord("BE1", "1867.75000000|0.08320000|0.9638|0.3000|BE1", "sl_1")
	be2 := edgeBERecord("BE2", "1867.75000000|0.08320000|1.6064|0.4498|BE2", "sl_2")
	if err := st.SaveDynamicProtectionRecord(be1); err != nil {
		t.Fatalf("seed be1: %v", err)
	}
	if err := st.SaveDynamicProtectionRecord(be2); err != nil {
		t.Fatalf("seed be2: %v", err)
	}
	at.supersedeConflictingOrderClaim(be1, be2)
	if got := armedIdentities(t, st, "sl_1"); len(got) != 1 {
		t.Fatalf("不同 orderID 各自独立,sl_1 armed=%v", got)
	}
	if got := armedIdentities(t, st, "sl_2"); len(got) != 1 {
		t.Fatalf("不同 orderID 各自独立,sl_2 armed=%v", got)
	}

	// orderID 为空的记录之间也不得互相退役。
	empty1 := edgeBERecord("BE1", "1867.75000000|0.08320000|0.9638|0.3000|BE1", "")
	empty2 := edgeBERecord("BE2", "1867.75000000|0.08320000|1.6064|0.4498|BE2", "")
	at.supersedeConflictingOrderClaim(empty1, empty2)
}
