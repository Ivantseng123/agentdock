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

workflows:
  # One prompt (goal + response_schema + output_rules) per workflow. Unset fields fall back to hardcoded defaults.
  issue:
    prompt:
      goal: "Use the /triage-issue skill to investigate and produce a triage result."
      response_schema: |
        Your final response MUST end with ONE of these three shapes after ===TRIAGE_RESULT===:
        CREATED  → {"status":"CREATED","title":"<required>","body":"...","labels":[...],"confidence":"high|medium","files_found":<int>,"open_questions":<int>}
        REJECTED → {"status":"REJECTED","message":"..."}
        ERROR    → {"status":"ERROR","message":"..."}
      output_rules: []                # formatting rules live in the triage-issue skill's SKILL.md body template, not here
  ask:
    prompt:
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
    enabled: true                     # PR Review workflow feature flag; on by default — set `false` to opt out
    prompt:
      goal: "Review the PR. Use the github-pr-review skill to analyze the diff and post line-level comments plus a summary review via agentdock pr-review-helper."
      response_schema: |
        Your final response MUST end with ONE of these three shapes after ===REVIEW_RESULT===:
        POSTED  → {"status":"POSTED","summary":"...","comments_posted":<int>,"comments_skipped":<int>,"severity_summary":"clean|minor|major"}
        SKIPPED → {"status":"SKIPPED","summary":"...","reason":"lockfile_only|vendored|generated|pure_docs|pure_config"}
        ERROR   → {"status":"ERROR","error":"<diagnostic>","summary":"<what you would have posted>"}
      output_rules:
        - "Focus on correctness, security, style"
        - "Summary ≤ 2000 chars"

prompt_defaults:
  language: English
  allow_worker_rules: true            # whether worker.prompt.extra_rules is rendered

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
  store: redis                        # JobStore backend: redis (default) / mem
  store_ttl: 1h                       # per-record TTL when store=redis (ignored when store=mem)
  job_timeout: 35m                    # watchdog: max job lifecycle
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

## JobStore backend (`queue.store`)

The app tracks each Job's lifecycle (Pending → Running → Completed/Failed/Cancelled) via `JobStore`. `queue.store` picks where that state lives:

| Value | Behaviour | Recommended for |
|---|---|---|
| `redis` (default) | Persisted to Redis (`jobstore:*` keys). In-flight jobs survive app restarts. | Production, most deployments |
| `mem` | In-process memory. All state is lost when the app restarts. | Unit tests, single-pod local dev (when Redis persistence isn't needed) |

`queue.store_ttl` (default `1h`) is the per-record TTL applied by `RedisJobStore` on every Put / UpdateStatus / SetWorker / SetAgentStatus. Terminal-state jobs are not deleted proactively — TTL evicts them. Set the TTL comfortably larger than your **longest expected job runtime**; otherwise a slow job risks having its state evicted mid-run. Ignored when `store=mem` (MemJobStore runs its own 1h cleanup).

On startup with `store=redis`, the app calls `ListAll()` once and logs `rehydrated in-flight jobs from previous instance` with the count of non-terminal records. No in-memory index is rebuilt — `ResultListener` resolves jobs via `store.Get` directly against Redis.

**Expected Redis load**: switching to `store=redis` adds ~2 `store.Get` calls per worker StatusReport (default `worker.status_interval: 5s`) plus 1 `UpdateStatus` (WATCH/MULTI/EXEC round-trip) on state transitions. Roughly `0.6 × N` QPS per active worker — size your Redis accordingly; negligible for typical deployments.

Background / incident: [#123](https://github.com/Ivantseng123/agentdock/issues/123) (in-flight Slack jobs orphaned on app restart), [#146](https://github.com/Ivantseng123/agentdock/issues/146) (wire-up PR).

## Workflow-specific prompts

`workflows.issue.prompt` / `workflows.ask.prompt` / `workflows.pr_review.prompt` each carry their own `goal` + `response_schema` + `output_rules`:
- `goal` is the **task description** — what to do, which skill to invoke (`triage-issue` / `ask-assistant` / `github-pr-review`). Keep output format OUT of the goal.
- `response_schema` is the **machine-readable output contract** — marker + JSON shape (`===ASK_RESULT===` / `===REVIEW_RESULT===`, etc). This section is **NOT** XML-escaped in the rendered prompt — literal `"` and `<` reach the LLM verbatim, so weaker models don't copy `&quot;` into their output and break downstream JSON parsing.
- `output_rules` are **formatting rules** (Slack mrkdwn, length caps, self-reference handle) rendered at the end of the prompt, XML-escaped.
- Any unset field is filled from `app/config/defaults.go` (`defaultIssueGoal` / `defaultAskGoal` / `defaultPRReviewGoal` / `defaultIssueResponseSchema` / `defaultAskResponseSchema` / `defaultPRReviewResponseSchema` / `defaultAskOutputRules` / `defaultPRReviewOutputRules`).

Cross-workflow knobs live in `prompt_defaults:`:
- `prompt_defaults.language` — language preference passed to the agent (e.g. `English`).
- `prompt_defaults.allow_worker_rules` — whether worker-side `prompt.extra_rules` is rendered (default `true`).

**Legacy aliases** (handled automatically in v2.3+; **migrate when convenient**):
- **Old-A (pre-v2.1)**: flat `prompt.goal` / `prompt.output_rules` / `prompt.response_schema` → `workflows.issue.prompt.*`.
- **Old-B (v2.2 interim)**: `prompt.{issue,ask,pr_review}.*` + top-level `pr_review.enabled` → `workflows.<name>.prompt.*` / `workflows.pr_review.enabled`. Also `prompt.language` / `prompt.allow_worker_rules` → `prompt_defaults.*`.
- If a file contains both the new shape and a legacy block, the new shape wins and a `config` warning is logged at startup suggesting migration.

## PR Review

`workflows.pr_review.enabled` **defaults to `true`** (the `github-pr-review` skill and `agentdock pr-review-helper` subcommand both ship in the release image, so opt-in was just ceremony). To turn it off, set `workflows.pr_review.enabled: false` explicitly (the legacy top-level `pr_review.enabled: false` still works via alias).

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
