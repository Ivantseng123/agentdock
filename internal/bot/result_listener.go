package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Ivantseng123/agentdock/shared/metrics"
	"github.com/Ivantseng123/agentdock/shared/queue"
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
	results      queue.ResultBus
	store        queue.JobStore
	attachments  queue.AttachmentStore
	slack        SlackPoster
	github       IssueCreator
	onDedupClear func(channelID, threadTS string)
	logger       *slog.Logger

	mu                 sync.Mutex
	processedJobs      map[string]bool
	clearStatusMapping func(jobID string)
}

func NewResultListener(
	results queue.ResultBus,
	store queue.JobStore,
	attachments queue.AttachmentStore,
	slack SlackPoster,
	github IssueCreator,
	onDedupClear func(channelID, threadTS string),
	logger *slog.Logger,
) *ResultListener {
	return &ResultListener{
		results:       results,
		store:         store,
		attachments:   attachments,
		slack:         slack,
		github:        github,
		onDedupClear:  onDedupClear,
		logger:        logger,
		processedJobs: make(map[string]bool),
	}
}

func (r *ResultListener) Listen(ctx context.Context) {
	ch, err := r.results.Subscribe(ctx)
	if err != nil {
		r.logger.Error("訂閱 result bus 失敗", "phase", "失敗", "error", err)
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
		r.logger.Debug("重複結果已忽略", "phase", "處理中", "job_id", result.JobID)
		return
	}
	r.processedJobs[result.JobID] = true
	r.mu.Unlock()

	state, err := r.store.Get(result.JobID)
	if err != nil {
		r.logger.Error("找不到工作結果對應的工作", "phase", "失敗", "job_id", result.JobID, "error", err)
		return
	}

	r.recordMetrics(state, result)

	job := state.Job
	owner, repo := splitRepo(job.Repo)

	// Design A: user cancellation dominates, regardless of result.Status.
	if state.Status == queue.JobCancelled || result.Status == "cancelled" {
		r.handleCancellation(state.Job, state, result)
		r.attachments.Cleanup(ctx, result.JobID)
		return
	}

	logger := r.logger.With("job_id", result.JobID, "repo", job.Repo, "status", result.Status)
	switch result.Status {
	case "failed":
		truncated := result.RawOutput
		if len(truncated) > 2000 {
			truncated = truncated[:2000] + "…(truncated)"
		}
		logger.Warn("工作失敗", "phase", "降級", "error", result.Error, "raw_output", truncated)
	default:
		logger.Info("工作完成", "phase", "完成", "title", result.Title, "confidence", result.Confidence, "files_found", result.FilesFound)
	}

	switch {
	case result.Status == "failed":
		r.handleFailure(job, state, result)

	case result.Confidence == "low":
		metrics.IssueRejectedTotal.WithLabelValues("low_confidence").Inc()
		r.store.UpdateStatus(job.ID, queue.JobCompleted)
		text := ":warning: 判斷不屬於此 repo，已跳過"
		if result.Message != "" {
			text = text + "\n> " + result.Message
		}
		r.updateStatus(job, text)
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

func (r *ResultListener) recordMetrics(state *queue.JobState, result *queue.JobResult) {
	job := state.Job

	// End-to-end duration (app clock only — avoids clock skew with remote workers).
	if !job.SubmittedAt.IsZero() {
		elapsed := time.Since(job.SubmittedAt).Seconds()
		metrics.RequestDuration.Observe(elapsed)
		metrics.QueueJobDuration.WithLabelValues(result.Status).Observe(elapsed)
	}

	// Queue wait time (computed by MemJobStore when status transitions to Running).
	if state.WaitTime > 0 {
		metrics.QueueWait.Observe(state.WaitTime.Seconds())
	}

	// Agent metrics from StatusReport (relayed by StatusListener from worker's StatusBus).
	if as := state.AgentStatus; as != nil {
		provider := as.AgentCmd
		if provider == "" {
			provider = "unknown"
		}

		// Prepare duration.
		if as.PrepareSeconds > 0 {
			metrics.AgentPrepare.Observe(as.PrepareSeconds)
		}

		// Execution time ≈ total - wait - prepare.
		if !job.SubmittedAt.IsZero() {
			total := time.Since(job.SubmittedAt).Seconds()
			exec := total - state.WaitTime.Seconds() - as.PrepareSeconds
			if exec > 0 {
				metrics.AgentExecution.WithLabelValues(provider).Observe(exec)
			}
		}

		// Execution outcome.
		status := "success"
		switch result.Status {
		case "failed":
			if strings.Contains(result.Error, "timeout") {
				status = "timeout"
			} else {
				status = "error"
			}
		case "cancelled":
			status = "cancelled"
		}
		metrics.AgentExecutionsTotal.WithLabelValues(provider, status).Inc()

		// Tool calls and files read.
		if as.ToolCalls > 0 {
			metrics.AgentToolCalls.WithLabelValues(provider).Observe(float64(as.ToolCalls))
		}
		if as.FilesRead > 0 {
			metrics.AgentFilesRead.WithLabelValues(provider).Observe(float64(as.FilesRead))
		}

		// Cost and tokens.
		if as.CostUSD > 0 {
			metrics.AgentCostUSD.WithLabelValues(provider).Add(as.CostUSD)
		}
		if as.InputTokens > 0 {
			metrics.AgentTokensTotal.WithLabelValues(provider, "input").Add(float64(as.InputTokens))
		}
		if as.OutputTokens > 0 {
			metrics.AgentTokensTotal.WithLabelValues(provider, "output").Add(float64(as.OutputTokens))
		}
	} else if result.Status == "failed" {
		// No agent status — job failed before agent started.
		metrics.AgentExecutionsTotal.WithLabelValues("unknown", "error").Inc()
	} else if result.Status == "cancelled" {
		metrics.AgentExecutionsTotal.WithLabelValues("unknown", "cancelled").Inc()
	}
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
		metrics.IssueRetryTotal.WithLabelValues("exhausted").Inc()
		text := fmt.Sprintf(":x: 分析失敗（重試後仍失敗）: %s\nrepo: `%s` | job: `%s`%s", errMsg, job.Repo, job.ID, workerInfo)
		r.updateStatus(job, text)
		r.clearDedup(job)
	}
}

func (r *ResultListener) handleCancellation(job *queue.Job, state *queue.JobState, result *queue.JobResult) {
	r.store.UpdateStatus(job.ID, queue.JobCancelled)
	r.updateStatus(job, ":white_check_mark: 已取消")
	r.clearDedup(job)
}

func (r *ResultListener) createAndPostIssue(ctx context.Context, job *queue.Job, owner, repo string, result *queue.JobResult, degraded bool) {
	if r.github == nil {
		metrics.IssueRejectedTotal.WithLabelValues("no_github").Inc()
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
		r.store.UpdateStatus(job.ID, queue.JobFailed)
		r.updateStatus(job, fmt.Sprintf(":warning: Triage 完成但建立 issue 失敗: %v", err))
		return
	}

	confidence := result.Confidence
	if confidence == "" {
		confidence = "unknown"
	}
	metrics.IssueCreatedTotal.WithLabelValues(confidence, strconv.FormatBool(degraded)).Inc()

	r.store.UpdateStatus(job.ID, queue.JobCompleted)

	// Preserve worker diagnostics (worker id, elapsed, tool calls, files read,
	// cost) on the final message so the thread captures what the job actually
	// consumed. StatusReport only lives in the store while the job ran.
	line := fmt.Sprintf(":white_check_mark: Issue created%s: %s", branchInfo, url)
	if diag := r.formatDiagnostics(job, result); diag != "" {
		line = line + "\n" + diag
	}
	r.updateStatus(job, line)
}

// formatDiagnostics renders "worker-0 · 5m 12s · 工具呼叫 42 · 讀檔 18 · $0.23".
// Each segment is omitted when the underlying value is zero/empty so the line
// stays compact for short jobs. Returns "" when nothing useful is available.
func (r *ResultListener) formatDiagnostics(job *queue.Job, result *queue.JobResult) string {
	var parts []string
	var agent *queue.StatusReport
	if state, err := r.store.Get(job.ID); err == nil && state != nil {
		agent = state.AgentStatus
	}
	if agent != nil {
		if id := shortWorkerID(agent.WorkerID); id != "" {
			parts = append(parts, id)
		}
	}
	if elapsed := result.FinishedAt.Sub(result.StartedAt); elapsed > 0 {
		parts = append(parts, humanDuration(elapsed))
	}
	if agent != nil {
		if n := agent.ToolCalls; n > 0 {
			parts = append(parts, fmt.Sprintf("工具呼叫 %d", n))
		}
		if n := agent.FilesRead; n > 0 {
			parts = append(parts, fmt.Sprintf("讀檔 %d", n))
		}
	}
	if result.CostUSD > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f", result.CostUSD))
	}
	return strings.Join(parts, " · ")
}

func shortWorkerID(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}

func humanDuration(d time.Duration) string {
	s := int(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	m := s / 60
	s = s % 60
	if s == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dm %ds", m, s)
}

// SetStatusJobClearer installs a hook called after a result is fully handled,
// so the StatusListener can drop its debounce bookkeeping for that job.
func (r *ResultListener) SetStatusJobClearer(f func(jobID string)) {
	r.clearStatusMapping = f
}

func (r *ResultListener) updateStatus(job *queue.Job, text string) {
	if job.StatusMsgTS != "" {
		r.slack.UpdateMessage(job.ChannelID, job.StatusMsgTS, text)
		// Defensive re-write 2s later narrows the race with StatusListener's
		// in-flight update (spec §7). Same text is idempotent.
		ch, ts, finalText := job.ChannelID, job.StatusMsgTS, text
		time.AfterFunc(2*time.Second, func() {
			r.slack.UpdateMessage(ch, ts, finalText)
		})
		// Tell StatusListener to wipe its debounce bookkeeping for this job.
		if r.clearStatusMapping != nil {
			r.clearStatusMapping(job.ID)
		}
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
