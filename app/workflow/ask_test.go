package workflow

import (
	"context"
	"log/slog"
	"testing"

	"github.com/Ivantseng123/agentdock/app/config"
)

func TestAskWorkflow_Type(t *testing.T) {
	w := &AskWorkflow{}
	if w.Type() != "ask" {
		t.Errorf("Type() = %q", w.Type())
	}
}

func TestAskWorkflow_Trigger_ReturnsRepoPrompt(t *testing.T) {
	w, _ := newTestAskWorkflow(t)
	step, err := w.Trigger(context.Background(), TriggerEvent{ChannelID: "C1", ThreadTS: "1.0"}, "what does X do?")
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if step.Kind != NextStepPostSelector {
		t.Errorf("expected NextStepPostSelector, got %v", step.Kind)
	}
	if len(step.SelectorActions) != 2 {
		t.Errorf("expected 2 actions (attach/skip), got %d", len(step.SelectorActions))
	}
}

func newTestAskWorkflow(t *testing.T) (*AskWorkflow, *fakeSlackPort) {
	t.Helper()
	cfg := &config.Config{}
	config.ApplyDefaults(cfg)
	slack := newFakeSlackPort()
	return NewAskWorkflow(cfg, slack, nil, slog.Default()), slack
}

func TestAskWorkflow_Selection_SkipGoesToSubmit(t *testing.T) {
	w, _ := newTestAskWorkflow(t)
	p := &Pending{Phase: "ask_repo_prompt", State: &askState{Question: "Q"}, ChannelID: "C1", ThreadTS: "1.0"}
	step, err := w.Selection(context.Background(), p, "skip")
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != NextStepSubmit {
		t.Errorf("expected NextStepSubmit, got %v", step.Kind)
	}
	st := p.State.(*askState)
	if st.AttachRepo {
		t.Error("AttachRepo should be false")
	}
}

func TestAskWorkflow_Selection_AttachShowsRepoSelector(t *testing.T) {
	w, _ := newTestAskWorkflow(t)
	w.cfg.ChannelDefaults.Repos = []string{"foo/bar", "baz/qux"}
	p := &Pending{Phase: "ask_repo_prompt", State: &askState{Question: "Q"}, ChannelID: "C1", ThreadTS: "1.0"}
	step, err := w.Selection(context.Background(), p, "attach")
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != NextStepPostSelector {
		t.Errorf("expected NextStepPostSelector (repo choice), got %v", step.Kind)
	}
}

func TestAskWorkflow_Selection_RepoChoiceGoesToSubmit(t *testing.T) {
	w, _ := newTestAskWorkflow(t)
	p := &Pending{Phase: "ask_repo_select", State: &askState{Question: "Q", AttachRepo: true}, ChannelID: "C1", ThreadTS: "1.0"}
	step, err := w.Selection(context.Background(), p, "foo/bar")
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != NextStepSubmit {
		t.Errorf("expected NextStepSubmit, got %v", step.Kind)
	}
	st := p.State.(*askState)
	if st.SelectedRepo != "foo/bar" {
		t.Errorf("SelectedRepo = %q", st.SelectedRepo)
	}
}

// TestAskWorkflow_Selection_AttachWithNoReposUsesExternalSearch covers the
// fallback path that fires when ChannelDefaults.Repos and Channels[ID] are
// both empty. The dispatcher routes NextStepPostExternalSelector to a
// searchable Slack modal rather than a button selector. Regression guard
// for the Task 5.2 plan-deviation (plan's NextStepPostSelector+empty-actions
// approach was broken — see commit 37bc67b).
func TestAskWorkflow_Selection_AttachWithNoReposUsesExternalSearch(t *testing.T) {
	w, _ := newTestAskWorkflow(t)
	// ChannelDefaults.Repos left empty on purpose — no Channels override either.
	p := &Pending{Phase: "ask_repo_prompt", State: &askState{Question: "Q"}, ChannelID: "C1", ThreadTS: "1.0"}
	step, err := w.Selection(context.Background(), p, "attach")
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != NextStepPostExternalSelector {
		t.Errorf("expected NextStepPostExternalSelector, got %v", step.Kind)
	}
	if step.SelectorActionID != "ask_repo" {
		t.Errorf("SelectorActionID = %q, want ask_repo", step.SelectorActionID)
	}
	if step.SelectorPlaceholder == "" {
		t.Error("SelectorPlaceholder should be set for external search")
	}
	if p.Phase != "ask_repo_select" {
		t.Errorf("Phase = %q, want ask_repo_select", p.Phase)
	}
	st := p.State.(*askState)
	if !st.AttachRepo {
		t.Error("AttachRepo should be true after attach value")
	}
}
