package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Ivantseng123/agentdock/app/config"
	ghclient "github.com/Ivantseng123/agentdock/shared/github"
	"github.com/Ivantseng123/agentdock/shared/logging"
	"github.com/Ivantseng123/agentdock/shared/metrics"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

// AskWorkflow handles @bot ask queries. Optional attached repo with branch
// selection (mirrors Issue when channel has branch_select enabled), plus an
// optional description modal. Result is an agent-produced answer posted as a
// bot message in the thread.
type AskWorkflow struct {
	cfg       *config.Config
	slack     SlackPort
	repoCache *ghclient.RepoCache
	logger    *slog.Logger
}

type askState struct {
	Question       string // from args; empty = use thread only
	AttachRepo     bool
	SelectedRepo   string
	SelectedBranch string
}

// NewAskWorkflow constructs a workflow instance.
func NewAskWorkflow(cfg *config.Config, slack SlackPort, repoCache *ghclient.RepoCache, logger *slog.Logger) *AskWorkflow {
	if cfg == nil || slack == nil || logger == nil {
		panic("workflow: NewAskWorkflow missing required dep")
	}
	return &AskWorkflow{cfg: cfg, slack: slack, repoCache: repoCache, logger: logger}
}

// Type returns the TaskType discriminator.
func (w *AskWorkflow) Type() string { return "ask" }

// Trigger posts the attach-repo selector regardless of whether args has
// question text; if args is empty, the thread content is the question.
func (w *AskWorkflow) Trigger(ctx context.Context, ev TriggerEvent, args string) (NextStep, error) {
	// Populate common fields on the pending envelope — matches IssueWorkflow
	// so BuildJob can rely on p.RequestID / p.Reporter / p.ChannelName.
	reqID := logging.NewRequestID()
	reporter := w.slack.ResolveUser(ev.UserID)
	channelName := w.slack.GetChannelName(ev.ChannelID)

	pending := &Pending{
		ChannelID:   ev.ChannelID,
		ThreadTS:    ev.ThreadTS,
		TriggerTS:   ev.TriggerTS,
		UserID:      ev.UserID,
		Reporter:    reporter,
		ChannelName: channelName,
		RequestID:   reqID,
		Phase:       "ask_repo_prompt",
		TaskType:    "ask",
		State:       &askState{Question: args},
	}
	return NextStep{
		Kind:           NextStepPostSelector,
		SelectorPrompt: ":question: 要附加 repo context 嗎？",
		SelectorActions: []SelectorAction{
			{ActionID: "ask_attach_repo", Label: "附加", Value: "attach"},
			{ActionID: "ask_attach_repo", Label: "不用", Value: "skip"},
		},
		Pending: pending,
	}, nil
}

// Selection handles follow-up button clicks for the ask wizard. Five
// phases flow through here:
//   - ask_repo_prompt: attach/skip decision.
//   - ask_repo_select: user picked a specific repo, via button or external search.
//   - ask_branch_select: user picked a branch (only when branch_select enabled and repo has >1 branch).
//   - ask_description_prompt: optionally supplement the question via modal.
//   - ask_description_modal: modal submit; value is the text the user typed.
//
// Both skip-attach and repo-pick converge into ask_description_prompt so
// every ask gets the chance to clarify what the agent should actually do.
// The D-selector path (empty args) especially needs this — otherwise the
// agent only sees the raw thread, which is often ambiguous.
func (w *AskWorkflow) Selection(ctx context.Context, p *Pending, value string) (NextStep, error) {
	st, ok := p.State.(*askState)
	if !ok {
		return NextStep{Kind: NextStepError, ErrorText: "invalid pending state"}, nil
	}

	switch p.Phase {
	case "ask_repo_prompt":
		if value == "skip" {
			st.AttachRepo = false
			p.Phase = "ask_description_prompt"
			return w.descriptionPromptStep(p), nil
		}
		// "attach" → move to repo selection.
		st.AttachRepo = true
		channelCfg := w.cfg.ChannelDefaults
		if cc, ok := w.cfg.Channels[p.ChannelID]; ok {
			channelCfg = cc
		}
		repos := channelCfg.GetRepos()
		p.Phase = "ask_repo_select"
		if len(repos) == 0 {
			// No repos configured — fall back to external search. Cancel button
			// rides along so the user isn't stuck if they change their mind.
			return NextStep{
				Kind:                   NextStepPostExternalSelector,
				SelectorPrompt:         ":point_right: Search and select a repo:",
				SelectorActionID:       "ask_repo",
				SelectorPlaceholder:    "Type to search repos...",
				SelectorCancelActionID: "ask_cancel",
				SelectorCancelLabel:    "取消",
				Pending:                p,
			}, nil
		}
		actions := make([]SelectorAction, 0, len(repos)+1)
		for _, r := range repos {
			actions = append(actions, SelectorAction{ActionID: "ask_repo", Label: r, Value: r})
		}
		actions = append(actions, SelectorAction{ActionID: "ask_repo", Label: "取消", Value: "取消"})
		return NextStep{
			Kind:            NextStepPostSelector,
			SelectorPrompt:  ":point_right: Which repo?",
			SelectorActions: actions,
			Pending:         p,
		}, nil

	case "ask_repo_select":
		if value == "取消" {
			return NextStep{Kind: NextStepCancel}, nil
		}
		st.SelectedRepo = value
		return w.afterRepoSelectedStep(p), nil

	case "ask_branch_select":
		if value == "取消" {
			return NextStep{Kind: NextStepCancel}, nil
		}
		st.SelectedBranch = value
		p.Phase = "ask_description_prompt"
		return w.descriptionPromptStep(p), nil

	case "ask_description_prompt":
		switch value {
		case "跳過":
			return NextStep{Kind: NextStepSubmit, Pending: p}, nil
		case "補充說明":
			// Phase must flip before OpenModal so the modal submit routes to
			// ask_description_modal (HandleDescriptionSubmit no longer rewrites
			// phase — it's workflow-owned now).
			p.Phase = "ask_description_modal"
			return NextStep{
				Kind:           NextStepOpenModal,
				ModalTitle:     "補充說明",
				ModalLabel:     "補充你想讓 agent 做什麼",
				ModalInputName: "description",
				ModalMetadata:  p.SelectorTS,
				Pending:        p,
			}, nil
		default:
			return NextStep{Kind: NextStepError, ErrorText: fmt.Sprintf(":x: unexpected description value: %q", value)}, nil
		}

	case "ask_description_modal":
		// value is the text the user submitted in the modal (empty on modal
		// close). Append to the original args-based Question so users who
		// typed `@bot ask <prefix>` can add more context without losing it.
		if value != "" {
			if st.Question != "" {
				st.Question = st.Question + "\n\n" + value
			} else {
				st.Question = value
			}
		}
		return NextStep{Kind: NextStepSubmit, Pending: p}, nil
	}

	return NextStep{Kind: NextStepError, ErrorText: fmt.Sprintf("unknown phase %q", p.Phase)}, nil
}

// afterRepoSelectedStep decides whether to show a branch selector or jump
// straight to the description prompt. Mirrors IssueWorkflow.afterRepoSelected:
// branch_select flag off, or ≤1 branch resolved, → skip branch step entirely.
// When repoCache is nil or branch listing fails, fall through to description
// prompt rather than erroring — branch is optional context for Ask.
func (w *AskWorkflow) afterRepoSelectedStep(p *Pending) NextStep {
	st := p.State.(*askState)

	channelCfg := w.cfg.ChannelDefaults
	if cc, ok := w.cfg.Channels[p.ChannelID]; ok {
		channelCfg = cc
	}

	if !channelCfg.IsBranchSelectEnabled() {
		p.Phase = "ask_description_prompt"
		return w.descriptionPromptStep(p)
	}

	var branches []string
	if len(channelCfg.Branches) > 0 {
		branches = channelCfg.Branches
	} else if w.repoCache != nil {
		ghToken := ""
		if w.cfg.Secrets != nil {
			ghToken = w.cfg.Secrets["GH_TOKEN"]
		}
		if repoPath, err := w.repoCache.EnsureRepo(st.SelectedRepo, ghToken); err == nil {
			if lb, listErr := w.repoCache.ListBranches(repoPath); listErr == nil {
				branches = lb
			}
		}
	}

	if len(branches) <= 1 {
		if len(branches) == 1 {
			st.SelectedBranch = branches[0]
		}
		p.Phase = "ask_description_prompt"
		return w.descriptionPromptStep(p)
	}

	p.Phase = "ask_branch_select"
	actions := make([]SelectorAction, 0, len(branches)+1)
	for _, b := range branches {
		actions = append(actions, SelectorAction{ActionID: "ask_branch", Label: b, Value: b})
	}
	actions = append(actions, SelectorAction{ActionID: "ask_branch", Label: "取消", Value: "取消"})
	return NextStep{
		Kind:            NextStepPostSelector,
		SelectorPrompt:  fmt.Sprintf(":point_right: Which branch of `%s`?", st.SelectedRepo),
		SelectorActions: actions,
		Pending:         p,
	}
}

// descriptionPromptStep returns the 補充說明 / 跳過 selector. ActionID
// mirrors IssueWorkflow's so app.go's description_action route (which owns
// the modal-trigger-id forwarding) handles the click without Ask needing
// its own special case.
func (w *AskWorkflow) descriptionPromptStep(p *Pending) NextStep {
	return NextStep{
		Kind:           NextStepPostSelector,
		SelectorPrompt: ":pencil2: 要補充說明想讓 agent 做什麼嗎？",
		SelectorActions: []SelectorAction{
			{ActionID: "description_action", Label: "補充說明", Value: "補充說明"},
			{ActionID: "description_action", Label: "跳過", Value: "跳過"},
		},
		Pending: p,
	}
}

// BuildJob assembles the queue.Job from the completed pending state.
// Status text is the message posted while the worker runs.
func (w *AskWorkflow) BuildJob(ctx context.Context, p *Pending) (*queue.Job, string, error) {
	st, ok := p.State.(*askState)
	if !ok {
		return nil, "", fmt.Errorf("AskWorkflow.BuildJob: unexpected state type")
	}

	reqID := p.RequestID
	if reqID == "" {
		reqID = logging.NewRequestID()
	}

	cloneURL := ""
	if st.AttachRepo && st.SelectedRepo != "" {
		cloneURL = cleanCloneURL(st.SelectedRepo)
	}

	job := &queue.Job{
		ID:          reqID,
		RequestID:   reqID,
		TaskType:    "ask",
		ChannelID:   p.ChannelID,
		ThreadTS:    p.ThreadTS,
		UserID:      p.UserID,
		Repo:        st.SelectedRepo,
		Branch:      st.SelectedBranch,
		CloneURL:    cloneURL,
		SubmittedAt: time.Now(),
		PromptContext: &queue.PromptContext{
			Goal:             w.cfg.Prompt.Ask.Goal,
			OutputRules:      w.cfg.Prompt.Ask.OutputRules,
			Language:         w.cfg.Prompt.Language,
			ExtraDescription: st.Question,
			Branch:           st.SelectedBranch,
			Channel:          p.ChannelName,
			Reporter:         p.Reporter,
			AllowWorkerRules: w.cfg.Prompt.IsWorkerRulesAllowed(),
			// ThreadMessages, Attachments filled by downstream submit-helper.
		},
		// Skills is populated by submitJob via loadSkills(); leaving it unset
		// here (instead of Skills: nil) avoids the misleading impression that
		// Ask opts out — the override in app.go:286 applies to every workflow.
	}
	return job, ":thinking_face: 思考中...", nil
}

const askMaxChars = 38000

// askInlineThreshold is the char length above which the answer goes into
// an uploaded .md file instead of the Slack message body. 2000 keeps most
// replies inline while packaging long-form answers so the channel isn't
// swamped. Matches where Slack's own text readability starts to degrade.
const askInlineThreshold = 2000

// HandleResult renders the agent output into the Slack thread. Failure paths
// are posted without a retry button (Ask is best-effort). Parse failures and
// answers are both final — no retry lane.
func (w *AskWorkflow) HandleResult(ctx context.Context, state *queue.JobState, r *queue.JobResult) error {
	if state == nil || state.Job == nil {
		return fmt.Errorf("HandleResult: state or state.Job is nil")
	}
	job := state.Job

	if r.Status == "failed" {
		text := fmt.Sprintf(":x: 思考失敗：%s", r.Error)
		return w.post(job, text)
	}

	parsed, err := ParseAskOutput(r.RawOutput)
	if err != nil {
		truncated := r.RawOutput
		if len(truncated) > 2000 {
			truncated = truncated[:2000] + "…(truncated)"
		}
		w.logger.Warn("ask parse failed", "phase", "失敗", "output", truncated, "err", err)
		metrics.WorkflowCompletionsTotal.WithLabelValues("ask", "parse_failed").Inc()
		// Intentionally keep r.Status="completed" — Ask is best-effort with no
		// retry lane, so letting the listener clear dedup on this path is
		// correct. IssueWorkflow flips to "failed" to gate its retry button.
		return w.post(job, fmt.Sprintf(":x: 解析失敗：%v", err))
	}

	answer := parsed.Answer
	if len(answer) > askMaxChars {
		answer = answer[:askMaxChars] + "\n…(已截斷)"
	}

	metrics.WorkflowCompletionsTotal.WithLabelValues("ask", "success").Inc()
	return w.post(job, answer)
}

// post writes the answer into the thread. Short answers replace the
// status message inline; answers over askInlineThreshold are uploaded as
// an answer.md file with a short preview comment, because long bodies
// render awkwardly in Slack's message column and agent output is
// mrkdwn-styled (which collapses its own structure at length).
func (w *AskWorkflow) post(job *queue.Job, text string) error {
	if len(text) <= askInlineThreshold {
		if job.StatusMsgTS != "" {
			return w.slack.UpdateMessage(job.ChannelID, job.StatusMsgTS, text)
		}
		return w.slack.PostMessage(job.ChannelID, text, job.ThreadTS)
	}

	preview := fmt.Sprintf(":memo: 答案較長（約 %d 字），已附為檔案：", len(text))
	if job.StatusMsgTS != "" {
		// Flip the earlier "思考中..." status into the preview so the
		// thread has one lifecycle marker + the file, not two notices.
		_ = w.slack.UpdateMessage(job.ChannelID, job.StatusMsgTS, preview)
		return w.slack.UploadFile(job.ChannelID, job.ThreadTS, "answer.md", "Answer", text, "")
	}
	return w.slack.UploadFile(job.ChannelID, job.ThreadTS, "answer.md", "Answer", text, preview)
}
