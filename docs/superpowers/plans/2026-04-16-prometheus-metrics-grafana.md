# Prometheus Metrics & Grafana Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add 23 Prometheus metrics to AgentDock (app pod only), expose `/metrics` endpoint, and deliver a Grafana dashboard as ConfigMap.

**Architecture:** Centralized `internal/metrics/` package defines all metrics. App-side code imports and records. Workers stay untouched (zero prometheus dependency). GaugeFunc for state gauges (queue depth, worker active/idle). All duration calculations use app pod's clock to avoid clock skew with remote workers.

**Tech Stack:** `github.com/prometheus/client_golang` (prometheus, promhttp, promauto)

---

### Task 1: Add prometheus dependency and create metrics package

**Files:**
- Modify: `go.mod`
- Create: `internal/metrics/metrics.go`
- Create: `internal/metrics/metrics_test.go`

- [ ] **Step 1: Add prometheus client_golang**

Run:
```bash
cd /Users/ivantseng/local_file/slack-issue-bot
go get github.com/prometheus/client_golang/prometheus
go get github.com/prometheus/client_golang/prometheus/promhttp
```

- [ ] **Step 2: Write metrics_test.go — verify Register doesn't panic**

```go
// internal/metrics/metrics_test.go
package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// stubQueue implements the two methods Register needs from JobQueue.
type stubQueue struct{ depth int }

func (s *stubQueue) QueueDepth() int                                                { return s.depth }
func (s *stubQueue) ListWorkers(ctx context.Context) ([]WorkerInfo, error)          { return nil, nil }

// stubStore implements the one method Register needs from JobStore.
type stubStore struct{}

func (s *stubStore) ListAll() ([]JobState, error) { return nil, nil }

func TestRegister_NoPanic(t *testing.T) {
	reg := prometheus.NewRegistry()
	Register(reg, &stubQueue{depth: 3}, &stubStore{})
	// If we get here without panic, metrics registered fine.
}

func TestRegister_Idempotent(t *testing.T) {
	reg := prometheus.NewRegistry()
	Register(reg, &stubQueue{}, &stubStore{})
	// Second call should not panic (re-register same metrics).
	Register(reg, &stubQueue{}, &stubStore{})
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/metrics/ -v -run TestRegister`
Expected: FAIL — package doesn't exist yet.

- [ ] **Step 4: Write metrics.go with all 23 metric definitions**

```go
// internal/metrics/metrics.go
package metrics

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"
)

// Interfaces — minimal slices of queue.JobQueue and queue.JobStore
// to avoid importing the queue package (which would create a cycle
// if queue ever imports metrics).

type QueueDepther interface {
	QueueDepth() int
	ListWorkers(ctx context.Context) ([]WorkerInfo, error)
}

type WorkerInfo struct {
	WorkerID    string
	Name        string
	ConnectedAt any
}

type JobState struct {
	Status string
}

type JobLister interface {
	ListAll() ([]JobState, error)
}

// --- Request Pipeline ---

var RequestTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "agentdock",
	Subsystem: "request",
	Name:      "total",
	Help:      "Incoming triage requests by status.",
}, []string{"status"})

var RequestDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
	Namespace: "agentdock",
	Subsystem: "request",
	Name:      "duration_seconds",
	Help:      "End-to-end system processing time from trigger to result.",
	Buckets:   []float64{30, 60, 120, 300, 600, 900, 1200},
})

// --- Queue ---

// QueueDepth is registered as GaugeFunc inside Register().
// No exported var needed.

var QueueSubmitted = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "agentdock",
	Subsystem: "queue",
	Name:      "submitted_total",
	Help:      "Jobs submitted to queue by priority bucket.",
}, []string{"priority"})

var QueueWait = prometheus.NewHistogram(prometheus.HistogramOpts{
	Namespace: "agentdock",
	Subsystem: "queue",
	Name:      "wait_seconds",
	Help:      "Time from submission to worker pickup.",
	Buckets:   []float64{1, 5, 10, 30, 60, 120, 300},
})

var QueueJobDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Namespace: "agentdock",
	Subsystem: "queue",
	Name:      "job_duration_seconds",
	Help:      "Total job lifecycle time.",
	Buckets:   []float64{30, 60, 120, 300, 600, 900, 1200},
}, []string{"status"})

// --- Agent ---

var AgentExecution = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Namespace: "agentdock",
	Subsystem: "agent",
	Name:      "execution_seconds",
	Help:      "Agent process runtime excluding repo prepare.",
	Buckets:   []float64{30, 60, 120, 300, 600, 900},
}, []string{"provider"})

var AgentExecutions = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "agentdock",
	Subsystem: "agent",
	Name:      "executions_total",
	Help:      "Agent execution outcomes.",
}, []string{"provider", "status"})

var AgentPrepare = prometheus.NewHistogram(prometheus.HistogramOpts{
	Namespace: "agentdock",
	Subsystem: "agent",
	Name:      "prepare_seconds",
	Help:      "Time spent cloning/fetching repo.",
	Buckets:   []float64{1, 5, 10, 30, 60, 120},
})

var AgentToolCalls = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Namespace: "agentdock",
	Subsystem: "agent",
	Name:      "tool_calls",
	Help:      "Tool calls per agent execution.",
	Buckets:   prometheus.LinearBuckets(0, 10, 20), // 0,10,20,...,190
}, []string{"provider"})

var AgentFilesRead = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Namespace: "agentdock",
	Subsystem: "agent",
	Name:      "files_read",
	Help:      "Files read per agent execution.",
	Buckets:   prometheus.LinearBuckets(0, 5, 20), // 0,5,10,...,95
}, []string{"provider"})

var AgentCostUSD = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "agentdock",
	Subsystem: "agent",
	Name:      "cost_usd",
	Help:      "Cumulative LLM spend in USD.",
}, []string{"provider"})

var AgentTokens = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "agentdock",
	Subsystem: "agent",
	Name:      "tokens_total",
	Help:      "Cumulative token usage.",
}, []string{"provider", "type"})

// --- Issue ---

var IssueCreated = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "agentdock",
	Subsystem: "issue",
	Name:      "created_total",
	Help:      "GitHub issues created.",
}, []string{"confidence", "degraded"})

var IssueRejected = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "agentdock",
	Subsystem: "issue",
	Name:      "rejected_total",
	Help:      "Rejected triages.",
}, []string{"reason"})

var IssueRetry = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "agentdock",
	Subsystem: "issue",
	Name:      "retry_total",
	Help:      "Retry attempts.",
}, []string{"status"})

// --- Handler ---

var HandlerDedup = prometheus.NewCounter(prometheus.CounterOpts{
	Namespace: "agentdock",
	Subsystem: "handler",
	Name:      "dedup_rejections_total",
	Help:      "Thread-level dedup rejections.",
})

var HandlerRateLimit = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "agentdock",
	Subsystem: "handler",
	Name:      "rate_limit_total",
	Help:      "Rate limit rejections.",
}, []string{"type"})

// --- Watchdog ---

var WatchdogKills = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "agentdock",
	Subsystem: "watchdog",
	Name:      "kills_total",
	Help:      "Watchdog-initiated kills.",
}, []string{"reason"})

// --- External ---

var ExternalDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Namespace: "agentdock",
	Subsystem: "external",
	Name:      "duration_seconds",
	Help:      "External API latency.",
	Buckets:   []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10},
}, []string{"service", "operation"})

var ExternalErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "agentdock",
	Subsystem: "external",
	Name:      "errors_total",
	Help:      "External API errors.",
}, []string{"service", "operation"})

// Register registers all metrics with the given registry.
// Pass prometheus.DefaultRegisterer for production use.
// queue and store are used for GaugeFunc (computed on scrape).
func Register(reg prometheus.Registerer, q QueueDepther, store JobLister) {
	// Counters and Histograms.
	for _, c := range []prometheus.Collector{
		RequestTotal, RequestDuration,
		QueueSubmitted, QueueWait, QueueJobDuration,
		AgentExecution, AgentExecutions, AgentPrepare,
		AgentToolCalls, AgentFilesRead, AgentCostUSD, AgentTokens,
		IssueCreated, IssueRejected, IssueRetry,
		HandlerDedup, HandlerRateLimit,
		WatchdogKills,
		ExternalDuration, ExternalErrors,
	} {
		reg.MustRegister(c)
	}

	// GaugeFunc — computed on every Prometheus scrape.
	reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: "agentdock",
		Subsystem: "queue",
		Name:      "depth",
		Help:      "Current pending job count.",
	}, func() float64 {
		return float64(q.QueueDepth())
	}))

	reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: "agentdock",
		Subsystem: "worker",
		Name:      "active",
		Help:      "Busy workers.",
	}, func() float64 {
		all, err := store.ListAll()
		if err != nil {
			return 0
		}
		var count float64
		for _, s := range all {
			if s.Status == "running" {
				count++
			}
		}
		return count
	}))

	reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: "agentdock",
		Subsystem: "worker",
		Name:      "idle",
		Help:      "Idle workers.",
	}, func() float64 {
		workers, err := q.ListWorkers(context.Background())
		if err != nil {
			return 0
		}
		all, err := store.ListAll()
		if err != nil {
			return float64(len(workers))
		}
		var running int
		for _, s := range all {
			if s.Status == "running" {
				running++
			}
		}
		idle := len(workers) - running
		if idle < 0 {
			return 0
		}
		return float64(idle)
	}))
}
```

**Important:** The interfaces `QueueDepther`, `JobLister`, `WorkerInfo`, `JobState` are defined locally to avoid import cycles. The `queue` package types satisfy these interfaces structurally (Go duck typing).

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/metrics/ -v -run TestRegister`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/metrics/ go.mod go.sum
git commit -m "feat: add internal/metrics package with 23 Prometheus metric definitions"
```

---

### Task 2: Add PrepareSeconds to StatusReport

**Files:**
- Modify: `internal/queue/interface.go:43-57`
- Modify: `internal/worker/executor.go:52-58`
- Modify: `internal/worker/status.go:57-74`

- [ ] **Step 1: Add PrepareSeconds field to StatusReport**

In `internal/queue/interface.go`, add after line 56 (`OutputTokens`):

```go
	PrepareSeconds float64   `json:"prepare_seconds,omitempty"`
```

- [ ] **Step 2: Add prepareSeconds to statusAccumulator**

In `internal/worker/status.go`, add field after `outputTokens` (line 24):

```go
	prepareSeconds float64
```

Add a setter method after `recordEvent`:

```go
func (s *statusAccumulator) setPrepareSeconds(d float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prepareSeconds = d
}
```

In `toReport()`, add to the returned struct:

```go
		PrepareSeconds: s.prepareSeconds,
```

- [ ] **Step 3: Compute PrepareSeconds in executor.go**

In `internal/worker/executor.go`, wrap the `deps.repoCache.Prepare()` call (lines 53-58) with timing:

Replace:
```go
	logger.Info("準備 repo 中", "phase", "處理中", "branch", job.Branch)
	repoPath, err := deps.repoCache.Prepare(job.CloneURL, job.Branch)
	if err != nil {
		return failedResult(job, startedAt, fmt.Errorf("repo prepare failed: %w", err), "")
	}
	logger.Info("Repo 已就緒", "phase", "處理中", "path", repoPath)
```

With:
```go
	logger.Info("準備 repo 中", "phase", "處理中", "branch", job.Branch)
	prepareStart := time.Now()
	repoPath, err := deps.repoCache.Prepare(job.CloneURL, job.Branch)
	if err != nil {
		return failedResult(job, startedAt, fmt.Errorf("repo prepare failed: %w", err), "")
	}
	prepareSeconds := time.Since(prepareStart).Seconds()
	logger.Info("Repo 已就緒", "phase", "處理中", "path", repoPath, "prepare_seconds", prepareSeconds)
```

The `executeJob` function receives `opts bot.RunOptions` — the `prepareSeconds` value needs to reach the `statusAccumulator` in `pool.go`. Since `executeJob` doesn't own the accumulator, return prepareSeconds on the `JobResult`.

Actually, simpler approach: add `PrepareSeconds` to `JobResult` as a local-only field:

In `internal/queue/job.go`, add after `RepoPath` (line 67):

```go
	PrepareSeconds float64 `json:"-"` // local only, not serialized over Redis
```

Then in `executor.go`, set it on the successful result:

```go
	return &queue.JobResult{
		// ... existing fields ...
		RepoPath:       repoPath,
		PrepareSeconds: prepareSeconds,
		// ...
	}
```

And in the `pool.go` `executeWithTracking`, after `executeJob` returns, set it on the status accumulator:

```go
	result := executeJob(jobCtx, job, deps, opts, logger)
	status.setPrepareSeconds(result.PrepareSeconds)
```

This way the final StatusReport sent to app includes PrepareSeconds.

- [ ] **Step 4: Run existing tests**

Run: `go test ./internal/queue/ ./internal/worker/ -v`
Expected: PASS — field additions are backwards-compatible.

- [ ] **Step 5: Commit**

```bash
git add internal/queue/interface.go internal/queue/job.go internal/worker/executor.go internal/worker/status.go
git commit -m "feat: add PrepareSeconds to StatusReport and JobResult"
```

---

### Task 3: Wire /metrics endpoint in app.go

**Files:**
- Modify: `cmd/agentdock/app.go:221-232`

- [ ] **Step 1: Add imports and Register call**

In `cmd/agentdock/app.go`, add to imports:

```go
	"agentdock/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
```

Before the HTTP server block (before line 221 `if cfg.Server.Port > 0`), add:

```go
	metrics.Register(prometheus.DefaultRegisterer, coordinator, jobStore)
```

- [ ] **Step 2: Add /metrics handler**

Inside the HTTP server goroutine, after the `/jobs/` handler (line 228), add:

```go
			http.Handle("/metrics", promhttp.Handler())
```

Update the log line to include `/metrics`:

```go
			appLogger.Info("HTTP 端點已啟動", "phase", "處理中", "addr", addr, "endpoints", []string{"/healthz", "/jobs", "/jobs/{id}", "/metrics"})
```

- [ ] **Step 3: Verify compilation**

Run: `go build ./cmd/agentdock/`
Expected: builds without errors.

- [ ] **Step 4: Commit**

```bash
git add cmd/agentdock/app.go
git commit -m "feat: wire /metrics endpoint with promhttp.Handler()"
```

---

### Task 4: Instrument handler.go (request_total, dedup, rate_limit)

**Files:**
- Modify: `internal/slack/handler.go:49-67`

- [ ] **Step 1: Add metrics import**

In `internal/slack/handler.go`, add to imports:

```go
	"agentdock/internal/metrics"
```

- [ ] **Step 2: Instrument HandleTrigger**

Replace the `HandleTrigger` method (lines 49-67):

```go
func (h *Handler) HandleTrigger(event TriggerEvent) bool {
	if h.threadDedup.isDuplicate(event.ChannelID, event.ThreadTS) {
		metrics.RequestTotal.WithLabelValues("dedup").Inc()
		metrics.HandlerDedup.Inc()
		return false
	}
	if !h.userLimit.allow(event.UserID) {
		metrics.RequestTotal.WithLabelValues("rate_limited").Inc()
		metrics.HandlerRateLimit.WithLabelValues("user").Inc()
		if h.onRejected != nil {
			h.onRejected(event, "rate limit exceeded")
		}
		return false
	}
	if !h.channelLimit.allow(event.ChannelID) {
		metrics.RequestTotal.WithLabelValues("rate_limited").Inc()
		metrics.HandlerRateLimit.WithLabelValues("channel").Inc()
		if h.onRejected != nil {
			h.onRejected(event, "channel rate limit exceeded")
		}
		return false
	}
	metrics.RequestTotal.WithLabelValues("accepted").Inc()
	go h.onEvent(event)
	return true
}
```

- [ ] **Step 3: Run handler tests**

Run: `go test ./internal/slack/ -v -run TestHandler`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/slack/handler.go
git commit -m "feat: instrument handler with request_total, dedup, rate_limit metrics"
```

---

### Task 5: Instrument result_listener.go (bulk of metrics)

**Files:**
- Modify: `internal/bot/result_listener.go`

This is the biggest instrumentation point: request_duration, queue_wait, job_duration, agent metrics, issue metrics.

- [ ] **Step 1: Add metrics import**

```go
	"agentdock/internal/metrics"
	"strconv"
	"time"
```

(`time` is already imported via `queue` types but add explicitly if needed.)

- [ ] **Step 2: Add metrics recording in handleResult**

After line 88 (`r.processedJobs[result.JobID] = true`) and after getting `state` (line 90), add a helper call. Insert before the `switch` block (before line 110):

```go
	// Record metrics for completed jobs.
	r.recordMetrics(state, result)
```

- [ ] **Step 3: Add recordMetrics method**

After the `handleResult` method, add:

```go
func (r *ResultListener) recordMetrics(state *queue.JobState, result *queue.JobResult) {
	job := state.Job

	// End-to-end duration (app clock only, no clock skew).
	if !job.SubmittedAt.IsZero() {
		elapsed := time.Since(job.SubmittedAt).Seconds()
		metrics.RequestDuration.Observe(elapsed)
		metrics.QueueJobDuration.WithLabelValues(result.Status).Observe(elapsed)
	}

	// Queue wait time (already computed by MemJobStore on status → Running).
	if state.WaitTime > 0 {
		metrics.QueueWait.Observe(state.WaitTime.Seconds())
	}

	// Agent metrics from StatusReport (set by StatusListener from worker's StatusBus).
	if as := state.AgentStatus; as != nil {
		provider := as.AgentCmd
		if provider == "" {
			provider = "unknown"
		}

		// Prepare seconds.
		if as.PrepareSeconds > 0 {
			metrics.AgentPrepare.Observe(as.PrepareSeconds)
		}

		// Execution time = total job time - wait - prepare.
		if !job.SubmittedAt.IsZero() {
			total := time.Since(job.SubmittedAt).Seconds()
			exec := total - state.WaitTime.Seconds() - as.PrepareSeconds
			if exec > 0 {
				metrics.AgentExecution.WithLabelValues(provider).Observe(exec)
			}
		}

		// Execution outcome.
		status := "success"
		if result.Status == "failed" {
			if strings.Contains(result.Error, "timeout") {
				status = "timeout"
			} else {
				status = "error"
			}
		}
		metrics.AgentExecutions.WithLabelValues(provider, status).Inc()

		// Tool calls and files read.
		if as.ToolCalls > 0 {
			metrics.AgentToolCalls.WithLabelValues(provider).Observe(float64(as.ToolCalls))
		}
		if as.FilesRead > 0 {
			metrics.AgentFilesRead.WithLabelValues(provider).Observe(float64(as.FilesRead))
		}

		// Cost and tokens.
		if as.CostUSD > 0 {
			metrics.AgentCostUSD.WithLabelValues(provider).Add(as.CostUSD)
		}
		if as.InputTokens > 0 {
			metrics.AgentTokens.WithLabelValues(provider, "input").Add(float64(as.InputTokens))
		}
		if as.OutputTokens > 0 {
			metrics.AgentTokens.WithLabelValues(provider, "output").Add(float64(as.OutputTokens))
		}
	} else if result.Status == "failed" {
		// No agent status — job failed before agent started (e.g. prepare timeout).
		metrics.AgentExecutions.WithLabelValues("unknown", "error").Inc()
	}
}
```

- [ ] **Step 4: Instrument issue created/rejected in handleResult switch**

In the `switch` block (lines 110-125), add metrics:

```go
	switch {
	case result.Status == "failed":
		r.handleFailure(job, state, result)

	case result.Confidence == "low":
		metrics.IssueRejected.WithLabelValues("low_confidence").Inc()
		r.updateStatus(job, ":warning: 判斷不屬於此 repo，已跳過")
		r.clearDedup(job)

	case result.FilesFound == 0 || result.Questions >= 5:
		r.createAndPostIssue(ctx, job, owner, repo, result, true)
		r.clearDedup(job)

	default:
		r.createAndPostIssue(ctx, job, owner, repo, result, false)
		r.clearDedup(job)
	}
```

- [ ] **Step 5: Instrument createAndPostIssue**

In `createAndPostIssue` (line 170), after the successful `CreateIssue` call (after line 187), add:

```go
	confidence := result.Confidence
	if confidence == "" {
		confidence = "unknown"
	}
	metrics.IssueCreated.WithLabelValues(confidence, strconv.FormatBool(degraded)).Inc()
```

When GitHub client is nil (line 171-174), add:

```go
	if r.github == nil {
		metrics.IssueRejected.WithLabelValues("no_github").Inc()
		r.slack.PostMessage(job.ChannelID,
			":warning: GitHub client not configured", job.ThreadTS)
		return
	}
```

- [ ] **Step 6: Run result_listener tests**

Run: `go test ./internal/bot/ -v -run TestResult`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/bot/result_listener.go
git commit -m "feat: instrument result_listener with request_duration, agent, issue metrics"
```

---

### Task 6: Instrument retry_handler.go, coordinator.go, watchdog.go

**Files:**
- Modify: `internal/bot/retry_handler.go:28-88`
- Modify: `internal/queue/coordinator.go:30-37`
- Modify: `internal/queue/watchdog.go:102-118`

- [ ] **Step 1: Instrument retry_handler.go**

Add import:
```go
	"agentdock/internal/metrics"
```

In `Handle` method, after successful `h.queue.Submit` (after line 69), add:
```go
	metrics.IssueRetry.WithLabelValues("submitted").Inc()
```

When job is not found or not failed (lines 31-38), these are non-retriable — no metric needed.

In `handleFailure` in `result_listener.go`, when `job.RetryCount >= 1` (retry exhausted, line 162), add:
```go
	metrics.IssueRetry.WithLabelValues("exhausted").Inc()
```

- [ ] **Step 2: Instrument coordinator.go**

Add import:
```go
	"agentdock/internal/metrics"
	"strconv"
```

In `Submit` method (line 30), add at the top before routing:
```go
func (c *Coordinator) Submit(ctx context.Context, job *Job) error {
	metrics.QueueSubmitted.WithLabelValues(strconv.Itoa(job.Priority)).Inc()
	if job.TaskType != "" {
		if q, ok := c.queues[job.TaskType]; ok {
			return q.Submit(ctx, job)
		}
	}
	return c.fallback.Submit(ctx, job)
}
```

- [ ] **Step 3: Instrument watchdog.go**

Add import:
```go
	"agentdock/internal/metrics"
```

In `killAndPublish` (line 102), add at the top:
```go
func (w *Watchdog) killAndPublish(state *JobState, reason string) {
	metrics.WatchdogKills.WithLabelValues(reason).Inc()
	// ... rest of existing code
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/bot/ ./internal/queue/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/bot/retry_handler.go internal/bot/result_listener.go internal/queue/coordinator.go internal/queue/watchdog.go
git commit -m "feat: instrument retry, queue_submitted, watchdog_kills metrics"
```

---

### Task 7: Instrument external API calls (slack/client.go, github/)

**Files:**
- Modify: `internal/slack/client.go`
- Modify: `internal/github/issue.go`
- Modify: `internal/github/discovery.go`

- [ ] **Step 1: Instrument slack/client.go — FetchMessage**

Add import:
```go
	"agentdock/internal/metrics"
	"time"
```

(`time` is already imported — verify.)

Wrap `FetchMessage` (line 53) with timing. Add at the start of the method:
```go
func (c *Client) FetchMessage(channelID, messageTS string) (FetchedMessage, error) {
	start := time.Now()
	defer func() {
		metrics.ExternalDuration.WithLabelValues("slack", "fetch_message").Observe(time.Since(start).Seconds())
	}()
	// ... rest of existing code
```

- [ ] **Step 2: Instrument slack/client.go — PostMessage**

Wrap `PostMessage` (line 186):
```go
func (c *Client) PostMessage(channelID, text, threadTS string) error {
	start := time.Now()
	opts := []slack.MsgOption{slack.MsgOptionText(text, false)}
	if threadTS != "" {
		opts = append(opts, slack.MsgOptionTS(threadTS))
	}
	_, _, err := c.api.PostMessage(channelID, opts...)
	metrics.ExternalDuration.WithLabelValues("slack", "post_message").Observe(time.Since(start).Seconds())
	if err != nil {
		metrics.ExternalErrors.WithLabelValues("slack", "post_message").Inc()
		return fmt.Errorf("post message: %w", err)
	}
	return nil
}
```

Also wrap `UpdateMessage` (line 327) and `PostMessageWithButton` (line 305) similarly — all use `"slack", "post_message"` label.

For `UpdateMessage`:
```go
func (c *Client) UpdateMessage(channelID, messageTS, text string) error {
	start := time.Now()
	_, _, _, err := c.api.UpdateMessage(channelID, messageTS,
		slack.MsgOptionText(text, false),
	)
	metrics.ExternalDuration.WithLabelValues("slack", "post_message").Observe(time.Since(start).Seconds())
	if err != nil {
		metrics.ExternalErrors.WithLabelValues("slack", "post_message").Inc()
		return fmt.Errorf("update message: %w", err)
	}
	return nil
}
```

For `PostMessageWithButton`:
```go
func (c *Client) PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value string) (string, error) {
	start := time.Now()
	// ... existing block/opts code ...
	_, ts, err := c.api.PostMessage(channelID, opts...)
	metrics.ExternalDuration.WithLabelValues("slack", "post_message").Observe(time.Since(start).Seconds())
	if err != nil {
		metrics.ExternalErrors.WithLabelValues("slack", "post_message").Inc()
		return "", fmt.Errorf("post message with button: %w", err)
	}
	return ts, nil
}
```

- [ ] **Step 3: Instrument github/issue.go — CreateIssue**

Add import:
```go
	"agentdock/internal/metrics"
```

The method already has `start := time.Now()` (line 29). Add after the API call (after line 36):
```go
	issue, _, err := ic.client.Issues.Create(ctx, owner, repo, req)
	metrics.ExternalDuration.WithLabelValues("github", "create_issue").Observe(time.Since(start).Seconds())
	if err != nil {
		metrics.ExternalErrors.WithLabelValues("github", "create_issue").Inc()
		ic.logger.Error("Issue 建立失敗", "phase", "失敗", "owner", owner, "repo", repo, "error", err)
		return "", fmt.Errorf("create issue: %w", err)
	}
```

- [ ] **Step 4: Instrument github/discovery.go — ListRepos**

Add import:
```go
	"agentdock/internal/metrics"
```

In `ListRepos`, add timing around the GitHub API loop. After the cache check returns (after line 39), add:
```go
	start := time.Now()
```

After the loop completes (before line 62 `d.logger.Info`), add:
```go
	metrics.ExternalDuration.WithLabelValues("github", "list_repos").Observe(time.Since(start).Seconds())
```

If any error in the loop, add before return:
```go
		if err != nil {
			metrics.ExternalErrors.WithLabelValues("github", "list_repos").Inc()
			return nil, err
		}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/slack/ ./internal/github/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/slack/client.go internal/github/issue.go internal/github/discovery.go
git commit -m "feat: instrument Slack and GitHub API calls with external_duration metrics"
```

---

### Task 8: Add Prometheus annotations to deployment.yaml

**Files:**
- Modify: `deploy/base/deployment.yaml`

- [ ] **Step 1: Add annotations**

In `deploy/base/deployment.yaml`, add under `spec.template` (after line 9 `spec:`), a `metadata` section with annotations:

```yaml
  template:
    metadata:
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "8080"
        prometheus.io/path: "/metrics"
    spec:
```

- [ ] **Step 2: Commit**

```bash
git add deploy/base/deployment.yaml
git commit -m "feat: add Prometheus scrape annotations to deployment"
```

---

### Task 9: Create ServiceMonitor

**Files:**
- Create: `deploy/metrics/servicemonitor.yaml`

- [ ] **Step 1: Create file**

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: agentdock
  labels:
    app: agentdock
spec:
  selector:
    matchLabels:
      app: agentdock
  endpoints:
    - port: http
      path: /metrics
      interval: 30s
```

- [ ] **Step 2: Commit**

```bash
git add deploy/metrics/servicemonitor.yaml
git commit -m "feat: add ServiceMonitor for Prometheus Operator"
```

---

### Task 10: Create Grafana dashboard JSON

**Files:**
- Create: `deploy/grafana/agentdock-dashboard.json`

- [ ] **Step 1: Create the dashboard JSON**

This is a large JSON file. Create `deploy/grafana/agentdock-dashboard.json` with a complete Grafana dashboard following the spec's 6-row layout:

- Row 1: Overview (6 Stat panels)
- Row 2: Request Pipeline (3 panels)
- Row 3: Queue & Workers (4 panels)
- Row 4: Agent Performance (6 panels)
- Row 5: Issue Output (4 panels)
- Row 6: External Dependencies (4 panels)

Template variables: `provider` (multi-select, label_values from `agentdock_agent_executions_total`).

All time series use `$__rate_interval`. Dark theme. Latency panels show P50/P95/P99 with green/yellow/red coloring.

Use the exact PromQL queries from the spec's dashboard section.

- [ ] **Step 2: Commit**

```bash
git add deploy/grafana/agentdock-dashboard.json
git commit -m "feat: add Grafana dashboard JSON (6 rows, 27 panels)"
```

---

### Task 11: Create Grafana dashboard ConfigMap

**Files:**
- Create: `deploy/grafana/dashboard-configmap.yaml`

- [ ] **Step 1: Create ConfigMap**

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: agentdock-grafana-dashboard
  labels:
    grafana_dashboard: "1"
data:
  agentdock.json: |
    # Inline the content of agentdock-dashboard.json here,
    # or reference it via kustomize configMapGenerator.
```

In practice, use kustomize's `configMapGenerator` to avoid duplicating the JSON. Create a simpler ConfigMap that references the file:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: agentdock-grafana-dashboard
  labels:
    grafana_dashboard: "1"
```

And add to `deploy/grafana/kustomization.yaml`:

```yaml
configMapGenerator:
  - name: agentdock-grafana-dashboard
    files:
      - agentdock.json=agentdock-dashboard.json
    options:
      labels:
        grafana_dashboard: "1"
```

Actually, simpler: just embed the JSON directly in the ConfigMap data field. The dashboard JSON will be the full content of `agentdock-dashboard.json`.

- [ ] **Step 2: Commit**

```bash
git add deploy/grafana/
git commit -m "feat: add Grafana dashboard ConfigMap for sidecar auto-loading"
```

---

### Task 12: Integration verification

- [ ] **Step 1: Run all tests**

Run: `go test ./... -count=1`
Expected: all tests pass.

- [ ] **Step 2: Build binary**

Run: `go build -o /dev/null ./cmd/agentdock/`
Expected: builds cleanly.

- [ ] **Step 3: Verify /metrics output (manual)**

If you can start the bot locally:
```bash
./bot app -c config.yaml &
sleep 2
curl -s http://localhost:8080/metrics | grep agentdock_
```

Expected: all 23 metric names appear with correct types (COUNTER, HISTOGRAM, GAUGE).

- [ ] **Step 4: Final commit (if any test fixes needed)**

```bash
git add -A
git commit -m "fix: test adjustments for metrics integration"
```
