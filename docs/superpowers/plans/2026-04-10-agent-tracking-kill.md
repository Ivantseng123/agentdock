# Agent Process Tracking & Kill Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add real-time agent process visibility, kill capability, and cost tracking across all deployment modes.

**Architecture:** CommandBus for kill signals (app→worker), StatusBus for agent status reports (worker→app), ProcessRegistry for local process control, stream-json parsing for claude event tracking. InMemTransport split into 5 focused structs.

**Tech Stack:** Go stdlib (`os/exec`, `syscall`, `bufio`, `container/heap`, `sync`), existing `slog` logging, no new dependencies.

---

### Task 1: Config Changes — Add Stream, Timeouts, Status Interval

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write tests for new config fields**

Add to `internal/config/config_test.go`:

```go
func TestLoad_AgentStream(t *testing.T) {
	yaml := `
agents:
  claude:
    command: claude
    args: ["--print", "--output-format", "stream-json", "-p", "{prompt}"]
    stream: true
  opencode:
    command: opencode
    args: ["--prompt", "{prompt}"]
`
	cfg := loadFromString(t, yaml)
	if !cfg.Agents["claude"].Stream {
		t.Error("claude stream should be true")
	}
	if cfg.Agents["opencode"].Stream {
		t.Error("opencode stream should be false")
	}
}

func TestLoad_TrackingTimeouts(t *testing.T) {
	yaml := `
queue:
  agent_idle_timeout: 3m
  prepare_timeout: 2m
  status_interval: 10s
agents:
  claude:
    command: claude
`
	cfg := loadFromString(t, yaml)
	if cfg.Queue.AgentIdleTimeout != 3*time.Minute {
		t.Errorf("agent_idle_timeout = %v", cfg.Queue.AgentIdleTimeout)
	}
	if cfg.Queue.PrepareTimeout != 2*time.Minute {
		t.Errorf("prepare_timeout = %v", cfg.Queue.PrepareTimeout)
	}
	if cfg.Queue.StatusInterval != 10*time.Second {
		t.Errorf("status_interval = %v", cfg.Queue.StatusInterval)
	}
}

func TestLoad_TrackingTimeoutDefaults(t *testing.T) {
	yaml := `
agents:
  claude:
    command: claude
`
	cfg := loadFromString(t, yaml)
	if cfg.Queue.AgentIdleTimeout != 5*time.Minute {
		t.Errorf("default agent_idle_timeout = %v, want 5m", cfg.Queue.AgentIdleTimeout)
	}
	if cfg.Queue.PrepareTimeout != 3*time.Minute {
		t.Errorf("default prepare_timeout = %v, want 3m", cfg.Queue.PrepareTimeout)
	}
	if cfg.Queue.StatusInterval != 5*time.Second {
		t.Errorf("default status_interval = %v, want 5s", cfg.Queue.StatusInterval)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run "TestLoad_Agent|TestLoad_Tracking" -v`
Expected: FAIL — fields not defined.

- [ ] **Step 3: Add new fields to config.go**

Add `Stream` to `AgentConfig`:

```go
type AgentConfig struct {
	Command  string        `yaml:"command"`
	Args     []string      `yaml:"args"`
	Timeout  time.Duration `yaml:"timeout"`
	SkillDir string        `yaml:"skill_dir"`
	Stream   bool          `yaml:"stream"`
}
```

Add timeout fields to `QueueConfig`:

```go
type QueueConfig struct {
	Capacity         int           `yaml:"capacity"`
	Transport        string        `yaml:"transport"`
	JobTimeout       time.Duration `yaml:"job_timeout"`
	AgentIdleTimeout time.Duration `yaml:"agent_idle_timeout"`
	PrepareTimeout   time.Duration `yaml:"prepare_timeout"`
	StatusInterval   time.Duration `yaml:"status_interval"`
}
```

Add defaults in `applyDefaults`:

```go
if cfg.Queue.AgentIdleTimeout <= 0 {
	cfg.Queue.AgentIdleTimeout = 5 * time.Minute
}
if cfg.Queue.PrepareTimeout <= 0 {
	cfg.Queue.PrepareTimeout = 3 * time.Minute
}
if cfg.Queue.StatusInterval <= 0 {
	cfg.Queue.StatusInterval = 5 * time.Second
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/config/ -v`
Expected: All tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add stream, agent_idle_timeout, prepare_timeout, status_interval"
```

---

### Task 2: New Interfaces — CommandBus, StatusBus, StatusReport

**Files:**
- Modify: `internal/queue/interface.go`
- Modify: `internal/queue/job.go`

- [ ] **Step 1: Add CommandBus and StatusBus interfaces to `internal/queue/interface.go`**

Add after the existing `ResultBus` interface:

```go
type Command struct {
	JobID  string `json:"job_id"`
	Action string `json:"action"`
}

type CommandBus interface {
	Send(ctx context.Context, cmd Command) error
	Receive(ctx context.Context) (<-chan Command, error)
	Close() error
}

type StatusReport struct {
	JobID        string    `json:"job_id"`
	WorkerID     string    `json:"worker_id"`
	PID          int       `json:"pid"`
	AgentCmd     string    `json:"agent_cmd"`
	Alive        bool      `json:"alive"`
	LastEvent    string    `json:"last_event,omitempty"`
	LastEventAt  time.Time `json:"last_event_at"`
	ToolCalls    int       `json:"tool_calls"`
	FilesRead    int       `json:"files_read"`
	OutputBytes  int       `json:"output_bytes"`
	CostUSD      float64   `json:"cost_usd,omitempty"`
	InputTokens  int       `json:"input_tokens,omitempty"`
	OutputTokens int       `json:"output_tokens,omitempty"`
}

type StatusBus interface {
	Report(ctx context.Context, report StatusReport) error
	Subscribe(ctx context.Context) (<-chan StatusReport, error)
	Close() error
}
```

Add `"time"` to the import block.

- [ ] **Step 2: Update JobStore interface — replace SetAgent with SetAgentStatus**

Replace `SetAgent(jobID string, pid int, command string) error` with:

```go
SetAgentStatus(jobID string, report StatusReport) error
```

- [ ] **Step 3: Update JobState — replace AgentPID/AgentCommand with AgentStatus**

In `internal/queue/job.go`, update `JobState`:

```go
type JobState struct {
	Job         *Job
	Status      JobStatus
	Position    int
	WorkerID    string
	StartedAt   time.Time
	WaitTime    time.Duration
	AgentStatus *StatusReport
}
```

Add cost fields to `JobResult`:

```go
type JobResult struct {
	JobID        string    `json:"job_id"`
	Status       string    `json:"status"`
	Title        string    `json:"title"`
	Body         string    `json:"body"`
	Labels       []string  `json:"labels"`
	Confidence   string    `json:"confidence"`
	FilesFound   int       `json:"files_found"`
	Questions    int       `json:"open_questions"`
	RawOutput    string    `json:"raw_output"`
	Error        string    `json:"error"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
	CostUSD      float64   `json:"cost_usd,omitempty"`
	InputTokens  int       `json:"input_tokens,omitempty"`
	OutputTokens int       `json:"output_tokens,omitempty"`
}
```

Add `DownloadURL` to `AttachmentMeta`:

```go
type AttachmentMeta struct {
	SlackFileID string `json:"slack_file_id"`
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
	MimeType    string `json:"mime_type"`
	DownloadURL string `json:"download_url"`
}
```

- [ ] **Step 4: Update MemJobStore — replace SetAgent with SetAgentStatus**

In `internal/queue/memstore.go`, replace the `SetAgent` method:

```go
func (s *MemJobStore) SetAgentStatus(jobID string, report StatusReport) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.jobs[jobID]
	if !ok {
		return nil // silently ignore — job may have been deleted (normal completion)
	}
	state.AgentStatus = &report
	return nil
}
```

Delete the old `SetAgent` method.

- [ ] **Step 5: Fix compilation errors**

The `executor.go` references `SetAgent` — update it to use `SetAgentStatus`. In `internal/worker/executor.go`, replace the `TrackedRunner` block:

```go
// Old: SetOnStarted with SetAgent — remove this block entirely for now.
// It will be replaced in Task 7 with RunOptions pattern.
```

For now, just comment out or remove the `TrackedRunner` check at line 73 so it compiles. The full replacement comes in Task 7.

Also update `internal/queue/httpstatus.go` — replace `state.AgentPID` references with `state.AgentStatus.PID` (add nil check).

- [ ] **Step 6: Verify compilation**

Run: `go build ./...`
Expected: Success.

- [ ] **Step 7: Run all tests**

Run: `go test ./... -count=1`
Expected: All tests pass (some memstore tests may need updating if they test SetAgent).

- [ ] **Step 8: Commit**

```bash
git add internal/queue/ internal/worker/
git commit -m "feat(queue): add CommandBus, StatusBus interfaces; replace SetAgent with SetAgentStatus"
```

---

### Task 3: Split InMemTransport into 5 Focused Structs

**Files:**
- Create: `internal/queue/inmem_jobqueue.go`
- Create: `internal/queue/inmem_resultbus.go`
- Create: `internal/queue/inmem_attachments.go`
- Create: `internal/queue/inmem_commandbus.go`
- Create: `internal/queue/inmem_statusbus.go`
- Create: `internal/queue/inmem_bundle.go`
- Remove: `internal/queue/inmem.go`
- Modify: `internal/queue/inmem_test.go`

This is the largest task. The existing `InMemTransport` (192 lines) is split into 5 single-responsibility structs plus a factory.

- [ ] **Step 1: Create `internal/queue/inmem_jobqueue.go`**

Extract the priority queue dispatch loop and job submission logic:

```go
package queue

import (
	"container/heap"
	"context"
	"sync"
	"sync/atomic"
)

type InMemJobQueue struct {
	mu         sync.Mutex
	cond       *sync.Cond
	pq         priorityQueue
	capacity   int
	seqCounter atomic.Uint64
	store      JobStore
	jobCh      chan *Job
	closed     chan struct{}
}

func NewInMemJobQueue(capacity int, store JobStore) *InMemJobQueue {
	q := &InMemJobQueue{
		capacity: capacity,
		store:    store,
		jobCh:    make(chan *Job, capacity),
		closed:   make(chan struct{}),
	}
	q.cond = sync.NewCond(&q.mu)
	heap.Init(&q.pq)
	go q.dispatchLoop()
	return q
}

func (q *InMemJobQueue) dispatchLoop() {
	for {
		q.mu.Lock()
		for q.pq.Len() == 0 {
			select {
			case <-q.closed:
				q.mu.Unlock()
				return
			default:
			}
			q.cond.Wait()
		}
		entry := heap.Pop(&q.pq).(*queueEntry)
		q.mu.Unlock()

		select {
		case q.jobCh <- entry.job:
		case <-q.closed:
			return
		}
	}
}

func (q *InMemJobQueue) Submit(ctx context.Context, job *Job) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pq.Len() >= q.capacity {
		return ErrQueueFull
	}
	job.Seq = q.seqCounter.Add(1)
	heap.Push(&q.pq, &queueEntry{job: job})
	q.store.Put(job)
	q.cond.Signal()
	return nil
}

func (q *InMemJobQueue) QueuePosition(jobID string) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.pq.position(jobID), nil
}

func (q *InMemJobQueue) QueueDepth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.pq.Len()
}

func (q *InMemJobQueue) Receive(ctx context.Context) (<-chan *Job, error) {
	return q.jobCh, nil
}

func (q *InMemJobQueue) Ack(ctx context.Context, jobID string) error {
	q.store.UpdateStatus(jobID, JobPreparing)
	return nil
}

func (q *InMemJobQueue) Register(ctx context.Context, info WorkerInfo) error   { return nil }
func (q *InMemJobQueue) Unregister(ctx context.Context, workerID string) error { return nil }
func (q *InMemJobQueue) ListWorkers(ctx context.Context) ([]WorkerInfo, error) {
	return nil, nil
}

func (q *InMemJobQueue) Close() error {
	select {
	case <-q.closed:
	default:
		close(q.closed)
		q.cond.Broadcast()
	}
	return nil
}
```

- [ ] **Step 2: Create `internal/queue/inmem_resultbus.go`**

```go
package queue

import "context"

type InMemResultBus struct {
	ch     chan *JobResult
	closed chan struct{}
}

func NewInMemResultBus(capacity int) *InMemResultBus {
	return &InMemResultBus{
		ch:     make(chan *JobResult, capacity),
		closed: make(chan struct{}),
	}
}

func (b *InMemResultBus) Publish(ctx context.Context, result *JobResult) error {
	select {
	case b.ch <- result:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *InMemResultBus) Subscribe(ctx context.Context) (<-chan *JobResult, error) {
	return b.ch, nil
}

func (b *InMemResultBus) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}
```

- [ ] **Step 3: Create `internal/queue/inmem_attachments.go`**

```go
package queue

import (
	"context"
	"sync"
)

type InMemAttachmentStore struct {
	mu    sync.Mutex
	ready map[string]chan []AttachmentReady
}

func NewInMemAttachmentStore() *InMemAttachmentStore {
	return &InMemAttachmentStore{
		ready: make(map[string]chan []AttachmentReady),
	}
}

func (s *InMemAttachmentStore) Prepare(ctx context.Context, jobID string, attachments []AttachmentMeta) error {
	s.mu.Lock()
	ch, ok := s.ready[jobID]
	if !ok {
		ch = make(chan []AttachmentReady, 1)
		s.ready[jobID] = ch
	}
	s.mu.Unlock()

	result := make([]AttachmentReady, len(attachments))
	for i, a := range attachments {
		result[i] = AttachmentReady{Filename: a.Filename, URL: a.DownloadURL}
	}
	ch <- result
	return nil
}

func (s *InMemAttachmentStore) Resolve(ctx context.Context, jobID string) ([]AttachmentReady, error) {
	s.mu.Lock()
	ch, ok := s.ready[jobID]
	if !ok {
		ch = make(chan []AttachmentReady, 1)
		s.ready[jobID] = ch
	}
	s.mu.Unlock()

	select {
	case ready := <-ch:
		return ready, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *InMemAttachmentStore) Cleanup(ctx context.Context, jobID string) error {
	s.mu.Lock()
	delete(s.ready, jobID)
	s.mu.Unlock()
	return nil
}
```

- [ ] **Step 4: Create `internal/queue/inmem_commandbus.go`**

```go
package queue

import "context"

type InMemCommandBus struct {
	ch     chan Command
	closed chan struct{}
}

func NewInMemCommandBus(capacity int) *InMemCommandBus {
	return &InMemCommandBus{
		ch:     make(chan Command, capacity),
		closed: make(chan struct{}),
	}
}

func (b *InMemCommandBus) Send(ctx context.Context, cmd Command) error {
	select {
	case b.ch <- cmd:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *InMemCommandBus) Receive(ctx context.Context) (<-chan Command, error) {
	return b.ch, nil
}

func (b *InMemCommandBus) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}
```

- [ ] **Step 5: Create `internal/queue/inmem_statusbus.go`**

```go
package queue

import "context"

type InMemStatusBus struct {
	ch     chan StatusReport
	closed chan struct{}
}

func NewInMemStatusBus(capacity int) *InMemStatusBus {
	return &InMemStatusBus{
		ch:     make(chan StatusReport, capacity),
		closed: make(chan struct{}),
	}
}

func (b *InMemStatusBus) Report(ctx context.Context, report StatusReport) error {
	select {
	case b.ch <- report:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *InMemStatusBus) Subscribe(ctx context.Context) (<-chan StatusReport, error) {
	return b.ch, nil
}

func (b *InMemStatusBus) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}
```

- [ ] **Step 6: Create `internal/queue/inmem_bundle.go`**

```go
package queue

type InMemBundle struct {
	Queue       *InMemJobQueue
	Results     *InMemResultBus
	Attachments *InMemAttachmentStore
	Commands    *InMemCommandBus
	Status      *InMemStatusBus
}

func NewInMemBundle(capacity int, workerCount int, store JobStore) *InMemBundle {
	return &InMemBundle{
		Queue:       NewInMemJobQueue(capacity, store),
		Results:     NewInMemResultBus(capacity),
		Attachments: NewInMemAttachmentStore(),
		Commands:    NewInMemCommandBus(10),
		Status:      NewInMemStatusBus(workerCount * 2),
	}
}

func (b *InMemBundle) Close() error {
	b.Queue.Close()
	b.Results.Close()
	b.Commands.Close()
	b.Status.Close()
	return nil
}
```

- [ ] **Step 7: Delete `internal/queue/inmem.go`**

```bash
rm internal/queue/inmem.go
```

- [ ] **Step 8: Update `internal/queue/inmem_test.go`**

Replace all `NewInMemTransport` calls with `NewInMemBundle` equivalents. For tests that used the transport as both JobQueue and ResultBus, use the bundle fields separately. Example:

```go
// Old:
tr := NewInMemTransport(10, NewMemJobStore())
defer tr.Close()
tr.Submit(ctx, job)
ch, _ := tr.Receive(ctx)

// New:
store := NewMemJobStore()
bundle := NewInMemBundle(10, 3, store)
defer bundle.Close()
bundle.Queue.Submit(ctx, job)
ch, _ := bundle.Queue.Receive(ctx)
```

For result bus tests:
```go
// Old: tr.Publish / tr.Subscribe
// New: bundle.Results.Publish / bundle.Results.Subscribe
```

For attachment tests (if any):
```go
// Old: tr.Prepare / tr.Resolve
// New: bundle.Attachments.Prepare / bundle.Attachments.Resolve
```

Update every test function in the file accordingly.

- [ ] **Step 9: Update integration tests in `internal/queue/integration_test.go`**

Same pattern — replace `NewInMemTransport` with `NewInMemBundle`, use bundle fields.

- [ ] **Step 10: Update worker tests in `internal/worker/pool_test.go`**

Replace transport usage with bundle. The worker Config now takes separate fields:

```go
// Old:
transport := queue.NewInMemTransport(10, store)
pool := NewPool(Config{
    Queue:       transport,
    Attachments: transport,
    Results:     transport,
    ...
})

// New:
bundle := queue.NewInMemBundle(10, 1, store)
pool := NewPool(Config{
    Queue:       bundle.Queue,
    Attachments: bundle.Attachments,
    Results:     bundle.Results,
    ...
})
```

Also update `Prepare` calls: `transport.Prepare(ctx, "j1", nil)` → `bundle.Attachments.Prepare(ctx, "j1", nil)`

- [ ] **Step 11: Verify all tests pass**

Run: `go test ./... -count=1`
Expected: All tests PASS.

- [ ] **Step 12: Commit**

```bash
git add internal/queue/ internal/worker/
git commit -m "refactor(queue): split InMemTransport into 5 focused structs + bundle factory"
```

---

### Task 4: Stream Event Parser

**Files:**
- Create: `internal/queue/stream.go`
- Create: `internal/queue/stream_test.go`

- [ ] **Step 1: Write stream parser tests**

```go
package queue

import (
	"strings"
	"testing"
)

func TestReadStreamJSON_ResultEvent(t *testing.T) {
	input := `{"type":"message_delta","delta":{"text":"Looking at code..."}}
{"type":"tool_use","name":"Read","input":{"file_path":"/src/main.go"}}
{"type":"message_delta","delta":{"text":"Found the issue..."}}
{"type":"result","result":"Final answer text here","total_cost_usd":0.042,"usage":{"input_tokens":8500,"output_tokens":1200}}`

	r := strings.NewReader(input)
	eventCh := make(chan StreamEvent, 100)
	text := readStreamJSON(r, eventCh)
	close(eventCh)

	if text != "Final answer text here" {
		t.Errorf("text = %q, want 'Final answer text here'", text)
	}

	var events []StreamEvent
	for e := range eventCh {
		events = append(events, e)
	}
	// Expect: message_delta, tool_use:Read, message_delta, result
	if len(events) < 3 {
		t.Fatalf("expected at least 3 events, got %d", len(events))
	}

	// Check tool_use event
	found := false
	for _, e := range events {
		if e.Type == "tool_use" && e.ToolName == "Read" {
			found = true
		}
	}
	if !found {
		t.Error("missing tool_use:Read event")
	}

	// Check result event has cost
	var resultEvent StreamEvent
	for _, e := range events {
		if e.Type == "result" {
			resultEvent = e
		}
	}
	if resultEvent.CostUSD != 0.042 {
		t.Errorf("cost = %f, want 0.042", resultEvent.CostUSD)
	}
	if resultEvent.InputTokens != 8500 {
		t.Errorf("input_tokens = %d, want 8500", resultEvent.InputTokens)
	}
}

func TestReadStreamJSON_FallbackToReassembly(t *testing.T) {
	// No result event — should reassemble from message_delta
	input := `{"type":"message_delta","delta":{"text":"Hello "}}
{"type":"message_delta","delta":{"text":"World"}}`

	r := strings.NewReader(input)
	eventCh := make(chan StreamEvent, 100)
	text := readStreamJSON(r, eventCh)
	close(eventCh)

	if text != "Hello World" {
		t.Errorf("reassembled text = %q, want 'Hello World'", text)
	}
}

func TestReadRawOutput(t *testing.T) {
	input := "line1\nline2\nline3"
	r := strings.NewReader(input)
	text := readRawOutput(r)
	if !strings.Contains(text, "line1") || !strings.Contains(text, "line3") {
		t.Errorf("raw output = %q", text)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/queue/ -run "TestReadStream|TestReadRaw" -v`
Expected: FAIL — functions not defined.

- [ ] **Step 3: Implement `internal/queue/stream.go`**

```go
package queue

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

type StreamEvent struct {
	Type         string
	ToolName     string
	TextBytes    int
	CostUSD      float64
	InputTokens  int
	OutputTokens int
}

// readStreamJSON reads NDJSON from claude --output-format stream-json.
// Returns final text from "result" event, or reassembled message_delta as fallback.
func readStreamJSON(r io.Reader, eventCh chan<- StreamEvent) string {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var reassembled strings.Builder
	var resultText string

	for scanner.Scan() {
		line := scanner.Text()
		var raw map[string]any
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}

		eventType, _ := raw["type"].(string)
		switch eventType {
		case "message_delta":
			if delta, ok := raw["delta"].(map[string]any); ok {
				if text, ok := delta["text"].(string); ok {
					reassembled.WriteString(text)
					select {
					case eventCh <- StreamEvent{Type: "message_delta", TextBytes: len(text)}:
					default:
					}
				}
			}

		case "tool_use":
			name, _ := raw["name"].(string)
			select {
			case eventCh <- StreamEvent{Type: "tool_use", ToolName: name}:
			default:
			}

		case "result":
			if res, ok := raw["result"].(string); ok {
				resultText = res
			}
			costEvent := StreamEvent{Type: "result"}
			if cost, ok := raw["total_cost_usd"].(float64); ok {
				costEvent.CostUSD = cost
			}
			if usage, ok := raw["usage"].(map[string]any); ok {
				if in, ok := usage["input_tokens"].(float64); ok {
					costEvent.InputTokens = int(in)
				}
				if out, ok := usage["output_tokens"].(float64); ok {
					costEvent.OutputTokens = int(out)
				}
			}
			select {
			case eventCh <- costEvent:
			default:
			}
		}
	}

	if resultText != "" {
		return resultText
	}
	return reassembled.String()
}

// readRawOutput reads plain text stdout (non-stream agents).
func readRawOutput(r io.Reader) string {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	var buf strings.Builder
	for scanner.Scan() {
		buf.WriteString(scanner.Text())
		buf.WriteByte('\n')
	}
	return buf.String()
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/queue/ -run "TestReadStream|TestReadRaw" -v`
Expected: All 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/queue/stream.go internal/queue/stream_test.go
git commit -m "feat(queue): add stream-json parser with result event + message_delta fallback"
```

---

### Task 5: ProcessRegistry

**Files:**
- Create: `internal/queue/registry.go`
- Create: `internal/queue/registry_test.go`

- [ ] **Step 1: Write tests**

```go
package queue

import (
	"context"
	"testing"
	"time"
)

func TestProcessRegistry_RegisterAndKill(t *testing.T) {
	reg := NewProcessRegistry()

	ctx, cancel := context.WithCancel(context.Background())
	reg.Register("j1", 12345, "claude", cancel)

	agent, ok := reg.Get("j1")
	if !ok {
		t.Fatal("expected agent to be registered")
	}
	if agent.PID != 12345 {
		t.Errorf("PID = %d, want 12345", agent.PID)
	}

	// Kill should cancel the context
	err := reg.Kill("j1")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil {
		t.Error("expected context to be cancelled after kill")
	}
}

func TestProcessRegistry_RemoveClosesDone(t *testing.T) {
	reg := NewProcessRegistry()
	_, cancel := context.WithCancel(context.Background())
	reg.Register("j1", 100, "claude", cancel)

	agent, _ := reg.Get("j1")
	done := agent.Done()

	reg.Remove("j1")

	select {
	case <-done:
		// expected
	case <-time.After(time.Second):
		t.Fatal("done channel not closed after Remove")
	}
}

func TestProcessRegistry_KillNotFound(t *testing.T) {
	reg := NewProcessRegistry()
	err := reg.Kill("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent job")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/queue/ -run TestProcessRegistry -v`
Expected: FAIL.

- [ ] **Step 3: Implement `internal/queue/registry.go`**

```go
package queue

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type ProcessRegistry struct {
	mu        sync.RWMutex
	processes map[string]*RunningAgent
}

type RunningAgent struct {
	JobID     string
	PID       int
	Command   string
	StartedAt time.Time
	cancel    context.CancelFunc
	done      chan struct{}
}

func (a *RunningAgent) Done() <-chan struct{} {
	return a.done
}

func NewProcessRegistry() *ProcessRegistry {
	return &ProcessRegistry{
		processes: make(map[string]*RunningAgent),
	}
}

func (r *ProcessRegistry) Register(jobID string, pid int, command string, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.processes[jobID] = &RunningAgent{
		JobID:     jobID,
		PID:       pid,
		Command:   command,
		StartedAt: time.Now(),
		cancel:    cancel,
		done:      make(chan struct{}),
	}
}

func (r *ProcessRegistry) Remove(jobID string) {
	r.mu.Lock()
	agent, ok := r.processes[jobID]
	if ok {
		delete(r.processes, jobID)
		close(agent.done)
	}
	r.mu.Unlock()
}

func (r *ProcessRegistry) Kill(jobID string) error {
	r.mu.RLock()
	agent, ok := r.processes[jobID]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("no running agent for job %q", jobID)
	}

	// Cancel job context → triggers cmd.Cancel (SIGTERM) → cmd.WaitDelay (auto SIGKILL)
	agent.cancel()

	select {
	case <-agent.done:
		return nil
	case <-time.After(15 * time.Second):
		return fmt.Errorf("kill timeout for job %q", jobID)
	}
}

func (r *ProcessRegistry) Get(jobID string) (*RunningAgent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	agent, ok := r.processes[jobID]
	return agent, ok
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/queue/ -run TestProcessRegistry -v`
Expected: All 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/queue/registry.go internal/queue/registry_test.go
git commit -m "feat(queue): add ProcessRegistry with cancel-based kill"
```

---

### Task 6: AgentRunner Refactor — RunOptions + StdoutPipe + cmd.Cancel

**Files:**
- Modify: `internal/bot/agent.go`
- Modify: `internal/bot/agent_test.go`

- [ ] **Step 1: Add RunOptions type and update Run signature**

In `internal/bot/agent.go`:

Remove `AgentStarted` type, `onStarted` field, `SetOnStarted` method.

Add:

```go
type RunOptions struct {
	OnStarted func(pid int, command string)
	OnEvent   func(event queue.StreamEvent)
}
```

Update `Run` signature:

```go
func (r *AgentRunner) Run(ctx context.Context, logger *slog.Logger, workDir, prompt string, opts RunOptions) (string, error) {
	var errs []string
	for i, agent := range r.agents {
		logger.Info("trying agent", "command", agent.Command, "index", i, "total", len(r.agents), "timeout", agent.Timeout)
		output, err := r.runOne(ctx, logger, agent, workDir, prompt, opts)
		if err != nil {
			logger.Warn("agent failed", "command", agent.Command, "index", i, "error", err)
			errs = append(errs, fmt.Sprintf("%s: %s", agent.Command, err))
			continue
		}
		logger.Info("agent succeeded", "command", agent.Command, "output_len", len(output))
		return output, nil
	}
	logger.Error("all agents exhausted", "errors", strings.Join(errs, "; "))
	return "", fmt.Errorf("all agents failed: %s", strings.Join(errs, "; "))
}
```

- [ ] **Step 2: Refactor `runOne` — cmd.Cancel, StdoutPipe, stream support**

Replace the entire `runOne` method:

```go
func (r *AgentRunner) runOne(ctx context.Context, logger *slog.Logger, agent config.AgentConfig, workDir, prompt string, opts RunOptions) (string, error) {
	timeout := agent.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	const maxArgLen = 32 * 1024

	hasPlaceholder := false
	for _, a := range agent.Args {
		if strings.Contains(a, "{prompt}") {
			hasPlaceholder = true
			break
		}
	}

	useStdin := !hasPlaceholder || len(prompt) >= maxArgLen
	var args []string
	if useStdin && hasPlaceholder {
		for _, a := range agent.Args {
			if !strings.Contains(a, "{prompt}") {
				args = append(args, a)
			}
		}
		logger.Info("prompt too large for args, using stdin", "prompt_len", len(prompt))
	} else {
		args = substitutePrompt(agent.Args, prompt)
	}

	cmd := exec.CommandContext(ctx, agent.Command, args...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), fmt.Sprintf("GH_TOKEN=%s", r.githubToken))

	// Graceful termination: SIGTERM first, auto-SIGKILL after 10s
	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 10 * time.Second

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if useStdin {
		cmd.Stdin = strings.NewReader(prompt)
	}

	if err := cmd.Start(); err != nil {
		return "", err
	}

	if opts.OnStarted != nil {
		opts.OnStarted(cmd.Process.Pid, agent.Command)
	}
	logger.Info("agent process started", "command", agent.Command, "pid", cmd.Process.Pid)

	// Read stdout — stream or raw mode
	var readerWg sync.WaitGroup
	readerWg.Add(1)
	var finalText string
	eventCh := make(chan queue.StreamEvent, 1000)

	go func() {
		defer readerWg.Done()
		defer close(eventCh)
		if agent.Stream {
			finalText = queue.ReadStreamJSON(stdoutPipe, eventCh)
		} else {
			finalText = queue.ReadRawOutput(stdoutPipe)
		}
	}()

	// Forward events to callback
	if opts.OnEvent != nil {
		go func() {
			for {
				select {
				case event, ok := <-eventCh:
					if !ok {
						return
					}
					opts.OnEvent(event)
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	readerWg.Wait()

	err = cmd.Wait()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("cancelled")
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("exit %d: %s", exitErr.ExitCode(), strings.TrimSpace(stderr.String()))
		}
		return "", err
	}

	return strings.TrimSpace(finalText), nil
}
```

Add `"sync"`, `"syscall"`, and `"slack-issue-bot/internal/queue"` to imports.

Note: Export `readStreamJSON` → `ReadStreamJSON` and `readRawOutput` → `ReadRawOutput` in `stream.go`.

- [ ] **Step 3: Remove TrackedRunner interface from executor.go**

In `internal/worker/executor.go`, remove the `TrackedRunner` interface definition entirely. Remove the `SetOnStarted` check block. The `Runner` interface stays, but now `executeJob` takes `RunOptions` as a parameter:

Update `executeJob` signature:

```go
func executeJob(ctx context.Context, job *queue.Job, deps executionDeps, opts bot.RunOptions) *queue.JobResult {
```

Replace the agent execution section:

```go
// Execute agent
deps.store.UpdateStatus(job.ID, queue.JobRunning)
output, err := deps.runner.Run(ctx, repoPath, job.Prompt, opts)
```

Update `Runner` interface:

```go
type Runner interface {
	Run(ctx context.Context, workDir, prompt string, opts bot.RunOptions) (string, error)
}
```

Remove `RepoProvider` and `TrackedRunner` interface — keep only `Runner`.

- [ ] **Step 4: Update adapter in main.go**

Replace `agentRunnerAdapter`:

```go
type agentRunnerAdapter struct {
	runner *bot.AgentRunner
}

func (a *agentRunnerAdapter) Run(ctx context.Context, workDir, prompt string, opts bot.RunOptions) (string, error) {
	return a.runner.Run(ctx, slog.Default(), workDir, prompt, opts)
}
```

Remove `SetOnStarted` method.

- [ ] **Step 5: Fix all test compilation**

Update mock runners in `internal/worker/pool_test.go`:

```go
type mockRunner struct {
	output string
	err    error
}

func (m *mockRunner) Run(ctx context.Context, workDir, prompt string, opts bot.RunOptions) (string, error) {
	return m.output, m.err
}
```

Update integration test mock runners similarly.

- [ ] **Step 6: Verify all tests pass**

Run: `go test ./... -count=1`
Expected: All tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/bot/agent.go internal/worker/ cmd/bot/main.go internal/queue/stream.go
git commit -m "refactor(agent): RunOptions per-call callbacks, cmd.Cancel/WaitDelay, stream-json support"
```

---

### Task 7: Worker Pool — Command Listener + Status Reporting + Post-Kill Cleanup

**Files:**
- Create: `internal/worker/status.go`
- Modify: `internal/worker/pool.go`
- Modify: `internal/worker/executor.go`

- [ ] **Step 1: Create `internal/worker/status.go` — statusAccumulator**

```go
package worker

import (
	"sync"
	"time"

	"slack-issue-bot/internal/queue"
)

type statusAccumulator struct {
	mu           sync.Mutex
	jobID        string
	workerID     string
	pid          int
	agentCmd     string
	alive        bool
	lastEvent    string
	lastEventAt  time.Time
	toolCalls    int
	filesRead    int
	outputBytes  int
	costUSD      float64
	inputTokens  int
	outputTokens int
}

func (s *statusAccumulator) setPID(pid int, cmd string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pid = pid
	s.agentCmd = cmd
	s.alive = true
}

func (s *statusAccumulator) recordEvent(event queue.StreamEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastEventAt = time.Now()
	switch event.Type {
	case "tool_use":
		s.toolCalls++
		s.lastEvent = "tool_use:" + event.ToolName
		if event.ToolName == "Read" {
			s.filesRead++
		}
	case "message_delta":
		s.outputBytes += event.TextBytes
		s.lastEvent = "message_delta"
	case "result":
		s.costUSD = event.CostUSD
		s.inputTokens = event.InputTokens
		s.outputTokens = event.OutputTokens
		s.lastEvent = "result"
	}
}

func (s *statusAccumulator) toReport() queue.StatusReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	return queue.StatusReport{
		JobID:        s.jobID,
		WorkerID:     s.workerID,
		PID:          s.pid,
		AgentCmd:     s.agentCmd,
		Alive:        s.alive,
		LastEvent:    s.lastEvent,
		LastEventAt:  s.lastEventAt,
		ToolCalls:    s.toolCalls,
		FilesRead:    s.filesRead,
		OutputBytes:  s.outputBytes,
		CostUSD:      s.costUSD,
		InputTokens:  s.inputTokens,
		OutputTokens: s.outputTokens,
	}
}
```

- [ ] **Step 2: Update `internal/worker/pool.go` — add CommandBus, StatusBus, ProcessRegistry**

Update `Config`:

```go
type Config struct {
	Queue          queue.JobQueue
	Attachments    queue.AttachmentStore
	Results        queue.ResultBus
	Commands       queue.CommandBus
	Status         queue.StatusBus
	Store          queue.JobStore
	Runner         Runner
	RepoCache      RepoProvider
	WorkerCount    int
	SkillDir       string
	StatusInterval time.Duration
}
```

Update `Pool`:

```go
type Pool struct {
	cfg      Config
	registry *queue.ProcessRegistry
}

func NewPool(cfg Config) *Pool {
	return &Pool{
		cfg:      cfg,
		registry: queue.NewProcessRegistry(),
	}
}
```

Update `Start`:

```go
func (p *Pool) Start(ctx context.Context) {
	if p.cfg.Commands != nil {
		go p.commandListener(ctx)
	}
	for i := 0; i < p.cfg.WorkerCount; i++ {
		go p.runWorker(ctx, i)
	}
	slog.Info("worker pool started", "count", p.cfg.WorkerCount)
}
```

Add `commandListener`:

```go
func (p *Pool) commandListener(ctx context.Context) {
	commands, err := p.cfg.Commands.Receive(ctx)
	if err != nil {
		slog.Error("failed to receive commands", "error", err)
		return
	}
	for {
		select {
		case cmd, ok := <-commands:
			if !ok {
				return
			}
			if cmd.Action == "kill" {
				if err := p.registry.Kill(cmd.JobID); err != nil {
					slog.Warn("kill command failed", "job_id", cmd.JobID, "error", err)
				}
			}
		case <-ctx.Done():
			return
		}
	}
}
```

Rewrite `runWorker`:

```go
func (p *Pool) runWorker(ctx context.Context, id int) {
	logger := slog.With("worker_id", id)
	jobs, err := p.cfg.Queue.Receive(ctx)
	if err != nil {
		logger.Error("failed to receive jobs", "error", err)
		return
	}

	for {
		select {
		case job, ok := <-jobs:
			if !ok {
				logger.Info("job channel closed, worker exiting")
				return
			}
			// Check if job was cancelled while pending
			state, err := p.cfg.Store.Get(job.ID)
			if err != nil || state.Status == queue.JobFailed {
				p.cfg.Results.Publish(ctx, &queue.JobResult{
					JobID: job.ID, Status: "failed", Error: "cancelled before execution",
				})
				continue
			}
			p.executeWithTracking(ctx, id, job)
		case <-ctx.Done():
			logger.Info("worker shutting down")
			return
		}
	}
}
```

Add `executeWithTracking`:

```go
func (p *Pool) executeWithTracking(ctx context.Context, workerID int, job *queue.Job) {
	logger := slog.With("worker_id", workerID, "job_id", job.ID)
	jobCtx, jobCancel := context.WithCancel(ctx)
	defer jobCancel()

	status := &statusAccumulator{
		jobID:    job.ID,
		workerID: fmt.Sprintf("worker-%d", workerID),
		alive:    true,
	}

	opts := bot.RunOptions{
		OnStarted: func(pid int, command string) {
			status.setPID(pid, command)
			p.registry.Register(job.ID, pid, command, jobCancel)
			logger.Info("agent registered", "pid", pid, "command", command)
		},
		OnEvent: func(event queue.StreamEvent) {
			status.recordEvent(event)
		},
	}

	// Start periodic status reporting
	var stopReporter chan struct{}
	if p.cfg.Status != nil {
		stopReporter = make(chan struct{})
		interval := p.cfg.StatusInterval
		if interval <= 0 {
			interval = 5 * time.Second
		}
		go p.reportStatus(jobCtx, status, interval, stopReporter)
	}

	// Ack + execute
	if err := p.cfg.Queue.Ack(jobCtx, job.ID); err != nil {
		logger.Error("ack failed", "error", err)
		p.cfg.Results.Publish(ctx, &queue.JobResult{
			JobID: job.ID, Status: "failed", Error: fmt.Sprintf("ack failed: %v", err),
		})
		return
	}

	deps := executionDeps{
		attachments: p.cfg.Attachments,
		repoCache:   p.cfg.RepoCache,
		runner:      p.cfg.Runner,
		store:       p.cfg.Store,
		skillDir:    p.cfg.SkillDir,
	}

	result := executeJob(jobCtx, job, deps, opts)

	// Post-execution cleanup
	if stopReporter != nil {
		close(stopReporter)
	}
	p.registry.Remove(job.ID)

	// Post-kill repo cleanup (git reset)
	if result.Status == "failed" {
		repoPath, err := p.cfg.RepoCache.Prepare(job.CloneURL, job.Branch)
		if err == nil {
			exec.Command("git", "-C", repoPath, "checkout", ".").Run()
			exec.Command("git", "-C", repoPath, "clean", "-fd").Run()
		}
	}

	p.cfg.Store.UpdateStatus(job.ID, queue.JobStatus(result.Status))
	if err := p.cfg.Results.Publish(ctx, result); err != nil {
		logger.Error("failed to publish result", "error", err)
	}
	logger.Info("job completed", "status", result.Status)
}

func (p *Pool) reportStatus(ctx context.Context, status *statusAccumulator, interval time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.cfg.Status.Report(ctx, status.toReport())
		case <-stop:
			return
		case <-ctx.Done():
			return
		}
	}
}
```

Add `"os/exec"` and `"slack-issue-bot/internal/bot"` to pool.go imports.

- [ ] **Step 3: Update `executeJob` in executor.go to pass RunOptions and add cost to result**

Update the return statement to include cost from the last stream event:

```go
func executeJob(ctx context.Context, job *queue.Job, deps executionDeps, opts bot.RunOptions) *queue.JobResult {
	startedAt := time.Now()

	attachments, err := deps.attachments.Resolve(ctx, job.ID)
	if err != nil {
		return failedResult(job, startedAt, fmt.Errorf("attachments failed: %w", err))
	}

	repoPath, err := deps.repoCache.Prepare(job.CloneURL, job.Branch)
	if err != nil {
		return failedResult(job, startedAt, fmt.Errorf("repo prepare failed: %w", err))
	}

	for _, att := range attachments {
		if att.URL != "" {
			_ = att
		}
	}

	if len(job.Skills) > 0 {
		if err := mountSkills(repoPath, job.Skills, deps.skillDir); err != nil {
			return failedResult(job, startedAt, fmt.Errorf("skill mount failed: %w", err))
		}
		defer cleanupSkills(repoPath, job.Skills, deps.skillDir)
	}

	deps.store.UpdateStatus(job.ID, queue.JobRunning)
	output, err := deps.runner.Run(ctx, repoPath, job.Prompt, opts)
	if err != nil {
		return failedResult(job, startedAt, err)
	}

	parsed, err := bot.ParseAgentOutput(output)
	if err != nil {
		return failedResult(job, startedAt, fmt.Errorf("parse failed: %w", err))
	}

	return &queue.JobResult{
		JobID:      job.ID,
		Status:     "completed",
		Title:      parsed.Title,
		Body:       parsed.Body,
		Labels:     parsed.Labels,
		Confidence: parsed.Confidence,
		FilesFound: parsed.FilesFound,
		Questions:  parsed.Questions,
		RawOutput:  output,
		StartedAt:  startedAt,
		FinishedAt: time.Now(),
	}
}
```

- [ ] **Step 4: Fix tests, verify compilation**

Run: `go build ./... && go test ./... -count=1`
Expected: All pass.

- [ ] **Step 5: Commit**

```bash
git add internal/worker/
git commit -m "feat(worker): command listener, status reporting, per-job context, post-kill cleanup"
```

---

### Task 8: Watchdog Enhancements — Idle Detection + Prepare Timeout + CommandBus Kill

**Files:**
- Modify: `internal/queue/watchdog.go`

- [ ] **Step 1: Update Watchdog struct and StuckNotifier**

```go
type StuckNotifier func(job *Job, status JobStatus, reason string)

type Watchdog struct {
	store          JobStore
	commands       CommandBus
	jobTimeout     time.Duration
	idleTimeout    time.Duration
	prepareTimeout time.Duration
	interval       time.Duration
	notifier       StuckNotifier
}

func NewWatchdog(store JobStore, commands CommandBus, cfg WatchdogConfig, notifier StuckNotifier) *Watchdog {
	interval := cfg.JobTimeout / 3
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	return &Watchdog{
		store:          store,
		commands:       commands,
		jobTimeout:     cfg.JobTimeout,
		idleTimeout:    cfg.IdleTimeout,
		prepareTimeout: cfg.PrepareTimeout,
		interval:       interval,
		notifier:       notifier,
	}
}

type WatchdogConfig struct {
	JobTimeout     time.Duration
	IdleTimeout    time.Duration
	PrepareTimeout time.Duration
}
```

- [ ] **Step 2: Update check method with three timeout checks**

```go
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

		// 1. Job-level timeout
		if now.Sub(state.Job.SubmittedAt) > w.jobTimeout {
			w.killAndNotify(state, "job timeout")
			continue
		}

		// 2. Prepare timeout
		if state.Status == JobPreparing && w.prepareTimeout > 0 {
			if state.AgentStatus == nil || state.AgentStatus.LastEventAt.IsZero() {
				if !state.StartedAt.IsZero() && now.Sub(state.StartedAt) > w.prepareTimeout {
					w.killAndNotify(state, "prepare timeout")
					continue
				}
			}
		}

		// 3. Agent idle timeout (stream-json agents only)
		if w.idleTimeout > 0 && state.AgentStatus != nil && !state.AgentStatus.LastEventAt.IsZero() {
			if now.Sub(state.AgentStatus.LastEventAt) > w.idleTimeout {
				w.killAndNotify(state, "agent idle timeout")
				continue
			}
		}
	}
}

func (w *Watchdog) killAndNotify(state *JobState, reason string) {
	slog.Warn("watchdog: killing stuck job",
		"job_id", state.Job.ID, "status", state.Status, "reason", reason)
	if w.commands != nil {
		w.commands.Send(context.Background(), Command{JobID: state.Job.ID, Action: "kill"})
	}
	w.store.UpdateStatus(state.Job.ID, JobFailed)
	if w.notifier != nil {
		w.notifier(state.Job, state.Status, reason)
	}
}
```

- [ ] **Step 3: Update FormatStuckMessage**

```go
func FormatStuckMessage(job *Job, status JobStatus, reason string) string {
	return fmt.Sprintf(":warning: Job 已終止 (%s)，狀態停在 `%s`，repo: `%s`。請重新觸發。",
		reason, status, job.Repo)
}
```

- [ ] **Step 4: Verify compilation**

Run: `go build ./...`
Expected: Success (main.go will need updating in Task 11).

- [ ] **Step 5: Commit**

```bash
git add internal/queue/watchdog.go
git commit -m "feat(watchdog): add idle detection, prepare timeout, CommandBus kill"
```

---

### Task 9: StatusListener

**Files:**
- Create: `internal/bot/status_listener.go`

- [ ] **Step 1: Implement StatusListener**

```go
package bot

import (
	"context"
	"log/slog"

	"slack-issue-bot/internal/queue"
)

type StatusListener struct {
	status queue.StatusBus
	store  queue.JobStore
}

func NewStatusListener(status queue.StatusBus, store queue.JobStore) *StatusListener {
	return &StatusListener{status: status, store: store}
}

func (l *StatusListener) Listen(ctx context.Context) {
	ch, err := l.status.Subscribe(ctx)
	if err != nil {
		slog.Error("failed to subscribe to status bus", "error", err)
		return
	}
	for {
		select {
		case report, ok := <-ch:
			if !ok {
				return
			}
			l.store.SetAgentStatus(report.JobID, report)
		case <-ctx.Done():
			return
		}
	}
}
```

- [ ] **Step 2: Commit**

```bash
git add internal/bot/status_listener.go
git commit -m "feat(bot): add StatusListener — updates JobStore from worker status reports"
```

---

### Task 10: HTTP Endpoints — Enhanced /jobs + DELETE /jobs/{id}

**Files:**
- Modify: `internal/queue/httpstatus.go`

- [ ] **Step 1: Update job entry to use AgentStatus**

Replace `AgentPID`, `AgentCommand`, `AgentAlive` fields in `jobStatusEntry` with a nested struct:

```go
type agentStatusEntry struct {
	PID          int     `json:"pid,omitempty"`
	Command      string  `json:"command,omitempty"`
	Alive        bool    `json:"alive,omitempty"`
	LastEvent    string  `json:"last_event,omitempty"`
	LastEventAge string  `json:"last_event_age,omitempty"`
	ToolCalls    int     `json:"tool_calls,omitempty"`
	FilesRead    int     `json:"files_read,omitempty"`
	OutputBytes  int     `json:"output_bytes,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
}

type jobStatusEntry struct {
	ID        string            `json:"id"`
	Status    JobStatus         `json:"status"`
	Repo      string            `json:"repo"`
	Branch    string            `json:"branch,omitempty"`
	Position  int               `json:"position,omitempty"`
	Age       string            `json:"age"`
	WaitTime  string            `json:"wait_time,omitempty"`
	WorkerID  string            `json:"worker_id,omitempty"`
	Agent     *agentStatusEntry `json:"agent,omitempty"`
	ChannelID string            `json:"channel_id"`
	ThreadTS  string            `json:"thread_ts"`
}
```

Update the loop to populate from `state.AgentStatus`:

```go
if state.AgentStatus != nil {
	as := state.AgentStatus
	agentEntry := &agentStatusEntry{
		PID:          as.PID,
		Command:      as.AgentCmd,
		Alive:        as.Alive,
		ToolCalls:    as.ToolCalls,
		FilesRead:    as.FilesRead,
		OutputBytes:  as.OutputBytes,
		CostUSD:      as.CostUSD,
		InputTokens:  as.InputTokens,
		OutputTokens: as.OutputTokens,
	}
	if !as.LastEventAt.IsZero() {
		agentEntry.LastEvent = as.LastEvent
		agentEntry.LastEventAge = now.Sub(as.LastEventAt).Truncate(time.Second).String()
	}
	if as.PID > 0 {
		agentEntry.Alive = isProcessAlive(as.PID)
	}
	entry.Agent = agentEntry
}
```

- [ ] **Step 2: Add DELETE /jobs/{id} handler**

```go
func KillHandler(store JobStore, commands CommandBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Extract job ID from path: /jobs/{id}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/jobs/"), "/")
		if len(parts) == 0 || parts[0] == "" {
			http.Error(w, `{"error":"job ID required"}`, http.StatusBadRequest)
			return
		}
		jobID := parts[0]

		state, err := store.Get(jobID)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "job not found"})
			return
		}

		if state.Status == JobCompleted || state.Status == JobFailed {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"error": "job not running"})
			return
		}

		store.UpdateStatus(jobID, JobFailed)
		if commands != nil {
			commands.Send(r.Context(), Command{JobID: jobID, Action: "kill"})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "killing", "job_id": jobID})
	}
}
```

Add `"strings"` to imports.

- [ ] **Step 3: Verify compilation**

Run: `go build ./...`

- [ ] **Step 4: Commit**

```bash
git add internal/queue/httpstatus.go
git commit -m "feat(http): enhanced /jobs with agent status, add DELETE /jobs/{id} kill endpoint"
```

---

### Task 11: Wire Everything in main.go

**Files:**
- Modify: `cmd/bot/main.go`

- [ ] **Step 1: Replace InMemTransport with InMemBundle**

Replace:
```go
jobQueue := queue.NewInMemTransport(cfg.Queue.Capacity, jobStore)
```

With:
```go
bundle := queue.NewInMemBundle(cfg.Queue.Capacity, cfg.Workers.Count, jobStore)
```

- [ ] **Step 2: Update worker pool creation**

```go
workerPool := worker.NewPool(worker.Config{
	Queue:          bundle.Queue,
	Attachments:    bundle.Attachments,
	Results:        bundle.Results,
	Commands:       bundle.Commands,
	Status:         bundle.Status,
	Store:          jobStore,
	Runner:         &agentRunnerAdapter{runner: agentRunner},
	RepoCache:      &repoCacheAdapter{cache: repoCache},
	WorkerCount:    cfg.Workers.Count,
	SkillDir:       skillDir,
	StatusInterval: cfg.Queue.StatusInterval,
})
```

- [ ] **Step 3: Update workflow, result listener, and watchdog**

```go
wf := bot.NewWorkflow(cfg, slackClient, repoCache, repoDiscovery, agentRunner, mantisClient,
	bundle.Queue, jobStore, skills)

resultListener := bot.NewResultListener(bundle.Results, jobStore, bundle.Attachments,
	slackAdapter, issueClient)
go resultListener.Listen(context.Background())

statusListener := bot.NewStatusListener(bundle.Status, jobStore)
go statusListener.Listen(context.Background())

watchdog := queue.NewWatchdog(jobStore, bundle.Commands, queue.WatchdogConfig{
	JobTimeout:     cfg.Queue.JobTimeout,
	IdleTimeout:    cfg.Queue.AgentIdleTimeout,
	PrepareTimeout: cfg.Queue.PrepareTimeout,
}, func(job *queue.Job, status queue.JobStatus, reason string) {
	msg := queue.FormatStuckMessage(job, status, reason)
	slackAdapter.PostMessage(job.ChannelID, msg, job.ThreadTS)
	handler.ClearThreadDedup(job.ChannelID, job.ThreadTS)
})
go watchdog.Start(make(chan struct{}))
```

- [ ] **Step 4: Update HTTP handlers**

```go
http.HandleFunc("/jobs", queue.StatusHandler(jobStore, bundle.Queue))
http.Handle("/jobs/", queue.KillHandler(jobStore, bundle.Commands))
```

- [ ] **Step 5: Remove agentRunnerAdapter.SetOnStarted**

Already done in Task 6, but verify it's gone.

- [ ] **Step 6: Build and test**

Run: `go build -o bot ./cmd/bot/ && go test ./... -count=1`
Expected: Build succeeds, all tests pass.

- [ ] **Step 7: Commit**

```bash
git add cmd/bot/main.go
git commit -m "feat(main): wire InMemBundle, StatusListener, enhanced watchdog, kill endpoint"
```

---

### Task 12: Slack Cancel Button

**Files:**
- Modify: `internal/slack/client.go`
- Modify: `internal/bot/workflow.go`
- Modify: `cmd/bot/main.go`

- [ ] **Step 1: Add PostMessageWithButton to slack client**

In `internal/slack/client.go`, add:

```go
func (c *Client) PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value string) (string, error) {
	btnBlock := slack.NewActionBlock("cancel_actions",
		slack.NewButtonBlockElement(actionID, value,
			slack.NewTextBlockObject("plain_text", buttonText, false, false)),
	)
	textBlock := slack.NewSectionBlock(
		slack.NewTextBlockObject("mrkdwn", text, false, false), nil, nil)

	opts := []slack.MsgOption{
		slack.MsgOptionBlocks(textBlock, btnBlock),
	}
	if threadTS != "" {
		opts = append(opts, slack.MsgOptionTS(threadTS))
	}
	_, ts, err := c.api.PostMessage(channelID, opts...)
	if err != nil {
		return "", fmt.Errorf("post message with button: %w", err)
	}
	return ts, nil
}
```

- [ ] **Step 2: Update workflow.go to use cancel button**

In the queue position feedback section of `runTriage`, replace `w.slack.PostMessage` calls with:

```go
if pos <= 1 {
	w.slack.PostMessageWithButton(pt.ChannelID,
		":hourglass_flowing_sand: 正在處理你的請求...",
		pt.ThreadTS, "cancel_job", "取消", job.ID)
} else {
	w.slack.PostMessageWithButton(pt.ChannelID,
		fmt.Sprintf(":hourglass_flowing_sand: 已加入排隊，前面有 %d 個請求", pos-1),
		pt.ThreadTS, "cancel_job", "取消", job.ID)
}
```

- [ ] **Step 3: Add cancel button handler in main.go Socket Mode loop**

In the `InteractionTypeBlockActions` switch, add:

```go
case strings.HasPrefix(action.ActionID, "cancel_job"):
	jobID := action.Value
	state, err := jobStore.Get(jobID)
	if err == nil && state.Status != queue.JobFailed && state.Status != queue.JobCompleted {
		bundle.Commands.Send(context.Background(), queue.Command{JobID: jobID, Action: "kill"})
		jobStore.UpdateStatus(jobID, queue.JobFailed)
		slackClient.UpdateMessage(cb.Channel.ID, selectorTS, ":stop_sign: 正在取消...")
		handler.ClearThreadDedup(cb.Channel.ID, state.Job.ThreadTS)
	} else {
		slackClient.UpdateMessage(cb.Channel.ID, selectorTS, ":information_source: 此任務已結束")
	}
```

- [ ] **Step 4: Build and test**

Run: `go build ./...`
Expected: Success.

- [ ] **Step 5: Commit**

```bash
git add internal/slack/client.go internal/bot/workflow.go cmd/bot/main.go
git commit -m "feat(slack): add cancel button for running jobs"
```

---

### Task 13: Update Config Files

**Files:**
- Modify: `config.yaml`
- Modify: `config.example.yaml`

- [ ] **Step 1: Update config.yaml**

Add stream and tracking settings:

```yaml
agents:
  claude:
    command: claude
    args: ["--print", "--output-format", "stream-json", "-p", "{prompt}"]
    timeout: 15m
    skill_dir: ".claude/skills"
    stream: true

queue:
  capacity: 50
  transport: inmem
  job_timeout: 20m
  agent_idle_timeout: 5m
  prepare_timeout: 3m
  status_interval: 5s
```

- [ ] **Step 2: Update config.example.yaml similarly**

- [ ] **Step 3: Commit**

```bash
git add config.yaml config.example.yaml
git commit -m "chore(config): add stream-json, agent tracking timeouts"
```

---

### Task 14: Final Verification

- [ ] **Step 1: Run full test suite**

Run: `go test ./... -v -count=1`
Expected: All tests PASS.

- [ ] **Step 2: Build binary**

Run: `go build -o bot ./cmd/bot/`
Expected: Success.

- [ ] **Step 3: Verify kill endpoint works conceptually**

Run: `go test ./internal/queue/ -run TestProcessRegistry -v`
Expected: Kill + done channel tests pass.

- [ ] **Step 4: Final commit if needed**

```bash
git status
# If clean, done. Otherwise:
git add -A && git commit -m "chore: final cleanup for agent tracking + kill"
```
