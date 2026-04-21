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
  base_url: https://mantis.example.com
  api_token: xxxxx
  username: ""
  password: ""

channel_priority:
  C_INCIDENTS: 100
  default: 50

prompt:
  language: English
  goal: "Use the /triage-issue skill to investigate and produce a triage result."
  output_rules: []                    # app-side rules (empty by default)
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
