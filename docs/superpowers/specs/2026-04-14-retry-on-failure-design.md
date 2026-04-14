# Retry on Failure Design

**Date:** 2026-04-14
**Status:** Approved

## Problem

When a worker crashes, times out, or an agent fails mid-execution, the app relies on watchdog timeout detection to notify the user via Slack. The user must then manually re-tag the bot to retry. This is slow (waits for full timeout) and friction-heavy (requires re-triggering the entire workflow).

## Goals

1. All failure types (watchdog kill, agent error, infra error) are retryable via a Slack button.
2. Failure reason is visible in the Slack message.
3. Maximum 1 retry per job. Second failure shows error only, no button.
4. Re-tagging the bot remains an independent path (re-reads thread context, re-selects repo).
5. Worker identity (hostname + index) is visible in status and failure messages.

## Non-Goals

- Automatic retry without user action.
- Retry count > 1.
- `WORKER_NAME` environment variable override.
- Re-queuing via Redis pending entry list (PEL) recovery.

## Design

### 1. Job Model Changes

Add two fields to `Job`:

```go
type Job struct {
    // ... existing fields
    RetryCount   int    `json:"retry_count"`      // 0 = first attempt, 1 = retried
    RetryOfJobID string `json:"retry_of_job_id"`  // original job ID for tracing
}
```

Retry creates a **new Job** (new ID via `logging.NewRequestID()`) copying the original job's prompt, repo, thread context, branch, user ID, attachments, and skills. `RetryCount` is set to `original + 1`.

**Bug fix (included in scope):** `workflow.go` currently does not set `UserID` on the Job when submitting. Add `UserID` from `pendingTriage` (sourced from `TriggerEvent.UserID`).

### 2. Watchdog Publishes to ResultBus

Current flow: Watchdog detects timeout → kills agent → calls `StuckNotifier` callback → callback posts directly to Slack.

New flow: Watchdog detects timeout → kills agent → marks job as failed → publishes a `failed` result to `ResultBus`.

Changes to `Watchdog`:
- Remove `StuckNotifier` type and the `notifier` field.
- Add `ResultBus` dependency.
- `killAndNotify()` does three things:
  1. Send kill command via `CommandBus` (unchanged).
  2. `store.UpdateStatus(jobID, JobFailed)` — prevents watchdog from re-processing on next tick.
  3. Publish `JobResult{Status: "failed", Error: "job terminated: <reason>"}` to `ResultBus`.
- Remove `FormatStuckMessage()` helper (dead code after this change).

**Double-publish:** Both watchdog and the dying worker may publish a failed result for the same job. This is handled by `ResultListener`'s dedup guard (see Section 3).

### 3. ResultListener Unified Failure Handling

**Dedup guard:** `ResultListener` maintains an in-memory `processedJobs map[string]bool`. When a result arrives, if the job ID is already in the map, drop it. This prevents double-processing from the watchdog + worker race without relying on store status (which the watchdog sets before publishing).

`ResultListener.handleResult()` on `status == "failed"`:

```
if processedJobs[jobID]:
    drop (duplicate)
    return

processedJobs[jobID] = true

if job.RetryCount < 1:
    post failure message WITH retry button (via PostMessageWithButton)
    do NOT clear thread dedup (user can click retry; tag-bot is blocked while button exists)
else:
    post failure message WITHOUT button (text indicates retry was attempted)
    clear thread dedup (allow re-tag bot)

cleanup attachments
```

**Thread dedup strategy:**
- `RetryCount < 1` (button shown): Do NOT clear dedup. User either clicks retry or waits for dedup TTL to expire before re-tagging.
- `RetryCount >= 1` (no button): Clear dedup immediately. User can only re-tag bot.
- This prevents the race window between clear-dedup and retry-submit where a re-tag could sneak in.

Message format with button:
```
:x: 分析失敗: <error reason>
repo: owner/repo | worker: Ivans-MacBook-Pro/worker-0

[🔄 重試]   ← Slack button (action_id: "retry_job", value: job.ID)
```

Message format after retry exhausted:
```
:x: 分析失敗（重試後仍失敗）: <error reason>
repo: owner/repo | worker: a1b2c3d4/worker-0
```

**Interface changes:** Promote existing `PostMessageWithButton` from `slack.Client` to `SlackPoster` interface:

```go
type SlackPoster interface {
    PostMessage(channelID, text, threadTS string)
    UpdateMessage(channelID, messageTS, text string)
    PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value string) (string, error)
}
```

No new methods needed — `PostMessageWithButton` already exists on `slack.Client`. It must be added to the `SlackPoster` interface and the `slackPosterAdapter` in `main.go`.

**Thread dedup callback:** `ResultListener` receives a `func(channelID, threadTS string)` callback for clearing dedup, rather than depending on `slack.Handler` directly.

**Worker identity:** `ResultListener` reads `WorkerID` from `JobState`. In Redis mode, `WorkerID` reaches the bot's `JobStore` via `StatusReport` → `StatusBus` → `StatusListener` → `SetAgentStatus()`. The bot reads it from `state.AgentStatus.WorkerID`.

### 4. Retry Button Interaction Handler

New file: `internal/bot/retry_handler.go` — a `RetryHandler` struct (not inline in main.go).

**Dependencies:**
- `JobStore` — `Get()` original job, `Put()` new job
- `JobQueue` (or `Coordinator`) — `Submit()` new job
- `SlackPoster` — `UpdateMessage()` + `PostMessageWithButton()`

Handles `block_actions` interaction with `action_id: "retry_job"`:

1. Look up original job from `JobStore` using `value` (job ID). If not found (TTL expired), post error message and return.
2. If original job is not in `JobFailed` state (stale button), ignore.
3. Update the original failure message to `:arrows_counterclockwise: 已重新排入佇列`.
4. Create new `Job` with `logging.NewRequestID()`, copying: `Prompt`, `Repo`, `CloneURL`, `Branch`, `ChannelID`, `ThreadTS`, `UserID`, `Attachments`, `Skills`, `Priority`. Set `RetryCount = original + 1`, `RetryOfJobID = original.ID`, `SubmittedAt = time.Now()`.
5. `store.Put(newJob)` — must happen before PostMessageWithButton so cancel_job can find the job.
6. `queue.Submit(newJob)`.
7. Post new status message via `PostMessageWithButton` with `cancel_job` button (retry job also gets a cancel button during execution).
8. Update `newJob.StatusMsgTS` with the new message timestamp; `store.Put(newJob)` again.

Routing: `main.go` switch case for `InteractionTypeBlockActions` routes `action_id == "retry_job"` to `retryHandler.Handle()`.

### 5. Coexistence with Re-tag Bot

Re-tagging the bot in the same thread triggers the full workflow: re-reads all thread messages, presents repo/branch selector, creates a brand new job. This is completely independent of the retry mechanism.

No conflict because:
- When retry button is shown (`RetryCount < 1`): dedup is NOT cleared. Re-tag is blocked. User must use the button.
- When no button (`RetryCount >= 1`): dedup IS cleared. Re-tag works.
- After dedup TTL expires (5 minutes): re-tag works regardless.

### 6. Worker Identity

Worker ID format: `<hostname>/worker-<index>`

- Native: `Ivans-MacBook-Pro/worker-0`
- Docker: `a1b2c3d4/worker-0` (container short ID from `os.Hostname()`)

Where it's used:
- `StatusReport.WorkerID` — already exists, currently only `worker-0`. Change to include hostname.
- `JobState.WorkerID` — already exists but `SetWorker()` is never called today. Add call in pool after Ack (new behavior).
- In Redis mode, `WorkerID` propagates to the bot via `StatusReport` → `StatusBus` → `StatusListener` → `SetAgentStatus()`.
- Failure messages in Slack — `ResultListener` reads from `JobState`.

Implementation: `Pool` receives hostname at construction time (`cmd/bot/worker.go` calls `os.Hostname()`). Each worker goroutine uses `<hostname>/worker-<index>`.

## Data Flow

```
First attempt:
  trigger → workflow → Submit(Job{RetryCount:0}) → worker executes
    → success: ResultListener → create issue → post URL → clear dedup
    → failure: ResultListener → dedup guard (first result wins)
               → post error + retry button (dedup NOT cleared)

Watchdog timeout:
  watchdog → kill + UpdateStatus(JobFailed) + publish failed result
  worker   → context cancelled → also publishes failed result
  ResultListener → first result: processedJobs guard passes → post error + retry button
                 → second result: processedJobs guard drops it

User clicks retry:
  retry handler → check job is JobFailed
    → update old message to "已重新排入佇列"
    → Put(newJob) → Submit(newJob) → PostMessageWithButton (with cancel button)
    → worker executes
      → success: ResultListener → create issue → update to issue URL → clear dedup
      → failure: ResultListener → post error (no button) → clear dedup

User re-tags bot (after dedup cleared or TTL expired):
  independent new workflow → new thread context → repo selection → new job
```

## Files Changed

| File | Change |
|------|--------|
| `internal/queue/job.go` | Add `RetryCount`, `RetryOfJobID` to `Job` |
| `internal/queue/watchdog.go` | Remove `StuckNotifier`, `FormatStuckMessage`; add `ResultBus`; `killAndNotify` keeps `UpdateStatus` + publishes result |
| `cmd/bot/main.go` | Update Watchdog wiring: remove notifier callback, pass ResultBus; add `PostMessageWithButton` to `slackPosterAdapter`; route `retry_job` action to `RetryHandler` |
| `internal/bot/result_listener.go` | Add `processedJobs` map; unified failure handling with RetryCount; conditional dedup clear via callback; read WorkerID; promote `PostMessageWithButton` to `SlackPoster` interface |
| `internal/bot/retry_handler.go` | New: `RetryHandler` struct (deps: JobStore, JobQueue, SlackPoster) |
| `internal/bot/workflow.go` | Bug fix: set `UserID` on Job at submission |
| `internal/slack/handler.go` | No change needed (routing is in main.go switch) |
| `internal/worker/pool.go` | Worker ID uses hostname/index; call `SetWorker()` after Ack |
| `cmd/bot/worker.go` | Pass `os.Hostname()` to Pool at startup |

## Testing

- Unit test: `ResultListener` posts button when `RetryCount == 0`, no button when `RetryCount == 1`.
- Unit test: `ResultListener` `processedJobs` guard drops duplicate result for same job ID.
- Unit test: `ResultListener` does NOT clear dedup when button is shown; clears dedup when no button.
- Unit test: `RetryHandler` creates new job with correct fields (`UserID`, `RetryCount + 1`, new ID, etc.).
- Unit test: `RetryHandler` ignores click if job is not in `JobFailed` state (stale button).
- Unit test: `RetryHandler` returns graceful error if job not found (TTL expired).
- Unit test: `RetryHandler` posts new status message with cancel button.
- Unit test: Watchdog publishes failed result to ResultBus and calls `UpdateStatus(JobFailed)`.
- Unit test: worker ID format includes hostname.
- Integration: trigger failure → see button → click retry → job re-executes with cancel button.
