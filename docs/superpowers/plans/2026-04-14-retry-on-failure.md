# Retry on Failure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Slack retry button on job failure, with max 1 retry, unified failure handling through ResultBus, and visible worker identity.

**Architecture:** Watchdog stops posting to Slack directly and instead publishes failed results to ResultBus. ResultListener becomes the single exit point for all job outcomes — it decides whether to show a retry button based on RetryCount. A new RetryHandler struct handles button clicks by creating a new job and submitting it to the queue.

**Tech Stack:** Go, Slack Block Kit (buttons), Redis Streams / InMem transport (no new deps)

**Spec:** `docs/superpowers/specs/2026-04-14-retry-on-failure-design.md`

---

## File Map

| File | Action | Responsibility |
|------|--------|----------------|
| `internal/queue/job.go` | Modify | Add `RetryCount`, `RetryOfJobID` fields to `Job` |
| `internal/queue/watchdog.go` | Modify | Remove `StuckNotifier`; add `ResultBus`; publish failed result |
| `internal/bot/result_listener.go` | Modify | `processedJobs` dedup; retry button logic; conditional dedup clear; WorkerID display |
| `internal/bot/result_listener_test.go` | Modify | Update mock; add dedup/button/no-button tests |
| `internal/bot/retry_handler.go` | Create | `RetryHandler` struct — handles retry button interaction |
| `internal/bot/retry_handler_test.go` | Create | Tests for RetryHandler |
| `internal/bot/workflow.go` | Modify | Bug fix: set `UserID` on Job |
| `internal/worker/pool.go` | Modify | Hostname in worker ID; call `SetWorker()` after Ack |
| `cmd/bot/main.go` | Modify | Watchdog wiring; slackPosterAdapter; retry_job routing |
| `cmd/bot/worker.go` | Modify | Pass hostname to Pool |

---

### Task 1: Add RetryCount and RetryOfJobID to Job

**Files:**
- Modify: `internal/queue/job.go:18-35`

- [ ] **Step 1: Add fields to Job struct**

In `internal/queue/job.go`, add two fields to the `Job` struct after `TaskType`:

```go
TaskType    string            `json:"task_type,omitempty"`
RetryCount  int               `json:"retry_count,omitempty"`
RetryOfJobID string           `json:"retry_of_job_id,omitempty"`
SubmittedAt time.Time         `json:"submitted_at"`
```

- [ ] **Step 2: Run tests to verify nothing breaks**

Run: `go test ./...`
Expected: All 114 tests pass (fields are zero-valued by default, no existing code references them).

- [ ] **Step 3: Commit**

```bash
git add internal/queue/job.go
git commit -m "feat: add RetryCount and RetryOfJobID to Job struct"
```

---

### Task 2: Fix UserID bug in workflow.go

**Files:**
- Modify: `internal/bot/workflow.go:137-144` and `internal/bot/workflow.go:414-427`

- [ ] **Step 1: Add UserID to pendingTriage**

In `internal/bot/workflow.go`, the `HandleTrigger` method creates a `pendingTriage` at line 137. Add `UserID` from the event. Change the `pendingTriage` initialization:

```go
pt := &pendingTriage{
    ChannelID:   event.ChannelID,
    ThreadTS:    event.ThreadTS,
    TriggerTS:   event.TriggerTS,
    Reporter:    reporter,
    ChannelName: channelName,
    CmdArgs:     parseTriggerArgs(event.Text),
}
```

to:

```go
pt := &pendingTriage{
    ChannelID:   event.ChannelID,
    ThreadTS:    event.ThreadTS,
    TriggerTS:   event.TriggerTS,
    UserID:      event.UserID,
    Reporter:    reporter,
    ChannelName: channelName,
    CmdArgs:     parseTriggerArgs(event.Text),
}
```

This requires adding `UserID string` to the `pendingTriage` struct (after `TriggerTS`):

```go
type pendingTriage struct {
    ChannelID      string
    ThreadTS       string
    TriggerTS      string
    UserID         string
    Attachments    []string
    // ... rest unchanged
}
```

- [ ] **Step 2: Set UserID on Job in runTriage**

In `runTriage()` at line 414, add `UserID` to the Job:

```go
job := &queue.Job{
    ID:          pt.RequestID,
    Priority:    w.channelPriority(pt.ChannelID),
    ChannelID:   pt.ChannelID,
    ThreadTS:    pt.ThreadTS,
    UserID:      pt.UserID,
    Repo:        pt.SelectedRepo,
    // ... rest unchanged
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./...`
Expected: All tests pass.

- [ ] **Step 4: Commit**

```bash
git add internal/bot/workflow.go
git commit -m "fix: set UserID on Job in workflow submission"
```

---

### Task 3: Promote PostMessageWithButton to SlackPoster interface

**Files:**
- Modify: `internal/bot/result_listener.go:12-16`
- Modify: `internal/bot/result_listener_test.go:13-28`
- Modify: `cmd/bot/main.go:385-401`

- [ ] **Step 1: Add PostMessageWithButton to SlackPoster interface**

In `internal/bot/result_listener.go`, update the `SlackPoster` interface:

```go
type SlackPoster interface {
	PostMessage(channelID, text, threadTS string)
	UpdateMessage(channelID, messageTS, text string)
	PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value string) (string, error)
}
```

- [ ] **Step 2: Update mockSlackPoster in tests**

In `internal/bot/result_listener_test.go`, add the method to `mockSlackPoster`:

```go
type mockSlackPoster struct {
	mu       sync.Mutex
	messages []string
	buttons  []string // track button posts
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
```

- [ ] **Step 3: Add PostMessageWithButton to slackPosterAdapter**

In `cmd/bot/main.go`, add the method to `slackPosterAdapter`:

```go
func (a *slackPosterAdapter) PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value string) (string, error) {
	return a.client.PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./...`
Expected: All tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/bot/result_listener.go internal/bot/result_listener_test.go cmd/bot/main.go
git commit -m "refactor: promote PostMessageWithButton to SlackPoster interface"
```

---

### Task 4: Refactor Watchdog to publish to ResultBus

**Files:**
- Modify: `internal/queue/watchdog.go`
- Create: `internal/queue/watchdog_test.go`
- Modify: `cmd/bot/main.go:187-199`

- [ ] **Step 1: Write the failing test**

Create `internal/queue/watchdog_test.go`:

```go
package queue

import (
	"context"
	"testing"
	"time"
)

func TestWatchdog_PublishesFailedResultOnTimeout(t *testing.T) {
	store := NewMemJobStore()
	store.Put(&Job{ID: "j1", SubmittedAt: time.Now().Add(-10 * time.Minute)})
	store.UpdateStatus("j1", JobRunning)

	results := NewInMemResultBus(10)
	defer results.Close()

	commands := NewInMemCommandBus(10)
	defer commands.Close()

	wd := NewWatchdog(store, commands, results, WatchdogConfig{
		JobTimeout:     1 * time.Minute,
		IdleTimeout:    0,
		PrepareTimeout: 0,
	})

	wd.check()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	ch, _ := results.Subscribe(ctx)
	select {
	case result := <-ch:
		if result.JobID != "j1" {
			t.Errorf("jobID = %q, want j1", result.JobID)
		}
		if result.Status != "failed" {
			t.Errorf("status = %q, want failed", result.Status)
		}
		if result.Error == "" {
			t.Error("error should contain timeout reason")
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for result on ResultBus")
	}

	// Verify job marked as failed in store (prevents re-tick).
	state, _ := store.Get("j1")
	if state.Status != JobFailed {
		t.Errorf("store status = %q, want failed", state.Status)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/queue/ -run TestWatchdog_PublishesFailedResultOnTimeout -v`
Expected: FAIL — `NewWatchdog` signature mismatch (still expects `StuckNotifier`).

- [ ] **Step 3: Refactor Watchdog**

Replace the full content of `internal/queue/watchdog.go`:

```go
package queue

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

type WatchdogConfig struct {
	JobTimeout     time.Duration
	IdleTimeout    time.Duration
	PrepareTimeout time.Duration
}

type Watchdog struct {
	store          JobStore
	commands       CommandBus
	results        ResultBus
	jobTimeout     time.Duration
	idleTimeout    time.Duration
	prepareTimeout time.Duration
	interval       time.Duration
}

func NewWatchdog(store JobStore, commands CommandBus, results ResultBus, cfg WatchdogConfig) *Watchdog {
	interval := cfg.JobTimeout / 3
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	return &Watchdog{
		store:          store,
		commands:       commands,
		results:        results,
		jobTimeout:     cfg.JobTimeout,
		idleTimeout:    cfg.IdleTimeout,
		prepareTimeout: cfg.PrepareTimeout,
		interval:       interval,
	}
}

func (w *Watchdog) Start(stop <-chan struct{}) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	slog.Info("job watchdog started",
		"job_timeout", w.jobTimeout,
		"idle_timeout", w.idleTimeout,
		"prepare_timeout", w.prepareTimeout,
		"check_interval", w.interval,
	)

	for {
		select {
		case <-ticker.C:
			w.check()
		case <-stop:
			slog.Info("job watchdog stopped")
			return
		}
	}
}

func (w *Watchdog) check() {
	all, err := w.store.ListAll()
	if err != nil {
		slog.Warn("watchdog: failed to list jobs", "error", err)
		return
	}

	now := time.Now()
	for _, state := range all {
		if state.Status == JobCompleted || state.Status == JobFailed {
			continue
		}

		// 1. Job-level timeout (all jobs)
		if now.Sub(state.Job.SubmittedAt) > w.jobTimeout {
			w.killAndPublish(state, "job timeout")
			continue
		}

		// 2. Prepare timeout (stuck in preparing stage)
		if state.Status == JobPreparing && w.prepareTimeout > 0 {
			if state.AgentStatus == nil || state.AgentStatus.LastEventAt.IsZero() {
				if !state.StartedAt.IsZero() && now.Sub(state.StartedAt) > w.prepareTimeout {
					w.killAndPublish(state, "prepare timeout")
					continue
				}
			}
		}

		// 3. Agent idle timeout (stream-json agents only)
		if w.idleTimeout > 0 && state.AgentStatus != nil && !state.AgentStatus.LastEventAt.IsZero() {
			if now.Sub(state.AgentStatus.LastEventAt) > w.idleTimeout {
				w.killAndPublish(state, "agent idle timeout")
				continue
			}
		}
	}
}

func (w *Watchdog) killAndPublish(state *JobState, reason string) {
	slog.Warn("watchdog: killing stuck job",
		"job_id", state.Job.ID, "status", state.Status, "reason", reason)

	if w.commands != nil {
		w.commands.Send(context.Background(), Command{JobID: state.Job.ID, Action: "kill"})
	}

	// Mark failed in store to prevent re-processing on next tick.
	w.store.UpdateStatus(state.Job.ID, JobFailed)

	// Publish to ResultBus — ResultListener handles Slack notification.
	if w.results != nil {
		w.results.Publish(context.Background(), &JobResult{
			JobID:      state.Job.ID,
			Status:     "failed",
			Error:      fmt.Sprintf("job terminated: %s", reason),
			FinishedAt: time.Now(),
		})
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/queue/ -run TestWatchdog_PublishesFailedResultOnTimeout -v`
Expected: PASS

- [ ] **Step 5: Update Watchdog wiring in main.go**

In `cmd/bot/main.go`, replace the watchdog block (lines 187-199):

```go
	// Job watchdog — detect stuck jobs and notify Slack.
	slackAdapter := &slackPosterAdapter{client: slackClient}
	watchdog := queue.NewWatchdog(jobStore, bundle.Commands, queue.WatchdogConfig{
		JobTimeout:     cfg.Queue.JobTimeout,
		IdleTimeout:    cfg.Queue.AgentIdleTimeout,
		PrepareTimeout: cfg.Queue.PrepareTimeout,
	}, func(job *queue.Job, status queue.JobStatus, reason string) {
		msg := queue.FormatStuckMessage(job, status, reason)
		slackAdapter.PostMessage(job.ChannelID, msg, job.ThreadTS)
		// Also clear dedup so user can re-trigger.
		handler.ClearThreadDedup(job.ChannelID, job.ThreadTS)
	})
```

with:

```go
	// Job watchdog — detect stuck jobs, publish failed results via ResultBus.
	watchdog := queue.NewWatchdog(jobStore, bundle.Commands, bundle.Results, queue.WatchdogConfig{
		JobTimeout:     cfg.Queue.JobTimeout,
		IdleTimeout:    cfg.Queue.AgentIdleTimeout,
		PrepareTimeout: cfg.Queue.PrepareTimeout,
	})
```

Remove the `slackAdapter` variable if it is no longer used elsewhere. (Check: it was only used here and for `resultListener` at line 181. The `resultListener` at line 181 creates its own `&slackPosterAdapter{client: slackClient}` inline, so this local `slackAdapter` is safe to remove.)

- [ ] **Step 6: Run all tests**

Run: `go test ./...`
Expected: All tests pass.

- [ ] **Step 7: Commit**

```bash
git add internal/queue/watchdog.go internal/queue/watchdog_test.go cmd/bot/main.go
git commit -m "refactor: watchdog publishes to ResultBus instead of StuckNotifier"
```

---

### Task 5: ResultListener unified failure handling with retry button

**Files:**
- Modify: `internal/bot/result_listener.go`
- Modify: `internal/bot/result_listener_test.go`

- [ ] **Step 1: Write failing test — retry button shown when RetryCount == 0**

Add to `internal/bot/result_listener_test.go`:

```go
func TestResultListener_FailedShowsRetryButton(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1", RetryCount: 0})

	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}
	dedupCleared := false

	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, nil,
		func(channelID, threadTS string) { dedupCleared = true })

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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/bot/ -run TestResultListener_FailedShowsRetryButton -v`
Expected: FAIL — `NewResultListener` signature mismatch.

- [ ] **Step 3: Write failing test — no button when RetryCount >= 1**

Add to `internal/bot/result_listener_test.go`:

```go
func TestResultListener_FailedNoButtonAfterRetry(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1", RetryCount: 1})

	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}
	dedupCleared := false

	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, nil,
		func(channelID, threadTS string) { dedupCleared = true })

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
```

- [ ] **Step 4: Write failing test — processedJobs dedup drops duplicate**

Add to `internal/bot/result_listener_test.go`:

```go
func TestResultListener_DedupDropsDuplicateResult(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1"})

	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	slackMock := &mockSlackPoster{}

	listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, nil,
		func(channelID, threadTS string) {})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go listener.Listen(ctx)

	// Publish two failed results for same job (simulates watchdog + worker race).
	bundle.Results.Publish(ctx, &queue.JobResult{JobID: "j1", Status: "failed", Error: "timeout"})
	bundle.Results.Publish(ctx, &queue.JobResult{JobID: "j1", Status: "failed", Error: "context cancelled"})

	time.Sleep(300 * time.Millisecond)

	slackMock.mu.Lock()
	defer slackMock.mu.Unlock()

	// Should only see one button post, not two.
	if len(slackMock.buttons) != 1 {
		t.Errorf("expected 1 button post (dedup), got %d", len(slackMock.buttons))
	}
}
```

- [ ] **Step 5: Implement ResultListener changes**

Replace `internal/bot/result_listener.go` entirely:

```go
package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"agentdock/internal/queue"
)

// SlackPoster abstracts Slack message posting for testing.
type SlackPoster interface {
	PostMessage(channelID, text, threadTS string)
	UpdateMessage(channelID, messageTS, text string)
	PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value string) (string, error)
}

// IssueCreator abstracts GitHub issue creation for testing.
type IssueCreator interface {
	CreateIssue(ctx context.Context, owner, repo, title, body string, labels []string) (string, error)
}

type ResultListener struct {
	results       queue.ResultBus
	store         queue.JobStore
	attachments   queue.AttachmentStore
	slack         SlackPoster
	github        IssueCreator
	onDedupClear  func(channelID, threadTS string)

	mu            sync.Mutex
	processedJobs map[string]bool
}

func NewResultListener(
	results queue.ResultBus,
	store queue.JobStore,
	attachments queue.AttachmentStore,
	slack SlackPoster,
	github IssueCreator,
	onDedupClear func(channelID, threadTS string),
) *ResultListener {
	return &ResultListener{
		results:       results,
		store:         store,
		attachments:   attachments,
		slack:         slack,
		github:        github,
		onDedupClear:  onDedupClear,
		processedJobs: make(map[string]bool),
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
	// Dedup guard: drop duplicate results for same job.
	r.mu.Lock()
	if r.processedJobs[result.JobID] {
		r.mu.Unlock()
		slog.Debug("dropping duplicate result", "job_id", result.JobID)
		return
	}
	r.processedJobs[result.JobID] = true
	r.mu.Unlock()

	state, err := r.store.Get(result.JobID)
	if err != nil {
		slog.Error("job not found for result", "job_id", result.JobID, "error", err)
		return
	}

	job := state.Job
	owner, repo := splitRepo(job.Repo)

	switch {
	case result.Status == "failed":
		r.handleFailure(job, state, result)

	case result.Confidence == "low":
		r.updateStatus(job, ":warning: 判斷不屬於此 repo，已跳過")
		r.clearDedup(job)

	case result.FilesFound == 0 || result.Questions >= 5:
		r.createAndPostIssue(ctx, job, owner, repo, result, true)
		r.clearDedup(job)

	default:
		r.createAndPostIssue(ctx, job, owner, repo, result, false)
		r.clearDedup(job)
	}

	// Cleanup attachments.
	r.attachments.Cleanup(ctx, result.JobID)
}

func (r *ResultListener) handleFailure(job *queue.Job, state *queue.JobState, result *queue.JobResult) {
	r.store.UpdateStatus(job.ID, queue.JobFailed)

	workerID := ""
	if state.AgentStatus != nil {
		workerID = state.AgentStatus.WorkerID
	}
	if workerID == "" {
		workerID = state.WorkerID
	}

	workerInfo := ""
	if workerID != "" {
		workerInfo = fmt.Sprintf(" | worker: %s", workerID)
	}

	if job.RetryCount < 1 {
		// Show retry button.
		text := fmt.Sprintf(":x: 分析失敗: %s\nrepo: `%s`%s", result.Error, job.Repo, workerInfo)
		r.slack.PostMessageWithButton(job.ChannelID, text, job.ThreadTS,
			"retry_job", "🔄 重試", job.ID)
		// Do NOT clear dedup — user should use retry button.
	} else {
		// Retry exhausted, no button.
		text := fmt.Sprintf(":x: 分析失敗（重試後仍失敗）: %s\nrepo: `%s`%s", result.Error, job.Repo, workerInfo)
		r.updateStatus(job, text)
		r.clearDedup(job)
	}
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

func (r *ResultListener) clearDedup(job *queue.Job) {
	if r.onDedupClear != nil {
		r.onDedupClear(job.ChannelID, job.ThreadTS)
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
```

- [ ] **Step 6: Update existing tests to pass new constructor arg**

In `internal/bot/result_listener_test.go`, update the three existing test functions to pass `nil` as the `onDedupClear` callback:

For `TestResultListener_CompletedCreatesIssue`:
```go
listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, githubMock, nil)
```

For `TestResultListener_FailedPostsError`:
```go
listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, nil, nil)
```

For `TestResultListener_LowConfidenceRejects`:
```go
listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, nil, nil)
```

- [ ] **Step 7: Update main.go to pass onDedupClear**

In `cmd/bot/main.go`, update the `resultListener` construction (around line 181):

```go
resultListener := bot.NewResultListener(bundle.Results, jobStore, bundle.Attachments,
    &slackPosterAdapter{client: slackClient}, issueClient,
    func(channelID, threadTS string) {
        handler.ClearThreadDedup(channelID, threadTS)
    })
```

- [ ] **Step 8: Run all tests**

Run: `go test ./...`
Expected: All tests pass, including the three new tests.

- [ ] **Step 9: Commit**

```bash
git add internal/bot/result_listener.go internal/bot/result_listener_test.go cmd/bot/main.go
git commit -m "feat: ResultListener unified failure handling with retry button"
```

---

### Task 6: Create RetryHandler

**Files:**
- Create: `internal/bot/retry_handler.go`
- Create: `internal/bot/retry_handler_test.go`

- [ ] **Step 1: Write failing test — successful retry**

Create `internal/bot/retry_handler_test.go`:

```go
package bot

import (
	"context"
	"testing"
	"time"

	"agentdock/internal/queue"
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
		Prompt:    "test prompt",
		Priority:  50,
		Skills:    map[string]string{"s1": "content"},
	}
	store.Put(original)
	store.UpdateStatus("j1", queue.JobFailed)

	q := &mockJobQueue{}
	slackMock := &mockSlackPoster{}

	handler := NewRetryHandler(store, q, slackMock)
	handler.Handle("C1", "j1", "msg-ts-1")

	// Verify new job was submitted.
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
	if newJob.Prompt != "test prompt" {
		t.Errorf("Prompt = %q, want test prompt", newJob.Prompt)
	}
	if newJob.UserID != "U1" {
		t.Errorf("UserID = %q, want U1", newJob.UserID)
	}
	if newJob.Priority != 50 {
		t.Errorf("Priority = %d, want 50", newJob.Priority)
	}

	// Verify old message was updated.
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

	// Verify new status message with cancel button was posted.
	if len(slackMock.buttons) != 1 {
		t.Fatalf("expected 1 button post, got %d", len(slackMock.buttons))
	}
}
```

- [ ] **Step 2: Write failing test — job not found (TTL expired)**

Add to `internal/bot/retry_handler_test.go`:

```go
func TestRetryHandler_JobNotFound(t *testing.T) {
	store := queue.NewMemJobStore()
	q := &mockJobQueue{}
	slackMock := &mockSlackPoster{}

	handler := NewRetryHandler(store, q, slackMock)
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
```

- [ ] **Step 3: Write failing test — job not in failed state (stale button)**

Add to `internal/bot/retry_handler_test.go`:

```go
func TestRetryHandler_IgnoresNonFailedJob(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "T1"})
	store.UpdateStatus("j1", queue.JobCompleted)

	q := &mockJobQueue{}
	slackMock := &mockSlackPoster{}

	handler := NewRetryHandler(store, q, slackMock)
	handler.Handle("C1", "j1", "msg-ts-1")

	if len(q.submitted) != 0 {
		t.Error("should not submit when job is not failed")
	}
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `go test ./internal/bot/ -run TestRetryHandler -v`
Expected: FAIL — `NewRetryHandler` not defined.

- [ ] **Step 5: Implement RetryHandler**

Create `internal/bot/retry_handler.go`:

```go
package bot

import (
	"context"
	"log/slog"
	"time"

	"agentdock/internal/logging"
	"agentdock/internal/queue"
)

// JobSubmitter abstracts queue submission for testing.
type JobSubmitter interface {
	Submit(ctx context.Context, job *queue.Job) error
}

type RetryHandler struct {
	store queue.JobStore
	queue JobSubmitter
	slack SlackPoster
}

func NewRetryHandler(store queue.JobStore, q JobSubmitter, slack SlackPoster) *RetryHandler {
	return &RetryHandler{store: store, queue: q, slack: slack}
}

func (h *RetryHandler) Handle(channelID, jobID, messagTS string) {
	state, err := h.store.Get(jobID)
	if err != nil {
		slog.Warn("retry: job not found", "job_id", jobID, "error", err)
		h.slack.UpdateMessage(channelID, messagTS, ":warning: 此任務已過期，請重新觸發")
		return
	}

	if state.Status != queue.JobFailed {
		slog.Info("retry: job not in failed state, ignoring", "job_id", jobID, "status", state.Status)
		return
	}

	original := state.Job

	// Update old message to indicate retry is queued.
	h.slack.UpdateMessage(channelID, messagTS, ":arrows_counterclockwise: 已重新排入佇列")

	// Create new job copying relevant fields.
	newJob := &queue.Job{
		ID:           logging.NewRequestID(),
		Priority:     original.Priority,
		ChannelID:    original.ChannelID,
		ThreadTS:     original.ThreadTS,
		UserID:       original.UserID,
		Repo:         original.Repo,
		Branch:       original.Branch,
		CloneURL:     original.CloneURL,
		Prompt:       original.Prompt,
		Skills:       original.Skills,
		RequestID:    logging.NewRequestID(),
		Attachments:  original.Attachments,
		RetryCount:   original.RetryCount + 1,
		RetryOfJobID: original.ID,
		SubmittedAt:  time.Now(),
	}

	// Put in store before posting button (so cancel_job can find it).
	h.store.Put(newJob)

	ctx := context.Background()
	if err := h.queue.Submit(ctx, newJob); err != nil {
		slog.Error("retry: failed to submit", "job_id", newJob.ID, "error", err)
		h.slack.PostMessage(channelID, ":x: 重試失敗: "+err.Error(), original.ThreadTS)
		return
	}

	// Post new status message with cancel button.
	msgTS, err := h.slack.PostMessageWithButton(original.ChannelID,
		":hourglass_flowing_sand: 重試中，正在處理你的請求...",
		original.ThreadTS, "cancel_job", "取消", newJob.ID)
	if err == nil {
		newJob.StatusMsgTS = msgTS
		h.store.Put(newJob) // update with StatusMsgTS
	}

	slog.Info("retry: new job submitted",
		"original_job_id", original.ID,
		"new_job_id", newJob.ID,
		"retry_count", newJob.RetryCount)
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/bot/ -run TestRetryHandler -v`
Expected: All 3 tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/bot/retry_handler.go internal/bot/retry_handler_test.go
git commit -m "feat: add RetryHandler for retry button interaction"
```

---

### Task 7: Route retry_job action in main.go

**Files:**
- Modify: `cmd/bot/main.go:310-334`

- [ ] **Step 1: Create and wire RetryHandler**

In `cmd/bot/main.go`, after the `resultListener` construction (around line 182), add:

```go
retryHandler := bot.NewRetryHandler(jobStore, coordinator, &slackPosterAdapter{client: slackClient})
```

- [ ] **Step 2: Add retry_job case to the block_actions switch**

In the `InteractionTypeBlockActions` switch (around line 310), add a new case before the `cancel_job` case:

```go
					case action.ActionID == "retry_job":
						retryHandler.Handle(cb.Channel.ID, action.Value, selectorTS)
```

The full switch block should look like:

```go
					switch {
					case action.ActionID == "repo_search" && action.SelectedOption.Value != "":
						wf.HandleSelection(cb.Channel.ID, action.ActionID, action.SelectedOption.Value, selectorTS)

					case strings.HasPrefix(action.ActionID, "repo_select"):
						wf.HandleSelection(cb.Channel.ID, action.ActionID, action.Value, selectorTS)

					case strings.HasPrefix(action.ActionID, "branch_select"):
						wf.HandleSelection(cb.Channel.ID, action.ActionID, action.Value, selectorTS)

					case strings.HasPrefix(action.ActionID, "description_action"):
						wf.HandleDescriptionAction(cb.Channel.ID, action.Value, selectorTS, cb.TriggerID)

					case action.ActionID == "retry_job":
						retryHandler.Handle(cb.Channel.ID, action.Value, selectorTS)

					case strings.HasPrefix(action.ActionID, "cancel_job"):
						// ... existing cancel_job code unchanged
					}
```

- [ ] **Step 3: Run all tests**

Run: `go test ./...`
Expected: All tests pass.

- [ ] **Step 4: Commit**

```bash
git add cmd/bot/main.go
git commit -m "feat: route retry_job button action to RetryHandler"
```

---

### Task 8: Worker identity (hostname + index)

**Files:**
- Modify: `internal/worker/pool.go:33-36` and `103-110` and `139-141`
- Modify: `cmd/bot/worker.go:70-82`
- Modify: `internal/worker/pool_test.go`

- [ ] **Step 1: Write failing test**

Add to `internal/worker/pool_test.go`:

```go
func TestPool_WorkerIDIncludesHostname(t *testing.T) {
	store := queue.NewMemJobStore()
	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	agentOutput := "Analysis done.\n\n===TRIAGE_RESULT===\n" + `{
  "status": "CREATED",
  "title": "Bug fix",
  "body": "## Problem\nSomething broke",
  "labels": ["bug"],
  "confidence": "high",
  "files_found": 3,
  "open_questions": 0
}`

	pool := NewPool(Config{
		Queue:       bundle.Queue,
		Attachments: bundle.Attachments,
		Results:     bundle.Results,
		Store:       store,
		Runner:      &mockRunner{output: agentOutput},
		RepoCache:   &mockRepo{path: "/tmp/test-repo"},
		WorkerCount: 1,
		Hostname:    "test-host",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool.Start(ctx)
	bundle.Attachments.Prepare(ctx, "j1", nil)
	bundle.Queue.Submit(ctx, &queue.Job{ID: "j1", Priority: 50, Prompt: "test"})

	ch, _ := bundle.Results.Subscribe(ctx)
	select {
	case <-ch:
		// Job completed, check worker ID was set.
		state, _ := store.Get("j1")
		if state.WorkerID == "" {
			t.Error("WorkerID should be set after execution")
		}
		if state.WorkerID != "test-host/worker-0" {
			t.Errorf("WorkerID = %q, want test-host/worker-0", state.WorkerID)
		}
	case <-ctx.Done():
		t.Fatal("timeout")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/worker/ -run TestPool_WorkerIDIncludesHostname -v`
Expected: FAIL — `Config` has no `Hostname` field.

- [ ] **Step 3: Add Hostname to Config and update Pool**

In `internal/worker/pool.go`, add `Hostname` to the `Config` struct:

```go
type Config struct {
	Queue          queue.JobQueue
	Attachments    queue.AttachmentStore
	Results        queue.ResultBus
	Store          queue.JobStore
	Runner         Runner
	RepoCache      RepoProvider
	WorkerCount    int
	Hostname       string
	SkillDirs      []string
	Commands       queue.CommandBus
	Status         queue.StatusBus
	StatusInterval time.Duration
}
```

In `executeWithTracking`, change the `statusAccumulator` init (line 110):

```go
	workerID := fmt.Sprintf("%s/worker-%d", p.cfg.Hostname, workerID)

	status := &statusAccumulator{
		jobID:    job.ID,
		workerID: workerID,
		alive:    true,
	}
```

Note: the method parameter is also called `workerID int` — rename it to `workerIndex` to avoid collision with the string ID. Update ALL references in the method signature and the logger line:

```go
func (p *Pool) executeWithTracking(ctx context.Context, workerIndex int, job *queue.Job) {
	logger := slog.With("worker_id", workerIndex, "job_id", job.ID)
	jobCtx, jobCancel := context.WithCancel(ctx)
	defer jobCancel()

	wID := fmt.Sprintf("%s/worker-%d", p.cfg.Hostname, workerIndex)

	status := &statusAccumulator{
		jobID:    job.ID,
		workerID: wID,
		alive:    true,
	}
```

The call site in `runWorker` (`p.executeWithTracking(ctx, id, job)`) doesn't change — `id` is already `int`.

- [ ] **Step 4: Call SetWorker after Ack**

In `executeWithTracking`, after the `Ack` call (around line 139-149), add `SetWorker`:

```go
	// Ack.
	if err := p.cfg.Queue.Ack(jobCtx, job.ID); err != nil {
		logger.Error("ack failed", "error", err)
		p.cfg.Results.Publish(ctx, &queue.JobResult{
			JobID: job.ID, Status: "failed", Error: fmt.Sprintf("ack failed: %v", err),
		})
		if stopReporter != nil {
			close(stopReporter)
		}
		return
	}

	p.cfg.Store.SetWorker(job.ID, wID)
```

- [ ] **Step 5: Pass Hostname in worker.go**

In `cmd/bot/worker.go`, get hostname and pass to Pool. Add to imports:

```go
import (
	"os"
	// ... existing imports
)
```

Before the `pool := worker.NewPool(...)` call, add:

```go
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
```

Add `Hostname` to the Config:

```go
	pool := worker.NewPool(worker.Config{
		Queue:          bundle.Queue,
		Attachments:    bundle.Attachments,
		Results:        bundle.Results,
		Store:          jobStore,
		Runner:         &agentRunnerAdapter{runner: agentRunner},
		RepoCache:      &repoCacheAdapter{cache: repoCache},
		WorkerCount:    cfg.Workers.Count,
		Hostname:       hostname,
		SkillDirs:      skillDirs,
		Commands:       bundle.Commands,
		Status:         bundle.Status,
		StatusInterval: cfg.Queue.StatusInterval,
	})
```

- [ ] **Step 6: Also pass Hostname in main.go (app mode)**

Check if main.go creates a Pool. Search for `worker.NewPool` in main.go:

In `cmd/bot/main.go`, if the app mode also creates a pool (it uses `bundle` and may have workers), find the same pattern and add `Hostname`. If not, this step is a no-op.

(In this codebase, the app mode in `main.go` does NOT create a `worker.Pool` — workers only run in `worker.go`. So this step is a no-op.)

- [ ] **Step 7: Run all tests**

Run: `go test ./...`
Expected: All tests pass.

- [ ] **Step 8: Commit**

```bash
git add internal/worker/pool.go internal/worker/pool_test.go cmd/bot/worker.go
git commit -m "feat: include hostname in worker ID for visibility"
```

---

### Task 9: Final integration verification

- [ ] **Step 1: Run full test suite**

Run: `go test ./... -count=1`
Expected: All tests pass.

- [ ] **Step 2: Build**

Run: `go build -o bot ./cmd/bot/`
Expected: Compiles without errors.

- [ ] **Step 3: Verify no leftover references**

Run: `grep -r 'StuckNotifier\|FormatStuckMessage' --include='*.go' internal/ cmd/`
Expected: No matches (these were removed in Task 4).

Run: `grep -r 'cfg\.Fallback' --include='*.go' internal/ cmd/`
Expected: No matches (renamed to Providers earlier).

- [ ] **Step 4: Commit (if any fixups needed)**

```bash
git add -A
git commit -m "chore: final cleanup for retry-on-failure feature"
```
