package bot

import (
	"context"
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

	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, githubMock)

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

	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, nil)

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

	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, nil)

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
