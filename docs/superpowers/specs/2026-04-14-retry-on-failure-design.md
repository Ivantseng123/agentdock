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

Retry creates a **new Job** (new ID) copying the original job's prompt, repo, thread context, branch, and attachments. `RetryCount` is set to `original + 1`.

### 2. Watchdog Publishes to ResultBus

Current flow: Watchdog detects timeout → kills agent → calls `StuckNotifier` callback → callback posts directly to Slack.

New flow: Watchdog detects timeout → kills agent → publishes a `failed` result to `ResultBus`.

Changes to `Watchdog`:
- Remove `StuckNotifier` type and the `notifier` field.
- Add `ResultBus` dependency.
- `killAndNotify()` publishes a `JobResult{Status: "failed", Error: "job terminated: <reason>"}` instead of calling the notifier.

This means all failures — whether from agent execution, infra errors, or watchdog kills — are routed through `ResultListener`.

### 3. ResultListener Unified Failure Handling

`ResultListener.handleResult()` on `status == "failed"`:

```
if job.RetryCount < 1:
    post failure message WITH retry button
else:
    post failure message WITHOUT button (text indicates retry was attempted)
```

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

`SlackPoster` interface gains a new method:

```go
PostBlocks(channelID, threadTS string, blocks []slack.Block) (string, error)
```

`ResultListener` reads `JobState.WorkerID` from `JobStore` to include worker identity in the message.

### 4. Retry Button Interaction Handler

New file: `internal/bot/retry_handler.go`

Handles `block_actions` interaction with `action_id: "retry_job"`:

1. Look up original job from `JobStore` using `value` (job ID).
2. Update the original failure message to `:arrows_counterclockwise: 重試中，已重新排入佇列...` (button removed).
3. Create new `Job` copying: `Prompt`, `Repo`, `CloneURL`, `Branch`, `ChannelID`, `ThreadTS`, `Attachments`, `Skills`, `Priority`. Set `RetryCount = original + 1`, `RetryOfJobID = original.ID`.
4. Submit new job to queue.
5. Set `StatusMsgTS` on the new job to the same message timestamp, so subsequent status updates overwrite it.

Routing: `internal/slack/handler.go` routes `block_actions` with `action_id == "retry_job"` to the retry handler.

### 5. Coexistence with Re-tag Bot

Re-tagging the bot in the same thread triggers the full workflow: re-reads all thread messages, presents repo/branch selector, creates a brand new job. This is completely independent of the retry mechanism.

No conflict because:
- Retry creates a new job with a new ID.
- Thread dedup in `handler.go` checks for running/pending jobs. The original failed job is already in `JobFailed` status, so it won't block a new trigger.

### 6. Worker Identity

Worker ID format: `<hostname>/worker-<index>`

- Native: `Ivans-MacBook-Pro/worker-0`
- Docker: `a1b2c3d4/worker-0` (container short ID from `os.Hostname()`)

Where it's used:
- `StatusReport.WorkerID` — already exists, currently only `worker-0`. Change to include hostname.
- `JobState.WorkerID` — already exists. Pool calls `SetWorker()` after Ack.
- Failure messages in Slack — `ResultListener` reads from `JobState.WorkerID`.

Implementation: `Pool` receives hostname at construction. Each worker goroutine uses `<hostname>/worker-<index>`.

## Data Flow

```
First attempt:
  trigger → workflow → Submit(Job{RetryCount:0}) → worker executes
    → success: ResultListener → create issue → post URL
    → failure: ResultListener → post error + retry button

Watchdog timeout:
  watchdog → kill + publish failed result to ResultBus
    → ResultListener → post error + retry button

User clicks retry:
  interaction handler → update message to "retrying..."
    → Submit(Job{RetryCount:1, RetryOfJobID:original}) → worker executes
      → success: ResultListener → update same message to issue URL
      → failure: ResultListener → update same message to error (no button)

User re-tags bot:
  independent new workflow → new thread context → repo selection → new job
```

## Files Changed

| File | Change |
|------|--------|
| `internal/queue/job.go` | Add `RetryCount`, `RetryOfJobID` to `Job` |
| `internal/queue/watchdog.go` | Remove `StuckNotifier`, add `ResultBus` dependency |
| `cmd/bot/main.go` | Update Watchdog wiring: remove notifier, pass ResultBus |
| `internal/bot/result_listener.go` | Unified failure handling: check RetryCount, post with/without button, show worker |
| `internal/slack/client.go` | Add `PostBlocks` to `SlackPoster` interface |
| `internal/bot/retry_handler.go` | New: retry button interaction handler |
| `internal/slack/handler.go` | Route `block_actions` `retry_job` to retry handler |
| `internal/worker/pool.go` | Worker ID uses hostname/index; call `SetWorker()` after Ack |
| `cmd/bot/worker.go` | Pass `os.Hostname()` to Pool at startup |

## Testing

- Unit test: `ResultListener` posts button when `RetryCount == 0`, no button when `RetryCount == 1`.
- Unit test: retry handler creates new job with correct fields and `RetryCount + 1`.
- Unit test: Watchdog publishes failed result to ResultBus (no longer calls notifier).
- Unit test: worker ID format includes hostname.
- Integration: trigger failure → see button → click retry → job re-executes.
