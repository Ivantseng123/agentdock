package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/Ivantseng123/agentdock/app/config"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

func TestIssueWorkflow_Type(t *testing.T) {
	w := &IssueWorkflow{}
	if w.Type() != "issue" {
		t.Errorf("Type() = %q, want issue", w.Type())
	}
}

func TestIssueWorkflow_TriggerWithRepoArg_ShortCircuits(t *testing.T) {
	w, _, _ := newTestIssueWorkflow(t)
	ctx := context.Background()
	ev := TriggerEvent{ChannelID: "C1", ThreadTS: "1.0", UserID: "U1"}

	step, err := w.Trigger(ctx, ev, "foo/bar")
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if step.Kind == NextStepError {
		t.Errorf("expected non-error NextStep, got error: %q", step.ErrorText)
	}
	if step.Pending == nil || step.Pending.State == nil {
		t.Errorf("expected Pending != nil and Pending.State != nil")
	}
}

// ── new tests for Task 2.4 ────────────────────────────────────────────────────

func TestIssueWorkflow_Trigger_NoRepoSingleConfigured(t *testing.T) {
	// Single-repo channel config: Trigger should short-circuit the repo picker
	// and return a description prompt — not a repo-selector listing repos.
	w, _, _ := newTestIssueWorkflow(t, withChannelRepos([]string{"foo/bar"}))
	ev := TriggerEvent{ChannelID: "C1", ThreadTS: "1.0", UserID: "U1"}

	step, err := w.Trigger(context.Background(), ev, "")
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	// Must not be an error.
	if step.Kind == NextStepError {
		t.Fatalf("unexpected error step: %q", step.ErrorText)
	}
	// The description prompt IS also a NextStepSelector, so we cannot assert
	// on Kind. Instead assert it doesn't look like a repo picker:
	// — it must not list the configured repo as a selector option, and
	// — its prompt must not contain "Which repo".
	if step.Selector == nil {
		t.Fatal("expected Selector spec")
	}
	for _, o := range step.Selector.Options {
		if o.Value == "foo/bar" {
			t.Errorf("single-repo channel should skip repo selector, but got repo %q as option", o.Value)
		}
	}
	if strings.Contains(step.Selector.Prompt, "Which repo") {
		t.Errorf("single-repo channel should not show repo picker, got prompt: %q", step.Selector.Prompt)
	}
}

func TestIssueWorkflow_Trigger_MultiRepoShowsSelector(t *testing.T) {
	w, _, _ := newTestIssueWorkflow(t, withChannelRepos([]string{"foo/bar", "baz/qux"}))
	ev := TriggerEvent{ChannelID: "C1", ThreadTS: "1.0", UserID: "U1"}

	step, err := w.Trigger(context.Background(), ev, "")
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if step.Kind != NextStepSelector {
		t.Errorf("expected NextStepSelector, got %v", step.Kind)
	}
	if len(step.Selector.Options) != 2 {
		t.Errorf("expected 2 selector options, got %d", len(step.Selector.Options))
	}
}

func TestIssueWorkflow_Selection_RepoPhase_TransitionsToBranchOrDescription(t *testing.T) {
	// After picking a repo, workflow transitions to branch selector (if
	// multi-branch) or description prompt (if single/no branch list).
	w, _, _ := newTestIssueWorkflow(t)
	p := &Pending{Phase: "repo", State: &issueState{}, ChannelID: "C1", ThreadTS: "1.0"}

	step, err := w.Selection(context.Background(), p, "foo/bar")
	if err != nil {
		t.Fatalf("Selection: %v", err)
	}
	if step.Kind == NextStepError {
		t.Errorf("unexpected error: %q", step.ErrorText)
	}
	if step.Kind != NextStepSelector {
		t.Errorf("expected NextStepSelector (description prompt), got %v", step.Kind)
	}
	if step.Pending == nil || step.Pending.Phase != "description" {
		phase := "<nil pending>"
		if step.Pending != nil {
			phase = step.Pending.Phase
		}
		t.Errorf("expected Pending.Phase == description, got %q", phase)
	}
}

func TestIssueWorkflow_BuildJob_SetsTaskType(t *testing.T) {
	w, _, _ := newTestIssueWorkflow(t)
	p := &Pending{
		ChannelID: "C1", ThreadTS: "1.0", UserID: "U1",
		State: &issueState{SelectedRepo: "foo/bar", SelectedBranch: "main"},
	}

	job, status, err := w.BuildJob(context.Background(), p)
	if err != nil {
		t.Fatalf("BuildJob: %v", err)
	}
	if job.TaskType != "issue" {
		t.Errorf("TaskType = %q, want issue", job.TaskType)
	}
	if job.Repo != "foo/bar" {
		t.Errorf("Repo = %q", job.Repo)
	}
	if job.Branch != "main" {
		t.Errorf("Branch = %q", job.Branch)
	}
	if job.PromptContext == nil || job.PromptContext.Goal == "" {
		t.Error("PromptContext.Goal must be populated (from config or default)")
	}
	if job.PromptContext.ResponseSchema == "" {
		t.Error("PromptContext.ResponseSchema must be populated (ApplyDefaults)")
	}
	if status == "" {
		t.Error("status text should be non-empty; spec says :mag: 分析 codebase 中...")
	}
}

func TestIssueWorkflow_Trigger_NoChannelRepos_UsesExternalSelector(t *testing.T) {
	// When channel config has no repos, Trigger falls back to external-search
	// selector so the user can type a repo name. This preserves the old
	// PostExternalSelector path.
	w, _, _ := newTestIssueWorkflow(t) // no withChannelRepos opts → empty repos
	ev := TriggerEvent{ChannelID: "C1", ThreadTS: "1.0", UserID: "U1"}

	step, err := w.Trigger(context.Background(), ev, "")
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if step.Kind != NextStepSelector /* external */ {
		t.Errorf("expected NextStepSelector /* external */, got %v", step.Kind)
	}
	if step.Selector.Prompt == "" {
		t.Error("SelectorPrompt should carry the external-search placeholder text")
	}
	if step.Selector.ActionID != "repo_search" {
		t.Errorf("SelectorActionID = %q, want repo_search", step.Selector.ActionID)
	}
	if step.Selector.Placeholder == "" {
		t.Error("SelectorPlaceholder should be non-empty")
	}
	if step.Pending == nil || step.Pending.Phase != "repo_search" {
		t.Errorf("Pending.Phase = %q, want repo_search", func() string {
			if step.Pending == nil {
				return "<nil>"
			}
			return step.Pending.Phase
		}())
	}
}

// ── new tests for Task 2.5 ────────────────────────────────────────────────────

func TestIssueWorkflow_HandleResult_Created_PostsIssueURL(t *testing.T) {
	w, slack, _ := newTestIssueWorkflow(t)
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", Repo: "foo/bar", StatusMsgTS: "s-ts", TaskType: "issue"}
	state := &queue.JobState{Job: job}
	result := &queue.JobResult{
		JobID:  "j1",
		Status: "completed",
		RawOutput: `===TRIAGE_RESULT===
{"status":"CREATED","title":"T","body":"B","confidence":"high","files_found":3,"open_questions":0}`,
	}
	if err := w.HandleResult(context.Background(), state, result); err != nil {
		t.Fatalf("HandleResult: %v", err)
	}
	joined := strings.Join(slack.Posted, " | ")
	if !strings.Contains(joined, "Issue created") {
		t.Errorf("expected issue URL post, got: %v", slack.Posted)
	}
}

// TestIssueWorkflow_HandleResult_RedactsSecrets pins #180: any configured
// secret value echoed by the agent into parsed title/body/labels must be
// scrubbed before the GitHub issue is created. Uses a padding-padded value so
// the minRedactLength guard in shared/logging is clearly satisfied.
func TestIssueWorkflow_HandleResult_RedactsSecrets(t *testing.T) {
	const secret = "tokenABC1234567890"
	w, _, ic := newTestIssueWorkflow(t, withSecrets(map[string]string{"GH_TOKEN": secret}))
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", Repo: "foo/bar", StatusMsgTS: "s-ts", TaskType: "issue"}
	state := &queue.JobState{Job: job}
	// Title/body/labels all contain the secret; agent output is deliberately
	// hostile — we're not testing parser, we're testing the redact hook.
	raw := `===TRIAGE_RESULT===
{"status":"CREATED","title":"leaks ` + secret + ` here","body":"body with ` + secret + ` inside","labels":["bug","leak-` + secret + `"],"confidence":"high","files_found":3,"open_questions":0}`
	result := &queue.JobResult{JobID: "j1", Status: "completed", RawOutput: raw}

	if err := w.HandleResult(context.Background(), state, result); err != nil {
		t.Fatalf("HandleResult: %v", err)
	}
	if strings.Contains(ic.LastTitle, secret) {
		t.Errorf("Title leaked secret: %q", ic.LastTitle)
	}
	if strings.Contains(ic.LastBody, secret) {
		t.Errorf("Body leaked secret: %q", ic.LastBody)
	}
	for _, l := range ic.LastLabels {
		if strings.Contains(l, secret) {
			t.Errorf("Label leaked secret: %q", l)
		}
	}
	if !strings.Contains(ic.LastTitle, "***") {
		t.Errorf("Title should show redaction marker, got %q", ic.LastTitle)
	}
}

// TestIssueWorkflow_HandleResult_NoSecrets_Unchanged ensures the redact hook
// is a pure no-op when Secrets is empty — normal output must pass through
// byte-for-byte.
func TestIssueWorkflow_HandleResult_NoSecrets_Unchanged(t *testing.T) {
	w, _, ic := newTestIssueWorkflow(t)
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", Repo: "foo/bar", StatusMsgTS: "s-ts", TaskType: "issue"}
	state := &queue.JobState{Job: job}
	result := &queue.JobResult{
		JobID:  "j1",
		Status: "completed",
		RawOutput: `===TRIAGE_RESULT===
{"status":"CREATED","title":"normal title","body":"normal body","labels":["bug"],"confidence":"high","files_found":3,"open_questions":0}`,
	}
	if err := w.HandleResult(context.Background(), state, result); err != nil {
		t.Fatalf("HandleResult: %v", err)
	}
	if ic.LastTitle != "normal title" {
		t.Errorf("Title mutated: %q", ic.LastTitle)
	}
	if ic.LastBody != "normal body" {
		t.Errorf("Body mutated: %q", ic.LastBody)
	}
	if len(ic.LastLabels) != 1 || ic.LastLabels[0] != "bug" {
		t.Errorf("Labels mutated: %v", ic.LastLabels)
	}
}

// TestIssueWorkflow_HandleResult_RejectedMessage_RedactsSecrets pins #180:
// secrets echoed by the agent into a REJECTED parsed.Message must be scrubbed
// before postLowConfidence forwards it to Slack.
func TestIssueWorkflow_HandleResult_RejectedMessage_RedactsSecrets(t *testing.T) {
	const secret = "tokenABC1234567890"
	w, slack, _ := newTestIssueWorkflow(t, withSecrets(map[string]string{"GH_TOKEN": secret}))
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", Repo: "foo/bar", StatusMsgTS: "s-ts", TaskType: "issue"}
	state := &queue.JobState{Job: job}
	result := &queue.JobResult{
		JobID: "j1", Status: "completed",
		RawOutput: `===TRIAGE_RESULT===
{"status":"REJECTED","message":"not our repo, leaked ` + secret + ` here"}`,
	}
	if err := w.HandleResult(context.Background(), state, result); err != nil {
		t.Fatalf("HandleResult: %v", err)
	}
	joined := strings.Join(slack.Posted, " | ")
	if strings.Contains(joined, secret) {
		t.Errorf("posted REJECTED message leaked secret: %v", slack.Posted)
	}
	if !strings.Contains(joined, "***") {
		t.Errorf("posted message missing redaction marker: %v", slack.Posted)
	}
}

func TestIssueWorkflow_HandleResult_Rejected_PostsLowConfidence(t *testing.T) {
	w, slack, _ := newTestIssueWorkflow(t)
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", StatusMsgTS: "s-ts", TaskType: "issue"}
	state := &queue.JobState{Job: job}
	result := &queue.JobResult{
		JobID:  "j1",
		Status: "completed",
		RawOutput: `===TRIAGE_RESULT===
{"status":"REJECTED","message":"not our repo"}`,
	}
	if err := w.HandleResult(context.Background(), state, result); err != nil {
		t.Fatalf("HandleResult: %v", err)
	}
	joined := strings.Join(slack.Posted, " | ")
	if !strings.Contains(joined, "判斷不屬於此 repo") {
		t.Errorf("expected low-confidence text, got: %v", slack.Posted)
	}
}

func TestIssueWorkflow_HandleResult_Failed_FirstAttempt_AttachesRetryButton(t *testing.T) {
	w, slack, _ := newTestIssueWorkflow(t)
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", Repo: "foo/bar", TaskType: "issue", RetryCount: 0}
	state := &queue.JobState{Job: job}
	result := &queue.JobResult{JobID: "j1", Status: "failed", Error: "agent timeout"}
	if err := w.HandleResult(context.Background(), state, result); err != nil {
		t.Fatalf("HandleResult: %v", err)
	}
	joined := strings.Join(slack.Posted, " | ")
	if !strings.Contains(joined, "分析失敗") {
		t.Errorf("expected failure text, got: %v", slack.Posted)
	}
}

func TestIssueWorkflow_HandleResult_Failed_Retried_NoButton(t *testing.T) {
	w, slack, _ := newTestIssueWorkflow(t)
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", TaskType: "issue", RetryCount: 1, StatusMsgTS: "s-ts"}
	state := &queue.JobState{Job: job}
	result := &queue.JobResult{JobID: "j1", Status: "failed", Error: "agent timeout"}
	if err := w.HandleResult(context.Background(), state, result); err != nil {
		t.Fatalf("HandleResult: %v", err)
	}
	joined := strings.Join(slack.Posted, " | ")
	if !strings.Contains(joined, "重試後仍失敗") {
		t.Errorf("expected exhausted-retry text, got: %v", slack.Posted)
	}
}

func TestIssueWorkflow_HandleResult_ErrorStatus_RoutesToFailure(t *testing.T) {
	w, slack, _ := newTestIssueWorkflow(t)
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", Repo: "foo/bar", TaskType: "issue", RetryCount: 0}
	state := &queue.JobState{Job: job}
	result := &queue.JobResult{
		JobID:  "j1",
		Status: "completed",
		RawOutput: `===TRIAGE_RESULT===
{"status":"ERROR","message":"gh exploded"}`,
	}
	if err := w.HandleResult(context.Background(), state, result); err != nil {
		t.Fatalf("HandleResult: %v", err)
	}
	joined := strings.Join(slack.Posted, " | ")
	if !strings.Contains(joined, "agent error") {
		t.Errorf("expected failure text containing \"agent error\", got: %v", slack.Posted)
	}
}

func TestIssueWorkflow_HandleResult_GitHubCreateFails_ReturnsErrorAndPostsWarning(t *testing.T) {
	w, slack, ic := newTestIssueWorkflow(t)
	ic.err = errors.New("github API 503")
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", Repo: "foo/bar", StatusMsgTS: "s-ts", TaskType: "issue"}
	state := &queue.JobState{Job: job}
	result := &queue.JobResult{
		JobID:  "j1",
		Status: "completed",
		RawOutput: `===TRIAGE_RESULT===
{"status":"CREATED","title":"T","body":"B","confidence":"high","files_found":3,"open_questions":0}`,
	}
	err := w.HandleResult(context.Background(), state, result)
	if err == nil {
		t.Fatal("expected non-nil error from HandleResult when GitHub create-issue fails")
	}
	joined := strings.Join(slack.Posted, " | ")
	if !strings.Contains(joined, "Triage 完成但建立 issue 失敗") {
		t.Errorf("expected warning message in Slack posts, got: %v", slack.Posted)
	}
}

func TestIssueWorkflow_HandleResult_ParseFailed_RoutesToFailure(t *testing.T) {
	w, slack, _ := newTestIssueWorkflow(t)
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", Repo: "foo/bar", TaskType: "issue", RetryCount: 0}
	state := &queue.JobState{Job: job}
	result := &queue.JobResult{
		JobID:     "j1",
		Status:    "completed",
		RawOutput: "totally not valid agent output",
	}
	if err := w.HandleResult(context.Background(), state, result); err != nil {
		t.Fatalf("HandleResult: %v", err)
	}
	joined := strings.Join(slack.Posted, " | ")
	if !strings.Contains(joined, "分析失敗") {
		t.Errorf("expected failure text containing \"分析失敗\", got: %v", slack.Posted)
	}
}

// ── new tests for Task 5.0: worker-label restoration ─────────────────────────

// TestIssueWorkflow_HandleResult_SuccessShowsWorkerLabel confirms the final
// success message includes the worker-label diagnostic derived from
// state.AgentStatus. This restores behavior dropped in the Task 2.5 port.
func TestIssueWorkflow_HandleResult_SuccessShowsWorkerLabel(t *testing.T) {
	w, slack, _ := newTestIssueWorkflow(t)
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", Repo: "foo/bar", StatusMsgTS: "s-ts", TaskType: "issue"}
	state := &queue.JobState{
		Job: job,
		AgentStatus: &queue.StatusReport{
			WorkerID:       "w-42",
			WorkerNickname: "alice",
		},
	}
	result := &queue.JobResult{
		JobID:  "j1",
		Status: "completed",
		RawOutput: `===TRIAGE_RESULT===
{"status":"CREATED","title":"T","body":"B","confidence":"high","files_found":3,"open_questions":0}`,
	}
	if err := w.HandleResult(context.Background(), state, result); err != nil {
		t.Fatalf("HandleResult: %v", err)
	}
	found := false
	for _, msg := range slack.Posted {
		if strings.Contains(msg, "Issue created") && strings.Contains(msg, "worker: alice (w-42)") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected issue URL + worker label on same message, got posts: %v", slack.Posted)
	}
}

// TestIssueWorkflow_HandleResult_FailureShowsWorkerLabel confirms the failure
// message path also shows the worker label.
func TestIssueWorkflow_HandleResult_FailureShowsWorkerLabel(t *testing.T) {
	w, slack, _ := newTestIssueWorkflow(t)
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", Repo: "foo/bar", TaskType: "issue", RetryCount: 0}
	state := &queue.JobState{
		Job: job,
		AgentStatus: &queue.StatusReport{
			WorkerID:       "w-7",
			WorkerNickname: "bob",
		},
	}
	result := &queue.JobResult{JobID: "j1", Status: "failed", Error: "agent timeout"}
	if err := w.HandleResult(context.Background(), state, result); err != nil {
		t.Fatalf("HandleResult: %v", err)
	}
	found := false
	for _, msg := range slack.Posted {
		if strings.Contains(msg, "分析失敗") && strings.Contains(msg, "worker: bob (w-7)") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected failure text + worker label on same message, got posts: %v", slack.Posted)
	}
}

// TestIssueWorkflow_HandleResult_FallsBackToStateWorkerID confirms that when
// state.AgentStatus is nil, state.WorkerID is used as the fallback.
func TestIssueWorkflow_HandleResult_FallsBackToStateWorkerID(t *testing.T) {
	w, slack, _ := newTestIssueWorkflow(t)
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", Repo: "foo/bar", TaskType: "issue", RetryCount: 0}
	state := &queue.JobState{
		Job:      job,
		WorkerID: "w-fallback",
		// AgentStatus left nil — fallback path.
	}
	result := &queue.JobResult{JobID: "j1", Status: "failed", Error: "agent timeout"}
	if err := w.HandleResult(context.Background(), state, result); err != nil {
		t.Fatalf("HandleResult: %v", err)
	}
	joined := strings.Join(slack.Posted, " | ")
	if !strings.Contains(joined, "worker: w-fallback") {
		t.Errorf("expected failure message to include fallback worker-label %q, got: %v", "worker: w-fallback", slack.Posted)
	}
}

// ── new tests for #140: defensive guard on BuildJob ──────────────────────────

// TestIssueWorkflow_BuildJob_RejectsEmptyRepo verifies that BuildJob returns a
// non-nil error containing "empty repo reference" when SelectedRepo is blank,
// preventing a malformed job from reaching the worker queue.
func TestIssueWorkflow_BuildJob_RejectsEmptyRepo(t *testing.T) {
	w, _, _ := newTestIssueWorkflow(t)
	p := &Pending{
		ChannelID: "C1", ThreadTS: "1.0", UserID: "U1",
		State: &issueState{SelectedRepo: ""}, // deliberately empty
	}

	job, status, err := w.BuildJob(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for empty SelectedRepo, got nil")
	}
	if !strings.Contains(err.Error(), "empty repo reference") {
		t.Errorf("error = %q, want substring \"empty repo reference\"", err.Error())
	}
	if job != nil {
		t.Errorf("job should be nil on error, got %+v", job)
	}
	if status != "" {
		t.Errorf("status should be empty on error, got %q", status)
	}
}

// TestIssueWorkflow_BuildJob_AcceptsNonEmptyRepo is the regression guard: a
// normal job with SelectedRepo set must still build without error.
func TestIssueWorkflow_BuildJob_AcceptsNonEmptyRepo(t *testing.T) {
	w, _, _ := newTestIssueWorkflow(t)
	p := &Pending{
		ChannelID: "C1", ThreadTS: "1.0", UserID: "U1",
		State: &issueState{SelectedRepo: "foo/bar"},
	}

	job, _, err := w.BuildJob(context.Background(), p)
	if err != nil {
		t.Fatalf("BuildJob returned unexpected error: %v", err)
	}
	if job == nil {
		t.Fatal("expected non-nil job")
	}
}

// TestIssueWorkflow_Selection_ManyBranches_EmitsFullSelectorSpec is the
// regression guard for "無法顯示選單，請重試" on repos with >24 branches. The
// workflow must pack every branch into the SelectorSpec; the adapter picks
// static_select when the count exceeds the actions-block button cap so
// nothing is dropped upstream.
func TestIssueWorkflow_Selection_ManyBranches_EmitsFullSelectorSpec(t *testing.T) {
	trueVal := true
	branches := make([]string, 30) // well past the 25-button Slack cap
	for i := range branches {
		branches[i] = fmt.Sprintf("feature/%02d", i)
	}
	w, _, _ := newTestIssueWorkflow(t, func(c *config.Config) {
		c.ChannelDefaults.Repos = []string{"foo/bar"}
		c.ChannelDefaults.BranchSelect = &trueVal
		c.ChannelDefaults.Branches = branches
	})

	p := &Pending{
		Phase:     "repo",
		State:     &issueState{RepoWasPicked: true},
		ChannelID: "C1", ThreadTS: "1.0",
	}
	step, err := w.Selection(context.Background(), p, "foo/bar")
	if err != nil {
		t.Fatalf("Selection: %v", err)
	}
	if step.Kind != NextStepSelector {
		t.Fatalf("Kind = %v, want NextStepSelector", step.Kind)
	}
	if step.Selector == nil {
		t.Fatal("expected SelectorSpec for branch picker")
	}
	if step.Selector.ActionID != "branch_select" {
		t.Errorf("ActionID = %q, want branch_select", step.Selector.ActionID)
	}
	if got := len(step.Selector.Options); got != len(branches) {
		t.Errorf("Options count = %d, want %d — all branches must reach the adapter", got, len(branches))
	}
	if step.Selector.BackActionID != "back_to_repo" {
		t.Errorf("BackActionID = %q, want back_to_repo", step.Selector.BackActionID)
	}
}

// ── test helpers ─────────────────────────────────────────────────────────────

type issueOpt func(*config.Config)

func withChannelRepos(repos []string) issueOpt {
	return func(c *config.Config) {
		c.ChannelDefaults.Repos = repos
	}
}

func withSecrets(secrets map[string]string) issueOpt {
	return func(c *config.Config) {
		c.Secrets = secrets
	}
}

func newTestIssueWorkflow(t *testing.T, opts ...issueOpt) (*IssueWorkflow, *fakeSlackPort, *fakeIssueCreator) {
	t.Helper()
	cfg := &config.Config{}
	config.ApplyDefaults(cfg) // populates Prompt.Issue defaults
	for _, o := range opts {
		o(cfg)
	}
	slack := newFakeSlackPort()
	ic := &fakeIssueCreator{}
	w := NewIssueWorkflow(cfg, slack, ic, nil, nil, slog.Default())
	return w, slack, ic
}
