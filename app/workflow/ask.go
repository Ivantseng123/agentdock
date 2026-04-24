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

// AskPriorAnswerOptIn is the button value for the "include previous
// answer" opt-in on the description prompt. Exported so app/bot's
// HandleDescriptionAction can recognise it as a submit-path (no modal
// follows, so the selector message must be deleted on click).
const AskPriorAnswerOptIn = "帶上次回覆"

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
	// PriorAnswer caches the bot's most recent substantive reply in this
	// thread (if any), fetched once at descriptionPromptStep time. nil means
	// either none exists, no fetch yet, or the fetch failed — which we
	// collapse into "no opt-in affordance".
	PriorAnswer *queue.ThreadMessage
	// IncludePriorAnswer is set by the user clicking the opt-in button on
	// the description prompt. BuildJob honours this to attach PriorAnswer
	// to the worker's PromptContext.
	IncludePriorAnswer bool
	// priorAnswerFetchAttempted guards against double-fetching if
	// descriptionPromptStep runs twice for the same ask (shouldn't happen
	// in current flow, but cheap defense against future back-nav changes).
	priorAnswerFetchAttempted bool
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
	return w.attachPromptStep(pending), nil
}

// attachPromptStep builds the "附加 Repository?" selector used both on the
// initial Trigger and when the user backs out of the repo picker.
func (w *AskWorkflow) attachPromptStep(p *Pending) NextStep {
	return NextStep{
		Kind: NextStepSelector,
		Selector: &SelectorSpec{
			Prompt:   ":question: 要附加 Repository 嗎？",
			ActionID: "ask_attach_repo",
			Options: []SelectorOption{
				{Label: "附加", Value: "attach"},
				{Label: "不用", Value: "skip"},
			},
		},
		Pending: p,
	}
}

// Selection handles follow-up button clicks for the ask wizard. Six
// phases flow through here:
//   - ask_repo_prompt: attach/skip decision.
//   - ask_repo_select: user picked a specific repo, via button or external search.
//   - ask_branch_select: user picked a branch (only when branch_select enabled and repo has >1 branch).
//   - ask_prior_answer_prompt: yes/no on carrying the last bot answer (only when one exists).
//   - ask_description_prompt: optionally supplement the question via modal.
//   - ask_description_modal: modal submit; value is the text the user typed.
//
// Both skip-attach and repo-pick converge into priorAnswerOrDescriptionStep,
// which routes to either ask_prior_answer_prompt or straight to
// ask_description_prompt. Every ask therefore gets the chance to clarify
// what the agent should do — the D-selector path (empty args) especially
// needs this — otherwise the agent only sees the raw thread.
func (w *AskWorkflow) Selection(ctx context.Context, p *Pending, value string) (NextStep, error) {
	st, ok := p.State.(*askState)
	if !ok {
		return NextStep{Kind: NextStepError, ErrorText: "invalid pending state"}, nil
	}

	switch p.Phase {
	case "ask_repo_prompt":
		if value == "skip" {
			st.AttachRepo = false
			return w.priorAnswerOrDescriptionStep(p), nil
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
			// No repos configured — fall back to external search. Back button
			// rides along so the user can bail to the attach prompt without
			// abandoning the whole flow.
			return NextStep{
				Kind: NextStepSelector,
				Selector: &SelectorSpec{
					Prompt:         ":point_right: Search and select a repo:",
					ActionID:       "ask_repo",
					Searchable:     true,
					Placeholder:    "Type to search repos...",
					CancelActionID: "ask_repo_back",
					CancelLabel:    "← 返回",
				},
				Pending: p,
			}, nil
		}
		options := make([]SelectorOption, 0, len(repos)+1)
		for _, r := range repos {
			options = append(options, SelectorOption{Label: r, Value: r})
		}
		options = append(options, SelectorOption{Label: "← 返回", Value: "back_to_attach"})
		return NextStep{
			Kind: NextStepSelector,
			Selector: &SelectorSpec{
				Prompt:   ":point_right: Which repo?",
				ActionID: "ask_repo",
				Options:  options,
			},
			Pending: p,
		}, nil

	case "ask_repo_select":
		if value == "back_to_attach" || value == "← 返回" {
			// User bailed out of the repo picker — reset the attach choice
			// and re-emit the attach/skip prompt so they can try again.
			st.AttachRepo = false
			st.SelectedRepo = ""
			p.Phase = "ask_repo_prompt"
			return w.attachPromptStep(p), nil
		}
		st.SelectedRepo = value
		return w.afterRepoSelectedStep(p), nil

	case "ask_branch_select":
		if value == "取消" {
			return NextStep{Kind: NextStepCancel}, nil
		}
		st.SelectedBranch = value
		return w.priorAnswerOrDescriptionStep(p), nil

	case "ask_prior_answer_prompt":
		// Standalone yes/no on whether to carry the bot's last substantive
		// reply into this turn. The cached PriorAnswer was populated in
		// priorAnswerOrDescriptionStep; we only need to flip the flag here.
		// Both answers continue to the description prompt — the two questions
		// are orthogonal, so neither subsumes the other.
		if value == AskPriorAnswerOptIn {
			st.IncludePriorAnswer = true
		}
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
		return w.priorAnswerOrDescriptionStep(p)
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
		return w.priorAnswerOrDescriptionStep(p)
	}

	p.Phase = "ask_branch_select"
	options := make([]SelectorOption, 0, len(branches)+1)
	for _, b := range branches {
		options = append(options, SelectorOption{Label: b, Value: b})
	}
	options = append(options, SelectorOption{Label: "取消", Value: "取消"})
	return NextStep{
		Kind: NextStepSelector,
		Selector: &SelectorSpec{
			Prompt:   fmt.Sprintf(":point_right: Which branch of `%s`?", st.SelectedRepo),
			ActionID: "ask_branch",
			Options:  options,
		},
		Pending: p,
	}
}

// priorAnswerOrDescriptionStep is the single entry into the post-repo/branch
// phases. It fetches the thread's most recent substantive bot reply once per
// ask; when one exists, it shows a dedicated 帶上次回覆 / 不用 selector so the
// opt-in is a standalone yes/no question. Otherwise it falls through to the
// 補充說明 / 跳過 prompt untouched. Splitting these used to live in one
// 3-button selector, but that forced users to pick opt-in XOR 補充說明 even
// though the choices are orthogonal.
//
// Fetch failures degrade silently to the no-prior-answer path — the feature
// is a convenience, not a core path (issue #151).
func (w *AskWorkflow) priorAnswerOrDescriptionStep(p *Pending) NextStep {
	st, _ := p.State.(*askState)
	if st != nil && !st.priorAnswerFetchAttempted {
		st.priorAnswerFetchAttempted = true
		if raw, err := w.slack.FetchPriorBotAnswer(p.ChannelID, p.ThreadTS, p.TriggerTS, w.cfg.MaxThreadMessages); err != nil {
			w.logger.Warn("prior bot answer fetch failed, continuing without opt-in",
				"phase", "處理中", "error", err)
		} else if raw != nil {
			st.PriorAnswer = &queue.ThreadMessage{
				User:      raw.User,
				Timestamp: raw.Timestamp,
				Text:      raw.Text,
			}
		}
	}

	if st != nil && st.PriorAnswer != nil {
		p.Phase = "ask_prior_answer_prompt"
		return NextStep{
			Kind: NextStepSelector,
			Selector: &SelectorSpec{
				Prompt:   ":arrows_counterclockwise: 要帶上次回覆一起問嗎？",
				ActionID: "ask_prior_answer",
				Options: []SelectorOption{
					{Label: "帶上次回覆", Value: AskPriorAnswerOptIn},
					{Label: "不用", Value: "不用"},
				},
			},
			Pending: p,
		}
	}

	p.Phase = "ask_description_prompt"
	return w.descriptionPromptStep(p)
}

// descriptionPromptStep returns the 補充說明 / 跳過 selector. The opt-in
// 帶上次回覆 button used to live here but was promoted to its own phase —
// see priorAnswerOrDescriptionStep for the routing wrapper. ActionID mirrors
// IssueWorkflow's so app.go's description_action route (which owns the
// modal-trigger-id forwarding) handles the click without Ask needing its
// own special case.
func (w *AskWorkflow) descriptionPromptStep(p *Pending) NextStep {
	return NextStep{
		Kind: NextStepSelector,
		Selector: &SelectorSpec{
			Prompt:   ":pencil2: 要補充說明想讓 agent 做什麼嗎？",
			ActionID: "description_action",
			Options: []SelectorOption{
				{Label: "補充說明", Value: "補充說明"},
				{Label: "跳過", Value: "跳過"},
			},
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

	var priorAnswer []queue.ThreadMessage
	if st.IncludePriorAnswer && st.PriorAnswer != nil {
		priorAnswer = []queue.ThreadMessage{*st.PriorAnswer}
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
			ResponseSchema:   w.cfg.Prompt.Ask.ResponseSchema,
			OutputRules:      w.cfg.Prompt.Ask.OutputRules,
			Language:         w.cfg.Prompt.Language,
			ExtraDescription: st.Question,
			Branch:           st.SelectedBranch,
			Channel:          p.ChannelName,
			Reporter:         p.Reporter,
			AllowWorkerRules: w.cfg.Prompt.IsWorkerRulesAllowed(),
			PriorAnswer:      priorAnswer,
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
		truncated := logging.Redact(r.RawOutput, w.cfg.Secrets)
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
//
// When file upload fails (e.g. workspace missing the files:write scope
// for files.upload v2), the answer is posted inline as a fallback so the
// user still sees the content — truncated messages are preferable to a
// dangling "已附為檔案：" preview with no file.
func (w *AskWorkflow) post(job *queue.Job, text string) error {
	if len(text) <= askInlineThreshold {
		if job.StatusMsgTS != "" {
			return w.slack.UpdateMessage(job.ChannelID, job.StatusMsgTS, text)
		}
		return w.slack.PostMessage(job.ChannelID, text, job.ThreadTS)
	}

	preview := fmt.Sprintf(":memo: 答案較長（約 %d 字），已附為檔案：", len(text))
	var uploadErr error
	if job.StatusMsgTS != "" {
		// Flip the earlier "思考中..." status into the preview so the
		// thread has one lifecycle marker + the file, not two notices.
		_ = w.slack.UpdateMessage(job.ChannelID, job.StatusMsgTS, preview)
		uploadErr = w.slack.UploadFile(job.ChannelID, job.ThreadTS, "answer.md", "Answer", text, "")
	} else {
		uploadErr = w.slack.UploadFile(job.ChannelID, job.ThreadTS, "answer.md", "Answer", text, preview)
	}
	if uploadErr == nil {
		return nil
	}

	w.logger.Warn("Slack 檔案上傳失敗，改用內嵌訊息",
		"phase", "失敗", "error", uploadErr, "length", len(text))
	if job.StatusMsgTS != "" {
		_ = w.slack.UpdateMessage(job.ChannelID, job.StatusMsgTS, ":memo: 答案如下：")
	}
	return w.slack.PostMessage(job.ChannelID, text, job.ThreadTS)
}
