package workflow

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Ivantseng123/agentdock/app/config"
	ghclient "github.com/Ivantseng123/agentdock/shared/github"
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
	CmdArgs        string
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
// the legacy `@bot <repo>` paths. Skeleton — Task 2.4 ports the real logic.
func (w *IssueWorkflow) Trigger(ctx context.Context, ev TriggerEvent, args string) (NextStep, error) {
	if args != "" {
		// Task 2.4 ports real logic; for the skeleton, acknowledge the arg
		// path so tests compile and the "repo arg short-circuits" test passes.
		return NextStep{
			Kind: NextStepSubmit,
			Pending: &Pending{
				ChannelID: ev.ChannelID,
				ThreadTS:  ev.ThreadTS,
				UserID:    ev.UserID,
				TaskType:  "issue",
				State:     &issueState{CmdArgs: args},
			},
		}, nil
	}
	return NextStep{
		Kind:      NextStepError,
		ErrorText: fmt.Sprintf("IssueWorkflow.Trigger not yet implemented (args=%q)", args),
	}, nil
}

// Selection handles follow-up button clicks / modal submits.
func (w *IssueWorkflow) Selection(ctx context.Context, p *Pending, value string) (NextStep, error) {
	return NextStep{Kind: NextStepError, ErrorText: "IssueWorkflow.Selection not yet implemented"}, nil
}

// BuildJob assembles the queue.Job from the completed pending state.
func (w *IssueWorkflow) BuildJob(ctx context.Context, p *Pending) (*queue.Job, string, error) {
	return nil, "", fmt.Errorf("IssueWorkflow.BuildJob not yet implemented")
}

// HandleResult parses the agent's ===TRIAGE_RESULT=== output and posts back
// to Slack / creates the GitHub issue.
func (w *IssueWorkflow) HandleResult(ctx context.Context, job *queue.Job, r *queue.JobResult) error {
	return fmt.Errorf("IssueWorkflow.HandleResult not yet implemented")
}
