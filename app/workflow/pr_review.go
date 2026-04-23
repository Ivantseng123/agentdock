package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/Ivantseng123/agentdock/app/config"
	ghclient "github.com/Ivantseng123/agentdock/shared/github"
	"github.com/Ivantseng123/agentdock/shared/logging"
	"github.com/Ivantseng123/agentdock/shared/metrics"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

// PRReviewWorkflow handles @bot review <PR URL>. Feature-flag gated.
// Three trigger paths:
//   - A-path: URL supplied inline — validate + submit.
//   - D-path scan: no URL, scan thread → post confirm selector.
//   - D-path modal: scan miss → open modal asking for URL.
type PRReviewWorkflow struct {
	cfg       *config.Config
	slack     SlackPort
	github    GitHubPR
	repoCache *ghclient.RepoCache
	logger    *slog.Logger
}

// prReviewState is the workflow-specific Pending.State for PRReviewWorkflow.
// URL + Owner/Repo/Number come from the parsed URL; HeadRepo/HeadRef/BaseRef
// come from the GitHub API response — HeadRepo can differ from Owner/Repo when
// the PR is from a fork.
type prReviewState struct {
	URL      string
	Owner    string
	Repo     string
	Number   int
	HeadRepo string // head.repo.full_name; may differ from Owner/Repo for forks
	HeadRef  string
	HeadSHA  string // head.sha — immutable across branch deletion; used as Job.Branch git ref
	BaseRef  string
}

// NewPRReviewWorkflow constructs a workflow instance. cfg/slack/logger are
// required; github and repoCache may be nil (tests / degraded env).
func NewPRReviewWorkflow(cfg *config.Config, slack SlackPort, gh GitHubPR, repoCache *ghclient.RepoCache, logger *slog.Logger) *PRReviewWorkflow {
	if cfg == nil || slack == nil || logger == nil {
		panic("workflow: NewPRReviewWorkflow missing required dep")
	}
	return &PRReviewWorkflow{cfg: cfg, slack: slack, github: gh, repoCache: repoCache, logger: logger}
}

// Type returns the TaskType discriminator.
func (w *PRReviewWorkflow) Type() string { return "pr_review" }

// Trigger gates on the feature flag, then dispatches:
//   - args non-empty → A-path (validateAndBuild).
//   - args empty, thread scan finds URL → D-path confirm selector.
//   - args empty, scan miss → D-path modal.
//
// All three paths produce a Pending with identity fields (Reporter / ChannelName
// / RequestID / TaskType) populated so BuildJob can reuse them later.
func (w *PRReviewWorkflow) Trigger(ctx context.Context, ev TriggerEvent, args string) (NextStep, error) {
	if !w.cfg.PRReview.IsEnabled() {
		return NextStep{Kind: NextStepError, ErrorText: "PR Review 尚未啟用，請聯絡管理員"}, nil
	}

	// Identity resolution shared by all three paths — matches IssueWorkflow /
	// AskWorkflow so BuildJob can rely on p.RequestID / p.Reporter / p.ChannelName.
	reqID := logging.NewRequestID()
	reporter := w.slack.ResolveUser(ev.UserID)
	channelName := w.slack.GetChannelName(ev.ChannelID)

	args = strings.TrimSpace(args)
	if args != "" {
		return w.validateAndBuild(ctx, ev, reqID, reporter, channelName, args)
	}

	// D-path: scan thread.
	msgs, err := w.slack.FetchThreadContext(ev.ChannelID, ev.ThreadTS, ev.TriggerTS, 50)
	if err == nil {
		texts := make([]string, len(msgs))
		for i, m := range msgs {
			texts[i] = m.Text
		}
		if url, ok := ScanThreadForPRURL(texts); ok {
			pending := &Pending{
				ChannelID:   ev.ChannelID,
				ThreadTS:    ev.ThreadTS,
				TriggerTS:   ev.TriggerTS,
				UserID:      ev.UserID,
				Reporter:    reporter,
				ChannelName: channelName,
				RequestID:   reqID,
				TaskType:    "pr_review",
				Phase:       "pr_review_confirm",
				State:       &prReviewState{URL: url},
			}
			return NextStep{
				Kind:           NextStepPostSelector,
				SelectorPrompt: fmt.Sprintf(":eyes: 找到 `%s`，review？", url),
				SelectorActions: []SelectorAction{
					{ActionID: "pr_review_confirm", Label: "是", Value: url},
					{ActionID: "pr_review_confirm", Label: "改貼 URL", Value: "manual"},
				},
				Pending: pending,
			}, nil
		}
	}

	// Not found → modal.
	pending := &Pending{
		ChannelID:   ev.ChannelID,
		ThreadTS:    ev.ThreadTS,
		TriggerTS:   ev.TriggerTS,
		UserID:      ev.UserID,
		Reporter:    reporter,
		ChannelName: channelName,
		RequestID:   reqID,
		TaskType:    "pr_review",
		Phase:       "pr_review_modal",
		State:       &prReviewState{},
	}
	return NextStep{
		Kind:           NextStepOpenModal,
		ModalTriggerID: "",
		ModalTitle:     "PR Review",
		ModalLabel:     "貼上 PR URL",
		ModalInputName: "pr_url",
		Pending:        pending,
	}, nil
}

// validateAndBuild runs the URL validator + GitHub API check, returning
// either a submit-ready NextStep or an error step with a friendly message.
// Identity fields (reqID/reporter/channelName) are threaded through so the
// A-path Pending carries the same fields the D-path pendings do.
func (w *PRReviewWorkflow) validateAndBuild(ctx context.Context, ev TriggerEvent, reqID, reporter, channelName, urlStr string) (NextStep, error) {
	parts, err := ParsePRURL(urlStr)
	if err != nil {
		return NextStep{Kind: NextStepError, ErrorText: "請貼完整 PR URL"}, nil
	}

	if w.github == nil {
		return NextStep{Kind: NextStepError, ErrorText: "GitHub client not configured"}, nil
	}

	pr, err := w.github.GetPullRequest(ctx, parts.Owner, parts.Repo, parts.Number)
	if err != nil {
		msg := mapGitHubErrorToSlack(err)
		return NextStep{Kind: NextStepError, ErrorText: msg}, nil
	}

	state := &prReviewState{
		URL:      urlStr,
		Owner:    parts.Owner,
		Repo:     parts.Repo,
		Number:   parts.Number,
		HeadRepo: pr.Head.Repo.FullName,
		HeadRef:  pr.Head.Ref,
		HeadSHA:  pr.Head.SHA,
		BaseRef:  pr.Base.Ref,
	}
	pending := &Pending{
		ChannelID:   ev.ChannelID,
		ThreadTS:    ev.ThreadTS,
		TriggerTS:   ev.TriggerTS,
		UserID:      ev.UserID,
		Reporter:    reporter,
		ChannelName: channelName,
		RequestID:   reqID,
		TaskType:    "pr_review",
		Phase:       "", // A-path submits directly; no phase label needed.
		State:       state,
	}
	return NextStep{Kind: NextStepSubmit, Pending: pending}, nil
}

// mapGitHubErrorToSlack turns raw GitHub client errors into friendly Slack text.
// 404 / 403 / network classes each get a distinct message so operators can
// self-diagnose without reading logs. Status codes use HasPrefix because
// ghclient.Client.GetPullRequest formats errors as "<code> <body>" — a Contains
// check would misclassify a 500 whose body happens to contain "404". Network
// errors (dial/timeout) stay on Contains since they have no structured prefix.
func mapGitHubErrorToSlack(err error) string {
	msg := err.Error()
	switch {
	case strings.HasPrefix(msg, "404"):
		return "找不到 PR"
	case strings.HasPrefix(msg, "403"):
		return "沒權限存取 PR"
	case strings.Contains(msg, "dial"), strings.Contains(msg, "timeout"):
		return "GitHub 不可達，請稍後重試"
	default:
		return "GitHub API 錯誤: " + msg
	}
}

// Selection handles follow-up button clicks and modal submits.
//   - pr_review_confirm: "是" (value=URL) re-runs validateAndBuild; "manual"
//     opens the modal instead.
//   - pr_review_modal: value is the URL the user pasted into the modal.
func (w *PRReviewWorkflow) Selection(ctx context.Context, p *Pending, value string) (NextStep, error) {
	st, ok := p.State.(*prReviewState)
	if !ok {
		return NextStep{Kind: NextStepError, ErrorText: "PRReviewWorkflow: unexpected state type"}, nil
	}
	_ = st

	switch p.Phase {
	case "pr_review_confirm":
		if value == "manual" {
			p.Phase = "pr_review_modal"
			return NextStep{
				Kind:           NextStepOpenModal,
				ModalTitle:     "PR Review",
				ModalLabel:     "貼上 PR URL",
				ModalInputName: "pr_url",
				ModalMetadata:  p.SelectorTS,
				Pending:        p,
			}, nil
		}
		// "是" — value is the URL that was offered by the confirm prompt.
		ev := TriggerEvent{ChannelID: p.ChannelID, ThreadTS: p.ThreadTS, TriggerTS: p.TriggerTS, UserID: p.UserID}
		return w.validateAndBuild(ctx, ev, p.RequestID, p.Reporter, p.ChannelName, value)

	case "pr_review_modal":
		ev := TriggerEvent{ChannelID: p.ChannelID, ThreadTS: p.ThreadTS, TriggerTS: p.TriggerTS, UserID: p.UserID}
		return w.validateAndBuild(ctx, ev, p.RequestID, p.Reporter, p.ChannelName, value)
	}

	return NextStep{Kind: NextStepError, ErrorText: fmt.Sprintf("PRReviewWorkflow: unknown phase %q", p.Phase)}, nil
}

// BuildJob assembles the queue.Job. Repo/Branch/CloneURL come from the PR's
// head repo so worker clones the fork, not the base. WorkflowArgs ferries
// pr_url + pr_number to the worker so the pr-review-helper skill can target
// the right PR.
func (w *PRReviewWorkflow) BuildJob(ctx context.Context, p *Pending) (*queue.Job, string, error) {
	st, ok := p.State.(*prReviewState)
	if !ok {
		return nil, "", fmt.Errorf("PRReviewWorkflow.BuildJob: unexpected state type")
	}

	reqID := p.RequestID
	if reqID == "" {
		reqID = logging.NewRequestID()
	}

	cloneURL := fmt.Sprintf("https://github.com/%s.git", st.HeadRepo)

	job := &queue.Job{
		ID:          reqID,
		RequestID:   reqID,
		TaskType:    "pr_review",
		ChannelID:   p.ChannelID,
		ThreadTS:    p.ThreadTS,
		UserID:      p.UserID,
		Repo:        st.HeadRepo,
		Branch:      st.HeadSHA, // SHA — immutable git ref consumed by `git worktree add`
		CloneURL:    cloneURL,
		SubmittedAt: time.Now(),
		PromptContext: &queue.PromptContext{
			Branch:           st.HeadRef, // human-readable branch name for prompt
			Goal:             w.cfg.Prompt.PRReview.Goal,
			ResponseSchema:   w.cfg.Prompt.PRReview.ResponseSchema,
			OutputRules:      w.cfg.Prompt.PRReview.OutputRules,
			Language:         w.cfg.Prompt.Language,
			Channel:          p.ChannelName,
			Reporter:         p.Reporter,
			AllowWorkerRules: w.cfg.Prompt.IsWorkerRulesAllowed(),
			// ThreadMessages / Attachments filled by downstream submit-helper.
		},
		WorkflowArgs: map[string]string{
			"pr_url":    st.URL,
			"pr_number": strconv.Itoa(st.Number),
		},
	}
	return job, fmt.Sprintf(":eyes: Reviewing `%s/%s#%d`...", st.Owner, st.Repo, st.Number), nil
}

// HandleResult routes the review output through POSTED / SKIPPED / ERROR /
// parse-fail / failure / cancelled branches. Metrics go to
// WorkflowCompletionsTotal("pr_review", status).
func (w *PRReviewWorkflow) HandleResult(ctx context.Context, state *queue.JobState, r *queue.JobResult) error {
	if state == nil || state.Job == nil {
		return fmt.Errorf("HandleResult: state or state.Job is nil")
	}
	job := state.Job
	prURL := job.WorkflowArgs["pr_url"]

	if r.Status == "failed" {
		metrics.WorkflowCompletionsTotal.WithLabelValues("pr_review", "error").Inc()
		text := fmt.Sprintf(":x: Review 失敗：%s", r.Error)
		return w.post(job, text)
	}

	if r.Status == "cancelled" {
		metrics.WorkflowCompletionsTotal.WithLabelValues("pr_review", "cancelled").Inc()
		return w.post(job, fmt.Sprintf(":white_check_mark: 已取消。已貼的 comments 保留於 PR %s", prURL))
	}

	parsed, err := ParseReviewOutput(r.RawOutput)
	if err != nil {
		metrics.WorkflowCompletionsTotal.WithLabelValues("pr_review", "parse_failed").Inc()
		w.logger.Warn("pr_review parse failed", "output_head", firstN(r.RawOutput, 2000))
		// Intentionally keep r.Status="completed" — PR Review is best-effort
		// with no retry lane. Listener clears dedup after this branch returns.
		return w.post(job, fmt.Sprintf(":x: Review 失敗：parse error: %v", err))
	}

	switch parsed.Status {
	case "POSTED":
		metrics.WorkflowCompletionsTotal.WithLabelValues("pr_review", "posted").Inc()
		return w.post(job, fmt.Sprintf(
			":white_check_mark: Review 完成 (severity: %s · %d comments, %d skipped) on %s\n> %s",
			fallback(parsed.Severity, "unknown"), parsed.CommentsPosted, parsed.CommentsSkipped, prURL,
			firstN(parsed.Summary, 200),
		))
	case "SKIPPED":
		metrics.WorkflowCompletionsTotal.WithLabelValues("pr_review", "skipped").Inc()
		return w.post(job, fmt.Sprintf(":information_source: Review 跳過 (%s): %s", parsed.Reason, firstN(parsed.Summary, 200)))
	case "ERROR":
		metrics.WorkflowCompletionsTotal.WithLabelValues("pr_review", "error").Inc()
		return w.post(job, fmt.Sprintf(":x: Review 失敗：%s", parsed.Error))
	}
	return nil
}

func (w *PRReviewWorkflow) post(job *queue.Job, text string) error {
	if job.StatusMsgTS != "" {
		return w.slack.UpdateMessage(job.ChannelID, job.StatusMsgTS, text)
	}
	return w.slack.PostMessage(job.ChannelID, text, job.ThreadTS)
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func fallback(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
