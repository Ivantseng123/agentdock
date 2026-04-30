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
	// Single-repo channel ensures the new ref-decide phase auto-skips
	// (0 candidates after primary filtered out — spec AC-I11 mirror).
	w, _, _ := newTestIssueWorkflow(t, withChannelRepos([]string{"foo/bar"}))
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

// ── ref-flow phase tests (mirror ask_test.go ref tests) ──────────────────────

// TestIssueWorkflow_RefFlow_DecidePromptOffered covers the entry to the ref
// flow after primary branch is picked: ref candidates exist (channel has
// extra repos beyond primary) so issue_ref_decide fires.
func TestIssueWorkflow_RefFlow_DecidePromptOffered(t *testing.T) {
	w, _, _ := newTestIssueWorkflow(t, withChannelRepos([]string{"foo/bar", "baz/qux", "third/repo"}))
	p := &Pending{
		Phase:     "branch",
		State:     &issueState{SelectedRepo: "foo/bar"},
		ChannelID: "C1", ThreadTS: "1.0",
	}
	step, err := w.Selection(context.Background(), p, "main")
	if err != nil {
		t.Fatal(err)
	}
	if p.Phase != "issue_ref_decide" {
		t.Fatalf("Phase = %q, want issue_ref_decide", p.Phase)
	}
	if len(step.Selector.Options) != 2 {
		t.Errorf("decide selector should have 2 options (加入/不用), got %d", len(step.Selector.Options))
	}
}

// TestIssueWorkflow_RefFlow_ZeroCandidatesSkipsDecide covers AC-I11 mirror:
// when the channel's static repo list contains only primary, the ref decide
// phase is skipped entirely so the thread doesn't see "加入參考 repo？".
func TestIssueWorkflow_RefFlow_ZeroCandidatesSkipsDecide(t *testing.T) {
	w, _, _ := newTestIssueWorkflow(t, withChannelRepos([]string{"foo/bar"}))
	p := &Pending{
		Phase:     "branch",
		State:     &issueState{SelectedRepo: "foo/bar"},
		ChannelID: "C1", ThreadTS: "1.0",
	}
	_, err := w.Selection(context.Background(), p, "main")
	if err != nil {
		t.Fatal(err)
	}
	if p.Phase == "issue_ref_decide" {
		t.Errorf("Phase = issue_ref_decide; want it skipped (0 candidates)")
	}
	if p.Phase != "description" {
		t.Errorf("Phase = %q, want description", p.Phase)
	}
}

// TestIssueWorkflow_RefFlow_DecideAddRoutesToPick covers the "加入" → ref
// pick transition. AddRefs flips true and the picker phase appears.
func TestIssueWorkflow_RefFlow_DecideAddRoutesToPick(t *testing.T) {
	w, _, _ := newTestIssueWorkflow(t, withChannelRepos([]string{"foo/bar", "baz/qux"}))
	p := &Pending{
		Phase:     "issue_ref_decide",
		State:     &issueState{SelectedRepo: "foo/bar"},
		ChannelID: "C1", ThreadTS: "1.0",
	}
	_, err := w.Selection(context.Background(), p, "add")
	if err != nil {
		t.Fatal(err)
	}
	if p.Phase != "issue_ref_pick" {
		t.Errorf("Phase = %q, want issue_ref_pick", p.Phase)
	}
	st := p.State.(*issueState)
	if !st.AddRefs {
		t.Error("AddRefs should be true after 加入")
	}
}

// TestIssueWorkflow_RefFlow_DecideSkipRoutesToDescription covers the "不用"
// path — AddRefs stays false and we proceed to the existing description
// flow without touching ref state.
func TestIssueWorkflow_RefFlow_DecideSkipRoutesToDescription(t *testing.T) {
	w, _, _ := newTestIssueWorkflow(t, withChannelRepos([]string{"foo/bar", "baz/qux"}))
	p := &Pending{
		Phase:     "issue_ref_decide",
		State:     &issueState{SelectedRepo: "foo/bar"},
		ChannelID: "C1", ThreadTS: "1.0",
	}
	_, err := w.Selection(context.Background(), p, "skip")
	if err != nil {
		t.Fatal(err)
	}
	if p.Phase != "description" {
		t.Errorf("Phase = %q, want description", p.Phase)
	}
	st := p.State.(*issueState)
	if st.AddRefs {
		t.Error("AddRefs should remain false after skip")
	}
	if len(st.RefRepos) != 0 {
		t.Errorf("RefRepos should be empty, got %v", st.RefRepos)
	}
}

// TestIssueWorkflow_RefFlow_PickFiltersPrimary covers AC-I10 mirror: primary
// must not appear in the ref candidate list.
func TestIssueWorkflow_RefFlow_PickFiltersPrimary(t *testing.T) {
	w, _, _ := newTestIssueWorkflow(t, withChannelRepos([]string{"foo/bar", "baz/qux", "third/repo"}))
	p := &Pending{
		Phase:     "issue_ref_decide",
		State:     &issueState{SelectedRepo: "foo/bar"},
		ChannelID: "C1", ThreadTS: "1.0",
	}
	step, err := w.Selection(context.Background(), p, "add")
	if err != nil {
		t.Fatal(err)
	}
	values := selectorValues(step.Selector.Options)
	for _, v := range values {
		if v == "foo/bar" {
			t.Errorf("primary foo/bar must not appear in ref candidates; got %v", values)
		}
	}
	wantSet := map[string]bool{"baz/qux": true, "third/repo": true, "back_to_decide": true}
	for _, v := range values {
		if !wantSet[v] {
			t.Errorf("unexpected ref candidate value: %q (got %v)", v, values)
		}
	}
}

// TestIssueWorkflow_RefFlow_PickGoesToContinueWhenNoBranchSelect covers the
// inline flow when branch_select is disabled: pick a ref → branch loop drains
// instantly → land on continue ("再加一個 / 開始建 issue").
func TestIssueWorkflow_RefFlow_PickGoesToContinueWhenNoBranchSelect(t *testing.T) {
	w, _, _ := newTestIssueWorkflow(t, withChannelRepos([]string{"foo/bar", "baz/qux", "third/repo"}))
	// branch_select default false → ref branch picker skipped per ref.
	p := &Pending{
		Phase:     "issue_ref_pick",
		State:     &issueState{SelectedRepo: "foo/bar", AddRefs: true},
		ChannelID: "C1", ThreadTS: "1.0",
	}
	step, err := w.Selection(context.Background(), p, "baz/qux")
	if err != nil {
		t.Fatal(err)
	}
	if p.Phase != "issue_ref_continue" {
		t.Errorf("Phase = %q, want issue_ref_continue", p.Phase)
	}
	st := p.State.(*issueState)
	if len(st.RefRepos) != 1 || st.RefRepos[0].Repo != "baz/qux" {
		t.Errorf("RefRepos = %v, want one entry of baz/qux", st.RefRepos)
	}
	if st.RefRepos[0].CloneURL == "" {
		t.Error("RefRepos[0].CloneURL should be populated by cleanCloneURL")
	}
	values := selectorValues(step.Selector.Options)
	wantSet := map[string]bool{"more": true, "done": true}
	for _, v := range values {
		if !wantSet[v] {
			t.Errorf("unexpected continue option %q (got %v)", v, values)
		}
	}
}

// TestIssueWorkflow_RefFlow_PickGoesToBranchWhenBranchSelect covers the
// inline interleaved flow: pick a ref → issue_ref_branch fires for THAT ref
// (not all refs collected first). Mirrors ask Q4 grill — inline matches
// primary's flow shape.
func TestIssueWorkflow_RefFlow_PickGoesToBranchWhenBranchSelect(t *testing.T) {
	w, _, _ := newTestIssueWorkflow(t, withChannelRepos([]string{"foo/bar", "baz/qux", "third/repo"}))
	trueVal := true
	w.cfg.ChannelDefaults.BranchSelect = &trueVal
	w.cfg.ChannelDefaults.Branches = []string{"main", "release"}
	p := &Pending{
		Phase:     "issue_ref_pick",
		State:     &issueState{SelectedRepo: "foo/bar", AddRefs: true},
		ChannelID: "C1", ThreadTS: "1.0",
	}
	step, err := w.Selection(context.Background(), p, "baz/qux")
	if err != nil {
		t.Fatal(err)
	}
	if p.Phase != "issue_ref_branch" {
		t.Fatalf("Phase = %q, want issue_ref_branch (inline branch pick)", p.Phase)
	}
	st := p.State.(*issueState)
	if st.BranchTargetRepo != "baz/qux" {
		t.Errorf("BranchTargetRepo = %q, want baz/qux (the just-picked ref)", st.BranchTargetRepo)
	}
	if !strings.Contains(step.Selector.Prompt, "baz/qux") {
		t.Errorf("prompt should reference baz/qux; got %q", step.Selector.Prompt)
	}
}

// TestIssueWorkflow_RefFlow_PickDedupAlreadyPicked covers AC-I10 mirror:
// same repo can't appear twice in the candidate list.
func TestIssueWorkflow_RefFlow_PickDedupAlreadyPicked(t *testing.T) {
	w, _, _ := newTestIssueWorkflow(t, withChannelRepos([]string{"foo/bar", "baz/qux", "third/repo"}))
	p := &Pending{
		Phase: "issue_ref_continue",
		State: &issueState{
			SelectedRepo: "foo/bar",
			AddRefs:      true,
			RefRepos:     []queue.RefRepo{{Repo: "baz/qux", CloneURL: "https://github.com/baz/qux.git"}},
		},
		ChannelID: "C1", ThreadTS: "1.0",
	}
	step, err := w.Selection(context.Background(), p, "more")
	if err != nil {
		t.Fatal(err)
	}
	if p.Phase != "issue_ref_pick" {
		t.Errorf("Phase = %q, want issue_ref_pick", p.Phase)
	}
	values := selectorValues(step.Selector.Options)
	for _, v := range values {
		if v == "baz/qux" {
			t.Errorf("already-picked baz/qux must not appear in candidates; got %v", values)
		}
	}
	found := false
	for _, v := range values {
		if v == "third/repo" {
			found = true
		}
	}
	if !found {
		t.Errorf("third/repo missing from candidates: %v", values)
	}
}

// TestIssueWorkflow_RefFlow_ContinueExhaustedPoolHidesMore covers a UX
// detail: when the static candidate pool is fully consumed, "再加一個" drops
// from the continue selector.
func TestIssueWorkflow_RefFlow_ContinueExhaustedPoolHidesMore(t *testing.T) {
	w, _, _ := newTestIssueWorkflow(t, withChannelRepos([]string{"foo/bar", "baz/qux"}))
	p := &Pending{
		Phase: "issue_ref_pick",
		State: &issueState{
			SelectedRepo: "foo/bar",
			AddRefs:      true,
		},
		ChannelID: "C1", ThreadTS: "1.0",
	}
	step, err := w.Selection(context.Background(), p, "baz/qux")
	if err != nil {
		t.Fatal(err)
	}
	if p.Phase != "issue_ref_continue" {
		t.Fatalf("Phase = %q, want issue_ref_continue (no branch_select → straight to continue)", p.Phase)
	}
	values := selectorValues(step.Selector.Options)
	for _, v := range values {
		if v == "more" {
			t.Errorf("'more' option should be hidden when pool is exhausted; got %v", values)
		}
	}
	if len(values) != 1 || values[0] != "done" {
		t.Errorf("expected only 'done' option, got %v", values)
	}
}

// TestIssueWorkflow_RefFlow_ContinueDoneRoutesToDescription covers
// "開始建 issue" — exits the ref loop entirely. Branches are already filled
// per-ref during pick (inline flow); done has no branch loop to enter.
func TestIssueWorkflow_RefFlow_ContinueDoneRoutesToDescription(t *testing.T) {
	w, _, _ := newTestIssueWorkflow(t, withChannelRepos([]string{"foo/bar", "baz/qux"}))
	p := &Pending{
		Phase: "issue_ref_continue",
		State: &issueState{
			SelectedRepo: "foo/bar",
			AddRefs:      true,
			RefRepos: []queue.RefRepo{
				{Repo: "baz/qux", CloneURL: "https://github.com/baz/qux.git"},
			},
		},
		ChannelID: "C1", ThreadTS: "1.0",
	}
	_, err := w.Selection(context.Background(), p, "done")
	if err != nil {
		t.Fatal(err)
	}
	if p.Phase != "description" {
		t.Errorf("Phase = %q, want description", p.Phase)
	}
}

// TestIssueWorkflow_RefFlow_InlineBranchPickerPerRef covers the full inline
// flow: pick ref1 → branch1 → continue → pick ref2 → branch2 → done. Each
// ref's branch is asked immediately after picking that ref.
func TestIssueWorkflow_RefFlow_InlineBranchPickerPerRef(t *testing.T) {
	w, _, _ := newTestIssueWorkflow(t, withChannelRepos([]string{"foo/bar", "frontend/web", "backend/api"}))
	trueVal := true
	w.cfg.ChannelDefaults.BranchSelect = &trueVal
	w.cfg.ChannelDefaults.Branches = []string{"main", "release"}
	p := &Pending{
		Phase: "issue_ref_pick",
		State: &issueState{
			SelectedRepo: "foo/bar",
			AddRefs:      true,
		},
		ChannelID: "C1", ThreadTS: "1.0",
	}

	step, err := w.Selection(context.Background(), p, "frontend/web")
	if err != nil {
		t.Fatal(err)
	}
	if p.Phase != "issue_ref_branch" {
		t.Fatalf("after ref1 pick, Phase = %q, want issue_ref_branch", p.Phase)
	}
	st := p.State.(*issueState)
	if st.BranchTargetRepo != "frontend/web" {
		t.Errorf("BranchTargetRepo = %q, want frontend/web", st.BranchTargetRepo)
	}
	if !strings.Contains(step.Selector.Prompt, "frontend/web") {
		t.Errorf("prompt should reference frontend/web; got %q", step.Selector.Prompt)
	}

	_, err = w.Selection(context.Background(), p, "main")
	if err != nil {
		t.Fatal(err)
	}
	if p.Phase != "issue_ref_continue" {
		t.Fatalf("after ref1 branch pick, Phase = %q, want issue_ref_continue", p.Phase)
	}
	if st.RefRepos[0].Branch != "main" {
		t.Errorf("ref[0].Branch = %q, want main", st.RefRepos[0].Branch)
	}

	_, err = w.Selection(context.Background(), p, "more")
	if err != nil {
		t.Fatal(err)
	}
	if p.Phase != "issue_ref_pick" {
		t.Fatalf("after 'more', Phase = %q, want issue_ref_pick", p.Phase)
	}

	_, err = w.Selection(context.Background(), p, "backend/api")
	if err != nil {
		t.Fatal(err)
	}
	if p.Phase != "issue_ref_branch" {
		t.Fatalf("after ref2 pick, Phase = %q, want issue_ref_branch", p.Phase)
	}
	if st.BranchTargetRepo != "backend/api" {
		t.Errorf("BranchTargetRepo = %q, want backend/api", st.BranchTargetRepo)
	}

	_, err = w.Selection(context.Background(), p, "release")
	if err != nil {
		t.Fatal(err)
	}
	if p.Phase != "issue_ref_continue" {
		t.Fatalf("after ref2 branch pick, Phase = %q, want issue_ref_continue", p.Phase)
	}
	if st.RefRepos[1].Branch != "release" {
		t.Errorf("ref[1].Branch = %q, want release", st.RefRepos[1].Branch)
	}

	_, err = w.Selection(context.Background(), p, "done")
	if err != nil {
		t.Fatal(err)
	}
	if p.Phase != "description" {
		t.Errorf("after 'done', Phase = %q, want description", p.Phase)
	}
}

// TestIssueWorkflow_BuildJob_WithRefs_PopulatesJobAndRules covers AC-I9:
// Job.RefRepos passthrough + dynamic output_rules injection of THREE rules
// (Issue has the additional `## Related repos` H2 spelling rule beyond Ask's
// two).
func TestIssueWorkflow_BuildJob_WithRefs_PopulatesJobAndRules(t *testing.T) {
	w, _, _ := newTestIssueWorkflow(t)
	w.cfg.Workflows.Issue.Prompt.OutputRules = []string{"existing rule"}
	p := &Pending{
		ChannelID:   "C1",
		ThreadTS:    "1.0",
		Reporter:    "Alice",
		ChannelName: "general",
		State: &issueState{
			SelectedRepo: "foo/bar",
			RefRepos: []queue.RefRepo{
				{Repo: "frontend/web", CloneURL: "https://github.com/frontend/web.git", Branch: "main"},
				{Repo: "backend/api", CloneURL: "https://github.com/backend/api.git"},
			},
		},
	}
	job, _, err := w.BuildJob(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(job.RefRepos) != 2 {
		t.Errorf("Job.RefRepos len = %d, want 2", len(job.RefRepos))
	}
	rules := job.PromptContext.OutputRules
	if len(rules) != 4 {
		t.Fatalf("OutputRules len = %d, want 4 (existing + 3 injected); got %v", len(rules), rules)
	}
	if rules[0] != "existing rule" {
		t.Errorf("existing rule should remain at index 0; got %q", rules[0])
	}
	if !strings.Contains(rules[1], "不可寫入") {
		t.Errorf("rule[1] should be read-only enforcement; got %q", rules[1])
	}
	if !strings.Contains(rules[2], "AGENTDOCK:CRITICAL_REF_UNAVAILABLE") {
		t.Errorf("rule[2] should mention sentinel; got %q", rules[2])
	}
	if !strings.Contains(rules[3], "Related repos") {
		t.Errorf("rule[3] should require ## Related repos heading; got %q", rules[3])
	}
}

// TestIssueWorkflow_BuildJob_NoRefs_NoRulesInjected covers the regression
// case: no refs picked → output_rules unchanged from configured value.
func TestIssueWorkflow_BuildJob_NoRefs_NoRulesInjected(t *testing.T) {
	w, _, _ := newTestIssueWorkflow(t)
	w.cfg.Workflows.Issue.Prompt.OutputRules = []string{"existing rule"}
	p := &Pending{
		ChannelID:   "C1",
		ThreadTS:    "1.0",
		Reporter:    "Alice",
		ChannelName: "general",
		State: &issueState{
			SelectedRepo: "foo/bar",
		},
	}
	job, _, err := w.BuildJob(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(job.RefRepos) != 0 {
		t.Errorf("Job.RefRepos = %v, want empty", job.RefRepos)
	}
	rules := job.PromptContext.OutputRules
	if len(rules) != 1 || rules[0] != "existing rule" {
		t.Errorf("OutputRules = %v, want only 'existing rule'", rules)
	}
}

// TestIssueState_RefExclusions_PrimaryAndPicked covers the RefExclusionReader
// implementation used by HandleRefRepoSuggestion to filter type-ahead
// results in external-search channels.
func TestIssueState_RefExclusions_PrimaryAndPicked(t *testing.T) {
	st := &issueState{
		SelectedRepo: "foo/bar",
		RefRepos: []queue.RefRepo{
			{Repo: "frontend/web"},
			{Repo: "backend/api"},
		},
	}
	excl := st.RefExclusions()
	if len(excl) != 3 {
		t.Fatalf("RefExclusions len = %d, want 3 (primary + 2 picked)", len(excl))
	}
	want := map[string]bool{"foo/bar": true, "frontend/web": true, "backend/api": true}
	for _, r := range excl {
		if !want[r] {
			t.Errorf("unexpected exclusion: %q", r)
		}
	}
}

// TestIssueState_BranchSelectedRepo_FollowsBranchTarget asserts the transient
// field swap that lets a single BranchStateReader interface serve both
// primary and per-ref branch picking.
func TestIssueState_BranchSelectedRepo_FollowsBranchTarget(t *testing.T) {
	st := &issueState{
		SelectedRepo:     "foo/bar",
		BranchTargetRepo: "foo/bar",
	}
	if got := st.BranchSelectedRepo(); got != "foo/bar" {
		t.Errorf("primary phase: BranchSelectedRepo = %q, want foo/bar", got)
	}
	st.BranchTargetRepo = "frontend/web"
	if got := st.BranchSelectedRepo(); got != "frontend/web" {
		t.Errorf("ref phase: BranchSelectedRepo = %q, want frontend/web", got)
	}
}

// ── createAndPostIssue 5-step pipeline tests (Issue-specific) ────────────────

// TestCreateAndPostIssue_S1_RefViolations_FailsAndDoesNotPush asserts that a
// JobResult carrying RefViolations from the worker triggers the strict
// fail-fast path: no GitHub push, banner names the violating refs, metric
// emits with rejected_ref_violation outcome. (AC-I7)
func TestCreateAndPostIssue_S1_RefViolations_FailsAndDoesNotPush(t *testing.T) {
	w, slack, ic := newTestIssueWorkflow(t)
	job := &queue.Job{
		ID: "j1", Repo: "foo/bar", ChannelID: "C1", ThreadTS: "1.0",
	}
	state := &queue.JobState{Job: job}
	r := &queue.JobResult{
		JobID:         "j1",
		Status:        "completed",
		RawOutput:     "===TRIAGE_RESULT===\n{\"status\":\"CREATED\",\"title\":\"x\",\"body\":\"## Description\\n\\nfoo\"}",
		RefViolations: []string{"frontend/web"},
	}
	parsed := TriageResult{Status: "CREATED", Title: "x", Body: "## Description\n\nfoo"}

	if err := w.createAndPostIssue(context.Background(), state, r, parsed); err != nil {
		t.Fatalf("createAndPostIssue: %v", err)
	}

	if ic.LastTitle != "" {
		t.Errorf("CreateIssue should NOT have been called on s1 fail; got title=%q", ic.LastTitle)
	}
	if got := slack.LastPosted(); got == "" || !strings.Contains(got, "frontend/web") {
		t.Errorf("Slack banner should mention violating ref; got %q", got)
	}
	if got := slack.LastPosted(); !strings.Contains(got, "違規寫入") {
		t.Errorf("Slack banner should explain the failure; got %q", got)
	}
}

// TestCreateAndPostIssue_S2_CriticalSentinel_FailsAndDoesNotPush asserts
// that an agent body containing the HTML-comment sentinel triggers the
// critical-unavailable fail-fast path: no GitHub push, banner names the
// unavailable refs from PromptContext (not from the sentinel — it's bare).
// (AC-I12)
func TestCreateAndPostIssue_S2_CriticalSentinel_FailsAndDoesNotPush(t *testing.T) {
	w, slack, ic := newTestIssueWorkflow(t)
	job := &queue.Job{
		ID: "j1", Repo: "foo/bar", ChannelID: "C1", ThreadTS: "1.0",
		PromptContext: &queue.PromptContext{
			UnavailableRefs: []string{"broken/repo"},
		},
	}
	state := &queue.JobState{Job: job}
	body := "<!-- AGENTDOCK:CRITICAL_REF_UNAVAILABLE -->\n\n## 無法回答\n\nbackend ref unreachable."
	r := &queue.JobResult{JobID: "j1", Status: "completed", RawOutput: "x"}
	parsed := TriageResult{Status: "CREATED", Title: "x", Body: body}

	if err := w.createAndPostIssue(context.Background(), state, r, parsed); err != nil {
		t.Fatalf("createAndPostIssue: %v", err)
	}

	if ic.LastTitle != "" {
		t.Errorf("CreateIssue should NOT have been called on sentinel; got title=%q", ic.LastTitle)
	}
	if got := slack.LastPosted(); !strings.Contains(got, "broken/repo") {
		t.Errorf("Slack banner should list UnavailableRefs; got %q", got)
	}
	if got := slack.LastPosted(); !strings.Contains(got, "關鍵脈絡") {
		t.Errorf("Slack banner should explain critical-unavailable failure; got %q", got)
	}
}

// TestCreateAndPostIssue_S3_AgentWroteRelatedRepos_NotPrepended asserts that
// when the agent already wrote a `## Related repos` section, the worker
// does NOT prepend a duplicate. (AC-I4)
func TestCreateAndPostIssue_S3_AgentWroteRelatedRepos_NotPrepended(t *testing.T) {
	w, _, ic := newTestIssueWorkflow(t)
	job := &queue.Job{
		ID: "j1", Repo: "foo/bar", Branch: "main", ChannelID: "C1", ThreadTS: "1.0",
		RefRepos: []queue.RefRepo{
			{Repo: "frontend/web", Branch: "main"},
		},
	}
	state := &queue.JobState{Job: job}
	body := "## Related repos\n\n- `frontend/web@main` — root cause suspect\n\n## Description\n\nfoo"
	r := &queue.JobResult{JobID: "j1", Status: "completed"}
	parsed := TriageResult{Status: "CREATED", Title: "x", Body: body}

	if err := w.createAndPostIssue(context.Background(), state, r, parsed); err != nil {
		t.Fatalf("createAndPostIssue: %v", err)
	}

	if ic.LastBody == "" {
		t.Fatal("CreateIssue should have been called with body")
	}
	// Worker must not have prepended a second `## Related repos` heading.
	headingCount := strings.Count(strings.ToLower(ic.LastBody), "## related repos")
	if headingCount != 1 {
		t.Errorf("expected exactly 1 `## Related repos` heading, got %d in body:\n%s", headingCount, ic.LastBody)
	}
	// Agent's role description must survive (no truncation/replacement).
	if !strings.Contains(ic.LastBody, "root cause suspect") {
		t.Errorf("agent's role description lost; body=%q", ic.LastBody)
	}
}

// TestCreateAndPostIssue_S3_AgentMissed_PrependsMinimal asserts that when
// the agent forgot to include `## Related repos`, the worker prepends a
// minimal version derived from Job.RefRepos using the `reference context`
// placeholder. (AC-I5)
func TestCreateAndPostIssue_S3_AgentMissed_PrependsMinimal(t *testing.T) {
	w, _, ic := newTestIssueWorkflow(t)
	job := &queue.Job{
		ID: "j1", Repo: "foo/bar", Branch: "main", ChannelID: "C1", ThreadTS: "1.0",
		RefRepos: []queue.RefRepo{
			{Repo: "frontend/web", Branch: "main"},
			{Repo: "backend/api"},
		},
	}
	state := &queue.JobState{Job: job}
	body := "## Description\n\nfoo bar"
	r := &queue.JobResult{JobID: "j1", Status: "completed"}
	parsed := TriageResult{Status: "CREATED", Title: "x", Body: body}

	if err := w.createAndPostIssue(context.Background(), state, r, parsed); err != nil {
		t.Fatalf("createAndPostIssue: %v", err)
	}
	if ic.LastBody == "" {
		t.Fatal("CreateIssue should have been called with body")
	}
	// Body should now start with `## Related repos`.
	if !strings.HasPrefix(ic.LastBody, "## Related repos\n\n") {
		t.Errorf("body should start with ## Related repos; got prefix:\n%s", ic.LastBody[:min(120, len(ic.LastBody))])
	}
	// Primary entry uses the Chinese parenthetical role.
	if !strings.Contains(ic.LastBody, "`foo/bar@main` — primary（issue 開立目標）") {
		t.Errorf("primary entry missing or wrong format; body=%q", ic.LastBody)
	}
	// Refs use the placeholder `reference context`.
	if !strings.Contains(ic.LastBody, "`frontend/web@main` — reference context") {
		t.Errorf("ref1 entry missing/wrong format; body=%q", ic.LastBody)
	}
	// Ref without branch should omit the @branch suffix.
	if !strings.Contains(ic.LastBody, "`backend/api` — reference context") {
		t.Errorf("ref2 (no branch) entry missing/wrong format; body=%q", ic.LastBody)
	}
	// Original body content survives after the prepended section.
	if !strings.Contains(ic.LastBody, "foo bar") {
		t.Errorf("original body content lost; got %q", ic.LastBody)
	}
}

// TestCreateAndPostIssue_S3_NoRefs_NoOp asserts that the body normalization
// pipeline is a no-op when no refs are attached — pushed body equals the
// agent body byte-for-byte (modulo Redact passthrough). (AC-I6)
func TestCreateAndPostIssue_S3_NoRefs_NoOp(t *testing.T) {
	w, _, ic := newTestIssueWorkflow(t)
	job := &queue.Job{
		ID: "j1", Repo: "foo/bar", Branch: "main", ChannelID: "C1", ThreadTS: "1.0",
	}
	state := &queue.JobState{Job: job}
	body := "## Description\n\nfoo bar"
	r := &queue.JobResult{JobID: "j1", Status: "completed"}
	parsed := TriageResult{Status: "CREATED", Title: "x", Body: body}

	if err := w.createAndPostIssue(context.Background(), state, r, parsed); err != nil {
		t.Fatalf("createAndPostIssue: %v", err)
	}
	if ic.LastBody != body {
		t.Errorf("body should be unchanged when no refs; got %q want %q", ic.LastBody, body)
	}
	if strings.Contains(ic.LastBody, "## Related repos") {
		t.Errorf("body should NOT contain ## Related repos when no refs; got %q", ic.LastBody)
	}
}

// TestHasRelatedReposSection_RegexVariants table-tests the loose regex used
// for auto-fill detection. Hits should cover normal LLM output variation
// (singular/plural, case, H1-H4); misses should cover non-heading mentions
// (bold inline, Chinese, plain text). (AC-I10 + grill Q3)
func TestHasRelatedReposSection_RegexVariants(t *testing.T) {
	hits := []string{
		"## Related repos\n\n- foo",
		"## Related Repos\n",
		"## Related Repositories\n",
		"### Related Repo\n",
		"# RELATED REPOS\n",
		"## related repo\n",
		"#### Related repos\n",
	}
	misses := []string{
		"## 相關 repos\n",
		"**Related repos:**\n",
		"This issue is related to backend repos.\n",
		"## Description\n\n關於 related repos 的事...\n",
		"",
	}
	for _, in := range hits {
		if !hasRelatedReposSection(in) {
			t.Errorf("expected hit, got miss for: %q", in)
		}
	}
	for _, in := range misses {
		if hasRelatedReposSection(in) {
			t.Errorf("expected miss, got hit for: %q", in)
		}
	}
}

// TestCreateAndPostIssue_PipelineOrder_S1BeforeS2 asserts that when both
// strict guard and critical sentinel signals are present, s1 (RefViolations)
// fires first — outcome metric is rejected_ref_violation, not
// rejected_critical_ref. Order matters because each step is a return path.
func TestCreateAndPostIssue_PipelineOrder_S1BeforeS2(t *testing.T) {
	w, slack, ic := newTestIssueWorkflow(t)
	job := &queue.Job{
		ID: "j1", Repo: "foo/bar", ChannelID: "C1", ThreadTS: "1.0",
		PromptContext: &queue.PromptContext{
			UnavailableRefs: []string{"broken/repo"},
		},
	}
	state := &queue.JobState{Job: job}
	body := "<!-- AGENTDOCK:CRITICAL_REF_UNAVAILABLE -->\n\n## Body\n\n..."
	r := &queue.JobResult{
		JobID:         "j1",
		Status:        "completed",
		RefViolations: []string{"frontend/web"}, // s1 signal
	}
	parsed := TriageResult{Status: "CREATED", Title: "x", Body: body}

	if err := w.createAndPostIssue(context.Background(), state, r, parsed); err != nil {
		t.Fatalf("createAndPostIssue: %v", err)
	}
	if ic.LastTitle != "" {
		t.Errorf("CreateIssue should NOT have been called; got title=%q", ic.LastTitle)
	}
	// s1 banner mentions frontend/web (the violating ref); s2 banner would
	// mention broken/repo. Confirm s1 won.
	got := slack.LastPosted()
	if !strings.Contains(got, "frontend/web") {
		t.Errorf("expected s1 banner (frontend/web), got %q", got)
	}
	if strings.Contains(got, "broken/repo") {
		t.Errorf("s2 fired before s1 — banner contains broken/repo: %q", got)
	}
}

// min is a tiny helper for slicing logic in error messages.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
