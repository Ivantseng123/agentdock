package bot

import (
	"context"
	"log/slog"
	"time"

	"agentdock/internal/logging"
	"agentdock/internal/metrics"
	"agentdock/internal/queue"
)

// JobSubmitter abstracts queue submission for testing.
type JobSubmitter interface {
	Submit(ctx context.Context, job *queue.Job) error
}

type RetryHandler struct {
	store  queue.JobStore
	queue  JobSubmitter
	slack  SlackPoster
	logger *slog.Logger
}

func NewRetryHandler(store queue.JobStore, q JobSubmitter, slack SlackPoster, logger *slog.Logger) *RetryHandler {
	return &RetryHandler{store: store, queue: q, slack: slack, logger: logger}
}

func (h *RetryHandler) Handle(channelID, jobID, msgTS string) {
	state, err := h.store.Get(jobID)
	if err != nil {
		h.logger.Warn("重試：找不到工作", "phase", "重試", "job_id", jobID, "error", err)
		h.slack.UpdateMessage(channelID, msgTS, ":warning: 此任務已過期，請重新觸發")
		return
	}

	if state.Status != queue.JobFailed {
		h.logger.Info("重試：工作非失敗狀態，忽略", "phase", "重試", "job_id", jobID, "status", state.Status)
		return
	}

	original := state.Job

	// Update old message to indicate retry is queued.
	h.slack.UpdateMessage(channelID, msgTS, ":arrows_counterclockwise: 已重新排入佇列")

	// Create new job copying relevant fields.
	newJob := &queue.Job{
		ID:               logging.NewRequestID(),
		Priority:         original.Priority,
		ChannelID:        original.ChannelID,
		ThreadTS:         original.ThreadTS,
		UserID:           original.UserID,
		Repo:             original.Repo,
		Branch:           original.Branch,
		CloneURL:         original.CloneURL,
		PromptContext:    original.PromptContext,
		Skills:           original.Skills,
		RequestID:        logging.NewRequestID(),
		Attachments:      original.Attachments,
		RetryCount:       original.RetryCount + 1,
		RetryOfJobID:     original.ID,
		SubmittedAt:      time.Now(),
		EncryptedSecrets: original.EncryptedSecrets,
	}

	// Put in store before posting button (so cancel_job can find it).
	h.store.Put(newJob)

	ctx := context.Background()
	if err := h.queue.Submit(ctx, newJob); err != nil {
		h.logger.Error("重試：提交失敗", "phase", "重試", "job_id", newJob.ID, "error", err)
		h.slack.PostMessage(channelID, ":x: 重試失敗: "+err.Error(), original.ThreadTS)
		return
	}
	metrics.IssueRetryTotal.WithLabelValues("submitted").Inc()

	// Post new status message with cancel button.
	statusMsgTS, err := h.slack.PostMessageWithButton(original.ChannelID,
		":hourglass_flowing_sand: 重試中，正在處理你的請求...",
		original.ThreadTS, "cancel_job", "取消", newJob.ID)
	if err == nil {
		newJob.StatusMsgTS = statusMsgTS
		h.store.Put(newJob) // update with StatusMsgTS
	}

	h.logger.Info("重試工作已提交", "phase", "重試",
		"original_job_id", original.ID,
		"new_job_id", newJob.ID,
		"retry_count", newJob.RetryCount)
}
