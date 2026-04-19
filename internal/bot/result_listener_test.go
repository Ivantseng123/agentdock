package bot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

var errBoomGitHub = errors.New("github down")

type updateCall struct{ ChannelID, MessageTS, Text string }

type mockSlackPoster struct {
	mu          sync.Mutex
	messages    []string
	buttons     []string
	updateCalls []updateCall // NEW
}

func (m *mockSlackPoster) PostMessage(channelID, text, threadTS string) {
	m.mu.Lock()
	m.messages = append(m.messages, text)
	m.mu.Unlock()
}

func (m *mockSlackPoster) UpdateMessage(channelID, messageTS, text string) {
	m.mu.Lock()
	m.messages = append(m.messages, text)
	m.updateCalls = append(m.updateCalls, updateCall{channelID, messageTS, text}) // NEW
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

	state, _ := store.Get("j1")
	if state.Status != queue.JobCompleted {
		t.Errorf("store status = %q, want JobCompleted", state.Status)
	}
}

func TestResultListener_IssueCreationFailureMarksJobFailed(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "jcerr", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1"})

	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}
	githubMock := &mockIssueCreator{err: errBoomGitHub}

	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, githubMock, nil, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go listener.Listen(ctx)

	bundle.Results.Publish(ctx, &queue.JobResult{
		JobID: "jcerr", Status: "completed",
		Title: "Bug", Body: "body", Confidence: "high", FilesFound: 2,
	})
	time.Sleep(200 * time.Millisecond)

	state, _ := store.Get("jcerr")
	if state.Status != queue.JobFailed {
		t.Errorf("store status = %q, want JobFailed", state.Status)
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

	state, _ := store.Get("j1")
	if state.Status != queue.JobCompleted {
		t.Errorf("store status = %q, want JobCompleted", state.Status)
	}
}

// REJECTED results carry the agent's reason in Message — the listener must
// surface it so the user knows *why* we skipped, not just that we did.
func TestResultListener_LowConfidenceIncludesMessage(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "jmsg", Repo: "o/r", ChannelID: "C1", ThreadTS: "T1"})

	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}
	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, nil, nil, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go listener.Listen(ctx)

	bundle.Results.Publish(ctx, &queue.JobResult{
		JobID:      "jmsg",
		Status:     "completed",
		Confidence: "low",
		Message:    "repo has no login page code",
	})

	time.Sleep(200 * time.Millisecond)

	slackMock.mu.Lock()
	defer slackMock.mu.Unlock()
	found := false
	for _, msg := range slackMock.messages {
		if strings.Contains(msg, "repo has no login page code") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected message in Slack text, got %v", slackMock.messages)
	}
}

func TestResultListener_FailedShowsRetryButton(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1", RetryCount: 0})

	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}
	var dedupMu sync.Mutex
	dedupCleared := false

	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, nil,
		func(channelID, threadTS string) {
			dedupMu.Lock()
			dedupCleared = true
			dedupMu.Unlock()
		}, slog.Default())

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
	dedupMu.Lock()
	actualDedup := dedupCleared
	dedupMu.Unlock()
	if actualDedup {
		t.Error("dedup should NOT be cleared when retry button is shown")
	}
}

func TestResultListener_FailedNoButtonAfterRetry(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1", RetryCount: 1})

	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}
	var dedupMu sync.Mutex
	dedupCleared := false

	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, nil,
		func(channelID, threadTS string) {
			dedupMu.Lock()
			dedupCleared = true
			dedupMu.Unlock()
		}, slog.Default())

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
	dedupMu.Lock()
	actualDedup := dedupCleared
	dedupMu.Unlock()
	if !actualDedup {
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

func TestHandleResult_FinalStatusMessageDoubleWrite(t *testing.T) {
	slack := &mockSlackPoster{}
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{
		ID: "jdouble", Repo: "o/r", ChannelID: "C1",
		ThreadTS: "T1", StatusMsgTS: "S1",
	})
	store.UpdateStatus("jdouble", queue.JobCompleted)

	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	gh := &mockIssueCreator{url: "https://github.com/o/r/issues/1"}
	r := NewResultListener(nil, store, bundle.Attachments, slack, gh, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	result := &queue.JobResult{
		JobID: "jdouble", Status: "completed",
		Title: "bug", Body: "desc", Labels: []string{"bug"},
	}
	r.handleResult(context.Background(), result)

	slack.mu.Lock()
	initialCalls := len(slack.updateCalls)
	slack.mu.Unlock()
	if initialCalls < 1 {
		t.Fatalf("expected ≥1 immediate UpdateMessage call, got %d", initialCalls)
	}

	// Wait just over the 2s defensive delay.
	time.Sleep(2500 * time.Millisecond)

	slack.mu.Lock()
	afterDelay := len(slack.updateCalls)
	calls := append([]updateCall(nil), slack.updateCalls...)
	slack.mu.Unlock()

	if afterDelay != initialCalls+1 {
		t.Fatalf("expected exactly one extra UpdateMessage after 2s; initial=%d after=%d",
			initialCalls, afterDelay)
	}

	first := calls[initialCalls-1]
	second := calls[initialCalls]
	if first.MessageTS != second.MessageTS {
		t.Errorf("MessageTS mismatch: %q vs %q", first.MessageTS, second.MessageTS)
	}
	if first.Text != second.Text {
		t.Errorf("Text mismatch: %q vs %q", first.Text, second.Text)
	}
	if second.MessageTS != "S1" {
		t.Errorf("double-write target wrong: %q", second.MessageTS)
	}
}

// TestResultListener_ParseCompletedCreatesIssue verifies the app-side parse
// path: worker ships RawOutput with a valid TRIAGE_RESULT, the listener
// parses it, and the parsed Title/Body/Labels flow into CreateIssue.
func TestResultListener_ParseCompletedCreatesIssue(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "jparse", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1"})

	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}
	githubMock := &mockIssueCreator{url: "https://github.com/owner/repo/issues/9"}

	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, githubMock, nil, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go listener.Listen(ctx)

	rawOutput := "Analysis done.\n\n===TRIAGE_RESULT===\n" +
		`{"status":"CREATED","title":"Cache bug","body":"details","labels":["bug"],"confidence":"high","files_found":3}`

	bundle.Results.Publish(ctx, &queue.JobResult{
		JobID:     "jparse",
		Status:    "completed",
		RawOutput: rawOutput,
	})

	time.Sleep(200 * time.Millisecond)

	slackMock.mu.Lock()
	defer slackMock.mu.Unlock()
	found := false
	for _, msg := range slackMock.messages {
		if strings.Contains(msg, "issues/9") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected issue URL in messages, got %v", slackMock.messages)
	}

	state, _ := store.Get("jparse")
	if state.Status != queue.JobCompleted {
		t.Errorf("store status = %q, want JobCompleted", state.Status)
	}
}

// TestResultListener_ParseRejectedRoutesToLowConfidence verifies that a
// REJECTED agent payload triggers the "跳過" lane instead of issue creation.
func TestResultListener_ParseRejectedRoutesToLowConfidence(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "jrej", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1"})

	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}
	// If the listener were to misroute REJECTED to issue creation, this mock
	// would return an error-free URL — a positive signal for the test to catch.
	githubMock := &mockIssueCreator{url: "https://github.com/owner/repo/issues/should-not-appear"}

	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, githubMock, nil, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go listener.Listen(ctx)

	rawOutput := "done.\n\n===TRIAGE_RESULT===\n" +
		`{"status":"REJECTED","message":"not our repo"}`

	bundle.Results.Publish(ctx, &queue.JobResult{
		JobID:     "jrej",
		Status:    "completed",
		RawOutput: rawOutput,
	})

	time.Sleep(200 * time.Millisecond)

	slackMock.mu.Lock()
	defer slackMock.mu.Unlock()

	for _, msg := range slackMock.messages {
		if strings.Contains(msg, "should-not-appear") {
			t.Errorf("issue URL must not be posted for REJECTED; messages=%v", slackMock.messages)
		}
	}

	skippedFound := false
	reasonFound := false
	for _, msg := range slackMock.messages {
		if strings.Contains(msg, "跳過") {
			skippedFound = true
		}
		if strings.Contains(msg, "not our repo") {
			reasonFound = true
		}
	}
	if !skippedFound {
		t.Errorf("expected '跳過' in messages, got %v", slackMock.messages)
	}
	if !reasonFound {
		t.Errorf("expected 'not our repo' reason in messages, got %v", slackMock.messages)
	}

	state, _ := store.Get("jrej")
	if state.Status != queue.JobCompleted {
		t.Errorf("store status = %q, want JobCompleted", state.Status)
	}
}

// TestResultListener_ParseErrorRoutesToFailure verifies that an ERROR agent
// payload is routed to the failure path (retry button, no issue).
func TestResultListener_ParseErrorRoutesToFailure(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "jerr", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1"})

	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}
	githubMock := &mockIssueCreator{url: "https://github.com/owner/repo/issues/should-not-appear"}

	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, githubMock, nil, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go listener.Listen(ctx)

	rawOutput := "done.\n\n===TRIAGE_RESULT===\n" +
		`{"status":"ERROR","message":"gh exploded"}`

	bundle.Results.Publish(ctx, &queue.JobResult{
		JobID:     "jerr",
		Status:    "completed",
		RawOutput: rawOutput,
	})

	time.Sleep(200 * time.Millisecond)

	slackMock.mu.Lock()
	defer slackMock.mu.Unlock()

	for _, msg := range slackMock.messages {
		if strings.Contains(msg, "should-not-appear") {
			t.Errorf("issue URL must not be posted when agent ERRORs; messages=%v", slackMock.messages)
		}
	}

	// handleFailure on first attempt posts a button.
	if len(slackMock.buttons) != 1 {
		t.Errorf("expected 1 retry button post, got %d (buttons=%v)", len(slackMock.buttons), slackMock.buttons)
	}

	foundAgentError := false
	for _, msg := range slackMock.messages {
		// handleFailure truncates the error at the first ":", so the Slack
		// message surfaces "分析失敗: agent error" — we key on that prefix.
		if strings.Contains(msg, "agent error") {
			foundAgentError = true
		}
	}
	if !foundAgentError {
		t.Errorf("expected 'agent error' in failure message, got %v", slackMock.messages)
	}

	state, _ := store.Get("jerr")
	if state.Status != queue.JobFailed {
		t.Errorf("store status = %q, want JobFailed", state.Status)
	}
}

// TestResultListener_ParseMalformedRoutesToFailure verifies that
// unparseable agent output fails the job with a clear "parse failed" reason.
func TestResultListener_ParseMalformedRoutesToFailure(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "jbad", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1"})

	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}
	githubMock := &mockIssueCreator{url: "https://github.com/owner/repo/issues/should-not-appear"}

	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, githubMock, nil, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go listener.Listen(ctx)

	bundle.Results.Publish(ctx, &queue.JobResult{
		JobID:     "jbad",
		Status:    "completed",
		RawOutput: "totally not valid agent output",
	})

	time.Sleep(200 * time.Millisecond)

	slackMock.mu.Lock()
	defer slackMock.mu.Unlock()

	for _, msg := range slackMock.messages {
		if strings.Contains(msg, "should-not-appear") {
			t.Errorf("issue URL must not be posted when parse fails; messages=%v", slackMock.messages)
		}
	}

	if len(slackMock.buttons) != 1 {
		t.Errorf("expected 1 retry button post, got %d", len(slackMock.buttons))
	}

	foundParseFailed := false
	for _, msg := range slackMock.messages {
		if strings.Contains(msg, "parse failed") || strings.Contains(msg, "分析失敗") {
			foundParseFailed = true
		}
	}
	if !foundParseFailed {
		t.Errorf("expected parse-failure indicator in messages, got %v", slackMock.messages)
	}

	state, _ := store.Get("jbad")
	if state.Status != queue.JobFailed {
		t.Errorf("store status = %q, want JobFailed", state.Status)
	}
}
