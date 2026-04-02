package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"slack-issue-bot/internal/config"
	"slack-issue-bot/internal/diagnosis"
	ghclient "slack-issue-bot/internal/github"
	"slack-issue-bot/internal/llm"
	slackclient "slack-issue-bot/internal/slack"
)

// pendingIssue stores context between the reaction event and user selections.
type pendingIssue struct {
	Event       slackclient.ReactionEvent
	ReactionCfg config.ReactionConfig
	ChannelCfg  config.ChannelConfig
	Message     string
	Reporter    string
	ChannelName string
	SelectorTS  string // timestamp of the current selector message
	SelectedRepo string // set after repo selection
	Phase       string // "repo" or "branch"
}

type Workflow struct {
	cfg         *config.Config
	slack       *slackclient.Client
	issueClient *ghclient.IssueClient
	repoCache   *ghclient.RepoCache
	diagEngine  *diagnosis.Engine

	mu      sync.Mutex
	pending map[string]*pendingIssue // keyed by selectorTS
}

func NewWorkflow(
	cfg *config.Config,
	slack *slackclient.Client,
	issueClient *ghclient.IssueClient,
	repoCache *ghclient.RepoCache,
	diagEngine *diagnosis.Engine,
) *Workflow {
	return &Workflow{
		cfg:         cfg,
		slack:       slack,
		issueClient: issueClient,
		repoCache:   repoCache,
		diagEngine:  diagEngine,
		pending:     make(map[string]*pendingIssue),
	}
}

func (w *Workflow) HandleReaction(event slackclient.ReactionEvent) {
	channelCfg, ok := w.cfg.Channels[event.ChannelID]
	if !ok {
		slog.Debug("channel not configured, ignoring", "channel", event.ChannelID)
		return
	}

	reactionCfg, ok := w.cfg.Reactions[event.Reaction]
	if !ok {
		slog.Debug("reaction not configured, ignoring", "reaction", event.Reaction)
		return
	}

	repos := channelCfg.GetRepos()
	if len(repos) == 0 {
		slog.Warn("no repos configured for channel", "channel", event.ChannelID)
		return
	}

	message, err := w.slack.FetchMessage(event.ChannelID, event.MessageTS)
	if err != nil {
		w.notifyError(event.ChannelID, "Failed to read the original message: %v", err)
		return
	}

	reporter := w.slack.ResolveUser(event.UserID)
	channelName := w.slack.GetChannelName(event.ChannelID)

	slog.Info("processing reaction event",
		"channel", event.ChannelID,
		"reaction", event.Reaction,
		"type", reactionCfg.Type,
		"repos", repos,
	)

	pi := &pendingIssue{
		Event:       event,
		ReactionCfg: reactionCfg,
		ChannelCfg:  channelCfg,
		Message:     message,
		Reporter:    reporter,
		ChannelName: channelName,
	}

	if len(repos) == 1 {
		pi.SelectedRepo = repos[0]
		w.afterRepoSelected(pi)
		return
	}

	// Multiple repos: show repo selector
	pi.Phase = "repo"
	selectorTS, err := w.slack.PostSelector(event.ChannelID,
		":point_right: Which repo should this issue go to?",
		"repo_select", repos)
	if err != nil {
		w.notifyError(event.ChannelID, "Failed to show repo selector: %v", err)
		return
	}

	pi.SelectorTS = selectorTS
	w.mu.Lock()
	w.pending[selectorTS] = pi
	w.mu.Unlock()
}

// HandleSelection is called when a user clicks any selector button.
func (w *Workflow) HandleSelection(channelID, actionID, value, selectorMsgTS string) {
	w.mu.Lock()
	pi, ok := w.pending[selectorMsgTS]
	if ok {
		delete(w.pending, selectorMsgTS)
	}
	w.mu.Unlock()

	if !ok {
		slog.Debug("no pending issue for selector", "ts", selectorMsgTS)
		return
	}

	switch pi.Phase {
	case "repo":
		w.slack.UpdateMessage(channelID, selectorMsgTS,
			fmt.Sprintf(":white_check_mark: Repo: `%s`", value))
		pi.SelectedRepo = value
		slog.Info("repo selected", "repo", value)
		w.afterRepoSelected(pi)

	case "branch":
		w.slack.UpdateMessage(channelID, selectorMsgTS,
			fmt.Sprintf(":white_check_mark: Branch: `%s`", value))
		slog.Info("branch selected", "branch", value)
		w.afterBranchSelected(pi, value)
	}
}

// afterRepoSelected is called once a repo is determined. Shows branch selector if enabled.
func (w *Workflow) afterRepoSelected(pi *pendingIssue) {
	if !pi.ChannelCfg.IsBranchSelectEnabled() {
		// No branch selection, proceed with default branch
		w.createIssue(pi, "")
		return
	}

	// Clone/fetch the repo first to get branch list
	repoPath, err := w.repoCache.EnsureRepo(pi.SelectedRepo)
	if err != nil {
		w.notifyError(pi.Event.ChannelID, "Failed to access repo %s: %v", pi.SelectedRepo, err)
		return
	}

	// Get branches: use whitelist from config, or auto-detect
	var branches []string
	if len(pi.ChannelCfg.Branches) > 0 {
		branches = pi.ChannelCfg.Branches
	} else {
		branches, err = w.repoCache.ListBranches(repoPath)
		if err != nil {
			slog.Warn("failed to list branches, skipping selection", "error", err)
			w.createIssue(pi, "")
			return
		}
	}

	if len(branches) <= 1 {
		// Only one branch, skip selection
		branch := ""
		if len(branches) == 1 {
			branch = branches[0]
		}
		w.createIssue(pi, branch)
		return
	}

	// Show branch selector
	pi.Phase = "branch"
	selectorTS, err := w.slack.PostSelector(pi.Event.ChannelID,
		fmt.Sprintf(":point_right: Which branch of `%s`?", pi.SelectedRepo),
		"branch_select", branches)
	if err != nil {
		slog.Warn("failed to show branch selector, using default", "error", err)
		w.createIssue(pi, "")
		return
	}

	pi.SelectorTS = selectorTS
	w.mu.Lock()
	w.pending[selectorTS] = pi
	w.mu.Unlock()
}

// afterBranchSelected checkouts the branch and proceeds to issue creation.
func (w *Workflow) afterBranchSelected(pi *pendingIssue, branch string) {
	w.createIssue(pi, branch)
}

func (w *Workflow) createIssue(pi *pendingIssue, branch string) {
	ctx := context.Background()

	repoPath, err := w.repoCache.EnsureRepo(pi.SelectedRepo)
	if err != nil {
		w.notifyError(pi.Event.ChannelID, "Failed to access repo %s: %v", pi.SelectedRepo, err)
		return
	}

	if branch != "" {
		if err := w.repoCache.Checkout(repoPath, branch); err != nil {
			w.notifyError(pi.Event.ChannelID, "Failed to checkout branch %s: %v", branch, err)
			return
		}
	}

	keywords := slackclient.ExtractKeywords(pi.Message)
	diagInput := diagnosis.DiagnoseInput{
		Type:     pi.ReactionCfg.Type,
		Message:  pi.Message,
		RepoPath: repoPath,
		Keywords: keywords,
		Prompt: llm.PromptOptions{
			Language:   w.cfg.Diagnosis.Prompt.Language,
			ExtraRules: w.cfg.Diagnosis.Prompt.ExtraRules,
		},
	}

	var diagResp llm.DiagnoseResponse
	mode := w.cfg.Diagnosis.Mode
	if mode == "" {
		mode = "full"
	}

	if mode == "full" {
		var diagErr error
		diagResp, diagErr = w.diagEngine.Diagnose(ctx, diagInput)
		if diagErr != nil {
			slog.Warn("AI diagnosis failed, falling back to lite mode", "error", diagErr)
			w.slack.PostMessage(pi.Event.ChannelID, ":warning: AI diagnosis unavailable, creating issue with file references only")
			mode = "lite"
		}
	}

	if mode == "lite" {
		relevantFiles := w.diagEngine.FindFiles(diagInput)
		diagResp = llm.DiagnoseResponse{}
		diagResp.Files = relevantFiles
	}

	parts := strings.SplitN(pi.SelectedRepo, "/", 2)
	if len(parts) != 2 {
		w.notifyError(pi.Event.ChannelID, "Invalid repo format: %s (expected owner/repo)", pi.SelectedRepo)
		return
	}
	owner, repo := parts[0], parts[1]

	labels := append(pi.ReactionCfg.IssueLabels, pi.ChannelCfg.DefaultLabels...)

	issueInput := ghclient.IssueInput{
		Type:        pi.ReactionCfg.Type,
		TitlePrefix: pi.ReactionCfg.IssueTitlePrefix,
		Channel:     pi.ChannelName,
		Reporter:    pi.Reporter,
		Message:     pi.Message,
		Labels:      labels,
		Diagnosis:   diagResp,
	}

	issueURL, err := w.issueClient.CreateIssue(ctx, owner, repo, issueInput)
	if err != nil {
		w.notifyError(pi.Event.ChannelID, "Failed to create GitHub issue: %v", err)
		return
	}

	branchInfo := ""
	if branch != "" {
		branchInfo = fmt.Sprintf(" (branch: `%s`)", branch)
	}
	msg := fmt.Sprintf(":white_check_mark: Issue created%s: %s", branchInfo, issueURL)
	if err := w.slack.PostMessage(pi.Event.ChannelID, msg); err != nil {
		slog.Error("failed to post issue URL to slack", "error", err)
	}

	slog.Info("workflow completed", "issueURL", issueURL, "repo", pi.SelectedRepo, "branch", branch)
}

func (w *Workflow) notifyError(channelID string, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	slog.Error("workflow error", "message", msg)
	w.slack.PostMessage(channelID, fmt.Sprintf(":x: %s", msg))
}
