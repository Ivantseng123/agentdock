package bot

import (
	"context"
	"log/slog"
	"time"

	"github.com/Ivantseng123/agentdock/app/config"
	"github.com/Ivantseng123/agentdock/app/dispatch"
	"github.com/Ivantseng123/agentdock/app/githubapp"
	"github.com/Ivantseng123/agentdock/shared/logging"
	"github.com/Ivantseng123/agentdock/shared/metrics"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

// JobSubmitter abstracts queue submission for testing.
type JobSubmitter interface {
	Submit(ctx context.Context, job *queue.Job) error
}

type RetryHandler struct {
	store     queue.JobStore
	queue     JobSubmitter
	slack     SlackPoster
	logger    *slog.Logger
	cfg       *config.Config
	source    githubapp.TokenSource
	secretKey []byte
}

// NewRetryHandler builds a RetryHandler. cfg, source, and secretKey
// drive per-retry MintFresh + encrypt so retry jobs don't reuse a
// stale 50min+ EncryptedSecrets bundle from the original. Pass nil
// source / empty secretKey to fall back to legacy behavior (reuse
// original.EncryptedSecrets) — useful for tests that don't exercise
// secrets.
func NewRetryHandler(store queue.JobStore, q JobSubmitter, slack SlackPoster, logger *slog.Logger, cfg *config.Config, source githubapp.TokenSource, secretKey []byte) *RetryHandler {
	return &RetryHandler{
		store:     store,
		queue:     q,
		slack:     slack,
		logger:    logger,
		cfg:       cfg,
		source:    source,
		secretKey: secretKey,
	}
}

func (h *RetryHandler) Handle(channelID, jobID, msgTS string) {
	// Retry is driven from a Slack interaction that doesn't plumb ctx today;
	// bound JobStore calls so a Redis hang cannot wedge the handler.
	ctx, cancel := context.WithTimeout(context.Background(), queue.DefaultStoreOpTimeout)
	defer cancel()
	state, err := h.store.Get(ctx, jobID)
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

	// Mint a fresh per-job secrets bundle so the retry doesn't ride on
	// a 50min+ stale token from the original. PAT mode reuses the
	// static token (byte-equivalent to copying original.EncryptedSecrets);
	// App mode pulls a fresh installation token close to full TTL.
	encryptedSecrets := original.EncryptedSecrets
	if h.source != nil && h.cfg != nil && len(h.secretKey) > 0 {
		fresh, encErr := dispatch.BuildEncryptedSecrets(h.cfg, h.source, h.secretKey)
		if encErr != nil {
			h.logger.Error("重試：secrets 重新加密失敗", "phase", "重試", "job_id", original.ID, "error", encErr)
			h.slack.PostMessage(channelID, ":x: 重試失敗: "+encErr.Error(), original.ThreadTS)
			return
		}
		encryptedSecrets = fresh
	}

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
		EncryptedSecrets: encryptedSecrets,
	}

	// Put in store before posting button (so cancel_job can find it).
	h.store.Put(ctx, newJob)

	if err := h.queue.Submit(ctx, newJob); err != nil {
		h.logger.Error("重試：提交失敗", "phase", "重試", "job_id", newJob.ID, "error", err)
		h.slack.PostMessage(channelID, ":x: 重試失敗: "+err.Error(), original.ThreadTS)
		return
	}
	metrics.WorkflowRetryTotal.WithLabelValues("issue", "attempted").Inc()

	// Post new status message with cancel button.
	statusMsgTS, err := h.slack.PostMessageWithButton(original.ChannelID,
		":hourglass_flowing_sand: 重試中，正在處理你的請求...",
		original.ThreadTS, "cancel_job", "取消", newJob.ID)
	if err == nil {
		newJob.StatusMsgTS = statusMsgTS
		h.store.Put(ctx, newJob) // update with StatusMsgTS
	}

	h.logger.Info("重試工作已提交", "phase", "重試",
		"original_job_id", original.ID,
		"new_job_id", newJob.ID,
		"retry_count", newJob.RetryCount)
}
