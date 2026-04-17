# Distinguish User Cancellation from Agent Failure

**Date:** 2026-04-17
**Status:** Approved
**Issue:** [#36](https://github.com/Ivantseng123/agentdock/issues/36)

## Problem

When a user clicks the "取消" button in Slack, the app sends a `kill` command to the worker, which SIGTERMs the agent process. The agent exits with code 143 (128+15), which the worker currently treats identically to a real agent failure. Consequences:

- Slack shows `:x: 分析失敗: all agents failed` instead of a clean cancellation message.
- A retry button appears, inviting the user to re-run a job they explicitly cancelled.
- Worker log reads `[Worker][完成] 工作完成 status=failed`, obscuring operator visibility.

Root cause: `executeJob` in `internal/worker/executor.go` does not inspect `ctx.Err()` before returning a failed result. The app and worker also lack a dedicated `JobCancelled` status, so every code path converges on `JobFailed`.

Investigation also surfaced four related defects:

1. **Kill-during-prep gap.** `ProcessRegistry.Register` is only called from `OnStarted`, so kill commands issued while the worker is still in the prep phase (attachments, repo clone, skill mount) find no PID and are silently dropped.
2. **Provider chain waste on cancel.** `internal/bot/agent.go Run` iterates through providers. When SIGTERM cancels the ctx, the loop continues to the next provider (whose `exec.CommandContext` fails immediately), each emitting `Agent 失敗` warn and ending with `所有 agent 已耗盡` error.
3. **Completed-before-cancel race.** If the agent finishes just before the user clicks cancel, the completed result can be processed by `ResultListener` before the user's intent is visible in the store, creating a GitHub issue the user did not want.
4. **Admin force-kill overlaps.** `internal/queue/httpstatus.go` lets an admin force-kill by setting `JobFailed` and sending a kill command. The pool's short-circuit and the worker's classifier need to handle admin kill without treating it as a user cancel.
5. **Worker crash during cancel.** If the worker dies between receiving the kill and publishing a result, the Slack message stays on `:stop_sign: 正在取消...` indefinitely because nothing else republishes.

This spec addresses all five.

## Goals

1. A user-initiated cancellation produces a clean Slack state (`:white_check_mark: 已取消`), no retry button, and no spurious failure log.
2. Cancellation is honoured at every stage: job still in queue, worker in prep, agent running.
3. The design reaches a terminal state even when the worker crashes mid-cancel — via a watchdog fallback.
4. Watchdog timeouts, admin force-kills, and true agent failures continue to surface as `failed` with the existing retry affordance.
5. Metrics distinguish cancelled jobs from errored jobs, so the agent failure rate is not polluted.

## Non-Goals

- Introducing a separate `timeout` status. Watchdog timeouts remain `failed` (as today).
- Automatic cancellation based on idle detection or cost caps.
- Letting the user cancel an already-completed job.
- Changing `RepoProvider.Prepare` to accept a `context.Context`. Cancellation during an in-flight clone waits for the natural boundary to avoid partial repo state.

## Design

### 1. Data Model

**`internal/queue/job.go`** — add a status constant and a cancelled-timestamp field on `JobState`:

```go
const (
    JobPending   JobStatus = "pending"
    JobPreparing JobStatus = "preparing"
    JobRunning   JobStatus = "running"
    JobCompleted JobStatus = "completed"
    JobFailed    JobStatus = "failed"
    JobCancelled JobStatus = "cancelled" // NEW
)

type JobState struct {
    // ... existing fields ...
    CancelledAt time.Time // NEW — zero-valued unless Status transitions to JobCancelled
}
```

`JobResult.Status` gains a `"cancelled"` string value. No boolean flag is added; a single status field is the source of truth. `JobResult.Error` is left empty for cancelled results.

### 2. Store Behaviour

`internal/queue/memstore.go` stamps `CancelledAt` when status transitions to `JobCancelled`, mirroring the existing `StartedAt` pattern:

```go
func (s *MemJobStore) UpdateStatus(jobID string, status JobStatus) error {
    // ... existing logic ...
    state.Status = status
    if status == JobRunning && state.StartedAt.IsZero() {
        state.StartedAt = time.Now()
        state.WaitTime = state.StartedAt.Sub(state.Job.SubmittedAt)
    }
    if status == JobCancelled && state.CancelledAt.IsZero() { // NEW
        state.CancelledAt = time.Now()
    }
    return nil
}
```

Double-calls to `UpdateStatus(JobCancelled)` from both app and result-listener are idempotent — `CancelledAt` preserves the first value.

### 3. Executor Classification

In `internal/worker/executor.go`, all failure returns route through a single classifier that distinguishes user cancellation from other context cancellations (e.g., watchdog kill, admin force-kill):

```go
func classifyResult(job *queue.Job, startedAt time.Time, err error, repoPath string, ctx context.Context, store queue.JobStore) *queue.JobResult {
    if ctx.Err() == context.Canceled {
        if state, lookupErr := store.Get(job.ID); lookupErr == nil && state.Status == queue.JobCancelled {
            return cancelledResult(job, startedAt, repoPath)
        }
    }
    return failedResult(job, startedAt, err, repoPath)
}

func cancelledResult(job *queue.Job, startedAt time.Time, repoPath string) *queue.JobResult {
    return &queue.JobResult{
        JobID:      job.ID,
        Status:     "cancelled",
        RepoPath:   repoPath,
        StartedAt:  startedAt,
        FinishedAt: time.Now(),
    }
}
```

The store check is the discriminator: the app sets `JobCancelled` before sending the kill command (see §6), so a ctx cancellation with matching store state is a user cancel. Any other ctx cancellation — watchdog kill, admin force-kill — leaves the store at `JobRunning`/`JobFailed` and falls through to `failedResult`.

All existing `failedResult(...)` call sites in `executeJob` switch to `classifyResult(...)`:
- attachments resolve failure
- secret key missing / decrypt / unmarshal
- repo prepare
- skill mount
- `runner.Run` return (the original issue site)
- agent output parse

`context.DeadlineExceeded` deliberately falls through to `failedResult` so timeouts remain failures.

**Pre-Prepare cancellation check.** `RepoProvider.Prepare` does not consume ctx, so a cancellation that arrives while clone is in progress has to wait. But if ctx is already cancelled before we enter Prepare, we can skip the clone entirely:

```go
// Check cancellation before the expensive non-cancellable clone.
if err := ctx.Err(); err != nil {
    return classifyResult(job, startedAt, err, "", ctx, deps.store)
}

repoPath, err := deps.repoCache.Prepare(job.CloneURL, job.Branch, ghToken)
```

Only this single guard is added; skill mount and decrypt are fast enough to complete without a check, and `attachments.Resolve(ctx, ...)` / `runner.Run(ctx, ...)` honour ctx themselves.

### 4. Agent Runner

`internal/bot/agent.go Run` short-circuits the provider chain on context cancellation so the `所有 agent 已耗盡` spam no longer fires when the user cancels:

```go
for i, agent := range r.agents {
    logger.Info("嘗試 agent", ...)
    output, err := r.runOne(ctx, logger, agent, workDir, prompt, opts)
    if err != nil {
        if ctx.Err() == context.Canceled {
            logger.Info("Agent 執行已中斷", "phase", "完成", "command", agent.Command, "index", i)
            return "", fmt.Errorf("cancelled")
        }
        logger.Warn("Agent 失敗", "phase", "失敗", ...)
        errs = append(errs, fmt.Sprintf("%s: %s", agent.Command, err))
        continue
    }
    // ... success path unchanged ...
}
```

The runner cannot distinguish user cancel from watchdog kill — both are `ctx.Canceled`. That distinction is made one level up, in `classifyResult`. The error string `"cancelled"` is informational only; classification does not rely on it.

### 5. Process Registry Lifecycle

`internal/queue/registry.go` splits registration into two phases so `Kill` works before the agent starts:

```go
// Called at the start of executeWithTracking — stores the jobCancel function only.
func (r *ProcessRegistry) RegisterPending(jobID string, cancel context.CancelFunc) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.processes[jobID] = &RunningAgent{
        JobID:  jobID,
        cancel: cancel,
        done:   make(chan struct{}),
    }
}

// Called from OnStarted — fills in the process details.
func (r *ProcessRegistry) SetStarted(jobID string, pid int, command string) {
    r.mu.Lock()
    defer r.mu.Unlock()
    if a, ok := r.processes[jobID]; ok {
        a.PID = pid
        a.Command = command
        a.StartedAt = time.Now()
    }
}
```

`Kill`, `Remove`, and `Get` are unchanged. `Kill` no longer requires a PID to be set; calling `agent.cancel()` is sufficient to propagate cancellation through `ctx` into any `Resolve` / `Prepare` / `Run` call that honours it.

The existing `Register(jobID, pid, command, cancel)` method is removed; callers migrate to `RegisterPending` + `SetStarted`.

### 6. Pool Integration

**`internal/worker/pool.go executeWithTracking`** — registry lifecycle is consolidated around defers:

```go
jobCtx, jobCancel := context.WithCancel(ctx)
defer jobCancel()

p.registry.RegisterPending(job.ID, jobCancel)
defer p.registry.Remove(job.ID) // replaces the explicit Remove at end of function

// Race mitigation: close the window between queue-check and RegisterPending.
if state, _ := p.cfg.Store.Get(job.ID); state != nil &&
    (state.Status == queue.JobCancelled || state.Status == queue.JobFailed) {
    jobCancel()
}

// ... existing status accumulator, opts ...
opts := bot.RunOptions{
    OnStarted: func(pid int, command string) {
        status.setPID(pid, command)
        p.registry.SetStarted(job.ID, pid, command) // was Register(...)
        // ... rest unchanged ...
    },
    // ...
}
```

Re-reading for both `JobCancelled` and `JobFailed` also covers admin force-kill arriving in the same tiny window.

**`pool.go runWorker`** — queue short-circuit is split by terminal state so the right status propagates:

```go
state, err := p.cfg.Store.Get(job.ID)
if err != nil {
    p.cfg.Results.Publish(ctx, &queue.JobResult{
        JobID: job.ID, Status: "failed", Error: "state lookup failed",
    })
    continue
}
switch state.Status {
case queue.JobCancelled:
    p.cfg.Results.Publish(ctx, &queue.JobResult{
        JobID: job.ID, Status: "cancelled",
    })
    continue
case queue.JobFailed:
    p.cfg.Results.Publish(ctx, &queue.JobResult{
        JobID: job.ID, Status: "failed", Error: "cancelled before execution",
    })
    continue
}
```

**Completion log** — final `logger.Info("工作完成", ...)` becomes issue-compliant:

```go
if result.Status == "cancelled" {
    logger.Info("工作已取消", "phase", "完成")
} else {
    logger.Info("工作完成", "phase", "完成", "status", result.Status)
}
```

### 7. App-Side Cancel Button

`cmd/agentdock/app.go` cancel handler:

```go
case strings.HasPrefix(action.ActionID, "cancel_job"):
    jobID := action.Value
    state, err := jobStore.Get(jobID)
    if err == nil &&
        state.Status != queue.JobFailed &&
        state.Status != queue.JobCompleted &&
        state.Status != queue.JobCancelled {
        // Order matters: UpdateStatus BEFORE Send so the worker's classifyResult
        // observes JobCancelled when ctx cancellation propagates.
        jobStore.UpdateStatus(jobID, queue.JobCancelled)
        bundle.Commands.Send(context.Background(), queue.Command{JobID: jobID, Action: "kill"})
        slackClient.UpdateMessage(cb.Channel.ID, selectorTS, ":stop_sign: 正在取消...")
        handler.ClearThreadDedup(cb.Channel.ID, state.Job.ThreadTS)
    } else {
        slackClient.UpdateMessage(cb.Channel.ID, selectorTS, ":information_source: 此任務已結束")
    }
```

The ordering is load-bearing: the original code does `Send` then `UpdateStatus`, which races against the worker's `classifyResult` store read. Setting `JobCancelled` first and sending `kill` second guarantees the discriminator observes the cancelled state.

`:stop_sign: 正在取消...` stays as the intermediate feedback; the final `:white_check_mark: 已取消` comes from the result listener when the worker (or the watchdog fallback) confirms.

### 8. Result Listener

`internal/bot/result_listener.go handleResult` gets three changes:

**Store-state pre-check (Design A).** Before branching on `result.Status`, read the store. If the user has cancelled since the result was published, honour the cancel regardless of what the worker reported. This closes the completed-before-cancel race: an `in-flight completed result` paired with a recent cancel click does not create a GitHub issue.

```go
func (r *ResultListener) handleResult(ctx context.Context, result *queue.JobResult) {
    // ... existing dedup guard (processedJobs) ...
    state, err := r.store.Get(result.JobID)
    if err != nil {
        r.logger.Error("找不到工作結果對應的工作", ...)
        return
    }
    // ... recordMetrics ...

    // Design A: user cancellation dominates, regardless of result.Status.
    if state.Status == queue.JobCancelled || result.Status == "cancelled" {
        r.handleCancellation(state.Job, state, result)
        r.attachments.Cleanup(ctx, result.JobID)
        return
    }

    switch result.Status {
    case "failed":
        r.handleFailure(state.Job, state, result)
    // ... existing branches: low confidence, degraded, success ...
    }
    r.attachments.Cleanup(ctx, result.JobID)
}
```

New helper:

```go
func (r *ResultListener) handleCancellation(job *queue.Job, state *queue.JobState, result *queue.JobResult) {
    r.store.UpdateStatus(job.ID, queue.JobCancelled) // no-op if already set
    r.updateStatus(job, ":white_check_mark: 已取消")
    r.clearDedup(job)
}
```

No retry button, no issue creation, no error log. Dedup is cleared so the user can re-tag the bot in the same thread if they change their mind.

**Log branch.** The top-level log line uses a switch:

```go
switch result.Status {
case "failed":
    logger.Warn("工作失敗", "phase", "降級", "error", result.Error, "raw_output", truncated)
case "cancelled":
    logger.Info("工作已取消", "phase", "完成")
default:
    logger.Info("工作完成", "phase", "完成", "title", result.Title, ...)
}
```

**Metrics.** `recordMetrics` extends the `status` derivation for `AgentExecutionsTotal`, covering both the with-AgentStatus and without-AgentStatus paths:

```go
// When AgentStatus is non-nil
status := "success"
switch result.Status {
case "failed":
    if strings.Contains(result.Error, "timeout") {
        status = "timeout"
    } else {
        status = "error"
    }
case "cancelled":
    status = "cancelled"
}
metrics.AgentExecutionsTotal.WithLabelValues(provider, status).Inc()

// When AgentStatus is nil (prep-phase cancel, short-circuit, etc.)
} else if result.Status == "failed" {
    metrics.AgentExecutionsTotal.WithLabelValues("unknown", "error").Inc()
} else if result.Status == "cancelled" {
    metrics.AgentExecutionsTotal.WithLabelValues("unknown", "cancelled").Inc()
}
```

`RetryHandler.Handle` is untouched: it already rejects any status other than `JobFailed`, so a cancelled job is naturally ineligible for retry.

### 9. Watchdog Alignment

`internal/queue/watchdog.go` must not overwrite a cancelled job with a timeout failure, and must recover when the worker never publishes.

**Config.** Add `CancelTimeout time.Duration` (default 60 seconds) to `WatchdogConfig`. Zero or negative disables the fallback (useful for tests).

**`check()` reordering.** `JobCancelled` is handled ahead of the generic `jobTimeout` / `prepareTimeout` / `idleTimeout` checks so stuck cancels aren't misclassified as timeouts:

```go
for _, state := range all {
    // Already terminal.
    if state.Status == JobCompleted || state.Status == JobFailed {
        continue
    }
    // Cancelled: wait for worker, fall back after cancelTimeout.
    if state.Status == JobCancelled {
        if w.cancelTimeout > 0 && !state.CancelledAt.IsZero() &&
            now.Sub(state.CancelledAt) > w.cancelTimeout {
            w.publishCancelledFallback(state)
        }
        continue
    }
    // Regular timeout checks apply only to pending / preparing / running.
    if now.Sub(state.Job.SubmittedAt) > w.jobTimeout {
        w.killAndPublish(state, "job timeout")
        continue
    }
    // ... existing prepare / idle checks ...
}
```

**`publishCancelledFallback`** — recovery path when the worker never confirms:

```go
func (w *Watchdog) publishCancelledFallback(state *JobState) {
    if w.onKill != nil {
        w.onKill("cancel fallback")
    }
    w.logger.Warn("取消狀態逾時，補發 cancelled result", "phase", "完成",
        "job_id", state.Job.ID,
        "cancelled_age", time.Since(state.CancelledAt))
    if w.results != nil {
        w.results.Publish(context.Background(), &JobResult{
            JobID:      state.Job.ID,
            Status:     "cancelled",
            FinishedAt: time.Now(),
        })
    }
}
```

No kill is sent (one has already gone out from the app), and `UpdateStatus` is not called because the store is already `JobCancelled`. `ResultListener.processedJobs` dedup drops any late-arriving worker result.

**`killAndPublish` back-off (belt-and-suspenders).** The `check()` reorder alone prevents `killAndPublish` from being called on a cancelled job, but guarding defensively is cheap and resilient to future edits:

```go
// Immediately before the existing UpdateStatus(JobFailed) + Publish calls.
fresh, _ := w.store.Get(state.Job.ID)
if fresh != nil && fresh.Status == JobCancelled {
    return
}
```

When the watchdog legitimately times out a job, it proceeds as today. The worker's executor sees `ctx.Err() == Canceled` with a store status of `JobFailed`, so `classifyResult` returns `failed`. `ResultListener.processedJobs` dedup drops the loser.

### 10. Config Surface

Add `CancelTimeout time.Duration` to `internal/config/config.Config` (yaml key `cancel_timeout`, default 60s). The app constructs `WatchdogConfig.CancelTimeout` from the config value.

### 11. Admin Endpoint

`internal/queue/httpstatus.go` guard extended so admin force-kill does not overwrite a user cancellation:

```go
if state.Status == JobCompleted || state.Status == JobFailed || state.Status == JobCancelled {
    w.WriteHeader(http.StatusConflict)
    json.NewEncoder(w).Encode(map[string]string{"error": "job not running"})
    return
}
```

Admin force-kill still sets `JobFailed` (distinct intent from user cancel) and sends the kill command. The worker's `classifyResult` sees store `JobFailed` and returns `failed`, which `ResultListener` routes through `handleFailure`.

## Testing

### Unit

**`internal/worker/executor_test.go`** (new if absent):
- `classifyResult`: ctx.Canceled + store `JobCancelled` → `cancelled`.
- `classifyResult`: ctx.Canceled + store `JobFailed` → `failed` (admin-kill path).
- `classifyResult`: ctx.Canceled + store `JobRunning` → `failed` (watchdog-kill path).
- `classifyResult`: ctx.DeadlineExceeded + any store state → `failed`.
- `classifyResult`: err non-nil, ctx OK → `failed`.
- Pre-`Prepare` ctx check: cancelled ctx returns cancelled result without invoking `Prepare`.

**`internal/queue/registry_test.go`** — migrate existing `Register(...)` call sites; add:
- `RegisterPending` then `Kill` cancels the context without needing `SetStarted`.
- `RegisterPending` → `SetStarted` → `Kill` still records PID / Command correctly.
- `Kill` on an unknown job returns the existing error.

**`internal/queue/memstore_test.go`**:
- `UpdateStatus(JobCancelled)` stamps `CancelledAt`.
- Subsequent `UpdateStatus(JobCancelled)` does not overwrite the timestamp.

**`internal/bot/result_listener_test.go`**:
- `result.Status == "cancelled"` → store `JobCancelled`, Slack updated to `:white_check_mark: 已取消`, dedup cleared, no retry button posted.
- `result.Status == "completed"` + store `JobCancelled` (Design A) → routes to `handleCancellation`, no GitHub issue created.
- Metrics emit `AgentExecutionsTotal{status="cancelled"}` for both with- and without-AgentStatus paths.

**`internal/bot/agent_test.go`** (if absent — otherwise extend):
- Ctx cancellation mid-run: `Run` returns `"cancelled"` error, does not try the next provider.
- Logger emits `Agent 執行已中斷` (info), not `Agent 失敗` (warn).

**`internal/queue/watchdog_test.go`**:
- A `JobCancelled` state past `CancelTimeout` triggers `publishCancelledFallback` (cancelled result published, store untouched, `onKill("cancel fallback")` fired).
- A `JobCancelled` state within `CancelTimeout` is not republished.
- `JobCancelled` + expired `jobTimeout` does NOT invoke `killAndPublish`.
- `killAndPublish` back-off: a race-set `JobCancelled` during a watchdog kill skips UpdateStatus/Publish.

### Integration (`internal/worker/pool_test.go`)

Use a controllable fake `Runner` to exercise timing:

1. **Scenario A — queue hit post-cancel:** put a job, set store to `JobCancelled` before the worker pulls; assert published result is `cancelled`.
2. **Scenario A' — queue hit post-admin-kill:** put a job, set store to `JobFailed` before the worker pulls; assert published result is `failed` with `"cancelled before execution"`.
3. **Scenario B — prep cancel:** Runner sleeps during prep simulation; call `registry.Kill` and assert cancelled result.
4. **Scenario B-race — pre-Prepare guard:** set store to `JobCancelled` before Prepare runs; assert `Prepare` is not invoked and the result is cancelled.
5. **Scenario C — agent-running cancel:** Runner enters `Run` then blocks; call `registry.Kill`; assert cancelled result.
6. **Watchdog regression:** simulate a watchdog kill (store `JobFailed`, ctx cancel); assert result is `failed`, not cancelled.
7. **Watchdog fallback:** store `JobCancelled` with `CancelledAt` older than `CancelTimeout` and no worker publish; assert watchdog publishes cancelled.

### Manual smoke

Run app + worker locally, click cancel in Slack mid-agent, confirm:
- Worker log `[Worker][完成] 工作已取消`.
- Slack final message `:white_check_mark: 已取消`, no retry button.
- Re-tagging the bot in the same thread works (dedup cleared).
- Kill during repo clone: worker waits for clone to finish then reports cancelled.
- Kill a pending job (not yet pulled by any worker): the worker publishes cancelled on pull.

## Files Changed

- `internal/queue/job.go` — add `JobCancelled`; add `CancelledAt` to `JobState`.
- `internal/queue/memstore.go` — stamp `CancelledAt` on transition to `JobCancelled`.
- `internal/queue/memstore_test.go` — new assertions.
- `internal/queue/registry.go` — split `Register` into `RegisterPending` + `SetStarted`; remove `Register`.
- `internal/queue/registry_test.go` — migrate existing `Register(...)` call sites; add cancel-before-start assertions.
- `internal/queue/watchdog.go` — reorder `check()`, add `publishCancelledFallback`, add `CancelTimeout` to config, extend `onKill` hook usage.
- `internal/queue/watchdog_test.go` — new assertions for fallback and reorder.
- `internal/queue/httpstatus.go` — admin guard includes `JobCancelled`.
- `internal/worker/executor.go` — `classifyResult` + `cancelledResult`, pre-`Prepare` ctx check, all failure sites migrated.
- `internal/worker/executor_test.go` — new.
- `internal/worker/pool.go` — `RegisterPending` / `SetStarted` wiring, switch-based queue short-circuit (cancelled vs failed), race re-read covering both, cancelled log branch.
- `internal/worker/pool_test.go` — scenarios A/A'/B/B-race/C + watchdog regression + watchdog fallback.
- `internal/bot/agent.go` — `ctx.Canceled` early-return in provider chain, different log message.
- `internal/bot/agent_test.go` — new or extended.
- `internal/bot/result_listener.go` — store pre-check (Design A), `handleCancellation`, log switch, metrics cancelled branch (both paths).
- `internal/bot/result_listener_test.go` — cancellation + race assertions.
- `internal/config/config.go` — `CancelTimeout` field (yaml `cancel_timeout`, default 60s).
- `internal/config/config_test.go` — default + override assertions.
- `cmd/agentdock/app.go` — cancel button sets `JobCancelled` before sending kill; guard extended; pass `CancelTimeout` into `WatchdogConfig`.

## Risks

- **Incomplete clone on cancel during prep.** We deliberately let in-flight clones finish instead of wiring ctx through `Prepare`. The pre-Prepare ctx guard avoids starting new clones when cancel is already known, limiting wasted work to clones already underway.
- **Watchdog / user-cancel race.** Mitigated by the `check()` reorder and the defensive re-read in `killAndPublish`. If both fire, `ResultListener.processedJobs` dedup drops the loser.
- **Completed-before-cancel race.** Design A's store pre-check in `handleResult` ensures that once the user cancels, no GitHub issue is created from an in-flight completed result. A very narrow window remains (listener reads store micro-seconds before app's `UpdateStatus` lands), in which the issue gets created anyway. Acceptable given the rarity and the alternative complexity of compensating reversal.
- **CancelTimeout tuning.** Too short and the watchdog fires before a slow clone finishes (harmless — dedup drops the late worker result, but extra network chatter). Too long and a crashed worker leaves Slack on `:stop_sign:` longer than ideal. Default 60s covers the typical WaitDelay (10s) + clone slack.
- **Redis-backed store lag.** `classifyResult`'s discriminator is a store read. If Redis replication lag exceeds the command-bus path, a worker could see `JobRunning` instead of `JobCancelled` and classify as `failed`. The app's ordering (UpdateStatus before Send) minimises this; both traverse the same Redis so relative lag is bounded.
- **Admin force-kill intent drift.** Admin force-kill remains `JobFailed` with retry button. If the product decides admin kill should be indistinguishable from user cancel, change `httpstatus.go` to set `JobCancelled` instead; this spec keeps them separate because the semantic intent differs.
