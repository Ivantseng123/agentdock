package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Ivantseng123/agentdock/app/config"
	ghclient "github.com/Ivantseng123/agentdock/shared/github"
	"github.com/Ivantseng123/agentdock/shared/logging"
	"github.com/Ivantseng123/agentdock/shared/metrics"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

// IssueWorkflow handles the legacy `@bot <repo>` and `@bot issue <repo>` flow.
// Behaviour is preserved end-to-end from the pre-refactor `app/bot/workflow.go`
// implementation — users see no change.
type IssueWorkflow struct {
	cfg           *config.Config
	slack         SlackPort
	github        IssueCreator
	repoCache     *ghclient.RepoCache
	repoDiscovery *ghclient.RepoDiscovery
	logger        *slog.Logger
}

// issueState is the workflow-specific Pending.State for IssueWorkflow.
type issueState struct {
	SelectedRepo   string
	SelectedBranch string
	ExtraDesc      string
	RepoWasPicked  bool
}

// NewIssueWorkflow constructs a workflow instance. All dependencies are
// required. Panics on nil pointers to fail fast at startup.
func NewIssueWorkflow(
	cfg *config.Config,
	slack SlackPort,
	github IssueCreator,
	repoCache *ghclient.RepoCache,
	repoDiscovery *ghclient.RepoDiscovery,
	logger *slog.Logger,
) *IssueWorkflow {
	if cfg == nil || slack == nil || logger == nil {
		panic("workflow: NewIssueWorkflow missing required dep")
	}
	return &IssueWorkflow{
		cfg:           cfg,
		slack:         slack,
		github:        github,
		repoCache:     repoCache,
		repoDiscovery: repoDiscovery,
		logger:        logger,
	}
}

// Type returns the TaskType discriminator.
func (w *IssueWorkflow) Type() string { return "issue" }

// Trigger is the entry point from the dispatcher for `@bot issue ...` and
// the legacy bare-repo `@bot <repo>` paths. It parses args, checks channel
// config, short-circuits single-repo, and posts repo selector for multi.
func (w *IssueWorkflow) Trigger(ctx context.Context, ev TriggerEvent, args string) (NextStep, error) {
	// Resolve channel config — caller (Task 2.6 shim) has already verified the
	// channel is bound; Trigger just reads the config.
	channelCfg := w.cfg.ChannelDefaults
	if cc, ok := w.cfg.Channels[ev.ChannelID]; ok {
		channelCfg = cc
	}

	// Populate common fields on the pending envelope.
	reqID := logging.NewRequestID()
	reporter := w.slack.ResolveUser(ev.UserID)
	channelName := w.slack.GetChannelName(ev.ChannelID)

	p := &Pending{
		ChannelID:   ev.ChannelID,
		ThreadTS:    ev.ThreadTS,
		TriggerTS:   ev.TriggerTS,
		UserID:      ev.UserID,
		Reporter:    reporter,
		ChannelName: channelName,
		RequestID:   reqID,
		TaskType:    "issue",
		State:       &issueState{},
	}
	st := p.State.(*issueState)

	// Parse repo@branch from args when present.
	if args != "" {
		repo, branch := parseRepoArg(args)
		if repo != "" {
			st.SelectedRepo = repo
			if branch != "" {
				st.SelectedBranch = branch
				p.Phase = "description"
				return w.descriptionPromptStep(p), nil
			}
			return w.afterRepoSelected(p, channelCfg), nil
		}
		// args didn't look like a repo — fall through to the no-args path
		// (user typed something odd; treat as bare mention).
	}

	repos := channelCfg.GetRepos()

	switch len(repos) {
	case 0:
		// External-search: no repos configured for this channel.
		p.Phase = "repo_search"
		return NextStep{
			Kind:                NextStepPostExternalSelector,
			SelectorPrompt:      ":point_right: Search and select a repo:",
			SelectorActionID:    "repo_search",
			SelectorPlaceholder: "Type to search repos...",
			Pending:             p,
		}, nil

	case 1:
		st.SelectedRepo = repos[0]
		return w.afterRepoSelected(p, channelCfg), nil

	default:
		// Multi-repo: show button selector.
		p.Phase = "repo"
		actions := reposToActions(repos)
		return NextStep{
			Kind:            NextStepPostSelector,
			SelectorPrompt:  ":point_right: Which repo should this issue go to?",
			SelectorActions: actions,
			Pending:         p,
		}, nil
	}
}

// Selection handles follow-up button clicks / modal submits. The dispatcher
// looks up the Pending by SelectorTS and calls this with the user's value.
func (w *IssueWorkflow) Selection(ctx context.Context, p *Pending, value string) (NextStep, error) {
	st, ok := p.State.(*issueState)
	if !ok {
		return NextStep{Kind: NextStepError, ErrorText: ":x: IssueWorkflow: unexpected state type"}, nil
	}

	// Back-to-repo always wins regardless of phase.
	if value == "back_to_repo" {
		return w.handleBackToRepo(p, st), nil
	}

	channelCfg := w.cfg.ChannelDefaults
	if cc, ok := w.cfg.Channels[p.ChannelID]; ok {
		channelCfg = cc
	}

	switch p.Phase {
	case "repo", "repo_search":
		st.SelectedRepo = value
		st.RepoWasPicked = true
		return w.afterRepoSelected(p, channelCfg), nil

	case "branch":
		st.SelectedBranch = value
		p.Phase = "description"
		return w.descriptionPromptStep(p), nil

	case "description":
		switch value {
		case "跳過":
			return NextStep{Kind: NextStepSubmit, Pending: p}, nil
		case "補充說明":
			p.Phase = "description_modal"
			return NextStep{
				Kind:           NextStepOpenModal,
				ModalTitle:     "補充說明",
				ModalLabel:     "請輸入補充說明",
				ModalInputName: "description",
				ModalMetadata:  p.SelectorTS,
				// ModalTriggerID left empty — dispatcher fills from live event.
				Pending: p,
			}, nil
		default:
			return NextStep{Kind: NextStepError, ErrorText: fmt.Sprintf(":x: unexpected description value: %q", value)}, nil
		}

	case "description_modal":
		// value is the text the user submitted in the modal.
		st.ExtraDesc = value
		return NextStep{Kind: NextStepSubmit, Pending: p}, nil

	default:
		return NextStep{Kind: NextStepError, ErrorText: fmt.Sprintf(":x: IssueWorkflow: unknown phase %q", p.Phase)}, nil
	}
}

// BuildJob assembles the queue.Job from the completed pending state.
// Status text is the message posted while the worker runs.
func (w *IssueWorkflow) BuildJob(ctx context.Context, p *Pending) (*queue.Job, string, error) {
	st, ok := p.State.(*issueState)
	if !ok {
		return nil, "", fmt.Errorf("IssueWorkflow.BuildJob: unexpected state type")
	}

	// Guard against empty repo — the state-machine race (selector click on a
	// late-arriving orphan branch selector after back-to-repo) can leave
	// SelectedRepo blank while SelectedBranch is set. Refusing here means
	// app.submitJob's BuildJob-err branch posts ":x: internal state error" and
	// clears dedup, instead of shipping `CloneURL=https://github.com/.git` to
	// the worker. Matching language used in worker guard: "empty repo reference".
	if strings.TrimSpace(st.SelectedRepo) == "" {
		return nil, "", fmt.Errorf("empty repo reference: 內部狀態錯誤，請重試 /triage")
	}

	reqID := p.RequestID
	if reqID == "" {
		reqID = logging.NewRequestID()
	}

	job := &queue.Job{
		ID:          reqID,
		RequestID:   reqID,
		TaskType:    "issue",
		ChannelID:   p.ChannelID,
		ThreadTS:    p.ThreadTS,
		UserID:      p.UserID,
		Repo:        st.SelectedRepo,
		Branch:      st.SelectedBranch,
		CloneURL:    cleanCloneURL(st.SelectedRepo),
		Priority:    w.channelPriority(p.ChannelID),
		SubmittedAt: time.Now(),
		PromptContext: &queue.PromptContext{
			Goal:             w.cfg.Prompt.Issue.Goal,
			ResponseSchema:   w.cfg.Prompt.Issue.ResponseSchema,
			OutputRules:      w.cfg.Prompt.Issue.OutputRules,
			Language:         w.cfg.Prompt.Language,
			ExtraDescription: st.ExtraDesc,
			Branch:           st.SelectedBranch,
			Channel:          p.ChannelName,
			Reporter:         p.Reporter,
			AllowWorkerRules: w.cfg.Prompt.IsWorkerRulesAllowed(),
			// ThreadMessages, Attachments, Skills, EncryptedSecrets filled by Task 2.7 helper.
		},
	}

	return job, ":mag: 分析 codebase 中...", nil
}

// HandleResult routes the JobResult through REJECTED / ERROR / CREATED / parse-fail / failure branches.
// Slack posting and GitHub issue creation happen here. Store status transitions and dedup-clearing
// remain the ResultListener's responsibility (Phase 3 delegates through).
func (w *IssueWorkflow) HandleResult(ctx context.Context, state *queue.JobState, r *queue.JobResult) error {
	if state == nil || state.Job == nil {
		return fmt.Errorf("HandleResult: state or state.Job is nil")
	}
	job := state.Job
	if r.Status == "failed" {
		w.handleFailure(state, r)
		return nil
	}
	if r.RawOutput == "" {
		return fmt.Errorf("empty RawOutput for completed job")
	}
	parsed, err := ParseAgentOutput(r.RawOutput)
	if err != nil {
		truncated := r.RawOutput
		if len(truncated) > 2000 {
			truncated = truncated[:2000] + "…(truncated)"
		}
		w.logger.Warn("issue parse failed", "phase", "失敗", "output", truncated)
		r.Status = "failed"
		r.Error = fmt.Sprintf("parse failed: %v", err)
		w.handleFailure(state, r)
		return nil
	}
	switch parsed.Status {
	case "REJECTED":
		w.postLowConfidence(job, parsed.Message)
		return nil
	case "ERROR":
		msg := parsed.Message
		if msg == "" {
			msg = "agent reported ERROR without message"
		}
		r.Status = "failed"
		r.Error = "agent error: " + msg
		w.handleFailure(state, r)
		return nil
	case "CREATED":
		return w.createAndPostIssue(ctx, state, r, parsed)
	default:
		return fmt.Errorf("unknown parsed status %q", parsed.Status)
	}
}

// handleFailure posts either a retry button (first attempt) or an exhausted-retry
// message (subsequent attempts). Store status transitions are omitted here;
// ResultListener handles those in Phase 3.
func (w *IssueWorkflow) handleFailure(state *queue.JobState, result *queue.JobResult) {
	job := state.Job
	workerInfo := ""
	if label := workerLabel(state); label != "" {
		workerInfo = fmt.Sprintf(" | worker: %s", label)
	}

	// Extract short error reason for Slack (before first colon detail, max 80 chars).
	errMsg := result.Error
	if idx := strings.Index(errMsg, ":"); idx > 0 {
		errMsg = errMsg[:idx]
	}
	if len(errMsg) > 80 {
		errMsg = errMsg[:80] + "…"
	}

	if job.RetryCount < 1 {
		// Show retry button.
		text := fmt.Sprintf(":x: 分析失敗: %s\nrepo: `%s` | job: `%s`%s", errMsg, job.Repo, job.ID, workerInfo)
		_, _ = w.slack.PostMessageWithButton(job.ChannelID, text, job.ThreadTS,
			"retry_job", "🔄 重試", job.ID)
		// Do NOT clear dedup — user should use retry button.
	} else {
		// Retry exhausted, no button.
		metrics.WorkflowRetryTotal.WithLabelValues("issue", "exhausted").Inc()
		text := fmt.Sprintf(":x: 分析失敗（重試後仍失敗）: %s\nrepo: `%s` | job: `%s`%s", errMsg, job.Repo, job.ID, workerInfo)
		w.updateStatus(job, text)
	}
}

// workerLabel derives the worker identity label for diagnostics, preferring
// the live AgentStatus report (relayed by StatusListener) but falling back to
// JobState.WorkerID for jobs that finished before any status reports landed.
// Returns empty string when no identity is available.
func workerLabel(state *queue.JobState) string {
	if state == nil {
		return ""
	}
	workerID := ""
	workerNickname := ""
	if state.AgentStatus != nil {
		workerID = state.AgentStatus.WorkerID
		workerNickname = state.AgentStatus.WorkerNickname
	}
	if workerID == "" {
		workerID = state.WorkerID
	}
	label := workerNickname
	if label == "" {
		label = workerID
	} else if workerID != "" {
		label = fmt.Sprintf("%s (%s)", workerNickname, workerID)
	}
	return label
}

// postLowConfidence posts the REJECTED / low-confidence message to the thread.
func (w *IssueWorkflow) postLowConfidence(job *queue.Job, message string) {
	w.logger.Info("issue rejected", "reason", "low_confidence", "job_id", job.ID, "repo", job.Repo)
	metrics.WorkflowCompletionsTotal.WithLabelValues("issue", "rejected").Inc()
	text := ":warning: 判斷不屬於此 repo，已跳過"
	if message != "" {
		text = text + "\n> " + message
	}
	w.updateStatus(job, text)
}

// createAndPostIssue calls w.github.CreateIssue and posts the issue URL + diagnostics to Slack.
// The degraded flag is computed inline: files_found == 0 || open_questions >= 5 strips triage sections.
// Store status transitions are omitted here; ResultListener handles them in Phase 3.
func (w *IssueWorkflow) createAndPostIssue(ctx context.Context, state *queue.JobState, r *queue.JobResult, parsed TriageResult) error {
	job := state.Job
	if w.github == nil {
		w.logger.Info("issue rejected", "reason", "no_github", "job_id", job.ID, "repo", job.Repo)
		metrics.WorkflowCompletionsTotal.WithLabelValues("issue", "rejected").Inc()
		_ = w.slack.PostMessage(job.ChannelID,
			":warning: GitHub client not configured", job.ThreadTS)
		return nil
	}

	degraded := parsed.FilesFound == 0 || parsed.Questions >= 5
	body := parsed.Body
	if degraded {
		body = stripTriageSection(body)
	}

	branchInfo := ""
	if job.Branch != "" {
		branchInfo = fmt.Sprintf(" (branch: `%s`)", job.Branch)
	}

	owner, repo := splitRepo(job.Repo)
	url, err := w.github.CreateIssue(ctx, owner, repo, parsed.Title, body, []string(parsed.Labels))
	if err != nil {
		w.updateStatus(job, fmt.Sprintf(":warning: Triage 完成但建立 issue 失敗: %v", err))
		return fmt.Errorf("github create issue: %w", err)
	}

	confidence := parsed.Confidence
	if confidence == "" {
		confidence = "unknown"
	}
	// confidence and degraded go into the log to avoid label cardinality blow-ups.
	w.logger.Info("issue created", "job_id", job.ID, "repo", job.Repo, "confidence", confidence, "degraded", degraded)
	metrics.WorkflowCompletionsTotal.WithLabelValues("issue", "success").Inc()

	// Preserve worker diagnostics on the final message so the thread captures
	// what the job actually consumed.
	line := fmt.Sprintf(":white_check_mark: Issue created%s: %s", branchInfo, url)
	if diag := w.formatDiagnostics(state, r); diag != "" {
		line = line + "\n" + diag
	}
	w.updateStatus(job, line)
	return nil
}

// formatDiagnostics renders the elapsed time, cost, and worker-label diagnostics line.
// Order matches result_listener's failure-path format: stats first, identity last.
func (w *IssueWorkflow) formatDiagnostics(state *queue.JobState, result *queue.JobResult) string {
	var parts []string
	if elapsed := result.FinishedAt.Sub(result.StartedAt); elapsed > 0 {
		parts = append(parts, humanDuration(elapsed))
	}
	if result.CostUSD > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f", result.CostUSD))
	}
	if label := workerLabel(state); label != "" {
		parts = append(parts, "worker: "+label)
	}
	return strings.Join(parts, " · ")
}

// updateStatus updates the status message if StatusMsgTS is set, otherwise posts a new message.
// A defensive re-write 2 seconds later narrows the race with StatusListener's in-flight update.
func (w *IssueWorkflow) updateStatus(job *queue.Job, text string) {
	if job.StatusMsgTS != "" {
		_ = w.slack.UpdateMessage(job.ChannelID, job.StatusMsgTS, text)
		ch, ts, finalText := job.ChannelID, job.StatusMsgTS, text
		time.AfterFunc(2*time.Second, func() {
			_ = w.slack.UpdateMessage(ch, ts, finalText)
		})
	} else {
		_ = w.slack.PostMessage(job.ChannelID, text, job.ThreadTS)
	}
}

// splitRepo splits "owner/repo" into its components. If the string has no slash,
// it returns the whole string as owner and an empty repo.
func splitRepo(repo string) (string, string) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		return repo, ""
	}
	return parts[0], parts[1]
}

// stripTriageSection strips advanced triage sections from the issue body when
// the analysis is degraded (no files found or too many open questions).
func stripTriageSection(body string) string {
	for _, marker := range []string{"## Root Cause Analysis", "## TDD Fix Plan"} {
		if idx := strings.Index(body, marker); idx > 0 {
			body = strings.TrimSpace(body[:idx])
		}
	}
	return body
}

// humanDuration formats a duration as a compact human-readable string.
func humanDuration(d time.Duration) string {
	s := int(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	m := s / 60
	s = s % 60
	if s == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dm %ds", m, s)
}

// ── helpers ──────────────────────────────────────────────────────────────────

// afterRepoSelected decides whether to show the branch selector or jump
// straight to the description prompt, mirroring app/bot/workflow.go's
// afterRepoSelected.
func (w *IssueWorkflow) afterRepoSelected(p *Pending, channelCfg config.ChannelConfig) NextStep {
	st := p.State.(*issueState)

	if !channelCfg.IsBranchSelectEnabled() {
		p.Phase = "description"
		return w.descriptionPromptStep(p)
	}

	// Resolve branch list: config wins; fall back to live repo enumeration.
	var branches []string
	if len(channelCfg.Branches) > 0 {
		branches = channelCfg.Branches
	} else if w.repoCache != nil {
		ghToken := ""
		if w.cfg.Secrets != nil {
			ghToken = w.cfg.Secrets["GH_TOKEN"]
		}
		repoPath, err := w.repoCache.EnsureRepo(st.SelectedRepo, ghToken)
		if err != nil {
			// Surface the error so operators know repo access failed.
			return NextStep{
				Kind:      NextStepError,
				ErrorText: fmt.Sprintf(":x: Failed to access repo %s: %v", st.SelectedRepo, err),
				Pending:   p,
			}
		}
		var listErr error
		branches, listErr = w.repoCache.ListBranches(repoPath)
		if listErr != nil {
			// Graceful fallback: branch list unavailable → skip branch step.
			p.Phase = "description"
			return w.descriptionPromptStep(p)
		}
	}

	if len(branches) <= 1 {
		if len(branches) == 1 {
			st.SelectedBranch = branches[0]
		}
		p.Phase = "description"
		return w.descriptionPromptStep(p)
	}

	// Multi-branch: show selector.
	p.Phase = "branch"
	backAction := ""
	if st.RepoWasPicked {
		backAction = "back_to_repo"
	}
	actions := labelsToActions("branch_select", branches)
	return NextStep{
		Kind:            NextStepPostSelector,
		SelectorPrompt:  fmt.Sprintf(":point_right: Which branch of `%s`?", st.SelectedRepo),
		SelectorActions: actions,
		SelectorBack:    backAction,
		Pending:         p,
	}
}

// descriptionPromptStep builds the "need extra description?" selector NextStep.
func (w *IssueWorkflow) descriptionPromptStep(p *Pending) NextStep {
	st := p.State.(*issueState)
	p.Phase = "description"

	backAction := ""
	if st.RepoWasPicked {
		backAction = "back_to_repo"
	}

	return NextStep{
		Kind:           NextStepPostSelector,
		SelectorPrompt: ":memo: 需要補充說明嗎？（補充後可讓分析更精準）",
		SelectorActions: []SelectorAction{
			{ActionID: "description_action", Label: "補充說明", Value: "補充說明"},
			{ActionID: "description_action", Label: "跳過", Value: "跳過"},
		},
		SelectorBack: backAction,
		Pending:      p,
	}
}

// handleBackToRepo resets repo/branch/extra-desc and re-dispatches the repo
// picker, mirroring app/bot/workflow.go's HandleBackToRepo.
func (w *IssueWorkflow) handleBackToRepo(p *Pending, st *issueState) NextStep {
	st.SelectedRepo = ""
	st.SelectedBranch = ""
	st.ExtraDesc = ""

	channelCfg := w.cfg.ChannelDefaults
	if cc, ok := w.cfg.Channels[p.ChannelID]; ok {
		channelCfg = cc
	}

	repos := channelCfg.GetRepos()

	// Rare: channel config reloaded with exactly one repo — auto-select.
	if len(repos) == 1 {
		st.SelectedRepo = repos[0]
		return w.afterRepoSelected(p, channelCfg)
	}

	// Multi or external-search.
	if len(repos) == 0 {
		p.Phase = "repo_search"
		return NextStep{
			Kind:                NextStepPostExternalSelector,
			SelectorPrompt:      ":point_right: Search and select a repo:",
			SelectorActionID:    "repo_search",
			SelectorPlaceholder: "Type to search repos...",
			Pending:             p,
		}
	}

	p.Phase = "repo"
	actions := reposToActions(repos)
	return NextStep{
		Kind:            NextStepPostSelector,
		SelectorPrompt:  ":point_right: Which repo should this issue go to?",
		SelectorActions: actions,
		Pending:         p,
	}
}

// channelPriority mirrors app/bot/workflow.go's channelPriority helper.
func (w *IssueWorkflow) channelPriority(channelID string) int {
	if w.cfg.ChannelPriority == nil {
		return 50
	}
	if pri, ok := w.cfg.ChannelPriority[channelID]; ok {
		return pri
	}
	if pri, ok := w.cfg.ChannelPriority["default"]; ok {
		return pri
	}
	return 50
}

// reposToActions converts a slice of repo strings to SelectorActions for a
// repo picker. Each action uses "repo_select" as the ActionID (matches the
// old PostSelector "repo_select" prefix used in app/bot/workflow.go).
func reposToActions(repos []string) []SelectorAction {
	actions := make([]SelectorAction, len(repos))
	for i, r := range repos {
		actions[i] = SelectorAction{
			ActionID: "repo_select",
			Label:    r,
			Value:    r,
		}
	}
	return actions
}

// labelsToActions converts string labels to SelectorActions using the given
// actionID prefix for all entries.
func labelsToActions(actionID string, labels []string) []SelectorAction {
	actions := make([]SelectorAction, len(labels))
	for i, l := range labels {
		actions[i] = SelectorAction{
			ActionID: actionID,
			Label:    l,
			Value:    l,
		}
	}
	return actions
}

// cleanCloneURL normalises a repo reference to a full HTTPS clone URL. Raw
// "owner/repo" strings become https://github.com/owner/repo.git; full URLs
// (http, git@, file://) are passed through unchanged.
func cleanCloneURL(repoRef string) string {
	if strings.HasPrefix(repoRef, "http") || strings.HasPrefix(repoRef, "git@") || strings.HasPrefix(repoRef, "file://") {
		return repoRef
	}
	return fmt.Sprintf("https://github.com/%s.git", repoRef)
}

// parseRepoArg splits "owner/repo" or "owner/repo@branch" into its components.
// Returns empty strings if args is empty or doesn't contain a slash (not a
// repo reference).
func parseRepoArg(args string) (repo, branch string) {
	if args == "" {
		return "", ""
	}
	if !strings.Contains(args, "/") {
		return "", ""
	}
	parts := strings.SplitN(args, "@", 2)
	repo = strings.TrimSpace(parts[0])
	if len(parts) == 2 {
		branch = strings.TrimSpace(parts[1])
	}
	return repo, branch
}
