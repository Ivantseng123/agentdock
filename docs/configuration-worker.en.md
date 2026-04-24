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
| `args` | []string | CLI arguments; `{prompt}` is substituted with the job prompt; `{extra_args}` is expanded from `extra_args` below |
| `extra_args` | []string | Extra flags spliced into `args` at the `{extra_args}` slot (see below) |
| `timeout` | duration | Per-job wall-clock limit (e.g. `30m`) |
| `skill_dir` | string | Repo-relative directory where skill files are written |
| `stream` | bool | Enable real-time JSON event parsing (claude only). **Caveat**: because bool has no "unset" sentinel, writing `stream: false` alone in a partial override block (e.g. alongside `extra_args`) does NOT turn streaming off on a built-in — the built-in value wins. To disable streaming on a built-in agent, write a full override that also sets `command` + `args`. |

You only need to specify the fields you want to override; unset fields inherit from `BuiltinAgents`. Example:

```yaml
agents:
  opencode:
    timeout: 30m    # extend timeout; command/args/skill_dir stay at built-in values
  claude:
    skill_dir: .claude/custom-skills
```

### `extra_args` (per-agent flag injection)

To add one or two flags to a built-in agent (e.g. pin opencode to a specific model), you don't have to copy its entire `args`. Every built-in's `args` has a `{extra_args}` placeholder; at runtime it's expanded to 0..N arguments from the agent's `extra_args`:

```yaml
agents:
  opencode:
    extra_args: ["-m", "opencode/claude-opus-4-7"]
  codex:
    extra_args: ["--sandbox", "read-only"]
```

Resulting invocations:

- `opencode run --pure -m opencode/claude-opus-4-7 "{prompt}"`
- `codex exec --skip-git-repo-check --color never --sandbox read-only "{prompt}"`
- `claude --print --output-format stream-json <extra_args...> -p "{prompt}"`

**Why this exists:** previously the only way to inject a flag was to override the entire `agents.<name>` block. When the binary upgrades and built-in `args` change (e.g. `--pure` was added in v2.2), your snapshot falls behind. `extra_args` lets you keep riding the built-in defaults while layering your own flags.

**Placeholder positions (fixed by the binary, operators don't configure this):**

- `claude`: `{extra_args}` sits after `stream-json` and before `-p` (claude requires all flags before `-p`)
- `codex`: after `--color never`, before `{prompt}`
- `opencode`: after `--pure`, before `{prompt}` (same slot used by `-m`, `--agent`, `--variant`, `-c`, `--session`, `-f`)

**Precedence:**

1. Set only `extra_args` (no `command` / `args`) → keep the built-in `args` and layer your flags. Recommended.
2. Set a full `args` override AND `extra_args`: **the override wins**, `extra_args` is dropped, and startup emits a warn log (`extra_args 被忽略`). To keep both, put a literal `"{extra_args}"` element in your override.
3. Empty `extra_args` (nil or `[]`) → the `{extra_args}` slot vanishes with no empty-string leftover in the child process argv.

**Caveat:** `extra_args` is operator-supplied — the worker never injects flags for you. If you put `--dangerously-skip-permissions` in there, that's your call and your risk (the worker may be running on your laptop, not an isolated pod).

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
