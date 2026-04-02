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

const repoSelectCallbackID = "repo_select"

// pendingIssue stores context between the reaction event and the repo selection callback.
type pendingIssue struct {
	Event       slackclient.ReactionEvent
	ReactionCfg config.ReactionConfig
	ChannelCfg  config.ChannelConfig
	Message     string
	Reporter    string
	ChannelName string
	SelectorTS  string // timestamp of the repo selector message
}

type Workflow struct {
	cfg         *config.Config
	slack       *slackclient.Client
	issueClient *ghclient.IssueClient
	repoCache   *ghclient.RepoCache
	diagEngine  *diagnosis.Engine

	mu      sync.Mutex
	pending map[string]*pendingIssue // key: channelID:messageTS
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

	// Single repo: proceed directly
	if len(repos) == 1 {
		w.createIssue(event, reactionCfg, channelCfg, repos[0], message, reporter, channelName)
		return
	}

	// Multiple repos: store context and send selector buttons
	key := event.ChannelID + ":" + event.MessageTS
	pi := &pendingIssue{
		Event:       event,
		ReactionCfg: reactionCfg,
		ChannelCfg:  channelCfg,
		Message:     message,
		Reporter:    reporter,
		ChannelName: channelName,
	}

	selectorTS, err := w.slack.PostRepoSelector(event.ChannelID, repos, repoSelectCallbackID)
	if err != nil {
		w.notifyError(event.ChannelID, "Failed to show repo selector: %v", err)
		return
	}

	pi.SelectorTS = selectorTS

	w.mu.Lock()
	w.pending[key] = pi
	w.mu.Unlock()
}

// HandleRepoSelection is called when a user clicks a repo button.
func (w *Workflow) HandleRepoSelection(channelID, selectedRepo, selectorMsgTS string) {
	w.mu.Lock()
	var pi *pendingIssue
	var foundKey string
	for key, p := range w.pending {
		if p.SelectorTS == selectorMsgTS && p.Event.ChannelID == channelID {
			pi = p
			foundKey = key
			break
		}
	}
	if foundKey != "" {
		delete(w.pending, foundKey)
	}
	w.mu.Unlock()

	if pi == nil {
		slog.Warn("no pending issue found for repo selection", "selectorTS", selectorMsgTS)
		return
	}

	// Replace the button message with a confirmation
	w.slack.UpdateMessage(channelID, selectorMsgTS,
		fmt.Sprintf(":white_check_mark: Selected repo: `%s`", selectedRepo))

	w.createIssue(pi.Event, pi.ReactionCfg, pi.ChannelCfg, selectedRepo, pi.Message, pi.Reporter, pi.ChannelName)
}

// RepoSelectCallbackID returns the callback ID used for repo selection buttons.
func (w *Workflow) RepoSelectCallbackID() string {
	return repoSelectCallbackID
}

func (w *Workflow) createIssue(
	event slackclient.ReactionEvent,
	reactionCfg config.ReactionConfig,
	channelCfg config.ChannelConfig,
	repoRef string,
	message, reporter, channelName string,
) {
	ctx := context.Background()

	repoPath, err := w.repoCache.EnsureRepo(repoRef)
	if err != nil {
		w.notifyError(event.ChannelID, "Failed to access repo %s: %v", repoRef, err)
		return
	}

	keywords := slackclient.ExtractKeywords(message)
	diagInput := diagnosis.DiagnoseInput{
		Type:     reactionCfg.Type,
		Message:  message,
		RepoPath: repoPath,
		Keywords: keywords,
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
			w.slack.PostMessage(event.ChannelID, ":warning: AI diagnosis unavailable, creating issue with file references only")
			mode = "lite"
		}
	}

	if mode == "lite" {
		relevantFiles := w.diagEngine.FindFiles(diagInput)
		diagResp = llm.DiagnoseResponse{}
		diagResp.Files = relevantFiles
	}

	parts := strings.SplitN(repoRef, "/", 2)
	if len(parts) != 2 {
		w.notifyError(event.ChannelID, "Invalid repo format: %s (expected owner/repo)", repoRef)
		return
	}
	owner, repo := parts[0], parts[1]

	labels := append(reactionCfg.IssueLabels, channelCfg.DefaultLabels...)

	issueInput := ghclient.IssueInput{
		Type:        reactionCfg.Type,
		TitlePrefix: reactionCfg.IssueTitlePrefix,
		Channel:     channelName,
		Reporter:    reporter,
		Message:     message,
		Labels:      labels,
		Diagnosis:   diagResp,
	}

	issueURL, err := w.issueClient.CreateIssue(ctx, owner, repo, issueInput)
	if err != nil {
		w.notifyError(event.ChannelID, "Failed to create GitHub issue: %v", err)
		return
	}

	msg := fmt.Sprintf(":white_check_mark: Issue created: %s", issueURL)
	if err := w.slack.PostMessage(event.ChannelID, msg); err != nil {
		slog.Error("failed to post issue URL to slack", "error", err)
	}

	slog.Info("workflow completed", "issueURL", issueURL, "repo", repoRef)
}

func (w *Workflow) notifyError(channelID string, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	slog.Error("workflow error", "message", msg)
	w.slack.PostMessage(channelID, fmt.Sprintf(":x: %s", msg))
}
