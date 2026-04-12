package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"slack-issue-bot/internal/queue"
)

// SlackPoster abstracts Slack message posting for testing.
type SlackPoster interface {
	PostMessage(channelID, text, threadTS string)
	UpdateMessage(channelID, messageTS, text string)
}

// IssueCreator abstracts GitHub issue creation for testing.
type IssueCreator interface {
	CreateIssue(ctx context.Context, owner, repo, title, body string, labels []string) (string, error)
}

type ResultListener struct {
	results     queue.ResultBus
	store       queue.JobStore
	attachments queue.AttachmentStore
	slack       SlackPoster
	github      IssueCreator
}

func NewResultListener(
	results queue.ResultBus,
	store queue.JobStore,
	attachments queue.AttachmentStore,
	slack SlackPoster,
	github IssueCreator,
) *ResultListener {
	return &ResultListener{
		results:     results,
		store:       store,
		attachments: attachments,
		slack:       slack,
		github:      github,
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
	state, err := r.store.Get(result.JobID)
	if err != nil {
		slog.Error("job not found for result", "job_id", result.JobID, "error", err)
		return
	}

	job := state.Job
	owner, repo := splitRepo(job.Repo)

	switch {
	case result.Status == "failed":
		r.updateStatus(job, fmt.Sprintf(":x: 分析失敗: %s", result.Error))

	case result.Confidence == "low":
		r.updateStatus(job, ":warning: 判斷不屬於此 repo，已跳過")

	case result.FilesFound == 0 || result.Questions >= 5:
		r.createAndPostIssue(ctx, job, owner, repo, result, true)

	default:
		r.createAndPostIssue(ctx, job, owner, repo, result, false)
	}

	// Cleanup attachments; keep job in store for status visibility (TTL cleanup handles removal).
	r.attachments.Cleanup(ctx, result.JobID)
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

// updateStatus updates the original status message if possible, otherwise posts a new message.
func (r *ResultListener) updateStatus(job *queue.Job, text string) {
	if job.StatusMsgTS != "" {
		r.slack.UpdateMessage(job.ChannelID, job.StatusMsgTS, text)
	} else {
		r.slack.PostMessage(job.ChannelID, text, job.ThreadTS)
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
