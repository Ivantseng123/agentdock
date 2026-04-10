# Queue-Based App-Agent Decoupling Design

## Goal

Decouple the app → agent execution path using a producer/consumer queue architecture. The app becomes a pure Slack I/O coordinator (receive triggers, submit jobs, post results), while agent execution moves to independent workers that can run in-process, in separate pods, or on external machines.

## Requirements

- **Priority queue**: Bounded capacity queue with channel-based priority ordering
- **Producer/Consumer abstraction**: Interface designed for remote transport (fully serializable jobs), with in-memory implementation as first transport
- **Result queue**: Workers publish results back; app listens and handles all side effects (GitHub issue creation, Slack posting)
- **Attachment two-phase**: Job carries attachment metadata only; worker calls `Resolve` which blocks until app-side `Prepare` completes, avoiding storage bloat for queued jobs
- **Job observability**: Full lifecycle tracking (pending → running → completed/failed) with queue position queryable by Slack users
- **No retry**: Failures reported directly to Slack; existing agent fallback chain handles transient agent errors
- **Side effects in app only**: Workers do not create GitHub issues or post to Slack; they return structured triage results

## Architecture

```
┌─────────────────────────────────────────────────┐
│                  App Pod                         │
│                                                  │
│  Slack Event → Handler (dedup + rate limit)      │
│       ↓                                          │
│  Workflow: Slack UI → thread context → prompt    │
│       ↓                                          │
│  Producer.Submit(job)                            │
│       ↓                                          │
│  ┌──────────────────────────────────────────┐    │
│  │         Transport Layer                   │    │
│  │  ┌──────────────┐  ┌─────────────────┐   │    │
│  │  │ InMemTransport│  │ NATSTransport   │   │    │
│  │  │ (Go channels  │  │ (遠期)          │   │    │
│  │  │ + heap)       │  │                 │   │    │
│  │  └──────────────┘  └─────────────────┘   │    │
│  └──────────┬───────────────────────────────┘    │
│             │                                     │
│  ┌──────────┴──────────┐                         │
│  │ In-Pod Worker Pool  │                         │
│  └─────────────────────┘                         │
│                                                  │
│  ResultListener ← ResultBus                      │
│       ↓                                          │
│  Rejection/Degradation logic                     │
│       ↓                                          │
│  GitHub.CreateIssue() + Slack.PostMessage()       │
└─────────────────────────────────────────────────┘
         │ same JobQueue interface
    ┌────┴──────────┐
    │ External      │
    │ Workers       │
    │ (remote pods, │
    │  local dev    │
    │  machines)    │
    └───────────────┘
```

## Data Structures

### Job (fully JSON-serializable)

```go
type Job struct {
    ID          string            `json:"id"`
    Priority    int               `json:"priority"`
    Seq         uint64            `json:"seq"`             // monotonic counter for FIFO tie-breaking
    ChannelID   string            `json:"channel_id"`
    ThreadTS    string            `json:"thread_ts"`
    UserID      string            `json:"user_id"`
    Repo        string            `json:"repo"`            // "owner/repo" format (consistent with codebase)
    Branch      string            `json:"branch"`
    CloneURL    string            `json:"clone_url"`       // explicit for remote workers
    Prompt      string            `json:"prompt"`
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
```

### JobResult (worker output — no side effects)

```go
type JobResult struct {
    JobID      string    `json:"job_id"`
    Status     string    `json:"status"`         // "completed" | "failed"
    Title      string    `json:"title"`          // issue title
    Body       string    `json:"body"`           // issue markdown body
    Labels     []string  `json:"labels"`         // bug, feature, etc.
    Confidence string    `json:"confidence"`     // high | low
    FilesFound int       `json:"files_found"`
    Questions  int       `json:"open_questions"`
    RawOutput  string    `json:"raw_output"`     // full agent output for debug
    Error      string    `json:"error"`          // error message if failed
    StartedAt  time.Time `json:"started_at"`
    FinishedAt time.Time `json:"finished_at"`
}
```

### Job Lifecycle

```
Submit → Pending (queued, position queryable)
  → Ack → Preparing (downloading attachments + cloning repo)
    → Running (agent executing)
      → Completed / Failed → Cleaned up (attachments removed, dedup cleared)
```

### Delivery Guarantees

- **In-memory transport**: At-most-once delivery. A job is considered consumed when sent on the `Receive` channel. If a worker crashes mid-execution, the job is lost. This is acceptable because: (1) users are notified the job was accepted, (2) they can re-trigger, and (3) the current system already drops requests under load.
- **Future remote transport**: Should implement at-least-once with `Ack`-based redelivery. The `Ack` method exists in the interface to support this — in-memory `Ack` updates job state but does not gate redelivery.

## Interfaces

### Job Queue (transport-agnostic)

```go
type JobQueue interface {
    // Producer side (app)
    Submit(ctx context.Context, job *Job) error
    QueuePosition(jobID string) (int, error)
    QueueDepth() int

    // Consumer side (worker)
    Receive(ctx context.Context) (<-chan *Job, error)
    Ack(ctx context.Context, jobID string) error

    // Worker registration (預留 for external listeners)
    Register(ctx context.Context, info WorkerInfo) error
    Unregister(ctx context.Context, workerID string) error
    ListWorkers(ctx context.Context) ([]WorkerInfo, error)

    Close() error
}

type WorkerInfo struct {
    WorkerID    string   `json:"worker_id"`
    Name        string   `json:"name"`
    Agents      []string `json:"agents"`      // available agent CLIs
    Tags        []string `json:"tags"`        // capabilities: "gpu", "fast", etc.
    ConnectedAt time.Time
}
```

### Attachment Store (two-phase download)

The two-phase flow works as follows:
1. Worker calls `Ack(jobID)` — this triggers the transport to notify the app side
2. App side calls `Prepare(jobID, attachments)` — downloads from Slack to temp storage
3. Worker calls `Resolve(jobID)` — **blocks until `Prepare` completes**, then returns attachment locations

In-memory implementation: `Ack` sets a flag, `Prepare` is called synchronously by the `InMemTransport` Ack handler, `Resolve` waits on a `sync.WaitGroup` or channel that `Prepare` signals when done. No race condition because `Resolve` is guaranteed to block until ready.

For remote transport: `Ack` sends a message to app, app runs `Prepare` and signals readiness via a "attachments-ready" message on the result bus, worker's `Resolve` blocks on that message.

```go
type AttachmentStore interface {
    // App side: called by transport after worker Ack, downloads from Slack
    Prepare(ctx context.Context, jobID string, attachments []AttachmentMeta) error

    // Worker side: blocks until Prepare completes, returns attachment locations
    Resolve(ctx context.Context, jobID string) ([]AttachmentReady, error)

    // Cleanup after job completion
    Cleanup(ctx context.Context, jobID string) error
}

type AttachmentReady struct {
    Filename string `json:"filename"`
    URL      string `json:"url"`  // file:// (local) or http:// (remote)
}
```

### Result Bus

```go
type ResultBus interface {
    Publish(ctx context.Context, result *JobResult) error
    Subscribe(ctx context.Context) (<-chan *JobResult, error)
    Close() error
}
```

### Job Store (observability + user queries)

```go
type JobStore interface {
    Get(jobID string) (*JobState, error)
    GetByThread(channelID, threadTS string) (*JobState, error)
    ListPending() ([]*JobState, error)
    Update(jobID string, status JobStatus) error
    Delete(jobID string) error                    // remove after ResultListener processes
    ClearDedup(channelID, threadTS string)
}

type JobState struct {
    Job       *Job
    Status    string
    Position  int
    WorkerID  string
    StartedAt time.Time
    WaitTime  time.Duration
}
```

Job lifecycle in store: `Submit` creates entry → state transitions during execution → `ResultListener` processes result → calls `Delete` to remove. A background goroutine with TTL (default 1h) cleans up orphaned entries as a safety net.
```

## Priority Queue Implementation

In-memory implementation using `container/heap`:

```go
type priorityQueue []*queueEntry

type queueEntry struct {
    job   *Job
    index int
}

// Higher priority first; FIFO within same priority (using monotonic Seq)
func (pq priorityQueue) Less(i, j int) bool {
    if pq[i].job.Priority != pq[j].job.Priority {
        return pq[i].job.Priority > pq[j].job.Priority
    }
    return pq[i].job.Seq < pq[j].job.Seq
}
```

`Seq` is assigned by the `Submit` implementation using an internal `atomic.Uint64` counter — callers never set it:

```go
func (t *InMemTransport) Submit(ctx context.Context, job *Job) error {
    job.Seq = t.seqCounter.Add(1)
    // ... push to heap
}
```

Priority derived from channel config:

```yaml
channel_priority:
  C_INCIDENTS: 100
  C_ONCALL: 80
  default: 50
```

## Worker Pool

```go
type Pool struct {
    queue       JobQueue
    attachments AttachmentStore
    results     ResultBus
    agentRunner *AgentRunner
    repoCache   *RepoCache
    workerCount int
}

func (p *Pool) runWorker(ctx context.Context, id int) {
    jobs, _ := p.queue.Receive(ctx)
    for job := range jobs {
        result := p.executeJob(ctx, job)
        p.results.Publish(ctx, result)
    }
}

func (p *Pool) executeJob(ctx context.Context, job *Job) *JobResult {
    // Ack triggers app-side attachment download
    if err := p.queue.Ack(ctx, job.ID); err != nil {
        return failedResult(job, fmt.Errorf("ack failed: %w", err))
    }

    // Resolve blocks until attachments are ready
    attachments, err := p.attachments.Resolve(ctx, job.ID)
    if err != nil {
        return failedResult(job, fmt.Errorf("attachments failed: %w", err))
    }

    // Clone/fetch repo
    repoPath, err := p.repoCache.Prepare(job.CloneURL, job.Branch)
    if err != nil {
        return failedResult(job, fmt.Errorf("repo prepare failed: %w", err))
    }
    copyAttachmentsToRepo(attachments, repoPath)

    // Execute agent (uses existing fallback chain)
    output, err := p.agentRunner.Run(ctx, repoPath, job.Prompt)
    if err != nil {
        return failedResult(job, err)
    }

    return buildResult(job, output)
}
```

Workers don't need GH_TOKEN — only repo read access (clone). Agent CLI + skills are worker-local.

## Result Listener (App Side)

All side effects handled by app:

```go
func (r *ResultListener) Listen(ctx context.Context) {
    ch, _ := r.results.Subscribe(ctx)
    for result := range ch {
        job, err := r.jobStore.Get(result.JobID)
        if err != nil {
            slog.Error("job not found for result", "job_id", result.JobID)
            continue
        }

        owner, repo := splitRepo(job.Job.Repo) // "owner/repo" → "owner", "repo"

        switch {
        case result.Status == "failed":
            r.slack.PostMessage(job.Job.ChannelID, job.Job.ThreadTS, formatError(result))
        case result.Confidence == "low":
            r.slack.PostMessage(job.Job.ChannelID, job.Job.ThreadTS, "判斷不屬於此 repo，已跳過")
        case result.FilesFound == 0 || result.Questions >= 5:
            url, err := r.github.CreateIssue(owner, repo, result.Title,
                stripTriageSection(result.Body), result.Labels)
            if err != nil {
                r.slack.PostMessage(job.Job.ChannelID, job.Job.ThreadTS,
                    fmt.Sprintf("Triage 完成但建立 issue 失敗: %v", err))
            } else {
                r.slack.PostMessage(job.Job.ChannelID, job.Job.ThreadTS, url)
            }
        default:
            url, err := r.github.CreateIssue(owner, repo, result.Title,
                result.Body, result.Labels)
            if err != nil {
                r.slack.PostMessage(job.Job.ChannelID, job.Job.ThreadTS,
                    fmt.Sprintf("Triage 完成但建立 issue 失敗: %v", err))
            } else {
                r.slack.PostMessage(job.Job.ChannelID, job.Job.ThreadTS, url)
            }
        }

        // Cleanup: attachments, dedup, and job store entry
        r.attachments.Cleanup(ctx, result.JobID)
        r.jobStore.ClearDedup(job.Job.ChannelID, job.Job.ThreadTS)
        r.jobStore.Delete(result.JobID)
    }
}
```

## Flow Changes

### Handler (dedup + rate limit only, no semaphore)

Handler removes the semaphore. It still does dedup and rate limiting, then spawns the interactive workflow in a goroutine (same as today):

```go
func (h *Handler) HandleTrigger(event TriggerEvent) bool {
    if h.threadDedup.isDuplicate(...) { return false }
    if !h.userLimit.allow(...)       { return false }
    if !h.channelLimit.allow(...)    { return false }

    // No semaphore — concurrency controlled by queue + worker pool
    go h.onEvent(event)
    return true
}
```

### Workflow (interactive UI unchanged, submit replaces runTriage)

The interactive flow (repo selection → branch selection → description prompt → thread context → prompt building) stays in `workflow.go` unchanged. The **submission point** is where `runTriage` is currently called — after all interactive steps are complete and the prompt is built:

```go
func (w *Workflow) runTriage(ctx context.Context, pt *pendingTriage) {
    // Read thread context, download attachment metadata, build prompt — unchanged
    prompt := buildPrompt(pt)

    // NEW: submit to queue instead of calling agentRunner.Run directly
    job := &Job{
        ID:          pt.RequestID,
        Priority:    w.channelPriority(pt.ChannelID),
        Repo:        pt.SelectedRepo,
        Branch:      pt.SelectedBranch,
        CloneURL:    w.repoCache.ResolveURL(pt.SelectedRepo),
        Prompt:      prompt,
        ChannelID:   pt.ChannelID,
        ThreadTS:    pt.ThreadTS,
        UserID:      pt.UserID,
        RequestID:   pt.RequestID,
        Attachments: toAttachmentMeta(pt.Attachments),
        SubmittedAt: time.Now(),
    }
    err := w.queue.Submit(ctx, job)
    if err == ErrQueueFull {
        w.slack.PostMessage(..., "系統忙碌，請稍後再試")
        return
    }

    // Immediate feedback — position 1 means "next up", 0 means "already dequeued"
    pos, _ := w.queue.QueuePosition(job.ID)
    if pos <= 1 {
        w.slack.PostMessage(..., "正在處理你的請求...")
    } else {
        w.slack.PostMessage(..., fmt.Sprintf("已加入排隊，前面有 %d 個請求", pos-1))
    }
    // Function returns here — result will come back via ResultListener
}
```

### QueuePosition semantics

- `0` = job already dequeued (running or completed)
- `1` = next to be picked up
- `N` = N-1 jobs ahead in queue

User-facing message uses `pos-1` to show "前面有 X 個請求".

## Config

```yaml
queue:
  capacity: 50
  transport: inmem         # inmem | nats | redis (遠期)

channel_priority:
  C_INCIDENTS: 100
  C_ONCALL: 80
  default: 50

workers:
  count: 3

attachments:
  store: local             # local | s3 (遠期)
  temp_dir: /tmp/triage-attachments
  ttl: 30m

# Existing config retained
rate_limit:
  per_user: 5
  per_channel: 20
  window: 1m
```

## Skill and Prompt Changes

### Skill (`agents/skills/triage-issue/SKILL.md`)

The skill currently instructs the agent to create a GitHub issue via `gh issue create` and output `===TRIAGE_RESULT===CREATED:<url>`. This needs to change:

**Remove:**
- Step 6 (Create the GitHub issue) — agent no longer calls `gh issue create`
- `gh issue create` command and template

**Change Step 7 (Output result) to structured JSON output:**

```
===TRIAGE_RESULT===
{
  "status": "CREATED",
  "title": "...",
  "body": "...(full markdown issue body)...",
  "labels": ["bug"],
  "confidence": "high",
  "files_found": 5,
  "open_questions": 0
}
```

For rejection (confidence=low), keep the existing format:
```
===TRIAGE_RESULT===
{
  "status": "REJECTED",
  "message": "reason"
}
```

The rest of the skill (Steps 1-5: understand, explore, assess, fix approach, TDD plan) stays unchanged — this is the core analysis value.

### Prompt builder (`internal/bot/prompt.go`)

**Remove from prompt:**
- `RepoPath` — worker determines the local path after clone
- `github_repo` metadata line — agent doesn't need it (app handles issue creation)
- `labels` metadata line — same reason

**Keep in prompt:**
- Thread context (unchanged)
- Extra description (unchanged)
- Branch info (agent needs to know which branch to investigate)
- `channel`, `reporter` — still needed in issue body header (agent includes them in the body template)
- Attachments (unchanged, but paths are now relative to worker's clone dir)
- Language + extra rules (unchanged)

### Skill mounting for workers

All supported agent CLIs (claude, opencode, codex) read skills from the repo directory. Since the skill file is checked into the repo at `agents/skills/triage-issue/SKILL.md`, any worker that clones the repo automatically has access to it. No additional skill mounting or configuration is needed.

| Worker type | Skill source | How it works |
|-------------|-------------|--------------|
| In-pod worker | Clone repo → `agents/skills/` in repo | Agent reads on startup |
| Remote pod worker | Clone repo → same | Same as in-pod |
| External listener | Clone repo → same | Same; uses own agent CLI + API key |

## File Structure (new/changed)

```
agents/
  skills/
    triage-issue/
      SKILL.md            # CHANGED: remove gh issue create, output structured JSON
internal/
  queue/
    interface.go          # JobQueue, ResultBus, AttachmentStore, JobStore
    job.go                # Job, JobResult, JobState, AttachmentMeta
    priority.go           # container/heap priority queue implementation
    inmem.go              # InMemTransport (implements all interfaces)
  worker/
    pool.go               # Worker pool startup and management
    executor.go           # Single job execution (clone, agent, parse)
  bot/
    handler.go            # CHANGED: remove semaphore
    workflow.go           # CHANGED: split — interactive UI stays, runTriage → queue.Submit
    prompt.go             # CHANGED: remove RepoPath, github_repo, labels from prompt
    parser.go             # CHANGED: parse structured JSON output instead of ===TRIAGE_RESULT===CREATED:<url>
  config/
    config.go             # CHANGED: add queue, channel_priority, workers, attachments config
```

## What's Deferred (遠期)

- External transport implementations (NATS, Redis Stream, SQS)
- Worker authentication and registration enforcement
- Job routing / filtering (by repo, labels)
- S3 attachment store
- ResultCallback interface (webhook-based result delivery)
- Dynamic worker scaling

## Migration Notes

- `max_concurrent` config: config loader checks both `max_concurrent` (old path) and `workers.count` (new path); old path takes precedence if both set, with deprecation warning logged
- Skill change: `agents/skills/triage-issue/SKILL.md` — remove `gh issue create`, output structured JSON with title/body/labels/confidence/files_found/open_questions
- Prompt change: `internal/bot/prompt.go` — remove `RepoPath`, `github_repo`, `labels` from prompt; keep thread context, branch, channel, reporter, attachments
- Parser change: `internal/bot/parser.go` — parse structured JSON from `===TRIAGE_RESULT===` block instead of `CREATED:<url>` format
- Semaphore removal from handler.go
- Existing tests need updating for new flow split
