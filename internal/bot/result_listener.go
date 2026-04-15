package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"agentdock/internal/queue"
)

// SlackPoster abstracts Slack message posting for testing.
type SlackPoster interface {
	PostMessage(channelID, text, threadTS string)
	UpdateMessage(channelID, messageTS, text string)
	PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value string) (string, error)
}

// IssueCreator abstracts GitHub issue creation for testing.
type IssueCreator interface {
	CreateIssue(ctx context.Context, owner, repo, title, body string, labels []string) (string, error)
}

type ResultListener struct {
	results       queue.ResultBus
	store         queue.JobStore
	attachments   queue.AttachmentStore
	slack         SlackPoster
	github        IssueCreator
	onDedupClear  func(channelID, threadTS string)

	mu            sync.Mutex
	processedJobs map[string]bool
}

func NewResultListener(
	results queue.ResultBus,
	store queue.JobStore,
	attachments queue.AttachmentStore,
	slack SlackPoster,
	github IssueCreator,
	onDedupClear func(channelID, threadTS string),
) *ResultListener {
	return &ResultListener{
		results:       results,
		store:         store,
		attachments:   attachments,
		slack:         slack,
		github:        github,
		onDedupClear:  onDedupClear,
		processedJobs: make(map[string]bool),
	}
}

func (r *ResultListener) Listen(ctx context.Context) {
	ch, err := r.results.Subscribe(ctx)
	if err != nil {
		slog.Error("failed to subscribe to results", "error", err)
		return
	}

	for {
		select {
		case result, ok := <-ch:
			if !ok {
				return
			}
			r.handleResult(ctx, result)
		case <-ctx.Done():
			return
		}
	}
}

func (r *ResultListener) handleResult(ctx context.Context, result *queue.JobResult) {
	// Dedup guard: drop duplicate results for same job.
	r.mu.Lock()
	if r.processedJobs[result.JobID] {
		r.mu.Unlock()
		slog.Debug("dropping duplicate result", "job_id", result.JobID)
		return
	}
	r.processedJobs[result.JobID] = true
	r.mu.Unlock()

	state, err := r.store.Get(result.JobID)
	if err != nil {
		slog.Error("job not found for result", "job_id", result.JobID, "error", err)
		return
	}

	job := state.Job
	owner, repo := splitRepo(job.Repo)

	logger := slog.With("job_id", result.JobID, "repo", job.Repo, "status", result.Status)
	if result.Status == "failed" {
		truncated := result.RawOutput
		if len(truncated) > 2000 {
			truncated = truncated[:2000] + "…(truncated)"
		}
		logger.Warn("job failed", "error", result.Error, "raw_output", truncated)
	} else {
		logger.Info("job completed", "title", result.Title, "confidence", result.Confidence, "files_found", result.FilesFound)
	}

	switch {
	case result.Status == "failed":
		r.handleFailure(job, state, result)

	case result.Confidence == "low":
		r.updateStatus(job, ":warning: 判斷不屬於此 repo，已跳過")
		r.clearDedup(job)

	case result.FilesFound == 0 || result.Questions >= 5:
		r.createAndPostIssue(ctx, job, owner, repo, result, true)
		r.clearDedup(job)

	default:
		r.createAndPostIssue(ctx, job, owner, repo, result, false)
		r.clearDedup(job)
	}

	// Cleanup attachments.
	r.attachments.Cleanup(ctx, result.JobID)
}

func (r *ResultListener) handleFailure(job *queue.Job, state *queue.JobState, result *queue.JobResult) {
	r.store.UpdateStatus(job.ID, queue.JobFailed)

	workerID := ""
	if state.AgentStatus != nil {
		workerID = state.AgentStatus.WorkerID
	}
	if workerID == "" {
		workerID = state.WorkerID
	}

	workerInfo := ""
	if workerID != "" {
		workerInfo = fmt.Sprintf(" | worker: %s", workerID)
	}

	// Extract short error reason for Slack (before first colon detail, max 80 chars).
	errMsg := result.Error
	if idx := strings.Index(errMsg, ":"); idx > 0 {
		errMsg = errMsg[:idx]
	}
	if len(errMsg) > 80 {
		errMsg = errMsg[:80] + "…"
	}

	if job.RetryCount < 1 {
		// Show retry button.
		text := fmt.Sprintf(":x: 分析失敗: %s\nrepo: `%s` | job: `%s`%s", errMsg, job.Repo, job.ID, workerInfo)
		r.slack.PostMessageWithButton(job.ChannelID, text, job.ThreadTS,
			"retry_job", "🔄 重試", job.ID)
		// Do NOT clear dedup — user should use retry button.
	} else {
		// Retry exhausted, no button.
		text := fmt.Sprintf(":x: 分析失敗（重試後仍失敗）: %s\nrepo: `%s` | job: `%s`%s", errMsg, job.Repo, job.ID, workerInfo)
		r.updateStatus(job, text)
		r.clearDedup(job)
	}
}

func (r *ResultListener) createAndPostIssue(ctx context.Context, job *queue.Job, owner, repo string, result *queue.JobResult, degraded bool) {
	if r.github == nil {
		r.slack.PostMessage(job.ChannelID,
			":warning: GitHub client not configured", job.ThreadTS)
		return
	}

	body := result.Body
	if degraded {
		body = stripTriageSection(body)
	}

	branchInfo := ""
	if job.Branch != "" {
		branchInfo = fmt.Sprintf(" (branch: `%s`)", job.Branch)
	}

	url, err := r.github.CreateIssue(ctx, owner, repo, result.Title, body, result.Labels)
	if err != nil {
		r.updateStatus(job, fmt.Sprintf(":warning: Triage 完成但建立 issue 失敗: %v", err))
		return
	}

	r.updateStatus(job, fmt.Sprintf(":white_check_mark: Issue created%s: %s", branchInfo, url))
}

func (r *ResultListener) updateStatus(job *queue.Job, text string) {
	if job.StatusMsgTS != "" {
		r.slack.UpdateMessage(job.ChannelID, job.StatusMsgTS, text)
	} else {
		r.slack.PostMessage(job.ChannelID, text, job.ThreadTS)
	}
}

func (r *ResultListener) clearDedup(job *queue.Job) {
	if r.onDedupClear != nil {
		r.onDedupClear(job.ChannelID, job.ThreadTS)
	}
}

func splitRepo(repo string) (string, string) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		return repo, ""
	}
	return parts[0], parts[1]
}

func stripTriageSection(body string) string {
	for _, marker := range []string{"## Root Cause Analysis", "## TDD Fix Plan"} {
		if idx := strings.Index(body, marker); idx > 0 {
			body = strings.TrimSpace(body[:idx])
		}
	}
	return body
}
