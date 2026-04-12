# react2issue v2

[English](README.en.md)

Slack 對話 → AI codebase triage → GitHub Issue。Go 單一 binary，Socket Mode（不需公開 URL）。

在 Slack thread 中 `@bot` 或 `/triage`，bot 會讀取整段對話、透過 queue 分派給 CLI agent（claude/opencode/codex）探索 codebase，然後建立結構化的 GitHub issue。支援 in-memory 和 Redis 兩種 transport，worker 可在同一 process 或獨立 pod 執行。

## Quick Start

```bash
cp config.example.yaml config.yaml
# 填入 Slack / GitHub token
./run.sh
```

`run.sh` 會自動設定 agent skills → build → 啟動。

## 流程

```
@bot 或 /triage（thread 中）
  → dedup + rate limit → 讀取 thread 所有訊息
  → repo/branch 選擇（thread 內按鈕）→ 可選補充說明
  → 組 prompt → Submit 到 Priority Queue（立即回覆排隊狀態 + 取消按鈕）
  → Worker 從 Queue 取 job
    → clone repo → mount skills → spawn CLI agent
    → agent 探索 codebase + 判斷 confidence → 回傳 JSON 結果
  → App 收到結果 → 建 GitHub issue → post URL 到 Slack thread
```

## Queue 架構

Bot 使用 producer/consumer queue 解耦 Slack 事件處理和 agent 執行：

```
┌──────────┐     ┌──────────────┐     ┌─────────────┐     ┌──────────────┐
│ Slack    │────→│ Priority     │────→│ Worker Pool │────→│ Result       │
│ Handler  │     │ Queue        │     │ (N workers) │     │ Listener     │
│          │     │ (channel     │     │             │     │              │
│ dedup +  │     │  priority)   │     │ clone repo  │     │ create issue │
│ rate     │     │              │     │ mount skill │     │ post Slack   │
│ limit    │     │ capacity: 50 │     │ run agent   │     │ cleanup      │
└──────────┘     └──────────────┘     └─────────────┘     └──────────────┘
                       ↑ Submit              ↑ Kill              ↑ Status
                       │                     │                   │
                 ┌─────┴─────────────────────┴───────────────────┘
                 │              CommandBus / StatusBus
                 │        (kill signals, agent status reports)
                 └───────────────────────────────────────────────┘
```

- **Priority Queue**：channel-based 優先級 + FIFO，bounded capacity
- **Worker Pool**：N 個 goroutine 消費 job，每個 job 有獨立 context（可 cancel）
- **StatusBus**：worker 定期回報 agent 狀態（PID, tool calls, files read, cost）
- **CommandBus**：app → worker 的 kill 指令通道

### 部署模式

| 模式 | Transport | 說明 |
|------|-----------|------|
| In-Memory | `queue.transport: inmem` | 全部在同一個 process，Go channel 通訊（預設） |
| Redis Worker | `queue.transport: redis` | App 和 Worker 分開部署，Redis Stream/Pub/Sub 通訊 |
| External Worker | Redis + runner binary | 未來擴展：外部機器跑 `bot worker`，連同一個 Redis |

三種模式用同一套 interface（`JobQueue`, `ResultBus`, `StatusBus`, `CommandBus`, `AttachmentStore`），只換 transport 層。切換只改 config，不改代碼。

#### Redis 模式架構

```
┌─────────────┐                    ┌─────────────┐
│  App Pod    │                    │ Worker Pod  │
│             │    Redis Streams   │             │
│ Slack ──→   │──── JobQueue ────→│ consume job │
│ Workflow    │                    │ clone repo  │
│             │←── ResultBus ────│ run agent   │
│ create issue│←── StatusBus ────│ report      │
│ post Slack  │──── CommandBus ──→│ kill signal │
└─────────────┘                    └─────────────┘
```

App 不跑 agent。Worker 不需要 Slack token 或 GitHub write token。

## 觸發方式

| 方式 | 範例 | 說明 |
|------|------|------|
| `@bot` 提及 | 在 thread 中 `@bot` | 讀取 thread 所有前序訊息 |
| `/triage` | `/triage` | 互動選 repo |
| `/triage` + repo | `/triage owner/repo` | 跳過 repo 選擇 |
| `/triage` + repo + branch | `/triage owner/repo@main` | 直接開始分析 |

Bot 只在 **thread 中** 運作。在 channel 直接觸發會提示「請在對話串中使用」。

## 監控與管理

### 查看 Job 狀態

```bash
curl localhost:8180/jobs | jq .
```

回傳：

```json
{
  "queue_depth": 1,
  "total": 2,
  "jobs": [
    {
      "id": "req-abc123",
      "status": "running",
      "repo": "org/backend",
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

### 手動終止 Job

```bash
curl -X DELETE localhost:8180/jobs/req-abc123
```

### Slack 取消

Submit 後的排隊訊息帶有「取消」按鈕，點擊即可終止。

### 自動保護

| 機制 | 預設值 | 說明 |
|------|--------|------|
| Job timeout | 20m | 整個 job 的最大生命週期 |
| Agent idle timeout | 5m | stream-json agent 無 event 超過此時間自動終止 |
| Prepare timeout | 3m | clone/setup 超時自動終止 |

超時後 bot 會通知 Slack 使用者並清除 dedup，讓使用者可以重新觸發。

## 設定

完整選項見 `config.example.yaml`。

```yaml
auto_bind: true

channel_defaults:
  branch_select: true
  default_labels: ["from-slack"]

# Agent 設定
agents:
  claude:
    command: claude
    args: ["--print", "--output-format", "stream-json", "-p", "{prompt}"]
    timeout: 15m
    skill_dir: ".claude/skills"
    stream: true                      # 啟用即時事件追蹤
  opencode:
    command: opencode
    args: ["--prompt", "{prompt}"]
    timeout: 5m
    skill_dir: ".opencode/skills"

active_agent: claude
fallback: [claude, opencode]

# Queue 設定
queue:
  capacity: 50                        # queue 上限
  transport: inmem                    # inmem | redis
  job_timeout: 20m                    # watchdog: 最大 job 生命週期
  agent_idle_timeout: 5m              # stream-json: 無 event 超時
  prepare_timeout: 3m                 # clone/setup 超時
  status_interval: 5s                 # worker 回報狀態頻率

workers:
  count: 3                            # worker pool 大小

# Redis 設定（transport: redis 時使用）
# redis:
#   addr: redis:6379
#   password: ""
#   db: 0

channel_priority:
  # C_INCIDENTS: 100                  # production incidents 優先
  default: 50

prompt:
  language: "繁體中文"
  extra_rules:
    - "列出所有相關的檔案名稱與完整路徑"
```

### Agent Stream 模式

Claude 支援 `--output-format stream-json`，啟用後可即時追蹤：
- 目前在用什麼 tool（Read, Bash, Grep...）
- 已讀了幾個檔案
- 已生成多少文字
- 花了多少錢（cost_usd, input/output tokens）

不支援 stream 的 agent（opencode, codex）只追蹤 PID + 存活狀態。

### Agent Skills

Skills 隨 Job 發送給 worker（`Job.Skills` 欄位），worker 在 clone 的 repo 裡寫入 skill 檔案，agent CLI 啟動時自動載入。不需要手動安裝。

```
agents/
  skills/
    triage-issue/
      SKILL.md           # triage skill — agent 分析 codebase 回傳結構化結果
  setup.sh               # local 開發：建 symlink（run.sh 自動呼叫）
```

## Agent 行為

Agent 收到 prompt 後：
1. 載入 triage-issue skill
2. 探索 codebase（用自己的內建工具）
3. 評估 confidence（low → 拒絕）
4. 輸出結構化 JSON 結果（不直接建 issue）：

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

App 收到結果後：
- `confidence=low` → 不建 issue，通知使用者
- `files=0` 或 `questions>=5` → 建 issue 但不含 triage section
- 正常 → 建完整 issue + 回 Slack thread

## Slack App 設定

Bot Token Scopes：
- `chat:write`, `channels:read`, `channels:history`, `users:read`, `commands`
- 私人頻道：`groups:history`, `groups:read`

Event Subscriptions：
- `app_mention`
- auto-bind：`member_joined_channel`, `member_left_channel`

Interactivity：
- 啟用（用於 repo/branch 選擇按鈕 + 取消按鈕 + 補充說明 modal）

Slash Command：
- `/triage`

Socket Mode 啟用，App-Level Token scope `connections:write`。

## 部署

### Local（In-Memory 模式）

```bash
./run.sh
# 或
go build -o bot ./cmd/bot/ && ./bot -config config.yaml
```

### Local（Redis 模式）

```bash
# 啟動 Redis
redis-server --daemonize yes

# App（處理 Slack 事件、建 issue）
./bot -config config.yaml   # config 裡 queue.transport: redis

# Worker（消費 job、跑 agent）— 可以開多個
./bot worker -config worker.yaml
```

### Docker

```bash
docker build -t react2issue .
docker run -e SLACK_BOT_TOKEN=xoxb-... \
           -e SLACK_APP_TOKEN=xapp-... \
           -e GH_TOKEN=ghp_... \
           -e CLAUDE_AUTH_TOKEN=... \
           react2issue
```

### Kubernetes

使用 Kustomize：

```
deploy/
  base/                          # 通用 deployment（進 repo）
    kustomization.yaml
    deployment.yaml
  overlays/
    example/                     # 範本（進 repo）
      kustomization.yaml.example
      secret.yaml.example
    <your-env>/                  # 實際設定（gitignored）
      kustomization.yaml
      secret.yaml
```

```bash
cp deploy/overlays/example/*.example deploy/overlays/my-env/
vi deploy/overlays/my-env/kustomization.yaml
vi deploy/overlays/my-env/secret.yaml
kubectl apply -k deploy/overlays/my-env/
```

Claude CLI 認證：本地跑 `claude setup-token` 取得 token，存入 k8s Secret 的 `CLAUDE_AUTH_TOKEN`。

### CI/CD

Automated via [release-please](https://github.com/googleapis/release-please):

1. 寫 Conventional Commits（`feat:`, `fix:`, `chore:` 等）
2. release-please 自動維護 Release PR（version bump + CHANGELOG）
3. Merge Release PR → 自動建 GitHub Release + tag
4. GHA build Docker image → push 到 `ghcr.io`

## 架構

```
cmd/bot/
  main.go                    # entry point, transport switch, Socket Mode event loop
  local_adapter.go           # LocalAdapter: wraps worker.Pool for inmem mode
  worker.go                  # `bot worker` subcommand for standalone Redis worker
internal/
  config/config.go           # YAML config: agents, queue, redis, channels, prompt
  bot/
    workflow.go              # trigger → interact → build prompt → queue.Submit
    agent.go                 # AgentRunner: spawn CLI agent with RunOptions + stream
    parser.go                # parse ===TRIAGE_RESULT=== JSON (+ legacy fallback)
    prompt.go                # build user prompt for CLI agent
    result_listener.go       # ResultBus → create issue → post Slack
    status_listener.go       # StatusBus → update JobStore agent tracking
    enrich.go                # expand Mantis URLs in messages
  slack/
    client.go                # PostMessage/PostSelector/PostMessageWithButton/...
    handler.go               # TriggerEvent dedup, rate limiting
  github/
    issue.go                 # CreateIssue via GitHub API
    repo.go                  # RepoCache: clone, fetch, branch list, checkout
    discovery.go             # GitHub API repo discovery with cache
  queue/
    interface.go             # JobQueue, ResultBus, CommandBus, StatusBus, JobStore
    adapter.go               # Adapter interface + AdapterDeps
    coordinator.go           # Coordinator: JobQueue decorator, routes by TaskType
    bundle.go                # Common Bundle struct (transport-agnostic)
    job.go                   # Job, JobResult, JobState, AttachmentMeta
    inmem_*.go               # In-memory transport implementations
    redis_*.go               # Redis transport implementations (Stream, Pub/Sub, Hash)
    redis_bundle.go          # NewRedisBundle factory
    redis_client.go          # Redis client construction helper
    memstore.go              # MemJobStore (in-memory job state)
    priority.go              # container/heap priority queue
    registry.go              # ProcessRegistry (cancel-based kill)
    stream.go                # StreamEvent, ReadStreamJSON, ReadRawOutput
    watchdog.go              # Stuck job detection (timeout + idle + prepare)
    httpstatus.go            # GET /jobs, DELETE /jobs/{id}
  worker/
    pool.go                  # Worker pool with command listener + status reporting
    executor.go              # Single job execution (clone, skill, agent, parse)
    status.go                # statusAccumulator (stream event aggregation)
  mantis/                    # Mantis bug tracker URL enrichment
agents/
  skills/
    triage-issue/SKILL.md    # Agent skill: triage → structured JSON result
  setup.sh                   # Setup symlinks for local dev
deploy/
  base/                      # Kustomize base (deployment)
  overlays/example/          # Overlay template (secret.yaml.example)
```

## 測試

```bash
go test ./...   # 114 tests (Redis tests auto-skip if no Redis)
```

## HTTP Endpoints

| Endpoint | Method | 說明 |
|----------|--------|------|
| `/healthz` | GET | Health check |
| `/jobs` | GET | 列出所有 job 狀態（含 agent 追蹤） |
| `/jobs/{id}` | DELETE | 終止指定 job |

## License

MIT
