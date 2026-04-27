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

# agents: block is optional. When omitted, the worker fills claude / codex /
# opencode from BuiltinAgents at startup — always using the current binary's
# defaults. Add entries here only to override specific fields.
# To pick up updated built-in defaults after a binary upgrade, delete (or omit)
# your agents: block and restart.
#
# agents:
#   opencode:
#     timeout: 30m    # example: extend timeout for one agent only
#     extra_args: ["-m", "opencode/claude-opus-4-7"]
#     # example: inject flags (e.g. model) via extra_args; the {extra_args}
#     # token inside the builtin args is expanded with these tokens, so you
#     # don't have to copy the full args array just to add one flag.

providers: [claude, codex, opencode]  # ordered fallback chain; single-agent mode: providers: [claude]

count: 3                              # worker goroutine count (flat! was worker.count)

nickname_pool: ["Alice", "Bob", "Gary"]  # optional: random display nicknames drawn once at startup

prompt:
  extra_rules:                        # worker-side rules appended to the app prompt (flat!)
    - "Do not guess, do not invent"

repo_cache:
  dir: /var/cache/agentdock/repos     # must be absolute
  max_age: 10m

queue:
  capacity: 50
  transport: redis
  job_timeout: 35m
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

## extra_args — injecting extra flags

`extra_args` lets you add flags to a built-in agent **without copying the entire `args` list**. Every built-in agent's `args` contains an `{extra_args}` token; at runtime the worker splices your `extra_args` slice in at that position.

### Examples

```yaml
agents:
  opencode:
    extra_args: ["-m", "opencode/claude-opus-4-7"]    # pin a model

  claude:
    extra_args: ["--dangerously-skip-permissions"]     # skip permission prompts (sandboxed env)

  codex:
    extra_args: ["--sandbox", "off"]                  # disable sandbox
```

### Injection positions

| Agent      | `{extra_args}` position                          | Reason                                          |
|------------|--------------------------------------------------|-------------------------------------------------|
| `claude`   | after `stream-json`, before `-p`                 | claude CLI requires all option flags before `-p` |
| `codex`    | after fixed flags, before `{prompt}`             | `codex exec` accepts options before the positional |
| `opencode` | after `--pure`, before `{prompt}`                | `-m`, `--variant` etc. all go before the positional |

### Precedence

If you write both a full `args` override **and** `extra_args`, but the `args` override does not contain an `{extra_args}` token, `extra_args` is silently ignored and a startup warn is logged:

```
agent has both args override and extra_args; extra_args ignored
```

Recommendation: **only write `extra_args` — do not copy the full args list.** That way you automatically get upstream args refreshes when built-ins change.

## Worker Nicknames (optional)

`nickname_pool` is a list of display names. At startup each worker process randomly picks one (Fisher–Yates, no replacement when `len(pool) >= count`).

- Pool **≥** count: every worker gets a distinct entry.
- Pool **<** count: pool is exhausted, remaining workers fall back to `worker-0`, `worker-1`, ...
- Empty or absent pool: all workers display `worker-N` (current behavior).
- Each entry is 1–32 runes; leading/trailing whitespace is trimmed at load; **duplicates are allowed** (operator's choice).
- `<`, `>`, `&` are auto-escaped at render time, so pasting `<@U123>` into the pool will NOT accidentally ping a Slack user.

Slack status messages use a playful template regardless of whether a nickname is set:

- Preparing: `:toolbox: Alice 正在暖機...`
- Running: `:fire: Alice 開工啦！(claude) · 奮鬥 1m23s`
- Stats: `Alice 已經敲了 15 次工具、翻了 8 份檔`

(Text is Chinese because this is a zh-first product; the template applies to every worker uniformly.)

## Agent overrides (optional)

Omitting the `agents:` block is recommended — `mergeBuiltinAgents` fills the built-in defaults at startup. Add entries only when you need to override a specific field:

| Field | Type | Description |
|---|---|---|
| `command` | string | Executable name or path |
| `args` | []string | CLI arguments; `{prompt}` is substituted with the job prompt |
| `timeout` | duration | Per-job wall-clock limit (e.g. `30m`) |
| `skill_dir` | string | Repo-relative directory where skill files are written |
| `stream` | bool | Enable real-time JSON event parsing (claude only) |

You only need to specify the fields you want to override; unset fields inherit from `BuiltinAgents`. Example:

```yaml
agents:
  opencode:
    timeout: 30m    # extend timeout; command/args/skill_dir stay at built-in values
  claude:
    skill_dir: .claude/custom-skills
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
