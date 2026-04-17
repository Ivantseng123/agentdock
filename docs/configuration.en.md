# Configuration

[繁體中文](configuration.md)

Run `agentdock init -c /tmp/sample.yaml` to generate a template with all fields (add `-i` for interactive prompts). Full schema below:

```yaml
log_level: info                       # console / stderr log level: debug | info | warn | error (default: info)

auto_bind: true

channel_defaults:
  branch_select: true
  default_labels: ["from-slack"]

# Agent config
agents:
  claude:
    command: claude
    args: ["--print", "--output-format", "stream-json", "-p", "{prompt}"]
    timeout: 15m
    skill_dir: ".claude/skills"
    stream: true                      # enable real-time event tracking
  opencode:
    command: opencode
    args: ["run", "{prompt}"]
    timeout: 5m
    skill_dir: ".opencode/skills"

active_agent: claude
providers: [claude, opencode]

# Queue config
queue:
  capacity: 50                        # queue limit
  transport: inmem                    # inmem | redis
  job_timeout: 20m                    # watchdog: max job lifecycle
  agent_idle_timeout: 5m              # stream-json: no-event timeout
  prepare_timeout: 3m                 # clone/setup timeout
  status_interval: 5s                 # worker status report frequency

worker:
  count: 3                            # worker pool size
  prompt:
    extra_rules:                      # worker-side execution rules
      - "List all related file names with full paths"

# Redis config (used when transport: redis)
# redis:
#   addr: redis:6379
#   password: ""
#   db: 0

# Secret encryption (required for Redis mode)
secret_key: "64-char-hex-encoded-32-byte-AES-key"
secrets:
  GH_TOKEN: "ghp_xxx"
  K8S_TOKEN: "your-k8s-token"
  # key = env var name, value = plaintext

channel_priority:
  # C_INCIDENTS: 100                  # production incidents get priority
  default: 50

prompt:
  language: "English"
  goal: "Use the /triage-issue skill to investigate and produce a structured triage result."
  output_rules: []                    # app-level output rules (default empty; doesn't render section)
  allow_worker_rules: true            # whether to apply worker.prompt.extra_rules for this job
```

## Log Levels

Two independent log levels control console and file output:

| Field | Destination | Default |
|---|---|---|
| `log_level` | console / stderr (`./agentdock app` output) | `info` |
| `logging.level` | rotating files `logs/YYYY-MM-DD.jsonl` | `debug` |

Accepted values: `debug` / `info` / `warn` / `error`.

### Three ways to set

```yaml
# 1. YAML (persistent)
log_level: debug
```

```bash
# 2. CLI flag (one-shot)
./agentdock app -c ./config.yaml --log-level debug
```

```bash
# 3. Environment variable (when mapped via koanf)
LOG_LEVEL=debug ./agentdock app -c ./config.yaml
```

### When to enable debug

- Inspecting prompt assembly: worker dumps "Prompt XML 內容" (full XML); app dumps "Prompt context 詳細內容" (structured context)
- Tracing Slack attachment downloads
- Debugging skill loading

Debug logs are noisy. `info` is enough for normal operation. The file side defaults to `debug` (jsonl can be queried post-hoc with `jq -r`), while the console side defaults to `info` to keep signal clean.

## Secret Management

In Redis mode, the app centrally manages secrets and sends them encrypted to workers.

### How It Works

1. App config defines `secret_key` (AES-256 encryption key) and `secrets` (key-value pairs)
2. On startup, the app writes a beacon to Redis so workers can verify key consistency
3. When submitting a job, `secrets` are AES-256-GCM encrypted into `Job.EncryptedSecrets`
4. Workers decrypt and inject as environment variables for CLI agent processes (e.g., `GH_TOKEN`, `K8S_TOKEN`)

### Configuration

**App config (required):**
```yaml
secret_key: "0123456789abcdef..."   # 64 hex chars (32 bytes)
secrets:
  GH_TOKEN: "ghp_xxx"
  K8S_TOKEN: "eyJhb..."
```

**Worker config (`secret_key` required, `secrets` optional override):**
```yaml
secret_key: "same-key-as-app"
secrets:
  GH_TOKEN: "ghp_worker_override"   # optional, overrides app-provided value
```

**Environment variable injection:**
- `SECRET_KEY` → overrides `secret_key`
- `AGENTDOCK_SECRET_<NAME>` → injects into `secrets["<NAME>"]` (e.g., `AGENTDOCK_SECRET_K8S_TOKEN=xxx`)

### Interactive Startup

When running `agentdock app` or `agentdock worker` for the first time without `secret_key`:
- **App**: option to auto-generate a key (printed and saved to config)
- **Worker**: prompted to paste the app's key, with immediate beacon verification

### Priority Order

Secrets are applied in this order (later overrides earlier):

1. `github.token` (auto-merged as `secrets["GH_TOKEN"]`)
2. App config `secrets`
3. `AGENTDOCK_SECRET_*` environment variables
4. Worker config `secrets` (overrides app-provided values)

## Agent Stream Mode

Claude supports `--output-format stream-json`, enabling real-time tracking:
- Current tool in use (Read, Bash, Grep...)
- Files read count
- Text generated
- Cost (cost_usd, input/output tokens)

Agents without stream support (opencode, codex) only track PID + alive status.

## Agent Skills

Skills are sent with each Job (`Job.Skills` field). Workers write skill files (supporting full directory trees: SKILL.md + examples + references) into the cloned repo, and agent CLIs auto-discover them on startup. No manual installation needed.

```
agents/
  skills/
    triage-issue/
      SKILL.md           # triage skill — agent analyzes codebase, returns structured result
  setup.sh               # local dev: create symlinks (run.sh calls this automatically)
```

### Dynamic Skill Loading (Remote)

In addition to baked-in skills, you can dynamically load skills from npm registry via a separate `skills.yaml`:

```yaml
# skills.yaml (mounted via k8s ConfigMap)
skills:
  triage-issue:
    type: local
    path: agents/skills/triage-issue

  code-review:
    type: remote
    package: "@team/skill-code-review"
    version: "latest"

cache:
  ttl: 5m    # Remote skill cache TTL
```

In `config.yaml`, specify the path:
```yaml
skills_config: "/etc/agentdock/skills.yaml"
```

**Features:**
- **TTL cache + singleflight**: avoid duplicate fetches, re-fetch only when cache expires
- **Two-layer fallback**: remote fails → use cached old version → use baked-in → skip
- **Negative cache**: failed skills don't retry within TTL
- **Startup warmup**: prefetch all remote skills on app startup
- **Hot reload**: fsnotify watches skills.yaml, auto-reloads on ConfigMap update
- **Same-name conflict fail fast**: local and remote skills with same name → immediate error
- **File validation**: single skill < 1MB, job total < 5MB, extension whitelist, path traversal protection

**NPM package convention:**
```
node_modules/@team/skill-code-review/
  skills/
    code-review/
      SKILL.md           # required
      examples/           # optional
      references/         # optional
```

Private registries require separate `.npmrc` configuration (mount via k8s Secret to `/home/node/.npmrc`).
