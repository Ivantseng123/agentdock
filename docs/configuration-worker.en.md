# Worker configuration (`worker.yaml`)

[繁體中文](configuration-worker.md)

YAML consumed by `agentdock worker`. Default path: `~/.config/agentdock/worker.yaml`. Run `agentdock init worker -i` to generate a commented template.

> **v2 change**: schema is now flat. `worker.count` → `count`; `worker.prompt.extra_rules` → `prompt.extra_rules`. The file already lives at worker scope, so the extra nest was redundant.

## Schema

```yaml
log_level: info                       # console / stderr level

logging:
  dir: logs
  level: debug
  retention_days: 30
  agent_output_dir: logs/agent-outputs

github:
  token: ghp-...                      # REQUIRED: used by agent clone / push

agents:
  claude:
    command: claude
    args: ["--print", "--output-format", "stream-json", "-p", "{prompt}"]
    timeout: 15m
    skill_dir: .claude/skills
    stream: true                      # enable real-time event tracking
  codex:
    command: codex
    args: ["exec", "--skip-git-repo-check", "--color", "never", "{prompt}"]
    timeout: 15m
    skill_dir: .agents/skills         # Codex discovers skills in .agents/skills, not .codex/skills
  opencode:
    command: opencode
    args: ["run", "{prompt}"]
    timeout: 15m
    skill_dir: .opencode/skills

active_agent: claude                  # single-agent mode
providers: [claude, codex, opencode]  # ordered fallback chain

count: 3                              # worker goroutine count (flat! was worker.count)

prompt:
  extra_rules:                        # worker-side rules appended to the app prompt (flat!)
    - "List every related file with its full path"
    - "Do not guess, do not invent"

repo_cache:
  dir: /var/cache/agentdock/repos     # must be absolute
  max_age: 10m

queue:
  capacity: 50
  transport: redis
  job_timeout: 20m
  agent_idle_timeout: 5m
  prepare_timeout: 3m
  cancel_timeout: 60s
  status_interval: 5s

redis:
  addr: redis:6379
  password: ""
  db: 0
  tls: false

secret_key: same-hex-as-app           # REQUIRED: copy from app.yaml

secrets:
  GH_TOKEN: ghp_worker_override       # optional: overrides the app-provided value
```

## Agent streaming

Claude supports `--output-format stream-json`. With `stream: true`, the worker tracks:

- Tool activity (Read, Bash, Grep, ...)
- Files read, tokens emitted
- cost_usd / input tokens / output tokens

Agents without streaming (opencode, codex) are tracked via PID + liveness only.

## Agent skills

Skills travel with the job (`Job.Skills`). The worker writes them into the cloned repo (SKILL.md + examples + references) before launching the agent CLI — no host-side install required. The per-agent `skill_dir` decides where the files land.

## Preflight

`agentdock worker` runs preflight on startup:

1. `github.token` validity (`GET /user`)
2. `redis.addr` reachability
3. `secret_key` matches the app's beacon
4. Every `providers` agent CLI runs (`<cmd> --version`)

Preflight failure blocks start. Pass `--log-level debug` for verbose diagnostics.

## Secrets

- `github.token` auto-merges into `secrets["GH_TOKEN"]`
- `AGENTDOCK_SECRET_<NAME>` env vars slot into `secrets["<NAME>"]`
- Decrypted `secrets` are injected as env vars on the agent subprocess
