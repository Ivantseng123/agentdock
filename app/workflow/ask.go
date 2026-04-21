package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Ivantseng123/agentdock/app/config"
	ghclient "github.com/Ivantseng123/agentdock/shared/github"
	"github.com/Ivantseng123/agentdock/shared/logging"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

// AskWorkflow handles @bot ask queries. Optional attached repo (short wizard),
// no branch selection, no description modal. Result is an agent-produced
// answer posted as a bot message in the thread.
type AskWorkflow struct {
	cfg       *config.Config
	slack     SlackPort
	repoCache *ghclient.RepoCache
	logger    *slog.Logger
}

type askState struct {
	Question     string // from args; empty = use thread only
	AttachRepo   bool
	SelectedRepo string
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

// Selection handles follow-up button clicks for the ask wizard. Two phases
// are possible: ask_repo_prompt (attach/skip decision) and ask_repo_select
// (user picked a specific repo, or supplied one via external search).
func (w *AskWorkflow) Selection(ctx context.Context, p *Pending, value string) (NextStep, error) {
	st, ok := p.State.(*askState)
	if !ok {
		return NextStep{Kind: NextStepError, ErrorText: "invalid pending state"}, nil
	}

	switch p.Phase {
	case "ask_repo_prompt":
		if value == "skip" {
			st.AttachRepo = false
			return NextStep{Kind: NextStepSubmit, Pending: p}, nil
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
			// No repos configured — fall back to external search.
			return NextStep{
				Kind:                NextStepPostExternalSelector,
				SelectorPrompt:      ":point_right: Search and select a repo:",
				SelectorActionID:    "ask_repo",
				SelectorPlaceholder: "Type to search repos...",
				Pending:             p,
			}, nil
		}
		actions := make([]SelectorAction, len(repos))
		for i, r := range repos {
			actions[i] = SelectorAction{ActionID: "ask_repo", Label: r, Value: r}
		}
		return NextStep{
			Kind:            NextStepPostSelector,
			SelectorPrompt:  ":point_right: Which repo?",
			SelectorActions: actions,
			Pending:         p,
		}, nil

	case "ask_repo_select":
		st.SelectedRepo = value
		return NextStep{Kind: NextStepSubmit, Pending: p}, nil
	}

	return NextStep{Kind: NextStepError, ErrorText: fmt.Sprintf("unknown phase %q", p.Phase)}, nil
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
		CloneURL:    cloneURL,
		SubmittedAt: time.Now(),
		PromptContext: &queue.PromptContext{
			Goal:             w.cfg.Prompt.Ask.Goal,
			OutputRules:      w.cfg.Prompt.Ask.OutputRules,
			Language:         w.cfg.Prompt.Language,
			ExtraDescription: st.Question,
			Channel:          p.ChannelName,
			Reporter:         p.Reporter,
			AllowWorkerRules: w.cfg.Prompt.IsWorkerRulesAllowed(),
			// ThreadMessages, Attachments filled by downstream submit-helper.
		},
		// Skills intentionally nil — Ask flow defensive until empty-dir skill
		// spike (Phase 4) observed-safe for a release cycle.
		Skills: nil,
	}
	return job, ":thinking_face: 思考中...", nil
}
