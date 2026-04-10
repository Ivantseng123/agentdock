# Agent Process Tracking & Kill Design

## Goal

Add real-time agent process visibility and kill capability across all deployment modes (in-memory, remote worker, external listener). Enable operators and users to see what a running agent is doing, detect stuck agents, and terminate wasteful sessions.

## Requirements

- **Process tracking**: Track PID, command, alive status for all agents; track stream events (tool calls, files read, output bytes, cost) for agents that support stream-json
- **Kill mechanism**: Four trigger sources — manual HTTP, watchdog timeout, agent idle detection, Slack cancel button — all unified through a single CommandBus
- **Three deployment modes**: In-memory (direct), remote worker (via transport), external listener (via transport) — same interfaces, different transport implementations
- **Zero agent slowdown**: Stream parsing runs in a separate goroutine reading stdout pipe; agent writes at its own pace
- **Graceful termination**: SIGTERM via `cmd.Cancel`, auto-SIGKILL after `cmd.WaitDelay` (10s)
- **Cost tracking**: Capture `total_cost_usd`, `input_tokens`, `output_tokens` from claude's `result` event

## Architecture

```
┌────────────────────────────────────────────────────────┐
│                       App Side                          │
│                                                         │
│  Kill triggers:                                         │
│    DELETE /jobs/{id}     ─┐                             │
│    Watchdog timeout      ─┤→ CommandBus.Send(kill)      │
│    Idle/prepare timeout  ─┤                             │
│    Slack cancel button   ─┘                             │
│                                                         │
│  StatusListener ← StatusBus.Subscribe()                 │
│    → updates JobStore with AgentStatus                  │
│                                                         │
│  Watchdog checks:                                       │
│    → job_timeout (all jobs)                             │
│    → agent_idle_timeout (stream-json agents)            │
│    → prepare_timeout (preparing stage)                  │
│                                                         │
│  GET /jobs ← reads JobStore (includes agent status)     │
└──────────────┬─────────────────────┬───────────────────┘
               │ CommandBus          │ StatusBus
               ↓                     ↑
┌──────────────┴─────────────────────┴───────────────────┐
│                     Worker Side                         │
│                                                         │
│  CommandListener ← CommandBus.Receive()                 │
│    → ProcessRegistry.Kill(jobID) → calls jobCancel()    │
│                                                         │
│  Worker goroutine:                                      │
│    cmd.StdoutPipe() → streamReader goroutine            │
│      → stream: parse NDJSON, extract result event       │
│      → non-stream: raw text accumulation                │
│      → every status_interval: StatusBus.Report(status)  │
│    cmd.Cancel = SIGTERM, cmd.WaitDelay = 10s            │
│    cmd.Start() → ProcessRegistry.Register(jobID)        │
│    cmd.Wait()  → ProcessRegistry.Remove(jobID)          │
│                → cleanupSkills + git reset repo          │
└────────────────────────────────────────────────────────┘
```

## New Interfaces

### CommandBus

```go
type Command struct {
    JobID  string `json:"job_id"`
    Action string `json:"action"` // "kill"
}

type CommandBus interface {
    Send(ctx context.Context, cmd Command) error
    Receive(ctx context.Context) (<-chan Command, error)
    Close() error
}
```

### StatusBus

```go
type StatusReport struct {
    JobID       string    `json:"job_id"`
    WorkerID    string    `json:"worker_id"`
    PID         int       `json:"pid"`
    AgentCmd    string    `json:"agent_cmd"`
    Alive       bool      `json:"alive"`
    LastEvent   string    `json:"last_event,omitempty"`
    LastEventAt time.Time `json:"last_event_at"`
    ToolCalls   int       `json:"tool_calls"`
    FilesRead   int       `json:"files_read"`
    OutputBytes int       `json:"output_bytes"`
}

type StatusBus interface {
    Report(ctx context.Context, report StatusReport) error
    Subscribe(ctx context.Context) (<-chan StatusReport, error)
    Close() error
}
```

### Split In-Memory Transport

`InMemTransport` is split into 5 focused implementations + a factory:

```go
type InMemJobQueue struct { ... }      // priority heap + dispatch loop
type InMemResultBus struct { ... }     // buffered result channel
type InMemAttachmentStore struct { ... } // per-job ready channels
type InMemCommandBus struct { ... }    // buffered command channel (cap: 10)
type InMemStatusBus struct { ... }     // buffered status channel (cap: workerCount*2)

// Factory creates all five, returns a bundle
type InMemBundle struct {
    Queue       *InMemJobQueue
    Results     *InMemResultBus
    Attachments *InMemAttachmentStore
    Commands    *InMemCommandBus
    Status      *InMemStatusBus
}

func NewInMemBundle(cfg TransportConfig, store JobStore) *InMemBundle
```

Each struct implements one interface. For future remote transport (NATS/Redis), each has its own independent implementation (e.g., `NATSJobQueue`, `NATSCommandBus`).

## ProcessRegistry (Worker-Side, Simplified)

Lives on the worker side. Holds `context.CancelFunc` per job — **no `*os.Process` reference**. Kill = cancel the job's context, which triggers `cmd.Cancel` (SIGTERM) + `cmd.WaitDelay` (auto SIGKILL after 10s).

```go
type ProcessRegistry struct {
    mu        sync.RWMutex
    processes map[string]*runningAgent
}

type runningAgent struct {
    JobID     string
    PID       int
    Command   string
    StartedAt time.Time
    Cancel    context.CancelFunc
    done      chan struct{} // closed by Remove()
}

func (r *ProcessRegistry) Register(jobID string, pid int, command string, cancel context.CancelFunc) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.processes[jobID] = &runningAgent{
        JobID:     jobID,
        PID:       pid,
        Command:   command,
        StartedAt: time.Now(),
        Cancel:    cancel,
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

    // Cancel the job context → triggers cmd.Cancel (SIGTERM)
    // cmd.WaitDelay (10s) handles escalation to SIGKILL automatically
    agent.Cancel()

    // Wait for Remove() to confirm process exited, or timeout
    select {
    case <-agent.done:
        return nil
    case <-time.After(15 * time.Second): // 10s WaitDelay + 5s buffer
        return fmt.Errorf("kill timeout for job %q", jobID)
    }
}
```

### Post-Kill Cleanup

After `cmd.Wait()` returns (whether from normal exit, SIGTERM, or SIGKILL), the worker always runs:

```go
defer func() {
    cleanupSkills(repoPath, job.Skills, skillDir)
    // Reset repo to clean state (kill may leave dirty files)
    exec.Command("git", "-C", repoPath, "checkout", ".").Run()
    exec.Command("git", "-C", repoPath, "clean", "-fd").Run()
    p.registry.Remove(job.ID)
}()
```

## Agent Runner Changes

### Config addition

```go
type AgentConfig struct {
    Command  string        `yaml:"command"`
    Args     []string      `yaml:"args"`
    Timeout  time.Duration `yaml:"timeout"`
    SkillDir string        `yaml:"skill_dir"`
    Stream   bool          `yaml:"stream"` // enable stream-json event tracking
}
```

### Per-call RunOptions (no shared state)

Callbacks are per-call, not per-struct, to avoid race conditions when multiple workers share one AgentRunner:

```go
type RunOptions struct {
    OnStarted func(pid int, command string)
    OnProcess func(proc *os.Process) // worker captures for cmd.Cancel setup
    OnEvent   func(event StreamEvent)
}

func (r *AgentRunner) Run(ctx context.Context, logger *slog.Logger, workDir, prompt string, opts RunOptions) (string, error) {
    // ... fallback chain, each calling runOne with opts
}
```

### runOne refactor — StdoutPipe + stream parsing

```go
func (r *AgentRunner) runOne(ctx context.Context, logger *slog.Logger, agent config.AgentConfig, workDir, prompt string, opts RunOptions) (string, error) {
    cmd := exec.CommandContext(ctx, agent.Command, args...)
    cmd.Dir = workDir
    cmd.Env = append(os.Environ(), fmt.Sprintf("GH_TOKEN=%s", r.githubToken))

    // Graceful termination: SIGTERM first, auto-SIGKILL after 10s
    cmd.Cancel = func() error {
        return cmd.Process.Signal(syscall.SIGTERM)
    }
    cmd.WaitDelay = 10 * time.Second

    stdoutPipe, _ := cmd.StdoutPipe()
    var stderr strings.Builder
    cmd.Stderr = &stderr

    if useStdin {
        cmd.Stdin = strings.NewReader(prompt)
    }

    if err := cmd.Start(); err != nil {
        return "", err
    }

    // Notify callbacks
    if opts.OnStarted != nil {
        opts.OnStarted(cmd.Process.Pid, agent.Command)
    }
    if opts.OnProcess != nil {
        opts.OnProcess(cmd.Process)
    }
    logger.Info("agent process started", "command", agent.Command, "pid", cmd.Process.Pid)

    // Read stdout in goroutine — stream or raw mode
    var readerWg sync.WaitGroup
    readerWg.Add(1)

    var finalText string  // populated by result event or raw accumulation
    eventCh := make(chan StreamEvent, 1000)

    go func() {
        defer readerWg.Done()
        defer close(eventCh)

        if agent.Stream {
            finalText = readStreamJSON(stdoutPipe, eventCh)
        } else {
            finalText = readRawOutput(stdoutPipe)
        }
    }()

    // Forward events to callback with context awareness
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

    // Wait for reader to finish BEFORE cmd.Wait closes the pipe
    readerWg.Wait()

    err := cmd.Wait()
    if err != nil {
        if ctx.Err() != nil {
            return "", fmt.Errorf("cancelled")
        }
        if exitErr, ok := err.(*exec.ExitError); ok {
            return "", fmt.Errorf("exit %d: %s", exitErr.ExitCode(), strings.TrimSpace(stderr.String()))
        }
        return "", err
    }

    return finalText, nil
}
```

### Stream Reader — result event as primary text source

```go
// readStreamJSON reads NDJSON from claude --output-format stream-json.
// Returns the final text from the "result" event, or reassembled message_delta as fallback.
func readStreamJSON(r io.Reader, eventCh chan<- StreamEvent) string {
    scanner := bufio.NewScanner(r)
    scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

    var reassembled strings.Builder  // fallback: message_delta text
    var resultText string            // primary: result event text

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
            // Primary text source — complete final output
            if res, ok := raw["result"].(string); ok {
                resultText = res
            }
            // Cost tracking
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

    // Prefer result event text; fall back to reassembled message_delta
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

### Stream Event Types

```go
type StreamEvent struct {
    Type         string  // "tool_use", "message_delta", "result"
    ToolName     string  // e.g. "Read", "Bash", "Grep" (for tool_use)
    TextBytes    int     // bytes of text generated (for message_delta)
    CostUSD      float64 // total cost (for result)
    InputTokens  int     // (for result)
    OutputTokens int     // (for result)
}
```

## Worker Integration

### Command Listener (dedicated goroutine)

Kill commands MUST be processed while jobs are executing. A dedicated command listener goroutine runs independently of the job execution loop:

```go
func (p *Pool) Start(ctx context.Context) {
    go p.commandListener(ctx)

    for i := 0; i < p.cfg.WorkerCount; i++ {
        go p.runWorker(ctx, i)
    }
}

func (p *Pool) commandListener(ctx context.Context) {
    commands, _ := p.cfg.Commands.Receive(ctx)
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

### Worker — per-job context + status reporting + cancel check

```go
func (p *Pool) runWorker(ctx context.Context, id int) {
    jobs, _ := p.cfg.Queue.Receive(ctx)
    for {
        select {
        case job, ok := <-jobs:
            if !ok {
                return
            }
            // Check if job was already cancelled while pending
            state, err := p.cfg.Store.Get(job.ID)
            if err != nil || state.Status == queue.JobFailed {
                p.cfg.Results.Publish(ctx, &queue.JobResult{
                    JobID: job.ID, Status: "failed", Error: "cancelled before execution",
                })
                continue
            }
            p.executeWithTracking(ctx, id, job)
        case <-ctx.Done():
            return
        }
    }
}

func (p *Pool) executeWithTracking(ctx context.Context, workerID int, job *queue.Job) {
    // Per-job context with cancel for kill support
    jobCtx, jobCancel := context.WithCancel(ctx)
    defer jobCancel()

    // Status accumulator
    status := &statusAccumulator{
        jobID:    job.ID,
        workerID: fmt.Sprintf("worker-%d", workerID),
        alive:    true,
    }

    // Wire RunOptions — per-call, no shared state
    opts := RunOptions{
        OnStarted: func(pid int, command string) {
            status.setPID(pid, command)
            p.registry.Register(job.ID, pid, command, jobCancel)
        },
        OnEvent: func(event StreamEvent) {
            status.recordEvent(event)
        },
    }

    // Start periodic status reporting
    stopReporter := make(chan struct{})
    go p.reportStatus(jobCtx, status, stopReporter)

    // Execute job (blocks until agent finishes or is killed)
    result := executeJob(jobCtx, job, deps, opts)

    close(stopReporter)
    p.registry.Remove(job.ID)
    // No final Alive=false report — ResultListener handles completion

    p.cfg.Results.Publish(ctx, result)
}
```

### statusAccumulator

```go
type statusAccumulator struct {
    mu          sync.Mutex
    jobID       string
    workerID    string
    pid         int
    agentCmd    string
    alive       bool
    lastEvent   string
    lastEventAt time.Time
    toolCalls   int
    filesRead   int
    outputBytes int
    costUSD     float64
    inputTokens int
    outputTokens int
}

func (s *statusAccumulator) setPID(pid int, cmd string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.pid = pid
    s.agentCmd = cmd
}

func (s *statusAccumulator) recordEvent(event StreamEvent) {
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

func (s *statusAccumulator) toReport() StatusReport { ... }
```

## App-Side: StatusListener + Watchdog

### StatusListener

```go
func (l *StatusListener) Listen(ctx context.Context) {
    ch, _ := l.status.Subscribe(ctx)
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

### Watchdog — three timeout checks

```go
func (w *Watchdog) check() {
    all, _ := w.store.ListAll()
    now := time.Now()

    for _, state := range all {
        if state.Status == JobCompleted || state.Status == JobFailed {
            continue
        }

        // 1. Job-level timeout (all jobs)
        if now.Sub(state.Job.SubmittedAt) > w.jobTimeout {
            w.killAndNotify(state, "job timeout")
            continue
        }

        // 2. Prepare timeout (job stuck in preparing stage)
        if state.Status == JobPreparing && w.prepareTimeout > 0 {
            if state.AgentStatus == nil || state.AgentStatus.LastEventAt.IsZero() {
                // No events yet — check time since status changed to preparing
                if now.Sub(state.StartedAt) > w.prepareTimeout {
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
    w.commands.Send(context.Background(), Command{JobID: state.Job.ID, Action: "kill"})
    w.store.UpdateStatus(state.Job.ID, JobFailed)
    if w.notifier != nil {
        w.notifier(state.Job, state.Status, reason)
    }
}
```

### Updated StuckNotifier signature

```go
type StuckNotifier func(job *Job, status JobStatus, reason string)

func FormatStuckMessage(job *Job, status JobStatus, reason string) string {
    return fmt.Sprintf(":warning: Job 已終止 (%s)，狀態停在 `%s`，repo: `%s`。請重新觸發。",
        reason, status, job.Repo)
}
```

## HTTP Endpoints

### GET /jobs (enhanced)

```json
{
  "queue_depth": 0,
  "total": 1,
  "jobs": [
    {
      "id": "req-abc123",
      "status": "running",
      "repo": "org/backend",
      "age": "2m30s",
      "agent": {
        "pid": 12345,
        "command": "claude",
        "alive": true,
        "last_event": "tool_use:Read",
        "last_event_age": "3s",
        "tool_calls": 12,
        "files_read": 8,
        "output_bytes": 15360,
        "cost_usd": 0.042,
        "input_tokens": 8500,
        "output_tokens": 1200
      }
    }
  ]
}
```

### DELETE /jobs/{id}

```
DELETE /jobs/req-abc123

Response 200: {"status": "killing", "job_id": "req-abc123"}
Response 404: {"error": "job not found"}
Response 409: {"error": "job not running"}
```

Flow:
1. Validate job exists and is in running/preparing/pending state
2. Mark job as `JobFailed` in JobStore with reason "cancelled"
3. `CommandBus.Send({job_id, action: "kill"})` — worker kills process if running
4. If pending (still in queue): worker will dequeue the job but skip execution because JobStore shows `JobFailed`. No need for a `Cancel` method on JobQueue — the worker checks status after dequeue.
5. Return immediately (async — result comes via ResultListener or worker skip)

## Slack Cancel Button

When submitting a job, attach a "取消" button to the queue position message:

```go
// In workflow.go after queue.Submit
msg := fmt.Sprintf(":hourglass_flowing_sand: 正在處理你的請求...")
w.slack.PostMessageWithButton(pt.ChannelID, msg, pt.ThreadTS,
    "cancel_job", "取消", job.ID)
```

Button click handler in the Socket Mode event loop (uses existing `InteractionTypeBlockActions`):

```go
case strings.HasPrefix(action.ActionID, "cancel_job"):
    jobID := action.Value
    state, err := jobStore.Get(jobID)
    if err == nil && isActive(state.Status) {
        commandBus.Send(ctx, Command{JobID: jobID, Action: "kill"})
        slackClient.UpdateMessage(cb.Channel.ID, selectorTS,
            ":stop_sign: 正在取消...")
    }
```

No additional Slack App event subscriptions needed — Interactive buttons already work.

## Attachment Fix

`AttachmentMeta` gains `DownloadURL` for the two-phase flow:

```go
type AttachmentMeta struct {
    SlackFileID string `json:"slack_file_id"`
    Filename    string `json:"filename"`
    Size        int64  `json:"size"`
    MimeType    string `json:"mime_type"`
    DownloadURL string `json:"download_url"` // Slack private file URL
}
```

`workflow.runTriage` collects metadata + URLs but does NOT download. `AttachmentStore.Prepare` uses `DownloadURL` to fetch files after worker Ack.

## JobResult Cost Extension

```go
type JobResult struct {
    // ...existing fields
    CostUSD      float64 `json:"cost_usd,omitempty"`
    InputTokens  int     `json:"input_tokens,omitempty"`
    OutputTokens int     `json:"output_tokens,omitempty"`
}
```

## JobStore Extension

```go
type JobState struct {
    // ...existing fields (Job, Status, Position, WorkerID, StartedAt, WaitTime)
    AgentStatus *StatusReport // nil if no status reported yet
}

type JobStore interface {
    // ...existing methods
    // SetAgent is removed — replaced by SetAgentStatus
    SetAgentStatus(jobID string, report StatusReport) error
    // SetAgentStatus on a deleted/not-found job is silently ignored
}
```

## Config

```yaml
queue:
  capacity: 50
  transport: inmem
  job_timeout: 20m              # watchdog: max job lifetime
  agent_idle_timeout: 5m        # stream-json agent: no events for this long = stuck
  prepare_timeout: 3m           # preparing stage: clone/setup timeout
  status_interval: 5s           # worker status report frequency

agents:
  claude:
    command: claude
    args: ["--print", "--output-format", "stream-json", "-p", "{prompt}"]
    timeout: 15m
    skill_dir: ".claude/skills"
    stream: true
  opencode:
    command: opencode
    args: ["--prompt", "{prompt}"]
    timeout: 5m
    skill_dir: ".opencode/skills"
    stream: false
```

Note: `kill_grace_period` is removed from config — hardcoded as `cmd.WaitDelay = 10 * time.Second` since it's tied to Go's exec behavior, not a user-tunable knob.

## File Structure (new/changed)

```
internal/
  queue/
    interface.go          # CHANGED: add CommandBus, StatusBus, StatusReport, Command; remove SetAgent
    job.go                # CHANGED: extend JobState, JobResult; add AttachmentMeta.DownloadURL
    inmem_jobqueue.go     # NEW: InMemJobQueue (extracted from inmem.go)
    inmem_resultbus.go    # NEW: InMemResultBus
    inmem_attachments.go  # NEW: InMemAttachmentStore
    inmem_commandbus.go   # NEW: InMemCommandBus
    inmem_statusbus.go    # NEW: InMemStatusBus
    inmem_bundle.go       # NEW: InMemBundle factory
    inmem.go              # REMOVED: replaced by 6 files above
    memstore.go           # CHANGED: add SetAgentStatus; remove SetAgent
    httpstatus.go         # CHANGED: enhanced /jobs, add DELETE /jobs/{id}
    watchdog.go           # CHANGED: idle + prepare detection, CommandBus kill, new notifier signature
    registry.go           # NEW: ProcessRegistry (simplified, cancel-only)
    stream.go             # NEW: StreamEvent, readStreamJSON, readRawOutput
  worker/
    pool.go               # CHANGED: command listener, per-job context, cancel check, status reporting
    executor.go           # CHANGED: wire RunOptions, post-kill cleanup (git reset)
    status.go             # NEW: statusAccumulator
  bot/
    agent.go              # CHANGED: RunOptions, cmd.Cancel/WaitDelay, StdoutPipe reader
  config/
    config.go             # CHANGED: add Stream, prepare_timeout, agent_idle_timeout, status_interval
cmd/
  bot/
    main.go               # CHANGED: wire StatusListener, CommandBus, cancel button handler, updated watchdog
```

## Deployment Mode Summary

| Capability | In-Memory | Remote Worker | External Listener |
|-----------|-----------|---------------|-------------------|
| Kill | CommandBus (channel) → jobCancel() | CommandBus (NATS) → Worker's jobCancel() | Same as remote |
| Status tracking | StatusBus (channel) → JobStore | StatusBus (NATS) → JobStore | Same as remote |
| Stream events | Same-process, zero latency | Batched every status_interval | Same as remote |
| Idle detection | Watchdog reads JobStore | Same — based on StatusReport timestamps | Same |
| Prepare timeout | Watchdog reads JobStore | Same | Same |
| Cost tracking | From result event via StatusBus | Same | Same |

All modes use the same interfaces. Only the transport layer differs.

## Migration Notes

- `agent.Args` for claude changes to include `--output-format stream-json`
- Add `stream: true` to claude agent config
- Add `prepare_timeout`, `agent_idle_timeout`, `status_interval` to queue config
- `AgentRunner.Run` signature changes: adds `RunOptions` parameter
- `TrackedRunner` interface replaced by `RunOptions` pattern
- `SetAgent` removed from JobStore; replaced by `SetAgentStatus`
- `StuckNotifier` signature changes: `time.Duration` → `string` reason
- `InMemTransport` split into 5 structs + bundle factory
- `AttachmentMeta` gains `DownloadURL` field
- `JobResult` gains cost/token fields
- Existing tests need updating for new signatures
- Slack cancel button requires `PostMessageWithButton` helper on slack client
