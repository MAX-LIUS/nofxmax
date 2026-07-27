package trader

import (
	"path/filepath"
	"testing"

	"nofx/store"
)

func TestLoadDynamicProtectionStateFromStoreRestoresRuntimeMaps(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "dynamic-protection-runtime.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	traderID := "trader-1"
	records := []store.DynamicProtectionRecord{
		{TraderID: traderID, ExchangeID: "exchange-1", Symbol: "BTCUSDT", Side: "short", PositionFingerprint: "100|2", ProtectionType: "native_partial_trailing", RuleFingerprint: "100|2|5|40|50", CloseRatioPct: 50, Status: "armed", ExchangeOrderID: "algo-partial"},
		{TraderID: traderID, ExchangeID: "exchange-1", Symbol: "ETHUSDT", Side: "long", PositionFingerprint: "200|1", ProtectionType: "native_trailing", RuleFingerprint: "200|1|5|40|100", CloseRatioPct: 100, Status: "armed", ExchangeOrderID: "algo-full"},
		{TraderID: traderID, ExchangeID: "exchange-1", Symbol: "DOGEUSDT", Side: "long", PositionFingerprint: "0.09930000|650.00000000", ProtectionType: "break_even_stop", RuleFingerprint: "0.09930000|650.00000000|0.7000|0.3000", Status: "armed", StopPrice: 0.099598},
		{TraderID: "other-trader", ExchangeID: "exchange-1", Symbol: "SOLUSDT", Side: "long", PositionFingerprint: "50|10", ProtectionType: "native_trailing", RuleFingerprint: "50|10|5|40|100", CloseRatioPct: 100, Status: "armed"},
	}
	for _, record := range records {
		if err := st.SaveDynamicProtectionRecord(record); err != nil {
			t.Fatalf("save dynamic protection record: %v", err)
		}
	}

	at := &AutoTrader{id: traderID, store: st}
	at.loadDynamicProtectionStateFromStore()

	if got := at.getProtectionState("BTCUSDT", "short"); got != "native_partial_trailing_armed" {
		t.Fatalf("expected BTC partial trailing state restored, got %q", got)
	}
	if got := at.getProtectionState("ETHUSDT", "long"); got != "native_trailing_armed" {
		t.Fatalf("expected ETH full trailing state restored, got %q", got)
	}
	if got := at.getProtectionState("SOLUSDT", "long"); got != "" {
		t.Fatalf("expected other trader state ignored, got %q", got)
	}
	// armed 的 native 记录**不能**进 drawdownState。这个 map 只有一个读者:
	// auto_trader_risk.go:469 的"这一档已经平过了,别重复平"门禁。"已武装"≠"已执行" ——
	// 写进去等于重启后关掉 managed 兜底(交易所挂单万一失效就没人平了),违反双保险。
	// native 记录该恢复的是 protectionState(上面已断言)和 armed 集合
	// (getArmedDrawdownRuleFingerprints 直接从库里现读)。
	if got := at.getDrawdownExecutionFingerprint("BTCUSDT", "short"); got != "" {
		t.Fatalf("armed native 记录污染了已执行门禁:got %q —— 重启后 managed 兜底会被自己关掉", got)
	}
	if got := at.getDrawdownExecutionFingerprint("ETHUSDT", "long"); got != "" {
		t.Fatalf("armed native 记录污染了已执行门禁:got %q —— 重启后 managed 兜底会被自己关掉", got)
	}
	// 但 armed 集合必须照旧恢复,否则重启后每一档都会被判"没武装过"再挂一遍。
	if armed := at.getArmedDrawdownRuleFingerprints("ETHUSDT", "long"); len(armed) == 0 {
		t.Fatal("armed native 集合没有恢复 —— 重启后会重复挂单")
	}
	if got := at.getBreakEvenState("DOGEUSDT", "long"); got != "armed" {
		t.Fatalf("expected DOGE break-even state restored, got %q", got)
	}
	if got := at.breakEvenFingerprints["DOGEUSDT_long"]; got != "0.09930000|650.00000000" {
		t.Fatalf("expected DOGE break-even fingerprint restored, got %q", got)
	}
}

func TestLoadDynamicProtectionStateFromStoreSkipsInactiveNativeRecords(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "dynamic-protection-runtime-status.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	traderID := "trader-1"
	records := []store.DynamicProtectionRecord{
		{TraderID: traderID, ExchangeID: "exchange-1", Symbol: "BTCUSDT", Side: "long", PositionFingerprint: "100|1", ProtectionType: "native_trailing", RuleFingerprint: "100|1|5|40|100", CloseRatioPct: 100, Status: "canceled", ExchangeOrderID: "old-algo"},
		{TraderID: traderID, ExchangeID: "exchange-1", Symbol: "ETHUSDT", Side: "long", PositionFingerprint: "200|1", ProtectionType: "managed_drawdown", RuleFingerprint: "200|1|5|40|50", CloseRatioPct: 50, Status: "executed"},
	}
	for _, record := range records {
		if err := st.SaveDynamicProtectionRecord(record); err != nil {
			t.Fatalf("save dynamic protection record: %v", err)
		}
	}

	at := &AutoTrader{id: traderID, store: st}
	at.loadDynamicProtectionStateFromStore()

	if got := at.getProtectionState("BTCUSDT", "long"); got != "" {
		t.Fatalf("expected canceled native trailing not restored, got %q", got)
	}
	if got := at.getDrawdownExecutionFingerprint("BTCUSDT", "long"); got != "" {
		t.Fatalf("expected canceled native trailing fingerprint not restored, got %q", got)
	}
	if got := at.getDrawdownExecutionFingerprint("ETHUSDT", "long"); got != "200|1|5|40|50" {
		t.Fatalf("expected executed managed drawdown fingerprint restored, got %q", got)
	}
}

func TestLoadDynamicProtectionStateFromStoreRestoresMixedNativeRecordsPerPosition(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "dynamic-protection-runtime-latest.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	traderID := "trader-1"
	records := []store.DynamicProtectionRecord{
		{TraderID: traderID, ExchangeID: "exchange-1", Symbol: "DOGEUSDT", Side: "long", PositionFingerprint: "old", ProtectionType: "native_trailing", RuleFingerprint: "old-full", CloseRatioPct: 100, Status: "armed", UpdatedAt: 1000},
		{TraderID: traderID, ExchangeID: "exchange-1", Symbol: "DOGEUSDT", Side: "long", PositionFingerprint: "new", ProtectionType: "native_partial_trailing", RuleFingerprint: "new-partial", CloseRatioPct: 80, Status: "armed", UpdatedAt: 2000},
	}
	for _, record := range records {
		if err := st.SaveDynamicProtectionRecord(record); err != nil {
			t.Fatalf("save dynamic protection record: %v", err)
		}
	}

	at := &AutoTrader{id: traderID, store: st}
	at.loadDynamicProtectionStateFromStore()

	if got := at.getProtectionState("DOGEUSDT", "long"); got != "native_trailing_armed" {
		t.Fatalf("expected native full state restored when a full tier exists, got %q", got)
	}
	if got := at.getDrawdownExecutionMode("DOGEUSDT", "long"); got != "native_trailing_tiers" {
		t.Fatalf("expected mixed native tier mode restored, got %q", got)
	}
	armed := at.getArmedDrawdownRuleFingerprints("DOGEUSDT", "long")
	if _, ok := armed["old-full"]; !ok {
		t.Fatalf("expected full native fingerprint restored, got %+v", armed)
	}
	if _, ok := armed["new-partial"]; !ok {
		t.Fatalf("expected partial native fingerprint restored, got %+v", armed)
	}
}

func TestLoadDynamicProtectionStateFromStoreRestoresAllNativeTiers(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "dynamic-protection-runtime-tiers.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	traderID := "trader-1"
	records := []store.DynamicProtectionRecord{
		{TraderID: traderID, ExchangeID: "exchange-1", Symbol: "DOGEUSDT", Side: "long", PositionFingerprint: "pos", ProtectionType: "native_partial_trailing", RuleFingerprint: "tier-1", CloseRatioPct: 40, Status: "armed", UpdatedAt: 1000},
		{TraderID: traderID, ExchangeID: "exchange-1", Symbol: "DOGEUSDT", Side: "long", PositionFingerprint: "pos", ProtectionType: "native_partial_trailing", RuleFingerprint: "tier-2", CloseRatioPct: 60, Status: "armed", UpdatedAt: 2000},
	}
	for _, record := range records {
		if err := st.SaveDynamicProtectionRecord(record); err != nil {
			t.Fatalf("save dynamic protection record: %v", err)
		}
	}

	at := &AutoTrader{id: traderID, store: st}
	at.loadDynamicProtectionStateFromStore()

	if got := at.getProtectionState("DOGEUSDT", "long"); got != "native_partial_trailing_armed" {
		t.Fatalf("expected native partial state restored, got %q", got)
	}
	if got := at.getDrawdownExecutionMode("DOGEUSDT", "long"); got != "native_partial_trailing_tiers" {
		t.Fatalf("expected tiered native mode restored, got %q", got)
	}
	armed := at.getArmedDrawdownRuleFingerprints("DOGEUSDT", "long")
	if _, ok := armed["tier-1"]; !ok {
		t.Fatalf("expected tier-1 restored, got %+v", armed)
	}
	if _, ok := armed["tier-2"]; !ok {
		t.Fatalf("expected tier-2 restored, got %+v", armed)
	}
}
