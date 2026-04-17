package bot

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"agentdock/internal/queue"
)

type mockSlackPoster struct {
	mu       sync.Mutex
	messages []string
	buttons  []string
}

func (m *mockSlackPoster) PostMessage(channelID, text, threadTS string) {
	m.mu.Lock()
	m.messages = append(m.messages, text)
	m.mu.Unlock()
}

func (m *mockSlackPoster) UpdateMessage(channelID, messageTS, text string) {
	m.mu.Lock()
	m.messages = append(m.messages, text)
	m.mu.Unlock()
}

func (m *mockSlackPoster) PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value string) (string, error) {
	m.mu.Lock()
	m.buttons = append(m.buttons, actionID+":"+value)
	m.messages = append(m.messages, text)
	m.mu.Unlock()
	return "msg-ts-mock", nil
}

type mockIssueCreator struct {
	url string
	err error
}

func (m *mockIssueCreator) CreateIssue(ctx context.Context, owner, repo, title, body string, labels []string) (string, error) {
	return m.url, m.err
}

func TestResultListener_CompletedCreatesIssue(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1"})

	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}
	githubMock := &mockIssueCreator{url: "https://github.com/owner/repo/issues/1"}

	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, githubMock, nil, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go listener.Listen(ctx)

	bundle.Results.Publish(ctx, &queue.JobResult{
		JobID:      "j1",
		Status:     "completed",
		Title:      "Bug",
		Body:       "body",
		Labels:     []string{"bug"},
		Confidence: "high",
		FilesFound: 3,
	})

	time.Sleep(200 * time.Millisecond)

	slackMock.mu.Lock()
	defer slackMock.mu.Unlock()
	found := false
	for _, msg := range slackMock.messages {
		if strings.Contains(msg, "issues/1") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected issue URL in messages, got %v", slackMock.messages)
	}
}

func TestResultListener_FailedPostsError(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1"})

	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}

	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, nil, nil, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go listener.Listen(ctx)

	bundle.Results.Publish(ctx, &queue.JobResult{
		JobID:  "j1",
		Status: "failed",
		Error:  "agent crashed",
	})

	time.Sleep(200 * time.Millisecond)

	slackMock.mu.Lock()
	defer slackMock.mu.Unlock()
	found := false
	for _, msg := range slackMock.messages {
		if strings.Contains(msg, "agent crashed") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected error in messages, got %v", slackMock.messages)
	}
}

func TestResultListener_LowConfidenceRejects(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1"})

	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}

	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, nil, nil, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go listener.Listen(ctx)

	bundle.Results.Publish(ctx, &queue.JobResult{
		JobID:      "j1",
		Status:     "completed",
		Confidence: "low",
	})

	time.Sleep(200 * time.Millisecond)

	slackMock.mu.Lock()
	defer slackMock.mu.Unlock()
	found := false
	for _, msg := range slackMock.messages {
		if strings.Contains(msg, "跳過") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected rejection in messages, got %v", slackMock.messages)
	}
}

func TestResultListener_FailedShowsRetryButton(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1", RetryCount: 0})

	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}
	dedupCleared := false

	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, nil,
		func(channelID, threadTS string) { dedupCleared = true }, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go listener.Listen(ctx)

	bundle.Results.Publish(ctx, &queue.JobResult{
		JobID:  "j1",
		Status: "failed",
		Error:  "agent crashed",
	})

	time.Sleep(200 * time.Millisecond)

	slackMock.mu.Lock()
	defer slackMock.mu.Unlock()

	if len(slackMock.buttons) != 1 {
		t.Fatalf("expected 1 button post, got %d", len(slackMock.buttons))
	}
	if slackMock.buttons[0] != "retry_job:j1" {
		t.Errorf("button = %q, want retry_job:j1", slackMock.buttons[0])
	}
	if dedupCleared {
		t.Error("dedup should NOT be cleared when retry button is shown")
	}
}

func TestResultListener_FailedNoButtonAfterRetry(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1", RetryCount: 1})

	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}
	dedupCleared := false

	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, nil,
		func(channelID, threadTS string) { dedupCleared = true }, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go listener.Listen(ctx)

	bundle.Results.Publish(ctx, &queue.JobResult{
		JobID:  "j1",
		Status: "failed",
		Error:  "still broken",
	})

	time.Sleep(200 * time.Millisecond)

	slackMock.mu.Lock()
	defer slackMock.mu.Unlock()

	if len(slackMock.buttons) != 0 {
		t.Errorf("expected 0 button posts, got %d", len(slackMock.buttons))
	}
	found := false
	for _, msg := range slackMock.messages {
		if strings.Contains(msg, "重試後仍失敗") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected retry-exhausted message, got %v", slackMock.messages)
	}
	if !dedupCleared {
		t.Error("dedup should be cleared when no retry button")
	}
}

func TestResultListener_CancelledResultUpdatesSlack(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "jcan", Repo: "o/r", ChannelID: "C1", ThreadTS: "T1", StatusMsgTS: "S1"})

	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}
	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, nil, nil, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go listener.Listen(ctx)

	bundle.Results.Publish(ctx, &queue.JobResult{JobID: "jcan", Status: "cancelled"})
	time.Sleep(200 * time.Millisecond)

	slackMock.mu.Lock()
	defer slackMock.mu.Unlock()
	found := false
	for _, msg := range slackMock.messages {
		if strings.Contains(msg, "已取消") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected cancelled message, got %v", slackMock.messages)
	}
	if len(slackMock.buttons) != 0 {
		t.Errorf("no retry button expected, got %v", slackMock.buttons)
	}

	state, _ := store.Get("jcan")
	if state.Status != queue.JobCancelled {
		t.Errorf("store status = %q, want JobCancelled", state.Status)
	}
}

func TestResultListener_CompletedResultDeferredToCancellationWhenStoreCancelled(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "jrace", Repo: "o/r", ChannelID: "C1", ThreadTS: "T1", StatusMsgTS: "S1"})
	store.UpdateStatus("jrace", queue.JobCancelled)

	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}
	github := &mockIssueCreator{url: "https://github.com/o/r/issues/42"}
	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, github, nil, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go listener.Listen(ctx)

	bundle.Results.Publish(ctx, &queue.JobResult{
		JobID: "jrace", Status: "completed",
		Title: "Bug", Body: "b", Confidence: "high", FilesFound: 2,
	})
	time.Sleep(200 * time.Millisecond)

	slackMock.mu.Lock()
	defer slackMock.mu.Unlock()

	for _, msg := range slackMock.messages {
		if strings.Contains(msg, "issues/42") {
			t.Errorf("issue URL must not be posted when store says cancelled; messages=%v", slackMock.messages)
		}
	}
	found := false
	for _, msg := range slackMock.messages {
		if strings.Contains(msg, "已取消") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected cancelled message, got %v", slackMock.messages)
	}
}

func TestResultListener_DedupDropsDuplicateResult(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1"})

	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}

	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, nil,
		func(channelID, threadTS string) {}, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go listener.Listen(ctx)

	bundle.Results.Publish(ctx, &queue.JobResult{JobID: "j1", Status: "failed", Error: "timeout"})
	bundle.Results.Publish(ctx, &queue.JobResult{JobID: "j1", Status: "failed", Error: "context cancelled"})

	time.Sleep(300 * time.Millisecond)

	slackMock.mu.Lock()
	defer slackMock.mu.Unlock()

	if len(slackMock.buttons) != 1 {
		t.Errorf("expected 1 button post (dedup), got %d", len(slackMock.buttons))
	}
}
