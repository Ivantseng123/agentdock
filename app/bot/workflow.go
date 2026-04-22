package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Ivantseng123/agentdock/app/config"
	"github.com/Ivantseng123/agentdock/shared/crypto"
	ghclient "github.com/Ivantseng123/agentdock/shared/github"
	"github.com/Ivantseng123/agentdock/shared/logging"
	"github.com/Ivantseng123/agentdock/shared/queue"
	slackclient "github.com/Ivantseng123/agentdock/app/slack"
)

const pendingTimeout = 1 * time.Minute

// slackAPI is the narrow Slack surface used by Workflow. *slackclient.Client
// satisfies it; tests implement it with a stub.
type slackAPI interface {
	PostMessage(channelID, text, threadTS string) error
	PostMessageWithTS(channelID, text, threadTS string) (string, error)
	PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value string) (string, error)
	UpdateMessageWithButton(channelID, messageTS, text, actionID, buttonText, value string) error
	PostSelector(channelID, prompt, actionPrefix string, options []string, threadTS string) (string, error)
	PostSelectorWithBack(channelID, prompt, actionPrefix string, options []string, threadTS, backActionID, backLabel string) (string, error)
	PostExternalSelector(channelID, prompt, actionID, placeholder, threadTS string) (string, error)
	UpdateMessage(channelID, messageTS, text string) error
	OpenDescriptionModal(triggerID, selectorMsgTS string) error
	ResolveUser(userID string) string
	GetChannelName(channelID string) string
	FetchThreadContext(channelID, threadTS, triggerTS, botUserID, botID string, limit int) ([]slackclient.ThreadRawMessage, error)
	DownloadAttachments(messages []slackclient.ThreadRawMessage, tempDir string) []slackclient.AttachmentDownload
}

type pendingTriage struct {
	ChannelID      string
	ThreadTS       string
	TriggerTS      string
	UserID         string
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
	RepoWasPicked  bool
}

type Workflow struct {
	cfg           *config.Config
	slack         slackAPI // was *slackclient.Client
	handler       *slackclient.Handler
	repoCache     *ghclient.RepoCache
	repoDiscovery *ghclient.RepoDiscovery
	queue         queue.JobQueue
	store         queue.JobStore
	attachments   queue.AttachmentStore
	results       queue.ResultBus
	skillProvider SkillProvider
	secretKey     []byte // decoded AES key, nil if not configured
	identity      Identity

	mu        sync.Mutex
	pending   map[string]*pendingTriage
	autoBound map[string]bool
}

func NewWorkflow(
	cfg *config.Config,
	slack slackAPI, // was *slackclient.Client
	repoCache *ghclient.RepoCache,
	repoDiscovery *ghclient.RepoDiscovery,
	jobQueue queue.JobQueue,
	jobStore queue.JobStore,
	attachStore queue.AttachmentStore,
	resultBus queue.ResultBus,
	skillProvider SkillProvider,
	identity Identity,
) *Workflow {
	// Decode secret key once at startup (nil if not configured).
	var sk []byte
	if cfg.SecretKey != "" {
		var err error
		sk, err = crypto.DecodeSecretKey(cfg.SecretKey)
		if err != nil {
			slog.Error("secret_key 無效，secret 加密功能停用", "phase", "失敗", "error", err)
		}
	}
	return &Workflow{
		cfg:           cfg,
		slack:         slack,
		repoCache:     repoCache,
		repoDiscovery: repoDiscovery,
		queue:         jobQueue,
		store:         jobStore,
		attachments:   attachStore,
		results:       resultBus,
		skillProvider: skillProvider,
		secretKey:     sk,
		identity:      identity,
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
		UserID:      event.UserID,
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

	selectorTS, err := w.postRepoSelector(pt, channelCfg)
	if err != nil {
		w.notifyError(pt.Logger, event.ChannelID, pt.ThreadTS, "Failed to show repo selector: %v", err)
		return
	}
	pt.SelectorTS = selectorTS
	w.storePending(selectorTS, pt)
}

// postRepoSelector posts either the multi-repo button selector (len>1) or the
// external searchable selector (len==0). The len==1 auto-select case is
// handled by callers inline — see HandleTrigger and HandleBackToRepo.
func (w *Workflow) postRepoSelector(pt *pendingTriage, channelCfg config.ChannelConfig) (string, error) {
	repos := channelCfg.GetRepos()
	if len(repos) > 1 {
		pt.Phase = "repo"
		return w.slack.PostSelector(pt.ChannelID,
			":point_right: Which repo should this issue go to?",
			"repo_select", repos, pt.ThreadTS)
	}
	pt.Phase = "repo_search"
	return w.slack.PostExternalSelector(pt.ChannelID,
		":point_right: Search and select a repo:",
		"repo_search", "Type to search repos...", pt.ThreadTS)
}

func (w *Workflow) HandleRepoSuggestion(query string) []string {
	repos, err := w.repoDiscovery.SearchRepos(context.Background(), query)
	if err != nil {
		slog.Warn("Repo 搜尋失敗", "phase", "失敗", "error", err)
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
		pt.RepoWasPicked = true
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

	var branches []string
	if len(channelCfg.Branches) > 0 {
		branches = channelCfg.Branches
	} else {
		repoPath, err := w.repoCache.EnsureRepo(pt.SelectedRepo, w.cfg.Secrets["GH_TOKEN"])
		if err != nil {
			w.notifyError(pt.Logger, pt.ChannelID, pt.ThreadTS, "Failed to access repo %s: %v", pt.SelectedRepo, err)
			return
		}
		var listErr error
		branches, listErr = w.repoCache.ListBranches(repoPath)
		if listErr != nil {
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
	backAction := ""
	if pt.RepoWasPicked {
		backAction = "back_to_repo"
	}
	selectorTS, err := w.slack.PostSelectorWithBack(pt.ChannelID,
		fmt.Sprintf(":point_right: Which branch of `%s`?", pt.SelectedRepo),
		"branch_select", branches, pt.ThreadTS,
		backAction, "← 重新選 repo")
	if err != nil {
		w.showDescriptionPrompt(pt)
		return
	}
	pt.SelectorTS = selectorTS
	w.storePending(selectorTS, pt)
}

func (w *Workflow) showDescriptionPrompt(pt *pendingTriage) {
	pt.Phase = "description"
	backAction := ""
	if pt.RepoWasPicked {
		backAction = "back_to_repo"
	}
	selectorTS, err := w.slack.PostSelectorWithBack(pt.ChannelID,
		":memo: 需要補充說明嗎？（補充後可讓分析更精準）",
		"description_action", []string{"補充說明", "跳過"}, pt.ThreadTS,
		backAction, "← 重新選 repo")
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

	// Post the lifecycle status message once; later stages edit it in place
	// instead of posting new messages. If posting fails we fall back to a
	// second post after submit (rare — Slack outage etc.).
	statusMsgTS, err := w.slack.PostMessageWithTS(pt.ChannelID, ":mag: 正在排入處理佇列...", pt.ThreadTS)
	if err != nil {
		pt.Logger.Warn("狀態訊息發送失敗，後續改為另起新訊息", "phase", "失敗", "error", err)
		statusMsgTS = ""
	}

	// 1. Read thread context.
	rawMsgs, err := w.slack.FetchThreadContext(
		pt.ChannelID, pt.ThreadTS, pt.TriggerTS,
		w.identity.UserID, w.identity.BotID,
		w.cfg.MaxThreadMessages,
	)
	if err != nil {
		w.notifyError(pt.Logger, pt.ChannelID, pt.ThreadTS, "Failed to read thread: %v", err)
		w.clearDedup(pt)
		return
	}
	pt.Logger.Info("訊息串已讀取", "phase", "處理中", "messages", len(rawMsgs), "repo", pt.SelectedRepo)

	// 2. Shape messages for the queue. (Mantis enrichment is now the
	// agent's job via the mantis skill + env vars.)
	var threadMsgs []queue.ThreadMessage
	for _, m := range rawMsgs {
		threadMsgs = append(threadMsgs, queue.ThreadMessage{
			User:      w.slack.ResolveUser(m.User),
			Timestamp: m.Timestamp,
			Text:      m.Text,
		})
	}

	if len(threadMsgs) == 0 {
		w.notifyError(pt.Logger, pt.ChannelID, pt.ThreadTS, "Thread has no messages to process")
		w.clearDedup(pt)
		return
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

	// 4. Assemble structured prompt context (worker renders the actual prompt).
	promptCtx := AssemblePromptContext(
		threadMsgs,
		pt.ExtraDesc,
		pt.ChannelName,
		pt.Reporter,
		pt.SelectedBranch,
		w.cfg.Prompt,
	)
	pt.Logger.Info("Prompt context 已組裝", "phase", "處理中",
		"thread_messages", len(promptCtx.ThreadMessages),
		"has_extra_desc", promptCtx.ExtraDescription != "",
	)
	pt.Logger.Debug("Prompt context 詳細內容", "phase", "處理中", "prompt_context", promptCtx)

	// 5. Build attachment metadata and payloads for queue.
	var attachMeta []queue.AttachmentMeta
	var attachPayloads []queue.AttachmentPayload
	for _, d := range downloads {
		if d.Failed {
			continue
		}
		attachMeta = append(attachMeta, queue.AttachmentMeta{
			Filename: d.Name,
			MimeType: d.Type,
		})
		data, err := os.ReadFile(d.Path)
		if err != nil {
			pt.Logger.Warn("Failed to read attachment for queue", "name", d.Name, "error", err)
			continue
		}
		attachPayloads = append(attachPayloads, queue.AttachmentPayload{
			Filename: d.Name,
			MimeType: d.Type,
			Data:     data,
			Size:     int64(len(data)),
		})
	}

	// 6. Submit to queue.
	job := &queue.Job{
		ID:            pt.RequestID,
		Priority:      w.channelPriority(pt.ChannelID),
		ChannelID:     pt.ChannelID,
		ThreadTS:      pt.ThreadTS,
		UserID:        pt.UserID,
		Repo:          pt.SelectedRepo,
		Branch:        pt.SelectedBranch,
		CloneURL:      cleanCloneURL(pt.SelectedRepo),
		PromptContext: &promptCtx,
		Skills:        w.loadSkills(ctx),
		RequestID:     pt.RequestID,
		Attachments:   attachMeta,
		SubmittedAt:   time.Now(),
	}

	if len(w.secretKey) > 0 && len(w.cfg.Secrets) > 0 {
		secretsJSON, err := json.Marshal(w.cfg.Secrets)
		if err != nil {
			w.notifyError(pt.Logger, pt.ChannelID, pt.ThreadTS, "Failed to marshal secrets: %v", err)
			w.clearDedup(pt)
			return
		}
		encrypted, err := crypto.Encrypt(w.secretKey, secretsJSON)
		if err != nil {
			w.notifyError(pt.Logger, pt.ChannelID, pt.ThreadTS, "Failed to encrypt secrets: %v", err)
			w.clearDedup(pt)
			return
		}
		job.EncryptedSecrets = encrypted
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
	if len(attachPayloads) > 0 {
		if err := w.attachments.Prepare(ctx, job.ID, attachPayloads); err != nil {
			pt.Logger.Error("附件上傳至 Redis 失敗", "phase", "失敗", "error", err)
			w.store.UpdateStatus(job.ID, queue.JobFailed)
			w.results.Publish(ctx, &queue.JobResult{
				JobID:      job.ID,
				Status:     "failed",
				Error:      fmt.Sprintf("attachment prepare failed: %v", err),
				StartedAt:  time.Now(),
				FinishedAt: time.Now(),
			})
			return
		}
	}

	pos, _ := w.queue.QueuePosition(job.ID)
	var statusMsg string
	if pos <= 1 {
		statusMsg = ":hourglass_flowing_sand: 正在處理你的請求..."
	} else {
		statusMsg = fmt.Sprintf(":hourglass_flowing_sand: 已加入排隊，前面有 %d 個請求", pos-1)
	}

	// Prefer editing the earlier ":mag: 排入..." message so the thread has a
	// single lifecycle message (排入 → 處理中 → 已建立 issue). Fall back to a
	// fresh post if the earlier one failed.
	if statusMsgTS != "" {
		if err := w.slack.UpdateMessageWithButton(pt.ChannelID, statusMsgTS,
			statusMsg, "cancel_job", "取消", job.ID); err != nil {
			pt.Logger.Warn("狀態訊息更新失敗，改為另起新訊息", "phase", "失敗", "error", err)
			statusMsgTS = ""
		}
	}
	if statusMsgTS == "" {
		if msgTS, err := w.slack.PostMessageWithButton(pt.ChannelID,
			statusMsg, pt.ThreadTS, "cancel_job", "取消", job.ID); err == nil {
			statusMsgTS = msgTS
		}
	}
	if statusMsgTS != "" {
		job.StatusMsgTS = statusMsgTS
		w.store.Put(job) // update with StatusMsgTS
	}
	// Don't clearDedup here — ResultListener handles cleanup after job completes.
}

// HandleBackToRepo handles a "← 重新選 repo" button click. Invoked from
// cmd/agentdock/app.go when action.ActionID == "back_to_repo".
func (w *Workflow) HandleBackToRepo(channelID, selectorMsgTS string) {
	w.mu.Lock()
	pt, ok := w.pending[selectorMsgTS]
	if ok {
		delete(w.pending, selectorMsgTS)
	}
	w.mu.Unlock()
	if !ok {
		return
	}

	if pt.Logger != nil {
		pt.Logger.Info("收到返回 repo 請求",
			"phase", "接收", "from_selector_ts", selectorMsgTS)
	}

	channelCfg := w.cfg.ChannelDefaults
	if cc, ok := w.cfg.Channels[pt.ChannelID]; ok {
		channelCfg = cc
	}

	// Clear carried-over fields.
	pt.SelectedRepo = ""
	pt.SelectedBranch = ""
	pt.ExtraDesc = ""

	// Rare case: channel config reloaded and now has exactly one repo — auto-select
	// and go directly to the branch step (mirrors HandleTrigger's shortcut).
	repos := channelCfg.GetRepos()
	if len(repos) == 1 {
		pt.SelectedRepo = repos[0]
		w.slack.UpdateMessage(channelID, selectorMsgTS,
			":leftwards_arrow_with_hook: 已返回 repo 選擇")
		w.afterRepoSelected(pt, channelCfg)
		return
	}

	// Multi-repo or external-search case.
	newSelectorTS, err := w.postRepoSelector(pt, channelCfg)
	if err != nil {
		w.notifyError(pt.Logger, channelID, pt.ThreadTS,
			"重選 repo 失敗: %v", err)
		w.clearDedup(pt)
		return
	}

	w.slack.UpdateMessage(channelID, selectorMsgTS,
		":leftwards_arrow_with_hook: 已返回 repo 選擇")

	pt.SelectorTS = newSelectorTS
	w.storePending(newSelectorTS, pt)

	if pt.Logger != nil {
		pt.Logger.Info("已重新顯示 repo 選擇",
			"phase", "處理中", "new_selector_ts", newSelectorTS)
	}
}

func cleanCloneURL(repoRef string) string {
	if strings.HasPrefix(repoRef, "http") || strings.HasPrefix(repoRef, "git@") || strings.HasPrefix(repoRef, "file://") {
		return repoRef
	}
	return fmt.Sprintf("https://github.com/%s.git", repoRef)
}

func (w *Workflow) loadSkills(ctx context.Context) map[string]*queue.SkillPayload {
	if w.skillProvider == nil {
		return nil
	}
	skills, err := w.skillProvider.LoadAll(ctx)
	if err != nil {
		slog.Warn("載入 skill 失敗", "phase", "失敗", "error", err)
		return nil
	}
	return skills
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
