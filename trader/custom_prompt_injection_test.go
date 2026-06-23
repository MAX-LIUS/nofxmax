package trader

import (
	"nofx/kernel"
	"nofx/store"
	"testing"
)

// SetCustomPrompt must inject the prompt into the strategy engine's config,
// because that is what the prompt builder actually reads. Regression guard for
// the bug where trader-level custom prompts were stored in a dead field and
// never reached the system prompt.
func TestSetCustomPromptInjectsIntoEngineConfig(t *testing.T) {
	eng := kernel.NewStrategyEngine(&store.StrategyConfig{})
	at := &AutoTrader{strategyEngine: eng}

	const p = "# 方向纪律\n严格顺势,不抄底"
	at.SetCustomPrompt(p)

	if got := eng.GetConfig().CustomPrompt; got != p {
		t.Fatalf("engine config CustomPrompt not injected: got %q want %q", got, p)
	}
	if at.customPrompt != p {
		t.Fatalf("at.customPrompt not set: got %q", at.customPrompt)
	}
}

// Must not panic when no engine is attached (defensive).
func TestSetCustomPromptNilEngineSafe(t *testing.T) {
	at := &AutoTrader{}
	at.SetCustomPrompt("x")
	if at.customPrompt != "x" {
		t.Fatalf("at.customPrompt not set with nil engine")
	}
}
