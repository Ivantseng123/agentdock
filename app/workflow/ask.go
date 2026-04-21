package workflow

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Ivantseng123/agentdock/app/config"
	ghclient "github.com/Ivantseng123/agentdock/shared/github"
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
	pending := &Pending{
		ChannelID: ev.ChannelID,
		ThreadTS:  ev.ThreadTS,
		TriggerTS: ev.TriggerTS,
		UserID:    ev.UserID,
		Phase:     "ask_repo_prompt",
		TaskType:  "ask",
		State:     &askState{Question: args},
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
