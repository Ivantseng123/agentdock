package workflow

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/Ivantseng123/agentdock/app/config"
	"github.com/Ivantseng123/agentdock/shared/queue"
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
	// Identity resolution must populate RequestID on the Pending envelope
	// so downstream BuildJob can re-use it (matches IssueWorkflow.Trigger).
	if step.Pending == nil || step.Pending.RequestID == "" {
		t.Error("Pending.RequestID must be populated by Trigger")
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

func TestAskWorkflow_BuildJob_NoRepo_LeavesCloneURLEmpty(t *testing.T) {
	w, _ := newTestAskWorkflow(t)
	p := &Pending{
		ChannelID: "C1", ThreadTS: "1.0", UserID: "U1",
		RequestID: "req-1",
		State:     &askState{Question: "Q", AttachRepo: false},
	}
	job, status, err := w.BuildJob(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if job.TaskType != "ask" {
		t.Errorf("TaskType = %q", job.TaskType)
	}
	if job.CloneURL != "" {
		t.Errorf("CloneURL should be empty, got %q", job.CloneURL)
	}
	if job.Skills != nil {
		t.Errorf("Skills should be nil for Ask (spec defensive)")
	}
	if status != ":thinking_face: 思考中..." {
		t.Errorf("status = %q, want '思考中'", status)
	}
	if job.PromptContext == nil || job.PromptContext.Goal == "" {
		t.Error("PromptContext.Goal must be populated (ApplyDefaults)")
	}
}

func TestAskWorkflow_BuildJob_WithRepo_PopulatesCloneURL(t *testing.T) {
	w, _ := newTestAskWorkflow(t)
	p := &Pending{
		ChannelID: "C1", ThreadTS: "1.0", UserID: "U1",
		RequestID: "req-2",
		State:     &askState{Question: "Q", AttachRepo: true, SelectedRepo: "foo/bar"},
	}
	job, _, err := w.BuildJob(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if job.Repo != "foo/bar" {
		t.Errorf("Repo = %q", job.Repo)
	}
	if job.CloneURL != "https://github.com/foo/bar.git" {
		t.Errorf("CloneURL = %q", job.CloneURL)
	}
}

func TestAskWorkflow_HandleResult_SuccessPostsAnswer(t *testing.T) {
	w, slack := newTestAskWorkflow(t)
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", StatusMsgTS: "s-ts", TaskType: "ask"}
	state := &queue.JobState{Job: job}
	result := &queue.JobResult{
		JobID: "j1", Status: "completed",
		RawOutput: "===ASK_RESULT===\n{\"answer\":\"the answer is 42\"}",
	}
	if err := w.HandleResult(context.Background(), state, result); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(slack.Posted, " | ")
	if !strings.Contains(joined, "42") {
		t.Errorf("expected answer in posted text, got: %v", slack.Posted)
	}
}

func TestAskWorkflow_HandleResult_Truncates38K(t *testing.T) {
	w, slack := newTestAskWorkflow(t)
	long := strings.Repeat("a", 50000)
	result := &queue.JobResult{
		JobID: "j1", Status: "completed",
		RawOutput: "===ASK_RESULT===\n{\"answer\":\"" + long + "\"}",
	}
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", StatusMsgTS: "s-ts", TaskType: "ask"}
	state := &queue.JobState{Job: job}
	if err := w.HandleResult(context.Background(), state, result); err != nil {
		t.Fatal(err)
	}
	last := slack.Posted[len(slack.Posted)-1]
	if len(last) > 38000+len("\n…(已截斷)") {
		t.Errorf("posted text exceeds truncate limit: %d chars", len(last))
	}
	if !strings.Contains(last, "已截斷") {
		t.Error("truncate suffix missing")
	}
}

func TestAskWorkflow_HandleResult_FailureNoRetryButton(t *testing.T) {
	w, slack := newTestAskWorkflow(t)
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", TaskType: "ask"}
	state := &queue.JobState{Job: job}
	result := &queue.JobResult{JobID: "j1", Status: "failed", Error: "timeout"}
	if err := w.HandleResult(context.Background(), state, result); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(slack.Posted, " | ")
	if !strings.Contains(joined, "思考失敗") {
		t.Errorf("expected 思考失敗 text, got: %v", slack.Posted)
	}
}

func TestAskWorkflow_HandleResult_NilStateReturnsError(t *testing.T) {
	w, _ := newTestAskWorkflow(t)
	result := &queue.JobResult{JobID: "j1", Status: "completed"}
	if err := w.HandleResult(context.Background(), nil, result); err == nil {
		t.Error("expected error on nil state")
	}
}
