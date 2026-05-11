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
| `/metrics` | GET | Prometheus metrics (not mounted when `metrics.enabled: false` — returns 404) |

All served on `server.port`; with `server.port: 0` the whole HTTP server is off.

## Metrics (Prometheus)

The app exposes `agentdock_*` metrics at `/metrics` (pull model; configuration in [configuration-app.md](configuration-app.md), rationale in [adr/0005-metrics-prometheus.md](adr/0005-metrics-prometheus.md)). The PromQL below is written against the **existing** metrics.

### The four oncall questions

```promql
# 1. Triage success rate over the last hour
sum(rate(agentdock_agent_executions_total{status="success"}[1h]))
  / sum(rate(agentdock_agent_executions_total[1h]))

# 2. P95 agent execution time (per provider)
histogram_quantile(0.95,
  sum by (le, provider) (rate(agentdock_agent_execution_seconds_bucket[5m])))

# 3. P95 queue wait
histogram_quantile(0.95, rate(agentdock_queue_wait_seconds_bucket[5m]))

# 4. Worst-failing agents (top 3)
topk(3, sum by (provider) (rate(agentdock_agent_executions_total{status!="success"}[1h])))
```

### Stitching phase latency

v3 does not ship a unified `job_duration_seconds{phase}` (it would re-observe the intervals below — see ADR-0005). Each phase has its own histogram; add them up for the end-to-end view:

```promql
# queue wait (P95)
histogram_quantile(0.95, rate(agentdock_queue_wait_seconds_bucket[5m]))

# agent env prep: clone / checkout / skill mount (P95)
histogram_quantile(0.95, rate(agentdock_agent_prepare_seconds_bucket[5m]))

# agent execution (P95, per provider)
histogram_quantile(0.95,
  sum by (le, provider) (rate(agentdock_agent_execution_seconds_bucket[5m])))

# sum of per-phase means ≈ end-to-end mean (result-post / external-API leg is covered by external_duration_seconds)
  (rate(agentdock_queue_wait_seconds_sum[5m])      / rate(agentdock_queue_wait_seconds_count[5m]))
+ (rate(agentdock_agent_prepare_seconds_sum[5m])   / rate(agentdock_agent_prepare_seconds_count[5m]))
+ (sum(rate(agentdock_agent_execution_seconds_sum[5m])) / sum(rate(agentdock_agent_execution_seconds_count[5m])))

# result-post / external API (Slack / GitHub) leg
histogram_quantile(0.95,
  sum by (le, service, operation) (rate(agentdock_external_duration_seconds_bucket[5m])))
```

### Anomaly thresholds

```promql
# Distinct exit-code values > 16 → treat as worker anomaly, investigate worker/agent/runner.go
# (agent_exit_code_total only records real codes; the -1 sentinel is excluded. Normally very few: 0/1/124/137 and the like)
count(count by (exit_code) (agentdock_agent_exit_code_total)) > 16

# Non-zero exit-code ratio (rolling 1h)
sum(rate(agentdock_agent_exit_code_total{exit_code!="0"}[1h]))
  / sum(rate(agentdock_agent_exit_code_total[1h]))

# slack_events_total{type="unknown"} share > 5% → a new Slack event type is unclassified;
# add a case to EventTypeLabel in app/slack/event_label.go
sum(rate(agentdock_slack_events_total{type="unknown"}[1h]))
  / sum(rate(agentdock_slack_events_total[1h])) > 0.05
```

## Agent Behavior

Each workflow has its own skill, its own JSON fence marker, and its own app-side follow-up.

### Issue (`@bot issue` / legacy bare-repo)

1. Loads the `triage-issue` skill
2. Explores the cloned repo with the agent's built-in tools
3. Evaluates confidence (low → reject)
4. Emits `===TRIAGE_RESULT===` followed by JSON:

```
===TRIAGE_RESULT===
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

App side:
- `confidence=low` → don't create the issue; notify the user
- `files_found=0` or `open_questions>=5` → create issue but omit the triage section
- Otherwise → create the full issue and post the URL back to the thread

### Ask (`@bot ask <question>`)

1. Loads the `ask-assistant` skill
2. Reads `<issue_context>` (thread, `<bot>` handle, optional attached repo)
3. Classifies intent per the skill (codebase question / general question / out of scope)
4. Emits `===ASK_RESULT===` followed by JSON:

```
===ASK_RESULT===
{"answer": "<markdown answer in Slack mrkdwn, ≤30000 chars>"}
```

App side: posts `answer` directly into the thread; no issue is created. Over-length answers are truncated with a warning suffix.

### PR Review (`@bot review <PR URL>`; on by default, set `pr_review.enabled: false` to disable)

1. Loads the `github-pr-review` skill
2. Reads the PR diff (the skill instructs the agent to use the `agentdock pr-review-helper` subcommand for fetching + posting)
3. The agent itself posts line-level comments and a summary review on the PR
4. Emits `===REVIEW_RESULT===` followed by JSON — three possible statuses:

```
===REVIEW_RESULT===
{"status": "POSTED", "summary": "...", "severity_summary": "major|minor|nit"}
{"status": "SKIPPED", "reason": "lockfile_only", "summary": "..."}
{"status": "ERROR", "summary": "..."}
```

App side: does not touch the PR (the agent posts its own review); only the status/summary is reported back to the thread.
