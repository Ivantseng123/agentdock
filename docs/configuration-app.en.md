# App configuration (`app.yaml`)

[繁體中文](configuration-app.md)

YAML consumed by `agentdock app`. Default path: `~/.config/agentdock/app.yaml`. Run `agentdock init app -i` to generate a commented template.

## Schema

```yaml
log_level: info                       # console / stderr level: debug | info | warn | error

server:
  port: 8080                          # /healthz, /jobs, /metrics HTTP endpoints

slack:
  bot_token: xoxb-...                 # REQUIRED
  app_token: xapp-...                 # REQUIRED

github:
  token: ghp-...                      # REQUIRED: list repos, create issues

channels:
  C0123456789:
    repos: [owner/repo-a, owner/repo-b]
    default_labels: [triage]
    branches: [main, release]
    branch_select: true

channel_defaults:
  branch_select: true
  default_labels: [from-slack]

auto_bind: true                       # bind channels automatically on join
max_thread_messages: 50               # how many thread messages to read into the prompt
semaphore_timeout: 30s

rate_limit:
  per_user: 5
  per_channel: 10
  window: 1m

mantis:
  base_url: https://mantis.example.com    # host root; no /api/rest suffix needed
  api_token: xxxxx                        # both fields must be set, or both empty

channel_priority:
  C_INCIDENTS: 100
  default: 50

prompt:
  language: English
  allow_worker_rules: true            # whether worker.prompt.extra_rules is rendered

  # One goal + response_schema + output_rules block per workflow. Unset fields fall back to hardcoded defaults.
  issue:
    goal: "Use the /triage-issue skill to investigate and produce a triage result."
    response_schema: |
      Your final response MUST end with ONE of these three shapes after ===TRIAGE_RESULT===:
      CREATED  → {"status":"CREATED","title":"<required>","body":"...","labels":[...],"confidence":"high|medium","files_found":<int>,"open_questions":<int>}
      REJECTED → {"status":"REJECTED","message":"..."}
      ERROR    → {"status":"ERROR","message":"..."}
    output_rules: []                  # formatting rules live in the triage-issue skill's SKILL.md body template, not here
  ask:
    goal: "Answer the user's question using the thread, and (if a codebase is attached) the repo. Follow the ask-assistant skill for scope, boundaries, and punt rules."
    response_schema: |
      Your final response MUST end with this exact block (no leading whitespace, no markdown fence around it):

      ===ASK_RESULT===
      {"answer": "<your full markdown answer as a single JSON string>"}

      The JSON key MUST be literally "answer". Do NOT use "text", "content", "response" or any synonym.
    output_rules:
      - "Format the answer in Slack mrkdwn — NOT GitHub markdown ..."
      - "No title, no labels — output the answer content only. Keep it ≤30000 chars."
      - "When referring to yourself, use the exact Slack handle from the <bot> tag ..."
  pr_review:
    goal: "Review the PR. Use the github-pr-review skill to analyze the diff and post line-level comments plus a summary review via agentdock pr-review-helper."
    response_schema: |
      Your final response MUST end with ONE of these three shapes after ===REVIEW_RESULT===:
      POSTED  → {"status":"POSTED","summary":"...","comments_posted":<int>,"comments_skipped":<int>,"severity_summary":"clean|minor|major"}
      SKIPPED → {"status":"SKIPPED","summary":"...","reason":"lockfile_only|vendored|generated|pure_docs|pure_config"}
      ERROR   → {"status":"ERROR","error":"<diagnostic>","summary":"<what you would have posted>"}
    output_rules:
      - "Focus on correctness, security, style"
      - "Summary ≤ 2000 chars"

  # Legacy flat fields (pre-v2.1). Only copied into prompt.issue.* if prompt.issue.* is
  # unset. New configs should write prompt.issue.* directly.
  # goal: "..."
  # output_rules: []

pr_review:
  enabled: true                       # PR Review workflow feature flag; on by default — set `false` to opt out

skills_config: /etc/agentdock/skills.yaml   # optional dynamic skill loader config

attachments:
  store: ""                           # reserved for future backends
  temp_dir: /tmp/triage-attachments
  ttl: 30m

repo_cache:
  dir: /var/cache/agentdock/repos     # must be an absolute path
  max_age: 10m

queue:
  capacity: 50
  transport: redis                    # extension point; only redis is supported today
  store: mem                          # JobStore backend: mem (default; lost on restart) or redis (persisted; in-flight jobs survive app restart)
  store_ttl: 1h                       # TTL for each RedisJobStore key when store=redis; refreshed on every write
  job_timeout: 20m                    # watchdog: max job lifecycle
  agent_idle_timeout: 5m              # stream-json: no-event timeout
  prepare_timeout: 3m
  cancel_timeout: 60s
  status_interval: 5s

logging:
  dir: logs
  level: debug                        # rotated file level
  retention_days: 30
  agent_output_dir: logs/agent-outputs

redis:
  addr: redis:6379                    # REQUIRED when queue.transport=redis
  password: ""
  db: 0
  tls: false

secret_key: 0123456789abcdef...       # 64 hex chars (32-byte AES-256), REQUIRED in redis mode

secrets:
  GH_TOKEN: ghp_xxx                   # key = env var name, value = plaintext; encrypted before sending to worker
  K8S_TOKEN: your-k8s-token
```

## Workflow-specific prompts

`prompt.issue` / `prompt.ask` / `prompt.pr_review` each carry their own `goal` + `response_schema` + `output_rules`:
- `goal` is the **task description** — what to do, which skill to invoke (`triage-issue` / `ask-assistant` / `github-pr-review`). Keep output format OUT of the goal.
- `response_schema` is the **machine-readable output contract** — marker + JSON shape (`===ASK_RESULT===` / `===REVIEW_RESULT===`, etc). This section is **NOT** XML-escaped in the rendered prompt — literal `"` and `<` reach the LLM verbatim, so weaker models don't copy `&quot;` into their output and break downstream JSON parsing.
- `output_rules` are **formatting rules** (Slack mrkdwn, length caps, self-reference handle) rendered at the end of the prompt, XML-escaped.
- Any unset field is filled from `app/config/defaults.go` (`defaultIssueGoal` / `defaultAskGoal` / `defaultPRReviewGoal` / `defaultIssueResponseSchema` / `defaultAskResponseSchema` / `defaultPRReviewResponseSchema` / `defaultAskOutputRules` / `defaultPRReviewOutputRules`). `issue.output_rules` defaults to empty — formatting rules live in the triage-issue skill's SKILL.md body template, not here.

**Legacy alias**: the flat `prompt.goal` / `prompt.output_rules` fields are copied into `prompt.issue.*` at load time, but only if `prompt.issue.*` is empty. This keeps pre-v2.1 yaml valid; new configs should write `prompt.issue.*` directly.

## Job store (`queue.store`)

The JobStore holds `JobState` for every in-flight job (status, worker assignment, latest agent status report, thread ↔ jobID mapping). Whether this state survives an app restart depends on the backend:

| Value | Storage | Restart behaviour |
|---|---|---|
| `mem` (default) | app process memory | All in-flight state is lost. When the worker publishes its final JobResult, the ResultListener can't find the JobState, logs `找不到工作結果對應的工作`, and drops it — the Slack hourglass never updates |
| `redis` | Redis keys `jobstore:<jobID>` + secondary index `jobstore:thread:<channel>:<ts>` | State is persisted across processes; a new app instance sees the old JobState and the ResultListener delivers the final Slack message and clears dedup as usual |

**Pick `redis` when:**
- Running in production (redeploys / OOM kills must not abandon in-flight review or triage jobs)
- Multiple app pods share the same Slack workspace
- Long-running tasks (e.g. PR review taking 5–10 minutes)

**`mem` is still fine for:**
- Local development / testing
- Single-pod deployments where restarts are rare and stranded hourglass messages are tolerable

**How to switch**: set `queue.store: redis` and optionally tune `queue.store_ttl` (default 1h; refreshed on every write so long-running tasks don't expire mid-flight). `redis.addr` must be set (required by `queue.transport: redis` anyway).

When `queue.store` is `redis`, the app logs the number of rehydrated in-flight jobs on startup — useful for confirming the new instance actually picked up state from the previous one.

Background & design notes: issue [#123](https://github.com/Ivantseng123/agentdock/issues/123).

## PR Review

`pr_review.enabled` **defaults to `true`** (the `github-pr-review` skill and `agentdock pr-review-helper` subcommand both ship in the release image, so opt-in was just ceremony). To turn it off, set `pr_review.enabled: false` explicitly.

`@bot review <PR URL>` routes to PRReviewWorkflow; with no URL, the workflow scans the thread and falls back to a modal. Before relying on it, verify:
1. Workers have the `github-pr-review` skill mounted (their `skills_config` points to `agents/skills/github-pr-review`).
2. `agentdock pr-review-helper` is available on the worker host (built-in subcommand — keep app/worker binaries on the same version).
3. `secrets.GH_TOKEN` has enough permission to post review comments on the target PR.

## Log levels

Two independent knobs:

| Field | Sink | Default |
|---|---|---|
| `log_level` | console / stderr | `info` |
| `logging.level` | rotated file `logs/YYYY-MM-DD.jsonl` | `debug` |

Accepts `debug` / `info` / `warn` / `error`. Also overridable via `--log-level` or the `LOG_LEVEL` env var.

## Secrets

In Redis mode, app owns all secrets and ships them encrypted to workers:

1. Configure `secret_key` (AES-256 key) and `secrets` (key-value map) in app.yaml.
2. On startup, app writes a beacon to Redis so workers can verify key equality.
3. On every submit, `secrets` gets AES-256-GCM encrypted into `Job.EncryptedSecrets`.
4. Worker decrypts and injects the values as env vars on the agent subprocess.

`github.token` is auto-merged into `secrets["GH_TOKEN"]`. `AGENTDOCK_SECRET_<NAME>` env vars are also slurped into `secrets`.

## Mantis (optional)

When a thread contains Mantis issue URLs (`view.php?id=` or `/issues/`), the agent calls the
bundled `mantis` skill to fetch issue title, description, and attachments. Config is two fields:

```yaml
mantis:
  base_url: https://mantis.example.com    # host root; no /api/rest suffix needed
  api_token: <your-mantis-api-token>
```

Both fields must be set, or both left empty — setting only one fails validation at startup.

**How it works**: on app startup, `base_url + /api/rest` is stored in `secrets["MANTIS_API_URL"]`
and `api_token` goes into `secrets["MANTIS_API_TOKEN"]`. The worker forwards both as env vars
when spawning the agent subprocess. The bundled `mantis` skill reads those env vars; the agent
invokes the skill whenever it sees a Mantis URL in the thread context.

**Basic auth removed**: the bundled skill only supports API token auth. If your Mantis version
is too old for API tokens, upgrade Mantis or leave the `mantis` block empty (skill disabled).

**When unconfigured**: the agent still sees Mantis URLs in the thread, but does not fetch issue
content — URLs pass through to the GitHub issue body as-is.

**Worker host prerequisite**: the worker needs Node.js 18+ to execute the JS in the bundled
mantis skill. The official Docker image already includes it.
