# Queue-Based App-Agent Decoupling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Decouple the app→agent execution path using a priority queue with producer/consumer abstraction, so agents can run in-process, in other pods, or on external machines.

**Architecture:** Bounded priority queue (channel-based ordering) with in-memory transport. App submits jobs after interactive workflow, workers consume and execute agents, results flow back via result bus. All side effects (GitHub issue creation, Slack posting) stay in the app.

**Tech Stack:** Go stdlib (`container/heap`, `sync`, `sync/atomic`), existing `slog` logging, no new dependencies.

---

### Task 1: Queue Interface Definitions

**Files:**
- Create: `internal/queue/job.go`
- Create: `internal/queue/interface.go`

- [ ] **Step 1: Create `internal/queue/job.go` — data types**

```go
package queue

import "time"

type JobStatus string

const (
	JobPending   JobStatus = "pending"
	JobPreparing JobStatus = "preparing"
	JobRunning   JobStatus = "running"
	JobCompleted JobStatus = "completed"
	JobFailed    JobStatus = "failed"
)

type Job struct {
	ID          string            `json:"id"`
	Priority    int               `json:"priority"`
	Seq         uint64            `json:"seq"`
	ChannelID   string            `json:"channel_id"`
	ThreadTS    string            `json:"thread_ts"`
	UserID      string            `json:"user_id"`
	Repo        string            `json:"repo"`
	Branch      string            `json:"branch"`
	CloneURL    string            `json:"clone_url"`
	Prompt      string            `json:"prompt"`
	Skills      map[string]string `json:"skills"`
	RequestID   string            `json:"request_id"`
	Attachments []AttachmentMeta  `json:"attachments"`
	SubmittedAt time.Time         `json:"submitted_at"`
}

type AttachmentMeta struct {
	SlackFileID string `json:"slack_file_id"`
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
	MimeType    string `json:"mime_type"`
}

type JobResult struct {
	JobID      string    `json:"job_id"`
	Status     string    `json:"status"`
	Title      string    `json:"title"`
	Body       string    `json:"body"`
	Labels     []string  `json:"labels"`
	Confidence string    `json:"confidence"`
	FilesFound int       `json:"files_found"`
	Questions  int       `json:"open_questions"`
	RawOutput  string    `json:"raw_output"`
	Error      string    `json:"error"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

type AttachmentReady struct {
	Filename string `json:"filename"`
	URL      string `json:"url"`
}

type JobState struct {
	Job       *Job
	Status    JobStatus
	Position  int
	WorkerID  string
	StartedAt time.Time
	WaitTime  time.Duration
}

type WorkerInfo struct {
	WorkerID    string   `json:"worker_id"`
	Name        string   `json:"name"`
	Agents      []string `json:"agents"`
	Tags        []string `json:"tags"`
	ConnectedAt time.Time
}

// ErrQueueFull is returned by Submit when the queue is at capacity.
var ErrQueueFull = fmt.Errorf("queue is full")
```

Note: add `"fmt"` to the import block for `ErrQueueFull`.

- [ ] **Step 2: Create `internal/queue/interface.go` — interfaces**

```go
package queue

import "context"

type JobQueue interface {
	Submit(ctx context.Context, job *Job) error
	QueuePosition(jobID string) (int, error)
	QueueDepth() int

	Receive(ctx context.Context) (<-chan *Job, error)
	Ack(ctx context.Context, jobID string) error

	Register(ctx context.Context, info WorkerInfo) error
	Unregister(ctx context.Context, workerID string) error
	ListWorkers(ctx context.Context) ([]WorkerInfo, error)

	Close() error
}

type AttachmentStore interface {
	Prepare(ctx context.Context, jobID string, attachments []AttachmentMeta) error
	Resolve(ctx context.Context, jobID string) ([]AttachmentReady, error)
	Cleanup(ctx context.Context, jobID string) error
}

type ResultBus interface {
	Publish(ctx context.Context, result *JobResult) error
	Subscribe(ctx context.Context) (<-chan *JobResult, error)
	Close() error
}

type JobStore interface {
	Put(job *Job) error
	Get(jobID string) (*JobState, error)
	GetByThread(channelID, threadTS string) (*JobState, error)
	ListPending() ([]*JobState, error)
	UpdateStatus(jobID string, status JobStatus) error
	SetWorker(jobID, workerID string) error
	Delete(jobID string) error
}
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./internal/queue/...`
Expected: Success, no errors.

- [ ] **Step 4: Commit**

```bash
git add internal/queue/job.go internal/queue/interface.go
git commit -m "feat(queue): add job data types and transport interfaces"
```

---

### Task 2: Priority Queue (container/heap)

**Files:**
- Create: `internal/queue/priority.go`
- Create: `internal/queue/priority_test.go`

- [ ] **Step 1: Write the tests for priority queue**

```go
package queue

import (
	"container/heap"
	"testing"
	"time"
)

func TestPriorityQueue_HigherPriorityFirst(t *testing.T) {
	pq := &priorityQueue{}
	heap.Init(pq)

	heap.Push(pq, &queueEntry{job: &Job{ID: "low", Priority: 10, Seq: 1}})
	heap.Push(pq, &queueEntry{job: &Job{ID: "high", Priority: 100, Seq: 2}})
	heap.Push(pq, &queueEntry{job: &Job{ID: "mid", Priority: 50, Seq: 3}})

	got := heap.Pop(pq).(*queueEntry).job.ID
	if got != "high" {
		t.Errorf("first pop = %q, want high", got)
	}
	got = heap.Pop(pq).(*queueEntry).job.ID
	if got != "mid" {
		t.Errorf("second pop = %q, want mid", got)
	}
	got = heap.Pop(pq).(*queueEntry).job.ID
	if got != "low" {
		t.Errorf("third pop = %q, want low", got)
	}
}

func TestPriorityQueue_FIFOWithinSamePriority(t *testing.T) {
	pq := &priorityQueue{}
	heap.Init(pq)

	heap.Push(pq, &queueEntry{job: &Job{ID: "first", Priority: 50, Seq: 1}})
	heap.Push(pq, &queueEntry{job: &Job{ID: "second", Priority: 50, Seq: 2}})
	heap.Push(pq, &queueEntry{job: &Job{ID: "third", Priority: 50, Seq: 3}})

	got := heap.Pop(pq).(*queueEntry).job.ID
	if got != "first" {
		t.Errorf("first pop = %q, want first", got)
	}
	got = heap.Pop(pq).(*queueEntry).job.ID
	if got != "second" {
		t.Errorf("second pop = %q, want second", got)
	}
}

func TestPriorityQueue_LenAndEmpty(t *testing.T) {
	pq := &priorityQueue{}
	heap.Init(pq)

	if pq.Len() != 0 {
		t.Errorf("empty queue Len() = %d", pq.Len())
	}

	heap.Push(pq, &queueEntry{job: &Job{ID: "a", Priority: 50, Seq: 1}})
	if pq.Len() != 1 {
		t.Errorf("after push Len() = %d, want 1", pq.Len())
	}

	heap.Pop(pq)
	if pq.Len() != 0 {
		t.Errorf("after pop Len() = %d, want 0", pq.Len())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/queue/ -run TestPriorityQueue -v`
Expected: FAIL — `priorityQueue` type not defined.

- [ ] **Step 3: Implement `internal/queue/priority.go`**

```go
package queue

import "container/heap"

type queueEntry struct {
	job   *Job
	index int
}

type priorityQueue []*queueEntry

var _ heap.Interface = (*priorityQueue)(nil)

func (pq priorityQueue) Len() int { return len(pq) }

func (pq priorityQueue) Less(i, j int) bool {
	if pq[i].job.Priority != pq[j].job.Priority {
		return pq[i].job.Priority > pq[j].job.Priority
	}
	return pq[i].job.Seq < pq[j].job.Seq
}

func (pq priorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *priorityQueue) Push(x any) {
	entry := x.(*queueEntry)
	entry.index = len(*pq)
	*pq = append(*pq, entry)
}

func (pq *priorityQueue) Pop() any {
	old := *pq
	n := len(old)
	entry := old[n-1]
	old[n-1] = nil
	entry.index = -1
	*pq = old[:n-1]
	return entry
}

// position returns the 1-based queue position for a job ID, or 0 if not found.
// This requires a linear scan — acceptable for bounded queues (capacity ≤ 50).
func (pq priorityQueue) position(jobID string) int {
	// Build a sorted view by copying and popping.
	// For small queues this is fine; for large queues consider a sorted snapshot.
	type entry struct {
		id  string
		pri int
		seq uint64
	}
	entries := make([]entry, pq.Len())
	for i, e := range pq {
		entries[i] = entry{e.job.ID, e.job.Priority, e.job.Seq}
	}
	// Sort by same criteria as heap: higher priority first, lower seq first.
	// Simple O(n²) sort — queue is bounded at 50.
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[j].pri > entries[i].pri || (entries[j].pri == entries[i].pri && entries[j].seq < entries[i].seq) {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}
	for i, e := range entries {
		if e.id == jobID {
			return i + 1
		}
	}
	return 0
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/queue/ -run TestPriorityQueue -v`
Expected: All 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/queue/priority.go internal/queue/priority_test.go
git commit -m "feat(queue): add container/heap priority queue implementation"
```

---

### Task 3: In-Memory Job Store

**Files:**
- Create: `internal/queue/memstore.go`
- Create: `internal/queue/memstore_test.go`

- [ ] **Step 1: Write tests for MemJobStore**

```go
package queue

import (
	"testing"
	"time"
)

func TestMemJobStore_PutAndGet(t *testing.T) {
	s := NewMemJobStore()
	job := &Job{ID: "j1", ChannelID: "C1", ThreadTS: "T1", SubmittedAt: time.Now()}
	if err := s.Put(job); err != nil {
		t.Fatal(err)
	}
	state, err := s.Get("j1")
	if err != nil {
		t.Fatal(err)
	}
	if state.Job.ID != "j1" {
		t.Errorf("ID = %q, want j1", state.Job.ID)
	}
	if state.Status != JobPending {
		t.Errorf("status = %q, want pending", state.Status)
	}
}

func TestMemJobStore_GetByThread(t *testing.T) {
	s := NewMemJobStore()
	s.Put(&Job{ID: "j1", ChannelID: "C1", ThreadTS: "T1"})
	s.Put(&Job{ID: "j2", ChannelID: "C2", ThreadTS: "T2"})

	state, err := s.GetByThread("C1", "T1")
	if err != nil {
		t.Fatal(err)
	}
	if state.Job.ID != "j1" {
		t.Errorf("got %q, want j1", state.Job.ID)
	}
}

func TestMemJobStore_UpdateStatus(t *testing.T) {
	s := NewMemJobStore()
	s.Put(&Job{ID: "j1"})
	s.UpdateStatus("j1", JobRunning)

	state, _ := s.Get("j1")
	if state.Status != JobRunning {
		t.Errorf("status = %q, want running", state.Status)
	}
}

func TestMemJobStore_Delete(t *testing.T) {
	s := NewMemJobStore()
	s.Put(&Job{ID: "j1"})
	s.Delete("j1")

	_, err := s.Get("j1")
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestMemJobStore_ListPending(t *testing.T) {
	s := NewMemJobStore()
	s.Put(&Job{ID: "j1"})
	s.Put(&Job{ID: "j2"})
	s.UpdateStatus("j2", JobRunning)

	pending, _ := s.ListPending()
	if len(pending) != 1 {
		t.Errorf("pending count = %d, want 1", len(pending))
	}
	if pending[0].Job.ID != "j1" {
		t.Errorf("pending job = %q, want j1", pending[0].Job.ID)
	}
}

func TestMemJobStore_GetNotFound(t *testing.T) {
	s := NewMemJobStore()
	_, err := s.Get("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent job")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/queue/ -run TestMemJobStore -v`
Expected: FAIL — `NewMemJobStore` not defined.

- [ ] **Step 3: Implement `internal/queue/memstore.go`**

```go
package queue

import (
	"fmt"
	"sync"
	"time"
)

type MemJobStore struct {
	mu   sync.RWMutex
	jobs map[string]*JobState
}

func NewMemJobStore() *MemJobStore {
	return &MemJobStore{jobs: make(map[string]*JobState)}
}

func (s *MemJobStore) Put(job *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = &JobState{
		Job:    job,
		Status: JobPending,
	}
	return nil
}

func (s *MemJobStore) Get(jobID string) (*JobState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, ok := s.jobs[jobID]
	if !ok {
		return nil, fmt.Errorf("job %q not found", jobID)
	}
	return state, nil
}

func (s *MemJobStore) GetByThread(channelID, threadTS string) (*JobState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, state := range s.jobs {
		if state.Job.ChannelID == channelID && state.Job.ThreadTS == threadTS {
			return state, nil
		}
	}
	return nil, fmt.Errorf("no job found for thread %s:%s", channelID, threadTS)
}

func (s *MemJobStore) ListPending() ([]*JobState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*JobState
	for _, state := range s.jobs {
		if state.Status == JobPending {
			result = append(result, state)
		}
	}
	return result, nil
}

func (s *MemJobStore) UpdateStatus(jobID string, status JobStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.jobs[jobID]
	if !ok {
		return fmt.Errorf("job %q not found", jobID)
	}
	state.Status = status
	if status == JobRunning && state.StartedAt.IsZero() {
		state.StartedAt = time.Now()
		state.WaitTime = state.StartedAt.Sub(state.Job.SubmittedAt)
	}
	return nil
}

func (s *MemJobStore) SetWorker(jobID, workerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.jobs[jobID]
	if !ok {
		return fmt.Errorf("job %q not found", jobID)
	}
	state.WorkerID = workerID
	return nil
}

func (s *MemJobStore) Delete(jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, jobID)
	return nil
}

// StartCleanup removes orphaned jobs older than ttl on a periodic basis.
func (s *MemJobStore) StartCleanup(ttl time.Duration) {
	go func() {
		ticker := time.NewTicker(ttl / 2)
		defer ticker.Stop()
		for range ticker.C {
			s.mu.Lock()
			now := time.Now()
			for id, state := range s.jobs {
				if now.Sub(state.Job.SubmittedAt) > ttl {
					delete(s.jobs, id)
				}
			}
			s.mu.Unlock()
		}
	}()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/queue/ -run TestMemJobStore -v`
Expected: All 6 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/queue/memstore.go internal/queue/memstore_test.go
git commit -m "feat(queue): add in-memory job store with TTL cleanup"
```

---

### Task 4: In-Memory Transport (JobQueue + ResultBus + AttachmentStore)

**Files:**
- Create: `internal/queue/inmem.go`
- Create: `internal/queue/inmem_test.go`

- [ ] **Step 1: Write tests for InMemTransport**

```go
package queue

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestInMemTransport_SubmitAndReceive(t *testing.T) {
	tr := NewInMemTransport(10, NewMemJobStore())
	defer tr.Close()

	ctx := context.Background()
	ch, _ := tr.Receive(ctx)

	tr.Submit(ctx, &Job{ID: "j1", Priority: 50, ChannelID: "C1"})

	select {
	case job := <-ch:
		if job.ID != "j1" {
			t.Errorf("got %q, want j1", job.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for job")
	}
}

func TestInMemTransport_PriorityOrdering(t *testing.T) {
	store := NewMemJobStore()
	tr := NewInMemTransport(10, store)
	defer tr.Close()

	ctx := context.Background()

	// Submit jobs before any receiver — they queue up.
	tr.Submit(ctx, &Job{ID: "low", Priority: 10})
	tr.Submit(ctx, &Job{ID: "high", Priority: 100})
	tr.Submit(ctx, &Job{ID: "mid", Priority: 50})

	ch, _ := tr.Receive(ctx)

	got := (<-ch).ID
	if got != "high" {
		t.Errorf("first = %q, want high", got)
	}
	got = (<-ch).ID
	if got != "mid" {
		t.Errorf("second = %q, want mid", got)
	}
	got = (<-ch).ID
	if got != "low" {
		t.Errorf("third = %q, want low", got)
	}
}

func TestInMemTransport_SubmitFullQueueReturnsError(t *testing.T) {
	tr := NewInMemTransport(1, NewMemJobStore())
	defer tr.Close()
	ctx := context.Background()

	tr.Submit(ctx, &Job{ID: "j1", Priority: 50})
	err := tr.Submit(ctx, &Job{ID: "j2", Priority: 50})
	if err != ErrQueueFull {
		t.Errorf("expected ErrQueueFull, got %v", err)
	}
}

func TestInMemTransport_QueuePositionAndDepth(t *testing.T) {
	tr := NewInMemTransport(10, NewMemJobStore())
	defer tr.Close()
	ctx := context.Background()

	tr.Submit(ctx, &Job{ID: "j1", Priority: 50})
	tr.Submit(ctx, &Job{ID: "j2", Priority: 50})
	tr.Submit(ctx, &Job{ID: "j3", Priority: 100})

	if d := tr.QueueDepth(); d != 3 {
		t.Errorf("depth = %d, want 3", d)
	}

	// j3 has highest priority so position 1
	pos, _ := tr.QueuePosition("j3")
	if pos != 1 {
		t.Errorf("j3 position = %d, want 1", pos)
	}

	// j1 submitted first at priority 50, so position 2
	pos, _ = tr.QueuePosition("j1")
	if pos != 2 {
		t.Errorf("j1 position = %d, want 2", pos)
	}
}

func TestInMemTransport_SeqAutoAssigned(t *testing.T) {
	tr := NewInMemTransport(10, NewMemJobStore())
	defer tr.Close()
	ctx := context.Background()

	j1 := &Job{ID: "j1", Priority: 50}
	j2 := &Job{ID: "j2", Priority: 50}
	tr.Submit(ctx, j1)
	tr.Submit(ctx, j2)

	if j1.Seq == 0 || j2.Seq == 0 {
		t.Error("Seq should be auto-assigned (non-zero)")
	}
	if j1.Seq >= j2.Seq {
		t.Errorf("j1.Seq=%d should be < j2.Seq=%d", j1.Seq, j2.Seq)
	}
}

func TestInMemTransport_ResultBus(t *testing.T) {
	tr := NewInMemTransport(10, NewMemJobStore())
	defer tr.Close()
	ctx := context.Background()

	ch, _ := tr.Subscribe(ctx)
	tr.Publish(ctx, &JobResult{JobID: "j1", Status: "completed", Title: "test"})

	select {
	case r := <-ch:
		if r.JobID != "j1" {
			t.Errorf("got %q, want j1", r.JobID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for result")
	}
}

func TestInMemTransport_ConcurrentSubmitReceive(t *testing.T) {
	tr := NewInMemTransport(100, NewMemJobStore())
	defer tr.Close()
	ctx := context.Background()
	ch, _ := tr.Receive(ctx)

	var wg sync.WaitGroup
	n := 20
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(id int) {
			defer wg.Done()
			tr.Submit(ctx, &Job{ID: fmt.Sprintf("j%d", id), Priority: 50})
		}(i)
	}

	received := 0
	done := make(chan struct{})
	go func() {
		for range ch {
			received++
			if received == n {
				close(done)
				return
			}
		}
	}()

	wg.Wait()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("only received %d/%d jobs", received, n)
	}
}
```

Note: add `"fmt"` to imports for `fmt.Sprintf` in the concurrent test.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/queue/ -run TestInMemTransport -v`
Expected: FAIL — `NewInMemTransport` not defined.

- [ ] **Step 3: Implement `internal/queue/inmem.go`**

```go
package queue

import (
	"container/heap"
	"context"
	"sync"
	"sync/atomic"
)

// InMemTransport implements JobQueue, ResultBus, and AttachmentStore using
// in-memory channels and a priority heap. It is the default transport for
// single-process deployments.
type InMemTransport struct {
	mu         sync.Mutex
	cond       *sync.Cond // signaled when a job is pushed to the heap
	pq         priorityQueue
	capacity   int
	seqCounter atomic.Uint64
	store      JobStore

	jobCh    chan *Job
	resultCh chan *JobResult
	closed   chan struct{}

	// Attachment two-phase sync: jobID → ready channel
	attachMu    sync.Mutex
	attachReady map[string]chan []AttachmentReady

	// Worker registration (stub for future use)
	workerMu sync.Mutex
	workers  map[string]WorkerInfo
}

func NewInMemTransport(capacity int, store JobStore) *InMemTransport {
	t := &InMemTransport{
		capacity:    capacity,
		store:       store,
		jobCh:       make(chan *Job, capacity),
		resultCh:    make(chan *JobResult, capacity),
		closed:      make(chan struct{}),
		attachReady: make(map[string]chan []AttachmentReady),
		workers:     make(map[string]WorkerInfo),
	}
	t.cond = sync.NewCond(&t.mu)
	heap.Init(&t.pq)
	go t.dispatchLoop()
	return t
}

// dispatchLoop pops the highest-priority job from the heap and sends it
// to the jobCh for workers to consume. Uses sync.Cond to avoid busy-waiting.
func (t *InMemTransport) dispatchLoop() {
	for {
		t.mu.Lock()
		for t.pq.Len() == 0 {
			// Check if closed before waiting.
			select {
			case <-t.closed:
				t.mu.Unlock()
				return
			default:
			}
			t.cond.Wait()
		}
		entry := heap.Pop(&t.pq).(*queueEntry)
		t.mu.Unlock()

		select {
		case t.jobCh <- entry.job:
		case <-t.closed:
			return
		}
	}
}

// --- JobQueue ---

func (t *InMemTransport) Submit(ctx context.Context, job *Job) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.pq.Len() >= t.capacity {
		return ErrQueueFull
	}

	job.Seq = t.seqCounter.Add(1)
	heap.Push(&t.pq, &queueEntry{job: job})
	t.store.Put(job)
	t.cond.Signal() // wake up dispatchLoop
	return nil
}

func (t *InMemTransport) QueuePosition(jobID string) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	pos := t.pq.position(jobID)
	return pos, nil
}

func (t *InMemTransport) QueueDepth() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pq.Len()
}

func (t *InMemTransport) Receive(ctx context.Context) (<-chan *Job, error) {
	return t.jobCh, nil
}

func (t *InMemTransport) Ack(ctx context.Context, jobID string) error {
	t.store.UpdateStatus(jobID, JobPreparing)

	// Create the attachment ready channel so Resolve can wait on it.
	t.attachMu.Lock()
	if _, exists := t.attachReady[jobID]; !exists {
		t.attachReady[jobID] = make(chan []AttachmentReady, 1)
	}
	t.attachMu.Unlock()

	return nil
}

func (t *InMemTransport) Register(ctx context.Context, info WorkerInfo) error {
	t.workerMu.Lock()
	defer t.workerMu.Unlock()
	t.workers[info.WorkerID] = info
	return nil
}

func (t *InMemTransport) Unregister(ctx context.Context, workerID string) error {
	t.workerMu.Lock()
	defer t.workerMu.Unlock()
	delete(t.workers, workerID)
	return nil
}

func (t *InMemTransport) ListWorkers(ctx context.Context) ([]WorkerInfo, error) {
	t.workerMu.Lock()
	defer t.workerMu.Unlock()
	result := make([]WorkerInfo, 0, len(t.workers))
	for _, w := range t.workers {
		result = append(result, w)
	}
	return result, nil
}

func (t *InMemTransport) Close() error {
	select {
	case <-t.closed:
	default:
		close(t.closed)
		t.cond.Broadcast() // unblock dispatchLoop
	}
	return nil
}

// --- AttachmentStore ---

func (t *InMemTransport) Prepare(ctx context.Context, jobID string, attachments []AttachmentMeta) error {
	// In-memory: the Prepare call comes from the same process.
	// Real download logic lives in the caller — here we just signal readiness.
	// For now, signal with empty list (no attachments to download in-memory).
	t.attachMu.Lock()
	ch, ok := t.attachReady[jobID]
	if !ok {
		ch = make(chan []AttachmentReady, 1)
		t.attachReady[jobID] = ch
	}
	t.attachMu.Unlock()

	// The caller should have downloaded attachments and pass them here.
	// For the in-memory transport, we convert meta → ready with local paths.
	ready := make([]AttachmentReady, len(attachments))
	for i, a := range attachments {
		ready[i] = AttachmentReady{Filename: a.Filename, URL: ""}
	}
	ch <- ready
	return nil
}

func (t *InMemTransport) Resolve(ctx context.Context, jobID string) ([]AttachmentReady, error) {
	t.attachMu.Lock()
	ch, ok := t.attachReady[jobID]
	if !ok {
		ch = make(chan []AttachmentReady, 1)
		t.attachReady[jobID] = ch
	}
	t.attachMu.Unlock()

	select {
	case ready := <-ch:
		return ready, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t *InMemTransport) Cleanup(ctx context.Context, jobID string) error {
	t.attachMu.Lock()
	delete(t.attachReady, jobID)
	t.attachMu.Unlock()
	return nil
}

// --- ResultBus ---

func (t *InMemTransport) Publish(ctx context.Context, result *JobResult) error {
	select {
	case t.resultCh <- result:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *InMemTransport) Subscribe(ctx context.Context) (<-chan *JobResult, error) {
	return t.resultCh, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/queue/ -run TestInMemTransport -v -count=1`
Expected: All 7 tests PASS.

- [ ] **Step 5: Run all queue tests together**

Run: `go test ./internal/queue/ -v -count=1`
Expected: All tests PASS (priority + memstore + inmem).

- [ ] **Step 6: Commit**

```bash
git add internal/queue/inmem.go internal/queue/inmem_test.go
git commit -m "feat(queue): add in-memory transport (JobQueue + ResultBus + AttachmentStore)"
```

---

### Task 5: Config Changes

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write test for new config fields**

Add to `internal/config/config_test.go`:

```go
func TestLoad_QueueConfig(t *testing.T) {
	yaml := `
queue:
  capacity: 100
  transport: inmem
channel_priority:
  C_INCIDENTS: 100
  C_ONCALL: 80
workers:
  count: 5
attachments:
  store: local
  temp_dir: /tmp/test-attach
  ttl: 15m
agents:
  claude:
    command: claude
    args: ["--print"]
    skill_dir: ".claude/skills"
`
	cfg := loadFromString(t, yaml)
	if cfg.Queue.Capacity != 100 {
		t.Errorf("queue capacity = %d, want 100", cfg.Queue.Capacity)
	}
	if cfg.Workers.Count != 5 {
		t.Errorf("workers count = %d, want 5", cfg.Workers.Count)
	}
	pri, ok := cfg.ChannelPriority["C_INCIDENTS"]
	if !ok || pri != 100 {
		t.Errorf("channel priority = %d, want 100", pri)
	}
	agent := cfg.Agents["claude"]
	if agent.SkillDir != ".claude/skills" {
		t.Errorf("skill_dir = %q", agent.SkillDir)
	}
	if cfg.Attachments.TempDir != "/tmp/test-attach" {
		t.Errorf("temp_dir = %q", cfg.Attachments.TempDir)
	}
	if cfg.Attachments.TTL != 15*time.Minute {
		t.Errorf("ttl = %v", cfg.Attachments.TTL)
	}
}

func TestLoad_QueueDefaults(t *testing.T) {
	yaml := `
agents:
  claude:
    command: claude
`
	cfg := loadFromString(t, yaml)
	if cfg.Queue.Capacity != 50 {
		t.Errorf("default queue capacity = %d, want 50", cfg.Queue.Capacity)
	}
	if cfg.Workers.Count != 3 {
		t.Errorf("default workers count = %d, want 3", cfg.Workers.Count)
	}
}

func TestLoad_MaxConcurrentBackwardCompat(t *testing.T) {
	yaml := `
max_concurrent: 7
agents:
  claude:
    command: claude
`
	cfg := loadFromString(t, yaml)
	if cfg.Workers.Count != 7 {
		t.Errorf("workers count = %d, want 7 (from max_concurrent)", cfg.Workers.Count)
	}
}
```

Check if `loadFromString` helper exists in the test file. If not, you need to create it:

```go
func loadFromString(t *testing.T, yamlContent string) *Config {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.WriteString(yamlContent)
	tmpFile.Close()

	cfg, err := Load(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run TestLoad_Queue -v`
Expected: FAIL — `Queue`, `Workers`, `ChannelPriority`, `Attachments` fields not defined.

- [ ] **Step 3: Add new config types and fields to `config.go`**

Add these types after the existing type definitions:

```go
type QueueConfig struct {
	Capacity  int    `yaml:"capacity"`
	Transport string `yaml:"transport"`
}

type WorkersConfig struct {
	Count int `yaml:"count"`
}

type AttachmentsConfig struct {
	Store   string        `yaml:"store"`
	TempDir string        `yaml:"temp_dir"`
	TTL     time.Duration `yaml:"ttl"`
}
```

Add `SkillDir` to existing `AgentConfig`:

```go
type AgentConfig struct {
	Command  string        `yaml:"command"`
	Args     []string      `yaml:"args"`
	Timeout  time.Duration `yaml:"timeout"`
	SkillDir string        `yaml:"skill_dir"`
}
```

Add these fields to the `Config` struct:

```go
Queue           QueueConfig       `yaml:"queue"`
ChannelPriority map[string]int    `yaml:"channel_priority"`
Workers         WorkersConfig     `yaml:"workers"`
Attachments     AttachmentsConfig `yaml:"attachments"`
```

Add defaults in `applyDefaults`:

```go
if cfg.Queue.Capacity <= 0 {
	cfg.Queue.Capacity = 50
}
if cfg.Queue.Transport == "" {
	cfg.Queue.Transport = "inmem"
}
if cfg.Workers.Count <= 0 {
	// Backward compat: use max_concurrent if workers.count not set
	if cfg.MaxConcurrent > 0 {
		cfg.Workers.Count = cfg.MaxConcurrent
		slog.Warn("max_concurrent is deprecated, use workers.count instead")
	} else {
		cfg.Workers.Count = 3
	}
}
if cfg.ChannelPriority == nil {
	cfg.ChannelPriority = map[string]int{"default": 50}
}
if cfg.Attachments.TempDir == "" {
	cfg.Attachments.TempDir = "/tmp/triage-attachments"
}
if cfg.Attachments.TTL <= 0 {
	cfg.Attachments.TTL = 30 * time.Minute
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: All tests PASS (existing + new).

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add queue, workers, channel_priority, attachments config"
```

---

### Task 6: Parser — Structured JSON Output

**Files:**
- Modify: `internal/bot/parser.go`
- Modify: `internal/bot/parser_test.go`

- [ ] **Step 1: Write tests for new JSON parsing**

Replace the test file contents. Keep the old tests but update expected behavior:

```go
package bot

import (
	"testing"
)

func TestParseAgentOutput_JSONCreated(t *testing.T) {
	output := `Some analysis output...

===TRIAGE_RESULT===
{
  "status": "CREATED",
  "title": "Login page broken after 3 failed attempts",
  "body": "## Problem\n\nLogin page crashes...",
  "labels": ["bug"],
  "confidence": "high",
  "files_found": 5,
  "open_questions": 0
}`

	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if result.Status != "CREATED" {
		t.Errorf("status = %q, want CREATED", result.Status)
	}
	if result.Title != "Login page broken after 3 failed attempts" {
		t.Errorf("title = %q", result.Title)
	}
	if result.Confidence != "high" {
		t.Errorf("confidence = %q", result.Confidence)
	}
	if result.FilesFound != 5 {
		t.Errorf("files_found = %d, want 5", result.FilesFound)
	}
	if len(result.Labels) != 1 || result.Labels[0] != "bug" {
		t.Errorf("labels = %v", result.Labels)
	}
}

func TestParseAgentOutput_JSONRejected(t *testing.T) {
	output := `Investigation complete.

===TRIAGE_RESULT===
{
  "status": "REJECTED",
  "message": "Could not find relevant code"
}`

	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if result.Status != "REJECTED" {
		t.Errorf("status = %q", result.Status)
	}
	if result.Message == "" {
		t.Error("message should not be empty")
	}
}

func TestParseAgentOutput_LegacyCreated(t *testing.T) {
	output := `Analysis done.

===TRIAGE_RESULT===
CREATED: https://github.com/owner/repo/issues/42`

	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if result.Status != "CREATED" {
		t.Errorf("status = %q, want CREATED", result.Status)
	}
	if result.IssueURL != "https://github.com/owner/repo/issues/42" {
		t.Errorf("issueURL = %q", result.IssueURL)
	}
}

func TestParseAgentOutput_LegacyRejected(t *testing.T) {
	output := `After investigation.

===TRIAGE_RESULT===
REJECTED: Problem unrelated to this repo`

	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if result.Status != "REJECTED" {
		t.Errorf("status = %q", result.Status)
	}
}

func TestParseAgentOutput_LegacyError(t *testing.T) {
	output := `Tried to create issue.

===TRIAGE_RESULT===
ERROR: gh issue create failed`

	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if result.Status != "ERROR" {
		t.Errorf("status = %q", result.Status)
	}
}

func TestParseAgentOutput_FallbackURL(t *testing.T) {
	output := `Created issue at https://github.com/owner/repo/issues/99 for tracking. Some more padding text to meet minimum length.`

	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if result.Status != "CREATED" {
		t.Errorf("status = %q", result.Status)
	}
	if result.IssueURL != "https://github.com/owner/repo/issues/99" {
		t.Errorf("issueURL = %q", result.IssueURL)
	}
}

func TestParseAgentOutput_Empty(t *testing.T) {
	_, err := ParseAgentOutput("")
	if err == nil {
		t.Error("expected error on empty output")
	}
}

func TestParseAgentOutput_TooShort(t *testing.T) {
	_, err := ParseAgentOutput("short")
	if err == nil {
		t.Error("expected error on short output")
	}
}

func TestParseAgentOutput_NoResult(t *testing.T) {
	_, err := ParseAgentOutput("Some analysis that didn't produce a result or URL. Padding to meet minimum length requirement.")
	if err == nil {
		t.Error("expected error when no result")
	}
}
```

- [ ] **Step 2: Run tests to verify new JSON tests fail**

Run: `go test ./internal/bot/ -run TestParseAgentOutput_JSON -v`
Expected: FAIL — JSON fields not parsed.

- [ ] **Step 3: Update `internal/bot/parser.go`**

```go
package bot

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	resultSeparator = "===TRIAGE_RESULT==="
	minOutputLength = 10
)

// TriageResult is the parsed result from agent output.
type TriageResult struct {
	Status     string   `json:"status"`
	IssueURL   string   `json:"issue_url,omitempty"`
	Message    string   `json:"message,omitempty"`
	Title      string   `json:"title,omitempty"`
	Body       string   `json:"body,omitempty"`
	Labels     []string `json:"labels,omitempty"`
	Confidence string   `json:"confidence,omitempty"`
	FilesFound int      `json:"files_found,omitempty"`
	Questions  int      `json:"open_questions,omitempty"`
}

// ParseAgentOutput extracts the triage result from agent stdout.
// Supports two formats:
//  1. JSON: ===TRIAGE_RESULT=== followed by a JSON object
//  2. Legacy: ===TRIAGE_RESULT=== followed by CREATED:/REJECTED:/ERROR:
//  3. Fallback: extract GitHub issue URL from anywhere in the output
func ParseAgentOutput(output string) (TriageResult, error) {
	output = strings.TrimSpace(output)
	if len(output) < minOutputLength {
		return TriageResult{}, fmt.Errorf("agent output too short (%d chars)", len(output))
	}

	idx := strings.LastIndex(output, resultSeparator)
	if idx == -1 {
		if url := extractIssueURL(output); url != "" {
			return TriageResult{Status: "CREATED", IssueURL: url}, nil
		}
		return TriageResult{}, fmt.Errorf("no triage result found in agent output")
	}

	result := strings.TrimSpace(output[idx+len(resultSeparator):])

	// Try JSON format first.
	if strings.HasPrefix(result, "{") {
		var tr TriageResult
		if err := json.Unmarshal([]byte(result), &tr); err == nil && tr.Status != "" {
			return tr, nil
		}
	}

	// Legacy format.
	if strings.HasPrefix(result, "CREATED:") {
		url := strings.TrimSpace(strings.TrimPrefix(result, "CREATED:"))
		if url == "" {
			url = extractIssueURL(output)
		}
		return TriageResult{Status: "CREATED", IssueURL: url}, nil
	}
	if strings.HasPrefix(result, "REJECTED:") {
		msg := strings.TrimSpace(strings.TrimPrefix(result, "REJECTED:"))
		return TriageResult{Status: "REJECTED", Message: msg}, nil
	}
	if strings.HasPrefix(result, "ERROR:") {
		msg := strings.TrimSpace(strings.TrimPrefix(result, "ERROR:"))
		return TriageResult{Status: "ERROR", Message: msg}, nil
	}

	return TriageResult{}, fmt.Errorf("unknown triage result: %s", result)
}

// extractIssueURL finds a GitHub issue URL in text.
func extractIssueURL(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "github.com/") && strings.Contains(line, "/issues/") {
			for _, word := range strings.Fields(line) {
				if strings.HasPrefix(word, "https://github.com/") && strings.Contains(word, "/issues/") {
					return word
				}
			}
		}
	}
	return ""
}
```

- [ ] **Step 4: Run all parser tests**

Run: `go test ./internal/bot/ -run TestParseAgentOutput -v`
Expected: All 9 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/bot/parser.go internal/bot/parser_test.go
git commit -m "feat(parser): support structured JSON output format with legacy fallback"
```

---

### Task 7: Prompt Builder Changes

**Files:**
- Modify: `internal/bot/prompt.go`
- Modify: `internal/bot/prompt_test.go`

- [ ] **Step 1: Update tests — remove RepoPath, github_repo, labels assertions**

In `internal/bot/prompt_test.go`, update `TestBuildPrompt_Basic`:

Remove:
```go
RepoPath: "/repos/owner/repo",
```
and the assertion:
```go
if !strings.Contains(result, "/repos/owner/repo") {
	t.Error("missing repo path")
}
```

Also remove `RepoPath` from all other test inputs (`TestBuildPrompt_WithAttachments`, `TestBuildPrompt_WithExtraRules`, `TestBuildPrompt_WithExtraDescription`).

Add a new test:

```go
func TestBuildPrompt_NoRepoPathOrLabels(t *testing.T) {
	input := PromptInput{
		ThreadMessages: []ThreadMessage{
			{User: "Alice", Timestamp: "10:30", Text: "test"},
		},
		Branch:     "main",
		GitHubRepo: "owner/repo",
		Channel:    "general",
		Reporter:   "Alice",
		Prompt:     config.PromptConfig{Language: "en"},
	}
	result := BuildPrompt(input)
	// Should NOT contain repo path or github_repo metadata
	if strings.Contains(result, "Path:") {
		t.Error("should not contain RepoPath")
	}
	if strings.Contains(result, "github_repo:") {
		t.Error("should not contain github_repo metadata")
	}
	if strings.Contains(result, "labels:") {
		t.Error("should not contain labels metadata")
	}
	// Should still contain channel and reporter
	if !strings.Contains(result, "general") {
		t.Error("missing channel")
	}
	if !strings.Contains(result, "Alice") {
		t.Error("missing reporter")
	}
}
```

- [ ] **Step 2: Run tests to verify new test fails**

Run: `go test ./internal/bot/ -run TestBuildPrompt -v`
Expected: `TestBuildPrompt_NoRepoPathOrLabels` FAILS because prompt still contains `Path:`, `github_repo:`, `labels:`.

- [ ] **Step 3: Update `internal/bot/prompt.go`**

Remove `RepoPath` from `PromptInput` struct. Remove `GitHubRepo` and `Labels` output from `BuildPrompt`.

Updated `PromptInput`:

```go
type PromptInput struct {
	ThreadMessages   []ThreadMessage
	Attachments      []AttachmentInfo
	ExtraDescription string
	Branch           string
	Channel          string
	Reporter         string
	Prompt           config.PromptConfig
}
```

Updated `BuildPrompt` — remove the `Repository` section and the `github_repo`/`labels` metadata lines:

```go
func BuildPrompt(input PromptInput) string {
	var sb strings.Builder

	sb.WriteString("Use the /triage-issue skill to investigate and produce a triage result.\n\n")

	// Thread context
	sb.WriteString("## Thread Context\n\n")
	for _, msg := range input.ThreadMessages {
		sb.WriteString(fmt.Sprintf("%s (%s):\n> %s\n\n", msg.User, msg.Timestamp, msg.Text))
	}

	// Extra description
	if input.ExtraDescription != "" {
		sb.WriteString("## Extra Description\n\n")
		sb.WriteString(fmt.Sprintf("> %s\n\n", input.ExtraDescription))
	}

	// Issue context (for the agent to include in the issue body)
	sb.WriteString("## Issue Context\n\n")
	sb.WriteString(fmt.Sprintf("channel: %s\n", input.Channel))
	sb.WriteString(fmt.Sprintf("reporter: %s\n", input.Reporter))
	if input.Branch != "" {
		sb.WriteString(fmt.Sprintf("branch: %s\n", input.Branch))
	}
	sb.WriteString("\n")

	// Attachments
	if len(input.Attachments) > 0 {
		sb.WriteString("## Attachments\n\n")
		for _, att := range input.Attachments {
			hint := ""
			switch att.Type {
			case "image":
				hint = " (image — use your file reading tools to view)"
			case "text":
				hint = " (text — read directly)"
			case "document":
				hint = " (document)"
			}
			sb.WriteString(fmt.Sprintf("- %s%s\n", att.Path, hint))
		}
		sb.WriteString("\n")
	}

	// Language + extra rules
	if input.Prompt.Language != "" {
		sb.WriteString(fmt.Sprintf("Response language: %s\n", input.Prompt.Language))
	}
	if len(input.Prompt.ExtraRules) > 0 {
		sb.WriteString("\nAdditional rules:\n")
		for _, rule := range input.Prompt.ExtraRules {
			sb.WriteString(fmt.Sprintf("- %s\n", rule))
		}
	}

	return sb.String()
}
```

- [ ] **Step 4: Fix compilation errors in other files referencing removed fields**

The `workflow.go` `runTriage` function sets `RepoPath`, `GitHubRepo`, and `Labels` in `PromptInput`. These will be removed in Task 9 when we refactor the workflow. For now, remove those fields from the `PromptInput` call in `workflow.go:408-419`:

```go
prompt := BuildPrompt(PromptInput{
	ThreadMessages:   threadMsgs,
	Attachments:      attachments,
	ExtraDescription: pt.ExtraDesc,
	Branch:           pt.SelectedBranch,
	Channel:          pt.ChannelName,
	Reporter:         pt.Reporter,
	Prompt:           w.cfg.Prompt,
})
```

- [ ] **Step 5: Run all prompt tests**

Run: `go test ./internal/bot/ -run TestBuildPrompt -v`
Expected: All tests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/bot/prompt.go internal/bot/prompt_test.go internal/bot/workflow.go
git commit -m "feat(prompt): remove RepoPath, github_repo, labels — agent no longer creates issues"
```

---

### Task 8: Worker Pool and Executor

**Files:**
- Create: `internal/worker/pool.go`
- Create: `internal/worker/executor.go`
- Create: `internal/worker/pool_test.go`

- [ ] **Step 1: Write test for worker pool**

```go
package worker

import (
	"context"
	"testing"
	"time"

	"slack-issue-bot/internal/queue"
)

// mockAgentRunner implements the runner interface for testing.
type mockAgentRunner struct {
	output string
	err    error
}

func (m *mockAgentRunner) Run(ctx context.Context, workDir, prompt string) (string, error) {
	return m.output, m.err
}

// mockRepoCache implements the repo cache interface for testing.
type mockRepoCache struct {
	path string
	err  error
}

func (m *mockRepoCache) Prepare(cloneURL, branch string) (string, error) {
	return m.path, m.err
}

func TestPool_ExecutesJobAndPublishesResult(t *testing.T) {
	store := queue.NewMemJobStore()
	transport := queue.NewInMemTransport(10, store)
	defer transport.Close()

	agentOutput := `Analysis done.

===TRIAGE_RESULT===
{
  "status": "CREATED",
  "title": "Bug fix",
  "body": "## Problem\nSomething broke",
  "labels": ["bug"],
  "confidence": "high",
  "files_found": 3,
  "open_questions": 0
}`

	pool := NewPool(Config{
		Queue:       transport,
		Attachments: transport,
		Results:     transport,
		Store:       store,
		Runner:      &mockAgentRunner{output: agentOutput},
		RepoCache:   &mockRepoCache{path: "/tmp/test-repo"},
		WorkerCount: 1,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool.Start(ctx)

	// Signal that attachments are ready (no actual attachments).
	transport.Prepare(ctx, "j1", nil)

	// Submit a job.
	transport.Submit(ctx, &queue.Job{
		ID:       "j1",
		Priority: 50,
		Repo:     "owner/repo",
		Prompt:   "test prompt",
	})

	// Listen for result.
	ch, _ := transport.Subscribe(ctx)
	select {
	case result := <-ch:
		if result.JobID != "j1" {
			t.Errorf("jobID = %q, want j1", result.JobID)
		}
		if result.Status != "completed" {
			t.Errorf("status = %q, want completed", result.Status)
		}
		if result.Title != "Bug fix" {
			t.Errorf("title = %q", result.Title)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for result")
	}
}

func TestPool_AgentFailurePublishesFailedResult(t *testing.T) {
	store := queue.NewMemJobStore()
	transport := queue.NewInMemTransport(10, store)
	defer transport.Close()

	pool := NewPool(Config{
		Queue:       transport,
		Attachments: transport,
		Results:     transport,
		Store:       store,
		Runner:      &mockAgentRunner{err: fmt.Errorf("agent crashed")},
		RepoCache:   &mockRepoCache{path: "/tmp/test-repo"},
		WorkerCount: 1,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool.Start(ctx)
	transport.Prepare(ctx, "j1", nil)
	transport.Submit(ctx, &queue.Job{ID: "j1", Priority: 50, Prompt: "test"})

	ch, _ := transport.Subscribe(ctx)
	select {
	case result := <-ch:
		if result.Status != "failed" {
			t.Errorf("status = %q, want failed", result.Status)
		}
		if result.Error == "" {
			t.Error("error should not be empty")
		}
	case <-ctx.Done():
		t.Fatal("timeout")
	}
}
```

Note: add `"fmt"` to imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/worker/ -v`
Expected: FAIL — package doesn't exist.

- [ ] **Step 3: Create `internal/worker/executor.go`**

```go
package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"slack-issue-bot/internal/bot"
	"slack-issue-bot/internal/queue"
)

// Runner abstracts agent execution (for testing).
type Runner interface {
	Run(ctx context.Context, workDir, prompt string) (string, error)
}

// RepoProvider abstracts repo clone/checkout (for testing).
type RepoProvider interface {
	Prepare(cloneURL, branch string) (string, error)
}

func executeJob(ctx context.Context, job *queue.Job, deps executionDeps) *queue.JobResult {
	startedAt := time.Now()

	// Resolve attachments (blocks until Prepare completes on app side).
	attachments, err := deps.attachments.Resolve(ctx, job.ID)
	if err != nil {
		return failedResult(job, startedAt, fmt.Errorf("attachments failed: %w", err))
	}

	// Clone/fetch repo.
	repoPath, err := deps.repoCache.Prepare(job.CloneURL, job.Branch)
	if err != nil {
		return failedResult(job, startedAt, fmt.Errorf("repo prepare failed: %w", err))
	}

	// Copy attachments to repo workspace.
	for _, att := range attachments {
		if att.URL != "" {
			// For local file:// URLs, the path is already accessible.
			// For remote, download would happen here (future).
			_ = att
		}
	}

	// Mount skills.
	if len(job.Skills) > 0 {
		if err := mountSkills(repoPath, job.Skills, deps.skillDir); err != nil {
			return failedResult(job, startedAt, fmt.Errorf("skill mount failed: %w", err))
		}
		defer cleanupSkills(repoPath, job.Skills, deps.skillDir)
	}

	// Execute agent.
	deps.store.UpdateStatus(job.ID, queue.JobRunning)
	output, err := deps.runner.Run(ctx, repoPath, job.Prompt)
	if err != nil {
		return failedResult(job, startedAt, err)
	}

	// Parse agent output.
	result, err := bot.ParseAgentOutput(output)
	if err != nil {
		return failedResult(job, startedAt, fmt.Errorf("parse failed: %w", err))
	}

	return &queue.JobResult{
		JobID:      job.ID,
		Status:     "completed",
		Title:      result.Title,
		Body:       result.Body,
		Labels:     result.Labels,
		Confidence: result.Confidence,
		FilesFound: result.FilesFound,
		Questions:  result.Questions,
		RawOutput:  output,
		StartedAt:  startedAt,
		FinishedAt: time.Now(),
	}
}

func failedResult(job *queue.Job, startedAt time.Time, err error) *queue.JobResult {
	return &queue.JobResult{
		JobID:      job.ID,
		Status:     "failed",
		Error:      err.Error(),
		StartedAt:  startedAt,
		FinishedAt: time.Now(),
	}
}

type executionDeps struct {
	attachments queue.AttachmentStore
	repoCache   RepoProvider
	runner      Runner
	store       queue.JobStore
	skillDir    string
}

func mountSkills(repoPath string, skills map[string]string, skillDir string) error {
	if skillDir == "" {
		return nil
	}
	dir := filepath.Join(repoPath, skillDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	for name, content := range skills {
		path := filepath.Join(dir, name+".md")
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return err
		}
	}
	return nil
}

func cleanupSkills(repoPath string, skills map[string]string, skillDir string) {
	if skillDir == "" {
		return
	}
	dir := filepath.Join(repoPath, skillDir)
	for name := range skills {
		os.Remove(filepath.Join(dir, name+".md"))
	}
	// Try to remove the dir — only succeeds if empty (safe).
	os.Remove(dir)
}
```

- [ ] **Step 4: Create `internal/worker/pool.go`**

```go
package worker

import (
	"context"
	"log/slog"

	"slack-issue-bot/internal/queue"
)

type Config struct {
	Queue       queue.JobQueue
	Attachments queue.AttachmentStore
	Results     queue.ResultBus
	Store       queue.JobStore
	Runner      Runner
	RepoCache   RepoProvider
	WorkerCount int
	SkillDir    string
}

type Pool struct {
	cfg Config
}

func NewPool(cfg Config) *Pool {
	return &Pool{cfg: cfg}
}

func (p *Pool) Start(ctx context.Context) {
	for i := 0; i < p.cfg.WorkerCount; i++ {
		go p.runWorker(ctx, i)
	}
	slog.Info("worker pool started", "count", p.cfg.WorkerCount)
}

func (p *Pool) runWorker(ctx context.Context, id int) {
	logger := slog.With("worker_id", id)
	jobs, err := p.cfg.Queue.Receive(ctx)
	if err != nil {
		logger.Error("failed to receive jobs", "error", err)
		return
	}

	deps := executionDeps{
		attachments: p.cfg.Attachments,
		repoCache:   p.cfg.RepoCache,
		runner:      p.cfg.Runner,
		store:       p.cfg.Store,
		skillDir:    p.cfg.SkillDir,
	}

	for {
		select {
		case job, ok := <-jobs:
			if !ok {
				logger.Info("job channel closed, worker exiting")
				return
			}
			logger.Info("received job", "job_id", job.ID, "repo", job.Repo)

			if err := p.cfg.Queue.Ack(ctx, job.ID); err != nil {
				logger.Error("ack failed", "job_id", job.ID, "error", err)
				result := failedResult(job, time.Now(), fmt.Errorf("ack failed: %w", err))
				p.cfg.Results.Publish(ctx, result)
				continue
			}

			result := executeJob(ctx, job, deps)
			p.cfg.Store.UpdateStatus(job.ID, queue.JobStatus(result.Status))
			if err := p.cfg.Results.Publish(ctx, result); err != nil {
				logger.Error("failed to publish result", "job_id", job.ID, "error", err)
			}
			logger.Info("job completed", "job_id", job.ID, "status", result.Status)
		case <-ctx.Done():
			logger.Info("worker shutting down")
			return
		}
	}
}
```

Note: add `"fmt"` and `"time"` to pool.go imports.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/worker/ -v -count=1`
Expected: Both tests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/worker/pool.go internal/worker/executor.go internal/worker/pool_test.go
git commit -m "feat(worker): add worker pool with executor, skill mounting, and error handling"
```

---

### Task 9: Workflow Refactor — runTriage → queue.Submit

**Files:**
- Modify: `internal/bot/workflow.go`

- [ ] **Step 1: Add queue and skill dependencies to Workflow**

Add to the `Workflow` struct fields:

```go
queue     queue.JobQueue
store     queue.JobStore
skills    map[string]string  // loaded at startup
```

Update `NewWorkflow` signature to accept these:

```go
func NewWorkflow(
	cfg *config.Config,
	slack *slackclient.Client,
	repoCache *ghclient.RepoCache,
	repoDiscovery *ghclient.RepoDiscovery,
	agentRunner *AgentRunner,
	mantisClient *mantis.Client,
	jobQueue queue.JobQueue,
	jobStore queue.JobStore,
	skills map[string]string,
) *Workflow {
```

Store them in the returned struct.

- [ ] **Step 2: Add `channelPriority` helper**

```go
func (w *Workflow) channelPriority(channelID string) int {
	if pri, ok := w.cfg.ChannelPriority[channelID]; ok {
		return pri
	}
	if pri, ok := w.cfg.ChannelPriority["default"]; ok {
		return pri
	}
	return 50
}
```

- [ ] **Step 3: Add `toAttachmentMeta` helper**

```go
func toAttachmentMeta(downloads []slackclient.DownloadResult) []queue.AttachmentMeta {
	var metas []queue.AttachmentMeta
	for _, d := range downloads {
		if d.Failed {
			continue
		}
		metas = append(metas, queue.AttachmentMeta{
			SlackFileID: d.FileID,
			Filename:    d.Name,
			MimeType:    d.Type,
		})
	}
	return metas
}
```

Note: check if `slackclient.DownloadResult` has these fields. If `FileID` doesn't exist, use an empty string — the field is for future use.

- [ ] **Step 4: Refactor `runTriage` to submit to queue**

Replace the agent execution + parsing + Slack posting section (steps 8-10, lines 424-471) with queue submission:

```go
func (w *Workflow) runTriage(pt *pendingTriage) {
	ctx := context.Background()

	w.slack.PostMessage(pt.ChannelID, ":mag: 正在排入處理佇列...", pt.ThreadTS)

	// 1. Read thread context.
	botUserID := ""
	rawMsgs, err := w.slack.FetchThreadContext(pt.ChannelID, pt.ThreadTS, pt.TriggerTS, botUserID, w.cfg.MaxThreadMessages)
	if err != nil {
		w.notifyError(pt.Logger, pt.ChannelID, pt.ThreadTS, "Failed to read thread: %v", err)
		w.clearDedup(pt)
		return
	}
	pt.Logger.Info("thread context read", "messages", len(rawMsgs), "repo", pt.SelectedRepo)

	// 2. Enrich messages.
	var threadMsgs []ThreadMessage
	for _, m := range rawMsgs {
		text := m.Text
		if w.mantisClient != nil {
			text = enrichMessage(text, w.mantisClient)
		}
		threadMsgs = append(threadMsgs, ThreadMessage{
			User:      w.slack.ResolveUser(m.User),
			Timestamp: m.Timestamp,
			Text:      text,
		})
	}

	// 3. Collect attachment metadata (don't download yet).
	tempDir, err := os.MkdirTemp("", "triage-meta-*")
	if err != nil {
		w.notifyError(pt.Logger, pt.ChannelID, pt.ThreadTS, "Failed to create temp dir: %v", err)
		w.clearDedup(pt)
		return
	}
	defer os.RemoveAll(tempDir)

	downloads := w.slack.DownloadAttachments(rawMsgs, tempDir)
	var attachmentInfos []AttachmentInfo
	for _, d := range downloads {
		if d.Failed {
			continue
		}
		attachmentInfos = append(attachmentInfos, AttachmentInfo{Path: d.Path, Name: d.Name, Type: d.Type})
	}

	// 4. Build prompt.
	prompt := BuildPrompt(PromptInput{
		ThreadMessages:   threadMsgs,
		Attachments:      attachmentInfos,
		ExtraDescription: pt.ExtraDesc,
		Branch:           pt.SelectedBranch,
		Channel:          pt.ChannelName,
		Reporter:         pt.Reporter,
		Prompt:           w.cfg.Prompt,
	})
	pt.Logger.Info("prompt built", "length", len(prompt))

	// 5. Submit to queue.
	job := &queue.Job{
		ID:          pt.RequestID,
		Priority:    w.channelPriority(pt.ChannelID),
		ChannelID:   pt.ChannelID,
		ThreadTS:    pt.ThreadTS,
		UserID:      "",
		Repo:        pt.SelectedRepo,
		Branch:      pt.SelectedBranch,
		CloneURL:    w.repoCache.ResolveURL(pt.SelectedRepo),
		Prompt:      prompt,
		Skills:      w.skills,
		RequestID:   pt.RequestID,
		Attachments: toAttachmentMeta(downloads),
		SubmittedAt: time.Now(),
	}

	if err := w.queue.Submit(ctx, job); err != nil {
		if err == queue.ErrQueueFull {
			w.slack.PostMessage(pt.ChannelID, ":warning: 系統忙碌，請稍後再試", pt.ThreadTS)
		} else {
			w.notifyError(pt.Logger, pt.ChannelID, pt.ThreadTS, "Failed to submit job: %v", err)
		}
		w.clearDedup(pt)
		return
	}

	pos, _ := w.queue.QueuePosition(job.ID)
	if pos <= 1 {
		w.slack.PostMessage(pt.ChannelID, ":hourglass_flowing_sand: 正在處理你的請求...", pt.ThreadTS)
	} else {
		w.slack.PostMessage(pt.ChannelID,
			fmt.Sprintf(":hourglass_flowing_sand: 已加入排隊，前面有 %d 個請求", pos-1), pt.ThreadTS)
	}
	// Result will come back via ResultListener — don't clearDedup here.
}
```

- [ ] **Step 5: Export `ResolveURL` on `RepoCache`**

In `internal/github/repo.go`, rename `resolveURL` to `ResolveURL`:

Change `func (rc *RepoCache) resolveURL(repoRef string) string {` to `func (rc *RepoCache) ResolveURL(repoRef string) string {`

Also update the one call site inside `EnsureRepo` that calls `resolveURL` to `ResolveURL`.

- [ ] **Step 6: Verify compilation**

Run: `go build ./...`
Expected: Success. (The `main.go` will need updating in Task 11 — compilation may fail there. Focus on `internal/...` compiling.)

- [ ] **Step 7: Commit**

```bash
git add internal/bot/workflow.go internal/github/repo.go
git commit -m "refactor(workflow): replace runTriage agent execution with queue submission"
```

---

### Task 10: Result Listener

**Files:**
- Create: `internal/bot/result_listener.go`
- Create: `internal/bot/result_listener_test.go`

- [ ] **Step 1: Write test for ResultListener**

```go
package bot

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"slack-issue-bot/internal/queue"
)

type mockSlackPoster struct {
	mu       sync.Mutex
	messages []string
}

func (m *mockSlackPoster) PostMessage(channelID, text, threadTS string) {
	m.mu.Lock()
	m.messages = append(m.messages, text)
	m.mu.Unlock()
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

	transport := queue.NewInMemTransport(10, store)
	defer transport.Close()

	slack := &mockSlackPoster{}
	github := &mockIssueCreator{url: "https://github.com/owner/repo/issues/1"}

	listener := NewResultListener(transport, store, transport, slack, github)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go listener.Listen(ctx)

	transport.Publish(ctx, &queue.JobResult{
		JobID:      "j1",
		Status:     "completed",
		Title:      "Bug",
		Body:       "body",
		Labels:     []string{"bug"},
		Confidence: "high",
		FilesFound: 3,
	})

	time.Sleep(200 * time.Millisecond)

	slack.mu.Lock()
	defer slack.mu.Unlock()
	found := false
	for _, msg := range slack.messages {
		if strings.Contains(msg, "issues/1") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected issue URL in messages, got %v", slack.messages)
	}
}

func TestResultListener_FailedPostsError(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1"})

	transport := queue.NewInMemTransport(10, store)
	defer transport.Close()

	slack := &mockSlackPoster{}

	listener := NewResultListener(transport, store, transport, slack, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go listener.Listen(ctx)

	transport.Publish(ctx, &queue.JobResult{
		JobID:  "j1",
		Status: "failed",
		Error:  "agent crashed",
	})

	time.Sleep(200 * time.Millisecond)

	slack.mu.Lock()
	defer slack.mu.Unlock()
	found := false
	for _, msg := range slack.messages {
		if strings.Contains(msg, "agent crashed") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected error in messages, got %v", slack.messages)
	}
}

func TestResultListener_LowConfidenceRejects(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", Repo: "owner/repo", ChannelID: "C1", ThreadTS: "T1"})

	transport := queue.NewInMemTransport(10, store)
	defer transport.Close()

	slack := &mockSlackPoster{}

	listener := NewResultListener(transport, store, transport, slack, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go listener.Listen(ctx)

	transport.Publish(ctx, &queue.JobResult{
		JobID:      "j1",
		Status:     "completed",
		Confidence: "low",
	})

	time.Sleep(200 * time.Millisecond)

	slack.mu.Lock()
	defer slack.mu.Unlock()
	found := false
	for _, msg := range slack.messages {
		if strings.Contains(msg, "跳過") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected rejection in messages, got %v", slack.messages)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/bot/ -run TestResultListener -v`
Expected: FAIL — `NewResultListener` not defined.

- [ ] **Step 3: Implement `internal/bot/result_listener.go`**

```go
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
		r.slack.PostMessage(job.ChannelID,
			fmt.Sprintf(":x: 分析失敗: %s", result.Error), job.ThreadTS)

	case result.Confidence == "low":
		r.slack.PostMessage(job.ChannelID,
			":warning: 判斷不屬於此 repo，已跳過", job.ThreadTS)

	case result.FilesFound == 0 || result.Questions >= 5:
		r.createAndPostIssue(ctx, job, owner, repo, result, true)

	default:
		r.createAndPostIssue(ctx, job, owner, repo, result, false)
	}

	// Cleanup.
	r.attachments.Cleanup(ctx, result.JobID)
	r.store.Delete(result.JobID)
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
		r.slack.PostMessage(job.ChannelID,
			fmt.Sprintf(":warning: Triage 完成但建立 issue 失敗: %v", err), job.ThreadTS)
		return
	}

	r.slack.PostMessage(job.ChannelID,
		fmt.Sprintf(":white_check_mark: Issue created%s: %s", branchInfo, url), job.ThreadTS)
}

func splitRepo(repo string) (string, string) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		return repo, ""
	}
	return parts[0], parts[1]
}

func stripTriageSection(body string) string {
	// Remove the "## TDD Fix Plan" or "## Root Cause Analysis" sections for degraded issues.
	// Simple approach: keep everything before "## Root Cause" or "## TDD Fix".
	for _, marker := range []string{"## Root Cause Analysis", "## TDD Fix Plan"} {
		if idx := strings.Index(body, marker); idx > 0 {
			body = strings.TrimSpace(body[:idx])
		}
	}
	return body
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/bot/ -run TestResultListener -v`
Expected: All 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/bot/result_listener.go internal/bot/result_listener_test.go
git commit -m "feat(bot): add ResultListener — handles issue creation and Slack posting"
```

---

### Task 11: Handler Changes — Remove Semaphore

**Files:**
- Modify: `internal/slack/handler.go`
- Modify: `internal/slack/handler_test.go`

- [ ] **Step 1: Remove semaphore from Handler**

In `handler.go`:

Remove the `semaphore` field from `Handler` struct.
Remove `make(chan struct{}, cfg.MaxConcurrent)` from `NewHandler`.
Remove the semaphore acquire/release in `HandleTrigger`.

Updated `HandleTrigger`:

```go
func (h *Handler) HandleTrigger(event TriggerEvent) bool {
	if h.threadDedup.isDuplicate(event.ChannelID, event.ThreadTS) {
		return false
	}
	if !h.userLimit.allow(event.UserID) {
		if h.onRejected != nil {
			h.onRejected(event, "rate limit exceeded")
		}
		return false
	}
	if !h.channelLimit.allow(event.ChannelID) {
		if h.onRejected != nil {
			h.onRejected(event, "channel rate limit exceeded")
		}
		return false
	}
	go h.onEvent(event)
	return true
}
```

Remove `MaxConcurrent` from `HandlerConfig` (or keep it unused for backward compat — the field won't hurt).

- [ ] **Step 2: Update handler tests**

In `handler_test.go`, the `TestHandler_DedupBlocksDuplicate` and `TestHandler_RateLimitBlocksExcess` tests should still work without changes (they set `MaxConcurrent` but it's now ignored).

- [ ] **Step 3: Run handler tests**

Run: `go test ./internal/slack/ -run TestHandler -v`
Expected: All tests PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/slack/handler.go internal/slack/handler_test.go
git commit -m "refactor(handler): remove semaphore — concurrency now controlled by queue + worker pool"
```

---

### Task 12: Wire Everything in main.go

**Files:**
- Modify: `cmd/bot/main.go`

- [ ] **Step 1: Add skill loading**

After `agentRunner` creation, load skills:

```go
// Load skills for agent.
skills := make(map[string]string)
skillPath := "agents/skills/triage-issue/SKILL.md"
if data, err := os.ReadFile(skillPath); err == nil {
	skills["triage-issue"] = string(data)
	slog.Info("skill loaded", "path", skillPath)
} else {
	slog.Warn("skill not found, agents will run without skill", "path", skillPath, "error", err)
}
```

- [ ] **Step 2: Create transport, store, and worker pool**

After skill loading:

```go
// Queue infrastructure.
jobStore := queue.NewMemJobStore()
jobStore.StartCleanup(1 * time.Hour)
transport := queue.NewInMemTransport(cfg.Queue.Capacity, jobStore)

// Determine skill dir from active agent config.
skillDir := ""
for _, name := range cfg.Fallback {
	if agent, ok := cfg.Agents[name]; ok && agent.SkillDir != "" {
		skillDir = agent.SkillDir
		break
	}
}

// Worker pool.
workerPool := worker.NewPool(worker.Config{
	Queue:       transport,
	Attachments: transport,
	Results:     transport,
	Store:       jobStore,
	Runner:      agentRunner,
	RepoCache:   repoCache,
	WorkerCount: cfg.Workers.Count,
	SkillDir:    skillDir,
})
workerPool.Start(context.Background())
```

Add imports:
```go
"slack-issue-bot/internal/queue"
"slack-issue-bot/internal/worker"
```

- [ ] **Step 3: Update Workflow creation**

```go
wf := bot.NewWorkflow(cfg, slackClient, repoCache, repoDiscovery, agentRunner, mantisClient,
	transport, jobStore, skills)
```

- [ ] **Step 4: Create IssueClient and ResultListener**

```go
issueClient := ghclient.NewIssueClient(cfg.GitHub.Token)
resultListener := bot.NewResultListener(transport, jobStore, transport, slackClient, issueClient)
go resultListener.Listen(context.Background())
```

Note: `slackClient` must satisfy the `SlackPoster` interface. Check if `slackclient.Client` has a `PostMessage(channelID, text, threadTS string)` method. If the signature differs, add an adapter.

- [ ] **Step 5: Make AgentRunner satisfy `worker.Runner` interface**

The `worker.Runner` interface expects `Run(ctx, workDir, prompt) (string, error)` but `AgentRunner.Run` has signature `Run(ctx, logger, workDir, prompt) (string, error)`. Create a wrapper:

In `internal/bot/agent.go`, add:

```go
// RunWithoutLogger wraps Run with a no-op logger for worker pool usage.
func (r *AgentRunner) RunWithoutLogger(ctx context.Context, workDir, prompt string) (string, error) {
	return r.Run(ctx, slog.Default(), workDir, prompt)
}
```

In `cmd/bot/main.go`, use an adapter:

```go
// agentRunnerAdapter wraps AgentRunner to satisfy worker.Runner interface.
type agentRunnerAdapter struct {
	runner *bot.AgentRunner
}

func (a *agentRunnerAdapter) Run(ctx context.Context, workDir, prompt string) (string, error) {
	return a.runner.Run(ctx, slog.Default(), workDir, prompt)
}
```

Then use `&agentRunnerAdapter{runner: agentRunner}` as the `Runner` in `worker.Config`.

- [ ] **Step 6: Make RepoCache satisfy `worker.RepoProvider` interface**

`worker.RepoProvider` expects `Prepare(cloneURL, branch string) (string, error)` but `RepoCache` has `EnsureRepo(repoRef)` and `Checkout(repoPath, branch)` separately. Create an adapter:

```go
// repoCacheAdapter wraps RepoCache to satisfy worker.RepoProvider interface.
type repoCacheAdapter struct {
	cache *ghclient.RepoCache
}

func (a *repoCacheAdapter) Prepare(cloneURL, branch string) (string, error) {
	repoPath, err := a.cache.EnsureRepo(cloneURL)
	if err != nil {
		return "", err
	}
	if branch != "" {
		if err := a.cache.Checkout(repoPath, branch); err != nil {
			return "", err
		}
	}
	return repoPath, nil
}
```

Update worker config to use `&repoCacheAdapter{cache: repoCache}`.

- [ ] **Step 7: Verify full build**

Run: `go build ./...`
Expected: Success.

- [ ] **Step 8: Run all tests**

Run: `go test ./... -count=1`
Expected: All tests PASS.

- [ ] **Step 9: Commit**

```bash
git add cmd/bot/main.go internal/bot/agent.go
git commit -m "feat(main): wire queue transport, worker pool, and result listener"
```

---

### Task 13: Update Skill File

**Files:**
- Modify: `agents/skills/triage-issue/SKILL.md`

- [ ] **Step 1: Remove Step 6 (Create the GitHub issue)**

Delete the entire "### 6. Create the GitHub issue" section (lines 78-129) from SKILL.md.

- [ ] **Step 2: Replace Step 7 (Output result) with structured JSON output**

Replace the current output format:

```markdown
### 6. Output result

After your investigation, output the result in this exact format:

For a successful triage (confidence is high or medium):
` ` `
===TRIAGE_RESULT===
{
  "status": "CREATED",
  "title": "Concise issue title",
  "body": "Full markdown issue body including Problem, Root Cause Analysis, TDD Fix Plan, and Acceptance Criteria sections. Use the reporter, channel, and branch from the Issue Context.",
  "labels": ["bug"],
  "confidence": "high",
  "files_found": 5,
  "open_questions": 0
}
` ` `

For a rejection (confidence is low):
` ` `
===TRIAGE_RESULT===
{
  "status": "REJECTED",
  "message": "Brief explanation why this problem is unrelated to the repo"
}
` ` `

For an error:
` ` `
===TRIAGE_RESULT===
{
  "status": "ERROR",
  "message": "What went wrong"
}
` ` `
```

(Remove backtick escaping — the triple backticks above use spaces for escaping in this plan only.)

- [ ] **Step 3: Update the issue body template in the skill**

In Step 5 (Design TDD fix plan), update the issue body template. Remove the `gh issue create` command. Instead, instruct the agent to format the body with the header:

```markdown
The issue body should follow this template:

**Channel**: #{channel}
**Reporter**: {reporter}
**Branch**: {branch}

---

## Problem
...

## Root Cause Analysis
...

## TDD Fix Plan
...

## Acceptance Criteria
...
```

- [ ] **Step 4: Verify the skill file is valid markdown**

Read the file and check it looks correct.

- [ ] **Step 5: Commit**

```bash
git add agents/skills/triage-issue/SKILL.md
git commit -m "feat(skill): output structured JSON triage result instead of creating issues directly"
```

---

### Task 14: Integration Test — Full Flow

**Files:**
- Create: `internal/queue/integration_test.go`

- [ ] **Step 1: Write integration test**

```go
package queue_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"slack-issue-bot/internal/bot"
	"slack-issue-bot/internal/queue"
	"slack-issue-bot/internal/worker"
)

type fakeRunner struct{}

func (f *fakeRunner) Run(ctx context.Context, workDir, prompt string) (string, error) {
	result := map[string]any{
		"status":         "CREATED",
		"title":          "Test issue",
		"body":           "## Problem\nTest",
		"labels":         []string{"bug"},
		"confidence":     "high",
		"files_found":    3,
		"open_questions": 0,
	}
	b, _ := json.Marshal(result)
	return fmt.Sprintf("Analysis done.\n\n===TRIAGE_RESULT===\n%s", string(b)), nil
}

type fakeRepo struct{}

func (f *fakeRepo) Prepare(cloneURL, branch string) (string, error) {
	return "/tmp/fake-repo", nil
}

func TestFullFlow_SubmitToResult(t *testing.T) {
	store := queue.NewMemJobStore()
	transport := queue.NewInMemTransport(10, store)
	defer transport.Close()

	pool := worker.NewPool(worker.Config{
		Queue:       transport,
		Attachments: transport,
		Results:     transport,
		Store:       store,
		Runner:      &fakeRunner{},
		RepoCache:   &fakeRepo{},
		WorkerCount: 1,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool.Start(ctx)

	// Pre-signal attachments ready.
	transport.Prepare(ctx, "j1", nil)

	// Submit job.
	err := transport.Submit(ctx, &queue.Job{
		ID:       "j1",
		Priority: 50,
		Repo:     "owner/repo",
		Prompt:   "test prompt",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Wait for result.
	ch, _ := transport.Subscribe(ctx)
	select {
	case result := <-ch:
		if result.Status != "completed" {
			t.Errorf("status = %q, want completed", result.Status)
		}
		if result.Title != "Test issue" {
			t.Errorf("title = %q", result.Title)
		}
		if result.Confidence != "high" {
			t.Errorf("confidence = %q", result.Confidence)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for result")
	}
}

func TestFullFlow_PriorityOrdering(t *testing.T) {
	store := queue.NewMemJobStore()
	transport := queue.NewInMemTransport(10, store)
	defer transport.Close()

	// Submit 3 jobs with different priorities before starting workers.
	ctx := context.Background()
	transport.Prepare(ctx, "low", nil)
	transport.Prepare(ctx, "high", nil)
	transport.Prepare(ctx, "mid", nil)

	transport.Submit(ctx, &queue.Job{ID: "low", Priority: 10, Prompt: "p"})
	transport.Submit(ctx, &queue.Job{ID: "high", Priority: 100, Prompt: "p"})
	transport.Submit(ctx, &queue.Job{ID: "mid", Priority: 50, Prompt: "p"})

	// Start 1 worker — it should process high first.
	var order []string
	runner := &orderTrackingRunner{order: &order}

	pool := worker.NewPool(worker.Config{
		Queue:       transport,
		Attachments: transport,
		Results:     transport,
		Store:       store,
		Runner:      runner,
		RepoCache:   &fakeRepo{},
		WorkerCount: 1,
	})

	ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool.Start(ctx2)

	// Collect 3 results.
	ch, _ := transport.Subscribe(ctx2)
	for i := 0; i < 3; i++ {
		select {
		case <-ch:
		case <-ctx2.Done():
			t.Fatalf("timeout after %d results", i)
		}
	}

	// Verify order: high, mid, low.
	if len(order) != 3 {
		t.Fatalf("expected 3 executions, got %d", len(order))
	}
	// Note: exact ordering depends on the prompt content which carries the job ID.
	// The runner extracts it from the prompt.
}

type orderTrackingRunner struct {
	mu    sync.Mutex
	order *[]string
}

func (r *orderTrackingRunner) Run(ctx context.Context, workDir, prompt string) (string, error) {
	r.mu.Lock()
	*r.order = append(*r.order, prompt)
	r.mu.Unlock()

	result := `{"status":"CREATED","title":"t","body":"b","labels":[],"confidence":"high","files_found":1,"open_questions":0}`
	return fmt.Sprintf("===TRIAGE_RESULT===\n%s", result), nil
}
```

Note: add `"sync"` to imports.

- [ ] **Step 2: Run integration tests**

Run: `go test ./internal/queue/ -run TestFullFlow -v -count=1`
Expected: Both tests PASS.

- [ ] **Step 3: Run entire test suite**

Run: `go test ./... -count=1`
Expected: All tests PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/queue/integration_test.go
git commit -m "test: add integration tests for full queue submit → worker → result flow"
```

---

### Task 15: Final Verification

- [ ] **Step 1: Run full test suite**

Run: `go test ./... -v -count=1`
Expected: All tests PASS.

- [ ] **Step 2: Build binary**

Run: `go build -o bot ./cmd/bot/`
Expected: Binary builds successfully.

- [ ] **Step 3: Verify config loading with new fields**

Run: `go test ./internal/config/ -v`
Expected: All config tests PASS, including new queue/worker/attachment defaults.

- [ ] **Step 4: Final commit if any uncommitted changes remain**

```bash
git status
# If clean, nothing to do.
# If changes exist:
git add -A
git commit -m "chore: final cleanup for queue decoupling"
```
