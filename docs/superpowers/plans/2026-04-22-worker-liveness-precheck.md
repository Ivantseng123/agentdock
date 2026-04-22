# Worker Liveness Precheck Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/superpowers/specs/2026-04-22-worker-liveness-precheck-design.md`

**Goal:** Wire a two-stage worker availability precheck into the Slack triage flow so that the user is told (a) at trigger time when no workers are online, and (b) at submit time gets a hard reject (no workers) or an ETA (busy) — replacing today's silent wait that ends in a watchdog timeout.

**Architecture:** New `shared/queue.WorkerAvailability` service produces a typed `Verdict` from `JobQueue.ListWorkers`, `JobQueue.QueueDepth`, and `JobStore.ListAll`. The Slack adapter (`app/bot/workflow.go`) calls `CheckSoft` in `HandleTrigger` (non-blocking warn) and `CheckHard` at the top of `runTriage` (can reject). A small `app/bot/verdict_message.go` translates `Verdict` into Slack-flavored text — the seam where future mediums plug in their own renderers.

**Tech Stack:** Go 1.22+ / `slog` / `prometheus/client_golang` / existing `shared/queue` Redis transport / `queuetest` in-memory bundle for unit tests.

**Phases (one commit per task):**
1. Data model — `WorkerInfo.Slots`
2. Availability service — TDD growth
3. Metrics
4. Verdict rendering
5. Workflow integration (4 sub-tasks)
6. Worker side
7. App wiring + config
8. Final verification

---

## Phase 1 — Data Model

### Task 1: Add `Slots` field to `WorkerInfo`

**Files:**
- Modify: `shared/queue/job.go:116-123`

- [ ] **Step 1: Add the field**

In `shared/queue/job.go`, replace the existing `WorkerInfo` struct (lines 116–123) with:

```go
type WorkerInfo struct {
	WorkerID    string   `json:"worker_id"`
	Name        string   `json:"name"`
	Nickname    string   `json:"nickname,omitempty"`
	Agents      []string `json:"agents"`
	Tags        []string `json:"tags"`
	Slots       int      `json:"slots,omitempty"` // concurrent jobs this worker handles; 0 normalised to 1 by consumers
	ConnectedAt time.Time
}
```

- [ ] **Step 2: Verify the change compiles**

Run: `cd shared && go build ./...`
Expected: no output (success). Any callers building `WorkerInfo` literals continue to compile because `Slots` is positional-after-existing-fields and unset → `0`.

- [ ] **Step 3: Run existing queue tests (regression check)**

Run: `cd shared && go test ./queue/...`
Expected: all existing tests pass; `redis_jobqueue_test.go` JSON round-trip continues to work because `omitempty` on a zero int omits the field.

- [ ] **Step 4: Commit**

```bash
git add shared/queue/job.go
git commit -m "$(cat <<'EOF'
feat(queue): add Slots field to WorkerInfo

Per-worker concurrency declaration; zero value normalised to 1 by
consumers so existing single-job workers continue to work.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 2 — Availability Service (TDD)

### Task 2: Skeleton + healthy-path verdict

**Files:**
- Create: `shared/queue/availability.go`
- Create: `shared/queue/availability_test.go`

- [ ] **Step 1: Write the failing test**

Create `shared/queue/availability_test.go`:

```go
package queue_test

import (
	"context"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/shared/queue/queuetest"
)

func newAvail(t *testing.T) (*queuetest.JobQueue, queue.JobStore, queue.WorkerAvailability) {
	t.Helper()
	store := queue.NewMemJobStore()
	q := queuetest.NewJobQueue(50, store)
	a := queue.NewWorkerAvailability(q, store, queue.AvailabilityConfig{
		AvgJobDuration: 3 * time.Minute,
	})
	return q, store, a
}

func TestAvailability_HealthyOK(t *testing.T) {
	q, _, a := newAvail(t)

	q.Register(context.Background(), queue.WorkerInfo{WorkerID: "w1", Slots: 1})
	q.Register(context.Background(), queue.WorkerInfo{WorkerID: "w2", Slots: 1})

	v := a.CheckHard(context.Background())
	if v.Kind != queue.VerdictOK {
		t.Errorf("Kind = %q, want %q", v.Kind, queue.VerdictOK)
	}
	if v.WorkerCount != 2 {
		t.Errorf("WorkerCount = %d, want 2", v.WorkerCount)
	}
	if v.TotalSlots != 2 {
		t.Errorf("TotalSlots = %d, want 2", v.TotalSlots)
	}
}
```

- [ ] **Step 2: Run test — should fail (no symbols defined yet)**

Run: `cd shared && go test ./queue/ -run TestAvailability_HealthyOK -v`
Expected: FAIL — `undefined: queue.WorkerAvailability`, `undefined: queue.NewWorkerAvailability`, etc.

- [ ] **Step 3: Implement minimal availability service**

Create `shared/queue/availability.go`:

```go
package queue

import (
	"context"
	"log/slog"
	"time"
)

type VerdictKind string

const (
	VerdictOK            VerdictKind = "ok"
	VerdictBusyEnqueueOK VerdictKind = "busy_enqueue"
	VerdictNoWorkers     VerdictKind = "no_workers"
)

type Verdict struct {
	Kind          VerdictKind
	WorkerCount   int
	ActiveJobs    int
	TotalSlots    int
	EstimatedWait time.Duration
}

type WorkerAvailability interface {
	CheckSoft(ctx context.Context) Verdict
	CheckHard(ctx context.Context) Verdict
}

type AvailabilityConfig struct {
	AvgJobDuration time.Duration
}

type availability struct {
	queue   JobQueue
	store   JobStore
	avgJob  time.Duration
	logger  *slog.Logger
}

func NewWorkerAvailability(q JobQueue, store JobStore, cfg AvailabilityConfig) WorkerAvailability {
	avg := cfg.AvgJobDuration
	if avg <= 0 {
		avg = 3 * time.Minute
	}
	return &availability{
		queue:  q,
		store:  store,
		avgJob: avg,
		logger: slog.Default(),
	}
}

func (a *availability) CheckSoft(ctx context.Context) Verdict { return a.compute(ctx) }
func (a *availability) CheckHard(ctx context.Context) Verdict { return a.compute(ctx) }

func (a *availability) compute(ctx context.Context) Verdict {
	workers, err := a.queue.ListWorkers(ctx)
	if err != nil {
		a.logger.Warn("availability: ListWorkers failed", "error", err)
		return Verdict{Kind: VerdictOK}
	}
	totalSlots := 0
	for _, w := range workers {
		totalSlots += normaliseSlots(w.Slots)
	}
	return Verdict{
		Kind:        VerdictOK,
		WorkerCount: len(workers),
		TotalSlots:  totalSlots,
	}
}

func normaliseSlots(s int) int {
	if s <= 0 {
		return 1
	}
	return s
}
```

- [ ] **Step 4: Run test — should pass**

Run: `cd shared && go test ./queue/ -run TestAvailability_HealthyOK -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add shared/queue/availability.go shared/queue/availability_test.go
git commit -m "$(cat <<'EOF'
feat(queue): introduce WorkerAvailability service skeleton

Verdict types and OK-path computation. Subsequent commits add
NoWorkers, BusyEnqueueOK, slots normalisation, and fail-open paths.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 3: NoWorkers verdict

**Files:**
- Modify: `shared/queue/availability.go`
- Modify: `shared/queue/availability_test.go`

- [ ] **Step 1: Write the failing test**

Append to `shared/queue/availability_test.go`:

```go
func TestAvailability_NoWorkers(t *testing.T) {
	_, _, a := newAvail(t)

	v := a.CheckHard(context.Background())
	if v.Kind != queue.VerdictNoWorkers {
		t.Errorf("Kind = %q, want %q", v.Kind, queue.VerdictNoWorkers)
	}
	if v.WorkerCount != 0 {
		t.Errorf("WorkerCount = %d, want 0", v.WorkerCount)
	}
}
```

- [ ] **Step 2: Run — should fail (still returns OK)**

Run: `cd shared && go test ./queue/ -run TestAvailability_NoWorkers -v`
Expected: FAIL — `Kind = "ok", want "no_workers"`.

- [ ] **Step 3: Add the NoWorkers branch in `compute`**

In `shared/queue/availability.go`, replace the body of `compute` with:

```go
func (a *availability) compute(ctx context.Context) Verdict {
	workers, err := a.queue.ListWorkers(ctx)
	if err != nil {
		a.logger.Warn("availability: ListWorkers failed", "error", err)
		return Verdict{Kind: VerdictOK}
	}
	totalSlots := 0
	for _, w := range workers {
		totalSlots += normaliseSlots(w.Slots)
	}
	if len(workers) == 0 {
		return Verdict{Kind: VerdictNoWorkers}
	}
	return Verdict{
		Kind:        VerdictOK,
		WorkerCount: len(workers),
		TotalSlots:  totalSlots,
	}
}
```

- [ ] **Step 4: Run both tests**

Run: `cd shared && go test ./queue/ -run TestAvailability -v`
Expected: PASS for `TestAvailability_HealthyOK` and `TestAvailability_NoWorkers`.

- [ ] **Step 5: Commit**

```bash
git add shared/queue/availability.go shared/queue/availability_test.go
git commit -m "$(cat <<'EOF'
feat(queue): availability returns NoWorkers when no workers registered

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 4: BusyEnqueueOK verdict + ETA

**Files:**
- Modify: `shared/queue/availability.go`
- Modify: `shared/queue/availability_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestAvailability_BusyEnqueueOK_FullySaturated(t *testing.T) {
	q, store, a := newAvail(t)
	ctx := context.Background()

	q.Register(ctx, queue.WorkerInfo{WorkerID: "w1", Slots: 1})
	q.Register(ctx, queue.WorkerInfo{WorkerID: "w2", Slots: 1})

	// Two jobs in JobRunning consume both slots; queue depth = 0.
	store.Put(&queue.Job{ID: "j1"})
	store.UpdateStatus("j1", queue.JobRunning)
	store.Put(&queue.Job{ID: "j2"})
	store.UpdateStatus("j2", queue.JobRunning)

	v := a.CheckHard(ctx)
	if v.Kind != queue.VerdictBusyEnqueueOK {
		t.Errorf("Kind = %q, want %q", v.Kind, queue.VerdictBusyEnqueueOK)
	}
	if v.ActiveJobs != 2 {
		t.Errorf("ActiveJobs = %d, want 2", v.ActiveJobs)
	}
	wantETA := 1 * 3 * time.Minute // overflow=1
	if v.EstimatedWait != wantETA {
		t.Errorf("EstimatedWait = %v, want %v", v.EstimatedWait, wantETA)
	}
}

func TestAvailability_BusyEnqueueOK_WithQueueDepth(t *testing.T) {
	q, store, a := newAvail(t)
	ctx := context.Background()

	q.Register(ctx, queue.WorkerInfo{WorkerID: "w1", Slots: 1})

	// 1 running + 4 queued = 5 active, slots = 1, overflow = 5
	store.Put(&queue.Job{ID: "j1"})
	store.UpdateStatus("j1", queue.JobRunning)
	for i := 0; i < 4; i++ {
		q.Submit(ctx, &queue.Job{ID: "p" + string(rune('a'+i))})
	}

	v := a.CheckHard(ctx)
	if v.Kind != queue.VerdictBusyEnqueueOK {
		t.Errorf("Kind = %q, want %q", v.Kind, queue.VerdictBusyEnqueueOK)
	}
	wantETA := time.Duration(5) * 3 * time.Minute
	if v.EstimatedWait != wantETA {
		t.Errorf("EstimatedWait = %v, want %v", v.EstimatedWait, wantETA)
	}
}
```

- [ ] **Step 2: Run — should fail (active count not yet computed)**

Run: `cd shared && go test ./queue/ -run TestAvailability_BusyEnqueueOK -v`
Expected: FAIL — verdict still returns `OK` because the busy branch is missing.

- [ ] **Step 3: Add active-jobs computation + busy branch**

Replace `compute` body:

```go
func (a *availability) compute(ctx context.Context) Verdict {
	workers, err := a.queue.ListWorkers(ctx)
	if err != nil {
		a.logger.Warn("availability: ListWorkers failed", "error", err)
		return Verdict{Kind: VerdictOK}
	}
	totalSlots := 0
	for _, w := range workers {
		totalSlots += normaliseSlots(w.Slots)
	}
	if len(workers) == 0 {
		return Verdict{Kind: VerdictNoWorkers}
	}

	depth := a.queue.QueueDepth()
	states, err := a.store.ListAll()
	if err != nil {
		a.logger.Warn("availability: ListAll failed", "error", err)
		return Verdict{Kind: VerdictOK, WorkerCount: len(workers), TotalSlots: totalSlots}
	}
	running := 0
	for _, s := range states {
		if s.Status == JobPreparing || s.Status == JobRunning {
			running++
		}
	}
	active := depth + running

	if active >= totalSlots {
		overflow := active - totalSlots + 1
		return Verdict{
			Kind:          VerdictBusyEnqueueOK,
			WorkerCount:   len(workers),
			TotalSlots:    totalSlots,
			ActiveJobs:    active,
			EstimatedWait: time.Duration(overflow) * a.avgJob,
		}
	}
	return Verdict{
		Kind:        VerdictOK,
		WorkerCount: len(workers),
		TotalSlots:  totalSlots,
		ActiveJobs:  active,
	}
}
```

- [ ] **Step 4: Run all availability tests**

Run: `cd shared && go test ./queue/ -run TestAvailability -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add shared/queue/availability.go shared/queue/availability_test.go
git commit -m "$(cat <<'EOF'
feat(queue): availability returns BusyEnqueueOK with ETA when saturated

ETA = (active - slots + 1) * AvgJobDuration. Coarse intentional —
spec is signal "you'll wait", not minute-accurate prediction.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 5: Multi-slot + zero-slot normalisation

**Files:**
- Modify: `shared/queue/availability_test.go`

- [ ] **Step 1: Write the failing tests**

Append:

```go
func TestAvailability_MultiSlotWorker(t *testing.T) {
	q, store, a := newAvail(t)
	ctx := context.Background()

	// One worker with 3 slots, two running jobs → 1 spare slot → OK.
	q.Register(ctx, queue.WorkerInfo{WorkerID: "w1", Slots: 3})
	store.Put(&queue.Job{ID: "j1"})
	store.UpdateStatus("j1", queue.JobRunning)
	store.Put(&queue.Job{ID: "j2"})
	store.UpdateStatus("j2", queue.JobRunning)

	v := a.CheckHard(ctx)
	if v.Kind != queue.VerdictOK {
		t.Errorf("Kind = %q, want %q (3 slots, 2 active)", v.Kind, queue.VerdictOK)
	}
	if v.TotalSlots != 3 {
		t.Errorf("TotalSlots = %d, want 3", v.TotalSlots)
	}
}

func TestAvailability_ZeroSlotsNormalisedToOne(t *testing.T) {
	q, _, a := newAvail(t)
	ctx := context.Background()

	// Slots=0 (e.g. older worker that didn't set the field) → treated as 1.
	q.Register(ctx, queue.WorkerInfo{WorkerID: "old", Slots: 0})

	v := a.CheckHard(ctx)
	if v.TotalSlots != 1 {
		t.Errorf("TotalSlots = %d, want 1 (normalised)", v.TotalSlots)
	}
	if v.Kind != queue.VerdictOK {
		t.Errorf("Kind = %q, want %q", v.Kind, queue.VerdictOK)
	}
}
```

- [ ] **Step 2: Run — these should already pass**

Run: `cd shared && go test ./queue/ -run TestAvailability -v`
Expected: PASS for both new tests. (`normaliseSlots` is already in place from Task 2; this task locks that behavior with explicit tests.)

- [ ] **Step 3: Commit**

```bash
git add shared/queue/availability_test.go
git commit -m "$(cat <<'EOF'
test(queue): lock multi-slot and zero-slot normalisation behaviour

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 6: Fail-open on dependency errors

**Files:**
- Create: `shared/queue/availability_failopen_test.go` (separate file because we need a stub `JobQueue` that returns errors, distinct from `queuetest.JobQueue`)

- [ ] **Step 1: Write the failing tests**

Create `shared/queue/availability_failopen_test.go`:

```go
package queue_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

// erroringQueue satisfies queue.JobQueue but returns errors on the methods
// availability depends on. Other methods panic — they should never be called
// by the availability service.
type erroringQueue struct {
	listWorkersErr error
}

func (e *erroringQueue) Submit(context.Context, *queue.Job) error  { panic("unused") }
func (e *erroringQueue) QueuePosition(string) (int, error)          { panic("unused") }
func (e *erroringQueue) QueueDepth() int                            { return 0 }
func (e *erroringQueue) Receive(context.Context) (<-chan *queue.Job, error) {
	panic("unused")
}
func (e *erroringQueue) Ack(context.Context, string) error            { panic("unused") }
func (e *erroringQueue) Register(context.Context, queue.WorkerInfo) error {
	panic("unused")
}
func (e *erroringQueue) Unregister(context.Context, string) error { panic("unused") }
func (e *erroringQueue) ListWorkers(context.Context) ([]queue.WorkerInfo, error) {
	if e.listWorkersErr != nil {
		return nil, e.listWorkersErr
	}
	return nil, nil
}
func (e *erroringQueue) Close() error { return nil }

// erroringStore satisfies queue.JobStore but errors on ListAll.
type erroringStore struct {
	listAllErr error
}

func (s *erroringStore) Put(*queue.Job) error                    { panic("unused") }
func (s *erroringStore) Get(string) (*queue.JobState, error)      { panic("unused") }
func (s *erroringStore) GetByThread(string, string) (*queue.JobState, error) {
	panic("unused")
}
func (s *erroringStore) ListPending() ([]*queue.JobState, error) { panic("unused") }
func (s *erroringStore) UpdateStatus(string, queue.JobStatus) error {
	panic("unused")
}
func (s *erroringStore) SetWorker(string, string) error { panic("unused") }
func (s *erroringStore) SetAgentStatus(string, queue.StatusReport) error {
	panic("unused")
}
func (s *erroringStore) Delete(string) error { panic("unused") }
func (s *erroringStore) ListAll() ([]*queue.JobState, error) {
	if s.listAllErr != nil {
		return nil, s.listAllErr
	}
	return nil, nil
}

func TestAvailability_FailOpen_ListWorkersError(t *testing.T) {
	q := &erroringQueue{listWorkersErr: errors.New("redis down")}
	store := queue.NewMemJobStore()
	a := queue.NewWorkerAvailability(q, store, queue.AvailabilityConfig{
		AvgJobDuration: 3 * time.Minute,
	})

	v := a.CheckHard(context.Background())
	if v.Kind != queue.VerdictOK {
		t.Errorf("Kind = %q, want %q (fail-open)", v.Kind, queue.VerdictOK)
	}
}

func TestAvailability_FailOpen_ListAllError(t *testing.T) {
	// Need a queue that returns at least one worker (so we pass the NoWorkers gate)
	// AND a store that errors on ListAll.
	q := &workerListingQueue{erroringQueue: erroringQueue{}, workers: []queue.WorkerInfo{{WorkerID: "w1"}}}
	store := &erroringStore{listAllErr: errors.New("store down")}
	a := queue.NewWorkerAvailability(q, store, queue.AvailabilityConfig{
		AvgJobDuration: 3 * time.Minute,
	})

	v := a.CheckHard(context.Background())
	if v.Kind != queue.VerdictOK {
		t.Errorf("Kind = %q, want %q (fail-open on store error)", v.Kind, queue.VerdictOK)
	}
}

// workerListingQueue is erroringQueue but with a configurable workers list.
type workerListingQueue struct {
	erroringQueue
	workers []queue.WorkerInfo
}

func (w *workerListingQueue) ListWorkers(context.Context) ([]queue.WorkerInfo, error) {
	return w.workers, nil
}
```

- [ ] **Step 2: Run — `TestAvailability_FailOpen_ListWorkersError` already passes, `TestAvailability_FailOpen_ListAllError` should pass too**

Run: `cd shared && go test ./queue/ -run TestAvailability_FailOpen -v`
Expected: PASS for both. The `compute` function written in Task 4 already returns `OK` on `ListAll` error before the busy branch is evaluated.

- [ ] **Step 3: Commit**

```bash
git add shared/queue/availability_failopen_test.go
git commit -m "$(cat <<'EOF'
test(queue): lock availability fail-open behaviour for dep errors

Treat ListWorkers / ListAll errors as transient blindness; return OK
rather than mis-classify the system as broken. The watchdog backstops.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 3 — Metrics

### Task 7: Add availability metrics + register + wire

**Files:**
- Modify: `shared/metrics/metrics.go:139-194`
- Modify: `shared/queue/availability.go`

- [ ] **Step 1: Add the three new metrics in `shared/metrics/metrics.go`**

After the existing `WatchdogKillsTotal` declaration (around line 145), insert:

```go
// ---- Availability ----

var WorkerAvailabilityVerdictTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: namespace,
	Name:      "worker_availability_verdict_total",
	Help:      "Counts of availability verdicts by kind and stage.",
}, []string{"kind", "stage"})

var WorkerAvailabilityCheckDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
	Namespace: namespace,
	Name:      "worker_availability_check_duration_seconds",
	Help:      "Latency of WorkerAvailability.compute.",
	Buckets:   []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
})

var WorkerAvailabilityCheckErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: namespace,
	Name:      "worker_availability_check_errors_total",
	Help:      "Errors from availability dependencies.",
}, []string{"dependency"})
```

Then in the `reg.MustRegister(...)` block in `Register` (around line 173–194), add the three new metrics:

```go
reg.MustRegister(
	RequestTotal,
	// ... existing ...
	ExternalErrorsTotal,
	WorkerAvailabilityVerdictTotal,    // NEW
	WorkerAvailabilityCheckDuration,    // NEW
	WorkerAvailabilityCheckErrors,      // NEW
)
```

- [ ] **Step 2: Verify metrics package compiles & tests still pass**

Run: `cd shared && go test ./metrics/ -v`
Expected: PASS.

- [ ] **Step 3: Wire metrics into availability via a hook (avoid import cycle)**

`shared/queue` cannot import `shared/metrics` (would create a cycle: metrics imports queue for the GaugeFunc). Use the same pattern as `WithWatchdogKillHook`.

In `shared/queue/availability.go`, replace the file with:

```go
package queue

import (
	"context"
	"log/slog"
	"time"
)

type VerdictKind string

const (
	VerdictOK            VerdictKind = "ok"
	VerdictBusyEnqueueOK VerdictKind = "busy_enqueue"
	VerdictNoWorkers     VerdictKind = "no_workers"
)

type Verdict struct {
	Kind          VerdictKind
	WorkerCount   int
	ActiveJobs    int
	TotalSlots    int
	EstimatedWait time.Duration
}

type WorkerAvailability interface {
	CheckSoft(ctx context.Context) Verdict
	CheckHard(ctx context.Context) Verdict
}

type AvailabilityConfig struct {
	AvgJobDuration time.Duration
}

// AvailabilityOption configures observability hooks without creating an
// import cycle with shared/metrics.
type AvailabilityOption func(*availability)

// WithVerdictHook is invoked on every compute() call with kind, stage, and duration.
func WithVerdictHook(fn func(kind, stage string, d time.Duration)) AvailabilityOption {
	return func(a *availability) { a.verdictHook = fn }
}

// WithDepErrorHook is invoked when a dependency call fails, with the
// dependency name (e.g. "list_workers", "list_all").
func WithDepErrorHook(fn func(dep string)) AvailabilityOption {
	return func(a *availability) { a.depErrorHook = fn }
}

type availability struct {
	queue        JobQueue
	store        JobStore
	avgJob       time.Duration
	logger       *slog.Logger
	verdictHook  func(kind, stage string, d time.Duration)
	depErrorHook func(dep string)
}

func NewWorkerAvailability(q JobQueue, store JobStore, cfg AvailabilityConfig, opts ...AvailabilityOption) WorkerAvailability {
	avg := cfg.AvgJobDuration
	if avg <= 0 {
		avg = 3 * time.Minute
	}
	a := &availability{
		queue:  q,
		store:  store,
		avgJob: avg,
		logger: slog.Default(),
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

func (a *availability) CheckSoft(ctx context.Context) Verdict {
	return a.observe(ctx, "soft")
}

func (a *availability) CheckHard(ctx context.Context) Verdict {
	return a.observe(ctx, "hard")
}

func (a *availability) observe(ctx context.Context, stage string) Verdict {
	start := time.Now()
	v := a.compute(ctx)
	if a.verdictHook != nil {
		a.verdictHook(string(v.Kind), stage, time.Since(start))
	}
	return v
}

func (a *availability) compute(ctx context.Context) Verdict {
	workers, err := a.queue.ListWorkers(ctx)
	if err != nil {
		a.logger.Warn("availability: ListWorkers failed", "error", err)
		if a.depErrorHook != nil {
			a.depErrorHook("list_workers")
		}
		return Verdict{Kind: VerdictOK}
	}
	totalSlots := 0
	for _, w := range workers {
		totalSlots += normaliseSlots(w.Slots)
	}
	if len(workers) == 0 {
		return Verdict{Kind: VerdictNoWorkers}
	}

	depth := a.queue.QueueDepth()
	states, err := a.store.ListAll()
	if err != nil {
		a.logger.Warn("availability: ListAll failed", "error", err)
		if a.depErrorHook != nil {
			a.depErrorHook("list_all")
		}
		return Verdict{Kind: VerdictOK, WorkerCount: len(workers), TotalSlots: totalSlots}
	}
	running := 0
	for _, s := range states {
		if s.Status == JobPreparing || s.Status == JobRunning {
			running++
		}
	}
	active := depth + running

	if active >= totalSlots {
		overflow := active - totalSlots + 1
		return Verdict{
			Kind:          VerdictBusyEnqueueOK,
			WorkerCount:   len(workers),
			TotalSlots:    totalSlots,
			ActiveJobs:    active,
			EstimatedWait: time.Duration(overflow) * a.avgJob,
		}
	}
	return Verdict{
		Kind:        VerdictOK,
		WorkerCount: len(workers),
		TotalSlots:  totalSlots,
		ActiveJobs:  active,
	}
}

func normaliseSlots(s int) int {
	if s <= 0 {
		return 1
	}
	return s
}
```

- [ ] **Step 4: Run availability tests**

Run: `cd shared && go test ./queue/ -run TestAvailability -v`
Expected: all PASS (no test depends on the hook being set; default nil).

- [ ] **Step 5: Commit**

```bash
git add shared/queue/availability.go shared/metrics/metrics.go
git commit -m "$(cat <<'EOF'
feat(metrics): WorkerAvailability verdict + duration + dep errors

Wired via functional options (verdict hook + dep-error hook) to avoid
introducing a shared/queue → shared/metrics import cycle.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 4 — Verdict Rendering

### Task 8: `verdict_message.go` + tests

**Files:**
- Create: `app/bot/verdict_message.go`
- Create: `app/bot/verdict_message_test.go`

- [ ] **Step 1: Write the failing test**

Create `app/bot/verdict_message_test.go`:

```go
package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

func TestRenderSoftWarn_NoWorkers(t *testing.T) {
	got := RenderSoftWarn(queue.Verdict{Kind: queue.VerdictNoWorkers})
	if !strings.Contains(got, ":warning:") {
		t.Errorf("missing :warning: prefix; got %q", got)
	}
	if !strings.Contains(got, "沒有 worker") {
		t.Errorf("missing key phrase '沒有 worker'; got %q", got)
	}
}

func TestRenderHardReject_NoWorkers(t *testing.T) {
	got := RenderHardReject(queue.Verdict{Kind: queue.VerdictNoWorkers})
	if !strings.Contains(got, ":x:") {
		t.Errorf("missing :x: prefix; got %q", got)
	}
	if !strings.Contains(got, "無法處理") {
		t.Errorf("missing '無法處理'; got %q", got)
	}
}

func TestRenderBusyHint_WithETA(t *testing.T) {
	v := queue.Verdict{
		Kind:          queue.VerdictBusyEnqueueOK,
		EstimatedWait: 9 * time.Minute,
	}
	got := RenderBusyHint(v)
	if !strings.Contains(got, "預估等候") {
		t.Errorf("missing '預估等候'; got %q", got)
	}
	if !strings.Contains(got, "9m") {
		t.Errorf("expected '9m' in output; got %q", got)
	}
}

func TestRenderBusyHint_ZeroETA_ReturnsEmpty(t *testing.T) {
	v := queue.Verdict{Kind: queue.VerdictBusyEnqueueOK, EstimatedWait: 0}
	if got := RenderBusyHint(v); got != "" {
		t.Errorf("expected empty string for zero ETA; got %q", got)
	}
}
```

- [ ] **Step 2: Run — should fail**

Run: `cd app && go test ./bot/ -run "TestRender" -v`
Expected: FAIL — `undefined: RenderSoftWarn`, etc.

- [ ] **Step 3: Implement renderers**

Create `app/bot/verdict_message.go`:

```go
package bot

import (
	"fmt"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

// RenderSoftWarn produces the trigger-time soft warning. Currently only the
// NoWorkers verdict is rendered; other verdicts return "" (caller should not
// post empty messages).
func RenderSoftWarn(v queue.Verdict) string {
	if v.Kind != queue.VerdictNoWorkers {
		return ""
	}
	return ":warning: 目前沒有 worker 在線，你仍可繼續選擇，送出時會再確認一次。"
}

// RenderHardReject produces the submit-time rejection message.
func RenderHardReject(v queue.Verdict) string {
	if v.Kind != queue.VerdictNoWorkers {
		return ""
	}
	return ":x: 目前沒有 worker 在線，無法處理。請稍後再試。"
}

// RenderBusyHint produces the suffix appended to the lifecycle queue
// message when the verdict is BusyEnqueueOK with a non-zero ETA.
func RenderBusyHint(v queue.Verdict) string {
	if v.EstimatedWait <= 0 {
		return ""
	}
	return fmt.Sprintf("(預估等候 ~%dm)",
		int(v.EstimatedWait.Round(time.Minute).Minutes()))
}
```

- [ ] **Step 4: Run renderer tests — should pass**

Run: `cd app && go test ./bot/ -run "TestRender" -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add app/bot/verdict_message.go app/bot/verdict_message_test.go
git commit -m "$(cat <<'EOF'
feat(bot): verdict_message renders Slack text from queue.Verdict

Single seam where future mediums (X, Discord) plug in their own
renderers. Today's text is intentionally minimal — oncall handles
or richer guidance can be added without touching the verdict service.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 5 — Workflow Integration

### Task 9: Inject availability + add busyHint field (no behaviour change yet)

**Files:**
- Modify: `app/bot/workflow.go:60-115` (Workflow struct + NewWorkflow)
- Modify: `app/bot/workflow.go:41-58` (pendingTriage struct)

- [ ] **Step 1: Add the field, constructor param, and pendingTriage field**

In `app/bot/workflow.go`:

(a) Inside `pendingTriage` (around line 41–58), append:

```go
type pendingTriage struct {
	// ... existing fields ...
	RepoWasPicked  bool
	busyHint       string // populated by hard check when verdict is BusyEnqueueOK
}
```

(b) Inside `Workflow` (around line 60–77), add an `availability` field:

```go
type Workflow struct {
	// ... existing fields ...
	skillProvider SkillProvider
	secretKey     []byte
	identity      Identity
	availability  queue.WorkerAvailability // NEW

	mu        sync.Mutex
	pending   map[string]*pendingTriage
	autoBound map[string]bool
}
```

(c) Update the `NewWorkflow` signature (around line 79–115). Add `availability queue.WorkerAvailability` as the LAST parameter (after `identity Identity`):

```go
func NewWorkflow(
	cfg *config.Config,
	slack slackAPI,
	repoCache *ghclient.RepoCache,
	repoDiscovery *ghclient.RepoDiscovery,
	jobQueue queue.JobQueue,
	jobStore queue.JobStore,
	attachStore queue.AttachmentStore,
	resultBus queue.ResultBus,
	skillProvider SkillProvider,
	identity Identity,
	availability queue.WorkerAvailability, // NEW
) *Workflow {
```

And inside the function body, add `availability: availability,` to the struct literal.

- [ ] **Step 2: Update the only existing caller (`app/app.go:142`)**

In `app/app.go` line 142, the current call is:

```go
wf := bot.NewWorkflow(cfg, slackClient, repoCache, repoDiscovery, coordinator, jobStore, bundle.Attachments, bundle.Results, skillLoader, identity)
```

Update to pass `nil` for now (Task 15 will replace this with a real instance):

```go
wf := bot.NewWorkflow(cfg, slackClient, repoCache, repoDiscovery, coordinator, jobStore, bundle.Attachments, bundle.Results, skillLoader, identity, nil)
```

- [ ] **Step 3: Verify the whole tree compiles**

Run: `go build ./...`
Expected: success.

- [ ] **Step 4: Run all existing tests (regression)**

Run: `go test ./...`
Expected: all PASS. (No existing test calls `HandleTrigger` or `runTriage`, so the nil `availability` is not yet dereferenced. The new tests in Tasks 10–12 will set a stub.)

- [ ] **Step 5: Commit**

```bash
git add app/bot/workflow.go app/app.go
git commit -m "$(cat <<'EOF'
refactor(bot): inject WorkerAvailability + busyHint field

Wiring only — no behaviour change. The hard/soft check call sites
land in subsequent commits.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 10: Submit-time hard check — NoWorkers reject path

**Files:**
- Modify: `app/bot/workflow.go:386-396` (top of `runTriage`)
- Modify: `app/bot/workflow_test.go` (add `stubAvailability` + new test)

- [ ] **Step 1: Add the `stubAvailability` helper to `workflow_test.go`**

Append to `app/bot/workflow_test.go` (right above `func newTestWorkflow`):

```go
// stubAvailability lets tests pre-program verdicts.
type stubAvailability struct {
	mu          sync.Mutex
	SoftVerdict queue.Verdict
	HardVerdict queue.Verdict
	SoftCalls   int
	HardCalls   int
}

func (s *stubAvailability) CheckSoft(ctx context.Context) queue.Verdict {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.SoftCalls++
	return s.SoftVerdict
}
func (s *stubAvailability) CheckHard(ctx context.Context) queue.Verdict {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.HardCalls++
	return s.HardVerdict
}
```

You will also need to add `"context"` to the existing imports if not already present.

- [ ] **Step 2: Write the failing test**

Append to `app/bot/workflow_test.go`:

```go
func TestRunTriage_NoWorkers_HardRejects(t *testing.T) {
	slack := &stubSlack{}
	avail := &stubAvailability{
		HardVerdict: queue.Verdict{Kind: queue.VerdictNoWorkers},
	}
	cfg := &config.Config{
		Channels:        map[string]config.ChannelConfig{"C1": {Repo: "o/r"}},
		ChannelDefaults: config.ChannelConfig{},
	}
	w := newTestWorkflow(t, slack, cfg)
	w.availability = avail

	pt := testPending("C1", "T1", true, "")
	pt.SelectedRepo = "o/r"
	pt.SelectedBranch = "main"

	w.runTriage(pt)

	if avail.HardCalls != 1 {
		t.Errorf("HardCalls = %d, want 1", avail.HardCalls)
	}

	foundReject := false
	for _, m := range slack.PostMessageCalls {
		if containsStr(m.Text, ":x:") && containsStr(m.Text, "沒有 worker") {
			foundReject = true
		}
	}
	if !foundReject {
		t.Errorf("expected hard reject :x: message; got posts: %+v", slack.PostMessageCalls)
	}

	// Verify the lifecycle ":mag:" message was NOT posted (hard reject runs first).
	for _, m := range slack.PostMessageCalls {
		if containsStr(m.Text, ":mag:") {
			t.Errorf("lifecycle :mag: message should NOT appear after hard reject; got %q", m.Text)
		}
	}
}
```

- [ ] **Step 3: Run — should fail**

Run: `cd app && go test ./bot/ -run TestRunTriage_NoWorkers_HardRejects -v`
Expected: FAIL — the hard check is not yet wired; the test will see the `:mag:` lifecycle message and no `:x:` reject. (Note: it will likely panic on nil queue at `w.queue.Submit` — the failure is expected one way or another. The point is the green-light test will pass after Step 4.)

- [ ] **Step 4: Implement the hard check**

In `app/bot/workflow.go`, locate `runTriage` (line 386). Right after `ctx := context.Background()` (line 387) and BEFORE the `statusMsgTS, err := w.slack.PostMessageWithTS(...)` line (line 392), insert:

```go
	// Hard availability check — must run before any lifecycle Slack post so
	// rejections don't leave orphan ":mag:" messages.
	if w.availability != nil {
		verdict := w.availability.CheckHard(ctx)
		switch verdict.Kind {
		case queue.VerdictNoWorkers:
			w.slack.PostMessage(pt.ChannelID,
				RenderHardReject(verdict), pt.ThreadTS)
			w.clearDedup(pt)
			return
		case queue.VerdictBusyEnqueueOK:
			pt.busyHint = RenderBusyHint(verdict)
		case queue.VerdictOK:
			// continue
		}
	}
```

The `if w.availability != nil` guard exists only because Task 15 hasn't replaced the `nil` in `app.go` yet — once that's done, the field is always populated.

- [ ] **Step 5: Run — should pass**

Run: `cd app && go test ./bot/ -run TestRunTriage_NoWorkers_HardRejects -v`
Expected: PASS.

- [ ] **Step 6: Run full bot test package (regression)**

Run: `cd app && go test ./bot/`
Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add app/bot/workflow.go app/bot/workflow_test.go
git commit -m "$(cat <<'EOF'
feat(bot): runTriage hard-rejects when no workers are online

Posts the reject message, clears thread dedup so the user can retry,
and returns before the lifecycle ':mag:' message is sent.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 11: Submit-time hard check — BusyEnqueueOK appends ETA hint

**Files:**
- Modify: `app/bot/workflow.go:537-544` (the statusMsg block)
- Modify: `app/bot/workflow_test.go` (new test)

- [ ] **Step 1: Append busyHint to statusMsg in `workflow.go`**

In `app/bot/workflow.go`, locate the block (around lines 537–544):

```go
	pos, _ := w.queue.QueuePosition(job.ID)
	var statusMsg string
	if pos <= 1 {
		statusMsg = ":hourglass_flowing_sand: 正在處理你的請求..."
	} else {
		statusMsg = fmt.Sprintf(":hourglass_flowing_sand: 已加入排隊，前面有 %d 個請求", pos-1)
	}
```

Replace with:

```go
	pos, _ := w.queue.QueuePosition(job.ID)
	var statusMsg string
	if pos <= 1 {
		statusMsg = ":hourglass_flowing_sand: 正在處理你的請求..."
	} else {
		statusMsg = fmt.Sprintf(":hourglass_flowing_sand: 已加入排隊，前面有 %d 個請求", pos-1)
	}
	if pt.busyHint != "" {
		statusMsg += " " + pt.busyHint
	}
```

- [ ] **Step 2: Write the failing test**

Append to `app/bot/workflow_test.go`:

```go
func TestRunTriage_BusyEnqueueOK_LifecycleMessageIncludesETA(t *testing.T) {
	slack := &stubSlack{}
	avail := &stubAvailability{
		HardVerdict: queue.Verdict{
			Kind:          queue.VerdictBusyEnqueueOK,
			EstimatedWait: 6 * time.Minute,
		},
	}
	cfg := &config.Config{
		Channels:        map[string]config.ChannelConfig{"C1": {Repo: "o/r"}},
		ChannelDefaults: config.ChannelConfig{},
	}
	w := newTestWorkflow(t, slack, cfg)
	w.availability = avail

	// runTriage requires a working queue + store + attachments + results.
	store := queue.NewMemJobStore()
	bundle := queuetest.NewBundle(50, 1, store)
	w.queue = bundle.Queue
	w.store = store
	w.attachments = bundle.Attachments
	w.results = bundle.Results

	pt := testPending("C1", "T1", true, "")
	pt.SelectedRepo = "o/r"
	pt.SelectedBranch = "main"

	// FetchThreadContext stub returns nil messages — runTriage handles this by
	// notifying error and returning. To exercise the ETA path we need at least
	// one message. Inject one via a custom stub return.
	slack.FetchThreadResult = []slackclient.ThreadRawMessage{
		{User: "U1", Timestamp: "T1", Text: "help me"},
	}

	w.runTriage(pt)

	// Find the lifecycle status message (post-submit). It should contain the ETA hint.
	foundETA := false
	for _, u := range slack.UpdateMessageCalls {
		if containsStr(u.Text, ":hourglass") && containsStr(u.Text, "預估等候") {
			foundETA = true
		}
	}
	for _, m := range slack.PostMessageCalls {
		if containsStr(m.Text, ":hourglass") && containsStr(m.Text, "預估等候") {
			foundETA = true
		}
	}
	for _, b := range slack.PostMessageWithButtonCalls {
		if containsStr(b.Text, ":hourglass") && containsStr(b.Text, "預估等候") {
			foundETA = true
		}
	}
	if !foundETA {
		t.Errorf("expected lifecycle message containing 預估等候; got updates=%+v posts=%+v buttons=%+v",
			slack.UpdateMessageCalls, slack.PostMessageCalls, slack.PostMessageWithButtonCalls)
	}
}
```

You will also need to:
- Add `"time"` import to `workflow_test.go` if not present.
- Add `"github.com/Ivantseng123/agentdock/shared/queue/queuetest"` import.
- Extend `stubSlack` with a `FetchThreadResult []slackclient.ThreadRawMessage` field (and update `FetchThreadContext` to return it):

```go
type stubSlack struct {
	mu sync.Mutex
	// ... existing fields ...
	FetchThreadResult []slackclient.ThreadRawMessage
}

func (s *stubSlack) FetchThreadContext(channelID, threadTS, triggerTS, botUserID, botID string, limit int) ([]slackclient.ThreadRawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.FetchThreadResult, nil
}
```

(Replace the existing `FetchThreadContext` method that always returns `nil, nil`.)

- [ ] **Step 3: Run — should pass**

Run: `cd app && go test ./bot/ -run TestRunTriage_BusyEnqueueOK_LifecycleMessageIncludesETA -v`
Expected: PASS.

- [ ] **Step 4: Run full bot tests (regression)**

Run: `cd app && go test ./bot/`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add app/bot/workflow.go app/bot/workflow_test.go
git commit -m "$(cat <<'EOF'
feat(bot): runTriage appends ETA hint when verdict is BusyEnqueueOK

Lifecycle message becomes ':hourglass: 已加入排隊，前面有 N 個請求 (預估等候 ~Xm)'.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 12: Trigger-time soft warn

**Files:**
- Modify: `app/bot/workflow.go:141-208` (`HandleTrigger`)
- Modify: `app/bot/workflow_test.go` (new test)

- [ ] **Step 1: Insert the soft check in `HandleTrigger`**

In `app/bot/workflow.go`, locate `HandleTrigger`. After the channel-guard block (around line 156, right after `channelCfg = w.cfg.ChannelDefaults` for the auto-bind branch — i.e., after the `if !ok { ... }` block ends) and BEFORE `reqID := logging.NewRequestID()`, insert:

```go
	// Soft availability check — informational only; do NOT block the flow.
	// Hard check at submit time decides whether to proceed.
	if w.availability != nil {
		verdict := w.availability.CheckSoft(context.Background())
		if verdict.Kind == queue.VerdictNoWorkers {
			w.slack.PostMessage(event.ChannelID,
				RenderSoftWarn(verdict), event.ThreadTS)
		}
	}
```

You will need to import `"context"` if not already imported (it is, per existing `runTriage` body).

- [ ] **Step 2: Write the failing test**

Append to `app/bot/workflow_test.go`:

```go
func TestHandleTrigger_NoWorkers_PostsSoftWarnButContinues(t *testing.T) {
	slack := &stubSlack{}
	avail := &stubAvailability{
		SoftVerdict: queue.Verdict{Kind: queue.VerdictNoWorkers},
	}
	cfg := &config.Config{
		Channels: map[string]config.ChannelConfig{
			"C1": {Repos: []string{"o/a", "o/b"}},
		},
	}
	w := newTestWorkflow(t, slack, cfg)
	w.availability = avail

	w.HandleTrigger(slackclient.TriggerEvent{
		ChannelID: "C1",
		ThreadTS:  "T1",
		TriggerTS: "T1",
		UserID:    "U1",
		Text:      "<@bot>",
	})

	if avail.SoftCalls != 1 {
		t.Errorf("SoftCalls = %d, want 1", avail.SoftCalls)
	}

	foundWarn := false
	for _, m := range slack.PostMessageCalls {
		if containsStr(m.Text, ":warning:") && containsStr(m.Text, "沒有 worker") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Errorf("expected :warning: soft warn; got posts: %+v", slack.PostMessageCalls)
	}

	// Selector must still be posted — soft warn does NOT short-circuit.
	if len(slack.PostSelectorCalls) != 1 {
		t.Errorf("expected 1 PostSelector call (flow continues); got %d", len(slack.PostSelectorCalls))
	}
}

func TestHandleTrigger_HealthyOK_NoSoftWarn(t *testing.T) {
	slack := &stubSlack{}
	avail := &stubAvailability{
		SoftVerdict: queue.Verdict{Kind: queue.VerdictOK},
	}
	cfg := &config.Config{
		Channels: map[string]config.ChannelConfig{
			"C1": {Repos: []string{"o/a", "o/b"}},
		},
	}
	w := newTestWorkflow(t, slack, cfg)
	w.availability = avail

	w.HandleTrigger(slackclient.TriggerEvent{
		ChannelID: "C1", ThreadTS: "T1", TriggerTS: "T1", UserID: "U1", Text: "<@bot>",
	})

	for _, m := range slack.PostMessageCalls {
		if containsStr(m.Text, "沒有 worker") {
			t.Errorf("OK verdict should not post soft warn; got %q", m.Text)
		}
	}
}
```

- [ ] **Step 3: Run — should pass**

Run: `cd app && go test ./bot/ -run TestHandleTrigger -v`
Expected: PASS for both new tests, plus the existing `HandleTrigger` tests if any. (The existing tests don't call `HandleTrigger` directly — verify no regressions.)

- [ ] **Step 4: Run full bot tests (regression)**

Run: `cd app && go test ./bot/`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add app/bot/workflow.go app/bot/workflow_test.go
git commit -m "$(cat <<'EOF'
feat(bot): HandleTrigger posts soft warn when no workers (non-blocking)

Selection still proceeds; the hard check at submit is the gate.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 6 — Worker Side

### Task 13: Worker registers with `Slots: 1`

**Files:**
- Modify: `worker/pool/pool.go:242-248`
- Modify: `worker/pool/pool.go:260-266`

- [ ] **Step 1: Update both call sites in `workerHeartbeat`**

In `worker/pool/pool.go`, the function `workerHeartbeat` (around line 239) builds `WorkerInfo` in two places. Add `Slots: 1,` to both:

(a) Initial registration (around line 242–247):

```go
	for i := 0; i < p.cfg.WorkerCount; i++ {
		info := queue.WorkerInfo{
			WorkerID:    fmt.Sprintf("%s/worker-%d", p.cfg.Hostname, i),
			Name:        p.cfg.Hostname,
			Nickname:    p.nicknameForIndex(i),
			Slots:       1, // hardcoded; future work: read from worker.yaml when concurrent execution lands
			ConnectedAt: now,
		}
		// ...
	}
```

(b) Ticker re-registration (around line 260–265):

```go
			for i := 0; i < p.cfg.WorkerCount; i++ {
				info := queue.WorkerInfo{
					WorkerID:    fmt.Sprintf("%s/worker-%d", p.cfg.Hostname, i),
					Name:        p.cfg.Hostname,
					Nickname:    p.nicknameForIndex(i),
					Slots:       1, // see initial-registration comment above
					ConnectedAt: now,
				}
				p.cfg.Queue.Register(ctx, info)
			}
```

- [ ] **Step 2: Verify worker compiles & tests pass**

Run: `cd worker && go test ./...`
Expected: all PASS.

- [ ] **Step 3: Commit**

```bash
git add worker/pool/pool.go
git commit -m "$(cat <<'EOF'
feat(worker): declare Slots: 1 in heartbeat WorkerInfo

Hardcoded — matches today's single-job-per-worker pool. When the pool
gains concurrent execution, this lifts to worker.yaml config.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 7 — App Wiring + Config

### Task 14: Add `AvailabilityConfig` to `app/config`

**Files:**
- Modify: `app/config/config.go`
- Modify: `app/config/defaults.go`

- [ ] **Step 1: Add `AvailabilityConfig` type + `Availability` field on `Config`**

In `app/config/config.go`:

(a) Append the new type after the `QueueConfig` declaration (around line 132):

```go
type AvailabilityConfig struct {
	AvgJobDuration time.Duration `yaml:"avg_job_duration"`
}
```

(b) Add the field to the top-level `Config` struct (after `Queue QueueConfig`, around line 27):

```go
type Config struct {
	// ... existing fields ...
	Queue        QueueConfig        `yaml:"queue"`
	Availability AvailabilityConfig `yaml:"availability"` // NEW
	Logging      LoggingConfig      `yaml:"logging"`
	// ... rest ...
}
```

- [ ] **Step 2: Apply default in `defaults.go`**

In `app/config/defaults.go`, add inside `ApplyDefaults` (e.g. after the queue defaults block, around line 61):

```go
	if cfg.Availability.AvgJobDuration <= 0 {
		cfg.Availability.AvgJobDuration = 3 * time.Minute
	}
```

- [ ] **Step 3: Verify config tests pass**

Run: `cd app && go test ./config/ -v`
Expected: all PASS. `DefaultsMap` round-trips the new field.

- [ ] **Step 4: Commit**

```bash
git add app/config/config.go app/config/defaults.go
git commit -m "$(cat <<'EOF'
feat(app/config): add availability.avg_job_duration (default 3m)

Optional field; powers ETA calculation in the worker availability
service. Absent → 3m default applied via ApplyDefaults.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 15: Wire availability into `app/app.go`

**Files:**
- Modify: `app/app.go:135-184` (around coordinator construction and `metrics.Register`)

- [ ] **Step 1: Construct availability and pass to `NewWorkflow`**

In `app/app.go`, immediately after the `coordinator` is constructed and `RegisterQueue` is called (around line 140), but before the `wf := bot.NewWorkflow(...)` call (line 142), insert:

```go
	availability := queue.NewWorkerAvailability(coordinator, jobStore, queue.AvailabilityConfig{
		AvgJobDuration: cfg.Availability.AvgJobDuration,
	},
		queue.WithVerdictHook(func(kind, stage string, d time.Duration) {
			metrics.WorkerAvailabilityVerdictTotal.WithLabelValues(kind, stage).Inc()
			metrics.WorkerAvailabilityCheckDuration.Observe(d.Seconds())
		}),
		queue.WithDepErrorHook(func(dep string) {
			metrics.WorkerAvailabilityCheckErrors.WithLabelValues(dep).Inc()
		}),
	)
```

Then update the `NewWorkflow` call (line 142) — replace the trailing `nil` (added in Task 9) with `availability`:

```go
	wf := bot.NewWorkflow(cfg, slackClient, repoCache, repoDiscovery, coordinator, jobStore, bundle.Attachments, bundle.Results, skillLoader, identity, availability)
```

- [ ] **Step 2: Verify the whole tree compiles**

Run: `go build ./...`
Expected: success.

- [ ] **Step 3: Run all tests (regression)**

Run: `go test ./...`
Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add app/app.go
git commit -m "$(cat <<'EOF'
feat(app): construct WorkerAvailability and inject into Workflow

Wires the verdict + dep-error hooks into Prometheus. Behaviour is
now end-to-end: trigger soft warn + submit hard check both active.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 8 — Final Verification

### Task 16: Full build + import direction + test suite

**Files:** none (verification only)

- [ ] **Step 1: Build everything**

Run: `go build ./...`
Expected: success across all three modules (app, worker, shared, root).

- [ ] **Step 2: Run the full test suite**

Run: `go test ./...`
Expected: all PASS, including:
- `shared/queue/...` (new availability tests)
- `app/bot/...` (new workflow integration tests)
- `app/config/...` (config defaults round-trip)
- `worker/pool/...` (existing; Slots field is additive)
- `test/import_direction_test.go` (no new cross-module imports introduced)

- [ ] **Step 3: Verify import direction explicitly**

Run: `go test ./test/ -run TestImportDirection -v`
Expected: PASS. The new code respects boundaries:
- `shared/queue` does NOT import `shared/metrics` (uses functional-option hooks).
- `app/bot/verdict_message.go` imports `shared/queue` (allowed).
- `app/app.go` imports `shared/metrics` and `shared/queue` (allowed).

- [ ] **Step 4: Confirm no committed `nil` placeholder remains**

Run: `grep -n "NewWorkflow.*nil" app/app.go`
Expected: no matches (Task 15 replaced the `nil` from Task 9).

- [ ] **Step 5: Manually exercise (optional, off-CI)**

Optionally bring up Redis + start an `agentdock app` with no workers and confirm:
- `@bot` in a thread → soft warn appears, repo selector still appears.
- After completing selection → hard reject appears, `:mag:` lifecycle message does NOT.
- `agentdock worker` started → next `@bot` works normally with no warn.

This is documentation of the manual smoke; the integration tests are the gate.

- [ ] **Step 6: No commit needed**

If all of the above pass, the implementation is complete. The plan was committed-as-you-go; no final commit step.

---

## Spec Coverage Self-Review

Cross-referenced against `docs/superpowers/specs/2026-04-22-worker-liveness-precheck-design.md`:

| Spec Section | Implementing Task(s) |
|---|---|
| §1 Data Model — `WorkerInfo.Slots` | Task 1 |
| §2 Availability Service — types, interface, compute | Tasks 2–4 |
| §2 Slots normalisation | Task 5 (locked by test) |
| §2 Fail-open | Task 6 |
| §3.1 NewWorkflow constructor change | Task 9 |
| §3.2 Trigger-time soft warn | Task 12 |
| §3.3 Submit-time hard check (NoWorkers) | Task 10 |
| §3.3 Submit-time hard check (BusyEnqueueOK + ETA in lifecycle) | Task 11 |
| §3 `pendingTriage.busyHint` field | Task 9 |
| §4 Verdict rendering | Task 8 |
| §5 Wiring + config | Tasks 14, 15 |
| §6 Worker Slots: 1 | Task 13 |
| §7 Metrics (verdict, duration, dep errors) | Tasks 7, 15 |
| §8 Error Handling Summary | Tasks 6 (deps), 10 (Slack reject), 12 (Slack warn) |
| §9 Ordering Constraints — soft AFTER channel-guard | Task 12 (insertion location specified) |
| §9 Ordering — hard BEFORE first lifecycle post | Task 10 (insertion at line 387–392 boundary) |
| §9 Ordering — hard reject calls clearDedup | Task 10 (assertion implicit; impl explicit) |
| Testing — unit matrix (9 cases) | Tasks 2–6 cover all 9 rows |
| Testing — 3 integration cases | Tasks 10, 11, 12 |
| Migration / Rollout | Additive changes in Tasks 1, 14 (omitempty + optional config) |
