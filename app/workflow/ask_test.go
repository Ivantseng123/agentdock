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
	if step.Kind != NextStepSelector {
		t.Errorf("expected NextStepSelector, got %v", step.Kind)
	}
	if len(step.Selector.Options) != 2 {
		t.Errorf("expected 2 actions (attach/skip), got %d", len(step.Selector.Options))
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

func TestAskWorkflow_Selection_SkipRoutesToDescriptionPrompt(t *testing.T) {
	w, _ := newTestAskWorkflow(t)
	p := &Pending{Phase: "ask_repo_prompt", State: &askState{Question: "Q"}, ChannelID: "C1", ThreadTS: "1.0"}
	step, err := w.Selection(context.Background(), p, "skip")
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != NextStepSelector {
		t.Errorf("expected NextStepSelector (description prompt), got %v", step.Kind)
	}
	if p.Phase != "ask_description_prompt" {
		t.Errorf("Phase = %q, want ask_description_prompt", p.Phase)
	}
	st := p.State.(*askState)
	if st.AttachRepo {
		t.Error("AttachRepo should be false on skip")
	}
	// ActionID reuses description_action so app.go's existing route
	// handles the modal-trigger-id forwarding without a new special case.
	if step.Selector.ActionID != "description_action" {
		t.Errorf("ActionID = %q, want description_action", step.Selector.ActionID)
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
	if step.Kind != NextStepSelector {
		t.Errorf("expected NextStepSelector (repo choice), got %v", step.Kind)
	}
}

func TestAskWorkflow_Selection_RepoChoiceRoutesToDescriptionPrompt(t *testing.T) {
	// branch_select defaults to nil (disabled), so after repo pick we skip
	// the branch step entirely and jump straight to description prompt.
	w, _ := newTestAskWorkflow(t)
	p := &Pending{Phase: "ask_repo_select", State: &askState{Question: "Q", AttachRepo: true}, ChannelID: "C1", ThreadTS: "1.0"}
	step, err := w.Selection(context.Background(), p, "foo/bar")
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != NextStepSelector {
		t.Errorf("expected NextStepSelector (description prompt), got %v", step.Kind)
	}
	if p.Phase != "ask_description_prompt" {
		t.Errorf("Phase = %q, want ask_description_prompt", p.Phase)
	}
	st := p.State.(*askState)
	if st.SelectedRepo != "foo/bar" {
		t.Errorf("SelectedRepo = %q", st.SelectedRepo)
	}
}

func TestAskWorkflow_Selection_RepoChoiceWithBranchesShowsBranchSelector(t *testing.T) {
	w, _ := newTestAskWorkflow(t)
	trueVal := true
	w.cfg.ChannelDefaults.BranchSelect = &trueVal
	w.cfg.ChannelDefaults.Branches = []string{"main", "dev"}
	p := &Pending{Phase: "ask_repo_select", State: &askState{Question: "Q", AttachRepo: true}, ChannelID: "C1", ThreadTS: "1.0"}
	step, err := w.Selection(context.Background(), p, "foo/bar")
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != NextStepSelector {
		t.Errorf("expected NextStepSelector (branch selector), got %v", step.Kind)
	}
	if p.Phase != "ask_branch_select" {
		t.Errorf("Phase = %q, want ask_branch_select", p.Phase)
	}
	// 2 branches + 1 cancel button.
	if len(step.Selector.Options) != 3 {
		t.Errorf("expected 3 actions (2 branches + 取消), got %d", len(step.Selector.Options))
	}
	if step.Selector.ActionID != "ask_branch" {
		t.Errorf("ActionID = %q, want ask_branch", step.Selector.ActionID)
	}
	sawCancel := false
	for _, o := range step.Selector.Options {
		if o.Value == "取消" {
			sawCancel = true
		}
	}
	if !sawCancel {
		t.Error("cancel option missing from branch selector")
	}
}

func TestAskWorkflow_Selection_RepoSelectBackReturnsToAttachPrompt(t *testing.T) {
	// back_to_attach on repo picker rewinds to the attach/skip prompt
	// rather than ending the task — user changed their mind about
	// attaching a repo but doesn't want to abandon the whole ask.
	w, _ := newTestAskWorkflow(t)
	p := &Pending{Phase: "ask_repo_select", State: &askState{Question: "Q", AttachRepo: true}, ChannelID: "C1", ThreadTS: "1.0"}
	step, err := w.Selection(context.Background(), p, "back_to_attach")
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != NextStepSelector {
		t.Errorf("expected NextStepSelector (attach prompt), got %v", step.Kind)
	}
	if p.Phase != "ask_repo_prompt" {
		t.Errorf("phase = %q, want ask_repo_prompt (rewound)", p.Phase)
	}
	st := p.State.(*askState)
	if st.AttachRepo {
		t.Error("AttachRepo should reset to false on back_to_attach")
	}
	if st.SelectedRepo != "" {
		t.Errorf("SelectedRepo leaked on back: %q", st.SelectedRepo)
	}
	// Re-emitted prompt must be the attach/skip selector.
	if step.Selector == nil || step.Selector.ActionID != "ask_attach_repo" {
		t.Errorf("back must re-emit attach prompt, got action_id=%q", func() string {
			if step.Selector == nil {
				return "<nil>"
			}
			return step.Selector.ActionID
		}())
	}
}

func TestAskWorkflow_Selection_BranchSelectCancelReturnsCancel(t *testing.T) {
	w, _ := newTestAskWorkflow(t)
	p := &Pending{Phase: "ask_branch_select", State: &askState{Question: "Q", AttachRepo: true, SelectedRepo: "foo/bar"}, ChannelID: "C1", ThreadTS: "1.0"}
	step, err := w.Selection(context.Background(), p, "取消")
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != NextStepCancel {
		t.Errorf("expected NextStepCancel, got %v", step.Kind)
	}
	st := p.State.(*askState)
	if st.SelectedBranch != "" {
		t.Errorf("SelectedBranch leaked on cancel: %q", st.SelectedBranch)
	}
}

func TestAskWorkflow_Selection_AttachWithReposIncludesBack(t *testing.T) {
	// Button-based repo selector must include a back option so the user
	// can unwind to the attach prompt instead of being stuck choosing a
	// repo they no longer want.
	w, _ := newTestAskWorkflow(t)
	w.cfg.ChannelDefaults.Repos = []string{"foo/bar", "baz/qux"}
	p := &Pending{Phase: "ask_repo_prompt", State: &askState{Question: "Q"}, ChannelID: "C1", ThreadTS: "1.0"}
	step, err := w.Selection(context.Background(), p, "attach")
	if err != nil {
		t.Fatal(err)
	}
	// 2 repos + 1 back.
	if len(step.Selector.Options) != 3 {
		t.Errorf("expected 3 options (2 repos + back), got %d", len(step.Selector.Options))
	}
	sawBack := false
	for _, o := range step.Selector.Options {
		if o.Value == "back_to_attach" {
			sawBack = true
		}
	}
	if !sawBack {
		t.Error("back option missing from repo selector")
	}
}

func TestAskWorkflow_Selection_AttachExternalSearchIncludesCancel(t *testing.T) {
	// External-search path (no configured repos) must also carry cancel info
	// so the dispatcher renders the button alongside the search dropdown.
	w, _ := newTestAskWorkflow(t)
	p := &Pending{Phase: "ask_repo_prompt", State: &askState{Question: "Q"}, ChannelID: "C1", ThreadTS: "1.0"}
	step, err := w.Selection(context.Background(), p, "attach")
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != NextStepSelector /* external */ {
		t.Fatalf("expected NextStepSelector /* external */, got %v", step.Kind)
	}
	if step.Selector.CancelActionID == "" || step.Selector.CancelLabel == "" {
		t.Errorf("expected cancel info on external selector, got actionID=%q label=%q",
			step.Selector.CancelActionID, step.Selector.CancelLabel)
	}
}

func TestAskWorkflow_Selection_BranchPickGoesToDescriptionPrompt(t *testing.T) {
	w, _ := newTestAskWorkflow(t)
	p := &Pending{Phase: "ask_branch_select", State: &askState{Question: "Q", AttachRepo: true, SelectedRepo: "foo/bar"}, ChannelID: "C1", ThreadTS: "1.0"}
	step, err := w.Selection(context.Background(), p, "feature/xyz")
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != NextStepSelector {
		t.Errorf("expected NextStepSelector (description prompt), got %v", step.Kind)
	}
	if p.Phase != "ask_description_prompt" {
		t.Errorf("Phase = %q, want ask_description_prompt", p.Phase)
	}
	st := p.State.(*askState)
	if st.SelectedBranch != "feature/xyz" {
		t.Errorf("SelectedBranch = %q, want feature/xyz", st.SelectedBranch)
	}
}

func TestAskWorkflow_Selection_SingleBranchSkipsSelector(t *testing.T) {
	// With only one branch we auto-select and skip the picker — saves a
	// pointless click when repos have a single default branch.
	w, _ := newTestAskWorkflow(t)
	trueVal := true
	w.cfg.ChannelDefaults.BranchSelect = &trueVal
	w.cfg.ChannelDefaults.Branches = []string{"main"}
	p := &Pending{Phase: "ask_repo_select", State: &askState{Question: "Q", AttachRepo: true}, ChannelID: "C1", ThreadTS: "1.0"}
	step, err := w.Selection(context.Background(), p, "foo/bar")
	if err != nil {
		t.Fatal(err)
	}
	if p.Phase != "ask_description_prompt" {
		t.Errorf("Phase = %q, want ask_description_prompt", p.Phase)
	}
	st := p.State.(*askState)
	if st.SelectedBranch != "main" {
		t.Errorf("SelectedBranch = %q, want main (auto-selected)", st.SelectedBranch)
	}
	if step.Kind != NextStepSelector {
		t.Errorf("expected description prompt selector, got %v", step.Kind)
	}
}

func TestAskWorkflow_Selection_DescriptionSkipGoesToSubmit(t *testing.T) {
	w, _ := newTestAskWorkflow(t)
	p := &Pending{Phase: "ask_description_prompt", State: &askState{Question: "Q"}, ChannelID: "C1", ThreadTS: "1.0"}
	step, err := w.Selection(context.Background(), p, "跳過")
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != NextStepSubmit {
		t.Errorf("expected NextStepSubmit, got %v", step.Kind)
	}
}

func TestAskWorkflow_Selection_DescriptionOpensModal(t *testing.T) {
	w, _ := newTestAskWorkflow(t)
	p := &Pending{Phase: "ask_description_prompt", State: &askState{Question: "Q"}, ChannelID: "C1", ThreadTS: "1.0", SelectorTS: "sel-1"}
	step, err := w.Selection(context.Background(), p, "補充說明")
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != NextStepOpenModal {
		t.Errorf("expected NextStepOpenModal, got %v", step.Kind)
	}
	if p.Phase != "ask_description_modal" {
		t.Errorf("Phase = %q, want ask_description_modal", p.Phase)
	}
	if step.ModalMetadata != "sel-1" {
		t.Errorf("ModalMetadata = %q, want the current selectorTS so HandleDescriptionSubmit can find the pending again", step.ModalMetadata)
	}
}

func TestAskWorkflow_Selection_DescriptionModalAppendsToQuestion(t *testing.T) {
	w, _ := newTestAskWorkflow(t)
	p := &Pending{Phase: "ask_description_modal", State: &askState{Question: "原始問題"}, ChannelID: "C1", ThreadTS: "1.0"}
	step, err := w.Selection(context.Background(), p, "還請一併說明 X 如何運作")
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != NextStepSubmit {
		t.Errorf("expected NextStepSubmit, got %v", step.Kind)
	}
	st := p.State.(*askState)
	wantSubstrings := []string{"原始問題", "還請一併說明 X 如何運作"}
	for _, s := range wantSubstrings {
		if !strings.Contains(st.Question, s) {
			t.Errorf("Question missing %q: got %q", s, st.Question)
		}
	}
}

func TestAskWorkflow_Selection_DescriptionModalEmptyLeavesQuestion(t *testing.T) {
	// Modal close (ViewClosed) sends empty text. Question must stay unchanged
	// so the agent doesn't get an empty or double-newlined prompt.
	w, _ := newTestAskWorkflow(t)
	p := &Pending{Phase: "ask_description_modal", State: &askState{Question: "Q"}, ChannelID: "C1", ThreadTS: "1.0"}
	step, err := w.Selection(context.Background(), p, "")
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != NextStepSubmit {
		t.Errorf("expected NextStepSubmit, got %v", step.Kind)
	}
	st := p.State.(*askState)
	if st.Question != "Q" {
		t.Errorf("Question mutated on empty modal: %q", st.Question)
	}
}

// TestAskWorkflow_Selection_AttachWithNoReposUsesExternalSearch covers the
// fallback path that fires when ChannelDefaults.Repos and Channels[ID] are
// both empty. The dispatcher routes NextStepSelector /* external */ to a
// searchable Slack modal rather than a button selector. Regression guard
// for the Task 5.2 plan-deviation (plan's NextStepSelector+empty-actions
// approach was broken — see commit 37bc67b).
func TestAskWorkflow_Selection_AttachWithNoReposUsesExternalSearch(t *testing.T) {
	w, _ := newTestAskWorkflow(t)
	// ChannelDefaults.Repos left empty on purpose — no Channels override either.
	p := &Pending{Phase: "ask_repo_prompt", State: &askState{Question: "Q"}, ChannelID: "C1", ThreadTS: "1.0"}
	step, err := w.Selection(context.Background(), p, "attach")
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != NextStepSelector /* external */ {
		t.Errorf("expected NextStepSelector /* external */, got %v", step.Kind)
	}
	if step.Selector.ActionID != "ask_repo" {
		t.Errorf("SelectorActionID = %q, want ask_repo", step.Selector.ActionID)
	}
	if step.Selector.Placeholder == "" {
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
	if job.PromptContext.ResponseSchema == "" {
		t.Error("PromptContext.ResponseSchema must be populated (ApplyDefaults)")
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

func TestAskWorkflow_BuildJob_WithBranch_PopulatesBranch(t *testing.T) {
	// SelectedBranch must surface on Job.Branch AND PromptContext.Branch so
	// the worker (a) clones the right ref and (b) mentions it in the prompt.
	w, _ := newTestAskWorkflow(t)
	p := &Pending{
		ChannelID: "C1", ThreadTS: "1.0", UserID: "U1",
		RequestID: "req-3",
		State: &askState{
			Question: "Q", AttachRepo: true,
			SelectedRepo: "foo/bar", SelectedBranch: "feature/xyz",
		},
	}
	job, _, err := w.BuildJob(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if job.Branch != "feature/xyz" {
		t.Errorf("Job.Branch = %q, want feature/xyz", job.Branch)
	}
	if job.PromptContext == nil || job.PromptContext.Branch != "feature/xyz" {
		t.Errorf("PromptContext.Branch missing or wrong: %+v", job.PromptContext)
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
