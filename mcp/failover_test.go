package mcp

import "testing"

// newFailoverTestClient builds a *Client with a primary endpoint and one fallback,
// snapshotting the primary the same way the production path does.
func newFailoverTestClient() *Client {
	c := NewClient(WithLogger(NewMockLogger())).(*Client)
	c.BaseURL = "https://primary.example/v1"
	c.APIKey = "primary-key"
	c.Model = "primary-model"
	c.SetFallbackEndpoints([]FallbackEndpoint{
		{Name: "Backup", BaseURL: "https://backup.example/v1", APIKey: "backup-key", Model: "backup-model", Priority: 0},
	})
	return c
}

// TestSetFallbackEndpoints_SnapshotsPrimary 验证配置 fallback 时正确快照主端点。
func TestSetFallbackEndpoints_SnapshotsPrimary(t *testing.T) {
	c := newFailoverTestClient()
	if !c.primarySnapshotted {
		t.Fatal("primary should be snapshotted")
	}
	if c.primaryBaseURL != "https://primary.example/v1" || c.primaryAPIKey != "primary-key" || c.primaryModel != "primary-model" {
		t.Fatalf("primary snapshot wrong: url=%s key=%s model=%s", c.primaryBaseURL, c.primaryAPIKey, c.primaryModel)
	}
	if c.currentEndpointIndex != -1 {
		t.Fatalf("index should be -1 (primary), got %d", c.currentEndpointIndex)
	}
}

// TestSwitchThenRestore 验证切到 fallback 后 restoreToPrimary 完整恢复连接参数。
func TestSwitchThenRestore(t *testing.T) {
	c := newFailoverTestClient()

	if !c.switchToNextEndpoint() {
		t.Fatal("should switch to fallback")
	}
	if c.BaseURL != "https://backup.example/v1" || c.APIKey != "backup-key" || c.Model != "backup-model" {
		t.Fatalf("after switch should be on backup: url=%s key=%s model=%s", c.BaseURL, c.APIKey, c.Model)
	}
	if c.currentEndpointIndex != 0 {
		t.Fatalf("index should be 0 (first fallback), got %d", c.currentEndpointIndex)
	}

	// 关键：恢复后必须完整回到主端点（不仅是 index）。
	c.restoreToPrimary()
	if c.BaseURL != "https://primary.example/v1" || c.APIKey != "primary-key" || c.Model != "primary-model" {
		t.Fatalf("restore must return to primary: url=%s key=%s model=%s", c.BaseURL, c.APIKey, c.Model)
	}
	if c.currentEndpointIndex != -1 {
		t.Fatalf("index should be -1 after restore, got %d", c.currentEndpointIndex)
	}
}

// TestPerCallStartsFromPrimary 验证：即使上次停在 fallback，下次仍从主端点起步。
// 模拟 CallWithMessages 开头的 restoreToPrimary。
func TestPerCallStartsFromPrimary(t *testing.T) {
	c := newFailoverTestClient()

	// 第一次调用：主失败 → 切 fallback → 成功 → restoreToPrimary
	c.switchToNextEndpoint() // 模拟主失败后切到 fallback
	c.restoreToPrimary()     // 模拟 fallback 成功后立即恢复主

	// 下一次调用开头：应当处于主端点
	if c.BaseURL != "https://primary.example/v1" {
		t.Fatalf("next call must start from primary, got %s", c.BaseURL)
	}

	// 模拟下一次调用开头的 restoreToPrimary（幂等，仍是主）
	c.restoreToPrimary()
	if c.BaseURL != "https://primary.example/v1" || c.currentEndpointIndex != -1 {
		t.Fatalf("restore must be idempotent on primary: url=%s idx=%d", c.BaseURL, c.currentEndpointIndex)
	}
}

// TestNoFallback_RestoreNoop 无 fallback 配置时 restoreToPrimary 不应改动任何东西。
func TestNoFallback_RestoreNoop(t *testing.T) {
	c := NewClient(WithLogger(NewMockLogger())).(*Client)
	c.BaseURL = "https://only.example/v1"
	c.APIKey = "only-key"
	// 未调用 SetFallbackEndpoints → primarySnapshotted=false
	c.restoreToPrimary()
	if c.BaseURL != "https://only.example/v1" || c.APIKey != "only-key" {
		t.Fatalf("restore without snapshot must be noop, got url=%s key=%s", c.BaseURL, c.APIKey)
	}
}
