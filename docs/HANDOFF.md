# Session Handoff — Queue Decoupling + Agent Tracking

> **Historical document (pre-v2).** Paths in this file (`internal/queue/...`, `internal/worker/...`, `cmd/bot/...`, `config.yaml`, `transport: inmem`) reflect the queue-decoupling session from 2026-04-10, before the v1 cobra migration and v2 app/worker module split. For current architecture see `README.md`, `docs/configuration.md`, and `shared/queue/` / `app/` / `worker/`. Kept for history.


## What Was Done

Two major features implemented end-to-end in this session:

### Phase 1: Queue-Based App-Agent Decoupling (15 tasks, all complete)
Replaced semaphore-based concurrency with a priority queue architecture.

- **5 transport interfaces**: `JobQueue`, `ResultBus`, `AttachmentStore`, `CommandBus`, `StatusBus` (`internal/queue/interface.go`)
- **In-memory implementations**: each in its own file (`inmem_jobqueue.go`, `inmem_resultbus.go`, etc.), bundled via `inmem_bundle.go`
- **Priority queue**: `container/heap` with channel-based priority + FIFO within same priority (`priority.go`)
- **Worker pool**: N goroutines consuming from JobQueue, each job gets its own context (`internal/worker/pool.go`, `executor.go`)
- **ResultListener**: subscribes to ResultBus, creates GitHub issues, posts to Slack (`bot/result_listener.go`)
- **Skill mounting**: skills embedded in `Job.Skills`, worker writes them to cloned repo workspace
- **Two-phase attachments**: metadata in Job, download after worker Ack via AttachmentStore
- **Issue creation moved to app**: workers return structured JSON, app creates issues (security: workers don't need GH_TOKEN write)

### Phase 2: Agent Process Tracking + Kill (14 tasks, all complete)
Real-time agent status visibility and four kill triggers.

- **ProcessRegistry**: cancel-based kill with 15s timeout (`queue/registry.go`)
- **Stream-json parser**: parses claude's NDJSON output for tool_use, cost, tokens (`queue/stream.go`)
- **Per-call RunOptions**: `OnStarted(pid, command)` + `OnEvent(StreamEvent)` callbacks, avoids shared-state races (`bot/agent.go`)
- **StatusBus**: worker reports agent status every 5s (`worker/status.go` accumulator → `inmem_statusbus.go`)
- **StatusListener**: updates JobStore from StatusBus reports (`bot/status_listener.go`)
- **Watchdog**: 3-tier timeout — job (20m), agent idle (5m), prepare (3m) (`queue/watchdog.go`)
- **CommandBus**: kill signals from app → worker (`inmem_commandbus.go`)
- **HTTP endpoints**: `GET /jobs` (with nested agent status), `DELETE /jobs/{id}` (`queue/httpstatus.go`)
- **Slack cancel button**: interactive button on queue position message
- **cmd.Cancel + WaitDelay**: SIGTERM first, auto-SIGKILL after 10s (`bot/agent.go`)
- **Post-kill cleanup**: `git checkout . && git clean -fd` on failed jobs

## Bug Fixed at End of Session

**Status reports showing `{}`**: `reportStatus` goroutine started before `OnStarted` callback fired → all fields zero. Fixed by moving start inside `OnStarted` callback + sending immediate first report. Commit: `84f3d5e`.

## Current State

- **Branch**: `main` (48 commits ahead of `origin/main`, not pushed)
- **Tests**: 101 passing (`go test ./...`)
- **Untracked files**: `docs/superpowers/plans/2026-04-10-local-logging.md` (unrelated)
- **No uncommitted changes**

## What Needs Testing

User needs to rebuild and do a live test after the status reporting bug fix:

```bash
go build -o bot ./cmd/bot/ && ./bot -config config.yaml
```

Then:
1. Trigger a triage in Slack (`@bot` in a thread)
2. Verify queue position message appears with cancel button
3. Check agent status: `curl -s localhost:8180/jobs | jq ".jobs[0].agent"`
   - Should show real `pid`, `command`, `tool_calls`, `files_read`, `cost_usd`, etc.
   - Previously showed `{}` — should be fixed now
4. Test kill: `curl -X DELETE localhost:8180/jobs/{id}`
5. Test Slack cancel button
6. Let a job complete end-to-end → verify GitHub issue created + posted to Slack

## Key Files Changed (by area)

| Area | Files |
|------|-------|
| Queue interfaces | `internal/queue/interface.go`, `job.go` |
| In-memory transport | `internal/queue/inmem_*.go`, `inmem_bundle.go` |
| Priority queue | `internal/queue/priority.go` |
| Job store | `internal/queue/memstore.go` |
| Stream parsing | `internal/queue/stream.go` |
| Process registry | `internal/queue/registry.go` |
| Watchdog | `internal/queue/watchdog.go` |
| HTTP status | `internal/queue/httpstatus.go` |
| Worker pool | `internal/worker/pool.go`, `executor.go`, `status.go` |
| Agent runner | `internal/bot/agent.go` (RunOptions, cmd.Cancel) |
| Result handling | `internal/bot/result_listener.go`, `status_listener.go` |
| Workflow | `internal/bot/workflow.go` (queue.Submit instead of direct exec) |
| Parser | `internal/bot/parser.go` (JSON format + legacy fallback) |
| Prompt | `internal/bot/prompt.go` (removed repo/labels — agent no longer creates issues) |
| Slack | `internal/slack/client.go` (PostMessageWithButton), `handler.go` (removed semaphore) |
| Config | `internal/config/config.go` (queue, workers, channel_priority, stream) |
| Skill | `agents/skills/triage-issue/SKILL.md` (JSON output instead of gh issue create) |
| Entry point | `cmd/bot/main.go` (full wiring) |

## Design Specs & Plans

- `docs/superpowers/specs/2026-04-10-queue-decoupling-design.md`
- `docs/superpowers/specs/2026-04-10-agent-tracking-kill-design.md`
- `docs/superpowers/plans/2026-04-10-queue-decoupling.md`
- `docs/superpowers/plans/2026-04-10-agent-tracking-kill.md`

## Config (config.yaml)

```yaml
queue:
  capacity: 50
  transport: inmem
  job_timeout: 20m
  agent_idle_timeout: 5m
  prepare_timeout: 3m
  status_interval: 5s

workers:
  count: 3

channel_priority:
  default: 50

server:
  port: 8180   # enables /healthz, /jobs, /jobs/{id}
```

## Architecture Summary

```
Slack trigger → Handler (dedup + rate limit) → Workflow.runTriage
  → queue.Submit(job with priority, skills, attachments)
  → Worker pool (N goroutines) picks up job
    → clone repo → mount skills → spawn CLI agent with RunOptions
    → stream-json parsing → StatusBus reports every 5s
    → agent returns structured JSON
  → ResultListener receives result
    → creates GitHub issue (app-side) → posts URL to Slack
  
Kill paths:
  1. HTTP DELETE /jobs/{id} → CommandBus → worker cancels context
  2. Watchdog timeout → CommandBus → worker cancels context
  3. Agent idle detection → CommandBus → worker cancels context
  4. Slack cancel button → CommandBus → worker cancels context
  All → SIGTERM → 10s wait → SIGKILL → git cleanup
```
