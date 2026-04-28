package bot

import (
	"testing"

	"github.com/Ivantseng123/agentdock/app/config"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

func TestAssemblePromptContext_PassesConfigThrough(t *testing.T) {
	allow := false
	wp := config.WorkflowPromptConfig{
		Goal:        "custom",
		OutputRules: []string{"one", "two"},
	}
	defaults := config.PromptDefaultsConfig{
		Language:         "zh-TW",
		AllowWorkerRules: &allow,
	}
	msgs := []queue.ThreadMessage{{User: "Alice", Timestamp: "1", Text: "t"}}

	got := AssemblePromptContext(msgs, "extra", "general", "Alice", "main", wp, defaults)

	if got.Language != "zh-TW" {
		t.Errorf("Language = %q", got.Language)
	}
	if got.Goal != "custom" {
		t.Errorf("Goal = %q", got.Goal)
	}
	if len(got.OutputRules) != 2 {
		t.Errorf("OutputRules = %v", got.OutputRules)
	}
	if got.AllowWorkerRules {
		t.Error("AllowWorkerRules = true, expected false")
	}
	if got.ExtraDescription != "extra" || got.Channel != "general" || got.Reporter != "Alice" || got.Branch != "main" {
		t.Errorf("pass-through fields wrong: %+v", got)
	}
	if got.ThreadMessages[0].User != "Alice" {
		t.Errorf("ThreadMessages not passed through: %+v", got.ThreadMessages)
	}
}

func TestAssemblePromptContext_NilAllowWorkerRulesDefaultsTrue(t *testing.T) {
	// Delegates to PromptDefaultsConfig.IsWorkerRulesAllowed(), which treats
	// nil as "allow" (matching applyDefaults). Keeps the invariant in one place.
	defaults := config.PromptDefaultsConfig{AllowWorkerRules: nil}
	got := AssemblePromptContext(nil, "", "", "", "", config.WorkflowPromptConfig{}, defaults)
	if !got.AllowWorkerRules {
		t.Error("nil AllowWorkerRules should assemble as true (matches applyDefaults default)")
	}
}
