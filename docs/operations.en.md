# Monitoring & Management

[繁體中文](operations.md)

## View Job Status

```bash
curl localhost:8180/jobs | jq .
```

Returns:

```json
{
  "queue_depth": 1,
  "workers": [
    {
      "worker_id": "my-host/worker-0",
      "name": "my-host",
      "connected_at": "2026-04-14T17:41:33+08:00",
      "uptime": "5m30s",
      "current_job": "req-abc123",
      "status": "busy"
    },
    {
      "worker_id": "my-host/worker-1",
      "name": "my-host",
      "connected_at": "2026-04-14T17:41:33+08:00",
      "uptime": "5m30s",
      "status": "idle"
    }
  ],
  "total": 2,
  "jobs": [
    {
      "id": "req-abc123",
      "status": "running",
      "repo": "org/backend",
      "worker_id": "my-host/worker-0",
      "age": "45s",
      "agent": {
        "pid": 12345,
        "command": "claude",
        "alive": true,
        "last_event": "tool_use:Read",
        "last_event_age": "3s",
        "tool_calls": 12,
        "files_read": 8,
        "output_bytes": 15360,
        "cost_usd": 0.042
      }
    },
    {
      "id": "req-def456",
      "status": "pending",
      "repo": "org/frontend",
      "age": "10s",
      "position": 1
    }
  ]
}
```

## Manual Job Termination

```bash
curl -X DELETE localhost:8180/jobs/req-abc123
```

## Slack Cancel

The queue status message posted after submit includes a "Cancel" button.

## Automatic Protection

| Mechanism | Default | Description |
|-----------|---------|-------------|
| Job timeout | 20m | Maximum job lifecycle |
| Agent idle timeout | 5m | stream-json agent auto-terminates after no events |
| Prepare timeout | 3m | Clone/setup timeout auto-terminates |

After timeout, bot notifies the Slack user and clears dedup so users can re-trigger.

## HTTP Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/healthz` | GET | Health check |
| `/jobs` | GET | List all job states (with agent tracking) |
| `/jobs/{id}` | DELETE | Terminate a specific job |

## Agent Behavior

After receiving the prompt, the agent:
1. Loads triage-issue skill
2. Explores codebase (using its own built-in tools)
3. Evaluates confidence (low → reject)
4. Outputs structured JSON result (does not create issue directly):

```json
{
  "status": "CREATED",
  "title": "Login page broken after 3 failed attempts",
  "body": "## Problem\n...",
  "labels": ["bug"],
  "confidence": "high",
  "files_found": 5,
  "open_questions": 0
}
```

App receives result:
- `confidence=low` → don't create issue, notify user
- `files=0` or `questions>=5` → create issue without triage section
- Normal → create full issue + post to Slack thread
