package bot

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"slack-issue-bot/internal/config"
	ghclient "slack-issue-bot/internal/github"
	"slack-issue-bot/internal/mantis"
	slackclient "slack-issue-bot/internal/slack"
)

const pendingTimeout = 1 * time.Minute

type pendingTriage struct {
	ChannelID      string
	ThreadTS       string
	TriggerTS      string
	Attachments    []string
	SelectedRepo   string
	SelectedBranch string
	Phase          string
	SelectorTS     string
	Reporter       string
	ChannelName    string
	ExtraDesc      string
	CmdArgs        string
}

type Workflow struct {
	cfg           *config.Config
	slack         *slackclient.Client
	handler       *slackclient.Handler
	repoCache     *ghclient.RepoCache
	repoDiscovery *ghclient.RepoDiscovery
	agentRunner   *AgentRunner
	mantisClient  *mantis.Client

	mu        sync.Mutex
	pending   map[string]*pendingTriage
	autoBound map[string]bool
}

func NewWorkflow(
	cfg *config.Config,
	slack *slackclient.Client,
	repoCache *ghclient.RepoCache,
	repoDiscovery *ghclient.RepoDiscovery,
	agentRunner *AgentRunner,
	mantisClient *mantis.Client,
) *Workflow {
	return &Workflow{
		cfg:           cfg,
		slack:         slack,
		repoCache:     repoCache,
		repoDiscovery: repoDiscovery,
		agentRunner:   agentRunner,
		mantisClient:  mantisClient,
		pending:       make(map[string]*pendingTriage),
		autoBound:     make(map[string]bool),
	}
}

func (w *Workflow) SetHandler(h *slackclient.Handler) { w.handler = h }

func (w *Workflow) RegisterChannel(channelID string) {
	w.mu.Lock()
	w.autoBound[channelID] = true
	w.mu.Unlock()
}

func (w *Workflow) UnregisterChannel(channelID string) {
	w.mu.Lock()
	delete(w.autoBound, channelID)
	w.mu.Unlock()
}

func (w *Workflow) HandleTrigger(event slackclient.TriggerEvent) {
	if event.ThreadTS == "" {
		w.slack.PostMessage(event.ChannelID, ":warning: 請在對話串中使用此指令。", "")
		return
	}

	channelCfg, ok := w.cfg.Channels[event.ChannelID]
	if !ok {
		w.mu.Lock()
		isBound := w.autoBound[event.ChannelID]
		w.mu.Unlock()
		if !isBound && !w.cfg.AutoBind {
			return
		}
		channelCfg = w.cfg.ChannelDefaults
	}

	reporter := w.slack.ResolveUser(event.UserID)
	channelName := w.slack.GetChannelName(event.ChannelID)

	pt := &pendingTriage{
		ChannelID:   event.ChannelID,
		ThreadTS:    event.ThreadTS,
		TriggerTS:   event.TriggerTS,
		Reporter:    reporter,
		ChannelName: channelName,
		CmdArgs:     parseTriggerArgs(event.Text),
	}

	repo, branch := parseRepoArg(pt.CmdArgs)
	if repo != "" {
		pt.SelectedRepo = repo
		if branch != "" {
			pt.SelectedBranch = branch
			w.showDescriptionPrompt(pt)
			return
		}
		w.afterRepoSelected(pt, channelCfg)
		return
	}

	repos := channelCfg.GetRepos()

	if len(repos) == 1 {
		pt.SelectedRepo = repos[0]
		w.afterRepoSelected(pt, channelCfg)
		return
	}

	if len(repos) > 1 {
		pt.Phase = "repo"
		selectorTS, err := w.slack.PostSelector(event.ChannelID,
			":point_right: Which repo should this issue go to?",
			"repo_select", repos, pt.ThreadTS)
		if err != nil {
			w.notifyError(event.ChannelID, pt.ThreadTS, "Failed to show repo selector: %v", err)
			return
		}
		pt.SelectorTS = selectorTS
		w.storePending(selectorTS, pt)
		return
	}

	pt.Phase = "repo_search"
	selectorTS, err := w.slack.PostExternalSelector(event.ChannelID,
		":point_right: Search and select a repo:",
		"repo_search", "Type to search repos...", pt.ThreadTS)
	if err != nil {
		w.notifyError(event.ChannelID, pt.ThreadTS, "Failed to show repo search: %v", err)
		return
	}
	pt.SelectorTS = selectorTS
	w.storePending(selectorTS, pt)
}

func (w *Workflow) HandleRepoSuggestion(query string) []string {
	repos, err := w.repoDiscovery.SearchRepos(context.Background(), query)
	if err != nil {
		slog.Warn("repo search failed", "error", err)
		return nil
	}
	return repos
}

func (w *Workflow) HandleSelection(channelID, actionID, value, selectorMsgTS string) {
	w.mu.Lock()
	pt, ok := w.pending[selectorMsgTS]
	if ok {
		delete(w.pending, selectorMsgTS)
	}
	w.mu.Unlock()
	if !ok {
		return
	}

	channelCfg := w.cfg.ChannelDefaults
	if cc, ok := w.cfg.Channels[pt.ChannelID]; ok {
		channelCfg = cc
	}

	switch pt.Phase {
	case "repo", "repo_search":
		w.slack.UpdateMessage(channelID, selectorMsgTS,
			fmt.Sprintf(":white_check_mark: Repo: `%s`", value))
		pt.SelectedRepo = value
		w.afterRepoSelected(pt, channelCfg)
	case "branch":
		w.slack.UpdateMessage(channelID, selectorMsgTS,
			fmt.Sprintf(":white_check_mark: Branch: `%s`", value))
		pt.SelectedBranch = value
		w.showDescriptionPrompt(pt)
	}
}

func (w *Workflow) afterRepoSelected(pt *pendingTriage, channelCfg config.ChannelConfig) {
	if !channelCfg.IsBranchSelectEnabled() {
		w.showDescriptionPrompt(pt)
		return
	}

	repoPath, err := w.repoCache.EnsureRepo(pt.SelectedRepo)
	if err != nil {
		w.notifyError(pt.ChannelID, pt.ThreadTS, "Failed to access repo %s: %v", pt.SelectedRepo, err)
		return
	}

	var branches []string
	if len(channelCfg.Branches) > 0 {
		branches = channelCfg.Branches
	} else {
		branches, err = w.repoCache.ListBranches(repoPath)
		if err != nil {
			w.showDescriptionPrompt(pt)
			return
		}
	}

	if len(branches) <= 1 {
		if len(branches) == 1 {
			pt.SelectedBranch = branches[0]
		}
		w.showDescriptionPrompt(pt)
		return
	}

	pt.Phase = "branch"
	selectorTS, err := w.slack.PostSelector(pt.ChannelID,
		fmt.Sprintf(":point_right: Which branch of `%s`?", pt.SelectedRepo),
		"branch_select", branches, pt.ThreadTS)
	if err != nil {
		w.showDescriptionPrompt(pt)
		return
	}
	pt.SelectorTS = selectorTS
	w.storePending(selectorTS, pt)
}

func (w *Workflow) showDescriptionPrompt(pt *pendingTriage) {
	pt.Phase = "description"
	selectorTS, err := w.slack.PostSelector(pt.ChannelID,
		":memo: 需要補充說明嗎？（補充後可讓分析更精準）",
		"description_action", []string{"補充說明", "跳過"}, pt.ThreadTS)
	if err != nil {
		w.runTriage(pt)
		return
	}
	pt.SelectorTS = selectorTS
	w.storePending(selectorTS, pt)
}

func (w *Workflow) HandleDescriptionAction(channelID, value, selectorMsgTS, triggerID string) {
	w.mu.Lock()
	pt, ok := w.pending[selectorMsgTS]
	if !ok {
		w.mu.Unlock()
		return
	}

	if value == "跳過" {
		delete(w.pending, selectorMsgTS)
		w.mu.Unlock()
		w.slack.UpdateMessage(channelID, selectorMsgTS, ":fast_forward: 跳過補充說明")
		w.runTriage(pt)
		return
	}

	w.mu.Unlock()

	if triggerID == "" {
		w.mu.Lock()
		delete(w.pending, selectorMsgTS)
		w.mu.Unlock()
		w.runTriage(pt)
		return
	}

	if err := w.slack.OpenDescriptionModal(triggerID, selectorMsgTS); err != nil {
		w.mu.Lock()
		delete(w.pending, selectorMsgTS)
		w.mu.Unlock()
		w.runTriage(pt)
	}
}

func (w *Workflow) HandleDescriptionSubmit(selectorMsgTS, extraText string) {
	w.mu.Lock()
	pt, ok := w.pending[selectorMsgTS]
	if ok {
		delete(w.pending, selectorMsgTS)
	}
	w.mu.Unlock()
	if !ok {
		return
	}

	if extraText != "" {
		w.slack.UpdateMessage(pt.ChannelID, selectorMsgTS,
			fmt.Sprintf(":memo: 補充說明: %s", extraText))
		pt.ExtraDesc = extraText
	}
	w.runTriage(pt)
}

func (w *Workflow) runTriage(pt *pendingTriage) {
	ctx := context.Background()

	tempDir, err := os.MkdirTemp("", "triage-*")
	if err != nil {
		w.notifyError(pt.ChannelID, pt.ThreadTS, "Failed to create temp dir: %v", err)
		w.clearDedup(pt)
		return
	}
	defer os.RemoveAll(tempDir)

	w.slack.PostMessage(pt.ChannelID, ":mag: 正在分析...", pt.ThreadTS)

	// 1. Ensure repo checked out.
	repoPath, err := w.repoCache.EnsureRepo(pt.SelectedRepo)
	if err != nil {
		w.notifyError(pt.ChannelID, pt.ThreadTS, "Failed to access repo %s: %v", pt.SelectedRepo, err)
		w.clearDedup(pt)
		return
	}
	if pt.SelectedBranch != "" {
		if err := w.repoCache.Checkout(repoPath, pt.SelectedBranch); err != nil {
			w.notifyError(pt.ChannelID, pt.ThreadTS, "Failed to checkout branch %s: %v", pt.SelectedBranch, err)
			w.clearDedup(pt)
			return
		}
	}

	// 2. Read thread context.
	botUserID := ""
	rawMsgs, err := w.slack.FetchThreadContext(pt.ChannelID, pt.ThreadTS, pt.TriggerTS, botUserID, w.cfg.MaxThreadMessages)
	if err != nil {
		w.notifyError(pt.ChannelID, pt.ThreadTS, "Failed to read thread: %v", err)
		w.clearDedup(pt)
		return
	}

	// 3. Download attachments.
	downloads := w.slack.DownloadAttachments(rawMsgs, tempDir)

	// 4. Enrich messages (Mantis URLs).
	var threadMsgs []ThreadMessage
	for _, m := range rawMsgs {
		text := m.Text
		if w.mantisClient != nil {
			text = enrichMessage(text, w.mantisClient)
		}
		threadMsgs = append(threadMsgs, ThreadMessage{
			User:      w.slack.ResolveUser(m.User),
			Timestamp: m.Timestamp,
			Text:      text,
		})
	}

	// 5. Build attachments info.
	var attachments []AttachmentInfo
	for _, d := range downloads {
		if d.Failed {
			attachments = append(attachments, AttachmentInfo{
				Name: d.Name + " (download failed)",
				Type: d.Type,
			})
			continue
		}
		attachments = append(attachments, AttachmentInfo{
			Path: d.Path,
			Name: d.Name,
			Type: d.Type,
		})
	}

	// 6. Resolve labels.
	channelCfg := w.cfg.ChannelDefaults
	if cc, ok := w.cfg.Channels[pt.ChannelID]; ok {
		channelCfg = cc
	}

	// 7. Build prompt.
	prompt := BuildPrompt(PromptInput{
		ThreadMessages:   threadMsgs,
		Attachments:      attachments,
		ExtraDescription: pt.ExtraDesc,
		RepoPath:         repoPath,
		Branch:           pt.SelectedBranch,
		GitHubRepo:       pt.SelectedRepo,
		Channel:          pt.ChannelName,
		Reporter:         pt.Reporter,
		Labels:           channelCfg.DefaultLabels,
		Prompt:           w.cfg.Prompt,
	})

	// 8. Spawn agent — agent explores codebase, creates GitHub issue (or rejects).
	output, err := w.agentRunner.Run(ctx, repoPath, prompt)
	if err != nil {
		w.notifyError(pt.ChannelID, pt.ThreadTS, "分析工具暫時不可用: %v", err)
		w.clearDedup(pt)
		return
	}

	// 9. Parse result.
	result, err := ParseAgentOutput(output)
	if err != nil {
		w.notifyError(pt.ChannelID, pt.ThreadTS, "分析工具暫時不可用: %v", err)
		w.clearDedup(pt)
		return
	}

	// 10. Report to Slack.
	switch result.Status {
	case "CREATED":
		branchInfo := ""
		if pt.SelectedBranch != "" {
			branchInfo = fmt.Sprintf(" (branch: `%s`)", pt.SelectedBranch)
		}
		w.slack.PostMessage(pt.ChannelID,
			fmt.Sprintf(":white_check_mark: Issue created%s: %s", branchInfo, result.IssueURL),
			pt.ThreadTS)
	case "REJECTED":
		w.slack.PostMessage(pt.ChannelID,
			fmt.Sprintf(":warning: 無法建立 issue — %s", result.Message),
			pt.ThreadTS)
	case "ERROR":
		w.notifyError(pt.ChannelID, pt.ThreadTS, "Agent error: %s", result.Message)
	}

	w.clearDedup(pt)
}

func (w *Workflow) storePending(selectorTS string, pt *pendingTriage) {
	w.mu.Lock()
	w.pending[selectorTS] = pt
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
			w.slack.UpdateMessage(pt.ChannelID, selectorTS, ":hourglass: 已超時")
			w.slack.PostMessage(pt.ChannelID,
				":hourglass: 選擇已超時，請重新觸發。", pt.ThreadTS)
			w.clearDedup(pt)
		}
	}()
}

func (w *Workflow) clearDedup(pt *pendingTriage) {
	if w.handler != nil {
		w.handler.ClearThreadDedup(pt.ChannelID, pt.ThreadTS)
	}
}

func (w *Workflow) notifyError(channelID, threadTS string, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	slog.Error("workflow error", "message", msg)
	w.slack.PostMessage(channelID, fmt.Sprintf(":x: %s", msg), threadTS)
}

func parseTriggerArgs(text string) string {
	text = strings.TrimSpace(text)
	if idx := strings.Index(text, ">"); idx != -1 {
		text = strings.TrimSpace(text[idx+1:])
	}
	text = strings.TrimPrefix(text, "/triage")
	return strings.TrimSpace(text)
}

func parseRepoArg(args string) (repo, branch string) {
	if args == "" {
		return "", ""
	}
	if !strings.Contains(args, "/") {
		return "", ""
	}
	parts := strings.SplitN(args, "@", 2)
	repo = strings.TrimSpace(parts[0])
	if len(parts) == 2 {
		branch = strings.TrimSpace(parts[1])
	}
	return repo, branch
}
