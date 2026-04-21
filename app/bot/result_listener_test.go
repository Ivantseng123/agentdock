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

	"github.com/Ivantseng123/agentdock/app/workflow"
	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/shared/queue/queuetest"
)

type updateCall struct{ ChannelID, MessageTS, Text string }

type mockSlackPoster struct {
	mu          sync.Mutex
	messages    []string
	buttons     []string
	updateCalls []updateCall
}

func (m *mockSlackPoster) PostMessage(channelID, text, threadTS string) {
	m.mu.Lock()
	m.messages = append(m.messages, text)
	m.mu.Unlock()
}

func (m *mockSlackPoster) UpdateMessage(channelID, messageTS, text string) {
	m.mu.Lock()
	m.messages = append(m.messages, text)
	m.updateCalls = append(m.updateCalls, updateCall{channelID, messageTS, text})
	m.mu.Unlock()
}

func (m *mockSlackPoster) PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value string) (string, error) {
	m.mu.Lock()
	m.buttons = append(m.buttons, actionID+":"+value)
	m.messages = append(m.messages, text)
	m.mu.Unlock()
	return "msg-ts-mock", nil
}

// ── fake workflow ─────────────────────────────────────────────────────────────

// fakeWorkflow is a configurable workflow.Workflow stub for listener dispatch
// tests. It records calls to HandleResult and can be configured to return an
// error or mutate result.Status.
type fakeWorkflow struct {
	taskType        string
	handleResultErr error
	// onHandleResult is called synchronously inside HandleResult if non-nil.
	// Use it to mutate result.Status (e.g. simulate ERROR → "failed").
	onHandleResult func(job *queue.Job, result *queue.JobResult)

	mu           sync.Mutex
	handleCalls  int
	lastJob      *queue.Job
	lastResult   *queue.JobResult
}

func newFakeWorkflow() *fakeWorkflow { return &fakeWorkflow{taskType: "issue"} }

func (f *fakeWorkflow) Type() string { return f.taskType }
func (f *fakeWorkflow) Trigger(ctx context.Context, ev workflow.TriggerEvent, args string) (workflow.NextStep, error) {
	return workflow.NextStep{Kind: workflow.NextStepSubmit}, nil
}
func (f *fakeWorkflow) Selection(ctx context.Context, p *workflow.Pending, value string) (workflow.NextStep, error) {
	return workflow.NextStep{Kind: workflow.NextStepSubmit}, nil
}
func (f *fakeWorkflow) BuildJob(ctx context.Context, p *workflow.Pending) (*queue.Job, string, error) {
	return &queue.Job{TaskType: "issue"}, "status", nil
}
func (f *fakeWorkflow) HandleResult(ctx context.Context, job *queue.Job, result *queue.JobResult) error {
	f.mu.Lock()
	f.handleCalls++
	f.lastJob = job
	f.lastResult = result
	f.mu.Unlock()
	if f.onHandleResult != nil {
		f.onHandleResult(job, result)
	}
	return f.handleResultErr
}

// ── tests: failed path (listener-owned) ──────────────────────────────────────

func TestResultListener_FailedPostsError(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1"})

	bundle := queuetest.NewBundle(10, 3, store)
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

func TestResultListener_FailedShowsRetryButton(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1", RetryCount: 0})

	bundle := queuetest.NewBundle(10, 3, store)
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

	bundle := queuetest.NewBundle(10, 3, store)
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

// ── tests: cancelled path (listener-owned) ───────────────────────────────────

func TestResultListener_CancelledResultUpdatesSlack(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "jcan", Repo: "o/r", ChannelID: "C1", ThreadTS: "T1", StatusMsgTS: "S1"})

	bundle := queuetest.NewBundle(10, 3, store)
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

// TestResultListener_CompletedResultDeferredToCancellationWhenStoreCancelled
// verifies that Design A dominates: if the store marks the job cancelled, a
// concurrent "completed" result is routed to cancellation, not the workflow.
func TestResultListener_CompletedResultDeferredToCancellationWhenStoreCancelled(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "jrace", Repo: "o/r", ChannelID: "C1", ThreadTS: "T1", StatusMsgTS: "S1"})
	store.UpdateStatus("jrace", queue.JobCancelled)

	bundle := queuetest.NewBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}
	fw := newFakeWorkflow()
	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, fw, nil, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go listener.Listen(ctx)

	bundle.Results.Publish(ctx, &queue.JobResult{
		JobID: "jrace", Status: "completed",
		RawOutput: `===TRIAGE_RESULT===` + "\n" + `{"status":"CREATED","title":"Bug"}`,
	})
	time.Sleep(200 * time.Millisecond)

	// Workflow must NOT have been called — cancellation dominated.
	fw.mu.Lock()
	calls := fw.handleCalls
	fw.mu.Unlock()
	if calls != 0 {
		t.Errorf("workflow.HandleResult must not be called when store says cancelled; got %d calls", calls)
	}

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
}

// ── tests: dedup ──────────────────────────────────────────────────────────────

func TestResultListener_DedupDropsDuplicateResult(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1"})

	bundle := queuetest.NewBundle(10, 3, store)
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

// ── tests: double-write StatusMsgTS (listener-owned) ─────────────────────────

// TestHandleResult_FinalStatusMessageDoubleWrite verifies that the listener's
// updateStatus issues a defensive re-write 2s after the initial update.
// The "completed" path delegates to the workflow, which calls updateStatus
// on its own slack port — but the listener still controls dedup and store.
// Here we use a fake workflow that posts no Slack messages, verifying the
// listener does NOT itself double-write for completed results (the workflow
// owns that). We instead verify via the cancelled path, which uses
// listener's own updateStatus.
func TestHandleResult_FinalStatusMessageDoubleWrite(t *testing.T) {
	slack := &mockSlackPoster{}
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{
		ID: "jdouble", Repo: "o/r", ChannelID: "C1",
		ThreadTS: "T1", StatusMsgTS: "S1",
	})
	// Mark as cancelled so cancellation path fires (which uses listener's own updateStatus).
	store.UpdateStatus("jdouble", queue.JobCancelled)

	bundle := queuetest.NewBundle(10, 3, store)
	defer bundle.Close()

	r := NewResultListener(nil, store, bundle.Attachments, slack, nil, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	result := &queue.JobResult{
		JobID: "jdouble", Status: "completed",
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

// ── tests: completed path dispatches to workflow ──────────────────────────────

// TestResultListener_CompletedDispatchesToWorkflow verifies that a completed
// result is forwarded to the workflow's HandleResult and the store is updated
// to JobCompleted on success.
func TestResultListener_CompletedDispatchesToWorkflow(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "jdisp", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1"})

	bundle := queuetest.NewBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}
	fw := newFakeWorkflow()

	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, fw, nil, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go listener.Listen(ctx)

	published := &queue.JobResult{
		JobID:     "jdisp",
		Status:    "completed",
		RawOutput: "===TRIAGE_RESULT===\n{\"status\":\"CREATED\",\"title\":\"Bug\"}",
	}
	bundle.Results.Publish(ctx, published)

	time.Sleep(200 * time.Millisecond)

	fw.mu.Lock()
	calls := fw.handleCalls
	gotJob := fw.lastJob
	gotResult := fw.lastResult
	fw.mu.Unlock()

	if calls != 1 {
		t.Errorf("expected workflow.HandleResult called once, got %d", calls)
	}
	if gotJob == nil || gotJob.ID != "jdisp" {
		t.Errorf("workflow received wrong job: %+v", gotJob)
	}
	if gotResult == nil || gotResult.JobID != published.JobID {
		t.Errorf("workflow received wrong result: %+v", gotResult)
	}

	state, _ := store.Get("jdisp")
	if state.Status != queue.JobCompleted {
		t.Errorf("store status = %q, want JobCompleted", state.Status)
	}
}

// TestResultListener_WorkflowMutatesStatusToFailed verifies that when the
// workflow mutates result.Status to "failed" (e.g. for ERROR or parse-fail),
// the listener records JobFailed in the store and does NOT clear dedup.
func TestResultListener_WorkflowMutatesStatusToFailed(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "jmfail", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1"})

	bundle := queuetest.NewBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}
	var dedupMu sync.Mutex
	dedupCleared := false

	fw := newFakeWorkflow()
	fw.onHandleResult = func(job *queue.Job, result *queue.JobResult) {
		result.Status = "failed"
	}

	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, fw,
		func(channelID, threadTS string) {
			dedupMu.Lock()
			dedupCleared = true
			dedupMu.Unlock()
		}, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go listener.Listen(ctx)

	published := &queue.JobResult{
		JobID:     "jmfail",
		Status:    "completed",
		RawOutput: "===TRIAGE_RESULT===\n{\"status\":\"ERROR\",\"message\":\"gh exploded\"}",
	}
	bundle.Results.Publish(ctx, published)

	time.Sleep(200 * time.Millisecond)

	fw.mu.Lock()
	gotJob := fw.lastJob
	gotResult := fw.lastResult
	fw.mu.Unlock()

	if gotJob == nil || gotJob.ID != "jmfail" {
		t.Errorf("workflow received wrong job: %+v", gotJob)
	}
	if gotResult == nil || gotResult.JobID != published.JobID {
		t.Errorf("workflow received wrong result: %+v", gotResult)
	}

	state, _ := store.Get("jmfail")
	if state.Status != queue.JobFailed {
		t.Errorf("store status = %q, want JobFailed", state.Status)
	}

	dedupMu.Lock()
	actual := dedupCleared
	dedupMu.Unlock()
	if actual {
		t.Error("dedup must NOT be cleared when workflow routes to failure (retry button path)")
	}
}

// TestResultListener_WorkflowErrorMarksJobFailed verifies that when
// HandleResult returns a non-nil error (e.g. GitHub create-issue failure), the
// listener marks the job JobFailed, clears dedup, and does NOT post a retry
// button (non-retriable internal error). This covers the handleResultErr path
// on fakeWorkflow and restores the pre-diff behaviour asserted by the deleted
// TestResultListener_IssueCreationFailureMarksJobFailed test.
func TestResultListener_WorkflowErrorMarksJobFailed(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "jerr", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1"})

	bundle := queuetest.NewBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}
	var dedupMu sync.Mutex
	dedupCleared := false

	fw := newFakeWorkflow()
	fw.handleResultErr = errors.New("github create issue: API 503")

	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, fw,
		func(channelID, threadTS string) {
			dedupMu.Lock()
			dedupCleared = true
			dedupMu.Unlock()
		}, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go listener.Listen(ctx)

	bundle.Results.Publish(ctx, &queue.JobResult{
		JobID:  "jerr",
		Status: "completed",
	})

	time.Sleep(200 * time.Millisecond)

	state, _ := store.Get("jerr")
	if state.Status != queue.JobFailed {
		t.Errorf("store status = %q, want JobFailed", state.Status)
	}

	dedupMu.Lock()
	actual := dedupCleared
	dedupMu.Unlock()
	if !actual {
		t.Error("dedup should be cleared on workflow-error (non-retriable path)")
	}

	// No retry button should be posted.
	slackMock.mu.Lock()
	buttons := len(slackMock.buttons)
	slackMock.mu.Unlock()
	if buttons != 0 {
		t.Errorf("expected 0 retry buttons on internal workflow error, got %d", buttons)
	}
}

// ── tests: worker info in failure messages ────────────────────────────────────

func TestResultListener_FailureShowsNicknameAndWorkerID(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "jfnick", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1"})
	store.SetAgentStatus("jfnick", queue.StatusReport{
		JobID:          "jfnick",
		WorkerID:       "host/worker-2",
		WorkerNickname: "小明",
	})

	bundle := queuetest.NewBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}

	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, nil, nil, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go listener.Listen(ctx)

	bundle.Results.Publish(ctx, &queue.JobResult{
		JobID:  "jfnick",
		Status: "failed",
		Error:  "agent crashed",
	})

	time.Sleep(200 * time.Millisecond)

	slackMock.mu.Lock()
	defer slackMock.mu.Unlock()

	var fail string
	for _, msg := range slackMock.messages {
		if strings.Contains(msg, "分析失敗") {
			fail = msg
		}
	}
	if fail == "" {
		t.Fatalf("expected failure message, got %v", slackMock.messages)
	}
	if !strings.Contains(fail, "小明") {
		t.Errorf("failure should show nickname 小明, got %q", fail)
	}
	if !strings.Contains(fail, "host/worker-2") {
		t.Errorf("failure should also show raw worker id for debugging, got %q", fail)
	}
}

func TestResultListener_FailureWithoutNicknameShowsWorkerID(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "jfraw", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1"})
	store.SetAgentStatus("jfraw", queue.StatusReport{
		JobID:    "jfraw",
		WorkerID: "host/worker-3",
	})

	bundle := queuetest.NewBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}

	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, nil, nil, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go listener.Listen(ctx)

	bundle.Results.Publish(ctx, &queue.JobResult{
		JobID:  "jfraw",
		Status: "failed",
		Error:  "boom",
	})

	time.Sleep(200 * time.Millisecond)

	slackMock.mu.Lock()
	defer slackMock.mu.Unlock()

	var fail string
	for _, msg := range slackMock.messages {
		if strings.Contains(msg, "分析失敗") {
			fail = msg
		}
	}
	if fail == "" {
		t.Fatalf("expected failure message, got %v", slackMock.messages)
	}
	if !strings.Contains(fail, "host/worker-3") {
		t.Errorf("failure should show raw worker id when no nickname, got %q", fail)
	}
}
