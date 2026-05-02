package bot

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Ivantseng123/agentdock/app/config"
	"github.com/Ivantseng123/agentdock/shared/crypto"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

type fakeRetrySource struct {
	token   string
	mintErr error

	mintCalls atomic.Int32
}

func (f *fakeRetrySource) Get() (string, error) { return f.token, nil }

func (f *fakeRetrySource) MintFresh() (string, error) {
	f.mintCalls.Add(1)
	if f.mintErr != nil {
		return "", f.mintErr
	}
	return f.token, nil
}

func (f *fakeRetrySource) IsAccessible(_ string) bool { return true }

type mockJobQueue struct {
	submitted []*queue.Job
}

func (m *mockJobQueue) Submit(ctx context.Context, job *queue.Job) error {
	m.submitted = append(m.submitted, job)
	return nil
}

func TestRetryHandler_CreatesNewJob(t *testing.T) {
	ctx := context.Background()
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
	store.Put(ctx, original)
	store.UpdateStatus(ctx, "j1", queue.JobFailed)

	q := &mockJobQueue{}
	slackMock := &mockSlackPoster{}

	handler := NewRetryHandler(store, q, slackMock, slog.Default(), nil, nil, nil)
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

	handler := NewRetryHandler(store, q, slackMock, slog.Default(), nil, nil, nil)
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

func TestRetryHandler_MintsFreshSecretsOnRetry(t *testing.T) {
	ctx := context.Background()
	store := queue.NewMemJobStore()

	staleSecrets := []byte("stale-encrypted-blob-from-original-job")
	original := &queue.Job{
		ID:               "j1",
		ChannelID:        "C1",
		ThreadTS:         "T1",
		UserID:           "U1",
		Repo:             "owner/repo",
		EncryptedSecrets: staleSecrets,
		PromptContext:    &queue.PromptContext{ThreadMessages: []queue.ThreadMessage{{User: "u", Text: "x"}}, Channel: "c"},
	}
	store.Put(ctx, original)
	store.UpdateStatus(ctx, "j1", queue.JobFailed)

	q := &mockJobQueue{}
	slackMock := &mockSlackPoster{}
	src := &fakeRetrySource{token: "ghs_fresh_for_retry"}
	cfg := &config.Config{Secrets: map[string]string{"OTHER": "kept"}}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}

	handler := NewRetryHandler(store, q, slackMock, slog.Default(), cfg, src, key)
	handler.Handle("C1", "j1", "msg-ts-1")

	if len(q.submitted) != 1 {
		t.Fatalf("expected 1 submit, got %d", len(q.submitted))
	}
	if calls := src.mintCalls.Load(); calls != 1 {
		t.Errorf("MintFresh calls = %d, want 1", calls)
	}
	newJob := q.submitted[0]
	if string(newJob.EncryptedSecrets) == string(staleSecrets) {
		t.Error("retry job reused stale EncryptedSecrets — must mint fresh")
	}

	// Decrypt and verify GH_TOKEN is the freshly minted one.
	plain, err := crypto.Decrypt(key, newJob.EncryptedSecrets)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(plain, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got["GH_TOKEN"] != "ghs_fresh_for_retry" {
		t.Errorf("retry GH_TOKEN = %q, want ghs_fresh_for_retry", got["GH_TOKEN"])
	}
	if got["OTHER"] != "kept" {
		t.Errorf("non-token secrets dropped: %v", got)
	}
}

func TestRetryHandler_MintFailureBlocksSubmit(t *testing.T) {
	ctx := context.Background()
	store := queue.NewMemJobStore()
	original := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "T1"}
	store.Put(ctx, original)
	store.UpdateStatus(ctx, "j1", queue.JobFailed)

	q := &mockJobQueue{}
	slackMock := &mockSlackPoster{}
	src := &fakeRetrySource{mintErr: errors.New("github 503")}
	cfg := &config.Config{Secrets: map[string]string{}}

	key := make([]byte, 32)
	rand.Read(key)

	handler := NewRetryHandler(store, q, slackMock, slog.Default(), cfg, src, key)
	handler.Handle("C1", "j1", "msg-ts-1")

	if len(q.submitted) != 0 {
		t.Errorf("expected no submit on mint failure, got %d", len(q.submitted))
	}
	slackMock.mu.Lock()
	defer slackMock.mu.Unlock()
	foundFailMsg := false
	for _, msg := range slackMock.messages {
		if strings.Contains(msg, "重試失敗") {
			foundFailMsg = true
			break
		}
	}
	if !foundFailMsg {
		t.Errorf("expected '重試失敗' slack message, got %v", slackMock.messages)
	}
}

func TestRetryHandler_IgnoresNonFailedJob(t *testing.T) {
	ctx := context.Background()
	store := queue.NewMemJobStore()
	store.Put(ctx, &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "T1"})
	store.UpdateStatus(ctx, "j1", queue.JobCompleted)

	q := &mockJobQueue{}
	slackMock := &mockSlackPoster{}

	handler := NewRetryHandler(store, q, slackMock, slog.Default(), nil, nil, nil)
	handler.Handle("C1", "j1", "msg-ts-1")

	if len(q.submitted) != 0 {
		t.Error("should not submit when job is not failed")
	}
}
