package bot

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"agentdock/internal/config"
	ghclient "agentdock/internal/github"
	"agentdock/internal/logging"
	"agentdock/internal/mantis"
	"agentdock/internal/queue"
	slackclient "agentdock/internal/slack"
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
	RequestID      string
	Logger         *slog.Logger
}

type Workflow struct {
	cfg           *config.Config
	slack         *slackclient.Client
	handler       *slackclient.Handler
	repoCache     *ghclient.RepoCache
	repoDiscovery *ghclient.RepoDiscovery
	agentRunner   *AgentRunner
	mantisClient  *mantis.Client
	queue         queue.JobQueue
	store         queue.JobStore
	attachments   queue.AttachmentStore
	skills        map[string]string

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
	jobQueue queue.JobQueue,
	jobStore queue.JobStore,
	attachStore queue.AttachmentStore,
	skills map[string]string,
) *Workflow {
	return &Workflow{
		cfg:           cfg,
		slack:         slack,
		repoCache:     repoCache,
		repoDiscovery: repoDiscovery,
		agentRunner:   agentRunner,
		mantisClient:  mantisClient,
		queue:         jobQueue,
		store:         jobStore,
		attachments:   attachStore,
		skills:        skills,
		pending:       make(map[string]*pendingTriage),
		autoBound:     make(map[string]bool),
	}
}

func (w *Workflow) SetHandler(h *slackclient.Handler) { w.handler = h }

func (w *Workflow) channelPriority(channelID string) int {
	if pri, ok := w.cfg.ChannelPriority[channelID]; ok {
		return pri
	}
	if pri, ok := w.cfg.ChannelPriority["default"]; ok {
		return pri
	}
	return 50
}

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

	reqID := logging.NewRequestID()
	logger := slog.With(
		"request_id", reqID,
		"channel_id", event.ChannelID,
		"thread_ts", event.ThreadTS,
		"user_id", event.UserID,
	)

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
	pt.RequestID = reqID
	pt.Logger = logger

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
			w.notifyError(pt.Logger, event.ChannelID, pt.ThreadTS, "Failed to show repo selector: %v", err)
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
		w.notifyError(pt.Logger, event.ChannelID, pt.ThreadTS, "Failed to show repo search: %v", err)
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
		w.notifyError(pt.Logger, pt.ChannelID, pt.ThreadTS, "Failed to access repo %s: %v", pt.SelectedRepo, err)
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

	w.slack.PostMessage(pt.ChannelID, ":mag: 正在排入處理佇列...", pt.ThreadTS)

	// 1. Read thread context.
	botUserID := ""
	rawMsgs, err := w.slack.FetchThreadContext(pt.ChannelID, pt.ThreadTS, pt.TriggerTS, botUserID, w.cfg.MaxThreadMessages)
	if err != nil {
		w.notifyError(pt.Logger, pt.ChannelID, pt.ThreadTS, "Failed to read thread: %v", err)
		w.clearDedup(pt)
		return
	}
	pt.Logger.Info("thread context read", "messages", len(rawMsgs), "repo", pt.SelectedRepo)

	// 2. Enrich messages.
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

	// 3. Collect attachment metadata.
	tempDir, err := os.MkdirTemp("", "triage-meta-*")
	if err != nil {
		w.notifyError(pt.Logger, pt.ChannelID, pt.ThreadTS, "Failed to create temp dir: %v", err)
		w.clearDedup(pt)
		return
	}
	defer os.RemoveAll(tempDir)

	downloads := w.slack.DownloadAttachments(rawMsgs, tempDir)
	var attachmentInfos []AttachmentInfo
	for _, d := range downloads {
		if d.Failed {
			continue
		}
		attachmentInfos = append(attachmentInfos, AttachmentInfo{Path: d.Path, Name: d.Name, Type: d.Type})
	}

	// 4. Build prompt.
	prompt := BuildPrompt(PromptInput{
		ThreadMessages:   threadMsgs,
		Attachments:      attachmentInfos,
		ExtraDescription: pt.ExtraDesc,
		Branch:           pt.SelectedBranch,
		Channel:          pt.ChannelName,
		Reporter:         pt.Reporter,
		Prompt:           w.cfg.Prompt,
	})
	pt.Logger.Info("prompt built", "length", len(prompt))

	// 5. Build attachment metadata for queue.
	var attachMeta []queue.AttachmentMeta
	for _, d := range downloads {
		if d.Failed {
			continue
		}
		attachMeta = append(attachMeta, queue.AttachmentMeta{
			Filename: d.Name,
			MimeType: d.Type,
		})
	}

	// 6. Submit to queue.
	job := &queue.Job{
		ID:          pt.RequestID,
		Priority:    w.channelPriority(pt.ChannelID),
		ChannelID:   pt.ChannelID,
		ThreadTS:    pt.ThreadTS,
		Repo:        pt.SelectedRepo,
		Branch:      pt.SelectedBranch,
		CloneURL:    w.repoCache.ResolveURL(pt.SelectedRepo),
		Prompt:      prompt,
		Skills:      w.skills,
		RequestID:   pt.RequestID,
		Attachments: attachMeta,
		SubmittedAt: time.Now(),
	}

	if err := w.queue.Submit(ctx, job); err != nil {
		if err == queue.ErrQueueFull {
			w.slack.PostMessage(pt.ChannelID, ":warning: 系統忙碌，請稍後再試", pt.ThreadTS)
		} else {
			w.notifyError(pt.Logger, pt.ChannelID, pt.ThreadTS, "Failed to submit job: %v", err)
		}
		w.clearDedup(pt)
		return
	}

	// Signal attachment readiness so workers can proceed.
	if len(attachMeta) > 0 {
		w.attachments.Prepare(ctx, job.ID, attachMeta)
	}

	pos, _ := w.queue.QueuePosition(job.ID)
	var statusMsg string
	if pos <= 1 {
		statusMsg = ":hourglass_flowing_sand: 正在處理你的請求..."
	} else {
		statusMsg = fmt.Sprintf(":hourglass_flowing_sand: 已加入排隊，前面有 %d 個請求", pos-1)
	}
	if msgTS, err := w.slack.PostMessageWithButton(pt.ChannelID,
		statusMsg, pt.ThreadTS, "cancel_job", "取消", job.ID); err == nil {
		job.StatusMsgTS = msgTS
		w.store.Put(job) // update with StatusMsgTS
	}
	// Don't clearDedup here — ResultListener handles cleanup after job completes.
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

func (w *Workflow) notifyError(logger *slog.Logger, channelID, threadTS string, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	logger.Error("workflow error", "message", msg)
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
