package workflow

import (
	"context"
	"testing"
)

func TestIssueWorkflow_Type(t *testing.T) {
	w := &IssueWorkflow{}
	if w.Type() != "issue" {
		t.Errorf("Type() = %q, want issue", w.Type())
	}
}

func TestIssueWorkflow_TriggerWithRepoArg_ShortCircuits(t *testing.T) {
	w := &IssueWorkflow{}
	ctx := context.Background()
	ev := TriggerEvent{ChannelID: "C1", ThreadTS: "1.0", UserID: "U1"}

	step, err := w.Trigger(ctx, ev, "foo/bar")
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if step.Kind == NextStepError {
		t.Errorf("expected non-error NextStep, got error: %q", step.ErrorText)
	}
}
