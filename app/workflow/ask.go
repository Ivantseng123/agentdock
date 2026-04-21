package workflow

import (
	"context"
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
