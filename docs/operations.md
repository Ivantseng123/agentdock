# 監控與管理

[English](operations.en.md)

## 查看 Job 狀態

```bash
curl localhost:8180/jobs | jq .
```

回傳：

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

### Workers 欄位

`workers` 陣列顯示目前已註冊的 worker。Worker 透過 Redis key（`ad:workers:{id}`，30s TTL）維持心跳，斷線 30 秒後自動消失。每個 worker 會顯示 `status`（`busy`/`idle`）及正在處理的 `current_job`。

若 `workers` 為空陣列，代表沒有活著的 worker — pending job 不會被處理。

## 手動終止 Job

```bash
curl -X DELETE localhost:8180/jobs/req-abc123
```

## Slack 取消

Submit 後的排隊訊息帶有「取消」按鈕，點擊即可終止。

## 自動保護

| 機制 | 預設值 | 說明 |
|------|--------|------|
| Job timeout | 20m | 整個 job 的最大生命週期 |
| Agent idle timeout | 5m | stream-json agent 無 event 超過此時間自動終止 |
| Prepare timeout | 3m | clone/setup 超時自動終止 |

超時後 bot 會通知 Slack 使用者並清除 dedup，讓使用者可以重新觸發。

## Worker 啟動

### 互動模式（本地開發）

直接執行，缺少的參數會互動式提問：

```bash
./bot worker
```

啟動時會依序驗證：
1. Redis 連線（PING）
2. GitHub Token（API 驗證身份 + repo 存取權限）
3. Agent CLI（`<cmd> --version`）

### 非互動模式（env 帶齊）

```bash
REDIS_ADDR=<host>:<port> GITHUB_TOKEN=<token> PROVIDERS=claude ./bot worker
```

### Preflight 驗證項目

| 檢查項 | 驗證方式 | 失敗行為 |
|--------|---------|---------|
| Redis 連線 | PING | 互動：重新輸入（最多 3 次）；非互動：退出 |
| GitHub Token | GET /user + GET /user/repos | 互動：重新輸入（最多 3 次）；非互動：退出 |
| Agent CLI | `<cmd> --version` | 警告，至少一個可用才啟動 |

## HTTP Endpoints

| Endpoint | Method | 說明 |
|----------|--------|------|
| `/healthz` | GET | Health check |
| `/jobs` | GET | 列出所有 job 狀態（含 agent 追蹤） |
| `/jobs/{id}` | DELETE | 終止指定 job |

## Agent 行為

每個 workflow 各自有 skill、各自的 JSON fence marker、app 端也各自收尾。

### Issue（`@bot issue` / legacy bare-repo）

1. 載入 `triage-issue` skill
2. 探索 clone 下來的 repo（agent 內建工具）
3. 評估 confidence（low → 拒絕建 issue）
4. 輸出 `===TRIAGE_RESULT===` 後接 JSON：

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

App 端：
- `confidence=low` → 不建 issue，通知使用者
- `files_found=0` 或 `open_questions>=5` → 建 issue 但不含 triage section
- 正常 → 建完整 issue + URL 回 thread

### Ask（`@bot ask <問題>`）

1. 載入 `ask-assistant` skill
2. 讀 `<issue_context>`（含 thread、`<bot>` handle、如果有附 repo 就多 repo 內容）
3. 按 skill 分類意圖（codebase 問 / 一般問 / 超出範疇）
4. 輸出 `===ASK_RESULT===` 後接 JSON：

```
===ASK_RESULT===
{"answer": "<markdown answer, Slack mrkdwn 格式, ≤30000 chars>"}
```

App 端：
- 直接把 `answer` 貼回 thread；不建 issue
- 超過 30000 chars 會截尾並附警語

### PR Review（`@bot review <PR URL>`，需 `pr_review.enabled: true`）

1. 載入 `github-pr-review` skill
2. 讀 PR diff（skill 會指 agent 用 `agentdock pr-review-helper` subcommand 去 fetch + post）
3. Agent 本人在 PR 上留 line-level comments + summary review
4. 輸出 `===REVIEW_RESULT===` 後接 JSON，三種 status：

```
===REVIEW_RESULT===
{"status": "POSTED", "summary": "...", "severity_summary": "major|minor|nit"}
{"status": "SKIPPED", "reason": "lockfile_only", "summary": "..."}
{"status": "ERROR", "summary": "..."}
```

App 端：不碰 PR（agent 自己發 review），只把 status/summary 報回 thread。

## Debug：Redis Queue 診斷

當 worker 沒有接到 job 時，用以下指令確認 app 和 worker 是否連到同一個 Redis。

### 確認 Redis stream 狀態

```bash
# 查 stream 中有多少訊息
redis-cli -h <REDIS_HOST> -p 6379 XLEN ad:jobs:triage

# 查看 stream 內容
redis-cli -h <REDIS_HOST> -p 6379 XRANGE ad:jobs:triage - + COUNT 5

# 查 consumer group 狀態（有哪些 consumer、pending 數量）
redis-cli -h <REDIS_HOST> -p 6379 XINFO GROUPS ad:jobs:triage
```

### 確認 Redis 中是否有 AgentDock 的 key

```bash
redis-cli -h <REDIS_HOST> -p 6379 SCAN 0 MATCH "ad:*" COUNT 100
```

所有 AgentDock 的 key 都以 `ad:` 為 prefix：

| Key | 類型 | 用途 |
|-----|------|------|
| `ad:jobs:triage` | Stream | Job 佇列 |
| `ad:jobs:results` | Stream | Worker 回傳結果 |
| `ad:jobs:status` | Pub/Sub | Worker 狀態回報 |
| `ad:jobs:commands` | Pub/Sub | 取消指令 |
| `ad:workers:{id}` | Key (30s TTL) | Worker 心跳註冊 |

### 常見問題

| 症狀 | 原因 | 解法 |
|------|------|------|
| `/jobs` 有 pending job 但 `XLEN` 為 0 | App 和 worker 連到不同 Redis | 確認兩邊的 `REDIS_ADDR` 指向同一個實例 |
| `XLEN` 有值但 worker 沒接到 | Consumer group 問題 | 用 `XINFO GROUPS` 檢查 consumer 狀態 |
| `workers` 陣列為空 | 沒有 worker 連線或連到不同 Redis | 啟動 worker 並確認 Redis 地址一致 |

### K8s 環境：本地 worker 連 K8s Redis

```bash
# port-forward K8s 裡的 Redis service
kubectl port-forward svc/<REDIS_SERVICE> -n <NAMESPACE> 16379:6379

# 啟動 local worker 連到 port-forward 的 Redis
REDIS_ADDR=localhost:16379 GITHUB_TOKEN=<TOKEN> PROVIDERS=claude ./bot worker
```
