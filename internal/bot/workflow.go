package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"slack-issue-bot/internal/config"
	"slack-issue-bot/internal/diagnosis"
	ghclient "slack-issue-bot/internal/github"
	"slack-issue-bot/internal/llm"
	"slack-issue-bot/internal/mantis"
	slackclient "slack-issue-bot/internal/slack"
)

const pendingTimeout = 1 * time.Minute

// pendingIssue stores context between the reaction event and user selections.
type pendingIssue struct {
	Event          slackclient.ReactionEvent
	ReactionCfg    config.ReactionConfig
	ChannelCfg     config.ChannelConfig
	Message        string
	Images         []slackclient.ImageData
	Reporter       string
	ChannelName    string
	ThreadTS       string // thread parent = the original reacted message
	SelectorTS     string // timestamp of the current selector message
	SelectedRepo   string // set after repo selection
	SelectedBranch string // set after branch selection
	Phase          string // "repo", "branch", "repo_search", or "description"
}

type Workflow struct {
	cfg           *config.Config
	slack         *slackclient.Client
	handler       *slackclient.Handler
	issueClient   *ghclient.IssueClient
	repoCache     *ghclient.RepoCache
	repoDiscovery *ghclient.RepoDiscovery
	diagEngine    *diagnosis.Engine
	mantisClient  *mantis.Client

	mu           sync.Mutex
	pending      map[string]*pendingIssue // keyed by selectorTS
	autoBound    map[string]bool          // auto-bound channel IDs
}

func NewWorkflow(
	cfg *config.Config,
	slack *slackclient.Client,
	issueClient *ghclient.IssueClient,
	repoCache *ghclient.RepoCache,
	repoDiscovery *ghclient.RepoDiscovery,
	diagEngine *diagnosis.Engine,
	mantisClient *mantis.Client,
) *Workflow {
	return &Workflow{
		cfg:           cfg,
		slack:         slack,
		issueClient:   issueClient,
		repoCache:     repoCache,
		repoDiscovery: repoDiscovery,
		diagEngine:    diagEngine,
		mantisClient:  mantisClient,
		pending:       make(map[string]*pendingIssue),
		autoBound:     make(map[string]bool),
	}
}

// SetHandler sets the handler reference for clearing dedup on timeout.
func (w *Workflow) SetHandler(h *slackclient.Handler) {
	w.handler = h
}

// RegisterChannel marks a channel as auto-bound (called when bot joins a channel).
func (w *Workflow) RegisterChannel(channelID string) {
	w.mu.Lock()
	w.autoBound[channelID] = true
	w.mu.Unlock()
	slog.Info("auto-bound channel", "channel", channelID)
}

// UnregisterChannel removes an auto-bound channel (called when bot leaves).
func (w *Workflow) UnregisterChannel(channelID string) {
	w.mu.Lock()
	delete(w.autoBound, channelID)
	w.mu.Unlock()
	slog.Info("unbound channel", "channel", channelID)
}

func (w *Workflow) HandleReaction(event slackclient.ReactionEvent) {
	channelCfg, ok := w.cfg.Channels[event.ChannelID]

	// If not in static config, check auto-bound channels
	if !ok {
		w.mu.Lock()
		isBound := w.autoBound[event.ChannelID]
		w.mu.Unlock()

		if !isBound && !w.cfg.AutoBind {
			slog.Debug("channel not configured, ignoring", "channel", event.ChannelID)
			return
		}
		// Use channel defaults for auto-bound channels
		channelCfg = w.cfg.ChannelDefaults
	}

	reactionCfg, ok := w.cfg.Reactions[event.Reaction]
	if !ok {
		slog.Debug("reaction not configured, ignoring", "reaction", event.Reaction)
		return
	}

	fetched, err := w.slack.FetchMessage(event.ChannelID, event.MessageTS)
	if err != nil {
		w.notifyError(event.ChannelID, event.MessageTS, "Failed to read the original message: %v", err)
		return
	}

	// Enrich message: expand Mantis URLs with title + description
	fetched.Text = enrichMessage(fetched.Text, w.mantisClient)

	reporter := w.slack.ResolveUser(event.UserID)
	channelName := w.slack.GetChannelName(event.ChannelID)

	slog.Info("processing reaction event",
		"channel", event.ChannelID,
		"reaction", event.Reaction,
		"type", reactionCfg.Type,
	)

	pi := &pendingIssue{
		Event:       event,
		ReactionCfg: reactionCfg,
		ChannelCfg:  channelCfg,
		Message:     fetched.Text,
		Images:      fetched.Images,
		Reporter:    reporter,
		ChannelName: channelName,
		ThreadTS:    event.MessageTS,
	}

	repos := channelCfg.GetRepos()

	if len(repos) == 1 {
		pi.SelectedRepo = repos[0]
		w.afterRepoSelected(pi)
		return
	}

	if len(repos) > 1 {
		// Static config: show buttons
		pi.Phase = "repo"
		selectorTS, err := w.slack.PostSelector(event.ChannelID,
			":point_right: Which repo should this issue go to?",
			"repo_select", repos, pi.ThreadTS)
		if err != nil {
			w.notifyError(event.ChannelID, pi.ThreadTS, "Failed to show repo selector: %v", err)
			return
		}
		pi.SelectorTS = selectorTS
		w.storePending(selectorTS, pi)
		return
	}

	// No repos configured: use auto-discovery with searchable dropdown
	pi.Phase = "repo_search"
	selectorTS, err := w.slack.PostExternalSelector(event.ChannelID,
		":point_right: Search and select a repo:",
		"repo_search",
		"Type to search repos...",
		pi.ThreadTS)
	if err != nil {
		w.notifyError(event.ChannelID, pi.ThreadTS, "Failed to show repo search: %v", err)
		return
	}
	pi.SelectorTS = selectorTS
	w.storePending(selectorTS, pi)
}

// HandleRepoSuggestion returns filtered repo options for the type-ahead dropdown.
func (w *Workflow) HandleRepoSuggestion(query string) []string {
	ctx := context.Background()
	repos, err := w.repoDiscovery.SearchRepos(ctx, query)
	if err != nil {
		slog.Warn("repo search failed", "error", err)
		return nil
	}
	return repos
}

// HandleSelection is called when a user clicks any selector button.
func (w *Workflow) HandleSelection(channelID, actionID, value, selectorMsgTS string) {
	slog.Info("handling selection", "channelID", channelID, "action", actionID, "value", value, "selectorTS", selectorMsgTS)

	w.mu.Lock()
	pi, ok := w.pending[selectorMsgTS]
	if ok {
		delete(w.pending, selectorMsgTS)
	}
	w.mu.Unlock()

	if !ok {
		slog.Warn("no pending issue for selector", "ts", selectorMsgTS)
		return
	}

	switch pi.Phase {
	case "repo", "repo_search":
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
		w.showDescriptionPrompt(pi)
		return
	}

	repoPath, err := w.repoCache.EnsureRepo(pi.SelectedRepo)
	if err != nil {
		w.notifyError(pi.Event.ChannelID, pi.ThreadTS, "Failed to access repo %s: %v", pi.SelectedRepo, err)
		return
	}

	var branches []string
	if len(pi.ChannelCfg.Branches) > 0 {
		branches = pi.ChannelCfg.Branches
	} else {
		branches, err = w.repoCache.ListBranches(repoPath)
		if err != nil {
			slog.Warn("failed to list branches, skipping selection", "error", err)
			w.showDescriptionPrompt(pi)
			return
		}
	}

	if len(branches) <= 1 {
		if len(branches) == 1 {
			pi.SelectedBranch = branches[0]
		}
		w.showDescriptionPrompt(pi)
		return
	}

	pi.Phase = "branch"
	selectorTS, err := w.slack.PostSelector(pi.Event.ChannelID,
		fmt.Sprintf(":point_right: Which branch of `%s`?", pi.SelectedRepo),
		"branch_select", branches, pi.ThreadTS)
	if err != nil {
		slog.Warn("failed to show branch selector, using default", "error", err)
		w.showDescriptionPrompt(pi)
		return
	}

	pi.SelectorTS = selectorTS
	w.storePending(selectorTS, pi)
}

func (w *Workflow) afterBranchSelected(pi *pendingIssue, branch string) {
	pi.SelectedBranch = branch
	w.showDescriptionPrompt(pi)
}

// showDescriptionPrompt posts buttons to let the reporter add extra context or skip.
func (w *Workflow) showDescriptionPrompt(pi *pendingIssue) {
	pi.Phase = "description"
	selectorTS, err := w.slack.PostSelector(pi.Event.ChannelID,
		":memo: 需要補充說明嗎？（補充後可讓分析更精準）",
		"description_action", []string{"補充說明", "跳過"}, pi.ThreadTS)
	if err != nil {
		slog.Warn("failed to show description prompt, proceeding", "error", err)
		w.createIssue(pi, pi.SelectedBranch)
		return
	}
	pi.SelectorTS = selectorTS
	w.storePending(selectorTS, pi)
}

// HandleDescriptionAction is called when the user clicks "補充說明" or "跳過".
func (w *Workflow) HandleDescriptionAction(channelID, value, selectorMsgTS, triggerID string) {
	w.mu.Lock()
	pi, ok := w.pending[selectorMsgTS]
	if !ok {
		w.mu.Unlock()
		slog.Warn("no pending issue for description action", "ts", selectorMsgTS)
		return
	}

	if value == "跳過" {
		delete(w.pending, selectorMsgTS)
		w.mu.Unlock()
		w.slack.UpdateMessage(channelID, selectorMsgTS, ":fast_forward: 跳過補充說明")
		w.createIssue(pi, pi.SelectedBranch)
		return
	}

	// "補充說明" — open modal. Keep pending for modal submission.
	w.mu.Unlock()

	if triggerID == "" {
		slog.Warn("no trigger_id for modal, skipping description")
		w.mu.Lock()
		delete(w.pending, selectorMsgTS)
		w.mu.Unlock()
		w.createIssue(pi, pi.SelectedBranch)
		return
	}

	err := w.slack.OpenDescriptionModal(triggerID, selectorMsgTS)
	if err != nil {
		slog.Warn("failed to open description modal", "error", err)
		w.mu.Lock()
		delete(w.pending, selectorMsgTS)
		w.mu.Unlock()
		w.createIssue(pi, pi.SelectedBranch)
	}
}

// HandleDescriptionSubmit is called when the modal is submitted.
func (w *Workflow) HandleDescriptionSubmit(selectorMsgTS, extraText string) {
	w.mu.Lock()
	pi, ok := w.pending[selectorMsgTS]
	if ok {
		delete(w.pending, selectorMsgTS)
	}
	w.mu.Unlock()

	if !ok {
		slog.Warn("no pending issue for modal submit", "ts", selectorMsgTS)
		return
	}

	w.slack.UpdateMessage(pi.Event.ChannelID, selectorMsgTS,
		fmt.Sprintf(":memo: 補充說明: %s", extraText))

	if extraText != "" {
		pi.Message += "\n\n補充說明：" + extraText
		slog.Info("description added", "length", len(extraText))
	}

	w.createIssue(pi, pi.SelectedBranch)
}

const (
	maxOpenQuestions = 5
)

func (w *Workflow) createIssue(pi *pendingIssue, branch string) {
	ctx := context.Background()

	repoPath, err := w.repoCache.EnsureRepo(pi.SelectedRepo)
	if err != nil {
		w.notifyError(pi.Event.ChannelID, pi.ThreadTS, "Failed to access repo %s: %v", pi.SelectedRepo, err)
		return
	}

	if branch != "" {
		if err := w.repoCache.Checkout(repoPath, branch); err != nil {
			w.notifyError(pi.Event.ChannelID, pi.ThreadTS, "Failed to checkout branch %s: %v", branch, err)
			return
		}
	}

	w.slack.PostMessage(pi.Event.ChannelID, ":mag: 正在分析...", pi.ThreadTS)

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
			w.slack.PostMessage(pi.Event.ChannelID, ":warning: AI diagnosis unavailable, creating issue with file references only", pi.ThreadTS)
			mode = "lite"
		}
	}

	if mode == "lite" {
		relevantFiles := w.diagEngine.FindFiles(diagInput)
		diagResp = llm.DiagnoseResponse{}
		diagResp.Files = relevantFiles
	}

	// --- Rejection check (only confidence=low) ---
	if reason := w.shouldReject(diagResp); reason != "" {
		slog.Info("issue rejected", "reason", reason, "repo", pi.SelectedRepo)
		w.slack.PostMessage(pi.Event.ChannelID,
			fmt.Sprintf(":warning: 無法建立 issue — %s\n請試著更具體地描述問題（例如哪個畫面、什麼操作、預期結果）。", reason),
			pi.ThreadTS)
		if w.handler != nil {
			w.handler.ClearMessageDedup(pi.Event.ChannelID, pi.Event.MessageTS)
		}
		return
	}

	// --- Skip triage check (files=0 or too many questions, but not rejected) ---
	if w.shouldSkipTriage(diagResp) {
		slog.Info("skipping AI triage section", "files", len(diagResp.Files), "open_questions", len(diagResp.OpenQuestions), "repo", pi.SelectedRepo)
		diagResp = llm.DiagnoseResponse{} // Clear triage — issue will have only the original message
	}

	parts := strings.SplitN(pi.SelectedRepo, "/", 2)
	if len(parts) != 2 {
		w.notifyError(pi.Event.ChannelID, pi.ThreadTS, "Invalid repo format: %s (expected owner/repo)", pi.SelectedRepo)
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
		RepoOwner:   owner,
		RepoName:    repo,
		Branch:      branch,
	}

	issueURL, err := w.issueClient.CreateIssue(ctx, owner, repo, issueInput)
	if err != nil {
		w.notifyError(pi.Event.ChannelID, pi.ThreadTS, "Failed to create GitHub issue: %v", err)
		return
	}

	branchInfo := ""
	if branch != "" {
		branchInfo = fmt.Sprintf(" (branch: `%s`)", branch)
	}
	msg := fmt.Sprintf(":white_check_mark: Issue created%s: %s", branchInfo, issueURL)
	if err := w.slack.PostMessage(pi.Event.ChannelID, msg, pi.ThreadTS); err != nil {
		slog.Error("failed to post issue URL to slack", "error", err)
	}

	slog.Info("workflow completed", "issueURL", issueURL, "repo", pi.SelectedRepo, "branch", branch)

	// Clear dedup so the same message can be re-triggered
	if w.handler != nil {
		w.handler.ClearMessageDedup(pi.Event.ChannelID, pi.Event.MessageTS)
	}
}

// storePending saves a pending issue and starts a timeout goroutine.
func (w *Workflow) storePending(selectorTS string, pi *pendingIssue) {
	w.mu.Lock()
	w.pending[selectorTS] = pi
	w.mu.Unlock()

	go func() {
		time.Sleep(pendingTimeout)
		w.mu.Lock()
		_, stillPending := w.pending[selectorTS]
		if stillPending {
			delete(w.pending, selectorTS)
		}
		w.mu.Unlock()

		if stillPending {
			slog.Info("pending issue timed out", "selectorTS", selectorTS, "phase", pi.Phase)
			w.slack.UpdateMessage(pi.Event.ChannelID, selectorTS, ":hourglass: 已超時")
			w.slack.PostMessage(pi.Event.ChannelID,
				":hourglass: 選擇已超時，請移除貼圖後重新貼上以重新觸發。",
				pi.ThreadTS)
			// Clear dedup so re-triggering works
			if w.handler != nil {
				w.handler.ClearMessageDedup(pi.Event.ChannelID, pi.Event.MessageTS)
			}
		}
	}()
}

// shouldReject returns a rejection reason if the issue should NOT be created.
// Only rejects on confidence=low (likely wrong repo / completely irrelevant).
func (w *Workflow) shouldReject(resp llm.DiagnoseResponse) string {
	if resp.Summary == "" {
		return ""
	}
	if strings.EqualFold(resp.Confidence, "low") {
		return "問題與此 repo 的程式碼關聯性不足"
	}
	return ""
}

// shouldSkipTriage returns true if the AI triage section should be omitted
// from the issue (files=0 or too many open questions, but not rejected).
func (w *Workflow) shouldSkipTriage(resp llm.DiagnoseResponse) bool {
	if len(resp.Files) == 0 {
		return true
	}
	if len(resp.OpenQuestions) >= maxOpenQuestions {
		return true
	}
	return false
}

func (w *Workflow) notifyError(channelID, threadTS string, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	slog.Error("workflow error", "message", msg)
	w.slack.PostMessage(channelID, fmt.Sprintf(":x: %s", msg), threadTS)
}
