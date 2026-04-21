package workflow

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/Ivantseng123/agentdock/app/config"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

type fakeGitHubPR struct {
	pr      *PullRequest
	err     error
	calledN int
}

func (f *fakeGitHubPR) GetPullRequest(ctx context.Context, owner, repo string, number int) (*PullRequest, error) {
	f.calledN = number
	return f.pr, f.err
}

func TestPRReviewWorkflow_Type(t *testing.T) {
	w := &PRReviewWorkflow{}
	if w.Type() != "pr_review" {
		t.Errorf("Type() = %q", w.Type())
	}
}

func TestPRReviewWorkflow_TriggerAPath_Valid(t *testing.T) {
	pr := &PullRequest{Number: 7, State: "open", Title: "T"}
	pr.Head.Ref = "feature-x"
	pr.Head.SHA = "abc123"
	pr.Head.Repo.FullName = "forker/bar"
	pr.Head.Repo.CloneURL = "https://github.com/forker/bar.git"
	pr.Base.Ref = "main"

	w, _ := newTestPRReviewWorkflow(t)
	w.github = &fakeGitHubPR{pr: pr}

	step, err := w.Trigger(context.Background(), TriggerEvent{ChannelID: "C1", ThreadTS: "1.0"}, "https://github.com/foo/bar/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != NextStepSubmit {
		t.Errorf("expected NextStepSubmit, got %v", step.Kind)
	}
	st := step.Pending.State.(*prReviewState)
	if st.HeadRepo != "forker/bar" {
		t.Errorf("HeadRepo = %q", st.HeadRepo)
	}
}

func TestPRReviewWorkflow_TriggerAPath_404(t *testing.T) {
	w, slack := newTestPRReviewWorkflow(t)
	w.github = &fakeGitHubPR{err: errors.New("404 not found")}
	step, _ := w.Trigger(context.Background(), TriggerEvent{ChannelID: "C1", ThreadTS: "1.0"}, "https://github.com/foo/bar/pull/999")
	if step.Kind != NextStepError {
		t.Errorf("expected NextStepError, got %v", step.Kind)
	}
	_ = slack
}

func TestPRReviewWorkflow_TriggerAPath_PartialURLRejected(t *testing.T) {
	w, _ := newTestPRReviewWorkflow(t)
	step, _ := w.Trigger(context.Background(), TriggerEvent{ChannelID: "C1", ThreadTS: "1.0"}, "github.com/foo/bar/pull/7")
	if step.Kind != NextStepError {
		t.Errorf("expected NextStepError on partial URL")
	}
}

func TestPRReviewWorkflow_TriggerDisabled(t *testing.T) {
	w, _ := newTestPRReviewWorkflow(t)
	w.cfg.PRReview.Enabled = false
	step, _ := w.Trigger(context.Background(), TriggerEvent{ChannelID: "C1"}, "https://github.com/foo/bar/pull/7")
	if step.Kind != NextStepError {
		t.Errorf("expected NextStepError when feature-flag disabled")
	}
}

func TestPRReviewWorkflow_DisabledErrorTextNoPrefix(t *testing.T) {
	w, _ := newTestPRReviewWorkflow(t)
	w.cfg.PRReview.Enabled = false
	step, _ := w.Trigger(context.Background(), TriggerEvent{ChannelID: "C1"}, "")
	if strings.HasPrefix(step.ErrorText, ":warning:") || strings.HasPrefix(step.ErrorText, ":x:") {
		t.Errorf("ErrorText should NOT start with emoji prefix (dispatcher adds :x:): got %q", step.ErrorText)
	}
	if !strings.Contains(step.ErrorText, "尚未啟用") {
		t.Errorf("disabled message lost its intent: %q", step.ErrorText)
	}
}

func TestPRReviewWorkflow_HandleResult_Posted(t *testing.T) {
	w, slack := newTestPRReviewWorkflow(t)
	job := &queue.Job{
		ID: "j1", ChannelID: "C1", ThreadTS: "1.0", StatusMsgTS: "s-ts", TaskType: "pr_review",
		WorkflowArgs: map[string]string{"pr_url": "https://github.com/foo/bar/pull/7"},
	}
	state := &queue.JobState{Job: job}
	result := &queue.JobResult{
		JobID: "j1", Status: "completed",
		RawOutput: "===REVIEW_RESULT===\n" + `{"status":"POSTED","summary":"ok","comments_posted":2,"severity_summary":"clean"}`,
	}
	if err := w.HandleResult(context.Background(), state, result); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(slack.Posted, " | ")
	if !strings.Contains(joined, "Review 完成") {
		t.Errorf("got: %v", slack.Posted)
	}
}

func TestPRReviewWorkflow_HandleResult_Failed_NoRetry(t *testing.T) {
	w, slack := newTestPRReviewWorkflow(t)
	job := &queue.Job{
		ID: "j1", ChannelID: "C1", ThreadTS: "1.0", StatusMsgTS: "s-ts", TaskType: "pr_review",
		WorkflowArgs: map[string]string{"pr_url": "https://github.com/foo/bar/pull/7"},
	}
	state := &queue.JobState{Job: job}
	result := &queue.JobResult{JobID: "j1", Status: "failed", Error: "timeout"}
	if err := w.HandleResult(context.Background(), state, result); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(slack.Posted, " | ")
	if !strings.Contains(joined, "Review 失敗") {
		t.Errorf("got: %v", slack.Posted)
	}
}

func TestPRReviewWorkflow_HandleResult_Skipped(t *testing.T) {
	w, slack := newTestPRReviewWorkflow(t)
	job := &queue.Job{
		ID: "j1", ChannelID: "C1", ThreadTS: "1.0", StatusMsgTS: "s-ts", TaskType: "pr_review",
		WorkflowArgs: map[string]string{"pr_url": "https://github.com/foo/bar/pull/7"},
	}
	state := &queue.JobState{Job: job}
	result := &queue.JobResult{
		JobID: "j1", Status: "completed",
		RawOutput: "===REVIEW_RESULT===\n" + `{"status":"SKIPPED","reason":"lockfile_only","summary":"nothing to review"}`,
	}
	if err := w.HandleResult(context.Background(), state, result); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(slack.Posted, " | ")
	if !strings.Contains(joined, "Review 跳過") {
		t.Errorf("got: %v", slack.Posted)
	}
}

func TestPRReviewWorkflow_HandleResult_NilStateReturnsError(t *testing.T) {
	w, _ := newTestPRReviewWorkflow(t)
	result := &queue.JobResult{JobID: "j1", Status: "completed"}
	if err := w.HandleResult(context.Background(), nil, result); err == nil {
		t.Error("expected error on nil state")
	}
}

func newTestPRReviewWorkflow(t *testing.T) (*PRReviewWorkflow, *fakeSlackPort) {
	t.Helper()
	cfg := &config.Config{}
	config.ApplyDefaults(cfg)
	cfg.PRReview.Enabled = true
	slack := newFakeSlackPort()
	w := NewPRReviewWorkflow(cfg, slack, &fakeGitHubPR{}, nil, slog.Default())
	return w, slack
}
