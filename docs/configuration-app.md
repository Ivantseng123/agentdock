# App 設定 (`app.yaml`)

[English](configuration-app.en.md)

`agentdock app` 讀取的 YAML。預設位置：`~/.config/agentdock/app.yaml`。跑 `agentdock init app -i` 可自動產生含註解的範本。

## Schema

```yaml
log_level: info                       # console / stderr 層級：debug | info | warn | error

server:
  port: 8080                          # /healthz, /jobs, /metrics HTTP endpoint

slack:
  bot_token: xoxb-...                 # REQUIRED
  app_token: xapp-...                 # REQUIRED

github:
  token: ghp-...                      # REQUIRED：列出 repos、建 issues

channels:
  C0123456789:
    repos: [owner/repo-a, owner/repo-b]
    default_labels: [triage]
    branches: [main, release]
    branch_select: true

channel_defaults:
  branch_select: true
  default_labels: [from-slack]

auto_bind: true                       # 加入 channel 時自動綁
max_thread_messages: 50               # 讀多少訊息進 prompt
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
  language: 繁體中文
  goal: "使用 /triage-issue skill 調查 codebase 並產出結構化分類結果"
  output_rules: []                    # app 層規則（預設空）
  allow_worker_rules: true            # 是否讓 worker 的 extra_rules 生效

skills_config: /etc/agentdock/skills.yaml   # 動態 skill 載入設定（可選）

attachments:
  store: ""                           # 預留：未來可切換 backend
  temp_dir: /tmp/triage-attachments
  ttl: 30m

repo_cache:
  dir: /var/cache/agentdock/repos     # 必須是絕對路徑
  max_age: 10m

queue:
  capacity: 50
  transport: redis                    # 擴充點；目前僅支援 redis
  job_timeout: 20m                    # watchdog：job 生命週期上限
  agent_idle_timeout: 5m              # stream-json 無事件多久視為卡住
  prepare_timeout: 3m
  cancel_timeout: 60s
  status_interval: 5s

logging:
  dir: logs
  level: debug                        # 滾動檔案層級
  retention_days: 30
  agent_output_dir: logs/agent-outputs

redis:
  addr: redis:6379                    # queue.transport=redis 時必填
  password: ""
  db: 0
  tls: false

secret_key: 0123456789abcdef...       # 64 hex chars (32-byte AES-256 key)，redis 模式必填

secrets:
  GH_TOKEN: ghp_xxx                   # key = 環境變數名，value = 明文；會加密傳給 worker
  K8S_TOKEN: your-k8s-token
```

## Log 層級

兩個獨立的 log 層級：

| 欄位 | 去哪 | 預設 |
|---|---|---|
| `log_level` | console / stderr | `info` |
| `logging.level` | 滾動檔案 `logs/YYYY-MM-DD.jsonl` | `debug` |

支援值：`debug` / `info` / `warn` / `error`。可改用 CLI flag（`--log-level debug`）或 env var（`LOG_LEVEL`）。

## Secrets

Redis 模式下，app 集中管理 secrets 並加密傳給 worker：

1. App config 設定 `secret_key`（AES-256 金鑰）和 `secrets`（key-value）。
2. App 啟動時把 beacon 寫入 Redis，worker 靠 beacon 驗證金鑰一致性。
3. 每個 job 提交時，`secrets` 用 AES-256-GCM 加密後放進 `Job.EncryptedSecrets`。
4. Worker 解密後把值注入 agent 子進程的環境變數。

`github.token` 會自動 merge 進 `secrets["GH_TOKEN"]`。`AGENTDOCK_SECRET_<NAME>` 環境變數也會被收進 `secrets`。
