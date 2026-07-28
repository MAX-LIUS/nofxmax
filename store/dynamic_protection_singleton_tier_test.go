package store

import (
	"path/filepath"
	"testing"
)

func beTierRecord(stage, orderID string) DynamicProtectionRecord {
	return DynamicProtectionRecord{
		TraderID:            "trader-1",
		ExchangeID:          "exchange-1",
		Symbol:              "WLDUSDT",
		Side:                "short",
		PositionFingerprint: "0.36220000|95.00000000",
		ProtectionType:      "break_even_stop",
		RuleFingerprint:     "0.36220000|95.00000000|0.5000|0.2000|" + stage,
		Status:              "armed",
		ExchangeOrderID:     orderID,
	}
}

// 两档保本止损必须能同时保持 armed。
//
// 改造前独占组只按 protectionType 分,BE2 一 arm 就把 BE1 降级成 replaced —— 而
// BE1 的单在交易所上还活着。线上实证:22 条 BE 记录里 20 条 replaced、只剩 2 条 armed,
// 每个仓位恰好一条,而那两个仓位当时 BE1/BE2 都在场。后果是认领集合永远只认到一张,
// 兄弟档被判 stale 重复单进撤单路径。
func TestBreakEvenTiersStayArmedIndependently(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "be-tiers.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	be1 := beTierRecord("BE1", "algo-be1")
	if err := s.SaveDynamicProtectionRecord(be1); err != nil {
		t.Fatalf("save BE1: %v", err)
	}
	be2 := beTierRecord("BE2", "algo-be2")
	if err := s.SaveDynamicProtectionRecord(be2); err != nil {
		t.Fatalf("save BE2: %v", err)
	}

	state, err := s.LoadDynamicProtectionState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	armed := map[string]string{}
	for _, record := range state.Records {
		if record.ProtectionType != "break_even_stop" || record.Status != "armed" {
			continue
		}
		armed[dynamicProtectionStageFromFingerprint(record.RuleFingerprint)] = record.ExchangeOrderID
	}
	if len(armed) != 2 {
		t.Fatalf("两档都该保持 armed,got %+v(全部记录 %d 条)", armed, len(state.Records))
	}
	if armed["BE1"] != "algo-be1" || armed["BE2"] != "algo-be2" {
		t.Fatalf("两档的 order id 应各自保留,got %+v", armed)
	}
}

// 同一档重新 arm(例如漂移后换价重挂)时,该档的旧记录仍必须被降级 —— 否则认领集合里
// 会留下一个已经撤掉的 id,分类器把不存在的单当自己的。独占性在档内必须保住。
func TestBreakEvenSameTierRearmSupersedesOldRecord(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "be-same-tier.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	old := beTierRecord("BE1", "algo-old")
	old.StopPrice = 0.3604
	if err := s.SaveDynamicProtectionRecord(old); err != nil {
		t.Fatalf("save old BE1: %v", err)
	}
	// 同档、换价重挂:PositionFingerprint 变了(均价漂移)所以 key 不同。
	fresh := beTierRecord("BE1", "algo-new")
	fresh.PositionFingerprint = "0.36970000|95.00000000"
	fresh.RuleFingerprint = "0.36970000|95.00000000|0.5000|0.2000|BE1"
	fresh.StopPrice = 0.3568
	if err := s.SaveDynamicProtectionRecord(fresh); err != nil {
		t.Fatalf("save fresh BE1: %v", err)
	}

	state, err := s.LoadDynamicProtectionState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	armedIDs := []string{}
	for _, record := range state.Records {
		if record.ProtectionType == "break_even_stop" && record.Status == "armed" {
			armedIDs = append(armedIDs, record.ExchangeOrderID)
		}
	}
	if len(armedIDs) != 1 || armedIDs[0] != "algo-new" {
		t.Fatalf("同档重挂后只该有新记录 armed,got %+v", armedIDs)
	}
}

// stage 解析不出来时必须退回改造前语义(按 protectionType 独占),绝不猜档位。
func TestBreakEvenSingletonGroupFallsBackWithoutStage(t *testing.T) {
	withStage := beTierRecord("BE1", "id-1")
	if got := singletonDynamicProtectionGroupForRecord(withStage); got != "break_even_stop|BE1" {
		t.Fatalf("带档位应细化独占键,got %q", got)
	}

	noStage := withStage
	noStage.RuleFingerprint = "0.36220000|95.00000000|0.5000|0.2000"
	if got := singletonDynamicProtectionGroupForRecord(noStage); got != "break_even_stop" {
		t.Fatalf("无档位应退回 protectionType 独占,got %q", got)
	}

	notSingleton := withStage
	notSingleton.ProtectionType = "native_trailing"
	if got := singletonDynamicProtectionGroupForRecord(notSingleton); got != "" {
		t.Fatalf("非独占类型不应有独占键,got %q", got)
	}
}

// SaveDynamicProtectionRecordByKey 只原地改指定那条,不得触发独占降级牵连别人。
func TestSaveDynamicProtectionRecordByKeyIsInPlace(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "be-bykey.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	be1 := beTierRecord("BE1", "algo-be1")
	if err := s.SaveDynamicProtectionRecord(be1); err != nil {
		t.Fatalf("save BE1: %v", err)
	}
	be2 := beTierRecord("BE2", "algo-be2")
	if err := s.SaveDynamicProtectionRecord(be2); err != nil {
		t.Fatalf("save BE2: %v", err)
	}

	state, err := s.LoadDynamicProtectionState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	var be1Key string
	for key, record := range state.Records {
		if dynamicProtectionStageFromFingerprint(record.RuleFingerprint) == "BE1" {
			be1Key = key
		}
	}
	if be1Key == "" {
		t.Fatal("找不到 BE1 的 key")
	}

	target := state.Records[be1Key]
	target.Status = "replaced"
	if err := s.SaveDynamicProtectionRecordByKey(be1Key, target); err != nil {
		t.Fatalf("save by key: %v", err)
	}

	state, err = s.LoadDynamicProtectionState()
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	if got := state.Records[be1Key].Status; got != "replaced" {
		t.Fatalf("BE1 应被标记 replaced,got %q", got)
	}
	for key, record := range state.Records {
		if key == be1Key {
			continue
		}
		if record.Status != "armed" {
			t.Fatalf("不该牵连其他记录,%s 变成了 %q", key, record.Status)
		}
	}

	// 不存在的 key 不创建记录。
	before := len(state.Records)
	if err := s.SaveDynamicProtectionRecordByKey("no-such-key", target); err != nil {
		t.Fatalf("save unknown key should be a no-op: %v", err)
	}
	state, err = s.LoadDynamicProtectionState()
	if err != nil {
		t.Fatalf("reload after unknown key: %v", err)
	}
	if len(state.Records) != before {
		t.Fatalf("不存在的 key 不该新建记录,%d → %d", before, len(state.Records))
	}
}
