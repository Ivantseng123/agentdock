package bot

import (
	"context"
	"log/slog"
	"testing"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

type mockJobQueue struct {
	submitted []*queue.Job
}

func (m *mockJobQueue) Submit(ctx context.Context, job *queue.Job) error {
	m.submitted = append(m.submitted, job)
	return nil
}

func TestRetryHandler_CreatesNewJob(t *testing.T) {
	store := queue.NewMemJobStore()
	original := &queue.Job{
		ID:        "j1",
		ChannelID: "C1",
		ThreadTS:  "T1",
		UserID:    "U1",
		Repo:      "owner/repo",
		CloneURL:  "https://github.com/owner/repo.git",
		Branch:    "main",
		PromptContext: &queue.PromptContext{
			ThreadMessages: []queue.ThreadMessage{{User: "Alice", Timestamp: "1", Text: "test prompt"}},
			Channel:        "general",
			Reporter:       "Alice",
			Goal:           "triage",
		},
		Priority: 50,
		Skills:   map[string]*queue.SkillPayload{"s1": {Files: map[string][]byte{"SKILL.md": []byte("content")}}},
	}
	store.Put(original)
	store.UpdateStatus("j1", queue.JobFailed)

	q := &mockJobQueue{}
	slackMock := &mockSlackPoster{}

	handler := NewRetryHandler(store, q, slackMock, slog.Default())
	handler.Handle("C1", "j1", "msg-ts-1")

	if len(q.submitted) != 1 {
		t.Fatalf("expected 1 submitted job, got %d", len(q.submitted))
	}

	newJob := q.submitted[0]
	if newJob.ID == "j1" {
		t.Error("new job should have a different ID")
	}
	if newJob.RetryCount != 1 {
		t.Errorf("RetryCount = %d, want 1", newJob.RetryCount)
	}
	if newJob.RetryOfJobID != "j1" {
		t.Errorf("RetryOfJobID = %q, want j1", newJob.RetryOfJobID)
	}
	if newJob.PromptContext == nil {
		t.Fatal("PromptContext not copied to retry job (worker would reject as malformed)")
	}
	if len(newJob.PromptContext.ThreadMessages) == 0 || newJob.PromptContext.ThreadMessages[0].Text != "test prompt" {
		t.Errorf("PromptContext.ThreadMessages not preserved, got %+v", newJob.PromptContext.ThreadMessages)
	}
	if newJob.PromptContext.Goal != "triage" {
		t.Errorf("PromptContext.Goal = %q, want triage", newJob.PromptContext.Goal)
	}
	if newJob.UserID != "U1" {
		t.Errorf("UserID = %q, want U1", newJob.UserID)
	}
	if newJob.Priority != 50 {
		t.Errorf("Priority = %d, want 50", newJob.Priority)
	}

	slackMock.mu.Lock()
	defer slackMock.mu.Unlock()
	foundUpdate := false
	for _, msg := range slackMock.messages {
		if msg == ":arrows_counterclockwise: 已重新排入佇列" {
			foundUpdate = true
		}
	}
	if !foundUpdate {
		t.Errorf("expected update message, got %v", slackMock.messages)
	}

	if len(slackMock.buttons) != 1 {
		t.Fatalf("expected 1 button post, got %d", len(slackMock.buttons))
	}
}

func TestRetryHandler_JobNotFound(t *testing.T) {
	store := queue.NewMemJobStore()
	q := &mockJobQueue{}
	slackMock := &mockSlackPoster{}

	handler := NewRetryHandler(store, q, slackMock, slog.Default())
	handler.Handle("C1", "nonexistent", "msg-ts-1")

	if len(q.submitted) != 0 {
		t.Error("should not submit when job not found")
	}

	slackMock.mu.Lock()
	defer slackMock.mu.Unlock()
	if len(slackMock.messages) == 0 {
		t.Error("should post error message when job not found")
	}
}

func TestRetryHandler_IgnoresNonFailedJob(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "T1"})
	store.UpdateStatus("j1", queue.JobCompleted)

	q := &mockJobQueue{}
	slackMock := &mockSlackPoster{}

	handler := NewRetryHandler(store, q, slackMock, slog.Default())
	handler.Handle("C1", "j1", "msg-ts-1")

	if len(q.submitted) != 0 {
		t.Error("should not submit when job is not failed")
	}
}
