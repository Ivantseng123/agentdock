package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/Ivantseng123/agentdock/app/config"
	ghclient "github.com/Ivantseng123/agentdock/shared/github"
	"github.com/Ivantseng123/agentdock/shared/logging"
	"github.com/Ivantseng123/agentdock/shared/metrics"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

// criticalSentinel is the HTML-comment marker the agent emits in issue body
// when a critical ref repo is unavailable and the issue cannot meaningfully
// be created. Detection is plain `strings.Contains`; format is fixed (no
// repo name parameter — worker reads UnavailableRefs for the actual list).
// Spec §4.7 / grill Q6.
const criticalSentinel = "<!-- AGENTDOCK:CRITICAL_REF_UNAVAILABLE -->"

// relatedReposHeadingRE matches H1-H4 markdown headings whose text starts
// "Related rep…" (singular, plural, or Repositories), case-insensitive.
// Loose enough to cover normal LLM variation, strict enough to refuse
// non-heading mentions (bold inline / Chinese / plain text). Spec §4.7
// / grill Q3.
var relatedReposHeadingRE = regexp.MustCompile(`(?im)^#{1,4}\s+related\s+rep`)

// hasRelatedReposSection reports whether the issue body already contains a
// `## Related repos` (or close variant) H2 heading. Caller uses the negative
// to decide whether worker should prepend a minimal placeholder section.
func hasRelatedReposSection(body string) bool {
	return relatedReposHeadingRE.MatchString(body)
}

// prependRelatedRepos prepends a minimal `## Related repos` section to the
// body, listing primary + each ref by `repo[@branch]` with a fixed role
// placeholder. Agent's own version (when present) is preferred — caller
// should gate via hasRelatedReposSection. Spec §4.7 s3.
func prependRelatedRepos(body string, job *queue.Job) string {
	var sb strings.Builder
	sb.WriteString("## Related repos\n\n")
	sb.WriteString(formatRefLine(job.Repo, job.Branch, "primary（issue 開立目標）"))
	for _, ref := range job.RefRepos {
		sb.WriteString(formatRefLine(ref.Repo, ref.Branch, "reference context"))
	}
	sb.WriteString("\n---\n\n")
	sb.WriteString(body)
	return sb.String()
}

// formatRefLine renders one bullet for the Related repos section. Branch is
// omitted from the rendered line when empty so default-branch refs don't
// produce a trailing `@`.
func formatRefLine(repo, branch, role string) string {
	if branch == "" {
		return fmt.Sprintf("- `%s` — %s\n", repo, role)
	}
	return fmt.Sprintf("- `%s@%s` — %s\n", repo, branch, role)
}

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

	// Multi-repo (ref) state. Mirrors askState's same fields exactly — see
	// ask.go for full doc. AddRefs is the user's yes/no on the decide prompt;
	// RefRepos accumulates as each ref is picked (Branch is filled in the
	// per-ref branch loop). RefBranchIdx steps the per-ref branch picker
	// forward. BranchTargetRepo is the transient "which repo are we asking
	// branches for right now" — set before each branch select phase (primary
	// OR ref) so BranchSelectedRepo can stay a single-method interface that
	// doesn't need to know about phases.
	AddRefs          bool
	RefRepos         []queue.RefRepo
	RefBranchIdx     int
	BranchTargetRepo string
}

// BranchSelectedRepo satisfies workflow.BranchStateReader so app/bot can
// read the repo off a Pending.State without depending on issueState. Reads
// BranchTargetRepo (transient, set before each branch select phase) — for
// primary branch select it equals SelectedRepo; for per-ref branch select
// it's the current ref's repo. This keeps BranchStateReader phase-agnostic.
func (s *issueState) BranchSelectedRepo() string {
	if s == nil {
		return ""
	}
	return s.BranchTargetRepo
}

// RefExclusions returns the repos that should NOT appear as ref candidates:
// the primary plus any refs already picked. Used by HandleRefRepoSuggestion
// to filter type-ahead results when the channel has no static repo list.
// Body delegates to the shared refExclusionsFor helper.
func (s *issueState) RefExclusions() []string {
	if s == nil {
		return nil
	}
	return refExclusionsFor(s.SelectedRepo, s.RefRepos)
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
				return w.maybeAskRefStep(p), nil
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
			Kind: NextStepSelector,
			Selector: &SelectorSpec{
				Prompt:      ":point_right: Search and select a repo:",
				ActionID:    "repo_search",
				Searchable:  true,
				Placeholder: "Type to search repos...",
			},
			Pending: p,
		}, nil

	case 1:
		st.SelectedRepo = repos[0]
		return w.afterRepoSelected(p, channelCfg), nil

	default:
		// Multi-repo: show selector (button for ≤24, static_select above).
		p.Phase = "repo"
		return NextStep{
			Kind: NextStepSelector,
			Selector: &SelectorSpec{
				Prompt:   ":point_right: Which repo should this issue go to?",
				ActionID: "repo_select",
				Options:  reposToOptions(repos),
			},
			Pending: p,
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
		return w.maybeAskRefStep(p), nil

	case "issue_ref_decide":
		if value == "skip" {
			st.AddRefs = false
			return w.descriptionPromptStep(p), nil
		}
		st.AddRefs = true
		return w.refPickStep(p), nil

	case "issue_ref_pick":
		// Cancellation → bail out of refs entirely. Match both the static-button
		// value ("back_to_decide") and the external_select cancel button label
		// ("← 不加 ref"), since the slack adapter sends the label as action value
		// for cancel buttons.
		if value == "back_to_decide" || value == "issue_ref_back" || value == "← 不加 ref" {
			st.AddRefs = false
			return w.descriptionPromptStep(p), nil
		}
		st.RefRepos = append(st.RefRepos, queue.RefRepo{
			Repo:     value,
			CloneURL: cleanCloneURL(value),
		})
		// Inline branch pick — ask THIS ref's branch immediately so the
		// repo+branch decision is one coherent step from the user's view.
		st.RefBranchIdx = len(st.RefRepos) - 1
		return w.nextRefBranchStep(p), nil

	case "issue_ref_continue":
		switch value {
		case "more":
			return w.refPickStep(p), nil
		case "done":
			return w.descriptionPromptStep(p), nil
		default:
			return NextStep{Kind: NextStepError, ErrorText: fmt.Sprintf(":x: unexpected ref_continue value: %q", value)}, nil
		}

	case "issue_ref_branch":
		if value == "取消" {
			return NextStep{Kind: NextStepCancel}, nil
		}
		st.RefRepos[st.RefBranchIdx].Branch = value
		// Branch picked → loop pivot ("再加一個 / 開始建 issue").
		return w.refContinueStep(p), nil

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

	if st.SelectedRepo == "" {
		return nil, "", fmt.Errorf("empty repo reference")
	}

	reqID := p.RequestID
	if reqID == "" {
		reqID = logging.NewRequestID()
	}

	// Append ref-aware output_rules when the user attached refs (spec §4.6
	// Layer 2). Three rules: read-only contract, critical sentinel emit
	// instruction, `## Related repos` H2 spelling lock. SKILL.md (Layer 1)
	// repeats these as soft guidance; output_rules is the hard prompt-end
	// constraint.
	outputRules := w.cfg.Workflows.Issue.Prompt.OutputRules
	if len(st.RefRepos) > 0 {
		outputRules = append(outputRules,
			"不可寫入、修改、刪除 <ref_repos> 列出之任何 path 之下的檔案；refs 為唯讀脈絡。",
			"若 <unavailable_refs> 含關鍵 ref，必須在 issue body 中加入 HTML comment：<!-- AGENTDOCK:CRITICAL_REF_UNAVAILABLE --> （位置不限，存在即 fail-fast）；worker 偵測到此 marker 即不送 issue 到 GitHub。不要 best-effort 拼湊內容。",
			"Issue body 必須包含 `## Related repos` 段，heading spelling 為 `## Related repos`（lowercase repos），列出 <ref_repos> 中每個 repo 與其角色（含 primary）。Role 不確定時寫 `reference context`。",
		)
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
		RefRepos:    st.RefRepos,
		PromptContext: &queue.PromptContext{
			Goal:             w.cfg.Workflows.Issue.Prompt.Goal,
			ResponseSchema:   w.cfg.Workflows.Issue.Prompt.ResponseSchema,
			OutputRules:      outputRules,
			Language:         w.cfg.PromptDefaults.Language,
			ExtraDescription: st.ExtraDesc,
			Branch:           st.SelectedBranch,
			Channel:          p.ChannelName,
			Reporter:         p.Reporter,
			AllowWorkerRules: w.cfg.PromptDefaults.IsWorkerRulesAllowed(),
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
		truncated := logging.Redact(r.RawOutput, w.cfg.Secrets)
		if len(truncated) > 2000 {
			truncated = truncated[:2000] + "…(truncated)"
		}
		w.logger.Warn("issue parse failed", "phase", "失敗", "output", truncated)
		r.Status = "failed"
		r.Error = fmt.Sprintf("parse failed: %v", err)
		w.handleFailure(state, r)
		return nil
	}
	// Redact configured secrets that the agent may have echoed into Message
	// before any branch can surface it (#180).
	msg := logging.Redact(parsed.Message, w.cfg.Secrets)
	switch parsed.Status {
	case "REJECTED":
		w.postLowConfidence(job, msg)
		return nil
	case "ERROR":
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

	body := parsed.Body

	// [s1] Strict guard fail-fast — worker reported ref worktree dirty bits.
	// agent violated read-only contract → result is not trustworthy → skip
	// GitHub push entirely, surface the violation in Slack so user knows why.
	if len(r.RefViolations) > 0 {
		msg := fmt.Sprintf(
			":no_entry: 無法建立 issue：agent 違規寫入 ref repo `%s`，job 結果不可信。請重 trigger。",
			strings.Join(r.RefViolations, ", "))
		w.updateStatus(job, msg)
		metrics.WorkflowCompletionsTotal.WithLabelValues("issue", "rejected_ref_violation").Inc()
		w.logger.Warn("issue rejected by ref-violation guard",
			"phase", "失敗", "job_id", job.ID, "repo", job.Repo,
			"refs", r.RefViolations)
		return nil
	}

	// [s2] Critical-unavailable sentinel — agent self-declared that a critical
	// ref couldn't be reached and the body shouldn't be pushed. Lookup the
	// unavailable repos from PromptContext (worker fact, not agent claim).
	if strings.Contains(body, criticalSentinel) {
		var unavailableRefs []string
		if job.PromptContext != nil {
			unavailableRefs = job.PromptContext.UnavailableRefs
		}
		repoList := strings.Join(unavailableRefs, ", ")
		if repoList == "" {
			// Defensive: sentinel without UnavailableRefs (shouldn't happen —
			// agent only writes the sentinel when prompted by an unavailable
			// critical ref). Fall back to a generic message rather than emit
			// an empty bullet list.
			repoList = "(unspecified)"
		}
		msg := fmt.Sprintf(
			":no_entry: 無法建立 issue：以下 ref repo 不可達，agent 判定關鍵脈絡缺失\n- %s\n\n請確認 worker GH_TOKEN 對這些 repo 有讀權後重 trigger。",
			repoList)
		w.updateStatus(job, msg)
		metrics.WorkflowCompletionsTotal.WithLabelValues("issue", "rejected_critical_ref").Inc()
		w.logger.Warn("issue rejected by critical-unavailable sentinel",
			"phase", "失敗", "job_id", job.ID, "repo", job.Repo,
			"unavailable_refs", unavailableRefs)
		return nil
	}

	// [s4] (existing) Degraded-mode triage strip — runs before s3 prepend so
	// the strip logic can't accidentally truncate the worker-added section.
	degraded := parsed.FilesFound == 0 || parsed.Questions >= 5
	if degraded {
		body = stripTriageSection(body)
	}

	// [s3] Auto-fill `## Related repos` when refs were attached and agent
	// didn't write the section. Worker's prepend uses fixed role placeholders;
	// agent versions (with richer role descriptions) take priority.
	if len(job.RefRepos) > 0 && !hasRelatedReposSection(body) {
		body = prependRelatedRepos(body, job)
	}

	// [s5] (existing) Redact — runs after s3 prepend so worker-added content
	// is also screened for secrets (defense in depth).
	title := logging.Redact(parsed.Title, w.cfg.Secrets)
	body = logging.Redact(body, w.cfg.Secrets)
	labels := make([]string, len(parsed.Labels))
	for i, l := range parsed.Labels {
		labels[i] = logging.Redact(string(l), w.cfg.Secrets)
	}

	branchInfo := ""
	if job.Branch != "" {
		branchInfo = fmt.Sprintf(" (branch: `%s`)", job.Branch)
	}

	owner, repo := splitRepo(job.Repo)
	url, err := w.github.CreateIssue(ctx, owner, repo, title, body, labels)
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
		return w.maybeAskRefStep(p)
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
			return w.maybeAskRefStep(p)
		}
	}

	if len(branches) <= 1 {
		if len(branches) == 1 {
			st.SelectedBranch = branches[0]
		}
		return w.maybeAskRefStep(p)
	}

	// Multi-branch: show selector. The adapter picks button-row vs
	// static_select based on len(branches) — repos with >24 branches used
	// to hit Slack's actions-block cap and surface as ":x: 無法顯示選單".
	st.BranchTargetRepo = st.SelectedRepo
	p.Phase = "branch"
	backAction, backLabel := "", ""
	if st.RepoWasPicked {
		backAction = "back_to_repo"
		backLabel = "← 重新選 repo"
	}
	return NextStep{
		Kind: NextStepSelector,
		Selector: &SelectorSpec{
			Prompt:           fmt.Sprintf(":point_right: Which branch of `%s`?", st.SelectedRepo),
			ActionID:         "branch_select",
			Options:          labelsToOptions(branches),
			Placeholder:      "選擇 branch...",
			BackActionID:     backAction,
			BackLabel:        backLabel,
			MergeWithLastAck: true, // collapse "✅ repo" + "✅ branch" → "✅ repo, branch"
		},
		Pending: p,
	}
}

// descriptionPromptStep builds the "need extra description?" selector NextStep.
func (w *IssueWorkflow) descriptionPromptStep(p *Pending) NextStep {
	st := p.State.(*issueState)
	p.Phase = "description"

	backAction, backLabel := "", ""
	if st.RepoWasPicked {
		backAction = "back_to_repo"
		backLabel = "← 重新選 repo"
	}

	return NextStep{
		Kind: NextStepSelector,
		Selector: &SelectorSpec{
			Prompt:   ":memo: 需要補充說明嗎？（補充後可讓分析更精準）",
			ActionID: "description_action",
			Options: []SelectorOption{
				{Label: "補充說明", Value: "補充說明"},
				{Label: "跳過", Value: "跳過"},
			},
			BackActionID: backAction,
			BackLabel:    backLabel,
		},
		Pending: p,
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
			Kind: NextStepSelector,
			Selector: &SelectorSpec{
				Prompt:      ":point_right: Search and select a repo:",
				ActionID:    "repo_search",
				Searchable:  true,
				Placeholder: "Type to search repos...",
			},
			Pending: p,
		}
	}

	p.Phase = "repo"
	return NextStep{
		Kind: NextStepSelector,
		Selector: &SelectorSpec{
			Prompt:   ":point_right: Which repo should this issue go to?",
			ActionID: "repo_select",
			Options:  reposToOptions(repos),
		},
		Pending: p,
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

// reposToOptions converts a slice of repo strings to SelectorOptions for the
// repo picker (label and value both equal to the repo path).
func reposToOptions(repos []string) []SelectorOption {
	opts := make([]SelectorOption, len(repos))
	for i, r := range repos {
		opts[i] = SelectorOption{Label: r, Value: r}
	}
	return opts
}

// labelsToOptions converts a slice of strings to SelectorOptions, using each
// string as both the label and the value.
func labelsToOptions(labels []string) []SelectorOption {
	opts := make([]SelectorOption, len(labels))
	for i, l := range labels {
		opts[i] = SelectorOption{Label: l, Value: l}
	}
	return opts
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

// ── ref repo flow ────────────────────────────────────────────────────────────
//
// Mirrors AskWorkflow's ref helpers exactly — same shapes, same skip rules,
// only the action_id prefix and prompt strings differ ("建 issue" vs "問問題").
// See spec docs/superpowers/specs/2026-04-29-issue-multi-repo-design.md §4.4.

// refCandidates returns the channel-allowed repo list minus primary and
// already-picked refs. Returns useExternalSearch=true when the channel uses
// type-ahead repo search instead of a static list — caller dispatches to the
// search-style selector with HandleRefRepoSuggestion as the suggestion handler.
func (w *IssueWorkflow) refCandidates(p *Pending) (list []string, useExternalSearch bool) {
	st, _ := p.State.(*issueState)
	cc := w.cfg.ChannelDefaults
	if c, ok := w.cfg.Channels[p.ChannelID]; ok {
		cc = c
	}
	repos := cc.GetRepos()
	if len(repos) == 0 {
		// External search: filtering happens in HandleRefRepoSuggestion.
		return nil, true
	}
	picked := make(map[string]bool, len(st.RefRepos))
	for _, r := range st.RefRepos {
		picked[r.Repo] = true
	}
	for _, r := range repos {
		if r == st.SelectedRepo || picked[r] {
			continue
		}
		list = append(list, r)
	}
	return list, false
}

// maybeAskRefStep is the single entry into the ref flow. Skips entirely
// when no candidate repos exist (channel has only primary) — keeps the
// thread free of the "加入參考 repo？" message in that case (spec AC-12 mirror).
func (w *IssueWorkflow) maybeAskRefStep(p *Pending) NextStep {
	list, useExternalSearch := w.refCandidates(p)
	if !useExternalSearch && len(list) == 0 {
		return w.descriptionPromptStep(p)
	}
	p.Phase = "issue_ref_decide"
	return NextStep{
		Kind: NextStepSelector,
		Selector: &SelectorSpec{
			Prompt:   ":books: 加入參考 repo 嗎？(唯讀脈絡)",
			ActionID: "issue_ref_decide",
			Options: []SelectorOption{
				{Label: "加入", Value: "add"},
				{Label: "不用", Value: "skip"},
			},
		},
		Pending: p,
	}
}

// refPickStep posts the single-select picker for "next ref repo". Re-uses
// the existing selector infrastructure (button row / static_select /
// external_select auto-dispatch) — when the channel has no static list,
// falls back to type-ahead search via the issue_ref action_id which
// app/app.go routes to HandleRefRepoSuggestion (filters primary + picked).
func (w *IssueWorkflow) refPickStep(p *Pending) NextStep {
	st := p.State.(*issueState)
	list, useExternalSearch := w.refCandidates(p)
	p.Phase = "issue_ref_pick"
	prompt := ":point_right: 選參考 repo:"
	if len(st.RefRepos) > 0 {
		prompt = fmt.Sprintf(":point_right: 選下一個參考 repo（已加 %d 個）:", len(st.RefRepos))
	}
	if useExternalSearch {
		return NextStep{
			Kind: NextStepSelector,
			Selector: &SelectorSpec{
				Prompt:         prompt,
				ActionID:       "issue_ref",
				Searchable:     true,
				Placeholder:    "Type to search repos...",
				CancelActionID: "issue_ref_back",
				CancelLabel:    "← 不加 ref",
			},
			Pending: p,
		}
	}
	options := make([]SelectorOption, 0, len(list)+1)
	for _, r := range list {
		options = append(options, SelectorOption{Label: r, Value: r})
	}
	options = append(options, SelectorOption{Label: "← 不加 ref", Value: "back_to_decide"})
	return NextStep{
		Kind: NextStepSelector,
		Selector: &SelectorSpec{
			Prompt:   prompt,
			ActionID: "issue_ref",
			Options:  options,
		},
		Pending: p,
	}
}

// refContinueStep is the loop pivot after each ref pick: "再加一個 / 開始建 issue".
// When the candidate pool is exhausted (static list and all picked), the
// "再加一個" option drops so the user can only proceed to the description.
func (w *IssueWorkflow) refContinueStep(p *Pending) NextStep {
	st := p.State.(*issueState)
	p.Phase = "issue_ref_continue"
	list, useExternalSearch := w.refCandidates(p)
	options := []SelectorOption{
		{Label: "開始建 issue", Value: "done"},
	}
	// Allow another ref unless the static-list pool is fully consumed.
	// External search is unbounded so always offer "再加一個" there.
	if useExternalSearch || len(list) > 0 {
		options = append([]SelectorOption{{Label: "再加一個 ref", Value: "more"}}, options...)
	}
	return NextStep{
		Kind: NextStepSelector,
		Selector: &SelectorSpec{
			Prompt:   fmt.Sprintf(":heavy_plus_sign: 已加 %d 個 ref。", len(st.RefRepos)),
			ActionID: "issue_ref_continue",
			Options:  options,
		},
		Pending: p,
	}
}

// nextRefBranchStep handles the branch decision for the just-picked ref
// (st.RefRepos[RefBranchIdx]). Same skip rules as primary:
//   - branch_select disabled → no picker, leave Branch empty
//   - ≤ 1 branch resolved   → auto-fill (or leave empty), no picker
//   - otherwise             → show picker
//
// In all "no picker" cases the next step is refContinueStep (loop pivot),
// not descriptionPromptStep — the user is always given a chance to add
// another ref or end the loop, even when individual branch choices are
// skipped.
func (w *IssueWorkflow) nextRefBranchStep(p *Pending) NextStep {
	st := p.State.(*issueState)
	target := st.RefRepos[st.RefBranchIdx].Repo

	cc := w.cfg.ChannelDefaults
	if c, ok := w.cfg.Channels[p.ChannelID]; ok {
		cc = c
	}
	if !cc.IsBranchSelectEnabled() {
		return w.refContinueStep(p)
	}

	var branches []string
	if len(cc.Branches) > 0 {
		branches = cc.Branches
	} else if w.repoCache != nil {
		ghToken := ""
		if w.cfg.Secrets != nil {
			ghToken = w.cfg.Secrets["GH_TOKEN"]
		}
		if rp, err := w.repoCache.EnsureRepo(target, ghToken); err == nil {
			if lb, lerr := w.repoCache.ListBranches(rp); lerr == nil {
				branches = lb
			}
		}
	}
	if len(branches) <= 1 {
		if len(branches) == 1 {
			st.RefRepos[st.RefBranchIdx].Branch = branches[0]
		}
		return w.refContinueStep(p)
	}

	st.BranchTargetRepo = target
	p.Phase = "issue_ref_branch"
	options := make([]SelectorOption, 0, len(branches)+1)
	for _, b := range branches {
		options = append(options, SelectorOption{Label: b, Value: b})
	}
	options = append(options, SelectorOption{Label: "取消", Value: "取消"})
	return NextStep{
		Kind: NextStepSelector,
		Selector: &SelectorSpec{
			Prompt:           fmt.Sprintf(":point_right: Which branch of ref `%s`?", target),
			ActionID:         "issue_ref_branch",
			Options:          options,
			MergeWithLastAck: true, // collapse ref's "✅ repo" + "✅ branch" → "✅ repo, branch"
		},
		Pending: p,
	}
}
