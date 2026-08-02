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

// orderOf drains the fallback chain and returns the endpoint names in the order
// switchToNextEndpoint actually visits them. That is the only order that matters
// in production, so assertions are made against it rather than against the
// stored slice.
func orderOf(c *Client) []string {
	var seen []string
	for c.switchToNextEndpoint() {
		seen = append(seen, c.FallbackEndpoints[c.currentEndpointIndex].Name)
	}
	return seen
}

// TestFallbackOrderFollowsPriority 固定「按 Priority 升序切换」这一不变量。
//
// 回归用例：修复前 switchToNextEndpoint 只按数组下标推进，完全没读 Priority，
// 于是配置顺序就是尝试顺序,Priority 字段形同虚设。
func TestFallbackOrderFollowsPriority(t *testing.T) {
	c := NewClient(WithLogger(NewMockLogger())).(*Client)
	c.BaseURL = "https://primary.example/v1"
	c.APIKey = "primary-key"
	c.Model = "primary-model"
	// Deliberately supplied in the WRONG order: highest Priority number first.
	c.SetFallbackEndpoints([]FallbackEndpoint{
		{Name: "low", BaseURL: "https://low.example/v1", Priority: 9},
		{Name: "high", BaseURL: "https://high.example/v1", Priority: 1},
		{Name: "mid", BaseURL: "https://mid.example/v1", Priority: 5},
	})

	got := orderOf(c)
	want := []string{"high", "mid", "low"}
	if len(got) != len(want) {
		t.Fatalf("visited %d endpoints, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: got %q, want %q (full order %v)", i, got[i], want[i], got)
		}
	}
}

// TestFallbackOrderMatchesProductionConfig 用生产实际配置复现该 bug。
//
// claude 行配了 NovaI(priority=2) 在前、lt48(priority=1) 在后。修复前先打
// 低优先级的 NovaI,与运维意图相反;修复后必须先打 lt48。
func TestFallbackOrderMatchesProductionConfig(t *testing.T) {
	c := NewClient(WithLogger(NewMockLogger())).(*Client)
	c.BaseURL = "https://ai.ltcraft.cn/v1"
	c.APIKey = "primary-key"
	c.Model = "claude-opus-5"
	c.SetFallbackEndpoints([]FallbackEndpoint{
		{Name: "NovaI API", BaseURL: "https://us.novaiapi.com/v1", Model: "claude-opus-4-8", Priority: 2},
		{Name: "lt48", BaseURL: "https://api.lt4net.org/v1", Model: "claude-opus-4-8", Priority: 1},
	})

	got := orderOf(c)
	if len(got) != 2 || got[0] != "lt48" || got[1] != "NovaI API" {
		t.Errorf("priority order not honoured: got %v, want [lt48 NovaI API]", got)
	}
}

// TestFallbackOrderStableWithinSamePriority 同 Priority 时保持配置顺序,
// 便于运维在同一档位下用书写顺序表达偏好。
func TestFallbackOrderStableWithinSamePriority(t *testing.T) {
	c := NewClient(WithLogger(NewMockLogger())).(*Client)
	c.SetFallbackEndpoints([]FallbackEndpoint{
		{Name: "first", Priority: 3},
		{Name: "second", Priority: 3},
		{Name: "third", Priority: 3},
	})

	got := orderOf(c)
	want := []string{"first", "second", "third"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("stability broken at %d: got %v, want %v", i, got, want)
		}
	}
}

// TestSetFallbackEndpointsDoesNotMutateCaller 排序必须作用于副本。
// 调用方的切片来自 store.AIModel.GetFallbackEndpoints,可能被复用,
// 就地重排会污染他人数据。
func TestSetFallbackEndpointsDoesNotMutateCaller(t *testing.T) {
	input := []FallbackEndpoint{
		{Name: "low", Priority: 9},
		{Name: "high", Priority: 1},
	}
	c := NewClient(WithLogger(NewMockLogger())).(*Client)
	c.SetFallbackEndpoints(input)

	if input[0].Name != "low" || input[1].Name != "high" {
		t.Errorf("caller slice was reordered in place: %v", input)
	}
	if c.FallbackEndpoints[0].Name != "high" {
		t.Errorf("client copy not sorted: %v", c.FallbackEndpoints)
	}
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
