package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Ivantseng123/agentdock/app/workflow"
	"github.com/Ivantseng123/agentdock/shared/metrics"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

// SlackPoster abstracts Slack message posting for testing.
type SlackPoster interface {
	PostMessage(channelID, text, threadTS string)
	UpdateMessage(channelID, messageTS, text string)
	PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value string) (string, error)
}

type ResultListener struct {
	results      queue.ResultBus
	store        queue.JobStore
	attachments  queue.AttachmentStore
	slack        SlackPoster
	registry     *workflow.Registry
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
	registry *workflow.Registry,
	onDedupClear func(channelID, threadTS string),
	logger *slog.Logger,
) *ResultListener {
	return &ResultListener{
		results:       results,
		store:         store,
		attachments:   attachments,
		slack:         slack,
		registry:      registry,
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

	state, err := r.store.Get(ctx, result.JobID)
	if err != nil {
		r.logger.Error("找不到工作結果對應的工作", "phase", "失敗", "job_id", result.JobID, "error", err)
		return
	}

	r.recordMetrics(state, result)

	job := state.Job

	// Design A: user cancellation dominates, regardless of result.Status.
	if state.Status == queue.JobCancelled || result.Status == "cancelled" {
		r.handleCancellation(ctx, state.Job, state, result)
		r.attachments.Cleanup(ctx, result.JobID)
		return
	}

	// Failed path: listener owns retry-button logic and store transition.
	if result.Status == "failed" {
		r.handleFailure(ctx, job, state, result)
		r.attachments.Cleanup(ctx, result.JobID)
		return
	}

	// Completed path: delegate entirely to the workflow handler.
	// The workflow owns parsing (REJECTED/ERROR/CREATED), Slack posting, and
	// GitHub side-effects. The listener keeps store-status transitions,
	// dedup-clearing, and attachment cleanup.
	if r.registry != nil {
		wf, ok := r.registry.Get(job.TaskType)
		if !ok {
			r.logger.Error("unknown task_type", "phase", "失敗", "job_id", result.JobID, "task_type", job.TaskType)
			r.slack.PostMessage(job.ChannelID,
				fmt.Sprintf(":x: 未知的工作類型 `%s`", job.TaskType),
				job.ThreadTS)
			r.clearDedup(job)
			r.attachments.Cleanup(ctx, result.JobID)
			return
		}
		if err := wf.HandleResult(ctx, state, result); err != nil {
			r.logger.Error("工作完成處理失敗", "phase", "失敗", "job_id", result.JobID, "error", err)
			// Treat as failure — no retry button on internal errors.
			r.store.UpdateStatus(ctx, job.ID, queue.JobFailed)
			r.clearDedup(job)
			r.attachments.Cleanup(ctx, result.JobID)
			return
		}
	}

	// Store status transition: workflow may have mutated result.Status to
	// "failed" (e.g. ERROR or parse-fail paths inside HandleResult).
	if result.Status == "failed" {
		r.store.UpdateStatus(ctx, job.ID, queue.JobFailed)
		// Failed path inside completed: no dedup clear (retry button was posted
		// by the workflow's handleFailure — keep dedup locked).
		r.attachments.Cleanup(ctx, result.JobID)
		return
	}

	r.store.UpdateStatus(ctx, job.ID, queue.JobCompleted)
	r.clearDedup(job)

	// Cleanup attachments.
	r.attachments.Cleanup(ctx, result.JobID)
}

func (r *ResultListener) recordMetrics(state *queue.JobState, result *queue.JobResult) {
	job := state.Job

	// End-to-end duration (app clock only — avoids clock skew with remote workers).
	if !job.SubmittedAt.IsZero() {
		elapsed := time.Since(job.SubmittedAt).Seconds()
		metrics.RequestDuration.Observe(elapsed)
		metrics.QueueJobDuration.WithLabelValues(workflowLabel(job), result.Status).Observe(elapsed)
	}

	// Queue wait time (computed by MemJobStore when status transitions to Running).
	if state.WaitTime > 0 {
		metrics.QueueWait.Observe(state.WaitTime.Seconds())
	}

	// Ref-write violations: worker is task-agnostic and reports any ref
	// worktree dirty bits via JobResult.RefViolations. App side is where the
	// metric lives — Ask path is lenient (count, do not block); Issue path
	// fail-fasts at createAndPostIssue s1 in addition to this metric.
	for _, repo := range result.RefViolations {
		metrics.RefWriteViolationsTotal.WithLabelValues(workflowLabel(job), repo).Inc()
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
		metrics.AgentExecutionsTotal.WithLabelValues(provider, workflowLabel(job), status).Inc()

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
		metrics.AgentExecutionsTotal.WithLabelValues("unknown", workflowLabel(job), "error").Inc()
	} else if result.Status == "cancelled" {
		metrics.AgentExecutionsTotal.WithLabelValues("unknown", workflowLabel(job), "cancelled").Inc()
	}
}

func (r *ResultListener) handleFailure(ctx context.Context, job *queue.Job, state *queue.JobState, result *queue.JobResult) {
	r.store.UpdateStatus(ctx, job.ID, queue.JobFailed)

	// Pre-workflow failure path. Worker-label derivation is duplicated in
	// app/workflow/issue.go:workerLabel — keep the two in sync until a future
	// refactor exports one version (likely moved to shared/queue).
	workerID := ""
	workerNickname := ""
	if state.AgentStatus != nil {
		workerID = state.AgentStatus.WorkerID
		workerNickname = state.AgentStatus.WorkerNickname
	}
	if workerID == "" {
		workerID = state.WorkerID
	}

	label := workerNickname
	if label == "" {
		label = workerID
	} else if workerID != "" {
		label = fmt.Sprintf("%s (%s)", workerNickname, workerID)
	}

	workerInfo := ""
	if label != "" {
		workerInfo = fmt.Sprintf(" | worker: %s", label)
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
		metrics.WorkflowRetryTotal.WithLabelValues(workflowLabel(job), "exhausted").Inc()
		text := fmt.Sprintf(":x: 分析失敗（重試後仍失敗）: %s\nrepo: `%s` | job: `%s`%s", errMsg, job.Repo, job.ID, workerInfo)
		r.updateStatus(job, text)
		r.clearDedup(job)
	}
}

func (r *ResultListener) handleCancellation(ctx context.Context, job *queue.Job, state *queue.JobState, result *queue.JobResult) {
	r.store.UpdateStatus(ctx, job.ID, queue.JobCancelled)
	r.updateStatus(job, ":white_check_mark: 已取消")
	r.clearDedup(job)
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

// workflowLabel returns the job's TaskType for use as a metric label, falling
// back to "unknown" for empty strings (e.g. older jobs or test fixtures).
func workflowLabel(job *queue.Job) string {
	if job.TaskType == "" {
		return "unknown"
	}
	return job.TaskType
}
