# Local Logging Design

## Goal

Add comprehensive local logging so the bot can be run locally for testing, with all logs persisted to disk for debugging and usage analytics.

## Requirements

- **Dual output**: stderr (text, human-readable) + file (JSONL, machine-parseable)
- **Independent log levels**: stderr controlled by existing `log_level`, file level controlled by new `logging.level`
- **Daily file rotation**: one `.jsonl` file per day, auto-cleanup after configurable retention period (default 30 days)
- **Agent output isolation**: agent raw output saved as separate `.md` files, referenced by path in main log
- **Request correlation**: every triage workflow carries a `request_id` + context attrs through all log entries
- **Zero external dependencies**: built on stdlib `log/slog` only

## Config

New `logging` section in YAML config:

```yaml
logging:
  dir: "logs"                           # log file directory
  level: "debug"                        # file log level (independent of stderr log_level)
  retention_days: 30                    # auto-cleanup threshold
  agent_output_dir: "logs/agent-outputs" # agent output directory
```

Existing `log_level` field continues to control stderr output level.

### Defaults

| Field | Default |
|-------|---------|
| `dir` | `"logs"` |
| `level` | `"debug"` |
| `retention_days` | `30` |
| `agent_output_dir` | `"logs/agent-outputs"` |

## Architecture

### New package: `internal/logging/`

Three files:

**`handler.go`** — `MultiHandler` implements `slog.Handler`
- Holds two inner handlers: `TextHandler` (stderr) + `JSONHandler` (file)
- `Handle()` fans out to both, each with its own level filter
- `WithAttrs()` / `WithGroup()` propagate correctly to both inner handlers

**`rotator.go`** — Date-based file rotation + cleanup
- Wraps `io.Writer`; on each `Write()`, checks if the date has changed and opens a new file if so
- File naming: `YYYY-MM-DD.jsonl`
- Cleanup goroutine runs hourly, deletes `.jsonl` files older than `retention_days`. On deletion failure: log warning and continue (best-effort cleanup). Cleanup only targets files from previous days — never the current day's active file, so no write/delete race condition.

**`agent.go`** — Agent output persistence
- `SaveAgentOutput(requestID, repo, output string) (string, error)` — writes to `{agent_output_dir}/{requestID}.md`, returns file path (date is already encoded in requestID)
- Caller logs `slog.Info("agent output saved", "path", path, "length", len(output))`

### Request correlation

On each `HandleTrigger`:
1. Generate short ID: `YYYYMMDD-HHmmss-<4 hex chars>` (e.g. `20260410-143052-a3f8`)
2. Create scoped logger: `slog.With("request_id", id, "channel_id", ..., "user_id", ..., "repo", ...)`
3. Store logger in `pendingTriage.logger`
4. All downstream methods (`runTriage`, etc.) use `pt.logger` instead of global `slog`

### JSONL log entry example

```json
{"time":"2026-04-10T14:30:52+08:00","level":"INFO","msg":"agent output saved","request_id":"20260410-143052-a3f8","channel_id":"C05XXX","user_id":"U123","repo":"org/backend","path":"logs/agent-outputs/20260410-143052-a3f8.md","length":4523}
```

### Querying

```bash
# Full trace for one triage request
jq 'select(.request_id == "20260410-143052-a3f8")' logs/2026-04-10.jsonl

# All errors today
jq 'select(.level == "ERROR")' logs/2026-04-10.jsonl

# Usage analytics: triage count per user
jq 'select(.msg == "triage result") | .user_id' logs/2026-04-10.jsonl | sort | uniq -c

# All agent outputs for a repo
jq 'select(.msg == "agent output saved" and .repo == "org/backend") | .path' logs/2026-04-10.jsonl
```

## Files changed

| File | Change |
|------|--------|
| `internal/logging/handler.go` | **New** — MultiHandler (slog.Handler fan-out) |
| `internal/logging/rotator.go` | **New** — Date-based file rotation + cleanup |
| `internal/logging/agent.go` | **New** — Agent output file writer |
| `internal/config/config.go` | Add `LoggingConfig` struct + defaults |
| `cmd/bot/main.go` | Init MultiHandler, start cleanup goroutine |
| `internal/bot/workflow.go` | Add `requestID`/`logger` to pendingTriage, use scoped logger, save agent output |
| `internal/bot/agent.go` | Accept `*slog.Logger` param, use scoped logger |

Files NOT changed: `slack/client.go`, `github/repo.go`, `github/discovery.go`, `mantis/client.go` — these continue using global `slog`.

## Out of scope

- Remote log shipping (ELK, CloudWatch, etc.)
- Log search UI
- Metrics/prometheus integration
- Structured event bus / event sourcing
