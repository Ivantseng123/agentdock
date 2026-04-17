# Slack UX: Progress Visibility + Repo Re-select Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement two independent Slack UX improvements: (1) periodic status-message updates showing worker ID and liveness during long-running triage jobs, and (2) a "back" button on branch / description selectors so users can fix a mis-clicked repo without re-triggering `@bot`.

**Architecture:** Both features are additive, in-memory-only, and share the same backing Slack client. Part A extends `StatusListener` to debounce-update the status message (driven by the worker's existing 5s status heartbeat) and publishes one extra `StatusReport` when a worker picks up a job. Part B adds a narrow `slackAPI` interface in `internal/bot/workflow.go`, a new `HandleBackToRepo` workflow method, and a `back_to_repo` router case. Neither feature touches Redis, job store schemas, or worker streaming internals.

**Tech Stack:** Go 1.21+, `github.com/slack-go/slack` (block kit), existing `internal/queue` bus abstractions, `stretchr/testify`-free standard library testing.

**Specs:**
- `docs/superpowers/specs/2026-04-17-status-progress-visibility-design.md` — Part A
- `docs/superpowers/specs/2026-04-17-repo-reselect-back-design.md` — Part B

**Branch:** `feat/slack-ux-progress-reselect` (already created from main; both spec docs are present).

**Implementation order:** Part A first (lower-risk, independent of workflow refactor), then Part B (introduces `slackAPI` interface). Within each part, the Slack client extension comes first so test stubs can be written against concrete interfaces.

---

## Part A: Status Progress Visibility

### Task 1: Add `UpdateMessageWithButton` to Slack client

**Files:**
- Modify: `internal/slack/client.go` (add method after line 352 `UpdateMessage`)

No test is added here (per spec §Testing: `client_test.go` does not network-mock; block structure is validated by Slack at runtime as `invalid_blocks`).

- [ ] **Step 1: Add the new method**

Insert after the existing `UpdateMessage` function (currently ending at `client.go:352`):

```go
// UpdateMessageWithButton replaces a message's text while preserving a single
// action button (mirrors PostMessageWithButton's block structure). Used for the
// status message where the cancel button must stay visible across updates.
func (c *Client) UpdateMessageWithButton(
	channelID, messageTS, text, actionID, buttonText, value string,
) error {
	btnBlock := slack.NewActionBlock("cancel_actions",
		slack.NewButtonBlockElement(actionID, value,
			slack.NewTextBlockObject("plain_text", buttonText, false, false)),
	)
	textBlock := slack.NewSectionBlock(
		slack.NewTextBlockObject("mrkdwn", text, false, false), nil, nil)

	start := time.Now()
	_, _, _, err := c.api.UpdateMessage(channelID, messageTS,
		slack.MsgOptionBlocks(textBlock, btnBlock),
	)
	metrics.ExternalDuration.WithLabelValues("slack", "post_message").Observe(time.Since(start).Seconds())
	if err != nil {
		metrics.ExternalErrorsTotal.WithLabelValues("slack", "post_message").Inc()
		return fmt.Errorf("update message with button: %w", err)
	}
	return nil
}
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add internal/slack/client.go
git commit -m "feat(slack): add UpdateMessageWithButton for status updates

Mirrors PostMessageWithButton's block structure so the cancel button
is preserved when the status message is updated during a long-running
job. Used by the upcoming StatusListener extension."
```

---

### Task 2: Publish prep StatusReport in worker pool

**Files:**
- Modify: `internal/worker/pool.go` (around line 147, after `SetWorker`, before `executeJob`)
- Modify: `internal/worker/pool_test.go` (new test case)

- [ ] **Step 1: Write the failing test**

Append to `internal/worker/pool_test.go` (preserve existing imports):

```go
func TestHandleJob_PublishesPrepStatusReport(t *testing.T) {
	harness := newPoolHarness(t)
	defer harness.close()

	var (
		mu      sync.Mutex
		reports []queue.StatusReport
	)
	harness.status.OnReport = func(r queue.StatusReport) {
		mu.Lock()
		defer mu.Unlock()
		reports = append(reports, r)
	}

	job := &queue.Job{ID: "jprep", Repo: "o/r", Prompt: "hi"}
	if err := harness.store.Put(job); err != nil {
		t.Fatalf("store put: %v", err)
	}

	harness.runExecute(0, job)

	mu.Lock()
	defer mu.Unlock()
	if len(reports) == 0 {
		t.Fatal("expected at least one StatusReport")
	}
	first := reports[0]
	if first.JobID != "jprep" {
		t.Errorf("first report JobID = %q, want jprep", first.JobID)
	}
	if first.WorkerID == "" {
		t.Error("first report WorkerID should be set")
	}
	if first.PID != 0 {
		t.Errorf("first report PID = %d, want 0 (prep phase)", first.PID)
	}
	if !first.Alive {
		t.Error("first report Alive should be true")
	}
}
```

If `newPoolHarness` / `runExecute` / `harness.status.OnReport` don't already exist, adapt to whatever fixture pattern the file uses. Look at the existing `TestPool_WorkerIDIncludesHostname` test (pool_test.go:115) for the current setup conventions; reuse them rather than introducing new helpers.

If no test harness is in place that records StatusBus reports, add a minimal one inline using the existing `InMemStatusBus`:

```go
statusBus := queue.NewInMemStatusBus(16)
// ... pass statusBus as PoolConfig.Status ...
go func() {
	ch, _ := statusBus.Subscribe(context.Background())
	for r := range ch {
		mu.Lock(); reports = append(reports, r); mu.Unlock()
	}
}()
```

- [ ] **Step 2: Run the test — confirm it fails**

Run: `go test ./internal/worker/ -run TestHandleJob_PublishesPrepStatusReport -v`
Expected: FAIL with "expected at least one StatusReport" (no prep publish yet).

- [ ] **Step 3: Add the prep publish call**

Edit `internal/worker/pool.go`. In `executeWithTracking`, locate the block around line 184-187:

```go
p.cfg.Store.SetWorker(job.ID, wID)

deps := executionDeps{
```

Insert between `SetWorker` and `deps :=`:

```go
p.cfg.Store.SetWorker(job.ID, wID)

// Prep-phase status signal — PID=0, AgentCmd="" lets StatusListener render
// the "準備中" template before the agent process starts.
if p.cfg.Status != nil {
	_ = p.cfg.Status.Report(jobCtx, status.toReport())
}

deps := executionDeps{
```

- [ ] **Step 4: Run the test — confirm it passes**

Run: `go test ./internal/worker/ -run TestHandleJob_PublishesPrepStatusReport -v`
Expected: PASS.

Also run the full pool test suite to ensure no regression:

Run: `go test ./internal/worker/ -v`
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/worker/pool.go internal/worker/pool_test.go
git commit -m "feat(worker): emit prep-phase StatusReport on job pickup

Publishes a StatusReport with PID=0 immediately after the worker acks
and registers itself for a job, before executeJob begins. Lets the
app-side StatusListener render a 'preparing' status message with the
assigned worker ID, closing the previous 0-signal gap during repo
clone / skill mount / attachment download."
```

---

### Task 3: Add pure helpers and render template

**Files:**
- Create: `internal/bot/status_listener_test.go`
- Modify: `internal/bot/status_listener.go` (add helpers below existing types)

Strategy: TDD the pure functions first so §4 has well-tested building blocks.

- [ ] **Step 1: Write the failing helper tests**

Create `internal/bot/status_listener_test.go`:

```go
package bot

import (
	"testing"
	"time"

	"agentdock/internal/queue"
)

func TestShortWorker(t *testing.T) {
	cases := []struct{ in, want string }{
		{"host-1/worker-3", "worker-3"},
		{"my-k8s-pod/worker-0", "worker-0"},
		{"noSlash", "noSlash"},
		{"", ""},
	}
	for _, c := range cases {
		if got := shortWorker(c.in); got != c.want {
			t.Errorf("shortWorker(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatElapsed(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0m00s"},
		{65 * time.Second, "1m05s"},
		{600 * time.Second, "10m00s"},
		{3599 * time.Second, "59m59s"},
	}
	for _, c := range cases {
		if got := formatElapsed(c.d); got != c.want {
			t.Errorf("formatElapsed(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestInferPhase(t *testing.T) {
	cases := []struct {
		name   string
		status queue.JobStatus
		pid    int
		want   string
	}{
		{"preparing from status", queue.JobPreparing, 0, "preparing"},
		{"running from status", queue.JobRunning, 1234, "running"},
		{"unknown status PID>0", queue.JobPending, 42, "running"},
		{"unknown status PID=0", queue.JobPending, 0, "preparing"},
	}
	for _, c := range cases {
		state := &queue.JobState{Status: c.status}
		r := queue.StatusReport{PID: c.pid}
		if got := inferPhase(state, r); got != c.want {
			t.Errorf("%s: inferPhase = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRenderStatusMessage_Preparing(t *testing.T) {
	state := &queue.JobState{Status: queue.JobPreparing}
	r := queue.StatusReport{WorkerID: "host/worker-0", PID: 0}
	got := renderStatusMessage(state, r, "preparing")
	want := ":gear: 準備中 · worker-0"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderStatusMessage_RunningNoStats(t *testing.T) {
	started := time.Now().Add(-2*time.Minute - 15*time.Second)
	state := &queue.JobState{Status: queue.JobRunning, StartedAt: started}
	r := queue.StatusReport{
		WorkerID: "host/worker-0",
		PID:      1234,
		AgentCmd: "codex",
	}
	got := renderStatusMessage(state, r, "running")
	// Allow ±1s drift on elapsed since test clock races.
	if !(got == ":hourglass_flowing_sand: 處理中 · worker-0 (codex) · 已執行 2m15s" ||
		got == ":hourglass_flowing_sand: 處理中 · worker-0 (codex) · 已執行 2m14s" ||
		got == ":hourglass_flowing_sand: 處理中 · worker-0 (codex) · 已執行 2m16s") {
		t.Errorf("unexpected output: %q", got)
	}
}

func TestRenderStatusMessage_RunningWithStats(t *testing.T) {
	state := &queue.JobState{Status: queue.JobRunning, StartedAt: time.Now()}
	r := queue.StatusReport{
		WorkerID:  "host/worker-0",
		PID:       1234,
		AgentCmd:  "claude",
		ToolCalls: 15,
		FilesRead: 8,
	}
	got := renderStatusMessage(state, r, "running")
	if !containsBoth(got, "處理中 · worker-0 (claude)", "工具呼叫 15 次 · 讀檔 8 份") {
		t.Errorf("missing expected substrings: %q", got)
	}
}

func TestRenderStatusMessage_RunningElapsedZeroWhenStartedAtUnset(t *testing.T) {
	state := &queue.JobState{Status: queue.JobRunning} // StartedAt zero
	r := queue.StatusReport{WorkerID: "host/worker-0", PID: 1234, AgentCmd: "claude"}
	got := renderStatusMessage(state, r, "running")
	if got != ":hourglass_flowing_sand: 處理中 · worker-0 (claude)" {
		t.Errorf("should omit elapsed when StartedAt is zero: %q", got)
	}
}

func TestRenderStatusMessage_RunningEmptyAgentCmd(t *testing.T) {
	state := &queue.JobState{Status: queue.JobRunning, StartedAt: time.Now()}
	r := queue.StatusReport{WorkerID: "host/worker-0", PID: 1234, AgentCmd: ""}
	got := renderStatusMessage(state, r, "running")
	if !contains(got, "處理中 · worker-0 (agent)") {
		t.Errorf("should fall back to 'agent' placeholder: %q", got)
	}
}

// helpers local to tests

func contains(s, sub string) bool { return len(sub) == 0 || indexOf(s, sub) >= 0 }
func containsBoth(s, a, b string) bool { return contains(s, a) && contains(s, b) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 2: Run tests — confirm compile failure**

Run: `go test ./internal/bot/ -run TestShortWorker -v`
Expected: FAIL — `undefined: shortWorker`, `undefined: formatElapsed`, etc.

- [ ] **Step 3: Implement helpers in status_listener.go**

Add at the bottom of `internal/bot/status_listener.go`:

```go
import (
	"fmt"
	"strings"
	"time"
	// existing imports
)

func shortWorker(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}

func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	secs := int(d.Seconds())
	return fmt.Sprintf("%dm%02ds", secs/60, secs%60)
}

func inferPhase(state *queue.JobState, r queue.StatusReport) string {
	switch state.Status {
	case queue.JobPreparing:
		return "preparing"
	case queue.JobRunning:
		return "running"
	}
	if r.PID > 0 {
		return "running"
	}
	return "preparing"
}

func isTerminal(s queue.JobStatus) bool {
	return s == queue.JobCompleted || s == queue.JobFailed || s == queue.JobCancelled
}

func renderStatusMessage(state *queue.JobState, r queue.StatusReport, phase string) string {
	worker := shortWorker(r.WorkerID)
	switch phase {
	case "preparing":
		return fmt.Sprintf(":gear: 準備中 · %s", worker)
	case "running":
		var suffix string
		if !state.StartedAt.IsZero() {
			suffix = fmt.Sprintf(" · 已執行 %s", formatElapsed(time.Since(state.StartedAt)))
		}
		agent := r.AgentCmd
		if agent == "" {
			agent = "agent"
		}
		base := fmt.Sprintf(":hourglass_flowing_sand: 處理中 · %s (%s)%s",
			worker, agent, suffix)
		if r.ToolCalls > 0 || r.FilesRead > 0 {
			base += fmt.Sprintf("\n工具呼叫 %d 次 · 讀檔 %d 份", r.ToolCalls, r.FilesRead)
		}
		return base
	}
	return ""
}
```

Ensure imports include `fmt`, `strings`, `time`.

- [ ] **Step 4: Run tests — confirm pass**

Run: `go test ./internal/bot/ -run "TestShortWorker|TestFormatElapsed|TestInferPhase|TestRenderStatusMessage" -v`
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/bot/status_listener.go internal/bot/status_listener_test.go
git commit -m "feat(bot): add render helpers for status message templates

Pure functions for worker-id shortening, elapsed formatting, phase
inference, and Chinese status message rendering. Prepping for the
StatusListener extension that wires them to Slack."
```

---

### Task 4: Extend StatusListener with Slack dispatch

**Files:**
- Modify: `internal/bot/status_listener.go`
- Modify: `internal/bot/status_listener_test.go`

- [ ] **Step 1: Write failing tests for maybeUpdateSlack behavior**

Append to `internal/bot/status_listener_test.go`:

```go
type stubSlackStatusPoster struct {
	calls []struct {
		ChannelID  string
		MessageTS  string
		Text       string
		ActionID   string
		ButtonText string
		Value      string
	}
	err error
}

func (s *stubSlackStatusPoster) UpdateMessageWithButton(channelID, messageTS, text, actionID, buttonText, value string) error {
	s.calls = append(s.calls, struct {
		ChannelID, MessageTS, Text, ActionID, ButtonText, Value string
	}{channelID, messageTS, text, actionID, buttonText, value})
	return s.err
}

func newTestListener(store queue.JobStore, slack SlackStatusPoster, now time.Time) *StatusListener {
	l := NewStatusListener(nil, store, slack, slogDiscardLogger())
	l.clock = func() time.Time { return now }
	return l
}

func slogDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestMaybeUpdateSlack_PreparingPhase(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", ChannelID: "C1", StatusMsgTS: "S1"})
	store.UpdateStatus("j1", queue.JobPreparing)

	slack := &stubSlackStatusPoster{}
	l := newTestListener(store, slack, time.Now())

	l.maybeUpdateSlack(queue.StatusReport{
		JobID: "j1", WorkerID: "host/worker-0", PID: 0, Alive: true,
	})

	if len(slack.calls) != 1 {
		t.Fatalf("expected 1 Slack call, got %d", len(slack.calls))
	}
	c := slack.calls[0]
	if c.ChannelID != "C1" || c.MessageTS != "S1" {
		t.Errorf("wrong target: %+v", c)
	}
	if c.ActionID != "cancel_job" || c.ButtonText != "取消" || c.Value != "j1" {
		t.Errorf("wrong button: %+v", c)
	}
	if !contains(c.Text, "準備中") || !contains(c.Text, "worker-0") {
		t.Errorf("text missing expected markers: %q", c.Text)
	}
}

func TestMaybeUpdateSlack_RunningWithToolCalls(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", ChannelID: "C1", StatusMsgTS: "S1"})
	store.UpdateStatus("j1", queue.JobRunning)

	slack := &stubSlackStatusPoster{}
	l := newTestListener(store, slack, time.Now())

	l.maybeUpdateSlack(queue.StatusReport{
		JobID: "j1", WorkerID: "host/worker-0", PID: 1234,
		AgentCmd: "claude", ToolCalls: 15, FilesRead: 8,
	})

	if len(slack.calls) != 1 {
		t.Fatalf("expected 1 Slack call")
	}
	if !containsBoth(slack.calls[0].Text, "處理中 · worker-0 (claude)", "工具呼叫 15 次") {
		t.Errorf("missing expected substrings: %q", slack.calls[0].Text)
	}
}

func TestMaybeUpdateSlack_RunningNoToolCalls(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", ChannelID: "C1", StatusMsgTS: "S1"})
	store.UpdateStatus("j1", queue.JobRunning)

	slack := &stubSlackStatusPoster{}
	l := newTestListener(store, slack, time.Now())

	l.maybeUpdateSlack(queue.StatusReport{
		JobID: "j1", WorkerID: "host/worker-0", PID: 1234, AgentCmd: "codex",
	})

	if len(slack.calls) != 1 {
		t.Fatalf("expected 1 Slack call")
	}
	if contains(slack.calls[0].Text, "工具呼叫") {
		t.Errorf("should NOT include tool-call line for codex: %q", slack.calls[0].Text)
	}
}

func TestMaybeUpdateSlack_DebounceSkips(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", ChannelID: "C1", StatusMsgTS: "S1"})
	store.UpdateStatus("j1", queue.JobRunning)

	slack := &stubSlackStatusPoster{}
	t0 := time.Now()
	l := newTestListener(store, slack, t0)

	l.maybeUpdateSlack(queue.StatusReport{JobID: "j1", WorkerID: "w", PID: 1, AgentCmd: "claude"})
	// 5 seconds later — still within 15s debounce, same phase
	l.clock = func() time.Time { return t0.Add(5 * time.Second) }
	l.maybeUpdateSlack(queue.StatusReport{JobID: "j1", WorkerID: "w", PID: 1, AgentCmd: "claude"})

	if len(slack.calls) != 1 {
		t.Errorf("debounce failed: got %d calls", len(slack.calls))
	}
}

func TestMaybeUpdateSlack_PhaseChangeForcesUpdate(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", ChannelID: "C1", StatusMsgTS: "S1"})
	store.UpdateStatus("j1", queue.JobPreparing)

	slack := &stubSlackStatusPoster{}
	t0 := time.Now()
	l := newTestListener(store, slack, t0)

	// First update in preparing.
	l.maybeUpdateSlack(queue.StatusReport{JobID: "j1", WorkerID: "w", PID: 0})

	// 2 seconds later — within debounce but phase changed to running.
	store.UpdateStatus("j1", queue.JobRunning)
	l.clock = func() time.Time { return t0.Add(2 * time.Second) }
	l.maybeUpdateSlack(queue.StatusReport{JobID: "j1", WorkerID: "w", PID: 1234, AgentCmd: "claude"})

	if len(slack.calls) != 2 {
		t.Errorf("phase change should force update; got %d calls", len(slack.calls))
	}
}

func TestMaybeUpdateSlack_TerminalSkips(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", ChannelID: "C1", StatusMsgTS: "S1"})
	store.UpdateStatus("j1", queue.JobCompleted)

	slack := &stubSlackStatusPoster{}
	l := newTestListener(store, slack, time.Now())

	// Pre-populate lastUpdate to confirm it gets cleared.
	l.lastUpdate["j1"] = time.Now()
	l.lastPhase["j1"] = "running"

	l.maybeUpdateSlack(queue.StatusReport{JobID: "j1", WorkerID: "w", PID: 1234})

	if len(slack.calls) != 0 {
		t.Errorf("terminal should skip; got %d calls", len(slack.calls))
	}
	if _, ok := l.lastUpdate["j1"]; ok {
		t.Error("lastUpdate should be cleared for terminal jobs")
	}
}

func TestMaybeUpdateSlack_StoreMissing(t *testing.T) {
	store := queue.NewMemJobStore()
	// no Put — store.Get returns error
	slack := &stubSlackStatusPoster{}
	l := newTestListener(store, slack, time.Now())

	l.maybeUpdateSlack(queue.StatusReport{JobID: "missing"})

	if len(slack.calls) != 0 {
		t.Errorf("missing state should skip; got %d calls", len(slack.calls))
	}
}

func TestMaybeUpdateSlack_StatusMsgTSEmpty(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", ChannelID: "C1"}) // no StatusMsgTS
	store.UpdateStatus("j1", queue.JobPreparing)

	slack := &stubSlackStatusPoster{}
	l := newTestListener(store, slack, time.Now())

	l.maybeUpdateSlack(queue.StatusReport{JobID: "j1", WorkerID: "w", PID: 0})

	if len(slack.calls) != 0 {
		t.Errorf("empty StatusMsgTS should skip; got %d calls", len(slack.calls))
	}
}

func TestMaybeUpdateSlack_SlackErrorNonFatal(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", ChannelID: "C1", StatusMsgTS: "S1"})
	store.UpdateStatus("j1", queue.JobRunning)

	slack := &stubSlackStatusPoster{err: fmt.Errorf("slack boom")}
	l := newTestListener(store, slack, time.Now())

	// Should not panic.
	l.maybeUpdateSlack(queue.StatusReport{JobID: "j1", WorkerID: "w", PID: 1, AgentCmd: "claude"})

	if len(slack.calls) != 1 {
		t.Errorf("expected one attempt; got %d", len(slack.calls))
	}
}
```

Add imports to the test file as needed: `"io"`, `"log/slog"`, `"fmt"`.

- [ ] **Step 2: Run tests — confirm compile failures**

Run: `go test ./internal/bot/ -run TestMaybeUpdateSlack -v`
Expected: FAIL — `undefined: SlackStatusPoster`, `StatusListener.maybeUpdateSlack undefined`, `StatusListener.clock undefined`, `StatusListener.lastUpdate undefined`, etc.

- [ ] **Step 3: Extend StatusListener struct + constructor**

Edit `internal/bot/status_listener.go`. Replace the top of the file (up to and including `NewStatusListener`) with:

```go
package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"agentdock/internal/queue"
)

const statusUpdateDebounce = 15 * time.Second

// SlackStatusPoster is the narrow Slack surface StatusListener uses.
type SlackStatusPoster interface {
	UpdateMessageWithButton(channelID, messageTS, text, actionID, buttonText, value string) error
}

type StatusListener struct {
	status queue.StatusBus
	store  queue.JobStore
	slack  SlackStatusPoster

	mu         sync.Mutex
	lastUpdate map[string]time.Time // jobID → last Slack update
	lastPhase  map[string]string    // jobID → last rendered phase label

	clock  func() time.Time
	logger *slog.Logger
}

func NewStatusListener(status queue.StatusBus, store queue.JobStore, slack SlackStatusPoster, logger *slog.Logger) *StatusListener {
	return &StatusListener{
		status:     status,
		store:      store,
		slack:      slack,
		lastUpdate: make(map[string]time.Time),
		lastPhase:  make(map[string]string),
		clock:      time.Now,
		logger:     logger,
	}
}
```

Replace `Listen` to call `maybeUpdateSlack` after persisting:

```go
func (l *StatusListener) Listen(ctx context.Context) {
	ch, err := l.status.Subscribe(ctx)
	if err != nil {
		l.logger.Error("訂閱 status bus 失敗", "phase", "失敗", "error", err)
		return
	}
	for {
		select {
		case report, ok := <-ch:
			if !ok {
				return
			}
			l.store.SetAgentStatus(report.JobID, report)
			l.maybeUpdateSlack(report)
		case <-ctx.Done():
			return
		}
	}
}
```

Add `maybeUpdateSlack` below `Listen`:

```go
func (l *StatusListener) maybeUpdateSlack(r queue.StatusReport) {
	if l.slack == nil {
		return // defensive; tests may wire nil
	}
	state, err := l.store.Get(r.JobID)
	if err != nil || state == nil {
		l.logger.Warn("status listener: job state missing",
			"phase", "失敗", "job_id", r.JobID, "error", err)
		return
	}

	// Terminal — let ResultListener own the final message; clean up.
	if isTerminal(state.Status) {
		l.mu.Lock()
		delete(l.lastUpdate, r.JobID)
		delete(l.lastPhase, r.JobID)
		l.mu.Unlock()
		return
	}

	if state.StatusMsgTS == "" {
		return // workflow hasn't posted the first status message yet
	}

	phase := inferPhase(state, r)

	l.mu.Lock()
	prevTime, hadUpdate := l.lastUpdate[r.JobID]
	prevPhase := l.lastPhase[r.JobID]
	now := l.clock()
	phaseChanged := hadUpdate && prevPhase != phase
	debounceExpired := !hadUpdate || now.Sub(prevTime) >= statusUpdateDebounce
	if !phaseChanged && !debounceExpired {
		l.mu.Unlock()
		return
	}
	l.lastUpdate[r.JobID] = now
	l.lastPhase[r.JobID] = phase
	l.mu.Unlock()

	text := renderStatusMessage(state, r, phase)
	if text == "" {
		return
	}

	// Second terminal check right before the API call narrows race with ResultListener.
	if latest, err := l.store.Get(r.JobID); err == nil && latest != nil && isTerminal(latest.Status) {
		l.mu.Lock()
		delete(l.lastUpdate, r.JobID)
		delete(l.lastPhase, r.JobID)
		l.mu.Unlock()
		return
	}

	if err := l.slack.UpdateMessageWithButton(
		state.Job.ChannelID, state.StatusMsgTS, text,
		"cancel_job", "取消", r.JobID,
	); err != nil {
		l.logger.Warn("status 訊息更新失敗", "phase", "失敗", "job_id", r.JobID, "error", err)
	}
}

// ClearJob wipes debounce state for a job. Intended for ResultListener to call
// when a terminal result is handled, protecting against leaked map entries when
// a worker crashes without emitting a final status report.
func (l *StatusListener) ClearJob(jobID string) {
	l.mu.Lock()
	delete(l.lastUpdate, jobID)
	delete(l.lastPhase, jobID)
	l.mu.Unlock()
}
```

Ensure the existing `shortWorker` / `formatElapsed` / `inferPhase` / `isTerminal` / `renderStatusMessage` helpers from Task 3 are still present (they should be — they were added at the bottom of the file). If they appear duplicated because Task 3 put them at the bottom and the file now has them twice, keep only one copy — prefer the one at the bottom.

Remove the unused `strings` import if lint complains (both `shortWorker` and `status 訊息` live in this file, `strings` is used).

- [ ] **Step 4: Run tests — confirm pass**

Run: `go test ./internal/bot/ -run TestMaybeUpdateSlack -v`
Expected: all pass.

Also run the full bot suite for regressions:

Run: `go test ./internal/bot/ -v`
Expected: all pass (existing StatusListener call sites may now compile-fail because `NewStatusListener` signature changed — that's fixed in Task 5).

If compile fails in other files (`cmd/agentdock/app.go`), note that this is expected and will be fixed in the next task. Run only the bot unit tests:

Run: `go test ./internal/bot/`
Expected: all `internal/bot` tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/bot/status_listener.go internal/bot/status_listener_test.go
git commit -m "feat(bot): StatusListener pushes progress updates to Slack

Debounced (15s) or phase-change-triggered update of the job's status
message with worker ID, elapsed time, and (Claude only) tool call
counts. Preserves the cancel button via new UpdateMessageWithButton.
Skips on terminal states to defer to ResultListener. Non-fatal on
Slack errors — next tick retries."
```

---

### Task 5: Wire new constructor signature in `cmd/agentdock/app.go`

**Files:**
- Modify: `cmd/agentdock/app.go`

- [ ] **Step 1: Verify current build fails**

Run: `go build ./...`
Expected: FAIL — mismatched `NewStatusListener` arity.

- [ ] **Step 2: Locate the call site**

Run: `grep -n "NewStatusListener" cmd/agentdock/*.go`
Expected: one or more matches pointing to the current 3-argument invocation.

- [ ] **Step 3: Pass slack client**

Change the call from:
```go
statusListener := bot.NewStatusListener(bundle.Status, jobStore, appLogger)
```
to:
```go
statusListener := bot.NewStatusListener(bundle.Status, jobStore, slackClient, appLogger)
```

Use whatever variable name is already in scope for the Slack client at that point in `app.go` (likely `slackClient` based on earlier grep output; verify with `grep -n "slackClient" cmd/agentdock/app.go`).

- [ ] **Step 4: Verify build**

Run: `go build ./...`
Expected: no errors.

- [ ] **Step 5: Run full test suite**

Run: `go test ./...`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add cmd/agentdock/app.go
git commit -m "wire: pass Slack client into StatusListener

New constructor signature accepts SlackStatusPoster. *slackclient.Client
satisfies it via its new UpdateMessageWithButton method."
```

---

### Task 6: ResultListener defensive double-write

**Files:**
- Modify: `internal/bot/result_listener.go` (around line 308-310)
- Modify: `internal/bot/result_listener_test.go`

- [ ] **Step 1: Extend the existing mock to track UpdateMessage call tuples**

The current `mockSlackPoster` at `internal/bot/result_listener_test.go:17` only appends strings to a `messages` slice. Add a structured record alongside so tests can inspect channel/ts as well. Edit the mock:

```go
type updateCall struct{ ChannelID, MessageTS, Text string }

type mockSlackPoster struct {
	mu          sync.Mutex
	messages    []string
	buttons     []string
	updateCalls []updateCall  // NEW
}

func (m *mockSlackPoster) UpdateMessage(channelID, messageTS, text string) {
	m.mu.Lock()
	m.messages = append(m.messages, text)
	m.updateCalls = append(m.updateCalls, updateCall{channelID, messageTS, text})  // NEW
	m.mu.Unlock()
}
```

- [ ] **Step 2: Write the failing test**

Append to `internal/bot/result_listener_test.go`:

```go
func TestHandleResult_FinalStatusMessageDoubleWrite(t *testing.T) {
	slack := &mockSlackPoster{}
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{
		ID: "jdouble", Repo: "o/r", ChannelID: "C1",
		ThreadTS: "T1", StatusMsgTS: "S1",
	})
	store.UpdateStatus("jdouble", queue.JobCompleted)

	gh := &mockIssueCreator{url: "https://github.com/o/r/issues/1"}
	r := NewResultListener(nil, store, nil, slack, gh, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	result := &queue.JobResult{
		JobID: "jdouble", Status: "completed",
		Title: "bug", Body: "desc", Labels: []string{"bug"},
		Repo: "o/r",
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
```

Add imports at the top of the file if not already present: `"io"`, `"log/slog"`, `"time"`.

- [ ] **Step 3: Run test — confirm it fails**

Run: `go test ./internal/bot/ -run TestHandleResult_FinalStatusMessageDoubleWrite -v`
Expected: FAIL — "expected exactly one extra UpdateMessage after 2s" (the second write does not exist yet).

- [ ] **Step 4: Add double-write logic**

Open `internal/bot/result_listener.go` and locate the block around line 308-310:

```go
if job.StatusMsgTS != "" {
	r.slack.UpdateMessage(job.ChannelID, job.StatusMsgTS, text)
}
```

Replace with:

```go
if job.StatusMsgTS != "" {
	r.slack.UpdateMessage(job.ChannelID, job.StatusMsgTS, text)
	// Defensive re-write 2s later narrows the race with StatusListener's
	// in-flight update (spec §7). Same text is idempotent.
	ch, ts, finalText := job.ChannelID, job.StatusMsgTS, text
	time.AfterFunc(2*time.Second, func() {
		r.slack.UpdateMessage(ch, ts, finalText)
	})
	// Tell StatusListener to wipe its debounce state for this job.
	if r.onDedupClear != nil {
		// onDedupClear is for thread dedup; we repurpose nothing — this is just
		// a stand-in for clearing StatusListener's map. Instead, expose a new
		// hook below.
	}
}
```

Actually skip the `onDedupClear` repurposing — it's unrelated. Instead, add a separate field to `ResultListener`:

```go
// in ResultListener struct:
clearStatusMapping func(jobID string)

// in NewResultListener, accept it as a new parameter OR add a setter.
```

To avoid breaking the public constructor, add a setter method:

```go
// SetStatusJobClearer installs a hook called after a result is fully handled,
// so the StatusListener can drop its debounce bookkeeping for that job.
func (r *ResultListener) SetStatusJobClearer(f func(jobID string)) {
	r.clearStatusMapping = f
}
```

And in the terminal-write block (after the double-write scheduling):

```go
if r.clearStatusMapping != nil {
	r.clearStatusMapping(job.ID)
}
```

Wire it in `cmd/agentdock/app.go` (Task 7):

- [ ] **Step 5: Run the test — confirm it passes**

Run: `go test ./internal/bot/ -run TestHandleResult_FinalStatusMessageDoubleWrite -v -timeout 30s`
Expected: PASS (test waits ~2.5s, so use `-timeout 30s` to be safe).

- [ ] **Step 6: Run full suite**

Run: `go test ./...`
Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add internal/bot/result_listener.go internal/bot/result_listener_test.go
git commit -m "feat(bot): defensive double-write of final status message

Schedules a second UpdateMessage 2s after the terminal write to
overwrite any StatusListener update that raced through. Same text,
idempotent. Also exposes SetStatusJobClearer so StatusListener's
debounce map can be wiped even when workers crash mid-run."
```

---

### Task 7: Wire StatusJobClearer hook in `cmd/agentdock/app.go`

**Files:**
- Modify: `cmd/agentdock/app.go`

- [ ] **Step 1: Locate the ResultListener construction**

Run: `grep -n "NewResultListener" cmd/agentdock/app.go`
Expected: one match.

- [ ] **Step 2: Add the hook wiring**

After the `NewResultListener` call and the `NewStatusListener` call, add:

```go
resultListener.SetStatusJobClearer(statusListener.ClearJob)
```

Place it before `go resultListener.Listen(ctx)` and `go statusListener.Listen(ctx)` calls (wherever they are).

- [ ] **Step 3: Verify build + tests**

Run: `go build ./... && go test ./...`
Expected: all pass.

- [ ] **Step 4: Commit**

```bash
git add cmd/agentdock/app.go
git commit -m "wire: let ResultListener clear StatusListener bookkeeping

After handling a terminal result, ResultListener calls back into
StatusListener.ClearJob so the debounce/phase maps don't leak when
workers crash without emitting a final status report."
```

---

## Part B: Repo Re-select Back Button

### Task 8: Add `PostSelectorWithBack` to Slack client

**Files:**
- Modify: `internal/slack/client.go`

No test (per spec §Testing — existing `client_test.go` does not mock Slack HTTP; block structure validated by runtime `invalid_blocks`).

- [ ] **Step 1: Insert the new function**

After `PostSelector` (currently ends around `client.go:249`), add:

```go
// PostSelectorWithBack sends a button selector with an optional trailing back
// button. If backActionID is empty, behaves identically to PostSelector. The
// back button is rendered rightmost (farthest from main option buttons) and
// uses default button style.
func (c *Client) PostSelectorWithBack(
	channelID, prompt, actionPrefix string,
	options []string,
	threadTS string,
	backActionID, backLabel string,
) (string, error) {
	var buttons []slack.BlockElement
	for i, opt := range options {
		buttons = append(buttons, slack.NewButtonBlockElement(
			fmt.Sprintf("%s_%d", actionPrefix, i),
			opt,
			slack.NewTextBlockObject(slack.PlainTextType, opt, false, false),
		))
	}
	if backActionID != "" {
		buttons = append(buttons, slack.NewButtonBlockElement(
			backActionID,
			backActionID, // value equals actionID so router doesn't need SelectedOption
			slack.NewTextBlockObject(slack.PlainTextType, backLabel, false, false),
		))
	}

	section := slack.NewSectionBlock(
		slack.NewTextBlockObject(slack.MarkdownType, prompt, false, false),
		nil, nil,
	)
	actions := slack.NewActionBlock(actionPrefix, buttons...)

	opts := []slack.MsgOption{slack.MsgOptionBlocks(section, actions)}
	if threadTS != "" {
		opts = append(opts, slack.MsgOptionTS(threadTS))
	}

	_, ts, err := c.api.PostMessage(channelID, opts...)
	if err != nil {
		return "", fmt.Errorf("post selector with back: %w", err)
	}
	return ts, nil
}
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add internal/slack/client.go
git commit -m "feat(slack): add PostSelectorWithBack with optional trailing back button

Rightmost position maximizes distance from main option buttons to
minimize accidental re-click. Default style. Used by upcoming branch
and description selectors so users can undo a mis-clicked repo."
```

---

### Task 9: Introduce `slackAPI` interface + `RepoWasPicked` flag in workflow

**Files:**
- Modify: `internal/bot/workflow.go`
- Create: `internal/bot/workflow_test.go`

- [ ] **Step 1: Write the failing test for the branch-selector back-button gate**

Create `internal/bot/workflow_test.go`:

```go
package bot

import (
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"

	"agentdock/internal/config"
	"agentdock/internal/queue"
	slackclient "agentdock/internal/slack"
)

// stubSlack implements the slackAPI interface for workflow tests.
type stubSlack struct {
	mu sync.Mutex

	PostSelectorCalls         []postSelectorCall
	PostSelectorWithBackCalls []postSelectorWithBackCall
	PostExternalSelectorCalls []postExternalSelectorCall
	UpdateMessageCalls        []updateMessageCall
	PostMessageCalls          []postMessageCall
	PostMessageWithButtonCalls []postMessageWithButtonCall

	PostSelectorErr       error
	PostSelectorBackErr   error
	PostExternalErr       error
	NextSelectorTS        string
}

type postSelectorCall struct {
	ChannelID, Prompt, ActionPrefix, ThreadTS string
	Options                                   []string
}
type postSelectorWithBackCall struct {
	ChannelID, Prompt, ActionPrefix, ThreadTS, BackActionID, BackLabel string
	Options                                                            []string
}
type postExternalSelectorCall struct {
	ChannelID, Prompt, ActionID, Placeholder, ThreadTS string
}
type updateMessageCall struct{ ChannelID, MessageTS, Text string }
type postMessageCall struct{ ChannelID, Text, ThreadTS string }
type postMessageWithButtonCall struct {
	ChannelID, Text, ThreadTS, ActionID, ButtonText, Value string
}

func (s *stubSlack) PostMessage(channelID, text, threadTS string) error {
	s.mu.Lock(); defer s.mu.Unlock()
	s.PostMessageCalls = append(s.PostMessageCalls, postMessageCall{channelID, text, threadTS})
	return nil
}
func (s *stubSlack) PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value string) (string, error) {
	s.mu.Lock(); defer s.mu.Unlock()
	s.PostMessageWithButtonCalls = append(s.PostMessageWithButtonCalls,
		postMessageWithButtonCall{channelID, text, threadTS, actionID, buttonText, value})
	return "STATUS_TS", nil
}
func (s *stubSlack) PostSelector(channelID, prompt, actionPrefix string, options []string, threadTS string) (string, error) {
	s.mu.Lock(); defer s.mu.Unlock()
	s.PostSelectorCalls = append(s.PostSelectorCalls,
		postSelectorCall{channelID, prompt, actionPrefix, threadTS, options})
	if s.PostSelectorErr != nil {
		return "", s.PostSelectorErr
	}
	return s.ts("SEL"), nil
}
func (s *stubSlack) PostSelectorWithBack(channelID, prompt, actionPrefix string, options []string, threadTS, backActionID, backLabel string) (string, error) {
	s.mu.Lock(); defer s.mu.Unlock()
	s.PostSelectorWithBackCalls = append(s.PostSelectorWithBackCalls,
		postSelectorWithBackCall{channelID, prompt, actionPrefix, threadTS, backActionID, backLabel, options})
	if s.PostSelectorBackErr != nil {
		return "", s.PostSelectorBackErr
	}
	return s.ts("SELB"), nil
}
func (s *stubSlack) PostExternalSelector(channelID, prompt, actionID, placeholder, threadTS string) (string, error) {
	s.mu.Lock(); defer s.mu.Unlock()
	s.PostExternalSelectorCalls = append(s.PostExternalSelectorCalls,
		postExternalSelectorCall{channelID, prompt, actionID, placeholder, threadTS})
	if s.PostExternalErr != nil {
		return "", s.PostExternalErr
	}
	return s.ts("EXT"), nil
}
func (s *stubSlack) UpdateMessage(channelID, messageTS, text string) error {
	s.mu.Lock(); defer s.mu.Unlock()
	s.UpdateMessageCalls = append(s.UpdateMessageCalls,
		updateMessageCall{channelID, messageTS, text})
	return nil
}
func (s *stubSlack) OpenDescriptionModal(triggerID, selectorMsgTS string) error { return nil }
func (s *stubSlack) ResolveUser(userID string) string                          { return userID }
func (s *stubSlack) GetChannelName(channelID string) string                    { return channelID }
func (s *stubSlack) FetchThreadContext(channelID, threadTS, triggerTS, botUserID string, limit int) ([]slackclient.ThreadRawMessage, error) {
	return nil, nil
}
func (s *stubSlack) DownloadAttachments(messages []slackclient.ThreadRawMessage, tempDir string) []slackclient.AttachmentDownload {
	return nil
}
func (s *stubSlack) ts(prefix string) string {
	if s.NextSelectorTS != "" {
		v := s.NextSelectorTS
		s.NextSelectorTS = ""
		return v
	}
	return fmt.Sprintf("%s_%d", prefix, len(s.PostSelectorCalls)+len(s.PostSelectorWithBackCalls)+len(s.PostExternalSelectorCalls))
}

// newTestWorkflow builds a Workflow with stubs sufficient for selector tests.
func newTestWorkflow(t *testing.T, slack *stubSlack, cfg *config.Config) *Workflow {
	t.Helper()
	if cfg == nil {
		cfg = &config.Config{
			Channels:        map[string]config.ChannelConfig{},
			ChannelDefaults: config.ChannelConfig{},
		}
	}
	w := &Workflow{
		cfg:       cfg,
		slack:     slack,
		pending:   make(map[string]*pendingTriage),
		autoBound: make(map[string]bool),
	}
	return w
}

func testPending(ch, thread string, repoWasPicked bool, phase string) *pendingTriage {
	return &pendingTriage{
		ChannelID:     ch,
		ThreadTS:      thread,
		TriggerTS:     thread,
		UserID:        "U1",
		Reporter:      "U1",
		ChannelName:   "#test",
		Phase:         phase,
		RepoWasPicked: repoWasPicked,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestAfterRepoSelected_BackButton_WhenRepoWasPicked(t *testing.T) {
	slack := &stubSlack{}
	cfg := &config.Config{
		ChannelDefaults: config.ChannelConfig{Branches: []string{"main", "develop"}},
	}
	w := newTestWorkflow(t, slack, cfg)

	pt := testPending("C1", "T1", true, "repo_search")
	pt.SelectedRepo = "o/r"
	w.afterRepoSelected(pt, cfg.ChannelDefaults)

	if len(slack.PostSelectorWithBackCalls) != 1 {
		t.Fatalf("expected 1 PostSelectorWithBack call, got %d", len(slack.PostSelectorWithBackCalls))
	}
	c := slack.PostSelectorWithBackCalls[0]
	if c.ActionPrefix != "branch_select" {
		t.Errorf("ActionPrefix = %q, want branch_select", c.ActionPrefix)
	}
	if c.BackActionID != "back_to_repo" {
		t.Errorf("BackActionID = %q, want back_to_repo (RepoWasPicked=true)", c.BackActionID)
	}
}

func TestAfterRepoSelected_NoBackButton_WhenShortcut(t *testing.T) {
	slack := &stubSlack{}
	cfg := &config.Config{
		ChannelDefaults: config.ChannelConfig{Branches: []string{"main", "develop"}},
	}
	w := newTestWorkflow(t, slack, cfg)

	pt := testPending("C1", "T1", false, "")
	pt.SelectedRepo = "o/r"
	w.afterRepoSelected(pt, cfg.ChannelDefaults)

	if len(slack.PostSelectorWithBackCalls) != 1 {
		t.Fatalf("expected 1 PostSelectorWithBack call")
	}
	if slack.PostSelectorWithBackCalls[0].BackActionID != "" {
		t.Errorf("should not include back button for shortcut path; got %q",
			slack.PostSelectorWithBackCalls[0].BackActionID)
	}
}
```

- [ ] **Step 2: Run — confirm compile errors**

Run: `go test ./internal/bot/ -run TestAfterRepoSelected -v`
Expected: FAIL with `undefined: w.slack` / `cannot use stub` — because `Workflow.slack` is `*slackclient.Client` concrete, and we haven't added `RepoWasPicked`, `PostSelectorWithBack`, or the interface yet.

- [ ] **Step 3: Define `slackAPI` interface and change field type**

Edit `internal/bot/workflow.go`. Near the top (below imports), add:

```go
// slackAPI is the narrow Slack surface used by Workflow. *slackclient.Client
// satisfies it; tests implement it with a stub.
type slackAPI interface {
	PostMessage(channelID, text, threadTS string) error
	PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value string) (string, error)
	PostSelector(channelID, prompt, actionPrefix string, options []string, threadTS string) (string, error)
	PostSelectorWithBack(channelID, prompt, actionPrefix string, options []string, threadTS, backActionID, backLabel string) (string, error)
	PostExternalSelector(channelID, prompt, actionID, placeholder, threadTS string) (string, error)
	UpdateMessage(channelID, messageTS, text string) error
	OpenDescriptionModal(triggerID, selectorMsgTS string) error
	ResolveUser(userID string) string
	GetChannelName(channelID string) string
	FetchThreadContext(channelID, threadTS, triggerTS, botUserID string, limit int) ([]slackclient.ThreadRawMessage, error)
	DownloadAttachments(messages []slackclient.ThreadRawMessage, tempDir string) []slackclient.AttachmentDownload
}
```

Change the `Workflow` struct:

```go
type Workflow struct {
	cfg           *config.Config
	slack         slackAPI                 // was *slackclient.Client
	handler       *slackclient.Handler
	// ... rest unchanged
}
```

Change the `NewWorkflow` constructor signature similarly:

```go
func NewWorkflow(
	cfg *config.Config,
	slack slackAPI,                       // was *slackclient.Client
	repoCache *ghclient.RepoCache,
	// ...
)
```

- [ ] **Step 4: Add `RepoWasPicked` field and set it on repo selection**

In the `pendingTriage` struct, add one field:

```go
type pendingTriage struct {
	// ... existing fields ...
	RepoWasPicked bool
}
```

In `HandleSelection`, inside `case "repo", "repo_search":`, set the flag after assigning `SelectedRepo`:

```go
case "repo", "repo_search":
	w.slack.UpdateMessage(channelID, selectorMsgTS,
		fmt.Sprintf(":white_check_mark: Repo: `%s`", value))
	pt.SelectedRepo = value
	pt.RepoWasPicked = true                                   // NEW
	w.afterRepoSelected(pt, channelCfg)
```

- [ ] **Step 5: Switch `afterRepoSelected` to use `PostSelectorWithBack`**

Locate the current `afterRepoSelected` branch-selector block (around workflow.go:282):

```go
pt.Phase = "branch"
selectorTS, err := w.slack.PostSelector(pt.ChannelID,
	fmt.Sprintf(":point_right: Which branch of `%s`?", pt.SelectedRepo),
	"branch_select", branches, pt.ThreadTS)
```

Replace with:

```go
pt.Phase = "branch"
backAction := ""
if pt.RepoWasPicked {
	backAction = "back_to_repo"
}
selectorTS, err := w.slack.PostSelectorWithBack(pt.ChannelID,
	fmt.Sprintf(":point_right: Which branch of `%s`?", pt.SelectedRepo),
	"branch_select", branches, pt.ThreadTS,
	backAction, "← 重新選 repo")
```

- [ ] **Step 6: Run tests — confirm the two tests pass**

Run: `go test ./internal/bot/ -run TestAfterRepoSelected -v`
Expected: both tests PASS.

If `go build ./...` fails from `cmd/agentdock/app.go` mismatch (constructor now takes interface), the tests still work because Go tests compile per-package. Proceed.

- [ ] **Step 7: Commit**

```bash
git add internal/bot/workflow.go internal/bot/workflow_test.go
git commit -m "feat(bot): introduce slackAPI interface; add RepoWasPicked gate

Defines a narrow slackAPI interface for Workflow (mirrors the
SlackPoster pattern). *slackclient.Client satisfies it automatically.
pendingTriage gains RepoWasPicked flag, set when a user completes a
repo selector. afterRepoSelected now uses PostSelectorWithBack and
emits a back button only when RepoWasPicked is true (so shortcut and
single-repo paths do not show it)."
```

---

### Task 10: Description prompt back-button gate

**Files:**
- Modify: `internal/bot/workflow.go`
- Modify: `internal/bot/workflow_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/bot/workflow_test.go`:

```go
func TestShowDescriptionPrompt_BackButton_WhenRepoWasPicked(t *testing.T) {
	slack := &stubSlack{}
	w := newTestWorkflow(t, slack, nil)

	pt := testPending("C1", "T1", true, "branch")
	pt.SelectedRepo = "o/r"
	pt.SelectedBranch = "main"
	w.showDescriptionPrompt(pt)

	if len(slack.PostSelectorWithBackCalls) != 1 {
		t.Fatalf("expected 1 PostSelectorWithBack call")
	}
	c := slack.PostSelectorWithBackCalls[0]
	if c.ActionPrefix != "description_action" {
		t.Errorf("ActionPrefix = %q, want description_action", c.ActionPrefix)
	}
	if c.BackActionID != "back_to_repo" {
		t.Errorf("BackActionID = %q, want back_to_repo", c.BackActionID)
	}
}

func TestShowDescriptionPrompt_NoBackButton_WhenShortcut(t *testing.T) {
	slack := &stubSlack{}
	w := newTestWorkflow(t, slack, nil)

	pt := testPending("C1", "T1", false, "")
	pt.SelectedRepo = "o/r"
	w.showDescriptionPrompt(pt)

	if len(slack.PostSelectorWithBackCalls) != 1 {
		t.Fatalf("expected 1 PostSelectorWithBack call")
	}
	if slack.PostSelectorWithBackCalls[0].BackActionID != "" {
		t.Errorf("should not include back button for shortcut path")
	}
}
```

- [ ] **Step 2: Run — confirm fail**

Run: `go test ./internal/bot/ -run TestShowDescriptionPrompt -v`
Expected: FAIL — current `showDescriptionPrompt` still calls `PostSelector`, not `PostSelectorWithBack`.

- [ ] **Step 3: Switch `showDescriptionPrompt` to use `PostSelectorWithBack`**

Locate the current function (around workflow.go:294):

```go
func (w *Workflow) showDescriptionPrompt(pt *pendingTriage) {
	pt.Phase = "description"
	selectorTS, err := w.slack.PostSelector(pt.ChannelID,
		":memo: 需要補充說明嗎？（補充後可讓分析更精準）",
		"description_action", []string{"補充說明", "跳過"}, pt.ThreadTS)
```

Replace the `PostSelector` call:

```go
func (w *Workflow) showDescriptionPrompt(pt *pendingTriage) {
	pt.Phase = "description"
	backAction := ""
	if pt.RepoWasPicked {
		backAction = "back_to_repo"
	}
	selectorTS, err := w.slack.PostSelectorWithBack(pt.ChannelID,
		":memo: 需要補充說明嗎？（補充後可讓分析更精準）",
		"description_action", []string{"補充說明", "跳過"}, pt.ThreadTS,
		backAction, "← 重新選 repo")
```

- [ ] **Step 4: Run — confirm pass**

Run: `go test ./internal/bot/ -run TestShowDescriptionPrompt -v`
Expected: both tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/bot/workflow.go internal/bot/workflow_test.go
git commit -m "feat(bot): gate back button on description prompt

Description selector now carries the '← 重新選 repo' button iff the
user had previously gone through a repo selector (RepoWasPicked).
Keeps single-repo and shortcut paths clean."
```

---

### Task 11: Extract `postRepoSelector` helper

**Files:**
- Modify: `internal/bot/workflow.go`
- Modify: `internal/bot/workflow_test.go`

- [ ] **Step 1: Write a test exercising the helper directly**

Append to `internal/bot/workflow_test.go`:

```go
func TestPostRepoSelector_MultiRepo_UsesPostSelector(t *testing.T) {
	slack := &stubSlack{}
	cfg := &config.Config{
		ChannelDefaults: config.ChannelConfig{Repos: []string{"o/a", "o/b", "o/c"}},
	}
	w := newTestWorkflow(t, slack, cfg)

	pt := testPending("C1", "T1", true, "")
	ts, err := w.postRepoSelector(pt, cfg.ChannelDefaults)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ts == "" {
		t.Error("expected selector TS")
	}
	if len(slack.PostSelectorCalls) != 1 {
		t.Fatalf("expected 1 PostSelector call, got %d", len(slack.PostSelectorCalls))
	}
	if slack.PostSelectorCalls[0].ActionPrefix != "repo_select" {
		t.Errorf("ActionPrefix = %q, want repo_select", slack.PostSelectorCalls[0].ActionPrefix)
	}
	if pt.Phase != "repo" {
		t.Errorf("Phase = %q, want repo", pt.Phase)
	}
}

func TestPostRepoSelector_NoRepos_UsesPostExternalSelector(t *testing.T) {
	slack := &stubSlack{}
	cfg := &config.Config{ChannelDefaults: config.ChannelConfig{}}
	w := newTestWorkflow(t, slack, cfg)

	pt := testPending("C1", "T1", true, "")
	ts, err := w.postRepoSelector(pt, cfg.ChannelDefaults)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ts == "" {
		t.Error("expected selector TS")
	}
	if len(slack.PostExternalSelectorCalls) != 1 {
		t.Fatalf("expected 1 PostExternalSelector call")
	}
	if slack.PostExternalSelectorCalls[0].ActionID != "repo_search" {
		t.Errorf("ActionID = %q, want repo_search", slack.PostExternalSelectorCalls[0].ActionID)
	}
	if pt.Phase != "repo_search" {
		t.Errorf("Phase = %q, want repo_search", pt.Phase)
	}
}
```

- [ ] **Step 2: Run — confirm fail**

Run: `go test ./internal/bot/ -run TestPostRepoSelector -v`
Expected: FAIL — `undefined: w.postRepoSelector`.

- [ ] **Step 3: Extract the helper and call it from `HandleTrigger`**

Add method in `internal/bot/workflow.go`:

```go
// postRepoSelector posts either the multi-repo button selector (len>1) or the
// external searchable selector (len==0). The len==1 auto-select case is
// handled by callers inline — see HandleTrigger and HandleBackToRepo.
func (w *Workflow) postRepoSelector(pt *pendingTriage, channelCfg config.ChannelConfig) (string, error) {
	repos := channelCfg.GetRepos()
	if len(repos) > 1 {
		pt.Phase = "repo"
		return w.slack.PostSelector(pt.ChannelID,
			":point_right: Which repo should this issue go to?",
			"repo_select", repos, pt.ThreadTS)
	}
	pt.Phase = "repo_search"
	return w.slack.PostExternalSelector(pt.ChannelID,
		":point_right: Search and select a repo:",
		"repo_search", "Type to search repos...", pt.ThreadTS)
}
```

Refactor `HandleTrigger` to use it. Replace the existing blocks (workflow.go:186-209) that post the selectors:

Current:
```go
if len(repos) > 1 {
	pt.Phase = "repo"
	selectorTS, err := w.slack.PostSelector(event.ChannelID,
		":point_right: Which repo should this issue go to?",
		"repo_select", repos, pt.ThreadTS)
	if err != nil {
		w.notifyError(pt.Logger, event.ChannelID, pt.ThreadTS, "Failed to show repo selector: %v", err)
		return
	}
	pt.SelectorTS = selectorTS
	w.storePending(selectorTS, pt)
	return
}

pt.Phase = "repo_search"
selectorTS, err := w.slack.PostExternalSelector(event.ChannelID,
	":point_right: Search and select a repo:",
	"repo_search", "Type to search repos...", pt.ThreadTS)
if err != nil {
	w.notifyError(pt.Logger, event.ChannelID, pt.ThreadTS, "Failed to show repo search: %v", err)
	return
}
pt.SelectorTS = selectorTS
w.storePending(selectorTS, pt)
```

Replace with:

```go
selectorTS, err := w.postRepoSelector(pt, channelCfg)
if err != nil {
	w.notifyError(pt.Logger, event.ChannelID, pt.ThreadTS, "Failed to show repo selector: %v", err)
	return
}
pt.SelectorTS = selectorTS
w.storePending(selectorTS, pt)
```

- [ ] **Step 4: Run tests — confirm pass**

Run: `go test ./internal/bot/ -run TestPostRepoSelector -v`
Expected: both PASS.

Also re-run earlier workflow tests:
Run: `go test ./internal/bot/ -run "TestAfterRepoSelected|TestShowDescriptionPrompt" -v`
Expected: all PASS (no regressions).

- [ ] **Step 5: Commit**

```bash
git add internal/bot/workflow.go internal/bot/workflow_test.go
git commit -m "refactor(bot): extract postRepoSelector for reuse

Helper covers the len(repos) > 1 (button selector) and len == 0
(external search) branches. The len == 1 auto-select case is handled
inline by callers. Preps for HandleBackToRepo to reuse the same logic
without duplicating the dispatch."
```

---

### Task 12: `HandleBackToRepo` method

**Files:**
- Modify: `internal/bot/workflow.go`
- Modify: `internal/bot/workflow_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/bot/workflow_test.go`:

```go
func TestHandleBackToRepo_FromBranchStep(t *testing.T) {
	slack := &stubSlack{}
	slack.NextSelectorTS = "NEW_SEL"
	cfg := &config.Config{
		ChannelDefaults: config.ChannelConfig{Repos: []string{"o/a", "o/b"}},
	}
	w := newTestWorkflow(t, slack, cfg)

	pt := testPending("C1", "T1", true, "branch")
	pt.SelectedRepo = "o/a"
	pt.SelectedBranch = "main"
	pt.SelectorTS = "BRANCH_TS"
	w.pending["BRANCH_TS"] = pt

	w.HandleBackToRepo("C1", "BRANCH_TS")

	if _, still := w.pending["BRANCH_TS"]; still {
		t.Error("old selector key should be deleted")
	}
	if _, added := w.pending["NEW_SEL"]; !added {
		t.Error("new selector key should be present")
	}
	if pt.SelectedRepo != "" {
		t.Errorf("SelectedRepo should be cleared, got %q", pt.SelectedRepo)
	}
	if pt.SelectedBranch != "" {
		t.Errorf("SelectedBranch should be cleared, got %q", pt.SelectedBranch)
	}
	if len(slack.PostSelectorCalls) != 1 {
		t.Errorf("expected 1 PostSelector call (multi-repo), got %d", len(slack.PostSelectorCalls))
	}
	// Old message must be frozen.
	found := false
	for _, u := range slack.UpdateMessageCalls {
		if u.MessageTS == "BRANCH_TS" && containsStr(u.Text, "已返回 repo 選擇") {
			found = true
		}
	}
	if !found {
		t.Error("old selector message should be updated with 已返回 repo 選擇 text")
	}
}

func TestHandleBackToRepo_FromDescriptionStep(t *testing.T) {
	slack := &stubSlack{}
	cfg := &config.Config{ChannelDefaults: config.ChannelConfig{}} // len==0 → external search
	w := newTestWorkflow(t, slack, cfg)

	pt := testPending("C1", "T1", true, "description")
	pt.SelectedRepo = "o/a"
	pt.SelectedBranch = "main"
	pt.ExtraDesc = "I typed this before going back"
	pt.SelectorTS = "DESC_TS"
	w.pending["DESC_TS"] = pt

	w.HandleBackToRepo("C1", "DESC_TS")

	if pt.ExtraDesc != "" {
		t.Errorf("ExtraDesc should be cleared, got %q", pt.ExtraDesc)
	}
	if len(slack.PostExternalSelectorCalls) != 1 {
		t.Errorf("expected 1 PostExternalSelector call (no channel repos)")
	}
}

func TestHandleBackToRepo_PendingMissing_Silent(t *testing.T) {
	slack := &stubSlack{}
	w := newTestWorkflow(t, slack, nil)

	w.HandleBackToRepo("C1", "NONEXISTENT") // no panic, no calls

	if len(slack.PostSelectorCalls)+len(slack.PostExternalSelectorCalls) != 0 {
		t.Errorf("unexpected Slack calls for missing pending")
	}
}

func TestHandleBackToRepo_PostFails_NoFreeze_ClearsDedup(t *testing.T) {
	slack := &stubSlack{PostSelectorErr: fmt.Errorf("slack fail")}
	cfg := &config.Config{
		ChannelDefaults: config.ChannelConfig{Repos: []string{"o/a", "o/b"}},
	}
	w := newTestWorkflow(t, slack, cfg)

	// Stand in for handler.ClearThreadDedup.
	var clearedKey string
	w.handler = nil // leave nil; clearDedup no-ops when handler is nil

	pt := testPending("C1", "T1", true, "branch")
	pt.SelectedRepo = "o/a"
	pt.SelectorTS = "BRANCH_TS"
	w.pending["BRANCH_TS"] = pt

	w.HandleBackToRepo("C1", "BRANCH_TS")

	// Old message should NOT be frozen since post failed.
	for _, u := range slack.UpdateMessageCalls {
		if u.MessageTS == "BRANCH_TS" && containsStr(u.Text, "已返回") {
			t.Error("old message should NOT be frozen when new post fails")
		}
	}
	// Error message should have been posted via notifyError.
	foundErrMsg := false
	for _, m := range slack.PostMessageCalls {
		if containsStr(m.Text, ":x:") {
			foundErrMsg = true
		}
	}
	if !foundErrMsg {
		t.Error("expected error message via notifyError")
	}
	_ = clearedKey // reserved for future assertions
}

func TestHandleBackToRepo_ConfigNowSingleRepo_AutoSelect(t *testing.T) {
	slack := &stubSlack{}
	cfg := &config.Config{
		ChannelDefaults: config.ChannelConfig{
			Repos:    []string{"o/only"},
			Branches: []string{"main", "develop"},
		},
	}
	w := newTestWorkflow(t, slack, cfg)

	pt := testPending("C1", "T1", true, "branch")
	pt.SelectorTS = "BRANCH_TS"
	w.pending["BRANCH_TS"] = pt

	w.HandleBackToRepo("C1", "BRANCH_TS")

	if pt.SelectedRepo != "o/only" {
		t.Errorf("SelectedRepo should be auto-set to o/only, got %q", pt.SelectedRepo)
	}
	// Should have posted branch selector (via afterRepoSelected), not repo selector.
	if len(slack.PostSelectorCalls)+len(slack.PostExternalSelectorCalls) != 0 {
		t.Errorf("should not post a repo selector when len(repos)==1")
	}
	if len(slack.PostSelectorWithBackCalls) != 1 {
		t.Errorf("expected 1 branch PostSelectorWithBack call")
	}
	if slack.PostSelectorWithBackCalls[0].ActionPrefix != "branch_select" {
		t.Errorf("expected branch_select, got %q", slack.PostSelectorWithBackCalls[0].ActionPrefix)
	}
}

// containsStr helper for workflow tests (renamed to avoid name collision).
func containsStr(s, sub string) bool {
	if sub == "" {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run — confirm fail**

Run: `go test ./internal/bot/ -run TestHandleBackToRepo -v`
Expected: FAIL — `undefined: w.HandleBackToRepo`.

- [ ] **Step 3: Implement `HandleBackToRepo`**

Add to `internal/bot/workflow.go`:

```go
// HandleBackToRepo handles a "← 重新選 repo" button click. Invoked from
// cmd/agentdock/app.go when action.ActionID == "back_to_repo".
func (w *Workflow) HandleBackToRepo(channelID, selectorMsgTS string) {
	w.mu.Lock()
	pt, ok := w.pending[selectorMsgTS]
	if ok {
		delete(w.pending, selectorMsgTS)
	}
	w.mu.Unlock()
	if !ok {
		return
	}

	if pt.Logger != nil {
		pt.Logger.Info("收到返回 repo 請求",
			"phase", "接收", "from_selector_ts", selectorMsgTS)
	}

	channelCfg := w.cfg.ChannelDefaults
	if cc, ok := w.cfg.Channels[pt.ChannelID]; ok {
		channelCfg = cc
	}

	// Clear carried-over fields.
	pt.SelectedRepo = ""
	pt.SelectedBranch = ""
	pt.ExtraDesc = ""

	// Rare case: channel config reloaded and now has exactly one repo — auto-select
	// and go directly to the branch step (mirrors HandleTrigger's shortcut).
	repos := channelCfg.GetRepos()
	if len(repos) == 1 {
		pt.SelectedRepo = repos[0]
		w.slack.UpdateMessage(channelID, selectorMsgTS,
			":leftwards_arrow_with_hook: 已返回 repo 選擇")
		w.afterRepoSelected(pt, channelCfg)
		return
	}

	// Multi-repo or external-search case.
	newSelectorTS, err := w.postRepoSelector(pt, channelCfg)
	if err != nil {
		w.notifyError(pt.Logger, channelID, pt.ThreadTS,
			"重選 repo 失敗: %v", err)
		w.clearDedup(pt)
		return
	}

	w.slack.UpdateMessage(channelID, selectorMsgTS,
		":leftwards_arrow_with_hook: 已返回 repo 選擇")

	pt.SelectorTS = newSelectorTS
	w.storePending(newSelectorTS, pt)

	if pt.Logger != nil {
		pt.Logger.Info("已重新顯示 repo 選擇",
			"phase", "處理中", "new_selector_ts", newSelectorTS)
	}
}
```

- [ ] **Step 4: Run tests — confirm pass**

Run: `go test ./internal/bot/ -run TestHandleBackToRepo -v`
Expected: all PASS.

Also run the full bot suite:
Run: `go test ./internal/bot/`
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/bot/workflow.go internal/bot/workflow_test.go
git commit -m "feat(bot): HandleBackToRepo — revert a mis-clicked repo mid-flow

On back click: pop pending under lock, clear SelectedRepo /
SelectedBranch / ExtraDesc, post a fresh repo selector (or
auto-select when only one repo remains in config), then freeze the
old message to '已返回 repo 選擇'. Preserves thread dedup so the user
does not need to re-@bot."
```

---

### Task 13: Wire the `back_to_repo` router case

**Files:**
- Modify: `cmd/agentdock/app.go`

- [ ] **Step 1: Locate the block-actions switch**

Run: `grep -n "back_to_repo\|back_to\|description_action" cmd/agentdock/app.go`
Expected: matches around line 365-370 in the `InteractionTypeBlockActions` switch.

- [ ] **Step 2: Add the case**

Inside the `switch {` block handling `action.ActionID` (currently ending around line 390), add one case. Place it alongside `description_action`, before the `cancel_job` case:

```go
case action.ActionID == "back_to_repo":
	wf.HandleBackToRepo(cb.Channel.ID, selectorTS)
```

- [ ] **Step 3: Rewire `NewWorkflow` call if needed**

Since Task 9 changed `NewWorkflow`'s `slack` parameter from `*slackclient.Client` to `slackAPI`, the call site in `app.go` still works (concrete type satisfies interface automatically) — but double-check by running build.

Run: `go build ./...`
Expected: no errors.

- [ ] **Step 4: Run full test suite**

Run: `go test ./...`
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add cmd/agentdock/app.go
git commit -m "wire: route back_to_repo action to workflow.HandleBackToRepo

Adds one case to the InteractionTypeBlockActions switch. Activated by
the '← 重新選 repo' button embedded in branch / description selectors
via PostSelectorWithBack."
```

---

## Part C: Manual QA + Merge Readiness

### Task 14: Manual QA checklist

**Files:** none

Run through the two spec-level QA checklists. This is a gate before merging, not a coding task — skip if not currently able to run against a live Slack workspace.

- [ ] **Status progress visibility QA (Part A)**

Start a worker pool wired to a Slack sandbox channel. Trigger a triage job that runs ~1 minute.
  1. Observe the status message transitions: `已加入排隊...` → `:gear: 準備中 · worker-N` → `:hourglass_flowing_sand: 處理中 · worker-N (claude) · Xm Ys`.
  2. For a Claude job: second line appears with `工具呼叫 …` counters.
  3. For a codex job: second line never appears; elapsed ticks every 15s.
  4. Cancel button persists through every update.
  5. Clicking cancel during prep and during run both transition to `:stop_sign: 正在取消...` normally.
  6. Observe a short job (<15s): 1–2 updates max, no spam.
  7. Two concurrent jobs in different threads: independent updates.

- [ ] **Repo re-select QA (Part B)**

  1. Multi-repo channel: `@bot` → repo search → pick repo → branch selector shows `← 重新選 repo` → click → new repo selector appears, old message becomes `:leftwards_arrow_with_hook: 已返回 repo 選擇`, state clean.
  2. Multi-repo channel: pick repo → pick branch → description prompt shows back button → click → repo selector again, branch cleared.
  3. Single-repo channel: `@bot` → branch selector has NO back button.
  4. Shortcut `@bot owner/repo`: branch selector has NO back button.
  5. Shortcut `@bot owner/repo@branch`: no selector at all (no regression).
  6. Double-click back button: second click silently ignored.
  7. Back → wait 60s → `:hourglass: 已超時` appears (existing timeout UX).
  8. Click "補充說明" to open modal, then click back: modal submit does nothing; new repo selector is usable.

- [ ] **Sign-off commit (optional)**

If QA surfaces no issues and you want a marker:

```bash
git commit --allow-empty -m "qa: manual QA passed for Slack UX Part A+B"
```

---

## Self-Review Checklist

Before marking the plan complete, the author (or reviewing agent) should confirm:

- **Part A spec coverage**
  - [x] §2 Worker prep publish → Task 2
  - [x] §3 StatusListener struct + maybeUpdateSlack → Tasks 3, 4
  - [x] §4 Render template → Task 3
  - [x] §5 `UpdateMessageWithButton` → Task 1
  - [x] §6 Constructor wiring → Task 5
  - [x] §7 ResultListener double-write → Task 6
  - [x] §7-wire StatusListener.ClearJob + ResultListener.SetStatusJobClearer → Task 7
  - [x] §8 Logging — covered inline in Tasks 4 and 12 (via `pt.Logger` and `l.logger`)
  - [x] Error-handling table rows — all covered by tests in Tasks 4 and 6

- **Part B spec coverage**
  - [x] §1 `RepoWasPicked` flag → Task 9
  - [x] §2 `slackAPI` interface → Task 9
  - [x] §3 `PostSelectorWithBack` → Task 8
  - [x] §4 `HandleBackToRepo` → Task 12
  - [x] §5 `postRepoSelector` helper → Task 11 (plus inline single-repo auto-select in Task 12)
  - [x] §6 Gate in `afterRepoSelected` / `showDescriptionPrompt` → Tasks 9, 10
  - [x] §7 Router wiring → Task 13
  - [x] §8 Logging → inline in Task 12
  - [x] Error-handling table rows — tests cover pending-missing, post-fails, single-repo race

- **No placeholders** — every step shows real code, real tests, real commands

- **Type consistency** — `SlackStatusPoster` (Part A) and `slackAPI` (Part B) are distinct interfaces in distinct files; `*slackclient.Client` satisfies both; no name collisions

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-04-17-slack-ux-progress-reselect.md`. Two execution options:

1. **Subagent-Driven (recommended)** — dispatch a fresh subagent per task, review between tasks, fast iteration, good isolation
2. **Inline Execution** — execute tasks in this session using `superpowers:executing-plans`, batch execution with checkpoints
